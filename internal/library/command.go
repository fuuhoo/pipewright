package library

// command.go 是复用库「指令」的领域层:一条命名、可复用的 shell 命令。
//
// 与自定义节点(整份 Job 配置快照)互补 —— 指令只是一段命令正文,可被部署节点的
// 「部署前指令 / 重启命令 / 健康探测命令」直接引用。流水线存的是指令 ID,执行时才取正文:
// 库里的指令改一次,所有引用它的流水线跟着变,不用逐个项目重抄。
//
// 正文可含 {{变量}} 占位,执行期与手填命令走同一条渲染链路(运行参数 + 流水线变量)。
// 指令保存在平台库内,所以正文里不要写明文机密 —— 加密变量引用会在运行时被明确拒绝。
//
// 领域包无 init() 副作用、无包级重对象,避免抬高空载内存。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 指令领域错误。错误体不含任何命令正文。
var (
	// ErrCommandNotFound 表示引用的指令不存在。
	ErrCommandNotFound = errors.New("library: command not found")
	// ErrInvalidCommand 表示指令校验失败(名称空 / 正文空)。
	ErrInvalidCommand = errors.New("library: invalid command")
	// ErrCommandNameTaken 表示指令名已被占用(唯一约束)。
	ErrCommandNameTaken = errors.New("library: command name already in use")
)

// Command 是一条命名指令(名称 + 命令正文)。
type Command struct {
	ID          string
	Name        string
	Description string
	Body        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CommandInput 是创建/更新指令的入参。
type CommandInput struct {
	Name        string
	Description string
	Body        string
}

// CommandService 定义指令领域对外接口。
type CommandService interface {
	// List 返回所有指令(按名称升序),供复用库与部署节点选择器。
	List(ctx context.Context) ([]Command, error)
	// Get 返回单条指令;不存在 → ErrCommandNotFound。
	Get(ctx context.Context, id string) (*Command, error)
	// Create 校验(名称 / 正文非空)→ 持久化 → 回读。名称重复 → ErrCommandNameTaken。
	Create(ctx context.Context, in CommandInput) (*Command, error)
	// Update 同 Create 的校验,按 id 覆盖;不存在 → ErrCommandNotFound。
	Update(ctx context.Context, id string, in CommandInput) (*Command, error)
	// Delete 删除指令;不存在 → ErrCommandNotFound。
	Delete(ctx context.Context, id string) error
}

type commandService struct {
	db *sql.DB
}

// NewCommandService 构造 CommandService(经参数化 SQL 触库)。
func NewCommandService(db *sql.DB) CommandService {
	return &commandService{db: db}
}

func (s *commandService) List(ctx context.Context) ([]Command, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, body, created_at, updated_at
		 FROM library_commands ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("library: list commands: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Command, 0)
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library: iterate commands: %w", err)
	}
	return out, nil
}

func (s *commandService) Get(ctx context.Context, id string) (*Command, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, body, created_at, updated_at
		 FROM library_commands WHERE id = ?`, strings.TrimSpace(id))
	c, err := scanCommand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCommandNotFound
	}
	return c, err
}

func (s *commandService) Create(ctx context.Context, in CommandInput) (*Command, error) {
	name, body, err := validateCommand(in)
	if err != nil {
		return nil, err
	}

	id := uuid.NewString()
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO library_commands (id, name, description, body, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, strings.TrimSpace(in.Description), body, nowStr, nowStr,
	)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrCommandNameTaken
		}
		return nil, fmt.Errorf("library: insert command: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *commandService) Update(ctx context.Context, id string, in CommandInput) (*Command, error) {
	id = strings.TrimSpace(id)
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	name, body, err := validateCommand(in)
	if err != nil {
		return nil, err
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`UPDATE library_commands SET name = ?, description = ?, body = ?, updated_at = ?
		 WHERE id = ?`,
		name, strings.TrimSpace(in.Description), body, nowStr, id,
	)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrCommandNameTaken
		}
		return nil, fmt.Errorf("library: update command: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *commandService) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM library_commands WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("library: delete command: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("library: delete command rows: %w", err)
	}
	if n == 0 {
		return ErrCommandNotFound
	}
	return nil
}

// validateCommand 校验名称与正文非空;正文保留原始换行(命令是逐行的),只拒绝空白正文。
func validateCommand(in CommandInput) (name, body string, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", "", fmt.Errorf("%w: name must not be empty", ErrInvalidCommand)
	}
	body = strings.TrimRight(in.Body, "\r\n")
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("%w: command body must not be empty", ErrInvalidCommand)
	}
	return name, body, nil
}

func scanCommand(row rowScanner) (*Command, error) {
	var (
		id, name, desc, body   string
		createdStr, updatedStr string
	)
	if err := row.Scan(&id, &name, &desc, &body, &createdStr, &updatedStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("library: scan command: %w", err)
	}

	created, _ := time.Parse(time.RFC3339, createdStr)
	updated, _ := time.Parse(time.RFC3339, updatedStr)
	return &Command{
		ID:          id,
		Name:        name,
		Description: desc,
		Body:        body,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}, nil
}
