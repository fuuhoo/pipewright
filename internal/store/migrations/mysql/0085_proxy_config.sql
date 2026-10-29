-- 0085 proxy_config(MySQL):反代运行环境的全局单例配置(id=1)。
-- caddy_image 是目标主机上要运行的 Caddy 镜像;空串 = 跟随环境变量 PIPEWRIGHT_CADDY_IMAGE,
-- 再缺省用内置默认镜像。国内机器拉不动 ghcr.io 时,在「设置·系统」里改成可达的镜像地址。
CREATE TABLE IF NOT EXISTS proxy_config (
    id          INT PRIMARY KEY CHECK (id = 1),
    caddy_image VARCHAR(260) NOT NULL DEFAULT '',
    updated_at  VARCHAR(32) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
INSERT IGNORE INTO proxy_config (id, caddy_image, updated_at) VALUES (1, '', '');
