package vault

import (
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// 单元测试集中覆盖 rbac.go 的纯逻辑:
//
//   - Actor.IsAdmin() — admin / user / nil 三态
//   - effectiveFilter() — admin 全开、user 仅自己 personal(nil actor 视为 admin)
//   - authorizeRead() — global+admin OK;global+user → ErrForbidden;
//     personal+self OK;personal+other → ErrAccessDenied
//   - authorizeWrite() — 同上,但 user 也不能改别人的 personal
//
// 集成测试留到真正落地 0053_credentials_owner 迁移(给 credentials 加 owner_id/
// enabled/disabled_by/description/created_by 等列)后再补。
// 当前 commit 不重做 0053,避免与 P0 #2 UNIQUE+CHECK 双约束继续纠缠。

func TestActor_IsAdmin(t *testing.T) {
	if !(*Actor)(nil).IsAdmin() {
		t.Fatal("nil actor 应视为 admin(系统调用)")
	}
	if !(&Actor{UserID: "u1", Role: "admin"}).IsAdmin() {
		t.Fatal("role=admin 应为 admin")
	}
	if (&Actor{UserID: "u1", Role: "user"}).IsAdmin() {
		t.Fatal("role=user 不应为 admin")
	}
	if (&Actor{UserID: "u1", Role: ""}).IsAdmin() {
		t.Fatal("role 空字符串不应为 admin")
	}
}

func TestEffectiveFilter_NilActor_PassesThrough(t *testing.T) {
	req := ListFilter{IncludeGlobal: true, IncludePersonal: true, OwnerID: "any"}
	got := effectiveFilter(nil, req)
	if !got.IncludeGlobal || !got.IncludePersonal || got.OwnerID != "any" {
		t.Fatalf("nil actor 应透传请求: got=%+v", got)
	}
}

func TestEffectiveFilter_Admin_PassesThrough(t *testing.T) {
	admin := &Actor{UserID: "admin-1", Role: "admin"}
	req := ListFilter{IncludeGlobal: true, IncludePersonal: false}
	got := effectiveFilter(admin, req)
	if !got.IncludeGlobal || got.IncludePersonal {
		t.Fatalf("admin 应透传请求: got=%+v", got)
	}
}

func TestEffectiveFilter_User_KeepsGlobalButForcesOwnPersonal(t *testing.T) {
	user := &Actor{UserID: "u-1", Role: "user"}
	// user 申请「IncludePersonal=true OwnerID=别人的」时,OwnerID 必须被强制改成自己;
	// global 保留(存量凭据全是 global,藏起来会让老部署的项目页选不回凭据)。
	req := ListFilter{IncludeGlobal: true, IncludePersonal: true, OwnerID: "somebody-else"}
	got := effectiveFilter(user, req)
	if !got.IncludeGlobal {
		t.Fatalf("user 应仍能看到 global(只出掩码): got=%+v", got)
	}
	if !got.IncludePersonal {
		t.Fatalf("user 应保留 personal: got=%+v", got)
	}
	if got.OwnerID != "u-1" {
		t.Fatalf("user 看到的 OwnerID 必须是自己: got=%q want=u-1", got.OwnerID)
	}
	// 请求里没要 global,就不额外塞给用户(「只看我的」是合法诉求)。
	only := effectiveFilter(user, ListFilter{IncludePersonal: true})
	if only.IncludeGlobal {
		t.Fatalf("未请求 global 时不该返回 global: got=%+v", only)
	}
	// IncludeDisabled 应透传(用户想看自己的禁用凭据是合理的)。
	req2 := ListFilter{IncludeGlobal: true, IncludePersonal: true, IncludeDisabled: true}
	got2 := effectiveFilter(user, req2)
	if !got2.IncludeDisabled {
		t.Fatalf("IncludeDisabled 应透传: got=%+v", got2)
	}
}

func TestAuthorizeRead(t *testing.T) {
	cases := []struct {
		name    string
		actor   *Actor
		cred    *Credential
		wantErr error // nil / ErrForbidden / ErrAccessDenied
	}{
		{
			name:    "nil actor 看 global OK",
			actor:   nil,
			cred:    &Credential{Scope: "global", OwnerID: ""},
			wantErr: nil,
		},
		{
			name:    "nil actor 看 personal OK",
			actor:   nil,
			cred:    &Credential{Scope: "personal", OwnerID: "u-1"},
			wantErr: nil,
		},
		{
			name:    "admin 看 global OK",
			actor:   &Actor{UserID: "admin-1", Role: "admin"},
			cred:    &Credential{Scope: "global"},
			wantErr: nil,
		},
		{
			name:    "admin 看别人 personal OK",
			actor:   &Actor{UserID: "admin-1", Role: "admin"},
			cred:    &Credential{Scope: "personal", OwnerID: "u-9"},
			wantErr: nil,
		},
		{
			name:    "user 看 global → ErrForbidden",
			actor:   &Actor{UserID: "u-1", Role: "user"},
			cred:    &Credential{Scope: "global"},
			wantErr: ErrForbidden,
		},
		{
			name:    "user 看自己 personal OK",
			actor:   &Actor{UserID: "u-1", Role: "user"},
			cred:    &Credential{Scope: "personal", OwnerID: "u-1"},
			wantErr: nil,
		},
		{
			name:    "user 看别人 personal → ErrAccessDenied",
			actor:   &Actor{UserID: "u-1", Role: "user"},
			cred:    &Credential{Scope: "personal", OwnerID: "u-9"},
			wantErr: ErrAccessDenied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authorizeRead(tc.actor, tc.cred)
			if got != tc.wantErr {
				t.Fatalf("authorizeRead: got=%v want=%v", got, tc.wantErr)
			}
		})
	}
}

func TestAuthorizeWrite(t *testing.T) {
	cases := []struct {
		name    string
		actor   *Actor
		cred    *Credential
		wantErr error
	}{
		{
			name:    "nil actor 写 global OK",
			actor:   nil,
			cred:    &Credential{Scope: "global"},
			wantErr: nil,
		},
		{
			name:    "admin 写 global OK",
			actor:   &Actor{UserID: "admin-1", Role: "admin"},
			cred:    &Credential{Scope: "global"},
			wantErr: nil,
		},
		{
			name:    "user 写自己 personal OK",
			actor:   &Actor{UserID: "u-1", Role: "user"},
			cred:    &Credential{Scope: "personal", OwnerID: "u-1"},
			wantErr: nil,
		},
		{
			name:    "user 写别人 personal → ErrAccessDenied",
			actor:   &Actor{UserID: "u-1", Role: "user"},
			cred:    &Credential{Scope: "personal", OwnerID: "u-9"},
			wantErr: ErrAccessDenied,
		},
		{
			name:    "user 写 global → ErrAccessDenied",
			actor:   &Actor{UserID: "u-1", Role: "user"},
			cred:    &Credential{Scope: "global"},
			wantErr: ErrAccessDenied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authorizeWrite(tc.actor, tc.cred)
			if got != tc.wantErr {
				t.Fatalf("authorizeWrite: got=%v want=%v", got, tc.wantErr)
			}
		})
	}
}

// 回归:DisableWithActor 对 global 凭据曾返回裸 fmt.Errorf,HTTP 层落到 default
// → 500(应为 403)。联调抓到时才暴露。
func TestRegression_DisableGlobalReturnsForbidden(t *testing.T) {
	db := storetest.OpenDB(t)
	v := New(db, testKey())
	// 建一条 scope=global 的凭据
	g, err := v.Create(CreateInput{
		Name: "g", Type: TypeGitToken, Scope: "global", Secret: "s",
	})
	if err != nil {
		t.Fatalf("create global: %v", err)
	}
	admin := &Actor{UserID: "admin-1", Role: "admin"}
	if err := v.DisableWithActor(admin, g.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("禁 global 应 ErrForbidden(→403), got %v", err)
	}
	// 非 admin → 同样 ErrForbidden
	user := &Actor{UserID: "u-1", Role: "user"}
	if err := v.DisableWithActor(user, g.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user 禁 global 应 ErrForbidden, got %v", err)
	}
}

// TestDisableEnableRoundTrip 锁住 enabled 列的写入方向:曾经把 disable 写成
// enabled=1(变量名 n 当「禁用标记」用),导致「禁用成功返回 200、凭据却照常可用」。
func TestDisableEnableRoundTrip(t *testing.T) {
	db := storetest.OpenDB(t)
	v := New(db, testKey())
	owner := &Actor{UserID: "u-1", Role: "user"}
	admin := &Actor{UserID: "admin-1", Role: "admin"}
	created, err := v.Create(CreateInput{
		Name: "p", Type: TypeGitToken, Scope: "personal", OwnerID: owner.UserID,
		Secret: "secret-value", CreatedBy: owner.UserID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Enabled {
		t.Fatal("新建凭据应为可用")
	}
	if err := v.DisableWithActor(admin, created.ID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := v.GetWithActor(owner, created.ID); !errors.Is(err, ErrDisabledCredential) {
		t.Fatalf("禁用后仍可取明文: %v", err)
	}
	if _, err := v.RevealWithActor(admin, created.ID); !errors.Is(err, ErrDisabledCredential) {
		t.Fatalf("禁用后 admin reveal 未被拒: %v", err)
	}
	view, err := v.ListWithActor(admin, ListFilter{IncludePersonal: true, IncludeDisabled: true})
	if err != nil || len(view) != 1 {
		t.Fatalf("列表 %d 条: %v", len(view), err)
	}
	if view[0].Enabled || view[0].DisabledBy != admin.UserID || view[0].DisabledAt == nil {
		t.Fatalf("禁用元数据不对:%+v", view[0])
	}
	// 普通用户可见范围里也应带着这条(带 enabled=false),而不是凭空消失
	mine, err := v.ListWithActor(owner, ListFilter{IncludeGlobal: true, IncludePersonal: true, IncludeDisabled: true})
	if err != nil || len(mine) != 1 || mine[0].Enabled {
		t.Fatalf("owner 视角 %+v: %v", mine, err)
	}
	if err := v.EnableWithActor(admin, created.ID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got, err := v.GetWithActor(owner, created.ID); err != nil || got != "secret-value" {
		t.Fatalf("启用后取用失败: %v %q", err, got)
	}
	after, err := v.ListWithActor(admin, ListFilter{IncludePersonal: true})
	if err != nil || len(after) != 1 {
		t.Fatalf("启用后列表 %d 条: %v", len(after), err)
	}
	if after[0].DisabledBy != "" || after[0].DisabledAt != nil {
		t.Fatalf("启用后应清掉禁用痕迹:%+v", after[0])
	}
	// 非 admin 不能自行解禁
	if err := v.EnableWithActor(owner, created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user 自行启用应 ErrForbidden, got %v", err)
	}
}

// TestListWithActor_SQLScoping 盯住 ListWithActor 生成的那条 SQL:同时开两个 scope
// 且带 owner 时必须是「global ∪ 自己的 personal」,不能退化成「不过滤 owner」。
func TestListWithActor_SQLScoping(t *testing.T) {
	db := storetest.OpenDB(t)
	v := New(db, testKey())
	alice := &Actor{UserID: "u-alice", Role: "user"}
	bob := &Actor{UserID: "u-bob", Role: "user"}
	_, err := v.Create(CreateInput{Name: "shared", Type: TypeGitToken, Scope: "global", Secret: "s1"})
	if err != nil {
		t.Fatalf("create global: %v", err)
	}
	if _, err := v.Create(CreateInput{Name: "alice-p", Type: TypeGitToken, Scope: "personal", OwnerID: alice.UserID, Secret: "s2"}); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := v.Create(CreateInput{Name: "bob-p", Type: TypeGitToken, Scope: "personal", OwnerID: bob.UserID, Secret: "s3"}); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	both := ListFilter{IncludeGlobal: true, IncludePersonal: true}

	names := func(creds []Credential) map[string]bool {
		m := make(map[string]bool, len(creds))
		for _, c := range creds {
			m[c.Name] = true
		}
		return m
	}
	got := names(mustList(t, v, alice, both))
	if !got["shared"] || !got["alice-p"] || got["bob-p"] {
		t.Fatalf("alice 视角 = %v, want 含 shared+alice-p 且不含 bob-p", got)
	}
	if n := len(mustList(t, v, bob, both)); n != 2 {
		t.Fatalf("bob 视角 %d 条, want 2", n)
	}
	admin := &Actor{UserID: "root", Role: "admin"}
	if n := len(mustList(t, v, admin, both)); n != 3 {
		t.Fatalf("admin 视角 %d 条, want 3", n)
	}
	// 只要 global:不得漏出任何 personal
	onlyGlobal := names(mustList(t, v, alice, ListFilter{IncludeGlobal: true}))
	if len(onlyGlobal) != 1 || !onlyGlobal["shared"] {
		t.Fatalf("只请求 global 时 = %v", onlyGlobal)
	}
	// 两个开关都关 → 空集(fail closed),不是「全给」
	if n := len(mustList(t, v, admin, ListFilter{})); n != 0 {
		t.Fatalf("空 filter 应返回 0 条, got %d", n)
	}
}

func mustList(t *testing.T, v Vault, actor *Actor, f ListFilter) []Credential {
	t.Helper()
	out, err := v.ListWithActor(actor, f)
	if err != nil {
		t.Fatalf("ListWithActor: %v", err)
	}
	return out
}

// TestListWithActor_LegacyEmptyScope 盯住存量数据:0058 之前建的凭据 scope 是空串,
// 「共享」判定必须是 `scope <> 'personal'`,否则老部署里所有凭据都会从用户列表里消失。
func TestListWithActor_LegacyEmptyScope(t *testing.T) {
	db := storetest.OpenDB(t)
	v := New(db, testKey())
	if _, err := v.Create(CreateInput{Name: "legacy", Type: TypeGitToken, Secret: "s"}); err != nil {
		t.Fatalf("create legacy(scope 空): %v", err)
	}
	// 直接确认列确实是空串(Create 不再兜底填 global)
	var scope string
	if err := db.QueryRow(`SELECT scope FROM credentials WHERE name = 'legacy'`).Scan(&scope); err != nil {
		t.Fatalf("读 scope: %v", err)
	}
	if scope != "" {
		t.Fatalf("夹具应复现存量态: scope=%q want 空串", scope)
	}
	user := &Actor{UserID: "u-x", Role: "user"}
	both, err := v.ListWithActor(user, ListFilter{IncludeGlobal: true, IncludePersonal: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(both) != 1 || both[0].Name != "legacy" {
		t.Fatalf("普通用户应仍看到存量凭据, got %v", namesOf(both))
	}
	onlyShared, err := v.ListWithActor(user, ListFilter{IncludeGlobal: true})
	if err != nil || len(onlyShared) != 1 {
		t.Fatalf("只请求共享项时 %d 条: %v", len(onlyShared), err)
	}
}

func namesOf(creds []Credential) []string {
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Name)
	}
	return out
}
