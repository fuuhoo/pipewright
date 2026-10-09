// 本文件给「按 ref 读仓库里的单个文件」补一条口子:compose 部署节点可以引用项目自带的
// docker-compose.yml,而不是要用户把整份正文再粘一遍。
//
// 走的是已有的 bare 镜像:一次增量 fetch → commit.File(path),全程不检出工作区。
// 与 Clone 不同,这里**刻意不回退直连网络克隆** —— 为一个几百字节的文本文件拉全库,
// 代价与收益完全不成比例;读不到就让调用方给出人读失败(前端仍可选粘贴正文)。
package repocache

import (
	"context"
	"io"
	"strings"

	"github.com/fuuhoo/pipewright/internal/build"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// maxFileBytes 是单个可读文件的上限:compose 正文在部署侧本就有 512 KiB 额度,
// 读得比那更多只会把内存交给一个没人该上传的东西。
const maxFileBytes = 512 << 10

// repoPathMaxLen 是仓库相对路径的长度上限(路径本身不是内容,不该有额度)。
const repoPathMaxLen = 512

// 三类失败的值定义在 internal/build(调用方只能 import 那一侧的接口,见其注释),
// 这里直接抛出,调用方按值分辨。
var (
	ErrFileNotFound = build.ErrRepoFileNotFound // 该 ref 上没有这个文件(或它不是普通文件)
	ErrBadRepoPath  = build.ErrRepoBadFilePath  // 传入的不是合法的仓库相对路径
	ErrFileTooLarge = build.ErrRepoFileTooLarge // 文件超过 maxFileBytes
)

// ValidRepoPath 报告 path 是不是一个「仓库根下的相对文件路径」。
// 绝对路径、反斜杠、空段、. 与 .. 一律拒 —— 它由用户在流水线表单里填,不能拿去当寻址用。
func ValidRepoPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > repoPathMaxLen {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ReadFile 读出 repoURL 在 branch/commit 上那份 path 文件的内容。
// branch/commit 语义与 Clone 一致(commit 优先,皆空取 HEAD)。ref 解析不到按 ErrFileNotFound。
func (c *Cache) ReadFile(ctx context.Context, repoURL, username, token, branch, commit, path string) ([]byte, error) {
	if !ValidRepoPath(path) {
		return nil, ErrBadRepoPath
	}
	lock := c.repoLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	mirror, err := c.ensureMirror(ctx, repoURL, username, token)
	if err != nil {
		return nil, err
	}
	repo, oerr := gogit.PlainOpen(mirror)
	if oerr != nil {
		return nil, oerr
	}
	comm, cerr := c.commitAt(repo, branch, commit)
	if cerr != nil {
		return nil, cerr
	}
	file, ferr := comm.File(strings.TrimSpace(path))
	if ferr != nil {
		return nil, ErrFileNotFound
	}
	if sz := file.Size; sz > maxFileBytes {
		return nil, ErrFileTooLarge
	}
	r, rerr := file.Reader()
	if rerr != nil {
		return nil, rerr
	}
	defer func() { _ = r.Close() }()
	buf, aerr := io.ReadAll(io.LimitReader(r, maxFileBytes+1))
	if aerr != nil {
		return nil, aerr
	}
	if len(buf) > maxFileBytes {
		return nil, ErrFileTooLarge
	}
	return buf, nil
}

// commitAt 按「commit 优先 → branch → HEAD」解析出一个提交对象。
func (c *Cache) commitAt(repo *gogit.Repository, branch, commit string) (*object.Commit, error) {
	ref := strings.TrimSpace(commit)
	if ref == "" {
		ref = strings.TrimSpace(branch)
	}
	if ref == "" {
		head, herr := repo.Head()
		if herr != nil {
			return nil, ErrFileNotFound
		}
		ref = head.Hash().String()
	}
	var hash plumbing.Hash
	if h, rerr := repo.ResolveRevision(plumbing.Revision(ref)); rerr == nil && h != nil {
		hash = *h
	} else {
		hash = plumbing.NewHash(ref)
	}
	comm, cerr := repo.CommitObject(hash)
	if cerr != nil {
		return nil, ErrFileNotFound
	}
	return comm, nil
}
