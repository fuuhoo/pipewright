package access

import (
	"slices"
	"strings"
	"testing"
)

// perms_test.go —— 功能点这张表的自校验,以及「点集派生出的档位 == 引入点表之前的档位」。
//
// 后者是这次改造的安全带:菜单可以收紧(那是产品决定),但请求上限一格都不许动 ——
// 否则改一次入口名单就顺带把某个角色的 API 能力削掉,而这种事在 UI 上完全看不出来。

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

// TestRolePermsReferenceKnownPoints 保证角色名单里每个 ID 都在字典内:拼错的点永远不会被
// 前端拿到(PermsFor 会过滤掉),于是那个入口对所有该角色都消失 —— 静默且不报错。
func TestRolePermsReferenceKnownPoints(t *testing.T) {
	for _, role := range Roles() {
		for _, id := range rolePerms[role] {
			if _, ok := permByID[id]; !ok {
				t.Fatalf("角色 %s 点了不存在的功能点 %q", role, id)
			}
		}
	}
}

// TestCeilingMatrixUnchanged 冻结档位矩阵:五角色 × 四类资源,含空串与未知角色两条兜底。
func TestCeilingMatrixUnchanged(t *testing.T) {
	want := map[string]map[Kind]Act{
		RoleAdmin:     {KindProject: ActManage, KindRun: ActManage, KindServer: ActManage, KindKubeCluster: ActManage},
		RoleUser:      {KindProject: ActOperate, KindRun: ActOperate, KindServer: ActOperate, KindKubeCluster: ActOperate},
		RoleDeveloper: {KindProject: ActOperate, KindRun: ActOperate, KindServer: ActView, KindKubeCluster: ActView},
		RoleOps:       {KindProject: ActView, KindRun: ActOperate, KindServer: ActOperate, KindKubeCluster: ActOperate},
		RoleViewer:    {KindProject: ActView, KindRun: ActView, KindServer: ActView, KindKubeCluster: ActView},
		"":            {KindProject: ActManage, KindRun: ActManage, KindServer: ActManage, KindKubeCluster: ActManage},
		"root":        {KindProject: ActView, KindRun: ActView, KindServer: ActView, KindKubeCluster: ActView},
	}
	for role, row := range want {
		for _, kind := range ResourceKinds() {
			if got := Ceiling(role, kind); got != row[kind] {
				t.Fatalf("Ceiling(%q, %s) = %s, want %s", role, kind, got, row[kind])
			}
		}
	}
}

// TestPermsForShape 验点集回传的形状:管理员/旧会话全给,未知角色一个不给,普通用户不给设置点。
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
	// 普通用户:除设置点外全开(它是「什么都给但不当管理员」那一档)。
	user := PermsFor(RoleUser)
	if slices.Contains(user, PermSettingsAccess) {
		t.Fatalf("普通用户不该拿到设置点")
	}
	if len(user) != len(permDict)-1 {
		t.Fatalf("普通用户点集 = %d, want %d", len(user), len(permDict)-1)
	}
	// 按字典序回,前端做集合比较之外还能直接渲染同一顺序。
	if !slices.Equal(user, permsWithout(PermSettingsAccess)) {
		t.Fatalf("普通用户点集顺序与字典不一致:%v", user)
	}
	// 只读:每个资源点都必须是 .view,且拿不到设置点。
	for _, id := range PermsFor(RoleViewer) {
		if !strings.HasSuffix(id, ".view") {
			t.Fatalf("只读角色拿到了非只读点 %q", id)
		}
	}
	// 点集里手抄错的 ID 不该被回传(PermsFor 按字典过滤)。
	if got := PermsFor(RoleDeveloper); slices.Contains(got, "no.such.perm") {
		t.Fatalf("点集回传未经字典过滤:%v", got)
	}
}

// TestRoleMenuGranularity 钉住这次改造真正的目的:同一个资源类别下的入口能分开。
// KindServer 一条类别上挂着五处入口(主机状态 / 容器 / 证书 / 预览 / 异常检测),旧口径只有
// 「对 server 这一类能看还是能动」,所以这五处只能一起出现或一起消失 —— 想给开发者看容器
// 状态、又不让他把生产容器重启掉,表达不出来。
func TestRoleMenuGranularity(t *testing.T) {
	dev := PermsFor(RoleDeveloper)
	// 看得见落点(排障要看部署起来没有),但一个落点操作都不给。
	for _, id := range []string{"server.view", "container.view", "cert.view", "preview.view", "anomaly.view"} {
		if !slices.Contains(dev, id) {
			t.Fatalf("开发者该能看到 %s", id)
		}
	}
	for _, id := range []string{"server.exec", "container.operate", "preview.recycle", "anomaly.edit", "cluster.operate"} {
		if slices.Contains(dev, id) {
			t.Fatalf("开发者不该拿到落点操作点 %s:%v", id, dev)
		}
	}
	// 运维反过来:落点全能动,编排只读(读得到流水线,改不了)。
	ops := PermsFor(RoleOps)
	if slices.Contains(ops, "project.edit") || slices.Contains(ops, "library.edit") {
		t.Fatalf("运维不该拿到编排编辑点:%v", ops)
	}
	for _, id := range []string{"container.operate", "server.exec", "anomaly.edit"} {
		if !slices.Contains(ops, id) {
			t.Fatalf("运维该拿到 %s", id)
		}
	}
}

// TestSettingsAllowed 验设置点这条线:管理员与旧会话放行,其余一律拒,未知角色也拒。
func TestSettingsAllowed(t *testing.T) {
	for _, role := range []string{RoleAdmin, ""} {
		if !SettingsAllowed(role) {
			t.Fatalf("角色 %q 应能进设置类入口", role)
		}
	}
	for _, role := range []string{RoleUser, RoleDeveloper, RoleOps, RoleViewer, "root"} {
		if SettingsAllowed(role) {
			t.Fatalf("角色 %q 不该能进设置类入口", role)
		}
	}
}
