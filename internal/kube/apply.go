// apply.go 是「把一份清单交到集群手里」这条腿:多文档拆分、类型白名单、server-side apply。
//
// 与 SetImage 那条腿的分工很清楚:换镜像是**改一个字段**(其余部分一概不碰),应用清单是
// **声明整个对象**。所以这里用 SSA 而不是自己拼三向合并 —— 我们不该猜用户手工改过的字段
// 该怎么合,把「以这份声明为准」交给集群判断才是这条链路的本意。
//
// 三道闸全在发请求之前落(宁可现在多说两句,也不要把一句 422 推给三个月后的人):
//   - 类型白名单:认得的 kind 才知道复数资源名;猜 plural 会把补丁打到别人的资源上。
//     Secret 单独点名拒:清单正文要入库(回滚靠它),收 Secret 等于把集群机密抄进我们的库。
//   - 名字/命名空间走与负载同一套 DNS 校验:不让任何输入改变请求指向。
//   - 渲染后仍残留的 {{占位符}}:那是「变量没给全」。未渲染的 {{X}} 在 YAML 里会被解析成
//     一个嵌套映射(不报错!),所以这一条必须在**解析之后、发请求之前**扫原文。
package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// 领域错误(错误体不含凭据)。
var (
	// ErrManifestInvalid 表示清单本身不能用:解析失败、缺字段、类型不在白名单、名字非法。
	ErrManifestInvalid = errors.New("kube: manifest is not applicable")
	// ErrNamespaceMissing 表示按该命名空间寻不到资源路径 —— 实践中只有一种成因:命名空间还没建。
	ErrNamespaceMissing = errors.New("kube: namespace not found")
)

// FieldManager 是我们在 SSA 里的身份名。**固定值**:每次换一个就等于每次都从别人手里
// 抢字段属主,集群里的历史字段会永远赖在对象上清不掉。
const FieldManager = "pipewright"

// rePlaceholder 与构建层的渲染器同一套占位符语法(见 build.renderTemplate)。
var rePlaceholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Manifest 是一条通过校验、可直接应用的资源声明。
type Manifest struct {
	DocNum     int // 第几份文档(1 基;多文档清单报错时要说清是哪一份)
	Kind       string
	APIVersion string
	Namespace  string
	Name       string
	Body       string // 规范化 YAML:应用它、也把同一份正文入库(回滚 = 重新应用上一份)
	plural     string
}

// String 是人读定位串。
func (m Manifest) String() string { return fmt.Sprintf("%s %s/%s", m.Kind, m.Namespace, m.Name) }

// path 返回该资源的 API server 路径(调用前须已过 ParseManifests)。
func (m Manifest) path() string { return resourcePath(m.APIVersion, m.plural, m.Namespace, m.Name) }

// AsWorkload 转成负载定位以便等滚动;非负载类型返回 false(Service/ConfigMap 应用成功即终态)。
func (m Manifest) AsWorkload() (Workload, bool) {
	if !workloadKinds[m.Kind] {
		return Workload{}, false
	}
	return Workload{Kind: m.Kind, Namespace: m.Namespace, Name: m.Name}, true
}

// ParseManifests 把多文档 YAML 拆成可应用清单。
//
// defaultNS 是文档没写 metadata.namespace 时补上的命名空间(来自集群登记)。两处都空即报错:
// 「静默发到 default」是这条链路最阴的失败 —— 清单没问题、请求没问题,发到了一个没人看的命名空间里。
//
// 正文按**节点树**重新序列化(不重新按 map 编排):键的书写顺序与注释都留着,而
// kubectl 导出件里那些服务器自己写的字段(status / resourceVersion / uid...)会被剔掉 ——
// 它们留着只会让下一次 apply 撞 409,而用户无从下手。
func ParseManifests(text, defaultNS string) ([]Manifest, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var out []Manifest
	for docNum := 1; ; docNum++ {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w:第 %d 份文档解析失败(%v)%s",
				ErrManifestInvalid, docNum, trimYAMLErr(err), placeholderHint(text))
		}
		root := mappingOf(&doc)
		if root == nil {
			continue // 空文档、只有注释、只有 `---`:不算一份资源,也不报错
		}
		m, err := manifestFromNode(root, docNum, defaultNS)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w:文本里没有一份可用的资源(空内容、只有注释都不算)", ErrManifestInvalid)
	}
	return out, nil
}

// manifestFromNode 校验一份文档并产出清单(root 必须是映射节点)。
func manifestFromNode(root *yaml.Node, docNum int, defaultNS string) (Manifest, error) {
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	if err := root.Decode(&head); err != nil {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档不是一个资源对象(%v)", ErrManifestInvalid, docNum, trimYAMLErr(err))
	}
	kind := strings.TrimSpace(head.Kind)
	if kind == "" {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档缺 kind", ErrManifestInvalid, docNum)
	}
	// Secret 必须点名拒(而不是混在「类型不支持」里):这一条的理由是数据外流,不是功能缺失。
	// 清单正文会入库,收 Secret 就是把集群机密抄一份进我们的库,而且它还会被回滚逻辑再读出来。
	if kind == "Secret" {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档是 Secret —— 清单正文要入库(回滚靠它),不能把集群机密抄进来。请把凭据交给「凭据」页,清单里只写引用(SecretProviderClass / 外部注入)或改用已有的 Secret 名", ErrManifestInvalid, docNum)
	}
	info, ok := resources[kind]
	if !ok {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档的类型 %q 不在清单白名单内(支持 %s)",
			ErrManifestInvalid, docNum, kind, strings.Join(ManifestKinds(), " / "))
	}
	if strings.TrimSpace(head.APIVersion) == "" {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档缺 apiVersion", ErrManifestInvalid, docNum)
	}
	if strings.TrimSpace(head.APIVersion) != info.apiVersion {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档 %s 的 apiVersion 应为 %s,写的是 %s",
			ErrManifestInvalid, docNum, kind, info.apiVersion, strings.TrimSpace(head.APIVersion))
	}
	name := strings.TrimSpace(head.Metadata.Name)
	if err := checkDNSName("资源名", name); err != nil {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档 %s", ErrManifestInvalid, docNum, reason(err))
	}
	ns := strings.TrimSpace(head.Metadata.Namespace)
	if ns == "" {
		ns = strings.TrimSpace(defaultNS) // 集群登记的默认命名空间在这里落地
	}
	if ns == "" {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档没写 metadata.namespace,该集群也没登记默认命名空间 —— 拒绝猜测(静默发到 default 比失败更难查)", ErrManifestInvalid, docNum)
	}
	if err := checkDNSName("命名空间", ns); err != nil {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档 %s", ErrManifestInvalid, docNum, reason(err))
	}

	pruneServerFields(root)
	body, err := yaml.Marshal(root)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档序列化失败(%v)", ErrManifestInvalid, docNum, errKind(err))
	}
	if leftover := placeholderNames(string(body)); leftover != "" {
		return Manifest{}, fmt.Errorf("%w:第 %d 份文档里有未被替换的变量 %s —— 请在「变量」里补上,或检查拼写", ErrManifestInvalid, docNum, leftover)
	}
	return Manifest{
		DocNum: docNum, Kind: kind, APIVersion: info.apiVersion,
		Namespace: ns, Name: name, Body: string(body), plural: info.plural,
	}, nil
}

// placeholderNames 返回文本里残留的占位符名(去重、保持出现顺序),形如 `{{ENV}}`。
// 它同时用于「渲染后仍留着变量」的拒绝判定与语法错的可读提示 —— 两处的事实是同一件事。
func placeholderNames(text string) string {
	var names []string
	for _, m := range rePlaceholder.FindAllStringSubmatch(text, -1) {
		seen := false
		for _, s := range names {
			if s == m[1] {
				seen = true
				break
			}
		}
		if !seen {
			names = append(names, "{{"+m[1]+"}}")
		}
	}
	return strings.Join(names, " / ")
}

// placeholderHint 是附在 YAML 语法错后面的那一句:清单里若还留着 {{变量}},那格式坏掉
// 十有八九就是它 —— 未加引号的 `{{X}}` 写在流式 `{...}` 里会被当成嵌套映射,YAML 直接拒。
// 只说「第 2 行缺个 ,」没人能反推到「哦,是那个变量没给全」。
func placeholderHint(text string) string {
	if names := placeholderNames(text); names != "" {
		return ";另外:清单里还有未被替换的变量 " + names + ",先补全它再看格式"
	}
	return ""
}

// reason 只取错误说了什么(剥掉包前缀):它要嵌进一句已经带前缀的话里,不能再来一遍。
func reason(err error) string {
	return strings.TrimPrefix(strings.TrimSpace(err.Error()), "kube: ")
}

// Apply 用 server-side apply 提交一份清单(create-on-absent)。
//
// 返回 created=true 表示集群里原本没有这个对象(201)。fieldManager 固定,force=true:
// 这条链路的立场是「以这份清单为准」,与 kubectl apply --force-conflicts 同一取向 ——
// 字段属主撞上别人(手工 kubectl 改过一次)时,流水线该赢,而不是停在 409 等人来点。
func (c *Client) Apply(ctx context.Context, m Manifest) (bool, error) {
	if strings.TrimSpace(m.Body) == "" {
		return false, fmt.Errorf("%w:%s 正文为空", ErrManifestInvalid, m)
	}
	q := "?fieldManager=" + url.QueryEscape(FieldManager) + "&force=true"
	_, code, err := c.doCode(ctx, http.MethodPatch, m.path()+q, "application/apply-patch+yaml", []byte(m.Body))
	if err != nil {
		if errors.Is(err, ErrWorkloadNotFound) {
			// apply 撞到 404 只有两种成因,而两者都不是「资源不存在」那么轻:
			return false, fmt.Errorf("%w:%s 寻不到路径(命名空间 %q 还没建,或集群版本没有 %s 这一类资源)",
				ErrNamespaceMissing, m, m.Namespace, m.plural)
		}
		return false, err
	}
	return code == http.StatusCreated, nil
}

// ---- 节点树小工具 ----

// mappingOf 取一份文档的根映射;空文档 / 非映射返回 nil。
func mappingOf(doc *yaml.Node) *yaml.Node {
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil // 只有标量/序列的文档(`---` 空档、一行注释)在这里收敛为「不算一份资源」
	}
	return root
}

// pruneServerFields 剔掉「服务器自己写的」字段:kubectl get -o yaml 的导出件直接粘过来时,
// 这些字段会让下一次 apply 带着过期的 resourceVersion 撞 409,而报错里没有一个字提到它们。
func pruneServerFields(root *yaml.Node) {
	mapDel(root, "status")
	meta := mapGet(root, "metadata")
	for _, key := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "selfLink"} {
		mapDel(meta, key)
	}
}

// mapGet 按键取映射节点的子节点。映射节点的 Content 是 [k1,v1,k2,v2,...]。
func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// mapDel 从映射节点删一对键值(非映射节点静默忽略)。
func mapDel(n *yaml.Node, key string) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	kept := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			continue
		}
		kept = append(kept, n.Content[i], n.Content[i+1])
	}
	n.Content = kept
}
