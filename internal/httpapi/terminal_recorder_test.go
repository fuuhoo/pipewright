package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/huangchengsir/pipewright/internal/audit"
)

// --- 注入脚本的形状约束(这几条都是真机 pty 踩出来的,不是审美) ---

func TestCommandHookScriptShape(t *testing.T) {
	s := commandHookScript()
	// ① 结尾必须是真 CR:反斜杠+r 会让 shell 永远不提交这一行(钩子静默装不上)。
	if !strings.HasSuffix(s, "\r") {
		t.Fatalf("注入脚本必须以真 CR 结尾, got %q", s[len(s)-8:])
	}
	if strings.Contains(s, "\\r") {
		t.Fatalf("不该出现字面 \\r: %q", s)
	}
	// ② 只有一行:PTY 是行编辑,多行注入会被用户中途敲的字拆开。
	if strings.Contains(s, "\n") {
		t.Fatalf("注入脚本必须是单行")
	}
	// ③ 不能有裸 `!`:交互式 zsh 会对它做历史展开,整行直接 event not found。
	//    (脚本里唯一的 `!` 允许出现在单引号内 —— zsh 在单引号里不展开。本脚本刻意一个都不用。)
	if strings.Contains(s, "!") {
		t.Fatalf("注入脚本不该含 `!`(zsh 历史展开会吃掉整行): %q", s)
	}
	// ④ 两个 shell 各取「刚执行的那条」的写法都在,且都挂了提示符钩子。
	for _, want := range []string{"PROMPT_COMMAND=__pw_report", "precmd(){ __pw_report; }",
		"history 1", "fc -ln -1", "__pw_prev", `printf '\033]5522;%s\007'`} {
		if !strings.Contains(s, want) {
			t.Fatalf("注入脚本缺 %q", want)
		}
	}
	if !strings.Contains(s, cmdOscPrefix) && !strings.Contains(s, `\033]5522;`) {
		t.Fatalf("注入脚本没有回报序列")
	}
}

// --- 回报解码 ---

// oscRecordBytes 拼一条远端回报(与钩子的编码方式一致:字段用 US 分隔,整段 base64)。
func oscRecordBytes(user, cwd, cmd string, exit int) []byte {
	raw := user + cmdFieldSep + cwd + cmdFieldSep + cmd + cmdFieldSep + strconv.Itoa(exit)
	return []byte(cmdOscPrefix + base64.StdEncoding.EncodeToString([]byte(raw)) + cmdOscBel)
}

func TestDecodeCommandRecord(t *testing.T) {
	raw := "deploy" + cmdFieldSep + "/srv/app" + cmdFieldSep + "kubectl rollout status deploy/web" + cmdFieldSep + "0"
	c, ok, self := decodeCommandRecord(base64.StdEncoding.EncodeToString([]byte(raw)))
	if !ok || self {
		t.Fatalf("ok=%v self=%v, want true/false", ok, self)
	}
	if c.User != "deploy" || c.Cwd != "/srv/app" || c.Cmd != "kubectl rollout status deploy/web" || c.Exit != 0 {
		t.Fatalf("解出来不对: %+v", c)
	}

	// 钩子自己那一行:self=true(调用方据此知道「钩子活着」但不把它当用户命令)。
	if _, ok, self := decodeCommandRecord(encodeCmd("__pw_report", 0)); !ok || !self {
		t.Fatalf("sentinel 应判为 self, ok=%v self=%v", ok, self)
	}

	// 退出码解不出来 → -1(未知),不是 0(那是「成功」的假证据)。
	if c, ok, _ := decodeCommandRecord(encodeCmdRaw("u", "/tmp", "ls", "nan")); !ok || c.Exit != -1 {
		t.Fatalf("exit 应为 -1, got %+v ok=%v", c, ok)
	}

	// 脏输入一律 ok=false:非法 base64、字段不够、空命令、超长载荷。
	for name, p := range map[string]string{
		"非法 base64": "!!!not-base64!!!",
		"字段不够":      base64.StdEncoding.EncodeToString([]byte("u\x1f/tmp\x1fls")),
		"空命令":       encodeCmd("", 0),
		"空载荷":       "",
	} {
		if _, ok, _ := decodeCommandRecord(p); ok {
			t.Fatalf("%s 应解不开", name)
		}
	}
	if _, ok, _ := decodeCommandRecord(strings.Repeat("A", cmdPayloadMax+10)); ok {
		t.Fatalf("超长载荷应丢掉")
	}

	// 载荷里混进换行/空格(base64 被远端折行)不影响解码。
	lined := ""
	enc := encodeCmd("systemctl status nginx", 3)
	for i, r := range enc {
		if i > 0 && i%40 == 0 {
			lined += "\r\n"
		}
		lined += string(r)
	}
	if c, ok, _ := decodeCommandRecord(lined); !ok || c.Cmd != "systemctl status nginx" || c.Exit != 3 {
		t.Fatalf("折行载荷解不出: %+v ok=%v", c, ok)
	}

	// 超长命令行:截到 cmdTextMax 并打 truncated 标记(取证要的是「有这么长的命令 + 前缀」,不是整坨)。
	long := strings.Repeat("x", cmdTextMax+500)
	c, ok, self = decodeCommandRecord(encodeCmd(long, 0))
	if !ok || self {
		t.Fatalf("超长命令应能解, ok=%v self=%v", ok, self)
	}
	if len(c.Cmd) > cmdTextMax || !c.Truncated {
		t.Fatalf("截断不对: len=%d truncated=%v", len(c.Cmd), c.Truncated)
	}

	// 截断必须落在完整 rune 边界上(半个汉字是给取证留歧义)。
	wide := strings.Repeat("海", (cmdTextMax/3)+10)
	c, ok, _ = decodeCommandRecord(encodeCmd(wide, 0))
	if !ok {
		t.Fatalf("宽字符命令解不开")
	}
	if !strings.HasSuffix(c.Cmd, "海") || !c.Truncated {
		t.Fatalf("截断没落在 rune 边界: tail=%q", c.Cmd[len(c.Cmd)-3:])
	}
}

func encodeCmd(cmd string, exit int) string {
	return encodeCmdRaw("op", "/srv", cmd, strconv.Itoa(exit))
}

func encodeCmdRaw(user, cwd, cmd, exit string) string {
	raw := user + cmdFieldSep + cwd + cmdFieldSep + cmd + cmdFieldSep + exit
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// --- 字节流扫描 ---

func TestTerminalCommandRecorderObserve(t *testing.T) {
	var got []terminalCommand
	r := newTerminalCommandRecorder(func(c terminalCommand) { got = append(got, c) })

	// 一条回报横跨三次 Read(PTY 分块是常态)。
	full := oscRecordBytes("deploy", "/var/log", "tail -f app.log", 0)
	mid := len(full) / 2
	r.observe(full[:3])
	r.observe(full[3:mid])
	r.observe(full[mid:])
	if len(got) != 1 || got[0].Cmd != "tail -f app.log" {
		t.Fatalf("跨块回报没收齐: %+v", got)
	}

	// 同一条 PTY 块里塞两条回报 + 夹杂普通输出。
	chunk := append([]byte("\x1b[?2004hpwtest$ "), oscRecordBytes("u", "/a", "ls", 0)...)
	chunk = append(chunk, []byte("a.txt\r\n")...)
	chunk = append(chunk, oscRecordBytes("u", "/a", "cd a.txt", 0)...)
	r.observe(chunk)
	if len(got) != 3 || got[2].Cmd != "cd a.txt" {
		t.Fatalf("同块多回报没收齐: %+v", got)
	}

	// 自记录(钩子自己那一行)只证明钩子活着,不进命令列表。
	before := len(got)
	r.observe(oscRecordBytes("u", "/a", "__pw_report 被 echo 出来了", 0))
	if len(got) != before {
		t.Fatalf("self 记录不该写进命令")
	}
	if count, capped, hooked := r.stats(); count != 3 || capped || !hooked {
		t.Fatalf("stats = %d,%v,%v, want 3,false,true", count, capped, hooked)
	}

	// ST(ESC \)收尾也认。
	before = len(got)
	st := []byte(cmdOscPrefix + base64.StdEncoding.EncodeToString([]byte("u"+cmdFieldSep+"/x"+cmdFieldSep+"uptime"+cmdFieldSep+"1")) + cmdOscSt)
	r.observe(st)
	if len(got) != before+1 || got[len(got)-1].Exit != 1 {
		t.Fatalf("ST 收尾没解出来: %+v", got[len(got)-1:])
	}

	// 脏数据:有起始符却永远等不到终止符 → 丢掉这条,后续正常回报照样收。
	r.observe([]byte(cmdOscPrefix + strings.Repeat("A", cmdPayloadMax+64)))
	before = len(got)
	r.observe(oscRecordBytes("u", "/y", "whoami", 0))
	if len(got) != before+1 || got[len(got)-1].Cmd != "whoami" {
		t.Fatalf("脏候选之后的回报丢了: %+v", got[len(got)-1:])
	}

	// 非法 base64 的回报不炸、也不记账。
	before = len(got)
	r.observe([]byte(cmdOscPrefix + "%%%bad%%%"))
	if len(got) != before {
		t.Fatalf("非法回报不该记账")
	}
}

func TestTerminalCommandRecorderCapsPerSession(t *testing.T) {
	var n int
	r := newTerminalCommandRecorder(func(c terminalCommand) { n++ })
	for i := 0; i < cmdPerSessionMax+5; i++ {
		r.observe(oscRecordBytes("u", "/tmp", "date", 0))
	}
	count, capped, _ := r.stats()
	if count != cmdPerSessionMax || n != cmdPerSessionMax || !capped {
		t.Fatalf("上限不对: stats=%d 回调=%d capped=%v", count, n, capped)
	}
}

func TestTerminalCommandRecorderNilCallbackSafe(t *testing.T) {
	r := newTerminalCommandRecorder(nil)
	r.observe(oscRecordBytes("u", "/tmp", "uptime", 0)) // 不 panic 即通过
	if count, _, hooked := r.stats(); count != 1 || !hooked {
		t.Fatalf("nil 回调仍要记账: %d %v", count, hooked)
	}
}

func TestCutUTF8(t *testing.T) {
	if s, trimmed := cutUTF8("abc", 10); s != "abc" || trimmed {
		t.Fatalf("短串不该被截: %q %v", s, trimmed)
	}
	s, trimmed := cutUTF8("海海海", 4)
	if !trimmed || s != "海" {
		t.Fatalf("应回退到完整 rune: %q", s)
	}
}

// --- 端到端:注入钩子 → 远端回报 → 命令级审计落库(主机终端) ---

// detailInt 取 detail 里的整数:JSON 解出来是 float64,写进去的是 int,两种都认。
func detailInt(t *testing.T, d map[string]any, key string) int {
	t.Helper()
	switch v := d[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		t.Fatalf("detail[%q] 不是整数: %T", key, d[key])
		return 0
	}
}

// auditWait 轮询审计直到 cond 满足(命令行是泵 goroutine 异步写的,不能假设立刻可见)。
func auditWait(t *testing.T, rec audit.Recorder, action string, cond func([]auditRecord) bool) []auditRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := rec.List(context.Background(), audit.ListFilter{Action: action})
		if err != nil {
			t.Fatalf("audit list: %v", err)
		}
		if cond(res.Entries) {
			return res.Entries
		}
		if time.Now().After(deadline) {
			return res.Entries
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type auditRecord = audit.Record

// TestServerTerminalCommandAudit 走真实 WS:验「钩子注入到 PTY、回报被解出、命令逐条落审计、
// 会话开合两行能按 session 串起来、actor 是真实登录用户」。
func TestServerTerminalCommandAudit(t *testing.T) {
	dialer := &fakeInteractiveDialer{}
	srv, client, csrf, rec := setupTerminalAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hdr := http.Header{}
	for _, ck := range client.Jar.Cookies(mustParseURL(t, srv.URL)) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL)+"/api/servers/"+id+"/terminal", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("WS dial: %v", err)
	}

	// ① 钩子必须由**服务端**注入(sending 任何输入前先注入)。memSession 把写入原样回显,
	//    所以回显里出现 __pw_report 就等于证明了注入到位。
	if got := readWSUntil(t, ctx, conn, cmdSentinel); !strings.Contains(got, cmdSentinel) {
		t.Fatalf("没看到注入的钩子: %q", got)
	}

	sess := dialer.session()
	if sess == nil {
		t.Fatalf("会话未建立")
	}
	// ② 远端回报一条真命令 + 一条钩子自身(后者必须被当自记录丢掉)。
	sess.pushRemote(oscRecordBytes("deploy", "/srv/app", "systemctl reload nginx", 0))
	sess.pushRemote(oscRecordBytes("deploy", "/srv/app", "__pw_report 自己那一行", 0))

	cmds := auditWait(t, rec, audit.ActionServerCommand, func(e []auditRecord) bool { return len(e) >= 1 })
	if len(cmds) != 1 {
		t.Fatalf("应只有 1 条 server_command(自记录不入账), got %d: %+v", len(cmds), cmds)
	}
	c := cmds[0]
	if c.Action != audit.ActionServerCommand || c.TargetType != audit.TargetServer || c.TargetID != id {
		t.Fatalf("命令行归属不对: %+v", c)
	}
	if c.Actor != "admin:admin" {
		t.Fatalf("actor 应是真实登录用户, got %q", c.Actor)
	}
	for key, want := range map[string]string{
		"cmd": "systemctl reload nginx", "cwd": "/srv/app", "user": "deploy",
		"loginUser": "deploy", "serverName": "web-1", "kind": "host",
	} {
		if got, _ := c.Detail[key].(string); got != want {
			t.Fatalf("detail[%q] = %q, want %q", key, got, want)
		}
	}
	if detailInt(t, c.Detail, "exit") != 0 {
		t.Fatalf("退出码应记 0: %+v", c.Detail)
	}
	sessionID, _ := c.Detail["session"].(string)
	if sessionID == "" {
		t.Fatalf("命令行必须带 session 才能串起一次连接: %+v", c.Detail)
	}

	// ③ 开终端那行也在,且与命令行同一个 session。
	starts := auditWait(t, rec, audit.ActionServerTerminal, func(e []auditRecord) bool { return len(e) >= 1 })
	start := detailByOp(t, starts, "start")
	if s, _ := start.Detail["session"].(string); s != sessionID {
		t.Fatalf("start 与命令行 session 不同: %q vs %q", s, sessionID)
	}
	if start.Actor != "admin:admin" {
		t.Fatalf("start actor = %q", start.Actor)
	}

	// ④ 收尾:关 WS → end 行带命令条数与钩子状态。
	_ = conn.Close(websocket.StatusNormalClosure, "")
	ends := auditWait(t, rec, audit.ActionServerTerminal, func(e []auditRecord) bool {
		return detailByOp(t, e, "end") != nil
	})
	end := detailByOp(t, ends, "end")
	if end == nil {
		t.Fatalf("缺 end 汇总行: %+v", ends)
	}
	if got, _ := end.Detail["hook"].(string); got != "ok" {
		t.Fatalf("收到过回报,end 应记 hook=ok, got %q", got)
	}
	if got := detailInt(t, end.Detail, "commands"); got != 1 {
		t.Fatalf("end.commands = %d, want 1", got)
	}
	if s, _ := end.Detail["session"].(string); s != sessionID {
		t.Fatalf("end 与命令行 session 不同")
	}
}

// detailByOp 在同类 action 的条目里按 detail.op 找一条(start / end)。
func detailByOp(t *testing.T, entries []auditRecord, op string) *auditRecord {
	t.Helper()
	for i := range entries {
		if got, _ := entries[i].Detail["op"].(string); got == op {
			return &entries[i]
		}
	}
	return nil
}

// TestServerTerminalHookSilent 验「这台机的 shell 没有提示符钩子」时不静默失忆:
// 会话里一条回报都没有 → 汇总行必须记 hook=silent、commands=0。
func TestServerTerminalHookSilent(t *testing.T) {
	dialer := &fakeInteractiveDialer{}
	srv, client, csrf, rec := setupTerminalAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hdr := http.Header{}
	for _, ck := range client.Jar.Cookies(mustParseURL(t, srv.URL)) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL)+"/api/servers/"+id+"/terminal?shell=/bin/sh", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("WS dial: %v", err)
	}
	// 先跑一个回显往返,确保会话已建立(handler 是「起会话 → 注钩子 → 双向泵」)。
	if err := conn.Write(ctx, websocket.MessageText, []byte("pwd\n")); err != nil {
		t.Fatalf("write input: %v", err)
	}
	_ = readWSUntil(t, ctx, conn, "pwd")
	// 只喂普通输出,不喂任何回报(等价 dash/ash:钩子装上了但 shell 不报)。
	sess := dialer.session()
	if sess == nil {
		t.Fatalf("会话未建立")
	}
	sess.pushRemote([]byte("ordinary stdout\r\n"))
	_ = conn.Close(websocket.StatusNormalClosure, "")

	ends := auditWait(t, rec, audit.ActionServerTerminal, func(e []auditRecord) bool {
		return detailByOp(t, e, "end") != nil
	})
	end := detailByOp(t, ends, "end")
	if got, _ := end.Detail["hook"].(string); got != "silent" {
		t.Fatalf("没有任何回报时 end 应记 hook=silent, got %q", got)
	}
	if got := detailInt(t, end.Detail, "commands"); got != 0 {
		t.Fatalf("commands = %d, want 0", got)
	}
	if _, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionServerCommand}); err != nil {
		t.Fatalf("audit list: %v", err)
	}
}

// TestContainerTerminalCommandAudit 验容器那条腿:钩子同样由服务端注入,命令行按 container
// 归属落账(kind=container + containerId),开合两行走 container_terminal。
func TestContainerTerminalCommandAudit(t *testing.T) {
	dialer := &fakeInteractiveDialer{}
	srv, client, csrf, rec := setupTerminalAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hdr := http.Header{}
	for _, ck := range client.Jar.Cookies(mustParseURL(t, srv.URL)) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL)+"/api/servers/"+id+"/containers/myapp/terminal?shell=/bin/bash", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("WS dial: %v", err)
	}

	if got := readWSUntil(t, ctx, conn, cmdSentinel); !strings.Contains(got, cmdSentinel) {
		t.Fatalf("没看到注入的钩子: %q", got)
	}
	sess := dialer.session()
	if sess == nil {
		t.Fatalf("会话未建立")
	}
	sess.pushRemote(oscRecordBytes("root", "/app", "kill -HUP 1", 0))

	cmds := auditWait(t, rec, audit.ActionServerCommand, func(e []auditRecord) bool { return len(e) >= 1 })
	if len(cmds) != 1 {
		t.Fatalf("应只有 1 条 server_command, got %d: %+v", len(cmds), cmds)
	}
	for key, want := range map[string]string{
		"cmd": "kill -HUP 1", "kind": "container", "containerId": "myapp", "shell": "/bin/bash",
	} {
		if got, _ := cmds[0].Detail[key].(string); got != want {
			t.Fatalf("detail[%q] = %q, want %q", key, got, want)
		}
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
	ends := auditWait(t, rec, audit.ActionContainerTerminal, func(e []auditRecord) bool {
		return detailByOp(t, e, "end") != nil
	})
	end := detailByOp(t, ends, "end")
	if end == nil {
		t.Fatalf("缺 container_terminal 的 end 汇总行: %+v", ends)
	}
	if got, _ := end.Detail["hook"].(string); got != "ok" {
		t.Fatalf("end.hook = %q, want ok", got)
	}
	if got := detailInt(t, end.Detail, "commands"); got != 1 {
		t.Fatalf("end.commands = %d, want 1", got)
	}
	if s, _ := end.Detail["session"].(string); s != cmds[0].Detail["session"] {
		t.Fatalf("命令行与 end 的 session 不同")
	}
}
