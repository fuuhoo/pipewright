package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/serverpref"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

// /api/account/server-view 两个端点的契约:只认会话里的本人、空偏好是 200 而不是 404、
// 写必须回读归一后的那份。CSRF 由中间件统一管,这里不起 router,只测 handler 本身。
func TestServerViewPrefsHandler_NilService(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		var handler http.HandlerFunc
		if m == http.MethodGet {
			handler = makeGetServerViewPrefsHandler(nil)
		} else {
			handler = makePutServerViewPrefsHandler(nil)
		}
		w := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(m, "/api/account/server-view", strings.NewReader(`{}`)),
			&auth.Session{Role: "user", UserID: "u-1"})
		handler(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s 未注入服务应 503, got %d", m, w.Code)
		}
	}
}

func TestServerViewPrefsHandler_ReadYourOwn(t *testing.T) {
	st := storetest.Open(t)
	svc := serverpref.NewService(st.DB)

	get := func(userID string) serverViewPrefsDTO {
		w := httptest.NewRecorder()
		makeGetServerViewPrefsHandler(svc)(w, withSession(
			httptest.NewRequest(http.MethodGet, "/api/account/server-view", nil),
			&auth.Session{Role: "user", UserID: userID}))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, 期望 200(没设置过也是空偏好,不是 404)", userID, w.Code)
		}
		var out serverViewPrefsDTO
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("解码: %v", err)
		}
		return out
	}

	// 从没设置过:两个空数组,不是 null(前端直接 .length 用)。
	empty := get("u-1")
	if empty.Pinned == nil || empty.Order == nil {
		t.Fatalf("空偏好应为 [] 而非 null: %+v", empty)
	}

	put := func(userID, body string) (int, serverViewPrefsDTO) {
		w := httptest.NewRecorder()
		makePutServerViewPrefsHandler(svc)(w, withSession(
			httptest.NewRequest(http.MethodPut, "/api/account/server-view", strings.NewReader(body)),
			&auth.Session{Role: "user", UserID: userID}))
		var out serverViewPrefsDTO
		if w.Code == http.StatusOK {
			_ = json.Unmarshal(w.Body.Bytes(), &out)
		}
		return w.Code, out
	}

	code, saved := put("u-1", `{"pinned":["srv-b","srv-b",""],"order":["srv-a","srv-b"]}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d, 期望 200", code)
	}
	if strings.Join(saved.Pinned, ",") != "srv-b" {
		t.Errorf("PUT 应回归一化后的那份, got %v", saved.Pinned)
	}
	if got := get("u-1"); strings.Join(got.Order, ",") != "srv-a,srv-b" {
		t.Errorf("回读 = %v, 期望 [srv-a srv-b]", got.Order)
	}

	// 别人读不到你的视图 —— 这是「每用户一份」的全部意义。
	if other := get("u-2"); len(other.Order) != 0 || len(other.Pinned) != 0 {
		t.Errorf("u-2 读到 u-1 的偏好了: %+v", other)
	}
}

func TestServerViewPrefsHandler_BadInput(t *testing.T) {
	st := storetest.Open(t)
	svc := serverpref.NewService(st.DB)

	put := func(userID, body string) int {
		w := httptest.NewRecorder()
		makePutServerViewPrefsHandler(svc)(w, withSession(
			httptest.NewRequest(http.MethodPut, "/api/account/server-view", strings.NewReader(body)),
			&auth.Session{Role: "user", UserID: userID}))
		return w.Code
	}

	if code := put("u-1", "pinned=["); code != http.StatusBadRequest {
		t.Errorf("坏 JSON = %d, 期望 400", code)
	}
	if code := put("u-1", `{"pinned":{"a":1}}`); code != http.StatusBadRequest {
		t.Errorf("类型不对 = %d, 期望 400", code)
	}
	// 会话没绑账号(0053 之前的旧会话):写不下去,让人重登,和改档案同一手法。
	if code := put("", `{"pinned":["srv-a"]}`); code != http.StatusConflict {
		t.Errorf("无 user_id 写入 = %d, 期望 409", code)
	}
	// 但读不该炸:旧会话读到空偏好,容器页照常按默认排序摆。
	w := httptest.NewRecorder()
	makeGetServerViewPrefsHandler(svc)(w, withSession(
		httptest.NewRequest(http.MethodGet, "/api/account/server-view", nil),
		&auth.Session{Role: "user", UserID: ""}))
	if w.Code != http.StatusOK {
		t.Errorf("无 user_id 读取 = %d, 期望 200 + 空偏好", w.Code)
	}
}
