-- 0095 run_trigger_server:手动触发时可选的目标主机(仅留痕,不改部署落点)。
--
-- 手动触发弹窗允许从「该流水线部署节点配置过的主机」里选一台,作为本次运行的
-- 人工标注/上下文。它**不参与**部署:落点仍由每个部署节点的 config.serverIds 决定,
-- 所以这里独立成列,不复用 resolved_target_server_ids(那份是 webhook 分支映射解析出的
-- 元数据,预览环境等路径会读)。
--
--   trigger_server_id   : 所选主机 servers.id;未选为空串
--   trigger_server_name : 触发时刻的机器名快照(机器改名/删除后,历史记录仍可读)
--
-- 老运行两列都是空串,前端据空值不展示。

ALTER TABLE pipeline_runs ADD COLUMN trigger_server_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_runs ADD COLUMN trigger_server_name TEXT NOT NULL DEFAULT '';
