package project

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/huangchengsir/pipewright/internal/gitauth"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
)

// probeTimeout 是单次 ls-remote 探测的硬超时(防黑洞 IP/慢 DNS 把请求 goroutine 挂到 OS TCP 超时)。
const probeTimeout = 15 * time.Second

// goGitProber 用 go-git 的 ListRemote(= git ls-remote 语义)做仓库连通校验。
// 纯 Go,不要求宿主装 git;HTTPS 走 token(BasicAuth)、SSH 走私钥(PublicKeys),
// 两类都由 gitauth 统一构造(参数化 API,绝不拼命令字符串)。
// 全程在内存进行(memory.NewStorage),不落任何工作区/对象到磁盘。
//
// SSRF 收口:见 gitauth.ResolveRepoURL / internal/giturl —— 拒云元数据/链路本地/回环,
// 私网(自托管内网 Git)放行。allowInsecureSchemes 仅供测试注入(放行 file:// 等本地夹具),
// 生产构造(project.New 默认)永远为 false。
type goGitProber struct {
	// allowInsecureSchemes 仅供测试:为 true 时跳过 scheme/host SSRF 校验(放行 file:// 夹具)。
	// 生产路径绝不设置此字段。
	allowInsecureSchemes bool
}

// Probe 用 token 对 repoURL 做 ListRemote,成功返回远端默认分支(由 HEAD 符号引用解析)。
func (p goGitProber) Probe(ctx context.Context, repoURL, username, token string) (string, error) {
	branch, _, _, err := p.ProbeRefs(ctx, repoURL, username, token)
	return branch, err
}

// ProbeRefs 与 Probe 相同的 ls-remote 探测,但额外返回全部分支/tag 短名
// (供前端「新建项目」弹窗的默认分支下拉)。失败返回 (”, nil, nil, 干净领域错误)。
//
// 安全:token/私钥仅作为认证对象字段经参数化 API 传入,绝不进 URL/日志/错误。
// 失败统一映射为干净领域错误:鉴权类 → ErrCredentialError;其余(DNS/连接/不存在)
// → ErrRepoUnreachable。返回的错误不 %w 原始错误,避免把含敏感细节的底层错误外泄。
func (p goGitProber) ProbeRefs(ctx context.Context, repoURL, username, token string) (string, []string, []string, error) {
	if strings.TrimSpace(repoURL) == "" {
		return "", nil, nil, ErrRepoUnreachable
	}

	repo, auth, err := p.resolve(repoURL, username, token)
	if err != nil {
		return "", nil, nil, err
	}

	rem := gogit.NewRemote(memory.NewStorage(), &gogitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{repo},
	})
	// 硬超时:防黑洞 IP/慢 DNS 把请求 goroutine 挂死。
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	refs, err := rem.ListContext(cctx, &gogit.ListOptions{Auth: auth})
	if err != nil {
		return "", nil, nil, classifyProbeErr(err)
	}

	return defaultBranchFromRefs(refs), branchNamesFromRefs(refs), tagNamesFromRefs(refs), nil
}

// resolve 归一化地址并构造认证:生产路径过 SSRF 收口,测试夹具(file://)放行且匿名访问。
// 返回的 URL 才是交给 go-git 的地址(scp 语法已归一化为 ssh://)。
func (p goGitProber) resolve(repoURL, username, token string) (string, transport.AuthMethod, error) {
	if p.allowInsecureSchemes {
		return strings.TrimSpace(repoURL), gitauth.HTTPAuth(repoURL, username, token), nil
	}
	r, err := gitauth.ResolveRepoURL(repoURL)
	if err != nil {
		return "", nil, ErrRepoUnreachable
	}
	auth, err := gitauth.TransportFor(r, username, token)
	if err != nil {
		// 私钥类错误说明凭据本身不对,归凭据错误;其余(git_http 存了非 PEM 等)同样归凭据错误。
		if errors.Is(err, gitauth.ErrInvalidPrivateKey) || errors.Is(err, gitauth.ErrEncryptedPrivateKey) {
			return "", nil, ErrCredentialError
		}
		return "", nil, ErrRepoUnreachable
	}
	return r.URL, auth, nil
}

// branchNamesFromRefs 提取全部 refs/heads/* 分支短名(去重后按字母序,与代码管理区一致)。
func branchNamesFromRefs(refs []*plumbing.Reference) []string {
	names := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		if r.Name().IsBranch() && !seen[r.Name().Short()] {
			seen[r.Name().Short()] = true
			names = append(names, r.Name().Short())
		}
	}
	sort.Strings(names)
	return names
}

// tagNamesFromRefs 提取全部 refs/tags/* tag 名(剔除 peeled ^{} 重复项,去重后按字母序)。
func tagNamesFromRefs(refs []*plumbing.Reference) []string {
	names := make([]string, 0, 16)
	seen := make(map[string]bool, 16)
	for _, r := range refs {
		if r.Name().IsTag() && !strings.HasSuffix(r.Name().String(), "^{}") && !seen[r.Name().Short()] {
			seen[r.Name().Short()] = true
			names = append(names, r.Name().Short())
		}
	}
	sort.Strings(names)
	return names
}

// classifyProbeErr 把 go-git/transport 错误映射为干净领域错误(不携带底层文本)。
//
// 凭据类判定优先靠 go-git/transport 的哨兵错误,绝不做 "401"/"403" 文本子串嗅探
// (会把 "403ms"、含数字的主机名等无关文本误判为鉴权失败)。无法明确归为凭据错误的
// 一律归不可达(DNS/连接/仓库不存在/协议错误等)。
func classifyProbeErr(err error) error {
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired),
		errors.Is(err, transport.ErrAuthorizationFailed),
		errors.Is(err, transport.ErrInvalidAuthMethod):
		return ErrCredentialError
	case isSSHAuthFailure(err):
		return ErrCredentialError
	default:
		return ErrRepoUnreachable
	}
}

// sshAuthSentinels 是 SSH 鉴权失败的稳定特征串。go-git 的 ssh 传输把 x/crypto 的握手错误
// 原样返回(不像 HTTPS 那样包装成 transport 哨兵),所以这里只能按文本归类:特征取整句
// 措辞而非数字/短词,不会与主机名、路径、耗时等文本相撞。私钥不对 / 公钥未在 Git 服务
// 注册都会走到这里 —— 归成凭据错误,界面才不会把「密钥没登记」报成「仓库不可达」。
var sshAuthSentinels = []string{
	"unable to authenticate",
	"no supported methods remain",
}

func isSSHAuthFailure(err error) bool {
	msg := err.Error()
	for _, s := range sshAuthSentinels {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// defaultBranchFromRefs 从 ls-remote 引用列表解析远端默认分支(HEAD 指向的分支短名)。
// 解析不出时返回空字符串(调用方按缺省处理,不视为错误)。
func defaultBranchFromRefs(refs []*plumbing.Reference) string {
	// HEAD 通常是指向 refs/heads/<branch> 的符号引用。
	for _, r := range refs {
		if r.Name() == plumbing.HEAD && r.Type() == plumbing.SymbolicReference {
			return r.Target().Short()
		}
	}
	// 退化:若 HEAD 是 hash 引用,匹配同 hash 的某个分支。
	var headHash plumbing.Hash
	for _, r := range refs {
		if r.Name() == plumbing.HEAD {
			headHash = r.Hash()
			break
		}
	}
	if !headHash.IsZero() {
		for _, r := range refs {
			if r.Name().IsBranch() && r.Hash() == headHash {
				return r.Name().Short()
			}
		}
	}
	return ""
}
