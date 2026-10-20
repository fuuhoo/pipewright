package cert

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/target"
)

// fakePub 是 Publisher 的替身:把每一次上传/执行都按顺序记下来,失败由钩子注入。
//
// 记账式假实现是这条路径唯一有效的测法 —— 真实行为全在「调了几次、什么顺序、参数长什么样」上:
// 先私钥还是先正文、有没有把内容拼进命令、失败后临时文件清没清,看返回值根本看不出来。
type fakePub struct {
	calls []pubCall

	// uploadErr / execFail 决定单步成败:返回 nil 就是这一步顺利。
	uploadErr func(serverID, dest string) error
	execFail  func(serverID string, cmd []string) *target.ExecResult
}

type pubCall struct {
	kind     string // "upload" / "exec"
	serverID string
	dest     string // upload 的目标路径
	content  string // upload 的正文(用来验证私钥只走流、不进命令)
	cmd      []string
}

func (f *fakePub) Upload(ctx context.Context, serverID string, content io.Reader, remotePath string) error {
	data, _ := io.ReadAll(content)
	f.calls = append(f.calls, pubCall{kind: "upload", serverID: serverID, dest: remotePath, content: string(data)})
	if f.uploadErr != nil {
		return f.uploadErr(serverID, remotePath)
	}
	return nil
}

func (f *fakePub) Exec(ctx context.Context, serverID string, cmd []string) (*target.ExecResult, error) {
	f.calls = append(f.calls, pubCall{kind: "exec", serverID: serverID, cmd: cmd})
	if f.execFail != nil {
		if res := f.execFail(serverID, cmd); res != nil {
			return res, nil
		}
	}
	return &target.ExecResult{ExitCode: 0}, nil
}

// 断言辅助:分别取上传流与命令流,顺序保持调用序。
func (f *fakePub) uploads() []pubCall {
	out := make([]pubCall, 0, len(f.calls))
	for _, c := range f.calls {
		if c.kind == "upload" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakePub) execs() []pubCall {
	out := make([]pubCall, 0, len(f.calls))
	for _, c := range f.calls {
		if c.kind == "exec" {
			out = append(out, c)
		}
	}
	return out
}

// argvJoined 把所有命令拼成一坨文本,供「私钥绝不能出现在命令里」这类反向断言。
func (f *fakePub) argvJoined() string {
	var b strings.Builder
	for _, c := range f.execs() {
		b.WriteString(strings.Join(c.cmd, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// newPubSvc 构造接了假执行器、时间固定的 service。
func newPubSvc(t *testing.T, db *sql.DB, pub Publisher) *service {
	t.Helper()
	s, ok := New(db, fakeSeal{}, WithPublisher(pub)).(*service)
	if !ok {
		t.Fatal("New 应返回 *service")
	}
	s.now = func() time.Time { return testNow }
	return s
}

// seedCert 导入一张有效证书并回取其证书包(发布要铺的就是这两份真字节)。
func seedCert(t *testing.T, svc *service, domain string, serial int64) (*Certificate, *Bundle) {
	t.Helper()
	key := newKey(t)
	c, err := svc.Import(context.Background(), ImportInput{
		Domain:  domain,
		CertPEM: selfCertPEM(t, []string{domain}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, serial),
		KeyPEM:  keyPEMFor(t, key, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("导入测试证书: %v", err)
	}
	b, err := svc.Bundle(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("取证书包: %v", err)
	}
	return c, b
}

// stagingOf 从「临时名 → mv 到位」的调用记录里取出某个最终路径对应的临时名。
func stagingOf(t *testing.T, f *fakePub, final string) pubCall {
	t.Helper()
	base := path.Base(final)
	for _, c := range f.uploads() {
		b := path.Base(c.dest)
		if strings.HasPrefix(b, "."+base+".pipewright-") && strings.HasSuffix(b, ".tmp") && path.Dir(c.dest) == path.Dir(final) {
			return c
		}
	}
	t.Fatalf("没有 %s 的临时名上传记录(调用序: %+v)", final, f.calls)
	return pubCall{}
}

// —— 装配缺失与前置条件 ——————————————————————————————

func TestDeployRequiresPublisher(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()
	seedHost(t, st.DB, "srv-1")
	c, _ := seedCert(t, svc, "nopub.example.com", 11001)
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-1", CertPath: "/etc/ssl/nopub.crt", KeyPath: "/etc/ssl/nopub.key",
	}); err != nil {
		t.Fatalf("加落点: %v", err)
	}
	// 没接执行能力必须当场说清楚:一条网络超时会被读成「目标机坏了」,方向完全错。
	if _, err := svc.Deploy(ctx, c.ID); !errors.Is(err, ErrPublisherUnconfigured) {
		t.Errorf("未装配发布器 = %v, 期望 %v", err, ErrPublisherUnconfigured)
	}
}

func TestDeployRejectsPendingCertificate(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-p")
	seedProvider(t, st.DB, "dns-p")

	c, err := svc.Create(ctx, CreateInput{
		Domain: "pending.example.com", CA: CALetsEncryptStaging, DNSProviderID: "dns-p",
	})
	if err != nil {
		t.Fatalf("登记待签证书: %v", err)
	}
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-p", CertPath: "/etc/ssl/p.crt", KeyPath: "/etc/ssl/p.key",
	}); err != nil {
		t.Fatalf("加落点: %v", err)
	}
	// 待签记录没正文:Bundle 挡在前面,一个字节都不该往机器上铺。
	if _, err := svc.Deploy(ctx, c.ID); !errors.Is(err, ErrNoCertBlock) {
		t.Errorf("发布待签证书 = %v, 期望 %v", err, ErrNoCertBlock)
	}
	if len(pub.calls) != 0 {
		t.Errorf("前置失败却动了目标机: %+v", pub.calls)
	}
}

// —— 正常路径:顺序、权限、用户指令 ————————————————————

func TestDeployPlacesKeyThenCertAndRunsUserCommand(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-1")

	c, bundle := seedCert(t, svc, "shop.example.com", 12001)
	_, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-1", CertPath: "/etc/ssl/shop.crt", KeyPath: "/etc/ssl/shop.key",
		PostCommand: "nginx -s reload", AutoDeploy: true,
	})
	if err != nil {
		t.Fatalf("加落点: %v", err)
	}

	report, err := svc.Deploy(ctx, c.ID)
	if err != nil {
		t.Fatalf("发布: %v", err)
	}
	if report.CertID != c.ID || len(report.Results) != 1 || report.Failed() != 0 || report.Deployed() != 1 {
		t.Fatalf("发布报告异常: %+v", report)
	}
	res := report.Results[0]
	if !res.OK || res.Error != "" || res.ServerID != "srv-1" || !res.DeployedAt.Equal(testNow) {
		t.Errorf("落点结果异常: %+v", res)
	}

	// 全程只上传过两个文件,且都是临时名:直接写最终路径会在超时/断流时留下半个 PEM。
	ups := pub.uploads()
	if len(ups) != 2 {
		t.Fatalf("应有两次上传,实得 %d: %+v", len(ups), ups)
	}
	keyUp, certUp := stagingOf(t, pub, "/etc/ssl/shop.key"), stagingOf(t, pub, "/etc/ssl/shop.crt")
	// 铺到机器上的就是库里解封出来的那两份:私钥是明文那一份,证书是「叶子 + 链」的 fullchain。
	if keyUp.content != bundle.KeyPEM {
		t.Error("目标机上的私钥与库里解封的明文不一致")
	}
	if certUp.content != bundle.Fullchain() {
		t.Error("目标机上的正文与 fullchain 不一致")
	}
	if !strings.Contains(keyUp.content, "PRIVATE KEY") {
		t.Error("测试前提:包里的私钥应是 PEM 正文")
	}
	// 先私钥后正文:反过来的中间窗口里 nginx 一旦被 reload,读到的是「新正文 + 旧私钥」直接不匹配。
	if indexOfCall(pub.calls, keyUp) > indexOfCall(pub.calls, certUp) {
		t.Error("应先铺私钥再铺证书正文")
	}

	// 每个文件:chmod → 同目录 mv。权限必须早于改名 —— 改名是原子的,最终路径上不存在「先宽后紧」窗口。
	execs := pub.execs()
	wantSeq := []string{
		"chmod 0600 " + keyUp.dest,
		"mv -f " + keyUp.dest + " /etc/ssl/shop.key",
		"chmod 0644 " + certUp.dest,
		"mv -f " + certUp.dest + " /etc/ssl/shop.crt",
		"sh -c set -e\nnginx -s reload",
	}
	if len(execs) != len(wantSeq) {
		t.Fatalf("命令序列 = %d 条, 期望 %d:\n%s", len(execs), len(wantSeq), pub.argvJoined())
	}
	for i, want := range wantSeq {
		if got := strings.Join(execs[i].cmd, " "); got != want {
			t.Errorf("第 %d 条命令 = %q, 期望 %q", i+1, got, want)
		}
	}
	// 落位成功 → 没有清理动作,也没有多余的探测命令(平台不代替用户发明命令)。
	for _, c := range execs {
		if c.cmd[0] == "rm" {
			t.Errorf("成功路径不该删临时文件(已 mv 走): %v", c.cmd)
		}
	}

	// 私钥只能以流的形式过 SSH:任何一条命令的 argv 里出现密钥正文就是 AC-SEC-01 破防。
	if strings.Contains(pub.argvJoined(), "PRIVATE KEY") {
		t.Error("命令里出现了私钥正文")
	}

	// 结果回写:页面与告警读的就是这一行。
	targets, err := svc.Targets(ctx, c.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("回读落点: %v", err)
	}
	if targets[0].LastStatus != DeployOK || targets[0].LastError != "" || !targets[0].DeployedAt.Equal(testNow) {
		t.Errorf("落点状态回写异常: %+v", targets[0])
	}
}

func TestDeployWithoutPostCommandRunsNoShell(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-nc")

	c, _ := seedCert(t, svc, "nocmd.example.com", 13001)
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-nc", CertPath: "/etc/ssl/nc.crt", KeyPath: "/etc/ssl/nc.key",
	}); err != nil {
		t.Fatalf("加落点: %v", err)
	}
	if report, err := svc.Deploy(ctx, c.ID); err != nil || report.Failed() != 0 {
		t.Fatalf("发布 = %+v(err=%v)", report, err)
	}
	// 没填生效指令就一条也别跑:猜一条 `nginx -s reload` 或 `systemctl reload nginx`
	// 会把这台机器上跑着的别的服务重启掉,而且用户从没同意过。
	for _, e := range pub.execs() {
		if e.cmd[0] == "sh" {
			t.Errorf("post_command 为空却跑了 shell: %v", e.cmd)
		}
	}
}

// —— 逐落点独立 & auto_deploy 闸门 ——————————————————————

func TestDeployIsolatesFailuresPerTarget(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{uploadErr: func(serverID, dest string) error {
		if serverID == "srv-bad" {
			return errors.New("ssh: 连接被拒绝")
		}
		return nil
	}}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-bad")
	seedHost(t, st.DB, "srv-good")

	c, _ := seedCert(t, svc, "multi.example.com", 14001)
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-bad", CertPath: "/etc/ssl/bad.crt", KeyPath: "/etc/ssl/bad.key",
	}); err != nil {
		t.Fatalf("加落点 1: %v", err)
	}
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-good", CertPath: "/etc/ssl/good.crt", KeyPath: "/etc/ssl/good.key",
	}); err != nil {
		t.Fatalf("加落点 2: %v", err)
	}

	report, err := svc.Deploy(ctx, c.ID)
	if err != nil {
		t.Fatalf("发布本身不该报错(逐落点独立): %v", err)
	}
	if len(report.Results) != 2 || report.Failed() != 1 || report.Deployed() != 1 {
		t.Fatalf("报告 = %+v, 期望 2 个落点 1 成 1 败", report)
	}
	// 一台连不上不该让另一台跟着不发,也不回滚已铺好的那份。
	bad := resultOf(t, report, "srv-bad")
	good := resultOf(t, report, "srv-good")
	if bad.OK || !strings.Contains(bad.Error, "私钥落位失败") {
		t.Errorf("失败落点报告异常: %+v", bad)
	}
	if !good.OK {
		t.Errorf("健康落点被牵连: %+v", good)
	}
	// 上传失败 → 目标机上那个临时名必须清掉(用不受牵连的上下文,父 ctx 超时是最常见失败原因)。
	var cleaned bool
	for _, e := range pub.execs() {
		if e.serverID == "srv-bad" && e.cmd[0] == "rm" && e.cmd[1] == "-f" {
			cleaned = true
			if e.cmd[2] != stagingOf(t, pub, "/etc/ssl/bad.key").dest {
				t.Errorf("清理错了文件: %v", e.cmd)
			}
		}
	}
	if !cleaned {
		t.Errorf("上传失败后没清理临时文件: %s", pub.argvJoined())
	}
	// 失败的那台绝不能改名到位。
	for _, e := range pub.execs() {
		if e.serverID == "srv-bad" && e.cmd[0] == "mv" {
			t.Errorf("上传失败却仍落位: %v", e.cmd)
		}
	}

	targets, err := svc.Targets(ctx, c.ID)
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	badTarget := targetOf(t, targets, "srv-bad")
	if badTarget.LastStatus != DeployFailed || badTarget.LastError == "" || !badTarget.DeployedAt.IsZero() {
		t.Errorf("失败落点应留下原因且不动上次成功时间: %+v", badTarget)
	}
	if got := targetOf(t, targets, "srv-good"); got.LastStatus != DeployOK {
		t.Errorf("成功落点状态 = %s, 期望 %s", got.LastStatus, DeployOK)
	}
	// 人话原因里不能夹带私钥(错误会进库、进页面、进告警)。
	if strings.Contains(badTarget.LastError, "PRIVATE KEY") {
		t.Error("落点错误信息里出现了私钥正文")
	}
}

func TestDeployAutoSkipsManualTargets(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-auto")
	seedHost(t, st.DB, "srv-manual")

	c, _ := seedCert(t, svc, "auto.example.com", 15001)
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-auto", CertPath: "/etc/ssl/a.crt", KeyPath: "/etc/ssl/a.key", AutoDeploy: true,
	}); err != nil {
		t.Fatalf("加自动落点: %v", err)
	}
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-manual", CertPath: "/etc/ssl/m.crt", KeyPath: "/etc/ssl/m.key",
	}); err != nil {
		t.Fatalf("加手动落点: %v", err)
	}

	report, err := svc.DeployAuto(ctx, c.ID)
	if err != nil {
		t.Fatalf("自动发布: %v", err)
	}
	// auto_deploy 关掉的就是「不许平台自己往这台机器写文件」,续期后不能把它当成没说过。
	if len(report.Results) != 1 || report.Results[0].ServerID != "srv-auto" {
		t.Fatalf("自动发布只该碰 auto_deploy 的落点,实得 %+v", report.Results)
	}
	for _, call := range pub.calls {
		if call.serverID == "srv-manual" {
			t.Errorf("动关了自动发布的机器: %+v", call)
		}
	}
	// 手动发布仍然覆盖全部落点。
	if full, err := svc.Deploy(ctx, c.ID); err != nil || len(full.Results) != 2 {
		t.Errorf("手动发布应铺满两个落点,实得 %+v(err=%v)", full, err)
	}
}

// —— 失败语义:reload 慢 / 权限设不上 ————————————————————

func TestDeployReportsPostCommandFailure(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{execFail: func(serverID string, cmd []string) *target.ExecResult {
		if cmd[0] == "sh" {
			return &target.ExecResult{ExitCode: 1, Stderr: "nginx: [emerg] bad conf\n"}
		}
		return nil
	}}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-reload")

	c, _ := seedCert(t, svc, "reload.example.com", 16001)
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-reload", CertPath: "/etc/ssl/r.crt", KeyPath: "/etc/ssl/r.key",
		PostCommand: "nginx -t && nginx -s reload",
	}); err != nil {
		t.Fatalf("加落点: %v", err)
	}
	report, err := svc.Deploy(ctx, c.ID)
	if err != nil {
		t.Fatalf("发布: %v", err)
	}
	if report.Failed() != 1 {
		t.Fatalf("生效指令失败要算这次发布失败: %+v", report.Results)
	}
	if !strings.Contains(report.Results[0].Error, "生效指令失败") ||
		!strings.Contains(report.Results[0].Error, "退出码 1") ||
		!strings.Contains(report.Results[0].Error, "bad conf") {
		t.Errorf("原因要说清是哪一步、退出码与远端摘要,实得 %q", report.Results[0].Error)
	}
	// 多行/带 && 的指令体整体作为 sh 的脚本参数,绝不与路径插值进同一段脚本(AC-SEC-02)。
	shell := pub.execs()[len(pub.execs())-1]
	if shell.cmd[0] != "sh" || shell.cmd[1] != "-c" || !strings.HasPrefix(shell.cmd[2], "set -e\n") {
		t.Errorf("生效指令执行形态异常: %v", shell.cmd)
	}
	// 文件已经铺好了,失败原因只是没生效 —— 状态如实记 failed,但落点上留着原因。
	targets, _ := svc.Targets(ctx, c.ID)
	if targets[0].LastStatus != DeployFailed {
		t.Errorf("落点状态 = %s, 期望 %s", targets[0].LastStatus, DeployFailed)
	}
}

func TestDeployCleansTempWhenChmodFails(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{execFail: func(serverID string, cmd []string) *target.ExecResult {
		if cmd[0] == "chmod" {
			return &target.ExecResult{ExitCode: 1, Stderr: "chmod: Operation not permitted\n"}
		}
		return nil
	}}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()
	seedHost(t, st.DB, "srv-ro")

	c, _ := seedCert(t, svc, "ro.example.com", 17001)
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-ro", CertPath: "/etc/ssl/ro.crt", KeyPath: "/etc/ssl/ro.key",
	}); err != nil {
		t.Fatalf("加落点: %v", err)
	}
	report, err := svc.Deploy(ctx, c.ID)
	if err != nil {
		t.Fatalf("发布: %v", err)
	}
	if report.Failed() != 1 || !strings.Contains(report.Results[0].Error, "设权限失败") {
		t.Fatalf("设权限失败要报出来: %+v", report.Results)
	}
	// 收紧失败 = 这个文件还不能露面:绝不 mv 到位,且要把临时名清掉。
	tmpKey := stagingOf(t, pub, "/etc/ssl/ro.key").dest
	var cleaned bool
	for _, e := range pub.execs() {
		if e.cmd[0] == "mv" {
			t.Errorf("chmod 失败后仍落位: %v", e.cmd)
		}
		if e.cmd[0] == "rm" && e.cmd[2] == tmpKey {
			cleaned = true
		}
	}
	if !cleaned {
		t.Error("chmod 失败后没清掉临时文件")
	}
}

// —— 无落点 / 不存在 ——————————————————————————————————

func TestDeployEmptyAndUnknownCertificate(t *testing.T) {
	st := storetest.Open(t)
	pub := &fakePub{}
	svc := newPubSvc(t, st.DB, pub)
	ctx := context.Background()

	c, _ := seedCert(t, svc, "lonely.example.com", 18001)
	// 没配落点不是错误:报告里就是 0 条,页面据此提示「还没有发布目标」。
	report, err := svc.Deploy(ctx, c.ID)
	if err != nil || len(report.Results) != 0 || report.Failed() != 0 || report.Deployed() != 0 {
		t.Errorf("无落点 = %+v(err=%v), 期望空报告", report, err)
	}
	if len(pub.calls) != 0 {
		t.Errorf("无落点却动了目标机: %+v", pub.calls)
	}
	if _, err := svc.Deploy(ctx, "no-such-cert"); !errors.Is(err, ErrNotFound) {
		t.Errorf("发布不存在的证书 = %v, 期望 %v", err, ErrNotFound)
	}
}

// indexOfCall 返回某次调用在时间线里的位置(用于断言先后)。
func indexOfCall(calls []pubCall, want pubCall) int {
	for i, c := range calls {
		if c.kind == want.kind && c.dest == want.dest && c.serverID == want.serverID {
			return i
		}
	}
	return -1
}

// resultOf / targetOf 按主机取多落点结果。
// 落点表的排序是 created_at + id,而测试里 created_at 被钉死成同一个时刻,先后顺序只剩随机 uuid
// 说话 —— 位置下标会偶发翻车,只能按 ServerID 找。
func resultOf(t *testing.T, r *DeployReport, serverID string) DeployResult {
	t.Helper()
	for _, res := range r.Results {
		if res.ServerID == serverID {
			return res
		}
	}
	t.Fatalf("报告里没有 %s 这个落点: %+v", serverID, r.Results)
	return DeployResult{}
}

func targetOf(t *testing.T, targets []DeployTarget, serverID string) DeployTarget {
	t.Helper()
	for _, tg := range targets {
		if tg.ServerID == serverID {
			return tg
		}
	}
	t.Fatalf("落点清单里没有 %s: %+v", serverID, targets)
	return DeployTarget{}
}
