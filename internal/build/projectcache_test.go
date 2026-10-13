package build

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/projectcache"
)

// projectcache_test.go 钉住「每项目固定依赖缓存」在本地执行路径上的落地形态:docker run 的
// array 里确实多了挂载工作区之外的缓存目录(-v)与改道 env(-e),且冲突时让路、未注入时零改动。
//
// 断言全部打在 fakeCommander 收到的**实参数组**上:命令不经宿主 shell 拼接,数组里有什么
// docker 就执行什么,所以这就是真机行为的等价证据。

func TestRunScriptStepMountsProjectCache(t *testing.T) {
	cmdr := newFakeCommander()
	cmdr.script("run", fakeCmd{exitCode: 0})

	proj := &project.Project{ID: "p42", Name: "app", RepoURL: "https://example.com/r.git"}
	b := newTestBuilder(t, cmdr, proj, imageSettings(nil), nil)
	root := filepath.Join(t.TempDir(), "pc")
	l, err := projectcache.New(root)
	if err != nil {
		t.Fatalf("projectcache.New: %v", err)
	}
	WithProjectCache(l)(b) // 走真实注入路径(与 main 接线同一入口)

	step := pipeline.PipelineStep{
		ID: "s1", Name: "打包", Type: pipeline.StepTypeScript,
		Image: "registry.example.com/java:17", Commands: []string{"mvn -B package"},
	}
	sink := newFakeSink()
	if err := b.runScriptStep(context.Background(), sink, 0, step, "/tmp/ws", "p42"); err != nil {
		t.Fatalf("runScriptStep: %v", err)
	}

	runs := cmdr.scriptRunArgs()
	if len(runs) != 1 {
		t.Fatalf("应有 1 次 docker run, got %d", len(runs))
	}
	joined := strings.Join(runs[0], " ")

	dir := l.ProjectDir("p42")
	for _, want := range []string{
		"-v " + dir + ":" + projectcache.ContainerBase,
		"-v " + filepath.Join(dir, "maven") + ":/root/.m2/repository",
		"-e MAVEN_ARGS=-Dmaven.repo.local=" + projectcache.ContainerBase + "/maven",
		"-e GOMODCACHE=" + projectcache.ContainerBase + "/go-mod",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("docker run 参数缺 %q, got:\n%s", want, joined)
		}
	}
	// 工作区挂载照旧(缓存是**追加**,不替换)。
	if !strings.Contains(joined, "-v /tmp/ws:"+scriptWorkspaceMount) {
		t.Fatalf("工作区挂载不该被影响: %s", joined)
	}
	// 日志要说清缓存落点(便于用户判断第二次是否真吃到)。
	if logs := sink.allLogText(); !strings.Contains(logs, "项目依赖缓存:"+dir) {
		t.Fatalf("应回显项目缓存目录, got:\n%s", logs)
	}
}

func TestRunScriptStepWithoutLayoutOrProjectIsUnchanged(t *testing.T) {
	t.Run("未注入布局", func(t *testing.T) {
		cmdr := newFakeCommander()
		cmdr.script("run", fakeCmd{exitCode: 0})
		proj := &project.Project{ID: "p1", Name: "app", RepoURL: "https://example.com/r.git"}
		b := newTestBuilder(t, cmdr, proj, imageSettings(nil), nil)

		step := pipeline.PipelineStep{ID: "s1", Type: pipeline.StepTypeScript, Image: "node:22", Commands: []string{"npm ci"}}
		if err := b.runScriptStep(context.Background(), newFakeSink(), 0, step, "/tmp/ws", "p1"); err != nil {
			t.Fatalf("runScriptStep: %v", err)
		}
		joined := strings.Join(cmdr.scriptRunArgs()[0], " ")
		if strings.Contains(joined, projectcache.ContainerBase) {
			t.Fatalf("未注入布局不该挂缓存: %s", joined)
		}
	})

	t.Run("无项目 ID", func(t *testing.T) {
		cmdr := newFakeCommander()
		cmdr.script("run", fakeCmd{exitCode: 0})
		proj := &project.Project{ID: "p1", Name: "app", RepoURL: "https://example.com/r.git"}
		b := newTestBuilder(t, cmdr, proj, imageSettings(nil), nil)
		l, err := projectcache.New(filepath.Join(t.TempDir(), "pc"))
		if err != nil {
			t.Fatalf("projectcache.New: %v", err)
		}
		b.projectCache = l

		step := pipeline.PipelineStep{ID: "s1", Type: pipeline.StepTypeScript, Image: "node:22", Commands: []string{"npm ci"}}
		if err := b.runScriptStep(context.Background(), newFakeSink(), 0, step, "/tmp/ws", ""); err != nil {
			t.Fatalf("runScriptStep: %v", err)
		}
		joined := strings.Join(cmdr.scriptRunArgs()[0], " ")
		if strings.Contains(joined, projectcache.ContainerBase) || strings.Contains(joined, "/root/.npm") {
			t.Fatalf("拿不到项目 ID 时要么全挂要么全不挂,不能半挂: %s", joined)
		}
	})
}

func TestRunScriptStepYieldsToConfigProfileMount(t *testing.T) {
	cmdr := newFakeCommander()
	cmdr.script("run", fakeCmd{exitCode: 0})

	proj := &project.Project{ID: "p7", Name: "app", RepoURL: "https://example.com/r.git"}
	b := newTestBuilder(t, cmdr, proj, imageSettings(nil), nil)
	l, err := projectcache.New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("projectcache.New: %v", err)
	}
	b.projectCache = l

	// 配置资源(预置目录)把整个 /root/.m2 挂进容器 → maven 缓存位让路,其余照常。
	step := pipeline.PipelineStep{
		ID: "s1", Type: pipeline.StepTypeScript, Image: "maven:3.9", Commands: []string{"mvn -v"},
		Resource: pipeline.Resource{Mounts: []pipeline.ContainerMount{
			{HostPath: "/host/settings.xml", ContainerPath: "/root/.m2", ReadOnly: true},
		}},
	}
	sink := newFakeSink()
	if err := b.runScriptStep(context.Background(), sink, 0, step, "/tmp/ws", "p7"); err != nil {
		t.Fatalf("runScriptStep: %v", err)
	}
	joined := strings.Join(cmdr.scriptRunArgs()[0], " ")
	if strings.Contains(joined, "/root/.m2/repository:") {
		t.Fatalf("冲突档必须让路(否则遮蔽挂进来的配置): %s", joined)
	}
	if !strings.Contains(joined, "-v /host/settings.xml:/root/.m2:ro") {
		t.Fatalf("配置资源挂载必须原样保留: %s", joined)
	}
	if !strings.Contains(joined, ":/root/.npm") {
		t.Fatalf("不冲突的档仍要挂上: %s", joined)
	}
	if logs := sink.allLogText(); !strings.Contains(logs, "缓存位 /root/.m2/repository 与已挂载路径冲突") {
		t.Fatalf("让路要有一行说明,否则用户以为缓存生效了: %s", logs)
	}
}
