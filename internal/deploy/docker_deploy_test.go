package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/target"
)

// docker_deploy_test.go 覆盖「docker 部署」节点(deploy_docker)在目标机上的落地。
// 全部用 stubTarget 捕获命令/上传,不触真实 SSH 与 compose。
//
// 盯的都是几处静默失败面:compose 正文必须以**文件**落地(不拼 shell)、项目名直接成为
// 受管目录名(非法就拒绝且一个命令都不发)、目标机没装 compose CLI 要出成人读的 failed,
// 以及单容器方式在「镜像 + 文件产物并存」时必须发镜像 —— 否则是把 dist 铺进机器却报容器已起。

// composeCfg 组一份最小可用的 compose 部署 cfg。
func composeCfg(name, yaml string) map[string]string {
	return map[string]string{CfgKeyDockerMode: DockerModeCompose, CfgKeyStackName: name, CfgKeyComposeYaml: yaml}
}

// argContains 报告命令序列里是否有某条命令的某个参数含 sub(用于确认正文没被拼进命令行)。
func argContains(calls [][]string, sub string) bool {
	for _, c := range calls {
		for _, a := range c {
			if strings.Contains(a, sub) {
				return true
			}
		}
	}
	return false
}

// TestComposeDeployUploadsFileAndUp 正常链路:建受管目录 → compose 正文以文件上传 → docker compose up -d。
func TestComposeDeployUploadsFileAndUp(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")
	const yaml = "services:\n  web:\n    image: nginx:latest\n"

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID}, composeCfg("shop-web", yaml), "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetSuccess {
		t.Fatalf("want 1 success, got %+v", res)
	}
	dir := stackBaseDir + "/shop-web"
	if !hasCmd(tgt.calls, "mkdir", "-p", dir) {
		t.Fatalf("应建受管目录 %s: %v", dir, tgt.calls)
	}
	got, ok := tgt.uploads[dir+"/"+composeFileName]
	if !ok {
		t.Fatalf("compose 正文应上传到 %s/%s,uploads=%v", dir, composeFileName, keysOf(tgt.uploads))
	}
	// 正文落盘前只去首尾空白(免得一整串空行成为栈内容),字句一律原样。
	if string(got) != strings.TrimSpace(yaml) {
		t.Fatalf("上传的正文与配置不一致,got %q", string(got))
	}
	if !hasCmd(tgt.calls, "docker", "compose", "-p", "shop-web", "-f", dir+"/"+composeFileName, "up", "-d") {
		t.Fatalf("应以 docker compose -p/-f up -d 起栈: %v", tgt.calls)
	}
	// AC-SEC-02:正文只以文件落地,绝不出现在任何一条命令的参数里。
	if argContains(tgt.calls, "nginx:latest") {
		t.Fatalf("compose 正文不得拼进 shell/命令行: %v", tgt.calls)
	}
	// 也不该顺手做别的事:不铺产物文件、不起单容器。
	if hasCmd(tgt.calls, "ln", "-sfn") || runCmd(tgt.calls) != nil {
		t.Fatalf("compose 部署不该起单容器或直铺产物: %v", tgt.calls)
	}
}

// TestComposeDeployFallsBackToV1 目标机只有 v1 独立命令时,用 docker-compose 而不是 v2 插件。
func TestComposeDeployFallsBackToV1(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")
	tgt.execFn = func(_ string, cmd []string) (*target.ExecResult, error) {
		if len(cmd) >= 2 && cmd[0] == "docker" && cmd[1] == "compose" {
			return &target.ExecResult{ExitCode: 1}, nil // 无 v2 插件
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID}, composeCfg("shop", "services: {}"), "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetSuccess {
		t.Fatalf("want 1 success, got %+v", res)
	}
	if !hasCmd(tgt.calls, "docker-compose", "-p", "shop", "-f", stackBaseDir+"/shop/"+composeFileName, "up", "-d") {
		t.Fatalf("应回退到 docker-compose(v1): %v", tgt.calls)
	}
}

// TestComposeDeployNoComposeCLI 两种 compose CLI 都没有 → 该机 failed,且原因说得清(不是「产物不存在」)。
func TestComposeDeployNoComposeCLI(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")
	tgt.execFn = func(_ string, cmd []string) (*target.ExecResult, error) {
		if cmd[len(cmd)-1] == "version" {
			return nil, errors.New("exec: command not found")
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID}, composeCfg("shop", "services: {}"), "")
	if err != nil {
		t.Fatalf("单机失败由 target 表达,不上抛: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetFailed {
		t.Fatalf("want 1 failed target, got %+v", res)
	}
	if !strings.Contains(res[0].Message, "未检测到") {
		t.Errorf("失败原因应指向缺 compose CLI,got %q", res[0].Message)
	}
	for _, c := range tgt.calls {
		if len(c) >= 6 && c[2] == "-p" {
			t.Fatalf("探测不通后不该继续 up: %v", tgt.calls)
		}
	}
	// 结果照常落库,运行详情页才看得到这句失败(而不是凭空少一台)。
	dts, derr := run.New(db).ListDeployTargets(context.Background(), runID)
	if derr != nil {
		t.Fatalf("ListDeployTargets: %v", derr)
	}
	if len(dts) != 1 || dts[0].Status != run.TargetFailed {
		t.Fatalf("compose 失败目标应已持久化,got %+v", dts)
	}
}

// TestComposeDeployRejectsBadSpec 项目名非法 / 正文缺失或超限 → 运行时兜底拒绝,且一个命令都不发。
// 项目名会直接成为受管目录名:净化它等于换了个栈名、旧栈还留在机器上,所以只能拒绝。
func TestComposeDeployRejectsBadSpec(t *testing.T) {
	cases := []struct {
		name   string
		cfg    map[string]string
		wantIn string
	}{
		{"缺项目名", composeCfg("", "services: {}"), "项目名"},
		{"名字含斜杠", composeCfg("../escape", "services: {}"), "非法"},
		{"名字以连字符开头", composeCfg("-app", "services: {}"), "非法"},
		{"名字超长", composeCfg(strings.Repeat("a", 129), "services: {}"), "超过"},
		{"缺正文", composeCfg("shop", "  "), "正文"},
		{"正文超限", composeCfg("shop", strings.Repeat("x", composeMaxBytes+1)), "上限"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			rsvc := run.New(db)
			tgt := &stubTarget{}
			srv := seedServer(t, tgt, "web-1")
			runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")

			svc := New(tgt, rsvc)
			_, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID}, tc.cfg, "")
			if !errors.Is(err, ErrComposeSpecInvalid) {
				t.Fatalf("err = %v, want wraps ErrComposeSpecInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("报错应含 %q,got %q", tc.wantIn, err.Error())
			}
			if len(tgt.calls) != 0 {
				t.Fatalf("规格不成立时不该在目标机上执行任何命令: %v", tgt.calls)
			}
			if len(tgt.uploads) != 0 {
				t.Fatalf("规格不成立时不该上传任何东西: %v", keysOf(tgt.uploads))
			}
		})
	}
}

// TestComposeDeployHealthGate compose 起成功 != 服务可用:配了探测就沿用同一门控语义。
func TestComposeDeployHealthGate(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")
	tgt.execFn = func(_ string, cmd []string) (*target.ExecResult, error) {
		if cmd[0] == "sh" {
			return &target.ExecResult{ExitCode: 1, Stderr: "nope"}, nil
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}

	cfg := composeCfg("shop", "services: {}")
	cfg[CfgKeyHealthProbe] = "command"
	cfg[CfgKeyHealthCommand] = "test -f /opt/app/OK"
	cfg[CfgKeyHealthRetries] = "1"

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID}, cfg, "")
	if err != nil {
		t.Fatalf("单机失败由 target 表达: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetFailed {
		t.Fatalf("want 1 failed target, got %+v", res)
	}
	// 探测命令确实跑了,且命令文本整体是一个 array 元素(不被拆成 argv、不额外拼 shell)。
	if !hasCmd(tgt.calls, "sh", "-c", "test -f /opt/app/OK") {
		t.Fatalf("应执行健康探测命令: %v", tgt.calls)
	}
}

// TestComposeDeployMultiServerSequential 多台目标机:逐机各铺各的,一台失败不带垮其余。
func TestComposeDeployMultiServerSequential(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")
	tgt.execFn = func(serverID string, cmd []string) (*target.ExecResult, error) {
		if serverID == s2.ID && cmd[0] == "mkdir" {
			return &target.ExecResult{ExitCode: 1, Stderr: "read-only file system"}, nil
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{s1.ID, s2.ID}, composeCfg("shop", "services: {}"), "")
	if err != nil {
		t.Fatalf("部分失败不该整体报错: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 targets, got %+v", res)
	}
	if res[0].Status != run.TargetSuccess || res[1].Status != run.TargetFailed {
		t.Fatalf("want success+failed, got %+v", res)
	}
	if !strings.Contains(res[1].Message, "创建受管目录失败") {
		t.Errorf("失败原因应指向建目录那步,got %q", res[1].Message)
	}
	// 第一台成功 → 它的 compose 文件确实落了;第二台连目录都没建成就不该有上传。
	if _, ok := tgt.uploads[stackBaseDir+"/shop/"+composeFileName]; !ok {
		t.Fatalf("成功那台应已上传 compose: %v", keysOf(tgt.uploads))
	}
}

// TestDockerRunModePrefersImageArtifact 单容器方式即使本次也产出了文件产物,发的仍是镜像。
// 不显式纠正偏好,pickStageArtifact 默认选文件 → 把 dist 铺进机器却报容器已起。
func TestDockerRunModePrefersImageArtifact(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/shop:1.0")
	addArtifact(t, rsvc, runID, run.ArtifactDist, "web", "dist/shop.tar.gz")

	cfg := map[string]string{CfgKeyDockerMode: DockerModeRun, "containerName": "shop"}
	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID}, cfg, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetSuccess {
		t.Fatalf("want 1 success, got %+v", res)
	}
	if !hasCmd(tgt.calls, "docker", "pull", "registry/shop:1.0") {
		t.Fatalf("单容器方式应拉取镜像产物: %v", tgt.calls)
	}
	if runCmd(tgt.calls) == nil {
		t.Fatalf("应有 docker run: %v", tgt.calls)
	}
	if hasCmd(tgt.calls, "ln", "-sfn") {
		t.Fatalf("不该走文件产物直铺: %v", tgt.calls)
	}
}

// TestDockerModeHelpers 方式判定只看去空白后的值;非 docker 节点(cfg 空)两种都不成立。
func TestDockerModeHelpers(t *testing.T) {
	if dockerModeOf(map[string]string{CfgKeyDockerMode: "  compose "}) != DockerModeCompose {
		t.Error("dockerModeOf 应去空白")
	}
	if !IsDockerRunMode(map[string]string{CfgKeyDockerMode: DockerModeRun}) {
		t.Error("run 方式应判为单容器")
	}
	if IsDockerRunMode(map[string]string{}) || dockerModeOf(map[string]string{}) != "" {
		t.Error("非 docker 部署节点不应判出方式")
	}
}

// TestComposeStackPaths 受管目录/文件路径的推导(与容器页 stacks 同一布局,两处一处看穿)。
func TestComposeStackPaths(t *testing.T) {
	dir, path, err := composeStackPaths("shop-web", "services: {}")
	if err != nil {
		t.Fatalf("composeStackPaths: %v", err)
	}
	if dir != stackBaseDir+"/shop-web" || path != stackBaseDir+"/shop-web/"+composeFileName {
		t.Fatalf("路径推导异常: %q %q", dir, path)
	}
}
