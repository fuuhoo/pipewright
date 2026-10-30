-- 0091 pipeline_settings.var_group_ids(MySQL):流水线引用的复用库变量组,见 sqlite 同名迁移注释。

ALTER TABLE pipeline_settings ADD COLUMN var_group_ids LONGTEXT NOT NULL DEFAULT ('[]');
