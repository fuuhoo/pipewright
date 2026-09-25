package repocache

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ReadFile 是 compose「引用仓库文件」的地基:它必须能在**不检出工作区**的前提下,
// 按 commit / 按分支 / 按 HEAD 三种寻址读到同一份内容 —— 部署节点跑的时候,
// 工作区可能早就不在了。
func TestReadFileByCommitBranchAndHead(t *testing.T) {
	src, commit := makeSourceRepo(t)
	c := newTestCache(t)
	ctx := context.Background()

	body, err := c.ReadFile(ctx, src, "", "", "", commit, "README.md")
	if err != nil {
		t.Fatalf("按 commit 读: %v", err)
	}
	if string(body) != "v1 on main" {
		t.Errorf("按 commit 读到的内容 = %q", body)
	}

	body, err = c.ReadFile(ctx, src, "", "", "master", "", "README.md")
	if err != nil {
		t.Fatalf("按分支读: %v", err)
	}
	if string(body) != "v1 on main" {
		t.Errorf("按分支读到的内容 = %q", body)
	}

	body, err = c.ReadFile(ctx, src, "", "", "", "", "README.md")
	if err != nil {
		t.Fatalf("HEAD 兜底读: %v", err)
	}
	if string(body) != "v1 on main" {
		t.Errorf("HEAD 读到的内容 = %q", body)
	}
}

// 分支上独有的文件按别的分支读不到 —— 读错分支等于发错正文,必须失败而不是给一份相近的。
func TestReadFileRespectsBranch(t *testing.T) {
	src, _ := makeSourceRepo(t)
	c := newTestCache(t)
	if _, err := c.ReadFile(context.Background(), src, "", "", "feature", "", "feature.txt"); err != nil {
		t.Fatalf("feature 分支上应有 feature.txt: %v", err)
	}
	if _, err := c.ReadFile(context.Background(), src, "", "", "master", "", "feature.txt"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("master 上没有 feature.txt,应报 ErrFileNotFound,got %v", err)
	}
}

func TestReadFileNotFound(t *testing.T) {
	src, _ := makeSourceRepo(t)
	c := newTestCache(t)
	if _, err := c.ReadFile(context.Background(), src, "", "", "master", "", "nope.yml"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("got %v, want ErrFileNotFound", err)
	}
}

// 路径门是边界防护,不是输入提示:绝对路径、越界段、反斜杠一律在读仓库**之前**拒掉,
// 否则一个填错的 compose 路径就成了任意文件读取器。
func TestReadFileRejectsBadPaths(t *testing.T) {
	src, _ := makeSourceRepo(t)
	c := newTestCache(t)
	for _, p := range []string{"/etc/passwd", "../README.md", "./README.md", "a//b", "", strings.Repeat("d/", 300) + "x.yml"} {
		if _, err := c.ReadFile(context.Background(), src, "", "", "master", "", p); !errors.Is(err, ErrBadRepoPath) {
			t.Errorf("路径 %q 应被拒,got %v", p, err)
		}
	}
	// 目录不是文件:go-git 会给出一个「文件对象」形状的错,归入 NotFound 而不是透出内部报错。
	if _, err := c.ReadFile(context.Background(), src, "", "", "master", "", ".git"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("读目录应报 ErrFileNotFound,got %v", err)
	}
}

func TestValidRepoPath(t *testing.T) {
	// 这一层只管「能不能拿去寻址」,不管后缀是不是 yaml —— 那是 compose 的业务规则,
	// 在 pipeline.RepoYAMLPathOK 里判(两处判法不同是有意的:这里守边界,那里守表单)。
	ok := []string{"docker-compose.yml", "deploy/docker-compose.yml", "a/b.c/compose.yaml", "notes.txt"}
	bad := []string{"", "  ", "/abs.yml", `..\x.yml`, "a/../b.yml", "a//b.yml", "./a.yml", "a/./b.yml"}
	for _, p := range ok {
		if !ValidRepoPath(p) {
			t.Errorf("ValidRepoPath(%q) = false, want true", p)
		}
	}
	for _, p := range bad {
		if ValidRepoPath(p) {
			t.Errorf("ValidRepoPath(%q) = true, want false", p)
		}
	}
}
