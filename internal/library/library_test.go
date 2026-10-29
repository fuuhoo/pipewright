package library

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// testMasterKey 返回确定性测试用 master key。
func testMasterKey() *[32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 7)
	}
	return &k
}

// testDB 打开临时 SQLite(含全部迁移)。
func testDB(t *testing.T) *sql.DB {
	return storetest.OpenDB(t)
}

// seedProject 插入一个最小凭据 + 项目,返回 project id(供应用模板/项目存在性)。
func seedProject(t *testing.T, db *sql.DB) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	credID := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
		 VALUES (?, 'c', 'git_token', '', X'00', 'm', ?, ?)`,
		credID, now, now,
	); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	projID := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, repo_url, default_branch, credential_id, created_at, updated_at)
		 VALUES (?, 'p', 'https://example.com/p.git', 'main', ?, ?, ?)`,
		projID, credID, now, now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return projID
}

// sampleSpec 返回一个最小合法 spec(恰一个源阶段 + 一个构建阶段)。
func sampleSpec() pipeline.Spec {
	return pipeline.Spec{Stages: []pipeline.Stage{
		{ID: "src", Name: "源", Kind: pipeline.KindSource, Jobs: []pipeline.Job{
			{ID: "j1", Name: "git", Type: "git_source"},
		}},
		{ID: "build", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
			{ID: "j2", Name: "compile", Type: "build", Config: map[string]any{pipeline.ConfigKeyArtifactType: pipeline.ArtifactJAR}},
		}},
	}}
}

// ---- 模板 ----

func TestTemplateCreateApplyProducesValidPipeline(t *testing.T) {
	db := testDB(t)
	projID := seedProject(t, db)
	pipes := pipeline.New(db)
	svc := NewTemplateService(db, pipes)
	ctx := context.Background()

	tpl, err := svc.Create(ctx, TemplateInput{Name: "go-service", Description: "Go 服务模板", Spec: sampleSpec()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tpl.ID == "" || len(tpl.Spec.Stages) != 2 {
		t.Fatalf("unexpected template: %+v", tpl)
	}

	// 应用模板到项目:产出合法流水线(经 pipeline.Save 再校验/渲染)。
	cfg, err := svc.Apply(ctx, tpl.ID, projID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(cfg.Spec.Stages) != 2 {
		t.Fatalf("applied pipeline stages = %d, want 2", len(cfg.Spec.Stages))
	}
	if strings.TrimSpace(cfg.YAML) == "" {
		t.Fatalf("applied pipeline should render YAML")
	}
	// 应用后,项目流水线读回与模板一致。
	got, err := pipes.Get(ctx, projID)
	if err != nil {
		t.Fatalf("pipeline Get after apply: %v", err)
	}
	if len(got.Spec.Stages) != 2 {
		t.Fatalf("project pipeline stages = %d, want 2", len(got.Spec.Stages))
	}
}

func TestTemplateCreateRejectsInvalidSpec(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	// 无源阶段 → pipeline.NormalizeSpec 拒绝 → ErrInvalidTemplate。
	_, err := svc.Create(context.Background(), TemplateInput{
		Name: "bad",
		Spec: pipeline.Spec{Stages: []pipeline.Stage{
			{Name: "构建", Kind: pipeline.KindBuild},
		}},
	})
	if err == nil {
		t.Fatalf("expected error for spec without source stage")
	}
	if want := ErrInvalidTemplate; !isWrapped(err, want) {
		t.Fatalf("err = %v, want wrapped %v", err, want)
	}
}

func TestTemplateCreateRejectsEmptyName(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	_, err := svc.Create(context.Background(), TemplateInput{Name: "  ", Spec: sampleSpec()})
	if !isWrapped(err, ErrInvalidTemplate) {
		t.Fatalf("err = %v, want ErrInvalidTemplate", err)
	}
}

func TestTemplateDuplicateName(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	if _, err := svc.Create(ctx, TemplateInput{Name: "dup", Spec: sampleSpec()}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(ctx, TemplateInput{Name: "dup", Spec: sampleSpec()})
	if err != ErrTemplateNameTaken {
		t.Fatalf("err = %v, want ErrTemplateNameTaken", err)
	}
}

func TestTemplateUpdateOverwritesNameAndSpec(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	got, err := svc.Create(ctx, TemplateInput{Name: "old", Description: "第一版", Spec: sampleSpec()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 改名 + 换内容:构建阶段撤掉,只剩源阶段会被 NormalizeSpec 拒(恰一个源但仍要至少……),
	// 所以这里给一份同构但阶段名不同的 spec,证明 stages_json 真的被覆盖而不是追加。
	upd, err := svc.Update(ctx, got.ID, TemplateInput{
		Name:        "new",
		Description: "第二版",
		Spec: pipeline.Spec{Stages: []pipeline.Stage{
			{Name: "源", Kind: pipeline.KindSource, Jobs: []pipeline.Job{{Name: "git", Type: "git_source"}}},
			{Name: "部署", Kind: pipeline.KindDeploy, Jobs: []pipeline.Job{{Name: "push", Type: "deploy",
				Config: map[string]any{pipeline.ConfigKeyServerID: "srv-1"}}}},
		}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Name != "new" || upd.Description != "第二版" {
		t.Fatalf("元数据没改到位: %+v", upd)
	}
	if len(upd.Spec.Stages) != 2 || upd.Spec.Stages[1].Name != "部署" {
		t.Fatalf("stages 未覆盖: %+v", upd.Spec.Stages)
	}
	// updated_at 是秒精度(RFC3339 落库),同一秒内的建→改比不出大小,断言它会随机红。
	// 这里钉得住、也更该钉的是「覆盖内容不动出生时间」——列表页按它排序,改一次就变新创建时间算 bug。
	if !upd.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("更新改动了 created_at: got=%v upd=%v", got.CreatedAt, upd.CreatedAt)
	}
	// 覆盖后的模板仍要能被应用 —— 否则「改完就废了」。
	projID := seedProject(t, db)
	cfg, err := svc.Apply(ctx, got.ID, projID)
	if err != nil || len(cfg.Spec.Stages) != 2 {
		t.Fatalf("Apply 覆盖后的模板 = %+v, err=%v", cfg, err)
	}
}

func TestTemplateUpdateNotFound(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	_, err := svc.Update(context.Background(), "missing", TemplateInput{Name: "x", Spec: sampleSpec()})
	if err != ErrTemplateNotFound {
		t.Fatalf("err = %v, want ErrTemplateNotFound", err)
	}
}

func TestTemplateUpdateNameTakenKeepsOriginal(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	if _, err := svc.Create(ctx, TemplateInput{Name: "keep", Spec: sampleSpec()}); err != nil {
		t.Fatalf("Create keep: %v", err)
	}
	target, err := svc.Create(ctx, TemplateInput{Name: "mine", Spec: sampleSpec()})
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	if _, err := svc.Update(ctx, target.ID, TemplateInput{Name: "keep", Spec: sampleSpec()}); err != ErrTemplateNameTaken {
		t.Fatalf("撞名 err = %v, want ErrTemplateNameTaken", err)
	}
	// 撞名那次不能把原模板改成半截(名称仍是 mine,内容仍可应用)。
	got, err := svc.Get(ctx, target.ID)
	if err != nil || got.Name != "mine" {
		t.Fatalf("撞名后原模板被改坏: %+v, err=%v", got, err)
	}
}

func TestTemplateUpdateSelfNameIsAllowed(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	created, err := svc.Create(ctx, TemplateInput{Name: "same", Spec: sampleSpec()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 只改说明、名字原样回填 —— 详情弹窗里最常见的保存动作,不该被自己的唯一约束拦下。
	upd, err := svc.Update(ctx, created.ID, TemplateInput{Name: "same", Description: "只改说明", Spec: sampleSpec()})
	if err != nil {
		t.Fatalf("用原名更新自己: %v", err)
	}
	if upd.Description != "只改说明" {
		t.Fatalf("说明没存进去: %+v", upd)
	}
}

func TestTemplateUpdateRejectsInvalidSpec(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	created, err := svc.Create(ctx, TemplateInput{Name: "ok", Spec: sampleSpec()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = svc.Update(ctx, created.ID, TemplateInput{
		Name: "ok",
		Spec: pipeline.Spec{Stages: []pipeline.Stage{{Name: "构建", Kind: pipeline.KindBuild}}},
	})
	if !isWrapped(err, ErrInvalidTemplate) {
		t.Fatalf("err = %v, want ErrInvalidTemplate", err)
	}
	// 非法的那次 PUT 不留痕。
	got, err := svc.Get(ctx, created.ID)
	if err != nil || len(got.Spec.Stages) != 2 {
		t.Fatalf("非法更新改动了原模板: %+v, err=%v", got, err)
	}
}

func TestTemplateGetDeleteNotFound(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	if _, err := svc.Get(ctx, "missing"); err != ErrTemplateNotFound {
		t.Fatalf("Get err = %v, want ErrTemplateNotFound", err)
	}
	if err := svc.Delete(ctx, "missing"); err != ErrTemplateNotFound {
		t.Fatalf("Delete err = %v, want ErrTemplateNotFound", err)
	}
}

func TestTemplateListAndDelete(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	a, _ := svc.Create(ctx, TemplateInput{Name: "a", Spec: sampleSpec()})
	svc.Create(ctx, TemplateInput{Name: "b", Spec: sampleSpec()})
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v (n=%d), err=%v", list, len(list), err)
	}
	// 升序按名称。
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("List order = %q,%q want a,b", list[0].Name, list[1].Name)
	}
	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if list, _ := svc.List(ctx); len(list) != 1 {
		t.Fatalf("after delete len = %d, want 1", len(list))
	}
}

func TestTemplateApplyProjectNotFound(t *testing.T) {
	db := testDB(t)
	svc := NewTemplateService(db, pipeline.New(db))
	ctx := context.Background()
	tpl, _ := svc.Create(ctx, TemplateInput{Name: "t", Spec: sampleSpec()})
	if _, err := svc.Apply(ctx, tpl.ID, "no-such-project"); err != ErrProjectNotFound {
		t.Fatalf("Apply err = %v, want ErrProjectNotFound", err)
	}
}

// ---- 变量组 ----

func TestVarGroupNormalizationAndSecretRefs(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	cred, err := v.Create(vault.CreateInput{Name: "tok", Type: vault.TypeGitToken, Secret: "super-secret-plaintext"})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	svc := NewVarGroupService(db, v)
	ctx := context.Background()

	g, err := svc.Create(ctx, VarGroupInput{
		Name: "shared",
		Vars: []pipeline.BuildVar{
			{Key: "API_URL", Value: "https://api"},
			{Key: "TOKEN", Secret: true, CredentialID: cred.ID, Value: "leak-me"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 非 secret 保留明文;secret 只留引用 + 掩码,绝无明文。
	var apiVar, tokVar *pipeline.BuildVar
	for i := range g.Vars {
		switch g.Vars[i].Key {
		case "API_URL":
			apiVar = &g.Vars[i]
		case "TOKEN":
			tokVar = &g.Vars[i]
		}
	}
	if apiVar == nil || apiVar.Value != "https://api" {
		t.Fatalf("API_URL not preserved: %+v", apiVar)
	}
	if tokVar == nil {
		t.Fatalf("TOKEN missing")
	}
	if tokVar.Value != "" {
		t.Fatalf("secret var leaked plaintext value %q", tokVar.Value)
	}
	if tokVar.CredentialID != cred.ID {
		t.Fatalf("secret var credentialId = %q, want %q", tokVar.CredentialID, cred.ID)
	}
	if tokVar.MaskedValue == "" {
		t.Fatalf("secret var should carry masked value")
	}

	// 落库的 vars_json 绝不含明文 secret。
	var varsJSON string
	if err := db.QueryRowContext(ctx, `SELECT vars_json FROM variable_groups WHERE id = ?`, g.ID).Scan(&varsJSON); err != nil {
		t.Fatalf("read vars_json: %v", err)
	}
	if strings.Contains(varsJSON, "super-secret-plaintext") || strings.Contains(varsJSON, "leak-me") {
		t.Fatalf("plaintext secret leaked into DB: %s", varsJSON)
	}
}

func TestVarGroupRejectsDuplicateKeys(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	_, err := svc.Create(context.Background(), VarGroupInput{
		Name: "dupkeys",
		Vars: []pipeline.BuildVar{
			{Key: "K", Value: "1"},
			{Key: "K", Value: "2"},
		},
	})
	if !isWrapped(err, ErrInvalidVar) {
		t.Fatalf("err = %v, want ErrInvalidVar", err)
	}
}

func TestVarGroupRejectsEmptyName(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	_, err := svc.Create(context.Background(), VarGroupInput{Name: " "})
	if !isWrapped(err, ErrInvalidVarGroup) {
		t.Fatalf("err = %v, want ErrInvalidVarGroup", err)
	}
}

func TestVarGroupSecretRefMissingCredential(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	_, err := svc.Create(context.Background(), VarGroupInput{
		Name: "g",
		Vars: []pipeline.BuildVar{{Key: "T", Secret: true, CredentialID: "does-not-exist"}},
	})
	if !isWrapped(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestVarGroupVaultUnconfigured(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, nil)) // 未配置 master key
	_, err := svc.Create(context.Background(), VarGroupInput{
		Name: "g",
		Vars: []pipeline.BuildVar{{Key: "T", Secret: true, CredentialID: "x"}},
	})
	if !isWrapped(err, ErrVaultUnconfigured) {
		t.Fatalf("err = %v, want ErrVaultUnconfigured", err)
	}
}

func TestVarGroupUpdateAndDelete(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	ctx := context.Background()
	g, err := svc.Create(ctx, VarGroupInput{Name: "g", Vars: []pipeline.BuildVar{{Key: "A", Value: "1"}}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	upd, err := svc.Update(ctx, g.ID, VarGroupInput{Name: "g2", Vars: []pipeline.BuildVar{{Key: "B", Value: "2"}}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Name != "g2" || len(upd.Vars) != 1 || upd.Vars[0].Key != "B" {
		t.Fatalf("unexpected updated group: %+v", upd)
	}
	if err := svc.Delete(ctx, g.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, g.ID); err != ErrVarGroupNotFound {
		t.Fatalf("Get after delete err = %v, want ErrVarGroupNotFound", err)
	}
}

func TestVarGroupUpdateNotFound(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	_, err := svc.Update(context.Background(), "missing", VarGroupInput{Name: "x"})
	if err != ErrVarGroupNotFound {
		t.Fatalf("err = %v, want ErrVarGroupNotFound", err)
	}
}

func TestVarGroupDuplicateName(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	ctx := context.Background()
	if _, err := svc.Create(ctx, VarGroupInput{Name: "dup"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(ctx, VarGroupInput{Name: "dup"}); err != ErrVarGroupNameTaken {
		t.Fatalf("err = %v, want ErrVarGroupNameTaken", err)
	}
}

// seedVGroup 插入一个最小资源分组(variable_group_groups 无外键,校验只认这一行)。
func seedVGroup(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES (?, ?, '', 'private', 'u', 'u', ?, ?)`, id, name, now, now); err != nil {
		t.Fatalf("seed group %s: %v", name, err)
	}
	return id
}

// TestVarGroupGroupMembership 验变量组的多组归属(0088):建组带上、规范化去重排序、
// 列表按可见分组收敛、PUT 省略 groupIds 不动归属、显式空数组才移出。
func TestVarGroupGroupMembership(t *testing.T) {
	db := testDB(t)
	svc := NewVarGroupService(db, vault.New(db, testMasterKey()))
	ctx := context.Background()
	g1 := seedVGroup(t, db, "组一")
	g2 := seedVGroup(t, db, "组二")

	// 多组 + 空白 + 重复:入库应规范化成升序去重后的集合。
	vg, err := svc.Create(ctx, VarGroupInput{Name: "multi", GroupIDs: &[]string{g2, "", " " + g1 + " ", g1}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []string{g1, g2}
	slices.Sort(want)
	if !slices.Equal(vg.GroupIDs, want) {
		t.Fatalf("GroupIDs = %v, want %v", vg.GroupIDs, want)
	}
	if _, err := svc.Create(ctx, VarGroupInput{Name: "only-two", GroupIDs: &[]string{g2}}); err != nil {
		t.Fatalf("Create only-two: %v", err)
	}
	if _, err := svc.Create(ctx, VarGroupInput{Name: "free"}); err != nil {
		t.Fatalf("Create free: %v", err)
	}

	// 可见范围 = {组一, 未归组}:多组那份(挂了组一)与未归组那份可见,只挂组二那份不可见。
	got, err := svc.List(ctx, access.ListFilter{GroupIDs: []string{g1, access.Ungrouped}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, g := range got {
		names = append(names, g.Name)
	}
	if !slices.Equal(names, []string{"free", "multi"}) {
		t.Fatalf("List names = %v, want [free multi]", names)
	}

	// 不带 groupIds 的 PUT 不动归属(调用方可能只会改名称/变量)。
	renamed, err := svc.Update(ctx, vg.ID, VarGroupInput{Name: "multi2"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !slices.Equal(renamed.GroupIDs, want) {
		t.Fatalf("Update without groupIds changed ownership: %v", renamed.GroupIDs)
	}

	// 显式空数组 = 移出所有分组。
	cleared, err := svc.Update(ctx, vg.ID, VarGroupInput{Name: "multi2", GroupIDs: &[]string{}})
	if err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	if len(cleared.GroupIDs) != 0 {
		t.Fatalf("GroupIDs after clear = %v, want empty", cleared.GroupIDs)
	}

	// 悬挂引用在建/改两处都要挡住(成员表没有外键)。
	if _, err := svc.Create(ctx, VarGroupInput{Name: "ghost", GroupIDs: &[]string{"no-such-group"}}); err != ErrGroupNotFound {
		t.Fatalf("Create with ghost group err = %v, want ErrGroupNotFound", err)
	}
	if _, err := svc.Update(ctx, vg.ID, VarGroupInput{Name: "multi2", GroupIDs: &[]string{"no-such-group"}}); err != ErrGroupNotFound {
		t.Fatalf("Update with ghost group err = %v, want ErrGroupNotFound", err)
	}
	// 失败的回滚不该动原有归属。
	back, err := svc.Get(ctx, vg.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(back.GroupIDs) != 0 {
		t.Fatalf("failed update mutated ownership: %v", back.GroupIDs)
	}

	// 删组连同归属边一起清掉。
	if err := svc.Delete(ctx, vg.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM variable_group_groups WHERE variable_group_id = ?`, vg.ID).Scan(&n); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if n != 0 {
		t.Fatalf("delete left %d ownership edges", n)
	}
}

// isWrapped 判定 err 链上是否含 target(errors.Is 包装链)。
func isWrapped(err, target error) bool {
	return errors.Is(err, target)
}
