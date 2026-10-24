// gc.go 回收「库里没人认」的产物字节(孤儿)。纯文件系统操作:哪些句柄还活着由调用方
// 从库里读进来(run_artifacts.reference 是句柄唯一的落点),本包不碰数据库。
//
// 为什么会有孤儿:产物行随 run 一起被外键级联删掉(保留清理器裁运行、删项目连带删运行),
// 但字节在 <root>/<前缀>/<hash> 下谁也没动 —— 内容寻址库只会去重,不会失效。
// 于是每裁一轮运行,盘上就多一批没人认的文件。
//
// 为什么还要宽限期:构建是「先把字节 Put 进库、再往 run_artifacts 写行」,中间这段窗口里
// 文件确实没有任何引用;另有去重命中的 Put 会主动把 mtime 推到此刻(见 Put)——两件事合起来
// 保证「正在用的产物」永远落在 grace 之内,不会被这一轮扫掉。
//
// 删不动的逐个跳过(权限 / 正被下载打开),留给下一轮 —— 回收是 best-effort,绝不上抛。
package artifactstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tempPrefix 是 Put 的临时文件名前缀(崩溃遗留的半截文件,按同一宽限期清)。
const tempPrefix = ".put-"

// SweepResult 一轮回收的计数(供日志如实说删了多少、留下多少)。
type SweepResult struct {
	Deleted int   // 删掉的孤儿产物文件数
	Freed   int64 // 释放的字节数
	Kept    int   // 仍被引用而保留
	Young   int   // 无引用但落盘未超宽限期(本轮不动)
	Temps   int   // 清掉的 Put 残留临时文件数
}

// Sweep 删除「句柄不在 referenced 集合里、且落盘早于 now-grace」的产物文件,
// 并顺手清掉 root 下的 .put-* 残留。referenced 传空集合意味着「库里没有任何产物行」,
// 此时超宽限期的字节全算孤儿 —— 这是删光项目后的正常状态,不是异常。
func (s *Store) Sweep(referenced map[string]struct{}, now time.Time, grace time.Duration) (SweepResult, error) {
	cutoff := now.Add(-grace)
	var res SweepResult

	top, err := os.ReadDir(s.root)
	if err != nil {
		return res, fmt.Errorf("artifactstore: read root: %w", err)
	}
	for _, e := range top {
		name := e.Name()
		if !e.IsDir() {
			if strings.HasPrefix(name, tempPrefix) {
				res.Temps += s.sweepTemp(filepath.Join(s.root, name), cutoff)
			}
			continue
		}
		// 只认 2 位分片目录(路径规则见 pathFor);别的目录不是本库写的,不碰。
		if len(name) != 2 {
			continue
		}
		s.sweepShard(filepath.Join(s.root, name), referenced, cutoff, &res)
	}
	return res, nil
}

// sweepShard 扫一个分片目录:文件名即句柄,不在活引用集合里且已过宽限期就删。
func (s *Store) sweepShard(dir string, referenced map[string]struct{}, cutoff time.Time, res *SweepResult) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return // 读不动这一支(权限 / 刚被并发删空),下一轮再看
	}
	for _, f := range files {
		key := f.Name()
		if !validKey(key) {
			continue // 文件名不是 sha256 → 不是本库的产物,不在回收范围
		}
		if _, ok := referenced[key]; ok {
			res.Kept++
			continue
		}
		fi, err := f.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.ModTime().After(cutoff) {
			res.Young++
			continue
		}
		if err := s.Delete(key); err != nil {
			continue // 删不动(权限 / 正被占用),留到下一轮
		}
		res.Deleted++
		res.Freed += fi.Size()
	}
}

// sweepTemp 删一个超过宽限期的 Put 残留临时文件(删掉了才计数 1)。
func (s *Store) sweepTemp(path string, cutoff time.Time) int {
	fi, err := os.Stat(path)
	if err != nil || !fi.ModTime().Before(cutoff) {
		return 0
	}
	if err := os.Remove(path); err != nil {
		return 0
	}
	return 1
}
