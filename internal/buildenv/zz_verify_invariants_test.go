package buildenv

import (
	"errors"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
)

// [R11/P0#4] 三态校验:unchecked 拒、unavailable 强制 false、available 放行。
func TestVerify_R11_SetEnabledThreeState(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	svc := NewService(repo)

	// 1) 新建(内部补 StatusUnchecked)→ 启用失败
	env, err := svc.Create(&BuildEnv{
		Language: "node", Version: "20", DisplayName: "n20",
		SourceType: SourceOfficial, Image: "node:20-alpine",
		SortOrder: 1, CreatedBy: "t",
	})
	mustV(t, err, "create")
	if env.ImageCheckStatus != StatusUnchecked {
		t.Fatalf("新默认应为 unchecked, got %s", env.ImageCheckStatus)
	}
	if err := svc.SetEnabled(env.ID, true); !isCode(err, "IMAGE_NOT_CHECKED") {
		t.Fatalf("unchecked → 启用应拒(IMAGE_NOT_CHECKED), got %v", err)
	}
	if err := svc.SetEnabled(env.ID, false); err != nil {
		t.Fatalf("unchecked → 禁用应成功, got %v", err)
	}

	// 2) unavailable → 强制 enabled=false(即使请求 true)
	mustV(t, repo.UpdateCheckStatus(env.ID, StatusUnavailable, "not found", "2024-01-01T00:00:00Z"), "mark unavailable")
	if err := svc.SetEnabled(env.ID, true); !isCode(err, "IMAGE_UNAVAILABLE") {
		t.Fatalf("unavailable → 启用应拒(IMAGE_UNAVAILABLE), got %v", err)
	}
	after, _ := svc.GetByID(env.ID)
	if after.Enabled {
		t.Fatal("unavailable 行必须 enabled=false")
	}

	// 3) available → 启用成功
	mustV(t, repo.UpdateCheckStatus(env.ID, StatusAvailable, "", "2024-01-01T00:00:00Z"), "mark available")
	if err := svc.SetEnabled(env.ID, true); err != nil {
		t.Fatalf("available → 启用应成功, got %v", err)
	}
	after, _ = svc.GetByID(env.ID)
	if !after.Enabled {
		t.Fatal("available 行应 enabled=true")
	}
}

// [R8] 系统不拼接地址:resolver 原样返回 env.Image。
func TestVerify_R8_NoAddressConcat(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	svc := NewService(repo)
	const weird = "registry.example.com:5000/team/pipewright/node:20-slim"
	env, err := svc.Create(&BuildEnv{
		Language: "node", Version: "20", DisplayName: "x",
		SourceType: SourceCustom, Image: weird, SortOrder: 1, CreatedBy: "t",
	})
	mustV(t, err, "create")
	// 先标 available,再启用(resolver 要求 enabled)
	mustV(t, repo.UpdateCheckStatus(env.ID, StatusAvailable, "", "2024-01-01T00:00:00Z"), "mark")
	mustV(t, svc.SetEnabled(env.ID, true), "enable")

	got, err := ResolveImage(t.Context(), "node", "20", repo)
	mustV(t, err, "resolve")
	if got != weird {
		t.Fatalf("必须原样返回, got %q want %q", got, weird)
	}

	// ResolveByID 同
	got2, err := ResolveByID(t.Context(), env.ID, repo)
	mustV(t, err, "resolveByID")
	if got2 != weird {
		t.Fatalf("ResolveByID 必须原样返回, got %q", got2)
	}
}

// [R3] 必填字段缺一不可(language/version/display_name/image)。
func TestVerify_R3_RequiredFields(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(NewSQLiteRepo(db))
	cases := map[string]*BuildEnv{
		"缺 language":  {Version: "20", DisplayName: "x", SourceType: SourceOfficial, Image: "i"},
		"缺 version":   {Language: "n", DisplayName: "x", SourceType: SourceOfficial, Image: "i"},
		"缺 display":   {Language: "n", Version: "20", SourceType: SourceOfficial, Image: "i"},
		"缺 image":     {Language: "n", Version: "20", DisplayName: "x", SourceType: SourceOfficial},
		"source 非法":   {Language: "n", Version: "20", DisplayName: "x", SourceType: "bogus", Image: "i"},
	}
	for name, e := range cases {
		if _, err := svc.Create(e); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s 应 ErrInvalidInput, got %v", name, err)
		}
	}
}

func mustV(t *testing.T, err error, step string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// isCode 检查 *ValidationError.Code(SetEnabled 走 Code 而非 sentinel)。
func isCode(err error, code string) bool {
	var ve *ValidationError
	return errors.As(err, &ve) && ve.Code == code
}


// 回归:`Service.Update` 曾把调用方未提供的 image_check_status(空串)直接落库,
// 导致 status='' 在 SetEnabled 的 switch 里不命中任何 case → P0#4 三态校验被绕过
// (unchecked 环境可被启用)。锁住修复。
func TestRegression_UpdatePreservesCheckStatus(t *testing.T) {
	db := storetest.OpenDB(t)
	repo := NewSQLiteRepo(db)
	svc := NewService(repo)
	env, err := svc.Create(&BuildEnv{
		Language: "node", Version: "20", DisplayName: "n20",
		SourceType: SourceOfficial, Image: "node:20-alpine", SortOrder: 1, CreatedBy: "t",
	})
	mustV(t, err, "create")
	mustV(t, repo.UpdateCheckStatus(env.ID, StatusAvailable, "", "2024-01-01T00:00:00Z"), "mark available")

	// 以「handler 同款」方式调用:不填 ImageCheckStatus(零值空串)
	upd, err := svc.Update(&BuildEnv{
		ID: env.ID, Language: env.Language, Version: env.Version, DisplayName: "改名",
		SourceType: env.SourceType, Image: env.Image, SortOrder: env.SortOrder,
	})
	mustV(t, err, "update")
	if upd.ImageCheckStatus != StatusAvailable {
		t.Fatalf("Update 后检查状态应保留 available, got %q", upd.ImageCheckStatus)
	}

	// 直接验 DB(status 不是空串)
	var status string
	mustV(t, db.QueryRow(`SELECT image_check_status FROM build_envs WHERE id=?`, env.ID).Scan(&status), "db read")
	if status != StatusAvailable {
		t.Fatalf("DB 里 status 应为 available, got %q", status)
	}

	// 换镜像 → 必须重置为 unchecked
	mustV(t, repo.UpdateCheckStatus(env.ID, StatusAvailable, "", "2024-01-01T00:00:00Z"), "re-mark")
	upd2, err := svc.Update(&BuildEnv{
		ID: env.ID, Language: env.Language, Version: env.Version, DisplayName: "改名2",
		SourceType: SourceCustom, Image: "registry.example.com/x/node:20", SortOrder: env.SortOrder,
	})
	mustV(t, err, "update2")
	if upd2.ImageCheckStatus != StatusUnchecked {
		t.Fatalf("换镜像后应重置 unchecked, got %q", upd2.ImageCheckStatus)
	}
	// 重置后不能再启用
	if err := svc.SetEnabled(env.ID, true); !isCode(err, "IMAGE_NOT_CHECKED") {
		t.Fatalf("换镜像重置后启用应拒, got %v", err)
	}
}
