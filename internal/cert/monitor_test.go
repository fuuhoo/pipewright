package cert

// monitor_test.go 验的是到期扫描的**判断与副作用**:哪张进哪个桶、提醒发了几条、库里落下什么。
// 假 CA / 假 TXT / 假执行器都直接用既有夹具(acme_test.go、deploy_test.go),这里只补两样
// 扫描测试才需要的东西:一个能拨表的时钟(节流窗口只有把时间往前推才看得出效果),
// 和一个只记账的 notifier。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// —— 假 notifier ——————————————————————————————————

// recNotifier 把收到的事件原样记下来:监控的判断全在「发了几条、什么种类、带了什么话」上,
// 通知本身(渠道、模板)由 notify 侧另测。
type recNotifier struct {
	mu     sync.Mutex
	events []CertEvent
}

func (r *recNotifier) NotifyCertEvent(_ context.Context, e CertEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recNotifier) snapshot() []CertEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CertEvent(nil), r.events...)
}

// kinds 是事件种类序列,便于整串比对「哪几轮发了、哪几轮被节流吞了」。
func (r *recNotifier) kinds() []string {
	evs := r.snapshot()
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// —— 可控时钟与库内痕迹 ————————————————————————————

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) set(at time.Time) {
	c.mu.Lock()
	c.at = at.UTC()
	c.mu.Unlock()
}

func (c *clock) add(d time.Duration) { c.set(c.now().Add(d)) }

// installClock 把 service 的时间换成可控的:newSvc / newAcmeSvc 都会先钉死在 testNow,
// 而「6 小时后再扫一轮」「8 天后再扫一轮」正是节流要验的东西,所以扫描用例一律先换表。
func installClock(s *service) *clock {
	c := &clock{at: testNow}
	s.now = c.now
	return c
}

// stamps 读回扫描会写的三列,用来断言「这一轮到底在库里落下了什么」而不是只看返回值。
func stamps(t *testing.T, db *sql.DB, id string) (status string, checkedAt, remindAt time.Time) {
	t.Helper()
	var checked, remind string
	err := db.QueryRow(`SELECT status, checked_at, remind_at FROM certificates WHERE id=?`, id).
		Scan(&status, &checked, &remind)
	if err != nil {
		t.Fatalf("读 %s 的扫描痕迹: %v", id, err)
	}
	return status, parseTS(checked), parseTS(remind)
}

// pushNotAfter 把到期时刻改写进续期窗口。假 CA 恒签 90 天(acme_test.go 里写死的),
// 「剩 15 天」这种局面只能改这一列 —— 只改这一列,正文与私钥仍是真签出来的。
func pushNotAfter(t *testing.T, db *sql.DB, id string, at time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE certificates SET not_after=? WHERE id=?`, ts(at), id); err != nil {
		t.Fatalf("改写 %s 的到期时刻: %v", id, err)
	}
}

// setRemindAt 预置「上次提醒时刻」:节流窗口与「续期成功不受窗口约束」都得从已有值出发才验得出。
func setRemindAt(t *testing.T, db *sql.DB, id string, at time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE certificates SET remind_at=? WHERE id=?`, ts(at), id); err != nil {
		t.Fatalf("预置 %s 的提醒时刻: %v", id, err)
	}
}

// importExpiring 导入一张指定到期日的证书 —— upload 来源,正是「平台续不动、只能提醒人自己换」的那类。
func importExpiring(t *testing.T, svc *service, domain string, notAfter time.Time, serial int64) *Certificate {
	t.Helper()
	key := newKey(t)
	c, err := svc.Import(context.Background(), ImportInput{
		Domain:  domain,
		CertPEM: selfCertPEM(t, []string{domain}, testNow.AddDate(0, 0, -1), notAfter, key, serial),
		KeyPEM:  keyPEMFor(t, key, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("导入 %s: %v", domain, err)
	}
	return c
}

// renewReadyAcmeCert 用假 CA 真签一张证书,再把到期时刻改写进窗口,
// 造出「acme 来源 + 自动续期 + 落在窗口内」这张扫描器会自动接手的记录。
func renewReadyAcmeCert(t *testing.T, s *service, db *sql.DB, caID, domain string, notAfter time.Time) string {
	t.Helper()
	id := seedAndRegister(t, s, caID, domain, []string{domain})
	if _, err := s.Issue(context.Background(), id); err != nil {
		t.Fatalf("首次签发 %s: %v", domain, err)
	}
	pushNotAfter(t, db, id, notAfter)
	return id
}

// —— 第一道纪律:后台不起第一单 ——————————————————————

func TestCheckOnceSkipsPendingAndQuietCertificates(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	pending, err := svc.Create(ctx, CreateInput{
		Domain: "not-yet.example.com", CA: CALetsEncryptStaging, DNSProviderID: "dns-1", AutoRenew: true,
	})
	if err != nil {
		t.Fatalf("登记待签证书: %v", err)
	}
	fresh := importExpiring(t, svc, "far.example.com", testNow.AddDate(0, 0, 200), 7101)

	n := &recNotifier{}
	rep, err := svc.CheckOnce(ctx, n)
	if err != nil {
		t.Fatalf("扫描: %v", err)
	}
	if rep.Checked != 1 {
		t.Errorf("本轮看过 %d 张,期望只算有正文的那 1 张", rep.Checked)
	}
	if len(rep.Expiring)+len(rep.Renewed)+len(rep.Failed) != 0 {
		t.Errorf("离到期还有 200 天却动了作: %+v", rep)
	}
	if len(n.kinds()) != 0 {
		t.Errorf("一切还早却发了通知: %+v", n.snapshot())
	}

	// 待签记录连 checked_at 都不该被摸:扫它就等于给用户下了一单(DNS 写记录 + 吃 CA 限流)。
	if status, checked, remind := stamps(t, st.DB, pending.ID); status != StatusPending || !checked.IsZero() || !remind.IsZero() {
		t.Errorf("待签记录被动过: status=%s checked=%s remind=%s", status, checked, remind)
	}
	// 安静的那张只留下「看过了」的痕迹,供页面显示上次检查。
	if status, checked, remind := stamps(t, st.DB, fresh.ID); status != StatusIssued || !checked.Equal(testNow) || !remind.IsZero() {
		t.Errorf("安静记录痕迹异常: status=%s checked=%s(期望 %s) remind=%s", status, checked, testNow, remind)
	}
}

// —— 第二道纪律:提醒必节流 ————————————————————————————

func TestExpiringReminderIsThrottled(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	clk := installClock(svc)
	ctx := context.Background()
	c := importExpiring(t, svc, "manual.example.com", testNow.AddDate(0, 0, 20), 7201)
	n := &recNotifier{}

	rep, err := svc.CheckOnce(ctx, n)
	if err != nil {
		t.Fatalf("第一轮扫描: %v", err)
	}
	if len(rep.Expiring) != 1 {
		t.Fatalf("剩 20 天的导入证书该进需人工换的桶,实得 %+v", rep)
	}
	item := rep.Expiring[0]
	if item.CertID != c.ID || item.DaysLeft != 20 || item.Status != StatusIssued {
		t.Errorf("汇总条目异常: %+v", item)
	}
	if !strings.Contains(item.Detail, "导入的证书") {
		t.Errorf("提醒没说清「为什么平台不自己续」: %q", item.Detail)
	}
	evs := n.snapshot()
	if len(evs) != 1 || evs[0].Kind != MonitorExpiring || evs[0].Message != item.Detail || evs[0].CertID != c.ID {
		t.Fatalf("第一轮提醒 = %+v,期望一条 %s 且话与汇总一致", evs, MonitorExpiring)
	}
	if status, checked, remind := stamps(t, st.DB, c.ID); status != StatusIssued || !checked.Equal(testNow) || !remind.Equal(testNow) {
		t.Errorf("提醒时刻没落下(会被每轮重发): status=%s checked=%s remind=%s", status, checked, remind)
	}

	// 第二轮:6 小时后再扫。报告照旧汇总(页面「立即检查」得看见它),但不能再发第二条,
	// 也不能把 remind_at 往前推 —— 一推就等于给节流窗口续期,从此永远不会再提醒。
	clk.add(6 * time.Hour)
	rep2, err := svc.CheckOnce(ctx, n)
	if err != nil {
		t.Fatalf("第二轮扫描: %v", err)
	}
	if len(rep2.Expiring) != 1 {
		t.Errorf("第二轮仍该汇总出这一张,实得 %+v", rep2)
	}
	if got := n.kinds(); len(got) != 1 {
		t.Errorf("节流窗口内又发了提醒: %v", got)
	}
	if _, checked, remind := stamps(t, st.DB, c.ID); !checked.Equal(clk.now()) || !remind.Equal(testNow) {
		t.Errorf("checked=%s(期望 %s) remind=%s(期望保持 %s)", checked, clk.now(), remind, testNow)
	}

	// 第三轮:满了 7 天,该再吭一声。
	clk.add(8 * 24 * time.Hour)
	if _, err := svc.CheckOnce(ctx, n); err != nil {
		t.Fatalf("第三轮扫描: %v", err)
	}
	evs = n.snapshot()
	if len(evs) != 2 || evs[1].DaysLeft != 12 {
		t.Fatalf("满窗口后没再提醒: %+v", evs)
	}
	if _, _, remind := stamps(t, st.DB, c.ID); !remind.Equal(clk.now()) {
		t.Errorf("第二次的提醒时刻没落库: %s", remind)
	}
}

// 手动「立即检查」不吃自动提醒的额度:nil notifier = 只干活不吭声,也不写 remind_at。
func TestCheckOnceWithNilNotifierStaysSilent(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	installClock(svc)
	c := importExpiring(t, svc, "quiet.example.com", testNow.AddDate(0, 0, 20), 7301)

	rep, err := svc.CheckOnce(context.Background(), nil)
	if err != nil {
		t.Fatalf("扫描: %v", err)
	}
	if len(rep.Expiring) != 1 {
		t.Errorf("nil notifier 也该把要人换的证书汇总出来,实得 %+v", rep)
	}
	// 「上次检查」照旧刷新(页面靠它判断扫描还活着),只有 remind_at 保持空白。
	if _, checked, remind := stamps(t, st.DB, c.ID); !checked.Equal(testNow) || !remind.IsZero() {
		t.Errorf("checked=%s(期望 %s) remind=%s(手动检查不该吃自动提醒的额度)", checked, testNow, remind)
	}
}

// —— 续期失败:旧证书还在服务,所以要报「有人去看」而不是「已经 outage」 ——————

func TestCheckOnceReportsRenewFailure(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	issuer := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	seedProvider(t, st.DB, "dns-1")
	id := renewReadyAcmeCert(t, issuer, st.DB, CALetsEncryptStaging, "half-broken.example.com", testNow.AddDate(0, 0, 10))

	// 换一台「没接 DNS 写入能力」的实例来扫(装配缺 dnsprovider 的真实形态)。
	sweeper := newSvc(t, st.DB, fakeSeal{})
	clk := installClock(sweeper)
	n := &recNotifier{}

	rep, err := sweeper.CheckOnce(ctx, n)
	if err != nil {
		t.Fatalf("扫描: %v", err)
	}
	if len(rep.Failed) != 1 || len(rep.Renewed) != 0 || len(rep.Expiring) != 0 {
		t.Fatalf("续期没成功却归错了桶: %+v", rep)
	}
	if !strings.Contains(rep.Failed[0].Detail, "DNS TXT writer") {
		t.Errorf("失败原因没说清: %q", rep.Failed[0].Detail)
	}
	if got := n.kinds(); len(got) != 1 || got[0] != MonitorRenewFailed {
		t.Errorf("事件 = %v,期望一条 %s", got, MonitorRenewFailed)
	}
	// 状态必须留在 issued:正在服务的还是那张旧证书,标成失败会让页面假报警。
	if status, _, remind := stamps(t, st.DB, id); status != StatusIssued || !remind.Equal(testNow) {
		t.Errorf("失败轮库里痕迹异常: status=%s remind=%s", status, remind)
	}
	var lastErr string
	if err := st.DB.QueryRow(`SELECT last_error FROM certificates WHERE id=?`, id).Scan(&lastErr); err != nil || lastErr == "" {
		t.Errorf("失败原因没回写 last_error: %q(err=%v)", lastErr, err)
	}

	// 同一个失败下一轮不该再刷屏(两类提醒共用一道闸门)。
	clk.add(time.Hour)
	if _, err := sweeper.CheckOnce(ctx, n); err != nil {
		t.Fatalf("第二轮扫描: %v", err)
	}
	if got := n.kinds(); len(got) != 1 {
		t.Errorf("失败提醒没节流: %v", got)
	}

	// 撑到过期之后状态得跟着扫描落库:那一行再没人写,页面读的就是这一列。
	pushNotAfter(t, st.DB, id, testNow.AddDate(0, 0, -2))
	clk.add(8 * 24 * time.Hour)
	if _, err := sweeper.CheckOnce(ctx, n); err != nil {
		t.Fatalf("第三轮扫描: %v", err)
	}
	if status, _, _ := stamps(t, st.DB, id); status != StatusExpired {
		t.Errorf("正文早过期了,库里状态仍是 %s", status)
	}
}

// —— 续期成功:就地续 + 按配置自动铺发 + 不节流 ————————————

func TestCheckOnceRenewsAndAutoDeploys(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	pub := &fakePub{}
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	WithPublisher(pub)(s)
	installClock(s)
	seedProvider(t, st.DB, "dns-1")
	seedHost(t, st.DB, "srv-1")
	id := renewReadyAcmeCert(t, s, st.DB, CALetsEncryptStaging, "rotating.example.com", testNow.AddDate(0, 0, 15))

	if _, err := s.AddTarget(ctx, id, DeployTargetInput{
		ServerID: "srv-1", CertPath: "/etc/ssl/rotating.crt", KeyPath: "/etc/ssl/rotating.key",
		PostCommand: "nginx -s reload", AutoDeploy: true,
	}); err != nil {
		t.Fatalf("加自动铺发落点: %v", err)
	}

	n := &recNotifier{}
	rep, err := s.CheckOnce(ctx, n)
	if err != nil {
		t.Fatalf("扫描: %v", err)
	}
	if len(rep.Renewed) != 1 {
		t.Fatalf("窗口内的 acme 证书没被续期: %+v", rep)
	}
	item := rep.Renewed[0]
	if item.DaysLeft != 90 || !strings.Contains(item.Detail, "已续期到") {
		t.Errorf("续期条目异常: %+v", item)
	}
	if item.Deploy != "已铺发 1 个落点" {
		t.Errorf("自动铺发结果 = %q", item.Deploy)
	}
	evs := n.snapshot()
	if len(evs) != 1 || evs[0].Kind != MonitorRenewed {
		t.Fatalf("事件 = %+v,期望一条 %s", n.snapshot(), MonitorRenewed)
	}
	if !strings.Contains(evs[0].Message, "已续期到") || !strings.Contains(evs[0].Message, "已铺发 1 个落点") {
		t.Errorf("续期通知少了该说的话: %q", evs[0].Message)
	}
	// 真往目标机铺了两个文件(私钥 + 正文)。
	if len(pub.uploads()) != 2 {
		t.Errorf("落点上传次数 = %d,期望 2", len(pub.uploads()))
	}
	// 续期成功不写 remind_at:新正文把窗口推远 90 天,留着旧值也吞不掉下一次提醒。
	if _, checked, remind := stamps(t, st.DB, id); !checked.Equal(testNow) || !remind.IsZero() {
		t.Errorf("续期轮痕迹异常: checked=%s remind=%s", checked, remind)
	}

	// 刚提醒过也照样要报续期成功:节流是给「反复出现的提醒」的,不是给一次独立的事件的。
	pushNotAfter(t, st.DB, id, testNow.AddDate(0, 0, 15))
	setRemindAt(t, st.DB, id, testNow)
	if _, err := s.CheckOnce(ctx, n); err != nil {
		t.Fatalf("第二轮扫描: %v", err)
	}
	if got := n.kinds(); len(got) != 2 || got[1] != MonitorRenewed {
		t.Errorf("续期成功被节流连同吞掉了: %v", got)
	}
	if _, _, remind := stamps(t, st.DB, id); !remind.Equal(testNow) {
		t.Errorf("续期成功不该改提醒时刻,实得 %s", remind)
	}
}

// TestRenewedDeployHints 只管续期后那「一句话」:三种铺发配置的话术都得让用户知道下一步做什么。
func TestRenewedDeployHints(t *testing.T) {
	cases := []struct {
		name       string
		pub        Publisher
		target     bool
		wantPhrase string
	}{
		{"没接执行器", nil, false, "自动铺发没做成"},
		{"没配自动铺发落点", &fakePub{}, false, "没有配置自动铺发的落点"},
		{"落点铺发失败", &fakePub{uploadErr: func(string, string) error { return errors.New("连接被拒绝") }}, true, "1 个失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			txt := newTXTRecorder()
			f := newFakeCA(t, txt)
			s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
			WithPublisher(c.pub)(s)
			installClock(s)
			seedProvider(t, st.DB, "dns-1")
			seedHost(t, st.DB, "srv-1")
			id := renewReadyAcmeCert(t, s, st.DB, CALetsEncryptStaging, "hint.example.com", testNow.AddDate(0, 0, 15))
			if c.target {
				if _, err := s.AddTarget(ctx, id, DeployTargetInput{
					ServerID: "srv-1", CertPath: "/etc/ssl/hint.crt", KeyPath: "/etc/ssl/hint.key", AutoDeploy: true,
				}); err != nil {
					t.Fatalf("加落点: %v", err)
				}
			}
			rep, err := s.CheckOnce(ctx, nil)
			if err != nil {
				t.Fatalf("扫描: %v", err)
			}
			if len(rep.Renewed) != 1 {
				t.Fatalf("这张该被续期: %+v", rep)
			}
			if got := rep.Renewed[0].Deploy; !strings.Contains(got, c.wantPhrase) {
				t.Errorf("铺发一句话 = %q,期望含 %q", got, c.wantPhrase)
			}
			// 证书本身已经续好了:铺发再不顺也不能把它算进失败桶。
			if len(rep.Failed) != 0 {
				t.Errorf("铺发问题被当成了续期失败: %+v", rep.Failed)
			}
		})
	}
}

// —— 纯判断:节流窗口与「为什么不自己续」 ——————————————————

func TestReminderDueWindow(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		name     string
		remindAt time.Time
		at       time.Time
		want     bool
	}{
		{"从没发过", time.Time{}, testNow, true},
		{"刚发过", testNow, testNow, false},
		{"差一秒满窗口", testNow, testNow.Add(7*day - time.Second), false},
		{"正好满窗口", testNow, testNow.Add(7 * day), true},
		{"过了窗口", testNow, testNow.Add(8 * day), true},
	}
	for _, c := range cases {
		cert := &Certificate{RemindAt: c.remindAt}
		if got := cert.ReminderDue(c.at); got != c.want {
			t.Errorf("%s: ReminderDue(%s) = %v,期望 %v", c.name, c.at.Sub(testNow), got, c.want)
		}
	}
}

func TestManualRenewHintTellsWhatToDo(t *testing.T) {
	cases := []struct {
		name string
		cert Certificate
		want string
	}{
		{"已经过期", Certificate{Source: SourceUpload, NotAfter: testNow.Add(-25 * time.Hour)}, "过期 2 天"},
		{"导入的还没到期", Certificate{Source: SourceUpload, NotAfter: testNow.AddDate(0, 0, 10)}, "导入的证书"},
		{"关了自动续期", Certificate{Source: SourceACME, NotAfter: testNow.AddDate(0, 0, 10)}, "关了自动续期"},
	}
	for _, c := range cases {
		got := manualRenewHint(&c.cert, testNow)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: 提示 = %q,期望含 %q", c.name, got, c.want)
		}
	}
}

// —— 后台周期器:跑起来、停得下、单轮炸了也不死 ——————————————

type stubRunner struct {
	mu      sync.Mutex
	calls   int
	panicOn map[int]bool
}

func (r *stubRunner) CheckOnce(context.Context, Notifier) (*CheckReport, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if r.panicOn[n] {
		panic("扫描器炸了")
	}
	return &CheckReport{Checked: n}, nil
}

func (r *stubRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func waitFor(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// newTestMonitor 直接拼结构体:NewMonitor 只接受整个 Service,而这里要的是「一轮就 panic 的替身」。
// first / interval 压到毫秒级,否则测不到多轮。
func newTestMonitor(runner checkRunner) *Monitor {
	return &Monitor{
		runner:   runner,
		interval: 10 * time.Millisecond,
		first:    time.Millisecond,
		stop:     make(chan struct{}),
	}
}

func TestMonitorKeepsRunningAfterPanic(t *testing.T) {
	r := &stubRunner{panicOn: map[int]bool{2: true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newTestMonitor(r)
	m.Start(ctx)
	// 第 2 轮故意 panic:兜不住的话,后面永远等不到第 4 轮 —— 那正是「证书到期那天没人看」的形态。
	waitFor(t, 2*time.Second, func() bool { return r.count() >= 4 }, "扫描循环在单轮 panic 后就停了")

	before := r.count()
	m.Stop()
	m.Stop() // 幂等:第二次 Stop 不能再 close 已关的 channel
	time.Sleep(50 * time.Millisecond)
	if after := r.count(); after != before {
		t.Errorf("Stop 之后还在扫:%d → %d", before, after)
	}
}

func TestMonitorStopsOnContextCancel(t *testing.T) {
	r := &stubRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	m := newTestMonitor(r)
	m.Start(ctx)
	waitFor(t, 2*time.Second, func() bool { return r.count() >= 2 }, "扫描没跑起来")
	cancel()

	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Stop 没能等到 goroutine 退出")
	}
}

func TestNewMonitorDefaults(t *testing.T) {
	st := storetest.Open(t)
	svc := New(st.DB, fakeSeal{})
	for _, interval := range []time.Duration{0, -time.Minute} {
		m := NewMonitor(svc, nil, interval)
		if m.interval != DefaultMonitorInterval {
			t.Errorf("interval=%s 时应回落到默认节奏,实得 %s", interval, m.interval)
		}
		if m.first != time.Minute {
			t.Errorf("首轮延时 = %s,期望 1 分钟(避开启动高峰)", m.first)
		}
	}
	if got := NewMonitor(svc, nil, 3*time.Hour); got.interval != 3*time.Hour {
		t.Errorf("显式节奏被改写了: %s", got.interval)
	}
}
