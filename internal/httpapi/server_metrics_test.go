package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/target"
)

// --- 纯函数解析器单测(AC:健壮容错,空/格式异常 → 不 panic、false) ---

func TestParseLoadavg(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0.42 0.35 0.30 1/234 5678\n", 0.42, true},
		{"1.00 0.50 0.10 2/300 999", 1.00, true},
		{"", 0, false},
		{"garbage", 0, false},
		{"  \n ", 0, false},
	}
	for _, c := range cases {
		v, ok := parseLoadavg(c.in)
		if ok != c.ok || (ok && v != c.want) {
			t.Fatalf("parseLoadavg(%q) = %v,%v want %v,%v", c.in, v, ok, c.want, c.ok)
		}
	}
}

func TestParseUptimeLoadavg(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"12:00  up 5 days,  load averages: 1.23 1.10 1.05", 1.23, true},   // macOS
		{"12:00:00 up 1 day,  load average: 0.50, 0.40, 0.30", 0.50, true}, // Linux
		{"no load info here", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		v, ok := parseUptimeLoadavg(c.in)
		if ok != c.ok || (ok && v != c.want) {
			t.Fatalf("parseUptimeLoadavg(%q) = %v,%v want %v,%v", c.in, v, ok, c.want, c.ok)
		}
	}
}

func TestParseFreeBytes(t *testing.T) {
	out := "              total        used        free      shared  buff/cache   available\n" +
		"Mem:    17179869184  4123456789  1000000000   100000   500000000  12000000000\n" +
		"Swap:    2147483648           0  2147483648\n"
	used, usedWithCache, total, ok := parseFreeBytes(out)
	// used = 「used」列(不含缓存);usedWithCache = total - free(含页缓存)。
	if !ok || total != 17179869184 || used != 4123456789 || usedWithCache != 17179869184-1000000000 {
		t.Fatalf("parseFreeBytes = %d,%d,%d,%v", used, usedWithCache, total, ok)
	}
	if _, _, _, ok := parseFreeBytes(""); ok {
		t.Fatalf("empty should be false")
	}
	if _, _, _, ok := parseFreeBytes("no mem line here\n"); ok {
		t.Fatalf("no Mem line should be false")
	}
}

func TestParseSwapBytes(t *testing.T) {
	out := "" +
		"              total        used        free\n" +
		"Mem:    8054091776  2855583744  1000000000\n" +
		"Swap:   2147483648   536870912  1610612736\n"
	used, total, ok := parseSwapBytes(out)
	if !ok || total != 2147483648 || used != 536870912 {
		t.Fatalf("parseSwapBytes = %d,%d,%v", used, total, ok)
	}
	// 未配置 swap:total=0 仍算成功。
	if u, tt, ok := parseSwapBytes("Swap:   0   0   0\n"); !ok || u != 0 || tt != 0 {
		t.Fatalf("zero swap should be ok with 0/0, got %d,%d,%v", u, tt, ok)
	}
	// 无 Swap 行 → false。
	if _, _, ok := parseSwapBytes("Mem:  1 2 3\n"); ok {
		t.Fatalf("no swap line should be false")
	}
}

func TestParseVMStat(t *testing.T) {
	out := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
		"Pages free:                              100000.\n" +
		"Pages active:                            200000.\n" +
		"Pages inactive:                           50000.\n" +
		"Pages speculative:                         1000.\n" +
		"Pages wired down:                         30000.\n" +
		"Pages occupied by compressor:             10000.\n"
	pg, pageSize, ok := parseVMStat(out)
	if !ok || pageSize != 16384 {
		t.Fatalf("parseVMStat = %+v,%d,%v", pg, pageSize, ok)
	}
	if pg.free != 100000 || pg.active != 200000 || pg.wired != 30000 || pg.compressed != 10000 {
		t.Fatalf("page counts = %+v", pg)
	}
	// 缺必需项(无 wired 行)→ false,不给半截口径。
	if _, _, ok := parseVMStat("Pages free: 1.\nPages active: 2.\n"); ok {
		t.Fatalf("missing wired should be false")
	}
	if _, _, ok := parseVMStat(""); ok {
		t.Fatalf("empty should be false")
	}
}

// macOS 的页计数抬头可能被 vm_stat 换成大写单位(`page size of 4.1K bytes`),
// 那时抬头解析不出数字 → 用 4096 假设兜底(vm_stat 输出里唯一出现 K/M/G 的地方就是它)。
func TestParseVMStatFallbackPageSize(t *testing.T) {
	out := "Mach Virtual Memory Statistics: (page size of 4.1K bytes)\n" +
		"Pages free:     1000.\nPages active:   2000.\nPages wired down: 500.\n"
	pg, pageSize, ok := parseVMStat(out)
	if !ok || pageSize != 4096 || pg.active != 2000 {
		t.Fatalf("parseVMStat fallback = %+v,%d,%v", pg, pageSize, ok)
	}
}

func TestParseSwapUsage(t *testing.T) {
	out := "total = 1048576.00M  used = 32768.50M  free = 1015807.50M"
	used, total, ok := parseSwapUsage(out)
	if !ok || total != 1048576*1024*1024 || used != int64(32768.5*1024*1024) {
		t.Fatalf("parseSwapUsage = %d,%d,%v", used, total, ok)
	}
	// 未启用 swap(total=0)→ false,UI 不渲染 swap 行。
	if _, _, ok := parseSwapUsage("total = 0.00M  used = 0.00M  free = 0.00M"); ok {
		t.Fatalf("zero swap should be false")
	}
	for _, bad := range []string{"", "some = 1.00M", "total = x  used = 1M"} {
		if _, _, ok := parseSwapUsage(bad); ok {
			t.Fatalf("parseSwapUsage(%q) should be false", bad)
		}
	}
}

func TestParseSizeWithUnit(t *testing.T) {
	cases := map[string]int64{
		"1024":   1024,
		"1.5K":   1536,
		"2MiB":   2 * 1024 * 1024,
		"32768M": 32768 * 1024 * 1024,
		"1G":     1024 * 1024 * 1024,
		"0.00M":  0,
	}
	for in, want := range cases {
		got, ok := parseSizeWithUnit(in)
		if !ok || got != want {
			t.Fatalf("parseSizeWithUnit(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1M", "12petab", "="} {
		if v, ok := parseSizeWithUnit(bad); ok {
			t.Fatalf("parseSizeWithUnit(%q) should fail, got %d", bad, v)
		}
	}
}

func TestParseDmidecodeMemBytes(t *testing.T) {
	// 现版本 dmidecode:二进制单位 GiB;含一个未装设备(应跳过)。
	out := "" +
		"Memory Device\n\tSize: 8 GiB\n\tLocator: DIMM 0\n" +
		"Memory Device\n\tSize: No Module Installed\n\tLocator: DIMM 1\n"
	got, ok := parseDmidecodeMemBytes(out)
	if !ok || got != 8*1024*1024*1024 {
		t.Fatalf("parseDmidecodeMemBytes(GiB) = %d,%v want %d", got, ok, int64(8*1024*1024*1024))
	}

	// 旧版 dmidecode:十进制 MB,多条相加。
	out2 := "\tSize: 4096 MB\n\tSize: 4096 MB\n"
	got2, ok2 := parseDmidecodeMemBytes(out2)
	if !ok2 || got2 != 2*4096*1000*1000 {
		t.Fatalf("parseDmidecodeMemBytes(MB) = %d,%v", got2, ok2)
	}

	// 全部未装 / 空 → false(上层留 0 不展示)。
	if _, ok := parseDmidecodeMemBytes("Memory Device\n\tSize: No Module Installed\n"); ok {
		t.Fatalf("no installed module should be false")
	}
	if _, ok := parseDmidecodeMemBytes(""); ok {
		t.Fatalf("empty should be false")
	}
}

func TestParseDf(t *testing.T) {
	// df -B1 (bytes)
	out := "Filesystem      1B-blocks         Used    Available Use% Mounted on\n" +
		"/dev/disk1   494384795648  123456789012  370927006636  25% /\n"
	used, total, ok := parseDf(out, 1)
	if !ok || total != 494384795648 || used != 123456789012 {
		t.Fatalf("parseDf bytes = %d,%d,%v", used, total, ok)
	}
	// df -k (KiB) → ×1024
	outK := "Filesystem 1024-blocks    Used Available Capacity Mounted on\n" +
		"/dev/disk1   1000000  400000    600000      40% /\n"
	usedK, totalK, okK := parseDf(outK, 1024)
	if !okK || totalK != 1000000*1024 || usedK != 400000*1024 {
		t.Fatalf("parseDf kib = %d,%d,%v", usedK, totalK, okK)
	}
	// folded long device name (data on its own line)
	outFold := "Filesystem                 1B-blocks         Used    Available Use% Mounted on\n" +
		"/dev/mapper/very-long-name\n" +
		"            494384795648  123456789012  370927006636  25% /\n"
	uf, tf, of := parseDf(outFold, 1)
	if !of || tf != 494384795648 || uf != 123456789012 {
		t.Fatalf("parseDf folded = %d,%d,%v", uf, tf, of)
	}
	if _, _, ok := parseDf("", 1); ok {
		t.Fatalf("empty df should be false")
	}
}

func TestParseInt(t *testing.T) {
	cases := map[string]struct {
		v  int
		ok bool
	}{
		"8\n":   {8, true},
		"  16 ": {16, true},
		"":      {0, false},
		"x":     {0, false},
		"-1":    {0, false},
	}
	for in, want := range cases {
		v, ok := parseInt(in)
		if ok != want.ok || (ok && v != want.v) {
			t.Fatalf("parseInt(%q) = %d,%v want %d,%v", in, v, ok, want.v, want.ok)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	// PRETTY_NAME 优先(带引号、可能含空格)。
	if v, ok := parseOSRelease("NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n"); !ok || v != "Ubuntu 24.04.2 LTS" {
		t.Fatalf("parseOSReleasePRETTY = %q,%v", v, ok)
	}
	// 精简镜像只有 NAME。
	if v, ok := parseOSRelease("NAME=\"Alpine Linux\"\nID=alpine\n"); !ok || v != "Alpine Linux" {
		t.Fatalf("parseOSReleaseNAME = %q,%v", v, ok)
	}
	for _, bad := range []string{"", "\n\n", "ID=ubuntu\nHOME_URL=\"https://x\"\n"} {
		if _, ok := parseOSRelease(bad); ok {
			t.Fatalf("parseOSRelease(%q) should be false", bad)
		}
	}
}

func TestParseSwVers(t *testing.T) {
	out := "ProductName:\t\tmacOS\nProductVersion:\t\t15.5\nBuildVersion:\t\t24F74\n"
	if v, ok := parseSwVers(out); !ok || v != "macOS 15.5" {
		t.Fatalf("parseSwVers = %q,%v", v, ok)
	}
	// 只有版本号也认(Linux 上 sw_vers 不存在 → 空输出 false)。
	if v, ok := parseSwVers("ProductVersion:\t\t15.5\n"); !ok || v != "15.5" {
		t.Fatalf("parseSwVers version-only = %q,%v", v, ok)
	}
	for _, bad := range []string{"", "sw_vers: command not found", "SomeOther:\tx\n"} {
		if _, ok := parseSwVers(bad); ok {
			t.Fatalf("parseSwVers(%q) should be false", bad)
		}
	}
}

func TestParseProcUptime(t *testing.T) {
	if v, ok := parseProcUptime("123456.78 234567.89\n"); !ok || v != 123456 {
		t.Fatalf("parseProcUptime = %d,%v", v, ok)
	}
	for _, bad := range []string{"", "abc 1", "-1 2"} {
		if _, ok := parseProcUptime(bad); ok {
			t.Fatalf("parseProcUptime(%q) should be false", bad)
		}
	}
}

func TestParseBoottimeUptime(t *testing.T) {
	now := time.Unix(1800000000, 0)
	// sec 后的数字带尾逗号(C 结构体字面量)。
	if v, ok := parseBoottimeUptime("{ sec = 1799913600, usec = 511774 } Mon Sep 21 17:13:47 2026", now); !ok || v != 86400 {
		t.Fatalf("parseBoottimeUptime = %d,%v want 86400", v, ok)
	}
	// 主机时钟超前(启动时刻在 now 之后)→ 0 而不是负数。
	if v, ok := parseBoottimeUptime("{ sec = 1800009999, usec = 0 } Fri Jan 1 00:00:00 2038", now); !ok || v != 0 {
		t.Fatalf("clock skew should clamp to 0, got %d,%v", v, ok)
	}
	for _, bad := range []string{"", "nope", "{ sec = 0, usec = 0 }"} {
		if _, ok := parseBoottimeUptime(bad, now); ok {
			t.Fatalf("parseBoottimeUptime(%q) should be false", bad)
		}
	}
}

// --- per-command dialer:让集成测试按命令返回不同输出 ---
type cmdDialer struct {
	// byCmd 按命令首参(程序名 + 关键参数)路由 stdout / exitCode。
	fn func(cmd []string) (*target.ExecResult, error)
}

func (d cmdDialer) Run(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (*target.ExecResult, error) {
	return d.fn(cmd)
}

func (d cmdDialer) RunStream(_ context.Context, _ string, _ target.SSHConfig, _ []string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (d cmdDialer) RunInteractive(_ context.Context, _ string, _ target.SSHConfig, _ []string) (target.Session, error) {
	return nil, nil
}

func (d cmdDialer) RunWithStdin(_ context.Context, _ string, _ target.SSHConfig, cmd []string, _ io.Reader) (*target.ExecResult, error) {
	return d.fn(cmd)
}

// linuxLikeDialer 模拟一台 Linux 服务器:loadavg/nproc/free/df 全可回显。
func linuxLikeDialer() cmdDialer {
	return cmdDialer{fn: func(cmd []string) (*target.ExecResult, error) {
		switch {
		case len(cmd) >= 2 && cmd[0] == "cat" && cmd[1] == "/proc/loadavg":
			return &target.ExecResult{Stdout: "0.42 0.35 0.30 1/234 5678\n", ExitCode: 0}, nil
		case cmd[0] == "nproc":
			return &target.ExecResult{Stdout: "8\n", ExitCode: 0}, nil
		case cmd[0] == "free":
			return &target.ExecResult{Stdout: "              total        used        free\nMem:    17179869184  4123456789  1000000000\n", ExitCode: 0}, nil
		case cmd[0] == "df" && len(cmd) >= 2 && cmd[1] == "-B1":
			return &target.ExecResult{Stdout: "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 494384795648 123456789012 370927006636 25% /\n", ExitCode: 0}, nil
		// 系统信息:Linux 有 uname 各字段、/etc/os-release、/proc/uptime;sw_vers 不存在。
		case cmd[0] == "uname":
			out := map[string]string{
				"-s": "Linux\n", "-r": "6.6.87-orbstack\n", "-m": "x86_64\n", "-n": "pw-node-1\n",
			}[lastArg(cmd)]
			return &target.ExecResult{Stdout: out, ExitCode: 0}, nil
		case cmd[0] == "cat" && len(cmd) >= 2 && cmd[1] == "/etc/os-release":
			return &target.ExecResult{Stdout: "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n", ExitCode: 0}, nil
		case cmd[0] == "cat" && len(cmd) >= 2 && cmd[1] == "/proc/uptime":
			return &target.ExecResult{Stdout: "123456.78 234567.89\n", ExitCode: 0}, nil
		default:
			return &target.ExecResult{Stdout: "", Stderr: "command not found", ExitCode: 127}, nil
		}
	}}
}

// lastArg 取命令最后一个参数(uname -s / -r / -m / -n 的分派键)。
func lastArg(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	return cmd[len(cmd)-1]
}

// macLikeDialer 模拟 macOS:无 /proc/loadavg、无 nproc、无 free、df -B1 不识别 → 各自回退
// (内存走 vm_stat + sysctl hw.memsize)。
func macLikeDialer() cmdDialer {
	return cmdDialer{fn: func(cmd []string) (*target.ExecResult, error) {
		switch {
		case cmd[0] == "cat" && len(cmd) >= 2 && cmd[1] == "/proc/loadavg":
			return &target.ExecResult{Stdout: "", Stderr: "No such file or directory", ExitCode: 1}, nil
		case cmd[0] == "uptime":
			return &target.ExecResult{Stdout: "12:00  up 5 days, load averages: 1.23 1.10 1.05\n", ExitCode: 0}, nil
		case cmd[0] == "nproc":
			return &target.ExecResult{Stderr: "nproc: command not found", ExitCode: 127}, nil
		case cmd[0] == "getconf":
			return &target.ExecResult{Stdout: "10\n", ExitCode: 0}, nil
		case cmd[0] == "free":
			return &target.ExecResult{Stderr: "free: command not found", ExitCode: 127}, nil
		case cmd[0] == "vm_stat":
			return &target.ExecResult{Stdout: "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
				"Pages free:                              100000.\n" +
				"Pages active:                            200000.\n" +
				"Pages inactive:                           50000.\n" +
				"Pages wired down:                         30000.\n" +
				"Pages occupied by compressor:             10000.\n", ExitCode: 0}, nil
		case cmd[0] == "sysctl" && len(cmd) >= 3 && cmd[2] == "hw.memsize":
			return &target.ExecResult{Stdout: "34359738368\n", ExitCode: 0}, nil
		case cmd[0] == "sysctl" && len(cmd) >= 3 && cmd[2] == "vm.swapusage":
			return &target.ExecResult{Stdout: "total = 1024.00M  used = 256.50M  free = 767.50M\n", ExitCode: 0}, nil
		case cmd[0] == "df" && len(cmd) >= 2 && cmd[1] == "-B1":
			return &target.ExecResult{Stderr: "df: illegal option -- B", ExitCode: 1}, nil
		case cmd[0] == "df" && len(cmd) >= 2 && cmd[1] == "-k":
			return &target.ExecResult{Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk1 1000000 400000 600000 40% /\n", ExitCode: 0}, nil
		// 系统信息:macOS 有 uname 各字段与 sw_vers,没有 /etc/os-release、/proc/uptime;
		// 运行时长退 kern.boottime(启动时刻 = now − 2 天,用固定偏移便于断言)。
		case cmd[0] == "uname":
			out := map[string]string{
				"-s": "Darwin\n", "-r": "24.5.0\n", "-m": "arm64\n", "-n": "pw-mac-1\n",
			}[lastArg(cmd)]
			return &target.ExecResult{Stdout: out, ExitCode: 0}, nil
		case cmd[0] == "cat" && len(cmd) >= 2 && cmd[1] == "/etc/os-release":
			return &target.ExecResult{Stderr: "No such file or directory", ExitCode: 1}, nil
		case cmd[0] == "cat" && len(cmd) >= 2 && cmd[1] == "/proc/uptime":
			return &target.ExecResult{Stderr: "No such file or directory", ExitCode: 1}, nil
		case cmd[0] == "sw_vers":
			return &target.ExecResult{Stdout: "ProductName:\t\tmacOS\nProductVersion:\t\t15.5\nBuildVersion:\t\t24F74\n", ExitCode: 0}, nil
		case cmd[0] == "sysctl" && len(cmd) >= 3 && cmd[2] == "kern.boottime":
			boot := time.Now().Unix() - 2*86400
			return &target.ExecResult{Stdout: fmt.Sprintf("{ sec = %d, usec = 511774 } Mon Sep 21 17:13:47 2026\n", boot), ExitCode: 0}, nil
		default:
			return &target.ExecResult{ExitCode: 127}, nil
		}
	}}
}

func TestServerMetricsLinux(t *testing.T) {
	srv, client, csrf := setupServerAPI(t, linuxLikeDialer())
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	resp, _ := client.Get(srv.URL + "/api/servers/" + id + "/metrics")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var out serverMetricsDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if !out.Reachable {
		t.Fatalf("want reachable true: %+v", out)
	}
	if out.CPU == nil || out.CPU.Loadavg1 == nil || *out.CPU.Loadavg1 != 0.42 {
		t.Fatalf("cpu loadavg wrong: %+v", out.CPU)
	}
	if out.CPU.Cores == nil || *out.CPU.Cores != 8 {
		t.Fatalf("cores wrong: %+v", out.CPU)
	}
	if out.Memory == nil || out.Memory.TotalBytes != 17179869184 || out.Memory.UsedBytes != 4123456789 {
		t.Fatalf("memory wrong: %+v", out.Memory)
	}
	if out.Disk == nil || out.Disk.Path != "/" || out.Disk.TotalBytes != 494384795648 || out.Disk.UsedBytes != 123456789012 {
		t.Fatalf("disk wrong: %+v", out.Disk)
	}
	if out.System == nil {
		t.Fatalf("system 段应存在(可达就有)")
	}
	if out.System.OS != "Linux" || out.System.Distro != "Ubuntu 24.04.2 LTS" ||
		out.System.Kernel != "6.6.87-orbstack" || out.System.Arch != "x86_64" ||
		out.System.Hostname != "pw-node-1" || out.System.UptimeSeconds != 123456 {
		t.Fatalf("system 字段不符: %+v", out.System)
	}
	if out.CollectedAt == "" {
		t.Fatalf("collectedAt empty")
	}
}

func TestServerMetricsMacFallback(t *testing.T) {
	srv, client, csrf := setupServerAPI(t, macLikeDialer())
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	resp, _ := client.Get(srv.URL + "/api/servers/" + id + "/metrics")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var out serverMetricsDTO
	_ = json.Unmarshal(raw, &out)
	if !out.Reachable {
		t.Fatalf("want reachable true: %+v", out)
	}
	// loadavg via uptime fallback
	if out.CPU == nil || out.CPU.Loadavg1 == nil || *out.CPU.Loadavg1 != 1.23 {
		t.Fatalf("cpu loadavg fallback wrong: %+v", out.CPU)
	}
	// cores via getconf fallback
	if out.CPU.Cores == nil || *out.CPU.Cores != 10 {
		t.Fatalf("cores fallback wrong: %+v", out.CPU)
	}
	// free 缺失 → 走 vm_stat + hw.memsize(macOS 口径):页大小 16384。
	wantUsed := (200000 + 30000 + 10000) * 16384 // active + wired + 压缩页(不可回收缓存)
	wantCache := 34359738368 - 100000*16384      // 总量 - 空闲页 = 含可回收缓存
	if out.Memory == nil {
		t.Fatalf("memory should be resolved from vm_stat: %s", raw)
	}
	if out.Memory.TotalBytes != 34359738368 || out.Memory.UsedBytes != int64(wantUsed) {
		t.Fatalf("memory(vm_stat) = %+v, want total 34359738368 used %d", out.Memory, wantUsed)
	}
	if out.Memory.UsedWithCacheBytes != int64(wantCache) {
		t.Fatalf("usedWithCache = %d, want %d", out.Memory.UsedWithCacheBytes, wantCache)
	}
	if out.Memory.SwapTotalBytes != 1024*1024*1024 || out.Memory.SwapUsedBytes != int64(256.5*1024*1024) {
		t.Fatalf("swap = %d/%d, want %d/%d", out.Memory.SwapUsedBytes, out.Memory.SwapTotalBytes,
			int64(256.5*1024*1024), int64(1024*1024*1024))
	}
	// df -k fallback
	if out.Disk == nil || out.Disk.TotalBytes != 1000000*1024 || out.Disk.UsedBytes != 400000*1024 {
		t.Fatalf("disk fallback wrong: %+v", out.Disk)
	}
	// 系统信息:发行版走 sw_vers(该机没有 /etc/os-release),运行时长走 kern.boottime(now − 2 天)。
	if out.System == nil {
		t.Fatalf("system 段应存在(可达就有)")
	}
	if out.System.OS != "Darwin" || out.System.Distro != "macOS 15.5" ||
		out.System.Kernel != "24.5.0" || out.System.Arch != "arm64" || out.System.Hostname != "pw-mac-1" {
		t.Fatalf("system 字段不符: %+v", out.System)
	}
	if d := out.System.UptimeSeconds - 2*86400; d < -5 || d > 5 {
		t.Fatalf("uptimeSeconds = %d, want ≈%d(kern.boottime 换算)", out.System.UptimeSeconds, 2*86400)
	}
}

func TestServerMetricsUnreachableNot500(t *testing.T) {
	// dialer 返回不可达错误 → reachable:false + 人读 error,200 不 500。
	dialer := cmdDialer{fn: func(_ []string) (*target.ExecResult, error) {
		return nil, target.ErrUnreachable
	}}
	srv, client, csrf := setupServerAPI(t, dialer)
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	resp, _ := client.Get(srv.URL + "/api/servers/" + id + "/metrics")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (not 500): %s", resp.StatusCode, raw)
	}
	var out serverMetricsDTO
	_ = json.Unmarshal(raw, &out)
	if out.Reachable {
		t.Fatalf("want reachable false")
	}
	if out.Error == "" {
		t.Fatalf("want human error")
	}
	if out.CPU != nil || out.Memory != nil || out.Disk != nil || out.System != nil {
		t.Fatalf("metrics should be null when unreachable: %+v", out)
	}
}

func TestServerMetricsNotFound(t *testing.T) {
	srv, client, csrf := setupServerAPI(t, linuxLikeDialer())
	_ = csrf
	resp, _ := client.Get(srv.URL + "/api/servers/does-not-exist/metrics")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, raw)
	}
}

func TestServerMetricsRequiresAuth(t *testing.T) {
	srv, _, _ := setupServerAPI(t, linuxLikeDialer())
	// 不带 session 的裸客户端。
	bare := &http.Client{}
	resp, _ := bare.Get(srv.URL + "/api/servers/x/metrics")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics status = %d, want 401: %s", resp.StatusCode, raw)
	}
	resp2, _ := bare.Get(srv.URL + "/api/servers/metrics")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("batch metrics status = %d, want 401", resp2.StatusCode)
	}
}

func TestAllServerMetricsBatchIndependent(t *testing.T) {
	// 两台服务器:一台 Linux 可回显,一台不可达(死端口),批量端点逐台独立。
	// 用 host 区分行为:127.0.0.1 → ok;其它 → unreachable。
	dialer := cmdDialer{fn: func(cmd []string) (*target.ExecResult, error) {
		return linuxLikeDialer().fn(cmd)
	}}
	srv, client, csrf := setupServerAPI(t, dialer)
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	_ = createServerAPI(t, client, srv.URL, csrf, credID) // s1 (host 127.0.0.1)

	resp, _ := client.Get(srv.URL + "/api/servers/metrics")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d, want 200: %s", resp.StatusCode, raw)
	}
	var body struct {
		Items []serverMetricsDTO `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(body.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(body.Items))
	}
	if !body.Items[0].Reachable || body.Items[0].Disk == nil {
		t.Fatalf("item should be reachable with disk: %+v", body.Items[0])
	}
}

// countingBatchDialer 实现 target 的可选 batchDialer,并数出每条命令是怎么来的:
// 指标采集的正确形态 = 探针 1 次 Run(一整轮拨号)+ 其余命令 1 次 RunBatch(同一条连接)。
// 若哪天有人把采集改回逐条 Exec,这里会立刻涨成近十次 Run —— 那正是「刷新很慢」的病因。
type countingBatchDialer struct {
	mu      sync.Mutex
	runs    int
	batches int
	cmds    [][]string
}

func (d *countingBatchDialer) record(cmd []string) *target.ExecResult {
	d.mu.Lock()
	d.cmds = append(d.cmds, cmd)
	d.mu.Unlock()
	res, _ := linuxLikeDialer().fn(cmd)
	return res
}

func (d *countingBatchDialer) Run(_ context.Context, _ string, _ target.SSHConfig, cmd []string) (*target.ExecResult, error) {
	d.mu.Lock()
	d.runs++
	d.mu.Unlock()
	return d.record(cmd), nil
}

func (d *countingBatchDialer) RunBatch(_ context.Context, _ string, _ target.SSHConfig, cmds [][]string) ([]*target.ExecResult, error) {
	d.mu.Lock()
	d.batches++
	d.mu.Unlock()
	out := make([]*target.ExecResult, 0, len(cmds))
	for _, cmd := range cmds {
		out = append(out, d.record(cmd))
	}
	return out, nil
}

func (d *countingBatchDialer) RunStream(_ context.Context, _ string, _ target.SSHConfig, _ []string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (d *countingBatchDialer) RunInteractive(_ context.Context, _ string, _ target.SSHConfig, _ []string) (target.Session, error) {
	return nil, nil
}

func (d *countingBatchDialer) RunWithStdin(_ context.Context, _ string, _ target.SSHConfig, cmd []string, _ io.Reader) (*target.ExecResult, error) {
	return d.record(cmd), nil
}

func (d *countingBatchDialer) counts() (runs, batches int, cmds [][]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs, d.batches, d.cmds
}

func TestServerMetricsUsesOneConnectionPerHost(t *testing.T) {
	d := &countingBatchDialer{}
	srv, client, csrf := setupServerAPI(t, d)
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	get := func() serverMetricsDTO {
		t.Helper()
		resp, err := client.Get(srv.URL + "/api/servers/" + id + "/metrics")
		if err != nil {
			t.Fatalf("GET metrics: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, raw)
		}
		var out serverMetricsDTO
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	if out := get(); !out.Reachable || out.Memory == nil || out.Disk == nil {
		t.Fatalf("首轮采集指标不全: %+v", out)
	}
	runs, batches, cmds := d.counts()
	if runs != 1 || batches != 1 {
		t.Fatalf("应当只拨号一次(探针)+ 一次批量, got runs=%d batches=%d", runs, batches)
	}
	if got := countCmd(cmds, "dmidecode"); got != 1 {
		t.Fatalf("首轮应探测一次物理内存(dmidecode), got %d", got)
	}

	// 第二轮:仍然只「探针 + 一批」,且物理总量已缓存 → 不再跑 dmidecode。
	if out := get(); out.Memory == nil || out.Memory.TotalBytes <= 0 {
		t.Fatalf("二轮内存指标丢失: %+v", out)
	}
	runs, batches, cmds = d.counts()
	if runs != 2 || batches != 2 {
		t.Fatalf("两轮合计应为 runs=2 batches=2, got runs=%d batches=%d", runs, batches)
	}
	if got := countCmd(cmds, "dmidecode"); got != 1 {
		t.Fatalf("物理内存应只探测一次(其余走缓存), got %d", got)
	}
}

// countCmd 数出记录里程序名为 name 的命令条数。
func countCmd(cmds [][]string, name string) int {
	n := 0
	for _, c := range cmds {
		if len(c) > 0 && c[0] == name {
			n++
		}
	}
	return n
}
