-- 0072 cert_deployments(MySQL):证书发布目标,详见 sqlite 同序注释。

CREATE TABLE IF NOT EXISTS cert_deployments (
    id           VARCHAR(64) PRIMARY KEY,
    cert_id      VARCHAR(64) NOT NULL,
    server_id    VARCHAR(64) NOT NULL,
    cert_path    VARCHAR(512) NOT NULL,
    key_path     VARCHAR(512) NOT NULL,
    post_command TEXT NOT NULL DEFAULT (''),
    auto_deploy  TINYINT NOT NULL DEFAULT 1,
    last_status  VARCHAR(32) NOT NULL DEFAULT '',
    last_error   VARCHAR(1024) NOT NULL DEFAULT '',
    deployed_at  VARCHAR(32) NOT NULL DEFAULT '',
    created_at   VARCHAR(32) NOT NULL,
    updated_at   VARCHAR(32) NOT NULL,
    FOREIGN KEY (cert_id) REFERENCES certificates (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX idx_cert_deployments_cert ON cert_deployments (cert_id);
CREATE INDEX idx_cert_deployments_server ON cert_deployments (server_id);
