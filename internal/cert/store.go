package cert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/google/uuid"
)

// Store 是证书三张表的持久层(参数化 SQL,两方言一致)。
//
// 私钥与账户密钥**只以密文进出**:递进来的 keySealed 已是 vault 密封结果,本层不碰明文,
// 也不把它写进任何错误文本。对外视图 Certificate 不含密钥字段,故读取路径天然不外泄。
type Store struct {
	db *sql.DB
}

// NewStore 构造证书持久层。
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// certRow 是一行证书:公开材料放在内嵌的对外视图里,私钥密文单独放未导出字段。
// 这样「不小心把证书结构体 JSON 出去」这条路上根本不存在私钥。
type certRow struct {
	Certificate
	keySealed []byte
}

// accountRow 是一个 ACME 账户:账户私钥密文 + EAB 对称密钥密文同样只在未导出字段里。
type accountRow struct {
	Account
	accountKeySealed []byte
	eabKeySealed     []byte
}

const certCols = `id, domain, sans, source, status, ca, dns_provider_id, auto_renew, key_algo,
	not_before, not_after, issuer, serial, cert_pem, chain_pem, key_sealed, last_error,
	issued_at, checked_at, remind_at, created_at, updated_at`

// certArgs 把一行证书摊平成 certCols 顺序的入参。
// 时间一律经 ts 写成 RFC3339 **文本**:这几列是 TEXT,直接把 time.Time 交给驱动,
// SQLite 与 MySQL 会各存出一种格式,读回时 parseTS 只认一种,另一方言的数据就烂在库里。
func certArgs(r *certRow) []any {
	return []any{
		r.ID, r.Domain, sansJSON(r.SANs), r.Source, r.Status, r.CA, r.DNSProviderID,
		b2i(r.AutoRenew), r.KeyAlgo, ts(r.NotBefore), ts(r.NotAfter), r.Issuer, r.Serial,
		r.CertPEM, r.ChainPEM, nilIfEmpty(r.keySealed), r.LastError,
		ts(r.IssuedAt), ts(r.CheckedAt), ts(r.RemindAt), ts(r.CreatedAt), ts(r.UpdatedAt),
	}
}

// certUpdateArgs 是 UPDATE 的 SET 列表入参:除主键与 created_at 外的全部列,顺序与 certSet 一致。
func certUpdateArgs(r *certRow) []any {
	return []any{
		r.Domain, sansJSON(r.SANs), r.Source, r.Status, r.CA, r.DNSProviderID,
		b2i(r.AutoRenew), r.KeyAlgo, ts(r.NotBefore), ts(r.NotAfter), r.Issuer, r.Serial,
		r.CertPEM, r.ChainPEM, nilIfEmpty(r.keySealed), r.LastError,
		ts(r.IssuedAt), ts(r.CheckedAt), ts(r.RemindAt), ts(r.UpdatedAt),
	}
}

const certSet = `domain=?, sans=?, source=?, status=?, ca=?, dns_provider_id=?, auto_renew=?,
	key_algo=?, not_before=?, not_after=?, issuer=?, serial=?, cert_pem=?, chain_pem=?,
	key_sealed=?, last_error=?, issued_at=?, checked_at=?, remind_at=?, updated_at=?`

// insertCert 落库一张新证书(id 由这里生成)。domain 已有记录 → ErrDomainExists:
// 重签是就地更新那一条,不是攒出一堆同域名的历史行。
func (s *Store) insertCert(ctx context.Context, r *certRow) error {
	r.ID = uuid.NewString()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO certificates (`+certCols+`) VALUES (`+placeholders(22)+`)`,
		certArgs(r)...)
	if err != nil {
		if store.IsUniqueErr(err) {
			return ErrDomainExists
		}
		return fmt.Errorf("cert: insert certificate: %w", err)
	}
	return nil
}

// updateCert 就地更新一张证书的全部可变列(签发/续期回填、状态与 last_error 回写都走这一条)。
// 不动 created_at。id 不存在 → ErrNotFound。
func (s *Store) updateCert(ctx context.Context, r *certRow) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE certificates SET `+certSet+` WHERE id=?`,
		append(certUpdateArgs(r), r.ID)...)
	if err != nil {
		if store.IsUniqueErr(err) {
			return ErrDomainExists
		}
		return fmt.Errorf("cert: update certificate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// markCertChecked 刷新一张证书的「上次看过」时刻与状态(到期扫描每轮一笔)。
// 为什么连 status 一起写:一张再没人动的证书跨过到期线之后,库里那一行不会自己变,而页面读的
// 就是这一列 —— 不随扫描刷新,「已过期」会永远显示成「已签发」。
// 刻意不动 updated_at:那是「配置被人改过」的口径,每天被扫描器摸一下就跳,页面上的
// 「最近修改」就没有意义了。也刻意不整行 UPDATE:扫描器手里那份是本轮开头读的,期间签发
// 可能已经把正文换新,拿旧行覆盖回去会把刚签出来的证书签掉。
func (s *Store) markCertChecked(ctx context.Context, id string, at time.Time, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE certificates SET checked_at=?, status=? WHERE id=?`, ts(at), status, id)
	if err != nil {
		return fmt.Errorf("cert: mark checked: %w", err)
	}
	return nil
}

// markCertReminded 记下「这张证书上次发提醒的时刻」,同时刷新 checked_at 与 status
// (同一轮里几件事一起落,理由见 markCertChecked)。
func (s *Store) markCertReminded(ctx context.Context, id string, at time.Time, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE certificates SET remind_at=?, checked_at=?, status=? WHERE id=?`, ts(at), ts(at), status, id)
	if err != nil {
		return fmt.Errorf("cert: mark reminder: %w", err)
	}
	return nil
}

// getCert 按 id 读一张证书;不存在 → ErrNotFound。
func (s *Store) getCert(ctx context.Context, id string) (*certRow, error) {
	return s.queryCert(ctx, `WHERE id = ?`, id)
}

// getCertByDomain 按主域名读一张证书(导入撞名、重签复用这条路径时用)。不存在 → ErrNotFound。
func (s *Store) getCertByDomain(ctx context.Context, domain string) (*certRow, error) {
	return s.queryCert(ctx, `WHERE domain = ?`, strings.ToLower(strings.TrimSpace(domain)))
}

func (s *Store) queryCert(ctx context.Context, where string, arg any) (*certRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certificates `+where, arg)
	r, err := scanCert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cert: get certificate: %w", err)
	}
	return r, nil
}

// listCerts 返回全部证书(按到期时间升序:最紧的排最前,页面与到期扫描都据此优先处理。
// 无正文的记录 not_after 为空串,排在最前 —— 它们正是「还没签出来」的那批,放前面也合理)。
func (s *Store) listCerts(ctx context.Context) ([]*certRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+certCols+` FROM certificates ORDER BY not_after ASC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("cert: list certificates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*certRow, 0, 8)
	for rows.Next() {
		r, err := scanCert(rows)
		if err != nil {
			return nil, fmt.Errorf("cert: list certificates: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cert: list certificates: %w", err)
	}
	return out, nil
}

// delCert 删除证书;不存在 → ErrNotFound。落点行由 cert_deployments 的 ON DELETE CASCADE 跟走。
func (s *Store) delCert(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM certificates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("cert: delete certificate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// rowScanner 同时覆盖 QueryRow 与 Query 的单行扫描目标。
type rowScanner interface{ Scan(dest ...any) error }

func scanCert(sc rowScanner) (*certRow, error) {
	var (
		r                                                   certRow
		id, domain, source, status, ca, providerID, keyAlgo string
		issuer, serial, certPEM, chainPEM, lastError        string
		sans                                                string
		notBefore, notAfter, issuedAt, checkedAt            string
		remindAt                                            string
		created, updated                                    string
		autoRenew                                           int64
		keySealed                                           []byte
	)
	err := sc.Scan(&id, &domain, &sans, &source, &status, &ca, &providerID, &autoRenew, &keyAlgo,
		&notBefore, &notAfter, &issuer, &serial, &certPEM, &chainPEM, &keySealed, &lastError,
		&issuedAt, &checkedAt, &remindAt, &created, &updated)
	if err != nil {
		return nil, err
	}
	r.ID, r.Domain, r.Source, r.Status, r.CA = id, domain, source, status, ca
	r.DNSProviderID, r.KeyAlgo, r.Issuer, r.Serial = providerID, keyAlgo, issuer, serial
	r.CertPEM, r.ChainPEM, r.LastError = certPEM, chainPEM, lastError
	r.AutoRenew = autoRenew != 0
	r.SANs = parseSans(sans)
	r.HasKey = len(keySealed) > 0
	r.keySealed = keySealed
	r.NotBefore, r.NotAfter = parseTS(notBefore), parseTS(notAfter)
	r.IssuedAt, r.CheckedAt = parseTS(issuedAt), parseTS(checkedAt)
	r.RemindAt = parseTS(remindAt)
	r.CreatedAt, r.UpdatedAt = parseTS(created), parseTS(updated)
	return &r, nil
}

// —— ACME 账户 ————————————————————————————————————————————————

const accountCols = `id, ca, directory_url, contact, account_url, eab_kid, eab_key_sealed,
	account_key_sealed, registered_at, created_at, updated_at`

// accountUpdateCols 是冲突时要覆盖的列(不含 ca:一行的 CA 身份定了就不改;也不含 created_at)。
var accountUpdateCols = []string{
	"directory_url", "contact", "account_url", "eab_kid", "eab_key_sealed",
	"account_key_sealed", "registered_at", "updated_at",
}

// putAccount 按 ca upsert 一个账户(一个 CA 只留一行)。调用方负责 r.ID 非空(新建时生成)。
// eab_key_sealed 为空时写成 NULL:该列可空,而「NULL」才是「这个 CA 不需要 EAB」的语义。
func (s *Store) putAccount(ctx context.Context, r *accountRow) error {
	suffix := store.UpsertSuffix(store.DialectOf(s.db), []string{"ca"}, accountUpdateCols)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO acme_accounts (`+accountCols+`) VALUES (`+placeholders(11)+`) `+suffix,
		r.ID, r.CA, r.DirectoryURL, r.Contact, r.AccountURL, r.EABKeyID,
		nilIfEmpty(r.eabKeySealed), r.accountKeySealed,
		ts(r.RegisteredAt), ts(r.CreatedAt), ts(r.UpdatedAt))
	if err != nil {
		return fmt.Errorf("cert: upsert ACME account: %w", err)
	}
	return nil
}

// getAccount 按 CA 枚举读账户;不存在 → ErrNotFound。
func (s *Store) getAccount(ctx context.Context, ca string) (*accountRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM acme_accounts WHERE ca = ?`, ca)
	r, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cert: get ACME account: %w", err)
	}
	return r, nil
}

// listAccounts 返回全部账户(按创建序:先注册的排前面,页面顺序稳定)。
func (s *Store) listAccounts(ctx context.Context) ([]*accountRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountCols+` FROM acme_accounts ORDER BY created_at ASC, ca`)
	if err != nil {
		return nil, fmt.Errorf("cert: list ACME accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*accountRow, 0, 4)
	for rows.Next() {
		r, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("cert: list ACME accounts: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cert: list ACME accounts: %w", err)
	}
	return out, nil
}

func scanAccount(sc rowScanner) (*accountRow, error) {
	var (
		id, ca, dir, contact, acctURL, kid string
		eabKey, acctKey                    []byte
		registered, created, updated       string
	)
	if err := sc.Scan(&id, &ca, &dir, &contact, &acctURL, &kid, &eabKey, &acctKey,
		&registered, &created, &updated); err != nil {
		return nil, err
	}
	return &accountRow{
		Account: Account{
			ID: id, CA: ca, DirectoryURL: dir, Contact: contact, AccountURL: acctURL,
			EABKeyID: kid, HasEABKey: len(eabKey) > 0, RegisteredAt: parseTS(registered),
			CreatedAt: parseTS(created), UpdatedAt: parseTS(updated),
		},
		accountKeySealed: acctKey,
		eabKeySealed:     eabKey,
	}, nil
}

// —— 发布落点 ————————————————————————————————————————————————

const targetCols = `id, cert_id, server_id, cert_path, key_path, post_command, auto_deploy,
	last_status, last_error, deployed_at, created_at, updated_at`

// insertTarget 落库一个发布落点。cert_id 不存在 → FK 挡下 → ErrNotFound(证书是这里唯一有外键的列)。
func (s *Store) insertTarget(ctx context.Context, t *DeployTarget) error {
	t.ID = uuid.NewString()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cert_deployments (`+targetCols+`) VALUES (`+placeholders(12)+`)`,
		t.ID, t.CertID, t.ServerID, t.CertPath, t.KeyPath, t.PostCommand, b2i(t.AutoDeploy),
		t.LastStatus, t.LastError, ts(t.DeployedAt), ts(t.CreatedAt), ts(t.UpdatedAt))
	if err != nil {
		if store.IsForeignKeyErr(err) {
			return ErrNotFound
		}
		return fmt.Errorf("cert: insert deploy target: %w", err)
	}
	return nil
}

// updateTarget 就地更新落点**配置**(不含最近结果:那由发布流程单独回写,
// 否则改一次路径就把上次的失败记录抹平了)。
func (s *Store) updateTarget(ctx context.Context, t *DeployTarget) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE cert_deployments SET server_id=?, cert_path=?, key_path=?, post_command=?,
		 auto_deploy=?, updated_at=? WHERE id=?`,
		t.ServerID, t.CertPath, t.KeyPath, t.PostCommand, b2i(t.AutoDeploy), ts(t.UpdatedAt), t.ID)
	if err != nil {
		return fmt.Errorf("cert: update deploy target: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// markTarget 回写一次发布的结果。
// deployed_at 只在**成功**时刷新:失败必须留着上一次成功的时间,否则「这台最后什么时候真正
// 换上过新证书」在一轮失败后就再也问不出来了(页面显示的「上次发布」就是这个字段)。
func (s *Store) markTarget(ctx context.Context, id, status, lastError string, deployedAt time.Time) error {
	var res sql.Result
	var err error
	if status == DeployOK {
		res, err = s.db.ExecContext(ctx,
			`UPDATE cert_deployments SET last_status=?, last_error=?, deployed_at=?, updated_at=? WHERE id=?`,
			status, lastError, ts(deployedAt), ts(time.Now().UTC()), id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE cert_deployments SET last_status=?, last_error=?, updated_at=? WHERE id=?`,
			status, lastError, ts(time.Now().UTC()), id)
	}
	if err != nil {
		return fmt.Errorf("cert: mark deploy target: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// getTarget 按 id 读一个落点;不存在 → ErrNotFound。
func (s *Store) getTarget(ctx context.Context, id string) (*DeployTarget, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+targetCols+` FROM cert_deployments WHERE id = ?`, id)
	t, err := scanTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cert: get deploy target: %w", err)
	}
	return t, nil
}

// listTargets 返回一张证书的全部落点(创建序,与用户配置顺序一致)。
func (s *Store) listTargets(ctx context.Context, certID string) ([]*DeployTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+targetCols+` FROM cert_deployments WHERE cert_id = ? ORDER BY created_at ASC, id`, certID)
	if err != nil {
		return nil, fmt.Errorf("cert: list deploy targets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*DeployTarget, 0, 4)
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, fmt.Errorf("cert: list deploy targets: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cert: list deploy targets: %w", err)
	}
	return out, nil
}

// delTarget 删除落点;不存在 → ErrNotFound。
func (s *Store) delTarget(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cert_deployments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("cert: delete deploy target: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanTarget(sc rowScanner) (*DeployTarget, error) {
	var (
		id, certID, serverID, certPath, keyPath, post       string
		lastStatus, lastError, deployedAt, created, updated string
		auto                                                int64
	)
	if err := sc.Scan(&id, &certID, &serverID, &certPath, &keyPath, &post, &auto,
		&lastStatus, &lastError, &deployedAt, &created, &updated); err != nil {
		return nil, err
	}
	return &DeployTarget{
		ID: id, CertID: certID, ServerID: serverID, CertPath: certPath, KeyPath: keyPath,
		PostCommand: post, AutoDeploy: auto != 0, LastStatus: lastStatus, LastError: lastError,
		DeployedAt: parseTS(deployedAt), CreatedAt: parseTS(created), UpdatedAt: parseTS(updated),
	}, nil
}

// —— 列值归一小工具 ——————————————————————————————————————————

// placeholders 生成 n 个 ',' 分隔的 ? 占位符(两方言都用 ? 传参)。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// serverExists 报告目标主机是否存在。cert_deployments.server_id **刻意不建外键**
// (与 proxy_routes 同一手法:删主机由上层收敛),所以配置落点时得自己查一次,
// 免得一个错拼的 id 要到真正发布时才炸。直接读 servers 表,与 internal/group 同一样例。
func (s *Store) serverExists(ctx context.Context, id string) (bool, error) {
	return s.exists(ctx, `SELECT 1 FROM servers WHERE id = ?`, id)
}

// providerExists 报告 DNS 提供商是否存在(ACME 签发要靠它写 TXT;引用悬空等于必然签不出)。
func (s *Store) providerExists(ctx context.Context, id string) (bool, error) {
	return s.exists(ctx, `SELECT 1 FROM dns_providers WHERE id = ?`, id)
}

func (s *Store) exists(ctx context.Context, query, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, query, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cert: existence check: %w", err)
	}
	return true, nil
}

// sansJSON 把域名清单编成入库文本。nil 与空表都编成 "[]":列虽有 DEFAULT '[]',
// 显式写一致的文本省得读的时候分辨两种空形态。
func sansJSON(sans []string) string {
	if len(sans) == 0 {
		return "[]"
	}
	b, err := json.Marshal(sans)
	if err != nil {
		return "[]" // []string 不可能编失败;真失败也只能当空清单,不该让一次写入 panic
	}
	return string(b)
}

// parseSans 解回域名清单。文本损坏(手工改过库)时返回空清单而非报错:
// 页面还能列出这张证书并让人看见它坏了,比整页 500 有用。
func parseSans(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// ts 把时间写成 RFC3339 UTC 文本供入库。零值写成空串:库里 ” 就是「没有这个时间」,
// 比 0001-01-01 那种假时间好读,页面也据此区分「从未签发」。
func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTS 解回时间;空串或解析失败 → 零值。
func parseTS(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// b2i 把布尔写成 INTEGER 列(两方言都存 0/1)。
func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// nilIfEmpty 让「没有密文」写成 NULL 而不是零长度 BLOB:可空列里 NULL 才是
// 「这一列不存在」的语义(证书没私钥 / 该 CA 不要 EAB),导出与校验都据此区分。
func nilIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
