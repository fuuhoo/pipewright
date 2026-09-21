-- 0052 config_profiles:见 sqlite/0052_config_profiles.sql 注释。

CREATE TABLE config_profiles (
    id           VARCHAR(64)  PRIMARY KEY,
    language     VARCHAR(64)  NOT NULL,
    config_type  VARCHAR(64)  NOT NULL,
    name         VARCHAR(255) NOT NULL,
    target_path  VARCHAR(512) NOT NULL,
    file_path    VARCHAR(512) NOT NULL,
    content      TEXT         NOT NULL,
    is_default   TINYINT      NOT NULL DEFAULT 0,
    is_builtin   TINYINT      NOT NULL DEFAULT 0,
    description  TEXT         NOT NULL,
    enabled      TINYINT      NOT NULL DEFAULT 1,
    created_by   VARCHAR(64)  NOT NULL,
    created_at   VARCHAR(32)  NOT NULL,
    updated_at   VARCHAR(32)  NOT NULL,

    UNIQUE KEY uniq_cp_lang_type_name (language, config_type, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_config_profiles_language ON config_profiles(language);
CREATE INDEX idx_config_profiles_default  ON config_profiles(language, is_default);
CREATE INDEX idx_config_profiles_builtin  ON config_profiles(is_builtin);