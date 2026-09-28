package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialTimeout 是 TCP 拨号 + SSH 握手的兜底超时(ctx 无 deadline 时生效)。
const dialTimeout = 10 * time.Second

// sshDialer 是基于 golang.org/x/crypto/ssh 的默认 SSHDialer 实现。
//
// AC-SEC-02:cmd 以 []string(程序 + 参数)传入,经 quoteArgs 对各参数 POSIX shell 转义
// 后再交 session.Run。调用方**不**拼接原始 shell 字符串,杜绝命令注入(参数中的
// `; rm -rf /`、`$(...)`、反引号等被当作字面量,绝不被远端 shell 解释执行)。
//
// HostKeyCallback:本期 dev 用 InsecureIgnoreHostKey 以便对 localhost 真测。
// ⚠️ DEFERRED(生产硬性):生产必须固定 known_hosts(ssh.FixedHostKey / knownhosts.New),
// 否则无法防 MITM。本机自测场景无 known_hosts,故暂放宽;切勿带此设置上生产。
type sshDialer struct{}

// PhaseTiming 是一次远程操作各阶段的耗时(只有时长,绝无地址 / 凭据)。
//
// 为什么要拆阶段:一条命令「60s 超时」有三种完全不同的病因 —— 连不上、握手慢、命令本身在目标机
// 上跑不完。只报一句「部署执行超时」会把三者混成一个,查的人只能猜。
type PhaseTiming struct {
	Dial      time.Duration
	Handshake time.Duration
	Command   time.Duration
}

// String 输出可读阶段串(供日志一行说完)。
func (p PhaseTiming) String() string {
	return fmt.Sprintf("连接 %s / 握手 %s / 命令 %s",
		p.Dial.Round(100*time.Millisecond), p.Handshake.Round(100*time.Millisecond), p.Command.Round(100*time.Millisecond))
}

// timedErr 给 ctx 错误(超时 / 取消)挂上阶段耗时;errors.Is/As 照常透过 Unwrap 命中。
type timedErr struct {
	err    error
	timing PhaseTiming
}

func (e *timedErr) Error() string { return e.err.Error() }
func (e *timedErr) Unwrap() error { return e.err }

// PhaseTimingOf 取出错误里的阶段耗时;非本层带计时的错误 → ok=false。
func PhaseTimingOf(err error) (PhaseTiming, bool) {
	var te *timedErr
	if errors.As(err, &te) {
		return te.timing, true
	}
	return PhaseTiming{}, false
}

func (sshDialer) Run(ctx context.Context, addr string, cfg SSHConfig, cmd []string) (*ExecResult, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}

	client, timing, err := dial(ctx, addr, cfg, auth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	res, cmdDur, runErr := execSession(ctx, client, cmd)
	timing.Command = cmdDur
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			return nil, &timedErr{err: runErr, timing: timing}
		}
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}
	return res, nil
}

// RunBatch 在**一条** SSH 连接上依次跑多条命令(每条各自一条 session),结果与 cmds 同序等长。
//
// 为什么需要它:一次 Run 意味着一整轮 TCP 拨号 + 密钥交换 + 认证 + 收尾。像指标采集那样
// 一台机器要跑六七条只读命令时,逐条 Run 会把同一台机器拨号六七次,光握手就吃掉数秒 ——
// 这才是「刷新状态很慢」的大头,命令本身都是毫秒级。连接复用后只剩一次拨号。
//
// 失败语义:单条命令非零退出**不是**错误(退出码与输出照常落在那一条结果里,其它条不受影响);
// 只有连接层面(拨号/握手/认证)或 ctx 超时/取消才返回 error。此时前面已跑完的结果仍随 error
// 一并返回(切片长度 = 已完成的条数),调用方可就用手上这部分。
func (sshDialer) RunBatch(ctx context.Context, addr string, cfg SSHConfig, cmds [][]string) ([]*ExecResult, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}

	client, _, err := dial(ctx, addr, cfg, auth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	results := make([]*ExecResult, 0, len(cmds))
	for _, cmd := range cmds {
		res, _, runErr := execSession(ctx, client, cmd)
		if runErr != nil {
			if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
				return results, runErr
			}
			return results, fmt.Errorf("%w", ErrUnreachable)
		}
		results = append(results, res)
	}
	return results, nil
}

// dial 建立一条 SSH 连接(TCP 拨号 + 握手 + 认证),返回 client 与各阶段耗时。
// 错误一律映射为领域错误(ErrUnreachable / ErrAuth / ctx 错误),绝不含地址与凭据。
func dial(ctx context.Context, addr string, cfg SSHConfig, auth []ssh.AuthMethod) (*ssh.Client, PhaseTiming, error) {
	var timing PhaseTiming
	clientCfg := &ssh.ClientConfig{
		User: cfg.User,
		Auth: auth,
		// DEFERRED:生产应固定 known_hosts(见上方类型注释)。
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // dev-only;生产固定 known_hosts(deferred)
		Timeout:         resolveTimeout(ctx),
	}

	// 经 net.Dialer 让 TCP 拨号也尊重 ctx 取消/超时。
	t0 := time.Now()
	d := net.Dialer{Timeout: clientCfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, timing, fmt.Errorf("%w", classifyDialErr(err))
	}
	timing.Dial = time.Since(t0)

	t1 := time.Now()
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		return nil, timing, fmt.Errorf("%w", classifyHandshakeErr(err))
	}
	timing.Handshake = time.Since(t1)
	return ssh.NewClient(sshConn, chans, reqs), timing, nil
}

// execSession 在已有 client 上开一条 session 跑 cmd,返回结果与命令阶段耗时。
// AC-SEC-02:cmd 是 array,经 quoteArgs 逐参数 POSIX 转义后才交给远端,调用方不拼 shell。
// 远端非零退出 → 结果里带 ExitCode、error 为 nil(与 Run 一致,由调用方按语义降级)。
func execSession(ctx context.Context, client *ssh.Client, cmd []string) (*ExecResult, time.Duration, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	t0 := time.Now()
	runErr := runWithContext(ctx, session, quoteArgs(cmd))
	dur := time.Since(t0)

	res := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		// 远端命令以非零退出:不是连接错误,exitCode 据实回传。
		res.ExitCode = exitErr.ExitStatus()
		return res, dur, nil
	}
	if runErr != nil {
		return res, dur, runErr
	}
	res.ExitCode = 0
	return res, dur, nil
}

// RunWithStdin 同 Run,但把 stdin 接到远端命令标准输入(供 `cat > file` 流式上传产物字节)。
// 与 Run 同样的连接/认证/超时/退出码语义;stdin 读尽即由 session 关闭远端 stdin。
func (sshDialer) RunWithStdin(ctx context.Context, addr string, cfg SSHConfig, cmd []string, stdin io.Reader) (*ExecResult, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // dev-only;生产固定 known_hosts(deferred)
		Timeout:         resolveTimeout(ctx),
	}
	d := net.Dialer{Timeout: clientCfg.Timeout}
	var timing PhaseTiming
	t0 := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w", classifyDialErr(err))
	}
	timing.Dial = time.Since(t0)
	t1 := time.Now()
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w", classifyHandshakeErr(err))
	}
	timing.Handshake = time.Since(t1)
	client := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	session.Stdin = stdin // 产物字节经 stdin 流式喂给远端 `cat > file`(无 argv 长度限)。

	t2 := time.Now()
	runErr := runWithContext(ctx, session, quoteArgs(cmd))
	// 上传的 Command 阶段 = 传字节 + 远端落盘,它才是「大文件要多久」的真值。
	timing.Command = time.Since(t2)
	res := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if runErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			return nil, &timedErr{err: runErr, timing: timing}
		}
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}
	res.ExitCode = 0
	return res, nil
}

// RunStream 建连后在 session 上启动 cmd 并返回 stdout 流。底层 client/session 的生命周期
// 绑定到返回的 ReadCloser:Close()(或 ctx 取消)即关 session + client,释放远端进程与 TCP。
//
// AC-SEC-02:cmd 经 quoteArgs 各参数 POSIX 转义后 Start,杜绝注入(同 Run)。
func (sshDialer) RunStream(ctx context.Context, addr string, cfg SSHConfig, cmd []string) (io.ReadCloser, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // dev-only;生产固定 known_hosts(deferred)
		Timeout:         resolveTimeout(ctx),
	}

	d := net.Dialer{Timeout: clientCfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w", classifyDialErr(err))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w", classifyHandshakeErr(err))
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}

	if err := session.Start(quoteArgs(cmd)); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}

	rc := &streamReadCloser{
		r:       stdout,
		session: session,
		client:  client,
	}

	// ctx 取消时主动收尾(客户端断开 / 超时):杀远端进程并关 session/client,防泄漏挂死。
	stop := make(chan struct{})
	rc.stop = stop
	go func() {
		select {
		case <-ctx.Done():
			rc.Close()
		case <-stop:
		}
	}()

	return rc, nil
}

// RunInteractive 建连后请求 PTY、接好 stdin/stdout/stderr,启动 cmd 并返回双向 Session
// (Story 6.4;FR-18)。底层 client/session 的生命周期绑定到返回的 Session:Close()(或
// ctx 取消)即关 session + client,释放远端 PTY/进程与 TCP(不泄漏)。
//
// AC-SEC-02:cmd 经 quoteArgs 各参数 POSIX 转义后作为单行命令 Start(同 Run/RunStream),
// 杜绝注入(`docker exec -it <id> <shell>` 各参数被当字面量,绝不被远端 shell 二次解释)。
func (sshDialer) RunInteractive(ctx context.Context, addr string, cfg SSHConfig, cmd []string) (Session, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // dev-only;生产固定 known_hosts(deferred)
		Timeout:         resolveTimeout(ctx),
	}

	d := net.Dialer{Timeout: clientCfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w", classifyDialErr(err))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w", classifyHandshakeErr(err))
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}
	// stdout + stderr 合流到同一个管道(交互终端里二者本就交织呈现)。
	pr, pw := io.Pipe()
	session.Stdout = pw
	session.Stderr = pw

	// 请求 PTY。xterm + 合理初值;后续 WindowChange 据前端 fit 调整。
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}

	if err := session.Start(quoteArgs(cmd)); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}

	is := &interactiveSession{
		stdin:   stdin,
		out:     pr,
		outW:    pw,
		session: session,
		client:  client,
	}

	// 远端进程退出 → 关 pw 让读侧得到 EOF(否则 Read 永久阻塞)。
	go func() {
		_ = session.Wait()
		_ = pw.Close()
	}()

	// ctx 取消(客户端断开 / 超时)→ 主动收尾,防泄漏挂死。
	stop := make(chan struct{})
	is.stop = stop
	go func() {
		select {
		case <-ctx.Done():
			_ = is.Close()
		case <-stop:
		}
	}()

	return is, nil
}

// interactiveSession 实现 target.Session:把 SSH PTY 会话包成双向 ReadWriteCloser + Resize。
// Close 幂等(防 ctx 协程与调用方并发关 + 多次关)。
type interactiveSession struct {
	stdin   io.WriteCloser
	out     io.Reader // io.Pipe 读端(stdout+stderr 合流)
	outW    io.Closer // io.Pipe 写端(Close 时一并关,解阻塞 Wait 协程外的 Read)
	session *ssh.Session
	client  *ssh.Client
	stop    chan struct{}
	once    sync.Once
}

func (s *interactiveSession) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *interactiveSession) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize 通知远端 PTY 调整窗口(列 cols × 行 rows)。非法尺寸归一为 1;失败忽略(非致命)。
func (s *interactiveSession) Resize(cols, rows int) error {
	if cols <= 0 {
		cols = 1
	}
	if rows <= 0 {
		rows = 1
	}
	return s.session.WindowChange(rows, cols)
}

func (s *interactiveSession) Close() error {
	s.once.Do(func() {
		if s.stop != nil {
			close(s.stop)
		}
		// 先发 SIGKILL 杀远端 shell/容器 exec(否则远端进程驻留);忽略错误。
		_ = s.session.Signal(ssh.SIGKILL)
		_ = s.stdin.Close()
		_ = s.session.Close()
		_ = s.client.Close()
		_ = s.outW.Close() // 解阻塞任何在 Read 上等待的读者
	})
	return nil
}

// streamReadCloser 把 SSH session 的 stdout 包成 ReadCloser,Close 时杀远端进程并关 session/client。
// Close 幂等(防多次关 + ctx 协程与调用方并发关)。
type streamReadCloser struct {
	r       io.Reader
	session *ssh.Session
	client  *ssh.Client
	stop    chan struct{}
	once    sync.Once
}

func (s *streamReadCloser) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *streamReadCloser) Close() error {
	s.once.Do(func() {
		if s.stop != nil {
			close(s.stop)
		}
		// 先发 SIGKILL 杀远端 tail -f / journalctl -f(否则远端进程驻留);忽略错误。
		_ = s.session.Signal(ssh.SIGKILL)
		_ = s.session.Close()
		_ = s.client.Close()
	})
	return nil
}

// runWithContext 在 session 上跑 cmd,并让 ctx 取消/超时能中断阻塞的 Run。
func runWithContext(ctx context.Context, session *ssh.Session, cmd string) error {
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// authMethods 据 cfg 装配 SSH 认证法(私钥优先,否则口令)。明文仅进程内,不外泄。
func authMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			// 解析失败:不回显私钥内容,只给干净领域错误。
			return nil, ErrInvalidCredential
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if cfg.Password != "" {
		return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
	}
	return nil, ErrInvalidCredential
}

// resolveTimeout 取 ctx 剩余时间作为拨号超时;无 deadline 时用 dialTimeout 兜底。
func resolveTimeout(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			return d
		}
		return time.Millisecond // 已超时:让拨号立即失败
	}
	return dialTimeout
}

// classifyDialErr 把 TCP 拨号错误映射为干净领域错误(绝不含内部地址/栈)。
//
// 调用侧自己的 ctx 到期 / 被取消要原样上抛:它俩都不指向网络,把它报成「端口未开放」会把人引去
// 查目标机的防火墙,而真实原因是这次操作没拿到时间。ctx 错误本身不含地址,不违背脱敏要求。
func classifyDialErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return ErrUnreachable
}

// classifyHandshakeErr 把 SSH 握手错误映射:认证类 → ErrAuth,其余 → ErrUnreachable。
// 仅看错误**类别**,绝不透传可能含敏感细节的原始文本。
func classifyHandshakeErr(err error) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "auth") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "no supported methods") {
		return ErrAuth
	}
	return ErrUnreachable
}

// quoteArgs 把命令 array 转为安全的单行 shell 命令:程序名 + 各参数经 POSIX 单引号转义。
// AC-SEC-02 防注入核心:参数里的特殊字符(空格、`;`、`$()`、反引号、引号)全被当字面量。
func quoteArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

// shellQuote 对单个参数做 POSIX 单引号转义。空串 → ”。内部单引号用 '\” 序列拼接。
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// 若全为安全字符,免引号(更可读;不影响安全)。
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || r == '@') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
