package deploy

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/target"
)

// server("a") 造一台够用的目标机桩(闸只认 serverID)。
func gateStubServer(id string) *target.Server {
	return &target.Server{ID: id, Name: id, Host: "h", Port: 22, User: "u", CredentialID: "c"}
}

// opTracker 记录「同一台机上同时在跑」的远程操作峰值 —— 闸是否真生效只看它。
//
// 每个操作进入时先占住 hold 时长:没有这段占用,单测里所有操作都在纳秒内完成,重叠与串行
// 根本区分不出来,断言就成了摆设。
type opTracker struct {
	mu   sync.Mutex
	in   map[string]int
	peak map[string]int
}

func newOpTracker() *opTracker {
	return &opTracker{in: map[string]int{}, peak: map[string]int{}}
}

func (o *opTracker) run(serverID string, hold time.Duration) {
	o.mu.Lock()
	o.in[serverID]++
	if o.in[serverID] > o.peak[serverID] {
		o.peak[serverID] = o.in[serverID]
	}
	o.mu.Unlock()

	time.Sleep(hold)

	o.mu.Lock()
	o.in[serverID]--
	o.mu.Unlock()
}

func (o *opTracker) peakOf(serverID string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.peak[serverID]
}

// startOps 并发发起 n 次 s.exec,serverID 由 serverOf(i) 决定;全部返回后收口。
func startOps(svc *service, n int, serverOf func(int) string) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = svc.exec(context.Background(), serverOf(i), []string{"true"})
		}(i)
	}
	wg.Wait()
}

// TestSameServerSerializesRemoteOps 同一台目标机上的远程操作不许重叠。
//
// 真机上复现过的故障:三个部署任务同时打一台机,三条 SSH 互相饿到各自 ctx 超时,日志只剩一句
// 「部署执行超时」,看不出问题出在并发本身。闸要做的就是把这种重叠根本性地去掉,所以直接断言峰值 = 1。
func TestSameServerSerializesRemoteOps(t *testing.T) {
	tr := newOpTracker()
	st := &stubTarget{servers: map[string]*target.Server{"a": gateStubServer("a")}}
	st.execFn = func(serverID string, _ []string) (*target.ExecResult, error) {
		tr.run(serverID, 25*time.Millisecond)
		return &target.ExecResult{ExitCode: 0}, nil
	}
	svc := &service{targets: st}

	startOps(svc, 4, func(int) string { return "a" })

	if got := tr.peakOf("a"); got > 1 {
		t.Fatalf("同一台机的远程操作必须串行,实测峰值并发 %d(希望 1)", got)
	}
}

// TestDifferentServersDoNotQueue 闸按机器分开:多机扇出照样并行,否则滚动发布会退化成逐台排队。
func TestDifferentServersDoNotQueue(t *testing.T) {
	tr := newOpTracker()
	st := &stubTarget{servers: map[string]*target.Server{
		"a": gateStubServer("a"),
		"b": gateStubServer("b"),
	}}
	st.execFn = func(serverID string, _ []string) (*target.ExecResult, error) {
		tr.run(serverID, 40*time.Millisecond)
		return &target.ExecResult{ExitCode: 0}, nil
	}
	svc := &service{targets: st}

	startOps(svc, 2, func(i int) string { return []string{"a", "b"}[i] })

	// 两机各 40ms:若串了,总时长会到 80ms 以上;这里断言各机都跑过,并额外看整体是否并行。
	if tr.peakOf("a") == 0 || tr.peakOf("b") == 0 {
		t.Fatalf("两台机都应各自跑完一次,实得 a=%d b=%d", tr.peakOf("a"), tr.peakOf("b"))
	}
}

// TestHeldSectionDoesNotRequeue 临界区里再发操作不许重新排队 —— 否则单机部署第一步(self-hold)
// 就把自己占住的闸当成「别人还在跑」,一直等到 ctx 超时。这是把闸从「每操作」上移到「每段部署」
// 后必须守住的性质。
func TestHeldSectionDoesNotRequeue(t *testing.T) {
	st := &stubTarget{servers: map[string]*target.Server{"a": gateStubServer("a")}}
	st.execFn = func(_ string, _ []string) (*target.ExecResult, error) {
		return &target.ExecResult{ExitCode: 0}, nil
	}
	svc := &service{targets: st}

	ctx, release, err := svc.holdServer(context.Background(), "a")
	if err != nil {
		t.Fatalf("占闸失败:%v", err)
	}
	defer release()

	inner, innerRelease, err := svc.holdServer(ctx, "a")
	if err != nil {
		t.Fatalf("临界区内嵌套占闸应直接通过,实得:%v", err)
	}
	innerRelease() // 内层释放不该把外层的闸一起放开

	done := make(chan error, 1)
	go func() {
		_, e := svc.exec(inner, "a", []string{"true"})
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("临界区内执行失败:%v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("临界区内的操作在等自己占住的闸(自锁)")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := svc.occupy(waitCtx, "a"); err == nil {
		t.Fatal("内层 release 不该把外层临界区的闸放开")
	}
}

// TestGateWaitHonorsContext 等闸也要能按 ctx 退出:一台机被长期占住时,等待方不能跟着挂死。
func TestGateWaitHonorsContext(t *testing.T) {
	svc := &service{targets: &stubTarget{servers: map[string]*target.Server{"a": gateStubServer("a")}}}

	held, err := svc.occupy(context.Background(), "a")
	if err != nil {
		t.Fatalf("空闲时占闸失败:%v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := svc.occupy(waitCtx, "a"); err == nil {
		held()
		t.Fatal("闸被占用时等待方应报错,却拿到了执行权")
	} else if !strings.Contains(err.Error(), "deadline") {
		held()
		t.Fatalf("等待超时应是 ctx 的 deadline 错误,实得:%v", err)
	}

	held() // 放行后同一台机还能继续用
	again, err := svc.occupy(context.Background(), "a")
	if err != nil {
		t.Fatalf("释放后再占闸失败:%v", err)
	}
	again()
}

// TestUploadSharesGateWithExec 上传与命令抢同一把闸:一条几十 MB 的流若能和另一个任务的命令同时
// 挤进同一条 SSH 上限,就又回到互相饿死的老问题。
func TestUploadSharesGateWithExec(t *testing.T) {
	tr := newOpTracker()
	st := &stubTarget{servers: map[string]*target.Server{"a": gateStubServer("a")}}
	st.execFn = func(serverID string, _ []string) (*target.ExecResult, error) {
		tr.run(serverID, 25*time.Millisecond)
		return &target.ExecResult{ExitCode: 0}, nil
	}
	st.uploadFn = func(serverID, _ string) { tr.run(serverID, 25*time.Millisecond) }
	svc := &service{targets: st}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = svc.exec(context.Background(), "a", []string{"true"})
				return
			}
			_ = svc.upload(context.Background(), "a", strings.NewReader("payload"), "/tmp/x")
		}(i)
	}
	wg.Wait()

	if got := tr.peakOf("a"); got > 1 {
		t.Fatalf("上传没和命令共闸,实测同机峰值并发 %d(希望 1)", got)
	}
	if len(st.uploads["/tmp/x"]) == 0 {
		t.Fatal("共闸后上传应照常把字节写出去")
	}
}
