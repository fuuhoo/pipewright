-- 0077 cert_renew_before_days(MySQL):证书表补「到期前多少天开始自动续期」,语义见 sqlite 同序注释。
-- 0 = 用默认窗口(30 天),与 sqlite 侧同值同义。
ALTER TABLE certificates ADD COLUMN renew_before_days INT NOT NULL DEFAULT 0;
