package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/repocache"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/vault"
)

type manualTagEnv struct {
	srv    *httptest.Server
	client *http.Client
	csrf   string
	db     *sql.DB
	projID string
}

func setupManualTagEnv(t *testing.T, lister RefsLister) manualTagEnv {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	rsvc := run.New(st.DB)
	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(v), WithProjects(psvc), WithRuns(rsvc, nil), WithRefs(lister)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	credID := newGitCred(t, client, srv.URL, csrf, "ghp_token_for_manual_tag")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"tagged","repoUrl":"https://example.com/tagged.git","credentialId":"`+credID+`"}`)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ID == "" {
		t.Fatalf("project id empty: %s", raw)
	}
	return manualTagEnv{srv: srv, client: client, csrf: csrf, db: st.DB, projID: out.ID}
}

func TestManualTriggerTagResolvesToFixedCommit(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	env := setupManualTagEnv(t, fakeRefsLister{refs: &repocache.Refs{
		Tags: []repocache.Ref{{Name: "v1.2.3", Commit: sha, IsTag: true}},
	}})

	resp := doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/projects/"+env.projID+"/runs", env.csrf,
		`{"branch":"main","tag":"v1.2.3"}`)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, raw)
	}
	var dto struct {
		ID      string `json:"id"`
		Trigger struct {
			Branch string `json:"branch"`
			Commit string `json:"commit"`
		} `json:"trigger"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.Trigger.Branch != "main" || dto.Trigger.Commit != sha {
		t.Fatalf("trigger = %+v, want branch main + fixed tag commit", dto.Trigger)
	}

	detail := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/runs/"+dto.ID, "", "")
	raw, _ = io.ReadAll(detail.Body)
	_ = detail.Body.Close()
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d: %s", detail.StatusCode, raw)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	trigger, _ := got["trigger"].(map[string]any)
	if trigger["commit"] != sha {
		t.Fatalf("persisted commit = %v, want %s", trigger["commit"], sha)
	}
}

func TestManualTriggerTagRejectsInvalidOrConflictingRef(t *testing.T) {
	env := setupManualTagEnv(t, fakeRefsLister{refs: &repocache.Refs{}})
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{name: "missing tag", body: `{"branch":"main","tag":"v9"}`, code: "invalid_tag"},
		{name: "tag and commit", body: `{"branch":"main","tag":"v1","commit":"abc"}`, code: "conflicting_ref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/projects/"+env.projID+"/runs", env.csrf, tc.body)
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d: %s", resp.StatusCode, raw)
			}
			var out struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &out)
			if out.Error.Code != tc.code {
				t.Fatalf("error code = %q, want %q: %s", out.Error.Code, tc.code, raw)
			}
		})
	}

	list := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/runs?projectId="+env.projID, "", "")
	raw, _ := io.ReadAll(list.Body)
	_ = list.Body.Close()
	var runs struct {
		Items []any `json:"items"`
	}
	_ = json.Unmarshal(raw, &runs)
	if len(runs.Items) != 0 {
		t.Fatalf("rejected triggers must not create runs: %s", raw)
	}
}

func TestManualTriggerTagReportsRefLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lister RefsLister
		status int
	}{
		{name: "disabled", lister: nil, status: http.StatusServiceUnavailable},
		{name: "unavailable", lister: fakeRefsLister{err: errors.New("offline")}, status: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupManualTagEnv(t, tc.lister)
			resp := doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/projects/"+env.projID+"/runs", env.csrf,
				`{"branch":"main","tag":"v1"}`)
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, resp.StatusCode, raw)
			}
		})
	}
}
