package nacos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	fakeToken = "tok-3f9a1c7e5b"
	fakeBody  = "API_URL=https://api.example\nPASS=s3cr3t-value\n"
)

// newServer 起一个按 handler 行事的假 Nacos。httptest 绑 127.0.0.1,正好走「回环放行」这条判定。
func newServer(t *testing.T, handler http.HandlerFunc) (string, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

func req(base, dataID, group string) Request {
	return Request{BaseURL: base, DataID: dataID, Group: group}
}

// 未开鉴权:一次 GET 就返回原文,参数要逐个对上(v1 的命名空间叫 tenant,写错就永远读不到)。
func TestFetchAnonymousPassesV1Params(t *testing.T) {
	var gotPath, gotQuery string
	base, hits := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Encode()
		fmt.Fprint(w, fakeBody)
	})
	text, err := New(Policy{}).Fetch(context.Background(), Request{
		BaseURL: base, DataID: "prod.env", Group: "DEFAULT_GROUP", NamespaceID: "dev",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if text != fakeBody {
		t.Errorf("正文 = %q,要服务端原样返回的那份", text)
	}
	if gotPath != "/v1/cs/configs" {
		t.Errorf("路径 = %q,要 /v1/cs/configs", gotPath)
	}
	for _, want := range []string{"dataId=prod.env", "group=DEFAULT_GROUP", "tenant=dev"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("查询串 %s 里少了 %s", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "accessToken") {
		t.Errorf("没给凭据却带了 accessToken:%s", gotQuery)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("匿名读取该只发一次请求,发了 %d 次", n)
	}
}

// 开了鉴权:先 login 换 token,再带着 accessToken 读。
func TestFetchLogsInWhenAuthGiven(t *testing.T) {
	var loginUser, loginPass, bearer string
	var paths []string
	base, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/auth/login":
			_ = r.ParseForm()
			loginUser, loginPass = r.FormValue("username"), r.FormValue("password")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"accessToken":%q,"tokenTtl":18000,"globalAdmin":false}`, fakeToken)
		case "/v1/cs/configs":
			bearer = r.URL.Query().Get("accessToken")
			fmt.Fprint(w, fakeBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	text, err := New(Policy{}).Fetch(context.Background(), Request{
		BaseURL: base, DataID: "prod.env", Group: "G1",
		Auth: &Auth{Username: "nacos", Password: "secret-pw"},
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if text != fakeBody {
		t.Errorf("正文 = %q", text)
	}
	if loginUser != "nacos" || loginPass != "secret-pw" {
		t.Errorf("登录表单 = %q/%q,用户名和密码都该按字段传", loginUser, loginPass)
	}
	if bearer != fakeToken {
		t.Errorf("读取没带上 accessToken(got %q)", bearer)
	}
	if len(paths) != 2 || paths[0] != "/v1/auth/login" {
		t.Errorf("请求顺序 = %v,要先登录再读取", paths)
	}
}

// 上下文路径(挂在网关后面或改了 contextPath)要能拼对:根地址带 /nacos 时不能拼成 //nacos/nacos。
func TestFetchHonoursContextPathInBase(t *testing.T) {
	var got string
	base, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		fmt.Fprint(w, fakeBody)
	})
	if _, err := New(Policy{}).Fetch(context.Background(), Request{
		BaseURL: base + "/nacos/", DataID: "a", Group: "G",
	}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != "/nacos/v1/cs/configs" {
		t.Errorf("路径 = %q,要 %s/v1/cs/configs(尾部斜杠不重复)", got, "/nacos")
	}
}

func TestFetchNotFound(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "config data not found")
	})
	_, err := New(Policy{}).Fetch(context.Background(), req(base, "missing.env", "DEFAULT_GROUP"))
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("错误类 = %v,要 ErrConfigNotFound", err)
	}
	if !strings.Contains(err.Error(), "上下文路径") {
		t.Errorf("404 话术要点名「地址少了 /nacos」这另一种可能:%v", err)
	}
}

// 服务端开了鉴权但节点没选凭据:403 要说清「还没选凭据」,而不是含糊的「鉴权失败」。
func TestFetchForbiddenWithoutAuth(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := New(Policy{}).Fetch(context.Background(), Request{BaseURL: base, DataID: "a", Group: "G"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("错误类 = %v", err)
	}
	if !strings.Contains(err.Error(), "还没选 Nacos 凭据") {
		t.Errorf("话术要点名没选凭据:%v", err)
	}
}

// 密码不对 → 登录就 403:不该再发第二次请求,也不该重试(重来还是错的)。
func TestFetchLoginRejectedStopsEarly(t *testing.T) {
	base, hits := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/login" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, fakeBody)
	})
	_, err := New(Policy{}).Fetch(context.Background(), Request{
		BaseURL: base, DataID: "a", Group: "G", Auth: &Auth{Username: "nacos", Password: "wrong"},
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("错误类 = %v", err)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("登录失败后不该继续发请求,共发了 %d 次", n)
	}
}

// 选了凭据却取出空用户名空密码(凭据被删干净 / 保险库解出空):要说清是凭据的问题,不去打服务端。
func TestFetchEmptyAuth(t *testing.T) {
	base, hits := newServer(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, fakeBody) })
	_, err := New(Policy{}).Fetch(context.Background(), Request{BaseURL: base, DataID: "a", Group: "G", Auth: &Auth{}})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("错误类 = %v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("空凭据不该发请求,发了 %d 次", n)
	}
}

// 服务端 5xx 是「重来一次可能不一样」的那一类:按策略重试,成功就返回。
func TestFetchRetriesTransportFailure(t *testing.T) {
	var n int32
	base, hits := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, fakeBody)
	})
	text, err := New(Policy{}).Fetch(context.Background(), req(base, "a", "G"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if text != fakeBody {
		t.Errorf("正文 = %q", text)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Errorf("默认该重试一次,共 %d 次请求", got)
	}
}

// 重试次数可关:Attempts=1 时 5xx 直接算失败。
func TestFetchAttemptsConfigurable(t *testing.T) {
	base, hits := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := New(Policy{Attempts: 1}).Fetch(context.Background(), req(base, "a", "G"))
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("错误类 = %v", err)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("Attempts=1 不该重试,发了 %d 次", n)
	}
}

// 超时是这条功能最容易踩到的(配置中心在另一张网上):话术要带主机和时长口径。
func TestFetchTimeoutNamesHostNotURL(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, fakeBody)
	})
	_, err := New(Policy{DialTimeout: 10 * time.Millisecond, RequestTimeout: 20 * time.Millisecond, Attempts: 1}).
		Fetch(context.Background(), req(base, "a", "G"))
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("错误类 = %v", err)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("话术要说是超时:%v", err)
	}
}

// 连不上(端口没人听)也不该把 URL 带出来:那上面的查询串可能有 accessToken。
func TestFetchUnreachableHost(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	_, err := New(Policy{DialTimeout: 200 * time.Millisecond, RequestTimeout: 500 * time.Millisecond, Attempts: 1}).
		Fetch(context.Background(), Request{BaseURL: url, DataID: "a", Group: "G", Auth: &Auth{Username: "u", Password: "p"}})
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("错误类 = %v", err)
	}
	if strings.Contains(err.Error(), "accessToken") || strings.Contains(err.Error(), "v1/cs") {
		t.Errorf("错误里带了 URL 细节(可能有 token):%v", err)
	}
}

// 正文超限:拿错了配置(整份大文件)时不该把几十 MB 当环境变量塞进容器。
func TestFetchRejectsOversizedBody(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Repeat("A=1\n", 4000))
	})
	_, err := New(Policy{MaxContentBytes: 1 << 10}).Fetch(context.Background(), req(base, "a", "G"))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("错误类 = %v", err)
	}
}

// 空正文是「读到了但没内容」,不算错误:交给执行侧按「一条变量都没有」判。
func TestFetchAllowsEmptyBody(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {})
	text, err := New(Policy{}).Fetch(context.Background(), req(base, "a", "G"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if text != "" {
		t.Errorf("正文 = %q", text)
	}
}

// 形状与出网收口:这三种都该在被拒在发请求之前。
func TestFetchRejectsBadTargetBeforeDialing(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"空地址", ""},
		{"没有主机名", "http:///v1"},
		{"云元数据", "http://169.254.169.254"},
		{"非 http", "grpc://127.0.0.1:8848"},
	}
	for _, c := range cases {
		_, err := New(Policy{}).Fetch(context.Background(), Request{BaseURL: c.url, DataID: "a", Group: "G"})
		if !errors.Is(err, ErrBadTarget) {
			t.Errorf("%s: 错误类 = %v,要 ErrBadTarget", c.name, err)
		}
	}
	// 缺 Data ID / 缺分组是配置错,不是服务端错。
	if _, err := New(Policy{}).Fetch(context.Background(), Request{BaseURL: "http://127.0.0.1:1", Group: "G"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("缺 Data ID: 错误类 = %v", err)
	}
	if _, err := New(Policy{}).Fetch(context.Background(), Request{BaseURL: "http://127.0.0.1:1", DataID: "a"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("缺分组: 错误类 = %v", err)
	}
}

// 重定向是 SSRF 的后门:第一跳合法、跳到链路本地就得拒。
func TestFetchRefusesRedirectToBlockedHost(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cs/configs" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := New(Policy{Attempts: 1}).Fetch(context.Background(), req(base, "a", "G"))
	if !errors.Is(err, ErrBadTarget) {
		t.Fatalf("错误类 = %v,要 ErrBadTarget(重定向被收口拒了)", err)
	}
	if !strings.Contains(err.Error(), "出网收口") {
		t.Errorf("话术要说明是被收口拒的,否则会人去查网络:%v", err)
	}
}

// 错误文本必须是人读中文:分类哨兵(nacos: xxx)不能出现在给人看的那句里。
func TestErrorsAreHumanReadableOnly(t *testing.T) {
	base, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	_, err := New(Policy{Attempts: 1}).Fetch(context.Background(), req(base, "a", "G"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "nacos:") {
		t.Errorf("错误里混进了英文标识:%v", err)
	}
}
