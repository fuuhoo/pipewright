-- 0059 kube_clusters(MySQL):Kubernetes 集群目标,按引用绑定 kubeconfig 凭据。
-- API server 地址刻意不入库(它是 kubeconfig 的一部分,存两处会有两份真值),详见 sqlite 同序注释。

CREATE TABLE IF NOT EXISTS kube_clusters (
    id                VARCHAR(64) PRIMARY KEY,
    name              VARCHAR(255) NOT NULL,
    credential_id     VARCHAR(64) NOT NULL,
    namespace_default VARCHAR(255) NOT NULL DEFAULT '',
    group_id          VARCHAR(64) NOT NULL DEFAULT '',
    created_at        VARCHAR(32) NOT NULL,
    updated_at        VARCHAR(32) NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_kube_clusters_credential_id ON kube_clusters (credential_id);
CREATE INDEX idx_kube_clusters_group_id ON kube_clusters (group_id);
