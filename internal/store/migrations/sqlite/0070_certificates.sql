-- 0070 certificates:平台自持的 SSL 证书仓库。
-- 每行 = 一张证书(一个主域名 + 若干 SAN)。两条来源:source=acme 由平台自己经 DNS-01 签发,
-- source=upload 由用户粘贴已有证书。有了「私钥在平台手里」这一份,证书才谈得上监控与发布 ——
-- 反代那条线的证书住在 Caddy 容器的卷里(proxy.go 明确「绝不读 .key」),平台拿不到私钥,
-- 也就发不到别的主机。这张表补的就是那半条。
--
-- 加密纪律与 credentials 同一条(AC-SEC-01):只有**私钥**是秘密,key_sealed 存 vault secretbox
-- 的密文 BLOB(nonce||box),明文仅进程内经 OpenSecret 取出即弃。cert_pem / chain_pem 是公开材料,
-- 刻意存明文:证书本身不是秘密,存密文只会让「导出证书包」这条路径多一次解密而无收益,
-- 也让备份/排障时能直接读出到期时间对账。
--   id              : uuid v4
--   domain          : 主名(SAN 首项),全局唯一 —— 一个域名只有一条记录,重签是就地更新它,
--                     而不是攒出一堆同域名的历史行(那会让「这张发到了哪台」失去唯一答案)
--   sans            : JSON 数组(含主名),纯展示与 CSR 重建用
--   status          : pending(未签出) | issued(在有效期) | failed(签发/导入失败) | expired(已过期)
--                     由签发流程与到期扫描器回写,不是用户可填字段
--   ca              : ACME CA 枚举(见 0071 acme_accounts);upload 时为 ''
--   dns_provider_id : DNS-01 用哪个提供商写 TXT(引用 dns_providers.id)。不设外键:与
--                     proxy_routes.config.dnsProviderId 同一手法,删提供商由上层收敛
--   auto_renew      : 到期扫描器可否就地重签这张证书(默认开)
--   key_algo        : 私钥算法展示用(ECDSA-P256 / RSA-2048 …),不参与判定
--   not_before/not_after/issuer/serial : 从证书正文解析出来的元数据(RFC3339 / 十进制串)
--   checked_at      : 最近一次到期扫描/探测时间,供页面说「更新于」而不必每 12s 真探一次
--   last_error      : 人读失败原因(LE 拒绝 / DNS 未生效 / 域名不匹配),绝不含 token 与私钥
-- domain 唯一冲突由应用层映射为「该域名已有证书」。
CREATE TABLE IF NOT EXISTS certificates (
    id              TEXT PRIMARY KEY,
    domain          TEXT NOT NULL,
    sans            TEXT NOT NULL DEFAULT '[]',
    source          TEXT NOT NULL DEFAULT 'acme',
    status          TEXT NOT NULL DEFAULT 'pending',
    ca              TEXT NOT NULL DEFAULT '',
    dns_provider_id TEXT NOT NULL DEFAULT '',
    auto_renew      INTEGER NOT NULL DEFAULT 1,
    key_algo        TEXT NOT NULL DEFAULT '',
    not_before      TEXT NOT NULL DEFAULT '',
    not_after       TEXT NOT NULL DEFAULT '',
    issuer          TEXT NOT NULL DEFAULT '',
    serial          TEXT NOT NULL DEFAULT '',
    cert_pem        TEXT NOT NULL DEFAULT '',
    chain_pem       TEXT NOT NULL DEFAULT '',
    key_sealed      BLOB,
    last_error      TEXT NOT NULL DEFAULT '',
    issued_at       TEXT NOT NULL DEFAULT '',
    checked_at      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_certificates_domain ON certificates (domain);
CREATE INDEX IF NOT EXISTS idx_certificates_not_after ON certificates (not_after);
