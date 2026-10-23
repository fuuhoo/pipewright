package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/role"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// access_guard_points_test.go —— 落点这条线「按点判定」的契约。
//
// 要钉住的回归:主机终端 / 远程文件 / 容器终端曾按 KindServer 的**类别上限**判定,
// 而 server.view、server.exec、container.operate、cert.manage 全是 KindServer ——
// 只要持有其中任一 operate 点,上限就是 operate,于是「取消勾选 server.exec」在后端毫无效果。
// 这里用两个只给一条能力线的角色,把三入口的放行/拦下逐格钉死。

type pointEnv struct {
	srv    *httptest.Server
	id     string // 未归组服务器(数据轴不设障,让用例只反映功能轴)
	csHost string // hostop 的 CSRF
	csCont string // contop 的 CSRF
	host   *http.Client
	cont   *http.Client
}

func setupPointGuard(t *testing.T) *pointEnv {
	t.Helper()
	st := storetest.Open(t)
	db := st.DB
	userSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	loadRoleCatalog(t, db)

	ctx := context.Background()
	roleSvc := role.New(db)
	hostRole, err := roleSvc.Create(ctx, role.CreateInput{Name: "仅主机", Perms: []string{"server.view", "server.exec"}})
	if err != nil {
		t.Fatalf("建「仅主机」角色: %v", err)
	}
	contRole, err := roleSvc.Create(ctx, role.CreateInput{Name: "仅容器", Perms: []string{"server.view", "container.operate"}})
	if err != nil {
		t.Fatalf("建「仅容器」角色: %v", err)
	}
	// 角色集改了要重装载,和 main.go 那两行装配同形(判定的点集来自进程内缓存)。
	if err := access.ReloadRoles(ctx); err != nil {
		t.Fatalf("重装载角色目录: %v", err)
	}

	hash, err := auth.HashPassword(guardPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	mkUser := func(name, roleID string) {
		t.Helper()
		if _, err := userSvc.Create(users.CreateInput{Username: name, PasswordHash: hash, Role: roleID}); err != nil {
			t.Fatalf("建号 %s: %v", name, err)
		}
	}
	mkUser("hostop", hostRole.ID)
	mkUser("contop", contRole.ID)

	v := vault.New(db, testMasterKey())
	cred, err := v.Create(vault.CreateInput{Name: "point ssh", Type: vault.TypeSSHKey, Secret: "s"})
	if err != nil {
		t.Fatalf("seed ssh credential: %v", err)
	}
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO servers (id, name, host, port, user, credential_id, group_id, created_at, updated_at)
		 VALUES (?, 'point-srv', '10.0.0.1', 22, 'deploy', ?, '', ?, ?)`,
		id, cred.ID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	gs := group.New(db)
	dialer := &fakeFSDialer{ws: newMemWorkspace()}
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v),
		WithAudit(audit.New(db, mask.NewMasker(), nil)),
		WithGroups(gs),
		WithAccess(access.NewService(gs)),
		WithServers(target.New(db, v, dialer)),
	))
	t.Cleanup(srv.Close)

	host := newTestClient(t)
	cont := newTestClient(t)
	return &pointEnv{
		srv:  srv,
		id:   id,
		host: host, csHost: loginAs(t, host, srv.URL, "hostop", guardPass),
		cont: cont, csCont: loginAs(t, cont, srv.URL, "contop", guardPass),
	}
}

// pointRoutes 是三入口各一条代表路由。终端用**不带升级头**的 GET:守卫排在 websocket.Accept
// 之前,所以被拦下时拿到 403,而放行时会走到升级校验(426)—— 两个状态码天然把两条路分开,
// 也不需要真的拨一条 SSH 起来。
func pointRoutes(base, id string) map[string]string {
	return map[string]string{
		"主机终端": base + "/api/servers/" + id + "/terminal",
		"远程文件": base + "/api/servers/" + id + "/fs?path=/app",
		"容器终端": base + "/api/servers/" + id + "/containers/c1/terminal",
	}
}

func TestServerPointGuardMatrix(t *testing.T) {
	env := setupPointGuard(t)

	// 每格都是「这个入口对该角色放不放行」。远程文件那条放行时会真的列目录(假工作区),
	// 所以期望 200;两个终端放行时卡在升级校验(426)。
	cases := []struct {
		who, route string
		want       int
	}{
		{"hostop", "主机终端", http.StatusUpgradeRequired},
		{"hostop", "远程文件", http.StatusOK},
		{"hostop", "容器终端", http.StatusForbidden},
		{"contop", "主机终端", http.StatusForbidden},
		{"contop", "远程文件", http.StatusForbidden},
		{"contop", "容器终端", http.StatusUpgradeRequired},
	}
	routes := pointRoutes(env.srv.URL, env.id)
	for _, tc := range cases {
		t.Run(tc.who+"/"+tc.route, func(t *testing.T) {
			client, csrf := env.host, env.csHost
			if tc.who == "contop" {
				client, csrf = env.cont, env.csCont
			}
			status, dto, raw := getJSON(t, client, routes[tc.route], csrf)
			if status != tc.want {
				t.Fatalf("%s 对 %s = %d, want %d(%s)", tc.who, tc.route, status, tc.want, raw)
			}
			if tc.want == http.StatusForbidden && errCode(dto) != "role_denied" {
				t.Fatalf("被功能点拦下应回 role_denied(而不是伪装成分组问题),得 %q", errCode(dto))
			}
		})
	}
}

// TestServerPointGuardAdminPassesAll 补一格边界:管理员两点都有,三入口全放行。
// 「未装配权限服务时守卫直通」那格不由这里证 —— 包里已有的终端/远程文件用例全都没接
// WithAccess,它们跑绿本身就是那条路的回归测试。
func TestServerPointGuardAdminPassesAll(t *testing.T) {
	env := setupPointGuard(t)
	admin := newTestClient(t)
	adminCS := loginAs(t, admin, env.srv.URL, "admin", "testpass")
	want := map[string]int{
		"主机终端": http.StatusUpgradeRequired,
		"远程文件": http.StatusOK,
		"容器终端": http.StatusUpgradeRequired,
	}
	for name, code := range want {
		status, _, raw := getJSON(t, admin, pointRoutes(env.srv.URL, env.id)[name], adminCS)
		if status != code {
			t.Fatalf("管理员对 %s = %d, want %d(%s)", name, status, code, raw)
		}
	}
}
