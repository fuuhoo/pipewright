package target

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// fs.go —— 远程文件工作区(Story 6 运维:服务器卡片上的「远程」)。
//
// 为什么单开一条通道而不是继续用 Exec:网页文件面板要在一次会话里连打十几次
// 「列目录 / 读文件 / 改权限」,逐条 Exec 每次都是一整轮 TCP 拨号 + 密钥交换 + 认证,
// 点一下等半秒;而解析 `ls` / `stat` 的输出在 GNU / BSD / BusyBox 三种实现下字段与
// 日期格式都不同(指标采集已经为此写过一堆平台回退)。SFTP 是 sshd 自带的子系统,
// 给的是结构化属性,不碰 shell,自然也没有注入面 —— 路径只作为协议参数传递。
//
// 生命周期:一次 OpenWorkspace = 一条 SSH 连接,调用方 Close 即释放;不缓存连接
// (弹窗开着时并发请求数很小,连接池的失效检测复杂度暂时不值得)。
//
// AC-SEC-01/02:凭据仍经 vault 即用即弃;本文件不接受任何拼进 shell 的字符串。

// 远程文件层的领域错误(映射成人读 HTTP 文案,绝不含远端 stderr 细节)。
var (
	// ErrFSUnsupported 是该 target 的实现不提供文件工作区(如测试里的假拨号器)。
	ErrFSUnsupported = errors.New("target: 该实现不支持远程文件操作")
	// ErrRemoteNotFound 是远端路径不存在(与「无权限」区分开:前者是导航常态)。
	ErrRemoteNotFound = errors.New("target: 远程路径不存在")
	// ErrRemotePermission 是远端拒绝读写(权限 / 只读挂载)。
	ErrRemotePermission = errors.New("target: 远程路径权限不足")
)

// FileStat 是远端一个路径的可见属性。零值 MtimeUnix=0 表示对端没给时间。
type FileStat struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"isDir"`
	IsLink     bool   `json:"isLink"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	MtimeUnix  int64  `json:"mtime"`
}

// Workspace 是一台服务器上的远程文件视图,底层是一条已建立的 SSH 连接。
// 所有方法都尊重传入 ctx:ctx 取消即拆掉连接让阻塞中的调用尽快返回(不泄漏 goroutine)。
type Workspace interface {
	// Realpath 归一路径并解析 `.` 这类相对写法(返回绝对路径;不存在亦可,协议允许)。
	Realpath(ctx context.Context, p string) (string, error)
	// Stat 取单个路径属性(跟随符号链接;链接目标另在 LinkTarget)。
	Stat(ctx context.Context, p string) (FileStat, error)
	// ReadDir 列目录,按名升序;`.` 与 `..` 不返回。
	ReadDir(ctx context.Context, dir string) ([]FileStat, error)
	// ReadFile 读到 limit 字节为止;超出即截断(truncated=true),供文本编辑器用。
	ReadFile(ctx context.Context, p string, limit int64) (data []byte, truncated bool, err error)
	// OpenRead 返回流式读句柄(下载用)。调用方读完必须 Close。
	OpenRead(ctx context.Context, p string) (io.ReadCloser, error)
	// WriteFile 覆盖写 p(远端已有同名文件被截断;父目录须已存在)。
	WriteFile(ctx context.Context, p string, r io.Reader) error
	// Mkdir 创建目录(递归,等价 mkdir -p)。
	Mkdir(ctx context.Context, p string) error
	// Remove 删除文件或空目录。非空目录失败 —— 递归删除不在这层提供。
	Remove(ctx context.Context, p string) error
	// Rename 重命名 / 移动到 to。
	Rename(ctx context.Context, from, to string) error
	// Backend 回报实际生效的实现,供界面如实显示能力范围。
	Backend() string
	// Close 释放 SSH 连接;幂等。
	Close() error
}

// WorkspaceOpener 是 Service 的可选能力(先例:SSH 层的 batchDialer)。
// 支持远程文件的实现额外满足它;不支持的(测试里的假 Service)断言失败即可。
type WorkspaceOpener interface {
	OpenWorkspace(ctx context.Context, serverID string) (Workspace, error)
}

// fsDialer 是 SSHDialer 的可选能力:用装配好的认证材料开一个远程文件视图。
// 真实实现走 SFTP;测试里的假拨号器不实现它,service 据此回 ErrFSUnsupported。
type fsDialer interface {
	OpenFS(ctx context.Context, addr string, cfg SSHConfig) (Workspace, error)
}

// OpenWorkspace 为指定服务器开一条 SSH 连接并返回其远程文件视图。
// 凭据取用/清引用纪律与 Exec 完全一致:明文只在进程内,建连后即清本地引用。
//
// 顺序有讲究:先认服务器(404 优先于「不支持」,用户看到的是真原因),再判能力,
// 最后才取凭据 —— 不支持的实现不该白解密一次私钥。
func (s *service) OpenWorkspace(ctx context.Context, serverID string) (Workspace, error) {
	if _, err := s.Get(ctx, serverID); err != nil {
		return nil, err
	}
	fd, ok := s.dialer.(fsDialer)
	if !ok {
		return nil, ErrFSUnsupported
	}
	addr, cfg, err := s.sshTarget(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { cfg.PrivateKey = ""; cfg.Password = "" }()

	return fd.OpenFS(ctx, addr, cfg)
}

// OpenFS 实现 fsDialer:拨一条连接,优先挂 SFTP 子系统。
// sshd 未启用 sftp-server(最小镜像 / dropbear 常见)时返回可辨的错误,由上层降级。
func (sshDialer) OpenFS(ctx context.Context, addr string, cfg SSHConfig) (Workspace, error) {
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	client, _, err := dial(ctx, addr, cfg, auth)
	if err != nil {
		return nil, err
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		// 子系统不可用不是「连不上」:连接是好的,只是这台 sshd 没开 sftp-server。
		return nil, fmt.Errorf("%w: %s", ErrFSUnsupported, err)
	}
	return &sftpWorkspace{ssh: client, sftp: sc}, nil
}

// sftpWorkspace 把 pkg/sftp 客户端包成 Workspace。
//
// pkg/sftp 的调用会阻塞在 SSH channel 上且不吃 ctx,所以每个方法都经 call:
// ctx 一取消就拆掉整条连接 —— 被拆的调用会以错误返回,goroutine 不会悬挂。
type sftpWorkspace struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	once sync.Once
}

func (w *sftpWorkspace) Backend() string { return "sftp" }

// Realpath 归一远端路径(`.` → 该会话的家目录,由 sftp-server 的工作目录决定)。
func (w *sftpWorkspace) Realpath(ctx context.Context, p string) (string, error) {
	return call(ctx, w, func() (string, error) {
		return w.sftp.RealPath(cleanRemote(p))
	})
}

// Stat 取属性:先 Lstat 判符号链接,再对链接目标 Stat 拿到「点开要看的那个东西」的大小。
func (w *sftpWorkspace) Stat(ctx context.Context, p string) (FileStat, error) {
	return call(ctx, w, func() (FileStat, error) {
		p = cleanRemote(p)
		li, lerr := w.sftp.Lstat(p)
		if lerr != nil {
			return FileStat{}, mapSFSErr(lerr)
		}
		fi := li
		if li.Mode()&os.ModeSymlink != 0 {
			if tgt, terr := w.sftp.ReadLink(p); terr == nil {
				if si, serr := w.sftp.Stat(path.Join(path.Dir(p), tgt)); serr == nil {
					fi = si
				} else {
					// 断链:仍然按链接呈现(大小取自身),不让整个目录视图失败。
					fi = li
				}
			} else {
				fi = li
			}
		}
		st := statOf(p, fi)
		if li.Mode()&os.ModeSymlink != 0 {
			st.IsLink = true
			if tgt, terr := w.sftp.ReadLink(p); terr == nil {
				st.LinkTarget = tgt
			}
		}
		return st, nil
	})
}

// ReadDir 列目录(名升序)。目录在前由界面决定,这里只给稳定原始序。
func (w *sftpWorkspace) ReadDir(ctx context.Context, dir string) ([]FileStat, error) {
	return call(ctx, w, func() ([]FileStat, error) {
		dir = cleanRemote(dir)
		fis, err := w.sftp.ReadDir(dir)
		if err != nil {
			return nil, mapSFSErr(err)
		}
		out := make([]FileStat, 0, len(fis))
		for _, fi := range fis {
			name := fi.Name()
			if name == "." || name == ".." {
				continue
			}
			st := statOf(path.Join(dir, name), fi)
			if fi.Mode()&os.ModeSymlink != 0 {
				st.IsLink = true
				if tgt, terr := w.sftp.ReadLink(st.Path); terr == nil {
					st.LinkTarget = tgt
				}
			}
			out = append(out, st)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	})
}

// ReadFile 读到 limit 截断(文本编辑器用,绝不把整块大盘当正文读回来)。
func (w *sftpWorkspace) ReadFile(ctx context.Context, p string, limit int64) ([]byte, bool, error) {
	res, err := call(ctx, w, func() (readResult, error) {
		f, oerr := w.sftp.Open(cleanRemote(p))
		if oerr != nil {
			return readResult{}, mapSFSErr(oerr)
		}
		defer func() { _ = f.Close() }()
		buf := make([]byte, 0, 4096)
		chunk := make([]byte, 32*1024)
		for int64(len(buf)) < limit {
			want := int64(len(chunk))
			if remain := limit - int64(len(buf)); remain < want {
				want = remain
			}
			n, rerr := f.Read(chunk[:want])
			buf = append(buf, chunk[:n]...)
			if rerr == io.EOF {
				return readResult{data: buf}, nil
			}
			if rerr != nil {
				return readResult{}, mapSFSErr(rerr)
			}
		}
		// 还能读到一字节 → 说明被 limit 截断了。
		_, rerr := f.Read(chunk[:1])
		return readResult{data: buf, truncated: rerr == nil}, nil
	})
	return res.data, res.truncated, err
}

// callErr 是只有错误返回值的 call(写操作全是这一形)。
func callErr(ctx context.Context, w *sftpWorkspace, f func() error) error {
	_, err := call(ctx, w, func() (struct{}, error) { return struct{}{}, f() })
	return err
}

// readResult 是 ReadFile 的多值载荷(call 只穿一个值)。
type readResult struct {
	data      []byte
	truncated bool
}

// OpenRead 返回流式读句柄。句柄关闭只关文件,不关连接(连接由 Workspace.Close 收)。
func (w *sftpWorkspace) OpenRead(ctx context.Context, p string) (io.ReadCloser, error) {
	return call(ctx, w, func() (io.ReadCloser, error) {
		f, err := w.sftp.Open(cleanRemote(p))
		if err != nil {
			return nil, mapSFSErr(err)
		}
		return f, nil
	})
}

// WriteFile 覆盖写(先落同名 .tmp 再 rename,免得网页编辑器把线上文件写成半截)。
// rename 覆盖在 openssh 的 sftp-server 上是 rename(2),对同目录文件是原子的。
func (w *sftpWorkspace) WriteFile(ctx context.Context, p string, r io.Reader) error {
	return callErr(ctx, w, func() error {
		p = cleanRemote(p)
		tmp := p + ".pipewright-tmp"
		_ = w.sftp.Remove(tmp)
		f, err := w.sftp.Create(tmp)
		if err != nil {
			return mapSFSErr(err)
		}
		if _, cerr := io.Copy(f, r); cerr != nil {
			_ = f.Close()
			_ = w.sftp.Remove(tmp)
			return mapSFSErr(cerr)
		}
		if cerr := f.Close(); cerr != nil {
			_ = w.sftp.Remove(tmp)
			return mapSFSErr(cerr)
		}
		// 保住原文件的权限位:编辑器覆盖不该把 600 的密钥改成 644。
		if fi, serr := w.sftp.Stat(p); serr == nil {
			_ = w.sftp.Chmod(tmp, fi.Mode().Perm())
		}
		if rerr := renameOver(w.sftp, tmp, p); rerr != nil {
			_ = w.sftp.Remove(tmp)
			return mapSFSErr(rerr)
		}
		return nil
	})
}

func (w *sftpWorkspace) Mkdir(ctx context.Context, p string) error {
	return callErr(ctx, w, func() error {
		if err := w.sftp.MkdirAll(cleanRemote(p)); err != nil {
			return mapSFSErr(err)
		}
		return nil
	})
}

func (w *sftpWorkspace) Remove(ctx context.Context, p string) error {
	return callErr(ctx, w, func() error {
		p = cleanRemote(p)
		fi, err := w.sftp.Lstat(p)
		if err != nil {
			return mapSFSErr(err)
		}
		if fi.IsDir() {
			// 只让删空目录:非空由服务端报错,这正是要的保险(不替界面提供 rm -rf)。
			return mapSFSErr(w.sftp.RemoveDirectory(p))
		}
		return mapSFSErr(w.sftp.Remove(p))
	})
}

func (w *sftpWorkspace) Rename(ctx context.Context, from, to string) error {
	return callErr(ctx, w, func() error {
		return mapSFSErr(renameOver(w.sftp, cleanRemote(from), cleanRemote(to)))
	})
}

// renameOver 是「像 mv 那样覆盖式改名」。
//
// 为什么要单独写:本机实测(OpenSSH 9.x + macOS sftp-server)证明 SSH_FXP_RENAME **不覆盖**
// 已存在的目标 —— 直接 rename 回 SSH_FX_FAILURE,编辑器保存第二次就报错。
// 顺序:先试 posix-rename@openssh.com(原子覆盖,OpenSSH 系都支持);扩展不被认同时,
// 目标若不存在走普通 rename;存在则先删再 rename —— 中间有一瞬「文件不在」,
// 但绝不会让人读到半截正文,比截断重写更安全。
func renameOver(c *sftp.Client, from, to string) error {
	if err := c.PosixRename(from, to); err == nil {
		return nil
	}
	if _, serr := c.Stat(to); serr != nil {
		return c.Rename(from, to) // 目标本不存在:普通 rename 即可,失败原样上抛
	}
	if rerr := c.Remove(to); rerr != nil {
		return rerr
	}
	return c.Rename(from, to)
}

// Close 拆掉 SFTP 会话与底层 SSH 连接;经 sync.Once 保证幂等(ctx 取消与调用方并发关)。
func (w *sftpWorkspace) Close() error {
	w.once.Do(func() {
		_ = w.sftp.Close()
		_ = w.ssh.Close()
	})
	return nil
}

// statOf 把 os.FileInfo 归一为 FileStat。
func statOf(p string, fi os.FileInfo) FileStat {
	st := FileStat{
		Name:  path.Base(p),
		Path:  p,
		IsDir: fi.IsDir(),
		Size:  fi.Size(),
		Mode:  uint32(fi.Mode().Perm()),
	}
	if mt := fi.ModTime(); !mt.IsZero() {
		st.MtimeUnix = mt.Unix()
	}
	return st
}

// cleanRemote 只做路径归一(去掉多余斜杠与 . 段)。绝不拼 shell、绝不做「越界拦截」:
// 能在终端里敲 `cd /etc` 的会话,列目录拦它没有意义,给了假的安全感反而糟。
func cleanRemote(p string) string {
	if p == "" {
		return "."
	}
	return path.Clean(p)
}

// mapSFSErr 把协议/系统错误折成领域错误。原始文本留在 %s 里(供人读诊断),
// 但绝不含凭据 —— SFTP 的错误体只有路径与状态码。
//
// 为什么要 errors.As 到 *StatusError 再比 FxCode:sftp 客户端把服务端的 SSH_FXP_STATUS
// 原样带回来,`os.IsNotExist` 对它一律判不出来 —— 拿不到「不存在」就会把 404 报成 500。
func mapSFSErr(err error) error {
	if err == nil {
		return nil
	}
	var st *sftp.StatusError
	if errors.As(err, &st) {
		switch st.FxCode() {
		case sftp.ErrSSHFxNoSuchFile:
			return fmt.Errorf("%w: %s", ErrRemoteNotFound, err)
		case sftp.ErrSSHFxPermissionDenied:
			return fmt.Errorf("%w: %s", ErrRemotePermission, err)
		}
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrRemoteNotFound, err)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: %s", ErrRemotePermission, err)
	default:
		return err
	}
}

// call 把一个不吃 ctx 的 sftp 操作包成 ctx 可取消的调用:超时/断开时立刻拆连接,
// 让阻塞中的那个调用以错误返回,goroutine 不悬挂。
func call[T any](ctx context.Context, w *sftpWorkspace, f func() (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := f()
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		_ = w.Close() // 拆连接使阻塞中的调用返回;结果随频道丢弃(已无人等)。
		var zero T
		return zero, ctx.Err()
	}
}
