package main

import "testing"

func TestListenAddrFromArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		base string
		want string
	}{
		{"无参数沿用 base", nil, ":8080", ":8080"},
		{"空格形式 addr", []string{"--addr", ":9090"}, ":8080", ":9090"},
		{"等号形式 addr", []string{"--addr=0.0.0.0:9090"}, ":8080", "0.0.0.0:9090"},
		{"单横线(Windows 习惯)", []string{"-addr", "127.0.0.1:9090"}, ":8080", "127.0.0.1:9090"},
		{"port 只换端口保留主机", []string{"--port", "9090"}, "127.0.0.1:8080", "127.0.0.1:9090"},
		{"port 对空主机也能用", []string{"--port=9090"}, ":8080", ":9090"},
		{"addr 与 port 同时给,顺序无关", []string{"--port", "9090", "--addr", "127.0.0.1:1"}, ":8080", "127.0.0.1:9090"},
		{"base 无端口时整体当主机", []string{"--port", "9090"}, "localhost", "localhost:9090"},
		{"无关参数原样放行", []string{"--something-else", "x"}, ":8080", ":8080"},
		{"后面的参数不影响地址", []string{"run"}, ":8080", ":8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listenAddrFromArgs(tc.args, tc.base)
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListenAddrFromArgsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"addr 缺值", []string{"--addr"}},
		{"addr 后面紧跟别的参数", []string{"--addr", "--port", "9090"}},
		{"addr 少冒号", []string{"--addr", "9090"}},
		{"port 非数字", []string{"--port", "http"}},
		{"port 缺值", []string{"--port"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := listenAddrFromArgs(tc.args, ":8080"); err == nil {
				t.Fatal("应当报错却返回 nil")
			}
		})
	}
}
