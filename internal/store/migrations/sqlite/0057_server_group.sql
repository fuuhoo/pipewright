-- 0057 server_group:服务器归属分组。
-- 语义与 projects.group_id 一致('' = 未归组 = 全员可操作)。服务器的「操作」指
-- SSH exec / 终端 / 容器与镜像视图 / 服务动作;登记与改删仍只开放给管理员。
-- 不建外键,理由见 0056。

ALTER TABLE servers ADD COLUMN group_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_servers_group_id ON servers(group_id) WHERE group_id != '';
