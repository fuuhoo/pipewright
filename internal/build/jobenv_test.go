package build

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if err := (&Builder{}).applyJobEnv(context.Background(), &step, jb, "", nil); err != nil {
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
	if err := (&Builder{}).applyJobEnv(context.Background(), &step, jb, "", nil); err != nil {
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
	if err := (&Builder{}).applyJobEnv(context.Background(), &step, jb, "", nil); err != nil {
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
	if err := (&Builder{}).applyJobEnv(context.Background(), &step, jb, "", nil); err == nil {
		t.Fatal("非法来源该失败,不能静默当成「没导入」")
	}
}

// 日志侧只报键名:值(尤其是密码)不能出现在任何一行日志里。
func TestApplyJobEnvLogsKeysOnly(t *testing.T) {
	var lines []string
	step := pipeline.PipelineStep{}
	jb := pipeline.Job{Name: "n", Config: pasteCfg("API_URL=https://api.example\nPASS=s3cr3t-value\n这行不是键值对\n")}
	if err := (&Builder{}).applyJobEnv(context.Background(), &step, jb, "", func(_, line string) { lines = append(lines, line) }); err != nil {
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

// ── Nacos 来源 ─────────────────────────────────────────────────────────────

// fakeNacos 起一个按 handler 行事的假配置中心;httptest 绑 127.0.0.1,正好走「回环放行」这条判定。
func fakeNacos(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func nacosCfg(base string, extra map[string]any) map[string]any {
	cfg := map[string]any{
		pipeline.ConfigKeyEnvSource:        pipeline.EnvSourceNacos,
		pipeline.ConfigKeyNacosBaseURL:     base,
		pipeline.ConfigKeyNacosDataID:      "prod.env",
		pipeline.ConfigKeyNacosGroup:       "G1",
		pipeline.ConfigKeyNacosNamespaceID: "dev",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func nacosJob(base string, extra map[string]any) pipeline.Job {
	return pipeline.Job{ID: "j1", Name: "构建", Type: pipeline.StepTypeScript, Config: nacosCfg(base, extra)}
}

// 读到就注入:值进容器参数,日志只有键名;同名项按「导入的盖住更早那层」处理。
func TestApplyJobEnvNacosInjectsValues(t *testing.T) {
	srv := fakeNacos(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "API_URL=https://api.example\nPASS=s3cr3t-value\n")
	})
	step := pipeline.PipelineStep{Env: []pipeline.BuildVar{{Key: "API_URL", Value: "from-run"}, {Key: "KEEP", Value: "keep"}}}
	var lines []string
	jb := nacosJob(srv, nil)
	if err := (&Builder{}).applyJobEnv(context.Background(), &step, jb, "", func(_, l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("applyJobEnv: %v", err)
	}
	if got := countKey(step.Env, "API_URL"); got != 1 {
		t.Fatalf("API_URL 出现 %d 次,同名该只留 Nacos 那条: %+v", got, step.Env)
	}
	if v, _ := lastVar(step.Env, "API_URL"); v.Value != "https://api.example" {
		t.Errorf("API_URL=%q,要 Nacos 读回来的那份", v.Value)
	}
	if _, k := lastVar(step.Env, "KEEP"); k < 0 {
		t.Errorf("没导入的变量被弄丢了: %+v", step.Env)
	}
	plain, _ := (&Builder{}).buildArgs(step.Env)
	if !strings.Contains(strings.Join(plain, " "), "PASS=s3cr3t-value") {
		t.Errorf("传给容器的参数里没有 Nacos 的值:%v", plain)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "API_URL") || !strings.Contains(joined, "prod.env") {
		t.Errorf("日志要列出键名并点名读的是哪份配置:%q", joined)
	}
	for _, leak := range []string{"s3cr3t-value", "https://api.example"} {
		if strings.Contains(joined, leak) {
			t.Errorf("日志泄露了值 %q", leak)
		}
	}
	// 值只能进容器参数,不能顺手写回节点配置(那等于把配置中心的秘密落进流水线库)。
	if _, ok := jb.Config[pipeline.ConfigKeyEnvText]; ok {
		t.Error("Nacos 读回来的正文被写进了节点配置")
	}
}

// 选了凭据:先登录再读,而且密码与 token 都不能出现在日志或变量表里。
func TestApplyJobEnvNacosUsesCredentialWithoutLeak(t *testing.T) {
	const vaultPW, token = "pw-from-vault", "tok-3f9a1c7e5b"
	var loginUser, loginPass, bearer string
	var paths []string
	srv := fakeNacos(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/auth/login":
			_ = r.ParseForm()
			loginUser, loginPass = r.FormValue("username"), r.FormValue("password")
			fmt.Fprintf(w, `{"accessToken":%q,"tokenTtl":18000}`, token)
		case "/v1/cs/configs":
			bearer = r.URL.Query().Get("accessToken")
			fmt.Fprint(w, "DSN=user:pwd@tcp(db)/app\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	b := &Builder{vault: fakeVault{
		secrets: map[string]string{"nc-1": vaultPW},
		users:   map[string]string{"nc-1": "nacos-user"},
	}}
	step := pipeline.PipelineStep{}
	var lines []string
	jb := nacosJob(srv, map[string]any{pipeline.ConfigKeyNacosCredentialID: "nc-1"})
	if err := b.applyJobEnv(context.Background(), &step, jb, "", func(_, l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("applyJobEnv: %v", err)
	}
	if loginUser != "nacos-user" || loginPass != vaultPW {
		t.Errorf("登录表单 = %q/%q,用户名与密码都该来自凭据仓库", loginUser, loginPass)
	}
	if bearer != token {
		t.Errorf("读取没带上 accessToken(got %q);paths=%v", bearer, paths)
	}
	if v, _ := lastVar(step.Env, "DSN"); v.Value == "" {
		t.Errorf("值没进变量表: %+v", step.Env)
	}
	joined := strings.Join(lines, "\n") + "\n" + fmt.Sprint(step.Env)
	for _, secret := range []string{vaultPW, token} {
		if strings.Contains(joined, secret) {
			t.Errorf("凭据/token %q 泄露进了日志或变量表", secret)
		}
	}
}

// 读不到就是失败,而且要点名读的是哪一份:静默少几个变量会让人一路查到底层才发现。
func TestApplyJobEnvNacosHardFails(t *testing.T) {
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		extra     map[string]any
		wantInErr string
	}{
		{"配置不存在要点名坐标", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, nil, "命名空间 dev"},
		{"服务端开了鉴权但没选凭据", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, nil, "凭据"},
		{"YAML 配置读回来一条键值都没有", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "server:\n  port: 8080\n") }, nil, "properties"},
		{"空配置", func(w http.ResponseWriter, _ *http.Request) {}, nil, "用不了"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeNacos(t, tc.handler)
			step := pipeline.PipelineStep{Env: []pipeline.BuildVar{{Key: "KEEP", Value: "keep"}}}
			err := (&Builder{}).applyJobEnv(context.Background(), &step, nacosJob(srv, tc.extra), "", nil)
			if err == nil {
				t.Fatal("读不出来却跑成功了 = 假绿")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("错误要点名「%s」:%v", tc.wantInErr, err)
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("错误里带出了值:%v", err)
			}
			if len(step.Env) != 1 || step.Env[0].Key != "KEEP" {
				t.Errorf("失败时不该改动变量表: %+v", step.Env)
			}
		})
	}
}

// 凭据这一侧的三种「拿不到」:各自说清原因,而不是笼统一句「读取失败」。
func TestApplyJobEnvNacosCredentialReasons(t *testing.T) {
	srv := fakeNacos(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "A=1\n") })
	cfgWithCred := nacosCfg(srv, map[string]any{pipeline.ConfigKeyNacosCredentialID: "nc-1"})
	jb := pipeline.Job{ID: "j1", Name: "构建", Type: pipeline.StepTypeScript, Config: cfgWithCred}

	cases := []struct {
		name      string
		builder   *Builder
		wantInErr string
	}{
		{"保险库没装配(启动没给 master key)", &Builder{}, "master key"},
		{"凭据被删了", &Builder{vault: fakeVault{secrets: map[string]string{}}}, "不存在"},
		{"凭据是空的", &Builder{vault: fakeVault{secrets: map[string]string{"nc-1": ""}, users: map[string]string{"nc-1": ""}}}, "没开鉴权"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := pipeline.PipelineStep{}
			err := tc.builder.applyJobEnv(context.Background(), &step, jb, "", nil)
			if err == nil {
				t.Fatal("该失败")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("错误要说「%s」:%v", tc.wantInErr, err)
			}
		})
	}
}

// 端到端(执行器 → 容器参数):Nacos 那份配置要真的变成 docker run 的 -e。
func TestNacosEnvReachesContainerArgs(t *testing.T) {
	srv := fakeNacos(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "API_URL=https://api.example\nPASS=s3cr3t-value\n")
	})
	drv := &recordingDriver{code: 0}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	jb := nacosJob(srv, map[string]any{"image": "busybox", "commands": "true"})
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(jb), rep); err != nil {
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

// 配置中心只在中控机侧可达:地址被出网收口拒了(云元数据)就不该发出请求。
func TestApplyJobEnvNacosRefusesMetadataAddress(t *testing.T) {
	step := pipeline.PipelineStep{}
	err := (&Builder{}).applyJobEnv(context.Background(), &step,
		nacosJob("http://169.254.169.254/nacos", nil), "", nil)
	if err == nil {
		t.Fatal("云元数据地址该被拒")
	}
	if !strings.Contains(err.Error(), "出网收口") {
		t.Errorf("要说清是被出网收口拒的,否则人会去查网络:%v", err)
	}
}
