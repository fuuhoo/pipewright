package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
)

// 远程终端的「命令级」留痕。
//
// 机制:PTY 一起来就往它的 stdin 注入一段提示符钩子(commandHookScript),远端 shell 每出一个
// 提示符就用一条私有 OSC 序列回报「刚才执行了什么」;服务端在 PTY→WS 的字节流上顺路把它读出来
// 落审计(终端输出的字节原样转发,不做改写)。
//
// 为什么记「shell 自己报来的命令」,而不是「用户敲进 WS 的字节」:
//   - 敲进的字节里混着口令。sudo / ssh / passwd 的交互输入走的是同一条 WS→PTY 通道,录下来
//     等于把手输口令写进 append-only 审计表;而 mask.Masker 只替换登记过的密钥值
//     (internal/mask/mask.go:40),猜不到用户当场输了什么。shell 报的是真正执行的那一条,
//     天然不含 echo-off 的输入。
//   - 字节流要处理退格、方向键、Tab 补全,重建出来的行本身不可信。
//   - 换 shell 报的还白送 cwd、退出码和「当时是哪个账号」。
//
// 已知边界(都落在审计行里可见,不假装全知):
//   - 只有 bash/zsh 有提示符钩子。dash/ash 之类 POSIX shell 一条都报不上来,会话结束的汇总行
//     记 hook=silent,让「这台机没录到」这件事本身留痕(而不是静默地什么都没记)。
//   - 用户可以在会话里 unset PROMPT_COMMAND / 覆盖 precmd,或 `set +o history` 让 fc 取不到
//     那一行,来回避命令回报。审计只诚实记录平台看到的东西,它不是控制手段 —— 真要约束得住,
//     靠的是远端账号与 sudoers,不是这里。
//   - 命令行原样入库(用户把密钥写进命令参数也就进去了),长度按 cmdTextMax 截断。

const (
	// cmdOscPrefix 是回报用的私有 OSC 序列前缀。5522 不匹配任何终端模拟器的既有约定,
	// 前端 xterm 遇到不认识的 OSC 直接丢弃(与既有的 OSC 7 cwd 上报同一条通道)。
	cmdOscPrefix = "\x1b]5522;"
	// cmdOscBel / cmdOscSt 是 OSC 的两种终止符:钩子只发 BEL,解析两种都认。
	cmdOscBel = "\x07"
	cmdOscSt  = "\x1b\\"
	// cmdFieldSep 是载荷内部分隔字段用 Unit Separator(base64 之后才拼进序列,不会与框架字符撞)。
	cmdFieldSep = "\x1f"

	// cmdPayloadMax 是一条回报载荷的上限:超出说明流里混进了脏数据,丢掉而不是撑爆缓冲。
	cmdPayloadMax = 8 << 10
	// cmdTextMax 是落库命令行长度上限(超出截断并打 truncated 标记)。
	cmdTextMax = 1000
	// cmdPerSessionMax 是一场会话最多记多少条命令,防跑飞的循环把审计表刷爆。
	cmdPerSessionMax = 2000
	// cmdSentinel 是注入脚本自身的函数名。第一条回报必然是它自己那一行 —— 它证明钩子活着,
	// 但它不是用户敲的命令。
	cmdSentinel = "__pw_report"

	// auditDetachedTimeout 是「请求已经结束/hijack 之后」写审计的超时。PTY 会话的生命周期
	// 独立于单次 HTTP 请求(container_terminal.go 头注释),所以这里不能用 r.Context()。
	auditDetachedTimeout = 3 * time.Second
)

// commandHookScript 是注入远端 shell 的那一行钩子。
//
// 一次注入干两件事:①沿用既有的 OSC 7 上报 cwd(远程弹窗的文件面板靠它联动,见
// web/src/lib/serverFs.ts 的 pathFromOsc7);②新增 OSC 5522 回报「刚才那条命令 / 当时的目录 /
// 退出码 / 当时是哪个账号」。
//
// 几个刻意的写法:
//   - 必须是**一行**并自带 \r:PTY 是行编辑,多条注入会被用户中途敲的字拆开。
//   - bash 分支先把原 PROMPT_COMMAND 存进 __pw_prev,再在钩子末尾 eval 回去,并原样返回上一条
//     命令的退出码 —— 直接覆盖会静默废掉用户自己的提示符钩子(macOS bash 3.2 实测如此)。
//     zsh 分支定义 precmd:同名函数只能有一个,用户自己的 precmd 会被替换(与覆盖前行为一致,
//     不做链式保留 —— zsh 的 $functions 取值写法太碎,不值当)。
//   - 载荷走 base64:命令行可能是任意 UTF-8(中文路径很常见),而 base64 字母表不含
//     ESC/BEL/US,框架字符绝不会被内容伪造。
//   - 「刚执行的那条」按 shell 分开取(pty 实测,不是想当然):bash 3.2 在 PROMPT_COMMAND 里
//     `fc -ln -1` 拿到的是**上上一条**(当前事件号已经往前走了),`history 1` 才是刚跑完那条;
//     zsh 反过来 —— `fc -ln -1` 正是刚跑完那条,而它的 `history` 是 `fc -l` 的别名,取 1 会整段
//     倒出来。HISTTIMEFORMAT 在子 shell 里清掉,否则用户设了它时首行是 `#时间戳`。
//   - 已知会漏/滞后:远端把命令排除在历史之外时(HISTCONTROL=ignorespace、HISTIGNORE),报上来的
//     可能是上一条。审计如实记 shell 报的东西,不假装全知。
//   - 绝不能出现裸 `!`:交互式 zsh 会对它做**历史展开**,整行注入直接报 `event not found` 而不
//     执行(实测),钩子于是静默失效。所以去掉 `history` 行首编号用 sed,而不是
//     `${h%%[![:space:]]*}` 这类带 `!` 的写法。
func commandHookScript() string {
	cmd := `__pw_cmd(){ if [ -n "${BASH_VERSION:-}" ]; then HISTTIMEFORMAT='' history 1 2>/dev/null | sed 's/^ *[0-9][0-9]*  //'; else fc -ln -1 2>/dev/null; fi; }; `
	report := `__pw_report(){ local e=$? c u; c=$(__pw_cmd); u=${USER:-$(id -un 2>/dev/null)}; ` +
		`printf '\033]7;file://%s%s\007' "${HOSTNAME:-local}" "$PWD"; ` +
		`printf '\033]5522;%s\007' "$(printf '%s\037%s\037%s\037%s' "$u" "$PWD" "${c//$'\n'/ }" "$e" | base64 | tr -d '\n')"; ` +
		`[ -n "${__pw_prev:-}" ] && eval "$__pw_prev"; return $e; }; `
	return "if [ -n \"${BASH_VERSION:-}\" ] || [ -n \"${ZSH_VERSION:-}\" ]; then " +
		`__pw_prev="${PROMPT_COMMAND:-}"; ` + cmd + report +
		// 结尾必须是**真** CR(整段是喂给 PTY 的一行输入);写成反斜杠+r 的话 shell 收到的是
		// 普通字符,行永远不提交,钩子静默装不上 —— 真机 pty 实测踩过。
		"if [ -n \"${BASH_VERSION:-}\" ]; then PROMPT_COMMAND=__pw_report; else precmd(){ __pw_report; }; fi; fi\r"
}

// terminalCommand 是一条从回报里解出来的命令记录。
type terminalCommand struct {
	User      string // 当时生效的 OS 账号(su/sudo -s 之后与登录用户可能不同)
	Cwd       string
	Cmd       string
	Exit      int  // 退出码;-1 = 回报里没给或解不出来
	Truncated bool // 命令行超过 cmdTextMax,已截断
}

// decodeCommandRecord 解一条回报载荷。ok=false 表示载荷脏(空/超长/非法 base64/字段不够),
// self=true 表示这是注入脚本自己那一行 —— 调用方据此区分「钩子活着」与「用户敲了命令」。
func decodeCommandRecord(payload string) (c terminalCommand, ok, self bool) {
	p := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	if p == "" || len(p) > cmdPayloadMax {
		return c, false, false
	}
	raw, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		return c, false, false
	}
	fields := strings.Split(string(raw), cmdFieldSep)
	if len(fields) < 4 {
		return c, false, false
	}
	c.User = strings.TrimSpace(fields[0])
	c.Cwd = strings.TrimSpace(fields[1])
	c.Cmd = strings.TrimSpace(strings.ReplaceAll(fields[2], "\n", " "))
	if exit, perr := strconv.Atoi(strings.TrimSpace(fields[3])); perr == nil {
		c.Exit = exit
	} else {
		c.Exit = -1
	}
	// 钩子取不到命令(远端没 sed、或历史被 HISTIGNORE 排除)时报空串 —— 那不是证据,别落一行
	// 「谁在 /etc 里执行了〈空〉」。钩子是否活着由 self 记录与汇总行的 hook 字段说明。
	if c.Cmd == "" {
		return c, false, false
	}
	if strings.Contains(c.Cmd, cmdSentinel) {
		return c, true, true
	}
	if cut, trimmed := cutUTF8(c.Cmd, cmdTextMax); trimmed {
		c.Cmd, c.Truncated = cut, true
	}
	return c, true, false
}

// cutUTF8 按字节上限截断,并回退到完整 rune 边界(截半个汉字是给自己埋取证歧义)。
func cutUTF8(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	b := s[:max]
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r == utf8.RuneError && size <= 1 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b, true
}

// terminalCommandRecorder 在 PTY→WS 的字节流上顺路读回报。
//
// 它只**读**不改写:转发给前端的字节一个不动。回报可能横跨两次 Read,所以留一小截待定尾巴;
// 尾巴有界(cmdPayloadMax),不会出现「一直没等到终止符于是攒到 OOM」。
type terminalCommandRecorder struct {
	mu      sync.Mutex
	carry   []byte
	count   int
	capped  bool
	hooked  bool
	onWrite func(terminalCommand)
}

func newTerminalCommandRecorder(onWrite func(terminalCommand)) *terminalCommandRecorder {
	return &terminalCommandRecorder{onWrite: onWrite}
}

// observe 吃一块 PTY 输出。由 PTY→WS 那个 goroutine 单线程调用。
func (r *terminalCommandRecorder) observe(p []byte) {
	r.mu.Lock()
	r.carry = append(r.carry, p...)
	marker := []byte(cmdOscPrefix)
	for {
		i := bytes.Index(r.carry, marker)
		if i < 0 {
			// 没有起始符:只保留可能是起始符尾巴的那点字节,其余丢掉。
			keep := len(marker) - 1
			if len(r.carry) > keep {
				r.carry = append(r.carry[:0], r.carry[len(r.carry)-keep:]...)
			}
			break
		}
		body := r.carry[i+len(marker):]
		end := bytes.IndexByte(body, cmdOscBel[0])
		if st := bytes.Index(body, []byte(cmdOscSt)); st >= 0 && (end < 0 || st < end) {
			end = st // 用 ST 收尾的 shell:载荷在 ESC 前
		}
		if end < 0 {
			if len(body) > cmdPayloadMax {
				// 等不到终止符的脏候选:跳过这个起始符继续找,别让流卡死。
				r.carry = body
				continue
			}
			r.carry = r.carry[i:] // 留着半条,等下一块
			break
		}
		payload := string(body[:end])
		r.carry = append(r.carry[:0], r.carry[i+len(marker)+end+1:]...)
		c, ok, self := decodeCommandRecord(payload)
		if !ok {
			continue
		}
		r.hooked = true
		if self {
			continue // 注入脚本自己那一行
		}
		if r.count >= cmdPerSessionMax {
			r.capped = true
			continue
		}
		r.count++
		if r.onWrite != nil {
			// 锁外回调:写审计是 I/O,不占着扫描状态的锁。
			r.mu.Unlock()
			r.onWrite(c)
			r.mu.Lock()
		}
	}
	r.mu.Unlock()
}

// stats 返回已记录条数、是否撞到上限、钩子是否报过任何东西。
func (r *terminalCommandRecorder) stats() (count int, capped, hooked bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count, r.capped, r.hooked
}

// terminalAudit 是一场终端会话的审计落点:身份在这里定好一次,后续每行都带着同一串上下文。
type terminalAudit struct {
	rec   audit.Recorder
	actor string // 真实登录用户(不再是硬编码的 "admin")
	ip    string

	sessionID string
	serverID  string
	serverNm  string
	osUser    string // 这台机注册的 SSH 登录用户(target.Server.User)
	kind      string // host | container
	container string
	shell     string

	started time.Time
}

// newTerminalAudit 从请求派生 actor/IP。注意:hijack 之后 r.Context() 不可靠,
// 所以只在这里读请求,之后所有写入都用独立 ctx(见 write)。
func newTerminalAudit(r *http.Request, ac auth.Authenticator, rec audit.Recorder, serverID, serverName, osUser, kind, container, shell string) *terminalAudit {
	return &terminalAudit{
		rec:       rec,
		actor:     actorFromRequest(r, ac),
		ip:        clientIP(r),
		sessionID: uuid.NewString(),
		serverID:  serverID,
		serverNm:  serverName,
		osUser:    osUser,
		kind:      kind,
		container: container,
		shell:     shell,
		started:   time.Now(),
	}
}

// baseDetail 是每一行都带上的上下文(会话 id 让命令能串回同一次连接)。
func (a *terminalAudit) baseDetail() map[string]any {
	d := map[string]any{
		"session":    a.sessionID,
		"kind":       a.kind,
		"shell":      a.shell,
		"loginUser":  a.osUser,
		"serverName": a.serverNm,
	}
	if a.container != "" {
		d["containerId"] = a.container
	}
	return d
}

func (a *terminalAudit) write(e audit.Entry) {
	if a.rec == nil {
		return
	}
	e.Actor = a.actor
	e.IP = a.ip
	ctx, cancel := context.WithTimeout(context.Background(), auditDetachedTimeout)
	defer cancel()
	recordAudit(ctx, a.rec, e)
}

// start 在 PTY 建立后落一条「谁连上了哪台机」。
func (a *terminalAudit) start(action string) {
	d := a.baseDetail()
	d["op"] = "start"
	a.write(audit.Entry{
		Action:     action,
		TargetType: audit.TargetServer,
		TargetID:   a.serverID,
		Detail:     d,
	})
}

// command 落一条「这场会话里执行掉的一条命令」。
func (a *terminalAudit) command(c terminalCommand) {
	d := a.baseDetail()
	d["cmd"] = c.Cmd
	d["cwd"] = c.Cwd
	d["exit"] = c.Exit
	if c.User != "" {
		d["user"] = c.User
	}
	if c.Truncated {
		d["truncated"] = true
	}
	a.write(audit.Entry{
		Action:     audit.ActionServerCommand,
		TargetType: audit.TargetServer,
		TargetID:   a.serverID,
		Detail:     d,
	})
}

// end 在会话收尾时落一条汇总:时长、命令条数、钩子到底有没有在工作。
// hook=silent 是有意的 —— 它让「这台机的 shell 没钩子(或被用户拆了)」在审计里可见,
// 而不是静默地什么都没记。
func (a *terminalAudit) end(action string, rec *terminalCommandRecorder) {
	count, capped, hooked := rec.stats()
	d := a.baseDetail()
	d["op"] = "end"
	d["ms"] = time.Since(a.started).Milliseconds()
	d["commands"] = count
	d["hook"] = "silent"
	if hooked {
		d["hook"] = "ok"
	}
	if capped {
		d["capped"] = true
	}
	a.write(audit.Entry{
		Action:     action,
		TargetType: audit.TargetServer,
		TargetID:   a.serverID,
		Detail:     d,
	})
}
