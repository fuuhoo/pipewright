package access

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// roles_drift_test.go —— 前端角色名单与本表(功能轴唯一权威)的漂移守卫。
//
// 为什么会漂:Go 加一个角色,前端下拉不会报错,只是那个角色永远选不出来 —— 页面上一眼
// 看不出来,只有管理员试到时才发现。这类静默缺口适合留在测试里挡。
// 断言写在 Go 侧是因为前端 tsconfig 没引 node 类型,单测里读不了源码文件。

var (
	roleOrderRe = regexp.MustCompile(`export const ROLE_ORDER: UserRole\[\] = \[([^\]]*)\]`)
	labelKeysRe = regexp.MustCompile(`export const ROLE_LABEL_KEY: Record<UserRole, string> = \{([\s\S]*?)\n\}`)
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

	want := make([]string, 0, len(ceilings))
	for role := range ceilings {
		want = append(want, role)
	}
	got := append([]string(nil), order...)
	sort.Strings(want)
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("前端角色名单 %v 与 ceilings %v 条目数不同", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("前端角色名单与 ceilings 不一致:%v vs %v", got, want)
		}
	}

	labelSet := map[string]bool{}
	for _, role := range labeled {
		labelSet[role] = true
	}
	for role := range ceilings {
		if !labelSet[role] {
			t.Fatalf("角色 %s 在 ROLE_LABEL_KEY 里没有展示键,页面会露出裸 key", role)
		}
	}
}

// TestRolesOrderMatchesCeilings 保证导出的名单与查表是同一集合(漏一个就是选了没档位)。
func TestRolesOrderMatchesCeilings(t *testing.T) {
	listed := Roles()
	if len(listed) != len(ceilings) {
		t.Fatalf("Roles() 有 %d 项,ceilings 有 %d 项", len(listed), len(ceilings))
	}
	for _, role := range listed {
		if _, ok := ceilings[role]; !ok {
			t.Fatalf("Roles() 列了 %q,但 ceilings 没有它", role)
		}
	}
}
