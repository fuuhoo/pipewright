-- 0050 build_envs:管理员预置的"构建环境"目录(语言 + 版本 + 镜像)。
-- 业务含义(v6.2 §3.1, §4.3):
--   - 普通用户只能从此处选语言+版本;不可自由输入镜像地址。
--   - 管理员预置镜像 + 可选凭据 + 镜像状态。
--   - 状态机:unchecked / checking / available / unavailable;unavailable 强制不可启用。
--
-- 字段说明:
--   language        语言(node/java/go/python/custom)
--   version         版本号(22, 24, 17, 1.22, 3.11, default ...)
--   display_name    UI 友好名称(Java 11 (Temurin))
--   source_type     'official' | 'custom';系统不拼接地址
--   image           完整镜像地址,原样使用
--   credential_id   可选引用 credentials.id;custom 镜像私有仓库凭证
--   image_check_*   镜像可用性状态(覆盖式写入,见 §3.2)
--   enabled         启用标志;unavailable 强制为 0
--   sort_order      UI 排序权重
--   created_by      users.id(创建者)
--
-- 同 language + version 唯一(UNIQUE 约束)。

CREATE TABLE build_envs (
    id                 TEXT PRIMARY KEY,
    language           TEXT NOT NULL,
    version            TEXT NOT NULL,
    display_name       TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',

    source_type        TEXT NOT NULL DEFAULT 'official',
    image              TEXT NOT NULL,
    credential_id      TEXT NOT NULL DEFAULT '',

    image_check_status TEXT NOT NULL DEFAULT 'unchecked',
    image_check_error  TEXT NOT NULL DEFAULT '',
    image_checked_at   TEXT,

    enabled            INTEGER NOT NULL DEFAULT 1,
    sort_order         INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,

    UNIQUE(language, version)
);

CREATE INDEX idx_build_envs_language     ON build_envs(language);
CREATE INDEX idx_build_envs_enabled      ON build_envs(enabled);
CREATE INDEX idx_build_envs_check_status ON build_envs(image_check_status);