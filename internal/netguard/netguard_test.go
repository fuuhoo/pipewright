package netguard

import (
	"errors"
	"strings"
	"testing"
)

// 出网收口是两处共用的通知/配置中心门:口径错一处就会「webhook 能发、Nacos 读不到」这种查半天的分裂。
func TestCheckMatrix(t *testing.T) {
	cases := []struct {
		url           string
		allowLoopback bool
		wantOK        bool
	}{
		// 恒拒:链路本地(云元数据)、未指定、组播。
		{"http://169.254.169.254/latest/meta-data/", true, false},
		{"http://169.254.1.1/", false, false},
		{"http://0.0.0.0/", true, false},
		{"http://[::]/", true, false},
		{"http://ff02::1/", true, false},
		// 只认 http/https。
		{"https://hooks.example.com/x", true, true},
		{"ftp://example.com/", true, false},
		{"grpc://127.0.0.1:8848", true, false},
		{"//127.0.0.1/x", true, false},
		// 没有主机名 / 解析不了的形状。
		{"http:///v1/cs/configs", true, false},
		{"not a url with spaces ::", true, false},
		// 私网与公网域名放行(自托管平台本就跑在内网)。
		{"http://10.0.0.5/hook", false, true},
		{"http://192.168.1.20:8080/x", false, true},
		{"http://[fd00::2]:8848/nacos", false, true},
		// 回环由调用方决定:webhook 与配置中心要放本机自测,git 克隆那条路用 internal/giturl 的更严口径。
		{"http://127.0.0.1:9000/", true, true},
		{"http://127.0.0.1:9000/", false, false},
		{"http://[::1]:8848/nacos", false, false},
	}
	for _, c := range cases {
		err := Check(c.url, c.allowLoopback)
		if (err == nil) != c.wantOK {
			t.Errorf("Check(%q, allowLoopback=%v) 得 %v,想要放行=%v", c.url, c.allowLoopback, err, c.wantOK)
			continue
		}
		if err != nil && err.Error() == "" {
			t.Errorf("Check(%q) 拒了却没给人读的原因", c.url)
		}
	}
}

// 拒址错误既要能分类(errors.Is 认 ErrBlocked),又不能带出查询串 —— 那里可能正好是 accessToken。
func TestCheckErrorIsClassifiableAndLeakless(t *testing.T) {
	err := Check("http://169.254.169.254/?accessToken=tok-3f9a1c7e5b", true)
	if err == nil {
		t.Fatal("云元数据地址该被拒")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("调用方认不出这是「收口拒的」还是「压根连不上」:%v", err)
	}
	if strings.Contains(err.Error(), "accessToken") || strings.Contains(err.Error(), "tok-") {
		t.Errorf("错误文本带出了查询串:%v", err)
	}
	// 放行的地址不能带上 ErrBlocked 的形状,也不该有错误。
	if err := Check("http://127.0.0.1:8848/nacos", true); err != nil {
		t.Errorf("回环在允许回环的用途下该放行:%v", err)
	}
}
