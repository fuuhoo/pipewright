package buildenv_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/huangchengsir/pipewright/internal/buildenv"
	"github.com/huangchengsir/pipewright/internal/storetest"
)

func newRepo(t *testing.T) buildenv.Repo {
	return buildenv.NewSQLiteRepo(sharedDB(t))
}

func newService(t *testing.T) *buildenv.Service {
	return buildenv.NewService(newRepo(t))
}

// sharedDB 跨同一 t 内的所有 newRepo/newService 调用复用同一 DB。
// 注意:storetest.OpenDB 每次调用都新开一个 t.TempDir() 文件库;若每 helper
// 各开一个库,Create 在 A 库 / Get 在 B 库,自然找不到。这里用一个 package-level
// 缓存 + t.Cleanup 释放。
var dbCache sync.Map // t.Name() -> *sql.DB

func sharedDB(t *testing.T) *sql.DB {
	t.Helper()
	if v, ok := dbCache.Load(t.Name()); ok {
		return v.(*sql.DB)
	}
	st := storetest.Open(t)
	dbCache.Store(t.Name(), st.DB)
	t.Cleanup(func() {
		dbCache.Delete(t.Name())
		_ = st.Close()
	})
	return st.DB
}

func mkInput() *buildenv.BuildEnv {
	return &buildenv.BuildEnv{
		Language:    "java",
		Version:     "11",
		DisplayName: "Java 11 (Temurin)",
		Description: "JDK 11",
		SourceType:  buildenv.SourceOfficial,
		Image:       "eclipse-temurin:11-jdk",
		Enabled:     true,
		SortOrder:   10,
	}
}

// TestCreate_Get_Update_Delete 基础 CRUD。
func TestCreate_Get_Update_Delete(t *testing.T) {
	svc := newService(t)

	in := mkInput()
	out, err := svc.Create(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.ID == "" {
		t.Fatal("id 必填")
	}
	if out.ImageCheckStatus != buildenv.StatusUnchecked {
		t.Fatalf("默认状态应 unchecked: got %q", out.ImageCheckStatus)
	}

	got, err := svc.GetByID(out.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Image != out.Image {
		t.Fatalf("image 不一致: got %q want %q", got.Image, out.Image)
	}

	// Update description(不动 image)
	upd := *got
	upd.Description = "更新"
	if _, err := svc.Update(&upd); err != nil {
		t.Fatalf("update: %v", err)
	}

	got2, _ := svc.GetByID(out.ID)
	if got2.Description != "更新" {
		t.Fatalf("update 后 description 未持久化: %q", got2.Description)
	}

	if err := svc.Delete(out.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetByID(out.ID); !errors.Is(err, buildenv.ErrNotFound) {
		t.Fatalf("delete 后 get: 预期 ErrNotFound,got %v", err)
	}
}

// TestConflict 验证 UNIQUE(language, version)。
func TestConflict(t *testing.T) {
	svc := newService(t)

	a := mkInput()
	if _, err := svc.Create(a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	b := mkInput()
	b.DisplayName = "Java 11 again"
	b.Image = "different:11"
	if _, err := svc.Create(b); !errors.Is(err, buildenv.ErrConflict) {
		t.Fatalf("预期 ErrConflict,got %v", err)
	}
}

// TestUpdate_ResetsCheckOnImageChange 修改 image → 状态重置 unchecked。
func TestUpdate_ResetsCheckOnImageChange(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	out, err := svc.Create(mkInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 模拟"已检查可用"状态
	if err := repo.UpdateCheckStatus(out.ID, buildenv.StatusAvailable, "", "2024-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed available: %v", err)
	}

	// 改 image
	upd, _ := svc.GetByID(out.ID)
	upd.Image = "eclipse-temurin:11-jre"
	if _, err := svc.Update(upd); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := svc.GetByID(out.ID)
	if got.ImageCheckStatus != buildenv.StatusUnchecked {
		t.Fatalf("改 image 后状态应 reset 为 unchecked,got %q", got.ImageCheckStatus)
	}
	if got.ImageCheckedAt != nil {
		t.Fatalf("改 image 后 checked_at 应清空,got %v", got.ImageCheckedAt)
	}
}

// TestSetEnabled_TriState P0 #4 三态校验。
func TestSetEnabled_TriState(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	out, err := svc.Create(mkInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 1. unavailable 状态 → 启用拒
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusUnavailable, "no such image", "2024-01-01T00:00:00Z")
	if err := svc.SetEnabled(out.ID, true); err == nil {
		t.Fatal("unavailable 启用应被拒")
	} else if vErr, ok := err.(*buildenv.ValidationError); !ok || vErr.Code != "IMAGE_UNAVAILABLE" {
		t.Fatalf("预期 IMAGE_UNAVAILABLE ValidationError,got %v", err)
	}

	// 2. unchecked 状态 → 默认拒
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusUnchecked, "", "")
	if err := svc.SetEnabled(out.ID, true); err == nil {
		t.Fatal("unchecked 启用应默认拒")
	} else if vErr, ok := err.(*buildenv.ValidationError); !ok || vErr.Code != "IMAGE_NOT_CHECKED" {
		t.Fatalf("预期 IMAGE_NOT_CHECKED ValidationError,got %v", err)
	}

	// 3. available 状态 → 允许
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusAvailable, "", "2024-01-01T00:00:00Z")
	if err := svc.SetEnabled(out.ID, true); err != nil {
		t.Fatalf("available 启用应允许: %v", err)
	}

	// 4. 禁用(unavailable 也允许)
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusUnavailable, "", "")
	if err := svc.SetEnabled(out.ID, false); err != nil {
		t.Fatalf("禁用永远允许: %v", err)
	}
}

// TestSetEnabled_AllowUnchecked_Emergency 验证 PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE 紧急逃生。
func TestSetEnabled_AllowUnchecked_Emergency(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	out, _ := svc.Create(mkInput())
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusUnchecked, "", "")

	// 默认拒绝
	if err := svc.SetEnabled(out.ID, true); err == nil {
		t.Fatal("默认应拒")
	}

	// 紧急逃生
	t.Setenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE", "true")
	if err := svc.SetEnabled(out.ID, true); err != nil {
		t.Fatalf("逃生开关应放行: %v", err)
	}
}

// TestListEnabledLanguages 验证语言去重。
func TestListEnabledLanguages(t *testing.T) {
	svc := newService(t)

	a := mkInput()
	a.Language = "java"
	a.Version = "11"
	svc.Create(a)

	b := mkInput()
	b.Language = "java"
	b.Version = "17"
	svc.Create(b)

	c := mkInput()
	c.Language = "node"
	c.Version = "22"
	svc.Create(c)

	d := mkInput()
	d.Language = "go"
	d.Version = "1.22"
	d.Enabled = false // 禁用,不应出现在列表
	svc.Create(d)

	langs, err := svc.ListEnabledLanguages(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 应为 [go, java, node](字典序;go 应被排除因 enabled=0)
	// 实际: java, node(d 禁用)
	got := map[string]bool{}
	for _, l := range langs {
		got[l] = true
	}
	if !got["java"] || !got["node"] {
		t.Fatalf("应包含 java + node,got %v", langs)
	}
	if got["go"] {
		t.Fatalf("go 应被排除(禁用),got %v", langs)
	}
}

// TestResolver_NotFound 验证 env_resolver 行为。
func TestResolver_NotFound(t *testing.T) {
	repo := newRepo(t)
	_, err := buildenv.ResolveImage(context.Background(), "ghost", "0.0", repo)
	if err == nil {
		t.Fatal("预期错误")
	}
	vErr, ok := err.(*buildenv.ValidationError)
	if !ok || vErr.Code != "ENV_NOT_FOUND" {
		t.Fatalf("预期 ENV_NOT_FOUND,got %v", err)
	}
}

func TestResolver_Unavailable(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	in := mkInput()
	in.Language = "python"
	in.Version = "3.11"
	in.Image = "python:3.11-slim"
	out, _ := svc.Create(in)
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusUnavailable, "no image", "")

	_, err := buildenv.ResolveImage(context.Background(), "python", "3.11", repo)
	vErr, ok := err.(*buildenv.ValidationError)
	if !ok || vErr.Code != "IMAGE_UNAVAILABLE" {
		t.Fatalf("预期 IMAGE_UNAVAILABLE,got %v", err)
	}
}

func TestResolver_Disabled(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	in := mkInput()
	in.Language = "ruby"
	in.Version = "3.2"
	in.Image = "ruby:3.2"
	in.Enabled = false
	svc.Create(in)

	_, err := buildenv.ResolveImage(context.Background(), "ruby", "3.2", repo)
	vErr, ok := err.(*buildenv.ValidationError)
	if !ok || vErr.Code != "ENV_DISABLED" {
		t.Fatalf("预期 ENV_DISABLED,got %v", err)
	}
}

func TestResolver_OK(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	in := mkInput()
	in.Language = "java"
	in.Version = "17"
	in.Image = "eclipse-temurin:17-jdk"
	svc.Create(in)

	img, err := buildenv.ResolveImage(context.Background(), "java", "17", repo)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if img != "eclipse-temurin:17-jdk" {
		t.Fatalf("image 不一致: got %q", img)
	}
}

func TestResolverByID(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	out, _ := svc.Create(mkInput())

	img, err := buildenv.ResolveByID(context.Background(), out.ID, repo)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if img != out.Image {
		t.Fatalf("got %q want %q", img, out.Image)
	}

	// 不存在的 id
	_, err = buildenv.ResolveByID(context.Background(), uuid.NewString(), repo)
	if err == nil {
		t.Fatal("not found id 应报错")
	}
}

// TestValidate 验证入参校验。
func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*buildenv.BuildEnv)
		want bool
	}{
		{"ok", func(*buildenv.BuildEnv) {}, true},
		{"empty lang", func(e *buildenv.BuildEnv) { e.Language = "" }, false},
		{"empty ver", func(e *buildenv.BuildEnv) { e.Version = "" }, false},
		{"empty display", func(e *buildenv.BuildEnv) { e.DisplayName = "" }, false},
		{"empty image", func(e *buildenv.BuildEnv) { e.Image = "" }, false},
		{"bad source", func(e *buildenv.BuildEnv) { e.SourceType = "bogus" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := mkInput()
			c.mut(e)
			err := e.Validate()
			if c.want && err != nil {
				t.Fatalf("want ok,got %v", err)
			}
			if !c.want && err == nil {
				t.Fatalf("want error,got nil")
			}
		})
	}
}

// TestUpdate_UnavailableForcesDisabled 验证 unavailable 状态更新时强制 enabled=false。
func TestUpdate_UnavailableForcesDisabled(t *testing.T) {
	svc := newService(t)
	repo := newRepo(t)
	out, _ := svc.Create(mkInput())
	_ = repo.UpdateCheckStatus(out.ID, buildenv.StatusUnavailable, "x", "")

	upd, _ := svc.GetByID(out.ID)
	upd.Description = "改点别的"
	upd.Enabled = true // 试图启用 → 应被强制为 false
	_, err := svc.Update(upd)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := svc.GetByID(out.ID)
	if got.Enabled {
		t.Fatal("unavailable 状态 + Enabled=true 入参,Update 后应被强制为 false")
	}
}