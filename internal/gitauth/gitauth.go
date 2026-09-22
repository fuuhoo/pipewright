// Package gitauth centralizes how an HTTPS git token is turned into BasicAuth
// credentials for clone / ls-remote across the codebase (source reader, project
// prober, build cloner, AI diff/analyze).
//
// 背景:不同平台对「经 HTTPS BasicAuth 认证」的用户名要求不同:
//   - GitHub / GitLab / 自建 Gitea 等用 **token** 时:用户名任意非空,密码=token
//     (平台只认 token),填 "git" 或真实账号都可以。
//   - GitLab / Gitea 等用**账号密码**时:用户名必须是真实账号,写 "git" 会被拒。
//   - **Gitee**:个人访问令牌走 HTTPS 时,用户名必须是「真实账号用户名」,密码=token;
//     用 "git" 当用户名会被拒(表现为「凭据错误 / 认证失败」)。这正是用户真实
//     Gitee 令牌克隆失败的根因。
//
// 因此用户名取值:凭据里填了就用它(覆盖账号密码 + Gitee 令牌两种场景);没填则
// Gitee 取 URL owner,其余 host 回退 "git"(与历史行为一致,不回归 GitHub/GitLab token)。
//
// 安全:token 仅作为 BasicAuth.Password,绝不拼进 URL / 日志 / 错误。
package gitauth

import (
	"net/url"
	"strings"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// defaultUsername 是非 Gitee 平台沿用的 BasicAuth 用户名(任意非空即可)。
const defaultUsername = "git"

// BasicAuth 依据 repoURL 选择合适的 BasicAuth 用户名,密码恒为 token。
// token 为空时返回 nil，让 go-git 按匿名公开仓访问。
func BasicAuth(repoURL, username, token string) *githttp.BasicAuth {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: Username(repoURL, username), Password: token}
}

// Username 返回该 repoURL 应使用的 BasicAuth 用户名。
//   - 凭据显式填了用户名 → 一律以它为准。GitLab/Gitea/Bitbucket 的「账号 + 密码」登录
//     要求真实用户名(写 "git" 会 401),而 token 场景平台本就忽略用户名,故两种都正确。
//   - Gitee 且未填用户名 → 取 URL 路径第一段(owner;个人仓库 owner==账号名)。
//   - 其余 → 回退 "git"(GitHub/GitLab token 场景的历史行为)。
//
// 解析失败 / 无 host 时回退 "git",绝不 panic。
func Username(repoURL, explicitUsername string) string {
	if username := strings.TrimSpace(explicitUsername); username != "" {
		return username
	}
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return defaultUsername
	}
	host := strings.ToLower(u.Hostname()) // Hostname() 自动剥离端口与用户信息
	if host == "" {
		return defaultUsername
	}
	if host == "gitee.com" || strings.HasSuffix(host, ".gitee.com") {
		if owner := firstPathSegment(u.Path); owner != "" {
			return owner
		}
	}
	return defaultUsername
}

// firstPathSegment 取 URL path 的第一段(owner)。"/cool-jiawei/aireboot.git" → "cool-jiawei"。
func firstPathSegment(p string) string {
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	return strings.TrimSpace(p)
}
