-- 0052 config_profiles:管理员预置的"配置资源"(Maven/npm/pip/goproxy 等配置文件)。
-- 业务含义(v6.2 §3.3, §4.4):
--   - 运行时由 runner.ConfigInjector 拷贝到容器内 target_path。
--   - file_path = ${DATA_DIR}/config_profiles/<id>/<filename>(权威)。
--   - content 是冗余快照,用于审计/导入导出。
--   - is_builtin=1 行字段白名单(只允许改 description/enabled)。

CREATE TABLE config_profiles (
    id           TEXT PRIMARY KEY,
    language     TEXT NOT NULL,
    config_type  TEXT NOT NULL,
    name         TEXT NOT NULL,
    target_path  TEXT NOT NULL,
    file_path    TEXT NOT NULL,
    content      TEXT NOT NULL,
    is_default   INTEGER NOT NULL DEFAULT 0,
    is_builtin   INTEGER NOT NULL DEFAULT 0,
    description  TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,

    UNIQUE(language, config_type, name)
);

CREATE INDEX idx_config_profiles_language ON config_profiles(language);
CREATE INDEX idx_config_profiles_default  ON config_profiles(language, is_default);
CREATE INDEX idx_config_profiles_builtin  ON config_profiles(is_builtin);