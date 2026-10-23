// profile_test.go 守的是这次「显示名/邮箱」最容易写反的一条:
// user_identities 里的 username/email 是**平台快照**,每次登录都刷;
// users 里的 display_name/email 是**本地档案**,只在建号时写入、之后仅空则回填。
// 两者放反了就会出现「用户在本地改了昵称,下次登录又被平台顶掉」。
package identity

import (
	"context"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
)

func identityNamed(externalID, login, name, email string) *oauth.Identity {
	return &oauth.Identity{
		Provider: oauth.ProviderGitLab, ExternalID: externalID,
		Login: login, Name: name, Email: email,
	}
}

func TestProvisionSeedsProfileFromPlatform(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	u := users.NewService(db)
	svc := NewService(db, u, enabledApp(), nil)

	acc, err := svc.Login(context.Background(), identityNamed("7001", "dave", "大伟", "dave@corp.co"), nil)
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	got, err := u.GetByID(acc.UserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "大伟" || got.Email != "dave@corp.co" {
		t.Fatalf("建号时应把平台给的全名/邮箱写进本地档案: %+v", got)
	}

	// 平台侧改了昵称再登一次:本地档案保持不动(那是「之后本地可编辑」的那一档)…
	if _, err := svc.Login(context.Background(),
		identityNamed("7001", "dave", "改过的平台昵称", "other@corp.co"), nil); err != nil {
		t.Fatalf("二次登录: %v", err)
	}
	got, err = u.GetByID(acc.UserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "大伟" || got.Email != "dave@corp.co" {
		t.Fatalf("二次登录把本地档案顶掉了: %+v", got)
	}
	// …但平台快照照旧每次刷新,绑定管理页要看得见的就是它。
	var snapName, snapMail string
	if err := db.QueryRow(
		`SELECT username, email FROM user_identities WHERE provider = ? AND external_id = ?`,
		oauth.ProviderGitLab, "7001",
	).Scan(&snapName, &snapMail); err != nil {
		t.Fatalf("读快照: %v", err)
	}
	if snapName != "dave" || snapMail != "other@corp.co" {
		t.Fatalf("快照应跟随平台刷新: username=%q email=%q", snapName, snapMail)
	}
}

// TestLegacyProfileIsBackfilledOnNextLogin:0069 之前建出来的账号那两列是空串。
// 不回填的话,老用户升完级界面上依旧只有用户名,而且他自己没有任何入口补上
// (OAuth 账号的口令是随机占位,登不进「用户管理」那条路)。
func TestLegacyProfileIsBackfilledOnNextLogin(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	u := users.NewService(db)
	svc := NewService(db, u, enabledApp(), nil)

	acc, err := svc.Login(context.Background(), identityNamed("7002", "erin", "恩然", "erin@corp.co"), nil)
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	// 抹回迁移前的形态:本地档案两列全空。
	if _, err := db.Exec(`UPDATE users SET display_name = '', email = '' WHERE id = ?`, acc.UserID); err != nil {
		t.Fatalf("模拟迁移前: %v", err)
	}

	if _, err := svc.Login(context.Background(),
		identityNamed("7002", "erin", "恩然", "erin@corp.co"), nil); err != nil {
		t.Fatalf("二次登录: %v", err)
	}
	got, err := u.GetByID(acc.UserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "恩然" || got.Email != "erin@corp.co" {
		t.Fatalf("空档案没被回填: %+v", got)
	}
}

// TestBindBackfillsProfile:管理员先按名字建好账号、本人再来绑定。
// 这条路不经过 provision,漏掉回填就会留下一个永远显示「用户名」的账号。
func TestBindBackfillsProfile(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	u := users.NewService(db)
	svc := NewService(db, u, enabledApp(), nil)

	hash, err := auth.HashPassword("whatever-pass-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	created, err := u.Create(users.CreateInput{Username: "frank", PasswordHash: hash, Role: "user"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Bind(context.Background(), created.ID,
		identityNamed("7003", "frank", "弗兰克", "frank@corp.co"), nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, err := u.GetByID(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "弗兰克" || got.Email != "frank@corp.co" {
		t.Fatalf("绑定后应补齐本地档案: %+v", got)
	}
}
