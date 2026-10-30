// Package servicecatalog 是「归属服务」的领域层。
//
// 归属服务是项目上的一个纯展示 / 筛选属性,与资源分组(权限轴)正交:
//   - 一个项目最多挂一个服务(projects.service_id),服务可挂多个项目;
//   - 不参与任何权限判定 —— 看得见项目就看得见它的服务,反之亦然;
//   - service_id=” 表示未归属(存量数据默认态,不做迁移)。
//
// 表名为 services,而不是沿用 resource_groups 命名:它不承载可见性,别让名字误导判定。
// 删服务要求「先把项目移出」(ErrInUse):projects.service_id 没有外键,
// 静默删服务会留下一堆指向不存在服务的行,筛选下拉里就多了个查无此人的档位。
package servicecatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/google/uuid"
)

// 领域错误。HTTP 层按 errors.Is 映射状态码。
var (
	// ErrNotFound 表示服务不存在。
	ErrNotFound = errors.New("servicecatalog: not found")
	// ErrEmptyName 表示服务名为空。
	ErrEmptyName = errors.New("servicecatalog: name must not be empty")
	// ErrDuplicateName 表示服务名已被占用(下拉与筛选项按名字展示,重名分不清)。
	ErrDuplicateName = errors.New("servicecatalog: duplicate name")
	// ErrInUse 表示仍有项目挂在该服务下,须先移出再删。
	ErrInUse = errors.New("servicecatalog: still referenced by projects")
)

// Service 是归属服务。ProjectCount 只在 List 时填充,仅用于管理页展示与删前提示。
type Service struct {
	ID           string
	Name         string
	Description  string
	ProjectCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateInput 是建服务的入参。
type CreateInput struct {
	Name        string
	Description string
}

// UpdateInput 是改服务的入参;指针为 nil 表示不修改。
type UpdateInput struct {
	Name        *string
	Description *string
}

// Catalog 是归属服务的领域服务。
type Catalog struct {
	db *sql.DB
}

// New 构造 Catalog。
func New(db *sql.DB) *Catalog { return &Catalog{db: db} }

// List 按名字升序返回全部服务;项目数用 LEFT JOIN 一次算出来,不给每条再查一次。
func (c *Catalog) List(ctx context.Context) ([]Service, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT s.id, s.name, s.description, s.created_at, s.updated_at,
		        COUNT(p.id)
		 FROM services s
		 LEFT JOIN projects p ON p.service_id = s.id
		 GROUP BY s.id, s.name, s.description, s.created_at, s.updated_at
		 ORDER BY s.name ASC, s.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("servicecatalog: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Service{}
	for rows.Next() {
		var (
			s          Service
			createdStr string
			updatedStr string
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &createdStr, &updatedStr, &s.ProjectCount); err != nil {
			return nil, fmt.Errorf("servicecatalog: scan: %w", err)
		}
		if s.CreatedAt, err = time.Parse(time.RFC3339, createdStr); err != nil {
			return nil, fmt.Errorf("servicecatalog: parse created_at: %w", err)
		}
		if s.UpdatedAt, err = time.Parse(time.RFC3339, updatedStr); err != nil {
			return nil, fmt.Errorf("servicecatalog: parse updated_at: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("servicecatalog: iterate: %w", err)
	}
	return out, nil
}

// Get 返回单个服务(含项目数)。
func (c *Catalog) Get(ctx context.Context, id string) (*Service, error) {
	var (
		s          Service
		createdStr string
		updatedStr string
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT s.id, s.name, s.description, s.created_at, s.updated_at,
		        (SELECT COUNT(1) FROM projects p WHERE p.service_id = s.id)
		 FROM services s WHERE s.id = ?`, id,
	).Scan(&s.ID, &s.Name, &s.Description, &createdStr, &updatedStr, &s.ProjectCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("servicecatalog: get: %w", err)
	}
	if s.CreatedAt, err = time.Parse(time.RFC3339, createdStr); err != nil {
		return nil, fmt.Errorf("servicecatalog: parse created_at: %w", err)
	}
	if s.UpdatedAt, err = time.Parse(time.RFC3339, updatedStr); err != nil {
		return nil, fmt.Errorf("servicecatalog: parse updated_at: %w", err)
	}
	return &s, nil
}

// Create 建一个服务。名字唯一,空名拒。
func (c *Catalog) Create(ctx context.Context, in CreateInput) (*Service, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrEmptyName
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO services (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, strings.TrimSpace(in.Description), now, now,
	); err != nil {
		if store.IsUniqueErr(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("servicecatalog: insert: %w", err)
	}
	return c.Get(ctx, id)
}

// Update 改名 / 改说明;nil 字段不动。
func (c *Catalog) Update(ctx context.Context, id string, in UpdateInput) (*Service, error) {
	cur, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	name := cur.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, ErrEmptyName
		}
	}
	desc := cur.Description
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE services SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		name, desc, time.Now().UTC().Format(time.RFC3339), id,
	); err != nil {
		if store.IsUniqueErr(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("servicecatalog: update: %w", err)
	}
	return c.Get(ctx, id)
}

// Delete 删服务;仍有项目挂着时拒绝,由调用方提示先移出。
func (c *Catalog) Delete(ctx context.Context, id string) error {
	if _, err := c.Get(ctx, id); err != nil {
		return err
	}
	var used int
	if err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM projects WHERE service_id = ?`, id).Scan(&used); err != nil {
		return fmt.Errorf("servicecatalog: count projects: %w", err)
	}
	if used > 0 {
		return ErrInUse
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id); err != nil {
		return fmt.Errorf("servicecatalog: delete: %w", err)
	}
	return nil
}
