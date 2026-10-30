package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// cmdIndex 返回首条满足 match 的命令下标(找不到 → -1)。
func cmdIndex(calls [][]string, match func([]string) bool) int {
	for i, c := range calls {
		if match(c) {
			return i
		}
	}
	return -1
}

// TestBackupDirModeRunsBeforeArtifact 守住「先备份后铺」:整目录模式在产物落位**之前**打包，
// 且打包命令排除 .pw-backups 自身(否则备份里套备份,越滚越大)。
func TestBackupDirModeRunsBeforeArtifact(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := newFakeTarget(&fakeExec{})
	srv := seedServer(t, tgt, "web-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{
			"deployPath": "/srv/app",
			"backupMode": BackupModeDir,
			"backupKeep": "3",
		},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("want success, got %+v", res[0])
	}

	mkdirBk := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) == 3 && c[0] == "mkdir" && c[1] == "-p" && c[2] == "/srv/app/.pw-backups"
	})
	if mkdirBk < 0 {
		t.Fatalf("缺少备份目录 mkdir: %v", tgt.calls)
	}
	tarIdx := cmdIndex(tgt.calls, func(c []string) bool { return len(c) > 0 && c[0] == "tar" })
	if tarIdx < 0 {
		t.Fatalf("缺少备份 tar: %v", tgt.calls)
	}
	tar := tgt.calls[tarIdx]
	joined := strings.Join(tar, " ")
	if !strings.Contains(joined, "-C /srv/app") {
		t.Fatalf("备份应打部署目录: %v", tar)
	}
	if !strings.Contains(joined, "--exclude=./.pw-backups") {
		t.Fatalf("整目录备份必须排除备份目录自身: %v", tar)
	}
	// 产物落位(base64 写入)必须发生在备份之后。
	writeIdx := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) >= 3 && c[0] == "sh" && strings.Contains(c[2], "base64 -d")
	})
	if writeIdx < 0 || writeIdx < tarIdx {
		t.Fatalf("产物应在备份之后才落位(backup=%d, write=%d)", tarIdx, writeIdx)
	}
	// 保留份数:3 → tail -n +4(从第 4 份起删)。
	prune := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) >= 3 && c[0] == "sh" && strings.Contains(c[2], "tail -n +\"$2\"")
	})
	if prune < 0 || tgt.calls[prune][len(tgt.calls[prune])-1] != "4" {
		t.Fatalf("裁剪命令应保留 3 份(tail -n +4): %v", tgt.calls[prune])
	}

	// 落痕随结果持久化:恢复入口只认这张表。
	if res[0].DeployPath != "/srv/app" || res[0].BackupMode != BackupModeDir {
		t.Fatalf("结果未带部署落痕: %+v", res[0])
	}
	stored, err := rsvc.ListDeployTargets(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListDeployTargets: %v", err)
	}
	if len(stored) != 1 || stored[0].DeployPath != "/srv/app" || stored[0].BackupMode != BackupModeDir {
		t.Fatalf("部署落痕未落库: %+v", stored)
	}
}

// TestBackupSuffixModeSanitizesSuffixes 守住后缀白名单:只有安全形状的后缀会进 find 脚本,
// 带空格 / 分号 / 星号的注入尝试一律被丢掉(AC-SEC-02)。
func TestBackupSuffixModeSanitizesSuffixes(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := newFakeTarget(&fakeExec{})
	srv := seedServer(t, tgt, "web-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactJar, "target/app.jar")

	svc := New(tgt, rsvc)
	_, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{
			"deployPath":     "/srv/app",
			"backupMode":     BackupModeSuffix,
			"backupSuffixes": ".jar, war; rm -rf /, *.txt",
		},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	findIdx := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) >= 3 && c[0] == "sh" && strings.Contains(c[2], "find .")
	})
	if findIdx < 0 {
		t.Fatalf("缺少按后缀 find: %v", tgt.calls)
	}
	script := tgt.calls[findIdx][2]
	if !strings.Contains(script, "-name '*.jar'") || !strings.Contains(script, "-name '*.war'") {
		t.Fatalf("补齐前导点的后缀应进 find: %s", script)
	}
	if strings.Contains(script, "rm -rf") || strings.Contains(script, "*.txt") {
		t.Fatalf("不安全后缀必须被丢弃: %s", script)
	}
	tarIdx := cmdIndex(tgt.calls, func(c []string) bool { return len(c) > 0 && c[0] == "tar" })
	if tarIdx < 0 || !strings.Contains(strings.Join(tgt.calls[tarIdx], " "), "-T") {
		t.Fatalf("按后缀备份应经 -T 清单打包: %v", tgt.calls)
	}
}

// TestBackupFailureFailsTargetBeforeArtifact 守住「备份失败即该机失败」:用户要的是安全网,
// 悄悄跳过等于等他真要恢复时才发现没有备份。失败后产物一个字节都不该落位。
func TestBackupFailureFailsTargetBeforeArtifact(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	fx := &fakeExec{failOn: "tar"}
	tgt := newFakeTarget(fx)
	srv := seedServer(t, tgt, "web-1")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{"deployPath": "/srv/app", "backupMode": BackupModeDir},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res[0].Status != run.TargetFailed || !strings.Contains(res[0].Message, "备份失败") {
		t.Fatalf("备份失败应判该机 failed: %+v", res[0])
	}
	if writeIdx := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) >= 3 && c[0] == "sh" && strings.Contains(c[2], "base64 -d")
	}); writeIdx >= 0 {
		t.Fatalf("备份失败后不应再铺产物: %v", tgt.calls)
	}
}

// seedDeployedTarget 造一行「已部署过」的目标记录(恢复入口的唯一配置来源)。
func seedDeployedTarget(t *testing.T, rsvc run.Service, runID, serverID, name string, deployPath, restartCmd, backupMode string) {
	t.Helper()
	now := time.Now().UTC()
	err := rsvc.SaveDeployTargets(context.Background(), runID, []run.DeployTarget{{
		ServerID: serverID, ServerName: name, Status: run.TargetSuccess, StartedAt: now,
		DeployPath: deployPath, RestartCommand: restartCmd, BackupMode: backupMode,
	}})
	if err != nil {
		t.Fatalf("SaveDeployTargets: %v", err)
	}
}

// TestRestoreBackupOverwritesAndRestarts 守住恢复语义:解包回**该次部署记录的**目录,
// 再按记录的重启命令重启(目录作位置参数,用户命令不拼进脚本)。
func TestRestoreBackupOverwritesAndRestarts(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := newFakeTarget(&fakeExec{})
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")
	seedDeployedTarget(t, rsvc, runID, srv.ID, srv.Name, "/srv/app", "systemctl restart app", BackupModeDir)

	svc := New(tgt, rsvc)
	backupID := "20261030T101500Z-123456789.tar.gz"
	res, err := svc.RestoreBackup(context.Background(), RestoreInput{RunID: runID, ServerID: srv.ID, BackupID: backupID})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !res.OK || !res.Restarted || res.DeployPath != "/srv/app" {
		t.Fatalf("恢复结果不符: %+v", res)
	}

	archive := "/srv/app/.pw-backups/" + backupID
	if idx := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) == 3 && c[0] == "test" && c[1] == "-f" && c[2] == archive
	}); idx < 0 {
		t.Fatalf("恢复前应确认归档存在: %v", tgt.calls)
	}
	if idx := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) == 5 && c[0] == "tar" && c[1] == "-xzf" && c[2] == archive && c[4] == "/srv/app"
	}); idx < 0 {
		t.Fatalf("应按记录目录解包: %v", tgt.calls)
	}
	shIdx := cmdIndex(tgt.calls, func(c []string) bool {
		return len(c) == 4 && c[0] == "sh" && c[1] == "-c" && strings.Contains(c[2], "systemctl restart app")
	})
	if shIdx < 0 {
		t.Fatalf("应按记录的重启命令重启: %v", tgt.calls)
	}
	if tgt.calls[shIdx][3] != "/srv/app" {
		t.Fatalf("重启命令的部署目录应作位置参数: %v", tgt.calls[shIdx])
	}
}

// TestRestoreBackupRejectsUntrustedInput 守住定位类错误:没开备份 / 归档名不是平台形状 /
// 目标不在该 run 里,一律拒绝,绝不把客户端字符串拼进路径或命令。
func TestRestoreBackupRejectsUntrustedInput(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := newFakeTarget(&fakeExec{})
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")
	seedDeployedTarget(t, rsvc, runID, srv.ID, srv.Name, "/srv/app", "", "")

	svc := New(tgt, rsvc)
	ctx := context.Background()
	if _, err := svc.RestoreBackup(ctx, RestoreInput{RunID: runID, ServerID: srv.ID, BackupID: "20261030T101500Z-1.tar.gz"}); !errors.Is(err, ErrBackupsDisabled) {
		t.Fatalf("未开备份应报 ErrBackupsDisabled, got %v", err)
	}
	if _, err := svc.RestoreBackup(ctx, RestoreInput{RunID: runID, ServerID: "nope", BackupID: "20261030T101500Z-1.tar.gz"}); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("目标不存在应报 ErrTargetNotFound, got %v", err)
	}

	seedDeployedTarget(t, rsvc, runID, srv.ID, srv.Name, "/srv/app", "", BackupModeDir)
	for _, bad := range []string{"../../etc/passwd", "20261030T101500Z-1.tar.gz/../x", "sub/20261030T101500Z-1.tar.gz"} {
		if _, err := svc.RestoreBackup(ctx, RestoreInput{RunID: runID, ServerID: srv.ID, BackupID: bad}); !errors.Is(err, ErrBackupNotFound) {
			t.Fatalf("归档名 %q 应拒为 ErrBackupNotFound, got %v", bad, err)
		}
	}
}

// TestListBackupsParsesAndSorts 守住列表:只认平台形状的归档名,新 → 旧排序。
func TestListBackupsParsesAndSorts(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{execFn: func(_ string, cmd []string) (*target.ExecResult, error) {
		if len(cmd) > 0 && cmd[0] == "sh" {
			return &target.ExecResult{Stdout: strings.Join([]string{
				"20261030T101500Z-1.tar.gz\t100",
				"20261031T090000Z-5.tar.gz\t2048",
				"bogus.tar.gz\t7",
				"",
			}, "\n")}, nil
		}
		return &target.ExecResult{}, nil
	}}
	srv := seedServer(t, tgt, "web-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")
	seedDeployedTarget(t, rsvc, runID, srv.ID, srv.Name, "/srv/app", "", BackupModeDir)

	svc := New(tgt, rsvc)
	list, err := svc.ListBackups(context.Background(), runID, srv.ID)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if list.DeployPath != "/srv/app" || list.Mode != BackupModeDir {
		t.Fatalf("列表应带部署落痕: %+v", list)
	}
	if len(list.Archives) != 2 {
		t.Fatalf("只应保留平台形状的归档: %+v", list.Archives)
	}
	if list.Archives[0].ID != "20261031T090000Z-5.tar.gz" || list.Archives[0].SizeBytes != 2048 {
		t.Fatalf("应按时间新→旧: %+v", list.Archives)
	}
	if got := list.Archives[1].CreatedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-10-30T10:15:00Z" {
		t.Fatalf("创建时间解析错误: %s", got)
	}

	// 未开备份 → 空列表、不报错(也不是一次 SSH 往返)。
	seedDeployedTarget(t, rsvc, runID, srv.ID, srv.Name, "/srv/app", "", "")
	empty, err := svc.ListBackups(context.Background(), runID, srv.ID)
	if err != nil || len(empty.Archives) != 0 || empty.Mode != "" {
		t.Fatalf("未开备份应返回空列表: %+v, %v", empty, err)
	}
}
