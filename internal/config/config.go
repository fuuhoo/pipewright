// Package config loads platform runtime configuration from the environment.
//
// 单管理员实例的最小配置:监听地址、SQLite 数据库文件路径,以及凭据保险库的
// master key 来源。master key 缺失时保险库进入「未配置」态,平台仍正常启动。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MasterKeyLen 是凭据保险库 master key 的字节长度(NaCl secretbox 需 32B)。
const MasterKeyLen = 32

// Config 是平台运行期配置。
type Config struct {
	// Addr 是 HTTP 监听地址,如 ":8080"。
	Addr string
	// DBDriver 选择数据库后端:"sqlite"(默认)或 "mysql"。
	DBDriver string
	// DBDSN 是数据库连接串。mysql 必填(go-sql-driver DSN);
	// sqlite 可选,留空则回退到 DBPath。
	DBDSN string
	// DBPath 是 SQLite 数据库文件路径(向后兼容,driver=sqlite 时使用)。
	DBPath string
	// AdminUsername 是管理员用户名(首次启动引导用);默认 "admin"。
	AdminUsername string
	// AdminPassword 是管理员初始口令(首次启动引导用);已存在管理员时忽略。
	// 注:此字段仅用于首次引导,不持久化,不入日志。
	AdminPassword string

	// v6.2 阶段 14:构建环境/配置资源/UI YAML 校验 相关配置。
	DataDir         string // 配置资源文件存储目录(默认 ./data)
	ConfigUploadMax int64  // 配置文件上传大小上限(字节;默认 1<<20 = 1MB)
	// PIPEWRIGHT_ENFORCE_BUILD_ENV(默认 true):是否校验 job 镜像来源于 build_envs。
	EnforceBuildEnv bool
	// PIPEWRIGHT_UI_ONLY(默认 true):是否禁用 YAML 直接编辑(导入/导出保留)。
	UIOnly bool

	// v6.2 阶段 14:镜像检查器配置。
	AutoCheckOnStart     bool          // 服务启动后自动检查所有 build_env 镜像(默认 true)
	CheckConcurrency     int           // 并发上限(默认 10)
	CheckTimeout         time.Duration // 单次 inspect 超时(默认 60s)
	PullTimeoutMultiply  int           // pull 超时 = CheckTimeout × N(默认 4)
	AllowUncheckedEnable bool          // 紧急逃生:允许 unchecked 状态启用 env(默认 false)

	// Git over SSH:主机密钥校验策略。空串=不校验(自托管内网 Git 默认可用);
	// 生产收紧时填 known_hosts 文件路径,未知主机将拒绝连接。
	GitSSHKnownHosts string
}

// v6.2 阶段 14 配置默认值。
const (
	DefaultDataDir           = "./data"
	DefaultConfigUploadMax   = 1 << 20 // 1MB
	DefaultCheckTimeoutSec   = 60
	DefaultCheckConcurrency  = 10
	DefaultPullTimeoutMult   = 4
)

// Load 从环境变量读取配置,缺失项回退到合理默认值。
func Load() Config {
	return Config{
		Addr:          getenv("PIPEWRIGHT_ADDR", ":8080"),
		DBDriver:      getenv("PIPEWRIGHT_DB_DRIVER", "sqlite"),
		DBDSN:         os.Getenv("PIPEWRIGHT_DB_DSN"),
		DBPath:        getenv("PIPEWRIGHT_DB", "pipewright.db"),
		AdminUsername: getenv("PIPEWRIGHT_ADMIN_USERNAME", "admin"),
		AdminPassword: os.Getenv("PIPEWRIGHT_ADMIN_PASSWORD"), // 无默认值,空串表示未设置

		DataDir:         getenv("PIPEWRIGHT_DATA_DIR", DefaultDataDir),
		ConfigUploadMax: getenvInt64("PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE", DefaultConfigUploadMax),
		EnforceBuildEnv: getenvBool("PIPEWRIGHT_ENFORCE_BUILD_ENV", true),
		UIOnly:          getenvBool("PIPEWRIGHT_UI_ONLY", true),

		AutoCheckOnStart:     getenvBool("PIPEWRIGHT_AUTO_CHECK_ON_START", true),
		CheckConcurrency:     getenvInt("PIPEWRIGHT_CHECK_CONCURRENCY", DefaultCheckConcurrency),
		CheckTimeout:         time.Duration(getenvInt("PIPEWRIGHT_CHECK_TIMEOUT_SECONDS", DefaultCheckTimeoutSec)) * time.Second,
		PullTimeoutMultiply:  getenvInt("PIPEWRIGHT_PULL_TIMEOUT_MULTIPLIER", DefaultPullTimeoutMult),
		AllowUncheckedEnable: getenvBool("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE", false),

		GitSSHKnownHosts: os.Getenv("PIPEWRIGHT_GIT_SSH_KNOWN_HOSTS"),
	}
}

// StoreConfig 由 Config 推导出 store.OpenWithConfig 所需的 (driver, dsn)。
// mysql:必须显式提供 DBDSN,否则返回错误(配置缺失应被察觉)。
// sqlite:优先 DBDSN,否则用 DBPath(向后兼容)。
func (c Config) StoreConfig() (driver, dsn string, err error) {
	switch c.DBDriver {
	case "mysql":
		if strings.TrimSpace(c.DBDSN) == "" {
			return "", "", fmt.Errorf("config: PIPEWRIGHT_DB_DRIVER=mysql requires PIPEWRIGHT_DB_DSN")
		}
		return "mysql", c.DBDSN, nil
	case "", "sqlite":
		if strings.TrimSpace(c.DBDSN) != "" {
			return "sqlite", c.DBDSN, nil
		}
		return "sqlite", c.DBPath, nil
	default:
		return "", "", fmt.Errorf("config: unknown PIPEWRIGHT_DB_DRIVER %q (want sqlite|mysql)", c.DBDriver)
	}
}

// ErrNoMasterKey 表示既未设置 PIPEWRIGHT_MASTER_KEY 也未设置 _FILE。
// 调用方据此让保险库进入「未配置」态,而非视为致命错误。
var ErrNoMasterKey = errors.New("config: no master key configured")

// LoadMasterKey 读取凭据保险库 master key。
//
// 优先级:PIPEWRIGHT_MASTER_KEY(base64 编码的 32 字节)> PIPEWRIGHT_MASTER_KEY_FILE
// (文件内容为 base64,允许尾随空白/换行)。两者皆未设置时返回 (nil, ErrNoMasterKey),
// 由调用方决定进入未配置态(不 panic)。
//
// 解码失败或长度不为 32B 时返回非 nil error(配置错误,应被察觉)。
// 错误信息绝不含 key 内容。
func LoadMasterKey() (*[MasterKeyLen]byte, error) {
	raw := os.Getenv("PIPEWRIGHT_MASTER_KEY")
	if raw == "" {
		if path := os.Getenv("PIPEWRIGHT_MASTER_KEY_FILE"); path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("config: read master key file: %w", err)
			}
			raw = string(b)
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrNoMasterKey
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// 不回显原始值。
		return nil, errors.New("config: master key is not valid base64")
	}
	if len(decoded) != MasterKeyLen {
		return nil, fmt.Errorf("config: master key must decode to %d bytes, got %d", MasterKeyLen, len(decoded))
	}
	var key [MasterKeyLen]byte
	copy(key[:], decoded)
	// 明文 key 副本用后清零(防 GC 前残留被 core dump/swap 捕获),与他处 zero() 纪律一致。
	for i := range decoded {
		decoded[i] = 0
	}
	return &key, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getenvInt 读 int;解析失败或空 → def。
func getenvInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// getenvInt64 读 int64;解析失败或空 → def。
func getenvInt64(key string, def int64) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// getenvBool 读 bool;接受 1/true/yes/on(忽略大小写)→ true,其它 → def。
func getenvBool(key string, def bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return def
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
