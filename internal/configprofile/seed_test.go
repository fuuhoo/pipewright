package configprofile

import (
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
)

func TestSeedIfEmpty_InsertsFirstTime(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	dataDir := t.TempDir()

	n, err := SeedIfEmpty(repo, dataDir)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != len(builtinConfigProfiles) {
		t.Fatalf("seed count = %d, want %d", n, len(builtinConfigProfiles))
	}

	svc := NewService(repo, dataDir)
	list, err := svc.List(ListFilter{IncludeBuiltin: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != len(builtinConfigProfiles) {
		t.Fatalf("list len = %d, want %d", len(list), len(builtinConfigProfiles))
	}
}

func TestSeedIfEmpty_Idempotent(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	dataDir := t.TempDir()

	n1, err := SeedIfEmpty(repo, dataDir)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if n1 == 0 {
		t.Fatal("first seed 应插入")
	}
	n2, err := SeedIfEmpty(repo, dataDir)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second seed 应返回 0, got %d", n2)
	}
}

func TestSeedBuiltinLanguages_Coverage(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range builtinConfigProfiles {
		seen[p.Language] = true
	}
	wantLangs := []string{"node", "python", "java", "go"}
	for _, l := range wantLangs {
		if !seen[l] {
			t.Errorf("seed 缺少语言 %s", l)
		}
	}
}
