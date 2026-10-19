package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/run"
)

func pasteCfg(text string) map[string]any {
	return map[string]any{pipeline.ConfigKeyEnvSource: pipeline.EnvSourcePaste, pipeline.ConfigKeyEnvText: text}
}

func lastVar(vars []pipeline.BuildVar, key string) (pipeline.BuildVar, int) {
	for i := len(vars) - 1; i >= 0; i-- {
		if vars[i].Key == key {
			return vars[i], i
		}
	}
	return pipeline.BuildVar{}, -1
}

func countKey(vars []pipeline.BuildVar, key string) int {
	n := 0
	for _, v := range vars {
		if v.Key == key {
			n++
		}
	}
	return n
}

// 导入的 env 必须真的盖住流水线级同名变量 —— 包括 secret:执行时明文与 secret 分两段传参,
// secret 恒在后,不摘掉更早的同名项就会出现「节点上导入的值被流水线级 secret 反压」的假覆盖。
func TestApplyJobEnvOverridesSameKeySecret(t *testing.T) {
	step := pipeline.PipelineStep{Env: []pipeline.BuildVar{
		{Key: "TOKEN", Secret: true, CredentialID: "c1"},
		{Key: "KEEP", Value: "keep"},
	}}
	jb := pipeline.Job{Name: "构建", Type: pipeline.StepTypeScript, Config: pasteCfg("TOKEN=from-file\n")}
	if err := applyJobEnv(&step, jb, "", nil); err != nil {
		t.Fatalf("applyJobEnv: %v", err)
	}
	if got := countKey(step.Env, "TOKEN"); got != 1 {
		t.Fatalf("TOKEN 出现 %d 次,同名只该留节点导入那一条: %+v", got, step.Env)
	}
	v, idx := lastVar(step.Env, "TOKEN")
	if v.Secret || v.Value != "from-file" {
		t.Errorf("TOKEN=%q secret=%v,要的是明文 from-file", v.Value, v.Secret)
	}
	if _, k := lastVar(step.Env, "KEEP"); k < 0 {
		t.Errorf("没导入的同名变量被弄丢了: %+v", step.Env)
	}
	if idx != len(step.Env)-1 {
		t.Errorf("导入的变量该排在节点层末尾(idx=%d,共 %d 条),否则传参时被更早的值盖住", idx, len(step.Env))
	}
	plain, secret := (&Builder{}).buildArgs(step.Env)
	joined := strings.Join(append(plain, secret...), " ")
	if !strings.Contains(joined, "TOKEN=from-file") {
		t.Errorf("传给容器的参数里没有导入值(env=%s),真实注入路径没走通", joined)
	}
}

// YAML 导入写进 cfg["env"] 的节点变量:执行侧此前从不读它,这里补齐。
func TestApplyJobEnvReadsLegacyCfg(t *testing.T) {
	step := pipeline.PipelineStep{}
	jb := pipeline.Job{Name: "n", Config: map[string]any{"env": `{"Z":"z","A":"a"}`}}
	if err := applyJobEnv(&step, jb, "", nil); err != nil {
		t.Fatalf("applyJobEnv: %v", err)
	}
	if len(step.Env) != 2 || step.Env[0].Key != "A" || step.Env[1].Key != "Z" {
		t.Fatalf("要按键名升序拿到两条, got %+v", step.Env)
	}
}

// 同一节点上 cfg["env"] 与导入文件都有同名键时,以导入的为准。
func TestApplyJobEnvImportedBeatsLegacy(t *testing.T) {
	step := pipeline.PipelineStep{}
	jb := pipeline.Job{Name: "n", Config: map[string]any{
		"env":                       `{"A":"old"}`,
		pipeline.ConfigKeyEnvSource: pipeline.EnvSourcePaste,
		pipeline.ConfigKeyEnvText:   "A=new\n",
	}}
	if err := applyJobEnv(&step, jb, "", nil); err != nil {
		t.Fatalf("applyJobEnv: %v", err)
	}
	if got := countKey(step.Env, "A"); got != 1 {
		t.Fatalf("A 出现 %d 次, got %+v", got, step.Env)
	}
	if v, _ := lastVar(step.Env, "A"); v.Value != "new" {
		t.Errorf("A=%q,导入的正文要盖住 cfg env 里的同名项", v.Value)
	}
}

func TestApplyJobEnvUnknownSource(t *testing.T) {
	step := pipeline.PipelineStep{}
	jb := pipeline.Job{Name: "n", Config: map[string]any{pipeline.ConfigKeyEnvSource: "ftp"}}
	if err := applyJobEnv(&step, jb, "", nil); err == nil {
		t.Fatal("非法来源该失败,不能静默当成「没导入」")
	}
}

// 日志侧只报键名:值(尤其是密码)不能出现在任何一行日志里。
func TestApplyJobEnvLogsKeysOnly(t *testing.T) {
	var lines []string
	step := pipeline.PipelineStep{}
	jb := pipeline.Job{Name: "n", Config: pasteCfg("API_URL=https://api.example\nPASS=s3cr3t-value\n这行不是键值对\n")}
	if err := applyJobEnv(&step, jb, "", func(_, line string) { lines = append(lines, line) }); err != nil {
		t.Fatalf("applyJobEnv: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "API_URL") || !strings.Contains(joined, "PASS") {
		t.Errorf("日志没列出导入的键名, got=%q", joined)
	}
	for _, leak := range []string{"s3cr3t-value", "https://api.example"} {
		if strings.Contains(joined, leak) {
			t.Errorf("日志泄露了值 %q;只许出现键名", leak)
		}
	}
	if !strings.Contains(joined, "已跳过") {
		t.Errorf("跳过的行要让人看见, got=%q", joined)
	}
}

func TestReadWorkspaceEnv(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "deploy", "prod.env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readWorkspaceEnv(ws, "deploy/prod.env"); err != nil || got != "A=1\n" {
		t.Fatalf("subdir repo path: got=%q err=%v", got, err)
	}
}

func TestReadWorkspaceEnvHonestErrors(t *testing.T) {
	ws := t.TempDir()
	ext := t.TempDir()
	// 仓库里可以提交一个指向工作区外的符号链接:解析后不在本次工作区内就必须拒绝。
	outside := filepath.Join(ext, "master.key")
	if err := os.WriteFile(outside, []byte("LEAK=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(ws, "sub")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(linkDir, "escape.env")); err != nil {
		t.Fatal(err)
	}
	// 超长文件(超过 EnvMaxBytes)该在构建前说清楚,而不是啃进内存再截。
	big := filepath.Join(ws, "big.env")
	if err := os.WriteFile(big, []byte(strings.Repeat("A=1\n", pipeline.EnvMaxBytes/4+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		workspace string
		path      string
		wantInErr string
	}{
		{"没工作区(未绑仓库的项目)", "", ".env", "没有工作区"},
		{"路径非法", ws, "../secret.env", "路径非法"},
		{"文件不在仓库里", ws, ".env", "找不到"},
		{"符号链接指向工作区外", ws, "sub/escape.env", "工作区外"},
		{"超过上限", ws, "big.env", "上限"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readWorkspaceEnv(tc.workspace, tc.path)
			if err == nil {
				t.Fatalf("readWorkspaceEnv(%q, %q) 应当失败", tc.workspace, tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("错误要说清「%s」, got=%v", tc.wantInErr, err)
			}
		})
	}
}

// 端到端(执行器 → 容器参数):仓库来源的 .env 要真的变成 docker run 的 -e。
func TestRepoEnvReachesContainerArgs(t *testing.T) {
	drv := &recordingDriver{code: 0}
	b := newDAGTestBuilder(drv, &markerCloner{file: ".env", content: "API_URL=https://api.example\nPASS=s3cr3t-value\n"})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	jb := pipeline.Job{ID: "j1", Name: "构建", Type: pipeline.StepTypeScript, Config: map[string]any{
		"image": "busybox", "commands": "true",
		pipeline.ConfigKeyEnvSource: pipeline.EnvSourceRepo,
		pipeline.ConfigKeyEnvFile:   ".env",
	}}
	stage := scriptStage(jb)
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep); err != nil {
		t.Fatalf("exec: %v (logs=%v)", err, rep.logs)
	}
	joined := strings.Join(drv.gotEnv, " ")
	for _, want := range []string{"API_URL=https://api.example", "PASS=s3cr3t-value"} {
		if !strings.Contains(joined, want) {
			t.Errorf("容器参数里没有 %q;env=%v", want, drv.gotEnv)
		}
	}
	for _, l := range rep.logs {
		if strings.Contains(l, "s3cr3t-value") || strings.Contains(l, "https://api.example") {
			t.Errorf("日志行泄露了 env 值:%s", l)
		}
	}
}

// 仓库里根本没有这份文件 → 该失败并说人读的原因,不能当成「这次没变量」假绿过去。
func TestRepoEnvMissingFileFailsRun(t *testing.T) {
	drv := &recordingDriver{code: 0}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	jb := pipeline.Job{ID: "j1", Name: "构建", Type: pipeline.StepTypeScript, Config: map[string]any{
		"image": "busybox", "commands": "true",
		pipeline.ConfigKeyEnvSource: pipeline.EnvSourceRepo,
		pipeline.ConfigKeyEnvFile:   ".env.production",
	}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(jb), rep); err == nil {
		t.Fatal("缺文件却跑成功了 = 假绿")
	}
	if drv.callCount != 0 {
		t.Errorf("环境变量读不出来就不该起容器,却跑了 %d 次", drv.callCount)
	}
	joined := strings.Join(rep.logs, "\n")
	if !strings.Contains(joined, ".env.production") {
		t.Errorf("失败日志要点名那份文件;logs=%v", rep.logs)
	}
}
