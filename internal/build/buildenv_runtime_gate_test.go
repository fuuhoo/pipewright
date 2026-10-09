package build

import (
	"errors"
	"fmt"

	"github.com/fuuhoo/pipewright/internal/pipeline"
)

// 执行期预置目录的测试夹具(#9/#10)。
//
// 镜像只认目录里的条目:喂给构建器的假目录必须列出用例用到的镜像;目录外的镜像应被拒 ——
// 那正是「手填镜像已消灭」要证的。

// errGateBroken 模拟目录服务读失败(快照报错)——构建要诚实失败,不能退回手填镜像。
var errGateBroken = errors.New("buildenv 目录不可用")

type fakeGate struct{ snap pipeline.BuildEnvSnapshot }

func (g fakeGate) Snapshot() (pipeline.BuildEnvSnapshot, error) { return g.snap, nil }

// brokenGate 读取即失败:验证「目录服务挂了也不放行任意镜像」。
type brokenGate struct{}

func (brokenGate) Snapshot() (pipeline.BuildEnvSnapshot, error) {
	return pipeline.BuildEnvSnapshot{}, errGateBroken
}

// emptyGate 目录里一个环境都没有(全新安装尚未 seed)。
type emptyGate struct{}

func (emptyGate) Snapshot() (pipeline.BuildEnvSnapshot, error) {
	return pipeline.NewBuildEnvSnapshot(nil, nil), nil
}

// gateWithImages 造一个只含指定镜像的目录:用例常拿「镜像名」当执行顺序标记(见 orderDriver),
// 而执行期镜像必须来自目录,故这类夹具镜像在此显式登记 —— 而不是让构建器接受任意字符串。
func gateWithImages(images ...string) pipeline.BuildEnvGate {
	opts := make([]pipeline.BuildEnvOption, 0, len(images))
	for i, img := range images {
		opts = append(opts, pipeline.BuildEnvOption{
			ID: fmt.Sprintf("e-%d", i), Language: "test", Version: fmt.Sprintf("v%d", i),
			DisplayName: img, Image: img, Enabled: true,
		})
	}
	return fakeGate{snap: pipeline.NewBuildEnvSnapshot(opts, nil)}
}

// gateWithProfiles 造一个带配置资源的目录(注入用例 #10 用)。
func gateWithProfiles(envs []pipeline.BuildEnvOption, profiles []pipeline.ConfigProfileOption) pipeline.BuildEnvGate {
	return fakeGate{snap: pipeline.NewBuildEnvSnapshot(envs, profiles)}
}

// testBuildEnvGate 是默认可用目录(含测试里常写的几个镜像)。
var testBuildEnvGate = fakeGate{snap: testBuildEnvSnapshot()}

func testBuildEnvSnapshot() pipeline.BuildEnvSnapshot {
	return pipeline.NewBuildEnvSnapshot([]pipeline.BuildEnvOption{
		{ID: "e-node", Language: "node", Version: "20", DisplayName: "Node 20", Image: "node:20", Enabled: true},
		{ID: "e-node22", Language: "node", Version: "22", DisplayName: "Node 22", Image: "node:22", Enabled: true},
		{ID: "e-go", Language: "go", Version: "1.23", DisplayName: "Go 1.23", Image: "golang:1.23", Enabled: true},
		{ID: "e-busybox", Language: "shell", Version: "busybox", DisplayName: "BusyBox", Image: "busybox", Enabled: true},
		{ID: "e-alpine", Language: "shell", Version: "alpine", DisplayName: "Alpine", Image: "alpine", Enabled: true},
		{ID: "e-maven", Language: "java", Version: "21-maven", DisplayName: "Java 21 + Maven", Image: "maven:3.9-eclipse-temurin-21", Enabled: true},
		{ID: "e-priv", Language: "node", Version: "priv", DisplayName: "私有 Node", Image: "harbor.local/node:20", Enabled: true, CredentialID: "cred-1"},
		{ID: "e-off", Language: "node", Version: "18", DisplayName: "Node 18(已禁用)", Image: "node:18", Enabled: false},
	}, nil)
}
