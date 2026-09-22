package build

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/huangchengsir/pipewright/internal/gitauth"
)

// cloneTimeout 是单次克隆的硬超时(防黑洞 IP / 慢 DNS 把构建 goroutine 挂死)。
const cloneTimeout = 5 * time.Minute

// 克隆领域错误(不泄漏 URL 密钥/底层细节)。
var (
	// ErrRepoBlocked 表示仓库地址被 SSRF 收口拒绝(云元数据/链路本地/回环等)。
	ErrRepoBlocked = errors.New("build: repo url blocked by ssrf guard")
	// ErrCloneFailed 表示克隆/检出失败(鉴权/网络/ref 不存在等;不泄漏底层文本)。
	ErrCloneFailed = errors.New("build: clone failed")
)

// Cloner 把项目仓库在指定 commit/branch 上克隆到**磁盘临时工作区**(Docker build 需 `.` 上下文落盘)。
//
// 与 2-5/3-6 的内存克隆(memfs)不同:构建上下文须是真实目录树供容器 CLI 读取。工作区由调用方
// (Builder)经 MkdirTemp 建、defer RemoveAll 销(宿主零污染,FR-5)。SSRF 收口复用全平台同款策略
// (见 internal/giturl):http(s) 与 ssh 均放行;拒云元数据/链路本地/回环;私网放行(自托管内网 Git 友好)。
type Cloner struct {
	// allowInsecure 仅供测试:为 true 时跳过 SSRF scheme/host 校验(放行 file:// 本地夹具)。
	// 生产路径绝不设置(NewCloner 默认 false)。
	allowInsecure bool
}

// NewCloner 构造生产 Cloner(严格 SSRF 收口)。
func NewCloner() *Cloner { return &Cloner{} }

// CloneResolved 是克隆结果:实际检出的 commit 短 sha(供产物 tag/引用),空则未解析出。
type CloneResolved struct {
	CommitShort string
}

// Clone 把 repoURL 在 ref(commit sha 优先,否则分支名;皆空则默认分支)上克隆到 destDir。
// token 对 http(s) 是访问令牌(BasicAuth.Password)、对 ssh 是私钥 PEM;两者绝不进 URL/日志/错误。
// 失败统一映射干净错误。
//
// 策略:先克隆默认/指定分支(Depth:1 浅克隆省带宽),若指定了 commit 则再 checkout 到该 commit
//
//	(commit 不在浅克隆历史里时回退为不限深克隆重试一次,best-effort)。
func (c *Cloner) Clone(ctx context.Context, repoURL, username, token, branch, commit, destDir string) (*CloneResolved, error) {
	cloneURL, auth, err := c.resolve(repoURL, username, token)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	commit = strings.TrimSpace(commit)
	branch = strings.TrimSpace(branch)

	opts := &gogit.CloneOptions{
		URL:  cloneURL,
		Auth: auth,
		Tags: gogit.NoTags,
	}
	// 指定了具体 commit 时不浅克隆(浅克隆默认分支可能不含该 commit);否则浅克隆指定/默认分支。
	if commit == "" {
		opts.Depth = 1
		opts.SingleBranch = true
		if branch != "" {
			opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
		}
	}

	repo, err := gogit.PlainCloneContext(cctx, destDir, false, opts)
	if err != nil {
		// 浅克隆 + 指定分支失败时不再重试(可能是鉴权/不可达);映射干净错误。
		return nil, ErrCloneFailed
	}

	resolved := &CloneResolved{}
	if commit != "" {
		wt, werr := repo.Worktree()
		if werr != nil {
			return nil, ErrCloneFailed
		}
		hash := plumbing.NewHash(commit)
		if cerr := wt.Checkout(&gogit.CheckoutOptions{Hash: hash, Force: true}); cerr != nil {
			return nil, ErrCloneFailed
		}
		resolved.CommitShort = shortSHA(commit)
	} else if head, herr := repo.Head(); herr == nil {
		resolved.CommitShort = shortSHA(head.Hash().String())
	}
	return resolved, nil
}

// resolve 归一化地址并构造认证(生产路径过 SSRF 收口;测试夹具 file:// 放行且匿名)。
// 返回的地址已是 go-git 可直接消费的形式(scp 语法归一化为 ssh://)。
func (c *Cloner) resolve(repoURL, username, token string) (string, transport.AuthMethod, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", nil, ErrCloneFailed
	}
	if c.allowInsecure {
		return repoURL, gitauth.HTTPAuth(repoURL, username, token), nil
	}
	r, err := gitauth.ResolveRepoURL(repoURL)
	if err != nil {
		return "", nil, ErrRepoBlocked
	}
	auth, err := gitauth.TransportFor(r, username, token)
	if err != nil {
		return "", nil, ErrCloneFailed
	}
	return r.URL, auth, nil
}

// IsRepoURLAllowed 报告地址是否通过全平台 SSRF 收口(供 repocache 等复用,不重复实现)。
func IsRepoURLAllowed(repoURL string) bool {
	_, err := gitauth.ResolveRepoURL(repoURL)
	return err == nil
}

// shortSHA 取 commit 的前 7 位(短 sha);不足 7 位原样返回。
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// mkTempWorkspace 建一个临时构建工作区目录(调用方 defer RemoveAll 销毁;宿主零污染,FR-5)。
func mkTempWorkspace() (string, error) {
	return os.MkdirTemp("", "pipewright-build-*")
}
