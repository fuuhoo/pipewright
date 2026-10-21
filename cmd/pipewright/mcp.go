package main

// 子命令 `pipewright mcp`:在本机起一个 stdio MCP 服务,把平台工具接到 AI 客户端上。
//
// 为什么要有这条线:`/mcp` 要求客户端能连到平台端口,而客户端往往就在开发者笔记本上,
// 平台在内网、只有 443 一个口给跳板机。stdio 形态把方向反过来 —— 客户端拉起这个进程,
// 进程作为 HTTP 客户端出去连平台,本机不用监听任何东西。工具、权限判定、审计口径与
// `/mcp` 完全同一套(mcpsrv 里只换了往外的通道造法)。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fuuhoo/pipewright/internal/mcpsrv"
)

// mcpConfig 是 `pipewright mcp` 解析出来的东西:连哪儿、用哪把令牌。
type mcpConfig struct {
	baseURL string
	token   string
}

// parseMCPFlags 只认两个参数,且都允许用环境变量兜底(客户端配置文件里更适合放 env:
// 命令行上的明文令牌同机其他用户能从 ps 里看到)。
func parseMCPFlags(args []string) (mcpConfig, error) {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	// stdout 是 JSON-RPC 的管道,任何用法提示与报错都不许往那儿写。
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("url", strings.TrimSpace(os.Getenv("PIPEWRIGHT_URL")),
		"平台地址,如 https://pipewright.example.com;可用 PIPEWRIGHT_URL 兜底")
	token := fs.String("token", strings.TrimSpace(os.Getenv("PIPEWRIGHT_TOKEN")),
		"API 令牌明文(pwt_ 开头),网页「设置 → API 令牌」发放;可用 PIPEWRIGHT_TOKEN 兜底")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `pipewright mcp —— 给 AI 客户端用的 stdio MCP 服务

工具面与平台上的 /mcp 是同一套:建项目、配流水线、触发构建、看日志、取消运行。
这个进程只做一件事:把工具调用转发到 --url 那端的平台,用 --token 那把令牌的身份执行。

`)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, `
客户端配置(Claude Code 的 .mcp.json / Cursor 的 mcp.json 一类):
  {"mcpServers": {"pipewright": {"command": "pipewright",
    "args": ["mcp"], "env": {"PIPEWRIGHT_URL": "https://pipewright.example.com",
                             "PIPEWRIGHT_TOKEN": "pwt_…"}}})
`)
	}
	// 缺参数、多余位置参数都在这里报错:静默起一个「什么都连不上」的服务,
	// 比启动失败更难查。
	if err := fs.Parse(args); err != nil {
		return mcpConfig{}, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		fs.Usage()
		return mcpConfig{}, fmt.Errorf("不认识的参数:%s", strings.Join(rest, " "))
	}
	cfg := mcpConfig{baseURL: strings.TrimSpace(*baseURL), token: strings.TrimSpace(*token)}
	if cfg.baseURL == "" {
		fs.Usage()
		return mcpConfig{}, errors.New("要用 --url(或 PIPEWRIGHT_URL)说清连哪个平台")
	}
	if cfg.token == "" {
		fs.Usage()
		return mcpConfig{}, errors.New("要用 --token(或 PIPEWRIGHT_TOKEN)带一把 API 令牌")
	}
	return cfg, nil
}

// runMCPCommand 跑 stdio 服务,返回进程退出码。
//
// 走这里就到此为止:不开数据库、不装配平台、不监听端口。平台那一整套启动流程对这条线
// 没有意义,而误跑一遍的后果很难看(本地库被打开、8080 被占)。
func runMCPCommand(args []string) int {
	cfg, err := parseMCPFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0 // --help 已经把用法打到 stderr,正常收工
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipewright mcp: %v\n", err)
		return 2
	}
	service, err := mcpsrv.NewRemote(cfg.baseURL, cfg.token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipewright mcp: %v\n", err)
		return 2
	}

	// Ctrl-C / 客户端 kill 都要收干净:信号一到就取消 ctx,SDK 会结束读循环。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := service.RunStdio(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "pipewright mcp 退出:%v\n", err)
		return 1
	}
	return 0
}
