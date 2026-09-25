// client.go 是对 Kubernetes API server 的**最小** REST 客户端。
//
// 为什么不引 client-go:它会把依赖图从十个拉到三百多个包,而本层只需要四个调用
// (读版本 / 读负载 / 打补丁 / 轮询状态)。平台也不做「任意资源的通用客户端」——
// 支持哪些负载由下面 workloadAPI 那张白名单说了算,多一个 kind 就多一份契约面。
//
// 安全约束:
//   - URL 路径里的命名空间 / 名字一律经 DNS 名正则校验后才拼进 path(AC-SEC-02 的等价物:
//     不让用户输入改变请求指向的资源)。
//   - token / 客户端证书只在进程内装配 Transport 时使用,绝不写进日志、错误体或响应。
//   - API server 返回的错误体是 k8s Status JSON(其 message 本就设计给人看),原样摘录;
//     超出长度截断。
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// 领域错误(错误体不含凭据)。
var (
	// ErrUnreachable 表示连不上 API server(DNS / 端口 / TLS 握手失败)。
	ErrUnreachable = errors.New("kube: api server unreachable")
	// ErrDenied 表示凭据被 API server 拒(401/403)。
	ErrDenied = errors.New("kube: api server denied the request")
	// ErrWorkloadNotFound 表示目标负载不存在。
	ErrWorkloadNotFound = errors.New("kube: workload not found")
	// ErrUnsupportedKind 表示负载类型不在支持清单内。
	ErrUnsupportedKind = errors.New("kube: unsupported workload kind")
	// ErrBadName 表示命名空间 / 资源名非法(不能安全地放进 URL path)。
	ErrBadName = errors.New("kube: invalid namespace or resource name")
	// ErrNoContainer 表示规格里找不到指定的容器。
	ErrNoContainer = errors.New("kube: container not found in workload spec")
)

// reDNSName 覆盖 k8s 的 DNS-subdomain 命名(Deployment / Service 等),也够用 DNS-label(Namespace)。
// 首尾必须是小写字母或数字,中间可含 `-` `.`;杜绝 `/`、`..`、`%` 等一切能改变 URL 指向的字符。
var reDNSName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// maxNameLen 是 k8s 资源名长度上限。
const maxNameLen = 253

// apiResource 是一个 kind 的 REST 定位信息(API 组/版本 + 复数资源名)。
type apiResource struct{ apiVersion, plural string }

// resources 是本客户端认得的资源类型**唯一清单**(换镜像的寻址与应用清单的寻址都从这里取)。
// plural 只能查表:猜一个复数名就可能把补丁打到另一个资源上,而 API server 不会提醒。
var resources = map[string]apiResource{
	"Deployment":  {apiVersion: "apps/v1", plural: "deployments"},
	"StatefulSet": {apiVersion: "apps/v1", plural: "statefulsets"},
	"Service":     {apiVersion: "v1", plural: "services"},
	"ConfigMap":   {apiVersion: "v1", plural: "configmaps"},
	"Ingress":     {apiVersion: "networking.k8s.io/v1", plural: "ingresses"},
}

// workloadKinds 是「能换镜像、能等滚动」的子集:它们的门控判定共用一套逻辑
// (控制器观察到新规格 → 新 pod 全就绪 → 旧 pod 已退)。
var workloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true}

// IsWorkloadKind 报告该类型是否走负载那套门控(部署链路据此决定要不要等滚动)。
func IsWorkloadKind(kind string) bool { return workloadKinds[strings.TrimSpace(kind)] }

// SupportedKinds 返回可发布的负载类型(前端下拉与保存期校验共用一份)。
func SupportedKinds() []string {
	return []string{"Deployment", "StatefulSet"}
}

// ManifestKinds 返回清单里允许出现的类型(应用清单节点的白名单,前端提示与校验共用)。
func ManifestKinds() []string {
	return []string{"Deployment", "StatefulSet", "Service", "ConfigMap", "Ingress"}
}

// resourcePath 拼 API server 路径:有 API 组的走 /apis/<group>/<version>/...,
// 核心组(apiVersion 不含 `/`,如 v1)走 /api/v1/...。调用前名字须已过校验。
func resourcePath(apiVersion, plural, namespace, name string) string {
	prefix := "/api/"
	if strings.Contains(apiVersion, "/") {
		prefix = "/apis/"
	}
	return fmt.Sprintf("%s%s/namespaces/%s/%s/%s", prefix, apiVersion, namespace, plural, name)
}

// String 是人读定位串(日志与部署结果 message 共用)。
func (w Workload) String() string {
	return fmt.Sprintf("%s %s/%s", strings.TrimSpace(w.Kind), strings.TrimSpace(w.Namespace), strings.TrimSpace(w.Name))
}

// Workload 定位一个负载。
type Workload struct {
	Kind      string
	Namespace string
	Name      string
}

// validate 拒绝空值与任何可能改变 URL 指向的名字。
func (w Workload) validate() error {
	if !workloadKinds[strings.TrimSpace(w.Kind)] {
		return fmt.Errorf("%w: %q(支持 %s)", ErrUnsupportedKind, w.Kind, strings.Join(SupportedKinds(), " / "))
	}
	if err := checkDNSName("命名空间", w.Namespace); err != nil {
		return err
	}
	return checkDNSName("负载名", w.Name)
}

// checkDNSName 是「能安全地拼进 URL path」这一件事的唯一裁判(负载与清单共用一条规则)。
func checkDNSName(label, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("%w:%s 为空", ErrBadName, label)
	}
	if len(v) > maxNameLen || !reDNSName.MatchString(v) {
		return fmt.Errorf("%w:%s %q 非法(只允许小写字母、数字、`-`、`.`,且不能以它们之外字符开头/结尾)", ErrBadName, label, v)
	}
	return nil
}

// path 返回该负载的 API server 路径(调用前须已 validate)。
func (w Workload) path() string {
	info := resources[strings.TrimSpace(w.Kind)]
	return resourcePath(info.apiVersion, info.plural, strings.TrimSpace(w.Namespace), strings.TrimSpace(w.Name))
}

// Container 是规格里一个容器的最小视图。
type Container struct {
	Name  string
	Image string
}

// State 是一次读取的结果:回滚目标(当前镜像)与门控判定所需的状态字段都在里面。
type State struct {
	Generation         int64
	ObservedGeneration int64
	SpecReplicas       int32
	Replicas           int32
	UpdatedReplicas    int32
	AvailableReplicas  int32
	ReadyReplicas      int32
	CurrentRevision    string
	UpdateRevision     string
	Containers         []Container
}

// ImageOf 取某容器当前镜像(不存在 → ErrNoContainer)。空容器名按 ResolveContainer 的规则收敛。
func (st *State) ImageOf(container string) (string, error) {
	name, err := st.ResolveContainer(container)
	if err != nil {
		return "", err
	}
	for _, c := range st.Containers {
		if c.Name == name {
			return c.Image, nil
		}
	}
	return "", fmt.Errorf("%w:%q(该负载的容器有:%s)", ErrNoContainer, name, strings.Join(st.containerNames(), ", "))
}

// ResolveContainer 把空容器名收敛为「唯一容器的那个」;多容器时要求显式指定,绝不猜
// (猜中就把 sidecar 的镜像换成了业务镜像,猜不中则发布了一件不存在的东西)。
func (st *State) ResolveContainer(want string) (string, error) {
	if w := strings.TrimSpace(want); w != "" {
		return w, nil
	}
	if len(st.Containers) == 1 {
		return st.Containers[0].Name, nil
	}
	return "", fmt.Errorf("%w:该负载有 %d 个容器(%s),请显式指定要换哪个", ErrNoContainer, len(st.Containers), strings.Join(st.containerNames(), ", "))
}

func (st *State) containerNames() []string {
	names := make([]string, 0, len(st.Containers))
	for _, c := range st.Containers {
		names = append(names, c.Name)
	}
	return names
}

// Client 是一个集群的 API server 客户端。
type Client struct {
	acc      clusterAccess
	http     *http.Client
	endpoint string
}

// Endpoint 返回 API server 地址(展示用;非敏感)。
func (c *Client) Endpoint() string { return c.endpoint }

// NamespaceHint 返回 kubeconfig context 里写的默认命名空间(可能为空)。
func (c *Client) NamespaceHint() string { return c.acc.ns }

func newClient(a clusterAccess) *Client {
	tlsCfg := &tls.Config{InsecureSkipVerify: a.insecure} // 显式跳过校验只在 kubeconfig 配了 insecure 时发生
	if !a.insecure && a.rootCAs != nil {
		tlsCfg.RootCAs = a.rootCAs
	}
	if len(a.cert.Certificate) > 0 {
		// 客户端证书(mTLS)只在握手时出示:解析成功却不装配的凭据会一路匿名访问,
		// 最后表现成一句莫名的 401,而病根在这行之外。
		tlsCfg.Certificates = []tls.Certificate{a.cert}
	}
	return &Client{
		acc:      a,
		endpoint: a.server,
		http: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:     tlsCfg,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     60 * time.Second,
			},
			Timeout: 20 * time.Second,
		},
	}
}

// do 发一个请求;status 非 2xx 时映射为领域错误(错误体摘 k8s Status.message)。
func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte) ([]byte, error) {
	out, _, err := c.doCode(ctx, method, path, contentType, body)
	return out, err
}

// doCode 同 do,但把状态码也交出来:apply 的 201(建了这个对象)与 200(原样更新)
// 是两件不同的事实,日志与「上一版从哪来」都要靠它区分。
func (c *Client) doCode(ctx context.Context, method, path, contentType string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if c.acc.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.acc.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, classifyTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%w:读取响应失败(%v)", ErrUnreachable, errKind(err))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return out, resp.StatusCode, nil
	}
	return out, resp.StatusCode, statusError(resp.StatusCode, out)
}

// classifyTransportErr 把网络层错误分成「连不上」和「超时」(后者往往是 API server 慢或被策略挡)。
func classifyTransportErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w:请求超时", ErrUnreachable)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%w:%v", ErrUnreachable, errKind(err))
}

// statusError 把非 2xx 响应折成人读错误(摘录 k8s Status 的 reason/message;绝不含请求头)。
func statusError(code int, body []byte) error {
	reason, message := "", ""
	var st struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &st) == nil {
		reason, message = st.Reason, st.Message
	}
	detail := strings.TrimSpace(message)
	if len(detail) > 300 {
		detail = detail[:300] + "…"
	}
	if reason != "" && detail != "" {
		detail = reason + ":" + detail
	} else if reason != "" {
		detail = reason
	}
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		if detail != "" {
			return fmt.Errorf("%w(%s)", ErrDenied, detail)
		}
		return ErrDenied
	case code == http.StatusNotFound:
		return ErrWorkloadNotFound
	case detail != "":
		return fmt.Errorf("kube: API server 返回 %d(%s)", code, detail)
	default:
		return fmt.Errorf("kube: API server 返回 %d", code)
	}
}

// Version 读 API server 版本(连通性探测用)。
func (c *Client) Version(ctx context.Context) (string, error) {
	body, err := c.do(ctx, http.MethodGet, "/version", "", nil)
	if err != nil {
		return "", err
	}
	var v struct {
		GitVersion string `json:"gitVersion"`
		Platform   string `json:"platform"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "已连接(版本未解析)", nil
	}
	if v.Platform != "" {
		return v.GitVersion + " / " + v.Platform, nil
	}
	return v.GitVersion, nil
}

// Get 读一个负载的当前规格与状态。
func (c *Client) Get(ctx context.Context, w Workload) (*State, error) {
	if err := w.validate(); err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodGet, w.path(), "", nil)
	if err != nil {
		return nil, err
	}
	return decodeState(body)
}

func decodeState(body []byte) (*State, error) {
	var doc struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Replicas *int32 `json:"replicas"`
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ObservedGeneration int64  `json:"observedGeneration"`
			Replicas           int32  `json:"replicas"`
			UpdatedReplicas    int32  `json:"updatedReplicas"`
			AvailableReplicas  int32  `json:"availableReplicas"`
			ReadyReplicas      int32  `json:"readyReplicas"`
			CurrentRevision    string `json:"currentRevision"`
			UpdateRevision     string `json:"updateRevision"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("kube: 负载响应解析失败(%v)", errKind(err))
	}
	st := &State{
		Generation:         doc.Metadata.Generation,
		ObservedGeneration: doc.Status.ObservedGeneration,
		Replicas:           doc.Status.Replicas,
		UpdatedReplicas:    doc.Status.UpdatedReplicas,
		AvailableReplicas:  doc.Status.AvailableReplicas,
		ReadyReplicas:      doc.Status.ReadyReplicas,
		CurrentRevision:    doc.Status.CurrentRevision,
		UpdateRevision:     doc.Status.UpdateRevision,
	}
	// spec.replicas 缺省 = 1(k8s 语义),不把它当 0 处理,否则「等就绪」会立即假通过。
	st.SpecReplicas = 1
	if doc.Spec.Replicas != nil {
		st.SpecReplicas = *doc.Spec.Replicas
	}
	for _, c := range doc.Spec.Template.Spec.Containers {
		st.Containers = append(st.Containers, Container{Name: c.Name, Image: c.Image})
	}
	return st, nil
}

// SetImage 用 strategic-merge-patch 换某个容器的镜像(等价 kubectl set image)。
// containers 以 name 为合并键,所以只提交 {name,image} 不会抹掉 env/resources 等其余字段。
// container 必须是已解析出的真实容器名(调用方经 State.ResolveContainer 得到)。
func (c *Client) SetImage(ctx context.Context, w Workload, container, image string) error {
	name := strings.TrimSpace(container)
	if name == "" {
		return fmt.Errorf("%w:未指定容器名", ErrNoContainer)
	}
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("kube: 新镜像引用为空")
	}
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]string{{"name": name, "image": image}},
				},
			},
		},
	}
	return c.patch(ctx, w, patch)
}

// Restart 给 pod 模板打一条 restartedAt 注解(等价 kubectl rollout restart)。
// 镜像引用没变时(同一 commit 重跑)控制器不会重新滚 —— 不补这一刀,一次「重新发布」
// 就会在 API server 那里什么都没发生,而我们却报告成功。
func (c *Client) Restart(ctx context.Context, w Workload) error {
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{
						"pipewright.io/restartedAt": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	return c.patch(ctx, w, patch)
}

func (c *Client) patch(ctx context.Context, w Workload, patch map[string]any) error {
	if err := w.validate(); err != nil {
		return err
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("kube: 补丁序列化失败(%v)", errKind(err))
	}
	_, err = c.do(ctx, http.MethodPatch, w.path(), "application/strategic-merge-patch+json", body)
	return err
}

// WaitRollout 轮询到滚动完成(与 kubectl rollout status 同一套判定),超时返回最后一个状态 + 未完成原因。
// interval 用于测试注入;<=0 用默认 3s。
func (c *Client) WaitRollout(ctx context.Context, w Workload, timeout, interval time.Duration) (*State, error) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last *State
	var lastErr error
	for {
		st, err := c.Get(ctx, w)
		if err != nil {
			lastErr = err
			// 读失败不立刻判死:API server 抖一下很常见,给到下一次轮询。
		} else {
			last, lastErr = st, nil
			if done, _ := rolloutDone(w.Kind, st); done {
				return st, nil
			}
		}
		if !time.Now().Add(interval).Before(deadline) {
			if lastErr != nil {
				return last, lastErr
			}
			return last, fmt.Errorf("kube: 滚动未在 %s 内完成(%s)", timeout, rolloutProgress(w.Kind, last))
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// rolloutDone 报告该负载是否已完成这一轮滚动。判定字段与 kubectl 一致:
// 控制器必须已观察到新规格,且新模板的 pod 全部就绪、旧 pod 已缩容。
func rolloutDone(kind string, st *State) (bool, string) {
	if st == nil {
		return false, "尚未读到状态"
	}
	if st.ObservedGeneration < st.Generation {
		return false, fmt.Sprintf("控制器尚未处理本次变更(observedGeneration %d < %d)", st.ObservedGeneration, st.Generation)
	}
	if st.SpecReplicas == 0 {
		return true, "0 副本" // 显式缩到 0:没有 pod 要滚,完成。
	}
	if st.UpdatedReplicas < st.SpecReplicas {
		return false, fmt.Sprintf("新版本 pod %d/%d", st.UpdatedReplicas, st.SpecReplicas)
	}
	if st.Replicas > st.SpecReplicas {
		return false, fmt.Sprintf("旧版本 pod 仍在退出(%d > %d)", st.Replicas, st.SpecReplicas)
	}
	if kind == "StatefulSet" && st.CurrentRevision != "" && st.CurrentRevision != st.UpdateRevision {
		return false, fmt.Sprintf("revision 仍在切换(%s → %s)", st.CurrentRevision, st.UpdateRevision)
	}
	if st.AvailableReplicas < st.SpecReplicas {
		return false, fmt.Sprintf("可用 pod %d/%d", st.AvailableReplicas, st.SpecReplicas)
	}
	if st.ReadyReplicas < st.SpecReplicas {
		return false, fmt.Sprintf("就绪 pod %d/%d", st.ReadyReplicas, st.SpecReplicas)
	}
	return true, fmt.Sprintf("%d/%d 就绪", st.ReadyReplicas, st.SpecReplicas)
}

// rolloutProgress 是人读进度摘要(超时消息与成功消息共用)。
func rolloutProgress(kind string, st *State) string {
	done, why := rolloutDone(kind, st)
	if why == "" {
		why = "无状态可读"
	}
	if done {
		return why
	}
	return why
}
