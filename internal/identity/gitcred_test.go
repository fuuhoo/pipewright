// gitcred_test.go 守的是「登录顺手同步 git 凭据」那四条规矩(见 gitcred.go 注释):
// 只发个人凭据、映射行即幂等键、回指失效就新建绝不改写、同步失败不影响登录。
// 这里用的是**真** vault 与真库,不是凭据桩 —— 接管防护恰好藏在
// 「View 读到别人的行之后到底改不改」这个接缝上,桩对象替掉它就什么都测不出来。
package identity

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// testVault 给同步用例配一个真保险库(固定 key 即可:用例只看密文与明文,不测密钥轮换)。
func testVault(db *sql.DB) vault.Vault {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 11)
	}
	return vault.New(db, &k)
}

// syncApp 是开了「登录即同步凭据」的 git 登录应用。
func syncApp() fakeApps {
	a := enabledApp()
	a.cfg.SyncCredentials = true
	return a
}

// grantOf 模拟一次带时效与续期凭证的授权。
func grantOf(token string, expiresIn time.Duration, refresh string) *oauth.Grant {
	return &oauth.Grant{
		AccessToken: token, RefreshToken: refresh,
		ExpiresAt: time.Now().UTC().Add(expiresIn), Scope: "read_repository",
	}
}

func ptrStr(s string) *string { return &s }

func countRows(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// refreshMaterial 读映射行上的续期凭证密文与时效串(这两样都不在 Binding 视图里)。
func refreshMaterial(t *testing.T, db *sql.DB, bindingID string) ([]byte, string) {
	t.Helper()
	var sealed []byte
	var expires string
	if err := db.QueryRow(
		`SELECT git_refresh_ciphertext, git_expires_at FROM user_identities WHERE id = ?`, bindingID,
	).Scan(&sealed, &expires); err != nil {
		t.Fatalf("读续期材料: %v", err)
	}
	return sealed, expires
}

// TestLoginSyncsPersonalGitCredential 是这次改造的正身:一次登录 = 一个账号 + 一条只属于
// 这个人的 git 令牌,并且明文绝不落到任何一列里。
func TestLoginSyncsPersonalGitCredential(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	expiry := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	acct, err := svc.Login(ctx, identityOf("8001", "gina"),
		&oauth.Grant{AccessToken: "tok-first", RefreshToken: "rt-first", ExpiresAt: expiry, Scope: "read_repository"})
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	if acct.CredentialID == "" {
		t.Fatalf("开了同步的登录应带回凭据 id")
	}

	cred, err := v.View(acct.CredentialID)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if cred.Type != vault.TypeGitToken || cred.Scope != "personal" {
		t.Fatalf("同步出来该是一条**个人** git 令牌,得到 %+v", cred)
	}
	if cred.OwnerID != acct.UserID || cred.CreatedBy != acct.UserID {
		t.Fatalf("凭据归属应是登录这个人:%+v", cred)
	}
	if cred.Username != "gina" || cred.Name != "gitlab:gina" {
		t.Fatalf("凭据该带上平台登录名:%+v", cred)
	}
	if !strings.Contains(cred.Description, "OAuth 登录自动同步") {
		t.Fatalf("自动产出的凭据要写明来路,否则用户只看到一条自己没建过的令牌:%+v", cred)
	}
	if got, err := v.Reveal(cred.ID); err != nil || got != "tok-first" {
		t.Fatalf("存进去的应是这次授权的令牌:got=%q err=%v", got, err)
	}

	b, err := svc.Find(ctx, oauth.ProviderGitLab, "8001")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if b.CredentialID != cred.ID {
		t.Fatalf("映射行应回指这条凭据:%q vs %q", b.CredentialID, cred.ID)
	}
	if !b.ExpiresAt.Equal(expiry) {
		t.Fatalf("时效应记在映射行上:%v vs %v", b.ExpiresAt, expiry)
	}
	// 平台**实际**授予的 scope 要留着 —— 排查「为什么这个令牌拉不到那个仓库」全靠它。
	if b.Scope != "read_repository" {
		t.Fatalf("scope = %q", b.Scope)
	}

	// 明文不落库:凭据密文与映射行上的续期凭证都翻一遍。
	var cipher []byte
	if err := db.QueryRow(`SELECT ciphertext FROM credentials WHERE id = ?`, cred.ID).Scan(&cipher); err != nil {
		t.Fatalf("读密文: %v", err)
	}
	if strings.Contains(string(cipher), "tok-first") {
		t.Fatalf("凭据里存了明文令牌")
	}
	sealed, _ := refreshMaterial(t, db, b.ID)
	if strings.Contains(string(sealed), "rt-first") {
		t.Fatalf("续期凭证存了明文")
	}
	opened, err := v.OpenSecret(sealed)
	if err != nil || string(opened) != "rt-first" {
		t.Fatalf("续期凭证应能用同一把 master key 解回:opened=%q err=%v", opened, err)
	}
}

// TestRepeatLoginRotatesOnlyTheSecret 是幂等键那半条规矩:同一个人在同一个平台重复登录
// 只轮换那一条凭据的密钥,不新建第二条,也**不动**用户自己改过的名字与备注。
func TestRepeatLoginRotatesOnlyTheSecret(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	first, err := svc.Login(ctx, identityOf("8002", "hank"), grantOf("tok-1", time.Hour, "rt-1"))
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	// 本人把它改了名字、写了备注(这是用户可编辑的那一档)。
	if _, err := v.Update(first.CredentialID, vault.UpdateInput{
		Name: ptrStr("我的 git 令牌"), Description: ptrStr("只用来拉私有仓"),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	second, err := svc.Login(ctx, identityOf("8002", "hank"), grantOf("tok-2", 3*time.Hour, "rt-2"))
	if err != nil {
		t.Fatalf("二次登录: %v", err)
	}
	if second.CredentialID != first.CredentialID {
		t.Fatalf("重复登录该复用同一条凭据:%q vs %q", second.CredentialID, first.CredentialID)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM credentials`); n != 1 {
		t.Fatalf("重复登录建出了第二条凭据(把人淹掉的那种):%d", n)
	}
	cred, err := v.View(first.CredentialID)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if cred.Name != "我的 git 令牌" || cred.Description != "只用来拉私有仓" {
		t.Fatalf("同步覆盖了用户自己改过的名字/备注:%+v", cred)
	}
	if got, err := v.Reveal(cred.ID); err != nil || got != "tok-2" {
		t.Fatalf("密钥该轮换到这次授权的新令牌:got=%q err=%v", got, err)
	}
}

// TestNilGrantWritesNoCredential:同步由「这次登录有没有带回令牌」决定 ——
// 没开同步的应用在 oauth 层就不返回 Grant,这里必须一条都不写。
func TestNilGrantWritesNoCredential(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), enabledApp(), v)

	acct, err := svc.Login(context.Background(), identityOf("8003", "ivory"), nil)
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	if acct.CredentialID != "" {
		t.Fatalf("没给令牌就不该同步凭据,得到 %q", acct.CredentialID)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM credentials`); n != 0 {
		t.Fatalf("未同步时凭据表被写了 %d 行", n)
	}
}

// TestVaultAbsentStillLogsIn 是规矩 4 的最省事版:没配 master key 的部署 creds 为 nil,
// 登录照过,只是没有凭据可发。
func TestVaultAbsentStillLogsIn(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	svc := NewService(db, users.NewService(db), syncApp(), nil)

	acct, err := svc.Login(context.Background(), identityOf("8004", "jade"), grantOf("tok-1", time.Hour, "rt-1"))
	if err != nil {
		t.Fatalf("没保险库时登录必须照常: %v", err)
	}
	if acct.CredentialID != "" {
		t.Fatalf("creds 为 nil 时不该有凭据 id:%q", acct.CredentialID)
	}
}

// TestForeignCredentialIsAbandonedNotRewritten 是接管防护的反向用例:映射回指的那条凭据
// 已经不再属于这个账号,同步只能另建一条,绝不把令牌写进别人的行。
func TestForeignCredentialIsAbandonedNotRewritten(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	first, err := svc.Login(ctx, identityOf("8005", "kite"), grantOf("tok-mine", time.Hour, "rt-1"))
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	// 另一个人建一条自己的凭据,再把这条映射的回指改到它上面(模拟库里被改坏/管理员挪走)。
	other, err := v.Create(vault.CreateInput{
		Name: "别人的令牌", Type: vault.TypeGitToken, Scope: "personal",
		OwnerID: "u-other", CreatedBy: "u-other", Secret: "tok-other",
	})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE user_identities SET credential_id = ? WHERE external_id = '8005'`, other.ID,
	); err != nil {
		t.Fatalf("改回指: %v", err)
	}

	again, err := svc.Login(ctx, identityOf("8005", "kite"), grantOf("tok-new", 2*time.Hour, "rt-2"))
	if err != nil {
		t.Fatalf("二次登录: %v", err)
	}
	if again.CredentialID == "" || again.CredentialID == other.ID || again.CredentialID == first.CredentialID {
		t.Fatalf("别人的凭据不可复用,应另建一条,得到 %q", again.CredentialID)
	}
	if got, err := v.Reveal(other.ID); err != nil || got != "tok-other" {
		t.Fatalf("把新令牌写进了别人的凭据:got=%q err=%v", got, err)
	}
	cred, err := v.View(again.CredentialID)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if cred.OwnerID != again.UserID {
		t.Fatalf("新建那条该归登录的人自己:%+v", cred)
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM credentials`); n != 3 {
		t.Fatalf("应有「首次同步的 + 别人的 + 这次新建的」三条,得到 %d", n)
	}
}

// TestDeletedCredentialIsRecreated:用户自己删掉了那条同步凭据,下次登录要重新建出来,
// 而不是因为回指指向空行而永远同步不上。
func TestDeletedCredentialIsRecreated(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	first, err := svc.Login(ctx, identityOf("8006", "lark"), grantOf("tok-1", time.Hour, "rt-1"))
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	if err := v.Delete(first.CredentialID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	second, err := svc.Login(ctx, identityOf("8006", "lark"), grantOf("tok-2", time.Hour, "rt-2"))
	if err != nil {
		t.Fatalf("二次登录: %v", err)
	}
	if second.CredentialID == "" || second.CredentialID == first.CredentialID {
		t.Fatalf("删掉后应重新建一条,得到 %q", second.CredentialID)
	}
	if got, err := v.Reveal(second.CredentialID); err != nil || got != "tok-2" {
		t.Fatalf("新那条内容不对:got=%q err=%v", got, err)
	}
}

// TestBindSyncsCredential:绑定这条路也同步 —— 管理员先建好账号、本人来绑,
// 同步只是把他这次的授权多留一份可用令牌,照样只归他自己。
func TestBindSyncsCredential(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	u := users.NewService(db)
	svc := NewService(db, u, syncApp(), v)

	hash, err := auth.HashPassword("whatever-pass-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	created, err := u.Create(users.CreateInput{Username: "mast", PasswordHash: hash, Role: "user"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err := svc.Bind(context.Background(), created.ID, identityOf("8007", "mast"), grantOf("tok-bind", time.Hour, "rt-1"))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if b.CredentialID == "" {
		t.Fatalf("绑定后映射行应回指同步出来的凭据")
	}
	if n := countRows(t, db, `SELECT COUNT(1) FROM credentials WHERE owner_id = ?`, created.ID); n != 1 {
		t.Fatalf("绑定后应有且只有一条属于本人的凭据,得到 %d", n)
	}
}

// ---- 续期扫描器 ----

// TestLongLivedGrantNeverQueued:平台没告知时效(Gitea 常见:只回 access_token)时按长期令牌
// 处理 —— 不进续期队列,也不会哪天被扫描器判成「已过期」。
func TestLongLivedGrantNeverQueued(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	if _, err := svc.Login(ctx, identityOf("8008", "owl"),
		&oauth.Grant{AccessToken: "tok-long", Scope: "api"}); err != nil {
		t.Fatalf("首登: %v", err)
	}
	b, err := svc.Find(ctx, oauth.ProviderGitLab, "8008")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !b.ExpiresAt.IsZero() {
		t.Fatalf("长期令牌不该有时效:%v", b.ExpiresAt)
	}
	if _, expires := refreshMaterial(t, db, b.ID); expires != "" {
		t.Fatalf("时效串该留空(空 = 长期),得到 %q", expires)
	}
	pending, err := svc.ListStaleGrants(ctx, RefreshLead, time.Now().UTC())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("长期令牌不该被排进续期队列:%+v", pending)
	}
}

// TestRefreshRotatesCredentialAndMaterial 是续期成功那一支:凭据里的令牌、映射行上的
// 时效与续期凭证三样一起往前挪,否则「续期成功」只写在日志里。
func TestRefreshRotatesCredentialAndMaterial(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	acct, err := svc.Login(ctx, identityOf("8009", "pine"), grantOf("tok-old", 5*time.Minute, "rt-old"))
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	pending, err := svc.ListStaleGrants(ctx, RefreshLead, time.Now().UTC())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("5 分钟后到期的该进队列:%+v", pending)
	}
	if pending[0].CredentialID != acct.CredentialID || pending[0].UserID != acct.UserID {
		t.Fatalf("队列项指错了人/凭据:%+v", pending[0])
	}

	// 平台这次续了一张更久的,并且**没换发** refresh_token(RFC 6749 允许省略)。
	apps := syncApp()
	apps.next = &oauth.Grant{AccessToken: "tok-new", ExpiresAt: time.Now().UTC().Add(6 * time.Hour), Scope: "api"}
	svc = NewService(db, users.NewService(db), apps, v)
	ok, failed, err := svc.RefreshGrants(ctx)
	if err != nil || ok != 1 || failed != 0 {
		t.Fatalf("ok/failed/err = %d/%d/%v", ok, failed, err)
	}
	if got, err := v.Reveal(acct.CredentialID); err != nil || got != "tok-new" {
		t.Fatalf("续期成功但凭据里还是旧令牌:got=%q err=%v", got, err)
	}
	b, err := svc.Find(ctx, oauth.ProviderGitLab, "8009")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if b.ExpiresAt.Before(time.Now().UTC().Add(time.Hour)) {
		t.Fatalf("时效没跟着往前挪:%v", b.ExpiresAt)
	}
	sealed, _ := refreshMaterial(t, db, b.ID)
	opened, err := v.OpenSecret(sealed)
	if err != nil || string(opened) != "rt-old" {
		t.Fatalf("平台没换发时该沿用旧续期凭证 —— 留空等于这个人再也刷不动:opened=%q err=%v", opened, err)
	}

	// 续完之后离到期还远,不该再被排进队列(否则扫描器每一轮都在白撞)。
	again, err := svc.ListStaleGrants(ctx, RefreshLead, time.Now().UTC())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("续期成功后仍被排队:%+v", again)
	}
}

// TestRefreshRejectedMarksStaleAndSelfHeals 是「宁可留一条过期凭据,也不静默换指」那一支:
// 平台拒续 → 备注写清人读的因、清掉续期材料不再每轮撞墙,但**不动凭据行本身**;
// 等这个人重新登录一次,自己得回来。
func TestRefreshRejectedMarksStaleAndSelfHeals(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	apps := syncApp()
	svc := NewService(db, users.NewService(db), apps, v)
	ctx := context.Background()

	acct, err := svc.Login(ctx, identityOf("8010", "quay"), grantOf("tok-1", 5*time.Minute, "rt-1"))
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	// 续期被拒 —— 绝大多数是这个人自己在平台上撤销了授权,重试一万次也不会成。
	// 桩是值类型,换了行为要重建 Service。
	dead := syncApp()
	dead.refreshErr = oauth.ErrRefreshUnsupported
	svcDead := NewService(db, users.NewService(db), dead, v)
	ok, failed, err := svcDead.RefreshGrants(ctx)
	if err != nil || ok != 0 || failed != 1 {
		t.Fatalf("ok/failed/err = %d/%d/%v", ok, failed, err)
	}

	b, err := svc.Find(ctx, oauth.ProviderGitLab, "8010")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if b.CredentialID != acct.CredentialID {
		t.Fatalf("降级不该把凭据行删掉或改指:%q", b.CredentialID)
	}
	cred, err := v.View(acct.CredentialID)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if !strings.Contains(cred.Description, "重新用 git 账号登录") {
		t.Fatalf("备注要成人读的因 + 出路:%q", cred.Description)
	}
	if sealed, expires := refreshMaterial(t, db, b.ID); sealed != nil || expires != "" {
		t.Fatalf("续期材料该清干净,否则每轮都撞一次墙:sealed=%v expires=%q", sealed, expires)
	}
	pending, err := svc.ListStaleGrants(ctx, RefreshLead, time.Now().UTC())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("已判失效的映射不该再进队列:%+v", pending)
	}

	// 本人重新走一次登录(带新令牌)→ 同一条凭据复活,备注洗回来源说明。
	heal := syncApp()
	svc2 := NewService(db, users.NewService(db), heal, v)
	restored, err := svc2.Login(ctx, identityOf("8010", "quay"), grantOf("tok-2", 2*time.Hour, "rt-2"))
	if err != nil {
		t.Fatalf("恢复登录: %v", err)
	}
	if restored.CredentialID != acct.CredentialID {
		t.Fatalf("恢复该复用同一条凭据,得到 %q", restored.CredentialID)
	}
	cred, err = v.View(acct.CredentialID)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if strings.Contains(cred.Description, "已失效") {
		t.Fatalf("重新登录后备注没洗回正常态:%q", cred.Description)
	}
	if got, err := v.Reveal(acct.CredentialID); err != nil || got != "tok-2" {
		t.Fatalf("恢复后令牌不对:got=%q err=%v", got, err)
	}
}

// TestRefreshOnlySeesSyncedBindings:续期材料天生只写在「同步过凭据」的映射行上 ——
// 手工录入的凭据、走「连接凭据」那条流程建出来的都没有这几列,扫描器不该碰它们。
// 这里刻意造一条「有时效也有续期凭证、但 credential_id 为空」的行,把筛选条件钉在
// credential_id 上而不是顺带的空值上。
func TestRefreshOnlySeesSyncedBindings(t *testing.T) {
	db := storetest.OpenDB(t)
	loadRoles(t, db)
	v := testVault(db)
	svc := NewService(db, users.NewService(db), syncApp(), v)
	ctx := context.Background()

	first, err := svc.Login(ctx, identityOf("8011", "reef"), grantOf("tok-1", 5*time.Minute, "rt-1"))
	if err != nil {
		t.Fatalf("首登: %v", err)
	}
	// 同一个人的第二条身份:有时效、有续期凭证,但从没同步过凭据(等价于手工在库里补的行)。
	if _, err := db.Exec(
		`INSERT INTO user_identities (id, user_id, provider, external_id, username, email, created_at, updated_at)
		 VALUES ('u-manual', ?, 'gitlab', '8012', 'surf', '', '', '')`, first.UserID,
	); err != nil {
		t.Fatalf("insert manual: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE user_identities SET credential_id = '', git_expires_at = ?, git_refresh_ciphertext = ?
		 WHERE id = 'u-manual'`,
		time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339), []byte{1, 2, 3},
	); err != nil {
		t.Fatalf("补续期材料: %v", err)
	}

	pending, err := svc.ListStaleGrants(ctx, RefreshLead, time.Now().UTC())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 || pending[0].Provider != oauth.ProviderGitLab || pending[0].BindingID == "u-manual" {
		t.Fatalf("只该看见同步过凭据的那条:%+v", pending)
	}
}
