package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 一段贴近真实 `docker inspect <name>` 的输出片段(JSON 数组,仅保留我们关心的字段 + 少量噪声字段)。
const sampleInspectJSON = `[
  {
    "Id": "9f1e2c3d4b5a6789abcdef0123456789abcdef0123456789abcdef0123456789",
    "Created": "2026-06-01T08:30:00.123456789Z",
    "Path": "docker-entrypoint.sh",
    "Args": ["nginx", "-g", "daemon off;"],
    "State": {
      "Status": "running",
      "Running": true,
      "Pid": 12345
    },
    "Config": {
      "Image": "nginx:1.27-alpine",
      "Cmd": ["nginx", "-g", "daemon off;"],
      "Env": [
        "PATH=/usr/local/sbin:/usr/local/bin",
        "NGINX_VERSION=1.27.0",
        "DB_PASSWORD=s3cr3t"
      ],
      "Labels": {
        "com.example.team": "platform",
        "maintainer": "ops@example.com"
      }
    },
    "HostConfig": {
      "RestartPolicy": {
        "Name": "unless-stopped",
        "MaximumRetryCount": 0
      }
    },
    "Mounts": [
      {
        "Type": "bind",
        "Source": "/data/nginx/conf",
        "Destination": "/etc/nginx/conf.d",
        "Mode": "ro",
        "RW": false
      },
      {
        "Type": "volume",
        "Name": "nginx-logs",
        "Source": "/var/lib/docker/volumes/nginx-logs/_data",
        "Destination": "/var/log/nginx",
        "Mode": "z",
        "RW": true
      }
    ],
    "NetworkSettings": {
      "Networks": {
        "bridge": {
          "IPAddress": "172.17.0.2",
          "Gateway": "172.17.0.1"
        },
        "app-net": {
          "IPAddress": "10.5.0.7"
        }
      },
      "Ports": {
        "80/tcp": [
          {"HostIp": "0.0.0.0", "HostPort": "8080"}
        ],
        "443/tcp": null
      }
    }
  }
]`

func TestParseContainerInspect_SelectedFields(t *testing.T) {
	dto, err := parseContainerInspect(sampleInspectJSON)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}

	if !dto.Reachable {
		t.Errorf("Reachable = false, 期望 true")
	}
	if dto.Image != "nginx:1.27-alpine" {
		t.Errorf("Image = %q, 期望 nginx:1.27-alpine", dto.Image)
	}
	if dto.Command != "nginx -g daemon off;" {
		t.Errorf("Command = %q, 期望 'nginx -g daemon off;'", dto.Command)
	}
	if dto.CreatedAt != "2026-06-01T08:30:00.123456789Z" {
		t.Errorf("CreatedAt = %q", dto.CreatedAt)
	}
	if dto.State != "running" {
		t.Errorf("State = %q, 期望 running", dto.State)
	}
	if dto.RestartPolicy != "unless-stopped" {
		t.Errorf("RestartPolicy = %q, 期望 unless-stopped", dto.RestartPolicy)
	}

	// 运行中且从没重启过:退出码 0、无 OOM、docker 的零时间被折成空串(前端不必认魔法年份)。
	if dto.ExitCode != 0 || dto.OOMKilled || dto.RestartCount != 0 {
		t.Errorf("上次退出字段不该有值: exit=%d oom=%v restarts=%d", dto.ExitCode, dto.OOMKilled, dto.RestartCount)
	}
	if dto.FinishedAt != "" || dto.StartedAt != "" || dto.StateError != "" {
		t.Errorf("时间/错误应为空: finished=%q started=%q err=%q", dto.FinishedAt, dto.StartedAt, dto.StateError)
	}
	if dto.RestartMaxRetry != 0 {
		t.Errorf("RestartMaxRetry = %d, 非 on-failure 策略下不该有值", dto.RestartMaxRetry)
	}

	// 环境变量原样保留(含密钥值 —— 后端不特殊处理,展示给已登录管理员)。
	if len(dto.Env) != 3 {
		t.Fatalf("Env 长度 = %d, 期望 3", len(dto.Env))
	}
	if dto.Env[2] != "DB_PASSWORD=s3cr3t" {
		t.Errorf("Env[2] = %q, 期望 DB_PASSWORD=s3cr3t", dto.Env[2])
	}

	// 挂载。
	if len(dto.Mounts) != 2 {
		t.Fatalf("Mounts 长度 = %d, 期望 2", len(dto.Mounts))
	}
	m0 := dto.Mounts[0]
	if m0.Source != "/data/nginx/conf" || m0.Destination != "/etc/nginx/conf.d" || m0.Mode != "ro" || m0.RW {
		t.Errorf("Mounts[0] = %+v, 不符合预期", m0)
	}
	if !dto.Mounts[1].RW {
		t.Errorf("Mounts[1].RW = false, 期望 true")
	}

	// 网络按名排序:app-net < bridge。
	if len(dto.Networks) != 2 {
		t.Fatalf("Networks 长度 = %d, 期望 2", len(dto.Networks))
	}
	if dto.Networks[0].Name != "app-net" || dto.Networks[0].IPAddress != "10.5.0.7" {
		t.Errorf("Networks[0] = %+v, 期望 app-net/10.5.0.7", dto.Networks[0])
	}
	if dto.Networks[1].Name != "bridge" || dto.Networks[1].IPAddress != "172.17.0.2" {
		t.Errorf("Networks[1] = %+v, 期望 bridge/172.17.0.2", dto.Networks[1])
	}

	// 端口按键排序:443/tcp(未发布,占位)< 80/tcp(发布到 0.0.0.0:8080)。
	if len(dto.Ports) != 2 {
		t.Fatalf("Ports 长度 = %d, 期望 2", len(dto.Ports))
	}
	if dto.Ports[0].ContainerPort != "443/tcp" || dto.Ports[0].HostPort != "" {
		t.Errorf("Ports[0] = %+v, 期望 443/tcp 无绑定", dto.Ports[0])
	}
	if dto.Ports[1].ContainerPort != "80/tcp" || dto.Ports[1].HostIP != "0.0.0.0" || dto.Ports[1].HostPort != "8080" {
		t.Errorf("Ports[1] = %+v, 期望 80/tcp -> 0.0.0.0:8080", dto.Ports[1])
	}

	// 标签。
	if dto.Labels["com.example.team"] != "platform" {
		t.Errorf("Labels[com.example.team] = %q, 期望 platform", dto.Labels["com.example.team"])
	}
}

// 命令回退:无 Config.Cmd 时用 Path + Args 拼接。
func TestParseContainerInspect_CommandFallback(t *testing.T) {
	raw := `[{"Path":"/bin/myapp","Args":["--port","9000"],"Config":{"Image":"busybox"},"State":{"Status":"exited"}}]`
	dto, err := parseContainerInspect(raw)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	if dto.Command != "/bin/myapp --port 9000" {
		t.Errorf("Command = %q, 期望 '/bin/myapp --port 9000'", dto.Command)
	}
	if dto.State != "exited" {
		t.Errorf("State = %q, 期望 exited", dto.State)
	}
	// 空集合应序列化为非 nil 切片/映射(前端不必判 null)。
	if dto.Env == nil || dto.Mounts == nil || dto.Networks == nil || dto.Ports == nil || dto.Labels == nil {
		t.Errorf("空集合不应为 nil: env=%v mounts=%v networks=%v ports=%v labels=%v",
			dto.Env, dto.Mounts, dto.Networks, dto.Ports, dto.Labels)
	}
}

// 空数组 / 非法 JSON → 错误(handler 据此回 reachable:false)。
func TestParseContainerInspect_Errors(t *testing.T) {
	if _, err := parseContainerInspect("[]"); err == nil {
		t.Errorf("空数组应返回错误")
	}
	if _, err := parseContainerInspect("not-json"); err == nil {
		t.Errorf("非法 JSON 应返回错误")
	}
}

// 崩溃重启的容器正在退避(restarting):这是「上次为什么死」的现场,四个字段都得原样递到前端。
func TestParseContainerInspect_LastExitEvidence(t *testing.T) {
	// 真实抓取:--memory=64m 下吃内存被杀,重启策略 always,退避期间 inspect 的形状。
	raw := `[{"RestartCount":7,
	  "State":{"Status":"restarting","Running":true,"OOMKilled":true,"ExitCode":137,
	    "Error":"oom 了","StartedAt":"2026-11-02T13:07:29.886973246Z","FinishedAt":"2026-11-02T13:07:30.103425418Z"},
	  "HostConfig":{"RestartPolicy":{"Name":"always","MaximumRetryCount":0}},
	  "Config":{"Image":"ruby:3.3-alpine","Cmd":["ruby","-e","loop{}"]}}]`
	dto, err := parseContainerInspect(raw)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	if dto.ExitCode != 137 {
		t.Errorf("ExitCode = %d, 期望 137", dto.ExitCode)
	}
	if !dto.OOMKilled {
		t.Errorf("OOMKilled = false, 期望 true")
	}
	if dto.StateError != "oom 了" {
		t.Errorf("StateError = %q, 期望 'oom 了'", dto.StateError)
	}
	if dto.FinishedAt != "2026-11-02T13:07:30.103425418Z" {
		t.Errorf("FinishedAt = %q", dto.FinishedAt)
	}
	if dto.StartedAt != "2026-11-02T13:07:29.886973246Z" {
		t.Errorf("StartedAt = %q", dto.StartedAt)
	}
	if dto.RestartCount != 7 {
		t.Errorf("RestartCount = %d, 期望 7", dto.RestartCount)
	}
}

// 被重新拉起之后 docker 会抹掉死因现场:退出码归 0、OOM 标记归 false,但上次退出时刻与累计重启次数留着。
// 前端正是靠这个组合来判断「正在跑,上次为什么死的已经查不到了」。
func TestParseContainerInspect_RunningClearsEvidence(t *testing.T) {
	raw := `[{"RestartCount":8,
	  "State":{"Status":"running","Running":true,"OOMKilled":false,"ExitCode":0,"Error":"",
	    "StartedAt":"2026-11-02T13:07:49.577385498Z","FinishedAt":"2026-11-02T13:07:36.734320485Z"}}]`
	dto, err := parseContainerInspect(raw)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	if dto.ExitCode != 0 || dto.OOMKilled || dto.StateError != "" {
		t.Errorf("运行中不该带死因: exit=%d oom=%v err=%q", dto.ExitCode, dto.OOMKilled, dto.StateError)
	}
	if dto.RestartCount != 8 {
		t.Errorf("RestartCount = %d, 期望 8(docker 不会在重启后清零它)", dto.RestartCount)
	}
	if dto.FinishedAt != "2026-11-02T13:07:36.734320485Z" {
		t.Errorf("FinishedAt = %q, 期望保留上次退出时刻", dto.FinishedAt)
	}
}

// 最大重试次数只对 on-failure 有意义:其它策略下即便原始 JSON 里有残留值也归零,
// 否则前端会把「不适用」渲染成「只重试 0 次」。
func TestParseContainerInspect_MaxRetryOnlyForOnFailure(t *testing.T) {
	onFailure := `[{"HostConfig":{"RestartPolicy":{"Name":"on-failure","MaximumRetryCount":5}},"State":{"Status":"exited","ExitCode":1}}]`
	dto, err := parseContainerInspect(onFailure)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	if dto.RestartMaxRetry != 5 {
		t.Errorf("RestartMaxRetry = %d, 期望 5", dto.RestartMaxRetry)
	}
	if dto.ExitCode != 1 {
		t.Errorf("ExitCode = %d, 期望 1", dto.ExitCode)
	}

	always := `[{"HostConfig":{"RestartPolicy":{"Name":"always","MaximumRetryCount":5}},"State":{"Status":"running"}}]`
	dto2, err := parseContainerInspect(always)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	if dto2.RestartMaxRetry != 0 {
		t.Errorf("RestartMaxRetry = %d, always 策略下期望 0(不适用)", dto2.RestartMaxRetry)
	}
}

// 从没启动过的容器:docker 用 0001-01-01 表示「没发生过」,映射后应是空串。
func TestParseContainerInspect_ZeroTimes(t *testing.T) {
	raw := `[{"State":{"Status":"created","StartedAt":"0001-01-01T00:00:00Z","FinishedAt":"0001-01-01T00:00:00Z"}}]`
	dto, err := parseContainerInspect(raw)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	if dto.StartedAt != "" || dto.FinishedAt != "" {
		t.Errorf("零时间应折成空串: started=%q finished=%q", dto.StartedAt, dto.FinishedAt)
	}
}

// .State.Error 可能是一大段堆栈:截断只为展示,且按字切,不能把中文切成半个。
func TestParseContainerInspect_StateErrorClipped(t *testing.T) {
	long := strings.Repeat("崩", 600)
	raw := `[{"State":{"Status":"exited","ExitCode":2,"Error":"` + long + `"}}]`
	dto, err := parseContainerInspect(raw)
	if err != nil {
		t.Fatalf("parseContainerInspect 失败: %v", err)
	}
	r := []rune(dto.StateError)
	if len(r) != 513 {
		t.Fatalf("StateError 长度 = %d, 期望 512 字 + 省略号", len(r))
	}
	if string(r[:512]) != strings.Repeat("崩", 512) {
		t.Errorf("前 512 字不该被改动")
	}
	if r[512] != '…' {
		t.Errorf("结尾应是省略号, 实为 %q", string(r[512]))
	}
	if !utf8.ValidString(dto.StateError) {
		t.Errorf("截断后出现非法 UTF-8")
	}
}
