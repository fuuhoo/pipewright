package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/mask"
	"github.com/huangchengsir/pipewright/internal/target"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// server_fs_test.go —— 远程文件面板端点的契约测试(内存工作区 + 真 HTTP 栈)。
//
// 钉的是**接口面**的事:状态码到领域错误的映射(404/403/409/501)、路径与文件名的
// 消毒(上传落点不许被客户端塞路径)、审计只记路径不记正文、GET 也按 ActOperate 把关。
// 底层 SFTP/exec 的真行为在 internal/target 的真机端到端里验,两层各管一段。

// memWorkspace 是内存版 target.Workspace;`/root/` 前缀一律回权限错误,
// 用来钉住 403 映射(真机上撞这条的路径太多,拿它做样例最省事)。
type memWorkspace struct {
	files map[string][]byte
	dirs  map[string]bool
	// modes 是被 Chmod 改过的路径;没记的一律按 0644 呈现(与真机默认档一致)。
	modes map[string]uint32
	// links 是符号链接路径(列目录时标 isLink,读正文一律「找不到」——打包该跳过它)。
	links map[string]bool
	// closes 数的是这条工作区被放掉的次数(批次「整批传完就还连接」由它作证)。
	closes int
}

func newMemWorkspace() *memWorkspace {
	return &memWorkspace{
		modes: map[string]uint32{},
		links: map[string]bool{},
		files: map[string][]byte{
			"/app/a.txt":     []byte("hello"),
			"/app/b.log":     []byte("line1\nline2\n"),
			"/app/blob.bin":  []byte{0x00, 0x01, 0x02, 'x'},
			"/root/secret":   []byte("nope"),
			"/home/deploy/x": []byte("x"),
		},
		dirs: map[string]bool{"/": true, "/app": true, "/root": true, "/home": true, "/home/deploy": true, "/tmp": true},
	}
}

func denied(p string) bool { return strings.HasPrefix(p, "/root/") }

func (m *memWorkspace) Realpath(_ context.Context, p string) (string, error) {
	if p == "" {
		return "/home/deploy", nil
	}
	cleaned := path.Clean(p)
	if m.dirs[cleaned] || m.has(cleaned) {
		return cleaned, nil
	}
	return "", fmt.Errorf("%w: %s", target.ErrRemoteNotFound, cleaned)
}

func (m *memWorkspace) has(p string) bool {
	_, ok := m.files[p]
	return ok
}

func (m *memWorkspace) Stat(_ context.Context, p string) (target.FileStat, error) {
	p = path.Clean(p)
	if denied(p) {
		return target.FileStat{}, fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	if isDir := m.dirs[p]; isDir {
		return target.FileStat{Name: path.Base(p), Path: p, IsDir: true, Mode: 0o755, MtimeUnix: 1700000000}, nil
	}
	data, ok := m.files[p]
	if !ok {
		return target.FileStat{}, fmt.Errorf("%w: %s", target.ErrRemoteNotFound, p)
	}
	mode := uint32(0o644)
	if m, ok := m.modes[p]; ok {
		mode = m
	}
	return target.FileStat{Name: path.Base(p), Path: p, Size: int64(len(data)), Mode: mode, MtimeUnix: 1700000001}, nil
}

func (m *memWorkspace) ReadDir(_ context.Context, dir string) ([]target.FileStat, error) {
	dir = path.Clean(dir)
	if denied(dir) {
		return nil, fmt.Errorf("%w: %s", target.ErrRemotePermission, dir)
	}
	if !m.dirs[dir] {
		return nil, fmt.Errorf("%w: %s", target.ErrRemoteNotFound, dir)
	}
	var out []target.FileStat
	seen := map[string]bool{}
	for f, data := range m.files {
		if path.Dir(f) != dir {
			continue
		}
		out = append(out, target.FileStat{Name: path.Base(f), Path: f, Size: int64(len(data)), Mode: 0o644, MtimeUnix: 1700000002})
		seen[path.Base(f)] = true
	}
	for d := range m.dirs {
		if path.Dir(d) == dir && d != "/" && !seen[path.Base(d)] {
			out = append(out, target.FileStat{Name: path.Base(d), Path: d, IsDir: true, Mode: 0o755})
		}
	}
	for l := range m.links {
		if path.Dir(l) == dir {
			out = append(out, target.FileStat{Name: path.Base(l), Path: l, IsLink: true, LinkTarget: "/elsewhere", Mode: 0o777})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memWorkspace) ReadFile(_ context.Context, p string, limit int64) ([]byte, bool, error) {
	p = path.Clean(p)
	if denied(p) {
		return nil, false, fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	data, ok := m.files[p]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", target.ErrRemoteNotFound, p)
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return append([]byte(nil), data...), false, nil
}

func (m *memWorkspace) OpenRead(_ context.Context, p string) (io.ReadCloser, error) {
	p = path.Clean(p)
	if denied(p) {
		return nil, fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	// 符号链接读不到内容:打包那条路本该在列目录时就跳过它,真来开了就当「不存在」,
	// 于是「链接没进包」这件事会以最显眼的方式失败,而不是悄悄塞进去一个空条目。
	if m.links[p] {
		return nil, fmt.Errorf("%w: %s", target.ErrRemoteNotFound, p)
	}
	data, ok := m.files[p]
	if !ok {
		return nil, fmt.Errorf("%w: %s", target.ErrRemoteNotFound, p)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memWorkspace) WriteFile(_ context.Context, p string, r io.Reader) error {
	p = path.Clean(p)
	if denied(p) {
		return fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	if !m.dirs[path.Dir(p)] {
		return fmt.Errorf("%w: %s", target.ErrRemoteNotFound, path.Dir(p))
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.files[p] = data
	return nil
}

// AppendChunk 按偏移覆盖(与 sftp 的 WriteAt 同义:同偏移重发是幂等的,不是接在尾巴上)。
func (m *memWorkspace) AppendChunk(_ context.Context, p string, off int64, r io.Reader) (int64, error) {
	p = path.Clean(p)
	if denied(p) {
		return off, fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	if !m.dirs[path.Dir(p)] {
		return off, fmt.Errorf("%w: %s", target.ErrRemoteNotFound, path.Dir(p))
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return off, err
	}
	cur := m.files[p]
	if need := int(off) + len(data); need > len(cur) {
		cur = append(cur, make([]byte, need-len(cur))...)
	}
	copy(cur[off:], data)
	m.files[p] = cur
	return int64(off) + int64(len(data)), nil
}

func (m *memWorkspace) Chmod(_ context.Context, p string, mode uint32) error {
	p = path.Clean(p)
	if denied(p) {
		return fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	if _, ok := m.files[p]; !ok {
		if !m.dirs[p] {
			return fmt.Errorf("%w: %s", target.ErrRemoteNotFound, p)
		}
	}
	m.modes[p] = mode & 0o777
	return nil
}

func (m *memWorkspace) Mkdir(_ context.Context, p string) error {
	p = path.Clean(p)
	if denied(p) {
		return fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	if _, ok := m.files[p]; ok {
		return fmt.Errorf("%w: %s", target.ErrRemoteNotDirectory, p)
	}
	m.dirs[p] = true
	return nil
}

func (m *memWorkspace) Remove(_ context.Context, p string) error {
	p = path.Clean(p)
	if denied(p) {
		return fmt.Errorf("%w: %s", target.ErrRemotePermission, p)
	}
	if m.dirs[p] {
		for f := range m.files {
			if path.Dir(f) == p {
				return fmt.Errorf("%w: %s", target.ErrRemoteNotEmpty, p)
			}
		}
		delete(m.dirs, p)
		return nil
	}
	if _, ok := m.files[p]; !ok {
		return fmt.Errorf("%w: %s", target.ErrRemoteNotFound, p)
	}
	delete(m.files, p)
	return nil
}

func (m *memWorkspace) Rename(_ context.Context, from, to string) error {
	from, to = path.Clean(from), path.Clean(to)
	if denied(from) || denied(to) {
		return fmt.Errorf("%w: %s", target.ErrRemotePermission, to)
	}
	data, ok := m.files[from]
	if !ok {
		return fmt.Errorf("%w: %s", target.ErrRemoteNotFound, from)
	}
	delete(m.files, from)
	m.files[to] = data
	if mode, has := m.modes[from]; has {
		delete(m.modes, from)
		m.modes[to] = mode
	}
	return nil
}

func (m *memWorkspace) Backend() string { return "sftp" }

func (m *memWorkspace) Close() error {
	m.closes++
	return nil
}

// fakeFSDialer 在假 SSH 拨号器上补 fsDialer 能力(OpenFS 返回内存工作区)。
type fakeFSDialer struct {
	fakeInteractiveDialer
	ws   target.Workspace
	open int
}

func (d *fakeFSDialer) OpenFS(_ context.Context, _ string, _ target.SSHConfig) (target.Workspace, error) {
	d.open++
	return d.ws, nil
}

// fsFixture 是一套带远程文件能力的 HTTP 栈。带上假拨号器是为了数「拨了几次连接」——
// 上传批次复用连接这条承诺,只有计数器能证。
type fsFixture struct {
	srv    *httptest.Server
	client *http.Client
	csrf   string
	id     string
	ws     *memWorkspace
	dialer *fakeFSDialer
	rec    audit.Recorder
}

func setupFSAPIFull(t *testing.T) fsFixture {
	t.Helper()
	st := testStoreAuth(t)
	asvc := auth.NewService(st.DB, nil, nil)
	if err := asvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	rec := audit.New(st.DB, mask.NewMasker(), nil)
	ws := newMemWorkspace()
	dialer := &fakeFSDialer{ws: ws}
	tsvc := target.New(st.DB, v, dialer)
	srv := httptest.NewServer(New(testWebFSAuth(), asvc, WithVault(v), WithServers(tsvc), WithAudit(rec)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	id := newServerAPI(t, client, srv.URL, csrf)
	return fsFixture{srv: srv, client: client, csrf: csrf, id: id, ws: ws, dialer: dialer, rec: rec}
}

// setupFSAPI 是旧签名形状(多数用例不关心拨号次数)。
func setupFSAPI(t *testing.T) (*httptest.Server, *http.Client, string, string, *memWorkspace, audit.Recorder) {
	t.Helper()
	f := setupFSAPIFull(t)
	return f.srv, f.client, f.csrf, f.id, f.ws, f.rec
}

func decodeDTO(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解码响应失败: %v(%s)", err, raw)
	}
	return out
}

func TestFSListDirAndFile(t *testing.T) {
	srv, client, csrf, id, _, _ := setupFSAPI(t)

	status, dto, raw := getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs?path=/app", csrf)
	if status != http.StatusOK {
		t.Fatalf("列目录应 200,得 %d(%s)", status, raw)
	}
	if dto["isDir"] != true || dto["path"] != "/app" || dto["backend"] != "sftp" {
		t.Fatalf("列表头 = %v", dto)
	}
	entries, _ := dto["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d, want 3(%v)", len(entries), entries)
	}
	// 名升序 + 目录里不带子目录前缀。
	first, _ := entries[0].(map[string]any)
	if first["name"] != "a.txt" {
		t.Errorf("首条 = %v, want a.txt(升序)", first)
	}

	status, dto, raw = getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs?path=/app/a.txt", csrf)
	if status != http.StatusOK || dto["isDir"] != false {
		t.Fatalf("单文件应 200 + isDir=false,得 %d %s", status, raw)
	}
	entry, _ := dto["entry"].(map[string]any)
	if entry == nil || entry["size"] != float64(5) {
		t.Errorf("entry = %v, want size=5", entry)
	}

	// 空 path = 该会话的家目录(与 SFTP 的 RealPath(".") 同义)。
	status, dto, raw = getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs", csrf)
	if status != http.StatusOK || dto["path"] != "/home/deploy" {
		t.Errorf("空 path 应落家目录,得 %d %s", status, raw)
	}
}

// errCode 取错误响应里的人读码(全站错误都包在 {"error":{"code","message"}} 里)。
func errCode(dto map[string]any) string {
	e, _ := dto["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestFSContentTextBinaryAndErrors(t *testing.T) {
	srv, client, csrf, id, _, _ := setupFSAPI(t)

	_, dto, raw := getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs/content?path=/app/b.log", csrf)
	if dto["content"] != "line1\nline2\n" || dto["binary"] != false {
		t.Fatalf("正文 = %s", raw)
	}
	_, dto, raw = getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs/content?path=/app/blob.bin", csrf)
	if dto["binary"] != true || dto["content"] != "" {
		t.Fatalf("二进制应 binary=true 且不带正文,得 %s", raw)
	}

	// 不存在 → 404(界面按「文件不见了」处理,不是 500)。
	status, dto, _ := getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs/content?path=/app/missing", csrf)
	if status != http.StatusNotFound || errCode(dto) != "remote_not_found" {
		t.Errorf("不存在应 404 remote_not_found,得 %d %v", status, dto)
	}
	// 权限不足 → 403。
	status, dto, _ = getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs/content?path=/root/secret", csrf)
	if status != http.StatusForbidden || errCode(dto) != "remote_permission_denied" {
		t.Errorf("权限应 403,得 %d %v", status, dto)
	}
	// path 缺失 → 400(读正文没有「家目录」这种合理默认)。
	status, _, _ = getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs/content", csrf)
	if status != http.StatusBadRequest {
		t.Errorf("缺 path 应 400,得 %d", status)
	}
}

func TestFSWriteSavesContentAndAuditsWithoutBody(t *testing.T) {
	srv, client, csrf, id, ws, rec := setupFSAPI(t)

	const secretBody = "db_password=SECRETPHRASE-do-not-log\n"
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/fs/content", csrf,
		`{"path":"/app/new.conf","content":"`+strings.ReplaceAll(secretBody, "\n", `\n`)+`"}`)
	defer resp.Body.Close()
	dto := decodeDTO(t, resp)
	if resp.StatusCode != http.StatusOK || dto["ok"] != true {
		t.Fatalf("保存应 200 ok,得 %d %v", resp.StatusCode, dto)
	}
	if got := string(ws.files["/app/new.conf"]); got != secretBody {
		t.Fatalf("远端正文 = %q, want %q", got, secretBody)
	}

	entries := listAudit(t, rec, audit.ActionServerFS)
	if len(entries) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(entries))
	}
	d := entries[0].Detail
	if d["op"] != "save" || d["path"] != "/app/new.conf" || d["ok"] != true {
		t.Fatalf("审计 detail = %v", d)
	}
	if auditInt(d["bytes"]) != int64(len(secretBody)) {
		t.Errorf("审计 bytes = %v, want %d", d["bytes"], len(secretBody))
	}
	// 要害:审计只许记路径与体积。正文里那串口令要是进了审计库,等于把秘密抄进第二本账。
	if strings.Contains(fmt.Sprint(d), "SECRETPHRASE") {
		t.Fatalf("审计 detail 泄漏了正文: %v", d)
	}
}

func TestFSDownloadServesBytesAndHeaders(t *testing.T) {
	srv, client, _, id, _, rec := setupFSAPI(t)

	resp := download(t, client, srv.URL+"/api/servers/"+id+"/fs/download?path=/app/a.txt")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("下载 = %d %q", resp.StatusCode, body)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="a.txt"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "5" {
		t.Errorf("Content-Length = %q, want 5", cl)
	}
	// 下载留痕(读走配置文件这类动作得能查)。
	if len(listAudit(t, rec, audit.ActionServerFS)) != 1 {
		t.Errorf("下载应写一行审计")
	}
	_ = resp.Body.Close()

	// 不存在必须**在写响应头之前**判掉(否则用户收到 0 字节的「下载完成」)。
	resp = download(t, client, srv.URL+"/api/servers/"+id+"/fs/download?path=/app/missing")
	defer func() { _ = resp.Body.Close() }()
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("下载不存在应 404,得 %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "octet-stream") {
		t.Error("404 响应不该已按下载流开头")
	}
}

func TestFSOpEnumAndErrors(t *testing.T) {
	srv, client, csrf, id, ws, rec := setupFSAPI(t)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/fs/op", csrf,
		`{"op":"mkdir","path":"/app/sub"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mkdir 应 200,得 %d", resp.StatusCode)
	}
	if !ws.dirs["/app/sub"] {
		t.Fatal("mkdir 没落进远端")
	}

	// 非空目录删除:409 —— 平台不提供递归删,界面也就没有能清空目录树的按钮。
	resp = doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/fs/op", csrf,
		`{"op":"remove","path":"/app"}`)
	dto := decodeDTO(t, resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || errCode(dto) != "directory_not_empty" {
		t.Errorf("删非空目录应 409,得 %d %v", resp.StatusCode, dto)
	}

	// rename 需要 to。
	resp = doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/fs/op", csrf,
		`{"op":"rename","path":"/app/a.txt"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("rename 缺 to 应 400,得 %d", resp.StatusCode)
	}

	// 白名单之外的动作一律 400,且**不触达远端**。
	before := len(ws.files)
	resp = doJSON(t, client, http.MethodPost, srv.URL+"/api/servers/"+id+"/fs/op", csrf,
		`{"op":"rmtree","path":"/app"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 op 应 400,得 %d", resp.StatusCode)
	}
	if len(ws.files) != before {
		t.Error("非法 op 竟改了远端状态")
	}

	// 写动作的**任何尝试**都留痕(含失败的 remove);而入参就被拒的请求压根不开连接,
	// 也就没有可审计的目标 —— 它不该出现在这本账里(与其余写端点同一口径)。
	entries := listAudit(t, rec, audit.ActionServerFS)
	if len(entries) != 2 {
		t.Fatalf("审计行数 = %d, want 2(mkdir + 失败 remove)", len(entries))
	}
	var sawFailedRemove bool
	for _, e := range entries {
		if e.Detail["op"] == "remove" && e.Detail["ok"] == false {
			sawFailedRemove = true
		}
	}
	if !sawFailedRemove {
		t.Errorf("失败的 remove 也该留痕: %v", entries)
	}
}

func TestFSUnsupportedAndUnauthenticated(t *testing.T) {
	// 拨号器不提供 fsDialer(测试里的常见实现)→ 501,不是 500。
	st := testStoreAuth(t)
	asvc := auth.NewService(st.DB, nil, nil)
	if err := asvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	tsvc := target.New(st.DB, v, &fakeInteractiveDialer{})
	srv := httptest.NewServer(New(testWebFSAuth(), asvc, WithVault(v), WithServers(tsvc)))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	id := newServerAPI(t, client, srv.URL, csrf)

	status, dto, _ := getJSON(t, client, srv.URL+"/api/servers/"+id+"/fs?path=/app", csrf)
	if status != http.StatusNotImplemented || errCode(dto) != "fs_unsupported" {
		t.Errorf("不支持应 501 fs_unsupported,得 %d %v", status, dto)
	}

	// 未登录:GET 也必须 401(远程文件是操作通道,不是公开查看)。
	bare := newTestClient(t)
	status, _, _ = getJSON(t, bare, srv.URL+"/api/servers/"+id+"/fs?path=/app", "")
	if status != http.StatusUnauthorized {
		t.Errorf("未登录应 401,得 %d", status)
	}
	// 写方法缺 CSRF 头 → 中间件拒(与其余写端点同一条纪律)。
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/servers/"+id+"/fs/content", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("缺 CSRF 应 403,得 %d", resp.StatusCode)
	}
}

// --- 测试小工具 ---

// download 发一条裸 GET 并**留着响应体**:下载要验的是响应头与字节,
// 走通用的 JSON 解码助手会把这两样都吃掉。
func download(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	return resp
}

func listAudit(t *testing.T, rec audit.Recorder, action string) []audit.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := rec.List(ctx, audit.ListFilter{Action: action})
	if err != nil {
		t.Fatalf("审计列表: %v", err)
	}
	if res == nil {
		t.Fatal("审计列表为空结果")
	}
	return res.Entries
}

// auditInt 取 detail 里的数值。审计 detail 经 JSON 存取向,解码类型不止一种,
// 断言不该绑死在某一种上。
func auditInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return -1
	}
}
