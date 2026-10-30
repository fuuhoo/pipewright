-- 0089 deploy target backup(MySQL):语义与回填口径见 sqlite 同序注释。
-- 用 VARCHAR 而非 TEXT:MySQL 的 TEXT 列不允许 DEFAULT,这三列都需要「空串 = 未记录」。
ALTER TABLE deploy_targets ADD COLUMN deploy_path VARCHAR(1024) NOT NULL DEFAULT '';
ALTER TABLE deploy_targets ADD COLUMN restart_command VARCHAR(4096) NOT NULL DEFAULT '';
ALTER TABLE deploy_targets ADD COLUMN backup_mode VARCHAR(16) NOT NULL DEFAULT '';
