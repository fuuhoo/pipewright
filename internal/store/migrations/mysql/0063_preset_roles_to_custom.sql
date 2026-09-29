-- 0063 preset_roles_to_custom:把四档预置角色(user / developer / ops / viewer)从代码表搬进库。
--
-- 改动前:内置五档的权威是 internal/access/perms.go 的 rolePerms 代码表,库里只存自定义角色(0062)。
-- 改动后:代码表只剩 admin 一档,其余四档成为 roles 表里的普通行 —— 管理员可以改名、改点集、删掉,
-- 内置只剩「管理员」这一档不可改删(它兜住「管理员不会把自己锁在门外」这条,见 0062 注释②)。
--
-- 三条刻意的取舍:
--   1. id 沿用 'user' / 'developer' / 'ops' / 'viewer' 原字串,不生成 UUID —— 这样 users.role 与
--      sessions.role 里已有的取值继续命中(access.ValidRole 走「先代码表、再缓存」,现在走缓存),
--      存量账号一次都不用迁移;角色 id 的格式服务层并不校验为 UUID,只有新建时才用 UUID。
--   2. name 先填 id 本身。前端 labelForRoleId 对「name 仍等于 id」的预置档回落到八语种的
--      ROLE_LABEL_KEY 标签,所以页面文案与今天逐字一致;管理员改名后以库里的 name 为准。
--   3. created_at 逐档 +1 秒:列表按 (created_at, id) 排,这样四档在页面上保持原来的展示顺序
--      (普通用户 → 开发者 → 运维 → 只读),不会退化成字序。
-- description 留空:内置档那句说明(builtinHint)是写死的中文,搬进库就该由管理员自己写。
-- base_role 也留空:这四档不是从谁抄来的。顺带说明 0062 那句「复制来源的内置角色名」是搬过来
-- 之前的口径 —— 模板名单现在就是 roles 表本身,角色服务对该列只校验「这个 id 还存在」。

INSERT INTO roles (id, name, description, base_role, created_by, created_at, updated_at)
  VALUES ('user', 'user', '', '', '', '2026-09-29T00:00:01Z', '2026-09-29T00:00:01Z');
INSERT INTO role_perms (role_id, perm_id)
  VALUES
  ('user', 'dashboard.view'),
  ('user', 'project.view'),
  ('user', 'project.edit'),
  ('user', 'library.view'),
  ('user', 'library.edit'),
  ('user', 'run.view'),
  ('user', 'run.operate'),
  ('user', 'environments.view'),
  ('user', 'metrics.dora.view'),
  ('user', 'server.view'),
  ('user', 'server.exec'),
  ('user', 'container.view'),
  ('user', 'container.operate'),
  ('user', 'cert.view'),
  ('user', 'preview.view'),
  ('user', 'preview.recycle'),
  ('user', 'anomaly.view'),
  ('user', 'anomaly.edit'),
  ('user', 'cluster.view'),
  ('user', 'cluster.operate');

INSERT INTO roles (id, name, description, base_role, created_by, created_at, updated_at)
  VALUES ('developer', 'developer', '', '', '', '2026-09-29T00:00:02Z', '2026-09-29T00:00:02Z');
INSERT INTO role_perms (role_id, perm_id)
  VALUES
  ('developer', 'dashboard.view'),
  ('developer', 'project.view'),
  ('developer', 'project.edit'),
  ('developer', 'library.view'),
  ('developer', 'library.edit'),
  ('developer', 'run.view'),
  ('developer', 'run.operate'),
  ('developer', 'environments.view'),
  ('developer', 'metrics.dora.view'),
  ('developer', 'server.view'),
  ('developer', 'container.view'),
  ('developer', 'cert.view'),
  ('developer', 'preview.view'),
  ('developer', 'anomaly.view'),
  ('developer', 'cluster.view');

INSERT INTO roles (id, name, description, base_role, created_by, created_at, updated_at)
  VALUES ('ops', 'ops', '', '', '', '2026-09-29T00:00:03Z', '2026-09-29T00:00:03Z');
INSERT INTO role_perms (role_id, perm_id)
  VALUES
  ('ops', 'dashboard.view'),
  ('ops', 'project.view'),
  ('ops', 'library.view'),
  ('ops', 'run.view'),
  ('ops', 'run.operate'),
  ('ops', 'environments.view'),
  ('ops', 'metrics.dora.view'),
  ('ops', 'server.view'),
  ('ops', 'server.exec'),
  ('ops', 'container.view'),
  ('ops', 'container.operate'),
  ('ops', 'cert.view'),
  ('ops', 'preview.view'),
  ('ops', 'preview.recycle'),
  ('ops', 'anomaly.view'),
  ('ops', 'anomaly.edit'),
  ('ops', 'cluster.view'),
  ('ops', 'cluster.operate');

INSERT INTO roles (id, name, description, base_role, created_by, created_at, updated_at)
  VALUES ('viewer', 'viewer', '', '', '', '2026-09-29T00:00:04Z', '2026-09-29T00:00:04Z');
INSERT INTO role_perms (role_id, perm_id)
  VALUES
  ('viewer', 'dashboard.view'),
  ('viewer', 'project.view'),
  ('viewer', 'library.view'),
  ('viewer', 'run.view'),
  ('viewer', 'environments.view'),
  ('viewer', 'metrics.dora.view'),
  ('viewer', 'server.view'),
  ('viewer', 'container.view'),
  ('viewer', 'cert.view'),
  ('viewer', 'preview.view'),
  ('viewer', 'anomaly.view'),
  ('viewer', 'cluster.view');
