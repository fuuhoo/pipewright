package apitoken_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
)

const dummyHash = "$argon2id$v=19$m=65536,t=1,p=4$WyWRpzFnab6vlgFLECjL9g$ZJhWU5lKWEo1T7NhxBxQf70Gk+We+sUQ8SO52b8nnxQ"

// newUserService 建一个可用的归属用户并返回其 id。
func newUserService(t *testing.T, db *sql.DB, username, role string) string {
	t.Helper()
	us := users.NewService(db)
	u, err := us.Create(users.CreateInput{Username: username, PasswordHash: dummyHash, Role: role})
	if err != nil {
		t.Fatalf("建用户 %s: %v", username, err)
	}
	return u.ID
}

// TestCreate_And_Verify 覆盖签发 → 校验的正路,以及「明文只回一次」这条核心约束。
func TestCreate_And_Verify(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid := newUserService(t, db, "alice", "user")

	created, err := svc.Create(ctx, apitoken.CreateInput{Name: "本机脚本", UserID: uid, CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(created.Plaintext, "pwt_") || len(created.Plaintext) < 40 {
		t.Fatalf("明文形态异常: %q", created.Plaintext)
	}
	if created.Token.Name != "本机脚本" || created.Token.OwnerName != "alice" {
		t.Fatalf("视图字段错位: %+v", created.Token)
	}
	if !created.Token.LastUsedAt.IsZero() {
		t.Fatalf("新令牌不该有使用时间: %v", created.Token.LastUsedAt)
	}

	p, err := svc.Verify(ctx, created.Plaintext)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.UserID != uid || p.Username != "alice" || p.Role != "user" {
		t.Fatalf("身份不对: %+v", p)
	}
	if p.TokenID != created.Token.ID {
		t.Fatalf("tokenID 不对: %q want %q", p.TokenID, created.Token.ID)
	}
}

// TestVerify_MarksLastUsed 验 last_used_at 真被写上,且节流窗口内不反复写库。
func TestVerify_MarksLastUsed(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid := newUserService(t, db, "bob", "user")

	created, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: uid})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	used := readLastUsed(t, db, created.Token.ID)
	if used != "" {
		t.Fatalf("签发时 last_used_at 应为空,得 %q", used)
	}

	if _, err := svc.Verify(ctx, created.Plaintext); err != nil {
		t.Fatalf("verify: %v", err)
	}
	first := readLastUsed(t, db, created.Token.ID)
	if first == "" {
		t.Fatalf("校验后应写入 last_used_at")
	}

	// 同一节流窗口内再验:值不该变(每请求一次 UPDATE 会在单写 SQLite 上放大写锁竞争)。
	if _, err := svc.Verify(ctx, created.Plaintext); err != nil {
		t.Fatalf("verify 2: %v", err)
	}
	if again := readLastUsed(t, db, created.Token.ID); again != first {
		t.Fatalf("节流失效:last_used_at 被反复写(%q → %q)", first, again)
	}

	// 把时间拨回节流窗口之外,再验必须刷新 —— 否则「这把令牌还在用」这条线索是假的。
	old := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, old, created.Token.ID); err != nil {
		t.Fatalf("拨旧时间: %v", err)
	}
	if _, err := svc.Verify(ctx, created.Plaintext); err != nil {
		t.Fatalf("verify 3: %v", err)
	}
	if fresh := readLastUsed(t, db, created.Token.ID); fresh == old {
		t.Fatalf("超窗后未刷新 last_used_at")
	}
}

// TestVerify_RejectsBadTokens 覆盖空串、改过一个字符、从未签发过三种。
func TestVerify_RejectsBadTokens(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid := newUserService(t, db, "carol", "user")

	created, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: uid})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tampered := created.Plaintext[:len(created.Plaintext)-1] + "0"
	if tampered == created.Plaintext {
		tampered = created.Plaintext[:len(created.Plaintext)-1] + "1"
	}
	for _, bad := range []string{"", "   ", tampered, "pwt_" + strings.Repeat("a", 64)} {
		if _, err := svc.Verify(ctx, bad); !errors.Is(err, apitoken.ErrInvalid) {
			t.Fatalf("令牌 %q 应判无效,得 %v", bad, err)
		}
	}
}

// TestVerify_FollowsAccountState 是关键反向用例:禁用与删号都要立刻让令牌失效,
// 而不是「令牌还在,人已经没了」——机器凭据最常见的泄露路径正是离职/停用后忘了收。
func TestVerify_FollowsAccountState(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	us := users.NewService(db)
	uid := newUserService(t, db, "dave", "user")

	created, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: uid})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Verify(ctx, created.Plaintext); err != nil {
		t.Fatalf("初始校验应通过: %v", err)
	}

	if err := us.SetEnabled(uid, false); err != nil {
		t.Fatalf("禁用: %v", err)
	}
	if _, err := svc.Verify(ctx, created.Plaintext); !errors.Is(err, apitoken.ErrInvalid) {
		t.Fatalf("禁用后应失效,得 %v", err)
	}
	if err := us.SetEnabled(uid, true); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if _, err := svc.Verify(ctx, created.Plaintext); err != nil {
		t.Fatalf("恢复后应重新可用(令牌本身没错): %v", err)
	}

	if err := us.Delete(uid); err != nil {
		t.Fatalf("删号: %v", err)
	}
	if _, err := svc.Verify(ctx, created.Plaintext); !errors.Is(err, apitoken.ErrInvalid) {
		t.Fatalf("删号后应失效,得 %v", err)
	}
	// 删号事务必须连令牌行一起清掉,否则库里留着无主凭据(列表页也会显示成幽灵行)。
	if n := countTokens(t, db); n != 0 {
		t.Fatalf("删号后残留令牌行 %d 条", n)
	}
}

// TestRevoke 验撤销即删行:撤销后既校验不过,列表里也不再出现。
func TestRevoke(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid := newUserService(t, db, "erin", "user")

	a, err := svc.Create(ctx, apitoken.CreateInput{Name: "留", UserID: uid})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := svc.Create(ctx, apitoken.CreateInput{Name: "删", UserID: uid})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := svc.Revoke(ctx, b.Token.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Verify(ctx, b.Plaintext); !errors.Is(err, apitoken.ErrInvalid) {
		t.Fatalf("撤销后应失效,得 %v", err)
	}
	if _, err := svc.Verify(ctx, a.Plaintext); err != nil {
		t.Fatalf("同用户的另一把不该被牵连: %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "留" {
		t.Fatalf("列表应只剩一把: %+v", list)
	}
	if err := svc.Revoke(ctx, "no-such-id"); !errors.Is(err, apitoken.ErrNotFound) {
		t.Fatalf("撤销未知 id 应回 ErrNotFound,得 %v", err)
	}
}

// TestCreate_Validation 覆盖入参门:名称、归属人。
func TestCreate_Validation(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	us := users.NewService(db)
	uid := newUserService(t, db, "frank", "user")

	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: "   ", UserID: uid}); !errors.Is(err, apitoken.ErrValidation) {
		t.Fatalf("空名称应拒绝,得 %v", err)
	}
	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: strings.Repeat("名", apitoken.MaxNameLen+1), UserID: uid}); !errors.Is(err, apitoken.ErrValidation) {
		t.Fatalf("超长名称应拒绝,得 %v", err)
	}
	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: ""}); !errors.Is(err, apitoken.ErrValidation) {
		t.Fatalf("缺归属人应拒绝,得 %v", err)
	}
	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: "no-such-user"}); !errors.Is(err, apitoken.ErrUserNotFound) {
		t.Fatalf("未知归属人应回 ErrUserNotFound,得 %v", err)
	}
	if err := us.SetEnabled(uid, false); err != nil {
		t.Fatalf("禁用: %v", err)
	}
	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: uid}); !errors.Is(err, apitoken.ErrValidation) {
		t.Fatalf("给禁用账号签发应当场拒绝,得 %v", err)
	}
}

// TestCreate_NameNormalized 验折叠空白(名称会进表格与审计)。
func TestCreate_NameNormalized(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid := newUserService(t, db, "grace", "user")

	created, err := svc.Create(ctx, apitoken.CreateInput{Name: "  生产  巡检\t脚本  ", UserID: uid})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Token.Name != "生产 巡检 脚本" {
		t.Fatalf("名称未归一化: %q", created.Token.Name)
	}
}

// TestList_SortedAndScoped 验列表按创建时间倒序,并带出归属人登录名。
func TestList_SortedAndScoped(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid1 := newUserService(t, db, "heidi", "user")
	uid2 := newUserService(t, db, "ivan", "admin")

	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: "早", UserID: uid1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// created_at 是秒级 RFC3339,同一秒内插两条会失去顺序可比性 → 显式拨开。
	if _, err := db.Exec(`UPDATE api_tokens SET created_at = ? WHERE name = '早'`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("拨时间: %v", err)
	}
	if _, err := svc.Create(ctx, apitoken.CreateInput{Name: "晚", UserID: uid2}); err != nil {
		t.Fatalf("create: %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "晚" || list[1].Name != "早" {
		t.Fatalf("排序应为「晚,早」: %+v", list)
	}
	if list[0].OwnerName != "ivan" || list[1].OwnerName != "heidi" {
		t.Fatalf("归属人登录名没带出: %+v", list)
	}
}

// TestNoPlaintextInDB 是「库里绝不存明文」的实盘证据:直接读 SQLite 文件字节。
func TestNoPlaintextInDB(t *testing.T) {
	storetest.SkipIfMySQL(t)
	db, path := storetest.OpenDBWithPath(t)
	ctx := context.Background()
	svc := apitoken.NewService(db)
	uid := newUserService(t, db, "judy", "user")

	created, err := svc.Create(ctx, apitoken.CreateInput{Name: "n", UserID: uid})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Verify(ctx, created.Plaintext); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// WAL 下新写的页可能还留在 -wal 文件里,不合并就读主库等于什么都没查。
	if _, err := db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读库文件: %v", err)
	}
	if strings.Contains(string(raw), created.Plaintext) {
		t.Fatalf("裸 DB 不应含令牌明文")
	}
	if strings.Contains(string(raw), created.Plaintext[4:]) {
		t.Fatalf("裸 DB 不应含令牌明文片段")
	}
}

func readLastUsed(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT last_used_at FROM api_tokens WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("读 last_used_at: %v", err)
	}
	return v
}

func countTokens(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM api_tokens`).Scan(&n); err != nil {
		t.Fatalf("数令牌: %v", err)
	}
	return n
}
