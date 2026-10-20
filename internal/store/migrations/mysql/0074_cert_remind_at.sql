-- 0074 cert_remind_at(MySQL):证书表补「上次提醒时刻」,语义见 sqlite 同序注释。
-- 用 VARCHAR(32) 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT,且本列存的是 RFC3339 文本。
ALTER TABLE certificates ADD COLUMN remind_at VARCHAR(32) NOT NULL DEFAULT '';
