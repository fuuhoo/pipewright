// Package kubetest 是一个假 Kubernetes API server:它把 kube 客户端用到的那条 HTTP 契约
// 实现出来(读版本、读对象、strategic-merge-patch、server-side apply、滚动进度),
// 供 kube 自身的测试与上层(部署链路)的测试共用。
//
// 为什么单独成一个包:kube 的 newClient 是私有的,别的包凭空造不出 *kube.Client,
// 但它又必须验证「换镜像 / 等滚动 / 应用清单」在集群那边到底发生了什么。把假服务器留在
// kube 的测试文件里,上层就只能退回接口桩 —— 而这一层的全部风险恰恰在请求长什么样
// (路径拼错、补丁用错类型、把 sidecar 的镜像换了),桩会把这些都替我「想当然」掉。
//
// 类型白名单在这里是**第二份独立陈述**(客户端另有一份):两边各写一次,客户端扩了而假
// 服务器没跟上时会 404 炸在测试里,而不是「假服务器刚好也认」把漏检掩掉。
//
// 状态语义刻意贴着真服务器:
//   - apply 一个不存在的对象 → 201,且当场视为已滚完(新建负载的 pod 全部就绪);
//   - apply / patch 改了规格 → generation +1、滚动进度归零,由后续 GET 逐步追平;
//   - 规格没变 → 不 bump。真控制器不会滚,我们也不该报告「滚动完成」。
package kubetest

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// resourceByPlural 是假服务器认得的复数资源名 → (kind, apiVersion)。
// 未知复数名一律 404:客户端凭白名单拼出的路径若这里没有,说明两侧清单失配,必须炸在测试里。
var resourceByPlural = map[string]struct{ kind, apiVersion string }{
	"deployments":  {kind: "Deployment", apiVersion: "apps/v1"},
	"statefulsets": {kind: "StatefulSet", apiVersion: "apps/v1"},
	"services":     {kind: "Service", apiVersion: "v1"},
	"configmaps":   {kind: "ConfigMap", apiVersion: "v1"},
	"ingresses":    {kind: "Ingress", apiVersion: "networking.k8s.io/v1"},
	"secrets":      {kind: "Secret", apiVersion: "v1"},
}

// kindToPlural 反查 kind → 复数名(预置对象与断言按 kind 寻址,调用方不必记复数名)。
var kindToPlural = func() map[string]string {
	m := make(map[string]string, len(resourceByPlural))
	for plural, r := range resourceByPlural {
		m[r.kind] = plural
	}
	return m
}()

// Cluster 是假 API server。零值不可用,必须经 New()。
//
// 并发:处理器跑在服务器自己的 goroutine 里,内部状态一律在 mu 下读写;测试侧只用导出方法。
type Cluster struct {
	mu sync.Mutex

	// Version 是 GET /version 返回的 gitVersion(测试连接一栏会断言它)。
	Version string

	requests []string
	lastAuth string
	patchCT  string // 最近一次 PATCH 的 Content-Type
	sawCert  bool   // 最近一次握手是否出示了客户端证书
	objects  map[string]*object
	deny     bool
	missing  bool
	// rolloutStall 是写给每次规格变更的「还要报几次未就绪」;0 = 下一次 GET 即就绪。
	rolloutStall int
}

// object 是一个已存在资源的存储:正文 + 控制器进度。
type object struct {
	apiVersion, kind, namespace, plural, name string
	doc                                       map[string]any

	generation, observedGeneration int64
	updated, ready, available      int32
	polls, stall                   int
	workload                       bool
}

// resRef 是一次请求定位到的资源(由 URL 解出)。
type resRef struct {
	apiVersion, namespace, plural, name string
}

func (r resRef) key() string { return r.apiVersion + "/" + r.namespace + "/" + r.plural + "/" + r.name }

// New 返回一个空集群(不含任何对象);用 MustApplyYAML 造场景。
func New() *Cluster {
	return &Cluster{Version: "v1.29.2", objects: map[string]*object{}}
}

// Server 启动明文 httptest 服务器,返回地址与关闭函数(与 t.Cleanup 配合)。
func (c *Cluster) Server() (string, func()) {
	srv := httptest.NewServer(c)
	return srv.URL, srv.Close
}

// TLSServer 同上,走自签 HTTPS(客户端得配 insecure-skip-tls-verify 才连得上)。
func (c *Cluster) TLSServer() (string, func()) {
	srv := httptest.NewTLSServer(c)
	return srv.URL, srv.Close
}

// RequireClientCert 启动一个**要求客户端证书**的 HTTPS 服务器(要求但不校验链:
// 假服务器没有签发权)。它存在的理由是 mTLS 接法最容易测错的那种方式:凭据解析成功、
// 客户端也建起来了,只是握手时没出示证书 —— 于是真集群那里 401,而测试全绿。
func (c *Cluster) RequireClientCert() (string, func()) {
	srv := httptest.NewUnstartedServer(c)
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	return srv.URL, srv.Close
}

// SawClientCert 报告最近一次握手是否真收到了客户端证书。
func (c *Cluster) SawClientCert() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sawCert
}

// SetDeny 让后续所有请求返回 401(可在测试中途翻转:模拟凭据被撤)。
func (c *Cluster) SetDeny(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deny = v
}

// SetMissing 让后续资源请求返回 404(用于「API server 抖一下」这类读失败恢复测试)。
func (c *Cluster) SetMissing(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.missing = v
}

// SetRolloutStall 设定**之后每次规格变更**要轮询几次才算滚完(0 = 下一次 GET 即就绪,
// 负数 = 永不收敛)。它记在 bump 出的那次变更上,所以对「刚预置好、本来就就绪」的对象
// 没有影响 —— 要测超时,得先让负载真的滚起来。
// 名字里的「stall」说的是被测行为:控制器慢,而我们必须等 —— 等不够就是「秒过」的假绿。
func (c *Cluster) SetRolloutStall(polls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rolloutStall = polls
}

// Requests 返回已处理请求的 "METHOD uri" 副本(uri 含 query,所以看得见 fieldManager)。
func (c *Cluster) Requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
}

// CountRequests 返回以 prefix 开头的请求条数(断言「轮询了不止一次」)。
func (c *Cluster) CountRequests(prefix string) int {
	n := 0
	for _, r := range c.Requests() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// LastAuth 返回最近一次请求的 Authorization 头(验证客户端确实把凭据装配上了)。
func (c *Cluster) LastAuth() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAuth
}

// LastPatchContentType 返回最近一次 PATCH 的 Content-Type;空 = 没发过 PATCH。
// 用它断言补丁类型:apply 用成 merge-patch 在真集群上是 415,桩若照收就测不出来。
func (c *Cluster) LastPatchContentType() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.patchCT
}

// Doc 返回某资源当前存储正文的深拷贝(不存在 → ok=false)。
func (c *Cluster) Doc(kind, namespace, name string) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.lookup(kind, namespace, name)
	if !ok {
		return nil, false
	}
	return deepCopy(o.doc).(map[string]any), true
}

// ImageOf 返回某负载 pod 模板里指定容器的镜像;资源或容器不存在时返回空串。
func (c *Cluster) ImageOf(kind, namespace, name, container string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.lookup(kind, namespace, name)
	if !ok {
		return ""
	}
	for _, item := range podContainers(o.doc) {
		if str(item["name"]) == container {
			return str(item["image"])
		}
	}
	return ""
}

// Exists 报告资源是否已在集群里(apply 的 create-on-absent 到底发生了没有)。
func (c *Cluster) Exists(kind, namespace, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.lookup(kind, namespace, name)
	return ok
}

// Generation 报告资源的 metadata.generation;不存在时 ok=false。
// 用它断言「这次变更控制器看到了没有」,而不是只看我们自己的 PATCH 成没成。
func (c *Cluster) Generation(kind, namespace, name string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.lookup(kind, namespace, name)
	if !ok {
		return 0, false
	}
	return o.generation, true
}

func (c *Cluster) lookup(kind, namespace, name string) (*object, bool) {
	plural, known := kindToPlural[kind]
	if !known {
		return nil, false
	}
	o, found := c.objects[resRef{apiVersion: apiVersionOfPlural(plural), namespace: namespace, plural: plural, name: name}.key()]
	return o, found
}

// MustApplyYAML 走假服务器自己的 apply 逻辑预置一个对象(测试里用清单原文造场景,
// 比手搭结构体更贴近这条链路的真实输入)。**不记进 Requests**,所以「只发了一次请求」
// 这类断言不会被预置污染。失败即 panic。
func (c *Cluster) MustApplyYAML(doc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	parsed, err := decodeDoc(doc)
	if err != nil {
		panic(fmt.Sprintf("kubetest: 预置清单解析失败:%v", err))
	}
	ref, err := refOfDoc(parsed)
	if err != nil {
		panic(fmt.Sprintf("kubetest: 预置清单不合法:%v", err))
	}
	if _, _, err := c.applyLocked(ref, parsed); err != nil {
		panic(fmt.Sprintf("kubetest: 预置清单被拒:%v", err))
	}
}

// KubeConfig 返回一份指向 server 的 kubeconfig 文本(token 是测试假值,不是真凭据)。
// namespace 写进 context,用于验证「客户端会退回 kubeconfig 默认命名空间」那条兜底链;
// insecure=false 时不写 insecure-skip-tls-verify —— 配合 TLSServer 正是「自签证书必须连不上」
// 那条断言的输入(受信与拒信走同一份文本,只差那一行,免得两条路各测各的)。
func (c *Cluster) KubeConfig(server, token, namespace string, insecure bool) string {
	ns, skip := "", ""
	if strings.TrimSpace(namespace) != "" {
		ns = "\n    namespace: " + namespace
	}
	if insecure {
		skip = "\n    insecure-skip-tls-verify: true"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: test-ctx
clusters:
- name: test
  cluster:
    server: %s%s
users:
- name: ci
  user:
    token: %s
contexts:
- name: test-ctx
  context:
    cluster: test
    user: ci%s
`, server, skip, token, ns)
}

// ServeHTTP 实现 http.Handler:只管 /version 与已知资源路径,其余一律 404。
func (c *Cluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requests = append(c.requests, r.Method+" "+r.URL.RequestURI())
	c.lastAuth = r.Header.Get("Authorization")
	c.sawCert = r.TLS != nil && len(r.TLS.PeerCertificates) > 0

	if c.deny {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "the server has asked for the client to provide credentials")
		return
	}
	if r.URL.Path == "/version" {
		_ = json.NewEncoder(w).Encode(map[string]any{"gitVersion": c.Version, "platform": "linux/amd64"})
		return
	}
	ref, ok := parseResourcePath(r.URL.Path)
	if !ok {
		writeStatus(w, http.StatusNotFound, "NotFound", "the server could not find the requested resource")
		return
	}
	if c.missing {
		writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("%s %q not found", ref.plural, ref.name))
		return
	}

	switch r.Method {
	case http.MethodGet:
		o, found := c.objects[ref.key()]
		if !found {
			writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("%s %q not found", ref.plural, ref.name))
			return
		}
		_ = json.NewEncoder(w).Encode(o.render())
	case http.MethodPatch:
		c.handlePatch(w, r, ref)
	default:
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "unsupported method "+r.Method)
	}
}

// handlePatch 分派两种补丁:strategic-merge-patch(kubectl set image / restart)与
// apply-patch(清单)。其余一律 415 —— 用错补丁类型在真集群上就是这个码。
func (c *Cluster) handlePatch(w http.ResponseWriter, r *http.Request, ref resRef) {
	ct := r.Header.Get("Content-Type")
	c.patchCT = ct
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "failed reading body")
		return
	}
	switch {
	case ct == "application/strategic-merge-patch+json":
		patch, derr := decodeDoc(string(body))
		if derr != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body")
			return
		}
		o, found := c.objects[ref.key()]
		if !found {
			writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("%s %q not found", ref.plural, ref.name))
			return
		}
		merged := deepCopy(o.doc).(map[string]any)
		mergeMaps(merged, patch, "")
		// 规格没变就不 bump:控制器不会滚,报了「滚动完成」是假绿。
		if !reflect.DeepEqual(o.doc, merged) {
			o.doc = merged
			o.bump(c.rolloutStall)
		}
		_ = json.NewEncoder(w).Encode(o.render())
	case strings.HasPrefix(ct, "application/apply-patch+yaml"):
		parsed, derr := decodeDoc(string(body))
		if derr != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid apply body")
			return
		}
		o, code, aerr := c.applyLocked(ref, parsed)
		if aerr != nil {
			writeStatus(w, code, "Invalid", aerr.Error())
			return
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(o.render())
	default:
		writeStatus(w, http.StatusUnsupportedMediaType, "NotAcceptable", "unsupported patch type "+ct)
	}
}

// applyLocked 是一次 server-side apply:create-on-absent 返 201,规格变了才 bump,
// 规格未变原样返回 200。URL 与正文的 GVK/名字/命名空间不一致一律拒(400)——
// 「发到哪个资源」只能由 URL 说了算,否则一次路径手滑就能换掉别人的负载。
func (c *Cluster) applyLocked(ref resRef, parsed map[string]any) (*object, int, error) {
	docRef, err := refOfDoc(parsed)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if docRef.plural != ref.plural || docRef.apiVersion != ref.apiVersion {
		return nil, http.StatusBadRequest, fmt.Errorf("resource kind mismatch: URL says %s (%s), body says %s (%s)",
			ref.plural, ref.apiVersion, docRef.plural, docRef.apiVersion)
	}
	if name := docRef.name; name != "" && name != ref.name {
		return nil, http.StatusBadRequest, fmt.Errorf("metadata.name %q does not match %q on the URL", name, ref.name)
	}
	if ns := docRef.namespace; ns != "" && ns != ref.namespace {
		return nil, http.StatusBadRequest, fmt.Errorf("the namespace of the provided object does not match the namespace %q sent on the request", ref.namespace)
	}

	body := deepCopy(parsed).(map[string]any)
	delete(body, "status") // 真服务器在 apply 这些负载时忽略 status;留着它只会让断言含混。
	meta, _ := body["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		body["metadata"] = meta
	}
	meta["namespace"] = ref.namespace // 命名空间以 URL 为准
	meta["name"] = ref.name

	existing, found := c.objects[ref.key()]
	if !found {
		o := &object{
			apiVersion: ref.apiVersion, kind: kindOfPlural(ref.plural), namespace: ref.namespace,
			plural: ref.plural, name: ref.name, doc: body, workload: isWorkloadKind(kindOfPlural(ref.plural)),
		}
		c.objects[ref.key()] = o
		o.created()
		return o, http.StatusCreated, nil
	}
	changed := !reflect.DeepEqual(existing.doc, body)
	existing.doc = body
	if changed {
		existing.bump(c.rolloutStall)
	}
	return existing, http.StatusOK, nil
}

// created 是新建对象的收敛态:控制器已追平,pod 全数就绪(新建负载不会处于「滚动中」)。
func (o *object) created() {
	o.generation, o.observedGeneration = 1, 1
	want := desiredReplicas(o.doc)
	o.updated, o.ready, o.available = want, want, want
}

// bump 模拟控制器观察到新规格:generation +1、进度归零,等后续 GET 逐步追平。
func (o *object) bump(stall int) {
	o.generation++
	o.observedGeneration = o.generation
	o.updated, o.ready, o.available = 0, 0, 0
	o.polls, o.stall = 0, stall
}

// render 产出 GET/PATCH 响应体,并按真控制器的收敛形状推进滚动进度:
// 未就绪时要能连续报几次,而不是读一次就完成(那正是「秒过」的坑)。
func (o *object) render() map[string]any {
	out := deepCopy(o.doc).(map[string]any)
	meta, _ := out["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		out["metadata"] = meta
	}
	meta["generation"] = o.generation
	if !o.workload {
		return out
	}
	want := desiredReplicas(o.doc)
	if o.updated < want {
		switch {
		case o.stall < 0: // 永不收敛:超时那条断言要的就是这个
		case o.polls >= o.stall:
			o.updated, o.ready, o.available = want, want, want
		default:
			o.polls++
		}
	}
	out["status"] = map[string]any{
		"observedGeneration": o.observedGeneration,
		"replicas":           want,
		"updatedReplicas":    o.updated,
		"readyReplicas":      o.ready,
		"availableReplicas":  o.available,
	}
	return out
}

// ---- 路径与类型表 ----

func apiVersionOfPlural(plural string) string { return resourceByPlural[plural].apiVersion }
func kindOfPlural(plural string) string       { return resourceByPlural[plural].kind }
func isWorkloadKind(kind string) bool         { return kind == "Deployment" || kind == "StatefulSet" }

// parseResourcePath 解出 group/version/命名空间/复数名/名字。
// 核心组走 /api/v1/namespaces/.../...,有组资源走 /apis/<group>/<version>/namespaces/.../...;
// 其余一律不认(路径认不出就当没有这个资源,和真服务器的 404 同一折法)。
func parseResourcePath(p string) (resRef, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	var ref resRef
	switch {
	case len(segs) == 6 && segs[0] == "api" && segs[2] == "namespaces":
		ref = resRef{apiVersion: segs[1], namespace: segs[3], plural: segs[4], name: segs[5]}
	case len(segs) == 7 && segs[0] == "apis" && segs[3] == "namespaces":
		ref = resRef{apiVersion: segs[1] + "/" + segs[2], namespace: segs[4], plural: segs[5], name: segs[6]}
	default:
		return resRef{}, false
	}
	info, known := resourceByPlural[ref.plural]
	if !known || info.apiVersion != ref.apiVersion {
		return resRef{}, false
	}
	return ref, true
}

// refOfDoc 从清单正文解出定位信息(预置路径用它把 yaml 变成 ref)。
func refOfDoc(doc map[string]any) (resRef, error) {
	apiVersion := str(doc["apiVersion"])
	kind := str(doc["kind"])
	if apiVersion == "" || kind == "" {
		return resRef{}, fmt.Errorf("缺少 apiVersion 或 kind")
	}
	plural, known := kindToPlural[kind]
	if !known {
		return resRef{}, fmt.Errorf("kind %q 不在假服务器的资源表里", kind)
	}
	if want := apiVersionOfPlural(plural); want != apiVersion {
		return resRef{}, fmt.Errorf("kind %q 的 apiVersion 应是 %s,写的是 %s", kind, want, apiVersion)
	}
	meta, _ := doc["metadata"].(map[string]any)
	return resRef{apiVersion: apiVersion, namespace: str(meta["namespace"]), plural: plural, name: str(meta["name"])}, nil
}

// ---- 合并键语义(strategic merge 的最小可用子集)----

// mergeKeys 是「按哪个字段合并」的表,只列出这条链路真会用到的那几个列表。
// 容器列表必须按 name 合并:否则 {name,image} 一片就把 sidecar 整段挤掉了。
var mergeKeys = map[string]string{
	"spec.template.spec.containers":     "name",
	"spec.template.spec.initContainers": "name",
}

func mergeMaps(dst, patch map[string]any, path string) {
	for k, v := range patch {
		sub := k
		if path != "" {
			sub = path + "." + k
		}
		if key, isList := mergeKeys[sub]; isList {
			dst[k] = mergeListByKey(dst[k], v, key)
			continue
		}
		if pm, isMap := v.(map[string]any); isMap {
			dm, wasMap := dst[k].(map[string]any)
			if !wasMap {
				dm = map[string]any{}
				dst[k] = dm
			}
			mergeMaps(dm, pm, sub)
			continue
		}
		dst[k] = deepCopy(v)
	}
}

// mergeListByKey 按合并键逐项合并:同名项字段级覆盖,新名字追加,其余项原样保留。
func mergeListByKey(dst, patch any, key string) []any {
	items := func(v any) []map[string]any {
		list, _ := v.([]any)
		out := make([]map[string]any, 0, len(list))
		for _, it := range list {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	base := items(dst)
	for _, p := range items(patch) {
		name := str(p[key])
		found := false
		for i, b := range base {
			if str(b[key]) != name {
				continue
			}
			merged := deepCopy(b).(map[string]any)
			mergeMaps(merged, p, "")
			base[i] = merged
			found = true
			break
		}
		if !found {
			base = append(base, deepCopy(p).(map[string]any))
		}
	}
	out := make([]any, 0, len(base))
	for _, b := range base {
		out = append(out, b)
	}
	return out
}

// ---- 小工具 ----

func podContainers(doc map[string]any) []map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	pspec, _ := tmpl["spec"].(map[string]any)
	list, _ := pspec["containers"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// desiredReplicas 取 spec.replicas;缺省按 k8s 语义算 1,不能当 0 ——
// 当 0 处理会让「等就绪」立刻假通过。
func desiredReplicas(doc map[string]any) int32 {
	spec, _ := doc["spec"].(map[string]any)
	if spec == nil {
		return 1
	}
	v, ok := spec["replicas"]
	if !ok {
		return 1
	}
	switch n := v.(type) {
	case int:
		return int32(n)
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	default:
		return 1
	}
}

// decodeDoc 解 apply/patch 正文。apply 的正文是 YAML(JSON 是其子集),所以一路走 yaml。
func decodeDoc(s string) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("空文档")
	}
	return normalize(doc).(map[string]any), nil
}

// normalize 把 yaml 可能产出的 map[any]any 折成 map[string]any,让下游只做一种类型断言。
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			out = append(out, normalize(val))
		}
		return out
	default:
		return v
	}
}

func deepCopy(v any) any { return normalize(v) }

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
}
