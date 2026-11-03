package serverpref_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/serverpref"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

// 每用户一份的主机视图偏好:读写都按 user_id 定位。这里锁四件事 ——
// 没设置过的人读到空、存进去的顺序原样回来、各人看不到各人的、脏数据不该把整页拖垮。
func TestGetEmptyWithoutRow(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		svc := serverpref.NewService(st.DB)
		p, err := svc.Get(context.Background(), "nobody")
		if err != nil {
			t.Fatalf("没设置过不该报错: %v", err)
		}
		if p.Pinned == nil || p.Order == nil {
			t.Fatalf("应回非 nil 空列表(前端不必判 null): pinned=%v order=%v", p.Pinned, p.Order)
		}
		if len(p.Pinned) != 0 || len(p.Order) != 0 {
			t.Fatalf("空偏好应为空数组, got %+v", p)
		}
	})
}

func TestSaveKeepsOrderAndIsPerUser(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		svc := serverpref.NewService(st.DB)

		if _, err := svc.Save(ctx, "u-1", serverpref.Prefs{
			Pinned: []string{"srv-b", "srv-a"},
			Order:  []string{"srv-c", "srv-a", "srv-b"},
		}); err != nil {
			t.Fatalf("save u-1: %v", err)
		}
		if _, err := svc.Save(ctx, "u-2", serverpref.Prefs{Pinned: []string{"srv-a"}}); err != nil {
			t.Fatalf("save u-2: %v", err)
		}

		got, err := svc.Get(ctx, "u-1")
		if err != nil {
			t.Fatalf("get u-1: %v", err)
		}
		// 顺序就是这份偏好的语义,原样回来。
		if strings.Join(got.Pinned, ",") != "srv-b,srv-a" {
			t.Errorf("u-1 Pinned = %v, 期望 [srv-b srv-a]", got.Pinned)
		}
		if strings.Join(got.Order, ",") != "srv-c,srv-a,srv-b" {
			t.Errorf("u-1 Order = %v, 期望 [srv-c srv-a srv-b]", got.Order)
		}

		other, err := svc.Get(ctx, "u-2")
		if err != nil {
			t.Fatalf("get u-2: %v", err)
		}
		if strings.Join(other.Order, ",") != "" {
			t.Errorf("u-2 不该读到 u-1 的顺序: %v", other.Order)
		}

		// 整份覆盖:第二次保存不再保留第一次的项(幂等,最后一写为准)。
		if _, err := svc.Save(ctx, "u-1", serverpref.Prefs{Order: []string{"srv-a"}}); err != nil {
			t.Fatalf("overwrite u-1: %v", err)
		}
		after, err := svc.Get(ctx, "u-1")
		if err != nil {
			t.Fatalf("re-read u-1: %v", err)
		}
		if len(after.Pinned) != 0 || strings.Join(after.Order, ",") != "srv-a" {
			t.Errorf("覆盖后应只剩新的 order: %+v", after)
		}
	})
}

// 存进去的必须是干净列表:空串、重复、不像 id 的超长串、以及远超一屏的条数都在 Save 里处理掉,
// 免得库里攒出前端读不懂的东西。
func TestSaveNormalizes(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		svc := serverpref.NewService(st.DB)
		long := strings.Repeat("x", 100)
		many := make([]string, 0, 450)
		for i := 0; i < 450; i++ {
			many = append(many, "srv-"+string(rune('a'+i%26))+string(rune('A'+i%26))+strconv.Itoa(i))
		}

		saved, err := svc.Save(context.Background(), "u-3", serverpref.Prefs{
			Pinned: []string{" srv-a ", "", "srv-a", long, "srv-b"},
			Order:  many,
		})
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if strings.Join(saved.Pinned, ",") != "srv-a,srv-b" {
			t.Errorf("Pinned = %v, 期望去空白/去重/丢超长", saved.Pinned)
		}
		if len(saved.Order) != 400 {
			t.Errorf("Order 长度 = %d, 期望截到 400", len(saved.Order))
		}

		got, err := svc.Get(context.Background(), "u-3")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if strings.Join(got.Pinned, ",") != "srv-a,srv-b" || len(got.Order) != 400 {
			t.Errorf("回读与存下的不一致: %+v", got)
		}
	})
}

// 那一列被手改坏(或将来换了格式)不该让容器页读不出主机列表:按空偏好回,不报错。
func TestGetToleratesCorruptJSON(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		svc := serverpref.NewService(st.DB)
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO server_view_prefs (user_id, pinned_json, order_json, updated_at) VALUES (?, ?, ?, ?)`,
			"u-4", "{不是数组", `["srv-a",{"x":1}]`, "2026-11-02T00:00:00Z"); err != nil {
			t.Fatalf("seed 脏行: %v", err)
		}
		got, err := svc.Get(ctx, "u-4")
		if err != nil {
			t.Fatalf("脏数据不该报错: %v", err)
		}
		if len(got.Pinned) != 0 {
			t.Errorf("坏 JSON 应回空, got %v", got.Pinned)
		}
		// 数组里混进非字符串:整列按读不懂处理,而不是把半个列表塞给前端。
		if len(got.Order) != 0 {
			t.Errorf("混型的 order 应回空, got %v", got.Order)
		}
	})
}

// 没有归属就不写:宁可让前端拿到 409 去重登,也不要造出一行谁也读不到的偏好。
func TestSaveWithoutUser(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		svc := serverpref.NewService(st.DB)
		if _, err := svc.Save(context.Background(), "", serverpref.Prefs{Pinned: []string{"srv-a"}}); err == nil {
			t.Fatalf("空 user_id 应拒绝保存")
		}
	})
}
