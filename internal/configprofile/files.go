package configprofile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// AtomicWriteFile 按"tmp + fsync + rename"原子写入文件(P0 #3 修订)。
//
//   1. 写到 dir/<filename>.tmp.<pid>(同目录,保证 rename atomic)
//   2. f.Sync() 落盘
//   3. os.Rename(tmp, final)(POSIX 保证同 fs 上 atomic;失败时删 tmp)
//   4. dir fsync(确保 dir entry 落盘)
//
// 注意:Windows 下 rename 不原子;Linux/Mac 同 fs rename atomic。
// 本平台主运行环境是 Linux;在 Windows 上运行时接受 best-effort。
func AtomicWriteFile(path string, data []byte, perm os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	pid := strconv.Itoa(os.Getpid())
	tmp := filepath.Join(dir, "."+base+".tmp."+pid)

	// 确保 dir 存在(调用方应已确保;此处兜底)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("configprofile: mkdir %s: %w", dir, err)
	}

	// 1. open tmp
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("configprofile: open tmp %s: %w", tmp, err)
	}

	// 任何步骤失败都清理 tmp
	defer func() {
		if retErr != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	// 2. write + fsync
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("configprofile: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("configprofile: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("configprofile: close tmp: %w", err)
	}

	// 3. rename(同 fs atomic)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("configprofile: rename %s → %s: %w", tmp, path, err)
	}

	// 4. dir fsync(确保 rename 落盘)
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// RemoveFile 删单个文件;容忍"不存在"语义(返回 nil)。
// 与 AtomicWriteFile 对应:service.Delete 会调它清 disk。
func RemoveFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ProfileDir 给定 dataDir 与 id,返回该 profile 的固定目录路径。
// 不创建目录(由调用方决定何时 MkdirAll;典型:Create 时创建,Delete 时保留)。
func ProfileDir(dataDir, id string) string {
	return filepath.Join(dataDir, "config_profiles", id)
}

// ProfilePath 给定 dataDir, id, filename,返回 profile 文件完整路径。
func ProfilePath(dataDir, id, filename string) string {
	return filepath.Join(ProfileDir(dataDir, id), filename)
}