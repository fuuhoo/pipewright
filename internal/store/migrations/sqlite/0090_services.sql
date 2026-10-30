-- 0090 services:归属服务,项目的一个纯展示 / 筛选属性。
--
-- 与资源分组(resource_groups)是两条正交的轴:
--   - 分组决定「谁能看、谁能操作」;服务只决定「归到哪个业务系统」,不参与任何权限判定。
--   - 一个项目最多挂一个服务;service_id = '' 表示未归属(存量数据即此态,不做迁移)。
--   - 可挂多个项目;服务本身可重名吗?不可 —— 下拉与筛选项按名字展示,重名会分不清。
--
-- 为什么是一张表而不是 projects 上的一列自由文本:后续要按服务统计/批量操作,
-- 改名也得一处改全量;自由文本拼错就分裂成两个服务,且没法统一改名。
--
-- service_id 有意不建外键(与 projects.group_id 同口径):删服务由领域层先确认没有项目引用,
-- 而不是靠 ON DELETE SET NULL 在背后悄悄清空归属。

CREATE TABLE IF NOT EXISTS services (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

ALTER TABLE projects ADD COLUMN service_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_projects_service_id ON projects(service_id) WHERE service_id != '';
