package httpapi

// server_stacks_routes_test.go 打的是容器页那三条写路由的**整链路**:HTTP 入参 → 校验 →
// 经 SSH 发到目标机的命令 array → 响应契约 → 审计留痕。
//
// 纯函数那层(validateCreateSpec / buildDockerRunCmd / composeFileArgs / primaryComposePath)
// 早已各自有单测,这里补的是它们**接起来**之后的静默失败面:
//   - 单容器:参数以 array 落到 `docker run` 的位置上,env 值不进审计;
//   - compose 粘贴:正文只以文件流出去(绝不出现在任何一条命令的参数里),up 用探测到的那套 CLI;
//   - compose 文件选择:保存写回的是详情给的那份原文件(不是受管目录),路径不合法就地拒掉;
//   - 三条路由的拒绝路径都必须一个命令都不发(否则是「校验没拦住但机器上已经动手」)。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/target"
)

// progDialer 按命令内容给答复,并逐条记下真正发到目标机上的 argv 与上传正文。
// 与 capturingDialer 的差别就是「只记最后一条」不够用:compose 部署是四步链路
// (建目录 → 上传 → 探测 CLI → up),要盯的是整序列。
type progDialer struct {
	answer func(cmd []string) *target.ExecResult
	calls  [][]string
	stdin  map[string]string // 上传:远端路径 → 正文
}

func (d *progDialer) exec(cmd []string) *target.ExecResult {
	d.calls = append(d.calls, cmd)
	if d.answer == nil {
		return &target.ExecResult{ExitCode: 0}
	}
	res := d.answer(cmd)
	if res == nil {
		return &target.ExecResult{ExitCode: 0}
	}
	return res
}

func (d *progDialer) Run(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (*target.ExecResult, error) {
	return d.exec(cmd), nil
}

func (d *progDialer) RunWithStdin(_ context.Context, _ string, _ target.SSHConfig, cmd []string, in io.Reader) (*target.ExecResult, error) {
	body, _ := io.ReadAll(in)
	if p := uploadTargetPath(cmd); p != "" {
		if d.stdin == nil {
			d.stdin = map[string]string{}
		}
		d.stdin[p] = string(body)
	}
	return d.exec(cmd), nil
}

func (d *progDialer) RunStream(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (io.ReadCloser, error) {
	d.calls = append(d.calls, cmd)
	return nil, errors.New("RunStream not expected in these tests")
}

func (d *progDialer) RunInteractive(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (target.Session, error) {
	d.calls = append(d.calls, cmd)
	return nil, errors.New("RunInteractive not expected in these tests")
}

// uploadTargetPath 从 target.Service.Upload 的命令里取出目标路径。
// 该命令固定是 `sh -c 'mkdir -p "$(dirname "$0")" && cat > "$0"' <path>`:路径作 $0,
// 认这个形状本身就是断言「正文没被拼进脚本体」。
func uploadTargetPath(cmd []string) string {
	if len(cmd) != 4 || cmd[0] != "sh" || cmd[1] != "-c" || !strings.Contains(cmd[2], `cat > "$0"`) {
		return ""
	}
	return cmd[3]
}

func cmdHas(cmd []string, want ...string) bool {
	if len(cmd) < len(want) {
		return false
	}
	for i := range want {
		if cmd[i] != want[i] {
			return false
		}
	}
	return true
}

// findCall 返回首条以 want 开头的命令(没有则 nil)。
func (d *progDialer) findCall(want ...string) []string {
	for _, c := range d.calls {
		if cmdHas(c, want...) {
			return c
		}
	}
	return nil
}

func (d *progDialer) countCall(want ...string) int {
	n := 0
	for _, c := range d.calls {
		if cmdHas(c, want...) {
			n++
		}
	}
	return n
}

// argvBlob 把全部命令拼成一坨文本,用于断言某串内容**没有**出现在任何参数里。
func (d *progDialer) argvBlob() string {
	parts := make([]string, 0, len(d.calls))
	for _, c := range d.calls {
		parts = append(parts, strings.Join(c, "\x00"))
	}
	return strings.Join(parts, "\x01")
}

// composeV2 是装了 docker compose v2 插件的目标机(单容器/compose 两条路的默认夹具)。
func composeV2() *progDialer {
	return &progDialer{answer: func(cmd []string) *target.ExecResult {
		switch {
		case cmdHas(cmd, "docker", "compose", "version"):
			return &target.ExecResult{Stdout: "Docker Compose version v2.24.0"}
		case cmdHas(cmd, "docker", "compose"):
			return &target.ExecResult{Stdout: " ✔ Container shop-web-1  Started"}
		}
		return &target.ExecResult{}
	}}
}

func postStack(t *testing.T, client *http.Client, srvURL, csrf, id, path, body string) (stackDTOResult, []byte, int) {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srvURL+"/api/servers/"+id+path, csrf, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var dto stackDTOResult
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode %s: %v body=%s", path, err, truncateLog(string(raw), 200))
	}
	return dto, raw, resp.StatusCode
}

// rejectStack 打拒绝路径:400 的响应体是 errBody({code,message}),与成功/业务失败的
// stackDTOResult(error 是字符串)结构不同,不能用上面的解码器。
func rejectStack(t *testing.T, client *http.Client, srvURL, csrf, id, path, body string) (errBody, int, []byte) {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srvURL+"/api/servers/"+id+path, csrf, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var e errBody
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode %s: %v body=%s", path, err, truncateLog(string(raw), 200))
	}
	return e, resp.StatusCode, raw
}

// --- ① compose 粘贴部署 ---

// TestStackDeployWritesPastedComposeAsFile 正常链路:建受管目录 → 正文以文件写出 → v2 up -d。
// 盯三件事:正文除首尾空白外**逐字**落进上传流(YAML 的缩进一被动就等于换了服务定义)、
// 正文一个字都不出现在命令参数里、up 的 -p 用项目名(否则机器上是一栈匿名容器,下次再也找不到)。
func TestStackDeployWritesPastedComposeAsFile(t *testing.T) {
	dialer := composeV2()
	srv, client, csrf, rec := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	// 带注释、中文、引号与两层缩进:这些是最容易被「顺手规范化」弄坏的部分。
	body := "# 上线前的临时栈\nservices:\n  web:\n    image: nginx:latest\n    ports:\n      - \"8080:80\"\n    environment:\n      - GREETING=你好 世界\n"
	dto, raw, code := postStack(t, client, srv.URL, csrf, id, "/stacks/deploy",
		`{"name":"shop","compose":`+jsonString(body)+`}`)
	if code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", code, raw)
	}
	if !dto.OK || dto.Name != "shop" || dto.Action != "deploy" || dto.ServerID != id {
		t.Fatalf("响应契约不符: %+v body=%s", dto, raw)
	}

	wantDir := stacksBaseDir + "/shop"
	wantFile := wantDir + "/" + composeFileName
	if dialer.findCall("mkdir", "-p", wantDir) == nil {
		t.Fatalf("应先建受管目录 %s, calls=%v", wantDir, dialer.calls)
	}
	if got := dialer.stdin[wantFile]; got != strings.TrimSpace(body) {
		t.Fatalf("compose 正文应原样落到 %s, got %q", wantFile, got)
	}
	up := dialer.findCall("docker", "compose", "-p", "shop", "-f", wantFile, "up", "-d")
	if up == nil {
		t.Fatalf("应发 `docker compose -p shop -f %s up -d`, calls=%v", wantFile, dialer.calls)
	}
	// AC-SEC-02:整份正文(含镜像名、端口)都只能是文件内容,不能是 argv。
	for _, frag := range []string{"nginx:latest", "8080:80", "services:"} {
		if strings.Contains(dialer.argvBlob(), frag) {
			t.Fatalf("compose 正文片段 %q 泄漏进了命令参数(拼 shell 风险)", frag)
		}
	}

	// 审计只记项目名 + 动作 + 成败,正文绝不入审计(它会长期留在审计表里)。
	entries, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionStackOp})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries.Entries) != 1 {
		t.Fatalf("应有 1 条 stack_op 审计, got %d", len(entries.Entries))
	}
	e := entries.Entries[0]
	if e.Detail["name"] != "shop" || e.Detail["action"] != "deploy" || e.Detail["ok"] != true {
		t.Fatalf("审计 detail 不符: %+v", e.Detail)
	}
	if strings.Contains(fmtDetail(e), "nginx:latest") {
		t.Fatalf("compose 正文不该进审计: %s", fmtDetail(e))
	}
}

// TestStackDeployFallsBackToComposeV1 只有 v1 独立命令的机器:探测到 docker compose 失败就用
// docker-compose。认错 CLI 的现场是「命令不存在」,但报出来的却是 up 失败,排查方向整个偏掉。
func TestStackDeployFallsBackToComposeV1(t *testing.T) {
	dialer := &progDialer{answer: func(cmd []string) *target.ExecResult {
		switch {
		case cmdHas(cmd, "docker", "compose", "version"):
			return &target.ExecResult{ExitCode: 1, Stderr: "'docker' is not a docker command"}
		case cmdHas(cmd, "docker-compose", "version"):
			return &target.ExecResult{Stdout: "docker-compose version 1.29.2"}
		}
		return &target.ExecResult{}
	}}
	srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	dto, raw, _ := postStack(t, client, srv.URL, csrf, id, "/stacks/deploy",
		`{"name":"shop","compose":"services: {}"}`)
	if !dto.OK {
		t.Fatalf("v1 机器也该部署成功, got %+v body=%s", dto, raw)
	}
	if dialer.findCall("docker-compose", "-p", "shop") == nil {
		t.Fatalf("应退化成 docker-compose, calls=%v", dialer.calls)
	}
	if dialer.findCall("docker", "compose", "-p") != nil {
		t.Fatalf("v1 机器不该发 v2 插件命令, calls=%v", dialer.calls)
	}
}

// noCompose 是两种 CLI 都没装的机器:探测命令一律 127,其余(mkdir)照常成功。
// 前两步偏要成功,才盯得住最终报出来的是「没装 compose」而不是「建目录失败」。
func noCompose() *progDialer {
	return &progDialer{answer: func(cmd []string) *target.ExecResult {
		if cmdHas(cmd, "docker", "compose") || cmdHas(cmd, "docker-compose") {
			return &target.ExecResult{ExitCode: 127, Stderr: "command not found"}
		}
		return &target.ExecResult{}
	}}
}

// TestStackDeployWithoutComposeCLI 两种 CLI 都没有 → ok:false + 人读原因,且**不发** up。
// 文件已经铺上去了(前两步成功),所以这里还要确认失败原因说的是「没装 compose」而不是别的。
func TestStackDeployWithoutComposeCLI(t *testing.T) {
	dialer := noCompose()
	srv, client, csrf, rec := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	dto, raw, code := postStack(t, client, srv.URL, csrf, id, "/stacks/deploy",
		`{"name":"shop","compose":"services: {}"}`)
	if code != http.StatusOK {
		t.Fatalf("目标机缺 CLI 属业务失败,应 200 + ok:false, got %d body=%s", code, raw)
	}
	if dto.OK || !strings.Contains(dto.Error, "未检测到 docker compose") {
		t.Fatalf("失败原因要说清是没装 compose, got %+v", dto)
	}
	if dialer.findCall("docker", "compose", "-p") != nil || dialer.findCall("docker-compose", "-p") != nil {
		t.Fatalf("没探测到 CLI 就不该硬 up, calls=%v", dialer.calls)
	}
	entries, _ := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionStackOp})
	if len(entries.Entries) != 1 || entries.Entries[0].Detail["ok"] != false {
		t.Fatalf("失败也要留痕 ok:false, entries=%+v", entries.Entries)
	}
}

// TestStackDeployRejectsBeforeTouchingHost 入参非法一律 400,且**一条命令都不发**。
// 项目名会直接成为受管目录名与 -p 的值:放过一个 "shop/../x" 就是在机器上换个栈、
// 换个目录,旧栈还活着 —— 报错反而是最轻的后果。
func TestStackDeployRejectsBeforeTouchingHost(t *testing.T) {
	cases := []struct{ name, body, why string }{
		{"路径穿越", `{"name":"shop/../x","compose":"services: {}"}`, "斜杠能穿出受管目录"},
		{"前导连字符", `{"name":"-shop","compose":"services: {}"}`, "会被 compose 当 flag"},
		{"空名", `{"name":"","compose":"services: {}"}`, "栈没有身份"},
		{"名字超长", `{"name":"` + strings.Repeat("a", 129) + `","compose":"services: {}"}`, "超过 128"},
		{"正文留空", `{"name":"shop","compose":"   \n  "}`, "空文件 up 起来只会报解析错"},
		{"正文超限", `{"name":"shop","compose":` + jsonString(strings.Repeat("x", composeMaxBytes+1)) + `}`, "超过 512 KiB"},
	}
	for _, c := range cases {
		dialer := composeV2()
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		id := newServerAPI(t, client, srv.URL, csrf)

		e, code, raw := rejectStack(t, client, srv.URL, csrf, id, "/stacks/deploy", c.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s(%s):应 400, got %d body=%s", c.name, c.why, code, truncateLog(string(raw), 200))
		}
		if e.Error.Code != "invalid_stack" {
			t.Fatalf("%s:错误码应 invalid_stack, got %q", c.name, e.Error.Code)
		}
		if len(dialer.calls) != 0 {
			t.Fatalf("%s:拒绝路径不该碰目标机, calls=%v", c.name, dialer.calls)
		}
	}
}

// TestStackRoutesUnknownServer404 目标机不存在 → 404 server_not_found,一条命令都不发。
// 与「入参非法(400)」分开的意义:界面据此区分「这台机器被删了」和「我填错了」;
// 更要紧的是别把命令发去别处 —— 校验顺序里 svc.Get 在建目录/上传之前。
func TestStackRoutesUnknownServer404(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/stacks/deploy", `{"name":"shop","compose":"services: {}"}`},
		{"/stacks/save", `{"name":"shop","configFile":"/srv/shop/docker-compose.yml","compose":"services: {}"}`},
		{"/containers", `{"image":"nginx:latest"}`},
	} {
		dialer := composeV2()
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		e, code, raw := rejectStack(t, client, srv.URL, csrf, "00000000-0000-4000-8000-000000000000", tc.path, tc.body)
		if code != http.StatusNotFound {
			t.Fatalf("%s:目标机不存在应 404, got %d body=%s", tc.path, code, truncateLog(string(raw), 200))
		}
		if e.Error.Code != "server_not_found" {
			t.Fatalf("%s:错误码应 server_not_found, got %q", tc.path, e.Error.Code)
		}
		if len(dialer.calls) != 0 {
			t.Fatalf("%s:目标机都不存在还发了命令, calls=%v", tc.path, dialer.calls)
		}
	}
}

// --- ② compose 文件选择(在线编辑写回原文件) ---

// TestStackSaveWritesBackToSelectedFile 保存要写回详情给的那份**原文件**,而不是受管目录:
// 用户选的是 /srv/shop/docker-compose.yml,写到别处就是「点了保存,改了个没在用的文件」。
func TestStackSaveWritesBackToSelectedFile(t *testing.T) {
	dialer := composeV2()
	srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	const orig = "/srv/shop/docker-compose.yml"
	edited := "services:\n  web:\n    image: nginx:1.27\n"
	dto, raw, code := postStack(t, client, srv.URL, csrf, id, "/stacks/save",
		`{"name":"shop","configFile":"`+orig+`","compose":`+jsonString(edited)+`}`)
	if code != http.StatusOK || !dto.OK {
		t.Fatalf("保存重部署应 200 + ok, got %d body=%s", code, raw)
	}
	if got := dialer.stdin[orig]; got != strings.TrimSpace(edited) {
		t.Fatalf("应写回 %s, 实际写了 %v", orig, keysOfStdin(dialer.stdin))
	}
	if dialer.countCall("mkdir") != 0 {
		t.Fatalf("就地保存不该再建受管目录, calls=%v", dialer.calls)
	}
	if dialer.findCall("docker", "compose", "-p", "shop", "-f", orig, "up", "-d") == nil {
		t.Fatalf("up 要带上刚写回的那份文件, calls=%v", dialer.calls)
	}
	if strings.Contains(dialer.argvBlob(), "nginx:1.27") {
		t.Fatalf("正文不该出现在命令参数里: %v", dialer.calls)
	}
}

// TestStackSaveRejectsUnwritablePath 「可编辑」的判定只有一条:详情给的是单一绝对路径。
// 多文件(-f a.yml -f b.yml 叠出来的栈)、相对路径、带控制字符的路径都写不回原位,
// 这时候必须停住,而不是挑一个写过去(那等于悄悄改了一份别的栈的定义)。
func TestStackSaveRejectsUnwritablePath(t *testing.T) {
	cases := []struct{ name, configFile string }{
		{"多文件", "/srv/shop/docker-compose.yml,/srv/shop/override.yml"},
		{"相对路径", "docker-compose.yml"},
		{"空标签(v1 旧版常见)", ""},
		{"带换行", "/srv/shop/docker-compose.yml\n/etc/passwd"},
	}
	for _, c := range cases {
		dialer := composeV2()
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		id := newServerAPI(t, client, srv.URL, csrf)

		e, code, raw := rejectStack(t, client, srv.URL, csrf, id, "/stacks/save",
			`{"name":"shop","configFile":`+jsonString(c.configFile)+`,"compose":"services: {}"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("%s:应 400, got %d body=%s", c.name, code, truncateLog(string(raw), 200))
		}
		if e.Error.Code != "invalid_stack" {
			t.Fatalf("%s:错误码应 invalid_stack, got %q", c.name, e.Error.Code)
		}
		if len(dialer.calls) != 0 {
			t.Fatalf("%s:拒绝路径不该碰目标机, calls=%v", c.name, dialer.calls)
		}
	}
}

// --- ③ 详情怎么把「可选文件」暴露给界面 ---

// TestStackDetailReportsComposeSource 详情里的 composeSource/editable 是界面「粘贴 / 选文件」
// 的唯一依据。三级解析(标签 → 受管目录 → 有界扫描)各给一个用例:
// 判错的话,要么「明明能编辑却只读」,要么给了个写不回去的编辑器。
func TestStackDetailReportsComposeSource(t *testing.T) {
	const svcLine = "web\tshop-web-1\tnginx:latest\tUp 2 minutes\t0.0.0.0:8080->80/tcp\n"

	t.Run("标签给了单一绝对路径 → 可编辑", func(t *testing.T) {
		dialer := &progDialer{answer: func(cmd []string) *target.ExecResult {
			switch {
			case cmdHas(cmd, "docker", "ps", "-a", "--no-trunc"):
				return &target.ExecResult{Stdout: svcLine}
			case strings.Contains(joinCmd(cmd), "config_files"):
				return &target.ExecResult{Stdout: "/srv/shop/docker-compose.yml\n"}
			case cmdHas(cmd, "cat", "/srv/shop/docker-compose.yml"):
				return &target.ExecResult{Stdout: "services:\n  web:\n    image: nginx\n"}
			}
			return &target.ExecResult{ExitCode: 1}
		}}
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		id := newServerAPI(t, client, srv.URL, csrf)
		dto := getStackDetail(t, client, srv.URL, id, "shop")
		if dto.ComposeSource != "file" || !dto.Editable {
			t.Fatalf("标签路径可读应判 file + editable: %+v", dto)
		}
		if !strings.Contains(dto.Compose, "image: nginx") {
			t.Fatalf("详情要带回正文供编辑, got %q", dto.Compose)
		}
		if dto.ConfigFiles != "/srv/shop/docker-compose.yml" {
			t.Fatalf("应报标签里那份原路径, got %q", dto.ConfigFiles)
		}
	})

	t.Run("标签为空 → 退到受管目录(Pipewright 自己部署的栈)", func(t *testing.T) {
		dialer := &progDialer{answer: func(cmd []string) *target.ExecResult {
			switch {
			case cmdHas(cmd, "docker", "ps", "-a", "--no-trunc"):
				return &target.ExecResult{Stdout: svcLine}
			case strings.Contains(joinCmd(cmd), "config_files"):
				return &target.ExecResult{Stdout: "\n"}
			case cmdHas(cmd, "cat", stacksBaseDir+"/shop/"+composeFileName):
				return &target.ExecResult{Stdout: "services:\n  web:\n    image: nginx\n"}
			}
			return &target.ExecResult{ExitCode: 1}
		}}
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		id := newServerAPI(t, client, srv.URL, csrf)
		dto := getStackDetail(t, client, srv.URL, id, "shop")
		if dto.ComposeSource != "file" || dto.ConfigFiles != stacksBaseDir+"/shop/"+composeFileName {
			t.Fatalf("受管目录那份应被认出来, got %+v", dto)
		}
	})

	t.Run("三处都找不到 → none 且不可编辑", func(t *testing.T) {
		dialer := &progDialer{answer: func(cmd []string) *target.ExecResult {
			if cmdHas(cmd, "docker", "ps", "-a", "--no-trunc") {
				return &target.ExecResult{Stdout: svcLine}
			}
			return &target.ExecResult{ExitCode: 1}
		}}
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		id := newServerAPI(t, client, srv.URL, csrf)
		dto := getStackDetail(t, client, srv.URL, id, "shop")
		if dto.ComposeSource != "none" || dto.Editable {
			t.Fatalf("读不到原文就该是 none + 只读, got %+v", dto)
		}
		if dto.Total != 1 || dto.Services[0].Name != "shop-web-1" {
			t.Fatalf("容器列表与 compose 是否可读是两回事,不该一起空掉: %+v", dto)
		}
	})
}

// --- ④ 单容器创建 ---

// TestCreateContainerRouteSendsDockerRunArray 单容器方式:表单字段要按 docker run 的参数位置
// 落下去(每个值独立成 argv 元素),且成功回新容器 ID、失败回人读原因。
func TestCreateContainerRouteSendsDockerRunArray(t *testing.T) {
	dialer := &progDialer{answer: func(cmd []string) *target.ExecResult {
		return &target.ExecResult{Stdout: " 3f9a1c7b8d4e5f60718293a4b5c6d7e8\n"}
	}}
	srv, client, csrf, rec := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	body := `{"image":"nginx:latest","name":"shop-web","restart":"unless-stopped",` +
		`"ports":["8080:80","127.0.0.1:9090:90/tcp"],"env":["TZ=Asia/Shanghai"],` +
		`"volumes":["/data/web:/usr/share/nginx/html:ro"],"command":"nginx -t"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/containers", csrf, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", resp.StatusCode, raw)
	}
	var dto createContainerDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if !dto.OK || dto.ContainerID != "3f9a1c7b8d4e5f60718293a4b5c6d7e8" {
		t.Fatalf("ok/containerId 契约不符: %+v", dto)
	}
	want := []string{"docker", "run", "-d", "--name", "shop-web", "--restart", "unless-stopped",
		"-p", "8080:80", "-p", "127.0.0.1:9090:90/tcp", "-e", "TZ=Asia/Shanghai",
		"-v", "/data/web:/usr/share/nginx/html:ro", "nginx:latest", "nginx", "-t"}
	got := dialer.findCall("docker", "run", "-d")
	if got == nil || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("docker run 参数序列不符\n want %v\n  got %v", want, got)
	}

	// 审计只带 image/name/ok:env 值(常是密码/密钥)、卷路径都不该进审计表。
	entries, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionContainerCreate})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if len(entries.Entries) != 1 {
		t.Fatalf("应有 1 条 container_create 审计, got %d", len(entries.Entries))
	}
	e := entries.Entries[0]
	if e.Detail["image"] != "nginx:latest" || e.Detail["name"] != "shop-web" || e.Detail["ok"] != true {
		t.Fatalf("审计 detail 不符: %+v", e.Detail)
	}
	if s := fmtDetail(e); strings.Contains(s, "TZ=Asia/Shanghai") || strings.Contains(s, "/data/web") {
		t.Fatalf("env 值 / 卷路径不该进审计: %s", s)
	}
}

// TestCreateContainerRouteRejectsInjection 非法创建参数一律 400 且一条命令都不发。
// 这里是「白名单 + array 化」两层的入口闸门:validateCreateSpec 的单测证明了它认得出坏值,
// 这条证明路由真的在它拦下时不碰目标机。
func TestCreateContainerRouteRejectsInjection(t *testing.T) {
	cases := []struct{ name, body string }{
		{"缺镜像", `{"name":"x"}`},
		{"端口里塞 shell", `{"image":"nginx","ports":["80; shutdown -h now"]}`},
		{"端口号越界", `{"image":"nginx","ports":["80:99999"]}`},
		{"env 无等号", `{"image":"nginx","env":["TZ"]}`},
		{"env 名非法", `{"image":"nginx","env":["1TZ=x"]}`},
		{"卷模式非 ro/rw", `{"image":"nginx","volumes":["/a:/b:rwX"]}`},
		{"容器名以连字符开头", `{"image":"nginx","name":"-x"}`},
		{"镜像名带 shell 元字符", `{"image":"nginx$(id)"}`},
		{"命令带命令替换", `{"image":"nginx","command":"echo $(id)"}`},
		{"restart 白名单外", `{"image":"nginx","restart":"nope"}`},
	}
	for _, c := range cases {
		dialer := composeV2()
		srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
		id := newServerAPI(t, client, srv.URL, csrf)

		resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/containers", csrf, c.body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s:应 400, got %d body=%s", c.name, resp.StatusCode, truncateLog(string(raw), 200))
		}
		var e errBody
		_ = json.Unmarshal(raw, &e)
		if e.Error.Code != "invalid_create_spec" {
			t.Fatalf("%s:错误码应 invalid_create_spec, got %q", c.name, e.Error.Code)
		}
		if len(dialer.calls) != 0 {
			t.Fatalf("%s:拒绝路径不该碰目标机, calls=%v", c.name, dialer.calls)
		}
	}
}

// TestCreateContainerRouteReportsRunFailure docker run 非零退出是常态(端口被占、镜像拉不动):
// 要 200 + ok:false + stderr 原文给人看,不是 500 空壳 —— 500 会把「8080 已被占用」这种
// 一眼能懂的原因吞掉。
func TestCreateContainerRouteReportsRunFailure(t *testing.T) {
	dialer := &progDialer{answer: func(cmd []string) *target.ExecResult {
		return &target.ExecResult{ExitCode: 125, Stderr: "docker: Error response from daemon: port is already allocated."}
	}}
	srv, client, csrf, _ := setupServiceOpsAPI(t, dialer)
	id := newServerAPI(t, client, srv.URL, csrf)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/containers", csrf,
		`{"image":"nginx:latest","name":"shop-web","ports":["8080:80"]}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("命令失败应 200(不 500), got %d body=%s", resp.StatusCode, raw)
	}
	var dto createContainerDTO
	_ = json.Unmarshal(raw, &dto)
	if dto.OK || dto.ContainerID != "" {
		t.Fatalf("起容器失败不该报成功: %+v", dto)
	}
	if !strings.Contains(dto.Error, "port is already allocated") {
		t.Fatalf("要原样带出目标机的原因, got %q", dto.Error)
	}
}

// --- 夹具小工具 ---

// jsonString 把任意字符串编成 JSON 字面量(省去正文里的引号/换行手工转义)。
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func joinCmd(cmd []string) string { return strings.Join(cmd, " ") }

func fmtDetail(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func keysOfStdin(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func getStackDetail(t *testing.T, client *http.Client, srvURL, id, name string) stackDetailDTO {
	t.Helper()
	resp := doJSON(t, client, http.MethodGet, srvURL+"/api/servers/"+id+"/stacks/"+name, "", "")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("详情应 200, got %d body=%s", resp.StatusCode, truncateLog(string(raw), 300))
	}
	var dto stackDetailDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode detail: %v body=%s", err, truncateLog(string(raw), 300))
	}
	return dto
}
