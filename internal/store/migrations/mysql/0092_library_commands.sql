-- 0092 library_commands(MySQL):复用库的命名指令,见 sqlite 同名迁移注释。

CREATE TABLE IF NOT EXISTS library_commands (
    id          VARCHAR(64)  PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    body        LONGTEXT     NOT NULL,
    created_at  VARCHAR(32)  NOT NULL,
    updated_at  VARCHAR(32)  NOT NULL,

    UNIQUE KEY uniq_library_commands_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
