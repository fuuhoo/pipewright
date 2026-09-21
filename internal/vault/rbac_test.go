package vault

import (
	"errors"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
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

func TestEffectiveFilter_User_ForcesOnlyOwnPersonal(t *testing.T) {
	user := &Actor{UserID: "u-1", Role: "user"}
	// 即便 user 申请「IncludeGlobal=true IncludePersonal=true OwnerID=别人的」,
	// 也必须被强制覆盖为「仅 personal 且仅自己」。
	req := ListFilter{IncludeGlobal: true, IncludePersonal: true, OwnerID: "somebody-else"}
	got := effectiveFilter(user, req)
	if got.IncludeGlobal {
		t.Fatalf("user 不应能看到 global: got=%+v", got)
	}
	if !got.IncludePersonal {
		t.Fatalf("user 应保留 personal: got=%+v", got)
	}
	if got.OwnerID != "u-1" {
		t.Fatalf("user 看到的 OwnerID 必须是自己: got=%q want=u-1", got.OwnerID)
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
