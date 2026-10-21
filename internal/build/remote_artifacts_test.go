package build

// remote_artifacts_test.go 验远程 runner 的产物回传:在 fake 上模拟一个远程文件系统,
// 让它真的按命令语义回 tar.gz,再从中控机侧断言「文件落回同一相对路径 + 产物按类型登记」。
// 于是这里证明的是**端到端的数据流**,不是「函数被调用过」。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// simRemote 在 fakeRemoteTarget 之上模拟远程侧:认出三条产物装配命令(探存在 / 通配展开 / tar),
// 按 remoteFS 造出真实的 tar.gz 归档,交给 ExecStream(cat <归档>)取回。
type simRemote struct {
	*fakeRemoteTarget
	remoteFS map[string]string // 远程工作区相对路径 → 内容
	archives map[string][]byte // 远程归档绝对路径 → tar.gz 字节
	streams  int               // ExecStream 被取回几次(证明走流式而非整段缓冲)
}

func (s *simRemote) Exec(ctx context.Context, serverID string, cmd []string) (*target.ExecResult, error) {
	if len(cmd) >= 3 && cmd[0] == "sh" && cmd[1] == "-c" {
		switch cmd[2] {
		case remoteLocateScript, remoteGlobScript, remoteTarScript:
			s.cmds = append(s.cmds, cmd)
			return s.simulate(cmd)
		}
	}
	return s.fakeRemoteTarget.Exec(ctx, serverID, cmd)
}

func (s *simRemote) ExecStream(_ context.Context, serverID string, cmd []string) (io.ReadCloser, error) {
	s.serverIDs[serverID] = true
	if len(cmd) != 2 || cmd[0] != "cat" {
		return nil, errors.New("simRemote 只支持 cat <归档>: " + strings.Join(cmd, " "))
	}
	b, ok := s.archives[cmd[1]]
	if !ok {
		return nil, errors.New("远程没有这个归档: " + cmd[1])
	}
	s.streams++
	return io.NopCloser(bytes.NewReader(b)), nil
}

// simulate 按各条脚本的约定回退出码:12=路径不存在/成员缺失,0=命中。
// remoteFS 只存文件,目录按「有文件落在其下」推定存在(与真机 [ -e ] / tar 递归一致)。
func (s *simRemote) simulate(cmd []string) (*target.ExecResult, error) {
	switch cmd[2] {
	case remoteLocateScript:
		if !s.exists(cmd[4]) {
			return &target.ExecResult{ExitCode: 12}, nil
		}
		return &target.ExecResult{}, nil

	case remoteGlobScript:
		var hits []string
		for rel := range s.remoteFS {
			if ok, _ := path.Match(cmd[4], rel); ok {
				hits = append(hits, "./"+rel)
			}
		}
		return &target.ExecResult{Stdout: strings.Join(hits, "\x00")}, nil

	case remoteTarScript:
		archivePath := cmd[4]
		members := s.resolveMembers(cmd[5:])
		if len(members) == 0 {
			return &target.ExecResult{ExitCode: 12}, nil
		}
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		for _, m := range members {
			content := s.remoteFS[m]
			if err := tw.WriteHeader(&tar.Header{Name: m, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
				return nil, err
			}
			if _, err := tw.Write([]byte(content)); err != nil {
				return nil, err
			}
		}
		if err := tw.Close(); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}
		s.archives[archivePath] = buf.Bytes()
		return &target.ExecResult{}, nil
	}
	return &target.ExecResult{}, nil
}

func (s *simRemote) exists(rel string) bool {
	if _, ok := s.remoteFS[rel]; ok {
		return true
	}
	prefix := rel + "/"
	for k := range s.remoteFS {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// resolveMembers 把目录成员展开成其下文件(真机由 tar 递归完成),任一成员不存在 → 回退空(判 12)。
func (s *simRemote) resolveMembers(members []string) []string {
	var out []string
	for _, m := range members {
		if _, ok := s.remoteFS[m]; ok {
			out = append(out, m)
			continue
		}
		prefix := m + "/"
		var found bool
		for k := range s.remoteFS {
			if strings.HasPrefix(k, prefix) {
				out = append(out, k)
				found = true
			}
		}
		if !found {
			return nil
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func newSim(fs map[string]string) *simRemote {
	return &simRemote{fakeRemoteTarget: &fakeRemoteTarget{serverIDs: map[string]bool{}}, remoteFS: fs, archives: map[string][]byte{}}
}

// artifactJob 造一条声明了产物路径的 script job。
func artifactJob(id, name, artifactPath string) pipeline.Job {
	return pipeline.Job{ID: id, Name: name, Type: pipeline.StepTypeScript,
		Config: map[string]any{"image": "node:20", "commands": "npm run build", "artifactPath": artifactPath}}
}

func runRemoteStage(t *testing.T, sim *simRemote, jobs ...pipeline.Job) *fakeReporter {
	t.Helper()
	b := newRemoteTestBuilder(&recordingDriver{})
	exec := NewStageExecutorWithRunner(b, nil, fakeRunnerLookup{serverID: "srv-1"}, sim)
	rep := &fakeReporter{}
	r := &run.Run{ID: "run-9", ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}}
	if err := exec(context.Background(), r, scriptStage(jobs...), rep); err != nil {
		t.Fatalf("远程执行失败:%v", err)
	}
	return rep
}

func joinedLogs(rep *fakeReporter) string { return strings.Join(rep.logs, "\n") }

// 目录产物:远程 app/dist 下的文件要回传并判成 dist,metadata 记回原位的相对路径与来源 job。
func TestRemotePullsDistArtifact(t *testing.T) {
	sim := newSim(map[string]string{"app/dist/index.js": "console.log(1)"})
	rep := runRemoteStage(t, sim, artifactJob("j-dist", "前端构建", "app/dist"))

	if len(rep.arts) != 1 {
		t.Fatalf("应登记 1 个产物,实际 %+v(日志:%s)", rep.arts, joinedLogs(rep))
	}
	art := rep.arts[0]
	if art.Type != run.ArtifactDist {
		t.Errorf("类型应为 dist,实际 %s", art.Type)
	}
	if got, _ := art.Metadata["workspacePath"].(string); got != "app/dist" {
		t.Errorf("workspacePath 应为远程同一相对路径,实际 %q", got)
	}
	if got, _ := art.Metadata["sourceJobId"].(string); got != "j-dist" {
		t.Errorf("sourceJobId 应为来源 job(供部署节点按任务挑产物),实际 %q", got)
	}
	if sim.streams != 1 {
		t.Errorf("归档应经 ExecStream 取回 1 次,实际 %d", sim.streams)
	}
	if !strings.Contains(joinedLogs(rep), "已回传远程产物:app/dist") {
		t.Errorf("日志应有回传说明,实际:%s", joinedLogs(rep))
	}
	// 收尾:临时归档与工作区都要在远程删掉,不留残。
	var sawRmTar, sawRmWS bool
	for _, c := range sim.cmds {
		j := strings.Join(c, " ")
		if len(c) == 3 && c[0] == "rm" && c[1] == "-f" && strings.HasSuffix(j, ".artifacts.tar.gz") {
			sawRmTar = true
		}
		if len(c) == 3 && c[0] == "rm" && c[1] == "-rf" && strings.Contains(j, "pipewright-remote") {
			sawRmWS = true
		}
	}
	if !sawRmTar || !sawRmWS {
		t.Errorf("应清理临时归档与远程工作区:rmTar=%v rmWS=%v cmds=%v", sawRmTar, sawRmWS, sim.cmds)
	}
}

// 通配:后端两条 *.jar 展开成实际成员一起打包,回传后本地各自成一条产物。
func TestRemotePullsWildcardJars(t *testing.T) {
	sim := newSim(map[string]string{
		"backend/target/app.jar":         "JAR-A",
		"backend/target/app-sources.jar": "JAR-B",
		"backend/target/classes.txt":     "不该被匹配",
	})
	rep := runRemoteStage(t, sim, artifactJob("j-jar", "后端构建", "backend/target/*.jar"))

	if len(rep.arts) != 2 {
		t.Fatalf("两条 jar 都应产出,实际 %+v(日志:%s)", rep.arts, joinedLogs(rep))
	}
	for _, art := range rep.arts {
		if art.Type != run.ArtifactJar {
			t.Errorf("类型应为 jar,实际 %s", art.Type)
		}
	}
	// tar 命令的实参必须是展开后的两个具体路径,不能把通配原样丢给 tar。
	var tarCmd []string
	for _, c := range sim.cmds {
		if len(c) >= 3 && c[2] == remoteTarScript {
			tarCmd = c
		}
	}
	if tarCmd == nil {
		t.Fatal("没发出远程 tar 命令")
	}
	if got := strings.Join(tarCmd[5:], " "); got != "backend/target/app-sources.jar backend/target/app.jar" &&
		got != "backend/target/app.jar backend/target/app-sources.jar" {
		t.Errorf("tar 成员应只有两个 jar,实际 %q", got)
	}
}

// 找不到就按本地的口径跳过并说明,绝不因为产物缺失把已成功的构建判失败。
func TestRemoteMissingArtifactSkips(t *testing.T) {
	sim := newSim(map[string]string{"app/dist/index.js": "x"})
	rep := runRemoteStage(t, sim, artifactJob("j", "构建", "app/out"))

	if len(rep.arts) != 0 {
		t.Fatalf("不该产出产物:%+v", rep.arts)
	}
	if !strings.Contains(joinedLogs(rep), "远程产物路径未找到,跳过回传:app/out") {
		t.Errorf("应有回传侧未找到说明,实际:%s", joinedLogs(rep))
	}
	for _, c := range sim.cmds {
		if len(c) >= 3 && c[2] == remoteTarScript {
			t.Fatal("路径不存在时不该发起远程 tar")
		}
	}
}

// 越界路径在中控机就拒,连远程都不问(与本地同一道闸;也是防把整台机器打包回传)。
func TestRemoteRejectsEscapingArtifactPath(t *testing.T) {
	sim := newSim(map[string]string{"../../etc/passwd": "secret"})
	rep := runRemoteStage(t, sim, artifactJob("j", "构建", "../etc/passwd"))

	if len(rep.arts) != 0 {
		t.Fatalf("越界路径不该产出:%+v", rep.arts)
	}
	if !strings.Contains(joinedLogs(rep), "产物路径越界,已拒绝") {
		t.Errorf("应有越界拒绝日志,实际:%s", joinedLogs(rep))
	}
	for _, c := range sim.cmds {
		if len(c) >= 3 && (c[2] == remoteLocateScript || c[2] == remoteGlobScript || c[2] == remoteTarScript) {
			t.Fatalf("越界路径不该发任何远程探测:%v", c)
		}
	}
}

// 一个 job 声明多行产物:逐条定位、一次归档命令对应一条声明。
func TestRemotePullsMultipleArtifactLines(t *testing.T) {
	sim := newSim(map[string]string{
		"web/dist/index.html": "html",
		"server/bin/app":      "bin",
	})
	rep := runRemoteStage(t, sim, artifactJob("j", "构建", "web/dist\nserver/bin/app"))

	if len(rep.arts) != 2 {
		t.Fatalf("两条声明都应产出,实际 %+v(日志:%s)", rep.arts, joinedLogs(rep))
	}
	if sim.streams != 2 {
		t.Errorf("两条声明各自回传一次,实际 ExecStream %d 次", sim.streams)
	}
}
