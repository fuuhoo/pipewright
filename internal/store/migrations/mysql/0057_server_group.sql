-- 0057 server_group(MySQL):服务器归属分组,见 sqlite 同名注释。

ALTER TABLE servers ADD COLUMN group_id VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX idx_servers_group_id ON servers(group_id);
