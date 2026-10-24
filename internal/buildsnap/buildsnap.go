// Package buildsnap 保存构建节点跑完那一刻的工作区目录清单,供运行详情「构建目录」面板回看。
//
// 背景:构建工作区是即用即销的临时目录(cloner.go mkTempWorkspace → defer os.RemoveAll),
// 跑完连目录带内容一起消失。产物路径写错时,收集器只往日志留一行「产物路径未找到,跳过」,
// 既不登记产物行、也不记「当时目录里到底有什么」—— 于是"我到底生成在哪了"这个问题在运行记录里
// 根本没有答案,只能改一版重跑再猜。本包把这份清单留下来:每个构建节点各采一份(成功也采),
// 什么时候回看都在。生命周期完全跟着运行记录 —— 表带 ON DELETE CASCADE,保留清理器删 run 时
// 快照连带消失,本包不再有独立的过期窗口与清理器。
//
// 只存**清单文本**(相对路径 + 大小),绝不存文件内容:一个含 node_modules 的前端工作区实测
// 3.4 万条目(约 1.7MB 纯文本),所以采集受三重上限约束 —— 深度、总条数、单目录展开条数。
// 单目录超限不整支丢弃,而是在该目录条目上记 Dropped=N,让"这里还有 N 项没列出来"可见。
// 撞满上限的一份约 156KB、遍历 27~46ms(实测),体积上界因此是"每个节点几十到一百多 KB"。
package buildsnap

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 声明路径的判定结果(与产物收集器的四种"没收到"一一对应)。
const (
	StatusOK        = "ok"        // 命中并已归档
	StatusMissing   = "missing"   // 路径不存在
	StatusUnmatched = "unmatched" // 通配未匹配到任何成员
	StatusRejected  = "rejected"  // 越界(绝对路径 / ..)被拒
)

// 采集原因:一份快照只记一个,按"出问题优先"取 —— 失败 > 声明产物未命中 > 成功。
// 成功也采一份,是为了让"这次构建当时目录里有什么"在任何一次运行里都回得到。
const (
	ReasonFailed       = "failed"
	ReasonArtifactMiss = "artifact_miss"
	ReasonSuccess      = "success"
)

// 采集上限默认值。
const (
	defaultMaxDepth   = 6
	defaultMaxEntries = 3000
	defaultMaxPerDir  = 200
)

// skipDir 永不展开:纯版本控制噪声,对"产物生成在哪"零信号,且条目数通常是全场最大。
const skipDir = ".git"

// Limits 是采集上限;零值字段按默认处理。
type Limits struct {
	MaxDepth   int
	MaxEntries int
	MaxPerDir  int
}

func (l Limits) withDefaults() Limits {
	if l.MaxDepth <= 0 {
		l.MaxDepth = defaultMaxDepth
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = defaultMaxEntries
	}
	if l.MaxPerDir <= 0 {
		l.MaxPerDir = defaultMaxPerDir
	}
	return l
}

// Entry 是一条目录清单项:Path 为工作区相对路径(斜杠分隔,目录以 / 结尾)。
// Dropped>0 只出现在目录项上,表示该目录下另有 Dropped 个子项因单目录上限未列出。
type Entry struct {
	Path    string `json:"p"`
	Size    int64  `json:"s,omitempty"`
	Dropped int    `json:"d,omitempty"`
}

// Declared 是一条用户声明的产物路径及其判定。未命中时 Parent/Siblings 给出
// 「最近存在的祖先目录」与该目录下的实际条目 —— 排查"目录写错"的直接证据。
type Declared struct {
	Path         string   `json:"path"`
	Status       string   `json:"status"`
	Parent       string   `json:"parent,omitempty"`
	Siblings     []string `json:"siblings,omitempty"`
	SiblingsMore int      `json:"siblingsMore,omitempty"` // 兄弟项超过单目录上限时,另有几项未列
}

// Snapshot 一次采集:哪个 run / 阶段 / 节点、为什么采、声明路径判定、工作区清单。
type Snapshot struct {
	ID        string
	RunID     string
	Stage     string
	JobID     string
	JobName   string
	Reason    string
	Declared  []Declared
	Entries   []Entry
	Truncated bool
	CreatedAt time.Time
}

// HasMiss 报告声明清单里是否存在没收到东西的路径(ok 之外都算)。
func HasMiss(declared []Declared) bool {
	for _, d := range declared {
		if d.Status != StatusOK {
			return true
		}
	}
	return false
}

// Collect 采集 workspace 的目录快照(纯文件系统操作,不碰数据库;上限之外的静默截断)。
// declared 是产物收集器给出的逐条判定,可为 nil(纯失败场景)。
func Collect(workspace string, declared []Declared, limits Limits) Snapshot {
	limits = limits.withDefaults()
	snap := Snapshot{Declared: declared, CreatedAt: time.Now().UTC()}
	snap.Entries, snap.Truncated = walk(workspace, limits)
	for i := range snap.Declared {
		if snap.Declared[i].Status == StatusOK || snap.Declared[i].Status == StatusRejected {
			continue
		}
		snap.Declared[i].Parent, snap.Declared[i].Siblings, snap.Declared[i].SiblingsMore =
			nearestExistingDir(workspace, snap.Declared[i].Path, limits.MaxPerDir)
	}
	return snap
}

// walkDir 是待展开队列里的一项:目录绝对路径、其展示相对路径、以及它在结果里的条目下标
// (根目录没有条目,记 -1)—— 单目录超限时要按下标回填 Dropped,避免回头扫全表。
type walkDir struct {
	abs      string
	rel      string
	entryIdx int
	depth    int
}

// walk 广度优先列目录:浅层优先才能把有限的总条数花在"看清整体结构"上,
// 而不是被一棵深树(如 node_modules)吃满。返回 (清单, 是否因总条数上限被截断)。
func walk(root string, limits Limits) ([]Entry, bool) {
	var out []Entry
	queue := []walkDir{{abs: root, rel: "", entryIdx: -1, depth: 0}}
	truncated := false
	for head := 0; head < len(queue); head++ {
		cur := queue[head]
		children, err := os.ReadDir(cur.abs)
		if err != nil {
			continue // 读不动就跳过这一支(权限不足 / 已被删),采集绝不出错上抛
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		kept, dropped := 0, 0
		for _, ch := range children {
			name := ch.Name()
			if name == skipDir {
				continue
			}
			if len(out) >= limits.MaxEntries {
				truncated = true
				break
			}
			isDir := ch.IsDir()
			relPath := cur.rel + name
			if isDir {
				relPath += "/"
			}
			if kept >= limits.MaxPerDir {
				dropped++
				continue
			}
			kept++
			if !isDir {
				var size int64
				if fi, ierr := ch.Info(); ierr == nil {
					size = fi.Size()
				}
				out = append(out, Entry{Path: relPath, Size: size})
				continue
			}
			idx := len(out)
			out = append(out, Entry{Path: relPath})
			if cur.depth+1 < limits.MaxDepth {
				queue = append(queue, walkDir{abs: filepath.Join(cur.abs, name), rel: relPath, entryIdx: idx, depth: cur.depth + 1})
			}
		}
		if dropped > 0 {
			if cur.entryIdx >= 0 {
				out[cur.entryIdx].Dropped += dropped
			} else {
				truncated = true // 根目录本身被截:没有条目可挂 Dropped,只能整体标为截断
			}
		}
		if truncated {
			break // 总条数已满:后面的目录再读也填不进一条
		}
	}
	return out, truncated
}

// nearestExistingDir 从声明路径的父目录逐级上溯,返回第一个真实存在的目录(工作区相对路径,
// 根为 ".")及其下的实际条目名(目录带尾斜杠,同样跳过 .git)。用来回答"我写的路径不存在,
// 那它旁边到底有什么"。条目至多取 maxPerDir 个,超出只报数量 —— 祖先目录万一是 node_modules
// 时,列全名毫无意义。
func nearestExistingDir(workspace, declared string, maxPerDir int) (string, []string, int) {
	dir := filepath.Dir(filepath.Clean(strings.TrimSpace(declared)))
	for {
		abs := filepath.Join(workspace, dir)
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			rel := toSlashDir(dir)
			children, derr := os.ReadDir(abs)
			if derr != nil {
				return rel, nil, 0
			}
			names := make([]string, 0, len(children))
			for _, ch := range children {
				n := ch.Name()
				if n == skipDir {
					continue
				}
				if ch.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
			sort.Strings(names)
			more := 0
			if maxPerDir > 0 && len(names) > maxPerDir {
				more = len(names) - maxPerDir
				names = names[:maxPerDir]
			}
			return rel, names, more
		}
		parent := filepath.Dir(dir)
		if parent == dir { // 已到 "/" 仍不存在
			return ".", nil, 0
		}
		dir = parent
	}
}

// toSlashDir 把相对目录转成展示用斜杠路径(根为 ".",不带尾斜杠)。
func toSlashDir(dir string) string {
	rel := filepath.ToSlash(filepath.Clean(dir))
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "."
	}
	return rel
}
