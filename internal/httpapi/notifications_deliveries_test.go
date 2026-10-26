package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/notify"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// notifications_deliveries_test.go —— GET /api/notifications/deliveries 的契约。
//
// 台账页是这期新加的唯一「读发送结果」的出口,前端要依赖三件容易写反的事:
//  1. 空台账也是 200 + events/statuses 两份权威枚举(筛选下拉不硬编码,事件早已从 6 个涨到 11 个);
//  2. 正文随列表一起带出,点开一行不再多打一次接口;
//  3. 可见范围走数据轴:挂在私有组项目上的记录对局外人收窄,而 project_id 为空的全局事件
//     (证书到期、指标异常)本该所有人可见 —— 收敛写错等于把通知藏起来或把别人的项目漏出去。

const deliveriesPath = "/api/notifications/deliveries"

// setupLedgerServer 起一台带 notify + 分组权限的 server,并把 db 交回测试直接播种台账。
//
// 播种走 SQL 而不走「配渠道 → 配路由 → 真发一次」:投递结论与筛选/收敛都是纯读路径的事,
// 拿 stub server 发一轮只会把用例绑在网络上。notify.New 的 client 传 nil —— 这条线不发送。
func setupLedgerServer(t *testing.T) (*httptest.Server, *http.Client, string, *sql.DB) {
	t.Helper()
	st := storetest.Open(t)
	db := st.DB
	userSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(db, testMasterKey())
	gs := group.New(db)
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v),
		WithNotifications(notify.New(db, v, nil)),
		WithGroups(gs),
		WithAccess(access.NewService(gs)),
	))
	t.Cleanup(srv.Close)

	c := newTestClient(t)
	csrf := loginWithClient(t, c, srv.URL)
	return srv, c, csrf, db
}

// ledgerRow 是一行台账的种子(空串=该列写 NULL,与领域侧的落库口径一致)。
type ledgerRow struct {
	id, event, projectID, runID, channelID, channelName, status, title, body, errorText, createdAt string
}

func seedLedger(t *testing.T, db *sql.DB, r ledgerRow) string {
	t.Helper()
	id := r.id
	if id == "" {
		id = uuid.NewString()
	}
	nul := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	created := r.createdAt
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := db.Exec(
		`INSERT INTO notification_deliveries
		 (id, event, project_id, run_id, channel_id, channel_name, status, title, body, error_text, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.event, nul(r.projectID), nul(r.runID), r.channelID, r.channelName,
		r.status, r.title, r.body, r.errorText, created)
	if err != nil {
		t.Fatalf("seed ledger %s: %v", r.channelName, err)
	}
	return id
}

// decodeDeliveries 读出台账列表响应。items 一律是数组(空台账也是),前端不必判 undefined。
func decodeDeliveries(t *testing.T, resp *http.Response) deliveryListDTO {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	var out deliveryListDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode deliveries: %v (%s)", err, raw)
	}
	return out
}

func getDeliveries(t *testing.T, c *http.Client, srvURL, csrf, query string) (*http.Response, deliveryListDTO) {
	t.Helper()
	url := srvURL + deliveriesPath
	if query != "" {
		url += "?" + query
	}
	resp := doJSON(t, c, http.MethodGet, url, csrf, "")
	defer func() { _ = resp.Body.Close() }()
	return resp, decodeDeliveries(t, resp)
}

// seedLedgerProject 落一个项目(分组权限收敛按 group_id 判定,归组必须先有真项目)。
func seedLedgerProject(t *testing.T, db *sql.DB, credID, name, groupID string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := db.Exec(
		`INSERT INTO projects (id, name, repo_url, default_branch, credential_id, group_id, created_at, updated_at)
		 VALUES (?, ?, 'https://example.com/acme/x.git', 'main', ?, ?, ?, ?)`,
		id, name, credID, groupID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("seed project %s: %v", name, err)
	}
	return id
}

func TestDeliveriesEmptyLedgerStillCarriesEnums(t *testing.T) {
	srv, c, csrf, _ := setupLedgerServer(t)
	resp, body := getDeliveries(t, c, srv.URL, csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("空台账应 200, got %d", resp.StatusCode)
	}
	if body.Items == nil {
		t.Fatal("items 必须是空数组而非 null")
	}
	if len(body.Items) != 0 || body.Total != 0 || body.Page != 1 {
		t.Fatalf("空台账形状不对: %+v", body)
	}
	if len(body.Events) != len(notify.Events()) {
		t.Fatalf("events 应等于领域枚举 %v, got %v", notify.Events(), body.Events)
	}
	// 事件数会随功能涨(建这页时已从 6 涨到 11),所以下拉必须由接口喂;这里钉住几条
	// 前端确实要展示的,防止 events 字段退化成空表。
	joined := strings.Join(body.Events, ",")
	for _, want := range []string{"build_succeeded", "build_failed", "cert_expiring", "anomaly_detected"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("events 缺 %q: %v", want, body.Events)
		}
	}
	want := []string{notify.DeliveryOK, notify.DeliveryFailed, notify.DeliverySkipped}
	if strings.Join(body.Statuses, ",") != strings.Join(want, ",") {
		t.Fatalf("statuses 应是 ok,failed,skipped, got %v", body.Statuses)
	}
}

func TestDeliveriesItemsCarryBodyAndNames(t *testing.T) {
	srv, c, csrf, db := setupLedgerServer(t)
	credID := seedGuardCredential(t, vault.New(db, testMasterKey()), "ledger cred")
	projID := seedLedgerProject(t, db, credID, "ledger-demo", access.Ungrouped)
	runID := seedGuardRun(t, db, projID)

	seedLedger(t, db, ledgerRow{
		event: notify.EventBuildFailed, projectID: projID, runID: runID,
		channelID: "ch-1", channelName: "值班群", status: notify.DeliveryFailed,
		title: "构建失败: ledger-demo", body: "分支 main 提交 abcdef1 构建失败",
		errorText: "对方返回 500", createdAt: "2026-10-01T00:00:00Z",
	})

	resp, body := getDeliveries(t, c, srv.URL, csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(body.Items) != 1 {
		t.Fatalf("应 1 条, got %d", len(body.Items))
	}
	it := body.Items[0]
	if it.Event != notify.EventBuildFailed || it.Status != notify.DeliveryFailed {
		t.Fatalf("事件/结论不对: %+v", it)
	}
	if it.ProjectID != projID || it.ProjectName != "ledger-demo" || it.RunID != runID {
		t.Fatalf("项目/运行回填不对: %+v", it)
	}
	if it.ChannelName != "值班群" || it.ErrorText != "对方返回 500" {
		t.Fatalf("渠道名/失败原因应原样带出: %+v", it)
	}
	// 正文是这页「点开看全文」的全部内容:必须随列表一起出来。
	if it.Body != "分支 main 提交 abcdef1 构建失败" {
		t.Fatalf("正文应随列表带出, got %q", it.Body)
	}
	if it.CreatedAt != "2026-10-01T00:00:00Z" {
		t.Fatalf("createdAt 应是 RFC3339 串, got %q", it.CreatedAt)
	}
}

func TestDeliveriesFiltersAndPaging(t *testing.T) {
	srv, c, csrf, db := setupLedgerServer(t)
	credID := seedGuardCredential(t, vault.New(db, testMasterKey()), "ledger cred")
	projA := seedLedgerProject(t, db, credID, "proj-a", access.Ungrouped)
	projB := seedLedgerProject(t, db, credID, "proj-b", access.Ungrouped)
	runA := seedGuardRun(t, db, projA)

	// 三条:同一项目两条(其中一条挂运行)、另一项目一条、外加一条全局事件。
	seedLedger(t, db, ledgerRow{event: notify.EventBuildSucceeded, projectID: projA,
		channelID: "ch-1", channelName: "A 群", status: notify.DeliveryOK, title: "构建成功",
		body: "A 成功", createdAt: "2026-10-03T00:00:00Z"})
	seedLedger(t, db, ledgerRow{event: notify.EventBuildFailed, projectID: projA, runID: runA,
		channelID: "ch-1", channelName: "A 群", status: notify.DeliveryFailed, title: "构建失败",
		body: "A 失败", errorText: "超时", createdAt: "2026-10-02T00:00:00Z"})
	seedLedger(t, db, ledgerRow{event: notify.EventDeployFailed, projectID: projB,
		channelID: "ch-2", channelName: "B 群", status: notify.DeliverySkipped, title: "部署失败",
		body: "B 跳过", createdAt: "2026-10-01T00:00:00Z"})
	seedLedger(t, db, ledgerRow{event: notify.EventCertExpiring,
		channelID: "ch-3", channelName: "运维邮箱", status: notify.DeliveryOK, title: "证书快到期",
		body: "还剩 3 天", createdAt: "2026-10-04T00:00:00Z"})

	cases := []struct {
		name, query string
		wantTotal   int
		wantFirst   string // 倒序后第一条的标题
	}{
		{"无筛选全给", "", 4, "证书快到期"},
		{"按事件", "event=build_failed", 1, "构建失败"},
		{"按结论", "status=skipped", 1, "部署失败"},
		{"按项目", "projectId=" + projA, 2, "构建成功"},
		{"按运行", "runId=" + runA, 1, "构建失败"},
		{"项目加结论", "projectId=" + projA + "&status=failed", 1, "构建失败"},
		{"筛空了", "status=ok&event=deploy_failed", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body := getDeliveries(t, c, srv.URL, csrf, tc.query)
			if body.Total != tc.wantTotal {
				t.Fatalf("total = %d, want %d (query %q)", body.Total, tc.wantTotal, tc.query)
			}
			if len(body.Items) != tc.wantTotal {
				t.Fatalf("items=%d total=%d 应同口径", len(body.Items), body.Total)
			}
			if tc.wantFirst != "" && body.Items[0].Title != tc.wantFirst {
				t.Fatalf("首条 = %q, want %q", body.Items[0].Title, tc.wantFirst)
			}
		})
	}

	// 倒序 + 分页:pageSize=1 时逐页翻,时间从新到旧,页码回显给前端。
	page1, body := getDeliveries(t, c, srv.URL, csrf, "pageSize=1&page=1")
	if page1.StatusCode != http.StatusOK || len(body.Items) != 1 || body.Page != 1 || body.Total != 4 {
		t.Fatalf("首页形状不对: page=%d items=%d total=%d", body.Page, len(body.Items), body.Total)
	}
	if body.Items[0].Title != "证书快到期" {
		t.Fatalf("首页应是最新那条, got %q", body.Items[0].Title)
	}
	_, last := getDeliveries(t, c, srv.URL, csrf, "pageSize=1&page=4")
	if len(last.Items) != 1 || last.Items[0].Title != "部署失败" {
		t.Fatalf("末页应是最旧那条, got %+v", last.Items)
	}
	_, beyond := getDeliveries(t, c, srv.URL, csrf, "pageSize=1&page=9")
	if beyond.Total != 4 || len(beyond.Items) != 0 {
		t.Fatalf("越界页只给空 items,total 不变: %+v", beyond)
	}
}

func TestDeliveriesRejectsBadFilters(t *testing.T) {
	srv, c, csrf, _ := setupLedgerServer(t)

	// 非法结论:400 + 说清只能是三档之一(前端下拉之外的手改 URL / MCP 调用都要看得懂)。
	resp := doJSON(t, c, http.MethodGet, srv.URL+deliveriesPath+"?status=boom", csrf, "")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法结论应 400, got %d (%s)", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "结论筛选非法") {
		t.Fatalf("400 要说清是三档之一, got %s", raw)
	}

	for _, q := range []string{"page=0", "page=abc", "page=999999999", "pageSize=0", "pageSize=x"} {
		if resp, _ := getDeliveries(t, c, srv.URL, csrf, q); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q 应 400, got %d", q, resp.StatusCode)
		}
	}
}

// TestDeliveriesGroupScopeHidesPrivateProjects 验证台账随项目分组收敛,全局事件不受此限。
func TestDeliveriesGroupScopeHidesPrivateProjects(t *testing.T) {
	st := storetest.Open(t)
	db := st.DB
	userSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	loadRoleCatalog(t, db)

	hash, err := auth.HashPassword(guardPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	mkUser := func(name string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, 'user', 1, ?, ?)`,
			id, name, hash, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed user %s: %v", name, err)
		}
		return id
	}
	ownerID := mkUser("ledger-owner")
	memberID := mkUser("ledger-member")
	mkUser("ledger-stranger") // 只用来登录,不进任何组

	gs := group.New(db)
	ctx := context.Background()
	priv, err := gs.Create(ctx, group.CreateInput{
		Name: "私密组", OwnerID: ownerID, MemberIDs: []string{memberID}, Visibility: access.VisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	v := vault.New(db, testMasterKey())
	credID := seedGuardCredential(t, v, "ledger cred")
	inPriv := seedLedgerProject(t, db, credID, "private-proj", priv.ID)
	free := seedLedgerProject(t, db, credID, "free-proj", access.Ungrouped)

	seedLedger(t, db, ledgerRow{event: notify.EventBuildFailed, projectID: inPriv,
		channelID: "ch-1", channelName: "组内群", status: notify.DeliveryOK, title: "私密项目的通知", body: "内部细节"})
	seedLedger(t, db, ledgerRow{event: notify.EventBuildSucceeded, projectID: free,
		channelID: "ch-1", channelName: "公开群", status: notify.DeliveryOK, title: "未归组的通知", body: "外部可见"})
	seedLedger(t, db, ledgerRow{event: notify.EventCertExpiring,
		channelID: "ch-2", channelName: "运维邮箱", status: notify.DeliveryOK, title: "证书快到期", body: "全局事件"})

	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v),
		WithNotifications(notify.New(db, v, nil)),
		WithGroups(gs),
		WithAccess(access.NewService(gs)),
	))
	t.Cleanup(srv.Close)

	titles := func(client *http.Client, csrf string) map[string]bool {
		t.Helper()
		_, body := getDeliveries(t, client, srv.URL, csrf, "")
		got := map[string]bool{}
		for _, it := range body.Items {
			got[it.Title] = true
		}
		return got
	}
	member := newTestClient(t)
	memberCS := loginAs(t, member, srv.URL, "ledger-member", guardPass)
	got := titles(member, memberCS)
	if !got["私密项目的通知"] || !got["未归组的通知"] || !got["证书快到期"] {
		t.Fatalf("组员该看见组内、未归组与全局三档: %v", got)
	}

	other := newTestClient(t)
	otherCS := loginAs(t, other, srv.URL, "ledger-stranger", guardPass)
	got = titles(other, otherCS)
	if got["私密项目的通知"] {
		t.Fatalf("局外人不该看见私有组项目的投递: %v", got)
	}
	// 全局事件(project_id 为 NULL)本就不属于任何分组,收敛时不能跟着一起消失。
	if !got["证书快到期"] {
		t.Fatalf("局外人仍该看见全局事件, got %v", got)
	}
	if !got["未归组的通知"] {
		t.Fatalf("未归组项目不该被收敛掉, got %v", got)
	}
}

func TestDeliveriesUnauthenticatedRejected(t *testing.T) {
	srv, _, _, _ := setupLedgerServer(t)
	resp, err := http.Get(srv.URL + deliveriesPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d", resp.StatusCode)
	}
}

// TestDeliveriesWithoutNotifyService 验证没装配 notify(单机裁剪)时接口明确报 503,
// 而不是拿着 nil service 打进去 panic 成 500。
func TestDeliveriesWithoutNotifyService(t *testing.T) {
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	srv := httptest.NewServer(New(testWebFSAuth(), svc)) // 刻意不 WithNotifications
	t.Cleanup(srv.Close)

	c := newTestClient(t)
	csrf := loginWithClient(t, c, srv.URL)
	resp := doJSON(t, c, http.MethodGet, srv.URL+deliveriesPath, csrf, "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配通知服务应 503, got %d", resp.StatusCode)
	}
}
