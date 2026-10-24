-- 0082 retention_config 补「全局保留条数」并把保留策略默认打开(MySQL 版)。
--
-- 与 sqlite 版同义:keep_global = 全局保留最近 N 条终态运行(0=不限);并把从未被配置过的默认行
-- 打开(enabled=1、max_age_days=365)。keep_per_project 列保留不动,旧配置继续作为一个保留维度生效。
-- 裁剪语义改为「已配置维度全部越界才删」(满足其一即保留),只删终态运行。
--
-- additive only:仅 ADD COLUMN + UPDATE 单行。

ALTER TABLE retention_config ADD COLUMN keep_global INT NOT NULL DEFAULT 1000;

UPDATE retention_config
   SET enabled = 1, max_age_days = 365
 WHERE id = 1 AND enabled = 0 AND keep_per_project = 0 AND max_age_days = 0;
