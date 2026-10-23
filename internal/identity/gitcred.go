// gitcred.go 是「登录这次授权，顺手变成一条能克隆仓库的凭据」那一小块。
//
// 为什么和认人逻辑放在同一个包:自动同步凭据改变的是「一次登录换来多大权限」——
// 关掉时登录只申请身份 scope，打开时连仓库读权限一起拿到手。这条判定和
// 「谁能拿到哪个账号」是同一件事，拆到两个包反而会两边各写一半规则、谁也看不全。
//
// 四条规矩:
//  1. 只写 personal、owner 恒为这次登录落到的那个账号 —— 同步出来的凭据绝不进全局，
//     否则任何人的登录都会把私有仓令牌变成全站可用。
//  2. 幂等键是这条映射行本身(唯一键 (provider, external_id))：重复登录只轮换那一条凭据，
//     不会每次新建一条把人的凭据页淹掉。
//  3. 回指失效就重新建，绝不改写：凭据被人删了 / 被改成全局 / owner 不是这个人了 ——
//     三种都意味着「这条已经不是我们那条」，继续往里写等于覆盖别人的凭据(接管面)。
//  4. 同步失败不影响登录：登进来是本来的契约，凭据只是附带。失败要看得见(日志 + 下次登录重试),
//     但不能把人挡在门外。
package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// GitCredentialStore 是本包需要的保险库能力子集(注入用,便于测试)。
// nil = 同步关闭:登录照旧成功,什么也不写(未配 master key 的部署就长期停在这个形态)。
type GitCredentialStore interface {
	Create(in vault.CreateInput) (*vault.Credential, error)
	Update(id string, in vault.UpdateInput) (*vault.Credential, error)
	// View 只读元数据(scope / owner_id),用于规矩 3 的归属复核;绝不为拿明文而调它。
	View(id string) (*vault.Credential, error)
	SealSecret(plaintext []byte) ([]byte, error)
	OpenSecret(sealed []byte) ([]byte, error)
}

// RefreshLead 是「离到期还有多久就该去续期」的窗口。
//
// 为什么提前而不是等过期:续期要访问平台,而平台正在维护 / 网络正好断了的时候,
// 令牌会在跑构建的那一秒突然失效 —— 表现为一堆流水线同时克隆失败。留出这一窗口,
// 一次续期失败还有几十次重试机会(扫描器每轮都会再看一眼)。
const RefreshLead = 30 * time.Minute

// staleReason 是续期彻底失败后写进凭据备注的那句话。
// 为什么还要留着凭据而不是删掉:删了会连带让项目配置里的引用变成悬挂引用(vault 本来就拦删除),
// 而且用户会以为是自己手滑弄丢了；留一条说得清原因的备注,他才晓得该去重新登录一次。
const staleReason = "OAuth 授权已失效(续期被平台拒绝),请重新用 git 账号登录一次以恢复此凭据"

// syncGitCredential 把这次登录/绑定拿到的 git 令牌落到该账号的一条个人凭据上(幂等)。
//
// grant 为 nil = 这个登录应用没开凭据同步(或身份源给不出 git 令牌),直接返回不做事。
// 成功时把凭据 id、续期材料、到期时刻与实授 scope 一起写回映射行。
func (s *Service) syncGitCredential(ctx context.Context, b *Binding, id *oauth.Identity, grant *oauth.Grant) string {
	if s.creds == nil || grant == nil || strings.TrimSpace(grant.AccessToken) == "" {
		return ""
	}
	credID, err := s.upsertCredential(ctx, b, id, grant)
	if err != nil {
		// 登录已经判定通过,这里只报失败、不改变结果(见包注释规矩 4)。
		// 错误体里只有 id 与原因:令牌明文绝不进日志。
		log.Printf("[identity] %s 凭据同步失败(登录照常成功):%v", b.Provider, err)
		return ""
	}
	b.CredentialID = credID
	return credID
}

// upsertCredential 走「有回指就轮换、没有就新建」那两条路,返回凭据 id。
func (s *Service) upsertCredential(ctx context.Context, b *Binding, id *oauth.Identity, grant *oauth.Grant) (string, error) {
	if b.CredentialID != "" {
		owner, err := s.creds.View(b.CredentialID)
		switch {
		case errors.Is(err, vault.ErrNotFound):
			// 用户把这条凭据删了:当作「没同步过」重新建一条,而不是报错 ——
			// 「删掉自动同步的凭据」之后就永远登不进去了是更难解释的结果。
		case err != nil:
			return "", fmt.Errorf("查看既有凭据: %w", err)
		case owner.Scope != "personal" || owner.OwnerID != b.UserID:
			// 回指的那条已经不是「这个人的私有凭据」了(被改成全局 / 归属换了 / id 被复用)。
			// 继续往里写等于拿这次登录的令牌覆盖别人的凭据,那是最直接的接管面。
			// 宁可给这个人新建一条,也绝不写它。
			log.Printf("[identity] %s 映射 %s 回指的凭据 %s 归属已变(作用域 %s),改为新建",
				b.Provider, b.ID, b.CredentialID, owner.Scope)
		default:
			// 只轮换密钥,用户自己改过的名字/备注一律不动 —— 唯一的例外是这条凭据被上一轮
			// 扫描标成了「授权已失效」:这次登录既然带回了可用的令牌,那句判词就成了假信息,
			// 不洗掉它就等于告诉用户一条其实好着的凭据已死(而他也无从知道该重新登录几次)。
			in := vault.UpdateInput{Secret: &grant.AccessToken}
			if owner.Description == staleReason {
				d := credentialDescription(b.Provider, id)
				in.Description = &d
			}
			if _, uerr := s.creds.Update(b.CredentialID, in); uerr != nil {
				return "", fmt.Errorf("轮换凭据 %s: %w", b.CredentialID, uerr)
			}
			if serr := s.storeGrant(ctx, b.ID, b.CredentialID, grant); serr != nil {
				return "", serr
			}
			return b.CredentialID, nil
		}
	}

	c, err := s.creds.Create(vault.CreateInput{
		Name:    credentialName(b.Provider, id),
		Type:    vault.TypeGitToken,
		Scope:   "personal",
		OwnerID: b.UserID,
		// 用户名走 BasicAuth 的用户名位:GitLab/GitHub 用它无所谓,**Gitee 必须真实账号**
		// (填 "git" 会被拒,这正是历史上 Gitee 令牌克隆失败的根因,见 internal/gitauth)。
		Username:    id.Login,
		Secret:      grant.AccessToken,
		CreatedBy:   b.UserID,
		Description: credentialDescription(b.Provider, id),
	})
	if err != nil {
		return "", fmt.Errorf("新建凭据: %w", err)
	}
	if serr := s.storeGrant(ctx, b.ID, c.ID, grant); serr != nil {
		// 凭据已经建好了,只是续期材料没落上:下次登录仍会走「View 到归属正确 → 轮换」这条路,
		// 不会重复建第二条。这里报错是为了让人去查数据库,而不是静默给出一个「看起来同步成功了」的假象。
		return c.ID, serr
	}
	return c.ID, nil
}

// credentialName 是同步凭据的展示名。带上平台与账号,用户在凭据页一眼能看出这是哪次登录给的;
// 不做唯一约束(库里 name 本就没有 UNIQUE),真正的幂等由映射行的回指保证。
func credentialName(provider string, id *oauth.Identity) string {
	return provider + ":" + displayHandle(id)
}

// credentialDescription 是这条凭据的「来路说明」。同步出来的凭据是用户没建过的一条,
// 列表里必须一眼看得懂它是谁给的,否则和凭据被凭空塞进账户没有区别。
func credentialDescription(provider string, id *oauth.Identity) string {
	return fmt.Sprintf("OAuth 登录自动同步(%s:%s),仅本人可见", provider, displayHandle(id))
}

// storeGrant 把凭据回指与续期材料写回映射行。refresh_token 为空时把那三列一并清空 ——
// 留着一张旧平台的续期凭证只会让扫描器去撞一个已经不存在的授权。
func (s *Service) storeGrant(ctx context.Context, bindingID, credID string, grant *oauth.Grant) error {
	var sealed []byte
	if grant.RefreshToken != "" {
		b, err := s.creds.SealSecret([]byte(grant.RefreshToken))
		if err != nil {
			// 没有续期材料也不能把访问令牌丢掉不写:凭据本身照常用,只是到期后需要重新登录。
			log.Printf("[identity] 续期凭证加密失败,本次只同步访问令牌:%v", err)
		} else {
			sealed = b
		}
	}
	expires := ""
	if !grant.ExpiresAt.IsZero() {
		expires = grant.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE user_identities SET credential_id = ?, git_refresh_ciphertext = ?, git_expires_at = ?,
		       git_scope = ?, updated_at = ? WHERE id = ?`,
		credID, sealed, expires, grant.Scope, time.Now().UTC().Format(time.RFC3339), bindingID)
	if err != nil {
		return fmt.Errorf("identity: store grant: %w", err)
	}
	return nil
}

// PendingRefresh 是一条到期待续期的外部身份(扫描器的输入)。
type PendingRefresh struct {
	BindingID    string
	UserID       string
	Provider     string
	CredentialID string
	ExpiresAt    time.Time
}

// ListStaleGrants 列出「同步过凭据、留了续期凭证、且访问令牌已进入续期窗口」的映射。
//
// 为什么在 Go 里比时间而不是 SQL:expires 存的是 RFC3339 文本,字符串比较只在「同一格式、
// 同一时区、无小数秒」时才等于时间比较,而这正是最容易因为将来改格式而悄悄失效的一种条件 ——
// 映射行的量级是一个个登录用户,读全量再筛完全不亏,换个放心的比较方式更值。
func (s *Service) ListStaleGrants(ctx context.Context, lead time.Duration, now time.Time) ([]PendingRefresh, error) {
	if s.creds == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, provider, credential_id, git_expires_at, git_refresh_ciphertext
		 FROM user_identities WHERE credential_id <> '' AND git_expires_at <> ''`)
	if err != nil {
		return nil, fmt.Errorf("identity: list stale grants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]PendingRefresh, 0)
	for rows.Next() {
		var (
			p          PendingRefresh
			expiresRaw string
			sealed     []byte
		)
		if err := rows.Scan(&p.BindingID, &p.UserID, &p.Provider, &p.CredentialID, &expiresRaw, &sealed); err != nil {
			return nil, fmt.Errorf("identity: scan stale grant: %w", err)
		}
		// 没有续期凭证的行(该平台只发短时令牌)扫出来也没用,当场跳过。
		if len(sealed) == 0 {
			continue
		}
		exp, perr := time.Parse(time.RFC3339, expiresRaw)
		if perr != nil {
			continue
		}
		p.ExpiresAt = exp
		if exp.Sub(now.UTC()) > lead {
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// refreshOne 续期单条映射:成功则同时更新凭据明文与续期材料,失败则把凭据标成人读的原因。
func (s *Service) refreshOne(ctx context.Context, p PendingRefresh) (bool, error) {
	sealed, err := s.readRefreshSealed(ctx, p.BindingID)
	if err != nil {
		return false, err
	}
	if len(sealed) == 0 {
		return false, nil // 已被别处清掉(并发登录/解绑):不报错,也不算续期
	}
	plain, err := s.creds.OpenSecret(sealed)
	if err != nil {
		return false, s.markStale(ctx, p, fmt.Errorf("解密续期凭证(疑似 master key 换过): %w", err))
	}
	grant, rerr := s.apps.RefreshGrant(ctx, p.Provider, string(plain))
	if rerr != nil {
		plain = nil
		// 平台拒绝(用户在 git 站上撤销了授权 / 应用被删):重试一万次也不会成,标成失效等下次登录。
		return false, s.markStale(ctx, p, fmt.Errorf("向 %s 续期被拒: %w", p.Provider, rerr))
	}
	// 续期时平台常常**不换发** refresh_token(RFC 6749 允许省略)。oauth 层的 requestRefresh
	// 已经沿用了旧的那张,这里再兜一次:这张凭证一旦落空,这个人就再也续不动了,只能重新登录 ——
	// 而「重新登录」在流水线里意味着一次无人值守的失败。
	if grant.RefreshToken == "" {
		grant.RefreshToken = string(plain)
	}
	plain = nil
	if _, uerr := s.creds.Update(p.CredentialID, vault.UpdateInput{Secret: &grant.AccessToken}); uerr != nil {
		if errors.Is(uerr, vault.ErrNotFound) {
			// 凭据被删了:续期材料随之作废,清掉映射,下次登录重新建一条。
			if cerr := s.clearGrant(ctx, p.BindingID); cerr != nil {
				return false, cerr
			}
			return false, nil
		}
		return false, fmt.Errorf("identity: refresh update credential: %w", uerr)
	}
	if serr := s.storeGrant(ctx, p.BindingID, p.CredentialID, grant); serr != nil {
		return false, serr
	}
	return true, nil
}

// RefreshGrants 扫一轮到期的同步凭据并就地续期,返回 (续期成功数, 标为失效数, error)。
// 单条失败只记不进 error:一轮里一个人的令牌续不动,不该让其余人也停止被照看。
func (s *Service) RefreshGrants(ctx context.Context) (int, int, error) {
	if s.creds == nil || s.apps == nil {
		return 0, 0, nil
	}
	now := time.Now().UTC()
	due, err := s.ListStaleGrants(ctx, RefreshLead, now)
	if err != nil {
		return 0, 0, err
	}
	var ok, failed int
	for _, p := range due {
		refreshed, rerr := s.refreshOne(ctx, p)
		if rerr != nil {
			failed++
			log.Printf("[identity] %s 凭据 %s 续期失败:%v", p.Provider, p.CredentialID, rerr)
			continue
		}
		if refreshed {
			ok++
		}
	}
	if ok > 0 || failed > 0 {
		log.Printf("[identity] git 凭据续期扫描:%d 条待续,成功 %d、失效 %d", len(due), ok, failed)
	}
	return ok, failed, nil
}

// readRefreshSealed 现读一条映射的续期凭证密文,而不是用扫描器先前取到的那一份:
// 中间可能又登录过一次并把它换掉了,拿旧的去续只会白撞一次墙。
func (s *Service) readRefreshSealed(ctx context.Context, bindingID string) ([]byte, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT git_refresh_ciphertext FROM user_identities WHERE id = ?`, bindingID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: read refresh token: %w", err)
	}
	return sealed, nil
}

// markStale 把一条续不动的授权写进凭据备注,并清掉映射上的续期材料(不再每轮撞一次)。
// 凭据本身保留:项目/流水线还引用着它,删掉只会换来一个更难解释的「凭据不存在」。
func (s *Service) markStale(ctx context.Context, p PendingRefresh, cause error) error {
	reason := staleReason
	if _, err := s.creds.Update(p.CredentialID, vault.UpdateInput{Description: &reason}); err != nil &&
		!errors.Is(err, vault.ErrNotFound) {
		return fmt.Errorf("%w(且标注凭据也失败: %v)", cause, err)
	}
	if err := s.clearGrant(ctx, p.BindingID); err != nil {
		return err
	}
	return cause
}

// clearGrant 清空映射上的令牌材料(保留 credential_id:它仍指向那条待人工处理的凭据)。
func (s *Service) clearGrant(ctx context.Context, bindingID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE user_identities SET git_refresh_ciphertext = NULL, git_expires_at = '' WHERE id = ?`, bindingID)
	if err != nil {
		return fmt.Errorf("identity: clear grant: %w", err)
	}
	return nil
}
