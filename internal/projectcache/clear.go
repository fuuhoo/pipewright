package projectcache

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// clear.go —— 项目依赖缓存的「看得见体积 + 一键清空」。
//
// 为什么需要:这套缓存是**宿主上的固定目录**,永久 bind 进构建容器,所以它比一般的构建缓存更
// 「sticky」—— 尤其 Maven 的 `~/.m2/repository` 里存着 SNAPSHOT:工具的默认策略是「本地已有同
// 版本构件就不联网重拉」(daily/interval 检查只管 metadata),于是私服上重新 deploy 过的同一个
// SNAPSHOT 在项目缓存里可能一直是旧的那一份。用户改完依赖重新发布后构建仍拿到旧包,现象和「没
// 拉到最新代码」一模一样,而平台上没有任何出口能清掉它。

// CacheInfo 是某项目依赖缓存目录的磁盘实况。Path 即便目录不存在也会给出(说清要看哪儿)。
type CacheInfo struct {
	Path   string
	Bytes  int64
	Exists bool
}

// InfoOf 返回某项目缓存目录的体积(只 stat,不建目录、不参与挂载冲突判定)。
func (l *Layout) InfoOf(projectID string) CacheInfo {
	if l == nil || strings.TrimSpace(projectID) == "" {
		return CacheInfo{}
	}
	return statDir(l.ProjectDir(projectID))
}

// Clear 清空某项目的依赖缓存:整个项目目录删掉,返回**删除前**的实况。
//
// 删掉而不是逐个清子目录,是因为新增一档缓存只需要往 slots 里加一行 —— 清空逻辑不该跟着改。
// 目录不存在算成功(目标状态已达成)。下一次构建时 Plan 会重新 ensure 出 0777 的空目录,
// 所以这里不必(也不该)替构建预先建目录:那会把「什么都没缓存」和「刚建好空目录」混为一谈。
func (l *Layout) Clear(projectID string) (CacheInfo, error) {
	if l == nil || strings.TrimSpace(projectID) == "" {
		return CacheInfo{}, nil
	}
	info := statDir(l.ProjectDir(projectID))
	if !info.Exists {
		return info, nil
	}
	if err := os.RemoveAll(info.Path); err != nil {
		return info, err
	}
	return info, nil
}

// statDir 汇总目录体积(只算普通文件;打不开的条目跳过,统计尽力而为)。
func statDir(dir string) CacheInfo {
	info := CacheInfo{Path: dir}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return info
	}
	info.Exists = true
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, werr error) error {
		if werr != nil || e.IsDir() {
			return nil //nolint:nilerr // 体积统计尽力而为,跳过读不动的条目
		}
		if fi, ierr := e.Info(); ierr == nil {
			info.Bytes += fi.Size()
		}
		return nil
	})
	return info
}
