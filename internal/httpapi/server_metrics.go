package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/target"
)

// Story 6.1(FR-15):多机状态总览 —— 服务器层资源指标(CPU 负载/核数、内存 used/total、
// 磁盘 used/total),经 SSH 跑**固定白名单只读命令**采集,解析为结构化指标。
//
// AC-SEC-02 核心:采集命令是**纯静态命令 array**,绝不接受任何用户输入拼接 —— 无注入面。
// 指标无敏感信息。
//
// 容错纪律:
//   - 某台不可达 / 认证失败 → 该台 reachable:false + 人读 error,**不 500**,不连累其它台。
//   - 单个指标命令缺失 / 输出格式异常 → 该指标 null(指针为 nil),不报错、不影响其它指标
//     (跨平台 best-effort:Linux 与 macOS/BSD 各有自己的命令路径,都不支持才 null)。
//   - 一台机器**只拨号一次**:探针验可达,其余命令在同一条连接上跑完(见 collectServerMetrics)。
//   - 批量端点逐台并行采集,有界并发(信号量防 N 台同时 SSH 打爆)。

const (
	// metricsConcurrency 是批量采集的最大并发 SSH 数(有界,防打爆)。
	metricsConcurrency = 6
	// metricsProbeTimeout 是「这台机器可达吗」那一次拨号 + 命令的预算。不可达的主机(端口被
	// 过滤、主机下线)会一直挂到超时,这个值就是刷新最慢那台的代价 —— 必须明显小于整轮采集,
	// 否则一台死机器把全页拖到 15s。
	metricsProbeTimeout = 5 * time.Second
	// metricsBatchTimeout 是复用同一条连接跑完全部指标命令的预算。命令本身都是毫秒级,
	// 预算主要给慢链路留余量。
	metricsBatchTimeout = 8 * time.Second
	// metricsOutMax 是单条命令 stdout 解析前的截断上限(防超大输出撑爆内存;指标输出本就极小)。
	metricsOutMax = 64 * 1024
)

// 采集命令(AC-SEC-02:固定静态 array,绝不含任何用户输入)。
//
// 一台机器只拨号一次、在这条连接上把下表全跑完,再按「哪个平台有哪个命令」逐个解析:
// 不必先判 OS,也天然获得跨平台回退(Linux 缺 uptime、macOS 缺 free 都能落到另一条上)。
// 多跑几条「本机没有、直接非零退出」的命令只多几个 session 往返(毫秒级),比多拨一次号便宜得多。
//   - loadavg:`cat /proc/loadavg`(Linux);macOS 无 /proc → 回退 `uptime`。
//   - cores:`nproc`;缺失回退 `getconf _NPROCESSORS_ONLN`(含 macOS)。
//   - memory:`free -b`(Linux);macOS/BSD 无 free → `vm_stat` + `sysctl hw.memsize`。
//   - swap:`free -b` 的 Swap 行;macOS → `sysctl vm.swapusage`。
//   - disk:`df -B1 /`;不识别 -B1(老 macOS)→ 回退 `df -k /`(KiB)换算字节。
//   - dmidecode:仅 Linux 且缓存里没有该主机物理总量时才追加(需 root)。
//   - 系统信息:`uname -r/-m/-n` + `/etc/os-release`(Linux)/ `sw_vers`(macOS) +
//     `/proc/uptime`(Linux)/ `sysctl kern.boottime`(macOS/BSD)。
//     `uname` 必须一个字段一次:GNU/BSD/busybox 对 `-srmn` 这类组合参数**按各自固定顺序**
//     输出(macOS 与 busybox 实测都打成 `s n r m`,不是请求的 `s r m n`),拼一行没法稳定拆字段。
var (
	cmdUname      = []string{"uname", "-s"}
	cmdLoadavg    = []string{"cat", "/proc/loadavg"}
	cmdUptime     = []string{"uptime"}
	cmdNproc      = []string{"nproc"}
	cmdGetconfCPU = []string{"getconf", "_NPROCESSORS_ONLN"}
	cmdFreeBytes  = []string{"free", "-b"}
	cmdVMStat     = []string{"vm_stat"}
	cmdMemSize    = []string{"sysctl", "-n", "hw.memsize"}
	cmdSwapUsage  = []string{"sysctl", "-n", "vm.swapusage"}
	cmdDfBytes    = []string{"df", "-B1", "/"}
	cmdDfKiB      = []string{"df", "-k", "/"}
	// 系统信息(静态标识,不随负载变)。
	cmdUnameRelease = []string{"uname", "-r"}
	cmdUnameMachine = []string{"uname", "-m"}
	cmdUnameNode    = []string{"uname", "-n"}
	// Linux 发行版名PRETTY_NAME;macOS 无 /etc/os-release → 回退 sw_vers。
	cmdOSRelease = []string{"cat", "/etc/os-release"}
	cmdSwVers    = []string{"sw_vers"}
	// 运行时长:/proc/uptime 第一列(Linux);macOS/BSD 无 /proc → kern.boottime 换 now-启动时刻。
	cmdProcUptime   = []string{"cat", "/proc/uptime"}
	cmdKernBoottime = []string{"sysctl", "-n", "kern.boottime"}
	// 物理/分配内存(SMBIOS Type 17 内存设备容量之和)。`free` 的 MemTotal 是内核
	// **可用**总量(已扣固件/内核保留),通常略小于物理装机量;dmidecode 读 SMBIOS
	// 得「物理/分配」总量,与 PVE 等宿主面板显示的总量一致。需 root;非 root / 无
	// dmidecode / 虚拟化未暴露 SMBIOS → 采集失败 → physicalTotalBytes 为 0(不展示)。
	cmdDmidecodeMem = []string{"dmidecode", "-t", "17"}
)

// 指标命令的固定顺序 = 结果切片下标(见 metricPlan)。新增命令往末尾加,别插中间。
const (
	idxLoadavg = iota
	idxUptime
	idxNproc
	idxGetconfCPU
	idxFreeBytes
	idxVMStat
	idxMemSize
	idxSwapUsage
	idxDfBytes
	idxDfKiB
	idxUnameRelease
	idxUnameMachine
	idxUnameNode
	idxOSRelease
	idxSwVers
	idxProcUptime
	idxKernBoottime
	idxCount // 基数组长度,供校验/追加用
)

// idxDmidecodeMem 是物理内存命令在「本轮需要重新探测」时追加到计划末尾的下标。
const idxDmidecodeMem = idxCount

var metricPlanBase = [][]string{
	cmdLoadavg,
	cmdUptime,
	cmdNproc,
	cmdGetconfCPU,
	cmdFreeBytes,
	cmdVMStat,
	cmdMemSize,
	cmdSwapUsage,
	cmdDfBytes,
	cmdDfKiB,
	cmdUnameRelease,
	cmdUnameMachine,
	cmdUnameNode,
	cmdOSRelease,
	cmdSwVers,
	cmdProcUptime,
	cmdKernBoottime,
}

// metricPlan 组本轮要跑的命令;needPhys 为真时末尾追加 dmidecode。
func metricPlan(needPhys bool) [][]string {
	if !needPhys {
		return metricPlanBase
	}
	plan := make([][]string, 0, idxDmidecodeMem+1)
	plan = append(plan, metricPlanBase...)
	return append(plan, cmdDmidecodeMem)
}

// cpuMetric / memoryMetric / diskMetric 是各维度指标 DTO(冻结契约字段形状)。
// 任一维度采集/解析失败 → 整段为 null(指针 nil),不影响其它维度。
type cpuMetric struct {
	Loadavg1 *float64 `json:"loadavg1"`
	Cores    *int     `json:"cores"`
}

type memoryMetric struct {
	UsedBytes int64 `json:"usedBytes"` // 不含可回收页缓存(进程真实占用 / 内存压力口径)
	// 含页缓存(total - free);与 cgroup 总用量 / PVE 等容器面板的「已用」一致。
	UsedWithCacheBytes int64 `json:"usedWithCacheBytes"`
	TotalBytes         int64 `json:"totalBytes"` // free 的 MemTotal:内核**可用**总量
	// 物理/分配总量(dmidecode SMBIOS);0 表示采集不到。通常 ≥ TotalBytes,与宿主
	// 面板(PVE 等)显示的总量一致,用作「含缓存」口径的分母以对齐其百分比。
	PhysicalTotalBytes int64 `json:"physicalTotalBytes"`
	// 交换分区 used/total(free 的 Swap 行);SwapTotalBytes 为 0 表示未配置 swap。
	SwapUsedBytes  int64 `json:"swapUsedBytes"`
	SwapTotalBytes int64 `json:"swapTotalBytes"`
}

type diskMetric struct {
	Path       string `json:"path"`
	UsedBytes  int64  `json:"usedBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

// systemMetric 是该机的静态标识(发行版/内核/架构/主机名/运行时长)。
// 逐字段 best-effort:采不到的字段留空串 / 0,UI 跳过不显示;整段为 null 只在该机不可达时出现。
type systemMetric struct {
	// 内核名(`uname -s` 探针输出):Linux / Darwin 等。探针成功就一定有值。
	OS string `json:"os"`
	// 人读发行版:Linux 取 /etc/os-release 的 PRETTY_NAME(退 NAME),macOS 取 sw_vers 的
	// ProductName + ProductVersion(如「macOS 15.5」)。
	Distro string `json:"distro"`
	// 内核版本(`uname -r`),如 6.6.87-orbstack / 24.5.0。
	Kernel string `json:"kernel"`
	// CPU 架构(`uname -m`),如 x86_64 / arm64 / aarch64。
	Arch string `json:"arch"`
	// 机器自己报的主机名(`uname -n`),可能与登记名不同。
	Hostname string `json:"hostname"`
	// 开机至今秒数(Linux 读 /proc/uptime;macOS/BSD 用 kern.boottime 换算)。0 = 采不到。
	UptimeSeconds int64 `json:"uptimeSeconds"`
}

// serverMetricsDTO 是单台服务器指标响应体(冻结契约)。
//   - reachable:false 时 cpu/memory/disk 为 null,error 人读非空。
//   - reachable:true 时各指标独立:解析失败的维度为 null,其余正常。
type serverMetricsDTO struct {
	ServerID    string        `json:"serverId"`
	Reachable   bool          `json:"reachable"`
	Error       string        `json:"error"`
	CPU         *cpuMetric    `json:"cpu"`
	Memory      *memoryMetric `json:"memory"`
	Disk        *diskMetric   `json:"disk"`
	System      *systemMetric `json:"system"`
	CollectedAt string        `json:"collectedAt"`
}

// collectServerMetrics 对单台服务器采集指标。永不返回 error:不可达/失败均落到
// DTO.reachable=false + 人读 error(批量端点据此让单台失败不连累全局)。
// 第二个返回值是「定位类」错误(服务器/凭据不存在、保险库未配),仅供单台端点映射 422/503;
// 批量端点忽略它(逐台独立,定位类对某台亦只表现为该台 reachable:false)。
//
// 两轮动作,不是一轮一条命令:
//  1. 探可达:`uname -s` 一次拨号(短超时)——它的连接/定位错误决定 reachable。
//  2. 采集:其余命令在同一条 SSH 连接上跑完(ExecBatch),只再开若干 session。
//
// 第 2 步是关键:逐条 Exec 等于把同一台机器拨号 + 握手 + 认证近十次,时间全花在重复建连上
// (实测整批 15s,单机六七条命令串行拨号);复用连接后单机降到一两百毫秒量级。
func collectServerMetrics(ctx context.Context, svc target.Service, id string) (serverMetricsDTO, error) {
	out := serverMetricsDTO{ServerID: id, CollectedAt: time.Now().UTC().Format(time.RFC3339)}

	pctx, cancelProbe := context.WithTimeout(ctx, metricsProbeTimeout)
	probeRes, probeErr := svc.Exec(pctx, id, cmdUname)
	cancelProbe()
	if probeErr != nil {
		out.Reachable = false
		out.Error = humanMetricsError(probeErr)
		if isLocateError(probeErr) {
			return out, probeErr
		}
		return out, nil
	}
	out.Reachable = true
	// 探针的 uname -s 白捡一份内核名,不必再跑一次。
	osName := firstToken(probeRes.Stdout)

	// 物理总量是静态硬件量:缓存命中就不再跑 dmidecode(见 wantPhysicalProbe)。
	needPhys := wantPhysicalProbe(id)
	bctx, cancelBatch := context.WithTimeout(ctx, metricsBatchTimeout)
	defer cancelBatch()

	// 探针已确认这台可达,故此处只看结果:连接中途断/超时 → 已跑完的部分仍解析,缺的维度自然
	// 落为 null(单条命令非零退出从来不是错误,由解析层按空输出降级)。
	results, _ := svc.ExecBatch(bctx, id, metricPlan(needPhys))

	out.CPU = readCPU(results)
	out.Memory = readMemory(results, id, needPhys)
	out.Disk = readDisk(results)
	out.System = readSystem(results, osName, time.Now())
	return out, nil
}

// isLocateError 判定是否为「定位类」错误(服务器/凭据不存在、保险库未配)——这类该映射
// 422/503 而非 reachable:false。
func isLocateError(err error) bool {
	return errors.Is(err, target.ErrNotFound) ||
		errors.Is(err, target.ErrCredentialNotFound) ||
		errors.Is(err, target.ErrVaultUnconfigured)
}

// stdoutAt 取下标处命令的 stdout(截断到 metricsOutMax);该条没跑到 / 为 nil → 空串。
func stdoutAt(results []*target.ExecResult, i int) string {
	if i < 0 || i >= len(results) || results[i] == nil {
		return ""
	}
	out := results[i].Stdout
	if len(out) > metricsOutMax {
		out = out[:metricsOutMax]
	}
	return out
}

// readCPU 取 CPU 负载 + 核数:各维度独立,解析失败即子字段 nil。
func readCPU(results []*target.ExecResult) *cpuMetric {
	m := &cpuMetric{}
	if v, ok := parseLoadavg(stdoutAt(results, idxLoadavg)); ok {
		m.Loadavg1 = &v
	} else if v, ok := parseUptimeLoadavg(stdoutAt(results, idxUptime)); ok { // macOS 无 /proc/loadavg
		m.Loadavg1 = &v
	}
	if c, ok := parseInt(stdoutAt(results, idxNproc)); ok {
		m.Cores = &c
	} else if c, ok := parseInt(stdoutAt(results, idxGetconfCPU)); ok { // 无 nproc(macOS/BSD)
		m.Cores = &c
	}
	return m
}

// readMemory 取内存 used/total(字节)。Linux 走 `free -b`,macOS/BSD 走 `vm_stat` + hw.memsize;
// 两条路都解析不出来 → nil(契约允许某维度缺失)。
func readMemory(results []*target.ExecResult, id string, probedPhys bool) *memoryMetric {
	if used, withCache, total, ok := parseFreeBytes(stdoutAt(results, idxFreeBytes)); ok {
		m := &memoryMetric{UsedBytes: used, UsedWithCacheBytes: withCache, TotalBytes: total}
		// Swap 与内存来自同一份 `free -b`:解析 Swap 行(未配置 swap → 0/0)。
		if su, st, sok := parseSwapBytes(stdoutAt(results, idxFreeBytes)); sok {
			m.SwapUsedBytes, m.SwapTotalBytes = su, st
		}
		m.PhysicalTotalBytes = physicalTotalLinux(id, total, probedPhys, stdoutAt(results, idxDmidecodeMem))
		return m
	}
	return readMemoryDarwin(results)
}

// readMemoryDarwin 用 vm_stat(页计数)+ sysctl hw.memsize(物理总量)凑出 macOS/BSD 的内存口径。
// 任一必需项缺失 → nil(没有分母就不画百分比,别给半截数据)。
func readMemoryDarwin(results []*target.ExecResult) *memoryMetric {
	total, okTotal := parseInt(stdoutAt(results, idxMemSize))
	pg, pageSize, okPages := parseVMStat(stdoutAt(results, idxVMStat))
	if !okTotal || !okPages || total <= 0 {
		return nil
	}
	t64 := int64(total)
	// 「已用(不含可回收缓存)」对齐 Linux 的 used 列:活跃 + 内核常驻 + 压缩页 —— 压缩页
	// 不是可回收缓存,算进已用才与活动监视器的内存压力一致。
	used := (pg.active + pg.wired + pg.compressed) * pageSize
	if used < 0 || used > t64 {
		used = 0
	}
	m := &memoryMetric{
		UsedBytes: used,
		// 含缓存口径:总量 - 空闲页(macOS 的 inactive/speculative 就是可回收缓存)。
		UsedWithCacheBytes: t64 - pg.free*pageSize,
		TotalBytes:         t64,
		// macOS 的 hw.memsize 就是物理总量,与 TotalBytes 同源。
		PhysicalTotalBytes: t64,
	}
	if su, st, sok := parseSwapUsage(stdoutAt(results, idxSwapUsage)); sok {
		m.SwapUsedBytes, m.SwapTotalBytes = su, st
	}
	return m
}

// ─── 物理内存缓存 ──────────────────────────────────────────────────────────────
//
// dmidecode 取的物理/分配内存是静态量(不随负载变),但采集页可能每 10s 轮询一次。
// 按 serverID 缓存:成功值(>0)长期复用;失败(非 root / 无 dmidecode / SSH 抖动)缓存
// 一个冷却期,避免对失败主机每轮都重跑。进程重启即清空,自然容纳极少见的换内存。
//
// 缓存只决定「本轮要不要把 dmidecode 加进命令计划」,因此读取必须在发批次之前、写入在解析之后。
type physMemEntry struct {
	bytes    int64 // >0:已知物理总量;0:探测过但取不到
	probedAt time.Time
}

var (
	physMemMu    sync.Mutex
	physMemCache = map[string]physMemEntry{}
)

// physMemRetryCooldown:对「取不到」的主机多久重试一次 dmidecode(成功值不受此限,永久缓存)。
const physMemRetryCooldown = 10 * time.Minute

// wantPhysicalProbe 报告本轮是否需要把 dmidecode 加进命令计划:没有成功的缓存,
// 且(没探过 或 失败的冷却期已过)。
func wantPhysicalProbe(id string) bool {
	now := time.Now()
	physMemMu.Lock()
	defer physMemMu.Unlock()
	e, hit := physMemCache[id]
	if !hit {
		return true
	}
	return e.bytes <= 0 && now.Sub(e.probedAt) >= physMemRetryCooldown
}

// physicalTotalLinux 返回该 Linux 主机物理/分配内存(字节),0 表示取不到。probed 为真时
// 解析本轮 dmidecode 输出并写缓存;否则复用缓存。memTotal 用于合理性校验(物理量应 ≥ 内核可用量)。
func physicalTotalLinux(id string, memTotal int64, probed bool, dmiOut string) int64 {
	if !probed {
		physMemMu.Lock()
		defer physMemMu.Unlock()
		if e, hit := physMemCache[id]; hit && e.bytes >= memTotal {
			return e.bytes
		}
		return 0
	}

	var phys int64
	if p, ok := parseDmidecodeMemBytes(dmiOut); ok && p >= memTotal {
		phys = p
	}
	physMemMu.Lock()
	physMemCache[id] = physMemEntry{bytes: phys, probedAt: time.Now()}
	physMemMu.Unlock()
	return phys
}

// readDisk 取根分区 used/total(字节):`df -B1 /` 优先,不识别 -B1 时回退 `df -k /` 换算。
// 解析失败 → nil。
func readDisk(results []*target.ExecResult) *diskMetric {
	if used, total, ok := parseDf(stdoutAt(results, idxDfBytes), 1); ok {
		return &diskMetric{Path: "/", UsedBytes: used, TotalBytes: total}
	}
	if used, total, ok := parseDf(stdoutAt(results, idxDfKiB), 1024); ok {
		return &diskMetric{Path: "/", UsedBytes: used, TotalBytes: total}
	}
	return nil
}

// --- 解析器(纯函数,可单测;空/格式异常一律 ok=false,绝不 panic) ---

// parseLoadavg 解析 `/proc/loadavg`,取第一个字段(1 分钟负载)。
// 例:`0.42 0.35 0.30 1/234 5678` → 0.42。
func parseLoadavg(s string) (float64, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// parseUptimeLoadavg 从 `uptime` 输出解析 1 分钟负载(macOS/Linux 通用)。
// 例:`... load averages: 1.23 1.10 1.05`(macOS)或 `... load average: 1.23, 1.10, 1.05`(Linux)。
func parseUptimeLoadavg(s string) (float64, bool) {
	low := strings.ToLower(s)
	idx := strings.Index(low, "load average")
	if idx < 0 {
		return 0, false
	}
	rest := s[idx:]
	// 跳到冒号后。
	if c := strings.Index(rest, ":"); c >= 0 {
		rest = rest[c+1:]
	}
	// 逗号/空白都当分隔。
	rest = strings.ReplaceAll(rest, ",", " ")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// parseInt 解析单个整数(nproc / getconf 输出),裁剪空白。
func parseInt(s string) (int, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, false
	}
	// 取第一行第一个 token(防多余输出)。
	fields := strings.Fields(t)
	v, err := strconv.Atoi(fields[0])
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// parseFreeBytes 解析 `free -b` 输出,取 Mem 行的 total 与两种「已使用」口径(字节)。
// 形如:
//
//	              total        used        free      shared  buff/cache   available
//	Mem:    17179869184   2854748160   706924544  ...
//
// 返回两口径(均常见、各有用途,不绑定任何虚拟化平台):
//   - used:free 的「used」列(第 2 列)—— **不含可回收页缓存**,反映进程真实占用 /
//     内存压力(htop、node_exporter 同口径)。
//   - usedWithCache:total - free(= used + buff/cache)—— **含页缓存**,与 cgroup 总用量 /
//     PVE 等容器面板的「已用」一致(它们把页缓存算进已用)。
//
// 取 Mem 行第 1 列 total、第 2 列 used、第 3 列 free。容错:列不足/非数字/越界 → false。
func parseFreeBytes(s string) (used, usedWithCache, total int64, ok bool) {
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(fields[0]), "mem") {
			continue
		}
		t, err1 := strconv.ParseInt(fields[1], 10, 64)
		u, err2 := strconv.ParseInt(fields[2], 10, 64)
		f, err3 := strconv.ParseInt(fields[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || t <= 0 || u < 0 || f < 0 || f > t {
			return 0, 0, 0, false
		}
		return u, t - f, t, true
	}
	return 0, 0, 0, false
}

// parseSwapBytes 解析 `free -b` 的 Swap 行,取 total/used(字节)。形如:
//
//	Swap:    2147483648    536870912   1610612736
//
// 取第 1 列 total、第 2 列 used。无 Swap 行(部分系统)或列不足/非数字 → false;
// total=0(未配置 swap)仍算成功(used/total 均 0,UI 据此不渲染 swap 行)。
func parseSwapBytes(s string) (used, total int64, ok bool) {
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(fields[0]), "swap") {
			continue
		}
		t, err1 := strconv.ParseInt(fields[1], 10, 64)
		u, err2 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil || t < 0 || u < 0 {
			return 0, 0, false
		}
		return u, t, true
	}
	return 0, 0, false
}

// vmStatPages 是 vm_stat 里我们关心的四类页计数(单位:页,不含页大小)。
type vmStatPages struct {
	free       int64
	active     int64
	wired      int64
	compressed int64
}

// parseVMStat 解析 macOS/BSD 的 `vm_stat` 输出:页大小从抬头
// 「(page size of 16384 bytes)」取(缺省按 4096),页计数从各「Pages xxx:」行取。
// 必需项(free/active/wired)任一缺失 → false(宁可不显示,也不给半截口径)。
func parseVMStat(s string) (vmStatPages, int64, bool) {
	pageSize := int64(4096)
	pg := vmStatPages{}
	got := map[string]bool{}

	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		// 抬头「(page size of 16384 bytes)」:它不是「键: 数值」行,必须先单独取,否则会被
		// 下面的数值解析跳过,页大小就静默退化成 4096 假设。
		if strings.Contains(strings.ToLower(trimmed), "page size of") {
			if ps, ok := pageSizeFromHeader(trimmed); ok {
				pageSize = ps
			}
			continue
		}
		c := strings.Index(trimmed, ":")
		if c < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:c]))
		// 数值行末尾常带 '.'(`Pages free:  104855.`),取第一个 token 并去掉它。
		fields := strings.Fields(trimmed[c+1:])
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(fields[0], "."), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch key {
		case "pages free":
			pg.free, got["free"] = n, true
		case "pages active":
			pg.active, got["active"] = n, true
		case "pages wired down":
			pg.wired, got["wired"] = n, true
		case "pages occupied by compressor":
			pg.compressed = n
		}
	}
	if !got["free"] || !got["active"] || !got["wired"] {
		return vmStatPages{}, 0, false
	}
	return pg, pageSize, true
}

// pageSizeFromHeader 从「Mach Virtual Memory Statistics: (page size of 16384 bytes)」取页字节数。
func pageSizeFromHeader(line string) (int64, bool) {
	i := strings.Index(strings.ToLower(line), "page size of")
	if i < 0 {
		return 0, false
	}
	rest := strings.Fields(line[i+len("page size of"):])
	if len(rest) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parseSwapUsage 解析 macOS 的 `sysctl -n vm.swapusage`:
//
//	total = 1048576.00M  used = 32768.00M  free = 1015808.00M
//
// total/used 任一项取不到 → false;total=0(未启用 swap)也算 false,UI 同样不渲染 swap 行。
func parseSwapUsage(s string) (used, total int64, ok bool) {
	fields := strings.Fields(s)
	var gotTotal, gotUsed bool
	for i := 0; i+2 < len(fields); i++ {
		if fields[i+1] != "=" {
			continue
		}
		v, vok := parseSizeWithUnit(fields[i+2])
		if !vok {
			continue
		}
		switch strings.ToLower(fields[i]) {
		case "total":
			total, gotTotal = v, true
		case "used":
			used, gotUsed = v, true
		}
	}
	if !gotTotal || !gotUsed || total <= 0 {
		return 0, 0, false
	}
	return used, total, true
}

// parseSizeWithUnit 解析带 K/M/G/T(或 KB/KiB/…)后缀的容量数字,按二进制 1024 进位换算字节。
// 支持小数(vm.swapusage 就打印 `32768.00M`);无后缀按字节。
func parseSizeWithUnit(tok string) (int64, bool) {
	t := strings.ToLower(strings.TrimSpace(tok))
	i := len(t)
	for i > 0 && (t[i-1] < '0' || t[i-1] > '9') && t[i-1] != '.' {
		i--
	}
	num, unit := t[:i], t[i:]
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	mult, mok := sizeUnitMultiplier(unit)
	if !mok {
		return 0, false
	}
	return int64(f * float64(mult)), true
}

// sizeUnitMultiplier 把容量单位后缀换算为字节倍数;空后缀 = 字节。
func sizeUnitMultiplier(unit string) (int64, bool) {
	switch unit {
	case "":
		return 1, true
	case "k", "kb", "kib":
		return 1024, true
	case "m", "mb", "mib":
		return 1024 * 1024, true
	case "g", "gb", "gib":
		return 1024 * 1024 * 1024, true
	case "t", "tb", "tib":
		return 1024 * 1024 * 1024 * 1024, true
	}
	return 0, false
}

// parseDmidecodeMemBytes 解析 `dmidecode -t 17` 输出,累加各「已装」内存设备的 Size
// 得物理/分配总量(字节)。形如:
//
//	Memory Device
//	        Size: 8 GiB
//	Memory Device
//	        Size: No Module Installed
//
// 单位大小写不敏感,兼容 dmidecode 各版本写法:kB/MB/GB/TB(十进制千)与 KiB/MiB/GiB/TiB
// (二进制 1024)。"No Module Installed" / "Unknown" / 非数字 → 跳过该设备。
// 一个有效 Size 都没有 → false(交由上层留 0、不展示)。
func parseDmidecodeMemBytes(s string) (int64, bool) {
	var total int64
	found := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Size:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Size:"))
		if len(fields) < 2 {
			continue // "No Module Installed"/"Unknown" 等 → 跳过
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		mult, uok := memUnitMultiplier(fields[1])
		if !uok {
			continue
		}
		total += n * mult
		found = true
	}
	if !found || total <= 0 {
		return 0, false
	}
	return total, true
}

// memUnitMultiplier 把内存单位换算为字节倍数。二进制单位(KiB/MiB/...)按 1024 进位,
// 十进制单位(kB/MB/...)按 1000 进位;dmidecode 现版本用二进制(GiB),旧版用 MB/GB。
func memUnitMultiplier(unit string) (int64, bool) {
	switch strings.ToLower(unit) {
	case "kb":
		return 1000, true
	case "mb":
		return 1000 * 1000, true
	case "gb":
		return 1000 * 1000 * 1000, true
	case "tb":
		return 1000 * 1000 * 1000 * 1000, true
	case "kib":
		return 1024, true
	case "mib":
		return 1024 * 1024, true
	case "gib":
		return 1024 * 1024 * 1024, true
	case "tib":
		return 1024 * 1024 * 1024 * 1024, true
	}
	return 0, false
}

// parseDf 解析 `df` 输出的根分区行,取 total(第 2 列)/ used(第 3 列),乘以 unit 化为字节。
// `df -B1 /` 时 unit=1(已是字节);`df -k /` 时 unit=1024(KiB)。
// 形如:
//
//	Filesystem     1B-blocks       Used   Available Use% Mounted on
//	/dev/disk1  494384795648  ...
//
// df 可能把长设备名折行;故扫描所有非表头行,取**首个含 ≥4 个数值列**的数据行。容错 → false。
func parseDf(s string, unit int64) (used, total int64, ok bool) {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// 跳过表头(首列 Filesystem)。
		if i == 0 || strings.EqualFold(fields[0], "Filesystem") {
			continue
		}
		// df 折行时数据可能在下一行,字段数少；规整后期望:[fs] total used avail use% mounted
		// 或折行后:total used avail use% mounted(无 fs 列)。统一找连续两个可解析为大整数的列。
		t, u, found := extractDfTotalsUsed(fields)
		if found {
			if t <= 0 || u < 0 {
				return 0, 0, false
			}
			return u * unit, t * unit, true
		}
	}
	return 0, 0, false
}

// extractDfTotalsUsed 从 df 数据行字段里取 total/used。标准布局:
// Filesystem total used avail capacity ... → total=fields[1], used=fields[2]。
// 折行布局(首列已被折到上一行):total used avail ... → total=fields[0], used=fields[1]。
// 用启发式:找首个连续两列都是纯数字(且后续还有列)的位置当 total/used。
func extractDfTotalsUsed(fields []string) (total, used int64, ok bool) {
	for i := 0; i+1 < len(fields); i++ {
		t, e1 := strconv.ParseInt(fields[i], 10, 64)
		u, e2 := strconv.ParseInt(fields[i+1], 10, 64)
		if e1 == nil && e2 == nil {
			return t, u, true
		}
	}
	return 0, 0, false
}

// readSystem 组装系统标识:内核名来自探针(必得),其余字段逐条 best-effort。
// 采集不到就留空 / 0,UI 跳过该段,不显示「不可用」这类噪音。
func readSystem(results []*target.ExecResult, osName string, now time.Time) *systemMetric {
	m := &systemMetric{
		OS:       osName,
		Kernel:   firstLine(stdoutAt(results, idxUnameRelease)),
		Arch:     firstLine(stdoutAt(results, idxUnameMachine)),
		Hostname: firstLine(stdoutAt(results, idxUnameNode)),
	}
	// 发行版:Linux 读 /etc/os-release;macOS/BSD 没有该文件 → 退 sw_vers。
	if d, ok := parseOSRelease(stdoutAt(results, idxOSRelease)); ok {
		m.Distro = d
	} else if d, ok := parseSwVers(stdoutAt(results, idxSwVers)); ok {
		m.Distro = d
	}
	// 运行时长:Linux 有 /proc/uptime;macOS/BSD 无 /proc → 用 kern.boottime 换算。
	if s, ok := parseProcUptime(stdoutAt(results, idxProcUptime)); ok {
		m.UptimeSeconds = s
	} else if s, ok := parseBoottimeUptime(stdoutAt(results, idxKernBoottime), now); ok {
		m.UptimeSeconds = s
	}
	return m
}

// firstToken 取输出的第一个空白分隔 token(去空白);空 → ""。
func firstToken(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// firstLine 取第一行的首 token,并截到 128 字节(防异常输出把响应体撑大)。
func firstLine(s string) string {
	line := strings.TrimSpace(s)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	t := strings.TrimSpace(line)
	if len(t) > 128 {
		t = t[:128]
	}
	return t
}

// parseOSRelease 从 /etc/os-release 取人读发行版名:PRETTY_NAME 优先(「Ubuntu 24.04.2 LTS」),
// 退 NAME(部分精简镜像只有 NAME)。键值都按 shell 赋值语法 `KEY=value` 取,值去外层引号。
// 该文件是**只读解析**,绝不执行。两样都没有 → false(交给上层退 sw_vers)。
func parseOSRelease(s string) (string, bool) {
	var name string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if val == "" {
			continue
		}
		switch key {
		case "PRETTY_NAME":
			return val, true
		case "NAME":
			name = val
		}
	}
	return name, name != ""
}

// parseSwVers 把 macOS 的 sw_vers 输出(ProductName / ProductVersion / BuildVersion 三行)
// 拼成「macOS 15.5」。任一行有值即成功;两行都没有 → false。
func parseSwVers(s string) (string, bool) {
	var product, version string
	for _, line := range strings.Split(s, "\n") {
		key, val, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		switch key {
		case "ProductName":
			product = val
		case "ProductVersion":
			version = val
		}
	}
	switch {
	case product != "" && version != "":
		return product + " " + version, true
	case version != "":
		return version, true
	case product != "":
		return product, true
	}
	return "", false
}

// parseProcUptime 取 /proc/uptime 第一列(开机至今秒,浮点)→ 整数秒。
func parseProcUptime(s string) (int64, bool) {
	tok := firstToken(s)
	if tok == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(f), true
}

// parseBoottimeUptime 从 macOS/BSD 的 `sysctl -n kern.boottime` 算运行秒数。
// 输出形如 `{ sec = 1758000000, usec = 511774 } Mon Sep 21 17:13:47 2026`:取 `sec =` 后的
// 启动时刻(**带尾逗号**,那是 C 结构体字面量),用 now 相减(now 由调用方传入,单测可喂固定时钟)。
// 主机时钟比本机还晚(倒挂)→ 0,不显示负运行时长。
func parseBoottimeUptime(s string, now time.Time) (int64, bool) {
	fields := strings.Fields(s)
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] != "sec" || fields[i+1] != "=" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(fields[i+2], ","), 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		up := now.Unix() - n
		if up < 0 {
			up = 0
		}
		return up, true
	}
	return 0, false
}

// humanMetricsError 把领域错误映射为人读文案(绝不含凭据明文/内部栈)。
func humanMetricsError(err error) string {
	switch {
	case errors.Is(err, target.ErrAuth):
		return "SSH 认证失败:密钥或口令无效,或无登录权限"
	case errors.Is(err, target.ErrUnreachable):
		return "无法连接服务器:端口未开放、主机不可达或超时"
	case errors.Is(err, target.ErrInvalidCredential):
		return "凭据不是可用的 SSH 私钥或口令"
	case errors.Is(err, context.DeadlineExceeded):
		return "采集超时"
	default:
		return "采集指标失败:连接或命令执行错误"
	}
}

// --- HTTP handlers ---

// makeServerMetricsHandler 返回 GET /api/servers/{id}/metrics(认证,只读)。
// 服务器不存在/凭据不存在/保险库未配 → 标准状态码;连接/认证/采集失败 → 200 + reachable:false,不 500。
func makeServerMetricsHandler(svc target.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		// 先确认服务器存在(404 在写任何 200 体之前)。
		if _, err := svc.Get(r.Context(), id); err != nil {
			writeServerError(w, err)
			return
		}
		out, locErr := collectServerMetrics(r.Context(), svc, id)
		// 定位类错误(凭据不存在 / 保险库未配)→ 走标准映射(422/503),而非 reachable:false。
		if locErr != nil {
			writeServerError(w, locErr)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeAllServerMetricsHandler 返回 GET /api/servers/metrics(认证,只读;批量)。
// 逐台并行采集(有界并发),各自独立:某台失败仅该台 reachable:false,不连累其它台、不 500。
// 聚合范围 = actor 可见分组内的服务器(与容器总览同一口径)。
//
// 两种响应形态,同一套采集:
//   - 默认 JSON `{items:[…]}`:等全部采完一次性返回。Dashboard 只要一台数/汇总,逐台时序无意义。
//   - `?stream=1` NDJSON(一行一台,采完即 flush):首屏不必等最慢那台 —— 可达机 0.2s 就出卡,
//     死机拖到探针超时也只压住它自己那一行。
func makeAllServerMetricsHandler(svc target.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		visible, err := visibleGroups(r, acc)
		if err != nil {
			writeAccessError(w, err)
			return
		}
		servers, err := svc.ListScoped(r.Context(), target.ListFilter{Visible: visible})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		ids := make([]string, len(servers))
		for i, srv := range servers {
			ids[i] = srv.ID
		}
		events := runMetricsPool(r.Context(), svc, ids)

		if !wantsMetricsStream(r) {
			items := make([]serverMetricsDTO, len(ids))
			seen := make([]bool, len(ids))
			for ev := range events {
				items[ev.idx] = ev.dto
				seen[ev.idx] = true
			}
			// 客户端中途断开 → 未采到的台补 reachable:false,别吐零值假数据。
			for i, ok := range seen {
				if !ok {
					items[i] = offlineDTO(ids[i], "采集未完成")
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
			return
		}
		writeMetricsStream(r.Context(), w, events, len(ids))
	}
}

// wantsMetricsStream 判定批量端点是否走 NDJSON 逐台流式(?stream=1)。
func wantsMetricsStream(r *http.Request) bool {
	q := r.URL.Query().Get("stream")
	return q != "" && q != "0" && !strings.EqualFold(q, "false")
}

// metricsEvent 是单台采集结果带下标的投递单元(idx 供 JSON 路径还原登记顺序)。
type metricsEvent struct {
	idx int
	dto serverMetricsDTO
}

// runMetricsPool 有界并发采集一批服务器,结果**按完成顺序**送入返回的 channel;
// 全部完成后关闭。channel 只由采集 goroutine 写,调用方读完即止;ctx 取消后 worker
// 会各自收尾(定位类错误亦落为该台 reachable:false)。
func runMetricsPool(ctx context.Context, svc target.Service, ids []string) <-chan metricsEvent {
	out := make(chan metricsEvent, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, metricsConcurrency)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
			}
			// 批量逐台独立:定位类错误对某台亦只表现为该台 reachable:false(忽略 locErr)。
			dto, _ := collectServerMetrics(ctx, svc, id)
			select {
			case out <- metricsEvent{idx: i, dto: dto}:
			case <-ctx.Done():
			}
		}(i, id)
	}
	go func() { wg.Wait(); close(out) }()
	return out
}

// writeMetricsStream 把逐台结果写成 NDJSON(一行一个 JSON 对象)。
func writeMetricsStream(ctx context.Context, w http.ResponseWriter, events <-chan metricsEvent, total int) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	// 逐台推送不能被缓存;X-Accel-Buffering 关掉反代(nginx 系)的响应缓冲,否则一行也攒着不发。
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// 头一写就定了 200;流式响应没有「整包错误」可回,失败只表现为少了行。
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	written := 0
	for written < total {
		select {
		case ev, ok := <-events:
			if !ok {
				return // 采集已全部完成(或被取消)
			}
			if err := enc.Encode(ev.dto); err != nil {
				return // 客户端断开:再采下去也没人收
			}
			written++
			// flush 失败 = 这个 ResponseWriter 不支持逐块下发,退回攒着发(内容不变,只是不实时)。
			_ = rc.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// offlineDTO 是「没采到」的兜底行(客户端断开/采集未完成),与 unreachable 同形态:
// reachable:false + 人读 error,绝不返回各指标皆零值的假样本。
func offlineDTO(id, reason string) serverMetricsDTO {
	return serverMetricsDTO{
		ServerID:    id,
		Error:       reason,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
	}
}
