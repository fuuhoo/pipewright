package config

import (
	"strings"
	"testing"
)

// 从本机 devtest 容器(:8081,pwtest 项目)里 `cat /proc/self/mountinfo` 抄来的三条关键行:
// 容器根(overlay)、配置资源默认落点所在的具名卷(/data)、按宿主同路径映射进来的工作区。
const devtestMountinfo = `424 292 0:105 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/docker/snapshots
433 424 0:38 /docker/volumes/pipewright-data/_data /data rw,noatime master:69 - btrfs /dev/vdb1 rw,ssd
438 424 0:36 /Users/cw/.pipewright-composetest/work /Users/cw/.pipewright-composetest/work rw,relatime - virtiofs mac rw`

func TestDataDirVisibility(t *testing.T) {
	cases := []struct {
		name string
		path string
		info string // 空 = 用 devtestMountinfo
		want string // 空串 = 不该告警;否则告警必须含这段文本
	}{
		{
			name: "具名卷里的数据目录要告警",
			path: "/data/data/config_profiles",
			want: "PIPEWRIGHT_DATA_DIR=/data/data/config_profiles",
		},
		{name: "卷挂载本身也要告警", path: "/data", want: "落在挂载点 /data"},
		{
			name: "容器可写层要告警",
			path: "/opt/pipewright/data",
			want: "落在挂载点 /",
		},
		{
			name: "宿主同路径的映射目录不告警",
			path: "/Users/cw/.pipewright-composetest/work/data",
		},
		{
			name: "宿主同路径挂载点自身不告警",
			path: "/Users/cw/.pipewright-composetest/work",
		},
		{
			// 读不到有效挂载信息(非 Linux / 文本异常)时宁可不说话,别误伤健康部署。
			name: "没有可用挂载信息不告警",
			path: "/data/data",
			info: "not a mountinfo line",
		},
	}
	for _, c := range cases {
		info := c.info
		if info == "" {
			info = devtestMountinfo
		}
		got := dataDirVisibility(c.path, info)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s:不该告警,实际 = %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s:告警应含 %q,实际 = %q", c.name, c.want, got)
		}
	}
}

// 告警文案得真的能照着做:给出宿主真身 + 点名要改哪个环境变量。
func TestDataDirVisibilityWarningIsActionable(t *testing.T) {
	got := dataDirVisibility("/data/data", devtestMountinfo)
	for _, want := range []string{"/docker/volumes/pipewright-data/_data", "PIPEWRIGHT_WORK_DIR", "bind source path does not exist", "docker-compose.yml"} {
		if !strings.Contains(got, want) {
			t.Errorf("告警缺 %q:%s", want, got)
		}
	}
}

// 172.17.4.75(CentOS 7,/home 独立卷)实机 `cat /proc/self/mountinfo` 抄来的两条:
// 同路径映射的工作区,和一条路径不一致的宿主 bind(/data)。前者的 root 只是源文件系统内的子路径,
// 少了宿主那层 /home 前缀 —— 早先版本据此判定「宿主看不见」,对这类最普通的 RHEL 分区布局误报。
const centosHomeMountinfo = `706 649 253:2 /base/docker-pipewright/data /data rw,relatime - xfs /dev/mapper/centos-home rw,seclabel,attr2
712 649 253:2 /base/docker-pipewright/work /home/base/docker-pipewright/work rw,relatime - xfs /dev/mapper/centos-home rw,seclabel,attr2`

func TestDataDirVisibilityCentosHome(t *testing.T) {
	cases := []struct {
		name, path, want string
	}{
		{
			name: "独立 /home 卷上的同路径映射不告警",
			path: "/home/base/docker-pipewright/work/data",
		},
		{
			name: "挂载点自身也不告警",
			path: "/home/base/docker-pipewright/work",
		},
		{
			// 宿主真身是 /home/base/docker-pipewright/data,容器里却叫 /data:daemon 按 /data 解析落空。
			name: "宿主与容器路径不一致仍要告警",
			path: "/data/config_profiles",
			want: "PIPEWRIGHT_DATA_DIR=/data/config_profiles",
		},
	}
	for _, c := range cases {
		got := dataDirVisibility(c.path, centosHomeMountinfo)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s:不该告警,实际 = %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s:告警应含 %q,实际 = %q", c.name, c.want, got)
		}
	}
}

// 尾缀对齐要按路径段,否则 /fooobar 会被当成 /foo + /bar 的同路径映射。
func TestHostSamePathSegmentBoundary(t *testing.T) {
	info := `900 649 253:2 /bar /fooobar rw,relatime - xfs /dev/mapper/vg rw`
	if got := dataDirVisibility("/fooobar/data", info); !strings.Contains(got, "PIPEWRIGHT_DATA_DIR=/fooobar/data") {
		t.Errorf("段边界不对齐时应告警,实际 = %q", got)
	}
}

func TestParseMountinfoLine(t *testing.T) {
	c, ok := parseMountinfoLine(`438 424 0:36 /host/a\040b /ctr/a\040b rw,relatime - virtiofs mac rw`)
	if !ok {
		t.Fatal("应能解出这条挂载")
	}
	if c.root != "/host/a b" || c.mountpoint != "/ctr/a b" || c.fstype != "virtiofs" {
		t.Errorf("解析结果 = %+v", c)
	}
	// "-" 之前的可选 tag 段(share/slots 等)长度不固定,不能按定长下标取后三列。
	if _, ok := parseMountinfoLine("1 0 0:1 / /mnt rw shared:1 - ext4 /dev/vda rw"); !ok {
		t.Error("带可选 tag 的行应能解析")
	}
	if _, ok := parseMountinfoLine(""); ok {
		t.Error("空行不该解析成挂载")
	}
}

func TestPathUnder(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/data/data", "/data", true},
		{"/data", "/data", true},
		{"/data/data/", "/data", true},
		{"/data-x/y", "/data", false}, // 按段比较,别把 /data-x 算进 /data
		{"/anything", "/", true},
	}
	for _, c := range cases {
		if got := pathUnder(c.path, c.dir); got != c.want {
			t.Errorf("pathUnder(%q,%q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}
