package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/projectpref"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

// /api/account/project-view 的契约:只认会话里的本人、空顺序是 200 而不是 404、写必须回读归一后的那份。
// CSRF 由中间件统一管,这里不起 router,只测 handler 本身。
func TestProjectViewPrefsHandler_NilService(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		var handler http.HandlerFunc
		if m == http.MethodGet {
			handler = makeGetProjectViewPrefsHandler(nil)
		} else {
			handler = makePutProjectViewPrefsHandler(nil)
		}
		w := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(m, "/api/account/project-view", strings.NewReader(`{}`)),
			&auth.Session{Role: "user", UserID: "u-1"})
		handler(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s 未注入服务应 503, got %d", m, w.Code)
		}
	}
}

func TestProjectViewPrefsHandler_ReadYourOwn(t *testing.T) {
	st := storetest.Open(t)
	svc := projectpref.NewService(st.DB)

	get := func(userID string) projectViewPrefsDTO {
		w := httptest.NewRecorder()
		makeGetProjectViewPrefsHandler(svc)(w, withSession(
			httptest.NewRequest(http.MethodGet, "/api/account/project-view", nil),
			&auth.Session{Role: "user", UserID: userID}))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, 期望 200(没设置过也是空顺序,不是 404)", userID, w.Code)
		}
		var out projectViewPrefsDTO
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("解码: %v", err)
		}
		return out
	}

	if empty := get("u-1"); empty.Order == nil || len(empty.Order) != 0 {
		t.Fatalf("空偏好应为 [] 而非 null: %+v", empty)
	}

	put := func(userID, body string) (int, projectViewPrefsDTO) {
		w := httptest.NewRecorder()
		makePutProjectViewPrefsHandler(svc)(w, withSession(
			httptest.NewRequest(http.MethodPut, "/api/account/project-view", strings.NewReader(body)),
			&auth.Session{Role: "user", UserID: userID}))
		var out projectViewPrefsDTO
		if w.Code == http.StatusOK {
			_ = json.Unmarshal(w.Body.Bytes(), &out)
		}
		return w.Code, out
	}

	code, saved := put("u-1", `{"order":["p-b","p-b","","p-a"]}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d, 期望 200", code)
	}
	if strings.Join(saved.Order, ",") != "p-b,p-a" {
		t.Errorf("PUT 应回归一化后的那份, got %v", saved.Order)
	}
	if got := get("u-1"); strings.Join(got.Order, ",") != "p-b,p-a" {
		t.Errorf("回读 = %v, 期望 [p-b p-a]", got.Order)
	}

	// 别人读不到你的顺序 —— 这是「每用户一份」的全部意义。
	if other := get("u-2"); len(other.Order) != 0 {
		t.Errorf("u-2 读到 u-1 的偏好了: %+v", other)
	}
}

func TestProjectViewPrefsHandler_BadInput(t *testing.T) {
	st := storetest.Open(t)
	svc := projectpref.NewService(st.DB)

	put := func(userID, body string) int {
		w := httptest.NewRecorder()
		makePutProjectViewPrefsHandler(svc)(w, withSession(
			httptest.NewRequest(http.MethodPut, "/api/account/project-view", strings.NewReader(body)),
			&auth.Session{Role: "user", UserID: userID}))
		return w.Code
	}

	if code := put("u-1", "order=["); code != http.StatusBadRequest {
		t.Errorf("坏 JSON = %d, 期望 400", code)
	}
	if code := put("u-1", `{"order":{"a":1}}`); code != http.StatusBadRequest {
		t.Errorf("类型不对 = %d, 期望 400", code)
	}
	if code := put("", `{"order":["p-a"]}`); code != http.StatusConflict {
		t.Errorf("无 user_id 写入 = %d, 期望 409", code)
	}

	// 但读不该炸:旧会话读到空顺序,项目页照常按默认排序摆。
	w := httptest.NewRecorder()
	makeGetProjectViewPrefsHandler(svc)(w, withSession(
		httptest.NewRequest(http.MethodGet, "/api/account/project-view", nil),
		&auth.Session{Role: "user", UserID: ""}))
	if w.Code != http.StatusOK {
		t.Errorf("无 user_id 读取 = %d, 期望 200 + 空顺序", w.Code)
	}
}
