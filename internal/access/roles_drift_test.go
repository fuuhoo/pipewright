package access

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// roles_drift_test.go —— 「播种的预置档」与前端名单、与派生档位之间的漂移守卫。
//
// 为什么会漂:加一档角色(改 0063 的 SQL 或改代码表),前端下拉与标签不会报错,只是那个角色永远
// 选不出来 —— 页面上一眼看不出来,只有管理员试到时才发现。这类静默缺口适合留在测试里挡。
// 断言写在 Go 侧是因为前端 tsconfig 没引 node 类型,单测里读不了源码文件。
//
// 第二条守卫管功能点:前端 PermId 名单必须与本表同集合,且侧栏 / 路由里引用的每个点都得
// 存在。引用一个不存在的点不会报错,只会让那个入口对该角色永远消失(PermsFor 按字典过滤),
// 是这整套设计里最难查的一种「菜单少了」。

var (
	roleOrderRe = regexp.MustCompile(`export const ROLE_ORDER: UserRole\[\] = \[([^\]]*)\]`)
	labelKeysRe = regexp.MustCompile(`export const ROLE_LABEL_KEY: Record<UserRole, string> = \{([\s\S]*?)\n\}`)
	permIdsRe   = regexp.MustCompile(`export type PermId =([\s\S]*?)(?:\n\n|\n/\*)`)
	permRefRe   = regexp.MustCompile(`perm:\s*'([^']+)'`)
)

func tsStrings(list string) []string {
	re := regexp.MustCompile(`'([^']+)'`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(list, -1) {
		out = append(out, m[1])
	}
	return out
}

func readFrontendRoles(t *testing.T) (order []string, labeled []string) {
	t.Helper()
	path := filepath.Join("..", "..", "web", "src", "lib", "roles.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 %s(单独取 Go 模块时属正常):%v", path, err)
	}
	src := string(raw)
	orderMatch := roleOrderRe.FindStringSubmatch(src)
	if orderMatch == nil {
		t.Fatalf("roles.ts 里的 ROLE_ORDER 形状变了,请同步这里的解析")
	}
	labelMatch := labelKeysRe.FindStringSubmatch(src)
	if labelMatch == nil {
		t.Fatalf("roles.ts 里的 ROLE_LABEL_KEY 形状变了,请同步这里的解析")
	}
	keyRe := regexp.MustCompile(`(?m)^\s*(\w+):`)
	for _, m := range keyRe.FindAllStringSubmatch(labelMatch[1], -1) {
		labeled = append(labeled, m[1])
	}
	return tsStrings(orderMatch[1]), labeled
}

// TestFrontendRolesMatchCeilings 盯两件事:下拉里的名单 = 内置档 ∪ 0063 播种进库的预置档,
// 且每个都有展示键(缺键会让页面渲染成裸 i18n key)。
//
// 0063 之后预置档不再是代码表的一部分,所以这里改读迁移文件本身 —— 播种新角色的人
// 只会顺手改 SQL,不会想到前端要加标签;这条断言就是把这两端焊在一起。
func TestFrontendRolesMatchCeilings(t *testing.T) {
	order, labeled := readFrontendRoles(t)

	want := []string{}
	for role := range rolePerms {
		want = append(want, role)
	}
	want = append(want, seededRoleIDs(t)...)

	got := append([]string(nil), order...)
	sort.Strings(want)
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("前端角色名单 %v 与「内置 ∪ 播种」%v 条目数不同", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("前端角色名单与「内置 ∪ 播种」不一致:%v vs %v", got, want)
		}
	}

	labelSet := map[string]bool{}
	for _, role := range labeled {
		labelSet[role] = true
	}
	for _, role := range want {
		if !labelSet[role] {
			t.Fatalf("角色 %s 在 ROLE_LABEL_KEY 里没有展示键,页面会露出裸 key", role)
		}
	}
}

// seededRoleIDs 从 0063 迁移里读出被播种进 roles 表的预置档 id。
// 只认 roles 的那行插入(`VALUES ('<id>', '<name>', ...`,且要求 name 仍等于 id —— 点集行以
// `('id', 'point')` 出现,不以 VALUES 开头,不会被吃进来。
func seededRoleIDs(t *testing.T) []string {
	t.Helper()
	src := readPresetMigration(t)
	re := regexp.MustCompile(`(?m)^  VALUES \('([a-z]+)', '([a-z]+)', `)
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if m[1] != m[2] {
			t.Fatalf("预置档 %q 的 name = %q,本测试只认 name 仍等于 id 的播种(见 0063 取舍 2)", m[1], m[2])
		}
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("0063 里一条 roles 插入都没解析出来,形状变了请同步这里的正则")
	}
	return out
}

// readPresetMigration 读 0063 的 sqlite 那份。这里不 Skip:迁移目录与本包同属一个 Go 模块,
// 读不到只有两种可能 —— 文件被改名(那这条守卫就永远形同虚设)或正则失配,两者都该炸出来。
// 断言只读 sqlite 版即可:mysql 那份由 internal/store 的迁移测试逐字比对内容。
func readPresetMigration(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "store", "migrations", "sqlite", "0063_preset_roles_to_custom.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 0063 迁移(%s):%v", path, err)
	}
	return string(raw)
}

// seededPresetPoints 解析出 0063 给每个预置档播种的功能点集(按角色分组,保留声明序)。
// 点集行形如 `  ('developer', 'server.view'),`,末行以 `;` 收尾;roles 的插入行以 VALUES 开头,不会被吃进来。
func seededPresetPoints(t *testing.T) map[string][]string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^[ \t]*\('([a-z]+)', '([a-z_.]+)'\)`)
	out := map[string][]string{}
	for _, m := range re.FindAllStringSubmatch(readPresetMigration(t), -1) {
		out[m[1]] = append(out[m[1]], m[2])
	}
	if len(out) != len(seededRoleIDs(t)) {
		t.Fatalf("解析到 %d 个角色的点集,roles 插入却有 %d 档,请同步这里的正则",
			len(out), len(seededRoleIDs(t)))
	}
	return out
}

// useSeededPresets 把 0063 播种的四档预置灌进判定缓存,让本包的用例能在与线上装配
// 相同的形状下跑这些角色(内置只剩 admin,其余的点集权威已经在库里)。
func useSeededPresets(t *testing.T) map[string][]string {
	t.Helper()
	points := seededPresetPoints(t)
	rows := make([]CustomRole, 0, len(points))
	for _, id := range seededRoleIDs(t) {
		rows = append(rows, CustomRole{ID: id, Name: id, Perms: points[id]})
	}
	useStore(t, &fakeRoleStore{rows: rows})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("装载预置档失败: %v", err)
	}
	return points
}

// TestSeededPresetCeilings 冻结「库里播种的点集派生出的档位」。
//
// 这四档自 0063 起不再是代码表的一部分,但它们的档位仍然是对外承诺:改一行 SQL 少给一个点,
// 页面不会报错,只是那个角色的入口静静少一块 —— 所以断言必须跟着数据源走,而不是留在旧表上。
// 这里读的是迁移文件本身(播种的权威),而不是第二份手抄名单。
func TestSeededPresetCeilings(t *testing.T) {
	useSeededPresets(t)

	want := map[string]map[Kind]Act{
		// 普通用户:四类资源都能动(与引入档位表之前的行为逐字一致)。
		"user":      {KindProject: ActOperate, KindRun: ActOperate, KindServer: ActOperate, KindKubeCluster: ActOperate},
		"developer": {KindProject: ActOperate, KindRun: ActOperate, KindServer: ActView, KindKubeCluster: ActView},
		"ops":       {KindProject: ActView, KindRun: ActOperate, KindServer: ActOperate, KindKubeCluster: ActOperate},
		"viewer":    {KindProject: ActView, KindRun: ActView, KindServer: ActView, KindKubeCluster: ActView},
	}
	for role, row := range want {
		if !ValidRole(role) {
			t.Fatalf("预置档 %q 没被认下来:该角色的存量账号会 401 登不进去", role)
		}
		for _, kind := range ResourceKinds() {
			if got := Ceiling(role, kind); got != row[kind] {
				t.Fatalf("Ceiling(%q, %s) = %s, want %s(0063 的点集与档位承诺漂移了)", role, kind, got, row[kind])
			}
		}
	}
}

// TestSeededPresetGranularity 钉住这套设计的真正目的:同一类别下的入口能分开给。
// KindServer 一条类别上挂着五处入口(主机状态 / 容器 / 证书 / 预览 / 异常检测),旧口径只有
// 「对 server 这一类能看还是能动」,所以这五处只能一起出现或一起消失。
func TestSeededPresetGranularity(t *testing.T) {
	useSeededPresets(t)

	// 开发者:看得见落点(排障要看部署起来没有),但一个落点操作都不给。
	for _, id := range []string{"server.view", "container.view", "cert.view", "preview.view", "anomaly.view"} {
		if !HasPerm("developer", id) {
			t.Fatalf("开发者该能看到 %s", id)
		}
	}
	for _, id := range []string{"server.exec", "container.operate", "preview.recycle", "anomaly.edit", "cluster.operate"} {
		if HasPerm("developer", id) {
			t.Fatalf("开发者不该拿到落点操作点 %s", id)
		}
	}
	// 运维反过来:落点全能动,编排只读(读得到流水线,改不了)。
	if HasPerm("ops", "project.edit") || HasPerm("ops", "library.edit") {
		t.Fatal("运维不该拿到编排编辑点")
	}
	for _, id := range []string{"container.operate", "server.exec", "anomaly.edit"} {
		if !HasPerm("ops", id) {
			t.Fatalf("运维该拿到 %s", id)
		}
	}
	// 只读:一个 operate 都不许有,设置类入口也进不去。
	for _, id := range PermsFor("viewer") {
		if !strings.HasSuffix(id, ".view") {
			t.Fatalf("只读角色拿到了非只读点 %q", id)
		}
	}
	for _, role := range []string{"user", "developer", "ops", "viewer"} {
		if SettingsAllowed(role) {
			t.Fatalf("预置档 %q 拿到了设置点,那等于批量造管理员", role)
		}
	}
	if HasPerm("user", PermSettingsAccess) {
		t.Fatal("普通用户不该拿到设置点")
	}
	if len(PermsFor("user")) != len(Perms())-1 {
		t.Fatalf("普通用户点集 = %d, want 全字典减一 %d", len(PermsFor("user")), len(Perms()))
	}
}

// TestRolesOrderMatchesCeilings 保证导出的名单与点集表是同一集合(漏一个就是选了没档位)。
func TestRolesOrderMatchesCeilings(t *testing.T) {
	listed := Roles()
	if len(listed) != len(rolePerms) {
		t.Fatalf("Roles() 有 %d 项,rolePerms 有 %d 项", len(listed), len(rolePerms))
	}
	for _, role := range listed {
		if _, ok := rolePerms[role]; !ok {
			t.Fatalf("Roles() 列了 %q,但 rolePerms 没有它", role)
		}
	}
}

// readFrontend 读 web 侧源码;读不到就 Skip(单独取 Go 模块时属正常)。
func readFrontend(t *testing.T, rel ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "web", "src"}, rel...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 %s(单独取 Go 模块时属正常):%v", path, err)
	}
	return string(raw)
}

// TestFrontendPermsMatchDict 验前端 PermId 联合类型与本表同集合,并逐个核对侧栏、路由、
// 设置页里引用的点都在字典内。
func TestFrontendPermsMatchDict(t *testing.T) {
	authSrc := readFrontend(t, "api", "auth.ts")
	match := permIdsRe.FindStringSubmatch(authSrc)
	if match == nil {
		t.Fatalf("api/auth.ts 里的 PermId 形状变了,请同步这里的解析")
	}
	declared := tsStrings(match[1])

	dict := make([]string, 0, len(permDict))
	for _, p := range permDict {
		dict = append(dict, p.ID)
	}
	sortedDict := append([]string(nil), dict...)
	sort.Strings(sortedDict)
	got := append([]string(nil), declared...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(sortedDict, ",") {
		t.Fatalf("前端 PermId 与字典不同集合:\n  前端 %v\n  字典 %v", got, sortedDict)
	}

	// 引用侧:三处门名单共用一份字典判据。
	for _, rel := range [][]string{{"layouts", "AppShell.vue"}, {"router", "index.ts"}, {"views", "Settings.vue"}} {
		for _, ref := range permRefRe.FindAllStringSubmatch(readFrontend(t, rel...), -1) {
			if _, ok := permByID[ref[1]]; !ok {
				t.Fatalf("%s 引用了字典里没有的功能点 %q(那个入口会对所有角色静默消失)",
					filepath.Join(rel...), ref[1])
			}
		}
	}
}
