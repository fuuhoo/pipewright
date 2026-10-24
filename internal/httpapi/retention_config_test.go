// retention_config_test.go 守 /api/retention/config 的对外契约。
//
// 这里盯三件会咬人的事:
//   - 新装实例读到的默认值必须是「开启 + 全局 1000 条 + 365 天」——构建目录快照改成随运行记录
//     保留后,这个策略是唯一的生长边界;默认关就等于永久增长。
//   - keepPerProject 是旧维度:界面不再暴露,但**GET 到再 PUT 回去必须不丢**。设置页是整份对象
//     来回存的,一旦 PUT 把它清零,老实例上「每项目留 N 条」的约定就悄悄没了(方向上变宽松,
//     但仍然是一次无人授权的行为变更)。
//   - 负值归零由服务端做,响应体回读的是归一后的值,不能让前端显示一个库里并不存在的数。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/retention"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

func getRetention(t *testing.T, svc *retention.Service) (int, retentionConfigDTO) {
	t.Helper()
	rec := httptest.NewRecorder()
	makeGetRetentionConfigHandler(svc)(rec, httptest.NewRequest(http.MethodGet, "/api/retention/config", nil))
	var out retentionConfigDTO
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("解析 GET 响应: %v", err)
		}
	}
	return rec.Code, out
}

func putRetention(t *testing.T, svc *retention.Service, body string) (int, retentionConfigDTO) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/retention/config", strings.NewReader(body))
	makeSetRetentionConfigHandler(svc)(rec, req)
	var out retentionConfigDTO
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("解析 PUT 响应: %v", err)
		}
	}
	return rec.Code, out
}

func TestRetentionConfigDefaultOnFreshDB(t *testing.T) {
	svc := retention.NewService(storetest.OpenDB(t))

	code, got := getRetention(t, svc)
	if code != http.StatusOK {
		t.Fatalf("GET = %d, 期望 200", code)
	}
	if !got.Enabled || got.KeepGlobal != 1000 || got.MaxAgeDays != 365 {
		t.Fatalf("新装默认应为「开 + 全局 1000 条 + 365 天」, got %+v", got)
	}
}

func TestRetentionConfigKeepsLegacyPerProjectAcrossSave(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := retention.NewService(db)
	// 模拟旧实例:只配过每项目条数。
	if err := svc.SetConfig(context.Background(), retention.Config{
		Enabled: true, KeepPerProject: 20,
	}); err != nil {
		t.Fatalf("预置旧配置: %v", err)
	}

	_, loaded := getRetention(t, svc)
	if loaded.KeepPerProject != 20 {
		t.Fatalf("GET 应带回旧的 keepPerProject=20, got %+v", loaded)
	}
	// 设置页原样回存(界面不展示该字段,但值仍在对象里)。
	body, err := json.Marshal(loaded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	code, saved := putRetention(t, svc, string(body))
	if code != http.StatusOK {
		t.Fatalf("PUT = %d, 期望 200", code)
	}
	if saved.KeepPerProject != 20 {
		t.Fatalf("PUT 后 keepPerProject 应仍为 20(不许被清零), got %+v", saved)
	}
}

func TestRetentionConfigNormalizesNegativeKeepGlobal(t *testing.T) {
	svc := retention.NewService(storetest.OpenDB(t))

	code, saved := putRetention(t, svc, `{"enabled":true,"keepGlobal":-9,"keepPerProject":-3,"maxAgeDays":-1}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d, 期望 200", code)
	}
	// 三个维度都归零 = 无条件,Prune 会直接返回 0(见 retention.Prune 的守卫)。
	if saved.KeepGlobal != 0 || saved.KeepPerProject != 0 || saved.MaxAgeDays != 0 {
		t.Fatalf("负值应归零后回显, got %+v", saved)
	}
	got, err := svc.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.KeepGlobal != 0 || got.KeepPerProject != 0 || got.MaxAgeDays != 0 {
		t.Fatalf("库里也应归零, got %+v", got)
	}
	if n, err := svc.Prune(context.Background(), time.Now()); err != nil || n != 0 {
		t.Fatalf("三维全零时不应删任何运行(n=%d, err=%v)", n, err)
	}
}

func TestRetentionConfigServiceUnavailable(t *testing.T) {
	if code, _ := getRetention(t, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("未注入服务时 GET = %d, 期望 503", code)
	}
	if code, _ := putRetention(t, nil, `{"enabled":true}`); code != http.StatusServiceUnavailable {
		t.Fatalf("未注入服务时 PUT = %d, 期望 503", code)
	}
}
