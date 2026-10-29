-- 0088 variable_group_groups:变量组可以归属资源分组(与 0087 的 servers 同一套多组语义)。
--
-- 变量组此前是「跨项目共享」的纯功能轴资源:只要角色有 library.edit 就能改,数据轴没有落点。
-- 一旦变量组里装着某几个项目的密钥引用,「谁能看、谁能改这一份」就也该由分组来切。
--
-- 语义与服务器的成员表逐条相同(见 internal/access/access.go 的 Can):
--   - 没有行 = 未归组:全员可见可操作(存量变量组即此态,故不需要回填);
--   - 命中任意一个可见分组即可见可操作(并集);
--   - 改名 / 删除 / 改组这类归属级动作要求**每一个**所在分组都管得住(交集)。
--
-- 不加外键:resource_groups 的删除在 internal/group 里按「仍被引用」拒绝(与 0056/0057/0087
-- 同一口径);变量组删除时由 internal/library 在同一事务里清掉本表的行。

CREATE TABLE IF NOT EXISTS variable_group_groups (
    variable_group_id TEXT NOT NULL,
    group_id          TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    PRIMARY KEY (variable_group_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_variable_group_groups_group ON variable_group_groups (group_id);
