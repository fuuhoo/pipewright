package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// 「流水线源」那张卡片的可自愈性:源阶段是画布上唯一没有「+ 加任务」入口的列
// (加任务只能挂在已有卡片上),所以 git_source 任务被删掉后用户无法重新加回来。
// 这里的规则是:源阶段恒有一张 git_source 卡片,缺了就在存/读时补回来。

func sourceStageWith(jobs ...Job) Spec {
	return Spec{Stages: []Stage{
		{ID: "st_src", Name: "流水线源", Kind: KindSource, Jobs: jobs},
		{ID: "st_build", Name: "构建", Kind: KindBuild, Needs: []string{"st_src"}, Jobs: []Job{
			{ID: "job_b", Name: "打包", Type: "script", Config: map[string]any{"artifactKind": "file", "commands": "echo hi"}},
		}},
	}}
}

func countSourceJobs(spec Spec) int {
	n := 0
	for _, st := range spec.Stages {
		if st.Kind != KindSource {
			continue
		}
		for _, jb := range st.Jobs {
			if jb.Type == "git_source" {
				n++
			}
		}
	}
	return n
}

// Save 时源阶段缺 git_source → 补回一张,而不是拒绝落库(拒绝只会让用户对着改不动的画布发愣)。
func TestSave_InjectsMissingSourceJob(t *testing.T) {
	svc, _, projID := newSvc(t)

	cfg, err := svc.Save(context.Background(), projID, sourceStageWith())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := countSourceJobs(cfg.Spec); got != 1 {
		t.Fatalf("源阶段 git_source 卡片数 = %d, want 1", got)
	}

	// 再存一次不该越补越多(幂等)。
	again, err := svc.Save(context.Background(), projID, cfg.Spec)
	if err != nil {
		t.Fatalf("Save(第二次): %v", err)
	}
	if got := countSourceJobs(again.Spec); got != 1 {
		t.Fatalf("重复保存后 git_source 卡片数 = %d, want 1", got)
	}
}

// 整条流水线没有源阶段(把源阶段整个删了)→ 明确报「必须有且仅有一个源阶段」,
// 不再是那句和场景无关的「任务名与类型不能为空」。
func TestSave_RequiresExactlyOneSourceStage(t *testing.T) {
	svc, _, projID := newSvc(t)

	noSource := Spec{Stages: []Stage{{ID: "st_build", Name: "构建", Kind: KindBuild, Jobs: []Job{
		{ID: "job_b", Name: "打包", Type: "script", Config: map[string]any{"artifactKind": "file", "commands": "echo hi"}},
	}}}}
	_, err := svc.Save(context.Background(), projID, noSource)
	if !errors.Is(err, ErrSourceStageRequired) {
		t.Fatalf("err = %v, want ErrSourceStageRequired", err)
	}
	if !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("ErrSourceStageRequired 应同时匹配 ErrInvalidStage(兼容既有映射),got %v", err)
	}

	twoSource := Spec{Stages: append([]Stage{
		{ID: "st_src2", Name: "源二", Kind: KindSource, Jobs: []Job{{ID: "job_s2", Name: "源2", Type: "git_source"}}},
	}, sourceStageWith(Job{ID: "job_s1", Name: "源1", Type: "git_source"}).Stages...)}
	if _, err := svc.Save(context.Background(), projID, twoSource); !errors.Is(err, ErrSourceStageRequired) {
		t.Fatalf("两个源阶段 err = %v, want ErrSourceStageRequired", err)
	}
}

// 任务依赖了一个已被删除的任务:报清「依赖悬空」,别再回显「任务名与类型不能为空」。
func TestSave_DanglingJobDepIsItsOwnError(t *testing.T) {
	svc, _, projID := newSvc(t)

	spec := sourceStageWith(Job{ID: "job_s", Name: "源", Type: "git_source"})
	spec.Stages[1].Jobs = append(spec.Stages[1].Jobs, Job{
		ID: "job_ghost", Name: "幽灵下游", Type: "script",
		Needs:  []string{"job_deleted"},
		Config: map[string]any{"artifactKind": "file", "commands": "echo hi"},
	})
	_, err := svc.Save(context.Background(), projID, spec)
	if !errors.Is(err, ErrJobDepUnknown) {
		t.Fatalf("err = %v, want ErrJobDepUnknown", err)
	}
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("ErrJobDepUnknown 应同时匹配 ErrInvalidJob,got %v", err)
	}
}

// 存量自愈:库里已经躺着「源阶段没有 git_source」的 spec(删任务时保存下来的),
// Get 读出来就该是补好的,前端才能立刻给出可操作的画布。
func TestGet_HealsStoredSpecWithoutSourceJob(t *testing.T) {
	svc, db, projID := newSvc(t)

	broken := sourceStageWith()
	raw, err := json.Marshal(broken)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := db.Exec(`UPDATE pipeline_configs SET spec_json = ? WHERE project_id = ?`, string(raw), projID); err != nil {
		t.Fatalf("seed broken spec: %v", err)
	}

	cfg, err := svc.Get(context.Background(), projID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := countSourceJobs(cfg.Spec); got != 1 {
		t.Fatalf("Get 后 git_source 卡片数 = %d, want 1", got)
	}
}

// restoreSourceJob 补的卡片 id 固定用 job_src(与默认种子同名),撞车时才退化成 uuid。
func TestRestoreSourceJob_IDChoice(t *testing.T) {
	spec := sourceStageWith()
	if !restoreSourceJob(&spec) {
		t.Fatal("restoreSourceJob 应报告改动了 spec")
	}
	if id := spec.Stages[0].Jobs[0].ID; id != "job_src" {
		t.Fatalf("补回的 job id = %q, want job_src", id)
	}
	if restoreSourceJob(&spec) {
		t.Fatal("已存在源卡片时不应再改动 spec")
	}

	taken := sourceStageWith()
	taken.Stages[0].Jobs = []Job{{ID: "job_src", Name: "别的任务", Type: "script", Config: map[string]any{}}}
	if !restoreSourceJob(&taken) {
		t.Fatal("script 不算源卡片,应补回")
	}
	src := taken.Stages[0].Jobs[0]
	if src.Type != "git_source" || src.ID == "" || src.ID == "job_src" {
		t.Fatalf("被占用的 id 应退化成新 uuid, got %+v", src)
	}
}
