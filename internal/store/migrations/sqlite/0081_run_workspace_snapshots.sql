-- 0081 run_workspace_snapshots:构建失败(或产物路径未命中)那一刻的工作区目录快照。
--
-- 为什么需要:工作区是即用即销的临时目录(见 cloner.go mkTempWorkspace),构建跑完连目录带内容
-- 一起 rm -rf。产物路径写错时,收集器只往日志留一行「产物路径未找到,跳过」,既不登记产物行也不记
-- 「当时目录里到底有什么」—— 于是"我到底生成在哪了"这个问题在运行记录里根本没有答案,只能重跑一次猜。
-- 本表把这份现场留一小会儿:只在 job 失败、或声明的产物路径没命中时采,默认保留 2 小时。
--
-- 只存**清单文本**(相对路径 + 大小),绝不存文件内容:一个含 node_modules 的前端工作区有
-- 3.4 万条目(约 1.7MB 纯文本),所以遍历受深度、总条数、单目录展开条数三重上限约束,
-- 超出置 truncated=1、并在对应目录条目上记 Dropped=N。
-- 上限 3000 条 × 约 60 字节 ≈ 180KB,已超 MySQL TEXT 的 64KB 上限 → MySQL 侧用 LONGTEXT。
--
-- declared_json 逐条记用户声明的产物路径与其判定(ok / missing / unmatched / rejected),未命中时
-- 附「最近存在的祖先目录 + 该目录下的实际条目」—— 这一栏才是排查"目录写错"的直接证据。
--
-- additive only:新建独立表,不改既有 runs/steps/artifacts 列或语义;带 ON DELETE CASCADE,
-- 故删 run(含保留清理器裁剪)会连带清掉快照。

CREATE TABLE IF NOT EXISTS run_workspace_snapshots (
    id             TEXT PRIMARY KEY,
    run_id         TEXT NOT NULL,
    stage          TEXT NOT NULL DEFAULT '',
    job_id         TEXT NOT NULL DEFAULT '',
    job_name       TEXT NOT NULL DEFAULT '',
    reason         TEXT NOT NULL DEFAULT '',            -- failed | artifact_miss(一次采集只记一个原因)
    declared_json  TEXT NOT NULL DEFAULT '',            -- [{"path","status","parent","siblings"}]
    entries_json   TEXT NOT NULL DEFAULT '',            -- [{"p":"a/b.jar","s":123}] 工作区相对路径清单
    entry_count    INTEGER NOT NULL DEFAULT 0,
    truncated      INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES pipeline_runs (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_ws_snap_run ON run_workspace_snapshots (run_id);
-- (created_at) 索引:TTL 清理器按时间删旧快照的主查询路径。
CREATE INDEX IF NOT EXISTS idx_run_ws_snap_created ON run_workspace_snapshots (created_at);
