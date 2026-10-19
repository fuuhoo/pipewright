// jobenv.go 把构建节点上导入的 env 文件接进容器环境变量(契约层见 internal/pipeline/envfile.go)。
//
// 三种来源在这一点合成一份变量表:仓库文件从本次运行的工作区读(所以未绑定代码仓库的节点只能粘贴/上传),
// 粘贴与上传都直接读节点里存的正文。
//
// 覆盖口径按用户定的「导入的 env 覆盖同名」:这些变量排在 step.Env 末尾(节点层),
// 并且会把更靠前的同名项摘掉 —— 执行时明文与 secret 是分两段传给 docker 的(secret 恒在后),
// 只追加不摘除就会出现在节点上导入的值盖不住流水线级 secret 的假覆盖。
package build

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuuhoo/pipewright/internal/pipeline"
)

// applyJobEnv 解析节点导入的 env 并合并进 step.Env(就地改)。log 只报键名,永不回显值。
// 返回 error = 配置或文件读不出来,调用方应让该 job 失败(静默少几个变量会让人查半天)。
func applyJobEnv(step *pipeline.PipelineStep, jb pipeline.Job, workspace string, log func(stream, line string)) error {
	ref := pipeline.JobEnvRef(jb.Config)
	node := pipeline.LegacyJobEnvVars(jb.Config) // YAML 导入的 cfg["env"]:执行侧从没读过它,在这里接上

	var (
		imported []pipeline.BuildVar
		skipped  int
		label    string
	)
	switch {
	case ref.Source == pipeline.EnvSourceNone:
	case ref.BodyIsText():
		parsed := pipeline.ParseEnvBody(ref.Text)
		imported, skipped = parsed.Vars, parsed.Skipped
		label = "节点填写的正文"
	case ref.Source == pipeline.EnvSourceRepo:
		text, err := readWorkspaceEnv(workspace, ref.Path)
		if err != nil {
			return err
		}
		parsed := pipeline.ParseEnvBody(text)
		imported, skipped = parsed.Vars, parsed.Skipped
		label = "仓库文件 " + ref.Path
	default:
		return fmt.Errorf("环境变量导入的来源无法识别(%s),只能是 仓库文件 / 粘贴 / 上传 / 不导入", ref.Source)
	}

	// 同一节点内也按「导入的赢」合并:cfg["env"] 里被导入文件重名的那条不要了。
	fromImport := make(map[string]bool, len(imported))
	for _, v := range imported {
		fromImport[v.Key] = true
	}
	merged := make([]pipeline.BuildVar, 0, len(node)+len(imported))
	for _, v := range node {
		if !fromImport[v.Key] {
			merged = append(merged, v)
		}
	}
	merged = append(merged, imported...)

	keys := make([]string, 0, len(imported))
	byKey := make(map[string]bool, len(merged))
	for _, v := range merged {
		byKey[v.Key] = true
	}
	for _, v := range imported {
		keys = append(keys, v.Key)
	}

	// 摘掉更早层的同名项(见文件头「覆盖口径」),节点层留到最后。
	if n := len(step.Env); n > 0 {
		kept := make([]pipeline.BuildVar, 0, n)
		for _, v := range step.Env {
			if byKey[v.Key] {
				continue
			}
			kept = append(kept, v)
		}
		step.Env = append(kept, merged...)
	} else {
		step.Env = merged
	}

	if log != nil {
		if len(imported) > 0 {
			log(streamStdout, fmt.Sprintf("导入环境变量(%s):%d 个 %s", label, len(imported), strings.Join(keys, " ")))
		}
		if legacy := len(merged) - len(imported); legacy > 0 {
			log(streamStdout, fmt.Sprintf("另有 %d 个节点级变量(配置 env 字段)", legacy))
		}
		if skipped > 0 {
			log(streamStdout, fmt.Sprintf("⚠ %s 里有 %d 行不是 KEY=VALUE,已跳过", label, skipped))
		}
	}
	return nil
}

// readWorkspaceEnv 从本次运行的工作区读出仓库里那份 env 文件正文。
//
// 路径在保存期已校验过,这里再判一次:流水线可能来自 AI 生成或旧版本落库,直接拼路径读就是
// 一条任中控机读文件的通路,所以「相对 + 不越界」这两条必须在读之前成立。
func readWorkspaceEnv(workspace, path string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("环境变量导入选了仓库文件「%s」,但这次运行没有工作区:未绑定代码仓库的项目请改用粘贴或上传", path)
	}
	if !pipeline.RepoEnvPathOK(path) {
		return "", fmt.Errorf("环境变量导入的仓库路径非法(%s):要填仓库内相对路径,文件名形如 .env / .env.production / prod.env", path)
	}

	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("工作区目录打不开(%v)", err)
	}
	target := filepath.Join(root, filepath.FromSlash(path))
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("工作区里找不到 %s:这份文件多半没提交进仓库(或被 .gitignore 忽略了),克隆下来自然没有;要临时用就改用粘贴或上传", path)
		}
		return "", fmt.Errorf("读 %s 失败(%v)", path, err)
	}
	// 仓库里可以提交一个指向工作区外的符号链接,解析后必须仍在本次工作区内。
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("环境变量文件 %s 指向工作区外,拒绝读取", path)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("读 %s 失败(%v)", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("环境变量文件 %s 不是普通文件", path)
	}
	if info.Size() > pipeline.EnvMaxBytes {
		return "", fmt.Errorf("仓库里的 %s 超过 %d KiB 上限(%d 字节):env 文件该是几十行配置,不是日志或数据", path, pipeline.EnvMaxBytes>>10, info.Size())
	}

	f, err := os.Open(real)
	if err != nil {
		return "", fmt.Errorf("打开 %s 失败(%v)", path, err)
	}
	defer func() { _ = f.Close() }()
	// 再限一次读长度:Stat 与实际读之间文件可能已被改写,不能信那个 size。
	data, err := io.ReadAll(io.LimitReader(f, pipeline.EnvMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("读 %s 失败(%v)", path, err)
	}
	if len(data) > pipeline.EnvMaxBytes {
		return "", fmt.Errorf("仓库里的 %s 超过 %d KiB 上限", path, pipeline.EnvMaxBytes>>10)
	}
	return string(data), nil
}
