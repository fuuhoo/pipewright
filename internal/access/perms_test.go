package access

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// perms_test.go —— 功能点这张表的自校验,以及「点集派生出的档位 == 引入点表之前的档位」。
//
// 后者是这次改造的安全带:菜单可以收紧(那是产品决定),但请求上限一格都不许动 ——
// 否则改一次入口名单就顺带把某个角色的 API 能力削掉,而这种事在 UI 上完全看不出来。
// 四档预置的点集自 0063 起存在库里,同一条安全带改在 roles_drift_test.go 里读迁移文件派生。

// TestPermDictSane 验字典本身没有拼不出、重复或越界的点。
func TestPermDictSane(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range permDict {
		if seen[p.ID] {
			t.Fatalf("功能点 %q 重复声明", p.ID)
		}
		seen[p.ID] = true
		if strings.ContainsAny(p.ID, "/") {
			t.Fatalf("功能点 %q 带了路由路径:点要跟能力走,不跟页面走", p.ID)
		}
		isPlatform := p.Kind == Platform
		if !isPlatform && !slices.Contains(ResourceKinds(), p.Kind) {
			t.Fatalf("功能点 %q 的类别 %q 既不是 Platform 也不在四类资源里", p.ID, p.Kind)
		}
		// 资源点只许封 view / operate;manage 归数据轴(见 perms.go 包注释)。
		if !isPlatform && p.Act != ActView && p.Act != ActOperate {
			t.Fatalf("功能点 %q 的档位 = %s,manage 不该由功能轴给", p.ID, p.Act)
		}
		// 命名口径:view 点以 .view 结尾,operate 点不许伪装成 view(前端按点后缀读不懂,但人会)。
		if !strings.HasSuffix(p.ID, ".view") && p.Act == ActView && !isPlatform {
			t.Fatalf("功能点 %q 声明 View 档却不像只读点,命名要与档位对得上", p.ID)
		}
	}
}

// TestRolePermsReferenceKnownPoints 保证内置档名单里每个 ID 都在字典内:拼错的点永远不会被
// 前端拿到(PermsFor 会过滤掉),于是那个入口对所有该角色都消失 —— 静默且不报错。
// 预置档的点集现在在 0063 里,同样的检查由 internal/store 的迁移测试与本包的
// TestSeededPresetCeilings 承担。
func TestRolePermsReferenceKnownPoints(t *testing.T) {
	for _, role := range Roles() {
		for _, id := range rolePerms[role] {
			if _, ok := permByID[id]; !ok {
				t.Fatalf("角色 %s 点了不存在的功能点 %q", role, id)
			}
		}
	}
}

// TestCeilingMatrixUnchanged 冻结代码侧那几行的档位:内置 admin、旧会话空角色、未知角色。
//
// 四档预置自 0063 起是库里的普通角色,它们的矩阵由 TestSeededPresetCeilings 直接从迁移文件
// 派生校验;这里再留一份手抄名单的话,就有两处权威可以互相漂移了。
func TestCeilingMatrixUnchanged(t *testing.T) {
	want := map[string]map[Kind]Act{
		RoleAdmin: {KindProject: ActManage, KindRun: ActManage, KindServer: ActManage, KindKubeCluster: ActManage},
		// 空串是 0053 迁移前旧会话的取值,语义「按管理员」:否则升级当天旧会话被静默降成只读。
		"":           {KindProject: ActManage, KindRun: ActManage, KindServer: ActManage, KindKubeCluster: ActManage},
		"root":       {KindProject: ActView, KindRun: ActView, KindServer: ActView, KindKubeCluster: ActView},
		"no-such-id": {KindProject: ActView, KindRun: ActView, KindServer: ActView, KindKubeCluster: ActView},
	}
	for role, row := range want {
		for _, kind := range ResourceKinds() {
			if got := Ceiling(role, kind); got != row[kind] {
				t.Fatalf("Ceiling(%q, %s) = %s, want %s", role, kind, got, row[kind])
			}
		}
	}
}

// TestPermsForShape 验点集回传的形状:管理员与旧会话全给,未知角色一个不给。
// 顺序按字典声明序 —— 前端做集合比较之外还能直接渲染同一顺序。
func TestPermsForShape(t *testing.T) {
	if got := len(PermsFor(RoleAdmin)); got != len(permDict) {
		t.Fatalf("管理员点集 = %d, want 全字典 %d", got, len(permDict))
	}
	// 空串是 0053 前旧会话的取值,语义「按管理员」,菜单不能少一块。
	if got := strings.Join(PermsFor(""), ","); got != strings.Join(permIDs(), ",") {
		t.Fatalf("空角色的点集不是全字典:%s", got)
	}
	if got := PermsFor("root"); len(got) != 0 {
		t.Fatalf("未知角色不该拿到任何点,got %v", got)
	}
	// 字典外的 ID 不该被回传(自定义角色写路径也可能带进残留引用,读侧按字典过滤)。
	useStore(t, &fakeRoleStore{rows: []CustomRole{{ID: "r-ghost", Perms: []string{"project.view", "no.such.perm"}}}})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got := PermsFor("r-ghost"); len(got) != 1 || got[0] != "project.view" {
		t.Fatalf("点集回传未经字典过滤:%v", got)
	}
}

// TestSettingsAllowed 验设置点这条线:内置管理员与旧会话放行,其余一律拒。
// 预置档那一半拒在哪见 TestSeededPresetGranularity(它们现在得先装进缓存才认得出来)。
func TestSettingsAllowed(t *testing.T) {
	for _, role := range []string{RoleAdmin, ""} {
		if !SettingsAllowed(role) {
			t.Fatalf("角色 %q 应能进设置类入口", role)
		}
	}
	for _, role := range []string{"root", "no-such-id"} {
		if SettingsAllowed(role) {
			t.Fatalf("角色 %q 不该能进设置类入口", role)
		}
	}
}
