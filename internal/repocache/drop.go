package repocache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// drop.go —— 镜像的「看得见体积 + 一键删掉」,供项目的「清除本地缓存」端点使用。
//
// 为什么需要显式清除:镜像是**持久** bare 仓库,构建只增量 fetch。日常这很好,但当用户怀疑
// 本地镜像和远程不一致(ref 被强推过、镜像半截损坏、想从干净全量重来)时,平台上没有任何出口
// —— 只能人工去删目录,而那个目录在容器形态下落在具名卷里,人根本找不到。

// ErrBusy 表示该仓库的镜像正被一次构建占用(fetch/checkout 持着仓库锁)。
// 清除是「顺手做」的运维动作,绝不排队等一场构建结束(那可能等十几分钟),所以这里用 TryLock
// 直接回绝,让调用方告诉用户稍后再试。
var ErrBusy = errors.New("repocache: mirror busy")

// MirrorInfo 是某仓库本地镜像在磁盘上的实况。Path 一律给出(即便不存在),便于日志里说清删了哪儿。
type MirrorInfo struct {
	Path   string
	Bytes  int64
	Exists bool
}

// MirrorInfoOf 返回某仓库镜像的磁盘实况(只 stat,不触网、不加锁;镜像不存在时 Bytes=0)。
func (c *Cache) MirrorInfoOf(repoURL string) MirrorInfo {
	if c == nil {
		return MirrorInfo{}
	}
	return statDir(c.mirrorPath(repoURL))
}

// DropMirror 删掉某仓库的本地镜像,返回**删除前**的实况。
// 镜像本就不存在 = 成功(目标状态已达成,Bytes=0);正被构建占用时返回 ErrBusy 且什么都没删。
// 删完下一次构建会重新 mirror clone(全量,从远程拿最新),这正是「不受以前影响」的语义。
func (c *Cache) DropMirror(repoURL string) (MirrorInfo, error) {
	if c == nil {
		return MirrorInfo{}, nil
	}
	mirror := c.mirrorPath(repoURL)
	lock := c.repoLock(repoURL)
	if !lock.TryLock() {
		return statDir(mirror), ErrBusy
	}
	defer lock.Unlock()
	info := statDir(mirror)
	if !info.Exists {
		return info, nil
	}
	if err := os.RemoveAll(mirror); err != nil {
		return info, err
	}
	return info, nil
}

// statDir 汇总一个目录的体积(只算普通文件,子目录自身字节数不计;读不动的条目跳过)。
func statDir(dir string) MirrorInfo {
	info := MirrorInfo{Path: dir}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return info
	}
	info.Exists = true
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, werr error) error {
		if werr != nil || e.IsDir() {
			return nil //nolint:nilerr // 体积统计是尽力而为:打不开的条目跳过,不影响主流程
		}
		if fi, ierr := e.Info(); ierr == nil {
			info.Bytes += fi.Size()
		}
		return nil
	})
	return info
}
