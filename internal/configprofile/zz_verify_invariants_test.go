package configprofile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
)

// [R13 + P0#3] 原子写:tmp+fsync+rename,DB 与磁盘一致;失败时无残留 tmp。
func TestVerify_P0_3_AtomicWriteAndFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(NewSQLiteRepo(storetest.OpenDB(t)), dir)

	p, err := svc.Create(&ConfigProfile{
		Language: "java", ConfigType: "maven-settings", Name: "sm",
		TargetPath: "/root/.m2/settings.xml", Content: "<settings>v1</settings>",
		CreatedBy: "t",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 磁盘文件存在且内容 == content(权威路径)
	got, err := os.ReadFile(p.FilePath)
	if err != nil {
		t.Fatalf("磁盘文件应存在: %v", err)
	}
	if string(got) != p.Content {
		t.Fatalf("磁盘内容 ≠ DB.content: %q vs %q", got, p.Content)
	}
	// 无 tmp 残留
	entries, _ := os.ReadDir(filepath.Dir(p.FilePath))
	if len(entries) != 1 {
		t.Fatalf("目录应只有正式文件(无 tmp 残留), got %d: %v", len(entries), entries)
	}

	// 更新 content → 磁盘同步覆盖
	upd, err := svc.Update(&ConfigProfile{
		ID: p.ID, Language: p.Language, ConfigType: p.ConfigType, Name: p.Name,
		TargetPath: p.TargetPath, Content: "<settings>v2</settings>",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, err := os.ReadFile(upd.FilePath)
	if err != nil || string(got2) != "<settings>v2</settings>" {
		t.Fatalf("更新后磁盘应为 v2, got %q err=%v", got2, err)
	}
	// DB.content 冗余快照同步
	if upd.Content != "<settings>v2</settings>" {
		t.Fatalf("DB.content 应同步为 v2, got %q", upd.Content)
	}
	entries, _ = os.ReadDir(filepath.Dir(p.FilePath))
	if len(entries) != 1 {
		t.Fatalf("更新后仍应只有 1 个文件, got %d", len(entries))
	}
}

// [§3.3] builtin 行字段白名单:仅 description/enabled 可改,其余 → ErrBuiltinReadonly。
func TestVerify_BuiltinFieldWhitelist(t *testing.T) {
	dir := t.TempDir()
	repo := NewSQLiteRepo(storetest.OpenDB(t))
	if _, err := SeedIfEmpty(repo, dir); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := NewService(repo, dir)
	list, err := svc.List(ListFilter{IncludeBuiltin: true})
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v (%d)", err, len(list))
	}
	b := list[0]
	if !b.IsBuiltin {
		t.Fatal("seed 行应为 builtin")
	}

	// 1) 改 description → OK
	ok, err := svc.Update(&ConfigProfile{
		ID: b.ID, Language: b.Language, ConfigType: b.ConfigType, Name: b.Name,
		TargetPath: b.TargetPath, FilePath: b.FilePath, Content: b.Content,
		IsDefault: b.IsDefault, IsBuiltin: b.IsBuiltin,
		Description: "新说明", Enabled: b.Enabled,
	})
	if err != nil {
		t.Fatalf("builtin 改 description 应成功: %v", err)
	}
	if ok.Description != "新说明" {
		t.Fatalf("description 未更新: %q", ok.Description)
	}
	// 磁盘未被 builtin 更新重写(白名单只碰 DB)
	onDisk, _ := os.ReadFile(b.FilePath)
	if string(onDisk) != b.Content {
		t.Fatalf("builtin description 更新不应重写磁盘文件")
	}

	// 2) 改 target_path / content / name / language → 拒
	for name, f := range map[string]func() *ConfigProfile{
		"content":     func() *ConfigProfile { return &ConfigProfile{ID: b.ID, Language: b.Language, ConfigType: b.ConfigType, Name: b.Name, TargetPath: b.TargetPath, FilePath: b.FilePath, Content: "hacked", Description: "x", Enabled: true} },
		"target_path": func() *ConfigProfile { return &ConfigProfile{ID: b.ID, Language: b.Language, ConfigType: b.ConfigType, Name: b.Name, TargetPath: "/etc/passwd", FilePath: b.FilePath, Content: b.Content, IsDefault: b.IsDefault, Description: "x"} },
		"name":        func() *ConfigProfile { return &ConfigProfile{ID: b.ID, Language: b.Language, ConfigType: b.ConfigType, Name: "renamed", TargetPath: b.TargetPath, FilePath: b.FilePath, Content: b.Content, IsDefault: b.IsDefault, Description: "x"} },
		"language":    func() *ConfigProfile { return &ConfigProfile{ID: b.ID, Language: "ruby", ConfigType: b.ConfigType, Name: b.Name, TargetPath: b.TargetPath, FilePath: b.FilePath, Content: b.Content, IsDefault: b.IsDefault, Description: "x"} },
	} {
		if _, err := svc.Update(f()); err == nil {
			t.Errorf("builtin 改 %s 应被拒(ErrBuiltinReadonly), got nil", name)
		}
	}
}

// [§3.3] 内置配置资源不可通过 web API 创建;is_builtin=1 由 seed 直接灌。
func TestVerify_BuiltinCannotBeCreatedViaAPI(t *testing.T) {
	svc := NewService(NewSQLiteRepo(storetest.OpenDB(t)), t.TempDir())
	_, err := svc.Create(&ConfigProfile{
		Language: "java", ConfigType: "maven-settings", Name: "x",
		TargetPath: "/tmp/x.xml", Content: "y", IsBuiltin: true,
	})
	if err == nil {
		t.Fatal("is_builtin=1 应被 Service.Create 拒绝")
	}
}

// [§3.3] 权威路径:读时优先磁盘文件。文件被删时回退 DB.content(空则报错)。
func TestVerify_AuthoritativePathIsFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(NewSQLiteRepo(storetest.OpenDB(t)), dir)
	p, err := svc.Create(&ConfigProfile{
		Language: "node", ConfigType: "npmrc", Name: "n",
		TargetPath: "/root/.npmrc", Content: "registry=x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(p.FilePath); err != nil {
		t.Fatalf("file_path 应指向真实文件: %v", err)
	}
	// file_path 在 DATA_DIR 下固定位置
	if !filepath.IsAbs(p.FilePath) || filepath.Dir(filepath.Dir(filepath.Dir(p.FilePath))) != dir {
		t.Fatalf("file_path 应在 DATA_DIR 下: %s (dir=%s)", p.FilePath, dir)
	}
}
