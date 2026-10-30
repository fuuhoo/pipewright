package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/library"
	"github.com/go-chi/chi/v5"
)

// library_commands.go 暴露复用库命名指令端点(部署节点的前 / 重启 / 探测命令可直接引用)。
//
//	GET    /api/library-commands       → 列指令(选择器 / 复用库画廊)
//	POST   /api/library-commands       → 建指令(name + 命令正文)
//	GET    /api/library-commands/{id}  → 取单指令(含正文)
//	PUT    /api/library-commands/{id}  → 覆盖指令
//	DELETE /api/library-commands/{id}  → 删指令
//
// 指令正文是**shell 片段**,支持 {{变量}} 占位;它不含 secret(正文会显示在复用库里,
// 也无法引用保险库凭据),运行时由执行器按运行参数 / 流水线变量渲染。
// 写操作过 auth + CSRF + 审计(detail 仅元数据:名称,绝无正文)。

// commandDTO 是指令对外 DTO(冻结契约;camelCase)。
type commandDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func toCommandDTO(c *library.Command) commandDTO {
	return commandDTO{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Body:        c.Body,
		CreatedAt:   c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// writeCommandError 把指令领域错误映射为契约错误码/状态码。
func writeCommandError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, library.ErrCommandNotFound):
		writeError(w, http.StatusNotFound, "command_not_found", "指令不存在")
	case errors.Is(err, library.ErrCommandNameTaken):
		writeError(w, http.StatusConflict, "command_name_taken", "指令名已被占用")
	case errors.Is(err, library.ErrInvalidCommand):
		writeError(w, http.StatusUnprocessableEntity, "invalid_command", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// commandReqBody 是建/改指令的请求体。
type commandReqBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

func (b commandReqBody) toInput() library.CommandInput {
	return library.CommandInput{Name: b.Name, Description: b.Description, Body: b.Body}
}

func makeListCommandsHandler(svc library.CommandService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "指令服务未初始化")
			return
		}
		list, err := svc.List(r.Context())
		if err != nil {
			writeCommandError(w, err)
			return
		}
		out := make([]commandDTO, 0, len(list))
		for i := range list {
			out = append(out, toCommandDTO(&list[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"commands": out})
	}
}

func makeGetCommandHandler(svc library.CommandService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "指令服务未初始化")
			return
		}
		c, err := svc.Get(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeCommandError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toCommandDTO(c))
	}
}

func makeCreateCommandHandler(svc library.CommandService, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "指令服务未初始化")
			return
		}
		if !requireLibraryPerm(w, r, permLibraryEdit) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<18) // 256KB
		var req commandReqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		c, err := svc.Create(r.Context(), req.toInput())
		if err != nil {
			writeCommandError(w, err)
			return
		}
		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      auditActor,
			Action:     audit.ActionCommandCreate,
			TargetType: audit.TargetCommand,
			TargetID:   c.ID,
			Detail:     map[string]any{"name": c.Name},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toCommandDTO(c))
	}
}

func makeUpdateCommandHandler(svc library.CommandService, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "指令服务未初始化")
			return
		}
		if !requireLibraryPerm(w, r, permLibraryEdit) {
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<18)
		var req commandReqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		c, err := svc.Update(r.Context(), id, req.toInput())
		if err != nil {
			writeCommandError(w, err)
			return
		}
		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      auditActor,
			Action:     audit.ActionCommandUpdate,
			TargetType: audit.TargetCommand,
			TargetID:   c.ID,
			Detail:     map[string]any{"name": c.Name},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toCommandDTO(c))
	}
}

func makeDeleteCommandHandler(svc library.CommandService, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "指令服务未初始化")
			return
		}
		if !requireLibraryPerm(w, r, permLibraryEdit) {
			return
		}
		id := chi.URLParam(r, "id")
		if err := svc.Delete(r.Context(), id); err != nil {
			writeCommandError(w, err)
			return
		}
		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      auditActor,
			Action:     audit.ActionCommandDelete,
			TargetType: audit.TargetCommand,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}
