package project

import (
	"context"
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// 「不绑仓库的项目」= 只用来发布(把别处产出的产物/镜像发到目标机),源码这一环整个不存在。
// 规则:仓库可空;空就别挂凭据,别开需要读仓库的开关;解绑时这些东西一并撤掉。

func TestCreate_WithoutRepo(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	pr := &stubProber{branch: "main"}
	svc := New(db, v, pr)

	p, err := svc.Create(context.Background(), CreateInput{Name: "release-only"})
	if err != nil {
		t.Fatalf("无仓库项目应能建: %v", err)
	}
	if p.RepoURL != "" || p.CredentialID != "" || p.DefaultBranch != "" {
		t.Fatalf("纯发布项目不该带仓库/凭据/默认分支, got %+v", p)
	}
	if pr.calls != 0 {
		t.Fatalf("没仓库就不该探测远端, calls = %d", pr.calls)
	}
	// 列表路径同样要读得回来(credential_id 存的是 NULL,靠 COALESCE 归一成空串)。
	items, err := svc.List(context.Background(), access.ListFilter{Unrestricted: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].CredentialID != "" {
		t.Fatalf("List 应把 NULL 凭据读成空串, got %+v", items)
	}
}

// 解绑仓库:凭据引用、默认分支与两个「要读仓库」的开关一并撤掉。
func TestUpdate_UnbindRepo(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	pr := &stubProber{branch: "main"}
	svc := New(db, v, pr)
	credID := newCred(t, v, "tok")

	on := true
	repoURL := "https://gitee.com/a/b.git"
	p, err := svc.Create(context.Background(), CreateInput{Name: "x", RepoURL: repoURL, CredentialID: credID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(context.Background(), p.ID, UpdateInput{PacEnabled: &on, PRStatusEnabled: &on}); err != nil {
		t.Fatalf("开 PaC/PR: %v", err)
	}

	callsBefore := pr.calls
	empty := ""
	got, err := svc.Update(context.Background(), p.ID, UpdateInput{RepoURL: &empty})
	if err != nil {
		t.Fatalf("解绑仓库: %v", err)
	}
	if pr.calls != callsBefore {
		t.Fatalf("解绑不该探测远端, calls before=%d after=%d", callsBefore, pr.calls)
	}
	if got.RepoURL != "" || got.CredentialID != "" || got.DefaultBranch != "" {
		t.Fatalf("解绑后仓库/凭据/分支应清空, got %+v", got)
	}
	if got.PacEnabled || got.PRStatusEnabled {
		t.Fatalf("解绑后 PaC / PR 状态回写应关闭, got pac=%v pr=%v", got.PacEnabled, got.PRStatusEnabled)
	}

	// 再读一次:确实落库了,不是只改了返回值。
	again, err := svc.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if again.RepoURL != "" || again.CredentialID != "" || again.PacEnabled {
		t.Fatalf("解绑未持久化, got %+v", again)
	}
}

// 重新绑仓库:必须带凭据、必须探得通,默认分支没填就用远端 HEAD 兜。
func TestUpdate_BindRepo(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	pr := &stubProber{branch: "release"}
	svc := New(db, v, pr)
	credID := newCred(t, v, "tok")

	p, err := svc.Create(context.Background(), CreateInput{Name: "x"})
	if err != nil {
		t.Fatalf("Create(无仓库): %v", err)
	}

	// 只给仓库不给凭据 → 拒(和新建同一套规则)。
	repoURL := "https://gitee.com/a/b.git"
	if _, err := svc.Update(context.Background(), p.ID, UpdateInput{RepoURL: &repoURL}); !errors.Is(err, ErrEmptyCredentialID) {
		t.Fatalf("绑仓库缺凭据 err = %v, want ErrEmptyCredentialID", err)
	}

	got, err := svc.Update(context.Background(), p.ID, UpdateInput{RepoURL: &repoURL, CredentialID: &credID})
	if err != nil {
		t.Fatalf("绑仓库: %v", err)
	}
	if got.RepoURL != repoURL || got.CredentialID != credID {
		t.Fatalf("绑仓库后 %+v", got)
	}
	if got.DefaultBranch != "release" {
		t.Fatalf("默认分支应由探测兜成 release, got %q", got.DefaultBranch)
	}

	// 探不通就不改(不给项目留坏引用)。
	pr.err = errors.New("boom")
	bad := "https://gitee.com/a/c.git"
	if _, err := svc.Update(context.Background(), p.ID, UpdateInput{RepoURL: &bad, CredentialID: &credID}); err == nil {
		t.Fatal("探测失败时绑仓库应报错")
	}
	if q := mustGet(t, svc, p.ID); q.RepoURL != repoURL {
		t.Fatalf("绑失败后仓库不该变, got %q", q.RepoURL)
	}
}

// 需要读仓库的设置(流水线即代码 / PR 状态回写)在纯发布项目上开不了。
func TestUpdate_RepoNeedingSettingsGuarded(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	svc := New(db, v, &stubProber{branch: "main"})

	p, err := svc.Create(context.Background(), CreateInput{Name: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	on := true
	if _, err := svc.Update(context.Background(), p.ID, UpdateInput{PacEnabled: &on}); !errors.Is(err, ErrRepoRequired) {
		t.Fatalf("无仓库开 PaC err = %v, want ErrRepoRequired", err)
	}
	if _, err := svc.Update(context.Background(), p.ID, UpdateInput{PRStatusEnabled: &on}); !errors.Is(err, ErrRepoRequired) {
		t.Fatalf("无仓库开 PR 回写 err = %v, want ErrRepoRequired", err)
	}
	// 没仓库却单挂一把凭据,同样是「无的放矢」。
	credID := newCred(t, v, "tok")
	if _, err := svc.Update(context.Background(), p.ID, UpdateInput{CredentialID: &credID}); !errors.Is(err, ErrCredentialWithoutRepo) {
		t.Fatalf("无仓库绑凭据 err = %v, want ErrCredentialWithoutRepo", err)
	}
}

// 测试连接是「要读仓库」的操作:空地址直接拒,不放到 git 层撞出误导人的报错。
func TestTestClone_WithoutRepo(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	svc := New(db, v, &stubProber{branch: "main"})
	credID := newCred(t, v, "tok")

	if _, err := svc.TestClone(context.Background(), "", credID); !errors.Is(err, ErrEmptyRepoURL) {
		t.Fatalf("err = %v, want ErrEmptyRepoURL", err)
	}
}

func mustGet(t *testing.T, svc Service, id string) *Project {
	t.Helper()
	p, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return p
}
