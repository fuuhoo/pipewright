package approval

import (
	"testing"
	"time"
)

func TestKeyComposition(t *testing.T) {
	if Key("r1", "s1") != "r1|s1" {
		t.Errorf("Key = %q", Key("r1", "s1"))
	}
}

func TestResolveDelivers(t *testing.T) {
	c := New()
	key := Key("r1", "s1")
	ch := c.Wait(key)
	if !c.IsWaiting(key) {
		t.Fatal("应在等待")
	}
	go func() {
		// 给主协程时间进入接收。
		time.Sleep(5 * time.Millisecond)
		if !c.Resolve(key, Decision{Approved: true, Actor: "admin"}) {
			t.Errorf("Resolve 应成功")
		}
	}()
	select {
	case d := <-ch:
		if !d.Approved || d.Actor != "admin" {
			t.Errorf("决定不符:%+v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("超时未收到决定")
	}
	if c.IsWaiting(key) {
		t.Error("决定后应移除等待者")
	}
}

func TestResolveNoWaiter(t *testing.T) {
	c := New()
	if c.Resolve(Key("x", "y"), Decision{Approved: true}) {
		t.Error("无等待者时 Resolve 应返回 false")
	}
}

func TestResolveTwiceSecondFails(t *testing.T) {
	c := New()
	key := Key("r", "s")
	ch := c.Wait(key)
	if !c.Resolve(key, Decision{Approved: true, Actor: "a"}) {
		t.Fatal("首次 Resolve 应成功")
	}
	<-ch
	if c.Resolve(key, Decision{Approved: false, Actor: "b"}) {
		t.Error("二次 Resolve 应失败(同门只决一次)")
	}
}

func TestCancelRemovesWaiter(t *testing.T) {
	c := New()
	key := Key("r", "s")
	c.Wait(key)
	c.Cancel(key)
	if c.IsWaiting(key) {
		t.Error("Cancel 后不应仍在等待")
	}
	if c.Resolve(key, Decision{}) {
		t.Error("Cancel 后 Resolve 应失败")
	}
	c.Cancel(key) // 幂等
}

func TestPendingKeys(t *testing.T) {
	c := New()
	c.Wait(Key("r1", "s1"))
	c.Wait(Key("r2", "s2"))
	if len(c.PendingKeys()) != 2 {
		t.Errorf("PendingKeys = %v", c.PendingKeys())
	}
}

// TestBeginWaitRefCountsPerRun 是「一个运行同时挂两道门」的地基:第一道门进等待时才该把
// run 置成 waiting_approval,第二道不该重复翻;最后一道决定完才该放回 running。
func TestBeginWaitRefCountsPerRun(t *testing.T) {
	c := New()
	_, sharedFirst := c.BeginWait("r1", Key("r1", "s1"))
	if sharedFirst {
		t.Error("第一道门不该被认成「已有门在等」")
	}
	if !c.HasWaiterFor("r1") {
		t.Error("有门在等时 HasWaiterFor 应为 true")
	}
	_, sharedSecond := c.BeginWait("r1", Key("r1", "deploy:job-1"))
	if !sharedSecond {
		t.Error("第二道门应看到别的门在等(否则会把状态重复翻一遍)")
	}

	// 决定掉一道:另一道还在等,run 就该留在 waiting。
	c.Resolve(Key("r1", "s1"), Decision{Approved: true})
	if !c.HasWaiterFor("r1") {
		t.Error("还剩一道门在等,不该报「无等待者」")
	}
	c.Resolve(Key("r1", "deploy:job-1"), Decision{Approved: true})
	if c.HasWaiterFor("r1") {
		t.Error("两道门都决完才该报无等待者")
	}
	if c.HasWaiterFor("r2") {
		t.Error("不该串到别的运行")
	}
}

// 门 ID 前缀是前端分辨「阶段审批」还是「分批确认」的唯一依据。
func TestDeployGateID(t *testing.T) {
	id := DeployGateID("job-9")
	if id != "deploy:job-9" || !IsDeployGate(id) {
		t.Errorf("DeployGateID = %q (IsDeployGate=%v)", id, IsDeployGate(id))
	}
	if IsDeployGate("stage-1") {
		t.Error("阶段门不该被认成分批确认门")
	}
}
