package main

// 子命令 `pipewright reset-admin-password`:在本机把内置管理员的口令改掉。
//
// 为什么要有这条线:口令是 argon2id 的 PHC 串(带随机 salt),sqlite 命令行里造不出合法值,
// 而「忘了管理员口令」恰恰是最需要在没有浏览器、没有可用会话的那台机器上自救的一次。
// 接口那条路(网页「用户管理」/ POST /api/admin/users/{id}/password)要求先有一个能登录的
// 管理员,对这种情况没用;直接 UPDATE 库又容易只改一张表 —— 内置管理员的真身在 admin_user,
// auth.Login 优先命中它,users 表那行只是同步影:少写它口令照样能用,但账号清单与角色归属
// 会和口令脱节。所以两张表一起写是本命令的底线,哈希也复用登录期同一套实现,不造第二口径。
//
// 只管内置管理员(admin_user.id=1)。普通账号与「管理员建出来的额外管理员」口令只在 users 表,
// 该走接口(前提是你还能用另一个管理员登录),这里刻意不越界。

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/config"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/users"
)

// resetPWConfig 是 `pipewright reset-admin-password` 解析出来的东西:新口令从哪来、
// 空库时用什么用户名建号、允许不允许在没有库文件的情况下开工。
type resetPWConfig struct {
	plain        string // 新口令明文(三种来源解析后的结果)
	usernameHint string // 仅库里还没有 admin_user 行时用于建号
	usedGenerate bool   // 口令是本命令生成的 → 成功时打印一次
	newDBOK      bool   // --create-missing:库文件不存在时允许新建
}

// generateLen 是 --generate 产出的口令长度。字符表去掉易混字符(i/l/1/o/0 与它们的
// 大写 I/O),因为这串字符是要人手打进登录框的。
// 字母表本身必须真的不含这些字符 —— 之前小写档漏掉了 i,注释与测试都承诺去掉,
// 于是随机样本约三成踩中,CI 一阵红。
const (
	generateLen      = 20
	generateAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// ambiguousRunes 是字母表里不该出现的易混字符(两种大小写都算)。
const ambiguousRunes = "il1oOI0"

func parseResetPasswordFlags(args []string, lookupEnv func(string) string, stdin io.Reader) (resetPWConfig, error) {
	fs := flag.NewFlagSet("reset-admin-password", flag.ContinueOnError)
	// 口令不许做成位置参数或普通 flag:命令行会被同机用户从 ps 里看到,也会进 shell 历史。
	// 变量名不能叫 stdin:它会遮住参数里那个 io.Reader,测试就再也喂不进假输入了。
	fromStdin := fs.Bool("stdin", false, "从标准输入读一行新口令(管道/脚本用)")
	file := fs.String("password-file", "", "从文件读新口令(整个文件内容即口令,允许尾随换行)")
	generate := fs.Bool("generate", false, "自动生成强口令,只在成功时打印一次")
	username := fs.String("username", defaultAdminUsername(lookupEnv), "库里还没有管理员时用它建号;已有管理员时不改名")
	newDBOK := fs.Bool("create-missing", false, "解析到的 sqlite 库文件不存在时,允许新建一个空库并建号(只在冷启动时用)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `pipewright reset-admin-password —— 在本机重置内置管理员口令(不需要能登录)

改的是当前配置解析出来的那个库(PIPEWRIGHT_DB / PIPEWRIGHT_DB_DRIVER + PIPEWRIGHT_DB_DSN),
用的是与登录同一套 argon2id 哈希;admin_user 与 users 的同步行一次写齐,下一次登录即生效。
只处理内置管理员;普通账号与额外管理员请在网页「用户管理」里重置(那里要求先用另一个管理员登录)。

库路径默认是「相对当前目录」的 ./pipewright.db,所以「在哪个目录跑」决定改的是哪个库:
在子目录里跑,解析到的就是那个子目录下的一个不存在的文件,本命令会因此停下(而不是顺手
在旁边建一个空库、把口令设给一个不存在的管理员,再回一句成功)。切到服务用的那个目录,
或用 PIPEWRIGHT_DB 写全路径;确实要从空库冷启动,加 --create-missing。

新口令必须显式给一个来源(--stdin / --password-file / --generate)。刻意不回读
PIPEWRIGHT_ADMIN_PASSWORD:compose 形态里它常驻容器的环境,「一条参数都没输却把口令改成了
env 里那个」是这条自救命令最不该有的行为。想用它就自己管道进去。

`)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, `
例:
  pipewright reset-admin-password --stdin < newpass.txt
  pipewright reset-admin-password --password-file /run/secrets/pw
  pipewright reset-admin-password --generate

容器形态用镜像里的 /pipewright(它不在 PATH 上,要写全路径)。docker exec 默认落在 /data,
那正是库所在的地方;要是 cd 进了子目录(如 /data/artifacts),把目录切回来或写全路径:
  docker exec -it <容器名> /pipewright reset-admin-password --stdin
  docker exec -it <容器名> sh -c 'cd /data && /pipewright reset-admin-password --stdin'
  docker exec -it -e PIPEWRIGHT_DB=/data/pipewright.db <容器名> /pipewright reset-admin-password --stdin
`)
	}
	if err := fs.Parse(args); err != nil {
		return resetPWConfig{}, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		fs.Usage()
		return resetPWConfig{}, fmt.Errorf("不认识的参数:%s", strings.Join(rest, " "))
	}

	// 三选一:同时给两种来源(比如 --generate 又 --password-file)时,哪条赢全看写代码的顺序,
	// 这种「口令由谁定」的不确定不该交给一次账号恢复。
	n := 0
	for _, on := range []bool{*fromStdin, *file != "", *generate} {
		if on {
			n++
		}
	}
	if n > 1 {
		return resetPWConfig{}, errors.New("--stdin / --password-file / --generate 三选一,别同时给")
	}

	cfg := resetPWConfig{usernameHint: strings.TrimSpace(*username), newDBOK: *newDBOK}
	switch {
	case *fromStdin:
		plain, err := readFirstLine(stdin)
		if err != nil {
			return resetPWConfig{}, err
		}
		cfg.plain = plain
	case *file != "":
		plain, err := readPasswordFile(*file)
		if err != nil {
			return resetPWConfig{}, err
		}
		cfg.plain = plain
	case *generate:
		plain, err := generatePassword(generateLen)
		if err != nil {
			return resetPWConfig{}, err
		}
		cfg.plain, cfg.usedGenerate = plain, true
	default:
		fs.Usage()
		return resetPWConfig{}, errors.New("要说清口令从哪来:--stdin、--password-file 或 --generate")
	}
	return cfg, nil
}

func defaultAdminUsername(lookupEnv func(string) string) string {
	if v := strings.TrimSpace(lookupEnv("PIPEWRIGHT_ADMIN_USERNAME")); v != "" {
		return v
	}
	return "admin"
}

// readFirstLine 取第一行并去掉换行与首尾空白 —— 粘贴口令时多带一个空格很常见,
// 不该因此变成一个不同的口令。
func readFirstLine(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("读标准输入: %w", err)
	}
	line := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	if line == "" {
		return "", errors.New("标准输入是空的,没读到口令")
	}
	return line, nil
}

func readPasswordFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读口令文件: %w", err)
	}
	plain := strings.TrimSpace(string(b))
	if plain == "" {
		return "", fmt.Errorf("%s 是空的,没读到口令", path)
	}
	return plain, nil
}

// generatePassword 用 crypto/rand 从去掉易混字符的字母表里取 n 位。
func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成口令: %w", err)
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = generateAlphabet[int(v)%len(generateAlphabet)]
	}
	return string(out), nil
}

// resetResult 报告这一次到底干了什么:用户名、有没有顺带建号。
type resetResult struct {
	username string
	created  bool
}

// resetAdminPassword 把 plain 哈希后写进 admin_user(id=1),并同步 users 里那一行。
//
// 已有管理员时只用库里现成的用户名:用户名是登录凭据,拿它当改名手段会让账号看起来
// 「新口令也不对」。空库时按 usernameHint 建号,顺带覆盖「管理员还没配好」的冷启动。
func resetAdminPassword(db *sql.DB, usersSvc *users.Service, plain, usernameHint string) (*resetResult, error) {
	if len(plain) < users.MinPasswordLen {
		return nil, fmt.Errorf("口令太短:%d 位,至少 %d 位", len(plain), users.MinPasswordLen)
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return nil, fmt.Errorf("哈希口令: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res := &resetResult{}
	var existing string
	err = db.QueryRow(`SELECT username FROM admin_user WHERE id = 1`).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if usernameHint == "" {
			return nil, errors.New("库里没有管理员,又没给用户名(--username)可建号")
		}
		res.username, res.created = usernameHint, true
		if _, err := db.Exec(
			`INSERT INTO admin_user (id, username, password_hash, created_at, updated_at) VALUES (1, ?, ?, ?, ?)`,
			res.username, hash, now, now,
		); err != nil {
			return nil, fmt.Errorf("写入 admin_user: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("查管理员: %w", err)
	default:
		if usernameHint != "" && usernameHint != existing {
			return nil, fmt.Errorf("库里已有管理员 %q,--username 只在建号时用(要改名请走网页「账户设置」)", existing)
		}
		res.username = existing
		if _, err := db.Exec(
			`UPDATE admin_user SET password_hash = ?, updated_at = ? WHERE id = 1`,
			hash, now,
		); err != nil {
			return nil, fmt.Errorf("更新 admin_user: %w", err)
		}
	}

	// 同步 users 那一行(固定 UUID;role=admin、enabled=1 由该方法硬保证,与首次引导同一条路)。
	if err := usersSvc.BootstrapAdminRow(res.username, hash); err != nil {
		return nil, fmt.Errorf("同步 users 表: %w", err)
	}
	return res, nil
}

// runResetPasswordCommand 开库、改口令、收工,返回进程退出码。
//
// 到此为止:不装配平台、不监听端口、不起后台任务。它要的只是同一份配置(库路径、驱动)
// 和同一段哈希实现;把平台那一整套跟着跑一遍,等于把一次自救变成一次重启。
func runResetPasswordCommand(args []string, cfg config.Config) int {
	pw, err := parseResetPasswordFlags(args, os.Getenv, os.Stdin)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipewright reset-admin-password: %v\n", err)
		return 2
	}

	dbDriver, dbDSN, err := cfg.StoreConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipewright reset-admin-password: %v\n", err)
		return 2
	}
	if err := guardMissingDatabase(dbDriver, dbDSN, pw.newDBOK); err != nil {
		fmt.Fprintf(os.Stderr, "pipewright reset-admin-password: %v\n", err)
		return 2
	}
	st, err := store.OpenWithConfig(store.OpenConfig{Driver: dbDriver, DSN: dbDSN})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipewright reset-admin-password: 打开数据库失败: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	res, err := resetAdminPassword(st.DB, users.NewService(st.DB), pw.plain, pw.usernameHint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipewright reset-admin-password: %v\n", err)
		return 1
	}

	if pw.usedGenerate {
		// 这是明文唯一一次出现:之后库里只剩 argon2id 摘要,谁也拿不回去。
		fmt.Printf("新生成的口令(只显示这一次):%s\n", pw.plain)
	}
	verb := "已重置内置管理员口令"
	if res.created {
		verb = "已为内置管理员设好口令"
	}
	fmt.Printf("%s:username=%s,数据库=%s\n", verb, res.username, describeDBTarget(dbDriver, dbDSN))
	fmt.Println("admin_user(id=1) 与 users 的同步行已一起更新;下一次登录即用新口令。")
	if notice := liveInstanceNotice(cfg.Addr); notice != "" {
		fmt.Fprintln(os.Stderr, notice)
	}
	return 0
}

// describeDBTarget 把「改的是哪个库」打成人读的一句话:mysql 只报驱动,
// 免得连接串里的账号密码进终端历史。
func describeDBTarget(driver, dsn string) string {
	if driver == "mysql" {
		return "mysql(连接串未显示)"
	}
	if abs, err := filepath.Abs(dsn); err == nil {
		return abs
	}
	return dsn
}

// guardMissingDatabase 在解析出来的 sqlite 文件根本不存在时停住,除非 --create-missing。
//
// 为什么要拦:库路径的默认值是「相对当前目录」的 pipewright.db,而自救最常发生的场合是
// docker exec 进容器后又 cd 到了某个子目录(如 /data/artifacts)。在那儿跑一次,store.Open
// 会在旁边新建一个空库、迁移建齐表,然后走「空库建号」那条路给一个并不存在的管理员设好口令,
// 末了回一句成功 —— 正在跑的那个库一个字节没动,登录依旧失败,而人只会以为「命令不管用」。
// 判据用文件存不存在,不用「库里有没有管理员」:服务既然在跑,它用的库必然已经存在;
// 「有文件但还没管理员」仍是合法的冷启动,照旧放行。
// mysql 没有文件可 stat,跳过(而且连不上就是连不上,静默不成功)。
func guardMissingDatabase(driver, dsn string, allowCreate bool) error {
	if allowCreate || driver != "sqlite" {
		return nil
	}
	path := sqliteDBFilePath(dsn)
	if path == "" || strings.HasPrefix(path, ":memory:") {
		return nil // 内存库之类的非常规 DSN:没有文件可查,交给 store.Open 自己判定
	}
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	_, statErr := os.Stat(abs)
	if statErr == nil {
		return nil
	}
	if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("查数据库文件 %s: %w", abs, statErr)
	}
	cwd, _ := os.Getwd()
	return fmt.Errorf(
		"数据库文件不存在:%s(当前目录 %s)\n"+
			"这里不建空库 —— 在子目录里跑这条命令,改的会是那个子目录旁边的新库,正在跑的实例用的库不受影响。\n"+
			"切到服务真正在用的一侧:cd 到它的工作目录(容器形态通常是 /data),或写全路径 PIPEWRIGHT_DB=/data/pipewright.db;\n"+
			"确实要从空库冷启动,再加 --create-missing。",
		abs, cwd,
	)
}

// sqliteDBFilePath 从 sqlite DSN 里取出文件路径:去掉查询串与 file: 前缀。
// 本仓库自己拼的就是「路径?_pragma=...」,而 PIPEWRIGHT_DB_DSN 由用户给,两种写法都得认。
func sqliteDBFilePath(dsn string) string {
	path := dsn
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return strings.TrimPrefix(path, "file:")
}

// liveInstanceNotice 在监听地址上有实例时提一句:库是同一个的话新口令即刻生效。
// 这只提示、不拦 —— 容器形态里最常见的用法就是 `docker exec` 进正在跑的那个容器改口令,
// 拦下来只会逼人加一个每次都加的 --force。
func liveInstanceNotice(addr string) string {
	if addr == "" {
		addr = ":8080"
	}
	conn, err := net.DialTimeout("tcp", canonicalTCPAddr(addr), 300*time.Millisecond)
	if err != nil {
		return ""
	}
	_ = conn.Close()
	return fmt.Sprintf("提示:监听地址 %s 上有实例在运行;若它用的就是刚才那个库,新口令下一次登录即刻生效(无需重启)。", addr)
}

// canonicalTCPAddr 把 ":8080" / "8080" 这类写法补成 net.Dial 认的 host:port。
func canonicalTCPAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	if _, err := net.LookupPort("tcp", addr); err == nil {
		return "127.0.0.1:" + addr
	}
	return addr
}
