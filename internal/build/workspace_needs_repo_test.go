package build

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/pipeline"
	"github.com/huangchengsir/pipewright/internal/project"
	"github.com/huangchengsir/pipewright/internal/run"
)

// 未绑仓库的纯发布项目:镜像档构建确实吃源码(仓库里的 Dockerfile),该拦就拦;
// script 任务不需要源码,工作区从空目录开始照跑 —— 否则「只用来发布」的项目连个脚本都跑不了。
func TestWorkspaceNeedsRepo(t *testing.T) {
	bound := &project.Project{RepoURL: "https://g/a.git"}
	for _, needsSource := range []bool{true, false} {
		if err := workspaceNeedsRepo(bound, needsSource); err != nil {
			t.Fatalf("绑了仓库应放行(needsSource=%v), got %v", needsSource, err)
		}
	}
	for _, proj := range []*project.Project{nil, {RepoURL: ""}, {RepoURL: "   "}} {
		if err := workspaceNeedsRepo(proj, true); err == nil {
			t.Fatal("未绑仓库 + 吃源码的任务应报错")
		} else if !strings.Contains(err.Error(), "未绑定仓库") {
			t.Fatalf("报错应面向用户、可操作, got %q", err.Error())
		}
		if err := workspaceNeedsRepo(proj, false); err != nil {
			t.Fatalf("未绑仓库 + 不吃源码的 script 任务应放行, got %v", err)
		}
	}
}

// 未绑仓库时 job 工作区真的建出空目录(且绝不惊动克隆器),供不吃源码的 script 任务使用。
func TestCloneJobWorkspace_SourcelessProjectGetsEmptyWorkspace(t *testing.T) {
	cl := &countingCloner{}
	b := &Builder{cloner: cl}
	r := &run.Run{ID: "run-1", ProjectID: "p-1"}
	rep := &fakeReporter{}

	ws, commitTag, cleanup, err := b.cloneJobWorkspace(context.Background(), r, &project.Project{ID: "p-1", Name: "只发布"}, rep, false)
	defer cleanup()
	if err != nil {
		t.Fatalf("未绑仓库的 script 工作区应放行: %v", err)
	}
	if entries, rerr := os.ReadDir(ws); rerr != nil || len(entries) != 0 {
		t.Fatalf("工作区应为空目录, entries=%v err=%v", entries, rerr)
	}
	if commitTag != "latest" {
		t.Fatalf("无源码时 commitTag = %q, want latest", commitTag)
	}
	if cl.n != 0 {
		t.Fatalf("未绑仓库绝不该调克隆器, 次数=%d", cl.n)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "未绑定仓库") {
		t.Fatalf("日志应说明工作区为何为空, got %v", rep.logs)
	}

	// 镜像档构建要吃仓库里的 Dockerfile:同一份项目配置下必须被拦,且同样不调克隆器。
	if _, _, _, err := b.cloneJobWorkspace(context.Background(), r, &project.Project{ID: "p-1", Name: "只发布"}, rep, true); err == nil {
		t.Fatal("吃源码的任务应被拦")
	}
	if cl.n != 0 {
		t.Fatalf("被拦时也不该调克隆器, 次数=%d", cl.n)
	}
}

// 整条阶段路径:未绑仓库的项目里,script 任务照常在其构建环境容器里跑完并成功。
func TestStageExecutor_ScriptJobRunsInSourcelessProject(t *testing.T) {
	drv := &recordingDriver{}
	cl := &countingCloner{}
	b := &Builder{
		projects: fakeProjects{proj: &project.Project{ID: "p-1", Name: "只发布"}},
		settings: fakeSettings{settings: &pipeline.Settings{}},
		vault:    fakeVault{secrets: map[string]string{}},
		driver:   drv,
		cloner:   cl,
		envGate:  gateWithImages("img1"),
	}
	exec := NewStageExecutor(b, nil)
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		scriptJobID("only", "img1"),
	}}
	rep := &fakeReporter{}
	if err := exec(context.Background(), &run.Run{ID: "run-1", ProjectID: "p-1"}, stage, rep); err != nil {
		t.Fatalf("无源码的 script 任务应跑通, got %v(日志:%v)", err, rep.logs)
	}
	if drv.callCount != 1 {
		t.Fatalf("容器应被起一次, got %d", drv.callCount)
	}
	if cl.n != 0 {
		t.Fatalf("未绑仓库不该克隆, 次数=%d", cl.n)
	}
}

// 同一份未绑仓库的项目配置里放镜像档构建 → 明确失败并给出可操作的中文说明。
func TestStageExecutor_ImageBuildNeedsRepo(t *testing.T) {
	drv := &recordingDriver{}
	cl := &countingCloner{}
	b := &Builder{
		projects: fakeProjects{proj: &project.Project{ID: "p-1", Name: "只发布"}},
		settings: fakeSettings{settings: &pipeline.Settings{}},
		vault:    fakeVault{secrets: map[string]string{}},
		driver:   drv,
		cloner:   cl,
		envGate:  gateWithImages("img1"),
	}
	exec := NewStageExecutor(b, nil)
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		{ID: "img", Name: "打镜像", Type: "build_image", Config: map[string]any{
			"artifactType": pipeline.ArtifactImage, "dockerfilePath": "Dockerfile",
		}},
	}}
	rep := &fakeReporter{}
	if err := exec(context.Background(), &run.Run{ID: "run-1", ProjectID: "p-1"}, stage, rep); err == nil {
		t.Fatal("无源码时镜像档构建应失败")
	}
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "未绑定仓库") {
		t.Fatalf("失败要说明原因, got %v", rep.logs)
	}
	if drv.callCount != 0 {
		t.Fatalf("被拦时不该起容器, got %d", drv.callCount)
	}
}

// 源阶段那张卡片在未绑仓库时也不能糊人:不报凭空的分支/提交,直说「不拉源码」。
func TestGitSourceLogLines_Sourceless(t *testing.T) {
	srcJob := pipeline.Job{ID: "job_src", Name: "流水线源", Type: "git_source"}
	joined := strings.Join(gitSourceLogLines(srcJob, &run.Run{}, &project.Project{ID: "p-1"}), "\n")
	if !strings.Contains(joined, "未绑定仓库") {
		t.Fatalf("未绑仓库应直说, got %q", joined)
	}
	if strings.Contains(joined, "分支:") {
		t.Fatalf("未绑仓库不该报一个凭空的分支名, got %q", joined)
	}

	bound := &project.Project{ID: "p-1", RepoURL: "https://g/a.git", CredentialID: "c-1"}
	joined = strings.Join(gitSourceLogLines(srcJob, &run.Run{}, bound), "\n")
	if !strings.Contains(joined, "https://g/a.git") || !strings.Contains(joined, "凭据") {
		t.Fatalf("绑了仓库要报出仓库与凭据状态, got %q", joined)
	}

	// 项目读不到(proj 为 nil)≠ 没绑仓库:不能把读取失败说成「未绑定仓库」。
	joined = strings.Join(gitSourceLogLines(srcJob, &run.Run{}, nil), "\n")
	if strings.Contains(joined, "未绑定仓库") {
		t.Fatalf("项目读不到时不该断言未绑仓库, got %q", joined)
	}
}
