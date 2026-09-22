package gitauth

import "testing"

func TestUsername(t *testing.T) {
	cases := []struct {
		name    string
		repoURL string
		want    string
	}{
		// Gitee:取 owner 段当用户名(用户的真实仓库场景)
		{"gitee personal repo", "https://gitee.com/cool-jiawei/aireboot.git", "cool-jiawei"},
		{"gitee no .git suffix", "https://gitee.com/cool-jiawei/aireboot", "cool-jiawei"},
		{"gitee with port", "https://gitee.com:443/octo/app.git", "octo"},
		{"gitee subdomain", "https://api.gitee.com/octo/app.git", "octo"},
		{"gitee with creds in url", "https://u:p@gitee.com/octo/app.git", "octo"},
		// 非 Gitee:沿用 "git"(不回归)
		{"github", "https://github.com/octo/app.git", "git"},
		{"gitlab", "https://gitlab.com/octo/app.git", "git"},
		{"self-hosted", "https://git.example.com/octo/app.git", "git"},
		// 退化:仍回退 "git",不 panic
		{"empty", "", "git"},
		{"garbage", "::::not a url", "git"},
		{"gitee no path", "https://gitee.com", "git"},
		{"gitee root slash", "https://gitee.com/", "git"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Username(c.repoURL, ""); got != c.want {
				t.Errorf("Username(%q, empty) = %q, want %q", c.repoURL, got, c.want)
			}
		})
	}
}

func TestBasicAuthCarriesToken(t *testing.T) {
	auth := BasicAuth("https://gitee.com/cool-jiawei/aireboot.git", "actual-account", "token-value")
	if auth.Username != "actual-account" {
		t.Errorf("username = %q, want actual-account", auth.Username)
	}
	if auth.Password != "token-value" {
		t.Errorf("password not carried through")
	}
}

func TestBasicAuthReturnsNilWithoutToken(t *testing.T) {
	if auth := BasicAuth("file:///tmp/fixture", "", ""); auth != nil {
		t.Fatalf("empty token should use anonymous access, got %+v", auth)
	}
}

// TestUsernameUsesExplicitAccount 验证凭据里显式填的用户名对所有平台都生效
// (GitLab/Gitea 的「账号 + 密码」要求真实用户名,写死 "git" 会被拒)。
func TestUsernameUsesExplicitAccount(t *testing.T) {
	for _, c := range []struct{ repoURL, want string }{
		{"https://gitee.com/university-org/private-repo.git", "actual-account"},
		{"https://github.com/org/private-repo.git", "actual-account"},
		{"http://172.17.4.41/fuuhoo/sw-lowcode.git", "actual-account"},
	} {
		if got := Username(c.repoURL, "actual-account"); got != c.want {
			t.Fatalf("Username(%q, actual-account) = %q, want %q", c.repoURL, got, c.want)
		}
	}
}

// TestBasicAuthUsesExplicitUsername 验证 BasicAuth 透传凭据用户名 + 密钥当密码。
func TestBasicAuthUsesExplicitUsername(t *testing.T) {
	auth := BasicAuth("http://172.17.4.41/fuuhoo/sw-lowcode.git", "fuuhoo", "p@ssw0rd")
	if auth.Username != "fuuhoo" || auth.Password != "p@ssw0rd" {
		t.Fatalf("BasicAuth = %+v, want username=fuuhoo", auth)
	}
}
