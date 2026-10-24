-- 0082 retention_config 补「全局保留条数」并把保留策略默认打开。
--
-- 为什么要改:构建目录快照(run_workspace_snapshots)改成「每次构建都采、随运行记录一起留」,
-- 不再自带 TTL 清理器 —— 于是整条链上唯一的生长边界就是「运行记录本身会不会被裁」。策略默认关
-- 时它等于永远增长,所以这里把边界做实。
--
-- 两件事:
--  1. 新增 keep_global:全局(不分项目)保留最近 N 条终态运行,0=不限。条数口径从「每项目」改
--     成「全局」是产品决定;keep_per_project 列**保留不动**(删列会连带风险),旧实例若配过它,
--     仍作为一个保留维度继续生效,只是新界面不再暴露。
--  2. 把**从未被配置过**的默认行打开:enabled=1、max_age_days=365。判定条件严格要求三个字段
--     都还是 0042 的初始值,这样任何人手工调过的策略都不会被升级覆盖。
--
-- 裁剪语义同时改为「已配置维度全部越界才删」(见 internal/retention):一次运行只要还在最近
-- 1000 条之内、或还不满一年,就保留。删的只有「既老又多出来」的那些。只删终态运行,进行中绝不动。
--
-- additive only:仅 ADD COLUMN + UPDATE 单行,不重建表、不删列、不碰任何运行数据。

ALTER TABLE retention_config ADD COLUMN keep_global INTEGER NOT NULL DEFAULT 1000;

UPDATE retention_config
   SET enabled = 1, max_age_days = 365
 WHERE id = 1 AND enabled = 0 AND keep_per_project = 0 AND max_age_days = 0;
