package users

import (
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// TestHasLocalPassword_CreateAndSet 验证「本地口令」标记的两条来源:
//   - 正常建号(管理员建号/OAuth 之外的路径)默认可登录 → true
//   - PlaceholderPassword(OAuth 首登自动建号的随机占位哈希)→ false
//   - 管理员重置口令(SetPassword)→ 翻回 true
func TestHasLocalPassword_CreateAndSet(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)

	normal, err := svc.Create(CreateInput{
		Username: "normal", PasswordHash: "$argon2id$v=19$m=65536,t=1,p=4$fake$fake",
	})
	if err != nil {
		t.Fatalf("create normal: %v", err)
	}
	if !normal.HasLocalPassword {
		t.Fatalf("普通建号应 HasLocalPassword=true")
	}

	oauthUser, err := svc.Create(CreateInput{
		Username: "oauthuser", PasswordHash: "$argon2id$v=19$m=65536,t=1,p=4$fake$fake",
		PlaceholderPassword: true,
	})
	if err != nil {
		t.Fatalf("create oauth user: %v", err)
	}
	if oauthUser.HasLocalPassword {
		t.Fatalf("占位口令账号应 HasLocalPassword=false")
	}
	got, err := svc.GetByID(oauthUser.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.HasLocalPassword {
		t.Fatalf("回读占位口令账号应 HasLocalPassword=false")
	}

	if err := svc.SetPassword(oauthUser.ID, "$argon2id$v=19$m=65536,t=1,p=4$fake2$fake2"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	got, err = svc.GetByID(oauthUser.ID)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if !got.HasLocalPassword {
		t.Fatalf("重置口令后应 HasLocalPassword=true")
	}
}
