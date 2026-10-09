package httpapi

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/fuuhoo/pipewright/internal/target"
)

// fs_zip.go —— 目录下载(平台侧流式打包)+ 长传的静默看门狗。
//
// 为什么不在远端 tar 再管道出来:那要求对端有 tar,而且得把路径拼进一条命令 ——
// 面板本来就在没有 sftp-server 的机器上用 sh 兜底,再叠一层「远端打包」等于把下载
// 绑死在远端工具链上(BusyBox 的 tar 与 GNU 的 tar 连报错措辞都不同)。现在用的
// ReadDir + OpenRead 是 SFTP 与兜底两条实现都已具备的能力,平台内存里过一遍就吐出去。
//
// archive/zip 是**流式写**的:长度未知的条目用 data descriptor 收尾(Go 标准库内部
// 用 countWriter, close 时补写),所以边读边写不会把整个目录压在内存里。响应因此
// 不由平台预设 Content-Length(小目录恰好落进响应缓冲时,Go 会自己补一个 —— 不能依赖),
// 界面按「下载中」而非百分比显示。
//
// 大文件的超时口径也在这条文件里改口径:下载不该有「总时长上限」(10 GiB 走慢链路
// 本来就该传得上),只有「静默太久」才该断 —— 见 idleWatch。

const (
	// fsStreamIdle 是一条流允许静默多久(没有字节流动即视为卡死并拆掉)。
	fsStreamIdle = 2 * time.Minute
	// fsZipMaxEntries 是一次打包的条目上限(文件 + 目录)。超了宁可让这次下载失败,
	// 也不给人一个看起来完整、其实少了一半的压缩包。
	fsZipMaxEntries = 20000
	// fsZipMaxDepth 是递归深度上限:符号链接已不跟随,这道保险挡的是病态深树与环形挂载。
	fsZipMaxDepth = 64
	// zipCopyBuf 是逐块搬运的缓冲大小:太小则每轮都压在协议开销上,太大则白占内存。
	zipCopyBuf = 256 * 1024
)

// errZipTooMany 是撞到条目/深度上限(响应头早已发出,状态码改不了,只能掐断连接)。
var errZipTooMany = errors.New("httpapi: 目录过大,打包条目超出上限")

// idleWatch 是「静默即断」看门狗:每次有字节流动就 touch,静默到点执行 onFire。
//
// 为什么不用 context.WithTimeout 封顶总时长:大文件传输的合理耗时取决于链路带宽,
// 平台没法猜;而卡死的流一秒都不该多占一条 SSH 连接。
type idleWatch struct {
	mu      sync.Mutex
	timer   *time.Timer
	d       time.Duration
	onFire  func()
	stopped bool
}

func newIdleWatch(d time.Duration, onFire func()) *idleWatch {
	w := &idleWatch{d: d, onFire: onFire}
	w.timer = time.AfterFunc(d, w.fire)
	return w
}

func (w *idleWatch) fire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.onFire()
}

// touch 记下「这一刻还有字节在动」。
func (w *idleWatch) touch() {
	w.mu.Lock()
	if !w.stopped {
		w.timer.Reset(w.d)
	}
	w.mu.Unlock()
}

// stop 收尾(必须调,否则计时器会在请求结束后回调)。
func (w *idleWatch) stop() {
	w.mu.Lock()
	w.stopped = true
	w.timer.Stop()
	w.mu.Unlock()
}

// touchingReader 在读到任何字节时给看门狗报一次平安:客户端断线、远端 stall 都会让
// 这里静下来,而「静下来」正是该拆流的信号。
type touchingReader struct {
	r     io.Reader
	touch func()
}

func (t *touchingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.touch()
	}
	return n, err
}

// storedExt 是「压了也白压」的扩展名:镜像、压缩包、媒体在远端已经压缩过,
// 平台再 deflate 只是拿 CPU 换一点几乎不存在的体积收益,还会拖慢整条链。
var storedExt = map[string]bool{
	"zip": true, "gz": true, "tgz": true, "bz2": true, "xz": true, "zst": true,
	"7z": true, "rar": true, "jar": true, "war": true, "apk": true, "deb": true,
	"rpm": true, "png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"heic": true, "mp4": true, "m4v": true, "mkv": true, "mov": true, "avi": true,
	"mp3": true, "m4a": true, "flac": true, "ogg": true, "wav": true, "pdf": true,
	"wasm": true, "so": true, "dylib": true, "whl": true,
}

// methodFor 按扩展名选打包方式:已知压缩过的直接存,其余 deflate。
func methodFor(name string) uint16 {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if storedExt[ext] {
		return zip.Store
	}
	return zip.Deflate
}

// streamZipDir 把 root 目录整棵子树流式打包进 w(不含 root 自身的路径前缀)。
//
// 三条纪律:
//
//	· 符号链接一律不跟随也不写入 —— 跟随时目录树里的软链能把一次下载变成环,
//	  不跟随又会在包里凭空少东西,所以对界面上「这个目录里的链接没打进包」是唯一诚实答案
//	  (审计里记 skippedLinks 让人查得到)。
//	· 每个目录都写一条目录项:空目录否则解压后会不存在。
//	· 撞到条目/深度上限就**掐断连接**(响应头早已发出,状态码改不了)。半截 zip 看着
//	  像完整包,比一次失败的下载危险得多。
func streamZipDir(ctx context.Context, w io.Writer, ws target.Workspace, root string, touch func()) (zipStats, error) {
	zw := zip.NewWriter(w)
	st := zipStats{}
	prefix := path.Base(root) + "/"

	type level struct {
		abs   string
		rel   string
		depth int
	}
	stack := []level{{abs: root, rel: prefix, depth: 0}}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := ws.ReadDir(ctx, cur.abs)
		if err != nil {
			return st, err
		}
		// 列到目录就是进展:全是一个个空文件的大目录,正文侧几乎没字节流过,
		// 只按字节算「静默」会把这种正常打包掐掉。
		touch()
		st.entries++
		if st.entries > fsZipMaxEntries {
			return st, errZipTooMany
		}
		// 目录项(Modified 留空 → 标准库落 0,解压端按 1980 纪元处理;远端没给时间时不编一个)。
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: cur.rel, Method: zip.Store}); err != nil {
			return st, err
		}
		for _, e := range entries {
			if e.IsLink {
				st.skippedLinks++
				continue
			}
			name := cur.rel + e.Name
			if e.IsDir {
				if cur.depth+1 > fsZipMaxDepth {
					return st, errZipTooMany
				}
				stack = append(stack, level{abs: e.Path, rel: name + "/", depth: cur.depth + 1})
				continue
			}
			st.entries++
			if st.entries > fsZipMaxEntries {
				return st, errZipTooMany
			}
			hdr := &zip.FileHeader{Name: name, Method: methodFor(e.Name)}
			if e.MtimeUnix > 0 {
				hdr.Modified = time.Unix(e.MtimeUnix, 0)
			}
			ew, err := zw.CreateHeader(hdr)
			if err != nil {
				return st, err
			}
			rc, err := ws.OpenRead(ctx, e.Path)
			if err != nil {
				return st, err
			}
			n, err := io.CopyBuffer(ew, &touchingReader{r: rc, touch: touch}, make([]byte, zipCopyBuf))
			_ = rc.Close()
			if err != nil {
				return st, err
			}
			st.bytes += n
			if err := zw.Flush(); err != nil {
				return st, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return st, err
	}
	return st, nil
}

// zipStats 是一次打包的计量(进审计,也用于界面完成提示)。
type zipStats struct {
	entries      int
	bytes        int64
	skippedLinks int
}
