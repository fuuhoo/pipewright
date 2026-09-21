package configprofile

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const isoLayout = "2006-01-02T15:04:05Z07:00"

// NewSQLiteRepo 构造 Repo。
func NewSQLiteRepo(db *sql.DB) Repo { return &sqlRepo{db: db} }

type sqlRepo struct {
	db *sql.DB
}

const cpCols = `id, language, config_type, name, target_path, file_path, content,
	is_default, is_builtin, description, enabled, created_by, created_at, updated_at`

func (r *sqlRepo) Create(p *ConfigProfile) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	now := time.Now().UTC().Format(isoLayout)
	p.UpdatedAt = now
	p.CreatedAt = now

	_, err := r.db.Exec(`
		INSERT INTO config_profiles (
			id, language, config_type, name, target_path, file_path, content,
			is_default, is_builtin, description, enabled, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		p.ID, p.Language, p.ConfigType, p.Name, p.TargetPath, p.FilePath, p.Content,
		boolToInt(p.IsDefault), boolToInt(p.IsBuiltin), p.Description,
		boolToInt(p.Enabled), p.CreatedBy, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s/%s/%s", ErrConflict, p.Language, p.ConfigType, p.Name)
		}
		return fmt.Errorf("configprofile: insert: %w", err)
	}
	return nil
}

func (r *sqlRepo) GetByID(id string) (*ConfigProfile, error) {
	row := r.db.QueryRow(`SELECT `+cpCols+` FROM config_profiles WHERE id = ?`, id)
	return scanProfile(row)
}

func (r *sqlRepo) List(filter ListFilter) ([]*ConfigProfile, error) {
	conds := []string{"1=1"}
	args := []any{}
	if filter.Language != "" {
		conds = append(conds, "language = ?")
		args = append(args, filter.Language)
	}
	if filter.ConfigType != "" {
		conds = append(conds, "config_type = ?")
		args = append(args, filter.ConfigType)
	}
	if !filter.IncludeBuiltin {
		conds = append(conds, "is_builtin = 0")
	}
	if !filter.IncludeDisabled {
		conds = append(conds, "enabled = 1")
	}
	q := `SELECT ` + cpCols + ` FROM config_profiles WHERE ` +
		strings.Join(conds, " AND ") + ` ORDER BY language, config_type, name`
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("configprofile: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*ConfigProfile, 0)
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *sqlRepo) Update(p *ConfigProfile) error {
	// 二次确认存在(SQLite RowsAffected 值未变时返回 0,不可据此判 ErrNotFound)。
	var dummy int
	err := r.db.QueryRow(`SELECT 1 FROM config_profiles WHERE id = ?`, p.ID).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("configprofile: update (lookup): %w", err)
	}
	p.UpdatedAt = time.Now().UTC().Format(isoLayout)
	if _, err := r.db.Exec(`
		UPDATE config_profiles SET
			language = ?, config_type = ?, name = ?, target_path = ?,
			file_path = ?, content = ?,
			is_default = ?, description = ?, enabled = ?, updated_at = ?
		WHERE id = ?
	`,
		p.Language, p.ConfigType, p.Name, p.TargetPath,
		p.FilePath, p.Content,
		boolToInt(p.IsDefault), p.Description, boolToInt(p.Enabled), p.UpdatedAt,
		p.ID,
	); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s/%s/%s", ErrConflict, p.Language, p.ConfigType, p.Name)
		}
		return fmt.Errorf("configprofile: update: %w", err)
	}
	return nil
}

func (r *sqlRepo) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM config_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("configprofile: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(sc scanner) (*ConfigProfile, error) {
	var p ConfigProfile
	var isDefault, isBuiltin, enabled int
	if err := sc.Scan(
		&p.ID, &p.Language, &p.ConfigType, &p.Name, &p.TargetPath, &p.FilePath, &p.Content,
		&isDefault, &isBuiltin, &p.Description, &enabled, &p.CreatedBy,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("configprofile: scan: %w", err)
	}
	p.IsDefault = isDefault != 0
	p.IsBuiltin = isBuiltin != 0
	p.Enabled = enabled != 0
	return &p, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToUpper(err.Error())
	return strings.Contains(s, "UNIQUE") || strings.Contains(s, "DUPLICATE")
}