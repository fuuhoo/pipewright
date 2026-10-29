package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/users"
)

// TestSetInitialPasswordHandler 覆盖「首次设置口令」的 HTTP 行为:
// 占位口令账号(has_local_password=0)无需当前口令即可设；弱口令 422；设完再用 → 409；
// 会话载荷 hasLocalPassword 由 false 翻到 true。
func TestSetInitialPasswordHandler(t *testing.T) {
	srv, adminClient, _, st := setupAccountServer(t)
	_ = adminClient

	// 占位口令账号:明文在测试里可控,只为了能登录拿到会话;标记仍是「没有本地口令」。
	hash, err := auth.HashPassword("placeholder-known")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	us := users.NewService(st.DB)
	u, err := us.Create(users.CreateInput{
		Username: "oidcuser", PasswordHash: hash, Role: "user", PlaceholderPassword: true,
	})
	if err != nil {
		t.Fatalf("create placeholder user: %v", err)
	}

	client := newTestClient(t)
	csrf := loginAs(t, client, srv.URL, "oidcuser", "placeholder-known")

	sessResp := doJSON(t, client, http.MethodGet, srv.URL+"/api/auth/session", "", "")
	defer sessResp.Body.Close()
	if sessResp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", sessResp.StatusCode)
	}
	var sess struct {
		HasLocalPassword bool `json:"hasLocalPassword"`
	}
	if err := json.NewDecoder(sessResp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.HasLocalPassword {
		t.Fatalf("占位口令账号会话应 hasLocalPassword=false")
	}

	weak := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/password/initial", csrf, `{"newPassword":"short"}`)
	defer weak.Body.Close()
	if weak.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("weak password status = %d, want 422", weak.StatusCode)
	}

	ok := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/password/initial", csrf, `{"newPassword":"brandnew1234"}`)
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(ok.Body)
		t.Fatalf("initial set status = %d, want 204; body=%s", ok.StatusCode, raw)
	}

	// 新口令可登录;这条账号之后按正常改密路径走。
	other := doLogin(t, srv, "oidcuser", "brandnew1234")
	defer other.Body.Close()
	if other.StatusCode != http.StatusOK {
		t.Fatalf("login with new password status = %d, want 200", other.StatusCode)
	}

	// 会话载荷翻成 true(账户页据此刻换成「修改密码」表单)。
	sessResp2 := doJSON(t, client, http.MethodGet, srv.URL+"/api/auth/session", "", "")
	defer sessResp2.Body.Close()
	var sess2 struct {
		HasLocalPassword bool `json:"hasLocalPassword"`
	}
	if err := json.NewDecoder(sessResp2.Body).Decode(&sess2); err != nil {
		t.Fatalf("decode session after set: %v", err)
	}
	if !sess2.HasLocalPassword {
		t.Fatalf("设置后会话应 hasLocalPassword=true")
	}

	// 再走一次 = 绕过当前口令改密,必须 409。
	again := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/password/initial", csrf, `{"newPassword":"another1234"}`)
	defer again.Body.Close()
	if again.StatusCode != http.StatusConflict {
		t.Fatalf("second initial set status = %d, want 409", again.StatusCode)
	}
	raw, _ := io.ReadAll(again.Body)
	if !strings.Contains(string(raw), "password_already_set") {
		t.Fatalf("409 body should carry password_already_set, got %s", raw)
	}

	// 口令仍是第一次设的那个(409 没有偷改)。
	if resp := doLogin(t, srv, "oidcuser", "brandnew1234"); resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("password should stay brandnew1234, login status = %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doLogin(t, srv, "oidcuser", "another1234"); resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("second password must not apply, login status = %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// 账号行已标记为有本地口令。
	if got, err := us.GetByID(u.ID); err != nil || !got.HasLocalPassword {
		t.Fatalf("user has_local_password should be true, err=%v got=%+v", err, got)
	}
}
