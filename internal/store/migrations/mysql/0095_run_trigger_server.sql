-- 0095 run_trigger_server(MySQL):手动触发时可选的目标主机(仅留痕,不改部署落点)。
-- 语义与 sqlite 版一致;详情见 sqlite/0095_run_trigger_server.sql 的头注释。

ALTER TABLE pipeline_runs ADD COLUMN trigger_server_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE pipeline_runs ADD COLUMN trigger_server_name VARCHAR(255) NOT NULL DEFAULT '';
