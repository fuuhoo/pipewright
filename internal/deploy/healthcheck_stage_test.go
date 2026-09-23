package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/target"
)

// TestHealthCheckFromConfig 覆盖部署节点 cfg → 健康门控配置的组装(键名与前端表单一致)。
func TestHealthCheckFromConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
		want *HealthCheck
	}{
		{"未配置", nil, nil},
		{"显式不探测", map[string]string{CfgKeyHealthProbe: "none"}, nil},
		{"探测方式非法", map[string]string{CfgKeyHealthProbe: "tcp"}, nil},
		{"http 缺 url", map[string]string{CfgKeyHealthProbe: "http"}, nil},
		{"http 完整", map[string]string{CfgKeyHealthProbe: "http", CfgKeyHealthURL: "http://localhost:8080/healthz", CfgKeyHealthRetries: "5"},
			&HealthCheck{Type: HealthCheckHTTP, URL: "http://localhost:8080/healthz", Retries: 5, IntervalSeconds: defaultHealthIntervalSeconds, TimeoutSeconds: defaultHealthTimeoutSeconds}},
		{"command 缺命令", map[string]string{CfgKeyHealthProbe: "command"}, nil},
		{"command 完整", map[string]string{CfgKeyHealthProbe: "command", CfgKeyHealthCommand: "test -f /opt/app/OK", CfgKeyHealthInterval: "1", CfgKeyHealthTimeout: "2"},
			&HealthCheck{Type: HealthCheckCommand, Command: []string{"sh", "-c", "test -f /opt/app/OK"}, Retries: defaultHealthRetries, IntervalSeconds: 1, TimeoutSeconds: 2}},
		{"非法数字回落默认", map[string]string{CfgKeyHealthProbe: "http", CfgKeyHealthURL: "u", CfgKeyHealthRetries: "abc"},
			&HealthCheck{Type: HealthCheckHTTP, URL: "u", Retries: defaultHealthRetries, IntervalSeconds: defaultHealthIntervalSeconds, TimeoutSeconds: defaultHealthTimeoutSeconds}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HealthCheckFromConfig(tc.cfg)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("want nil(不探测), got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want 探测配置, got nil")
			}
			// HealthCheck 带 []string 字段不可直接比较,逐字段断言。
			if got.Type != tc.want.Type || got.URL != tc.want.URL || got.Retries != tc.want.Retries ||
				got.IntervalSeconds != tc.want.IntervalSeconds || got.TimeoutSeconds != tc.want.TimeoutSeconds ||
				strings.Join(got.Command, "\x00") != strings.Join(tc.want.Command, "\x00") {
				t.Fatalf("got %+v (command %v), want %+v (command %v)", *got, got.Command, *tc.want, tc.want.Command)
			}
		})
	}
}

// TestStageDeployHealthGate 证流水线部署节点的探测真的生效:产物部署成功但探测不通 → 该机 failed。
func TestStageDeployHealthGate(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{execFn: func(_ string, cmd []string) (*target.ExecResult, error) {
		if strings.Contains(strings.Join(cmd, " "), "probe-fail") {
			return &target.ExecResult{ExitCode: 1, Stderr: "connection refused"}, nil
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop")

	res, err := New(tgt, rsvc).DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{CfgKeyHealthProbe: "command", CfgKeyHealthCommand: "probe-fail", CfgKeyHealthRetries: "1", CfgKeyHealthInterval: "0"}, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if res[0].Status != run.TargetFailed {
		t.Fatalf("探测不通应判该机 failed, got %+v", res[0])
	}
	if !strings.Contains(res[0].Message, "健康检查失败") {
		t.Fatalf("message 应说明健康检查失败, got %q", res[0].Message)
	}
}

// TestStageDeployHealthGatePasses 探测通过 → 该机 success。
func TestStageDeployHealthGatePasses(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{execFn: func(_ string, _ []string) (*target.ExecResult, error) {
		return &target.ExecResult{ExitCode: 0}, nil
	}}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop")

	res, err := New(tgt, rsvc).DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{CfgKeyHealthProbe: "http", CfgKeyHealthURL: "http://localhost:8080/healthz", CfgKeyHealthRetries: "1"}, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("want success, got %+v", res[0])
	}
	// http 探测构造的是 array 化 curl(-f:非 2xx 即非零退出),不是拼好的 shell。
	var probed bool
	for _, c := range tgt.calls {
		if len(c) >= 2 && c[0] == "curl" && c[1] == "-fsS" {
			probed = true
		}
	}
	if !probed {
		t.Errorf("应跑 curl 探测命令, got %v", tgt.calls)
	}
}

// TestStageCommandOnlyHealthGate 命令型部署(frp 隧道那类)同样受健康门控:
// 命令成功但探测不通 → failed,不留「命令跑通就算部署好」的假绿。
func TestStageCommandOnlyHealthGate(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{execFn: func(_ string, cmd []string) (*target.ExecResult, error) {
		if strings.Contains(strings.Join(cmd, " "), "probe-fail") {
			return &target.ExecResult{ExitCode: 1}, nil
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}}
	srv := seedServer(t, tgt, "frpc-client")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/unused")

	res, err := svcCmdOnly(tgt, rsvc).DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{"artifactType": "command", "restartCommand": "nginx -s reload",
			CfgKeyHealthProbe: "command", CfgKeyHealthCommand: "probe-fail", CfgKeyHealthRetries: "1", CfgKeyHealthInterval: "0"}, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if res[0].Status != run.TargetFailed {
		t.Fatalf("命令成功但探测不通应 failed, got %+v", res[0])
	}
}
