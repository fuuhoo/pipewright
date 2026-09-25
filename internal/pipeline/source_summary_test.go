package pipeline

import (
	"context"
	"testing"
)

// 源卡片摘要必须说真话:项目没绑仓库时不能编一个分支名糊人(纯发布项目的源阶段
// 压根没有源可拉)。旧数据里的占位摘要形如「main · push/tag/PR」,读时一并改对;
// 用户在「摘要描述」里手写的文字不动。

func TestGet_SourceSummaryTracksRepoBinding(t *testing.T) {
	cases := []struct {
		name    string
		repoURL string // 项目绑定的仓库,'' = 未绑定
		branch  string
		cfgRepo string // 任务自己覆写的仓库(可从别处拉源码)
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
			name: "项目未绑定但任务自带仓库", repoURL: "", branch: "main",
			cfgRepo: "https://gitee.com/other/lib.git", stored: "main · push/tag/PR",
			want: "https://gitee.com/other/lib.git · main",
		},
		{
			name: "用户手写的摘要不动", repoURL: "", branch: "main",
			stored: "自家镜像,源码在外部准备好", want: "自家镜像,源码在外部准备好",
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
