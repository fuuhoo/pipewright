// identity_test.go 守的是包注释里那三条铁律里,HTTP 层够不着的接缝:
// 并发首登只产生一个账号、脏角色不白拿管理员、默认角色被删时拒建号而不是回退。
package identity

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/role"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
)

// fakeApps 是 LoginApps 的桩:本包的判定只读「启用/配全/白名单」这三件事。
// next / refreshErr 只服务续期扫描器 —— 令牌续得成续不成,是凭据降不降级的那条分岔。
type fakeApps struct {
	cfg        oauth.LoginAppConfig
	err        error
	next       *oauth.Grant
	refreshErr error
}

func (f fakeApps) LoginApp(context.Context, string) (*oauth.LoginAppConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	c := f.cfg
	return &c, nil
}

func (f fakeApps) RefreshGrant(context.Context, string, string) (*oauth.Grant, error) {
	if f.refreshErr != nil {
		return nil, f.refreshErr
	}
	if f.next == nil {
		return nil, fmt.Errorf("identity/桩: 未配置续期令牌")
	}
	g := *f.next
	return &g, nil
}

// loadRoles 把 0063 播种的四档预置装进进程内目录 —— viewer 不在代码表里,
// 不装载时 access.ValidRole("viewer") 一律 false,建号会走「角色不可用」那条分支。
func loadRoles(t *testing.T, db *sql.DB) {
	t.Helper()
	access.SetRoleStore(role.New(db))
	if err := access.ReloadRoles(context.Background()); err != nil {
		t.Fatalf("装载角色目录: %v", err)
	}
	t.Cleanup(func() { access.SetRoleStore(nil) })
}

func enabledApp(domains ...string) fakeApps {
	return fakeApps{cfg: oauth.LoginAppConfig{
		Provider: oauth.ProviderGitLab, ClientID: "cid", Enabled: true,
		Configured: true, AllowedDomains: domains,
	}}
}

func identityOf(externalID, login string) *oauth.Identity {
	return &oauth.Identity{Provider: oauth.ProviderGitLab, ExternalID: externalID, Login: login}
}

// TestConcurrentFirstLoginProvisionsSingleAccount 是 provisionMu 存在的理由:
// 同一个人(同 provider+external_id)并发首登只许建出一个账号。
func TestConcurrentFirstLoginProvisionsSingleAccount(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	svc := NewService(db, users.NewService(db), enabledApp(), nil)

	const n = 8
	var wg sync.WaitGroup
	accounts := make([]*Account, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accounts[i], errs[i] = svc.Login(context.Background(), identityOf("9001", "bob"), nil)
		}(i)
	}
	wg.Wait()

	var firstID string
	for i := range accounts {
		if errs[i] != nil {
			t.Fatalf("并发首登第 %d 个请求失败: %v", i, errs[i])
		}
		if firstID == "" {
			firstID = accounts[i].UserID
		}
		if accounts[i].UserID != firstID {
			t.Fatalf("并发首登建出了不同账号:%s vs %s", firstID, accounts[i].UserID)
		}
	}
	var userRows int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM users WHERE username = 'bob'`).Scan(&userRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if userRows != 1 {
		t.Fatalf("并发首登应只建 1 个账号,实际 %d 个", userRows)
	}
	var nb int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM user_identities WHERE external_id = '9001'`).Scan(&nb); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if nb != 1 {
		t.Fatalf("并发首登应只落 1 条映射,实际 %d 条", nb)
	}
}

// TestBoundAccountWithEmptyRoleIsRefused:role=” 是 0053 之前旧会话的取值,语义是「按管理员」。
// 映射指向的账号若查出空串必须拒登 —— 绝不能因为脏数据白拿一个管理员会话。
func TestBoundAccountWithEmptyRoleIsRefused(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	u := users.NewService(db)
	svc := NewService(db, u, enabledApp(), nil)

	hash, err := auth.HashPassword("whatever-pass-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	created, err := u.Create(users.CreateInput{Username: "legacy", PasswordHash: hash, Role: "user"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET role = '' WHERE id = ?`, created.ID); err != nil {
		t.Fatalf("脏化 role: %v", err)
	}
	if _, err := svc.Bind(context.Background(), created.ID, identityOf("9002", "legacy"), nil); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if _, err := svc.Login(context.Background(), identityOf("9002", "legacy"), nil); err != ErrAccountUnavailable {
		t.Fatalf("空角色账号登录 err = %v, want ErrAccountUnavailable", err)
	}
}

// TestProvisionRefusedWhenDefaultRoleMissing:管理员删掉 viewer 档之后,建号必须明确失败,
// 而不是像 users.Create 那样静默回退到 'user'(那一档权限明显更大)。
func TestProvisionRefusedWhenDefaultRoleMissing(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	if _, err := db.Exec(`DELETE FROM role_perms WHERE role_id = ?`, DefaultRole); err != nil {
		t.Fatalf("清理点集: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM roles WHERE id = ?`, DefaultRole); err != nil {
		t.Fatalf("删掉 viewer 档: %v", err)
	}
	// 重载后 viewer 不再存在于目录里(它不是内置档,代码表兜不住)。
	if err := access.ReloadRoles(context.Background()); err != nil {
		t.Fatalf("重载角色目录: %v", err)
	}

	svc := NewService(db, users.NewService(db), enabledApp(), nil)
	if _, err := svc.Login(context.Background(), identityOf("9003", "nobody"), nil); err != ErrRoleUnavailable {
		t.Fatalf("默认角色已删时 err = %v, want ErrRoleUnavailable", err)
	}
	var n int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM users WHERE username = 'nobody'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("角色不可用时不该建出任何账号,得到 %d 个", n)
	}
}

// TestSuggestUsernameStaysInsideTheAllowedCharset 守的是建号最后一步:
// 平台给的名字千奇百怪(中文昵称、带空格的显示名),派生出的本地用户名必须撞得进
// users.username 的字符集,否则访客看到的是一句说不清的登录失败。
func TestSuggestUsernameStaysInsideTheAllowedCharset(t *testing.T) {
	cases := []struct {
		name string
		id   *oauth.Identity
		want string
	}{
		{
			// 企业微信:查档案失败时 Login 退成 userid、主键形如 corpid/userid。
			// '/' 落在截断窗口内,照原文洗不洗都行不通 —— 必须逐字过滤再截。
			name: "企业微信 corpid/userid 主键",
			id:   &oauth.Identity{Provider: oauth.ProviderWeCom, ExternalID: "wp-corp-9/alice", Login: ""},
			want: "wecom-wp-corp-9ali",
		},
		{
			name: "中文昵称洗空后退主键",
			id:   &oauth.Identity{Provider: oauth.ProviderWeCom, ExternalID: "ww1234567/chen", Login: "张三"},
			want: "wecom-ww1234567che",
		},
		{
			name: "合法登录名原样保留",
			id:   &oauth.Identity{Provider: oauth.ProviderGitLab, ExternalID: "42", Login: " Fu Bin "},
			want: "FuBin",
		},
		{
			// IdP 只回邮箱时的退路:'@' 换成点,别把域名黏到名字后面冒充邮箱。
			name: "邮箱登录名把 @ 换成点",
			id:   &oauth.Identity{Provider: oauth.ProviderOIDC, ExternalID: "uuid-1", Login: "fuhongbin@swinfo.com.cn"},
			want: "fuhongbin.swinfo.com.cn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestUsername(tc.id)
			if got != tc.want {
				t.Fatalf("suggestUsername = %q, want %q", got, tc.want)
			}
			if err := checkUsernameCharset(got); err != nil {
				t.Fatalf("派生的用户名 %q 进不了 users.username: %v", got, err)
			}
			// 同一个身份再算一次必须得到同一个名字:名字变了就等于换人,
			// 下次登录会按 (provider, external_id) 命中这个账号,派生口径必须稳定。
			if again := suggestUsername(tc.id); again != got {
				t.Fatalf("派生不稳定: %q vs %q", got, again)
			}
		})
	}
}

// checkUsernameCharset 复刻 internal/users.validateUsername 的字符集与长度规则。
// 刻意抄一份而不调用它:users 那个函数未导出,而且这里要的是「两边口径一致」这条约束本身 ——
// 哪天 users 放宽或收紧,单靠编译期引用发现不了,派生名照样会撞库。
func checkUsernameCharset(u string) error {
	if len(u) < 2 || len(u) > 64 {
		return fmt.Errorf("需 2~64 字符,得到 %d", len(u))
	}
	for _, r := range u {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("非法字符 %q", r)
		}
	}
	return nil
}

// TestDomainWhitelistRequiresEmail:白名单非空时「拿不到邮箱」必须拒,
// 而不是把查不到当成「不限制」—— 那是把白名单变成摆设的最短路径。
func TestDomainWhitelistRequiresEmail(t *testing.T) {
	if err := checkDomain([]string{"corp.example.com"}, ""); err != ErrDomainNotAllowed {
		t.Fatalf("空邮箱 err = %v, want ErrDomainNotAllowed", err)
	}
	if err := checkDomain([]string{"corp.example.com"}, "bob@sub.corp.example.com"); err != ErrDomainNotAllowed {
		t.Fatalf("子域名不该命中父域白名单: %v", err)
	}
	if err := checkDomain(nil, ""); err != nil {
		t.Fatalf("空白名单 = 不限域名,得到 %v", err)
	}
}
