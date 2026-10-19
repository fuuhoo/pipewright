package pipeline

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEnvBody(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    []BuildVar
		skipped int
	}{
		{
			name: "基本/注释/空行",
			body: "A=1\n# 注释\n\nB=2\n",
			want: []BuildVar{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}},
		},
		{
			name: "export 前缀",
			body: "export A=1\n  export   B=2\n",
			want: []BuildVar{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}},
		},
		{
			name: "值两端成对引号脱掉,单引号同样",
			body: "A=\"1\"\nB='2'\nC=\"3'\n",
			want: []BuildVar{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}, {Key: "C", Value: `"3'`}},
		},
		{
			name: "行内 # 不剥(密码里带 # 很常见)",
			body: "PASS=abc#123\n",
			want: []BuildVar{{Key: "PASS", Value: "abc#123"}},
		},
		{
			name: "值里有 = 只切第一个;空值留空串",
			body: "DSN=user=pwd=1\nEMPTY=\n",
			want: []BuildVar{{Key: "DSN", Value: "user=pwd=1"}, {Key: "EMPTY", Value: ""}},
		},
		{
			name: "CRLF 与 BOM",
			body: "\ufeffA=1\r\nB=2\r\n",
			want: []BuildVar{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}},
		},
		{
			name: "同名后写覆盖,位置保持首次",
			body: "A=1\nB=2\nA=3\n",
			want: []BuildVar{{Key: "A", Value: "3"}, {Key: "B", Value: "2"}},
		},
		{
			name:    "非法行计数但不阻断",
			body:    "NOEQ\n=novalue\n9bad=x\nA B=1\nGOOD=1\n",
			want:    []BuildVar{{Key: "GOOD", Value: "1"}},
			skipped: 4,
		},
		{name: "空正文", body: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseEnvBody(tc.body)
			if len(got.Vars) != len(tc.want) {
				t.Fatalf("vars=%+v, want %d 条 %v", got.Vars, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if got.Vars[i].Key != w.Key || got.Vars[i].Value != w.Value {
					t.Errorf("第 %d 条 = %s=%q, want %s=%q", i, got.Vars[i].Key, got.Vars[i].Value, w.Key, w.Value)
				}
				if got.Vars[i].Secret || got.Vars[i].CredentialID != "" {
					t.Errorf("%s 被标成 secret 引用,导入的正文应当是明文变量", got.Vars[i].Key)
				}
			}
			if got.Skipped != tc.skipped {
				t.Errorf("skipped=%d, want %d", got.Skipped, tc.skipped)
			}
		})
	}
}

func TestRepoEnvPathOK(t *testing.T) {
	ok := []string{".env", ".env.production", "prod.env", "deploy/prod.env", "backend/.env.local"}
	bad := []string{"", "   ", "/etc/env", "../secret.env", "a/../b.env", "a//b.env", `deploy\prod.env`,
		"docker-compose.yml", "env", ".environmentrc", strings.Repeat("a/", composeFileMaxLen) + ".env"}
	for _, p := range ok {
		if !RepoEnvPathOK(p) {
			t.Errorf("%q 应当是合法的仓库 env 路径", p)
		}
	}
	for _, p := range bad {
		if RepoEnvPathOK(p) {
			t.Errorf("%q 应当被拒(绝对路径 / 上跳 / 空段 / 不像 env 文件 / 超长)", p)
		}
	}
}

func TestLegacyJobEnvVars(t *testing.T) {
	got := LegacyJobEnvVars(map[string]any{"env": `{"Z":"z","A":"a","BAD KEY":"x"}`})
	if len(got) != 2 || got[0].Key != "A" || got[1].Key != "Z" {
		t.Fatalf("要按键名升序且滤掉非法键名, got %+v", got)
	}
	for _, cfg := range []map[string]any{nil, {}, {"env": ""}, {"env": "不是 JSON"}, {"env": 42}} {
		if v := LegacyJobEnvVars(cfg); v != nil {
			t.Errorf("缺省/非法形状要当「没配」, cfg=%v got %+v", cfg, v)
		}
	}
}

func TestValidateJobEnv(t *testing.T) {
	script := StepTypeScript
	buildFile := map[string]any{ConfigKeyArtifactType: ArtifactFile}
	buildImage := map[string]any{ConfigKeyArtifactType: ArtifactImage}

	cases := []struct {
		name    string
		jobType string
		cfg     map[string]any
		wantErr bool
	}{
		{name: "不导入", jobType: script, cfg: map[string]any{}},
		{name: "仓库路径合法", jobType: script, cfg: map[string]any{"envSource": EnvSourceRepo, "envFile": ".env.production"}},
		{name: "仓库路径缺失", jobType: script, cfg: map[string]any{"envSource": EnvSourceRepo}, wantErr: true},
		{name: "仓库路径是 yml", jobType: script, cfg: map[string]any{"envSource": EnvSourceRepo, "envFile": "a.yml"}, wantErr: true},
		{name: "粘贴有正文", jobType: script, cfg: map[string]any{"envSource": EnvSourcePaste, "envText": "A=1\n"}},
		{name: "上传有正文", jobType: script, cfg: map[string]any{"envSource": EnvSourceUpload, "envText": "A=1", "envFileName": "prod.env"}},
		{name: "粘贴空正文", jobType: script, cfg: map[string]any{"envSource": EnvSourcePaste, "envText": "  \n "}, wantErr: true},
		{name: "正文超限", jobType: script, cfg: map[string]any{"envSource": EnvSourcePaste, "envText": strings.Repeat("A=", EnvMaxBytes+1)}, wantErr: true},
		{name: "来源不认识", jobType: script, cfg: map[string]any{"envSource": "ftp"}, wantErr: true},
		{name: "构建任务产物档按脚本跑,放行", jobType: JobTypeBuild, cfg: mergeCfg(buildFile, map[string]any{"envSource": EnvSourcePaste, "envText": "A=1"})},
		{name: "构建任务镜像档没有落点", jobType: JobTypeBuild, cfg: mergeCfg(buildImage, map[string]any{"envSource": EnvSourcePaste, "envText": "A=1"}), wantErr: true},
		{name: "部署节点不是脚本类", jobType: "deploy_host", cfg: map[string]any{"envSource": EnvSourcePaste, "envText": "A=1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateJobEnv("构建", "节点", tc.jobType, tc.cfg)
			if tc.wantErr && err == nil {
				t.Fatal("应当报错但放行了")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("不该报错:%v", err)
			}
			if err != nil {
				if !errors.Is(err, ErrJobEnvInvalid) {
					t.Errorf("错误没带哨兵 ErrJobEnvInvalid(HTTP 会掉进 500 分支):%v", err)
				}
				if strings.Contains(err.Error(), "pipeline:") {
					t.Errorf("422 消息里混进了英文内部前缀:%v", err)
				}
			}
		})
	}
}

func mergeCfg(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
