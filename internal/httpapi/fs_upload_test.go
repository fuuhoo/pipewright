package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
)

// fs_upload_test.go —— 会话式分块上传的契约测试。
//
// 钉的是这套设计里最容易在改动中悄悄丢掉的四件事:
//  1. 偏移由服务端核对 —— 客户端报错后拿到的是真实偏移,续传而非重头传整份;
//  2. 落点由服务端算 —— 相对路径逐段消毒,`..` 与绝对路径一律 400,且整批不落地;
//  3. 一个批次共用一条连接 —— 传文件夹时握手只付一次(拨号计数器作证);
//  4. 审计只记路径与体积 —— 上传的是密钥文件也不能把正文抄进第二本账。

func beginUpload(t *testing.T, f fsFixture, body string) map[string]any {
	t.Helper()
	resp := doJSON(t, f.client, http.MethodPost, f.srv.URL+"/api/servers/"+f.id+"/fs/upload/begin", f.csrf, body)
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusBadRequest, http.StatusConflict, http.StatusTooManyRequests:
	default:
		t.Fatalf("begin 状态 = %d", resp.StatusCode)
	}
	return decodeDTO(t, resp)
}

// putChunk 推一块裸字节(不是 multipart:平台本机磁盘不该当上传缓存)。
func putChunk(t *testing.T, f fsFixture, uploadID string, offset int64, payload string) (int, map[string]any) {
	t.Helper()
	chunkURL := fmt.Sprintf("%s/api/servers/%s/fs/upload/chunk?uploadId=%s&offset=%d",
		f.srv.URL, f.id, url.QueryEscape(uploadID), offset)
	req, err := http.NewRequest(http.MethodPut, chunkURL, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-CSRF-Token", f.csrf)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解码分块响应失败: %v(%s)", err, raw)
	}
	return resp.StatusCode, out
}

// callUpload 打吃 JSON 入参的两个动作(complete / abort)。
func callUpload(t *testing.T, f fsFixture, route, uploadID string) (int, map[string]any) {
	t.Helper()
	resp := doJSON(t, f.client, http.MethodPost, f.srv.URL+"/api/servers/"+f.id+"/fs/upload/"+route, f.csrf,
		`{"uploadId":"`+uploadID+`"}`)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, decodeDTO(t, resp)
}

func uploadStatus(t *testing.T, f fsFixture, uploadID string) (int, map[string]any) {
	t.Helper()
	status, dto, _ := getJSON(t, f.client,
		f.srv.URL+"/api/servers/"+f.id+"/fs/upload/status?uploadId="+url.QueryEscape(uploadID), f.csrf)
	return status, dto
}

// firstTask 取响应里的第一个上传任务。
func firstTask(t *testing.T, dto map[string]any) map[string]any {
	t.Helper()
	files, _ := dto["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("响应没有文件任务: %v", dto)
	}
	task, _ := files[0].(map[string]any)
	if task == nil {
		t.Fatalf("任务形状不对: %v", files[0])
	}
	return task
}

// allTasks 取全部上传任务。
func allTasks(t *testing.T, dto map[string]any) []map[string]any {
	t.Helper()
	files, _ := dto["files"].([]any)
	out := make([]map[string]any, 0, len(files))
	for _, raw := range files {
		task, _ := raw.(map[string]any)
		if task == nil {
			t.Fatalf("任务形状不对: %v", raw)
		}
		out = append(out, task)
	}
	return out
}

// dtoOffset 读 offset 字段(omitempty 让 0 干脆不出现,不能直接断言存在)。
func dtoOffset(dto map[string]any) int64 {
	if dto == nil || dto["offset"] == nil {
		return 0
	}
	return auditInt(dto["offset"])
}

func dtoStr(t *testing.T, m map[string]any, k string) string {
	t.Helper()
	s, _ := m[k].(string)
	if m[k] != nil && s == "" && k != "" {
		t.Fatalf("%s 不是字符串: %v", k, m[k])
	}
	return s
}

// tmpNames 列出远端还留着的上传半成品(约定前缀的隐藏临时名)。
func tmpNames(ws *memWorkspace) []string {
	var out []string
	for p := range ws.files {
		if strings.Contains(p, ".pipewright-up-") {
			out = append(out, p)
		}
	}
	return out
}

func TestFSUploadChunksRoundTrip(t *testing.T) {
	f := setupFSAPIFull(t)

	dto := beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"notes/a.txt","size":12}]}`)
	if dto["ok"] != true {
		t.Fatalf("begin 应 ok,得 %v", dto)
	}
	if got := dtoStr(t, dto, "dir"); got != "/tmp" {
		t.Errorf("批次目录 = %q", got)
	}
	if got := dtoStr(t, dto, "backend"); got != "sftp" {
		t.Errorf("生效实现 = %q", got)
	}
	task := firstTask(t, dto)
	uploadID := dtoStr(t, task, "uploadId")
	if uploadID == "" {
		t.Fatal("begin 没回 uploadId")
	}
	// 落点是服务端 join 出来的绝对路径,不是客户端递的那串相对写法。
	if got := dtoStr(t, task, "path"); got != "/tmp/notes/a.txt" {
		t.Fatalf("落点 = %q, want /tmp/notes/a.txt", got)
	}
	if !f.ws.dirs["/tmp/notes"] {
		t.Fatal("批次没把父目录建出来")
	}

	// 逐块推:每块的 offset 必须等于上一块回的那个,这就是断点续传的全部依据。
	next := int64(0)
	for _, part := range []string{"hello", " wor", "ld!"} {
		status, got := putChunk(t, f, uploadID, next, part)
		if status != http.StatusOK {
			t.Fatalf("分块应 200,得 %d %v", status, got)
		}
		next = dtoOffset(got)
	}
	if next != 12 {
		t.Fatalf("累计偏移 = %d, want 12", next)
	}
	// 没 complete 之前不许出现在正式路径上(界面列目录不会看见半个文件)。
	if _, ok := f.ws.files["/tmp/notes/a.txt"]; ok {
		t.Fatal("还没 complete 就落到正式名了")
	}

	status, done := callUpload(t, f, "complete", uploadID)
	if status != http.StatusOK || done["ok"] != true {
		t.Fatalf("complete = %d %v", status, done)
	}
	if got := dtoStr(t, done, "path"); got != "/tmp/notes/a.txt" {
		t.Errorf("complete 回的路径 = %q", got)
	}
	if got := string(f.ws.files["/tmp/notes/a.txt"]); got != "hello world!" {
		t.Fatalf("远端正文 = %q, want %q", got, "hello world!")
	}
	// 临时名必须消失:半成品留在盘上就是没人认领的孤儿字节。
	if left := tmpNames(f.ws); len(left) != 0 {
		t.Errorf("complete 后仍有半成品: %v", left)
	}

	entries := listAudit(t, f.rec, audit.ActionServerFS)
	if len(entries) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(entries))
	}
	d := entries[0].Detail
	if d["op"] != "upload" || d["path"] != "/tmp/notes/a.txt" || d["ok"] != true {
		t.Fatalf("上传审计 = %v", d)
	}
	if auditInt(d["bytes"]) != 12 {
		t.Errorf("审计 bytes = %v, want 12", d["bytes"])
	}
}

// 一条连接服务整批文件:传三个文件只该付一轮握手,传完还得把连接还回去。
func TestFSUploadReusesOneConnectionPerBatch(t *testing.T) {
	f := setupFSAPIFull(t)

	dto := beginUpload(t, f, `{"dir":"/tmp","files":[`+
		`{"rel":"one.txt","size":3},{"rel":"two.txt","size":3},{"rel":"three.txt","size":3}]}`)
	tasks := allTasks(t, dto)
	if len(tasks) != 3 {
		t.Fatalf("任务数 = %d, want 3", len(tasks))
	}
	if opened := f.dialer.open; opened != 1 {
		t.Fatalf("begin 应只拨一条连接,得 %d", opened)
	}
	// 三块并行推也行,这里按序走一遍最省时间。
	for _, task := range tasks {
		id := dtoStr(t, task, "uploadId")
		if status, got := putChunk(t, f, id, 0, "abc"); status != http.StatusOK {
			t.Fatalf("分块 = %d %v", status, got)
		}
		if status, got := callUpload(t, f, "complete", id); status != http.StatusOK {
			t.Fatalf("complete = %d %v", status, got)
		}
	}
	if extra := f.dialer.open - 1; extra != 0 {
		t.Errorf("批次又重拨了 %d 次,后续请求该复用那条连接", extra)
	}
	// 整批落地完就把连接放掉 —— 否则传一个文件夹等于占住一条 SSH 会话不放。
	if f.ws.closes != 1 {
		t.Errorf("放掉的连接数 = %d, want 1", f.ws.closes)
	}
	for _, p := range []string{"/tmp/one.txt", "/tmp/two.txt", "/tmp/three.txt"} {
		if got := string(f.ws.files[p]); got != "abc" {
			t.Errorf("%s = %q, want abc", p, got)
		}
	}
}

// 偏移对不上时回的是「远端实测长度」,已落地的字节一块不丢,客户端从那儿续。
func TestFSUploadOffsetMismatchReportsAuthoritativeOffset(t *testing.T) {
	f := setupFSAPIFull(t)

	dto := beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"big.bin","size":11}]}`)
	uploadID := dtoStr(t, firstTask(t, dto), "uploadId")
	if status, got := putChunk(t, f, uploadID, 0, "hello"); status != http.StatusOK {
		t.Fatalf("首块 = %d %v", status, got)
	}

	// 客户端以为已经传到 9(其实没有):必须 409 告知真实偏移,且这一块一个字节都不落。
	status, got := putChunk(t, f, uploadID, 9, " wor")
	if status != http.StatusConflict || errCode(got) != "offset_mismatch" {
		t.Fatalf("错偏移应 409 offset_mismatch,得 %d %v", status, got)
	}
	if dtoOffset(got) != 5 {
		t.Errorf("409 里的 offset = %v, want 5", got["offset"])
	}
	tmp := tmpNames(f.ws)
	if len(tmp) != 1 {
		t.Fatalf("临时文件数 = %d, want 1", len(tmp))
	}
	if body := string(f.ws.files[tmp[0]]); body != "hello" {
		t.Errorf("被拒的那块竟落了地: %q", body)
	}

	// status 问一次给同一个数:界面重连后就是靠它决定从哪续。
	sstatus, sdto := uploadStatus(t, f, uploadID)
	if sstatus != http.StatusOK || dtoOffset(sdto) != 5 {
		t.Fatalf("status = %d %v, want offset 5", sstatus, sdto)
	}
	if status, _ := putChunk(t, f, uploadID, 5, " world"); status != http.StatusOK {
		t.Fatalf("续传块失败: %d", status)
	}
	if status, done := callUpload(t, f, "complete", uploadID); status != http.StatusOK {
		t.Fatalf("complete = %d %v", status, done)
	}
	// 被拒请求之前那 5 字节仍在,拼起来还是完整原文。
	if body := string(f.ws.files["/tmp/big.bin"]); body != "hello world" {
		t.Errorf("续传后正文 = %q, want %q", body, "hello world")
	}
}

// 相对路径的规矩:保结构、挡穿越,一个非法则整批不落地。
func TestFSUploadRelPathRules(t *testing.T) {
	f := setupFSAPIFull(t)

	// 文件夹结构照原样保留(逐段消毒,不是只留基名);点开头的文件名不许被削掉。
	dto := beginUpload(t, f, `{"dir":"/tmp","files":[`+
		`{"rel":"demo/sub/one.txt","size":1},`+
		`{"rel":"demo/./two.txt","size":1},`+
		`{"rel":"demo\\win.txt","size":1},`+
		`{"rel":"demo/.env","size":1}]}`)
	if dto["ok"] != true {
		t.Fatalf("合法批次应 ok,得 %v", dto)
	}
	tasks := allTasks(t, dto)
	if len(tasks) != 4 {
		t.Fatalf("任务数 = %d, want 4", len(tasks))
	}
	for i, want := range []string{"/tmp/demo/sub/one.txt", "/tmp/demo/two.txt", "/tmp/demo/win.txt", "/tmp/demo/.env"} {
		if got := dtoStr(t, tasks[i], "path"); got != want {
			t.Errorf("任务 %d 落点 = %q, want %q", i, got, want)
		}
		if got := dtoStr(t, tasks[i], "rel"); got == "" {
			t.Errorf("任务 %d 没回显 rel", i)
		}
	}
	if !f.ws.dirs["/tmp/demo/sub"] {
		t.Error("深层父目录没建出来")
	}

	// 穿越:400,且**同批里的合法文件也不落地**(不给客户端一个半成批次)。
	before := len(f.ws.dirs)
	bad := beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"good/x.txt","size":1},{"rel":"../evil","size":1}]}`)
	if errCode(bad) != "invalid_path" {
		t.Errorf("穿越应 400 invalid_path,得 %v", bad)
	}
	if len(f.ws.dirs) != before {
		t.Errorf("非法批次竟建了目录: %v", f.ws.dirs)
	}
	if _, ok := f.ws.files["/evil"]; ok {
		t.Fatal("穿越路径落了地")
	}

	// 绝对路径同理被拒。
	if errCode(beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"/etc/passwd","size":1}]}`)) != "invalid_path" {
		t.Error("绝对路径应 400")
	}
	// 空相对路径。
	if errCode(beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"","size":1}]}`)) != "invalid_path" {
		t.Error("空相对路径应 400")
	}
	// 消毒后撞同一个落点。
	dup := beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"same.txt","size":1},{"rel":"./same.txt","size":1}]}`)
	if errCode(dup) != "duplicate_file" {
		t.Errorf("重名落点应 409 duplicate_file,得 %v", dup)
	}
	// 空批次连远端都不该碰。
	if errCode(beginUpload(t, f, `{"dir":"/tmp","files":[]}`)) != "bad_request" {
		t.Error("空 files 应 400")
	}
}

// 会话不存在、已取消还在推、没传完就 complete —— 客户端会犯的三条都得明确回话。
func TestFSUploadSessionErrors(t *testing.T) {
	f := setupFSAPIFull(t)

	// 未知 uploadId:重试没有意义,直接告知「这个会话没了」。
	status, got := putChunk(t, f, "deadbeef", 0, "x")
	if status != http.StatusNotFound || errCode(got) != "upload_not_found" {
		t.Errorf("未知会话应 404,得 %d %v", status, got)
	}
	if status, got := callUpload(t, f, "complete", "deadbeef"); status != http.StatusNotFound {
		t.Errorf("complete 未知会话应 404,得 %d %v", status, got)
	}
	if status, got := uploadStatus(t, f, "deadbeef"); status != http.StatusNotFound {
		t.Errorf("status 未知会话应 404,得 %d %v", status, got)
	}
	// 缺 uploadId 也是 404(而不是拿空串去查表)。
	if status, _ := uploadStatus(t, f, ""); status != http.StatusNotFound {
		t.Errorf("缺 uploadId 应 404,得 %d", status)
	}

	dto := beginUpload(t, f, `{"dir":"/tmp","files":[{"rel":"k.txt","size":100},{"rel":"keep.txt","size":3}]}`)
	tasks := allTasks(t, dto)
	uploadID := dtoStr(t, tasks[0], "uploadId")
	keepID := dtoStr(t, tasks[1], "uploadId")
	if status, _ := putChunk(t, f, uploadID, 0, "abc"); status != http.StatusOK {
		t.Fatalf("首块失败: %d", status)
	}

	// 只收到 3/100 就 complete:必须挡住并回真实偏移。静默落成半个文件最危险。
	status, got = callUpload(t, f, "complete", uploadID)
	if status != http.StatusConflict || errCode(got) != "incomplete_upload" {
		t.Fatalf("未传完就 complete 应 409,得 %d %v", status, got)
	}
	if dtoOffset(got) != 3 {
		t.Errorf("409 里的 offset = %v, want 3", got["offset"])
	}

	// offset 参数非法(负数)。
	if status, got := putChunk(t, f, uploadID, -1, ""); status != http.StatusBadRequest {
		t.Errorf("负 offset 应 400,得 %d %v", status, got)
	}

	// abort 之后这条任务就死了:再推 409,半成品清掉,正式名从没出现过。
	if status, got := callUpload(t, f, "abort", uploadID); status != http.StatusOK {
		t.Fatalf("abort = %d %v", status, got)
	}
	if _, ok := f.ws.files["/tmp/k.txt"]; ok {
		t.Error("abort 竟落了正式名")
	}
	if left := tmpNames(f.ws); len(left) != 0 {
		t.Errorf("abort 后仍有半成品: %v", left)
	}
	if status, got := putChunk(t, f, uploadID, 3, "def"); status != http.StatusConflict || errCode(got) != "upload_closed" {
		t.Errorf("已取消的任务再推应 409 upload_closed,得 %d %v", status, got)
	}
	if status, got := callUpload(t, f, "complete", uploadID); status != http.StatusConflict {
		t.Errorf("已取消的任务再 complete 应 409,得 %d %v", status, got)
	}

	// 批次里还剩一个任务活着:整批没结束就不能提前放掉连接、也不能提前回收会话。
	if status, _ := putChunk(t, f, keepID, 0, "xyz"); status != http.StatusOK {
		t.Fatalf("同批次另一个任务被 abort 连累了: %d", status)
	}
	if status, got := callUpload(t, f, "complete", keepID); status != http.StatusOK {
		t.Fatalf("另一个任务 complete = %d %v", status, got)
	}
	// 整批结束后批次从注册表摘掉,此时再拿旧 uploadId 来问就是 404(会话真没了)。
	if status, got := putChunk(t, f, uploadID, 3, "def"); status != http.StatusNotFound || errCode(got) != "upload_not_found" {
		t.Errorf("批次回收后应 404 upload_not_found,得 %d %v", status, got)
	}

	var sawAbort bool
	for _, e := range listAudit(t, f.rec, audit.ActionServerFS) {
		if e.Detail["op"] == "upload_abort" && e.Detail["path"] == "/tmp/k.txt" {
			sawAbort = true
		}
	}
	if !sawAbort {
		t.Error("abort 没留审计")
	}
}

// 零字节文件一块都不会发,complete 得自己把它建出来;覆盖写要保住原有权限位。
func TestFSUploadEmptyFileAndPreservesExistingMode(t *testing.T) {
	f := setupFSAPIFull(t)

	// /home/deploy/x 先按 600 存着(密钥文件的常见档位),上传覆盖后不许被放宽成 644。
	f.ws.modes["/home/deploy/x"] = 0o600

	dto := beginUpload(t, f, `{"dir":"/home/deploy","files":[{"rel":"x","size":4},{"rel":"fresh-empty","size":0}]}`)
	tasks := allTasks(t, dto)
	overwrite, empty := tasks[0], tasks[1]

	if status, _ := putChunk(t, f, dtoStr(t, overwrite, "uploadId"), 0, "abcd"); status != http.StatusOK {
		t.Fatal("覆盖写分块失败")
	}
	if status, got := callUpload(t, f, "complete", dtoStr(t, overwrite, "uploadId")); status != http.StatusOK {
		t.Fatalf("覆盖写 complete = %d %v", status, got)
	}
	if got := string(f.ws.files["/home/deploy/x"]); got != "abcd" {
		t.Errorf("覆盖后正文 = %q, want abcd", got)
	}
	if mode := f.ws.modes["/home/deploy/x"]; mode != 0o600 {
		t.Errorf("权限位 = %o, want 保住 600", mode)
	}

	// 空文件:一块没发,也要落地成 0 字节的真文件(不是「静默什么也没做」)。
	if status, got := callUpload(t, f, "complete", dtoStr(t, empty, "uploadId")); status != http.StatusOK {
		t.Fatalf("空文件 complete = %d %v", status, got)
	}
	data, ok := f.ws.files["/home/deploy/fresh-empty"]
	if !ok {
		t.Fatal("空文件没落地")
	}
	if len(data) != 0 {
		t.Errorf("空文件正文 = %q, want 空", data)
	}
}

// 上传的是密钥文件时,审计里只许有路径与字节数。
func TestFSUploadAuditNeverRecordsPayload(t *testing.T) {
	f := setupFSAPIFull(t)

	const payload = "db_password=SECRETPHRASE-upload\n"
	body := fmt.Sprintf(`{"dir":"/tmp","files":[{"rel":"creds.env","size":%d}]}`, len(payload))
	dto := beginUpload(t, f, body)
	uploadID := dtoStr(t, firstTask(t, dto), "uploadId")
	if status, _ := putChunk(t, f, uploadID, 0, payload); status != http.StatusOK {
		t.Fatalf("分块失败: %d", status)
	}
	if status, got := callUpload(t, f, "complete", uploadID); status != http.StatusOK {
		t.Fatalf("complete = %d %v", status, got)
	}

	entries := listAudit(t, f.rec, audit.ActionServerFS)
	if len(entries) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(entries))
	}
	if strings.Contains(fmt.Sprint(entries[0].Detail), "SECRETPHRASE") {
		t.Fatalf("审计泄漏了上传正文: %v", entries[0].Detail)
	}
}

// 上传端点与其余 fs 路由同一道闸:GET 也按操作权限判,缺 CSRF 一律拒。
func TestFSUploadRoutesGuarded(t *testing.T) {
	f := setupFSAPIFull(t)

	// 未登录。
	bare := newTestClient(t)
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/servers/"+f.id+"/fs/upload/begin", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := bare.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("未登录上传应 401,得 %d", resp.StatusCode)
	}

	// 已登录但缺 CSRF 头。
	req, err = http.NewRequest(http.MethodPost, f.srv.URL+"/api/servers/"+f.id+"/fs/upload/begin", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = f.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("缺 CSRF 应 403,得 %d", resp.StatusCode)
	}

	// status 是 GET,但同样不许匿名访问。
	status, _, _ := getJSON(t, bare, f.srv.URL+"/api/servers/"+f.id+"/fs/upload/status?uploadId=x", "")
	if status != http.StatusUnauthorized {
		t.Errorf("匿名问上传进度应 401,得 %d", status)
	}
}
