package users_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
)

const (
	dummyHash = "$argon2id$v=19$m=65536,t=1,p=4$WyWRpzFnab6vlgFLECjL9g$ZJhWU5lKWEo1T7NhxBxQf70Gk+We+sUQ8SO52b8nnxQ"
)

// TestBootstrapAdminRow_Basic 验证首次 Bootstrap 行为。
func TestBootstrapAdminRow_Basic(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)
	ctx := context.Background()

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	u, err := svc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.ID != users.BootstrapAdminRegularUserID {
		t.Fatalf("ID 错误: got %q want %q", u.ID, users.BootstrapAdminRegularUserID)
	}
	if u.Role != users.RoleAdmin {
		t.Fatalf("role 错误: got %q want %q", u.Role, users.RoleAdmin)
	}
	if !u.Enabled {
		t.Fatalf("enabled 应为 true")
	}
	if u.PasswordHash != dummyHash {
		t.Fatalf("hash 未持久化: got %q", u.PasswordHash)
	}

	// created_at 与 updated_at 应当非常接近(都是 now)
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatalf("created/updated_at 为零: %+v", u)
	}
	if abs(now.Sub(u.CreatedAt)) > time.Minute {
		t.Fatalf("created_at 异常: %v", u.CreatedAt)
	}
	_ = ctx
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TestBootstrapAdminRow_Idempotent 多次 Bootstrap 必须保持单行 + 不变 id。
func TestBootstrapAdminRow_Idempotent(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("second: %v", err)
	}
	if err := svc.BootstrapAdminRow("admin", dummyHash+"x"); err != nil {
		t.Fatalf("third: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = ?`, users.RoleAdmin).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("重复 Bootstrap 应保持 1 行 admin,got %d", n)
	}

	u, err := svc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.PasswordHash != dummyHash+"x" {
		t.Fatalf("hash 未被第三轮覆盖: got %q", u.PasswordHash)
	}
}

// TestSyncAdminPasswordChange_UpdatesHash 验证同步逻辑。
func TestSyncAdminPasswordChange_UpdatesHash(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	newHash := dummyHash + "_new"
	if err := svc.SyncAdminPasswordChange("admin", newHash); err != nil {
		t.Fatalf("sync: %v", err)
	}

	u, err := svc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.PasswordHash != newHash {
		t.Fatalf("hash 未同步: got %q", u.PasswordHash)
	}
}

// TestSyncAdminPasswordChange_NoAdminRow 验证未 Bootstrap 时返回 ErrNotFound。
func TestSyncAdminPasswordChange_NoAdminRow(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	err := svc.SyncAdminPasswordChange("admin", dummyHash)
	if !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}

// TestGetByUsername_NotFound 验证 ErrNotFound 路径。
func TestGetByUsername_NotFound(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	_, err := svc.GetByUsername("ghost")
	if !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}

// TestGetByID_NotFound 验证 GetByID 找不到。
func TestGetByID_NotFound(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	_, err := svc.GetByID("00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}

// ---- Delete ----

// seedSession 给某用户塞一条活会话(不校验口令,只为验证删号会把它一起抹掉)。
func seedSession(t *testing.T, db *sql.DB, userID, token string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO sessions (token, csrf_token, created_at, expires_at, last_seen_at, user_id, role)
		 VALUES (?, ?, ?, ?, ?, ?, 'user')`,
		token, "csrf-"+token, now, now, now, userID,
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// seedIdentity 塞一条外部身份绑定(它是 users 唯一有 ON DELETE CASCADE 的引用表)。
func seedIdentity(t *testing.T, db *sql.DB, userID, provider, externalID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO user_identities (id, user_id, provider, external_id, username, email, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		"idn-"+externalID, userID, provider, externalID, "someone", now, now,
	)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
}

// seedGroup 建一个组(owner 即组长),返回其 id。
func seedGroup(t *testing.T, db *sql.DB, name, ownerID string) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	id := "grp-" + name
	_, err := db.Exec(
		`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES (?, ?, '', 'private', ?, ?, ?, ?)`,
		id, name, ownerID, ownerID, now, now,
	)
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
	return id
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count(%s): %v", query, err)
	}
	return n
}

// TestDelete_RemovesAccountAndPureReferences 删号把账号连同会话、外部身份、分组名册一起抹掉。
// sessions.user_id 与 resource_group_members.user_id 都没有外键,靠的就是这段显式清理。
func TestDelete_RemovesAccountAndPureReferences(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	bob, err := svc.Create(users.CreateInput{
		Username: "bob", PasswordHash: dummyHash, DisplayName: "Bob", Email: "bob@example.com",
	})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	seedSession(t, db, bob.ID, "tok-bob")
	seedSession(t, db, users.BootstrapAdminRegularUserID, "tok-admin")
	seedIdentity(t, db, bob.ID, "gitlab", "42")
	groupID := seedGroup(t, db, "team-a", users.BootstrapAdminRegularUserID)
	if _, err := db.Exec(
		`INSERT INTO resource_group_members (group_id, user_id, created_at, created_by) VALUES (?, ?, ?, ?)`,
		groupID, bob.ID, time.Now().UTC().Format(time.RFC3339), users.BootstrapAdminRegularUserID,
	); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	if err := svc.Delete(bob.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := svc.GetByID(bob.ID); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("账号应已消失,预期 ErrNotFound,got %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM sessions WHERE user_id = ?`, bob.ID); n != 0 {
		t.Fatalf("会话没清干净,got %d", n)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM user_identities WHERE user_id = ?`, bob.ID); n != 0 {
		t.Fatalf("外部身份没清干净,got %d", n)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM resource_group_members WHERE user_id = ?`, bob.ID); n != 0 {
		t.Fatalf("分组名册没清干净,got %d", n)
	}
	// 别人的会话与组都不能被波及:名册删除只按 user_id 走,组本身留着(组长是 admin)。
	if n := countRows(t, db, `SELECT COUNT(1) FROM sessions WHERE token = ?`, "tok-admin"); n != 1 {
		t.Fatalf("管理员会话被误删,got %d", n)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM resource_groups WHERE id = ?`, groupID); n != 1 {
		t.Fatalf("分组被误删,got %d", n)
	}
	if _, err := svc.GetByUsername("bob"); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("按用户名也应查不到,got %v", err)
	}
}

// TestDelete_BlockedWhileGroupOwner 组长身份要先移交:删号不该造出无主组。
func TestDelete_BlockedWhileGroupOwner(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	bob, err := svc.Create(users.CreateInput{Username: "bob", PasswordHash: dummyHash})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	seedGroup(t, db, "team-a", bob.ID)
	seedGroup(t, db, "team-b", bob.ID)

	err = svc.Delete(bob.ID)
	if !errors.Is(err, users.ErrHasGroups) {
		t.Fatalf("预期 ErrHasGroups,got %v", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatalf("错误文案要带组长数量供管理员判断,got %q", err.Error())
	}
	if _, err := svc.GetByID(bob.ID); err != nil {
		t.Fatalf("被阻断时账号必须原样留着,got %v", err)
	}
}

// TestDelete_BlockedWhileOwnsPersonalCredentials 个人凭据密文一删不可恢复,先阻断。
// 共享凭据(scope 空或 'global')即使 created_by 是本人也不挡 —— 它不属于那个人。
func TestDelete_BlockedWhileOwnsPersonalCredentials(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	bob, err := svc.Create(users.CreateInput{Username: "bob", PasswordHash: dummyHash})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO credentials (id, name, type, scope, owner_id, created_by, ciphertext, masked_value, created_at, updated_at)
		 VALUES ('cred-p', 'my token', 'git_token', 'personal', ?, ?, X'00', '****', ?, ?)`,
		bob.ID, bob.ID, now, now,
	); err != nil {
		t.Fatalf("seed personal credential: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO credentials (id, name, type, scope, owner_id, created_by, ciphertext, masked_value, created_at, updated_at)
		 VALUES ('cred-g', 'shared', 'git_token', 'global', '', ?, X'00', '****', ?, ?)`,
		bob.ID, now, now,
	); err != nil {
		t.Fatalf("seed global credential: %v", err)
	}

	err = svc.Delete(bob.ID)
	if !errors.Is(err, users.ErrHasPersonalCredentials) {
		t.Fatalf("预期 ErrHasPersonalCredentials,got %v", err)
	}
	if _, err := db.Exec(`DELETE FROM credentials WHERE id = 'cred-p'`); err != nil {
		t.Fatalf("drop personal credential: %v", err)
	}
	if err := svc.Delete(bob.ID); err != nil {
		t.Fatalf("只剩共享凭据时应能删除,got %v", err)
	}
}

// TestDelete_BlockedForLastAdmin 删掉最后一个启用的管理员会把「用户管理」的入口一起删没。
// 这里刻意不 Bootstrap,让库里只有 Create 出来的那个 admin。
func TestDelete_BlockedForLastAdmin(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	only, err := svc.Create(users.CreateInput{Username: "solo", PasswordHash: dummyHash, Role: users.RoleAdmin})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := svc.Delete(only.ID); !errors.Is(err, users.ErrLastAdmin) {
		t.Fatalf("预期 ErrLastAdmin,got %v", err)
	}

	second, err := svc.Create(users.CreateInput{Username: "second", PasswordHash: dummyHash, Role: users.RoleAdmin})
	if err != nil {
		t.Fatalf("create second admin: %v", err)
	}
	if err := svc.Delete(second.ID); err != nil {
		t.Fatalf("有两个启用管理员时应可删除其一,got %v", err)
	}
}

// TestDelete_NotFound 不存在的 id 回 ErrNotFound(HTTP 层据此出 404)。
func TestDelete_NotFound(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.Delete("00000000-0000-0000-0000-000000000099"); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}
