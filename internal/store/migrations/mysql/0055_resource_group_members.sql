-- 0055 resource_group_members(MySQL):私有分组成员名册,见 sqlite 同名注释。
-- 复合主键 (group_id, user_id) 已覆盖「按组列成员」,故仅为「我加入了哪些组」建反向索引。

CREATE TABLE IF NOT EXISTS resource_group_members (
    group_id   VARCHAR(64) NOT NULL,
    user_id    VARCHAR(64) NOT NULL,
    created_at VARCHAR(32) NOT NULL,
    created_by VARCHAR(64) NOT NULL,

    PRIMARY KEY (group_id, user_id),
    FOREIGN KEY (group_id) REFERENCES resource_groups (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_rg_members_user ON resource_group_members(user_id);
