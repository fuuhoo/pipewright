package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// TestMigration0070Certificates 验证 0070 证书表落地:
//   - 一行只靠必填列就能插入,其余走默认值(source=acme / status=pending / auto_renew=1 / sans=[])
//   - domain 唯一:同域名只能有一条记录(重签是就地更新)
//   - key_sealed 可为 NULL(尚未有私钥的 pending 行),cert_pem 明文列默认为空串
func TestMigration0070Certificates(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO certificates (id, domain, created_at, updated_at)
		 VALUES ('c-1', 'app.example.com', ?, ?)`, now, now); err != nil {
		t.Fatalf("插入证书失败: %v", err)
	}

	var source, status, ca, sans, certPem, notAfter string
	var autoRenew int
	var keySealed []byte
	if err := st.DB.QueryRowContext(ctx,
		`SELECT source, status, ca, sans, cert_pem, not_after, auto_renew, key_sealed
		 FROM certificates WHERE id='c-1'`).
		Scan(&source, &status, &ca, &sans, &certPem, &notAfter, &autoRenew, &keySealed); err != nil {
		t.Fatalf("读回证书默认值: %v", err)
	}
	if source != "acme" || status != "pending" || ca != "" || sans != "[]" || certPem != "" || notAfter != "" {
		t.Fatalf("默认值不符: source=%q status=%q ca=%q sans=%q cert_pem=%q not_after=%q",
			source, status, ca, sans, certPem, notAfter)
	}
	if autoRenew != 1 {
		t.Fatalf("auto_renew 默认应为 1,实得 %d", autoRenew)
	}
	if keySealed != nil {
		t.Fatal("key_sealed 应允许 NULL:pending 行还没有私钥")
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO certificates (id, domain, created_at, updated_at)
		 VALUES ('c-2', 'app.example.com', ?, ?)`, now, now); err == nil {
		t.Fatal("domain 唯一约束未生效")
	}
}

// TestMigration0071AcmeAccounts 验证 0071 ACME 账户表:每个 CA 只留一行,账户私钥必填。
func TestMigration0071AcmeAccounts(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO acme_accounts (id, ca, directory_url, account_key_sealed, created_at, updated_at)
		 VALUES ('a-1', 'letsencrypt', 'https://acme-v02.api.letsencrypt.org/directory', X'0102', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("插入 ACME 账户失败: %v", err)
	}

	var contact, eabKid, accountURL string
	var eabSealed any
	if err := st.DB.QueryRowContext(ctx,
		`SELECT contact, eab_kid, account_url, eab_key_sealed FROM acme_accounts WHERE id='a-1'`).
		Scan(&contact, &eabKid, &accountURL, &eabSealed); err != nil {
		t.Fatalf("读回账户默认值: %v", err)
	}
	if contact != "" || eabKid != "" || accountURL != "" {
		t.Fatalf("EAB / contact 默认应为空串,实得 %q %q %q", contact, eabKid, accountURL)
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO acme_accounts (id, ca, directory_url, account_key_sealed, created_at, updated_at)
		 VALUES ('a-2', 'letsencrypt', 'https://other/directory', X'0304', ?, ?)`,
		now, now); err == nil {
		t.Fatal("ca 唯一约束未生效:同一 CA 不该有两个账户")
	}

	// 账户私钥是身份本身,没有它这行毫无意义 → 列上就该挡住。
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO acme_accounts (id, ca, directory_url, created_at, updated_at)
		 VALUES ('a-3', 'zerossl', 'https://acme.zerossl.com/directory', ?, ?)`,
		now, now); err == nil {
		t.Fatal("account_key_sealed 应拒绝 NULL")
	}
}

// TestMigration0072CertDeployments 验证 0072 发布目标表:
//   - post_command / auto_deploy / last_status 的默认值(留空指令 = 默认不执行任何东西)
//   - 删证书级联删掉它的落点(子行只有路径与指令,不留指向不存在证书的悬空配置)
//   - server_id 无外键:主机不在库里也允许登记落点(与 proxy_routes 同纪律)
func TestMigration0072CertDeployments(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO certificates (id, domain, created_at, updated_at) VALUES ('c-1', 'a.example.com', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("插入证书失败: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO cert_deployments (id, cert_id, server_id, cert_path, key_path, created_at, updated_at)
		 VALUES ('d-1', 'c-1', '不存在的机器', '/etc/nginx/tls/a.crt', '/etc/nginx/tls/a.key', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("插入发布目标失败: %v", err)
	}

	var postCmd, lastStatus, deployedAt string
	var autoDeploy int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT post_command, auto_deploy, last_status, deployed_at FROM cert_deployments WHERE id='d-1'`).
		Scan(&postCmd, &autoDeploy, &lastStatus, &deployedAt); err != nil {
		t.Fatalf("读回发布目标默认值: %v", err)
	}
	if postCmd != "" || lastStatus != "" || deployedAt != "" || autoDeploy != 1 {
		t.Fatalf("默认值不符: post_command=%q auto_deploy=%d last_status=%q deployed_at=%q",
			postCmd, autoDeploy, lastStatus, deployedAt)
	}

	if _, err := st.DB.ExecContext(ctx, `DELETE FROM certificates WHERE id='c-1'`); err != nil {
		t.Fatalf("删除证书失败: %v", err)
	}
	var left int
	err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cert_deployments WHERE id='d-1'`).Scan(&left)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("统计级联结果失败: %v", err)
	}
	if left != 0 {
		t.Fatalf("删证书应级联删掉发布目标,实得 %d 行", left)
	}
}
