package buildenv

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// 默认配置(可被环境变量覆盖 — 阶段 14 main.go 装配时注入)。
const (
	DefaultCheckTimeout     = 60 * time.Second
	DefaultPullTimeoutMult  = 4
	DefaultCheckConcurrency = 10
)

// CredentialRefetch 是 checker 拉取凭据的接口(供 ManualPull 用)。
// 实现位于 internal/buildenv/integrations(阶段 9 接入 vault);接口极简,避免
// buildenv 包对 vault 包的硬依赖。
type CredentialRefetch interface {
	GetByID(ctx context.Context, id string) (username, token string, err error)
}

// Checker 负责执行构建环境镜像可用性检查。
//   - 60s 检查超时(PIPEWRIGHT_CHECK_TIMEOUT_SECONDS)
//   - 10 并发上限(PIPEWRIGHT_CHECK_CONCURRENCY)
//   - ManualPull 用单独 pullSem,避免阻塞自动检查
//   - sync.Once 防服务重启时多次触发自动检查
type Checker struct {
	repo     Repo
	credRef  CredentialRefetch // optional;为 nil 时 ManualPull 跳过登录
	bin      string            // docker / nerdctl / podman
	timeout  time.Duration
	sem      chan struct{}
	pullSem  chan struct{}
	once     sync.Once
	pullMu      sync.Mutex
	pullPending map[string]struct{} // 正在排队/拉取中的 envID,防重复触发
	checkAllMu      sync.Mutex
	checkAllRunning bool // 一键检查进行中,防重复触发(409)
}

// NewChecker 构造 Checker。
func NewChecker(repo Repo, credRef CredentialRefetch, bin string,
	concurrency int, timeout time.Duration) *Checker {
	if concurrency <= 0 {
		concurrency = DefaultCheckConcurrency
	}
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	return &Checker{
		repo:        repo,
		credRef:     credRef,
		bin:         bin,
		timeout:     timeout,
		sem:         make(chan struct{}, concurrency),
		pullSem:     make(chan struct{}, 2),
		pullPending: map[string]struct{}{},
	}
}

// CheckResult 单次检查结果。
type CheckResult struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Check 检查单个构建环境的镜像可用性(60s 上限):
//  1. 本地已有(`image inspect`)→ available;
//  2. 本地没有但 `manifest inspect` 探测到 registry 有 → pullable(可拉取);
//  3. 都没有 / registry 不可达 → unavailable。
//
// 结果直接覆盖 build_envs 当前行,不存历史;不调用 dockerLogin(避免污染 ~/.docker/config.json)。
func (c *Checker) Check(ctx context.Context, env *BuildEnv) (*CheckResult, error) {
	if err := c.acquire(ctx, c.sem); err != nil {
		return nil, err
	}
	defer c.release(c.sem)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := c.repo.UpdateCheckStatus(env.ID, StatusChecking, "", now); err != nil {
		return nil, err
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	checkedAt := time.Now().UTC().Format(time.RFC3339)
	status, errMsg := c.probeImage(checkCtx, env.Image)
	_ = c.repo.UpdateCheckStatus(env.ID, status, errMsg, checkedAt)
	return &CheckResult{Status: status, Error: errMsg}, nil
}

// probeImage 返回三态之一:available(本地有)/ pullable(registry 有,本地无)/ unavailable。
// unavailable 时 errMsg 带失败原因。
func (c *Checker) probeImage(ctx context.Context, image string) (status, errMsg string) {
	if err := exec.CommandContext(ctx, c.bin, "image", "inspect", image).Run(); err == nil {
		return StatusAvailable, ""
	}
	out, err := exec.CommandContext(ctx, c.bin, "manifest", "inspect", image).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return StatusUnavailable, truncate(fmt.Sprintf("本地无该镜像,registry 探测失败: %s", msg), 1024)
	}
	return StatusPullable, ""
}

// PullResult 单次拉取结果。
type PullResult struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Output string `json:"output"`
}

// ManualPull 异步拉取镜像:同步把状态置为 checking 并立即返回,
// docker pull 在后台 goroutine 里跑(240s 上限),前端轮询列表读最终状态。
//   - 同一 env 正在拉取时返回 ErrConflict,不重复排队
//   - 后台拉取使用脱离请求的 context,r.Context() 取消只影响排队阶段
func (c *Checker) ManualPull(ctx context.Context, envID string) (*PullResult, error) {
	env, err := c.repo.GetByID(envID)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, ErrNotFound
	}

	c.pullMu.Lock()
	if _, pending := c.pullPending[envID]; pending {
		c.pullMu.Unlock()
		return nil, ErrConflict
	}
	c.pullPending[envID] = struct{}{}
	c.pullMu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	if err := c.repo.UpdateCheckStatus(envID, StatusChecking, "", now); err != nil {
		c.clearPullPending(envID)
		return nil, err
	}

	go func() {
		defer c.clearPullPending(envID)
		if _, err := c.runPull(context.WithoutCancel(ctx), env); err != nil {
			checkedAt := time.Now().UTC().Format(time.RFC3339)
			_ = c.repo.UpdateCheckStatus(envID, StatusUnavailable, truncate(err.Error(), 4096), checkedAt)
			log.Printf("[buildenv] 拉取 %s/%s 失败:%v", env.Language, env.Version, err)
		}
	}()
	return &PullResult{Status: StatusChecking}, nil
}

func (c *Checker) clearPullPending(envID string) {
	c.pullMu.Lock()
	delete(c.pullPending, envID)
	c.pullMu.Unlock()
}

// runPull 实际执行 docker pull 并落最终状态(available/unavailable)。
// 使用独立 pullSem(默认 2 并发),避免阻塞 Check。
func (c *Checker) runPull(ctx context.Context, env *BuildEnv) (*PullResult, error) {
	if err := c.acquire(ctx, c.pullSem); err != nil {
		return nil, err
	}
	defer c.release(c.pullSem)

	if env.CredentialID != "" && c.credRef != nil {
		username, token, err := c.credRef.GetByID(ctx, env.CredentialID)
		if err != nil {
			return c.markUnavailable(ctx, env.ID, fmt.Sprintf("加载凭据失败: %v", err))
		}
		cred := &CredentialLite{Username: username, Token: token}
		if err := c.dockerLogin(ctx, env.Image, cred); err != nil {
			return c.markUnavailable(ctx, env.ID, fmt.Sprintf("登录失败: %v", err))
		}
		defer c.dockerLogout(ctx, env.Image)
	}

	pullCtx, pullCancel := context.WithTimeout(ctx, c.timeout*DefaultPullTimeoutMult)
	defer pullCancel()

	cmd := exec.CommandContext(pullCtx, c.bin, "pull", env.Image)
	output, err := cmd.CombinedOutput()
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		msg := truncate(string(output), 4096)
		_ = c.repo.UpdateCheckStatus(env.ID, StatusUnavailable, msg, checkedAt)
		return &PullResult{Status: StatusUnavailable, Error: msg, Output: string(output)}, nil
	}
	_ = c.repo.UpdateCheckStatus(env.ID, StatusAvailable, "", checkedAt)
	return &PullResult{Status: StatusAvailable, Output: string(output)}, nil
}

// StartAutoCheck 启动后遍历所有 build_envs,异步检查每个镜像。
// sync.Once 防服务重启时多次触发。
// ctx 生命周期必须覆盖全部检查:调用方传入长期有效的 ctx(如 Background),
// 总超时在本函数内部的 goroutine 里管理,全部检查结束才释放。
func (c *Checker) StartAutoCheck(ctx context.Context) {
	c.once.Do(func() {
		go func() {
			envs, err := c.repo.List(ListFilter{IncludeDisabled: true})
			if err != nil {
				log.Printf("[buildenv] 加载构建环境列表失败:%v", err)
				return
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			var wg sync.WaitGroup
			for _, env := range envs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := c.Check(ctx, env); err != nil {
						log.Printf("[buildenv] 检查 %s/%s 失败:%v", env.Language, env.Version, err)
					}
				}()
			}
			wg.Wait()
		}()
	})
}

// CheckAll 手动一键检查所有。同步等待全部检查完成后返回 (可用数[本地+可拉取], 总数);
// 期间用 checkAllMu 防重复触发(再次调用返回 ErrCheckAllRunning → 409)。
func (c *Checker) CheckAll(ctx context.Context) (int, int, error) {
	c.checkAllMu.Lock()
	if c.checkAllRunning {
		c.checkAllMu.Unlock()
		return 0, 0, ErrCheckAllRunning
	}
	c.checkAllRunning = true
	c.checkAllMu.Unlock()
	defer func() {
		c.checkAllMu.Lock()
		c.checkAllRunning = false
		c.checkAllMu.Unlock()
	}()

	envs, err := c.repo.List(ListFilter{IncludeDisabled: true})
	if err != nil {
		return 0, 0, err
	}
	// 浏览器断开不应中断检查(否则行会卡在 checking)。
	ctx = context.WithoutCancel(ctx)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		available int
	)
	for _, env := range envs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.Check(ctx, env)
			mu.Lock()
			if err == nil && res != nil && (res.Status == StatusAvailable || res.Status == StatusPullable) {
				available++
			} else if err != nil {
				log.Printf("[buildenv] CheckAll %s/%s 失败:%v", env.Language, env.Version, err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return available, len(envs), nil
}

// CredentialLite 是 checker 内部的轻量凭据载体(只用于 ManualPull 的 docker login)。
type CredentialLite struct {
	Username string
	Token    string
}

// dockerLogin / dockerLogout 是私有辅助;registry URL 从 image 字符串粗解析(取首个 / 之前的 host:port)。
// 不处理全部边缘(私库如 mirror 域名),只在出现 [host] 时尝试 login。
func (c *Checker) dockerLogin(ctx context.Context, image string, cred *CredentialLite) error {
	host := parseRegistryHost(image)
	if host == "" {
		return nil // Docker Hub 公开镜像无需登录
	}
	loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(loginCtx, c.bin, "login", host, "-u", cred.Username, "--password-stdin")
	cmd.Stdin = nil
	// 简化:此处凭据 token 经 stdin;真实密码不暴露于 argv。
	// 由调用方负责把 cred.Token 写入 stdin。阶段 9+ 由 main.go 装配时把 secret 写入。
	return nil // 占位,实际实现阶段 14 完成
}

func (c *Checker) dockerLogout(ctx context.Context, image string) {
	host := parseRegistryHost(image)
	if host == "" {
		return
	}
	logoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(logoutCtx, c.bin, "logout", host).Run()
}

// markUnavailable 错误短路:标记 unavailable 并返回结果。
func (c *Checker) markUnavailable(ctx context.Context, envID, msg string) (*PullResult, error) {
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	_ = c.repo.UpdateCheckStatus(envID, StatusUnavailable, truncate(msg, 1024), checkedAt)
	return &PullResult{Status: StatusUnavailable, Error: msg}, nil
}

func (c *Checker) acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Checker) release(sem chan struct{}) { <-sem }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func parseRegistryHost(image string) string {
	// 简化解析:若含 ".",取第一个 "/" 之前的部分;否则空(Docker Hub 短名)。
	idx := -1
	for i := 0; i < len(image); i++ {
		if image[i] == '/' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	host := image[:idx]
	// 必须含 "." 或 ":" 才视为 registry(否则是 Docker Hub 官方库名前缀)
	if !containsAny(host, ".") && !containsAny(host, ":") {
		return ""
	}
	return host
}

func containsAny(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}