package build

// remote_artifacts.go 把远程 runner 上产出的文件产物**拉回中控机**,让远程模式也能进制品库、
// 被下游部署节点按来源任务挑选(此前远程只回日志,产物留在远程机上随工作区一起被 rm -rf)。
//
// 做法:按 job 声明的 artifactPath 在远程工作区定位 → 远程 tar.gz 到临时文件 → 经 SSH 流式 cat
// 回中控机,解包落到**同一相对路径** → 再交给与本地完全相同的 collectScriptArtifacts 归档。
// 于是类型判定(dist/jar/archive)、metadata(sourceJobId / workspacePath)、跨阶段恢复这些语义
// 远程与本地共用一份实现,不会各写一套再各自漂移。
//
// 为什么打包用 Exec、取回用 ExecStream:Exec 把 stdout 整段缓冲进内存,产物动辄上百 MB;
// ExecStream 只给字节流、拿不到退出码,所以「打包 + 探存在」这类要判成败的仍走同步 Exec,
// 只有中间那条 `cat 归档` 走流式。
//
// 诚实边界:
//   - 通配交给远程 `find -path`,它的 `*` 可以跨 `/`(比中控机 filepath.Glob 宽);写 `dist/*`
//     这类单层通配时两处行为一致,写 `a/*/b` 时远程可能多匹配几层。
//   - 符号链接/设备文件不回传(extractTarGzTo 只落普通文件与目录):靠软链聚合的产物请在节点上
//     改成声明真实目录。
//   - 单条路径回传失败只记一行并跳过,已成功的构建绝不因产物缺失翻车(本地收集器随后还会按自己的
//     口径说明一次);回传侧文案统一带「回传」二字,与本地的「产物路径未找到,跳过」区分开,
//     免得两行长得一模一样。

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/fuuhoo/pipewright/internal/pipeline"
)

// 远程侧的三条装配命令。路径一律作 positional 参数传入(target 层逐参数 shell 转义),
// 命令文本里不插值任何用户输入 —— 与既有 `mkdir -p "$0" && tar -xzf "$1"` 同一写法。
const (
	// remoteLocateScript 探一条路径是否存在:11=工作目录都进不去,12=路径不存在。
	remoteLocateScript = `cd "$0" || exit 11; [ -e "$1" ] || exit 12`
	// remoteGlobScript 把通配展开成实际成员,NUL 分隔(print0 连 GNU/busybox find 都有)。
	remoteGlobScript = `cd "$0" || exit 11; find . -path "./$1" -print0`
	// remoteTarScript 把给定成员打成归档($1=归档路径,其余=成员),失败退 12。
	remoteTarScript = `t=$1; shift; cd "$0" || exit 11; rm -f "$t"; tar -czf "$t" -- "$@" || exit 12`
)

// artifactPathsOf 读出 job 声明的产物路径(多行、渲染 {{参数}} 后逐行;本地与远程收集共用这一口径)。
func artifactPathsOf(jb pipeline.Job) []string {
	return splitCommands(renderTemplate(cfgString(jb.Config, "artifactPath"), templateContext(jb.Config)))
}

// pullRemoteArtifacts 把各 script job 声明的产物从远程工作区拉回本地工作区原位(见文件头)。
func (b *Builder) pullRemoteArtifacts(ctx context.Context, tgt remoteExec, serverID, remoteWS, workspace string, jobs []pipeline.Job, onLine func(stream, line string)) {
	for _, jb := range jobs {
		for _, rel := range artifactPathsOf(jb) {
			b.pullRemoteArtifact(ctx, tgt, serverID, remoteWS, workspace, rel, onLine)
		}
	}
}

// pullRemoteArtifact 回传一条产物路径:越界拒绝 → 远程定位为实际成员 → tar → 流回 → 解包到同一相对路径。
func (b *Builder) pullRemoteArtifact(ctx context.Context, tgt remoteExec, serverID, remoteWS, workspace, rel string, onLine func(stream, line string)) {
	clean := filepath.Clean(strings.TrimSpace(rel))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		onLine(streamStderr, "产物路径越界,已拒绝回传:"+rel)
		return
	}
	members, ok := remoteArtifactMembers(ctx, tgt, serverID, remoteWS, clean, onLine)
	if !ok {
		return
	}

	remoteTar := remoteWS + ".artifacts.tar.gz"
	defer func() { _, _ = tgt.Exec(context.WithoutCancel(ctx), serverID, []string{"rm", "-f", remoteTar}) }()

	args := append([]string{"sh", "-c", remoteTarScript, remoteWS, remoteTar}, members...)
	res, err := tgt.Exec(ctx, serverID, args)
	if err != nil || res == nil || res.ExitCode != 0 {
		onLine(streamStderr, "远程打包产物失败,跳过回传:"+clean)
		return
	}
	rc, serr := tgt.ExecStream(ctx, serverID, []string{"cat", remoteTar})
	if serr != nil {
		onLine(streamStderr, "远程取回产物失败,跳过回传:"+clean+":"+serr.Error())
		return
	}
	xerr := extractTarGzTo(rc, workspace)
	_ = rc.Close()
	if xerr != nil {
		onLine(streamStderr, "解包远程产物失败,跳过回传:"+clean+":"+xerr.Error())
		return
	}
	onLine(streamStdout, "← 已回传远程产物:"+clean+"("+strings.Join(members, " ")+")")
}

// remoteArtifactMembers 把声明路径换成远程上的实际成员:通配走远程 find 展开,普通路径先探一次
// 存在性 —— 与本地 os.Stat 同口径,找不到就记一行「未找到,跳过回传」,而不是让 tar 甩一句看不懂的错误。
// ok=false 表示这条已按语义跳过(日志已记)。
func remoteArtifactMembers(ctx context.Context, tgt remoteExec, serverID, remoteWS, clean string, onLine func(stream, line string)) ([]string, bool) {
	if !strings.ContainsAny(clean, "*?[") {
		res, err := tgt.Exec(ctx, serverID, []string{"sh", "-c", remoteLocateScript, remoteWS, clean})
		if err != nil || res == nil {
			onLine(streamStderr, "远程产物检查失败,跳过回传:"+clean)
			return nil, false
		}
		switch res.ExitCode {
		case 0:
			return []string{clean}, true
		case 12:
			onLine(streamStderr, "远程产物路径未找到,跳过回传:"+clean)
		default:
			onLine(streamStderr, "远程工作区不可读,跳过回传:"+clean)
		}
		return nil, false
	}

	res, err := tgt.Exec(ctx, serverID, []string{"sh", "-c", remoteGlobScript, remoteWS, clean})
	if err != nil || res == nil || res.ExitCode != 0 {
		onLine(streamStderr, "远程产物通配检查失败,跳过回传:"+clean)
		return nil, false
	}
	var out []string
	for _, m := range strings.Split(res.Stdout, "\x00") {
		m = strings.TrimPrefix(m, "./")
		if m == "" || m == "." {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		onLine(streamStderr, "远程产物通配未匹配,跳过回传:"+clean)
		return nil, false
	}
	return out, true
}
