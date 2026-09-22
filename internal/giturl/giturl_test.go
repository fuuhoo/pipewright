package giturl

import "testing"

func TestParseHTTP(t *testing.T) {
	cases := []struct {
		raw  string
		host string
	}{
		{"https://gitee.com/org/repo.git", "gitee.com"},
		{"http://172.17.4.41/fuuhoo/sw-lowcode.git", "172.17.4.41"},
		{"  https://git.example.com/a/b.git  ", "git.example.com"},
	}
	for _, c := range cases {
		r, ok := Parse(c.raw)
		if !ok || !r.IsHTTP() || r.IsSSH() {
			t.Fatalf("Parse(%q) ok=%v kind=%v, want http", c.raw, ok, r.Kind)
		}
		if r.Host != c.host {
			t.Fatalf("Parse(%q).Host = %q, want %q", c.raw, r.Host, c.host)
		}
		if r.URL != trimSpace(c.raw) {
			t.Fatalf("Parse(%q).URL = %q, want raw trimmed", c.raw, r.URL)
		}
	}
}

func TestParseSCP(t *testing.T) {
	r, ok := Parse("git@172.17.4.41:fuuhoo/sw-lowcode.git")
	if !ok || !r.IsSSH() {
		t.Fatalf("scp parse ok=%v kind=%v, want ssh", ok, r.Kind)
	}
	if r.URL != "ssh://git@172.17.4.41/fuuhoo/sw-lowcode.git" {
		t.Fatalf("URL = %q", r.URL)
	}
	if r.Host != "172.17.4.41" || r.User != "git" || !r.UserExplicit {
		t.Fatalf("host/user = %q/%q explicit=%v", r.Host, r.User, r.UserExplicit)
	}
	if r.Path != "fuuhoo/sw-lowcode.git" {
		t.Fatalf("path = %q", r.Path)
	}
}

func TestParseSSHURL(t *testing.T) {
	// 带显式用户与端口
	r, ok := Parse("ssh://deploy@git.example.com:2222/group/repo.git")
	if !ok || !r.IsSSH() {
		t.Fatalf("ssh:// parse ok=%v kind=%v", ok, r.Kind)
	}
	if r.URL != "ssh://deploy@git.example.com:2222/group/repo.git" {
		t.Fatalf("URL = %q", r.URL)
	}
	if r.Port != 2222 || r.User != "deploy" || !r.UserExplicit {
		t.Fatalf("port/user/explicit = %d/%q/%v", r.Port, r.User, r.UserExplicit)
	}
	// 无用户 → 默认 git
	r2, ok2 := Parse("ssh://git.example.com/group/repo.git")
	if !ok2 || r2.User != "git" || r2.UserExplicit {
		t.Fatalf("default user: %q explicit=%v", r2.User, r2.UserExplicit)
	}
	if r2.URL != "ssh://git@git.example.com/group/repo.git" {
		t.Fatalf("normalized URL = %q", r2.URL)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"file:///etc/passwd",
		"git://host/repo.git",
		"ftp://host/x",
		"/local/path/repo.git",
		"./relative/repo",
		"C:\\repo",
		"git@host",         // 无 path
		"git@host:/abs",    // path 以 / 开头 → 视为本地路径
		"host:path",        // 无 user
		"ssh://host",       // 无 path 也可连接?hostname 在但 path 空 → 仍算 ssh(go-git 层失败)
	} {
		if r, ok := Parse(raw); ok && raw != "ssh://host" {
			t.Fatalf("Parse(%q) should be invalid, got %+v", raw, r)
		}
	}
}

func TestBlockedHost(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "localhost", "169.254.169.254", "169.254.0.1", "0.0.0.0", "",
	}
	for _, h := range blocked {
		if !BlockedHost(h) {
			t.Fatalf("BlockedHost(%q) = false, want true", h)
		}
	}
	allowed := []string{
		"172.17.4.41", "10.0.0.5", "192.168.1.10", "gitee.com", "git.example.com",
	}
	for _, h := range allowed {
		if BlockedHost(h) {
			t.Fatalf("BlockedHost(%q) = true, want false", h)
		}
	}
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}
