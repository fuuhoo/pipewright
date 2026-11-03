package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/go-chi/chi/v5"
)

// 容器详情(inspect):经界面查看目标机上某容器的精选元信息(镜像/命令/状态/重启策略/
// 环境变量/挂载/网络/端口/标签),免去逐台 SSH 登录手敲 `docker inspect`。
//
// GET 只读 → 过 auth、豁免 CSRF。
//
// 安全(AC-SEC-02,与 6-3/6-4 同姿态):
//   - containerId 经严格白名单 reDockerTgt(首字符 [\w] 防 flag 注入、无 shell 元字符防命令注入),
//     >256 拒;
//   - 命令一律 array 化(`docker inspect <containerId>`),经 target.Exec 各参数 shell 转义后执行,
//     绝不拼 shell 字符串。
//   - inspect 失败 / 容器不存在 / 解析失败 → 200 + reachable:false + 人读 error,**不 500**。
//   - env 值可能含密钥:展示给已登录管理员,但**绝不记日志**(本 handler 不打印任何 inspect 输出)。
//
// 「上次退出」字段的取值时机(本地 Docker 29.4 实测,决定了前端怎么解释这些数):
//   - 容器处于 exited / restarting(退避等待下一次拉起)时,.State 完整保留死因:
//     ExitCode(137/143/…)、OOMKilled、Error、FinishedAt。
//   - 一旦被重新拉起进入 running,.ExitCode 归 0、.OOMKilled 归 false、.Error 清空 —— 死因现场被 docker 抹掉,
//     但 .FinishedAt(上次退出时刻)与顶层 .RestartCount(累计重启次数)仍然留着。
//   - 从没跑过的 created 容器:两个时间都是 0001-01-01(cleanInspectTime 折成空串),退出码 0,重启次数 0。

// containerMountDTO 是单条挂载(精选 .Mounts 字段)。
type containerMountDTO struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode"`
	RW          bool   `json:"rw"`
}

// containerNetworkDTO 是单个网络的接入信息(精选 .NetworkSettings.Networks)。
type containerNetworkDTO struct {
	Name      string `json:"name"`
	IPAddress string `json:"ipAddress"`
}

// containerPortDTO 是单条端口映射(展开 .NetworkSettings.Ports)。
// 未发布端口(映射为 null)仅含 containerPort;已发布则每个绑定一条。
type containerPortDTO struct {
	ContainerPort string `json:"containerPort"` // 如 "80/tcp"
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort,omitempty"`
}

// containerInspectDTO 是响应体(冻结契约)。reachable=false 时仅 error 有意义。
//
// 「上次退出」一组(exitCode / oomKilled / stateError / startedAt / finishedAt / restartCount)
// 是反复重启的容器唯一能拿到的死因证据 —— 容器一重启,上一世的日志就没了,但 docker 一直留着这些字段。
// exitCode 与 restartCount 不带 omitempty:0 本身就是信息(正常退出 / 从没重启过)。
type containerInspectDTO struct {
	ServerID      string `json:"serverId"`
	ContainerID   string `json:"containerId"`
	Reachable     bool   `json:"reachable"`
	Error         string `json:"error,omitempty"`
	Image         string `json:"image,omitempty"`
	Command       string `json:"command,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	State         string `json:"state,omitempty"`
	RestartPolicy string `json:"restartPolicy,omitempty"`
	ExitCode      int    `json:"exitCode"`
	OOMKilled     bool   `json:"oomKilled"`
	StateError    string `json:"stateError,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	RestartCount  int    `json:"restartCount"`
	// RestartMaxRetry 只在策略为 on-failure 时有意义(docker 只对这一种存最大次数);
	// 0 = 不限次数,所以不能用 omitempty 把「不限」和「不适用」混为一谈。
	RestartMaxRetry int                   `json:"restartMaxRetry"`
	Env             []string              `json:"env"`
	Mounts          []containerMountDTO   `json:"mounts"`
	Networks        []containerNetworkDTO `json:"networks"`
	Ports           []containerPortDTO    `json:"ports"`
	Labels          map[string]string     `json:"labels"`
}

// dockerInspectRaw 仅声明我们关心的字段,映射 `docker inspect` 数组元素;其余字段被忽略。
type dockerInspectRaw struct {
	Created      string   `json:"Created"`
	RestartCount int      `json:"RestartCount"`
	Path         string   `json:"Path"`
	Args         []string `json:"Args"`
	State        struct {
		Status     string `json:"Status"`
		ExitCode   int    `json:"ExitCode"`
		OOMKilled  bool   `json:"OOMKilled"`
		Err        string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Cmd    []string          `json:"Cmd"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	Mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

// parseContainerInspect 解析 `docker inspect` 的原始输出(JSON 数组),取首元素并映射为精选 DTO。
// 纯函数,便于单测。空数组 / 非法 JSON → 错误。
func parseContainerInspect(output string) (containerInspectDTO, error) {
	var arr []dockerInspectRaw
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &arr); err != nil {
		return containerInspectDTO{}, errors.New("无法解析 docker inspect 输出")
	}
	if len(arr) == 0 {
		return containerInspectDTO{}, errors.New("docker inspect 返回空")
	}
	return mapContainerInspect(arr[0]), nil
}

// mapContainerInspect 把原始 inspect 结构映射为精选 DTO(确定性顺序,便于前端稳定展示)。
func mapContainerInspect(raw dockerInspectRaw) containerInspectDTO {
	dto := containerInspectDTO{
		Reachable:     true,
		Image:         raw.Config.Image,
		Command:       inspectCommand(raw),
		CreatedAt:     raw.Created,
		State:         raw.State.Status,
		RestartPolicy: raw.HostConfig.RestartPolicy.Name,
		ExitCode:      raw.State.ExitCode,
		OOMKilled:     raw.State.OOMKilled,
		StateError:    clipInspectError(raw.State.Err),
		StartedAt:     cleanInspectTime(raw.State.StartedAt),
		FinishedAt:    cleanInspectTime(raw.State.FinishedAt),
		RestartCount:  raw.RestartCount,
		Env:           []string{},
		Mounts:        []containerMountDTO{},
		Networks:      []containerNetworkDTO{},
		Ports:         []containerPortDTO{},
		Labels:        map[string]string{},
	}

	// 只有 on-failure 会用到最大重试次数;其它策略下 docker 一律回 0,照抄会被读成「重试 0 次」。
	if dto.RestartPolicy == "on-failure" {
		dto.RestartMaxRetry = raw.HostConfig.RestartPolicy.MaximumRetryCount
	}

	if len(raw.Config.Env) > 0 {
		dto.Env = append(dto.Env, raw.Config.Env...)
	}

	for _, m := range raw.Mounts {
		dto.Mounts = append(dto.Mounts, containerMountDTO{
			Source:      m.Source,
			Destination: m.Destination,
			Mode:        m.Mode,
			RW:          m.RW,
		})
	}

	// 网络:map 无序 → 按网络名排序输出,展示稳定。
	netNames := make([]string, 0, len(raw.NetworkSettings.Networks))
	for name := range raw.NetworkSettings.Networks {
		netNames = append(netNames, name)
	}
	sort.Strings(netNames)
	for _, name := range netNames {
		dto.Networks = append(dto.Networks, containerNetworkDTO{
			Name:      name,
			IPAddress: raw.NetworkSettings.Networks[name].IPAddress,
		})
	}

	// 端口:map 无序 → 按容器端口键排序;每个绑定展开一条,未发布端口保留一条占位。
	portKeys := make([]string, 0, len(raw.NetworkSettings.Ports))
	for key := range raw.NetworkSettings.Ports {
		portKeys = append(portKeys, key)
	}
	sort.Strings(portKeys)
	for _, key := range portKeys {
		bindings := raw.NetworkSettings.Ports[key]
		if len(bindings) == 0 {
			dto.Ports = append(dto.Ports, containerPortDTO{ContainerPort: key})
			continue
		}
		for _, b := range bindings {
			dto.Ports = append(dto.Ports, containerPortDTO{
				ContainerPort: key,
				HostIP:        b.HostIP,
				HostPort:      b.HostPort,
			})
		}
	}

	if len(raw.Config.Labels) > 0 {
		dto.Labels = raw.Config.Labels
	}

	return dto
}

// cleanInspectTime 把 docker 的「零时间」(0001-01-01T00:00:00Z,表示从未发生过)折成空串,
// 前端就能用「有没有值」直接判断,不必自己认那个魔法年份。
func cleanInspectTime(v string) string {
	if strings.HasPrefix(strings.TrimSpace(v), "0001-01-01") {
		return ""
	}
	return strings.TrimSpace(v)
}

// clipInspectError 收起 .State.Error:它能长到一大段堆栈,展示只需要开头。
// 按 rune 截(不是按字节),免得把中文切成半个字。
func clipInspectError(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) <= 512 {
		return string(r)
	}
	return string(r[:512]) + "…"
}

// inspectCommand 取容器命令的可读表示:优先 .Config.Cmd(空格拼接),否则回退 .Path + .Args。
func inspectCommand(raw dockerInspectRaw) string {
	if len(raw.Config.Cmd) > 0 {
		return strings.Join(raw.Config.Cmd, " ")
	}
	parts := make([]string, 0, len(raw.Args)+1)
	if raw.Path != "" {
		parts = append(parts, raw.Path)
	}
	parts = append(parts, raw.Args...)
	return strings.Join(parts, " ")
}

// makeContainerInspectHandler 返回 GET /api/servers/{id}/containers/{containerId}/inspect handler。
// GET 只读(过 auth、豁免 CSRF)。containerId 严格白名单 → 非法 400 invalid_container_target;
// 服务器/凭据不存在等定位类错误走标准映射;inspect 失败/容器不存在/解析失败 → 200 + reachable:false。
func makeContainerInspectHandler(svc target.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		containerID := chi.URLParam(r, "containerId")

		// AC-SEC-02:容器 ID 严格白名单(首字符非 `-` 防 flag 注入、无 shell 元字符),>256 拒。
		if containerID == "" || len(containerID) > 256 || !reDockerTgt.MatchString(containerID) {
			writeError(w, http.StatusBadRequest, "invalid_container_target",
				"非法容器 ID(仅允许字母数字与 . _ -,且不得以 - 开头)")
			return
		}

		// 先确认服务器存在(404/422/503 在写任何 200 体之前)。
		if _, err := svc.Get(r.Context(), id); err != nil {
			writeServerError(w, err)
			return
		}

		out := containerInspectDTO{
			ServerID:    id,
			ContainerID: containerID,
			Env:         []string{},
			Mounts:      []containerMountDTO{},
			Networks:    []containerNetworkDTO{},
			Ports:       []containerPortDTO{},
			Labels:      map[string]string{},
		}

		// 命令 array 化(绝不拼 shell):docker inspect <containerId>。
		res, err := svc.Exec(r.Context(), id, []string{"docker", "inspect", containerID})
		if err != nil {
			// 凭据/保险库等定位类错误 → 标准映射;连接/认证/执行类 → 200 + reachable:false。
			if errors.Is(err, target.ErrNotFound) ||
				errors.Is(err, target.ErrCredentialNotFound) ||
				errors.Is(err, target.ErrVaultUnconfigured) {
				writeServerError(w, err)
				return
			}
			out.Reachable = false
			out.Error = humanServiceError(err)
			writeJSON(w, http.StatusOK, out)
			return
		}

		// 退出码非零(容器不存在 / docker 未装)→ reachable:false + stderr 人读,不 500。
		if res.ExitCode != 0 {
			msg := strings.TrimSpace(res.Stderr)
			if msg == "" {
				msg = strings.TrimSpace(res.Stdout)
			}
			if msg == "" {
				msg = "容器不存在或 docker inspect 以非零状态退出"
			}
			out.Reachable = false
			out.Error = truncateLog(msg, 1024)
			writeJSON(w, http.StatusOK, out)
			return
		}

		parsed, perr := parseContainerInspect(res.Stdout)
		if perr != nil {
			// 解析失败(输出非预期)→ reachable:false,绝不回显原始输出(可能含密钥)。
			out.Reachable = false
			out.Error = "解析容器信息失败:" + perr.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}

		parsed.ServerID = id
		parsed.ContainerID = containerID
		writeJSON(w, http.StatusOK, parsed)
	}
}
