package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/i18n"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/go-chi/chi/v5"
)

// Story 6.4(FR-18):容器内交互终端(WS ↔ SSH → `docker exec -it`)。
//
// 经界面进入目标机上某容器,执行交互式命令。架构约定:**WebSocket 仅**用于交互式容器终端
// (其余实时流走 SSE)。本端点是平台唯一的 WS 升级点。
//
// 安全(FR-18 / AC-SEC-02):
//   - 鉴权:WS 升级是 GET,经 /api 组的 requireAuth 校验会话 cookie(未登录 → 401,升级失败)。
//     CSRF 对 GET 豁免,改以**同源(Origin)校验**防跨站 WS 劫持(见 originPatterns)。
//   - 命令 array 化:`docker exec -it <containerID> <shell>`,containerID 经严格白名单
//     (首字符 [\w] 防 flag 注入、无 shell 元字符),shell 经枚举白名单;未指定 shell 时不改拼命令,
//     而是在容器里跑一段写死的挑选脚本(bash 优先、/bin/sh 兜底),候选路径无一处来自请求。
//     经 target.ExecInteractive 各参数 shell 转义后执行,绝不拼 shell 字符串。
//   - 凭据经 vault 即用即弃,绝不入 WS 帧/日志。
//   - 审计:开终端是高危操作,握手成功(SSH PTY 建立)后写 append-only 审计(谁/哪台/哪容器/何时)。

const (
	// wsReadLimit 限制单条 WS 消息大小(防超大输入帧;终端输入本就细碎)。
	wsReadLimit = 1 << 20 // 1 MiB
	// ptyReadChunk 是从 SSH PTY 读出转发到 WS 的缓冲块大小。
	ptyReadChunk = 32 * 1024
)

// reContainerID 校验 docker 容器名/ID:首字符强制 [\w](禁 `-` 开头防 flag 注入),
// 其余为字母数字下划线 + 点/-(无任何 shell 元字符)。与 6-2/6-3 风格一致。
var reContainerID = regexp.MustCompile(`^[\w][\w.-]*$`)

// allowedShells 是允许进入容器的交互 shell 白名单(绝对路径;防任意命令注入到 exec 参数)。
var allowedShells = map[string]struct{}{
	"/bin/sh":       {},
	"/bin/bash":     {},
	"/bin/ash":      {},
	"/bin/zsh":      {},
	"/usr/bin/sh":   {},
	"/usr/bin/bash": {},
	"/usr/bin/zsh":  {},
	"sh":            {},
	"bash":          {},
}

// validateContainerTarget 校验容器 ID 与 shell。shell 空串 = 交给 buildContainerExecCmd 在容器里
// 现挑一个可用的(bash 优先,没有再退 /bin/sh),而不是写死 /bin/sh —— 装了 bash 的容器才有补全、
// 历史与可用提示符钩子。
func validateContainerTarget(containerID, shell string) (string, error) {
	if containerID == "" {
		return "", errors.New("容器 ID 不能为空")
	}
	if len(containerID) > 128 {
		return "", errors.New("容器 ID 过长")
	}
	if !reContainerID.MatchString(containerID) {
		return "", errors.New("非法容器 ID(仅允许字母数字与 . _ -,且不得以 - 开头)")
	}
	if shell == "" {
		return "", nil
	}
	if _, ok := allowedShells[shell]; !ok {
		return "", errors.New("非法 shell(不在允许白名单内)")
	}
	return shell, nil
}

// autoContainerShellArgv 是未指定 shell 时在**容器内**挑 shell 的命令:bash 优先,容器没装
// bash 才退到 /bin/sh(= 本来的写死默认,不降级)。
//
// 候选路径全部写死、不含用户输入,也不含单引号,因此过 quoteArgs 后仍是**一个** argv。
// 用 exec 起 shell:PTY 的前台进程就是 shell 本身,中间不剩一层 sh。
// 起手解释器取 /bin/sh —— 它比 bash 普遍得多(Alpine 上就是 ash)。
func autoContainerShellArgv() []string {
	return []string{"/bin/sh", "-c",
		`for s in bash /bin/bash /usr/bin/bash; do ` +
			`command -v "$s" >/dev/null 2>&1 && exec "$s"; done; exec /bin/sh`}
}

// buildContainerExecCmd 构造 `docker exec -it <containerID> <shell>` 命令 array(不拼 shell)。
// 调用前须先过 validateContainerTarget;shell 为空即「自动」,追加的是上面那段常量挑选脚本。
func buildContainerExecCmd(containerID, shell string) []string {
	if shell == "" {
		return append([]string{"docker", "exec", "-it", containerID}, autoContainerShellArgv()...)
	}
	return []string{"docker", "exec", "-it", containerID, shell}
}

// makeContainerTerminalHandler 返回 GET /api/servers/{id}/containers/{containerId}/terminal handler。
// **唯一的 WS 升级点**。已由 /api 组套了 requireAuth(未登录 → 401,不会升级)。本 handler 再做
// 同源(Origin)校验,然后 WS↔SSH(PTY)双向泵:WS 文本帧 → stdin;PTY 输出 → WS 二进制帧;
// resize 控制帧(JSON {type:"resize",cols,rows})→ WindowChange。
func makeContainerTerminalHandler(svc target.Service, aud audit.Recorder, acc *access.Service, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		containerID := chi.URLParam(r, "containerId")
		shell, err := validateContainerTarget(containerID, r.URL.Query().Get("shell"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_container_target", "容器或 shell 非法:"+err.Error())
			return
		}

		// 终端 = 在目标机上执行任意命令,按 ActOperate 把关(GET 在中间件里只判到 ActView)。
		if !requireTerminalOperate(w, r, acc, id) {
			return
		}

		// 先确认服务器存在(在升级为 WS 之前给出标准 HTTP 状态码)。留下的 srv 供审计记
		// 「以哪个 SSH 账号登进去」(见 terminalAudit)。
		srv, err := svc.Get(r.Context(), id)
		if err != nil {
			writeServerError(w, err)
			return
		}

		// WS 升级:同源校验(防跨站 WS 劫持)。OriginPatterns 仅放行 Host 自身;
		// 非同源 → Accept 返回错误,直接 403,绝不开 SSH。
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: originPatterns(r),
		})
		if err != nil {
			// Accept 已写过响应(含 403 同源拒绝);此处仅返回。
			return
		}
		conn.SetReadLimit(wsReadLimit)

		// 开 PTY 交互会话(docker exec -it <id> <shell>)。失败 → 以 WS close 帧人读告知,不泄密。
		cmd := buildContainerExecCmd(containerID, shell)
		// PTY 会话生命周期独立于单次 HTTP 请求 ctx(r.Context() 在 hijack 后不可靠),
		// 用自管理 ctx,WS 关闭时 cancel 收尾。
		sessCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sess, err := svc.ExecInteractive(sessCtx, id, cmd)
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, truncateWSReason(humanTerminalError(terminalLocale(r), err)))
			return
		}
		defer func() { _ = sess.Close() }()

		// 握手成功(SSH PTY 已建立)= 高危操作落地 → 写审计(谁/哪台/哪容器/何时)。
		// 审计如实记 shell:自动模式记 auto,别记成 /bin/sh 骗过后面的追责(容器里实际起的可能是 bash)。
		auditShell := shell
		if auditShell == "" {
			auditShell = "auto"
		}
		ta := newTerminalAudit(r, ac, aud, id, srv.Name, srv.User, "container", containerID, auditShell)
		tr := newTerminalCommandRecorder(ta.command)
		ta.start(audit.ActionContainerTerminal)
		// 装上命令回报钩子(机制与边界见 terminal_recorder.go)。注入失败不致命:会话照常能用,
		// 只是收尾汇总行会记 hook=silent。
		if _, werr := sess.Write([]byte(commandHookScript())); werr != nil {
			log.Printf("[terminal] 警告:注入命令审计钩子失败(server=%s container=%s): %v", id, containerID, werr)
		}
		defer ta.end(audit.ActionContainerTerminal, tr)

		pumpTerminal(sessCtx, cancel, conn, sess, tr.observe)
	}
}

// validateHostShell 校验主机 shell(白名单)。空串 = 交给 autoHostShellArgv 在远端就地选一个
// 可用的(优先 bash/zsh),而不是硬写 /bin/sh —— 大多数机器都装了 bash,默认给它才能让终端有
// 补全与提示符钩子。
func validateHostShell(shell string) (string, error) {
	if shell == "" {
		return "", nil
	}
	if _, ok := allowedShells[shell]; !ok {
		return "", errors.New("非法 shell(不在允许白名单内)")
	}
	return shell, nil
}

// autoHostShellArgv 是未指定 shell 时的远端自选命令:bash / zsh 优先(只有这两个有提示符钩子,
// 上下联动与补全靠它们),其次账号登录 shell,最后 /bin/sh 兜底。
//
// 整段是常量脚本:候选路径全部写死,不含任何用户输入,过 quoteArgs 后原样成为一个 argv。
// 用 exec 是为了让 PTY 的前台进程就是那个 shell 本身(退出即会话结束,中间不剩一层 sh)。
// 起手解释器取 /bin/sh:它比 bash 更普遍,也正是本来的默认 shell,不引入新的前置要求。
func autoHostShellArgv() []string {
	return []string{"/bin/sh", "-c",
		`for s in bash /bin/bash /usr/bin/bash zsh /bin/zsh /usr/bin/zsh; do ` +
			`command -v "$s" >/dev/null 2>&1 && exec "$s"; done; ` +
			`if [ -n "$SHELL" ] && [ -x "$SHELL" ]; then exec "$SHELL"; fi; exec /bin/sh`}
}

// makeServerTerminalHandler 返回 GET /api/servers/{id}/terminal handler —— **主机 shell** 终端
// (SSH 直接起交互 shell,不进容器)。这是「连服务器终端」的默认目标;容器终端是可选的更窄目标。
//
// 安全姿态同容器终端:鉴权由 /api 组 requireAuth 把守;WS 同源校验防跨站劫持;shell 经白名单;
// cmd array 化(仅 [shell],无拼接);凭据 vault 即用即弃;握手成功写审计(server_terminal)。
// 注意:服务器注册的 SSH 凭据本就具宿主机权限(容器模式的 docker exec 亦在宿主跑),主机 shell
// 不扩大信任边界,只是把既有权限诚实暴露。
func makeServerTerminalHandler(svc target.Service, aud audit.Recorder, acc *access.Service, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		shell, err := validateHostShell(r.URL.Query().Get("shell"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_shell", "shell 非法:"+err.Error())
			return
		}
		// 主机 shell 是最宽的写通道,同样按 ActOperate 把关(见 requireTerminalOperate)。
		if !requireTerminalOperate(w, r, acc, id) {
			return
		}
		srv, err := svc.Get(r.Context(), id)
		if err != nil {
			writeServerError(w, err)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns(r)})
		if err != nil {
			return
		}
		conn.SetReadLimit(wsReadLimit)

		sessCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 主机 shell:显式选过就按白名单原样起,没选则在远端挑一个可用的(cmd 始终是 array,无拼接)。
		cmd := []string{shell}
		auditShell := shell
		if shell == "" {
			cmd = autoHostShellArgv()
			auditShell = "auto"
		}
		sess, err := svc.ExecInteractive(sessCtx, id, cmd)
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, truncateWSReason(humanTerminalError(terminalLocale(r), err)))
			return
		}
		defer func() { _ = sess.Close() }()

		ta := newTerminalAudit(r, ac, aud, id, srv.Name, srv.User, "host", "", auditShell)
		tr := newTerminalCommandRecorder(ta.command)
		ta.start(audit.ActionServerTerminal)
		if _, werr := sess.Write([]byte(commandHookScript())); werr != nil {
			log.Printf("[terminal] 警告:注入命令审计钩子失败(server=%s): %v", id, werr)
		}
		defer ta.end(audit.ActionServerTerminal, tr)

		pumpTerminal(sessCtx, cancel, conn, sess, tr.observe)
	}
}

// pumpTerminal 在 WS 与交互式 SSH 会话间双向泵数据,直到任一侧结束。
//   - WS → SSH:文本帧若是 resize 控制 JSON → WindowChange;否则原样写入 stdin。
//   - SSH → WS:PTY 输出按块读出 → 以二进制帧发回 WS;读到的每一块先交给 tap(可为 nil)
//     顺路扫一遍终端命令回报,字节本身不改写(见 terminal_recorder.go)。
//
// 任一方向出错/结束都 cancel,使另一方向的阻塞读/写解除,随后 defer 关 WS + 关 SSH 会话(不泄漏)。
func pumpTerminal(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, sess target.Session, tap func([]byte)) {
	// SSH PTY → WS。
	go func() {
		defer cancel()
		buf := make([]byte, ptyReadChunk)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				if tap != nil {
					tap(buf[:n])
				}
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				werr := conn.Write(wctx, websocket.MessageBinary, buf[:n])
				wcancel()
				if werr != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// 远端异常中止:以 close 帧人读告知(不泄密)。
					_ = conn.Close(websocket.StatusNormalClosure, "session ended")
				} else {
					_ = conn.Close(websocket.StatusNormalClosure, "session ended")
				}
				return
			}
		}
	}()

	// WS → SSH PTY(本 goroutine)。
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			// 尝试解析 resize 控制帧;非控制帧则当普通输入写入 stdin。
			if cols, rows, ok := parseResize(data); ok {
				_ = sess.Resize(cols, rows)
				continue
			}
		}
		if len(data) > 0 {
			if _, err := sess.Write(data); err != nil {
				return
			}
		}
	}
}

// resizeMsg 是前端发来的窗口尺寸控制帧(JSON,文本帧)。
type resizeMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// parseResize 尝试把一条文本帧解析为 resize 控制帧。仅当 type=="resize" 时返回 ok=true。
// 失败/非控制帧返回 ok=false(调用方据此把数据当普通终端输入)。
func parseResize(data []byte) (cols, rows int, ok bool) {
	// 控制帧体积很小;非 JSON / 非 resize 一律视为普通输入(快速短路:必须以 '{' 起头)。
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "{") {
		return 0, 0, false
	}
	var m resizeMsg
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return 0, 0, false
	}
	if m.Type != "resize" {
		return 0, 0, false
	}
	return m.Cols, m.Rows, true
}

// terminalLocale 解析 WS 终端的语言:浏览器无法在 WS 升级请求上设自定义头
// (X-Pipewright-Locale),故前端以 `?locale=` 查询参数携带当前界面语言;缺省时
// 回退到 Accept-Language(WS 升级仍带此头),再回退到默认语言。
func terminalLocale(r *http.Request) string {
	if loc := i18n.Normalize(r.URL.Query().Get("locale")); loc != "" {
		return loc
	}
	return i18n.FromHeaders("", r.Header.Get("Accept-Language"))
}

// humanTerminalError 把领域错误映射为人读 WS 关闭原因(绝不含凭据明文/内部栈)。
// 源串以 zh-CN 书写,按 lang 经 i18n 目录翻译(目录未命中则原样返回)。
func humanTerminalError(lang string, err error) string {
	var msg string
	switch {
	case errors.Is(err, target.ErrAuth):
		msg = "SSH 认证失败:密钥或口令无效,或无登录权限"
	case errors.Is(err, target.ErrUnreachable):
		msg = "无法连接服务器:端口未开放、主机不可达或超时"
	case errors.Is(err, target.ErrInvalidCredential):
		msg = "凭据不是可用的 SSH 私钥或口令"
	case errors.Is(err, target.ErrVaultUnconfigured):
		msg = "保险库未配置 master key,无法取 SSH 凭据"
	case errors.Is(err, target.ErrCredentialNotFound):
		msg = "引用的 SSH 凭据不存在"
	default:
		msg = "打开容器终端失败:连接或命令执行错误"
	}
	return i18n.T(lang, msg)
}

// originPatterns 计算同源放行清单:仅放行请求自身的 Host(同源)。
// coder/websocket 的 Accept 默认即拒绝跨站 Origin;此处显式放行本机 Host,确保同源页面可连。
func originPatterns(r *http.Request) []string {
	host := r.Host
	if host == "" {
		return nil
	}
	return []string{host}
}

// truncateWSReason 截断 WS 关闭原因到协议允许的上限(125 字节),避免 Close 失败。
func truncateWSReason(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max]
}
