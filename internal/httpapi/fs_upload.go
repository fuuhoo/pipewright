package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/i18n"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/go-chi/chi/v5"
)

// fs_upload.go —— 会话式分块上传(「远程」面板传文件 / 传文件夹的后端)。
//
// 为什么不再走 multipart:
//   · multipart 超出内存上限的部分会先落到**平台本机**临时文件再转发远端 —— 平台盘成了
//     上传缓存,几十 GB 的镜像往上一丢就见底,还白付一次落盘+读回。
//   · 一次 POST 要么整份成要么全废:断线、刷新页面、慢链路撞到总时长上限,前面传的全部作废。
//     分块 + 服务端记账才谈得上「续传」。
//   · 分块顺带给界面逐块进度与逐块重试,不用把整份文件读进内存才算进度。
//
// 一个批次(batch)= 一次界面动作的一批上传任务,**共用一条 SSH 连接**:逐文件重拨
// TCP+KEX+认证的话,500 个文件的文件夹要先付 500 轮握手,连接跟着批次走才是对的粒度。
// 批次内每个任务(uploadId)把自己的块按序追加到远端隐藏临时名,偏移由服务端核对 ——
// 断线后 GET status 拿到远端实测长度,从那儿接着推,拼起来仍是完整原文。
//
// 落点纪律:客户端只能给相对路径(逐段消毒,挡住 `..` 与绝对路径),绝对落点由服务端
// join 后的批次目录算出 —— 面板里再怎么写,上传也塞不进任意路径。
//
// 审计(NFR-8):每个文件落地一条 complete(路径 + 字节数),abort 一条;逐块不记 ——
// 大文件几百块会把审计表刷满,而「谁传了个什么上去」才是那条该留的事实。

const (
	// fsChunkMaxBytes 是单个请求体的硬上限。界面默认按 8/16/32 MiB 切块,这里留一倍富余,
	// 超出即 413 —— 客户端该切小点重发,而不是让平台把一个来路不明的巨块当正常块收。
	fsChunkMaxBytes = 64 << 20
	// fsUploadMaxFiles 是一个批次的文件数上限(文件夹上传的实际约束,防把连接与内存打满)。
	fsUploadMaxFiles = 5000
	// fsUploadMaxBatches 是同时存活的批次数上限:每批次握一条 SSH 连接,得有天花板。
	fsUploadMaxBatches = 64
	// fsUploadIdleTTL 是批次静默多久后被回收(界面关掉、标签页刷新都属正常弃传)。
	fsUploadIdleTTL = 30 * time.Minute
	// fsChunkTimeout 是**单块**的超时(不是整场上传的):块有上限,慢链路也只是多花几轮往返。
	fsChunkTimeout = 5 * time.Minute
	// fsUploadMaxDepth 是相对落点的目录深度上限。
	fsUploadMaxDepth = 32
)

// errUploadGone 是批次已被回收或不存在(对客户端就是「这个上传会话没了,重头来」)。
var errUploadGone = errors.New("upload: 会话不存在或已过期")

// fsUploadTask 是批次里的一个文件:一条独立的断点。
type fsUploadTask struct {
	id     string
	rel    string // 客户端给的相对落点(已消毒)
	dest   string // 最终绝对落点(服务端算出)
	tmp    string // 上传中的远端隐藏临时名
	total  int64  // 客户端申报的体积;0 = 不知道
	offset int64  // 已确认落地的字节数(与服务端临时文件实际长度同源)
	busy   bool   // 有一块正在飞(同一任务不并发追加:兜底实现只能 cat >>)
	done   bool
}

// fsUploadBatch 是一次界面动作的上传批次,持有一条复用的远程文件工作区。
type fsUploadBatch struct {
	id       string
	serverID string
	userID   string
	dial     func(context.Context) (target.Workspace, error)

	mu    sync.Mutex
	tasks map[string]*fsUploadTask
	order []string // 任务创建顺序,回给客户端时保持稳定

	wsMu    sync.Mutex
	ws      target.Workspace
	wsName  string // 生效实现名,供界面如实显示
	dropped bool

	idle      *time.Timer
	lastTouch time.Time
}

// fsUploadStore 是批次注册表(进程内;平台重启即所有会话作废,客户端从头重传)。
// byTask 是 uploadId → 批次的二级索引:每个块请求都要认一次门把,不能拿批次数量去扫。
type fsUploadStore struct {
	mu      sync.Mutex
	batches map[string]*fsUploadBatch
	byTask  map[string]*fsUploadBatch
}

func newFSUploadStore() *fsUploadStore {
	return &fsUploadStore{batches: map[string]*fsUploadBatch{}, byTask: map[string]*fsUploadBatch{}}
}

// newUploadToken 取一段随机 hex 作为 id(上传会话 id 必须不可猜:它是唯一的门把)。
func newUploadToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 在本机不会失败;真失败了宁可拒绝这次上传,也不发一个可预测的 id。
		return ""
	}
	return hex.EncodeToString(b[:])
}

// touch 重置空闲计时:每个请求(含逐块)都算一次「人还在传」。
func (b *fsUploadBatch) touch(s *fsUploadStore, ttl time.Duration) {
	b.mu.Lock()
	b.lastTouch = time.Now()
	if b.idle == nil {
		b.idle = time.AfterFunc(ttl, func() { s.expire(b.id) })
	} else {
		b.idle.Reset(ttl)
	}
	b.mu.Unlock()
}

// getWS 取批次的工作区;连接断了就重拨一条(冷启动那一次拨号由第一个请求付)。
// 拨号期间持 wsMu,故并发请求只会拨出一条连接。
func (b *fsUploadBatch) getWS(ctx context.Context) (target.Workspace, error) {
	b.wsMu.Lock()
	defer b.wsMu.Unlock()
	if b.dropped {
		return nil, errUploadGone
	}
	if b.ws != nil {
		return b.ws, nil
	}
	ws, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	b.ws = ws
	b.wsName = ws.Backend()
	return ws, nil
}

// dropWS 丢掉这条连接(出错后没人知道它对端还剩什么状态,重拨一条最省事)。
func (b *fsUploadBatch) dropWS() {
	b.wsMu.Lock()
	ws := b.ws
	b.ws = nil
	b.wsMu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
}

// backend 回显当前生效实现;还没拨上连接时为空(界面不显示徽标即可)。
func (b *fsUploadBatch) backend() string {
	b.wsMu.Lock()
	defer b.wsMu.Unlock()
	return b.wsName
}

// task 取批次里的某个任务。
func (b *fsUploadBatch) task(id string) (*fsUploadTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	return t, ok
}

// allDone 判整批是否都已落地/取消(收尾用:全完成就把那条连接放掉)。
func (b *fsUploadBatch) allDone() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range b.tasks {
		if !t.done {
			return false
		}
	}
	return true
}

// snapshotTasks 按创建顺序复制任务的关键字段,供响应体用(不暴露内部状态)。
func (b *fsUploadBatch) snapshotTasks() []fsUploadTaskDTO {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]fsUploadTaskDTO, 0, len(b.order))
	for _, id := range b.order {
		t := b.tasks[id]
		out = append(out, fsUploadTaskDTO{Rel: t.rel, UploadID: t.id, Path: t.dest, Offset: t.offset, Size: t.total, Done: t.done})
	}
	return out
}

// add 登记批次;超出上限时回 false(调用方回 429)。
func (s *fsUploadStore) add(b *fsUploadBatch, ttl time.Duration) bool {
	s.mu.Lock()
	if len(s.batches) >= fsUploadMaxBatches {
		s.mu.Unlock()
		return false
	}
	s.batches[b.id] = b
	for tid := range b.tasks {
		s.byTask[tid] = b
	}
	s.mu.Unlock()
	b.touch(s, ttl)
	return true
}

// get 取批次。
func (s *fsUploadStore) get(id string) (*fsUploadBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	return b, ok
}

// remove 摘掉批次并停掉计时(连同样里的任务索引一起清)。
func (s *fsUploadStore) remove(id string) (*fsUploadBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if ok {
		delete(s.batches, id)
		for tid := range b.tasks {
			delete(s.byTask, tid)
		}
	}
	return b, ok
}

// expire 回收一个闲置到期的批次:清掉远端半成品,再放掉连接。
// 计时器回调跑在别的 goroutine 上,所以这里绝不碰 ResponseWriter。
func (s *fsUploadStore) expire(id string) {
	b, ok := s.remove(id)
	if !ok {
		return
	}
	b.mu.Lock()
	b.droppedLocked()
	live := make([]*fsUploadTask, 0, len(b.order))
	for _, tid := range b.order {
		if t := b.tasks[tid]; !t.done {
			live = append(live, t)
		}
	}
	b.mu.Unlock()

	if len(live) == 0 {
		b.closeWS()
		return
	}
	// 半成品文件是远端磁盘上的真实字节(可能是几 GB),必须删;拨不上就连不上,
	// 别为了删临时文件在后台反复重拨 —— 那是用户已经关掉的那次上传。
	ctx, cancel := context.WithTimeout(context.Background(), fsMetaTimeout)
	defer cancel()
	ws, err := b.getWS(ctx)
	if err != nil {
		b.closeWS()
		return
	}
	for _, t := range live {
		_ = ws.Remove(ctx, t.tmp)
	}
	b.closeWS()
}

// droppedLocked 标记批次已作废(调用方须持 b.mu)。
func (b *fsUploadBatch) droppedLocked() { b.dropped = true }

// closeWS 放掉连接(批次收尾的唯一出口)。
func (b *fsUploadBatch) closeWS() {
	b.wsMu.Lock()
	ws := b.ws
	b.ws = nil
	b.wsName = ""
	b.wsMu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
}

// fsUploadFileRef 是 begin 里的一个待传文件。
type fsUploadFileRef struct {
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
}

type fsUploadBeginRequest struct {
	Dir   string            `json:"dir"`
	Files []fsUploadFileRef `json:"files"`
}

type fsUploadIDRequest struct {
	UploadID string `json:"uploadId"`
}

// fsUploadTaskDTO 是上传任务对外的形状:客户端据此画进度、决定从哪续。
type fsUploadTaskDTO struct {
	Rel      string `json:"rel,omitempty"`
	UploadID string `json:"uploadId"`
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	Size     int64  `json:"size"`
	Done     bool   `json:"done,omitempty"`
}

type fsUploadBatchDTO struct {
	OK      bool              `json:"ok"`
	BatchID string            `json:"batchId"`
	Dir     string            `json:"dir,omitempty"`
	Backend string            `json:"backend,omitempty"`
	Files   []fsUploadTaskDTO `json:"files"`
	Offset  int64             `json:"offset,omitempty"`
	Err     *errDetail        `json:"error,omitempty"`
}

// fsErr 造错误内层(与全站错误信封同形):409 这类「既要给码又要给数据」的响应
// 用得上 —— 客户端读 .error.code 的习惯不用为上传这条路另学一套。
func fsErr(w http.ResponseWriter, code, msg string) *errDetail {
	return &errDetail{Code: code, Message: i18n.T(localeOf(w), msg)}
}

// sanitizeRemoteRel 把客户端给的相对落点折成安全的远端相对路径:逐段消毒(复用上传
// 文件名的规则),丢掉空段与 `.` 段,拒绝 `..` 与绝对路径,并限深度与总长。
// 文件夹上传的子目录结构由此保留,路径穿越由此挡掉。
func sanitizeRemoteRel(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") {
		return "", errors.New("相对路径不能以 / 开头")
	}
	var segs []string
	for _, s := range strings.Split(rel, "/") {
		if s == "" || s == "." {
			continue
		}
		if s == ".." {
			return "", errors.New("相对路径不能向上跳")
		}
		n, err := sanitizeRemoteName(s)
		if err != nil {
			return "", err
		}
		segs = append(segs, n)
	}
	if len(segs) == 0 || len(segs) > fsUploadMaxDepth {
		return "", errors.New("相对路径为空或层级过深")
	}
	out := strings.Join(segs, "/")
	if len(out) > fsPathMax {
		return "", errors.New("相对路径过长")
	}
	return out, nil
}

// dialWorkspace 开一条新的远程文件工作区(供批次复用与断线重拨)。
// 与 openFS 同一顺序:先认服务器,再判能力,最后才碰凭据。
func dialWorkspace(ctx context.Context, svc target.Service, id string) (target.Workspace, error) {
	opener, ok := svc.(target.WorkspaceOpener)
	if !ok {
		return nil, target.ErrFSUnsupported
	}
	if _, err := svc.Get(ctx, id); err != nil {
		return nil, err
	}
	return opener.OpenWorkspace(ctx, id)
}

// requireFSAccess 是 openFS 的前半段:服务在否、操作权限够不够、这台服务器存不存在、
// 当前部署支不支持远程文件。失败时已写好状态码。
func requireFSAccess(w http.ResponseWriter, r *http.Request, svc target.Service, acc *access.Service, id string) bool {
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
		return false
	}
	if !requireTerminalOperate(w, r, acc, id) {
		return false
	}
	if _, ok := svc.(target.WorkspaceOpener); !ok {
		writeError(w, http.StatusNotImplemented, "fs_unsupported", "当前部署不支持远程文件操作")
		return false
	}
	// 先认服务器:不存在(404)不该被伪装成「连不上」。OpenWorkspace 内部也是这个顺序。
	if _, err := svc.Get(r.Context(), id); err != nil {
		writeServerError(w, err)
		return false
	}
	return true
}

// uploadActorID 取当前会话的用户 id(批次归属:换个人拿着 uploadId 也不能接着传)。
func uploadActorID(r *http.Request) string {
	if actor, ok := accessActorFromRequest(r); ok {
		return actor.UserID
	}
	return ""
}

// makeFSUploadBeginHandler POST /api/servers/{id}/fs/upload/begin —— 开一个上传批次。
//
// 请求 {dir, files:[{rel,size}]};响应逐文件给出 uploadId 与该任务在远端的已传偏移
// (全新批次恒为 0)。批次在这里拨好第一条连接并把需要的目录一次性建出来。
func makeFSUploadBeginHandler(svc target.Service, acc *access.Service, store *fsUploadStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !requireFSAccess(w, r, svc, acc, id) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, fsJSONBodyLimit)
		var req fsUploadBeginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if len(req.Files) == 0 || len(req.Files) > fsUploadMaxFiles {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("files 数量需在 1..%d", fsUploadMaxFiles))
			return
		}
		if len(req.Dir) > fsPathMax {
			writeError(w, http.StatusBadRequest, "invalid_path", "目标目录过长")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), fsDataTimeout)
		defer cancel()

		ws, err := dialWorkspace(ctx, svc, id)
		if err != nil {
			writeFSError(w, err)
			return
		}
		dir, err := ws.Realpath(ctx, req.Dir)
		if err != nil {
			_ = ws.Close()
			writeFSError(w, err)
			return
		}

		// 先把相对落点全部消毒并算出绝对 dest —— 有一个非法就整批不落地,
		// 免得客户端拿到「一半成功」的批次再去猜哪半成了。
		type planned struct {
			ref  fsUploadFileRef
			dest string
		}
		plan := make([]planned, 0, len(req.Files))
		seen := map[string]bool{}
		parents := map[string]bool{}
		for _, f := range req.Files {
			rel, err := sanitizeRemoteRel(f.Rel)
			if err != nil {
				_ = ws.Close()
				writeError(w, http.StatusBadRequest, "invalid_path", "相对路径非法:"+err.Error())
				return
			}
			if f.Size < 0 {
				_ = ws.Close()
				writeError(w, http.StatusBadRequest, "bad_request", "size 不能为负")
				return
			}
			dest := path.Join(dir, rel)
			if seen[dest] {
				_ = ws.Close()
				writeError(w, http.StatusConflict, "duplicate_file", "同一批次里有重名文件:"+rel)
				return
			}
			seen[dest] = true
			parents[path.Dir(dest)] = true
			plan = append(plan, planned{ref: f, dest: dest})
		}

		// 目录一次性建齐(文件夹上传的整棵树在这里落地)。按深度升序,父目录先于子目录,
		// 一次 Mkdir 递归建到位就省掉后面每一层的重复往返。
		dirs := make([]string, 0, len(parents))
		for d := range parents {
			dirs = append(dirs, d)
		}
		sort.Slice(dirs, func(i, j int) bool {
			if di, dj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/"); di != dj {
				return di < dj
			}
			return dirs[i] < dirs[j]
		})
		for _, d := range dirs {
			if err := ws.Mkdir(ctx, d); err != nil {
				_ = ws.Close()
				writeFSError(w, err)
				return
			}
		}

		b := &fsUploadBatch{
			id:       newUploadToken(),
			serverID: id,
			userID:   uploadActorID(r),
			ws:       ws,
			wsName:   ws.Backend(),
			tasks:    map[string]*fsUploadTask{},
			dial: func(ctx context.Context) (target.Workspace, error) {
				return dialWorkspace(ctx, svc, id)
			},
		}
		if b.id == "" {
			_ = ws.Close()
			writeError(w, http.StatusInternalServerError, "internal", "无法生成上传会话标识")
			return
		}
		for _, p := range plan {
			tid := newUploadToken()
			if tid == "" {
				_ = ws.Close()
				writeError(w, http.StatusInternalServerError, "internal", "无法生成上传会话标识")
				return
			}
			// 隐藏临时名(点前缀):面板列目录看不见半成品,也不用担心用户手抖删掉它。
			tmp := path.Join(path.Dir(p.dest), ".pipewright-up-"+tid[:12]+".tmp")
			t := &fsUploadTask{id: tid, rel: p.ref.Rel, dest: p.dest, tmp: tmp, total: p.ref.Size}
			b.tasks[tid] = t
			b.order = append(b.order, tid)
		}
		if !store.add(b, fsUploadIdleTTL) {
			_ = ws.Close()
			writeError(w, http.StatusTooManyRequests, "upload_busy", "并发上传过多,稍后再试")
			return
		}

		writeJSON(w, http.StatusOK, fsUploadBatchDTO{
			OK: true, BatchID: b.id, Dir: dir, Backend: b.backend(), Files: b.snapshotTasks(),
		})
	}
}

// makeFSUploadChunkHandler PUT /api/servers/{id}/fs/upload/chunk?uploadId=&offset= —— 追加一块。
//
// 请求体是裸字节流(不是 multipart,不进平台本机磁盘)。offset 必须等于服务端已确认的
// 长度,否则 409 offset_mismatch —— 客户端去 status 问真实长度再续,这就是断点续传的全部。
// 半截块不回收:它已经落在远端 EOF 之前,下一块接在其后仍然拼得回完整原文。
func makeFSUploadChunkHandler(svc target.Service, acc *access.Service, store *fsUploadStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !requireFSAccess(w, r, svc, acc, id) {
			return
		}
		tid := r.URL.Query().Get("uploadId")
		b, t := store.lookup(tid)
		if b == nil || b.serverID != id {
			writeError(w, http.StatusNotFound, "upload_not_found", "上传会话不存在或已过期")
			return
		}
		if b.userID != "" && b.userID != uploadActorID(r) {
			writeError(w, http.StatusForbidden, "forbidden", "无权继续这个上传会话")
			return
		}
		if t == nil {
			writeError(w, http.StatusNotFound, "upload_not_found", "上传会话不存在或已过期")
			return
		}
		want, ok := parseOffset(r.URL.Query().Get("offset"))
		if !ok || want < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "offset 需是非负整数")
			return
		}
		if r.ContentLength > fsChunkMaxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "chunk_too_large", "单块过大")
			return
		}

		b.mu.Lock()
		if t.done || b.dropped {
			b.mu.Unlock()
			writeError(w, http.StatusConflict, "upload_closed", "该文件已落地或会话已结束")
			return
		}
		if t.busy {
			b.mu.Unlock()
			writeError(w, http.StatusConflict, "chunk_in_flight", "同一文件的前一块还没落地")
			return
		}
		t.busy = true
		b.mu.Unlock()
		defer func() {
			b.mu.Lock()
			t.busy = false
			b.mu.Unlock()
		}()

		b.touch(store, fsUploadIdleTTL)
		// 客户端断开不该拆掉整条批次连接(同批次别的文件还在传),所以用 WithoutCancel;
		// 请求体一断,读侧自然报错,块也就停在已落地的那儿。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), fsChunkTimeout)
		defer cancel()

		body := http.MaxBytesReader(w, r.Body, fsChunkMaxBytes)
		if err := b.alignOffset(ctx, t, want); err != nil {
			var mis *fsOffsetMismatch
			if errors.As(err, &mis) {
				writeJSON(w, http.StatusConflict, fsUploadBatchDTO{
					OK: false, BatchID: b.id, Offset: mis.offset,
					Err: fsErr(w, "offset_mismatch", "偏移对不上,按返回的 offset 续传"),
				})
				return
			}
			writeFSError(w, err)
			return
		}

		ws, err := b.getWS(ctx)
		if err != nil {
			b.dropWS()
			writeFSError(w, err)
			return
		}
		end, err := ws.AppendChunk(ctx, t.tmp, t.offset, body)
		b.mu.Lock()
		if end > t.offset {
			t.offset = end
		}
		b.mu.Unlock()
		if err != nil {
			// 出错时无法确定对端连接还剩什么状态,直接放掉这条(下一块重拨一条)。
			b.dropWS()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				writeError(w, http.StatusRequestTimeout, "upload_timeout", "上传超时,请续传")
				return
			}
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "chunk_too_large", "单块过大")
				return
			}
			writeFSError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fsUploadBatchDTO{OK: true, BatchID: b.id, Offset: end, Files: []fsUploadTaskDTO{{UploadID: tid, Offset: end}}})
	}
}

// alignOffset 核对客户端申报的偏移。簿记一致就直接放行;不一致(重拨过、或客户端重发错了)
// 就以远端临时文件的实测长度为准,并把差值如实回给用户。
func (b *fsUploadBatch) alignOffset(ctx context.Context, t *fsUploadTask, want int64) error {
	b.mu.Lock()
	have := t.offset
	b.mu.Unlock()
	if have == want {
		return nil
	}
	ws, err := b.getWS(ctx)
	if err != nil {
		return err
	}
	st, err := ws.Stat(ctx, t.tmp)
	if err != nil {
		if errors.Is(err, target.ErrRemoteNotFound) {
			have = 0 // 一块都没落(或临时名被清了):从头开始。
		} else {
			b.dropWS()
			return err
		}
	} else {
		have = st.Size
	}
	b.mu.Lock()
	t.offset = have
	b.mu.Unlock()
	if have != want {
		return &fsOffsetMismatch{offset: have}
	}
	return nil
}

// fsOffsetMismatch 是内部信号(不是一条领域错误):只用来让 handler 走 409 + 真实偏移那条响应。
type fsOffsetMismatch struct{ offset int64 }

func (e *fsOffsetMismatch) Error() string { return "upload: 偏移不一致" }

// parseOffset 解析 query 里的偏移(空 = 0,便于客户端省参数)。
func parseOffset(s string) (int64, bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// lookup 按 uploadId 找批次与任务(批次与服务器/用户绑,handler 据此再核一遍)。
func (s *fsUploadStore) lookup(uploadID string) (*fsUploadBatch, *fsUploadTask) {
	if uploadID == "" {
		return nil, nil
	}
	s.mu.Lock()
	b := s.byTask[uploadID]
	s.mu.Unlock()
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	t := b.tasks[uploadID]
	b.mu.Unlock()
	return b, t
}

// makeFSUploadStatusHandler GET /api/servers/{id}/fs/upload/status?uploadId= —— 问一个文件传到哪了。
//
// 偏移一律取远端临时文件的**实测长度**,不拿进程里的簿记糊人:断线重连后簿记可能比
// 对端少几块,而客户端要的就是「远端真有多少字节」。
func makeFSUploadStatusHandler(svc target.Service, acc *access.Service, store *fsUploadStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !requireFSAccess(w, r, svc, acc, id) {
			return
		}
		b, t := store.lookup(r.URL.Query().Get("uploadId"))
		if b == nil || b.serverID != id || t == nil {
			writeError(w, http.StatusNotFound, "upload_not_found", "上传会话不存在或已过期")
			return
		}
		if b.userID != "" && b.userID != uploadActorID(r) {
			writeError(w, http.StatusForbidden, "forbidden", "无权查看这个上传会话")
			return
		}
		b.touch(store, fsUploadIdleTTL)

		ctx, cancel := context.WithTimeout(r.Context(), fsMetaTimeout)
		defer cancel()

		b.mu.Lock()
		dto := fsUploadTaskDTO{Rel: t.rel, UploadID: t.id, Path: t.dest, Offset: t.offset, Size: t.total, Done: t.done}
		b.mu.Unlock()
		if !dto.Done {
			ws, err := b.getWS(ctx)
			if err != nil {
				writeFSError(w, err)
				return
			}
			st, err := ws.Stat(ctx, t.tmp)
			switch {
			case err != nil && errors.Is(err, target.ErrRemoteNotFound):
				b.mu.Lock()
				t.offset = 0
				b.mu.Unlock()
				dto.Offset = 0
			case err != nil:
				b.dropWS()
				writeFSError(w, err)
				return
			default:
				b.mu.Lock()
				t.offset = st.Size
				b.mu.Unlock()
				dto.Offset = st.Size
			}
		}
		writeJSON(w, http.StatusOK, fsUploadBatchDTO{OK: true, BatchID: b.id, Backend: b.backend(), Offset: dto.Offset, Files: []fsUploadTaskDTO{dto}})
	}
}

// makeFSUploadCompleteHandler POST /api/servers/{id}/fs/upload/complete —— 临时名落成正式名。
//
// 落地顺序与编辑器覆盖写一致:若目标已存在,先把它原本的权限位搬到半成品上,再改名覆盖 ——
// 上传一个 600 的密钥不该被顺手放宽成 644。零字节文件没发过块,这里补一次空追加把文件建出来。
func makeFSUploadCompleteHandler(svc target.Service, acc *access.Service, store *fsUploadStore, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !requireFSAccess(w, r, svc, acc, id) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, fsJSONBodyLimit)
		var req fsUploadIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		b, t := store.lookup(req.UploadID)
		if b == nil || b.serverID != id || t == nil {
			writeError(w, http.StatusNotFound, "upload_not_found", "上传会话不存在或已过期")
			return
		}
		if b.userID != "" && b.userID != uploadActorID(r) {
			writeError(w, http.StatusForbidden, "forbidden", "无权完成这个上传会话")
			return
		}
		b.touch(store, fsUploadIdleTTL)

		b.mu.Lock()
		dest, tmp, total, offset, done := t.dest, t.tmp, t.total, t.offset, t.done
		b.mu.Unlock()
		if done {
			writeError(w, http.StatusConflict, "upload_closed", "该文件已落地或会话已结束")
			return
		}
		if total > 0 && offset < total {
			writeJSON(w, http.StatusConflict, fsUploadBatchDTO{
				OK: false, BatchID: b.id, Offset: offset,
				Err: fsErr(w, "incomplete_upload", fmt.Sprintf("只收到 %d/%d 字节,请续传", offset, total)),
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), fsDataTimeout)
		defer cancel()

		auditOp := func(ok bool, errText string) {
			e := audit.Entry{
				Action:     audit.ActionServerFS,
				TargetType: audit.TargetServer,
				TargetID:   id,
				// 只记动作/路径/体积:上传的是谁的什么文件,平台不该知道内容。
				Detail: map[string]any{"op": "upload", "path": dest, "bytes": offset, "ok": ok},
				IP:     clientIP(r),
			}
			if errText != "" {
				e.Detail["error"] = errText
			}
			recordAuditFromRequest(r, aud, ac, e)
		}

		ws, err := b.getWS(ctx)
		if err != nil {
			b.dropWS()
			auditOp(false, err.Error())
			writeFSError(w, err)
			return
		}
		if offset == 0 {
			// 空文件:一块都没发,临时名还不存在 —— 用同一次追加把它建出来,后面的改名才有主体。
			if _, err := ws.AppendChunk(ctx, tmp, 0, strings.NewReader("")); err != nil {
				b.dropWS()
				auditOp(false, err.Error())
				writeFSError(w, err)
				return
			}
		} else if _, err := ws.Stat(ctx, tmp); err != nil {
			b.dropWS()
			auditOp(false, err.Error())
			writeFSError(w, err)
			return
		}
		if st, err := ws.Stat(ctx, dest); err == nil && st.Mode != 0 {
			if cerr := ws.Chmod(ctx, tmp, st.Mode); cerr != nil {
				// 保不住原权限位不足以让上传失败:文件内容才是这次动作的主体,权限位另说。
				_ = cerr
			}
		}
		if err := ws.Rename(ctx, tmp, dest); err != nil {
			b.dropWS()
			auditOp(false, err.Error())
			writeFSError(w, err)
			return
		}
		b.mu.Lock()
		t.done = true
		b.mu.Unlock()
		auditOp(true, "")
		b.finishIfDone(store)

		writeJSON(w, http.StatusOK, fsResultDTO{OK: true, Path: dest, Bytes: offset, Backend: b.backend()})
	}
}

// makeFSUploadAbortHandler POST /api/servers/{id}/fs/upload/abort —— 撤一个文件,清掉半成品。
func makeFSUploadAbortHandler(svc target.Service, acc *access.Service, store *fsUploadStore, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !requireFSAccess(w, r, svc, acc, id) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, fsJSONBodyLimit)
		var req fsUploadIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		b, t := store.lookup(req.UploadID)
		if b == nil || b.serverID != id || t == nil {
			writeError(w, http.StatusNotFound, "upload_not_found", "上传会话不存在或已过期")
			return
		}
		if b.userID != "" && b.userID != uploadActorID(r) {
			writeError(w, http.StatusForbidden, "forbidden", "无权取消这个上传会话")
			return
		}
		b.mu.Lock()
		dest, tmp, offset := t.dest, t.tmp, t.offset
		t.done = true // 标成结束:后续块一律 409,计时回收也只剩「无半成品要删」。
		b.mu.Unlock()
		b.touch(store, fsUploadIdleTTL)

		ctx, cancel := context.WithTimeout(r.Context(), fsMetaTimeout)
		defer cancel()

		var err error
		if ws, gerr := b.getWS(ctx); gerr != nil {
			b.dropWS()
			err = gerr
		} else if err = ws.Remove(ctx, tmp); err != nil && !errors.Is(err, target.ErrRemoteNotFound) {
			b.dropWS()
		} else {
			err = nil
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServerFS,
			TargetType: audit.TargetServer,
			TargetID:   id,
			Detail:     map[string]any{"op": "upload_abort", "path": dest, "bytes": offset, "ok": err == nil},
			IP:         clientIP(r),
		})
		if err != nil {
			writeFSError(w, err)
			return
		}
		b.finishIfDone(store)
		writeJSON(w, http.StatusOK, fsResultDTO{OK: true, Path: dest})
	}
}

// finishIfDone 整批都落地/取消后收尾:从注册表摘掉、停计时、放掉那条连接。
// 批次没全完成就什么都不做 —— 留着连接给后面的文件用。
func (b *fsUploadBatch) finishIfDone(store *fsUploadStore) {
	if !b.allDone() {
		return
	}
	if got, ok := store.remove(b.id); ok && got == b {
		b.mu.Lock()
		idle := b.idle
		b.mu.Unlock()
		if idle != nil {
			idle.Stop()
		}
		b.closeWS()
	}
}
