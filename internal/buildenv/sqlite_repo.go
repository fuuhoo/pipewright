package buildenv

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NewSQLiteRepo 构造 Repo(SQLite/MySQL 同实现,通过 schema 区分;差异由 DDL 兼容)。
func NewSQLiteRepo(db *sql.DB) Repo { return &sqlRepo{db: db} }

type sqlRepo struct {
	db *sql.DB
}

const isoLayout = "2006-01-02T15:04:05Z07:00" // RFC3339

func (r *sqlRepo) Create(env *BuildEnv) error {
	now := time.Now().UTC().Format(isoLayout)
	env.CreatedAt = parseTimeOrNow(now)
	env.UpdatedAt = env.CreatedAt
	_, err := r.db.Exec(`
		INSERT INTO build_envs (
			id, language, version, display_name, description,
			source_type, image, credential_id,
			image_check_status, image_check_error, image_checked_at,
			enabled, sort_order, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		env.ID, env.Language, env.Version, env.DisplayName, env.Description,
		env.SourceType, env.Image, env.CredentialID,
		env.ImageCheckStatus, env.ImageCheckError, nullTime(env.ImageCheckedAt),
		boolToInt(env.Enabled), env.SortOrder, env.CreatedBy,
		env.CreatedAt.Format(isoLayout), env.UpdatedAt.Format(isoLayout),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s/%s", ErrConflict, env.Language, env.Version)
		}
		return fmt.Errorf("buildenv: insert: %w", err)
	}
	return nil
}

func (r *sqlRepo) GetByID(id string) (*BuildEnv, error) {
	row := r.db.QueryRow(`SELECT `+buildEnvCols+` FROM build_envs WHERE id = ?`, id)
	return scanBuildEnv(row)
}

func (r *sqlRepo) GetByLanguageVersion(lang, ver string) (*BuildEnv, error) {
	row := r.db.QueryRow(
		`SELECT `+buildEnvCols+` FROM build_envs WHERE language = ? AND version = ?`,
		lang, ver,
	)
	env, err := scanBuildEnv(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return env, nil
}

func (r *sqlRepo) List(filter ListFilter) ([]*BuildEnv, error) {
	conds := []string{"1=1"}
	args := []any{}
	if filter.Language != "" {
		conds = append(conds, "language = ?")
		args = append(args, filter.Language)
	}
	if !filter.IncludeDisabled {
		conds = append(conds, "enabled = 1")
	}
	if filter.SourceType != "" {
		conds = append(conds, "source_type = ?")
		args = append(args, filter.SourceType)
	}
	q := `SELECT ` + buildEnvCols + ` FROM build_envs WHERE ` +
		strings.Join(conds, " AND ") + ` ORDER BY sort_order, language, version`
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("buildenv: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*BuildEnv, 0)
	for rows.Next() {
		env, err := scanBuildEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

func (r *sqlRepo) Update(env *BuildEnv) error {
	// 二次确认存在(SQLite RowsAffected 在值未变时返回 0,不能据此判 ErrNotFound)。
	var dummy int
	err := r.db.QueryRow(`SELECT 1 FROM build_envs WHERE id = ?`, env.ID).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("buildenv: update (lookup): %w", err)
	}
	env.UpdatedAt = time.Now().UTC()
	if _, err := r.db.Exec(`
		UPDATE build_envs SET
			language = ?, version = ?, display_name = ?, description = ?,
			source_type = ?, image = ?, credential_id = ?,
			image_check_status = ?, image_check_error = ?, image_checked_at = ?,
			enabled = ?, sort_order = ?, updated_at = ?
		WHERE id = ?
	`,
		env.Language, env.Version, env.DisplayName, env.Description,
		env.SourceType, env.Image, env.CredentialID,
		env.ImageCheckStatus, env.ImageCheckError, nullTime(env.ImageCheckedAt),
		boolToInt(env.Enabled), env.SortOrder, env.UpdatedAt.Format(isoLayout),
		env.ID,
	); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s/%s", ErrConflict, env.Language, env.Version)
		}
		return fmt.Errorf("buildenv: update: %w", err)
	}
	return nil
}

func (r *sqlRepo) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM build_envs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("buildenv: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *sqlRepo) SetEnabled(id string, enabled bool) error {
	// RowsAffected 在值未变时返回 0;改用 SELECT 1 二次确认存在。
	var dummy int
	err := r.db.QueryRow(`SELECT 1 FROM build_envs WHERE id = ?`, id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("buildenv: set enabled (lookup): %w", err)
	}
	if _, err := r.db.Exec(
		`UPDATE build_envs SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), time.Now().UTC().Format(isoLayout), id,
	); err != nil {
		return fmt.Errorf("buildenv: set enabled: %w", err)
	}
	return nil
}

func (r *sqlRepo) UpdateCheckStatus(id, status, errMsg, checkedAt string) error {
	// SQLite 的 RowsAffected 在"所有列值未变"时返回 0(不是真实行计数);
	// 不能据此判 ErrNotFound。改用 QueryRow 二次确认存在。
	var dummy int
	err := r.db.QueryRow(`SELECT 1 FROM build_envs WHERE id = ?`, id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("buildenv: update check status (lookup): %w", err)
	}
	if _, err := r.db.Exec(
		`UPDATE build_envs SET image_check_status = ?, image_check_error = ?, image_checked_at = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, checkedAt, time.Now().UTC().Format(isoLayout), id,
	); err != nil {
		return fmt.Errorf("buildenv: update check status: %w", err)
	}
	return nil
}

const buildEnvCols = `id, language, version, display_name, description,
	source_type, image, credential_id,
	image_check_status, image_check_error, image_checked_at,
	enabled, sort_order, created_by, created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanBuildEnv(sc scanner) (*BuildEnv, error) {
	var env BuildEnv
	var enabled, sortOrder int
	var description, errMsg, credentialID string
	var checkedAt sql.NullString
	var createdStr, updatedStr string
	if err := sc.Scan(
		&env.ID, &env.Language, &env.Version, &env.DisplayName, &description,
		&env.SourceType, &env.Image, &credentialID,
		&env.ImageCheckStatus, &errMsg, &checkedAt,
		&enabled, &sortOrder, &env.CreatedBy,
		&createdStr, &updatedStr,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("buildenv: scan: %w", err)
	}
	env.Description = description
	env.CredentialID = credentialID
	env.ImageCheckError = errMsg
	env.Enabled = enabled != 0
	env.SortOrder = sortOrder
	env.CreatedAt = parseTimeOrPanic(createdStr)
	env.UpdatedAt = parseTimeOrPanic(updatedStr)
	if checkedAt.Valid && checkedAt.String != "" {
		t := parseTimeOrPanic(checkedAt.String)
		env.ImageCheckedAt = &t
	}
	return &env, nil
}

func parseTimeOrPanic(s string) time.Time {
	t, err := time.Parse(isoLayout, s)
	if err != nil {
		// 容忍 RFC3339 毫秒格式
		t2, err2 := time.Parse(time.RFC3339Nano, s)
		if err2 != nil {
			panic(fmt.Sprintf("buildenv: parse time %q failed: %v", s, err))
		}
		return t2
	}
	return t
}

func parseTimeOrNow(s string) time.Time {
	t, err := time.Parse(isoLayout, s)
	if err == nil {
		return t
	}
	t2, err2 := time.Parse(time.RFC3339Nano, s)
	if err2 == nil {
		return t2
	}
	return time.Now().UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(isoLayout)
}

// isUniqueViolation 判断是否为 UNIQUE 约束失败(SQLite modernc + MySQL go-sql-driver)。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToUpper(err.Error())
	return strings.Contains(s, "UNIQUE") || strings.Contains(s, "DUPLICATE")
}