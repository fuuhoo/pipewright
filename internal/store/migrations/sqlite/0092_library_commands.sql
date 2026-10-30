-- 0092 library_commands:复用库的「指令」(命名、可复用的 shell 命令)。
--
-- 用途:部署节点的「部署前指令 / 重启命令 / 健康探测命令」可从库里选一条命名指令直接引用,
-- 一次维护、各项目共用;改库里的指令即对所有引用它的流水线生效(引用存 ID,不抄正文)。
--
-- 与自定义节点的区别:自定义节点是整份 Job 配置快照;指令只是一段命令正文,不绑定节点类型。
-- 指令正文里可以写 {{变量}} 占位,执行时按本次运行参数 / 流水线变量渲染 —— 与手填命令同一条链路。

CREATE TABLE IF NOT EXISTS library_commands (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
