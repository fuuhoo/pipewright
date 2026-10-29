package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// TestCaddyImageSetting_Precedence 验证生效优先级:显式配置 > 环境变量 > 内置默认。
func TestCaddyImageSetting_Precedence(t *testing.T) {
	ctx := context.Background()
	st := storetest.OpenDB(t)
	svc := &service{store: NewStore(st)}

	t.Setenv(caddyImageEnv, "env.local/caddy:env")

	got, err := svc.CaddyImage(ctx)
	if err != nil {
		t.Fatalf("initial get: %v", err)
	}
	if got.Image != "" {
		t.Fatalf("初始应无显式配置, got %q", got.Image)
	}
	if got.Effective != "env.local/caddy:env" {
		t.Fatalf("未配置时应回落环境变量, got %q", got.Effective)
	}
	if got.DefaultImage != defaultCaddyImage {
		t.Fatalf("DefaultImage = %q, want %q", got.DefaultImage, defaultCaddyImage)
	}

	saved, err := svc.SetCaddyImage(ctx, "  registry.local/caddy:1  ")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if saved.Image != "registry.local/caddy:1" || saved.Effective != "registry.local/caddy:1" {
		t.Fatalf("显式配置应优先于环境变量: %+v", saved)
	}

	// 清除显式配置:回落到环境变量。
	cleared, err := svc.SetCaddyImage(ctx, "")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Image != "" || cleared.Effective != "env.local/caddy:env" {
		t.Fatalf("清除后应回落环境变量: %+v", cleared)
	}
}

// TestSetCaddyImage_RejectsInjection 非法字符必须拒绝:它会原样进 docker run 参数。
func TestSetCaddyImage_RejectsInjection(t *testing.T) {
	svc := &service{store: NewStore(storetest.OpenDB(t))}
	for _, bad := range []string{"-v /:/host", "caddy; rm -rf /", "caddy image", "$(whoami)"} {
		if _, err := svc.SetCaddyImage(context.Background(), bad); !errors.Is(err, ErrInvalidCaddyImage) {
			t.Fatalf("image %q: err = %v, want ErrInvalidCaddyImage", bad, err)
		}
	}
}

// TestResolveCaddyImage 空串/非法值一律回落到环境变量或内置默认,绝不把非法值带进 docker run。
func TestResolveCaddyImage(t *testing.T) {
	t.Setenv(caddyImageEnv, "")
	if got := resolveCaddyImage("registry.local/caddy:1"); got != "registry.local/caddy:1" {
		t.Fatalf("legal image = %q", got)
	}
	if got := resolveCaddyImage("bad image"); got != defaultCaddyImage {
		t.Fatalf("illegal image 应回落默认, got %q", got)
	}
	if got := resolveCaddyImage(""); got != defaultCaddyImage {
		t.Fatalf("empty image 应回落默认, got %q", got)
	}
	t.Setenv(caddyImageEnv, "env.local/caddy:env")
	if got := resolveCaddyImage(""); got != "env.local/caddy:env" {
		t.Fatalf("empty image 应回落环境变量, got %q", got)
	}
}
