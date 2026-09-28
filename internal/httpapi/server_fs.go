package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/target"
)

// server_fs.go —— 远程文件面板的 HTTP 端点(「远程」弹窗的下半屏)。
//
// 与主机终端是同一条能力线的两面:终端在远端跑命令,面板列/读/写这台机上的文件。
// 两者都落在 target.Workspace / ExecInteractive 这层,连接与凭据仍经 vault 即用即弃。
//
// 鉴权(FR-18 同口径):每条路由(含 GET)都过 requireTerminalOperate。
// 中间件对 GET 只判到 ActView,而「读主机上任意文件」不是平台语义的查看 ——
// 它是与开终端等价的一条通道,必须按 ActOperate 把关。
//
// 审计(NFR-8):每个**写**动作(保存正文 / 上传 / mkdir / remove / rename)与每个下载
// 都留一行。detail 只带路径与字节数,绝不带正文 —— 面板里读的可能是密钥。
//
// 路径不做「越界拦截」:能在终端里 `cd /etc` 的会话,拦它只会给假的安全感。
// 真正的边界是操作权限本身。

const (
	// fsTextReadLimit 是编辑器一次取回的正文上限;超出即截断并在响应里如实标出。
	fsTextReadLimit = 1 << 20 // 1 MiB
	// fsJSONBodyLimit 是「保存正文」请求体上限(编辑器里手打的文本,给 1 MiB 富余)。
	fsJSONBodyLimit = 8 << 20
	// fsMultipartMemory 是 multipart 解析驻留内存的上限,超出落临时文件后仍流式转发远端。
	fsMultipartMemory = 32 << 20
	// fsPathMax 是路径长度上限(远超正常需要,只为挡住把整份文件当路径提交的误操作)。
	fsPathMax = 4096
	// fsMetaTimeout 是列目录/取属性这类轻操作的超时。
	fsMetaTimeout = 30 * time.Second
	// fsDataTimeout 是读写正文与上传下载的超时(大文件经慢链路要留足时间)。
	fsDataTimeout = 5 * time.Minute
)

// fsEntryDTO 是一个远端条目的对外形态(直接复用 target 的 json tag)。
type fsEntryDTO = target.FileStat

// fsListDTO 是 GET /fs 的响应:目录给 entries,文件给 entry(界面据此决定画列表还是画预览)。
type fsListDTO struct {
	Path     string       `json:"path"`
	Backend  string       `json:"backend"`
	IsDir    bool         `json:"isDir"`
	Entries  []fsEntryDTO `json:"entries"`
	Entry    *fsEntryDTO  `json:"entry,omitempty"`
	ServerID string       `json:"serverId"`
}

// fsContentDTO 是 GET /fs/content 的响应。binary=true 时 content 恒空(不把二进制灌进编辑器)。
// Size 是**本次返回的正文字节数**,不是远端文件大小;是否被截断由 Truncated 如实标出。
type fsContentDTO struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// fsResultDTO 是写类操作的统一回执(界面按 ok/error 提示)。
type fsResultDTO struct {
	OK      bool   `json:"ok"`
	Path    string `json:"path,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Backend string `json:"backend,omitempty"`
}

type fsWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fsOpRequest struct {
	Op   string `json:"op"`
	Path string `json:"path"`
	To   string `json:"to"`
}

// fsOps 是改名/建目录/删除的枚举白名单(AC-SEC-02:只有这三种动作能落地)。
var fsOps = map[string]struct{}{
	"mkdir":  {},
	"remove": {},
	"rename": {},
}

// openFS 校验权限并开一个远程文件工作区;失败时已写好状态码,返回 false。
// 调用方拿到 true 后必须 defer ws.Close()。
func openFS(w http.ResponseWriter, r *http.Request, svc target.Service, acc *access.Service, id string) (target.Workspace, bool) {
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
		return nil, false
	}
	if !requireTerminalOperate(w, r, acc, id) {
		return nil, false
	}
	opener, ok := svc.(target.WorkspaceOpener)
	if !ok {
		writeError(w, http.StatusNotImplemented, "fs_unsupported", "当前部署不支持远程文件操作")
		return nil, false
	}
	// 先认服务器:不存在(404)不该被伪装成「连不上」。OpenWorkspace 内部也是这个顺序。
	if _, err := svc.Get(r.Context(), id); err != nil {
		writeServerError(w, err)
		return nil, false
	}
	ws, err := opener.OpenWorkspace(r.Context(), id)
	if err != nil {
		writeFSError(w, err)
		return nil, false
	}
	return ws, true
}

// writeFSError 把远程文件层的领域错误映射成人读契约码。
// 未识别的错误交回 writeServerError(服务器不存在/凭据/vault 等既有映射)。
func writeFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, target.ErrRemoteNotFound):
		writeError(w, http.StatusNotFound, "remote_not_found", "远程路径不存在")
	case errors.Is(err, target.ErrRemoteNotDirectory):
		writeError(w, http.StatusBadRequest, "not_a_directory", "远程路径不是目录")
	case errors.Is(err, target.ErrRemoteNotEmpty):
		writeError(w, http.StatusConflict, "directory_not_empty", "远程目录非空(平台不提供递归删除)")
	case errors.Is(err, target.ErrRemotePermission):
		writeError(w, http.StatusForbidden, "remote_permission_denied", "远程路径权限不足")
	case errors.Is(err, target.ErrFSUnsupported):
		writeError(w, http.StatusNotImplemented, "fs_unsupported", "该服务器没有 SFTP 子系统,也无法用命令兜底")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "fs_timeout", "远程文件操作超时")
	case errors.Is(err, target.ErrUnreachable):
		writeError(w, http.StatusBadGateway, "server_unreachable", "无法连接服务器:端口未开放、主机不可达或超时")
	case errors.Is(err, target.ErrAuth):
		writeError(w, http.StatusBadGateway, "ssh_auth_failed", "SSH 认证失败:密钥或口令无效,或无登录权限")
	default:
		writeServerError(w, err)
	}
}

// fsPathParam 取并校验路径参数:允许空(=该会话的家目录),超长即拒。
func fsPathParam(r *http.Request, name string) (string, error) {
	p := r.URL.Query().Get(name)
	if len(p) > fsPathMax {
		return "", fmt.Errorf("路径过长(上限 %d 字符)", fsPathMax)
	}
	return p, nil
}

// sanitizeRemoteName 把客户端传来的文件名折成一个安全的**基名**:去掉一切路径分隔符与前导
// `-`(防被当参数)、拒掉 `.`/`..`/空。上传的落点由服务端 join,不让客户端塞路径。
func sanitizeRemoteName(name string) (string, error) {
	n := path.Base(strings.ReplaceAll(name, "\\", "/"))
	n = strings.TrimLeft(n, "/.")
	if n == "" || n == ".." {
		return "", errors.New("文件名非法")
	}
	if len(n) > 255 {
		return "", errors.New("文件名过长")
	}
	return n, nil
}

// makeFSListHandler GET /api/servers/{id}/fs?path= —— 列目录(或回单个条目的属性)。
func makeFSListHandler(svc target.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := fsPathParam(r, "path")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		ws, ok := openFS(w, r, svc, acc, id)
		if !ok {
			return
		}
		defer func() { _ = ws.Close() }()

		ctx, cancel := context.WithTimeout(r.Context(), fsMetaTimeout)
		defer cancel()

		resolved, err := ws.Realpath(ctx, p)
		if err != nil {
			// 空路径(家目录)拿不到时按不存在处理:界面上这就是「进不去这个目录」。
			writeFSError(w, err)
			return
		}
		out := fsListDTO{Path: resolved, Backend: ws.Backend(), ServerID: id, Entries: []fsEntryDTO{}}

		st, err := ws.Stat(ctx, resolved)
		if err != nil {
			writeFSError(w, err)
			return
		}
		if !st.IsDir {
			out.IsDir = false
			entry := st
			out.Entry = &entry
			writeJSON(w, http.StatusOK, out)
			return
		}
		entries, err := ws.ReadDir(ctx, resolved)
		if err != nil {
			writeFSError(w, err)
			return
		}
		out.IsDir = true
		out.Entries = entries
		writeJSON(w, http.StatusOK, out)
	}
}

// makeFSContentHandler GET /api/servers/{id}/fs/content?path= —— 取文本正文(编辑器用)。
func makeFSContentHandler(svc target.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := fsPathParam(r, "path")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		if p == "" {
			writeError(w, http.StatusBadRequest, "invalid_path", "path 不能为空")
			return
		}
		ws, ok := openFS(w, r, svc, acc, id)
		if !ok {
			return
		}
		defer func() { _ = ws.Close() }()

		ctx, cancel := context.WithTimeout(r.Context(), fsDataTimeout)
		defer cancel()

		resolved, err := ws.Realpath(ctx, p)
		if err != nil {
			writeFSError(w, err)
			return
		}
		data, truncated, err := ws.ReadFile(ctx, resolved, fsTextReadLimit)
		if err != nil {
			writeFSError(w, err)
			return
		}
		out := fsContentDTO{Path: resolved, Size: int64(len(data)), Truncated: truncated}
		if looksBinary(data) {
			out.Binary = true
			out.Content = ""
		} else {
			out.Content = string(data)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeFSWriteHandler POST /api/servers/{id}/fs/content —— 保存文本正文(覆盖写)。
func makeFSWriteHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, fsJSONBodyLimit)
		var req fsWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if req.Path == "" || len(req.Path) > fsPathMax {
			writeError(w, http.StatusBadRequest, "invalid_path", "path 不能为空且不能超长")
			return
		}
		ws, ok := openFS(w, r, svc, acc, id)
		if !ok {
			return
		}
		defer func() { _ = ws.Close() }()

		ctx, cancel := context.WithTimeout(r.Context(), fsDataTimeout)
		defer cancel()

		auditOp := func(ok bool) {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionServerFS,
				TargetType: audit.TargetServer,
				TargetID:   id,
				// 只记动作/路径/体积:正文可能是密钥、配置或业务数据。
				Detail: map[string]any{"op": "save", "path": req.Path, "bytes": len(req.Content), "ok": ok},
				IP:     clientIP(r),
			})
		}

		if err := ws.WriteFile(ctx, req.Path, strings.NewReader(req.Content)); err != nil {
			auditOp(false)
			writeFSError(w, err)
			return
		}
		auditOp(true)
		writeJSON(w, http.StatusOK, fsResultDTO{OK: true, Path: req.Path, Bytes: int64(len(req.Content)), Backend: ws.Backend()})
	}
}

// makeFSDownloadHandler GET /api/servers/{id}/fs/download?path= —— 流式下载。
func makeFSDownloadHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := fsPathParam(r, "path")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		if p == "" {
			writeError(w, http.StatusBadRequest, "invalid_path", "path 不能为空")
			return
		}
		ws, ok := openFS(w, r, svc, acc, id)
		if !ok {
			return
		}
		defer func() { _ = ws.Close() }()

		ctx, cancel := context.WithTimeout(r.Context(), fsDataTimeout)
		defer cancel()

		resolved, err := ws.Realpath(ctx, p)
		if err != nil {
			writeFSError(w, err)
			return
		}
		st, err := ws.Stat(ctx, resolved)
		if err != nil {
			writeFSError(w, err)
			return
		}
		if st.IsDir {
			writeError(w, http.StatusBadRequest, "not_a_file", "目录不能直接下载")
			return
		}
		rc, err := ws.OpenRead(ctx, resolved)
		if err != nil {
			writeFSError(w, err)
			return
		}
		defer func() { _ = rc.Close() }()

		// 头必须在写第一字节之前定好:Content-Disposition 里的名字经 sanitizeRemoteName
		// 去过换行/引号,否则是一个响应头注入点。
		name := sanitizeOrBase(resolved)
		w.Header().Set("Content-Type", "application/octet-stream")
		if st.Size > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(st.Size, 10))
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)

		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServerFS,
			TargetType: audit.TargetServer,
			TargetID:   id,
			Detail:     map[string]any{"op": "download", "path": resolved, "bytes": st.Size},
			IP:         clientIP(r),
		})
	}
}

// makeFSUploadHandler POST /api/servers/{id}/fs/upload —— multipart 上传到指定目录。
// 表单:file(文件)+ dir(目标目录)。落点由服务端 join,文件名取自 sanitizeRemoteName。
func makeFSUploadHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := r.ParseMultipartForm(fsMultipartMemory); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "上传表单解析失败:"+err.Error())
			return
		}
		dir := r.FormValue("dir")
		if len(dir) > fsPathMax {
			writeError(w, http.StatusBadRequest, "invalid_path", "目标目录过长")
			return
		}
		f, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "缺少 file 字段")
			return
		}
		defer func() { _ = f.Close() }()
		name, err := sanitizeRemoteName(header.Filename)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_filename", err.Error())
			return
		}
		dest := path.Join(dir, name)

		ws, ok := openFS(w, r, svc, acc, id)
		if !ok {
			return
		}
		defer func() { _ = ws.Close() }()

		ctx, cancel := context.WithTimeout(r.Context(), fsDataTimeout)
		defer cancel()

		auditOp := func(size int64, ok bool) {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionServerFS,
				TargetType: audit.TargetServer,
				TargetID:   id,
				Detail:     map[string]any{"op": "upload", "path": dest, "bytes": size, "ok": ok},
				IP:         clientIP(r),
			})
		}

		// 流式转发:上传字节经 stdin 落远端临时名再 rename,不整份进内存(与 Upload 同纪律)。
		if err := ws.WriteFile(ctx, dest, f); err != nil {
			auditOp(0, false)
			writeFSError(w, err)
			return
		}
		auditOp(header.Size, true)
		writeJSON(w, http.StatusOK, fsResultDTO{OK: true, Path: dest, Bytes: header.Size, Backend: ws.Backend()})
	}
}

// makeFSOpHandler POST /api/servers/{id}/fs/op —— 建目录 / 删除 / 改名(枚举白名单)。
func makeFSOpHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, fsJSONBodyLimit)
		var req fsOpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if _, allowed := fsOps[req.Op]; !allowed {
			writeError(w, http.StatusBadRequest, "invalid_op", "op 只允许 mkdir / remove / rename")
			return
		}
		if req.Path == "" || len(req.Path) > fsPathMax || len(req.To) > fsPathMax {
			writeError(w, http.StatusBadRequest, "invalid_path", "path 必填且不能超长")
			return
		}
		if req.Op == "rename" && req.To == "" {
			writeError(w, http.StatusBadRequest, "invalid_path", "rename 需要 to")
			return
		}

		ws, ok := openFS(w, r, svc, acc, id)
		if !ok {
			return
		}
		defer func() { _ = ws.Close() }()

		ctx, cancel := context.WithTimeout(r.Context(), fsMetaTimeout)
		defer cancel()

		var err error
		switch req.Op {
		case "mkdir":
			err = ws.Mkdir(ctx, req.Path)
		case "remove":
			err = ws.Remove(ctx, req.Path)
		case "rename":
			err = ws.Rename(ctx, req.Path, req.To)
		}

		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServerFS,
			TargetType: audit.TargetServer,
			TargetID:   id,
			Detail:     map[string]any{"op": req.Op, "path": req.Path, "to": req.To, "ok": err == nil},
			IP:         clientIP(r),
		})
		if err != nil {
			writeFSError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fsResultDTO{OK: true, Path: req.Path, Backend: ws.Backend()})
	}
}

// looksBinary 用「前 8 KiB 有 NUL 或整体不是合法 UTF-8」判定二进制。
// 目的是别把镜像/压缩包当文本灌进编辑器 —— 那不是给人读的东西,也白占一次响应体。
func looksBinary(data []byte) bool {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	if strings.IndexByte(string(probe), 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}

// sanitizeOrBase 取路径基名并保证可用于响应头(下载文件名)。
func sanitizeOrBase(p string) string {
	if n, err := sanitizeRemoteName(path.Base(p)); err == nil {
		return n
	}
	return "download"
}
