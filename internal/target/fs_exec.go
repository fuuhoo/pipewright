package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// fs_exec.go —— 远程文件工作区的 exec 兜底实现。
//
// 什么时候用它:这台 sshd 没挂 sftp-server(最小镜像、dropbear、部分网络设备常见),
// SFTP 子系统开不出来。此时连接是好的、能跑命令,于是拿同一批 POSIX 工具凑出文件面板
// 需要的能力。这是**兜底**而非二等实现:界面据 Backend()="exec" 如实显示能力范围。
//
// 三条纪律:
//  1. 复用 OpenFS 已拨好的那条 *ssh.Client —— 每个操作只开一条 session,不再重拨号。
//     兜底若逐操作重拨,「SFTP 不可用所以慢上加慢」就成了真话。
//  2. 路径一律作 array 参数经 quoteArgs 转义(AC-SEC-02);脚本正文只认 $1/$2,
//     绝不把用户输入插值进 shell 字符串。
//  3. 进程内不保留 SSHConfig:凭据只到 OpenFS 返回为止,工作区只握连接句柄。
//
// 已知边界(兜底路径固有):
//   - 名含换行或制表符的条目会破坏行式解析(列目录按行切、字段按制表符切)。
//   - mtime 依赖远端有 GNU 或 BSD 一系的 stat;两者皆无时回 0(界面无时间)。
//   - 一次列目录要在远端 fork 若干进程(ls/stat/wc/readlink),明显慢于 SFTP ——
//     这是「没有结构化子系统」的代价,不是 bug。

// 兜底实现用哨兵字符串区分失败原因:各实现(BusyBox 尤甚)的错误文案措辞不同,
// 靠匹配 stderr 会误判;由脚本自己打印哨兵是确定且与 locale 无关的做法
// (脚本首行统一 LC_ALL=C,故回退到 stderr 匹配时也只有英文文案可匹配)。
const (
	markNotFound = "PIPEWRIGHT:NOT_FOUND"
	markNotDir   = "PIPEWRIGHT:NOT_A_DIR"
	markNotPerm  = "PIPEWRIGHT:PERMISSION_DENIED"
	markNotEmpty = "PIPEWRIGHT:NOT_EMPTY"

	// execTmpSuffix 是覆盖写落地的临时文件名后缀(同 SFTP 实现,便于两路一致排查)。
	execTmpSuffix = ".pipewright-tmp"
	// statFieldSep 是 emit_row 输出的字段分隔符。
	statFieldSep = "\t"
)

// execWorkspace 是 Workspace 的 exec 兜底实现。
type execWorkspace struct {
	client *ssh.Client
	// statFlavor 是开工作区时探测到的 stat 方言:"gnu" | "bsd" | ""(远端没有 stat)。
	// mtime 只有 stat 能可靠给出,而 GNU 的 -c %Y 与 BSD 的 -f %m 互不兼容。
	statFlavor string
	once       sync.Once
}

func (w *execWorkspace) Backend() string { return "exec" }

// newExecWorkspace 探测远端 stat 方言,顺带确认这条连接真能跑命令。
// 连 sh 都起不来时兜底也无从谈起 → ErrFSUnsupported。
func newExecWorkspace(ctx context.Context, client *ssh.Client) (*execWorkspace, error) {
	w := &execWorkspace{client: client}
	res, err := w.run(ctx, `if stat -c %Y / >/dev/null 2>&1; then echo gnu; `+
		`elif stat -f %m / >/dev/null 2>&1; then echo bsd; else echo none; fi`)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(res.Stdout) {
	case "gnu", "bsd":
		w.statFlavor = strings.TrimSpace(res.Stdout)
	default:
		// 没有可用 stat:面板照常提供,时间列留空。
		// 但探测本身非零退出说明远端连 if/统计工具都跑不动,别装作能用。
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("%w: 远端无法执行文件操作命令", ErrFSUnsupported)
		}
	}
	return w, nil
}

// run 在复用的连接上开一条 session 跑 POSIX sh 脚本;args 作为 $1.. 传入(逐参数转义)。
func (w *execWorkspace) run(ctx context.Context, script string, args ...string) (*ExecResult, error) {
	cmd := append([]string{"sh", "-c", "LC_ALL=C; export LC_ALL; " + script, "_"}, args...)
	res, _, err := execSession(ctx, w.client, cmd)
	return res, err
}

// shellHelper 是两段脚本共用的正文:把一个路径的可见属性按固定字段序打成一行。
//
// 与 SFTP 实现对齐的语义:符号链接**跟随**其目标定类型/大小/时间(点开链接要看的是
// 目标),链接自身信息留在 link 字段;断链仍按链接呈现,不让整份目录视图失败。
// 非普通文件(fifo、设备)不取大小 —— 对 fifo 做 `wc -c <` 会挂住整条 session。
const shellHelper = `
emit_entry() {
  p=$1
  if [ ! -e "$p" ] && [ ! -L "$p" ]; then echo ` + markNotFound + ` >&2; return 1; fi
  lt=; base=$p
  if [ -L "$p" ]; then
    lt=$(readlink "$p" 2>/dev/null)
    case $lt in
      "") :;;
      /*) base=$lt;;
      *) base=$(dirname "$p")/$lt;;
    esac
  fi
  if [ ! -e "$base" ]; then base=$p; fi
  kind=o; size=0
  if [ -d "$base" ]; then kind=d
  elif [ -f "$base" ]; then kind=f; size=$(wc -c <"$base" 2>/dev/null | tr -dc '0-9')
  fi
  # 链接一律标 l(界面画箭头);类型/大小已按目标算好,与 SFTP 实现同义。
  [ -L "$p" ] && kind=l
  mode=$(ls -dln -- "$base" 2>/dev/null | cut -c2-10 | head -1)
  case $flav in
    gnu) mt=$(stat -c %Y -- "$base" 2>/dev/null);;
    bsd) mt=$(stat -f %m "$base" 2>/dev/null);;
    *) mt=;;
  esac
  [ -n "$mt" ] || mt=0
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$kind" "$mode" "$size" "$mt" "$lt" "${p##*/}"
}`

// statScript 对单个路径输出一行属性。
const statScript = `p=$1; flav=$2` + shellHelper + `
emit_entry "$p"`

// readDirScript 在目标目录内 glob(含点文件),逐条输出一行。
//
// `* .[!.]* ..?*` 是 POSIX 下枚举点文件的写法:`.[!.]*` 排掉 `.`/`..`,`..?*` 补上
// `...` 这类合法名字;无匹配时字面量会残留,所以每条先 test。
const readDirScript = `dir=$1; flav=$2` + shellHelper + `
if [ ! -e "$dir" ] && [ ! -L "$dir" ]; then echo ` + markNotFound + ` >&2; exit 1; fi
if [ ! -d "$dir" ]; then echo ` + markNotDir + ` >&2; exit 1; fi
cd "$dir" 2>/dev/null || { echo ` + markNotPerm + ` >&2; exit 1; }
for ent in * .[!.]* ..?*; do
  [ -e "$ent" ] || [ -L "$ent" ] || continue
  emit_entry "$dir/$ent"
done`

func (w *execWorkspace) Stat(ctx context.Context, p string) (FileStat, error) {
	res, err := w.run(ctx, statScript, cleanRemote(p), w.statFlavor)
	if err != nil {
		return FileStat{}, err
	}
	if err := checkExec(res); err != nil {
		return FileStat{}, err
	}
	line := strings.TrimRight(res.Stdout, "\n")
	if line == "" {
		return FileStat{}, fmt.Errorf("%w: %s", ErrRemoteNotFound, p)
	}
	st, perr := parseStatLine(strings.SplitN(line, "\n", 2)[0])
	if perr != nil {
		return FileStat{}, perr
	}
	st.Path = cleanRemote(p)
	return st, nil
}

func (w *execWorkspace) ReadDir(ctx context.Context, dir string) ([]FileStat, error) {
	dir = cleanRemote(dir)
	res, err := w.run(ctx, readDirScript, dir, w.statFlavor)
	if err != nil {
		return nil, err
	}
	if err := checkExec(res); err != nil {
		return nil, err
	}
	out := make([]FileStat, 0, 16)
	for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		st, perr := parseStatLine(line)
		if perr != nil {
			return nil, perr
		}
		st.Path = path.Join(dir, st.Name)
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Realpath 用 `cd + pwd -P` 归一路径:与 SFTP 的 RealPath 同义(解析符号链接,
// `.` → 该会话登录后的工作目录)。
func (w *execWorkspace) Realpath(ctx context.Context, p string) (string, error) {
	res, err := w.run(ctx, `p=$1
if [ ! -e "$p" ] && [ ! -L "$p" ]; then echo `+markNotFound+` >&2; exit 1; fi
if [ ! -d "$p" ]; then echo `+markNotDir+` >&2; exit 1; fi
cd "$p" 2>/dev/null || { echo `+markNotPerm+` >&2; exit 1; }
pwd -P`, cleanRemote(p))
	if err != nil {
		return "", err
	}
	if err := checkExec(res); err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// ReadFile 读到 limit 截断。先经 head -c 预截,免得为了看一眼前 1 MiB 而把远端
// 50 G 的镜像整个传回来。
func (w *execWorkspace) ReadFile(ctx context.Context, p string, limit int64) ([]byte, bool, error) {
	res, err := w.run(ctx, `p=$1
if [ ! -f "$p" ]; then echo `+markNotFound+` >&2; exit 1; fi
[ -r "$p" ] || { echo `+markNotPerm+` >&2; exit 1; }
head -c "$2" < "$p"`, cleanRemote(p), strconv.FormatInt(limit+1, 10))
	if err != nil {
		return nil, false, err
	}
	if err := checkExec(res); err != nil {
		return nil, false, err
	}
	if int64(len(res.Stdout)) > limit {
		return []byte(res.Stdout[:limit]), true, nil
	}
	return []byte(res.Stdout), false, nil
}

// OpenRead 流式下载:session 的 stdout 直接接成 ReadCloser,关掉只收这条 session
// (连接是所有操作共用的,绝不在这里关)。
//
// 先 Stat 再开流:OpenRead 一返回就得能给出 404/403,而 Start 之后的错误只落在
// session 退出码里,那时已经无法回状态码,只会让用户拿到一个 0 字节的下载。
func (w *execWorkspace) OpenRead(ctx context.Context, p string) (io.ReadCloser, error) {
	if _, err := w.Stat(ctx, p); err != nil {
		return nil, err
	}
	cmd := []string{"sh", "-c", `LC_ALL=C; cat -- "$1"`, "_", cleanRemote(p)}
	session, err := w.client.NewSession()
	if err != nil {
		return nil, err
	}
	out, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := session.Start(quoteArgs(cmd)); err != nil {
		_ = session.Close()
		return nil, err
	}
	return &sessionReader{rc: out, session: session}, nil
}

// sessionReader 把远端 session 的 stdout 当流读;Close 只关这条 session,不关连接
// (x/crypto 的 StdoutPipe 只给 io.Reader,收尾动作全落在 session 上)。
type sessionReader struct {
	rc      io.Reader
	session *ssh.Session
	once    sync.Once
}

func (r *sessionReader) Read(p []byte) (int, error) { return r.rc.Read(p) }

func (r *sessionReader) Close() error {
	r.once.Do(func() {
		// 同 streamReadCloser 的收尾纪律:先 SIGKILL 杀远端 cat(中途断开否则进程驻留),
		// 再关 session;连接留给 Workspace.Close 收。
		_ = r.session.Signal(ssh.SIGKILL)
		_ = r.session.Close()
	})
	return nil
}

// WriteFile 覆盖写:正文经 stdin 喂给远端临时文件,再 mv -f 覆盖原名。
// 走 stdin 而非 argv —— 上传不受命令长度上限约束(与 Upload 同一纪律)。
// 临时名 + mv 保证读者要么看到旧全文、要么看到新全文,不会读到编辑器保存到一半的正文。
func (w *execWorkspace) WriteFile(ctx context.Context, p string, r io.Reader) error {
	p = cleanRemote(p)
	script := `p=$1; t=$2
d=$(dirname "$p")
if [ ! -d "$d" ]; then echo ` + markNotFound + ` >&2; exit 1; fi
if [ ! -w "$d" ]; then echo ` + markNotPerm + ` >&2; exit 1; fi
if ! cat > "$t"; then rm -f "$t"; echo ` + markNotPerm + ` >&2; exit 1; fi
if ! mv -f "$t" "$p"; then rm -f "$t"; echo ` + markNotPerm + ` >&2; exit 1; fi`
	res, err := w.runWithStdin(ctx, script, r, p, p+execTmpSuffix)
	if err != nil {
		return err
	}
	return checkExec(res)
}

// runWithStdin 同 run,但把远端进程 stdin 接上 r(上传正文经此流入)。
func (w *execWorkspace) runWithStdin(ctx context.Context, script string, r io.Reader, args ...string) (*ExecResult, error) {
	cmd := append([]string{"sh", "-c", "LC_ALL=C; export LC_ALL; " + script, "_"}, args...)
	session, err := w.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	session.Stdin = r

	res := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	runErr := runWithContext(ctx, session, quoteArgs(cmd))
	if runErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			res.Stderr = stderr.String()
			return res, nil
		}
		return nil, runErr
	}
	res.Stderr = stderr.String()
	res.ExitCode = 0
	return res, nil
}

// Mkdir 递归建目录(等价 mkdir -p)。
func (w *execWorkspace) Mkdir(ctx context.Context, p string) error {
	res, err := w.run(ctx, `p=$1
if [ -e "$p" ] && [ ! -d "$p" ]; then echo `+markNotDir+` >&2; exit 1; fi
mkdir -p "$p" 2>/dev/null && exit 0
[ -d "$p" ] && exit 0
echo `+markNotPerm+` >&2; exit 1`, cleanRemote(p))
	if err != nil {
		return err
	}
	return checkExec(res)
}

// Remove 删文件或空目录;非空目录回 ErrRemoteNotEmpty —— 与 SFTP 实现同一道保险:
// 这层不提供 rm -rf,界面上就没有能一键清空目录树的按钮。
func (w *execWorkspace) Remove(ctx context.Context, p string) error {
	res, err := w.run(ctx, `p=$1
if [ ! -e "$p" ] && [ ! -L "$p" ]; then echo `+markNotFound+` >&2; exit 1; fi
d=$(dirname "$p")
if [ ! -w "$d" ]; then echo `+markNotPerm+` >&2; exit 1; fi
if [ -d "$p" ] && [ ! -L "$p" ]; then
  err=$(rmdir "$p" 2>&1) || {
    case $err in
      *"not empty"*) echo `+markNotEmpty+` >&2;;
      *) echo `+markNotPerm+` >&2;;
    esac
    exit 1
  }
else
  rm -f "$p"
fi`, cleanRemote(p))
	if err != nil {
		return err
	}
	return checkExec(res)
}

// Rename 走 mv -f:覆盖已存在目标是 mv 的语义,正好是面板「重命名/移动」要的。
func (w *execWorkspace) Rename(ctx context.Context, from, to string) error {
	res, err := w.run(ctx, `f=$1; t=$2
if [ ! -e "$f" ] && [ ! -L "$f" ]; then echo `+markNotFound+` >&2; exit 1; fi
d=$(dirname "$t")
if [ ! -d "$d" ]; then echo `+markNotFound+` >&2; exit 1; fi
if [ ! -w "$d" ]; then echo `+markNotPerm+` >&2; exit 1; fi
mv -f "$f" "$t" || echo `+markNotPerm+` >&2`, cleanRemote(from), cleanRemote(to))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !strings.Contains(res.Stderr, markNotPerm) {
		return fmt.Errorf("target: 远程改名失败(exit=%d)", res.ExitCode)
	}
	return checkExec(res)
}

// Close 收掉复用的 SSH 连接;幂等。
func (w *execWorkspace) Close() error {
	w.once.Do(func() { _ = w.client.Close() })
	return nil
}

// checkExec 把脚本哨兵折成领域错误。原始 stderr 留在 %s 里供人读诊断 ——
// 经 quoteArgs 传下去的只有路径,不可能含凭据。
func checkExec(res *ExecResult) error {
	if res == nil {
		return errors.New("target: 远程无执行结果")
	}
	out := res.Stdout + res.Stderr
	switch {
	case strings.Contains(out, markNotFound):
		return fmt.Errorf("%w: %s", ErrRemoteNotFound, strings.TrimSpace(res.Stderr))
	case strings.Contains(out, markNotDir):
		return fmt.Errorf("%w: %s", ErrRemoteNotDirectory, strings.TrimSpace(res.Stderr))
	case strings.Contains(out, markNotPerm):
		return fmt.Errorf("%w: %s", ErrRemotePermission, strings.TrimSpace(res.Stderr))
	case strings.Contains(out, markNotEmpty):
		return fmt.Errorf("%w: %s", ErrRemoteNotEmpty, strings.TrimSpace(res.Stderr))
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("target: 远程文件操作失败(exit=%d)", res.ExitCode)
	}
	return nil
}

// parseStatLine 解析 emit_entry 的一行:kind/mode/size/mtime/link/name。
// name 放在最后一列并配合 SplitN —— 文件名里有空格也不能翻车(制表符/换行是兜底路径
// 的已知边界,见文件头注释)。
func parseStatLine(line string) (FileStat, error) {
	parts := strings.SplitN(line, statFieldSep, 6)
	if len(parts) != 6 {
		return FileStat{}, fmt.Errorf("target: 无法解析远程条目 %q", line)
	}
	st := FileStat{
		Name:       parts[5],
		IsDir:      parts[0] == "d",
		IsLink:     parts[0] == "l",
		LinkTarget: parts[4],
		Mode:       parseModeString(parts[1]),
	}
	if s, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
		st.Size = s
	}
	if m, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
		st.MtimeUnix = m
	}
	return st, nil
}

// parseModeString 把 ls 的 9 位权限串(rw-r--r--)折成权限位。
//
// 为什么逐位判而不是八进制转换:suid/sgid/sticky 就落在这 9 位的 x/r 位置上,小写 s/t
// 表示「特殊位 + 对应执行位都置了」,大写 S/T 表示「特殊位置了但执行位是空的」。
// 直接取字符比对正好把这两种情况都判对,拿不到识别的字符按 0 处理(宁可少显示权限,
// 也不要在界面上凭空造出一个可写位)。
func parseModeString(s string) uint32 {
	if len(s) < 9 {
		return 0
	}
	const perms = "rwxrwxrwx"
	var mode uint32
	for i := 0; i < 9; i++ {
		switch c := s[i]; c {
		case perms[i]:
			mode |= 1 << uint(8-i)
		case 's', 't': // 特殊位且执行位置起
			if i == 2 || i == 5 || i == 8 {
				mode |= 1 << uint(8-i)
			}
		}
	}
	return mode
}
