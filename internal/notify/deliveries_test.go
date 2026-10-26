package notify

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/google/uuid"
)

// deliveries_test.go —— 投递台账:三档结论怎么写、怎么写进库、又怎么按分组与筛选读出来。
// 台账是左栏「通知」页唯一的数据源,所以「渠道停用不能记成成功」「路由没配不该有记录」
// 这两条口径要在这里钉死。

// listAll 读全量台账(不受分组限制),按接口默认排序返回。
func listAll(t *testing.T, s *service) []Delivery {
	t.Helper()
	out, err := s.ListDeliveries(ctx(), DeliveryFilter{VisibleGroups: access.ListFilter{Unrestricted: true}})
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	return out.Items
}

// seedProjectRun 在临时库里造一个项目(可指定 group_id)与一条挂在其上的运行,返回两个 id。
func seedProjectRun(t *testing.T, db *sql.DB, groupID string) (string, string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	credID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
		VALUES (?, 'c', 'git_token', '', X'00', 'm', ?, ?)`, credID, now, now); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	projectID := uuid.NewString()
	// group_id 是 NOT NULL DEFAULT ''(空串即「未归组」),所以直接传字符串而不是 NULL。
	if _, err := db.Exec(`INSERT INTO projects (id, name, repo_url, default_branch, credential_id, group_id, created_at, updated_at)
		VALUES (?, 'acme', 'https://example.com/p.git', 'main', ?, ?, ?, ?)`, projectID, credID, groupID, now, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	runID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO pipeline_runs (id, project_id, status, trigger_type, created_at)
		VALUES (?, ?, 'success', 'manual', ?)`, runID, projectID, now); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return projectID, runID
}

// routeTo 给事件挂一条启用路由。
func routeTo(t *testing.T, s *service, event, channelID, projectID string) {
	t.Helper()
	if _, err := s.CreateRoute(ctx(), CreateRouteInput{
		Event: event, ChannelID: channelID, Enabled: true, ProjectID: projectID,
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
}

// TestDispatchWritesOKLedger 验证真发出去的那次投递在台账里是 ok,并带上事件/渠道名/标题。
func TestDispatchWritesOKLedger(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, _, _ := newSvc(t, srv.Client())
	chID := newWebhookChannel(t, s, srv.URL)
	routeTo(t, s, EventBuildFailed, chID, "")

	payload := EventPayload("zh-CN", EventBuildFailed, "demo", "main", "abcdef1234", "failed", 1500)
	if err := s.RouteEvent(ctx(), EventBuildFailed, payload); err != nil {
		t.Fatalf("route event: %v", err)
	}
	if hits != 1 {
		t.Fatalf("应真发 1 次 POST,实发 %d", hits)
	}
	items := listAll(t, s)
	if len(items) != 1 {
		t.Fatalf("台账应有 1 条, got %d", len(items))
	}
	d := items[0]
	if d.Status != DeliveryOK || d.Event != EventBuildFailed || d.ChannelID != chID || d.ChannelName != "test-webhook" {
		t.Fatalf("台账内容不对: %+v", d)
	}
	if d.Title == "" || d.ErrorText != "" {
		t.Fatalf("成功记录该有标题、不该有失败原因: %+v", d)
	}
	// 正文原样留档:记录页点开一行要看到的就是当时真发出去的那段话。
	if d.Body != payload.Body || d.Body == "" {
		t.Fatalf("台账正文应等于渲染出的载荷正文:\n got %q\nwant %q", d.Body, payload.Body)
	}
}

// TestDispatchTruncatesLongBody 钉住正文的存留上限:台账是「点开看当时发了什么」,
// 不是逐字归档 —— 超长正文只准吃尾,而且必须按字符切(切在多字节中间会产出乱码)。
func TestDispatchTruncatesLongBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, _, _ := newSvc(t, srv.Client())
	chID := newWebhookChannel(t, s, srv.URL)

	long := strings.Repeat("通", maxDeliveryBodyRunes+500)
	if err := s.dispatch(ctx(), Delivery{
		Event: EventBuildSucceeded, ChannelID: chID, Title: "构建成功",
	}, Payload{Title: "构建成功", Body: long}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	items := listAll(t, s)
	if len(items) != 1 {
		t.Fatalf("台账应有 1 条, got %d", len(items))
	}
	got := items[0].Body
	if n := len([]rune(got)); n != maxDeliveryBodyRunes {
		t.Fatalf("正文应截到 %d 个字符, got %d", maxDeliveryBodyRunes, n)
	}
	if got != strings.Repeat("通", maxDeliveryBodyRunes) {
		t.Fatalf("截断应只吃尾:前 %d 个字符要原样留着", maxDeliveryBodyRunes)
	}
}

// TestDispatchRecordsSkippedForDisabledChannel 钉住最容易说谎的一档:渠道被停用时平台「优雅跳过、
// 不发不报错」,台账必须记 skipped —— 记成 ok 就等于页面上凭空多出一条从没发出去的通知。
func TestDispatchRecordsSkippedForDisabledChannel(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, db, _ := newSvc(t, srv.Client())
	chID := newWebhookChannel(t, s, srv.URL)
	routeTo(t, s, EventBuildSucceeded, chID, "")
	if _, err := db.Exec(`UPDATE notification_channels SET enabled = 0 WHERE id = ?`, chID); err != nil {
		t.Fatalf("disable channel: %v", err)
	}

	if err := s.RouteEvent(ctx(), EventBuildSucceeded,
		EventPayload("zh-CN", EventBuildSucceeded, "demo", "main", "", "success", 0)); err != nil {
		t.Fatalf("停用渠道时应静默跳过而不是报错: %v", err)
	}
	items := listAll(t, s)
	if len(items) != 1 || items[0].Status != DeliverySkipped {
		t.Fatalf("台账应记 1 条 skipped, got %+v", items)
	}
	if hits != 0 {
		t.Fatalf("停用的渠道不该真发(%d 次)", hits)
	}
}

// TestDispatchRecordsFailedWhenChannelVanished 覆盖「路由查到了,发的时候渠道已经没了」这条并发线
// (notification_routes 对渠道是级联删,所以稳态下不会留下悬挂路由;能撞上的只有取完渠道集之后的删除)。
// 台账要记 failed 并把原因说成人话 —— 领域错误串不该原样甩给用户。
func TestDispatchRecordsFailedWhenChannelVanished(t *testing.T) {
	s, _, _ := newSvc(t, nil)
	const ghost = "channel-deleted-mid-flight"

	if err := s.dispatch(ctx(), Delivery{
		Event: EventBuildFailed, ChannelID: ghost, Title: "构建失败",
	}, testPayload()); err == nil {
		t.Fatal("渠道不存在时 dispatch 应回错(路由循环据此续发其余)")
	}
	items := listAll(t, s)
	if len(items) != 1 || items[0].Status != DeliveryFailed {
		t.Fatalf("台账应记 1 条 failed, got %+v", items)
	}
	if items[0].ErrorText != "渠道已不存在(可能已被删除)" {
		t.Fatalf("失败原因应是人读的渠道消失说明, got %q", items[0].ErrorText)
	}
	if items[0].ChannelID != ghost {
		t.Fatalf("渠道删了也要留得下当时的渠道 id, got %q", items[0].ChannelID)
	}
}

// TestDispatchRecordsFailedOnRejectedDelivery 验证对方拒收(非 2xx)记成 failed 并留下原因。
func TestDispatchRecordsFailedOnRejectedDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s, _, _ := newSvc(t, srv.Client())
	chID := newWebhookChannel(t, s, srv.URL)
	routeTo(t, s, EventBuildFailed, chID, "")

	if err := s.RouteEvent(ctx(), EventBuildFailed, testPayload()); err != nil {
		t.Fatalf("单渠道失败不该冒泡: %v", err)
	}
	items := listAll(t, s)
	if len(items) != 1 || items[0].Status != DeliveryFailed || items[0].ErrorText == "" {
		t.Fatalf("应记 1 条带原因的 failed, got %+v", items)
	}
}

// TestNoRouteWritesNoLedger 验证「未配置路由 → 不发送」同样不落台账:没发生过的事不该有记录。
func TestNoRouteWritesNoLedger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, _, _ := newSvc(t, srv.Client())
	newWebhookChannel(t, s, srv.URL) // 有渠道,但一条路由都没配

	if err := s.RouteEvent(ctx(), EventBuildFailed, testPayload()); err != nil {
		t.Fatalf("route event: %v", err)
	}
	if items := listAll(t, s); len(items) != 0 {
		t.Fatalf("没路由不该有台账, got %+v", items)
	}
}

// TestProjectRouteLedgerCarriesProjectAndRun 验证项目维度路由把归属一起记账:
// 前端要能「从这条运行回看它发过哪些通知」,也才能按项目/分组收敛。
func TestProjectRouteLedgerCarriesProjectAndRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, db, _ := newSvc(t, srv.Client())
	projectID, runID := seedProjectRun(t, db, "")
	chID := newWebhookChannel(t, s, srv.URL)
	routeTo(t, s, EventBuildSucceeded, chID, projectID)

	vars := TemplateVars{Project: "acme", RunID: runID}
	if err := s.RouteEventForProject(ctx(), projectID, EventBuildSucceeded, vars); err != nil {
		t.Fatalf("route for project: %v", err)
	}
	items := listAll(t, s)
	if len(items) != 1 {
		t.Fatalf("台账应有 1 条, got %d", len(items))
	}
	if items[0].ProjectID != projectID || items[0].RunID != runID {
		t.Fatalf("台账应带项目与运行归属, got %+v", items[0])
	}
	if items[0].ProjectName != "acme" {
		t.Fatalf("列表应 JOIN 出项目名, got %q", items[0].ProjectName)
	}

	// 按运行筛:正是运行详情页那个口径。
	byRun, err := s.ListDeliveries(ctx(), DeliveryFilter{RunID: runID, VisibleGroups: access.ListFilter{Unrestricted: true}})
	if err != nil {
		t.Fatalf("filter by run: %v", err)
	}
	if byRun.Total != 1 || byRun.Items[0].ID != items[0].ID {
		t.Fatalf("按运行筛应命中刚那条, got %+v", byRun)
	}
}

// TestLedgerGroupScopeKeepsGlobalEventsVisible 验证分组收敛的口径:挂在项目上的投递随可见组收窄,
// 但证书/异常这类全局事件(project_id 为 NULL)不属于任何组,不该因此消失;
// 而令牌范围与本人可见集无交集时整张台账 fail closed。
func TestLedgerGroupScopeKeepsGlobalEventsVisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, db, _ := newSvc(t, srv.Client())
	groupA := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		VALUES (?, 'A组', '', 'private', 'u1', 'u1', ?, ?)`, groupA, now, now); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	projectID, runID := seedProjectRun(t, db, groupA)
	chID := newWebhookChannel(t, s, srv.URL)
	vars := TemplateVars{Project: "acme", RunID: runID}

	// 一条挂项目(归 A 组),一条是全局事件(不挂项目)。
	routeTo(t, s, EventBuildSucceeded, chID, projectID)
	routeTo(t, s, EventCertExpiring, chID, "")
	if err := s.RouteEventForProject(ctx(), projectID, EventBuildSucceeded, vars); err != nil {
		t.Fatalf("route project event: %v", err)
	}
	if err := s.RouteEventVars(ctx(), EventCertExpiring, TemplateVars{Project: "example.com"}); err != nil {
		t.Fatalf("route cert event: %v", err)
	}

	cases := []struct {
		name  string
		scope access.ListFilter
		want  int
	}{
		{"不限组", access.ListFilter{Unrestricted: true}, 2},
		{"可见 A 组", access.ListFilter{GroupIDs: []string{access.Ungrouped, groupA}}, 2},
		{"只看未归组", access.ListFilter{GroupIDs: []string{access.Ungrouped}}, 1},
		{"只见别的组", access.ListFilter{GroupIDs: []string{access.Ungrouped, uuid.NewString()}}, 1},
		{"令牌范围无交集", access.ListFilter{DenyAll: true}, 0},
	}
	for _, c := range cases {
		if got := listScoped(t, s, c.scope); got != c.want {
			t.Fatalf("%s:应见 %d 条,实见 %d", c.name, c.want, got)
		}
	}
}

// listScoped 按给定分组可见集数台账条数。
func listScoped(t *testing.T, s *service, scope access.ListFilter) int {
	t.Helper()
	out, err := s.ListDeliveries(ctx(), DeliveryFilter{VisibleGroups: scope})
	if err != nil {
		t.Fatalf("ListDeliveries(scope=%+v): %v", scope, err)
	}
	return out.Total
}

// TestListDeliveriesRejectsBadStatus 验证非法 status 筛选是明确的领域错误(HTTP 层据此回 400),
// 而不是悄悄返回空集。
func TestListDeliveriesRejectsBadStatus(t *testing.T) {
	s, _, _ := newSvc(t, nil)
	if _, err := s.ListDeliveries(ctx(), DeliveryFilter{Status: "done"}); err != ErrInvalidDeliveryStatus {
		t.Fatalf("应回 ErrInvalidDeliveryStatus, got %v", err)
	}
	// 三档枚举都该被接受(空=不筛选)。
	for _, st := range append(DeliveryStatuses(), "") {
		if _, err := s.ListDeliveries(ctx(), DeliveryFilter{Status: st,
			VisibleGroups: access.ListFilter{Unrestricted: true}}); err != nil {
			t.Fatalf("status=%q 应合法: %v", st, err)
		}
	}
}

// TestListDeliveriesPaginationNewestFirst 验证倒序 + 分页:第一页给最新几条,Total 是全量条数。
func TestListDeliveriesPaginationNewestFirst(t *testing.T) {
	s, db, _ := newSvc(t, nil)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`INSERT INTO notification_deliveries
			(id, event, project_id, run_id, channel_id, channel_name, status, title, error_text, created_at)
			VALUES (?, 'build_failed', NULL, NULL, 'ch', '渠道', 'ok', ?, '', ?)`,
			uuid.NewString(), "标题"+string(rune('A'+i)), base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatalf("insert delivery: %v", err)
		}
	}
	page, err := s.ListDeliveries(ctx(), DeliveryFilter{Page: 1, PageSize: 2,
		VisibleGroups: access.ListFilter{Unrestricted: true}})
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if page.Total != 5 || len(page.Items) != 2 {
		t.Fatalf("Total 应为全量 5、本页 2 条, got total=%d items=%d", page.Total, len(page.Items))
	}
	if page.Items[0].Title != "标题E" || page.Items[1].Title != "标题D" {
		t.Fatalf("应按时间倒序, got %q / %q", page.Items[0].Title, page.Items[1].Title)
	}
}
