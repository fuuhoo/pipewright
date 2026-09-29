package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/target"
)

// --- 纯解析单元测试 ---

func TestParseContainers_WithStateField(t *testing.T) {
	// Docker 20.10+ 的 `docker ps --format {{json .}}`:逐行 JSON,含 State 字段。
	out := `{"ID":"abc123def456","Names":"web","Image":"nginx:latest","State":"running","Status":"Up 2 hours","Ports":"0.0.0.0:80->80/tcp","CreatedAt":"2026-06-01 10:00:00 +0800 CST"}
{"ID":"def789","Names":"/db","Image":"mysql:8","State":"exited","Status":"Exited (0) 3 days ago","Ports":"","CreatedAt":"2026-05-28 09:00:00 +0800 CST"}
`
	got := parseContainers(out)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].ID != "abc123def456" || got[0].Names != "web" || got[0].State != "running" {
		t.Fatalf("c0 wrong: %+v", got[0])
	}
	if got[0].Image != "nginx:latest" || got[0].Ports != "0.0.0.0:80->80/tcp" {
		t.Fatalf("c0 image/ports wrong: %+v", got[0])
	}
	// 前导 `/` 应去掉。
	if got[1].Names != "db" || got[1].State != "exited" {
		t.Fatalf("c1 wrong: %+v", got[1])
	}
}

func TestParseContainers_OldDockerNoState(t *testing.T) {
	// 旧 Docker 无 State 字段 → 据 Status 前缀推断。
	out := `{"ID":"a1","Names":"running-one","Image":"img","Status":"Up 5 minutes","Ports":"","CreatedAt":""}
{"ID":"a2","Names":"paused-one","Image":"img","Status":"Up 5 minutes (Paused)","Ports":"","CreatedAt":""}
{"ID":"a3","Names":"stopped-one","Image":"img","Status":"Exited (137) 1 hour ago","Ports":"","CreatedAt":""}
{"ID":"a4","Names":"created-one","Image":"img","Status":"Created","Ports":"","CreatedAt":""}`
	got := parseContainers(out)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	want := []string{"running", "paused", "exited", "created"}
	for i, w := range want {
		if got[i].State != w {
			t.Fatalf("c%d state = %q, want %q", i, got[i].State, w)
		}
	}
}

func TestParseContainers_SkipsGarbageLines(t *testing.T) {
	out := "not json at all\n{\"ID\":\"ok\",\"Names\":\"good\",\"Status\":\"Up 1 second\"}\n   \n"
	got := parseContainers(out)
	if len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("want 1 valid container, got %+v", got)
	}
}

func TestDeriveState(t *testing.T) {
	cases := map[string]string{
		"Up 2 hours":            "running",
		"Up 2 hours (Paused)":   "paused",
		"Exited (0) 3 days ago": "exited",
		"Created":               "created",
		"Restarting (1) 2s ago": "restarting",
		"Dead":                  "dead",
		"some weird status":     "unknown",
	}
	for status, want := range cases {
		if got := deriveState(status); got != want {
			t.Fatalf("deriveState(%q) = %q, want %q", status, got, want)
		}
	}
}

// --- 集成测试:经真 HTTP 端点 + 假 dialer ---

// dockerLikeDialer 模拟一台装了 docker 的服务器:`docker ps` 回显两个容器(一跑一停)。
func dockerLikeDialer() cmdDialer {
	return cmdDialer{fn: func(cmd []string) (*target.ExecResult, error) {
		if len(cmd) >= 2 && cmd[0] == "docker" && cmd[1] == "ps" {
			out := `{"ID":"c0full","Names":"web","Image":"nginx:latest","State":"running","Status":"Up 2 hours","Ports":"0.0.0.0:80->80/tcp","CreatedAt":"2026-06-01 10:00:00 +0800 CST"}
{"ID":"c1full","Names":"db","Image":"mysql:8","State":"exited","Status":"Exited (0) 3 days ago","Ports":"","CreatedAt":"2026-05-28 09:00:00 +0800 CST"}
`
			return &target.ExecResult{Stdout: out, ExitCode: 0}, nil
		}
		return &target.ExecResult{Stderr: "command not found", ExitCode: 127}, nil
	}}
}

// noDockerDialer 模拟连得上但没装 docker 的服务器:`docker ps` 命令找不到(127)。
func noDockerDialer() cmdDialer {
	return cmdDialer{fn: func(cmd []string) (*target.ExecResult, error) {
		return &target.ExecResult{Stderr: "docker: command not found", ExitCode: 127}, nil
	}}
}

func TestServerContainers_DockerPresent(t *testing.T) {
	srv, client, csrf := setupServerAPI(t, dockerLikeDialer())
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	resp, _ := client.Get(srv.URL + "/api/servers/" + id + "/containers")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var out serverContainersDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if !out.Reachable || out.Runtime != "docker" {
		t.Fatalf("want reachable+docker: %+v", out)
	}
	if out.Total != 2 || out.Running != 1 {
		t.Fatalf("counts wrong: total=%d running=%d", out.Total, out.Running)
	}
	if out.Containers[0].Names != "web" || out.Containers[0].State != "running" {
		t.Fatalf("c0 wrong: %+v", out.Containers[0])
	}
}

func TestServerContainers_NoRuntime(t *testing.T) {
	srv, client, csrf := setupServerAPI(t, noDockerDialer())
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	resp, _ := client.Get(srv.URL + "/api/servers/" + id + "/containers")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var out serverContainersDTO
	_ = json.Unmarshal(raw, &out)
	// 连得上(命令投递成功)但无运行时:reachable true、runtime 空、error 人读、容器空。
	if !out.Reachable {
		t.Fatalf("want reachable true (connection ok): %+v", out)
	}
	if out.Runtime != "" || out.Error == "" {
		t.Fatalf("want empty runtime + human error: %+v", out)
	}
	if len(out.Containers) != 0 {
		t.Fatalf("want no containers: %+v", out.Containers)
	}
}

func TestAllServerContainers_BatchAggregate(t *testing.T) {
	srv, client, csrf := setupServerAPI(t, dockerLikeDialer())
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	_ = createServerAPI(t, client, srv.URL, csrf, credID)
	_ = createServerAPI(t, client, srv.URL, csrf, credID)

	resp, _ := client.Get(srv.URL + "/api/servers/containers")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var body struct {
		Items []serverContainersDTO `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(body.Items) != 2 {
		t.Fatalf("want 2 servers in aggregate, got %d", len(body.Items))
	}
	for _, it := range body.Items {
		if it.Total != 2 || it.Running != 1 {
			t.Fatalf("server %s counts wrong: %+v", it.ServerID, it)
		}
	}
}

// slowDockerDialer 是一台快(即时回显两个容器)、一台慢(SSH 挂到判不可达 —— 死机器的真实形态)
// 的拨号器,用来量容器聚合端点「等不等最慢那台」:默认 JSON 必须等齐,?stream=1 不该等。
type slowDockerDialer struct {
	slowAddr string
	delay    time.Duration
}

func (d slowDockerDialer) Run(_ context.Context, addr string, _ target.SSHConfig, cmd []string) (*target.ExecResult, error) {
	if addr == d.slowAddr && len(cmd) >= 2 && cmd[0] == "docker" && cmd[1] == "ps" {
		time.Sleep(d.delay)
		return nil, target.ErrUnreachable
	}
	return dockerLikeDialer().fn(cmd)
}

func (d slowDockerDialer) RunStream(_ context.Context, _ string, _ target.SSHConfig, _ []string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (d slowDockerDialer) RunInteractive(_ context.Context, _ string, _ target.SSHConfig, _ []string) (target.Session, error) {
	return nil, nil
}

func (d slowDockerDialer) RunWithStdin(ctx context.Context, addr string, cfg target.SSHConfig, cmd []string, _ io.Reader) (*target.ExecResult, error) {
	return d.Run(ctx, addr, cfg, cmd)
}

func TestAllServerContainersStreamIsProgressive(t *testing.T) {
	const slowDelay = 1200 * time.Millisecond
	d := slowDockerDialer{slowAddr: "10.255.255.2:22", delay: slowDelay}
	srv, client, csrf := setupServerAPI(t, d)
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	fastID := createServerAtAPI(t, client, srv.URL, csrf, credID, "fast", "127.0.0.1")
	slowID := createServerAtAPI(t, client, srv.URL, csrf, credID, "slow", "10.255.255.2")

	resp, err := client.Get(srv.URL + "/api/servers/containers?stream=1")
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("content-type = %q, want application/x-ndjson", ct)
	}

	type frame struct {
		id      string
		reach   bool
		total   int
		arrived time.Duration
	}
	start := time.Now()
	var frames []frame
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var dto serverContainersDTO
		if err := json.Unmarshal(sc.Bytes(), &dto); err != nil {
			t.Fatalf("unmarshal line %q: %v", sc.Text(), err)
		}
		frames = append(frames, frame{dto.ServerID, dto.Reachable, dto.Total, time.Since(start)})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d: %+v", len(frames), frames)
	}
	// 核心断言:快的那台在慢台还没判死之前就已经下发 —— 首屏不必等齐。
	if frames[0].id != fastID || !frames[0].reach || frames[0].total != 2 {
		t.Fatalf("first frame should be the fast reachable host with 2 containers, got %+v", frames[0])
	}
	if frames[0].arrived >= slowDelay {
		t.Fatalf("fast frame arrived at %v, want < %v (stream waited for the slow host)", frames[0].arrived, slowDelay)
	}
	if frames[1].id != slowID || frames[1].reach {
		t.Fatalf("second frame should be the slow unreachable host, got %+v", frames[1])
	}
}

// 默认 JSON 形态按「名单顺序」返回(= /api/servers 那一份),不是采集完成顺序:前端每轮刷新要
// 把卡片排在同一位子上,顺序跟着谁先采完走就会每轮乱跳。名单本身是 created_at DESC, id,
// 同秒登记的两台靠 id 定序 —— 所以断言比对名单,不硬写登记先后。
func TestAllServerContainersBatchFollowsListOrder(t *testing.T) {
	d := slowDockerDialer{slowAddr: "10.255.255.2:22", delay: 300 * time.Millisecond}
	srv, client, csrf := setupServerAPI(t, d)
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	slowID := createServerAtAPI(t, client, srv.URL, csrf, credID, "slow-first", "10.255.255.2")
	fastID := createServerAtAPI(t, client, srv.URL, csrf, credID, "fast", "127.0.0.1")

	lresp, err := client.Get(srv.URL + "/api/servers")
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	lraw, _ := io.ReadAll(lresp.Body)
	lresp.Body.Close()
	if lresp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d: %s", lresp.StatusCode, lraw)
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(lraw, &list); err != nil {
		t.Fatalf("unmarshal list: %v: %s", err, lraw)
	}
	if len(list.Items) != 2 || list.Items[0].ID != slowID && list.Items[0].ID != fastID {
		t.Fatalf("名单应是刚登记的两台, got %s", lraw)
	}
	want := []string{list.Items[0].ID, list.Items[1].ID}

	resp, err := client.Get(srv.URL + "/api/servers/containers")
	if err != nil {
		t.Fatalf("batch request: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var body struct {
		Items []serverContainersDTO `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(body.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(body.Items))
	}
	for i := range want {
		if body.Items[i].ServerID != want[i] {
			t.Fatalf("items 顺序 = [%s, %s], want 名单顺序 [%s, %s]",
				body.Items[0].ServerID, body.Items[1].ServerID, want[0], want[1])
		}
	}
	// 慢机器仍要如实报不可达,而不是被快机器「带过去」。
	slow := body.Items[0]
	if slow.ServerID == fastID {
		slow = body.Items[1]
	}
	if slow.Reachable || slow.Error == "" {
		t.Fatalf("慢机器应报 unreachable + 人读 error: %+v", slow)
	}
}

// --- 生命周期 action 白名单扩展(docker pause/unpause/kill/rm) ---
func TestValidateServiceParams_DockerLifecycleWidened(t *testing.T) {
	ok := []string{"restart", "stop", "start", "pause", "unpause", "kill", "rm"}
	for _, a := range ok {
		if err := validateServiceParams("docker", "myapp", a); err != nil {
			t.Fatalf("docker action %q should be valid: %v", a, err)
		}
	}
	// systemd 不放宽:pause/kill/rm 仍非法。
	for _, a := range []string{"pause", "unpause", "kill", "rm"} {
		if err := validateServiceParams("systemd", "nginx.service", a); err == nil {
			t.Fatalf("systemd action %q should be rejected", a)
		}
	}
	// flag 注入仍被正则挡住。
	if err := validateServiceParams("docker", "-rf", "rm"); err == nil {
		t.Fatalf("container name starting with - must be rejected")
	}
}

// buildServiceCmd 对新增 docker action 仍是 `docker <action> <name>`。
func TestBuildServiceCmd_DockerNewActions(t *testing.T) {
	for _, a := range []string{"pause", "unpause", "kill", "rm"} {
		got := buildServiceCmd("docker", a, "myapp")
		want := []string{"docker", a, "myapp"}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("buildServiceCmd docker %s = %v, want %v", a, got, want)
		}
	}
}
