package target

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/vault"
)

// batchStubDialer 实现 batchDialer:记录每次 RunBatch 的整批命令,用于证明
// ExecBatch 在支持复用的拨号器上只拨一次。
type batchStubDialer struct {
	batches [][][]string
	outs    []*ExecResult
}

func (d *batchStubDialer) RunBatch(_ context.Context, _ string, _ SSHConfig, cmds [][]string) ([]*ExecResult, error) {
	d.batches = append(d.batches, cmds)
	return d.outs, nil
}

func (d *batchStubDialer) Run(_ context.Context, _ string, _ SSHConfig, cmd []string) (*ExecResult, error) {
	d.batches = append(d.batches, [][]string{cmd}) // 记为一批一条,便于断言「不该走单条」
	return &ExecResult{ExitCode: 0}, nil
}

func (d *batchStubDialer) RunStream(context.Context, string, SSHConfig, []string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (d *batchStubDialer) RunInteractive(context.Context, string, SSHConfig, []string) (Session, error) {
	return nil, nil
}

func (d *batchStubDialer) RunWithStdin(context.Context, string, SSHConfig, []string, io.Reader) (*ExecResult, error) {
	return &ExecResult{}, nil
}

// flakyRunDialer 不支持复用,且在第 failAt 条命令上返回连接错误(测部分结果回传)。
type flakyRunDialer struct {
	captured [][]string
	failAt   int
}

func (d *flakyRunDialer) Run(_ context.Context, _ string, _ SSHConfig, cmd []string) (*ExecResult, error) {
	at := len(d.captured)
	d.captured = append(d.captured, cmd)
	if d.failAt >= 0 && at == d.failAt {
		return nil, ErrUnreachable
	}
	return &ExecResult{Stdout: "out-" + cmd[0], ExitCode: 0}, nil
}

func (d *flakyRunDialer) RunStream(context.Context, string, SSHConfig, []string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (d *flakyRunDialer) RunInteractive(context.Context, string, SSHConfig, []string) (Session, error) {
	return nil, nil
}

func (d *flakyRunDialer) RunWithStdin(context.Context, string, SSHConfig, []string, io.Reader) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func newTestService(t *testing.T, dialer SSHDialer) (Service, string) {
	t.Helper()
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	credID := newSSHCred(t, v, leakMarker)
	svc := New(db, v, dialer)
	srv, err := svc.Create(context.Background(), CreateInput{
		Name: "metrics-host", Host: "10.0.0.9", Port: 22, User: "deploy", CredentialID: credID,
	})
	if err != nil {
		t.Fatalf("Create server: %v", err)
	}
	return svc, srv.ID
}

func TestExecBatchUsesSingleBatchCall(t *testing.T) {
	d := &batchStubDialer{outs: []*ExecResult{{Stdout: "a"}, {Stdout: "b"}}}
	svc, id := newTestService(t, d)

	res, err := svc.ExecBatch(context.Background(), id, [][]string{{"uname"}, {"nproc"}})
	if err != nil {
		t.Fatalf("ExecBatch: %v", err)
	}
	if len(res) != 2 || res[0].Stdout != "a" || res[1].Stdout != "b" {
		t.Fatalf("结果与入参不同序/不全: %+v", res)
	}
	if len(d.batches) != 1 || len(d.batches[0]) != 2 {
		t.Fatalf("应当只调一次 RunBatch(一条连接), got %v", d.batches)
	}
	if d.batches[0][0][0] != "uname" || d.batches[0][1][0] != "nproc" {
		t.Fatalf("命令 array 原样透传失败: %v", d.batches[0])
	}
}

func TestExecBatchFallsBackToPerCommandRun(t *testing.T) {
	d := &flakyRunDialer{failAt: -1}
	svc, id := newTestService(t, d)

	res, err := svc.ExecBatch(context.Background(), id, [][]string{{"df", "-k"}, {"free", "-b"}})
	if err != nil {
		t.Fatalf("ExecBatch: %v", err)
	}
	if len(res) != 2 || res[0].Stdout != "out-df" || res[1].Stdout != "out-free" {
		t.Fatalf("回退路径结果不符: %+v", res)
	}
	if len(d.captured) != 2 {
		t.Fatalf("回退路径应逐条 Run, got %d 次", len(d.captured))
	}
}

// 连接层面失败要带回「已经跑完的部分结果」:指标采集据此让前几条命令的产出不至于白费。
func TestExecBatchReturnsPartialResultsOnConnError(t *testing.T) {
	d := &flakyRunDialer{failAt: 1}
	svc, id := newTestService(t, d)

	res, err := svc.ExecBatch(context.Background(), id, [][]string{{"uname"}, {"nproc"}, {"df"}})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if len(res) != 1 || res[0].Stdout != "out-uname" {
		t.Fatalf("部分结果不符: %+v", res)
	}
}

func TestExecBatchEmptyAndLocate(t *testing.T) {
	svc, id := newTestService(t, &flakyRunDialer{})

	if _, err := svc.ExecBatch(context.Background(), id, nil); err == nil {
		t.Fatalf("空批次应报错")
	}
	if _, err := svc.ExecBatch(context.Background(), "no-such-server", [][]string{{"uname"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知服务器 err = %v, want ErrNotFound", err)
	}
}

// 失败路径也不得泄漏凭据明文(与 Exec 同一套即用即弃语义)。
func TestExecBatchErrorNoLeak(t *testing.T) {
	d := &flakyRunDialer{failAt: 0}
	svc, id := newTestService(t, d)

	_, err := svc.ExecBatch(context.Background(), id, [][]string{{"uname"}})
	if err != nil && strings.Contains(err.Error(), leakMarker) {
		t.Fatalf("错误泄漏凭据明文: %v", err)
	}
}
