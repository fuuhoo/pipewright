-- 0077 cert_renew_before_days:证书表补「到期前多少天开始自动续期」。
--
-- 为什么要这一列:30 天是 LE/ZeroSSL 90 天证书的行业默认(certbot 同值),但它不该是全员唯一的数 ——
-- 有人嫌 30 天太激进(想少耗 CA 配额与 DNS 写入),有人只想要「到期前 7 天」的紧凑节奏。
-- 写死在代码里就只能一起改,这里把阈值下放到每张证书,和已有的 auto_renew 开关同一粒度。
--
-- 0 = 从没设置过,按默认窗口走(cert.IssuanceWindow = 30 天)。旧行因此语义不变,
-- 用户改完还能填 0 恢复默认。仅 ADD COLUMN,不重建表(重建会级联删数据,见迁移纪律)。
ALTER TABLE certificates ADD COLUMN renew_before_days INTEGER NOT NULL DEFAULT 0;
