-- 0088 variable_group_groups(MySQL):变量组可以归属资源分组。语义与不加回填的理由见 sqlite 同序注释。

CREATE TABLE IF NOT EXISTS variable_group_groups (
    variable_group_id VARCHAR(64) NOT NULL,
    group_id          VARCHAR(64) NOT NULL,
    created_at        VARCHAR(32) NOT NULL,
    PRIMARY KEY (variable_group_id, group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_variable_group_groups_group ON variable_group_groups (group_id);
