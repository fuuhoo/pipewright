package pipeline

import (
	"context"
	"testing"
)

// 源卡片摘要必须说真话:它整行就是「项目仓库 · 项目默认分支」的镜像,项目没绑仓库时
// 不能编一个分支名糊人(纯发布项目的源阶段压根没有源可拉)。节点自己存过的仓库/摘要
// 一律按项目真值改对 —— 这三键不参与克隆,留旧值只会让画布和步骤日志各说一套。

func TestGet_SourceSummaryTracksRepoBinding(t *testing.T) {
	cases := []struct {
		name    string
		repoURL string // 项目绑定的仓库,'' = 未绑定
		branch  string
		cfgRepo string // 节点里残留的仓库(旧数据/API 手填)
		stored  string // 已落库的摘要
		want    string
	}{
		{
			name: "未绑定仓库_不再假称分支", repoURL: "", branch: "main",
			stored: "main · push/tag/PR", want: sourceSummaryUnbound,
		},
		{
			name: "摘要为空_未绑定仓库", repoURL: "", branch: "main",
			stored: "", want: sourceSummaryUnbound,
		},
		{
			name: "绑了仓库_报仓库与分支", repoURL: "https://gitee.com/acme/shop.git", branch: "dev",
			stored: "main · push/tag/PR", want: "https://gitee.com/acme/shop.git · dev",
		},
		{
			name: "摘要为空_报仓库与分支", repoURL: "https://gitee.com/acme/shop.git", branch: "dev",
			stored: "", want: "https://gitee.com/acme/shop.git · dev",
		},
		{
			// 源节点没有「自己的源」:项目没绑仓库时,节点里残留的仓库地址不算数。
			name: "节点残留仓库_仍按项目走", repoURL: "", branch: "main",
			cfgRepo: "https://gitee.com/other/lib.git", stored: "main · push/tag/PR",
			want: sourceSummaryUnbound,
		},
		{
			// 摘要对源卡片是推导出来的展示文字,不是给用户写的第二标题。
			name: "残留摘要_按项目真值改对", repoURL: "https://gitee.com/acme/shop.git", branch: "main",
			stored: "自家镜像,源码在外部准备好", want: "https://gitee.com/acme/shop.git · main",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, db, projID := newSvc(t)
			if _, err := db.Exec(
				`UPDATE projects SET repo_url = ?, default_branch = ?, credential_id = NULL WHERE id = ?`,
				tc.repoURL, tc.branch, projID,
			); err != nil {
				t.Fatalf("set project repo: %v", err)
			}

			cfgMap := map[string]any{}
			if tc.cfgRepo != "" {
				cfgMap["repoUrl"] = tc.cfgRepo
			}
			src := Job{ID: "job_src", Name: "Gitee 源", Type: "git_source", Summary: tc.stored, Config: cfgMap}
			if _, err := svc.Save(context.Background(), projID, sourceStageWith(src)); err != nil {
				t.Fatalf("Save: %v", err)
			}

			cfg, err := svc.Get(context.Background(), projID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			got := sourceJobOf(t, cfg.Spec).Summary
			if got != tc.want {
				t.Fatalf("源卡片摘要 = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSave_RebindRepoRefreshesPlaceholderSummary 覆盖解绑→重绑:绑上仓库后占位摘要
// 不能停留在「未绑定仓库」。
func TestSave_RebindRepoRefreshesPlaceholderSummary(t *testing.T) {
	svc, db, projID := newSvc(t)
	if _, err := db.Exec(
		`UPDATE projects SET repo_url = '', default_branch = '', credential_id = NULL WHERE id = ?`, projID,
	); err != nil {
		t.Fatalf("unbind repo: %v", err)
	}
	src := Job{ID: "job_src", Name: "Gitee 源", Type: "git_source", Config: map[string]any{}}
	if _, err := svc.Save(context.Background(), projID, sourceStageWith(src)); err != nil {
		t.Fatalf("Save(未绑定): %v", err)
	}
	unbound := sourceJobOf(t, mustGetSpec(t, svc, projID)).Summary
	if unbound != sourceSummaryUnbound {
		t.Fatalf("未绑定摘要 = %q, want %q", unbound, sourceSummaryUnbound)
	}

	if _, err := db.Exec(
		`UPDATE projects SET repo_url = 'https://gitee.com/acme/shop.git', default_branch = 'main' WHERE id = ?`, projID,
	); err != nil {
		t.Fatalf("rebind repo: %v", err)
	}
	// 源任务已存在,这里只是再存一次同一份 spec(用户在画布上改别的阶段)。
	again := sourceStageWith(sourceJobOf(t, mustGetSpec(t, svc, projID)))
	if _, err := svc.Save(context.Background(), projID, again); err != nil {
		t.Fatalf("Save(重绑): %v", err)
	}
	// Get 会把项目仓库填进 config 并重算占位摘要。
	got := sourceJobOf(t, mustGetSpec(t, svc, projID)).Summary
	if got != "https://gitee.com/acme/shop.git · main" {
		t.Fatalf("重绑后摘要 = %q, want 仓库 · 分支", got)
	}
}

func sourceJobOf(t *testing.T, spec Spec) Job {
	t.Helper()
	for _, st := range spec.Stages {
		if st.Kind != KindSource {
			continue
		}
		for _, jb := range st.Jobs {
			if jb.Type == "git_source" {
				return jb
			}
		}
	}
	t.Fatal("源阶段没有 git_source 任务")
	return Job{}
}

func mustGetSpec(t *testing.T, svc Service, projID string) Spec {
	t.Helper()
	cfg, err := svc.Get(context.Background(), projID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return cfg.Spec
}
