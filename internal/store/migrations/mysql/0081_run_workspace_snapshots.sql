-- 0081 run_workspace_snapshots(MySQL):构建失败/产物未命中时的工作区目录快照,列义见 sqlite 同序注释。
-- 两个 JSON 清单列用 LONGTEXT:上限 3000 条目录项约 180KB,已超 TEXT 的 64KB(与 spec_json 同档)。

CREATE TABLE IF NOT EXISTS run_workspace_snapshots (
    id            VARCHAR(64) PRIMARY KEY,
    run_id        VARCHAR(64) NOT NULL,
    stage         VARCHAR(128) NOT NULL DEFAULT '',
    job_id        VARCHAR(64) NOT NULL DEFAULT '',
    job_name      VARCHAR(255) NOT NULL DEFAULT '',
    reason        VARCHAR(32) NOT NULL DEFAULT '',
    declared_json LONGTEXT NOT NULL DEFAULT ('[]'),
    entries_json  LONGTEXT NOT NULL DEFAULT ('[]'),
    entry_count   INT NOT NULL DEFAULT 0,
    truncated     TINYINT NOT NULL DEFAULT 0,
    created_at    VARCHAR(32) NOT NULL,
    FOREIGN KEY (run_id) REFERENCES pipeline_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX idx_run_ws_snap_run ON run_workspace_snapshots (run_id);
CREATE INDEX idx_run_ws_snap_created ON run_workspace_snapshots (created_at);
