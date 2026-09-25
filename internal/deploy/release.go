package deploy

// release.go 实现文件类产物(dist / jar / archive)的**直铺发布**:
//
//	<部署路径>/
//	  index.html  assets/ …   ← dist:制品库 tar.gz 就地解包
//	  server-ny               ← 单文件产物:就地落盘
//
// 不再套 `releases/<runId>` + `current` 软链:部署路径下就是产物本身,发完就能直接用。
// 代价(明确取舍):切换非原子(有毫秒级窗口),且**没有**「一键回滚上一版」—— 要回到旧版
// 就把那个版本再跑一遍流水线。带进程的服务靠 restartCommand 重启,健康检查失败该机记 failed。
// image 类型不在此列(走容器编排 image_release.go,仍保留停旧起新 + 回滚上一镜像)。
//
// 流程(deployFileOne = stageFileOne 紧接 activateFileOne;蓝绿策略跨机分这两阶段):
//  1. 预备:mkdir -p <部署路径> → 把产物字节铺进去(**不重启、不探测**)。
//  2. 激活:在部署目录跑 restartCommand(配了才跑)→ 健康门控 → success / failed。
//
// 单文件产物先落到部署目录内的临时名再 mv 到位:上传经 SSH 通道直写目标路径时,超时或断流
// 会在部署目录里留下半个可执行文件(下一台机重启就直接跑到坏二进制)。同目录 rename 是原子的,
// 消费方只会看到旧版或新版。dist 是「解包进目录」,天生非原子,只能保持就地合并。
//
// 全程命令 **array 化**([]string)经 target.Exec(AC-SEC-02 不拼 shell)。

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/target"
)

// fileDeployArtifact 判定产物是否走「文件直铺」发布(dist / jar / archive 同形:
// 制品库真字节上传 / 远端解包,或历史占位写入)。image 走容器路径,不在此列。
func fileDeployArtifact(a run.Artifact) bool {
	switch a.Type {
	case run.ArtifactDist, run.ArtifactJar, run.ArtifactArchive:
		return true
	default:
		return false
	}
}

// deployTargetDir 解析产物直铺的目标目录:
//   - Config["deployPath"] 优先(流水线部署节点的「部署路径」);
//   - 兼容历史键 Config["path"] / Config["releaseBase"](手工部署面板与旧数据用过);
//   - 都没有 → defaultDeployRoot/<产物名净化>。
func deployTargetDir(a run.Artifact, cfg map[string]string) string {
	for _, k := range []string{"deployPath", "path", "releaseBase"} {
		if v := strings.TrimSpace(cfg[k]); v != "" {
			return v
		}
	}
	name := sanitizeName(a.Name)
	if name == "" {
		name = "app"
	}
	return path.Join(defaultDeployRoot, name)
}

// deployState 是「产物已铺好、尚未重启 / 探测」的一机中间态:stageFileOne 产出,
// activateFileOne 消费。rolling / canary 走 deployFileOne(两阶段紧挨着跑);
// 蓝绿才跨机分两阶段调度(先全机铺好,再全机重启 + 健康)。
type deployState struct {
	dir string // 产物直铺目录(= 部署路径)
}

// deployFileOne 在一台目标机上直铺文件产物 + 重启 + 健康门控。仅在 fileDeployArtifact(a)
// 为真时被 deployOne 调用。执行错误**不上抛**:映射为 status=failed + 人读 message(绝无明文密钥)。
func (s *service) deployFileOne(ctx context.Context, srv *target.Server, a run.Artifact, cfg map[string]string, hc *HealthCheck) TargetResult {
	started := time.Now().UTC()
	res := TargetResult{ServerID: srv.ID, ServerName: srv.Name, StartedAt: started}
	// 整段部署占住这台机:排队发生在任何计时起点之前,后面的 mkdir / 上传 / 落位才各自拿满额度。
	ctx, release, gerr := s.holdServer(ctx, srv.ID)
	if gerr != nil {
		return finishFailed(res, "等待目标机空闲时部署被中断:"+humanExecError(gerr))
	}
	defer release()

	st, failMsg, ok := s.stageFileOne(ctx, srv, a, cfg)
	if !ok {
		return finishFailed(res, failMsg)
	}
	return s.activateFileOne(ctx, srv, a, cfg, hc, st, started)
}

// stageFileOne 执行**预备阶段**:建目录 + 把产物铺进部署路径,**不重启、不探测**。
// 铺失败 → (中间态, 人读 message, false)。
func (s *service) stageFileOne(ctx context.Context, srv *target.Server, a run.Artifact, cfg map[string]string) (deployState, string, bool) {
	st := deployState{dir: deployTargetDir(a, cfg)}

	// 蓝绿是跨机分阶段调度的(这里铺、另一轮再激活),没有外层 deployFileOne 替它拿闸,
	// 所以两阶段各自占位;从 deployFileOne 进来时 ctx 已带凭证 → 空操作,不会自己等自己。
	ctx, release, gerr := s.holdServer(ctx, srv.ID)
	if gerr != nil {
		return st, "等待目标机空闲时部署被中断:" + humanExecError(gerr), false
	}
	defer release()

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	if failMsg, ok := s.runStep(execCtx, srv.ID, [][]string{{"mkdir", "-p", st.dir}}); !ok {
		return st, failMsg, false
	}

	// 制品库支撑的产物走「上传真字节」(上传自己按体积计时,不共用上面那条命令的 60s);
	// 否则历史占位路径(把 reference 串当内容写入)。
	if isStoredArtifact(a) {
		failMsg, ok := s.stageStoredArtifact(ctx, srv, a, st)
		return st, failMsg, ok
	}

	file := path.Join(st.dir, deployFileName(a))
	payload := base64.StdEncoding.EncodeToString([]byte(a.Reference + "\n"))
	placeCmds := [][]string{
		{"sh", "-c", `printf '%s' "$1" | base64 -d > "$0"`, file, payload},
	}
	if a.Type == run.ArtifactJar {
		// jar:放置后探测启动命令(目标无 java → 非零退出 → 该机 failed 人读)。
		placeCmds = append(placeCmds, []string{"java", "-jar", file, "--version"})
	}
	if failMsg, ok := s.runStep(execCtx, srv.ID, placeCmds); !ok {
		return st, failMsg, false
	}
	return st, "", true
}

// stageStoredArtifact 把制品库里的**真字节**直铺进部署目录:
//   - format=tar.gz(dist):上传临时包 → 就地解包 → 删包(包内已是产物内容,不额外套层)。
//   - 其余(jar / archive / 单文件):上传为部署目录内的临时名 → mv 到最终名。
//
// 上传与命令各自计时:命令 60s 足够,几十 MB 的制品经 SFTP 不该被它掐断(uploadTimeout 按体积给)。
// 制品库未配 / 取字节失败 / 上传失败 → (人读 message, false)。
func (s *service) stageStoredArtifact(ctx context.Context, srv *target.Server, a run.Artifact, st deployState) (string, bool) {
	if s.artStore == nil {
		return "部署侧未配置制品库,无法取产物真字节(产物已归档但制品库不可用)", false
	}
	rc, err := s.artStore.Open(a.Reference)
	if err != nil {
		return "从制品库取产物失败:" + err.Error(), false
	}
	defer func() { _ = rc.Close() }()

	// 上传走 SFTP,不经命令回显,所以这里自己打一行:否则日志从 mkdir 直接跳到重启命令,
	// 几十秒的传输在控制台上完全隐形,看不出「铺没铺、铺了多大」。
	lg := cmdLogFrom(ctx)
	size := ""
	if a.SizeBytes > 0 {
		size = fmt.Sprintf("(%s)", humanBytes(a.SizeBytes))
	}

	tmp := path.Join(st.dir, stagingName(a))
	budget := uploadTimeout(a.SizeBytes)
	// 体积算出来的额度只是**上限**;上级 ctx 更紧就得照它来,否则日志里写着「上限 5m36s」、实际
	// 100s 就被掐,查起来两头对不上账。
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left > 0 && left < budget {
			budget = left
		}
	}
	upCtx, cancelUpload := context.WithTimeout(ctx, budget)
	defer cancelUpload()

	switch artifactFormat(a) {
	case "tar.gz":
		lg(cmdStreamStdout, fmt.Sprintf("· 上传 %s%s → %s(上限 %s,目录将就地解包,不删目录内其它文件)",
			a.Name, size, st.dir, budget))
		if err := s.upload(upCtx, srv.ID, rc, tmp); err != nil {
			s.dropStaging(ctx, srv, tmp)
			return "上传 dist 制品到目标机失败:" + humanExecError(err), false
		}
		if failMsg, ok := s.runStagedCmd(ctx, srv, [][]string{
			{"tar", "-xzf", tmp, "-C", st.dir},
			{"rm", "-f", tmp},
		}); !ok {
			s.dropStaging(ctx, srv, tmp)
			return "目标机解包 dist 失败:" + failMsg, false
		}
		return "", true

	default:
		name := artifactFilename(a)
		if name == "" {
			name = deployFileName(a)
		}
		dest := path.Join(st.dir, name)
		lg(cmdStreamStdout, fmt.Sprintf("· 上传 %s%s → %s(上限 %s,传完原子改名到位)",
			a.Name, size, dest, budget))
		if err := s.upload(upCtx, srv.ID, rc, tmp); err != nil {
			s.dropStaging(ctx, srv, tmp)
			return "上传制品到目标机失败:" + humanExecError(err), false
		}
		if failMsg, ok := s.runStagedCmd(ctx, srv, [][]string{{"mv", "-f", tmp, dest}}); !ok {
			s.dropStaging(ctx, srv, tmp)
			return "制品落位失败:" + failMsg, false
		}
		if a.Type == run.ArtifactJar {
			if failMsg, ok := s.runStagedCmd(ctx, srv, [][]string{{"java", "-jar", dest, "--version"}}); !ok {
				return failMsg, false
			}
		}
		return "", true
	}
}

// runStagedCmd 跑一步命令类步骤,60s 额度从**此刻**起算。上传与命令绝不能共用一个计时起点:
// 68MB 传完已近 90s,若命令与上传同时开始,落位那条 mv 一上手拿到的就是过期 ctx,直接判「部署执行
// 超时」—— 传对了却报失败,比慢更糟。
//
// 上级 ctx 若自带更紧的 deadline,`WithTimeout` 会被它掐住(取两者较早者),报出来的还是同一句
// 「部署执行超时」,查不出是谁的超时。所以剩余额度不足 60s 时把这行说出来:健康运行里它不响,
// 一旦响就是「命令没拿到自己的额度」,方向立刻从目标机转回调用侧给的预算。
func (s *service) runStagedCmd(ctx context.Context, srv *target.Server, cmds [][]string) (string, bool) {
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left < execTimeout {
			cmdLogFrom(ctx)(cmdStreamStdout, fmt.Sprintf("· 落位命令只拿到上级剩余预算 %s(本步本可要用 %s)",
				left.Round(100*time.Millisecond), execTimeout))
		}
	}
	cmdCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	failMsg, ok := s.runStep(cmdCtx, srv.ID, cmds)
	if ok {
		return "", true
	}
	// 本级额度没走完就失败 = 上级 ctx 到期,那句「部署执行超时」指的是流水线给的预算,不是目标机慢。
	if ctx.Err() != nil {
		return failMsg + "(上级预算已用尽,非目标机执行慢)", false
	}
	return failMsg, false
}

// upload 走与 exec 同一把「同机串行」闸(见 gate.go):上传是占用 SSH 最久的一步,让它和命令
// 抢同一个执行权,才不会出现两条流互相把对方饿到 ctx 超时。
func (s *service) upload(ctx context.Context, serverID string, content io.Reader, remotePath string) error {
	unlock, err := s.occupy(ctx, serverID)
	if err != nil {
		return err
	}
	defer unlock()
	lg := cmdLogFrom(ctx)
	started := time.Now()
	if err := s.targets.Upload(ctx, serverID, content, remotePath); err != nil {
		lg(cmdStreamStderr, "  ✗ "+humanExecError(err)+phaseSuffix(err, started))
		return err
	}
	// 上传成功也要留耗时:几十 MB 的产物在路上花掉半分钟是常态,日志里没有它,
	// 下一步的等待就又会被读成「目标机卡住了」。
	lg(cmdStreamStdout, fmt.Sprintf("  · 上传完成,用时 %s", time.Since(started).Round(100*time.Millisecond)))
	return nil
}

// uploadTimeout 按产物体积给上传留时间:保底 60s,此后每 MB 再加 4s(约 250KB/s 的下限带宽
// 预算),上限 15 分钟。体积未知(旧数据没记 sizeBytes)直接给上限 —— 宁可慢判失败,也别把
// 一次正常的大文件传输判成超时。
func uploadTimeout(sizeBytes int64) time.Duration {
	const (
		base    = 60 * time.Second
		perMB   = 4 * time.Second
		ceiling = 15 * time.Minute
	)
	if sizeBytes <= 0 {
		return ceiling
	}
	d := base + time.Duration(sizeBytes/(1<<20)+1)*perMB
	if d > ceiling {
		return ceiling
	}
	return d
}

// stagingName 是部署目录内的临时落位名:带产物指纹与纳秒,使同目录上的并行部署不会互相覆盖,
// 也不会撞上上一次失败留下的同名文件。
func stagingName(a run.Artifact) string {
	id := strings.TrimSpace(a.Reference)
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		id = "anon"
	}
	return fmt.Sprintf(".pw-staging-%s-%d", sanitizeName(id), time.Now().UnixNano())
}

// dropStaging 尽力清掉失败留下的临时名(别在部署目录里攒垃圾)。失败一律忽略:此刻连接很可能
// 已经断了,而真正的失败原因已经回报给用户,不该被清理动作盖掉。
func (s *service) dropStaging(ctx context.Context, srv *target.Server, tmp string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTimeout)
	defer cancel()
	_, _ = s.exec(cleanupCtx, srv.ID, []string{"rm", "-f", tmp})
}

// isStoredArtifact 报告产物是否由制品库归档(metadata.stored=true → 部署取真字节)。
func isStoredArtifact(a run.Artifact) bool {
	v, ok := a.Metadata["stored"].(bool)
	return ok && v
}

// artifactFormat 取产物存储格式(file | tar.gz;缺省 file)。
func artifactFormat(a run.Artifact) string {
	if f, ok := a.Metadata["format"].(string); ok && f != "" {
		return f
	}
	return "file"
}

// artifactFilename 取产物原始文件名(直铺时的落盘名;缺省空)。
func artifactFilename(a run.Artifact) string {
	if n, ok := a.Metadata["filename"].(string); ok {
		return n
	}
	return ""
}

// activateFileOne 执行**激活阶段**:在部署目录跑 restartCommand(配了才跑)→ 健康门控 →
// success / failed。started 透传以保留单机起始时刻。
//
// 直铺没有「上一版本」可切回,所以健康失败就是 failed(人读里说清原因与如何回退)。
func (s *service) activateFileOne(ctx context.Context, srv *target.Server, a run.Artifact, cfg map[string]string, hc *HealthCheck, st deployState, started time.Time) TargetResult {
	res := TargetResult{ServerID: srv.ID, ServerName: srv.Name, StartedAt: started}

	ctx, release, gerr := s.holdServer(ctx, srv.ID)
	if gerr != nil {
		return finishFailed(res, "等待目标机空闲时部署被中断:"+humanExecError(gerr))
	}
	defer release()

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	if rc := strings.TrimSpace(cfg["restartCommand"]); rc != "" {
		script := "cd \"$0\" && set -e\n" + rc
		if failMsg, ok := s.runStep(execCtx, srv.ID, [][]string{{"sh", "-c", script, st.dir}}); !ok {
			return finishFailed(res, "重启/切换命令失败:"+failMsg)
		}
	}

	if hc.enabled() {
		if herr := s.runHealthCheck(execCtx, srv.ID, hc); herr != nil {
			return finishFailed(res, fmt.Sprintf("健康检查失败(直铺模式无上一版本可回滚,该机已停在本次发布):%s", herr.Error()))
		}
	}

	finish := time.Now().UTC()
	res.Status = run.TargetSuccess
	if hc.enabled() {
		res.Message = fmt.Sprintf("%s 部署完成 → %s(健康检查通过)", a.Type, st.dir)
	} else {
		res.Message = fmt.Sprintf("%s 部署完成 → %s", a.Type, st.dir)
	}
	res.FinishedAt = &finish
	return res
}

// runStep 顺序执行一组 array 命令;任一执行错误 / 非零退出 → 返回 (人读 message, false)。
// 全部成功 → ("", true)。供直铺的放置 / 激活阶段复用。
func (s *service) runStep(ctx context.Context, serverID string, cmds [][]string) (string, bool) {
	for _, cmd := range cmds {
		out, eerr := s.exec(ctx, serverID, cmd)
		if eerr != nil {
			return humanExecError(eerr), false
		}
		if out != nil && out.ExitCode != 0 {
			return fmt.Sprintf("部署命令退出码 %d:%s", out.ExitCode, truncate(strings.TrimSpace(out.Stderr))), false
		}
	}
	return "", true
}

// finishFailed 把结果置 failed + 人读 message + 结束时间。
func finishFailed(res TargetResult, msg string) TargetResult {
	finish := time.Now().UTC()
	res.Status = run.TargetFailed
	res.Message = msg
	res.FinishedAt = &finish
	return res
}

// humanBytes 把字节数收成可读串(上传回显用;够表达量级即可)。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// defaultDeployRoot 是未指定部署路径时的兜底根目录(本机真验友好:用临时区,不需 root)。
const defaultDeployRoot = "/tmp/pipewright-deploy"

// deployFileName 取产物落地文件名(reference 的 base 名;无则用净化产物名 + .bin)。
func deployFileName(a run.Artifact) string {
	base := path.Base(strings.TrimRight(a.Reference, "/"))
	base = sanitizeName(base)
	if base == "" || base == "." {
		base = sanitizeName(a.Name) + ".bin"
	}
	return base
}

// sanitizeName 把名字净化为安全路径段(仅字母数字 . _ -;其余替为 _)。
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
