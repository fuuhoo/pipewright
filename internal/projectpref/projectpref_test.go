package projectpref_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/projectpref"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

// 每用户一份的项目卡片顺序:锁四件事 —— 没设置过读到空、顺序原样回来、各人看不到各人的、
// 脏数据不该把整页拖垮。
func TestGetEmptyWithoutRow(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		svc := projectpref.NewService(st.DB)
		p, err := svc.Get(context.Background(), "nobody")
		if err != nil {
			t.Fatalf("没设置过不该报错: %v", err)
		}
		if p.Order == nil {
			t.Fatalf("应回非 nil 空列表(前端不必判 null)")
		}
		if len(p.Order) != 0 {
			t.Fatalf("空偏好应为空数组, got %+v", p)
		}
	})
}

func TestSaveKeepsOrderAndIsPerUser(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		svc := projectpref.NewService(st.DB)

		if _, err := svc.Save(ctx, "u-1", projectpref.Prefs{Order: []string{"p-c", "p-a", "p-b"}}); err != nil {
			t.Fatalf("save u-1: %v", err)
		}
		if _, err := svc.Save(ctx, "u-2", projectpref.Prefs{Order: []string{"p-z"}}); err != nil {
			t.Fatalf("save u-2: %v", err)
		}

		got, err := svc.Get(ctx, "u-1")
		if err != nil {
			t.Fatalf("get u-1: %v", err)
		}
		if strings.Join(got.Order, ",") != "p-c,p-a,p-b" {
			t.Errorf("u-1 Order = %v, 期望 [p-c p-a p-b]", got.Order)
		}

		other, err := svc.Get(ctx, "u-2")
		if err != nil {
			t.Fatalf("get u-2: %v", err)
		}
		if strings.Join(other.Order, ",") != "p-z" {
			t.Errorf("u-2 不该读到 u-1 的顺序: %v", other.Order)
		}

		// 整份覆盖:第二次保存不再保留第一次的项。
		if _, err := svc.Save(ctx, "u-1", projectpref.Prefs{Order: []string{"p-a"}}); err != nil {
			t.Fatalf("overwrite u-1: %v", err)
		}
		after, err := svc.Get(ctx, "u-1")
		if err != nil {
			t.Fatalf("re-read u-1: %v", err)
		}
		if strings.Join(after.Order, ",") != "p-a" {
			t.Errorf("覆盖后应只剩新的 order: %+v", after)
		}
	})
}

// 存进去的必须是干净列表:空串、重复、超长串、以及远超一屏的条数都在 Save 里处理掉。
func TestSaveNormalizes(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		svc := projectpref.NewService(st.DB)
		long := strings.Repeat("x", 100)
		many := make([]string, 0, 450)
		for i := 0; i < 450; i++ {
			many = append(many, "p-"+strconv.Itoa(i))
		}

		saved, err := svc.Save(context.Background(), "u-3", projectpref.Prefs{
			Order: append([]string{" p-a ", "", "p-a", long, "p-b"}, many...),
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if len(saved.Order) != 400 || saved.Order[0] != "p-a" || saved.Order[1] != "p-b" {
			t.Errorf("应去空白/去重/丢超长并截到 400, got %d", len(saved.Order))
		}

		got, err := svc.Get(context.Background(), "u-3")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(got.Order) != 400 {
			t.Errorf("回读与存下的不一致: %d", len(got.Order))
		}
	})
}

// 那一列被手改坏不该让项目页读不出项目:按空偏好回,不报错。
func TestGetToleratesCorruptJSON(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		svc := projectpref.NewService(st.DB)
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO project_view_prefs (user_id, order_json, updated_at) VALUES (?, ?, ?)`,
			"u-4", `["p-a",{"x":1}]`, "2026-11-13T00:00:00Z"); err != nil {
			t.Fatalf("seed 脏行: %v", err)
		}
		got, err := svc.Get(ctx, "u-4")
		if err != nil {
			t.Fatalf("脏数据不该报错: %v", err)
		}
		if len(got.Order) != 0 {
			t.Errorf("混型的 order 应回空, got %v", got.Order)
		}
	})
}

// 没有归属就不写:宁可让前端拿到 409 去重登,也不要造出一行谁也读不到的偏好。
func TestSaveWithoutUser(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		svc := projectpref.NewService(st.DB)
		if _, err := svc.Save(context.Background(), "", projectpref.Prefs{Order: []string{"p-a"}}); err == nil {
			t.Fatalf("空 user_id 应拒绝保存")
		}
	})
}
