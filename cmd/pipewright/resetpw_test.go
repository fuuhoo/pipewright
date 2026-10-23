// resetpw_test.go 守两条线:
//  1. 口令来源只能有一处(--stdin / --password-file / --generate),且绝不接受把它写进命令行
//     参数或回读 PIPEWRIGHT_ADMIN_PASSWORD —— 前者同机用户能从 ps 里看到、shell 历史里也留一份,
//     后者会让 compose 形态里「一条参数都没输」变成把口令改回 env 里那个。
//  2. 落库必须一次写齐 admin_user 与 users 两行,且已有管理员时绝不能被 --username 改名:
//     auth.Login 优先命中 admin_user,只改影行是「看着成功了、其实没改」,而误改用户名会让
//     新口令一起变得「登不进去」。
package main

import (
	"database/sql"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/config"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/users"
)

func staticEnv(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestParseResetPasswordFlags(t *testing.T) {
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pwFile, []byte("  FilePass123\n"), 0o600); err != nil {
		t.Fatalf("写口令文件: %v", err)
	}

	for _, tc := range []struct {
		name  string
		args  []string
		env   map[string]string
		stdin string
		want  resetPWConfig
	}{
		{
			name:  "标准输入,尾随换行与空白都收掉",
			args:  []string{"--stdin"},
			stdin: "  PipedPass123 \n忽略第二行\n",
			want:  resetPWConfig{plain: "PipedPass123", usernameHint: "admin"},
		},
		{
			name: "口令文件整份内容",
			args: []string{"--password-file", pwFile},
			want: resetPWConfig{plain: "FilePass123", usernameHint: "admin"},
		},
		{
			name:  "建号用户名走 env",
			args:  []string{"--stdin"},
			env:   map[string]string{"PIPEWRIGHT_ADMIN_USERNAME": "fubin"},
			stdin: "Whatever123\n",
			want:  resetPWConfig{plain: "Whatever123", usernameHint: "fubin"},
		},
		{
			name:  "命令行用户名压过 env",
			args:  []string{"--stdin", "--username", "ops"},
			env:   map[string]string{"PIPEWRIGHT_ADMIN_USERNAME": "fubin"},
			stdin: "Whatever123\n",
			want:  resetPWConfig{plain: "Whatever123", usernameHint: "ops"},
		},
		{
			name:  "等号与单横线",
			args:  []string{"-stdin=true"},
			stdin: "EqualPass123",
			want:  resetPWConfig{plain: "EqualPass123", usernameHint: "admin"},
		},
		{
			// 冷启动要新建库是显式意图,得靠开关表态 —— 默认那侧永远是「不许凭空建库」。
			name:  "冷启动开关",
			args:  []string{"--stdin", "--create-missing"},
			stdin: "ColdStart123",
			want:  resetPWConfig{plain: "ColdStart123", usernameHint: "admin", newDBOK: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResetPasswordFlags(tc.args, staticEnv(tc.env), strings.NewReader(tc.stdin))
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseResetPasswordFlagsGenerates(t *testing.T) {
	got, err := parseResetPasswordFlags([]string{"--generate"}, staticEnv(nil), strings.NewReader(""))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !got.usedGenerate {
		t.Fatal("--generate 要标记出来:成功时要把明文打印一次,否则口令没人知道")
	}
	if len(got.plain) != generateLen {
		t.Fatalf("生成口令长度 = %d, want %d", len(got.plain), generateLen)
	}
	for _, r := range got.plain {
		if !strings.ContainsRune(generateAlphabet, r) {
			t.Fatalf("生成口令含字母表外的字符:%q", got.plain)
		}
	}
	// 易混字符不该出现:这串是要人手敲进登录框的。判据落在字母表常量上,
	// 不落在这一轮的随机样本上 —— 拿样本来断言,同一个缺陷要靠运气才测得出来。
	if strings.ContainsAny(generateAlphabet, ambiguousRunes) {
		t.Fatalf("字母表含易混字符:%q", generateAlphabet)
	}
	if strings.ContainsAny(got.plain, ambiguousRunes) {
		t.Fatalf("生成口令含易混字符:%q", got.plain)
	}
	other, _ := parseResetPasswordFlags([]string{"--generate"}, staticEnv(nil), strings.NewReader(""))
	if other.plain == got.plain {
		t.Fatal("两次 --generate 出了同一个口令,随机源没生效")
	}
}

func TestParseResetPasswordFlagsRejects(t *testing.T) {
	dir := t.TempDir()
	emptyFile := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte("\n"), 0o600); err != nil {
		t.Fatalf("写空文件: %v", err)
	}
	missing := filepath.Join(dir, "nope.txt")

	for _, tc := range []struct {
		name    string
		args    []string
		env     map[string]string
		stdin   string
		wantErr string
	}{
		{"口令当位置参数(会漏进 ps)", []string{"SuperPass123"}, nil, "", "不认识的参数"},
		{"口令当 flag(同样会漏进 ps)", []string{"--password", "SuperPass123"}, nil, "", "flag provided but not defined"},
		{"两种来源同时给", []string{"--stdin", "--generate"}, nil, "Whatever123", "三选一"},
		{"文件和 stdin 同时给", []string{"--stdin", "--password-file", emptyFile}, nil, "Whatever123", "三选一"},
		{"没给任何来源", nil, nil, "", "要说清口令从哪来"},
		// compose 形态里 PIPEWRIGHT_ADMIN_PASSWORD 常驻容器环境:裸跑一条命令绝不能把它当成口令来源。
		{"env 里有口令也不算来源", nil, map[string]string{"PIPEWRIGHT_ADMIN_PASSWORD": "EnvPass123"}, "", "要说清口令从哪来"},
		{"标准输入是空的", []string{"--stdin"}, nil, "\n", "标准输入是空的"},
		{"口令文件是空的", []string{"--password-file", emptyFile}, nil, "", "是空的"},
		{"口令文件不存在", []string{"--password-file", missing}, nil, "", "读口令文件"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseResetPasswordFlags(tc.args, staticEnv(tc.env), strings.NewReader(tc.stdin))
			if err == nil {
				t.Fatal("该报错,却没报")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("报错没点到位:%v(期望含 %q)", err, tc.wantErr)
			}
		})
	}
}

func TestParseResetPasswordFlagsHelp(t *testing.T) {
	_, err := parseResetPasswordFlags([]string{"--help"}, staticEnv(nil), strings.NewReader(""))
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help 该回 flag.ErrHelp,实得:%v", err)
	}
}

// openTempStore 起一个跑完全部迁移的临时库,与平台启动开的是同一条路(store.Open)。
func openTempStore(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pw.db"))
	if err != nil {
		t.Fatalf("开临时库: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB
}

func adminHashes(t *testing.T, db *sql.DB) (username, adminHash, userHash, role string, enabled int) {
	t.Helper()
	if err := db.QueryRow(`SELECT username, password_hash FROM admin_user WHERE id = 1`).Scan(&username, &adminHash); err != nil {
		t.Fatalf("读 admin_user: %v", err)
	}
	if err := db.QueryRow(`SELECT password_hash, role, enabled FROM users WHERE id = ?`,
		users.BootstrapAdminRegularUserID).Scan(&userHash, &role, &enabled); err != nil {
		t.Fatalf("读 users 同步行: %v", err)
	}
	return username, adminHash, userHash, role, enabled
}

func TestResetAdminPasswordCreatesOnEmptyDB(t *testing.T) {
	db := openTempStore(t)
	const plain = "FreshPass123"

	res, err := resetAdminPassword(db, users.NewService(db), plain, "fubin")
	if err != nil {
		t.Fatalf("空库该建号却报错: %v", err)
	}
	if !res.created || res.username != "fubin" {
		t.Fatalf("结果不对:%+v", res)
	}

	username, adminHash, userHash, role, enabled := adminHashes(t, db)
	if username != "fubin" {
		t.Fatalf("admin_user 用户名 = %q", username)
	}
	if adminHash != userHash {
		t.Fatal("两张表的 hash 不一致:登录按 admin_user 判定,users 影行必须同步")
	}
	if role != users.RoleAdmin || enabled != 1 {
		t.Fatalf("users 同步行角色/启用不对:role=%q enabled=%d", role, enabled)
	}
	ok, err := auth.VerifyPassword(plain, adminHash)
	if err != nil || !ok {
		t.Fatalf("新口令该能验过:ok=%v err=%v", ok, err)
	}
}

func TestResetAdminPasswordKeepsExistingUsernameAndRetiresOld(t *testing.T) {
	db := openTempStore(t)
	svc := users.NewService(db)

	first, err := resetAdminPassword(db, svc, "FirstPass123", "fubin")
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	// 第二次不带 --username:库里那个用户名是登录凭据,不能被默认值 admin 顶掉。
	again, err := resetAdminPassword(db, svc, "SecondPass456", "")
	if err != nil {
		t.Fatalf("重置: %v", err)
	}
	if again.created || again.username != first.username {
		t.Fatalf("第二次不该建号也不该改名:%+v", again)
	}

	username, adminHash, userHash, _, _ := adminHashes(t, db)
	if username != "fubin" {
		t.Fatalf("用户名被改了:%q", username)
	}
	if adminHash != userHash {
		t.Fatal("重置后两表 hash 脱节")
	}
	if ok, _ := auth.VerifyPassword("SecondPass456", adminHash); !ok {
		t.Fatal("新口令验不过")
	}
	if ok, _ := auth.VerifyPassword("FirstPass123", adminHash); ok {
		t.Fatal("旧口令居然还能验过,说明没真的改")
	}
}

func TestResetAdminPasswordRejects(t *testing.T) {
	db := openTempStore(t)
	svc := users.NewService(db)
	if _, err := resetAdminPassword(db, svc, "FirstPass123", "fubin"); err != nil {
		t.Fatalf("建号: %v", err)
	}
	before, _, _, _, _ := adminHashes(t, db)

	for _, tc := range []struct {
		name    string
		plain   string
		hint    string
		wantErr string
	}{
		{"口令短于 8 位", "Ab1", "", "口令太短"},
		{"口令恰好 7 位", "Ab1cdef", "", "口令太短"},
		{"--username 想改名", "BrandNewPass1", "admin", "只在建号时用"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resetAdminPassword(db, svc, tc.plain, tc.hint)
			if err == nil {
				t.Fatal("该报错,却没报")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("报错没点到位:%v(期望含 %q)", err, tc.wantErr)
			}
			// 报错必须是「什么都没改」:半途失败留下一个既不是旧口令也不是新口令的库最难查。
			if after, _, _, _, _ := adminHashes(t, db); after != before {
				t.Fatal("失败的调用动过 admin_user")
			}
		})
	}
}

func TestResetAdminPasswordTooShortLeavesDBUntouched(t *testing.T) {
	db := openTempStore(t)
	if _, err := resetAdminPassword(db, users.NewService(db), "Ab1", "ops"); err == nil {
		t.Fatal("短口令该被拒")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM admin_user`).Scan(&n); err != nil {
		t.Fatalf("数 admin_user: %v", err)
	}
	if n != 0 {
		t.Fatalf("失败的建号留下了 %d 行半吊子管理员", n)
	}
}

func TestDescribeDBTargetHidesMySQLDSN(t *testing.T) {
	if got := describeDBTarget("mysql", "user:secretpw@tcp(10.0.0.1:3306)/pw"); !strings.Contains(got, "未显示") {
		t.Fatalf("mysql 连接串该被隐去,实得:%q", got)
	}
	if got := describeDBTarget("sqlite", "pw.db"); !strings.HasSuffix(got, "pw.db") {
		t.Fatalf("sqlite 该报出绝对路径,实得:%q", got)
	}
}

// TestGuardMissingDatabase 守的是这次真发生的事故:在子目录里跑,解析到一个不存在的
// pipewright.db,命令于是新建空库、给不存在的管理员设口令、还回一句成功。
func TestGuardMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "pipewright.db")
	if err := os.WriteFile(existing, []byte("占位:真内容无所谓,守卫只看存不存在"), 0o600); err != nil {
		t.Fatalf("写库文件: %v", err)
	}
	missing := filepath.Join(dir, "sub", "pipewright.db")

	for _, tc := range []struct {
		name        string
		driver      string
		dsn         string
		allowCreate bool
		wantErr     string
	}{
		{"库文件在,放行", "sqlite", existing, false, ""},
		{"带查询串的 DSN 也能认出路径", "sqlite", existing + "?_pragma=busy_timeout(5000)", false, ""},
		{"file: 前缀也能认出路径", "sqlite", "file:" + existing, false, ""},
		{"库文件不在,拦下并说要它在哪", "sqlite", missing, false, "不存在"},
		{"相对路径按当前目录解析", "sqlite", "definitely-not-here-9f3a2b.db", false, "不存在"},
		{"冷启动加开关就放行", "sqlite", missing, true, ""},
		{"已有的库加不加开关都放行", "sqlite", existing, true, ""},
		{"mysql 没有文件可 stat,不管", "mysql", "user:pw@tcp(127.0.0.1:3306)/db", false, ""},
		{"内存库不管", "sqlite", ":memory:", false, ""},
		{"空 DSN 不管(交给打开那步报错)", "sqlite", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := guardMissingDatabase(tc.driver, tc.dsn, tc.allowCreate)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("不该拦:%v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("该拦下,却放行了")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("报错没点到位:%v(期望含 %q)", err, tc.wantErr)
			}
		})
	}

	// 拦下来必须是「什么都没留下」:半途建出的空库会被下一次运行当成合法目标。
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("守卫不该建库文件,stat 得到:%v", err)
	}
}

func TestSQLiteDBFilePath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"pipewright.db", "pipewright.db"},
		{"/data/pipewright.db?_pragma=journal_mode(wal)", "/data/pipewright.db"},
		{"file:/data/pipewright.db?_pragma=busy_timeout(5000)", "/data/pipewright.db"},
		{"", ""},
	} {
		if got := sqliteDBFilePath(tc.in); got != tc.want {
			t.Fatalf("sqliteDBFilePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRunResetPasswordCommandRefusesMissingDB 端到端确认这道守卫真接在开库之前:
// 缺库时退出码 2,且没有在旁边留下一个新库。
func TestRunResetPasswordCommandRefusesMissingDB(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dbPath := filepath.Join(dir, "sub", "pipewright.db")

	pwFile := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pwFile, []byte("GuardPass123\n"), 0o600); err != nil {
		t.Fatalf("写口令文件: %v", err)
	}
	code := runResetPasswordCommand([]string{"--password-file", pwFile}, config.Config{
		DBDriver: "sqlite",
		DBPath:   dbPath,
		Addr:     "127.0.0.1:1", // 探不到实例,少一行无关输出
	})
	if code != 2 {
		t.Fatalf("缺库该以 2 退出,实得:%d", code)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("拒绝的一跑不该留下库文件,stat 得到:%v", err)
	}
}

func TestCanonicalTCPAddr(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":8080", "127.0.0.1:8080"},
		{"8080", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"[::1]:9090", "[::1]:9090"},
	} {
		if got := canonicalTCPAddr(tc.in); got != tc.want {
			t.Fatalf("canonicalTCPAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
