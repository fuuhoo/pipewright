package httpapi

// server_stacks_docker_e2e_test.go 是 docker 那三条写路由的**真执行**端到端 —— 但不碰任何远端:
// localDialer 把 target.SSHDialer 落在**本机 exec** 上,路由产出的 argv 一字不改地真跑,
// docker 命令最终由本机 OrbStack 的 daemon 收到。
//
// 它补的是 fake dialer 证不到的那一层:server_stacks_routes_test.go 能证明「命令长这样」,
// 证不了「那样拼 docker 真的起得来」「那份 compose 正文真的能 up」「保存之后 up 用的是改过的那份」。
// 这三件事只有真 daemon 会告诉你(拼错的 flag、被吞的缩进、-p 没带上导致起了个匿名栈)。
//
// 默认 SKIP。跑法(只用本机,不需要任何 SSH 目标机):
//
//	PIPEWRIGHT_E2E_DOCKER=1 go test ./internal/httpapi/ -run E2E -v
//
// 镜像默认取本机已有的那份(这台机 docker.io 拉不动);要换:PIPEWRIGHT_E2E_IMAGE=alpine:3.20。
// 起的容器/栈按唯一名 + t.Cleanup 兜底 docker rm -f,绝不留垃圾;受管目录指向临时目录,不写 /opt。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/target"
)

// e2eImageDefault 是本机一定拉得动的一份镜像(有 sh/sleep,够起一个长驻容器)。
const e2eImageDefault = "registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.12"

// requireLocalDocker 确认「本机 docker 可用 + 那份镜像在本地」,返回镜像名;任一不满足即 Skip
// (这是真机 e2e,环境不齐不该把单测跑成红)。
func requireLocalDocker(t *testing.T) string {
	t.Helper()
	if os.Getenv("PIPEWRIGHT_E2E_DOCKER") != "1" {
		t.Skip("设 PIPEWRIGHT_E2E_DOCKER=1 启用「本机 OrbStack 真跑」e2e")
	}
	return requireLocalDockerImage(t)
}

// requireLocalDockerImage 是「镜像在本地、daemon 在跑」那半截,不含开关判定:
// 另一条走真 SSH 的 e2e 要同一份镜像,但它有自己的开关(见 server_stacks_ssh_e2e_test.go)。
func requireLocalDockerImage(t *testing.T) string {
	t.Helper()
	dbin := dockerBinOrSkip(t)
	if _, err := exec.Command(dbin, "version", "-f", "{{.Server.Version}}").Output(); err != nil {
		t.Skipf("本机 docker daemon 不可用(OrbStack 没起?): %v", err)
	}
	img := os.Getenv("PIPEWRIGHT_E2E_IMAGE")
	if img == "" {
		img = e2eImageDefault
	}
	if _, err := exec.Command(dbin, "image", "inspect", img).Output(); err != nil {
		t.Skipf("本地没有镜像 %s:用 PIPEWRIGHT_E2E_IMAGE 指一份本地有的(能跑 sleep 的)", img)
	}
	return img
}

// hostDocker 从测试进程本身问 docker(不经被测路由),用于「亲验路由的产物真落在了机器上」。
func hostDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(dockerBinOrSkip(t), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// localDialer 是真 exec 版 dialer:Run/RunWithStdin 把 array 原样交给 exec,不拼 shell、
// 不改一个参数 —— 所以「路由怎么拼」与「机器上怎么跑」之间没有任何替换空间。
type localDialer struct {
	mu   sync.Mutex
	runs [][]string
}

func (d *localDialer) record(cmd []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs = append(d.runs, append([]string{}, cmd...))
}

func (d *localDialer) Run(ctx context.Context, _ string, _ target.SSHConfig, cmd []string) (*target.ExecResult, error) {
	return d.execLocal(ctx, cmd, nil)
}

func (d *localDialer) RunWithStdin(ctx context.Context, _ string, _ target.SSHConfig, cmd []string, stdin io.Reader) (*target.ExecResult, error) {
	return d.execLocal(ctx, cmd, stdin)
}

func (d *localDialer) RunStream(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (io.ReadCloser, error) {
	d.record(cmd)
	return nil, errors.New("localDialer: 这些路由不用流式执行")
}

func (d *localDialer) RunInteractive(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (target.Session, error) {
	d.record(cmd)
	return nil, errors.New("localDialer: 这些路由不用交互会话")
}

func (d *localDialer) execLocal(ctx context.Context, cmd []string, stdin io.Reader) (*target.ExecResult, error) {
	d.record(cmd)
	if len(cmd) == 0 {
		return nil, errors.New("localDialer: 空命令")
	}
	var out, errB bytes.Buffer
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Stdout, c.Stderr = &out, &errB
	if stdin != nil {
		c.Stdin = stdin
	}
	err := c.Run()
	if err == nil {
		return &target.ExecResult{Stdout: out.String(), Stderr: errB.String()}, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) { // 跑起来了但非零退出 → 与真机同一形状(退出码 + stderr)
		return &target.ExecResult{Stdout: out.String(), Stderr: errB.String(), ExitCode: ee.ExitCode()}, nil
	}
	// 本机没这个可执行 = 真机上的 127 command not found,是「目标机少东西」而不是「连不上」。
	return &target.ExecResult{Stdout: out.String(), Stderr: err.Error(), ExitCode: 127}, nil
}

// rmStackForTest 兜底清掉某个 compose 项目留下的容器与网络(t.Cleanup 里用,绝不 Fail)。
// 网络要单独删:`docker rm -f` 只带走容器,compose 建的 `<项目>_default` 会留在机器上。
func rmStackForTest(project string) {
	dbin, err := exec.LookPath("docker")
	if err != nil {
		return
	}
	if out, _ := exec.Command(dbin, "ps", "-aq", "--filter", "label=com.docker.compose.project="+project).Output(); len(strings.Fields(string(out))) > 0 {
		_, _ = exec.Command(dbin, append([]string{"rm", "-f"}, strings.Fields(string(out))...)...).CombinedOutput()
	}
	nets, _ := exec.Command(dbin, "network", "ls", "-q", "--filter", "name="+project+"_").Output()
	if names := strings.Fields(string(nets)); len(names) > 0 {
		_, _ = exec.Command(dbin, append([]string{"network", "rm"}, names...)...).CombinedOutput()
	}
}

// --- ① 单容器:docker run 真起来 ---

func TestE2ECreateContainerRunsOnLocalDocker(t *testing.T) {
	img := requireLocalDocker(t)
	name := fmt.Sprintf("pw-e2e-run-%d", os.Getpid())
	t.Cleanup(func() {
		_, _ = exec.Command(dockerBinOrSkip(t), "rm", "-f", name).CombinedOutput()
	})

	dialer := &localDialer{}
	srv, client, csrf, rec := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/containers", csrf,
		`{"image":"`+img+`","name":"`+name+`","restart":"no","env":["E2E_MARK=on"],"command":"sleep 300"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", resp.StatusCode, truncateLog(string(raw), 300))
	}
	var dto createContainerDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode: %v body=%s", err, truncateLog(string(raw), 300))
	}
	if !dto.OK || dto.ContainerID == "" {
		t.Fatalf("路由报创建失败(说明 docker run 的拼法在真 docker 上不成立): %+v", dto)
	}

	// 亲验一:容器真在跑,且回来的就是它的完整 ID。
	if running := hostDocker(t, "inspect", "-f", "{{.State.Running}}", name); running != "true" {
		t.Fatalf("容器应在运行,inspect=%q", running)
	}
	if longID := hostDocker(t, "inspect", "-f", "{{.Id}}", name); longID != dto.ContainerID {
		t.Fatalf("containerId 应是完整 ID %s, got %s", longID, dto.ContainerID)
	}
	// 亲验二:env 与 command 都真进了容器配置(不只是出现在 argv 里)。
	if env := hostDocker(t, "inspect", "-f", "{{json .Config.Env}}", name); !strings.Contains(env, "E2E_MARK=on") {
		t.Fatalf("容器内应有 E2E_MARK=on, got %s", env)
	}
	if args := hostDocker(t, "inspect", "-f", "{{json .Config.Cmd}}", name); !strings.Contains(args, "sleep") || !strings.Contains(args, "300") {
		t.Fatalf("容器命令应是 sleep 300, got %s", args)
	}

	// 审计留痕且不带上 env 的值。
	entries, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionContainerCreate})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries.Entries) != 1 || entries.Entries[0].Detail["ok"] != true {
		t.Fatalf("应有 1 条 ok:true 的 container_create 审计, got %+v", entries.Entries)
	}
	if strings.Contains(fmtDetail(entries.Entries[0]), "E2E_MARK") {
		t.Fatalf("env 值不该进审计: %s", fmtDetail(entries.Entries[0]))
	}
}

// --- ② + ③ compose 粘贴部署 → 详情认出那份文件 → 改完保存写回同一份文件并重新 up ---

// TestE2EComposePasteDeployThenSaveToSameFile 把「粘贴」和「文件选择」接成一串跑:
// 粘贴上去的正文要逐字成为机器上那份文件(注释/中文/缩进都不许被磨平),起起来的栈要带得上
// 项目标签(否则下次再也认不出它),详情据此给出的可编辑路径必须就是那份文件,保存之后
// 磁盘上的字节和**正在跑的容器**都要跟着变 —— 只有真 docker 能证明这条链没断。
func TestE2EComposePasteDeployThenSaveToSameFile(t *testing.T) {
	img := requireLocalDocker(t)
	project := fmt.Sprintf("pw-e2e-%d", os.Getpid())
	t.Cleanup(func() { rmStackForTest(project) })

	// 受管目录指到临时目录:既不写 /opt,也能直接在本地文件系统上核对「远端那份文件」。
	restoreBase := stacksBaseDir
	stacksBaseDir = filepath.Join(t.TempDir(), "stacks")
	t.Cleanup(func() { stacksBaseDir = restoreBase })

	dialer := &localDialer{}
	srv, client, csrf, rec := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	pasted := "# 上线前的临时栈\nservices:\n  web:\n    image: " + img + "\n    command: sleep 300\n"
	deployed, raw, code := postStack(t, client, srv.URL, csrf, id, "/stacks/deploy",
		`{"name":`+jsonString(project)+`,"compose":`+jsonString(pasted)+`}`)
	if code != http.StatusOK || !deployed.OK {
		t.Fatalf("粘贴部署应成功(真 compose up 失败即拼法有问题): code=%d body=%s", code, raw)
	}

	wantFile := filepath.Join(stacksBaseDir, project, composeFileName)
	onDisk, err := os.ReadFile(wantFile)
	if err != nil {
		t.Fatalf("受管目录里应有那份 compose: %v", err)
	}
	if string(onDisk) != strings.TrimSpace(pasted) {
		t.Fatalf("正文应逐字落盘(注释/缩进/中文都不动), got %q", onDisk)
	}

	ids := hostDocker(t, "ps", "-q", "--filter", "label=com.docker.compose.project="+project)
	if strings.TrimSpace(ids) == "" {
		t.Fatalf("栈应真起来(按项目标签查不到容器);up 输出=%s", deployed.Output)
	}
	if len(strings.Fields(ids)) != 1 {
		t.Fatalf("该栈应只有 1 个容器, got %q", ids)
	}
	if cmd := hostDocker(t, "inspect", "-f", "{{json .Config.Cmd}}", strings.Fields(ids)[0]); !strings.Contains(cmd, "300") {
		t.Fatalf("容器应按正文里的 sleep 300 起, got %s", cmd)
	}

	// 详情:文件是标签给的还是猜的都无所谓,关键是**同一份**,且据此判可编辑。
	detail := getStackDetail(t, client, srv.URL, id, project)
	if !detail.Reachable || detail.Runtime != "docker" {
		t.Fatalf("详情应可达且认出 docker: %+v", detail)
	}
	if detail.ComposeSource != "file" || !detail.Editable {
		t.Fatalf("详情应认到 compose 文件并判可编辑: %+v", detail)
	}
	if detail.ConfigFiles != wantFile {
		t.Fatalf("详情给的路径应就是受管目录那份, want %s got %s", wantFile, detail.ConfigFiles)
	}
	if strings.TrimSpace(detail.Compose) != strings.TrimSpace(pasted) {
		t.Fatalf("详情读回的正文应与提交的同一份, got %q", detail.Compose)
	}
	if detail.Total != 1 || detail.Running != 1 {
		t.Fatalf("详情应报 1 个服务在跑: total=%d running=%d", detail.Total, detail.Running)
	}

	// ③ 文件选择保存:改一个数,写回**同一份文件**,并让 up 按新那份重来。
	edited := strings.Replace(strings.TrimSpace(pasted), "sleep 300", "sleep 600", 1)
	saved, raw2, code2 := postStack(t, client, srv.URL, csrf, id, "/stacks/save",
		`{"name":`+jsonString(project)+`,"configFile":`+jsonString(wantFile)+`,"compose":`+jsonString(edited)+`}`)
	if code2 != http.StatusOK || !saved.OK {
		t.Fatalf("保存重部署应成功: code=%d body=%s", code2, raw2)
	}
	after, err := os.ReadFile(wantFile)
	if err != nil || strings.TrimSpace(string(after)) != edited {
		t.Fatalf("应写回原文件 %s, got %q err=%v", wantFile, after, err)
	}
	running := hostDocker(t, "ps", "-q", "--filter", "label=com.docker.compose.project="+project)
	if strings.TrimSpace(running) == "" {
		t.Fatalf("保存后栈应仍在运行")
	}
	if cmd := hostDocker(t, "inspect", "-f", "{{json .Config.Cmd}}", strings.Fields(running)[0]); !strings.Contains(cmd, "600") {
		t.Fatalf("保存后应按改过的文件重起(sleep 600), got %s", cmd)
	}

	// 审计:两次写操作都留痕,正文一个字都不进审计。
	entries, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionStackOp})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries.Entries) != 2 {
		t.Fatalf("deploy + save 应各留 1 条审计, got %d", len(entries.Entries))
	}
	for _, e := range entries.Entries {
		if e.Detail["name"] != project || e.Detail["ok"] != true {
			t.Fatalf("审计 detail 不符: %+v", e.Detail)
		}
		if strings.Contains(fmtDetail(e), "sleep 300") || strings.Contains(fmtDetail(e), "上线前") {
			t.Fatalf("compose 正文不该进审计: %s", fmtDetail(e))
		}
	}
}
