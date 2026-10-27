package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/library"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/role"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// library_perm_test.go —— 复用库三组写端点「按点判定」的契约。
//
// 要钉住的回归:library.view / library.edit 以前只活在前端的 meta.requires 里,而
// accessGuardMiddleware 认不出 /api/templates 这类 URL(三类资产都不归组,数据轴没有落点),
// 于是任意登录账号都能删掉别人的模板、变量组和自定义节点。这里用两个只差一个点的角色,
// 把「写拦下 / 读放行」逐格钉死 —— 读刻意仍只要登录,与 /api/runs 同口径,也免得流水线编辑器里的
// 「我的自定义节点」对没这个点的角色静默变空。

const libPass = "lib-pass-1234"

type libPermEnv struct {
	srv    *httptest.Server
	clEdit *http.Client
	clView *http.Client
	csEdit string // libop:有 library.edit
	csView string // libview:有 library.view 但没有 edit
}

func setupLibraryPerm(t *testing.T) *libPermEnv {
	t.Helper()
	db := storetest.Open(t).DB
	userSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	loadRoleCatalog(t, db)

	ctx := context.Background()
	roleSvc := role.New(db)
	editRole, err := roleSvc.Create(ctx, role.CreateInput{Name: "可改复用库",
		Perms: []string{"project.view", "project.edit", "library.view", "library.edit"}})
	if err != nil {
		t.Fatalf("建「可改复用库」角色: %v", err)
	}
	viewRole, err := roleSvc.Create(ctx, role.CreateInput{Name: "只看复用库",
		Perms: []string{"project.view", "project.edit", "library.view"}})
	if err != nil {
		t.Fatalf("建「只看复用库」角色: %v", err)
	}
	if err := access.ReloadRoles(ctx); err != nil {
		t.Fatalf("重装载角色目录: %v", err)
	}

	hash, err := auth.HashPassword(libPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	for _, u := range []struct{ name, roleID string }{{"libop", editRole.ID}, {"libview", viewRole.ID}} {
		if _, err := userSvc.Create(users.CreateInput{Username: u.name, PasswordHash: hash, Role: u.roleID}); err != nil {
			t.Fatalf("建号 %s: %v", u.name, err)
		}
	}

	v := vault.New(db, testMasterKey())
	pipes := pipeline.New(db)
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v), WithPipelines(pipes),
		WithTemplates(library.NewTemplateService(db, pipes)),
		WithVariableGroups(library.NewVarGroupService(db, v)),
		WithCustomNodes(library.NewCustomNodeService(db)),
	))
	t.Cleanup(srv.Close)

	clEdit := newTestClient(t)
	clView := newTestClient(t)
	return &libPermEnv{
		srv: srv, clEdit: clEdit, clView: clView,
		csEdit: loginAs(t, clEdit, srv.URL, "libop", libPass),
		csView: loginAs(t, clView, srv.URL, "libview", libPass),
	}
}

// libWrites 是三类资产各自的写端点 + 最小合法请求体:建一份(POST)和整份覆盖(PUT)。
// 形状不对会拿到 422,那会把「校验拦下」冒充成「权限拦下」,所以两份体都得先确认写得成。
// PUT 体只把 description/summary 改掉(同名覆盖自己是允许的),好在被拦下时一眼看出这行动没动。
var libWrites = []struct{ name, path, body, put string }{
	{"模板", "/api/templates", `{"name":"tpl-1","description":"","stages":[` +
		`{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]},` +
		`{"name":"构建","kind":"build","jobs":[{"name":"compile","type":"build","config":{"artifactType":"jar","commands":"make"}}]}]}`,
		`{"name":"tpl-1","description":"改过","stages":[` +
			`{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]}]}`},
	{"变量组", "/api/variable-groups", `{"name":"vg-1","description":"","vars":[{"key":"K","value":"v","secret":false}]}`,
		`{"name":"vg-1","description":"改过","vars":[{"key":"K","value":"v2","secret":false}]}`},
	{"自定义节点", "/api/custom-nodes", `{"name":"cn-1","nodeType":"script","summary":"","config":{"commands":"echo hi"}}`,
		`{"name":"cn-1","nodeType":"script","summary":"改过","config":{"commands":"echo hi"}}`},
}

// TestLibraryWriteNeedsEditPoint 逐格钉死:没有 library.edit 的账号对三类资产的任何写方法都 403,
// 且错误码是功能轴的 role_denied(不是伪装成分组问题 —— 这里根本没有分组可申请);
// 有这一点的账号建得成、改得动、也删得掉。
//
// PUT 单独钉一遍有实际理由:模板的覆盖更新是这次新加的端点,而变量组/自定义节点的 PUT
// 早就存在却被漏在守卫之外过 —— 只测 POST/DELETE 会放过「漏一个方法」这类缺口。
func TestLibraryWriteNeedsEditPoint(t *testing.T) {
	env := setupLibraryPerm(t)
	for _, w := range libWrites {
		t.Run(w.name+"缺 library.edit 应 403", func(t *testing.T) {
			resp := doJSON(t, env.clView, http.MethodPost, env.srv.URL+w.path, env.csView, w.body)
			defer resp.Body.Close()
			raw := readAll(t, resp)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("POST %s = %d, want 403(%s)", w.path, resp.StatusCode, raw)
			}
			if code := errCode(decodeJSONBody(t, raw)); code != "role_denied" {
				t.Fatalf("POST %s 错误码 = %q, want role_denied", w.path, code)
			}
		})
		t.Run(w.name+"有 library.edit 应可建可改可删", func(t *testing.T) {
			resp := doJSON(t, env.clEdit, http.MethodPost, env.srv.URL+w.path, env.csEdit, w.body)
			defer resp.Body.Close()
			raw := readAll(t, resp)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("POST %s = %d, want 201(%s)", w.path, resp.StatusCode, raw)
			}
			id, _ := decodeJSONBody(t, raw)["id"].(string)
			if id == "" {
				t.Fatalf("创建响应没有 id: %s", raw)
			}
			// 无点 PUT:拦下,且那一行还是空的 description(没写进去半个字)。
			denied := doJSON(t, env.clView, http.MethodPut, env.srv.URL+w.path+"/"+id, env.csView, w.put)
			defer denied.Body.Close()
			deniedRaw := readAll(t, denied)
			if denied.StatusCode != http.StatusForbidden {
				t.Fatalf("PUT %s 无点应 403,得 %d(%s)", w.path, denied.StatusCode, deniedRaw)
			}
			if code := errCode(decodeJSONBody(t, deniedRaw)); code != "role_denied" {
				t.Fatalf("PUT %s 错误码 = %q, want role_denied", w.path, code)
			}
			status, _, body := getJSON(t, env.clEdit, env.srv.URL+w.path+"/"+id, env.csEdit)
			if status != http.StatusOK || strings.Contains(body, "改过") {
				t.Fatalf("被 403 拦下的 PUT 仍改了这行: %d %s", status, body)
			}
			// 有点 PUT:整份覆盖成功。
			own := doJSON(t, env.clEdit, http.MethodPut, env.srv.URL+w.path+"/"+id, env.csEdit, w.put)
			defer own.Body.Close()
			ownRaw := readAll(t, own)
			if own.StatusCode != http.StatusOK {
				t.Fatalf("PUT %s = %d, want 200(%s)", w.path, own.StatusCode, ownRaw)
			}
			if !strings.Contains(ownRaw, "改过") {
				t.Fatalf("PUT %s 响应没体现覆盖后的内容: %s", w.path, ownRaw)
			}
			del := doJSON(t, env.clView, http.MethodDelete, env.srv.URL+w.path+"/"+id, env.csView, "")
			defer del.Body.Close()
			if del.StatusCode != http.StatusForbidden {
				t.Fatalf("DELETE %s 无点应 403,得 %d(%s)", w.path, del.StatusCode, readAll(t, del))
			}
			drop := doJSON(t, env.clEdit, http.MethodDelete, env.srv.URL+w.path+"/"+id, env.csEdit, "")
			defer drop.Body.Close()
			if drop.StatusCode != http.StatusNoContent {
				t.Fatalf("DELETE %s = %d, want 204(%s)", w.path, drop.StatusCode, readAll(t, drop))
			}
		})
	}
}

// TestLibraryWriteDeniedLeavesNoRow 钉住「判点排在落库之前」:被拦下的那次创建不该留下半个模板。
func TestLibraryWriteDeniedLeavesNoRow(t *testing.T) {
	env := setupLibraryPerm(t)
	resp := doJSON(t, env.clView, http.MethodPost, env.srv.URL+"/api/templates", env.csView, libWrites[0].body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403(%s)", resp.StatusCode, readAll(t, resp))
	}
	status, dto, raw := getJSON(t, env.clEdit, env.srv.URL+"/api/templates", env.csEdit)
	if status != http.StatusOK {
		t.Fatalf("列模板 = %d(%s)", status, raw)
	}
	items, _ := dto["templates"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["name"] == "tpl-1" {
			t.Fatal("被 403 拦下的创建仍然落库了")
		}
	}
}

// TestLibraryReadStaysLoginOnly 钉住读侧口径:没有 library.edit 的账号照样列得出三类资产 ——
// 流水线编辑器的节点选择器、模板画廊都靠这几个 GET,按点挡会让它们静默变空。
func TestLibraryReadStaysLoginOnly(t *testing.T) {
	env := setupLibraryPerm(t)
	for _, path := range []string{"/api/templates", "/api/variable-groups", "/api/custom-nodes"} {
		status, _, raw := getJSON(t, env.clView, env.srv.URL+path, env.csView)
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200(%s)", path, status, raw)
		}
	}
}
