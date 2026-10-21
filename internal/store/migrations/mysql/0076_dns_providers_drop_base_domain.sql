-- 0076 dns_providers 去掉 base_domain(MySQL):根域不再由用户填,改为用时从服务商账号现推。
--
-- 与 sqlite 同号同因:该列的答案在服务商账号里,抄进库只会过期,而且填错也拦不住任何事 ——
-- 反代签发走 Caddy 镜像内的 DNS 插件,它只读 AK/SK,不看这一列;平台自己写 TXT 时改为按记录名
-- 去账号域名清单里推区(见 internal/dnsprovider/zone.go)。MySQL 8 原生支持 DROP COLUMN,
-- 该列无索引/无外键引用,不涉及表重建。幂等性由 schema_migrations 跟踪保证(DDL 无 IF EXISTS)。
ALTER TABLE dns_providers DROP COLUMN base_domain;
