-- 0071 acme_accounts:ACME 账户(每个 CA 一行)。
-- ACME 的账户身份是**一把密钥**,不是一枚口令:CA 用它签每个请求(JWS),注册成功后回一个账户 URL。
-- 所以这里存的是一条要长期持有的私钥材料 —— 与 credentials 同纪律:account_key_sealed / eab_key_sealed
-- 是 vault secretbox 密文 BLOB,明文只在进程内取出即弃(AC-SEC-01)。
--   id           : uuid v4
--   ca           : CA 枚举(letsencrypt | letsencrypt_staging | zerossl | google | sslcom),唯一
--                  —— 一个 CA 只维护一个账户:多账户等于把同一批域名的签发速率配额切成几份,
--                     还会让「这张证书是谁签的」失去唯一答案
--   directory_url: 该 CA 的 ACME directory 地址。存下来是为了**钉死**首次注册时用的那个端点:
--                  CA 换目录(如 LE 从 v4 老端点迁走)时,拿旧账户密钥去打新目录会直接 400,
--                  宁可显式报错也不要静默重建账户重签一批证书
--   contact      : 账户联系邮箱(LE 用它发到期/吊销通知),可空
--   account_url  : 注册成功后 CA 给的账户 URL;空 = 尚未注册
--   eab_kid      : EAB(外部账户绑定)的 KeyID,由 CA 控制台签发。它是标识不是秘密,存明文;
--                  ZeroSSL / Google Trust Services / SSL.com 必须要,Let's Encrypt 留空
--   eab_key_sealed : EAB 的对称 MAC 密钥密文;NULL = 该 CA 不需要 EAB
--   registered_at  : 最近一次成功注册/更新账户的时间
CREATE TABLE IF NOT EXISTS acme_accounts (
    id               TEXT PRIMARY KEY,
    ca               TEXT NOT NULL,
    directory_url    TEXT NOT NULL,
    contact          TEXT NOT NULL DEFAULT '',
    account_url      TEXT NOT NULL DEFAULT '',
    eab_kid          TEXT NOT NULL DEFAULT '',
    eab_key_sealed   BLOB,
    account_key_sealed BLOB NOT NULL,
    registered_at    TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_acme_accounts_ca ON acme_accounts (ca);
