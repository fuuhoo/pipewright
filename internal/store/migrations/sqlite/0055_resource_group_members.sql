-- 0055 resource_group_members:私有分组成员名册。
--   - 复合主键 (group_id, user_id) 天然去重,无需唯一索引。
--   - 组长(owner_id)不写入本表:组长身份来自 resource_groups.owner_id,避免两处真相。
--   - ON DELETE CASCADE:删组即删名册;删用户不级联(用户行本期不删,只 disable)。
--
-- 表名避开 MySQL 8 保留字 groups,见 0054 注释。

CREATE TABLE IF NOT EXISTS resource_group_members (
    group_id   TEXT NOT NULL REFERENCES resource_groups (id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,

    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_rg_members_user ON resource_group_members(user_id);
