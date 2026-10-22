package cert

// monitor.go —— 「监控」这条线:定期扫一遍库里的证书,该续的续、续完按配置自动铺发,
// 再把三件值得人说的事讲出去:快到期而平台续不动的、平台刚续好的、平台尝试续却失败的。
//
// 三条纪律:
//   - **后台不起第一单**。登记了但还没签出正文的记录(status=pending)一律跳过:首次签发会写
//     DNS 记录、会吃 CA 的限流额度,必须是用户明确点「签发」的结果。后台只负责「别让它悄悄过期」。
//   - **提醒必节流**。一张剩 29 天的证书在整个续期窗口里每轮都会被判中,不节流就是几天一条
//     一模一样的通知,结局是用户把证书通知整体关掉 —— 那连真出事也没人看。见 ReminderInterval。
//   - **通知是旁路**。渠道挂了不能改变「证书该续就续」这件事,所以事件只发不等、失败只进日志。

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// 监控事件种类(领域枚举)。刻意不是通知渠道的事件名:cert 包不该 import notify,
// 由 httpapi 的适配器把这里映射到 notify.EventCert* —— 与 anomaly.Notifier 同一套解耦方式。
const (
	// MonitorExpiring 落到了续期窗口(或已过期),但平台不会自己续:导入的证书,或用户关了自动续期。
	MonitorExpiring = "expiring"
	// MonitorRenewed 平台这一轮把它续出来了(并已按配置自动铺发)。
	MonitorRenewed = "renewed"
	// MonitorRenewFailed 平台尝试续它但没成功(旧证书通常还在服务,所以这条是「要有人去看」而非「已 outage」)。
	MonitorRenewFailed = "renew_failed"
)

// CertEvent 是一条监控事件(notifier 的载荷)。字段全为人读展示而带,不含任何密钥材料。
type CertEvent struct {
	Kind     string // MonitorExpiring / MonitorRenewed / MonitorRenewFailed
	CertID   string
	Domain   string
	Status   string
	NotAfter time.Time
	DaysLeft int    // 剩余天数(负数 = 已过期多少天)
	Message  string // 一行话:该做什么 / 为什么没续上
}

// Notifier 抽象「监控里发生了一件值得说的事」(由 httpapi 适配到 notify;nil = 只干活不吭声)。
// 实现侧 best-effort:自带超时、失败只记日志,绝不阻断扫描。
type Notifier interface {
	NotifyCertEvent(ctx context.Context, event CertEvent)
}

// CheckItem 是本轮里被点到的一张证书。
type CheckItem struct {
	CertID   string
	Domain   string
	Status   string
	DaysLeft int
	Detail   string // 人话:为什么提醒 / 为什么没续上 / 续到了什么时候
	Deploy   string // 自动铺发的一句话结果(只有续成功那条会填)
}

// CheckReport 是一轮扫描的汇总。手动检查端点把它原样返回给页面,后台每轮则只拿去写日志。
type CheckReport struct {
	Checked  int         // 本轮看过(有正文)的证书数
	Expiring []CheckItem // 需要人工换正文的
	Renewed  []CheckItem // 平台续好的
	Failed   []CheckItem // 平台尝试续但失败的
}

// CheckOnce 扫一遍全部证书:落在续期窗口内的 acme 证书就地续期并按配置自动铺发,
// 平台续不动的那批报出来让人自己换。
//
// notifier 为 nil = 「只看只报,不发消息」,并且**不写 remind_at**:页面上点一下「立即检查」
// 不该把下一轮自动提醒的额度吃掉(节流是给后台重复扫描用的)。
func (s *service) CheckOnce(ctx context.Context, notifier Notifier) (*CheckReport, error) {
	rows, err := s.store.listCerts(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	rep := &CheckReport{}
	for _, r := range rows {
		c := r.Certificate
		if c.NotAfter.IsZero() {
			continue // 还没正文:见文件头第一条纪律。
		}
		rep.Checked++
		// 本轮对这张证书的判定结果(只有 issued / expired 两种:能走到这里的行必然有正文)。
		// 直接改写手里这份,下面汇总与事件里说的状态就和库里落的一致。
		c.Status = DeriveStatus(true, c.NotAfter, now)
		if !now.Add(IssuanceWindow).After(c.NotAfter) {
			// 离到期还远,本轮什么都没做 —— 但仍要留下「看过」的痕迹(页面的「上次检查」读它)。
			// 写不动就是库出问题了,后面每张的提醒时刻也会跟着不准,直接停下让下一轮重来。
			if err := s.store.markCertChecked(ctx, c.ID, now, c.Status); err != nil {
				return rep, err
			}
			continue
		}

		if !NeedsRenew(&c, now) {
			item := CheckItem{
				CertID: c.ID, Domain: c.Domain, Status: c.Status,
				DaysLeft: c.DaysLeft(now), Detail: manualRenewHint(&c, now),
			}
			rep.Expiring = append(rep.Expiring, item)
			if err := s.remind(ctx, notifier, &c, MonitorExpiring, item.Detail, now); err != nil {
				return rep, err
			}
			continue
		}

		updated, err := s.Issue(ctx, c.ID)
		if err != nil {
			// last_error 由 Issue 内部回写(markIssueFailure);它不会动 status(有正文时不该标 failed),
			// 所以这里照常走 remind:把「其实已经过期」落库,顺带决定要不要再吭一声。
			item := CheckItem{
				CertID: c.ID, Domain: c.Domain, Status: c.Status,
				DaysLeft: c.DaysLeft(now), Detail: humanErr(err),
			}
			rep.Failed = append(rep.Failed, item)
			if err := s.remind(ctx, notifier, &c, MonitorRenewFailed, item.Detail, now); err != nil {
				return rep, err
			}
			continue
		}

		item := CheckItem{
			CertID: updated.ID, Domain: updated.Domain, Status: updated.Status,
			DaysLeft: updated.DaysLeft(now),
			Detail:   fmt.Sprintf("已续期到 %s", updated.NotAfter.Format(time.RFC3339)),
		}
		item.Deploy = s.autoDeployHint(ctx, updated.ID)
		rep.Renewed = append(rep.Renewed, item)
		// 续期成功不节流:一次成功就是一次独立的事,而且新正文把窗口推远了 90 天,
		// 本来也不会连着再报。remind_at 也保持不动 —— 7 天的窗口远短于 90 天,留着旧值不会吞掉下一次提醒。
		publish(notifier, CertEvent{
			Kind: MonitorRenewed, CertID: updated.ID, Domain: updated.Domain,
			Status: updated.Status, NotAfter: updated.NotAfter, DaysLeft: item.DaysLeft,
			Message: fmt.Sprintf("%s;%s", item.Detail, item.Deploy),
		})
	}
	return rep, nil
}

// remind 处理「会反复出现」的两类提醒:节流窗口内(以及手动检查)直接哑火。
// 但**发不发都要把 checked_at 与本轮状态落库**:页面靠「上次检查」判断扫描还在跑,
// 落在窗口里的恰好是最需要被看见的那几张,不写就等于它们悄悄停了。
// 只有真发出去才连 remind_at 一起写 —— 那是节流窗口的依据。
func (s *service) remind(ctx context.Context, notifier Notifier, c *Certificate, kind, message string, now time.Time) error {
	if notifier == nil || !c.ReminderDue(now) {
		return s.store.markCertChecked(ctx, c.ID, now, c.Status)
	}
	publish(notifier, CertEvent{
		Kind: kind, CertID: c.ID, Domain: c.Domain, Status: c.Status,
		NotAfter: c.NotAfter, DaysLeft: c.DaysLeft(now), Message: message,
	})
	return s.store.markCertReminded(ctx, c.ID, now, c.Status)
}

// autoDeployHint 续期后按配置自动铺发,并给出**一句人话**结果(不带私钥、不带命令内容)。
//
// 为什么把失败算在「续期成功」这一条里而不是单独报错:证书已经换新了,服务照常跑,
// 只是某个落点上还是旧文件 —— 那是「记得手动发布一下」,不是「签证书失败了」。
func (s *service) autoDeployHint(ctx context.Context, certID string) string {
	report, err := s.DeployAuto(ctx, certID)
	if err != nil {
		return "自动铺发没做成:" + humanErr(err)
	}
	switch {
	case len(report.Results) == 0:
		return "没有配置自动铺发的落点,需要时手动发布"
	case report.Failed() > 0:
		return fmt.Sprintf("已铺发 %d 个落点,%d 个失败(首个:%s)",
			report.Deployed(), report.Failed(), report.Results[0].Error)
	default:
		return fmt.Sprintf("已铺发 %d 个落点", report.Deployed())
	}
}

// manualRenewHint 说清「这张平台为什么不自己续」。用户看到的是一句能接着行动的话,
// 而不是一个来源枚举:导入的与关了自动续期的,该做的事完全不同。
func manualRenewHint(c *Certificate, now time.Time) string {
	left := c.DaysLeft(now)
	if left < 0 {
		return fmt.Sprintf("已于 %s 过期 %d 天,平台不会自动替换它,需要重新导入或改用平台签发",
			c.NotAfter.Format("2006-01-02"), -left)
	}
	if c.Source != SourceACME {
		return fmt.Sprintf("剩 %d 天到期;这张是导入的证书,平台签不出来,请在来源处续好后重新导入", left)
	}
	return fmt.Sprintf("剩 %d 天到期;这张关了自动续期,需要时手动签发或重新打开自动续期", left)
}

// publish 把事件交出去:nil notifier = 本轮不发(手动检查路径)。不等结果、不看失败:
// 通知是旁路,渠道挂了不能改变「证书该续就续」这件事。
// 用 context.Background 而不是扫描那一轮的 ctx —— 进程停机时最后几条提醒不该跟着被取消
// (通知实现自己带超时,见 httpapi.NewCertNotifier)。
func publish(notifier Notifier, event CertEvent) {
	if notifier == nil {
		return
	}
	notifier.NotifyCertEvent(context.Background(), event)
}

// checkRunner 是 Monitor 需要的那一点能力(Service.CheckOnce;窄接口便于单测塞假实现)。
type checkRunner interface {
	CheckOnce(ctx context.Context, notifier Notifier) (*CheckReport, error)
}

// DefaultMonitorInterval 是到期扫描的默认节奏:一天一次。
//
// 为什么够用:续期窗口与提醒节流都按「天」计(窗口 30 天、节流 7 天),一天一扫就意味着
// 一张证书在整个窗口里最多被判定 30 次、被提醒 4 次,而一次续期失败也有几十天重试余地 ——
// 再密只是把 checked_at 刷得更勤,并不会让证书早一天续上。
// 需要更勤可用 PIPEWRIGHT_CERT_MONITOR_INTERVAL(整数秒)覆盖。
const DefaultMonitorInterval = 24 * time.Hour

// Monitor 是证书到期监控的后台周期器(ticker + ctx 取消 + Stop 幂等 + 单轮 panic 兜住)。
// 形态对齐 internal/previewenv.Sweeper:启动后先等一会儿再扫,避开启动高峰与端口还没就绪的时候。
type Monitor struct {
	runner   checkRunner
	notifier Notifier
	interval time.Duration
	first    time.Duration

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewMonitor 构造到期监控器。interval<=0 用 DefaultMonitorInterval。
// notifier 传 nil 也可以:监控照跑(续期与自动铺发仍会发生),只是不发消息。
func NewMonitor(svc Service, notifier Notifier, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = DefaultMonitorInterval
	}
	return &Monitor{
		runner:   svc,
		notifier: notifier,
		interval: interval,
		first:    time.Minute,
		stop:     make(chan struct{}),
	}
}

// Start 启动后台扫描 goroutine;未 Start 不起任务。
func (m *Monitor) Start(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		first := time.NewTimer(m.first)
		defer first.Stop()
		select {
		case <-m.stop:
			return
		case <-ctx.Done():
			return
		case <-first.C:
			m.runOnce(ctx)
		}
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runOnce(ctx)
			}
		}
	}()
}

// runOnce 跑一轮扫描;recover 包裹:任何 panic 只记日志,绝不杀死循环
// (后台任务被一次空指针杀死 = 从此没人再看证书,而那恰好是到期那天才会被发现的事)。
func (m *Monitor) runOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[cert] 到期扫描单轮 panic 已恢复:%v", r)
		}
	}()
	rep, err := m.runner.CheckOnce(ctx, m.notifier)
	if err != nil {
		log.Printf("[cert] 到期扫描失败:%v", err)
		return
	}
	if len(rep.Expiring)+len(rep.Renewed)+len(rep.Failed) == 0 {
		return // 一切还早,不占日志。
	}
	log.Printf("[cert] 到期扫描:%d 张里续期成功 %d、失败 %d、需人工换 %d",
		rep.Checked, len(rep.Renewed), len(rep.Failed), len(rep.Expiring))
}

// Stop 停止扫描 goroutine(幂等)并等待退出。
func (m *Monitor) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	m.wg.Wait()
}
