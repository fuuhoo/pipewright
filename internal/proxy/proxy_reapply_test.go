package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/target"
)

// mountInspectPrefix 是 ensureCaddy 读容器挂载时发的 inspect --format 前缀。
const mountInspectPrefix = "docker inspect pipewright-caddy --format {{range .Mounts}}"

// portInspectPrefix 是 ensureCaddy 读端口映射时发的 inspect --format 前缀。
const portInspectPrefix = "docker inspect pipewright-caddy --format {{range $p"

// TestReapplyMigratesLegacyContainerToVolume 验证老容器(配置目录没挂卷)在 Reapply 时被重建一次,
// 新容器带 /etc/caddy 卷,随后把库里的路由整体重推(reload)。
func TestReapplyMigratesLegacyContainerToVolume(t *testing.T) {
	ctx := context.Background()
	st := storetest.OpenDB(t)
	ft := caddyAbsentTarget()
	svc := &service{store: NewStore(st), tg: ft, prober: fakeProber{}}

	if _, err := svc.Create(ctx, CreateInput{ServerID: "srv-1", Domain: "app.example.com", UpstreamContainer: "web", UpstreamPort: 8080}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(ctx, CreateInput{
		ServerID: "srv-1", Domain: "nas.example.com", UpstreamContainer: "10.0.0.5",
		UpstreamPort: 9000, UpstreamKind: UpstreamKindAddress,
	}); err != nil {
		t.Fatalf("Create(address): %v", err)
	}

	// 容器已存在:端口 80/443 齐全,但挂载里没有 /etc/caddy(老版本形态)。
	ft.execCalls = nil
	ft.uploads = nil
	ft.uploadBytes = nil
	ft.resultFor = func(cmd []string) *target.ExecResult {
		joined := cmdJoin(cmd)
		switch {
		case strings.HasPrefix(joined, mountInspectPrefix):
			return &target.ExecResult{ExitCode: 0, Stdout: "/data=volume /config=volume "}
		case strings.HasPrefix(joined, portInspectPrefix):
			return &target.ExecResult{ExitCode: 0, Stdout: "80/tcp=80 443/tcp=443 "}
		}
		return nil
	}

	status, err := svc.Reapply(ctx, "srv-1")
	if err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	if !status.Installed {
		t.Fatalf("Reapply 后应报告容器已安装: %+v", status)
	}

	joined := joinedCalls(ft)
	mustHaveInOrder(t, joined, []string{
		"docker rm -f pipewright-caddy",         // 老容器缺配置卷 → 重建
		"docker run -d --name pipewright-caddy", // 新容器带卷
		"docker start pipewright-caddy",         // 落到运行态
		"docker network connect pipewright-proxy web",
		"docker cp /tmp/pipewright-caddyfile pipewright-caddy:/etc/caddy/Caddyfile",
		"docker exec pipewright-caddy caddy reload",
	})

	runIdx := firstIndexWithPrefix(joined, "docker run -d --name pipewright-caddy")
	if runIdx < 0 || !strings.Contains(joined[runIdx], "-v pipewright_caddy_etc:/etc/caddy") {
		t.Fatalf("新容器应挂 Caddyfile 卷:\n%s", joined[runIdx])
	}
	// address 上游不是容器,不该被 network connect。
	for _, c := range joined {
		if strings.Contains(c, "network connect") && strings.Contains(c, "10.0.0.5") {
			t.Fatalf("address 上游不应 docker network connect: %s", c)
		}
	}
	content := ft.uploadBytes["/tmp/pipewright-caddyfile"]
	for _, want := range []string{"app.example.com {", "reverse_proxy web:8080", "nas.example.com {", "reverse_proxy 10.0.0.5:9000"} {
		if !strings.Contains(content, want) {
			t.Fatalf("重新下发的 Caddyfile 缺 %q:\n%s", want, content)
		}
	}
}

// TestReapplyKeepsHealthyContainer 验证配置卷已就位、端口齐全时 Reapply 不重建容器,
// 只做 start + 接回上游 + 重推配置。
func TestReapplyKeepsHealthyContainer(t *testing.T) {
	ctx := context.Background()
	st := storetest.OpenDB(t)
	ft := caddyAbsentTarget()
	svc := &service{store: NewStore(st), tg: ft, prober: fakeProber{}}

	if _, err := svc.Create(ctx, CreateInput{ServerID: "srv-1", Domain: "app.example.com", UpstreamContainer: "web", UpstreamPort: 8080}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ft.execCalls = nil
	ft.uploadBytes = nil
	ft.resultFor = func(cmd []string) *target.ExecResult {
		joined := cmdJoin(cmd)
		switch {
		case strings.HasPrefix(joined, mountInspectPrefix):
			return &target.ExecResult{ExitCode: 0, Stdout: "/data=volume /config=volume /etc/caddy=volume "}
		case strings.HasPrefix(joined, portInspectPrefix):
			return &target.ExecResult{ExitCode: 0, Stdout: "80/tcp=80 443/tcp=443 "}
		}
		return nil
	}

	if _, err := svc.Reapply(ctx, "srv-1"); err != nil {
		t.Fatalf("Reapply: %v", err)
	}

	for _, c := range ft.execCalls {
		joined := cmdJoin(c)
		if strings.HasPrefix(joined, "docker rm ") || strings.HasPrefix(joined, "docker run ") {
			t.Fatalf("健康容器不应重建: %s", joined)
		}
	}
	joined := joinedCalls(ft)
	mustHaveInOrder(t, joined, []string{
		"docker start pipewright-caddy",
		"docker network connect pipewright-proxy web",
		"docker exec pipewright-caddy caddy reload",
	})
	if !strings.Contains(ft.uploadBytes["/tmp/pipewright-caddyfile"], "reverse_proxy web:8080") {
		t.Fatalf("应重新下发 Caddyfile: %v", ft.uploads)
	}
}

// TestReapplyEmptyServerID 验证空 serverID → ErrEmptyServerID(不触网)。
func TestReapplyEmptyServerID(t *testing.T) {
	ft := &fakeTarget{}
	svc := &service{store: NewStore(storetest.OpenDB(t)), tg: ft, prober: fakeProber{}}
	if _, err := svc.Reapply(context.Background(), "  "); err != ErrEmptyServerID {
		t.Fatalf("应返回 ErrEmptyServerID, got %v", err)
	}
	if len(ft.execCalls) != 0 {
		t.Fatalf("空 serverID 不应触网, got %v", ft.execCalls)
	}
}

// TestUpstreamContainersCollectsAllKinds 验证上游容器收集覆盖主上游 / 路径规则 / 额外上游 / TCP 透传,
// 去重保序,且跳过 address 主上游。
func TestUpstreamContainersCollectsAllKinds(t *testing.T) {
	routes := []Route{
		{
			UpstreamContainer: "web", UpstreamPort: 8080,
			Config: RouteConfig{
				PathRules:      []PathRule{{Path: "/api/*", UpstreamContainer: "api", UpstreamPort: 9000}},
				Upstreams:      []Upstream{{Container: "web2", Port: 8080}, {Container: "api", Port: 9000}},
				TCPPassthrough: &TCPConfig{ListenPort: 5432, UpstreamContainer: "db", UpstreamPort: 5432},
			},
		},
		{UpstreamContainer: "10.0.0.5", UpstreamPort: 80, Config: RouteConfig{UpstreamKind: UpstreamKindAddress}},
	}
	got := upstreamContainers(routes)
	want := []string{"web", "api", "web2", "db"}
	if len(got) != len(want) {
		t.Fatalf("上游容器列表不符: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("上游容器列表不符(顺序/去重): got %v want %v", got, want)
		}
	}
}
