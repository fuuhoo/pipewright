package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// buildenv_runtime_test.go 锁住执行期镜像/挂载解析(#9/#10):镜像只能来自预置目录,
// 配置资源只在语言与路径都合法时注入。

func runtimeSnapshot() BuildEnvSnapshot {
	return NewBuildEnvSnapshot([]BuildEnvOption{
		{ID: "e-node", Language: "node", Version: "20", DisplayName: "Node 20", Image: "node:20", Enabled: true},
		{ID: "e-java", Language: "java", Version: "21-maven", DisplayName: "Java 21 + Maven", Image: "maven:3.9-eclipse-temurin-21", Enabled: true, CredentialID: "cred-registry"},
		{ID: "e-off", Language: "node", Version: "18", DisplayName: "Node 18", Image: "node:18", Enabled: false},
	}, []ConfigProfileOption{
		{ID: "p-npmrc", Language: "node", Enabled: true, FilePath: "/data/config_profiles/p-npmrc/.npmrc", TargetPath: "/root/.npmrc"},
		{ID: "p-off", Language: "node", Enabled: false, FilePath: "/data/x", TargetPath: "/root/x"},
		{ID: "p-outside", Language: "node", Enabled: true, FilePath: "/data/escape", TargetPath: "/root/../etc/app.env"},
		{ID: "p-relative", Language: "node", Enabled: true, FilePath: "/data/rel", TargetPath: "relative/path.conf"},
		{ID: "p-nofile", Language: "node", Enabled: true, TargetPath: "/root/.nope"},
	})
}

func TestResolveJobEnvFromCatalog(t *testing.T) {
	s := runtimeSnapshot()
	cases := []struct {
		name, stage, job    string
		cfg                 map[string]any
		rendered            string
		wantImage, wantCred string
		wantMounts          int
	}{
		{name: "按引用", job: "构建", cfg: map[string]any{ConfigKeyBuildEnvID: "e-java", ConfigKeyConfigProfileIDs: "p-npmrc"}, wantImage: "maven:3.9-eclipse-temurin-21", wantCred: "cred-registry", wantMounts: 1},
		// 旧配置(只有镜像名 / 工具链语言+版本):命中目录即用,镜像取目录里的值。
		{name: "旧镜像名", job: "旧节点", cfg: map[string]any{ConfigKeyImage: "node:20"}, wantImage: "node:20"},
		{name: "旧工具链", job: "镜像构建", cfg: map[string]any{ConfigKeyToolchainLanguage: "java", ConfigKeyToolchainVersion: "21-maven"}, wantImage: "maven:3.9-eclipse-temurin-21", wantCred: "cred-registry"},
		{name: "参数渲染后命中", job: "自定义", cfg: map[string]any{ConfigKeyImage: "node:{{ver}}"}, rendered: "node:20", wantImage: "node:20"},
	}
	for _, c := range cases {
		got, err := s.ResolveJobEnv(c.cfg, c.rendered, orDefault(c.stage, "阶段"), c.job)
		if err != nil {
			t.Fatalf("%s: err = %v", c.name, err)
		}
		if got.Image != c.wantImage {
			t.Errorf("%s: image = %q, want %q", c.name, got.Image, c.wantImage)
		}
		if got.CredentialID != c.wantCred {
			t.Errorf("%s: cred = %q, want %q", c.name, got.CredentialID, c.wantCred)
		}
		if len(got.Mounts) != c.wantMounts {
			t.Errorf("%s: mounts = %v, want %d", c.name, got.Mounts, c.wantMounts)
		}
	}
}

func TestResolveJobEnvRejects(t *testing.T) {
	s := runtimeSnapshot()
	cases := []struct {
		name         string
		cfg          map[string]any
		rendered     string
		wantMsg      string
		wantUnusable bool // 配置资源数据本身有问题(而非用户没选环境)
	}{
		{name: "没选环境", cfg: map[string]any{}, wantMsg: "未选择构建环境"},
		{name: "目录外镜像", cfg: map[string]any{ConfigKeyImage: "nginx:1.27"}, wantMsg: "不在预置目录内"},
		{name: "已禁用环境", cfg: map[string]any{ConfigKeyBuildEnvID: "e-off"}, wantMsg: "已被禁用"},
		{name: "环境已删", cfg: map[string]any{ConfigKeyBuildEnvID: "e-gone"}, wantMsg: "已不存在"},
		{name: "占位未渲染", cfg: map[string]any{ConfigKeyImage: "node:{{ver}}"}, wantMsg: "未解析出实际值"},
		{name: "渲染后仍越界", cfg: map[string]any{ConfigKeyImage: "node:{{ver}}"}, rendered: "evil:latest", wantMsg: "不在预置目录内"},
		{name: "配置资源已删", cfg: map[string]any{ConfigKeyBuildEnvID: "e-node", ConfigKeyConfigProfileIDs: "p-gone"}, wantMsg: "已不存在或被禁用"},
		{name: "配置资源已禁用", cfg: map[string]any{ConfigKeyBuildEnvID: "e-node", ConfigKeyConfigProfileIDs: "p-off"}, wantMsg: "已不存在或被禁用"},
		{name: "挂载路径上跳", cfg: map[string]any{ConfigKeyBuildEnvID: "e-node", ConfigKeyConfigProfileIDs: "p-outside"}, wantMsg: "容器内路径非法", wantUnusable: true},
		{name: "挂载路径非绝对", cfg: map[string]any{ConfigKeyBuildEnvID: "e-node", ConfigKeyConfigProfileIDs: "p-relative"}, wantMsg: "容器内路径非法", wantUnusable: true},
		{name: "配置资源无文件", cfg: map[string]any{ConfigKeyBuildEnvID: "e-node", ConfigKeyConfigProfileIDs: "p-nofile"}, wantMsg: "没有磁盘文件", wantUnusable: true},
	}
	for _, c := range cases {
		_, err := s.ResolveJobEnv(c.cfg, c.rendered, "阶段", "节点")
		if err == nil {
			t.Fatalf("%s: 应被拒绝", c.name)
		}
		want := error(ErrBuildEnvRequired)
		if c.wantUnusable {
			want = ErrConfigProfileUnusable
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: 错误应可被 %v 识别, got %v", c.name, want, err)
		}
		if !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("%s: 报错应含 %q, got %v", c.name, c.wantMsg, err)
		}
	}
}

// 配置资源数据坏了(路径上跳)在保存期就被拒,而不是等构建跑到一半失败。
func TestValidateBuildEnvRefsRejectsBadProfilePath(t *testing.T) {
	snap := runtimeSnapshot()
	spec := Spec{Stages: []Stage{{Name: "构建", Jobs: []Job{{Name: "装依赖", Type: StepTypeScript,
		Config: map[string]any{ConfigKeyBuildEnvID: "e-node", ConfigKeyConfigProfileIDs: "p-outside"}}}}}}
	problems := ValidateBuildEnvRefs(spec, snap)
	if len(problems) != 1 || problems[0].Code != ProblemConfigProfilePath {
		t.Fatalf("应报一条 config_profile_path, got %+v", problems)
	}
}

// 挂载解析:文件只读、容器路径归一化(`a//b` → `/a/b`)。
func TestResolveJobEnvMountShape(t *testing.T) {
	got, err := runtimeSnapshot().ResolveJobEnv(map[string]any{
		ConfigKeyBuildEnvID:       "e-node",
		ConfigKeyConfigProfileIDs: "p-npmrc,p-npmrc", // 重复引用只挂一次(ConfigStringList 去重)
	}, "", "阶段", "节点")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got.Mounts) != 1 {
		t.Fatalf("mounts = %v", got.Mounts)
	}
	m := got.Mounts[0]
	if m.HostPath != "/data/config_profiles/p-npmrc/.npmrc" || m.ContainerPath != "/root/.npmrc" || !m.ReadOnly {
		t.Errorf("mount = %+v", m)
	}
}

// 工具链解析走同一条目录规则:目录里没有的 `语言:版本` 不再被拼成镜像跑。
func TestResolveToolchainEnv(t *testing.T) {
	s := runtimeSnapshot()
	if got, err := s.ResolveToolchainEnv(Toolchain{Language: "node", Version: "20"}, "构建配置", "app"); err != nil || got.Image != "node:20" {
		t.Fatalf("命中目录应成功: %+v %v", got, err)
	}
	if _, err := s.ResolveToolchainEnv(Toolchain{Language: "ruby", Version: "3.3"}, "构建配置", "app"); !errors.Is(err, ErrBuildEnvRequired) {
		t.Fatalf("目录外工具链应拒绝, got %v", err)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
