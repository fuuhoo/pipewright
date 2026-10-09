// Package httpapi — 构建环境管理端点(v6.2 §3.1 + 阶段 9)。
//
// 路由:
//
//	GET    /api/admin/build-envs                   → List(过滤)
//	POST   /api/admin/build-envs                   → Create
//	GET    /api/admin/build-envs/{id}              → GetByID
//	PUT    /api/admin/build-envs/{id}              → Update
//	DELETE /api/admin/build-envs/{id}              → Delete
//	POST   /api/admin/build-envs/{id}/toggle      → SetEnabled(P0 #4 三态)
//	POST   /api/admin/build-envs/{id}/check       → 手动镜像检查
//	POST   /api/admin/build-envs/{id}/pull        → 手动 pull 镜像
//	POST   /api/admin/build-envs/check-all        → 一键检查全部
//	POST   /api/admin/build-envs/check-batch      → 检查所选(多选/全选)
//	GET    /api/admin/build-envs/export           → 整表导出(yaml/json,可含禁用)
//	POST   /api/admin/build-envs/import           → 整表导入(skip/overwrite,dryRun 预览)
//	GET    /api/build-envs                       → 已启用环境(普通用户可访问)
//	GET    /api/build-envs/languages              → 已启用环境去重语言列表(普通用户可访问)
//
// 所有写端点套 RequireAdmin + CSRF;所有 admin 端点走 RequireAdmin。
// 普通用户端点走 RequireUser(阶段 9 接入 router)。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/buildenv"
	"github.com/go-chi/chi/v5"
)

// buildEnvDTO 是构建环境对外响应体(camelCase;不含任何密文)。
type buildEnvDTO struct {
	ID               string  `json:"id"`
	Language         string  `json:"language"`
	Version          string  `json:"version"`
	DisplayName      string  `json:"displayName"`
	Description      string  `json:"description"`
	SourceType       string  `json:"sourceType"`
	Image            string  `json:"image"`
	CredentialID     string  `json:"credentialId"`
	ImageCheckStatus string  `json:"imageCheckStatus"`
	ImageCheckError  string  `json:"imageCheckError"`
	ImageCheckedAt   *string `json:"imageCheckedAt"`
	Enabled          bool    `json:"enabled"`
	SortOrder        int     `json:"sortOrder"`
	CreatedBy        string  `json:"createdBy"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
}

func toBuildEnvDTO(e *buildenv.BuildEnv) buildEnvDTO {
	if e == nil {
		return buildEnvDTO{}
	}
	var checkedAt *string
	if e.ImageCheckedAt != nil {
		s := e.ImageCheckedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		checkedAt = &s
	}
	return buildEnvDTO{
		ID:               e.ID,
		Language:         e.Language,
		Version:          e.Version,
		DisplayName:      e.DisplayName,
		Description:      e.Description,
		SourceType:       e.SourceType,
		Image:            e.Image,
		CredentialID:     e.CredentialID,
		ImageCheckStatus: e.ImageCheckStatus,
		ImageCheckError:  e.ImageCheckError,
		ImageCheckedAt:   checkedAt,
		Enabled:          e.Enabled,
		SortOrder:        e.SortOrder,
		CreatedBy:        e.CreatedBy,
		CreatedAt:        e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        e.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// makeListBuildEnvsHandler GET /api/admin/build-envs。
// query: language / sourceType / includeDisabled。
func makeListBuildEnvsHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		q := r.URL.Query()
		includeDisabled := q.Get("includeDisabled") == "1" || q.Get("includeDisabled") == "true"
		list, err := svc.List(buildenv.ListFilter{
			Language:        q.Get("language"),
			SourceType:      q.Get("sourceType"),
			IncludeDisabled: includeDisabled,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询构建环境失败")
			return
		}
		out := make([]buildEnvDTO, 0, len(list))
		for _, e := range list {
			out = append(out, toBuildEnvDTO(e))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// publicBuildEnvDTO 是流水线编辑器用的预置目录条目:只保留「选哪个环境」需要的字段。
// credential_id / image_check_error / created_by 属于管理员运维信息,不出现在普通用户响应里。
type publicBuildEnvDTO struct {
	ID               string `json:"id"`
	Language         string `json:"language"`
	Version          string `json:"version"`
	DisplayName      string `json:"displayName"`
	Description      string `json:"description"`
	SourceType       string `json:"sourceType"`
	Image            string `json:"image"`
	ImageCheckStatus string `json:"imageCheckStatus"`
	SortOrder        int    `json:"sortOrder"`
}

// makeListEnabledBuildEnvsHandler GET /api/build-envs?language=node(普通用户可访问)。
// 无论调用方是否 admin,一律只返回已启用条目 —— 禁用环境不能成为流水线的新选项。
func makeListEnabledBuildEnvsHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		list, err := svc.List(buildenv.ListFilter{Language: r.URL.Query().Get("language")})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询构建环境失败")
			return
		}
		out := make([]publicBuildEnvDTO, 0, len(list))
		for _, e := range list {
			out = append(out, publicBuildEnvDTO{
				ID:               e.ID,
				Language:         e.Language,
				Version:          e.Version,
				DisplayName:      e.DisplayName,
				Description:      e.Description,
				SourceType:       e.SourceType,
				Image:            e.Image,
				ImageCheckStatus: e.ImageCheckStatus,
				SortOrder:        e.SortOrder,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeListBuildEnvLanguagesHandler GET /api/build-envs/languages(普通用户可访问)。
func makeListBuildEnvLanguagesHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		langs, err := svc.ListEnabledLanguages(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询语言列表失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"languages": langs})
	}
}

// makeCreateBuildEnvHandler POST /api/admin/build-envs。
func makeCreateBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req buildEnvCreateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		env := &buildenv.BuildEnv{
			Language:     req.Language,
			Version:      req.Version,
			DisplayName:  req.DisplayName,
			Description:  req.Description,
			SourceType:   req.SourceType,
			Image:        req.Image,
			CredentialID: req.CredentialID,
			SortOrder:    req.SortOrder,
			Enabled:      req.Enabled,
		}
		if actor := actorFromRequest(r, ac); actor != "" && actor != auditActorFallback {
			env.CreatedBy = actor
		}
		out, err := svc.Create(env)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCreate,
			TargetType: audit.TargetBuildEnv,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language": out.Language, "version": out.Version,
				"image": out.Image, "enabled": out.Enabled,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toBuildEnvDTO(out))
	}
}

type buildEnvCreateReq struct {
	Language     string `json:"language"`
	Version      string `json:"version"`
	DisplayName  string `json:"displayName"`
	Description  string `json:"description"`
	SourceType   string `json:"sourceType"`
	Image        string `json:"image"`
	CredentialID string `json:"credentialId"`
	SortOrder    int    `json:"sortOrder"`
	Enabled      bool   `json:"enabled"`
}

// makeUpdateBuildEnvHandler PUT /api/admin/build-envs/{id}。
func makeUpdateBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req buildEnvCreateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		env := &buildenv.BuildEnv{
			ID:           id,
			Language:     req.Language,
			Version:      req.Version,
			DisplayName:  req.DisplayName,
			Description:  req.Description,
			SourceType:   req.SourceType,
			Image:        req.Image,
			CredentialID: req.CredentialID,
			SortOrder:    req.SortOrder,
			Enabled:      req.Enabled,
		}
		out, err := svc.Update(env)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvUpdate,
			TargetType: audit.TargetBuildEnv,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language": out.Language, "version": out.Version,
				"image": out.Image, "enabled": out.Enabled,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, toBuildEnvDTO(out))
	}
}

// makeDeleteBuildEnvHandler DELETE /api/admin/build-envs/{id}。
func makeDeleteBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if err := svc.Delete(id); err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvDelete,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeGetBuildEnvHandler GET /api/admin/build-envs/{id}。
func makeGetBuildEnvHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		out, err := svc.GetByID(id)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toBuildEnvDTO(out))
	}
}

// makeToggleBuildEnvHandler POST /api/admin/build-envs/{id}/toggle。
// body: {"enabled": true|false};响应:{"enabled":bool,"status":string}
func makeToggleBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if err := svc.SetEnabled(id, req.Enabled); err != nil {
			writeBuildEnvError(w, err)
			return
		}
		// 回读用于响应。
		out, err := svc.GetByID(id)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvToggle,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			Detail:     map[string]any{"enabled": out.Enabled},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": out.Enabled,
			"status":  out.ImageCheckStatus,
		})
	}
}

// makeCheckBuildEnvHandler POST /api/admin/build-envs/{id}/check。
func makeCheckBuildEnvHandler(svc *buildenv.Service, c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		// 加载 env → 调 Check。
		ctx := r.Context()
		env, err := svc.GetByID(id)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		res, err := c.Check(ctx, env)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				writeError(w, 499, "client_closed", "客户端断开")
				return
			}
			writeError(w, http.StatusInternalServerError, "check_failed", "检查失败: "+err.Error())
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCheck,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			Detail:     map[string]any{"status": res.Status, "error": res.Error},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, res)
	}
}

// makePullBuildEnvHandler POST /api/admin/build-envs/{id}/pull。
// 异步:立即返回 202 + status=checking,前端轮询列表直到 available/unavailable。
func makePullBuildEnvHandler(c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		res, err := c.ManualPull(r.Context(), id)
		if err != nil {
			if errors.Is(err, buildenv.ErrConflict) {
				writeError(w, http.StatusConflict, "pull_in_progress", "该镜像正在拉取中,请等待完成")
				return
			}
			writeError(w, http.StatusInternalServerError, "pull_failed", "拉取失败: "+err.Error())
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvPull,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			Detail:     map[string]any{"status": res.Status, "queued": true},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusAccepted, res)
	}
}

// makeCheckAllBuildEnvsHandler POST /api/admin/build-envs/check-all。
func makeCheckAllBuildEnvsHandler(c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		ok, total, err := c.CheckAll(r.Context())
		if err != nil {
			if errors.Is(err, buildenv.ErrCheckAllRunning) {
				writeError(w, http.StatusConflict, "check_all_running", "一键检查正在进行中,请等待完成")
				return
			}
			writeError(w, http.StatusInternalServerError, "check_all_failed", "一键检查失败: "+err.Error())
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCheck,
			TargetType: audit.TargetBuildEnv,
			TargetID:   "all",
			Detail:     map[string]any{"ok": ok, "total": total},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "total": total})
	}
}

// makeExportBuildEnvsHandler GET /api/admin/build-envs/export?format=yaml|json&includeDisabled=1。
//
// 整表快照(默认含禁用条目,`includeDisabled=0` 只要启用)。文件里只有可移植字段:
// 不含 credential_id(密文引用,跨实例只会指向不存在的行),也不含 image_check_*
// (可用性由本机 docker/registry 决定)。读操作,不写审计。
func makeExportBuildEnvsHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		q := r.URL.Query()
		format := strings.ToLower(strings.TrimSpace(q.Get("format")))
		if format == "" {
			format = buildenv.TransferFormatYAML
		}
		includeDisabled := !(q.Get("includeDisabled") == "0" || strings.EqualFold(q.Get("includeDisabled"), "false"))
		items, err := svc.ExportItems(includeDisabled)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		content, err := buildenv.Marshal(items, format)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", buildenv.TransferMessage(err))
			return
		}
		ext := "yaml"
		if format == buildenv.TransferFormatJSON {
			ext = "json"
		}
		filename := "build-envs-" + time.Now().UTC().Format("20060102-150405") + "." + ext
		// 一律按文本下发:前端取文本自己造 Blob 下载,JSON 被 fetch 解析成对象就拿不到原文了。
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}
}

// buildEnvImportReq 是导入请求体。
//   - content:文件正文(YAML 或 JSON)
//   - format:  yaml(默认)/ json
//   - mode:    skip(默认,已存在保留)/ overwrite(已存在原地更新)
//   - dryRun:  true 只出计划,不写库
type buildEnvImportReq struct {
	Content string `json:"content"`
	Format  string `json:"format"`
	Mode    string `json:"mode"`
	DryRun  bool   `json:"dryRun"`
}

// makeImportBuildEnvsHandler POST /api/admin/build-envs/import。
//
// 逐行处理:坏行记 failed 不影响好行(整单不回滚);响应是逐行结果 + 汇总。
// dryRun 与 pipeline/import 的 save:false 同义 —— 先给管理员看清「会动哪些行」再决定落库。
// 落库路径要跑镜像检查(文件想启用而库里未检查时),耗时与「一键检查」同量级。
func makeImportBuildEnvsHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB:整表条目 + 描述文本足够宽裕
		var req buildEnvImportReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		format := strings.ToLower(strings.TrimSpace(req.Format))
		if format == "" {
			format = buildenv.TransferFormatYAML
		}
		items, err := buildenv.ParseTransfer(req.Content, format)
		if err != nil {
			code := "invalid_input"
			if strings.Contains(err.Error(), "解析失败") {
				code = "invalid_content"
			}
			writeError(w, http.StatusBadRequest, code, buildenv.TransferMessage(err))
			return
		}
		mode := strings.ToLower(strings.TrimSpace(req.Mode))
		if mode == "" {
			mode = buildenv.ImportModeSkip
		}
		if mode != buildenv.ImportModeSkip && mode != buildenv.ImportModeOverwrite {
			writeError(w, http.StatusBadRequest, "invalid_mode", "mode 只能是 skip 或 overwrite")
			return
		}
		report, err := svc.Import(r.Context(), items, mode, req.DryRun)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		if !req.DryRun {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionBuildEnvImport,
				TargetType: audit.TargetBuildEnv,
				TargetID:   "import",
				Detail: map[string]any{
					"mode":    mode,
					"created": report.Summary.Created,
					"updated": report.Summary.Updated,
					"skipped": report.Summary.Skipped,
					"failed":  report.Summary.Failed,
					"total":   report.Summary.Total,
				},
				IP: clientIP(r),
			})
		}
		writeJSON(w, http.StatusOK, report)
	}
}

// maxBatchCheckIDs 一次批量检查的行数上限(与全选场景对齐,再大就该走「一键检查」)。
const maxBatchCheckIDs = 200

// makeCheckBatchBuildEnvsHandler POST /api/admin/build-envs/check-batch。
// body {"ids":[...]}:只检查选中的行(前端多选/全选),上限 200 条。
// 同步等全部检查完成,与「一键检查」同量级耗时;并发由 checker 的信号量兜住。
func makeCheckBatchBuildEnvsHandler(c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		ids := make([]string, 0, len(req.IDs))
		seen := map[string]bool{}
		for _, id := range req.IDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			writeError(w, http.StatusBadRequest, "empty_selection", "请先勾选要检查的构建环境")
			return
		}
		if len(ids) > maxBatchCheckIDs {
			writeError(w, http.StatusBadRequest, "too_many_ids",
				fmt.Sprintf("一次最多检查 %d 个(当前 %d 个)", maxBatchCheckIDs, len(ids)))
			return
		}
		results, err := c.CheckBatch(r.Context(), ids)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "check_failed", "批量检查失败")
			return
		}
		ok := 0
		for _, res := range results {
			if res.Status == buildenv.StatusAvailable || res.Status == buildenv.StatusPullable {
				ok++
			}
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCheck,
			TargetType: audit.TargetBuildEnv,
			TargetID:   "batch",
			Detail:     map[string]any{"selected": len(ids), "ok": ok},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"items":   results,
			"ok":      ok,
			"total":   len(results),
			"skipped": len(results) - ok,
		})
	}
}

// writeBuildEnvError 把领域错误映射到 HTTP 状态码。
// 注意:SetEnabled 等入口返回 *buildenv.ValidationError(带 Code),不是 Err* sentinel,
// 必须先匹配 ValidationError 再匹配 sentinel,否则会落 default 500。
func writeBuildEnvError(w http.ResponseWriter, err error) {
	var ve *buildenv.ValidationError
	if errors.As(err, &ve) {
		switch ve.Code {
		case "ENV_NOT_FOUND":
			writeError(w, http.StatusNotFound, ve.Code, ve.Message)
		case "ENV_DISABLED", "IMAGE_UNAVAILABLE", "IMAGE_NOT_CHECKED":
			// 三态校验(P0 #4)拒绝:409 Conflict,消息已人读化。
			writeError(w, http.StatusConflict, ve.Code, ve.Message)
		default:
			writeError(w, http.StatusBadRequest, ve.Code, ve.Message)
		}
		return
	}
	switch {
	case errors.Is(err, buildenv.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, buildenv.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, buildenv.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, buildenv.ErrImageUnavail):
		writeError(w, http.StatusConflict, "image_unavailable", err.Error())
	case errors.Is(err, buildenv.ErrImageNotChecked):
		writeError(w, http.StatusConflict, "image_not_checked", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
