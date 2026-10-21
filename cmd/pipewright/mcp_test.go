// mcp_test.go 守 `pipewright mcp` 的入口形状:参数只有两个、都能用环境变量兜底,
// 且「起不来」一定要在还没读配置、没开库、没占端口之前就说不清的是哪一项 ——
// 这个子命令是给 AI 客户端拉起的,它没人看终端,只有退出码和 stderr。
package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestParseMCPFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		url  string // PIPEWRIGHT_URL,空表示不设
		tok  string // PIPEWRIGHT_TOKEN,空表示不设
		want mcpConfig
	}{
		{
			name: "两个参数都给",
			args: []string{"--url", "http://127.0.0.1:8080", "--token", "pwt_a"},
			want: mcpConfig{baseURL: "http://127.0.0.1:8080", token: "pwt_a"},
		},
		{
			name: "等号与单横线",
			args: []string{"-url=https://ci.example.com", "-token=pwt_b"},
			want: mcpConfig{baseURL: "https://ci.example.com", token: "pwt_b"},
		},
		{
			name: "环境变量兜底",
			url:  "http://from-env:8080",
			tok:  "pwt_env",
			want: mcpConfig{baseURL: "http://from-env:8080", token: "pwt_env"},
		},
		{
			name: "命令行压过环境变量",
			args: []string{"--url", "http://flag", "--token", "pwt_flag"},
			url:  "http://from-env:8080",
			tok:  "pwt_env",
			want: mcpConfig{baseURL: "http://flag", token: "pwt_flag"},
		},
		{
			name: "只给一半:令牌走环境",
			args: []string{"--url", "http://only-url"},
			tok:  " pwt_padded ",
			want: mcpConfig{baseURL: "http://only-url", token: "pwt_padded"},
		},
		{
			name: "前后空格一律收掉",
			args: []string{"--url", "  http://padded/  ", "--token", "  pwt_x  "},
			want: mcpConfig{baseURL: "http://padded/", token: "pwt_x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 不设的那个也要清成空:同一次 test 跑里别的用例可能留下过值。
			t.Setenv("PIPEWRIGHT_URL", tc.url)
			t.Setenv("PIPEWRIGHT_TOKEN", tc.tok)
			got, err := parseMCPFlags(tc.args)
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseMCPFlagsRejects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		url     string
		tok     string
		wantErr string
	}{
		{"两个都不给", nil, "", "", "要用 --url"},
		{"缺令牌", []string{"--url", "http://x"}, "", "", "要用 --token"},
		{"令牌是空白", []string{"--url", "http://x", "--token", "   "}, "", "", "要用 --token"},
		{"地址是空白", []string{"--url", "  ", "--token", "pwt_x"}, "", "", "要用 --url"},
		{"多余位置参数", []string{"--url", "http://x", "--token", "pwt_x", "start"}, "", "", "不认识的参数"},
		{"不认识的 flag", []string{"--nope", "1"}, "", "", "flag provided but not defined"},
		{"--url 缺值", []string{"--url"}, "", "", "flag needs an argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIPEWRIGHT_URL", tc.url)
			t.Setenv("PIPEWRIGHT_TOKEN", tc.tok)
			if _, err := parseMCPFlags(tc.args); err == nil {
				t.Fatal("应当报错却返回 nil")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误里应含 %q,得 %v", tc.wantErr, err)
			}
		})
	}
}

// TestRunMCPCommandExitCodes 退出码是这条线唯一的反馈渠道:用法错给 2(客户端配置写错了),
// --help 给 0(它是来问怎么写的,不是来报错的)。这里只能测到「还没起服务」的分支,
// 真起 stdio 那条留给 #375 的端到端。
func TestRunMCPCommandExitCodes(t *testing.T) {
	t.Run("缺参数", func(t *testing.T) {
		t.Setenv("PIPEWRIGHT_URL", "")
		t.Setenv("PIPEWRIGHT_TOKEN", "")
		if code := runMCPCommand(nil); code != 2 {
			t.Fatalf("配置不全应当退 2,得 %d", code)
		}
	})
	t.Run("help", func(t *testing.T) {
		if code := runMCPCommand([]string{"--help"}); code != 0 {
			t.Fatalf("--help 应当退 0,得 %d", code)
		}
	})
	t.Run("地址过不了规范化", func(t *testing.T) {
		if code := runMCPCommand([]string{"--url", "127.0.0.1:8080", "--token", "pwt_x"}); code != 2 {
			t.Fatalf("地址写错应当退 2,得 %d", code)
		}
	})
}

func TestParseMCPHelpIsErrHelp(t *testing.T) {
	t.Setenv("PIPEWRIGHT_URL", "")
	t.Setenv("PIPEWRIGHT_TOKEN", "")
	_, err := parseMCPFlags([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help 应当是 flag.ErrHelp,得 %v", err)
	}
}
