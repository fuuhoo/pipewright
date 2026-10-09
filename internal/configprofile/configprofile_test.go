package configprofile_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fuuhoo/pipewright/internal/configprofile"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

func newRepo(t *testing.T) configprofile.Repo {
	return configprofile.NewSQLiteRepo(sharedDB(t))
}

func newService(t *testing.T) *configprofile.Service {
	dataDir := t.TempDir() + "/data"
	return configprofile.NewService(newRepo(t), dataDir)
}

// sharedDB 同 buildenv 包设计:每测试一个独立 DB,但同一 t 内 helper 共享。
var dbCache sync.Map

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

func mkInput() *configprofile.ConfigProfile {
	return &configprofile.ConfigProfile{
		Language:    "java",
		ConfigType:  "maven",
		Name:        "默认 Maven",
		TargetPath:  "/root/.m2/settings.xml",
		Content:     "<settings><localRepository/></settings>",
		IsDefault:   true,
		IsBuiltin:   false,
		Description: "内置阿里云 Maven 镜像",
		Enabled:     true,
		CreatedBy:   "00000000-0000-0000-0000-000000000001",
	}
}

// TestAtomicWriteFile 验证原子写语义:写时正常,文件存在;删除后写也正常。
func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.xml")
	content := []byte("<x/>")

	if err := configprofile.AtomicWriteFile(target, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("内容不一致: got %q want %q", got, content)
	}

	// 覆盖写
	content2 := []byte("<y/>")
	if err := configprofile.AtomicWriteFile(target, content2, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got2, _ := os.ReadFile(target)
	if string(got2) != string(content2) {
		t.Fatalf("rewrite 内容不一致: got %q", got2)
	}
}

// TestCreate_AtomicWrite 验证 Create 触发磁盘文件 + DB 行。
func TestCreate_AtomicWrite(t *testing.T) {
	svc := newService(t)
	in := mkInput()
	out, err := svc.Create(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// DB 有行
	got, err := svc.GetByID(out.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// 磁盘有文件
	if _, err := os.Stat(got.FilePath); err != nil {
		t.Fatalf("磁盘文件缺: %v", err)
	}

	content, err := svc.ReadDiskContent(got)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if content != in.Content {
		t.Fatalf("磁盘内容不一致")
	}
}

// TestCreate_RejectsBuiltin API 不允许创建内置。
func TestCreate_RejectsBuiltin(t *testing.T) {
	svc := newService(t)
	in := mkInput()
	in.IsBuiltin = true
	if _, err := svc.Create(in); err == nil {
		t.Fatal("预期拒绝")
	}
}

// TestConflict 验证 UNIQUE(language, config_type, name)。
func TestConflict(t *testing.T) {
	svc := newService(t)
	if _, err := svc.Create(mkInput()); err != nil {
		t.Fatalf("first: %v", err)
	}
	b := mkInput()
	b.Content = "different content"
	if _, err := svc.Create(b); err == nil {
		t.Fatal("预期 ErrConflict")
	}
}

// TestUpload 验证 multipart 上传路径(等价 Create)。
func TestUpload(t *testing.T) {
	svc := newService(t)
	out, err := svc.Upload(&configprofile.UploadInput{
		Language:    "node",
		ConfigType:  "npm",
		Name:        "淘宝源",
		TargetPath:  "/root/.npmrc",
		Filename:    "settings.npmrc",
		Content:     []byte("registry=https://registry.npmmirror.com\n"),
		Description: "淘宝源",
		CreatedBy:   "test",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	got, _ := svc.GetByID(out.ID)
	if _, err := os.Stat(got.FilePath); err != nil {
		t.Fatalf("upload 磁盘文件缺: %v", err)
	}
}

// TestUpload_BadExt 验证扩展名白名单。
func TestUpload_BadExt(t *testing.T) {
	svc := newService(t)
	_, err := svc.Upload(&configprofile.UploadInput{
		Language:   "node",
		ConfigType: "npm",
		Name:       "evil",
		TargetPath: "/root/.npmrc",
		Filename:   "evil.exe",
		Content:    []byte("xx"),
		CreatedBy:  "test",
	})
	if err == nil {
		t.Fatal("预期拒绝")
	}
}

// TestUpdate_NonBuiltin_RewritesFile 非内置更新触发重写。
func TestUpdate_NonBuiltin_RewritesFile(t *testing.T) {
	svc := newService(t)
	out, _ := svc.Create(mkInput())
	got, _ := svc.GetByID(out.ID)

	got.Content = "new content"
	got.Description = "改"
	if _, err := svc.Update(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	content, _ := svc.ReadDiskContent(got)
	if content != "new content" {
		t.Fatalf("磁盘内容未更新: %q", content)
	}
}

// TestUpdate_Builtin_FieldWhitelist is_builtin=1 行字段白名单:允许 description/enabled,
// 其他字段变更 → 拒。
func TestUpdate_Builtin_FieldWhitelist(t *testing.T) {
	svc := newService(t)
	in := mkInput()
	in.IsBuiltin = true
	// is_builtin=1 不能通过 Create(API 限制),改用 repo.Create 直注
	if err := configprofile.NewSQLiteRepo(sharedDB(t)).Create(in); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}

	got, _ := svc.GetByID(in.ID)
	got.Description = "新说明"
	if _, err := svc.Update(got); err != nil {
		t.Fatalf("改 description 应被允许: %v", err)
	}
	got2, _ := svc.GetByID(in.ID)
	if got2.Description != "新说明" {
		t.Fatal("description 未更新")
	}

	// 改 content 应被拒
	got3, _ := svc.GetByID(in.ID)
	got3.Content = "被禁"
	if _, err := svc.Update(got3); err == nil {
		t.Fatal("改 content 应被拒")
	}

	// 改 enabled 应被允许(先 GetByID 重置 Content,因为前面 Content 改过未被持久化)
	got3, _ = svc.GetByID(in.ID)
	got3.Enabled = false
	if _, err := svc.Update(got3); err != nil {
		t.Fatalf("改 enabled 应被允许: %v", err)
	}
}

// TestDelete_NonBuiltin 删除非内置:DB + 磁盘文件 + 目录都被清。
func TestDelete_NonBuiltin(t *testing.T) {
	svc := newService(t)
	out, _ := svc.Create(mkInput())
	got, _ := svc.GetByID(out.ID)
	filePath := got.FilePath

	if err := svc.Delete(out.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetByID(out.ID); err == nil {
		t.Fatal("GetById 应报 not found")
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("磁盘文件应被删: %v", err)
	}
}

// TestDelete_Builtin_Refuses is_builtin=1 行不可删。
func TestDelete_Builtin_Refuses(t *testing.T) {
	svc := newService(t)
	in := mkInput()
	in.IsBuiltin = true
	if err := configprofile.NewSQLiteRepo(sharedDB(t)).Create(in); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.Delete(in.ID); err == nil {
		t.Fatal("预期拒绝")
	}
}

// TestIsExtAllowed 验证扩展名白名单函数。
func TestIsExtAllowed(t *testing.T) {
	allowed := []string{".xml", ".conf", ".npmrc", ".ini", ".env", ".toml", ".yaml", ".yml", ".YML"}
	denied := []string{"", ".exe", ".sh", ".zip", "noext"}
	for _, e := range allowed {
		if !configprofile.IsExtAllowed("file" + e) {
			t.Fatalf("应允许 %s", e)
		}
	}
	for _, e := range denied {
		if configprofile.IsExtAllowed("file" + e) {
			t.Fatalf("应拒绝 %s", e)
		}
	}
}

// TestMigration0052 验证 0052 迁移跑通。
func TestMigration0052(t *testing.T) {
	db := sharedDB(t)

	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM config_profiles`).Scan(&n); err != nil {
		t.Fatalf("表不可读: %v", err)
	}
}

// createForTest 建一条非内置配置资源并回读(带 FilePath 的完整行)。
func createForTest(t *testing.T, svc *configprofile.Service) *configprofile.ConfigProfile {
	t.Helper()
	created, err := svc.Create(mkInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.GetByID(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return got
}

// seedBuiltinForTest 直插一条内置行并写好磁盘副本(Create 拒绝 is_builtin=1)。
func seedBuiltinForTest(t *testing.T, svc *configprofile.Service) *configprofile.ConfigProfile {
	t.Helper()
	in := mkInput()
	in.ID = "sec-test-builtin"
	in.Name = "内置行" // 与 mkInput 的 (language,configType,name) 区分,便于同测并存
	in.IsBuiltin = true
	in.Content = "内置正文"
	in.FilePath = configprofile.ProfilePath(svc.DataDir(), in.ID, filepath.Base(in.TargetPath))
	if err := configprofile.NewSQLiteRepo(sharedDB(t)).Create(in); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}
	if err := configprofile.AtomicWriteFile(in.FilePath, []byte(in.Content), 0o644); err != nil {
		t.Fatalf("seed builtin 写文件: %v", err)
	}
	got, err := svc.GetByID(in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return got
}

// TestReplaceFile_OverwritesDisk 重新上传只换正文:磁盘副本、DB 快照同步,身份字段不动,
// 落盘文件名仍取 target_path 基名(与 Create/Upload 同一套规则)。
func TestReplaceFile_OverwritesDisk(t *testing.T) {
	svc := newService(t)
	old := createForTest(t, svc)

	out, err := svc.ReplaceFile(&configprofile.ReplaceFileInput{
		ID:       old.ID,
		Filename: "公司私服配置.xml",
		Content:  []byte("<settings><mirror/></settings>"),
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if filepath.Base(out.FilePath) != "settings.xml" {
		t.Fatalf("落盘文件名应取 target_path 基名: %s", out.FilePath)
	}
	if out.Language != old.Language || out.Name != old.Name || out.TargetPath != old.TargetPath {
		t.Fatalf("身份字段被改动: %+v", out)
	}
	if out.Description != old.Description || out.IsDefault != old.IsDefault || out.Enabled != old.Enabled {
		t.Fatalf("其余字段应保持不变: %+v", out)
	}
	disk, err := svc.ReadDiskContent(out)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if disk != "<settings><mirror/></settings>" {
		t.Fatalf("磁盘正文未替换: %q", disk)
	}
	if out.Content != disk {
		t.Fatal("DB 快照与磁盘副本不一致")
	}
}

// TestReplaceFile_TargetPathMovesFile 换 target_path 时写到新文件名,旧文件清掉不留垃圾。
func TestReplaceFile_TargetPathMovesFile(t *testing.T) {
	svc := newService(t)
	old := createForTest(t, svc)

	out, err := svc.ReplaceFile(&configprofile.ReplaceFileInput{
		ID:         old.ID,
		Filename:   "config.xml",
		Content:    []byte("<new/>"),
		TargetPath: "/root/.m2/settings-alt.xml",
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if out.TargetPath != "/root/.m2/settings-alt.xml" {
		t.Fatalf("target_path 未更新: %s", out.TargetPath)
	}
	if _, err := os.Stat(out.FilePath); err != nil {
		t.Fatalf("新文件缺: %v", err)
	}
	if _, err := os.Stat(old.FilePath); !os.IsNotExist(err) {
		t.Fatalf("旧文件应被清掉: %v", err)
	}
}

// TestReplaceFile_Rejects 内置行 / 非法扩展名 / 空正文都要拒。
func TestReplaceFile_Rejects(t *testing.T) {
	svc := newService(t)
	builtin := seedBuiltinForTest(t, svc)
	row := createForTest(t, svc)

	cases := []struct {
		name string
		in   *configprofile.ReplaceFileInput
		want error
	}{
		{"builtin", &configprofile.ReplaceFileInput{ID: builtin.ID, Filename: "a.xml", Content: []byte("x")}, configprofile.ErrBuiltinReadonly},
		{"badExt", &configprofile.ReplaceFileInput{ID: row.ID, Filename: "a.exe", Content: []byte("x")}, configprofile.ErrInvalidInput},
		{"empty", &configprofile.ReplaceFileInput{ID: row.ID, Filename: "a.xml", Content: []byte("  \n")}, configprofile.ErrInvalidInput},
	}
	for _, c := range cases {
		if _, err := svc.ReplaceFile(c.in); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := svc.ReplaceFile(&configprofile.ReplaceFileInput{ID: "no-such-id", Filename: "a.xml", Content: []byte("x")}); !errors.Is(err, configprofile.ErrNotFound) {
		t.Fatalf("不存在的 id: err = %v, want ErrNotFound", err)
	}
	// 拒绝路径不能碰原文件
	got, _ := svc.GetByID(row.ID)
	if disk, err := svc.ReadDiskContent(got); err != nil || disk != row.Content {
		t.Fatalf("失败请求改动了文件: %q %v", disk, err)
	}
}

// TestUpdate_EmptyContentKeepsFile 编辑弹窗没动正文时 PUT 传空 content:
// 既不能报「content 必填」,也不能把磁盘文件清空。
func TestUpdate_EmptyContentKeepsFile(t *testing.T) {
	svc := newService(t)
	row := createForTest(t, svc)

	in := *row
	in.Content = ""
	in.Description = "只改说明"
	out, err := svc.Update(&in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Content != row.Content {
		t.Fatalf("空 content 应沿用原正文: %q", out.Content)
	}
	if out.Description != "只改说明" {
		t.Fatalf("description 未更新: %q", out.Description)
	}
	disk, err := svc.ReadDiskContent(out)
	if err != nil || disk != row.Content {
		t.Fatalf("磁盘文件被动过: %q %v", disk, err)
	}
}

// TestUpdate_BuiltinEmptyContentAllowsDescription 内置行同理:空 content 不算改正文,
// 所以「改说明 / 启停」仍然放行,而带非空不同 content 的请求照旧 403 语义。
func TestUpdate_BuiltinEmptyContentAllowsDescription(t *testing.T) {
	svc := newService(t)
	row := seedBuiltinForTest(t, svc)

	in := *row
	in.Content = ""
	in.Description = "新说明"
	in.Enabled = false
	if _, err := svc.Update(&in); err != nil {
		t.Fatalf("内置行改说明应放行: %v", err)
	}
	got, _ := svc.GetByID(row.ID)
	if got.Description != "新说明" || got.Enabled {
		t.Fatalf("未生效: %+v", got)
	}
	if got.Content != "内置正文" {
		t.Fatalf("内置正文被改动: %q", got.Content)
	}
}

// TestContentForView 详情展示优先磁盘副本;磁盘读不到才回退 DB 快照并标明来源。
func TestContentForView(t *testing.T) {
	svc := newService(t)
	row := createForTest(t, svc)

	// 磁盘与 DB 故意不一致:以磁盘为准(运行时注入的就是磁盘那份)
	if err := configprofile.AtomicWriteFile(row.FilePath, []byte("磁盘上的正文"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	content, fromDisk := svc.ContentForView(row)
	if !fromDisk || content != "磁盘上的正文" {
		t.Fatalf("应回磁盘内容: %q fromDisk=%v", content, fromDisk)
	}

	if err := os.Remove(row.FilePath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	content, fromDisk = svc.ContentForView(row)
	if fromDisk {
		t.Fatal("文件丢失时不能标成磁盘来源")
	}
	if content != row.Content {
		t.Fatalf("应回退到 DB 快照: %q", content)
	}
}
