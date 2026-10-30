package build

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/library"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/run"
)

// fakeVarGroups 是 varGroupReader 的测试桩。
type fakeVarGroups struct {
	groups map[string]*library.VarGroup
}

func (f *fakeVarGroups) Get(_ context.Context, id string) (*library.VarGroup, error) {
	if g, ok := f.groups[id]; ok {
		return g, nil
	}
	return nil, library.ErrVarGroupNotFound
}

// fakeCommands 是 commandReader 的测试桩。
type fakeCommands struct {
	cmds map[string]*library.Command
}

func (f *fakeCommands) Get(_ context.Context, id string) (*library.Command, error) {
	if c, ok := f.cmds[id]; ok {
		return c, nil
	}
	return nil, library.ErrCommandNotFound
}

// 变量组并入变量表:项目自己的变量优先,组按引用顺序合并,同名先到先得,secret 引用保留。
func TestMergeReferencedVarGroupsPrecedence(t *testing.T) {
	b := &Builder{varGroups: &fakeVarGroups{groups: map[string]*library.VarGroup{
		"g1": {ID: "g1", Vars: []pipeline.BuildVar{
			{Key: "DEPLOY_ROOT", Value: "/srv/shared"},
			{Key: "GROUP_ONLY", Value: "from-g1"},
			{Key: "TOKEN", Secret: true, CredentialID: "cred-1"},
		}},
		"g2": {ID: "g2", Vars: []pipeline.BuildVar{
			{Key: "GROUP_ONLY", Value: "from-g2"},
			{Key: "APP_PORT", Value: "8080"},
		}},
	}}}
	settings := &pipeline.Settings{
		Build:       pipeline.BuildConfig{Vars: []pipeline.BuildVar{{Key: "DEPLOY_ROOT", Value: "/srv/project"}}},
		VarGroupIDs: []string{"g1", "g2"},
	}
	if err := b.mergeReferencedVarGroups(context.Background(), settings); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got := map[string]pipeline.BuildVar{}
	for _, v := range settings.Build.Vars {
		got[v.Key] = v
	}
	if got["DEPLOY_ROOT"].Value != "/srv/project" {
		t.Errorf("项目变量该优先,got=%q", got["DEPLOY_ROOT"].Value)
	}
	if got["GROUP_ONLY"].Value != "from-g1" {
		t.Errorf("同名变量先引用的组优先,got=%q", got["GROUP_ONLY"].Value)
	}
	if got["APP_PORT"].Value != "8080" {
		t.Errorf("后引用的组变量也该并入,got=%q", got["APP_PORT"].Value)
	}
	if !got["TOKEN"].Secret || got["TOKEN"].CredentialID != "cred-1" {
		t.Errorf("secret 引用要保留(脚本仍可用),got=%+v", got["TOKEN"])
	}
}

// 未装配变量组服务 / 没有引用时完全不动变量表。
func TestMergeReferencedVarGroupsNoop(t *testing.T) {
	settings := &pipeline.Settings{Build: pipeline.BuildConfig{Vars: []pipeline.BuildVar{{Key: "A", Value: "1"}}}}
	if err := (&Builder{}).mergeReferencedVarGroups(context.Background(), settings); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(settings.Build.Vars) != 1 {
		t.Fatalf("无引用时不该改变量表:%+v", settings.Build.Vars)
	}
}

// 引用悬挂(变量组已删)必须明确失败并点名,不静默漏变量。
func TestMergeReferencedVarGroupsDanglingFails(t *testing.T) {
	b := &Builder{varGroups: &fakeVarGroups{groups: map[string]*library.VarGroup{}}}
	err := b.mergeReferencedVarGroups(context.Background(),
		&pipeline.Settings{VarGroupIDs: []string{"gone"}})
	if err == nil {
		t.Fatal("want error for dangling group reference")
	}
	if !strings.Contains(err.Error(), "gone") {
		t.Errorf("错误要点名哪条引用失效,got=%v", err)
	}
}

// ssh 部署节点的配置只剩前/重启命令要用:构造带目标主机的 deploy_ssh job。
func deploySSHJob(cfg map[string]any) pipeline.Job {
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg[pipeline.ConfigKeyServerIDs] = "srv-1"
	return pipeline.Job{ID: "d1", Name: "发版", Type: "deploy_ssh", Config: cfg}
}

// 重启命令引用复用库指令:正文在运行时取出,{{占位符}} 照常渲染,节点里残留的文字不执行。
func TestRunDeployJobRestartCommandRef(t *testing.T) {
	dep := &stubStageDeployer{}
	b := &Builder{deployer: dep, commands: &fakeCommands{cmds: map[string]*library.Command{
		"cmd-1": {ID: "cmd-1", Name: "重启 api", Body: "systemctl restart api-{{ENV}}"},
	}}}
	settings := &pipeline.Settings{Build: pipeline.BuildConfig{Vars: []pipeline.BuildVar{
		{Key: "ENV", Value: "prod"},
	}}}
	job := deploySSHJob(map[string]any{
		pipeline.ConfigKeyRestartCommandRef: "cmd-1",
		"restartCommand":                    "echo 这段不该执行",
	})
	if err := b.runDeployJob(context.Background(), &fakeReporter{}, job,
		&run.Run{ID: "run-1"}, nil, settings); err != nil {
		t.Fatalf("runDeployJob err: %v", err)
	}
	if got := dep.gotCfg["restartCommand"]; got != "systemctl restart api-prod" {
		t.Errorf("该用指令正文并渲染变量,got=%q", got)
	}
}

// 引用指向已删指令:节点失败并点名,绝不静默当成空命令。
func TestRunDeployJobCommandRefDanglingFails(t *testing.T) {
	dep := &stubStageDeployer{}
	rep := &fakeReporter{}
	b := &Builder{deployer: dep, commands: &fakeCommands{cmds: map[string]*library.Command{}}}
	job := deploySSHJob(map[string]any{
		pipeline.ConfigKeyPreDeployCommandRef: "gone-cmd",
		"restartCommand":                      "echo ok",
	})
	err := b.runDeployJob(context.Background(), rep, job, &run.Run{ID: "run-1"}, nil, nil)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("want ErrBuildFailed, got %v", err)
	}
	if dep.gotCfg != nil {
		t.Errorf("失败时不该触发部署,cfg=%+v", dep.gotCfg)
	}
	logged := strings.Join(rep.logs, "\n")
	if !strings.Contains(logged, "gone-cmd") || !strings.Contains(logged, "复用库指令") {
		t.Errorf("日志要点名失效的引用,got=%q", logged)
	}
}

// 部署路径引用加密变量:明确报错,不把 {{SECRET}} 原样发到目标机。
func TestRunDeployJobSecretVarInDeployPathRejected(t *testing.T) {
	dep := &stubStageDeployer{}
	rep := &fakeReporter{}
	b := &Builder{deployer: dep}
	settings := &pipeline.Settings{Build: pipeline.BuildConfig{Vars: []pipeline.BuildVar{
		{Key: "DEPLOY_TOKEN", Secret: true, CredentialID: "cred-9"},
	}}}
	job := deploySSHJob(map[string]any{"deployPath": "/srv/{{DEPLOY_TOKEN}}"})
	err := b.runDeployJob(context.Background(), rep, job, &run.Run{ID: "run-1"}, nil, settings)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("want ErrBuildFailed, got %v", err)
	}
	if dep.gotCfg != nil {
		t.Errorf("失败时不该触发部署,cfg=%+v", dep.gotCfg)
	}
	logged := strings.Join(rep.logs, "\n")
	if !strings.Contains(logged, "{{DEPLOY_TOKEN}}") || !strings.Contains(logged, "加密变量") {
		t.Errorf("日志要点名是哪个加密变量,got=%q", logged)
	}
}

// 探测命令引用复用库指令:健康门禁那条路径同样认引用。
func TestRunDeployJobHealthCommandRef(t *testing.T) {
	dep := &stubStageDeployer{}
	b := &Builder{deployer: dep, commands: &fakeCommands{cmds: map[string]*library.Command{
		"cmd-h": {ID: "cmd-h", Name: "探活", Body: "curl -fsS localhost:{{PORT}}/healthz"},
	}}}
	settings := &pipeline.Settings{Build: pipeline.BuildConfig{Vars: []pipeline.BuildVar{
		{Key: "PORT", Value: "8080"},
	}}}
	job := deploySSHJob(map[string]any{
		pipeline.ConfigKeyHealthProbe:      pipeline.HealthProbeCommand,
		pipeline.ConfigKeyHealthCommandRef: "cmd-h",
	})
	if err := b.runDeployJob(context.Background(), &fakeReporter{}, job,
		&run.Run{ID: "run-1"}, nil, settings); err != nil {
		t.Fatalf("runDeployJob err: %v", err)
	}
	if got := dep.gotCfg["healthCommand"]; got != "curl -fsS localhost:8080/healthz" {
		t.Errorf("探测命令该用指令正文并渲染变量,got=%q", got)
	}
}
