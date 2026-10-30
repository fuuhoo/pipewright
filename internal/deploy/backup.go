package deploy

// backup.go 实现 deploy_ssh 文件类发布的**部署前备份 + 按份数保留 + 覆盖式恢复**。
//
// 备份落在部署目录内的 .pw-backups/ 下(同一块盘、同一个写入身份 —— 不额外假设目标机上
// 有 /opt 之类需要 root 的目录),每份一个 tar.gz:
//
//	<部署路径>/.pw-backups/20261030T101500Z-123456789.tar.gz
//
// 两种范围(节点上二选一,cfg[backupMode]):
//   - dir   :整份部署目录(排除 .pw-backups 自身,否则备份里会套备份、越滚越大);
//   - suffix:只备匹配后缀的文件(backupSuffixes,如 .jar,.war;递归匹配)。
//
// 备份发生在产物落位**之前**(stageFileOne 的 mkdir 与部署前指令之后):备份失败即该机 failed,
// 部署目录里旧文件一个字节都没动。新备份成功后按修改时间裁掉更老的,只留 backupKeep 份
// (默认 5,上限 50)。
//
// 恢复(运行详情按目标触发)是**覆盖式**:把所选归档解包回**该次部署记录的**部署目录,目录里
// 较新的多余文件保留不删;解包后按该次部署记录的 restartCommand 重启。恢复不改 run 的部署
// 结果 —— 历史是历史,恢复是一次新的动作,只回报本次结果。
//
// 归档名由平台生成(时间戳 + 纳秒),恢复时只接受这个形状:任何带路径分隔 / 点段的输入都当
// 「备份不存在」,不做路径拼接的算术。全程命令 array 化(AC-SEC-02 不拼 shell)。

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// deploy_ssh 文件发布的备份配置键(与流水线节点表单、build 层透传同名)。
const (
	// CfgKeyBackupMode 是备份范围:dir(整目录)| suffix(按后缀)| 空(不备份)。
	CfgKeyBackupMode = "backupMode"
	// CfgKeyBackupSuffixes 是 suffix 模式的后缀清单(逗号 / 空白分隔,如 ".jar,.war")。
	CfgKeyBackupSuffixes = "backupSuffixes"
	// CfgKeyBackupKeep 是保留份数(空 = 默认 5,夹紧 1..50)。
	CfgKeyBackupKeep = "backupKeep"

	// BackupModeDir / BackupModeSuffix 是 backupMode 的取值。
	BackupModeDir    = "dir"
	BackupModeSuffix = "suffix"

	// backupDirName 是部署目录内的备份子目录名(恢复与裁剪都只认它)。
	backupDirName = ".pw-backups"

	// defaultBackupKeep / maxBackupKeep 是保留份数的默认值与上限。
	defaultBackupKeep = 5
	maxBackupKeep     = 50

	// restoreTimeout 是恢复解包的上限:几十 MB 的归档在慢盘上超过 60s 是常态。
	restoreTimeout = 5 * time.Minute
	// backupTimeout 是部署前备份的上限:整目录打包远比一条普通命令重,不能共用 60s 的部署预算。
	backupTimeout = 10 * time.Minute
)

// backupArchivePattern 是平台生成的归档名形状(时间戳 + 纳秒);恢复只接受它。
var backupArchivePattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9]+\.tar\.gz$`)

// BackupModeOf 归一备份范围(空 / 认不出的串 → 不备份)。
func BackupModeOf(cfg map[string]string) string {
	switch strings.ToLower(strings.TrimSpace(cfg[CfgKeyBackupMode])) {
	case BackupModeDir:
		return BackupModeDir
	case BackupModeSuffix:
		return BackupModeSuffix
	default:
		return ""
	}
}

// backupKeep 解析保留份数(空 / 非法 → 默认 5;夹紧 1..50)。
func backupKeep(cfg map[string]string) int {
	raw := strings.TrimSpace(cfg[CfgKeyBackupKeep])
	if raw == "" {
		return defaultBackupKeep
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultBackupKeep
	}
	if n > maxBackupKeep {
		return maxBackupKeep
	}
	return n
}

// backupSuffixes 解析后缀清单:逗号 / 空白 / 分号分隔,自动补前导点,只保留安全形状
// (`.` + 字母数字 . _ -,最长 32)。返回值会被嵌进 find 的脚本片段,所以这里必须严格。
func backupSuffixes(cfg map[string]string) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(cfg[CfgKeyBackupSuffixes], func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if !strings.HasPrefix(tok, ".") {
			tok = "." + tok
		}
		if !safeBackupSuffix(tok) || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// safeBackupSuffix 报告后缀是否只含安全字符(防把它拼进脚本时产生注入面)。
func safeBackupSuffix(s string) bool {
	if len(s) < 2 || len(s) > 32 || s[0] != '.' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// backupBeforeDeploy 在产物落位前给部署目录留一份备份。
// 返回 (人读失败原因, false) 时调用方应把该机记 failed —— 备份是用户明确要的安全网,
// 悄悄跳过等于让他在需要恢复时才发现没有备份。
func (s *service) backupBeforeDeploy(ctx context.Context, srv *target.Server, dir string, cfg map[string]string) (string, bool) {
	mode := BackupModeOf(cfg)
	if mode == "" {
		return "", true
	}
	// 打包自己的预算:调用方传进来的部署 ctx 是给命令用的 60s 量级,整目录 tar 不该被它掐死。
	bkCtx, cancel := context.WithTimeout(ctx, backupTimeout)
	defer cancel()
	bkDir := path.Join(dir, backupDirName)
	if failMsg, ok := s.runStagedCmd(bkCtx, srv, [][]string{{"mkdir", "-p", bkDir}}); !ok {
		return "备份失败(建备份目录):" + failMsg, false
	}
	lg := cmdLogFrom(ctx)
	now := time.Now().UTC()
	archive := path.Join(bkDir, fmt.Sprintf("%s-%d.tar.gz", now.Format("20060102T150405Z"), now.Nanosecond()))

	if mode == BackupModeSuffix {
		suffixes := backupSuffixes(cfg)
		if len(suffixes) == 0 {
			lg(cmdStreamStdout, "· 备份方式为按后缀,但没有可用的后缀,本次跳过备份")
			return "", true
		}
		// 后缀已过 safeBackupSuffix 白名单(只有字母数字与 . _ -),嵌进单引号里不可能逃逸。
		clauses := make([]string, 0, len(suffixes))
		for _, sfx := range suffixes {
			clauses = append(clauses, "-name '*"+sfx+"'")
		}
		listPath := archive + ".list"
		script := `cd "$1" && find . -path './` + backupDirName + `' -prune -o -type f \( ` +
			strings.Join(clauses, " -o ") + ` \) -print > "$2"`
		if _, err := s.exec(bkCtx, srv.ID, []string{"sh", "-c", script, "sh", dir, listPath}); err != nil {
			return "备份失败(查找匹配文件):" + humanExecError(err), false
		}
		out, err := s.exec(bkCtx, srv.ID, []string{"sh", "-c", `test -s "$0"`, listPath})
		if err != nil {
			s.dropStaging(ctx, srv, listPath)
			return "备份失败(检查匹配文件):" + humanExecError(err), false
		}
		if out == nil || out.ExitCode != 0 {
			s.dropStaging(ctx, srv, listPath)
			lg(cmdStreamStdout, fmt.Sprintf("· 部署目录里没有匹配 %s 的文件,本次跳过备份", strings.Join(suffixes, " ")))
			return "", true
		}
		if failMsg, ok := s.runStagedCmd(bkCtx, srv, [][]string{
			{"tar", "-czf", archive, "-C", dir, "-T", listPath},
			{"rm", "-f", listPath},
		}); !ok {
			s.dropStaging(ctx, srv, listPath)
			return "备份失败(打包匹配文件):" + failMsg, false
		}
	} else {
		has, err := s.dirHasContent(bkCtx, srv, dir)
		if err != nil {
			return "备份失败(检查部署目录):" + humanExecError(err), false
		}
		if !has {
			lg(cmdStreamStdout, "· 部署目录还是空的,本次跳过备份")
			return "", true
		}
		if failMsg, ok := s.runStagedCmd(bkCtx, srv, [][]string{{
			"tar", "-czf", archive, "-C", dir,
			"--exclude=./" + backupDirName, "--exclude=" + backupDirName, ".",
		}}); !ok {
			return "备份失败(打包部署目录):" + failMsg, false
		}
	}

	keep := backupKeep(cfg)
	s.pruneBackups(bkCtx, srv, bkDir, keep)
	lg(cmdStreamStdout, fmt.Sprintf("· 已备份到 %s(保留最近 %d 份)", archive, keep))
	return "", true
}

// dirHasContent 报告部署目录里除备份目录外是否还有东西(空目录不值得占一份保留额度)。
func (s *service) dirHasContent(ctx context.Context, srv *target.Server, dir string) (bool, error) {
	script := `[ -n "$(find "$1" -mindepth 1 -maxdepth 1 ! -name ` + backupDirName + ` -print -quit 2>/dev/null)" ]`
	out, err := s.exec(ctx, srv.ID, []string{"sh", "-c", script, "sh", dir})
	if err != nil {
		return false, err
	}
	return out != nil && out.ExitCode == 0, nil
}

// pruneBackups 按修改时间从新到旧保留 keep 份,其余删除(best-effort:裁不掉不影响本次部署)。
func (s *service) pruneBackups(ctx context.Context, srv *target.Server, bkDir string, keep int) {
	script := `cd "$1" && ls -1t *.tar.gz 2>/dev/null | tail -n +"$2" | while IFS= read -r f; do rm -f -- "$f"; done`
	_, _ = s.exec(ctx, srv.ID, []string{"sh", "-c", script, "sh", bkDir, strconv.Itoa(keep + 1)})
}

// BackupArchive 是一份可恢复的备份(平台生成的归档,名即 ID)。
type BackupArchive struct {
	ID        string
	CreatedAt time.Time
	SizeBytes int64
}

// BackupList 是一次「按目标的备份列表」查询结果。
type BackupList struct {
	DeployPath string
	Mode       string
	Archives   []BackupArchive
}

// RestoreInput 是一次「用备份覆盖还原」请求(恢复入口在运行详情的部署目标上)。
type RestoreInput struct {
	RunID    string
	ServerID string
	BackupID string
}

// RestoreResult 是一次恢复的执行结果。OK=false 时 Message 是人读原因(执行失败不上抛)。
type RestoreResult struct {
	BackupID   string
	DeployPath string
	Restarted  bool
	OK         bool
	Message    string
}

// ListBackups 列出某 run 某台目标机上的备份(部署目录取自该次部署记录,不信任客户端)。
// 该目标没有开启备份 → 空列表(Mode 为空),这不是错误。
func (s *service) ListBackups(ctx context.Context, runID, serverID string) (BackupList, error) {
	tg, err := s.loadBackupTarget(ctx, runID, serverID)
	if err != nil {
		return BackupList{}, err
	}
	out := BackupList{DeployPath: tg.DeployPath, Mode: tg.BackupMode, Archives: []BackupArchive{}}
	if tg.DeployPath == "" || tg.BackupMode == "" {
		return out, nil
	}
	ctx, release, gerr := s.holdServer(ctx, serverID)
	if gerr != nil {
		return BackupList{}, gerr
	}
	defer release()

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	script := `cd "$1" 2>/dev/null || exit 0
for f in *.tar.gz; do
  [ -f "$f" ] || continue
  printf '%s\t%s\n' "$f" "$(wc -c < "$f" | tr -d ' ')"
done`
	res, err := s.exec(execCtx, serverID, []string{"sh", "-c", script, "sh", path.Join(tg.DeployPath, backupDirName)})
	if err != nil {
		return BackupList{}, err
	}
	if res != nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			name, sizeStr, ok := strings.Cut(strings.TrimSpace(line), "\t")
			if !ok || !backupArchivePattern.MatchString(name) {
				continue
			}
			size, _ := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64)
			created, _ := time.Parse("20060102T150405Z", name[:16])
			out.Archives = append(out.Archives, BackupArchive{ID: name, CreatedAt: created, SizeBytes: size})
		}
	}
	// 新 → 旧(归档名以 UTC 时间戳开头,字典序即时序)。
	sort.Slice(out.Archives, func(i, j int) bool { return out.Archives[i].ID > out.Archives[j].ID })
	return out, nil
}

// RestoreBackup 用所选备份覆盖式还原该目标的部署目录,随后按该次部署记录的重启命令重启。
// 定位类错误上抛;解包 / 重启执行失败内化为 OK=false + 人读原因(HTTP 整体 200)。
func (s *service) RestoreBackup(ctx context.Context, in RestoreInput) (RestoreResult, error) {
	tg, err := s.loadBackupTarget(ctx, in.RunID, in.ServerID)
	if err != nil {
		return RestoreResult{}, err
	}
	if tg.DeployPath == "" || tg.BackupMode == "" {
		return RestoreResult{}, ErrBackupsDisabled
	}
	if !backupArchivePattern.MatchString(in.BackupID) {
		return RestoreResult{}, ErrBackupNotFound
	}
	if _, gerr := s.targets.Get(ctx, in.ServerID); gerr != nil {
		if errors.Is(gerr, target.ErrNotFound) {
			return RestoreResult{}, ErrServerNotFound
		}
		return RestoreResult{}, gerr
	}
	ctx, release, herr := s.holdServer(ctx, in.ServerID)
	if herr != nil {
		return RestoreResult{}, herr
	}
	defer release()

	res := RestoreResult{BackupID: in.BackupID, DeployPath: tg.DeployPath}
	archive := path.Join(tg.DeployPath, backupDirName, in.BackupID)
	execCtx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()

	probe, perr := s.exec(execCtx, in.ServerID, []string{"test", "-f", archive})
	if perr != nil {
		return RestoreResult{}, perr
	}
	if probe == nil || probe.ExitCode != 0 {
		return RestoreResult{}, ErrBackupNotFound
	}

	if failMsg, ok := s.runStep(execCtx, in.ServerID, [][]string{{"tar", "-xzf", archive, "-C", tg.DeployPath}}); !ok {
		res.Message = "解包备份失败:" + failMsg
		return res, nil
	}
	if rc := strings.TrimSpace(tg.RestartCommand); rc != "" {
		// 与部署激活阶段逐字同形:目录作位置参数,用户命令体绝不拼进脚本(AC-SEC-02)。
		script := "cd \"$0\" && set -e\n" + rc
		if failMsg, ok := s.runStep(execCtx, in.ServerID, [][]string{{"sh", "-c", script, tg.DeployPath}}); !ok {
			res.Message = "备份已解包,但重启命令失败:" + failMsg
			return res, nil
		}
		res.Restarted = true
	}

	res.OK = true
	res.Message = "已用备份 " + in.BackupID + " 覆盖部署目录 " + tg.DeployPath
	if res.Restarted {
		res.Message += ",并已执行重启命令"
	} else {
		res.Message += "(该次部署没有配置重启命令)"
	}
	return res, nil
}

// loadBackupTarget 校验 run 存在并取该目标的那一行(部署目录 / 重启命令 / 备份方式都在行上)。
func (s *service) loadBackupTarget(ctx context.Context, runID, serverID string) (run.DeployTarget, error) {
	if _, err := s.runs.Get(ctx, runID); err != nil {
		if errors.Is(err, run.ErrNotFound) {
			return run.DeployTarget{}, ErrRunNotFound
		}
		return run.DeployTarget{}, err
	}
	targets, err := s.runs.ListDeployTargets(ctx, runID)
	if err != nil {
		return run.DeployTarget{}, err
	}
	for i := range targets {
		if targets[i].ServerID == serverID {
			return targets[i], nil
		}
	}
	return run.DeployTarget{}, ErrTargetNotFound
}
