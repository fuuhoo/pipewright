package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 源任务卡片的默认名不能替用户宣称某家 git 平台:平台没有判过 host,仓库可能在自建
// GitLab / GitHub / Gitea 上,卡片却一路写「Gitee 源」。规则是:
//   - 新种的、补回来的源卡片一律用中性名;
//   - 存量里恰好等于旧品牌字面量的名字在读/存两条路径收敛成中性名;
//   - 精确匹配才改 —— 用户自己取的名字(含手动起的相似名)不动。

// 存量自愈:库里躺着「Gitee 源」的 spec,Get 就该给中性名,用户不必先保存一次。
func TestGet_HealsLegacyBrandSourceJobName(t *testing.T) {
	svc, db, projID := newSvc(t)

	legacy := sourceStageWith(Job{ID: "job_src", Name: "Gitee 源", Type: "git_source", Config: map[string]any{}})
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := db.Exec(`UPDATE pipeline_configs SET spec_json = ? WHERE project_id = ?`, string(raw), projID); err != nil {
		t.Fatalf("seed legacy spec: %v", err)
	}

	cfg, err := svc.Get(context.Background(), projID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := sourceJobOf(t, cfg.Spec).Name; got != sourceJobDefaultName {
		t.Fatalf("Get 后源任务名 = %q, want %q", got, sourceJobDefaultName)
	}
}

// Save 也收敛,并且落库的 spec_json 里不再留品牌名(否则导出/YAML 还是旧文案)。
func TestSave_HealsLegacyBrandSourceJobName(t *testing.T) {
	svc, db, projID := newSvc(t)

	if _, err := svc.Save(context.Background(), projID,
		sourceStageWith(Job{ID: "job_src", Name: " Gitee 源 ", Type: "git_source", Config: map[string]any{}})); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT spec_json FROM pipeline_configs WHERE project_id = ?`, projID).Scan(&stored); err != nil {
		t.Fatalf("read stored spec: %v", err)
	}
	if strings.Contains(stored, "Gitee") {
		t.Fatalf("落库 spec 仍带旧品牌名: %s", stored)
	}
	if !strings.Contains(stored, sourceJobDefaultName) {
		t.Fatalf("落库 spec 应含中性默认名 %q: %s", sourceJobDefaultName, stored)
	}
}

// 只改精确命中的旧字面量:用户自己起的名字一律保留。
func TestSave_KeepsUserChosenSourceJobName(t *testing.T) {
	svc, _, projID := newSvc(t)

	for _, name := range []string{"拉取源码", "Gitee 源(主)", "自建 GitLab 源"} {
		if _, err := svc.Save(context.Background(), projID,
			sourceStageWith(Job{ID: "job_src", Name: name, Type: "git_source", Config: map[string]any{}})); err != nil {
			t.Fatalf("Save(%q): %v", name, err)
		}
		if got := sourceJobOf(t, mustGetSpec(t, svc, projID)).Name; got != name {
			t.Fatalf("用户起的名字 %q 被改成了 %q", name, got)
		}
	}
}

// 品牌名出现在非源任务上是用户自己的命名,不该被平台改写。
func TestSave_KeepsLegacyLiteralOnNonSourceJob(t *testing.T) {
	svc, _, projID := newSvc(t)

	spec := sourceStageWith(Job{ID: "job_src", Name: "Gitee 源", Type: "git_source", Config: map[string]any{}})
	spec.Stages[1].Jobs = append(spec.Stages[1].Jobs,
		Job{ID: "job_n", Name: "Gitee 源", Type: "script", Config: map[string]any{"artifactKind": "file", "commands": "echo hi"}})
	if _, err := svc.Save(context.Background(), projID, spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := mustGetSpec(t, svc, projID)
	for _, st := range got.Stages {
		for _, jb := range st.Jobs {
			if jb.ID == "job_n" && jb.Name != "Gitee 源" {
				t.Fatalf("非源任务的名字被动过: %q", jb.Name)
			}
		}
	}
	if name := sourceJobOf(t, got).Name; name != sourceJobDefaultName {
		t.Fatalf("源任务名 = %q, want %q", name, sourceJobDefaultName)
	}
}

// 新建项目的默认种子 + 补回的卡片都用同一个中性名(别让画布上出现两种平台默认名)。
func TestSourceJobNames_AreBrandNeutral(t *testing.T) {
	if strings.Contains(sourceJobDefaultName, "Gitee") || strings.Contains(sourceJobDefaultName, "GitHub") {
		t.Fatalf("默认源任务名 %q 仍带平台品牌", sourceJobDefaultName)
	}
	for _, j := range defaultSpec("x").Stages[0].Jobs {
		if j.Name != sourceJobDefaultName {
			t.Fatalf("默认种子源任务名 = %q, want %q", j.Name, sourceJobDefaultName)
		}
	}

	spec := sourceStageWith()
	if !restoreSourceJob(&spec) {
		t.Fatal("restoreSourceJob 应补回源卡片")
	}
	if got := spec.Stages[0].Jobs[0].Name; got != sourceJobDefaultName {
		t.Fatalf("补回的源任务名 = %q, want %q", got, sourceJobDefaultName)
	}
}
