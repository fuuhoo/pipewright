package buildenv

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// rsync_drift_test.go —— 「内置目录」与前端那张 rsync 实测清单之间的漂移守卫。
//
// 为什么会漂:步骤构建器对内置镜像会说出一句确定的「这个镜像里没有 rsync,排除项不生效」。
// 这句承诺的前提是内置目录每条都被实测过;漏一条,前端只会退回「运行时决定」—— 不算说谎,
// 但产品对外承诺的「平台自己发布的镜像都给确定答案」就悄悄打折了,而页面上一眼看不出来。
//
// 断言写在 Go 侧:前端 tsconfig 没引 node 类型,vitest 里读不了源文件
// (同 internal/access/roles_drift_test.go 的取舍)。这里直接 range builtinBuildEnvs,
// 不解析 seed.go 文本 —— 数据就在本包,抄一份解析只会先在自己这边漂。

// measuredRowRe 吃 MEASURED 里的 `'node:22-alpine': 'no',` 这种行。
var measuredRowRe = regexp.MustCompile(`(?m)^\s*'([^']+)':\s*'(yes|no|unknown)'`)

func readFrontendRsyncTable(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("..", "..", "web", "src", "lib", "buildEnvRsync.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 %s(单独取 Go 模块时属正常):%v", path, err)
	}
	out := map[string]string{}
	for _, m := range measuredRowRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("buildEnvRsync.ts 里一条实测清单都没解析出来,形状变了请同步这里的正则")
	}
	return out
}

func TestBuiltinEnvsHaveMeasuredRsyncVerdict(t *testing.T) {
	measured := readFrontendRsyncTable(t)

	var uncovered []string
	for _, sd := range builtinBuildEnvs {
		switch measured[sd.Image] {
		case "yes", "no":
			continue
		case "unknown":
			uncovered = append(uncovered, sd.Image+"(清单里标了 unknown)")
		default:
			uncovered = append(uncovered, sd.Image+"(清单里没有这一条)")
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Fatalf("内置目录这些镜像没有实测结论,前端只能对它说「运行时决定」:%s", strings.Join(uncovered, ", "))
	}
}
