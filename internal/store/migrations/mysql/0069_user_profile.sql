-- 0069 user_profile(MySQL):users 补显示名与档案邮箱,语义见 sqlite 同序注释。
-- 用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT。只 ADD COLUMN,不重建表。

ALTER TABLE users
    ADD COLUMN display_name VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN email        VARCHAR(254) NOT NULL DEFAULT '';
