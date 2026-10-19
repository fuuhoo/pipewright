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
	nacosOK := map[string]any{
		"envSource":               EnvSourceNacos,
		ConfigKeyNacosBaseURL:     "http://127.0.0.1:8848/nacos",
		ConfigKeyNacosDataID:      "prod.env",
		ConfigKeyNacosGroup:       "DEFAULT_GROUP",
		ConfigKeyNacosNamespaceID: "dev",
	}

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
		{name: "Nacos 坐标齐全", jobType: script, cfg: nacosOK},
		{name: "Nacos 分组留空也能存(归一成默认分组)", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosGroup: ""})},
		{name: "Nacos 命名空间留空也能存(public)", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosNamespaceID: ""})},
		{name: "Nacos 凭据留空也能存(未开鉴权)", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosCredentialID: ""})},
		{name: "Nacos 没填地址", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosBaseURL: ""}), wantErr: true},
		{name: "Nacos 地址是元数据段", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosBaseURL: "http://169.254.169.254/nacos"}), wantErr: true},
		{name: "Nacos 地址把密码写进 URL", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosBaseURL: "http://nacos:pw@127.0.0.1:8848"}), wantErr: true},
		{name: "Nacos 地址带了查询串", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosBaseURL: "http://127.0.0.1:8848/nacos?a=1"}), wantErr: true},
		{name: "Nacos 地址不是 http", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosBaseURL: "grpc://127.0.0.1:8848"}), wantErr: true},
		{name: "Nacos 没填 Data ID", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosDataID: ""}), wantErr: true},
		{name: "Nacos Data ID 带空白", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosDataID: "prod env"}), wantErr: true},
		{name: "Nacos 分组超长", jobType: script, cfg: mergeCfg(nacosOK, map[string]any{ConfigKeyNacosGroup: strings.Repeat("G", nacosFieldMaxLen+1)}), wantErr: true},
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

// 归一化只在这里做一次:默认分组若客户端也写一份,表单存空值时两边会读到不同的配置。
func TestJobEnvRefNacosNormalization(t *testing.T) {
	got := JobEnvRef(map[string]any{
		"envSource":                EnvSourceNacos,
		ConfigKeyNacosBaseURL:      "  http://10.0.0.9:8848/nacos  ",
		ConfigKeyNacosDataID:       " prod-api.env ",
		ConfigKeyNacosGroup:        "  ",
		ConfigKeyNacosNamespaceID:  " dev ",
		ConfigKeyNacosCredentialID: " c7 ",
	})
	if got.Nacos.Group != NacosDefaultGroup {
		t.Errorf("分组空值归一 = %q,要 %q", got.Nacos.Group, NacosDefaultGroup)
	}
	for k, v := range map[string]string{"地址": got.Nacos.BaseURL, "Data ID": got.Nacos.DataID, "命名空间": got.Nacos.NamespaceID, "凭据": got.Nacos.CredentialID} {
		if v != strings.TrimSpace(v) || strings.Contains(v, "  ") {
			t.Errorf("%s 没被 trim:%q", k, v)
		}
	}
	if got.Nacos.BaseURL != "http://10.0.0.9:8848/nacos" {
		t.Errorf("地址 = %q", got.Nacos.BaseURL)
	}
	// 非 Nacos 来源也照填坐标:调用方少读一次 cfg,不该出现「只有某来源才填」的分支。
	if none := JobEnvRef(map[string]any{"envSource": EnvSourceRepo, ConfigKeyNacosGroup: ""}); none.Nacos.Group != NacosDefaultGroup {
		t.Errorf("repo 来源的分组默认值也该归一,得 %q", none.Nacos.Group)
	}
	if JobEnvRef(nil).Source != EnvSourceNone {
		t.Error("nil config 要当「不导入」")
	}
}

func TestNacosBaseURLIssue(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:8848/nacos",     // 本机自建:回环必须放行,否则本地自测这条路死掉
		"http://10.0.0.9:8848",            // 私网:平台本就跑在内网
		"https://nacos.example.com/nacos", // 公网 + TLS
		"https://192.168.1.20:8848/base/nacos",
	}
	for _, u := range ok {
		if issue := NacosBaseURLIssue(u); issue != "" {
			t.Errorf("%q 该放行,得:%s", u, issue)
		}
	}
	bad := map[string]string{
		"":                         "空",
		"   ":                      "空白也算空",
		"nacos:8848":               "没协议",
		"ftp://127.0.0.1":          "非 http",
		"http://169.254.169.254":   "云元数据",
		"http://0.0.0.0:8848":      "未指定地址",
		"http://nacos:pw@10.0.0.9": "URL 里带凭据",
		"http://10.0.0.9?a=1":      "带查询串",
		"http://10.0.0.9#frag":     "带锚点",
		"http://10.0.0. 9":         "含空白",
		"http://10.0.0.9/" + strings.Repeat("p", nacosBaseURLMaxLen): "超长",
	}
	for u, why := range bad {
		if issue := NacosBaseURLIssue(u); issue == "" {
			t.Errorf("%q 该拒(%s),却放行了", u, why)
		}
	}
}

// 配置中心里 YAML/JSON 是常态:读回来一条 KEY=VALUE 都没有就是「构建成功但变量全空」的假绿,必须判出来。
func TestEnvBodyReadIssue(t *testing.T) {
	ok := []string{"A=1\n", "export A=1\n", "BAD LINE\nGOOD=1\n"}
	for _, body := range ok {
		if issue := EnvBodyReadIssue(body); issue != "" {
			t.Errorf("%q 能用却报:%s", body, issue)
		}
	}
	bad := []string{"", "  \n\t\n", "# 全是注释,一条变量也没有\n", "server:\n  port: 8080\n", `{"A":"1"}`}
	for _, body := range bad {
		if issue := EnvBodyReadIssue(body); issue == "" {
			t.Errorf("%q 该判成用不了", body)
		}
	}
	// YAML 那条话术要指路:改成 properties 或用粘贴来源。
	if issue := EnvBodyReadIssue("a:\n  b: 1\n"); !strings.Contains(issue, "properties") {
		t.Errorf("话术要给出路:%s", issue)
	}
}
