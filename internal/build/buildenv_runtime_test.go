package build

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/pipeline"
	"github.com/huangchengsir/pipewright/internal/target"
)

// buildenv_runtime_test.go 锁住执行期的两条铁律:
//   - 镜像只来自预置目录(#9):job 配置里残留的镜像串不参与决定跑什么。
//   - 配置资源以只读 bind 注入容器(#10):docker run 里出现 -v 宿主文件:容器路径:ro。

// ─── #9:镜像来源 ────────────────────────────────────────────────────────────────

func TestScriptStepImageComesFromCatalog(t *testing.T) {
	b := &Builder{envGate: testBuildEnvGate}
	// 节点同时留着旧的 image 串与新的 buildEnvId:以目录里的条目为准。
	jb := pipeline.Job{ID: "j", Name: "构建", Type: pipeline.StepTypeScript, Config: map[string]any{
		"image":      "attacker.example.com/evil:latest",
		"buildEnvId": "e-node22",
		"commands":   "npm ci",
	}}
	step, err := b.scriptStepFromJob(jb, "构建")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if step.Image != "node:22" {
		t.Errorf("image = %q, want 目录里的 node:22", step.Image)
	}
	if got := step.ImageCredentialID; got != "" {
		t.Errorf("公开镜像不应带凭据, got %q", got)
	}
}

// 私有镜像:环境绑定的凭据随解析结果带到执行侧(用于 docker login 拉取)。
func TestScriptStepCarriesImageCredential(t *testing.T) {
	b := &Builder{envGate: testBuildEnvGate}
	step, err := b.scriptStepFromJob(pipeline.Job{Name: "构建", Type: pipeline.StepTypeScript,
		Config: map[string]any{"buildEnvId": "e-priv", "commands": "npm ci"}}, "构建")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if step.Image != "harbor.local/node:20" || step.ImageCredentialID != "cred-1" {
		t.Errorf("step = %+v", step)
	}
	if h := imageRegistryHost(step.Image); h != "harbor.local" {
		t.Errorf("registry host = %q", h)
	}
	if h := imageRegistryHost("node:20"); h != "" {
		t.Errorf("Docker Hub 短名不应有 registry host, got %q", h)
	}
	if h := imageRegistryHost("registry.example.com:5000/team/tool:1"); h != "registry.example.com:5000" {
		t.Errorf("带端口仓库 = %q", h)
	}
}

// 目录缺席(未装配 / 未 seed / 读失败)时一律拒绝执行,绝不回退到跑任意镜像。
func TestScriptStepRequiresCatalog(t *testing.T) {
	cases := map[string]pipeline.BuildEnvGate{
		"未装配目录": nil,
		"目录为空":  emptyGate{},
		"目录读失败": brokenGate{},
	}
	for name, gate := range cases {
		b := &Builder{envGate: gate}
		jb := pipeline.Job{Name: "构建", Type: pipeline.StepTypeScript, Config: map[string]any{"image": "node:20", "commands": "npm ci"}}
		if _, err := b.scriptStepFromJob(jb, "构建"); err == nil {
			t.Errorf("%s: 应拒绝执行", name)
		} else if !strings.Contains(err.Error(), "构建环境") && !strings.Contains(err.Error(), "预置构建环境目录") {
			t.Errorf("%s: 报错应指向构建环境, got %v", name, err)
		}
	}
}

// 自定义节点的 {{参数}} 镜像:渲染后仍按目录白名单二次校验。
func TestScriptStepTemplatedImageRevalidated(t *testing.T) {
	b := &Builder{envGate: testBuildEnvGate}
	ok := pipeline.Job{Name: "自定义", Type: "templated", Config: map[string]any{
		"image": "node:{{ver}}", "ver": "20", "commandTemplate": "node -v"}}
	step, err := b.scriptStepFromJob(ok, "自定义")
	if err != nil {
		t.Fatalf("渲染命中目录应通过: %v", err)
	}
	if step.Image != "node:20" {
		t.Errorf("image = %q", step.Image)
	}
	bad := pipeline.Job{Name: "自定义", Type: "templated", Config: map[string]any{
		"image": "node:{{ver}}", "ver": "1-malicious", "commandTemplate": "node -v"}}
	if _, err := b.scriptStepFromJob(bad, "自定义"); err == nil {
		t.Error("渲染出目录外镜像应拒绝")
	}
}

// ─── #10:配置资源注入 ───────────────────────────────────────────────────────────

func TestScriptStepResolvesProfileMounts(t *testing.T) {
	// 宿主文件必须真实存在(构建器会 stat,防 docker 把缺失来源挂成空目录)。
	host := t.TempDir() + "/.npmrc"
	if err := os.WriteFile(host, []byte("registry=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gate := gateWithProfiles(
		[]pipeline.BuildEnvOption{{ID: "e-node", Language: "node", Version: "20", DisplayName: "Node 20", Image: "node:20", Enabled: true}},
		[]pipeline.ConfigProfileOption{
			{ID: "p-npmrc", Language: "node", Enabled: true, FilePath: host, TargetPath: "/root/.npmrc"},
			{ID: "p-off", Language: "node", Enabled: false, FilePath: host, TargetPath: "/root/x"},
		})
	b := &Builder{envGate: gate}
	jb := pipeline.Job{Name: "装依赖", Type: pipeline.StepTypeScript, Config: map[string]any{
		"buildEnvId": "e-node", "configProfileIds": "p-npmrc", "commands": "npm ci"}}
	step, err := b.scriptStepFromJob(jb, "构建")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(step.Resource.Mounts) != 1 {
		t.Fatalf("mounts = %+v", step.Resource.Mounts)
	}
	m := step.Resource.Mounts[0]
	if m.HostPath != host || m.ContainerPath != "/root/.npmrc" || !m.ReadOnly {
		t.Errorf("mount = %+v", m)
	}
	// 勾了已禁用的资源:诚实失败,不静默少注入。
	jb.Config["configProfileIds"] = "p-npmrc,p-off"
	if _, err := b.scriptStepFromJob(jb, "构建"); err == nil {
		t.Error("引用已禁用的配置资源应失败")
	}
	// 资源记录在,但磁盘文件没了 → 拒绝(而非挂一个空目录骗过构建)。
	if _, err := os.Stat(host); err == nil {
		_ = os.Remove(host)
		jb.Config["configProfileIds"] = "p-npmrc"
		if _, err := b.scriptStepFromJob(jb, "构建"); err == nil {
			t.Error("宿主文件缺失应拒绝执行")
		}
	}
}

// docker run 实参里出现只读 bind,且回显日志与实际参数一致(挂载路径非 secret)。
func TestShellDriverEmitsProfileMounts(t *testing.T) {
	cmdr := newFakeCommander()
	cmdr.script("run", fakeCmd{exitCode: 0})
	d := &shellDriver{bin: "docker", cmdr: cmdr}
	var logged []string
	_, err := d.RunToolchain(context.Background(), "node:20", "/ws", "/workspace", nil, []string{"sh", "-c", "npm ci"},
		pipeline.Resource{Mounts: []pipeline.ContainerMount{{HostPath: "/data/p-1/.npmrc", ContainerPath: "/root/.npmrc", ReadOnly: true}}},
		func(_, line string) { logged = append(logged, line) })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	rec := cmdr.execs[0]
	want := []string{"run", "--rm", "-v", "/ws:/workspace", "-w", "/workspace", "-v", "/data/p-1/.npmrc:/root/.npmrc:ro", "node:20", "sh", "-c", "npm ci"}
	if strings.Join(rec.args, " ") != strings.Join(want, " ") {
		t.Errorf("docker 实参 = %v\nwant %v", rec.args, want)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "/data/p-1/.npmrc:/root/.npmrc:ro") {
		t.Errorf("命令回显应含挂载: %v", logged)
	}
}

// 远程 runner 的容器看不见中控机文件:挂载被剥掉(执行侧另有一行诚实说明),不传给 docker 报错。
func TestRemoteDriverStripsMounts(t *testing.T) {
	ex := &fakeRemoteExecer{result: &target.ExecResult{ExitCode: 0}}
	d := NewRemoteDriver(ex, "srv-1", "docker")
	_, err := d.RunToolchain(context.Background(), "node:20", "/tmp/ws", "/workspace", nil, []string{"true"},
		pipeline.Resource{Mounts: []pipeline.ContainerMount{{HostPath: "/data/p-1/.npmrc", ContainerPath: "/root/.npmrc", ReadOnly: true}}}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(ex.gotCmds) != 1 {
		t.Fatalf("应只投递一条命令, got %v", ex.gotCmds)
	}
	for _, seg := range ex.gotCmds[0] {
		if strings.Contains(seg, ".npmrc") {
			t.Fatalf("远程不应带宿主挂载: %v", ex.gotCmds[0])
		}
	}
}
