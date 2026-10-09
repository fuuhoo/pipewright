package httpapi

// server_stacks_ssh_e2e_test.go 把 docker 那两条写路由放回**产品真正用的那条传输**上跑:
// dialer 传 nil → target.New 装上真的 x/crypto/ssh,于是这一条覆盖的是 localDialer
// 刻意绕开的那两层 —— SSH 会话本身,以及 svc.Upload 那句
// `mkdir -p "$(dirname "$0")" && cat > "$0"` 经 SSH stdin 把正文写到「对面」的盘上。
//
// 对面不是远端机器,就是这台 Mac 自己的 sshd(远程登录),docker 仍由本机 OrbStack 的
// daemon 接住:所以既没有外部依赖,也不是假 dialer 那种「我拼什么自己认什么」。
//
// 默认 SKIP。跑法:
//
//	# 系统设置 → 通用 → 共享 → 远程登录(且公钥在 ~/.ssh/authorized_keys 里)
//	PIPEWRIGHT_E2E_SSH=1 go test ./internal/httpapi/ -run OverRealSSH -v
//
// 可选覆盖:PIPEWRIGHT_E2E_SSH_HOST / _PORT / _USER / _KEY(私钥路径)。
// 前置不齐一律 Skip,不当失败:那是环境缺东西,不是代码错了。
//
// 与 server_stacks_docker_e2e_test.go 的分工:那一条证「argv 的拼法在真 docker 上成立」,
// 这一条证「同一套路由经真 SSH 落到一台机器上仍然成立」。两条都用临时受管目录,都不写 /opt。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fuuhoo/pipewright/internal/audit"
)

// sshE2EEnv 是本机 sshd 的登录参数(默认 cw@127.0.0.1:22 + 那份 id_ed25519)。
type sshE2EEnv struct {
	host    string
	port    int
	user    string
	privKey []byte
}

// requireRealSSH 确认「开了开关 + 这台机自己的 sshd 登得上去 + 那边找得到 docker」,并验到那份本地镜像。
// 探测直接用 ssh 库跑一遍 docker —— 与路由要走的是同一条路径,探测过了就没有中途变卦。
func requireRealSSH(t *testing.T) sshE2EEnv {
	t.Helper()
	if os.Getenv("PIPEWRIGHT_E2E_SSH") != "1" {
		t.Skip("设 PIPEWRIGHT_E2E_SSH=1 启用「经本机 sshd 真跑」e2e")
	}
	requireLocalDockerImage(t) // 同一条 daemon、同一份镜像:两条 e2e 用同一套前置

	env := sshE2EEnv{host: "127.0.0.1", port: 22}
	if v := os.Getenv("PIPEWRIGHT_E2E_SSH_HOST"); v != "" {
		env.host = v
	}
	if v := os.Getenv("PIPEWRIGHT_E2E_SSH_PORT"); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err != nil || p <= 0 || p > 65535 {
			t.Fatalf("PIPEWRIGHT_E2E_SSH_PORT=%q 不是合法端口", v)
		}
		env.port = p
	}
	env.user = os.Getenv("PIPEWRIGHT_E2E_SSH_USER")
	if env.user == "" {
		u, err := user.Current()
		if err != nil {
			t.Skipf("取不到当前用户名(设 PIPEWRIGHT_E2E_SSH_USER 指定): %v", err)
		}
		env.user = u.Username
	}
	keyPath := os.Getenv("PIPEWRIGHT_E2E_SSH_KEY")
	if keyPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("取不到 home(设 PIPEWRIGHT_E2E_SSH_KEY 指定私钥): %v", err)
		}
		keyPath = filepath.Join(home, ".ssh", "id_ed25519")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Skipf("读不到私钥 %s: %v", keyPath, err)
	}
	env.privKey = key

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Skipf("私钥 %s 解析不了(带口令的密钥这条不接): %v", keyPath, err)
	}
	addr := fmt.Sprintf("%s:%d", env.host, env.port)
	cli, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            env.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 对端是自己:威胁面在认证,不在第一跳的指纹
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Skipf("登不上 %s(远程登录没开?公钥没进 ~/.ssh/authorized_keys?): %v", addr, err)
	}
	defer cli.Close()
	sess, err := cli.NewSession()
	if err != nil {
		t.Fatalf("SSH 连上了却开不了会话: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("command -v docker >/dev/null 2>&1 && docker version -f '{{.Server.Version}}'")
	if err != nil {
		t.Skipf("SSH 过去了但那边跑不动 docker(OrbStack 没起,或 docker 不在非交互 shell 的 PATH 上): %v", err)
	}
	t.Logf("经真 SSH 到 %s@%s 跑通,docker server %s", env.user, addr, strings.TrimSpace(string(out)))
	return env
}

// newRealSSHServerAPI 用**真实私钥**建凭据 + 登记服务器(全走 API),返回服务器 id。
// 顺带验了凭据那条路:vault 里存的是加密后的正文,拿去连的还是同一个 sshd。
func newRealSSHServerAPI(t *testing.T, client *http.Client, srvURL, csrf string, env sshE2EEnv) string {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srvURL+"/api/credentials", csrf,
		`{"name":"e2e-loopback-ssh","type":"ssh_key","secret":`+jsonString(string(env.privKey))+`}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var cred struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &cred); err != nil || cred.ID == "" {
		t.Fatalf("建 ssh_key 凭据失败(%d): %s", resp.StatusCode, truncateLog(string(raw), 200))
	}
	resp2 := doJSON(t, client, http.MethodPost, srvURL+"/api/servers", csrf,
		fmt.Sprintf(`{"name":"e2e-loopback","host":%s,"port":%d,"user":%s,"credentialId":%s}`,
			jsonString(env.host), env.port, jsonString(env.user), jsonString(cred.ID)))
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(resp2.Body)
	var srv struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw2, &srv); err != nil || srv.ID == "" {
		t.Fatalf("登记服务器失败(%d): %s", resp2.StatusCode, truncateLog(string(raw2), 200))
	}
	return srv.ID
}

// --- ① compose 粘贴部署:经真 SSH 建目录 + 落文件 + up,再改一行存回去 ---

// TestE2EComposeDeployOverRealSSH 盯的是传输层换掉之后仍然成立的那几件事:compose 正文要逐字
// 出现在「对面」的盘上(那是 SSH 的 stdin,不是本地写文件),栈要以那个项目名起得来(-p 的值
// 经过一层 shell 引号转义之后还得是原值),保存之后 up 用的是改过的那份文件。
func TestE2EComposeDeployOverRealSSH(t *testing.T) {
	env := requireRealSSH(t)
	img := requireLocalDockerImage(t)
	project := fmt.Sprintf("pw-e2e-ssh-%d", os.Getpid())
	t.Cleanup(func() { rmStackForTest(project) })

	restoreBase := stacksBaseDir
	stacksBaseDir = filepath.Join(t.TempDir(), "stacks")
	t.Cleanup(func() { stacksBaseDir = restoreBase })

	srv, client, csrf, rec := setupServiceOpsAPI(t, nil) // nil = 产品默认的 dialer(真 SSH)
	id := newRealSSHServerAPI(t, client, srv.URL, csrf, env)

	pasted := "# 经真 SSH 落地的栈\nservices:\n  app:\n    image: " + img + "\n    command: sleep 300\n"
	deployed, raw, code := postStack(t, client, srv.URL, csrf, id, "/stacks/deploy",
		`{"name":`+jsonString(project)+`,"compose":`+jsonString(pasted)+`}`)
	if code != http.StatusOK || !deployed.OK {
		t.Fatalf("经真 SSH 部署应成功: code=%d body=%s", code, truncateLog(string(raw), 300))
	}

	wantFile := filepath.Join(stacksBaseDir, project, composeFileName)
	onDisk, err := os.ReadFile(wantFile)
	if err != nil {
		t.Fatalf("对面应被写出那份 compose: %v", err)
	}
	if string(onDisk) != strings.TrimSpace(pasted) {
		t.Fatalf("正文应逐字落到对面(中文/注释/缩进都不动), got %q", onDisk)
	}
	running := hostDocker(t, "ps", "-q", "--filter", "label=com.docker.compose.project="+project)
	if strings.TrimSpace(running) == "" {
		t.Fatalf("compose 正文经真 SSH 传过去了但栈没起来:见 %s", wantFile)
	}

	saved, raw2, code2 := postStack(t, client, srv.URL, csrf, id, "/stacks/save",
		`{"name":`+jsonString(project)+`,"configFile":`+jsonString(wantFile)+
			`,"compose":`+jsonString(strings.Replace(strings.TrimSpace(pasted), "sleep 300", "sleep 600", 1))+`}`)
	if code2 != http.StatusOK || !saved.OK {
		t.Fatalf("保存应成功: code=%d body=%s", code2, truncateLog(string(raw2), 300))
	}
	after, err := os.ReadFile(wantFile)
	if err != nil || !strings.Contains(string(after), "sleep 600") {
		t.Fatalf("对面那份文件应被改成 sleep 600, got %q err=%v", after, err)
	}
	latest := hostDocker(t, "ps", "-q", "--filter", "label=com.docker.compose.project="+project)
	if strings.TrimSpace(latest) == "" {
		t.Fatalf("保存后栈应仍在运行")
	}
	first := strings.Fields(latest)[0]
	if cmd := hostDocker(t, "inspect", "-f", "{{json .Config.Cmd}}", first); !strings.Contains(cmd, "600") {
		t.Fatalf("保存后应按改过的文件重起(sleep 600), got %s", cmd)
	}

	entries, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionStackOp})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries.Entries) != 2 {
		t.Fatalf("deploy + save 应各留 1 条审计, got %d", len(entries.Entries))
	}
	for _, e := range entries.Entries {
		if e.Detail["ok"] != true {
			t.Fatalf("两条审计都该是成功: %+v", e.Detail)
		}
		if strings.Contains(fmtDetail(e), "sleep 300") || strings.Contains(fmtDetail(e), "经真 SSH") {
			t.Fatalf("compose 正文不该进审计: %s", fmtDetail(e))
		}
	}
}

// --- ② 单容器:docker run 经真 SSH 落地 ---

// TestE2ECreateContainerOverRealSSH 单独留一条最普通的:env 值与 command 要穿过
// SSH 那一层 shell 引号转义,原样落到容器里 —— 少一道引号就会在这里红。
func TestE2ECreateContainerOverRealSSH(t *testing.T) {
	env := requireRealSSH(t)
	img := requireLocalDockerImage(t)
	name := fmt.Sprintf("pw-e2e-ssh-run-%d", os.Getpid())
	t.Cleanup(func() {
		_, _ = exec.Command("docker", "rm", "-f", name).CombinedOutput()
	})

	srv, client, csrf, _ := setupServiceOpsAPI(t, nil)
	id := newRealSSHServerAPI(t, client, srv.URL, csrf, env)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/containers", csrf,
		`{"image":`+jsonString(img)+`,"name":`+jsonString(name)+
			`,"restart":"no","env":["E2E_SSH_MARK=on"],"command":"sleep 300"}`)
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
		t.Fatalf("经真 SSH 建容器应成功: %+v", dto)
	}
	if got := hostDocker(t, "inspect", "-f", "{{.Id}}", name); got != dto.ContainerID {
		t.Fatalf("containerId 应是那台机器上真容器的完整 ID %s, got %s", got, dto.ContainerID)
	}
	if st := hostDocker(t, "inspect", "-f", "{{.State.Running}}", name); st != "true" {
		t.Fatalf("容器应在运行, got %q", st)
	}
	if envs := hostDocker(t, "inspect", "-f", "{{json .Config.Env}}", name); !strings.Contains(envs, "E2E_SSH_MARK=on") {
		t.Fatalf("env 经 SSH 的引号转义后仍要原样进容器, got %s", envs)
	}
}
