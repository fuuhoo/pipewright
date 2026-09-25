-- 0059 kube_clusters:Kubernetes 集群目标。
-- 一台集群 = 一个 API server 地址 + 一份访问凭据。地址**不单独建列**:它是 kubeconfig 的
-- 一部分,存两处就会有两份互相看不见的真值(kubeconfig 换了、页面地址没换 → 发布打到旧集群)。
-- 凭据按引用绑定,库里只有 credential_id,无任何 token / 客户端证书明文(AC-SEC-01);
-- 明文由 internal/kube 经 vault 在进程内取出、装配 TLS/ bearer 后即弃。
--   id                : uuid v4
--   name              : 集群展示名(如 prod-hz)
--   credential_id     : FK → credentials.id(type=kubeconfig);ON DELETE RESTRICT:仍被集群
--                       引用的凭据不可删(防悬挂:删凭据会让该集群再不可发布)
--   namespace_default : 该节点的默认命名空间(部署任务未填时用它;只是预填,不是限制)
--   group_id          : 资源分组('' = 未归组);刻意不建外键,理由见 0056/0057

CREATE TABLE IF NOT EXISTS kube_clusters (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    credential_id     TEXT NOT NULL,
    namespace_default TEXT NOT NULL DEFAULT '',
    group_id          TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    FOREIGN KEY (credential_id) REFERENCES credentials (id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_kube_clusters_credential_id ON kube_clusters (credential_id);
CREATE INDEX IF NOT EXISTS idx_kube_clusters_group_id ON kube_clusters (group_id) WHERE group_id != '';
