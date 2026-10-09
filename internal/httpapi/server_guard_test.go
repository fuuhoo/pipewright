package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
)

// server_guard_test.go —— 主机 / 容器端点的分组权限(P3)。
//
// 与项目的差别只在两点:
//  1. 服务器是「设置类」资源:登记一台不归属任何组的机器等于向全员开放一台可 SSH 的目标,
//     因此只有管理员能做;放进某个组则要求管得住那个组。
//  2. 终端(WS)虽是 GET,语义却是「在目标机上执行任意命令」,按 ActOperate 把关。

type serverRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	GroupID string `json:"groupId"`
}

func listServers(t *testing.T, env *guardEnv, c *http.Client) []serverRow {
	t.Helper()
	resp := doJSON(t, c, http.MethodGet, env.srv.URL+"/api/servers", "", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/servers = %d", resp.StatusCode)
	}
	var out struct {
		Items []serverRow `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode servers: %v", err)
	}
	return out.Items
}

func serverNamesOf(rows []serverRow) map[string]string {
	out := map[string]string{} // name → groupId
	for _, r := range rows {
		out[r.Name] = r.GroupID
	}
	return out
}

func TestListServers_ScopedByGroup(t *testing.T) {
	env := setupGuard(t)

	// 局外人:未归组 + 公开组两台,看不到他人私有组里的机器。
	names := serverNamesOf(listServers(t, env, env.other))
	if gid, ok := names["free-srv"]; !ok || gid != access.Ungrouped {
		t.Fatalf("局外人服务器列表 = %v, want 含未归组的 free-srv", names)
	}
	if _, ok := names["secret-srv"]; ok {
		t.Fatalf("局外人看到了私有组服务器")
	}

	// 成员:多一台所在私有组内的机器,且带回 groupId 供前端标注归属。
	names = serverNamesOf(listServers(t, env, env.member))
	if gid, ok := names["secret-srv"]; !ok || gid != env.privateID {
		t.Fatalf("成员列表 = %v, want 含 secret-srv(groupId=%s)", names, env.privateID)
	}

	if got := len(listServers(t, env, env.admin)); got != 3 {
		t.Fatalf("管理员看到 %d 台, want 3", got)
	}
}

// TestServerAggregates_ScopedByGroup 断言批量总览(整表聚合)同样按可见分组收敛:
// 否则私有组的机器会借 /servers/metrics 与 /servers/containers 漏出去。
func TestServerAggregates_ScopedByGroup(t *testing.T) {
	env := setupGuard(t)

	ids := func(path string, c *http.Client) map[string]bool {
		t.Helper()
		resp := doJSON(t, c, http.MethodGet, env.srv.URL+path, "", "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		var out struct {
			Items []struct {
				ServerID string `json:"serverId"`
			} `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		set := map[string]bool{}
		for _, it := range out.Items {
			set[it.ServerID] = true
		}
		return set
	}

	for _, path := range []string{"/api/servers/metrics", "/api/servers/containers"} {
		got := ids(path, env.other)
		if len(got) != 2 || got[env.srvInPrivate] {
			t.Fatalf("%s 局外人看到 %v, want 2 台且不含私有组机器", path, got)
		}
		if all := ids(path, env.admin); len(all) != 3 || !all[env.srvInPrivate] {
			t.Fatalf("%s 管理员看到 %v, want 全部 3 台", path, all)
		}
	}
}

// TestServerWrites_Matrix 覆盖中间件档位:读→View、写→Operate,删除另加 Manage。
func TestServerWrites_Matrix(t *testing.T) {
	env := setupGuard(t)
	prefix := env.srv.URL + "/api/servers/"

	// 局外人连私有组机器都看不见 → 读 403、写 403。
	if got := statusOf(t, env.other, http.MethodGet, prefix+env.srvInPrivate, "", ""); got != http.StatusForbidden {
		t.Fatalf("局外人读私有组服务器 = %d, want 403", got)
	}
	if got := statusOf(t, env.other, http.MethodPut, prefix+env.srvInPrivate, env.otherCS, `{"name":"hijack"}`); got != http.StatusForbidden {
		t.Fatalf("局外人写私有组服务器 = %d, want 403", got)
	}
	// 未归组与公开组照旧:组成员身份不变也能操作。
	if got := statusOf(t, env.other, http.MethodPut, prefix+env.srvUngrouped, env.otherCS, `{"name":"renamed"}`); got != http.StatusOK {
		t.Fatalf("局外人写未归组服务器 = %d, want 200", got)
	}

	// 成员在私有组内可操作,但删除是归属级动作:要 Manage。
	if got := statusOf(t, env.member, http.MethodPut, prefix+env.srvInPrivate, env.memberCS, `{"port":2222}`); got != http.StatusOK {
		t.Fatalf("成员改私有组服务器端口 = %d, want 200", got)
	}
	if got := statusOf(t, env.member, http.MethodDelete, prefix+env.srvInPrivate, env.memberCS, ""); got != http.StatusForbidden {
		t.Fatalf("成员删私有组服务器 = %d, want 403", got)
	}
	// 组长管得动自己组内的机器。
	if got := statusOf(t, env.owner, http.MethodDelete, prefix+env.srvInPrivate, env.ownerCS, ""); got != http.StatusNoContent {
		t.Fatalf("组长删私有组服务器 = %d, want 204", got)
	}
	// 不存在的服务器 → 404,不伪装成 403。
	if got := statusOf(t, env.other, http.MethodGet, prefix+"no-such-server", "", ""); got != http.StatusNotFound {
		t.Fatalf("读不存在的服务器 = %d, want 404", got)
	}
}

// TestCreateServer_RequiresAdminOrGroupManage 断言登记闸门的两侧。
func TestCreateServer_RequiresAdminOrGroupManage(t *testing.T) {
	env := setupGuard(t)
	body := func(groupID string) string {
		return `{"name":"new-box","host":"10.0.0.77","port":22,"user":"deploy","credentialId":"` +
			env.serverCredID + `","groupId":"` + groupID + `"}`
	}

	// 普通用户登记「未归组」机器 = 向全员开放一台 SSH 目标 → 403。
	if got := statusOf(t, env.other, http.MethodPost, env.srv.URL+"/api/servers", env.otherCS, body("")); got != http.StatusForbidden {
		t.Fatalf("普通用户登记未归组服务器 = %d, want 403", got)
	}
	// 塞进与自己无关的私有组同样不行。
	if got := statusOf(t, env.other, http.MethodPost, env.srv.URL+"/api/servers", env.otherCS, body(env.privateID)); got != http.StatusForbidden {
		t.Fatalf("普通用户把服务器塞进别人私有组 = %d, want 403", got)
	}
	// 组长可以在自己组内添机器。
	if got := statusOf(t, env.owner, http.MethodPost, env.srv.URL+"/api/servers", env.ownerCS, body(env.privateID)); got != http.StatusCreated {
		t.Fatalf("组长在私有组登记服务器 = %d, want 201", got)
	}
	// 管理员登记未归组机器是正常设置操作。
	if got := statusOf(t, env.admin, http.MethodPost, env.srv.URL+"/api/servers", env.adminCS, body("")); got != http.StatusCreated {
		t.Fatalf("管理员登记未归组服务器 = %d, want 201", got)
	}
	// 指向不存在的分组 → 422,而不是静默入库成悬挂引用。
	if got := statusOf(t, env.admin, http.MethodPost, env.srv.URL+"/api/servers", env.adminCS, body("no-such-group")); got != http.StatusUnprocessableEntity {
		t.Fatalf("登记到不存在的分组 = %d, want 422", got)
	}
}

// TestReassignServer_RequiresManageOnBothSides 与项目的移组断言同构。
func TestReassignServer_RequiresManageOnBothSides(t *testing.T) {
	env := setupGuard(t)
	prefix := env.srv.URL + "/api/servers/"

	// 成员能把机器藏进自己管不了的组吗?旧侧 Manage 先挡住。
	if got := statusOf(t, env.member, http.MethodPut, prefix+env.srvInPrivate, env.memberCS,
		`{"groupId":"`+env.publicID+`"}`); got != http.StatusForbidden {
		t.Fatalf("成员把私有组服务器移到公开组 = %d, want 403", got)
	}
	// 组长可以把它甩出去(移出分组)。
	if got := statusOf(t, env.owner, http.MethodPut, prefix+env.srvInPrivate, env.ownerCS,
		`{"groupId":""}`); got != http.StatusOK {
		t.Fatalf("组长把服务器移出私有组 = %d, want 200", got)
	}
	// 移出后未归组 = 全员可操作,而这次归属改动只有组长和管理员管得动。
	if got := statusOf(t, env.member, http.MethodPut, prefix+env.srvUngrouped, env.memberCS,
		`{"groupId":"`+env.privateID+`"}`); got != http.StatusForbidden {
		t.Fatalf("成员把未归组服务器塞进私有组 = %d, want 403", got)
	}
	if got := statusOf(t, env.admin, http.MethodPut, prefix+env.srvUngrouped, env.adminCS,
		`{"groupId":"`+env.privateID+`"}`); got != http.StatusOK {
		t.Fatalf("管理员把未归组服务器移入私有组 = %d, want 200", got)
	}
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(1) FROM audit_log WHERE action = ?`, audit.ActionServerReassign).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n == 0 {
		t.Fatalf("服务器移组未留审计")
	}
}

// TestServerTerminal_RequiresOperate 断言 WS 终端按操作档把关:
// 中间件对 GET 只判到 View,而终端是任意命令通道,握手前必须挡住。
func TestServerTerminal_RequiresOperate(t *testing.T) {
	env := setupGuard(t)
	url := env.srv.URL + "/api/servers/" + env.srvInPrivate + "/terminal"

	// 局外人:连 View 都没有,中间件先 403。
	if got := statusOf(t, env.other, http.MethodGet, url, "", ""); got != http.StatusForbidden {
		t.Fatalf("局外人请求私有组服务器终端 = %d, want 403", got)
	}
	// 成员有 View,但这里要的是 Operate:非升级请求在权限关卡就被拒,不会走到 WS 握手。
	resp := doJSON(t, env.member, http.MethodGet, url, "", "")
	_ = readAll(t, resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("成员的非升级 GET 终端请求 = %d, want 426(过了权限、卡在 WS 握手)", resp.StatusCode)
	}
	// 局外人对未归组机器则可操作(存量语义:未归组 = 全员可见可操作)。
	open := env.srv.URL + "/api/servers/" + env.srvUngrouped + "/terminal"
	resp = doJSON(t, env.other, http.MethodGet, open, "", "")
	_ = readAll(t, resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("局外人请求未归组服务器终端 = %d, want 426", resp.StatusCode)
	}
}
