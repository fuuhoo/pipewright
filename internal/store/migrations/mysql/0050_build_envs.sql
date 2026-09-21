-- 0050 build_envs:见 sqlite/0050_build_envs.sql 注释。
-- MySQL 路径逐条执行(无事务,见 store.go:184-189)。
-- 注:本表与 credentials.credential_id 之间**不建立外键**;credential 删除走
-- vault 包的"在用守卫"显式校验(避免 ON DELETE RESTRICT 强耦合)。

CREATE TABLE build_envs (
    id                 VARCHAR(64)  PRIMARY KEY,
    language           VARCHAR(64)  NOT NULL,
    version            VARCHAR(64)  NOT NULL,
    display_name       VARCHAR(255) NOT NULL,
    description        TEXT         NOT NULL,

    source_type        VARCHAR(32)  NOT NULL DEFAULT 'official',
    image              VARCHAR(512) NOT NULL,
    credential_id      VARCHAR(64)  NOT NULL DEFAULT '',

    image_check_status VARCHAR(32)  NOT NULL DEFAULT 'unchecked',
    image_check_error  TEXT         NOT NULL,
    image_checked_at   VARCHAR(32),

    enabled            TINYINT      NOT NULL DEFAULT 1,
    sort_order         INT          NOT NULL DEFAULT 0,
    created_by         VARCHAR(64)  NOT NULL,
    created_at         VARCHAR(32)  NOT NULL,
    updated_at         VARCHAR(32)  NOT NULL,

    UNIQUE KEY uniq_build_envs_lang_ver (language, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_build_envs_language     ON build_envs(language);
CREATE INDEX idx_build_envs_enabled      ON build_envs(enabled);
CREATE INDEX idx_build_envs_check_status ON build_envs(image_check_status);