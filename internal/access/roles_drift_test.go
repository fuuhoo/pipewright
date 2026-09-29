package access

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// roles_drift_test.go —— 前端角色名单与本表(功能轴唯一权威)的漂移守卫。
//
// 为什么会漂:Go 加一个角色,前端下拉不会报错,只是那个角色永远选不出来 —— 页面上一眼
// 看不出来,只有管理员试到时才发现。这类静默缺口适合留在测试里挡。
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

// TestFrontendRolesMatchCeilings 盯两件事:下拉里的名单与本表键集一致,
// 且每个角色都有展示键(缺键会让页面渲染成裸 i18n key)。
func TestFrontendRolesMatchCeilings(t *testing.T) {
	order, labeled := readFrontendRoles(t)

	want := make([]string, 0, len(rolePerms))
	for role := range rolePerms {
		want = append(want, role)
	}
	got := append([]string(nil), order...)
	sort.Strings(want)
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("前端角色名单 %v 与角色点集 %v 条目数不同", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("前端角色名单与角色点集不一致:%v vs %v", got, want)
		}
	}

	labelSet := map[string]bool{}
	for _, role := range labeled {
		labelSet[role] = true
	}
	for role := range rolePerms {
		if !labelSet[role] {
			t.Fatalf("角色 %s 在 ROLE_LABEL_KEY 里没有展示键,页面会露出裸 key", role)
		}
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
