package buildenv

import (
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
)

// TestSeedIfEmpty_InsertsFirstTime 验证空 DB 时 seed 插入所有内置条目。
func TestSeedIfEmpty_InsertsFirstTime(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	svc := NewService(repo)

	n, err := SeedIfEmpty(svc)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != len(builtinBuildEnvs) {
		t.Fatalf("seed count = %d, want %d", n, len(builtinBuildEnvs))
	}

	// 验证 List 返回 11 条。
	list, err := svc.List(ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != len(builtinBuildEnvs) {
		t.Fatalf("list len = %d, want %d", len(list), len(builtinBuildEnvs))
	}
}

// TestSeedIfEmpty_Idempotent 验证二次 seed 不会重复插入。
func TestSeedIfEmpty_Idempotent(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	svc := NewService(repo)

	// 第一次 seed
	n1, err := SeedIfEmpty(svc)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if n1 == 0 {
		t.Fatal("first seed 应插入")
	}

	// 第二次 seed:已存在 → 跳过,返回 0
	n2, err := SeedIfEmpty(svc)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second seed 应返回 0, got %d", n2)
	}

	// 验证总数不变
	list, _ := svc.List(ListFilter{})
	if len(list) != len(builtinBuildEnvs) {
		t.Fatalf("after 2nd seed, list = %d, want %d", len(list), len(builtinBuildEnvs))
	}
}

// TestSeedBuiltinLanguages_Coverage 验证 seed 覆盖主流语言。
func TestSeedBuiltinLanguages_Coverage(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range builtinBuildEnvs {
		seen[e.Language] = true
	}
	wantLangs := []string{"node", "python", "java", "go", "ruby", "php", "rust", "alpine"}
	for _, l := range wantLangs {
		if !seen[l] {
			t.Errorf("seed 缺少语言 %s", l)
		}
	}
}
