-- 0083 notification_deliveries(MySQL):通知投递台账,列义与保留口径见 sqlite 同序注释。
-- error_text / body 用 TEXT:渠道返回的人读错误可能较长(HTTP 响应体片段),正文是整条通知,
-- VARCHAR 会截断排查线索。

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id           VARCHAR(64) PRIMARY KEY,
    event        VARCHAR(64) NOT NULL,
    project_id   VARCHAR(64) NULL,
    run_id       VARCHAR(64) NULL,
    channel_id   VARCHAR(64) NOT NULL,
    channel_name VARCHAR(255) NOT NULL DEFAULT '',
    status       VARCHAR(16) NOT NULL,
    title        VARCHAR(512) NOT NULL DEFAULT '',
    body         TEXT NOT NULL,
    error_text   TEXT NOT NULL,
    created_at   VARCHAR(32) NOT NULL,
    FOREIGN KEY (run_id) REFERENCES pipeline_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX idx_notify_deliv_created ON notification_deliveries (created_at);
CREATE INDEX idx_notify_deliv_event   ON notification_deliveries (event, created_at);
CREATE INDEX idx_notify_deliv_project ON notification_deliveries (project_id);
CREATE INDEX idx_notify_deliv_run     ON notification_deliveries (run_id);
