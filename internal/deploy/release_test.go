package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/target"
)

// fakeExec 模拟目标机:记录每条命令,按程序名给可控结果(curl 可模拟健康失败)。
type fakeExec struct {
	failHealth bool   // curl 探测返回非零(模拟健康失败)
	failOn     string // 该程序名执行报错(模拟 SSH 层失败)
	calls      [][]string
}

func (f *fakeExec) exec(cmd []string) (*target.ExecResult, error) {
	f.calls = append(f.calls, cmd)
	if len(cmd) == 0 {
		return &target.ExecResult{ExitCode: 1}, nil
	}
	if cmd[0] == f.failOn {
		return nil, target.ErrUnreachable
	}
	switch cmd[0] {
	case "curl":
		if f.failHealth {
			return &target.ExecResult{ExitCode: 22, Stderr: "curl: (22) 503 Service Unavailable"}, nil
		}
		return &target.ExecResult{ExitCode: 0}, nil
	default:
		return &target.ExecResult{ExitCode: 0}, nil
	}
}

func newFakeTarget(f *fakeExec) *stubTarget {
	return &stubTarget{execFn: func(_ string, cmd []string) (*target.ExecResult, error) {
		return f.exec(cmd)
	}}
}

func httpHealth() *HealthCheck {
	return &HealthCheck{Type: HealthCheckHTTP, URL: "http://127.0.0.1:8080/healthz", Retries: 1, IntervalSeconds: 0, TimeoutSeconds: 1}
}

// findCmd 在调用序列中找首条以 prog 开头的命令(断言用)。
func findCmd(calls [][]string, prog string) []string {
	for _, c := range calls {
		if len(c) > 0 && c[0] == prog {
			return c
		}
	}
	return nil
}

// assertNoReleaseLayout 断言直铺:命令里既不出现 releases/ 段,也没有软链切换程序。
func assertNoReleaseLayout(t *testing.T, calls [][]string) {
	t.Helper()
	for _, c := range calls {
		if len(c) > 0 {
			switch c[0] {
			case "ln", "mv", "readlink":
				t.Fatalf("直铺模式不应有软链切换命令: %v", c)
			}
		}
		for _, tok := range c {
			if strings.Contains(tok, "/releases/") || strings.HasSuffix(tok, "/current") {
				t.Fatalf("直铺模式不应套 releases/<uuid> 或 current: %v", c)
			}
		}
	}
}

// TestFileDeployLandsDirectlyInDeployPath:dist 直铺 → mkdir 就是部署路径本身,产物落在其下。
func TestFileDeployLandsDirectlyInDeployPath(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	fx := &fakeExec{}
	tgt := newFakeTarget(fx)
	srv := seedServer(t, tgt, "web-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{"deployPath": "/home/fhb/cicdtest/web"},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("want success, got %+v", res[0])
	}
	assertNoReleaseLayout(t, tgt.calls)

	mkdir := findCmd(tgt.calls, "mkdir")
	if mkdir == nil || len(mkdir) != 3 || mkdir[1] != "-p" || mkdir[2] != "/home/fhb/cicdtest/web" {
		t.Fatalf("mkdir 应直建部署路径: %v", mkdir)
	}
	// 产物落地文件名 = reference 的 base,父目录就是部署路径(无中间层)。
	var wroteTo string
	for _, c := range tgt.calls {
		if len(c) >= 5 && c[0] == "sh" && c[1] == "-c" && strings.Contains(c[2], "base64 -d") {
			wroteTo = c[3]
		}
	}
	if wroteTo != "/home/fhb/cicdtest/web/shop.tar.gz" {
		t.Fatalf("产物未直铺到部署路径: %q", wroteTo)
	}
	if !strings.Contains(res[0].Message, "/home/fhb/cicdtest/web") {
		t.Fatalf("message 应回显部署路径: %q", res[0].Message)
	}
}

// TestDeployTargetDirKeyPrecedence:deployPath > path(历史) > releaseBase(历史) > 兜底根目录。
func TestDeployTargetDirKeyPrecedence(t *testing.T) {
	a := run.Artifact{Type: run.ArtifactDist, Name: "shop", Reference: "dist/shop.tar.gz"}
	cases := []struct {
		cfg  map[string]string
		want string
	}{
		{map[string]string{"deployPath": "/srv/a", "path": "/srv/b", "releaseBase": "/srv/c"}, "/srv/a"},
		{map[string]string{"path": "/srv/b", "releaseBase": "/srv/c"}, "/srv/b"},
		{map[string]string{"releaseBase": "/srv/c"}, "/srv/c"},
		{map[string]string{"deployPath": "  "}, defaultDeployRoot + "/shop"},
	}
	for i, c := range cases {
		if got := deployTargetDir(a, c.cfg); got != c.want {
			t.Fatalf("case %d = %q, want %q", i, got, c.want)
		}
	}
}

// TestFileDeployRestartRunsInDeployPath:restartCommand 在部署路径下执行(目录作位置参数,不拼进脚本)。
func TestFileDeployRestartRunsInDeployPath(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	fx := &fakeExec{}
	tgt := newFakeTarget(fx)
	srv := seedServer(t, tgt, "app-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactJar, "build/shop.jar")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{"deployPath": "/home/fhb/cicdtest/server", "restartCommand": "./server-ny &"},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("want success, got %+v", res[0])
	}
	assertNoReleaseLayout(t, tgt.calls)

	// jar 的 java 探测指向部署路径下的产物本身。
	if java := findCmd(tgt.calls, "java"); java == nil || java[1] != "-jar" ||
		java[2] != "/home/fhb/cicdtest/server/shop.jar" {
		t.Fatalf("java 探测命令应指向部署路径下的产物: %v", java)
	}
	// 重启:sh -c 'cd "$0" && set -e …' <部署路径> —— 目录作位置参数,不内联进脚本体(AC-SEC-02)。
	var restart []string
	for _, c := range tgt.calls {
		if len(c) >= 4 && c[0] == "sh" && c[1] == "-c" && strings.Contains(c[2], "cd \"$0\"") {
			restart = c
		}
	}
	if restart == nil {
		t.Fatalf("未发现 restartCommand 执行: %v", tgt.calls)
	}
	if restart[3] != "/home/fhb/cicdtest/server" || !strings.Contains(restart[2], "./server-ny &") {
		t.Fatalf("重启命令异常: %v", restart)
	}
}

// TestFileDeployHealthFailIsFailedNotRolledBack:健康失败 → failed + 说明无上一版本可回滚。
func TestFileDeployHealthFailIsFailedNotRolledBack(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	fx := &fakeExec{failHealth: true}
	tgt := newFakeTarget(fx)
	srv := seedServer(t, tgt, "web-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config:      map[string]string{"deployPath": "/srv/shop"},
		HealthCheck: httpHealth(),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res[0].Status != run.TargetFailed {
		t.Fatalf("直铺健康失败应 failed(无 rolled_back), got %+v", res[0])
	}
	if !strings.Contains(res[0].Message, "无上一版本可回滚") {
		t.Fatalf("message 应说明无版本可回滚: %q", res[0].Message)
	}
	// 健康探测在产物铺好之后跑。
	mkdirIdx, curlIdx := -1, -1
	for i, c := range tgt.calls {
		if len(c) > 0 && c[0] == "mkdir" && mkdirIdx < 0 {
			mkdirIdx = i
		}
		if len(c) > 0 && c[0] == "curl" {
			curlIdx = i
		}
	}
	if mkdirIdx < 0 || curlIdx < mkdirIdx {
		t.Fatalf("健康探测应在产物放置之后: mkdirIdx=%d curlIdx=%d", mkdirIdx, curlIdx)
	}
	targets, _ := rsvc.ListDeployTargets(context.Background(), runID)
	if len(targets) != 1 || targets[0].Status != run.TargetFailed {
		t.Fatalf("targets 持久化异常: %+v", targets)
	}
	rn, _ := rsvc.Get(context.Background(), runID)
	if rn.Status != run.StatusFailed {
		t.Fatalf("run 终态 = %q, want failed", rn.Status)
	}
}

// TestFileDeployExecFailStillHumanRead:放置命令执行失败 → failed + 人读,不上抛、不泄漏密钥。
func TestFileDeployExecFailStillHumanRead(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	fx := &fakeExec{failOn: "mkdir"}
	tgt := newFakeTarget(fx)
	srv := seedServer(t, tgt, "web-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{"deployPath": "/srv/shop"},
	})
	if err != nil {
		t.Fatalf("Deploy 不应上抛执行错误: %v", err)
	}
	if res[0].Status != run.TargetFailed || res[0].Message == "" {
		t.Fatalf("want failed + 人读, got %+v", res[0])
	}
	for _, bad := range []string{"PRIVATE KEY", "BEGIN OPENSSH", "password"} {
		if strings.Contains(res[0].Message, bad) {
			t.Fatalf("message 泄漏敏感串 %q: %q", bad, res[0].Message)
		}
	}
}

// TestUploadTimeoutScalesWithSize 锁住「大文件不该被 60s 命令超时掐断」这条:额度随体积给,
// 并有上限;体积未知(旧数据)直接给上限,宁可慢判失败也不误判超时。
func TestUploadTimeoutScalesWithSize(t *testing.T) {
	mb := int64(1 << 20)
	cases := []struct {
		size int64
		want time.Duration
	}{
		{0, 15 * time.Minute},        // 无体积信息 → 上限
		{1 * mb, 68 * time.Second},   // 保底 60s + (1+1) 个 MB 档 × 4s
		{70 * mb, 344 * time.Second}, // 本次真机撞 60s 超时的那个体量
		{4 << 30, 15 * time.Minute},  // 远超上限 → 封顶
	}
	for _, c := range cases {
		if got := uploadTimeout(c.size); got != c.want {
			t.Fatalf("size=%dMB uploadTimeout=%v, want %v", c.size/mb, got, c.want)
		}
	}
}
