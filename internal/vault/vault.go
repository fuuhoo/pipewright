// Package vault 是凭据加密保险库的领域层。
//
// 凭据明文(令牌/SSH 私钥/镜像账号)经 NaCl secretbox 加密后,仅以**密文 BLOB**
// 入库;DB 中绝无明文与 PEM 字段(AC-SEC-01)。master key 来自环境变量/文件
// (见 internal/config),绝不入库。对外(REST)只暴露掩码 + 元数据;明文仅经
// Vault.Get 在进程内供领域调用方(克隆/SSH/推镜像)取用。
//
// master key 缺失时 Vault 进入「未配置」态:所有操作返回 ErrVaultUnconfigured,
// 平台仍正常启动(不 panic)。
package vault

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// 凭据类型枚举(DB 存 snake_case 字串;JSON camelCase 字段)。
const (
	TypeGitToken = "git_token"
	TypeGitHTTP  = "git_http" // Git HTTPS 用户名 + 密码或 Token
	// TypeGitSSH 是 git over SSH 的部署私钥:username=登录用户(常为 git),secret=无口令 PEM 私钥。
	// 与 TypeSSHKey(主机登录用)分开,是为了让「可用于代码仓」的凭据在前端可被单独筛出。
	TypeGitSSH      = "git_ssh"
	TypeSSHKey      = "ssh_key"
	TypeRegistry    = "registry"
	TypeSSHPassword = "ssh_password" // SSH 登录密码(非 PEM);SSH 层据 looksLikePEM 自动按密码认证
	TypeDNSToken    = "dns_token"    // DNS 提供商 API 凭据(单字串:Cloudflare token / 「ID,Secret」);掩码走 default 全打点
)

// 领域错误。错误体永不含明文/密文/master key。
var (
	// ErrVaultUnconfigured 表示未配置 master key,保险库不可用。
	ErrVaultUnconfigured = errors.New("vault: master key not configured")
	// ErrNotFound 表示凭据不存在。
	ErrNotFound = errors.New("vault: credential not found")
	// ErrInvalidType 表示类型枚举非法。
	ErrInvalidType = errors.New("vault: invalid credential type")
	// ErrEmptySecret 表示创建/轮换时 secret 为空。
	ErrEmptySecret = errors.New("vault: secret must not be empty")
	// ErrEmptyName 表示名称为空。
	ErrEmptyName = errors.New("vault: name must not be empty")
	// ErrCredentialInUse 表示凭据正被项目/流水线配置引用,不可删除(防悬挂引用)。
	ErrCredentialInUse = errors.New("vault: credential in use")
	// ErrInvalidGitSSHKey 表示 git_ssh 凭据的 secret 不是可解析的 PEM 私钥。
	ErrInvalidGitSSHKey = errors.New("vault: git ssh 凭据需要有效的 PEM 私钥")
	// ErrEncryptedGitSSHKey 表示私钥带口令(passphrase)。无人值守克隆无法交互解锁,
	// 需先用 `ssh-keygen -p -N ""` 去掉口令再录入。
	ErrEncryptedGitSSHKey = errors.New("vault: git ssh 凭据不支持带口令的私钥")
)

// Credential 是对外可见的凭据视图:只含掩码 + 元数据,绝无明文/密文。
type Credential struct {
	ID          string
	Name        string
	Type        string
	Scope       string
	OwnerID     string // v6.2 阶段 5:personal 凭据=users.id;global 时为空
	Username    string
	MaskedValue string
	Description string // v6.2 阶段 5:admin 备注
	Enabled     bool   // v6.2 阶段 5:软禁用开关
	DisabledBy  string // v6.2 阶段 5:禁用操作者 users.id
	DisabledAt  *time.Time
	CreatedBy   string // v6.2 阶段 5:创建者 users.id
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

// CreateInput 是创建凭据的入参(含明文 secret,不被持久化为明文)。
type CreateInput struct {
	Name      string
	Type      string
	Scope     string
	Username  string
	Secret    string
	OwnerID   string // v6.2 阶段 5:personal 凭据必填 users.id
	CreatedBy string // v6.2 阶段 5:创建者 users.id;global 凭据即管理员行 id
}

// UpdateInput 是更新凭据的入参;指针字段为 nil 表示不修改。
// 给 Secret 即轮换密钥(重新加密)。
type UpdateInput struct {
	Name        *string
	Scope       *string
	Username    *string
	Description *string // admin 备注
	Secret      *string
	OwnerID     *string // v6.2 阶段 5:personal 必填
}

type GitAuth struct {
	Username string
	Token    string
}

// RegistryAuth 是镜像仓库登录对(ACR / Nexus / Harbor 都是用户名 + 密码或访问凭证)。
// Username 为空 = 匿名或纯 token 场景,调用方据此决定是否发起登录。
type RegistryAuth struct {
	Username string
	Password string
}

// Vault 定义保险库领域对外接口。(-er 约定的同类:领域聚合用 Vault)
type Vault interface {
	// Create 加密并持久化新凭据,返回掩码视图(不含明文)。
	Create(in CreateInput) (*Credential, error)
	// List 返回所有凭据的掩码视图(无明文/密文)。
	List() ([]Credential, error)
	// Get 解密并返回指定凭据的明文,**仅供进程内领域调用**;同时更新 last_used_at。
	Get(id string) (string, error)
	GetGitAuth(id string) (GitAuth, error)
	// GetRegistryAuth 取镜像仓库凭据并折成 (用户名, 密码):用户名栏优先,读不到才按存量
	// "user:password" 约定从明文里切(见 ResolveRegistryAuth)。同时更新 last_used_at。
	GetRegistryAuth(id string) (RegistryAuth, error)
	// Reveal 解密并返回指定凭据的明文,**不更新 last_used_at**(供脱敏登记等「只读取值、非真正使用」
	// 的场景:把凭据明文登记进 Masker 以便日志/诊断/通知出网前替换为 [MASKED],不应算「最近使用」)。
	// 未配置 master key → ErrVaultUnconfigured;不存在 → ErrNotFound;解密失败 → ErrDecrypt(不泄漏)。
	Reveal(id string) (string, error)
	// Exists 仅校验凭据是否存在,**不解密、不刷新 last_used_at**(供保存配置时校验引用,
	// 避免「仅编辑也算用过」的语义失真与无谓解密)。未配置 master key → ErrVaultUnconfigured。
	Exists(id string) (bool, error)
	// Update 改名/改作用域/轮换密钥;返回更新后的掩码视图。
	Update(id string, in UpdateInput) (*Credential, error)
	// Delete 删除凭据。
	Delete(id string) error
	// SealSecret 用 master key 加密任意明文,返回密文 BLOB(nonce||box),供其它
	// 领域(如 webhook 签名密钥)复用同一 secretbox 加密原语。未配置 master key
	// 时返回 ErrVaultUnconfigured。绝不持久化/日志明文。
	SealSecret(plaintext []byte) ([]byte, error)
	// OpenSecret 解密 SealSecret 产出的密文 BLOB;认证失败返回 ErrDecrypt(不泄漏细节)。
	// 未配置 master key 时返回 ErrVaultUnconfigured。
	OpenSecret(sealed []byte) ([]byte, error)

	// ---- v6.2 阶段 5:RBAC 接口(接受 Actor 参数)----
	// ListWithActor 按 Actor 推导的 ListFilter 列凭据。admin 看所有;user 仅自己的 personal。
	ListWithActor(actor *Actor, f ListFilter) ([]Credential, error)
	// GetWithActor 取凭据明文(同时更新 last_used_at);非 admin 不可越权。
	GetWithActor(actor *Actor, id string) (string, error)
	// RevealWithActor 同上但不更新 last_used_at。
	RevealWithActor(actor *Actor, id string) (string, error)
	// DeleteWithActor 仅 admin 或凭据 owner 可删。
	DeleteWithActor(actor *Actor, id string) error
	// UpdateWithActor 仅 admin 或凭据 owner 可改;非 admin 不能把 personal 升成 global。
	UpdateWithActor(actor *Actor, id string, in UpdateInput) (*Credential, error)
	// DisableWithActor admin 禁用 personal 凭据(enabled=0+disabled_by+disabled_at);
	// user 调 → ErrForbidden;global 凭据 → ErrForbidden。
	DisableWithActor(actor *Actor, id string) error
	// EnableWithActor admin 恢复被禁用的 personal 凭据。
	EnableWithActor(actor *Actor, id string) error
}

// service 是 store 支撑的 Vault 实现。master key 为 nil 时为「未配置」态。
type service struct {
	db  *sql.DB
	key *[keySize]byte // nil ⇒ 未配置
}

// New 构造 Vault。key 为 nil 时进入未配置态(操作返回 ErrVaultUnconfigured)。
// 不在此做任何加密/重计算(避免抬高空载内存)。
func New(db *sql.DB, key *[keySize]byte) Vault {
	return &service{db: db, key: key}
}

// Configured 报告保险库是否已配置 master key(供 HTTP 层快速短路/友好提示)。
func (s *service) configured() bool { return s.key != nil }

// validateType 校验类型枚举。
func validateType(t string) error {
	switch t {
	case TypeGitToken, TypeGitHTTP, TypeGitSSH, TypeSSHKey, TypeRegistry, TypeSSHPassword, TypeDNSToken:
		return nil
	default:
		return ErrInvalidType
	}
}

// validateSecret 在加密入库前按类型把关 secret 形态。git_ssh 必须是可解析的**无口令**
// PEM 私钥:带口令的私钥在无人值守克隆里解不开,晚失败会表现为莫名的「克隆失败」,
// 因此在录入这一刻就拒绝并给出可操作提示。
func validateSecret(credType, secret string) error {
	if credType != TypeGitSSH {
		return nil
	}
	if _, err := ssh.ParseRawPrivateKey([]byte(secret)); err != nil {
		var pme *ssh.PassphraseMissingError
		if errors.As(err, &pme) {
			return ErrEncryptedGitSSHKey
		}
		return ErrInvalidGitSSHKey
	}
	return nil
}

func (s *service) Create(in CreateInput) (*Credential, error) {
	if !s.configured() {
		return nil, ErrVaultUnconfigured
	}
	if err := validateType(in.Type); err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, ErrEmptyName
	}
	if in.Secret == "" {
		return nil, ErrEmptySecret
	}
	// personal 凭据没有归属就等于没人能再看见它,故在入库前拒掉,而不是静默落成全局。
	if in.Scope == "personal" && in.OwnerID == "" {
		return nil, ErrOwnerRequired
	}
	if err := validateSecret(in.Type, in.Secret); err != nil {
		return nil, err
	}

	sealed, err := seal(s.key, []byte(in.Secret))
	if err != nil {
		return nil, err
	}
	masked := maskWithUsername(in.Type, in.Username, in.Secret)

	id := uuid.NewString()
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	ownerID, createdBy := strings.TrimSpace(in.OwnerID), strings.TrimSpace(in.CreatedBy)

	_, err = s.db.Exec(
		`INSERT INTO credentials (id, name, type, scope, username, owner_id, created_by, enabled,
		                         ciphertext, masked_value, last_used_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, NULL, ?, ?)`,
		id, in.Name, in.Type, in.Scope, strings.TrimSpace(in.Username), ownerID, createdBy,
		sealed, masked, nowStr, nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("vault: insert credential: %w", err)
	}

	return &Credential{
		ID:          id,
		Name:        in.Name,
		Type:        in.Type,
		Scope:       in.Scope,
		OwnerID:     ownerID,
		Username:    strings.TrimSpace(in.Username),
		MaskedValue: masked,
		Enabled:     true,
		CreatedBy:   createdBy,
		LastUsedAt:  nil,
		CreatedAt:   now,
	}, nil
}

func (s *service) List() ([]Credential, error) {
	if !s.configured() {
		return nil, ErrVaultUnconfigured
	}
	rows, err := s.db.Query(
		"SELECT " + credentialViewCols + " FROM credentials ORDER BY created_at DESC, id",
	)
	if err != nil {
		return nil, fmt.Errorf("vault: list credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()

	creds := make([]Credential, 0)
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		creds = append(creds, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vault: iterate credentials: %w", err)
	}
	return creds, nil
}

func (s *service) Get(id string) (string, error) {
	if !s.configured() {
		return "", ErrVaultUnconfigured
	}
	var sealed []byte
	var enabled int
	err := s.db.QueryRow(`SELECT ciphertext, enabled FROM credentials WHERE id = ?`, id).Scan(&sealed, &enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("vault: get credential: %w", err)
	}
	if enabled != 1 {
		return "", ErrDisabledCredential
	}
	plaintext, err := open(s.key, sealed)
	if err != nil {
		return "", err // ErrDecrypt:不泄漏明文/密钥
	}
	// 更新最近使用时间(尽力而为:失败不影响取用)。
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.db.Exec(`UPDATE credentials SET last_used_at = ? WHERE id = ?`, now, id)
	return string(plaintext), nil
}

// GetGitAuth 取克隆用凭据(用户名 + 令牌/私钥明文),同时算一次「最近使用」。
func (s *service) GetGitAuth(id string) (GitAuth, error) {
	username, secret, err := s.takePair(id)
	if err != nil {
		return GitAuth{}, err
	}
	return GitAuth{Username: username, Token: secret}, nil
}

// GetRegistryAuth 取镜像仓库凭据明文并折成登录对(规则见 ResolveRegistryAuth)。
func (s *service) GetRegistryAuth(id string) (RegistryAuth, error) {
	username, secret, err := s.takePair(id)
	if err != nil {
		return RegistryAuth{}, err
	}
	return ResolveRegistryAuth(username, secret), nil
}

// takePair 按 id 解密「用户名 + 明文」一对,并刷新 last_used_at(即真正用了一次)。
// git 与镜像仓库两类凭据同表同存法,共用这一条取用路径。
func (s *service) takePair(id string) (username, secret string, err error) {
	if !s.configured() {
		return "", "", ErrVaultUnconfigured
	}
	var sealed []byte
	var enabled int
	err = s.db.QueryRow(`SELECT username, ciphertext, enabled FROM credentials WHERE id = ?`, id).Scan(&username, &sealed, &enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", fmt.Errorf("vault: get credential: %w", err)
	}
	if enabled != 1 {
		return "", "", ErrDisabledCredential
	}
	plaintext, err := open(s.key, sealed)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.db.Exec(`UPDATE credentials SET last_used_at = ? WHERE id = ?`, now, id)
	return username, string(plaintext), nil
}

// ResolveRegistryAuth 把凭据的用户名列 + 明文口令折成镜像仓库登录对:
//   - 用户名栏非空 → 整串明文都是密码(含冒号的密码不再被从中间截断);
//   - 用户名栏为空 → 回退到旧约定「把 user:password 整串写在口令栏里」,按首个冒号切。
//     凭据表单曾经只有口令一栏,那是当时唯一能表达用户名的方式,存量凭据照此仍可用。
//
// 切不出用户名时 Username 为空,调用方据此跳过登录(匿名库 / 只需 token 的场景)。
func ResolveRegistryAuth(username, secret string) RegistryAuth {
	if u := strings.TrimSpace(username); u != "" {
		return RegistryAuth{Username: u, Password: secret}
	}
	if i := strings.Index(secret, ":"); i > 0 {
		return RegistryAuth{Username: secret[:i], Password: secret[i+1:]}
	}
	return RegistryAuth{Password: secret}
}

func (s *service) Reveal(id string) (string, error) {
	if !s.configured() {
		return "", ErrVaultUnconfigured
	}
	var sealed []byte
	var enabled int
	err := s.db.QueryRow(`SELECT ciphertext, enabled FROM credentials WHERE id = ?`, id).Scan(&sealed, &enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("vault: reveal credential: %w", err)
	}
	if enabled != 1 {
		return "", ErrDisabledCredential
	}
	plaintext, err := open(s.key, sealed)
	if err != nil {
		return "", err // ErrDecrypt:不泄漏明文/密钥
	}
	return string(plaintext), nil
}

func (s *service) Exists(id string) (bool, error) {
	if !s.configured() {
		return false, ErrVaultUnconfigured
	}
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM credentials WHERE id = ?`, id).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("vault: check credential exists: %w", err)
	}
	return true, nil
}

func (s *service) Update(id string, in UpdateInput) (*Credential, error) {
	if !s.configured() {
		return nil, ErrVaultUnconfigured
	}

	// 先取出当前行(需 type 以便在轮换 secret 时重算掩码)。
	var name, credType, scope, username, masked, description string
	err := s.db.QueryRow(
		`SELECT name, type, scope, username, masked_value, description FROM credentials WHERE id = ?`, id,
	).Scan(&name, &credType, &scope, &username, &masked, &description)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("vault: load credential: %w", err)
	}

	var newSealed []byte
	rotate := false
	if in.Name != nil {
		if *in.Name == "" {
			return nil, ErrEmptyName
		}
		name = *in.Name
	}
	if in.Scope != nil {
		scope = *in.Scope
	}
	if in.Username != nil {
		username = strings.TrimSpace(*in.Username)
	}
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	if in.Secret != nil {
		if *in.Secret == "" {
			return nil, ErrEmptySecret
		}
		if err := validateSecret(credType, *in.Secret); err != nil {
			return nil, err
		}
		sealed, err := seal(s.key, []byte(*in.Secret))
		if err != nil {
			return nil, err
		}
		newSealed = sealed
		masked = maskWithUsername(credType, username, *in.Secret)
		rotate = true
	}
	// 只改用户名、不轮换口令的 registry 凭据:掩码里的账号名也得跟着换,
	// 否则列表上挂着的是上一个账号名。用户名列空(存量 user:password 写法)时保留原掩码。
	if !rotate && credType == TypeRegistry && username != "" {
		masked = username + " " + maskDots
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	if rotate {
		_, err = s.db.Exec(
			`UPDATE credentials SET name = ?, scope = ?, username = ?, description = ?, ciphertext = ?, masked_value = ?, updated_at = ? WHERE id = ?`,
			name, scope, username, description, newSealed, masked, nowStr, id,
		)
	} else {
		_, err = s.db.Exec(
			`UPDATE credentials SET name = ?, scope = ?, username = ?, description = ?, masked_value = ?, updated_at = ? WHERE id = ?`,
			name, scope, username, description, masked, nowStr, id,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("vault: update credential: %w", err)
	}

	// 回读完整视图(含 last_used_at/created_at)。
	return s.getView(id)
}

func (s *service) Delete(id string) error {
	if !s.configured() {
		return ErrVaultUnconfigured
	}
	// 在用守卫:被流水线配置 secret 引用的凭据不可删,否则悬挂引用使该项目配置再不可保存。
	// pipeline_settings 的引用以 JSON 存(无外键),此处显式 LIKE 检查;credentialId 为 UUID,
	// 无子串歧义。表不存在(迁移未应用)时 QueryRow 报错,跳过此检查、退回普通删除。
	var refCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pipeline_settings WHERE build_json LIKE ? OR environments_json LIKE ? OR steps_json LIKE ?`,
		"%"+id+"%", "%"+id+"%", "%"+id+"%",
	).Scan(&refCount); err == nil && refCount > 0 {
		return ErrCredentialInUse
	}
	res, err := s.db.Exec(`DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		// projects.credential_id 等外键(ON DELETE RESTRICT)引用 → 约束错误,映射为在用。
		if isConstraintErr(err) {
			return ErrCredentialInUse
		}
		return fmt.Errorf("vault: delete credential: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// isConstraintErr 判断错误是否为 SQLite 约束失败(外键/RESTRICT 等;modernc 文本匹配)。
func isConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "FOREIGN KEY") || strings.Contains(msg, "CONSTRAINT")
}

func (s *service) SealSecret(plaintext []byte) ([]byte, error) {
	if !s.configured() {
		return nil, ErrVaultUnconfigured
	}
	return seal(s.key, plaintext)
}

func (s *service) OpenSecret(sealed []byte) ([]byte, error) {
	if !s.configured() {
		return nil, ErrVaultUnconfigured
	}
	return open(s.key, sealed)
}

// getView 回读单条凭据的掩码视图。
func (s *service) getView(id string) (*Credential, error) {
	row := s.db.QueryRow("SELECT "+credentialViewCols+" FROM credentials WHERE id = ?", id)
	c, err := scanCredential(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...any) error
}

// credentialViewCols 是掩码视图的统一列序;所有读视图的 SELECT 与 scanCredential 必须同序。
// 刻意不含 ciphertext:密文只由 Get/GetGitAuth/Reveal 单独取。
const credentialViewCols = `id, name, type, scope, owner_id, username, masked_value, description,
		                    enabled, disabled_by, disabled_at, created_by, last_used_at, created_at`

// scanCredential 把一行扫描为 Credential 掩码视图(永不读 ciphertext)。
func scanCredential(sc scanner) (*Credential, error) {
	var c Credential
	var lastUsedStr, disabledAtStr sql.NullString
	var createdStr string
	var enabled int
	if err := sc.Scan(
		&c.ID, &c.Name, &c.Type, &c.Scope, &c.OwnerID, &c.Username, &c.MaskedValue, &c.Description,
		&enabled, &c.DisabledBy, &disabledAtStr, &c.CreatedBy, &lastUsedStr, &createdStr,
	); err != nil {
		return nil, err
	}
	c.Enabled = enabled == 1
	created, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("vault: parse created_at: %w", err)
	}
	c.CreatedAt = created
	if lastUsedStr.Valid && lastUsedStr.String != "" {
		t, err := time.Parse(time.RFC3339, lastUsedStr.String)
		if err != nil {
			return nil, fmt.Errorf("vault: parse last_used_at: %w", err)
		}
		c.LastUsedAt = &t
	}
	if disabledAtStr.Valid && disabledAtStr.String != "" {
		t, err := time.Parse(time.RFC3339, disabledAtStr.String)
		if err != nil {
			return nil, fmt.Errorf("vault: parse disabled_at: %w", err)
		}
		c.DisabledAt = &t
	}
	return &c, nil
}

// ---- v6.2 阶段 5:Actor-aware 凭据访问方法 ----
// ListWithActor 按 Actor 推导的 ListFilter 列凭据。
//   - Actor=nil 或 Role="admin" → 按请求的 f 过滤(可见 global+所有 personal)
//   - Role="user"             → global + 自己的 personal(强制覆盖 f.OwnerID)
//
// 两个 scope 开关都关 → 返回空集(不是「不过滤」)。默认值 fail open 会让一个忘了
// 填 filter 的调用点把全部凭据列出来,这里把它堵在 SQL 生成之前。
func (s *service) ListWithActor(actor *Actor, f ListFilter) ([]Credential, error) {
	if !s.configured() {
		return nil, ErrVaultUnconfigured
	}
	ef := effectiveFilter(actor, f)
	if !ef.IncludeGlobal && !ef.IncludePersonal {
		return []Credential{}, nil
	}

	conds := []string{"1=1"}
	var args []any
	// 「共享」判定用 `scope <> 'personal'` 而不是 `scope = 'global'`:0058 之前建的老凭据
	// scope 是空串(那时没有 scope 概念),按等值判定会让它们在列表里凭空消失。
	const sharedCond = "scope <> 'personal'"
	switch {
	case ef.IncludeGlobal && ef.IncludePersonal && ef.OwnerID != "":
		// 普通用户视角:共享的 + 自己的 personal。两者的并集必须写在同一条 SQL 里,
		// 否则「both + owner」会被误当成「不过滤 owner」而泄漏他人 personal。
		conds = append(conds, "("+sharedCond+" OR (scope = 'personal' AND owner_id = ?))")
		args = append(args, ef.OwnerID)
	case ef.IncludeGlobal && ef.IncludePersonal:
		// 两个 scope 都要且不限归属:admin / 系统调用。
	case ef.IncludeGlobal:
		conds = append(conds, sharedCond)
	case ef.IncludePersonal:
		conds = append(conds, "scope = 'personal'")
		if ef.OwnerID != "" {
			conds = append(conds, "owner_id = ?")
			args = append(args, ef.OwnerID)
		}
	}
	if !ef.IncludeDisabled {
		conds = append(conds, "enabled = 1")
	}
	query := "SELECT " + credentialViewCols + " FROM credentials WHERE " +
		strings.Join(conds, " AND ") + " ORDER BY created_at DESC, id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("vault: list with actor: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Credential, 0)
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetWithActor 取凭据明文,并按 Actor 校验所有权(非 admin 只能取自己的)。
//   - 凭据 scope='global' 且 Actor 非 admin → ErrAccessDenied
//   - 凭据 scope='personal' 且 owner_id != actor.UserID → ErrAccessDenied
//   - 凭据不存在 → ErrNotFound
//
// 同时更新 last_used_at(等同 Get 的语义)。
func (s *service) GetWithActor(actor *Actor, id string) (string, error) {
	cred, err := s.getView(id)
	if err != nil {
		return "", err
	}
	if err := authorizeRead(actor, cred); err != nil {
		return "", err
	}
	// 复用 Get 的解密+记 last_used_at 路径
	return s.Get(id)
}

// RevealWithActor 同上但用 Reveal(不更新 last_used_at)。
func (s *service) RevealWithActor(actor *Actor, id string) (string, error) {
	cred, err := s.getView(id)
	if err != nil {
		return "", err
	}
	if err := authorizeRead(actor, cred); err != nil {
		return "", err
	}
	return s.Reveal(id)
}

// DeleteWithActor:admin 可删任何;user 仅可删自己的 personal。
func (s *service) DeleteWithActor(actor *Actor, id string) error {
	cred, err := s.getView(id)
	if err != nil {
		return err
	}
	if err := authorizeWrite(actor, cred); err != nil {
		return err
	}
	return s.Delete(id)
}

// DisableWithActor admin 禁用 personal 凭据(v6.2 §3.4 矩阵);不能删。
//   - Actor.Role != "admin" → ErrForbidden
//   - 凭据 scope != "personal" → ErrForbidden(global 凭据不应被个别 disable)
//
// 禁用只改元数据:密文原样留着,重新启用即可恢复。取用路径(Get/GetGitAuth/Reveal)
// 对 enabled=0 直接报 ErrDisabledCredential,所以禁用会立刻让引用它的流水线失败——
// 这正是「立刻停用一把可疑令牌」想要的效果。
func (s *service) DisableWithActor(actor *Actor, id string) error {
	return s.setEnabledWithActor(actor, id, false)
}

// EnableWithActor admin 恢复被禁用的 personal 凭据。
func (s *service) EnableWithActor(actor *Actor, id string) error {
	return s.setEnabledWithActor(actor, id, true)
}

func (s *service) setEnabledWithActor(actor *Actor, id string, enabled bool) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	cred, err := s.getView(id)
	if err != nil {
		return err
	}
	if cred.Scope != "personal" {
		// 用 ErrForbidden 而非裸 error:HTTP 层据此返回 403 而不是 500。
		return ErrForbidden
	}
	// 写的是 enabled 列:1=可用。别把「disable」当成 1 写进去。
	n := 1
	disabledBy, disabledAt := "", ""
	if !enabled {
		n = 0
		disabledBy = actor.UserID
		disabledAt = time.Now().UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(
		`UPDATE credentials SET enabled = ?, disabled_by = ?, disabled_at = ?, updated_at = ? WHERE id = ?`,
		n, disabledBy, disabledAt, now, id,
	); err != nil {
		return fmt.Errorf("vault: set credential enabled: %w", err)
	}
	return nil
}

// UpdateWithActor 改凭据元数据/轮换密钥,非 admin 仅可改自己的 personal。
// 归属与开关不走本方法:owner_id 只由 admin 通过 disable/enable 与建号路径改动。
func (s *service) UpdateWithActor(actor *Actor, id string, in UpdateInput) (*Credential, error) {
	cred, err := s.getView(id)
	if err != nil {
		return nil, err
	}
	if err := authorizeWrite(actor, cred); err != nil {
		return nil, err
	}
	if !actor.IsAdmin() && in.Scope != nil && *in.Scope != "personal" {
		// 普通用户把自己的凭据升成 global = 把私钥共享给全站,必须拒绝。
		return nil, ErrForbidden
	}
	return s.Update(id, in)
}

// authorizeRead 校验 actor 可读该凭据。
//   - nil actor → 视为 admin(系统调用),允许所有
//   - admin → 允许所有
//   - user + scope=global → ErrForbidden
//   - user + scope=personal + owner_id != actor.UserID → ErrAccessDenied
func authorizeRead(actor *Actor, cred *Credential) error {
	if actor == nil || actor.IsAdmin() {
		return nil
	}
	if cred.Scope == "personal" && cred.OwnerID == actor.UserID {
		return nil
	}
	if cred.Scope == "global" {
		return ErrForbidden
	}
	return ErrAccessDenied
}

// authorizeWrite 同上,但 user 仅可写 personal 且 owner 自己。
func authorizeWrite(actor *Actor, cred *Credential) error {
	if actor == nil || actor.IsAdmin() {
		return nil
	}
	if cred.Scope == "personal" && cred.OwnerID == actor.UserID {
		return nil
	}
	return ErrAccessDenied
}
