// batch_test.go — 批量新增 / 批量删除的领域语义。
//
// 守住四件事:
//   - 逐行隔离:一行失败或已存在,同批其它行照常写库,已成功的不被撤销。
//   - (language, version) 冲突(与现有行、与同批前一行)都记 skipped,不是 failed。
//   - 校验失败的原因是人读那句,不带 "buildenv: invalid input" 哨兵前缀。
//   - enabled 只是意图:新行先以 disabled 落库再走三态门,批量入口开不了后门。
package buildenv_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/buildenv"
)

func batchEnv(lang, ver, display, image string) *buildenv.BuildEnv {
	return &buildenv.BuildEnv{
		Language:    lang,
		Version:     ver,
		DisplayName: display,
		SourceType:  buildenv.SourceCustom,
		Image:       image,
	}
}

func rowAt(results []buildenv.BatchRowResult, idx int) buildenv.BatchRowResult {
	for _, r := range results {
		if r.Index == idx {
			return r
		}
	}
	panic("批量结果缺少这一行")
}

// TestBatchCreate_PerRowIsolation 五种行各走各的结局,互不牵连。
func TestBatchCreate_PerRowIsolation(t *testing.T) {
	svc := newService(t)
	existing, err := svc.Create(batchEnv("batchx", "1", "Existing", "reg/batchx:1"))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	good := batchEnv("batchx", "2", "Batchx 2", "reg/batchx:2")
	good.CredentialID = "cred-1"
	good.SortOrder = 7
	collidesWithDB := batchEnv("batchx", "1", "想覆盖现有行", "evil.example.com/batchx:1")
	missingName := batchEnv("batchx", "3", "", "reg/batchx:3")
	// 与同批第 1 行同键:第 1 行此时已真落库,所以这一行也是冲突而非失败。
	collidesWithBatch := batchEnv("batchx", "2", "同批重复", "reg/batchx:2b")

	report := svc.BatchCreate(context.Background(), []*buildenv.BuildEnv{
		good, collidesWithDB, missingName, collidesWithBatch, nil,
	})

	if report.Summary.Total != 5 || report.Summary.Success != 1 ||
		report.Summary.Skipped != 2 || report.Summary.Failed != 2 {
		t.Fatalf("汇总应为 5/1/2/2,实际 %+v", report.Summary)
	}

	first := rowAt(report.Results, 0)
	if first.Action != buildenv.ResultActionCreated || first.ID == "" {
		t.Fatalf("首行应 created 且带回 id: %+v", first)
	}
	second := rowAt(report.Results, 1)
	if second.Action != buildenv.ResultActionSkipped || second.ID != existing.ID {
		t.Fatalf("与现有行冲突要 skipped 并指回现有 id: %+v", second)
	}
	if !strings.Contains(second.Reason, "已存在") {
		t.Fatalf("跳过要说清原因: %q", second.Reason)
	}
	third := rowAt(report.Results, 2)
	if third.Action != buildenv.ResultActionFailed || third.Reason != "display_name 必填" {
		t.Fatalf("校验失败要给人读原因: %+v", third)
	}
	if strings.Contains(third.Reason, "buildenv:") {
		t.Fatalf("原因里不该有哨兵前缀: %q", third.Reason)
	}
	fourth := rowAt(report.Results, 3)
	if fourth.Action != buildenv.ResultActionSkipped {
		t.Fatalf("同批内重复也是 skipped: %+v", fourth)
	}
	fifth := rowAt(report.Results, 4)
	if fifth.Action != buildenv.ResultActionFailed {
		t.Fatalf("空条目要记在行上: %+v", fifth)
	}

	// 首行落库带凭据与排序;冲突的现有行一个字都没改。
	created, err := svc.GetByID(first.ID)
	if err != nil {
		t.Fatalf("get created: %v", err)
	}
	if created.CredentialID != "cred-1" || created.SortOrder != 7 || created.Image != "reg/batchx:2" {
		t.Fatalf("新增行字段不符: %+v", created)
	}
	kept, err := svc.GetByID(existing.ID)
	if err != nil {
		t.Fatalf("get existing: %v", err)
	}
	if kept.DisplayName != "Existing" || kept.Image != "reg/batchx:1" {
		t.Fatalf("跳过模式改动了现有行: %+v", kept)
	}
	if n := countLanguage(t, svc, "batchx"); n != 2 {
		t.Fatalf("batchx 只该有 2 行(失败/跳过的都不建行),实际 %d", n)
	}
}

// TestBatchCreate_EnableIntent 想启用 ≠ 能启用:三态门对批量入口同样生效。
func TestBatchCreate_EnableIntent(t *testing.T) {
	svc := newService(t)
	wants := batchEnv("batchon", "1", "Batch On 1", "reg/batchon:1")
	wants.Enabled = true

	report := svc.BatchCreate(context.Background(), []*buildenv.BuildEnv{wants})
	row := rowAt(report.Results, 0)
	if row.Action != buildenv.ResultActionCreated {
		t.Fatalf("行本身该建成: %+v", row)
	}
	if row.Enabled {
		t.Fatalf("镜像没检查过不该被批量入口启用: %+v", row)
	}
	if !strings.Contains(row.Reason, "未检查") {
		t.Fatalf("要说清为什么没启用: %q", row.Reason)
	}
	got, err := svc.GetByID(row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled || got.ImageCheckStatus != buildenv.StatusUnchecked {
		t.Fatalf("库里该是 disabled + unchecked: %+v", got)
	}

	// 紧急逃生开关放行时(与单行启用同一扇门),批量新增才真的落启用态。
	t.Setenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE", "true")
	alsoWants := batchEnv("batchon", "2", "Batch On 2", "reg/batchon:2")
	alsoWants.Enabled = true
	second := svc.BatchCreate(context.Background(), []*buildenv.BuildEnv{alsoWants})
	srow := rowAt(second.Results, 0)
	if !srow.Enabled || srow.Reason != "已启用" {
		t.Fatalf("逃生开关下该启用并写明: %+v", srow)
	}

	// 没表达意图的行不碰门,原因留空。
	plain := svc.BatchCreate(context.Background(), []*buildenv.BuildEnv{batchEnv("batchon", "3", "B3", "reg/batchon:3")})
	prow := rowAt(plain.Results, 0)
	if prow.Enabled || prow.Reason != "" {
		t.Fatalf("不想启用的行不该有启用相关说明: %+v", prow)
	}
}

// TestBatchDelete_MixedRows 不存在的行记 skipped(列表可能已被别的会话动过),其余照常删。
func TestBatchDelete_MixedRows(t *testing.T) {
	svc := newService(t)
	a, err := svc.Create(batchEnv("batchdel", "1", "Del 1", "reg/batchdel:1"))
	if err != nil {
		t.Fatalf("seed a: %v", err)
	}
	b, err := svc.Create(batchEnv("batchdel", "2", "Del 2", "reg/batchdel:2"))
	if err != nil {
		t.Fatalf("seed b: %v", err)
	}

	report := svc.BatchDelete([]string{a.ID, "id-gone-by-another-session", b.ID})
	if report.Summary.Total != 3 || report.Summary.Success != 2 || report.Summary.Skipped != 1 {
		t.Fatalf("汇总应为 3/2/1,实际 %+v", report.Summary)
	}
	if r := rowAt(report.Results, 0); r.Action != buildenv.ResultActionDeleted ||
		r.Language != "batchdel" || r.Version != "1" {
		t.Fatalf("删除结果要带回是哪个环境: %+v", r)
	}
	if r := rowAt(report.Results, 1); r.Action != buildenv.ResultActionSkipped ||
		!strings.Contains(r.Reason, "不存在") {
		t.Fatalf("已不存在的行该 skipped 并说明: %+v", r)
	}
	if _, err := svc.GetByID(a.ID); err != buildenv.ErrNotFound {
		t.Fatalf("a 行应已删除: %v", err)
	}
	if _, err := svc.GetByID(b.ID); err != buildenv.ErrNotFound {
		t.Fatalf("b 行应已删除: %v", err)
	}
	if n := countLanguage(t, svc, "batchdel"); n != 0 {
		t.Fatalf("batchdel 该清空,剩 %d 行", n)
	}
}

// countLanguage 数某语言的行数(含禁用),测试用独立语言名避开 seed 干扰。
func countLanguage(t *testing.T, svc *buildenv.Service, language string) int {
	t.Helper()
	list, err := svc.List(buildenv.ListFilter{Language: language, IncludeDisabled: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(list)
}
