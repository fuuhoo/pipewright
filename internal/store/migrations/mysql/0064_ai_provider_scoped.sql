-- 0064 ai_provider_scoped(MySQL):三档协议各存一份配置,主键从 id=1 单例改为 provider。
-- 与 sqlite 版同义:claude / openai / ollama 各占一行,地址/模型/密钥/预算互不覆盖;
-- enabled 表示「这份是当前生效的」,互斥由 internal/ai 保存时保证。
-- MySQL 的 DDL 隐式提交,故按语句逐条执行(store.go 已如此处理)。
-- 原 id 列上的 CHECK(id=1) 名为 ai_config_chk_1(8.0.16+ 默认命名),要先摘掉再删列,
-- 否则删了主键仍会被这个 CHECK 卡住。provider='' 的「未配置」占位行直接丢弃。

ALTER TABLE ai_config DROP CHECK ai_config_chk_1;

DELETE FROM ai_config WHERE provider = '';

ALTER TABLE ai_config DROP COLUMN id;

ALTER TABLE ai_config
    MODIFY provider VARCHAR(16) NOT NULL,
    DROP PRIMARY KEY,
    ADD PRIMARY KEY (provider);

ALTER TABLE ai_config
    ADD CONSTRAINT chk_ai_config_provider CHECK (provider IN ('claude', 'openai', 'ollama'));
