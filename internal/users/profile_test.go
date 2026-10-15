package users

// 本地档案(display_name / email,迁移 0069)的行为锁定。
//
// 这三个语义是这次改动全部的复杂度所在,少了任何一条都会变成 bug:
//   - 首次建号写入,之后以本地值为准(identity 走 FillProfileIfEmpty 而不是覆盖);
//   - 两个字段各自独立判定「空则填」,平台只给得出其中一个时另一个也能补上;
//   - 归一化按**字符**截断,好让 Go 侧的截断和迁移里的 VARCHAR 宽度对上。

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

const profileHash = "phc_placeholder"

func TestCreateWritesNormalizedProfile(t *testing.T) {
	svc := NewService(storetest.OpenDB(t))

	u, err := svc.Create(CreateInput{
		Username:     "alice",
		PasswordHash: profileHash,
		DisplayName:  "  傅   兵\n测试 ",
		Email:        " Alice@Example.COM ",
	})
	mustV(t, err, "create")
	if u.DisplayName != "傅 兵 测试" {
		t.Fatalf("显示名应折叠空白并去首尾: %q", u.DisplayName)
	}
	if u.Email != "Alice@Example.COM" {
		t.Fatalf("邮箱只该去首尾空白(大小写留给用户): %q", u.Email)
	}

	// 三条读路径都要能带回这两列 —— 少一列就会变成「列表页有昵称、详情页没有」。
	got, err := svc.GetByID(u.ID)
	mustV(t, err, "get by id")
	if got.DisplayName != u.DisplayName || got.Email != u.Email {
		t.Fatalf("GetByID 档案失真: %+v", got)
	}
	byName, err := svc.GetByUsername("alice")
	mustV(t, err, "get by username")
	if byName.DisplayName != u.DisplayName || byName.Email != u.Email {
		t.Fatalf("GetByUsername 档案失真: %+v", byName)
	}
	list, err := svc.List(ListFilter{})
	mustV(t, err, "list")
	if len(list) != 1 || list[0].DisplayName != u.DisplayName || list[0].Email != u.Email {
		t.Fatalf("List 档案失真: %+v", list)
	}
}

func TestNormalizeDisplayNameTruncatesByRune(t *testing.T) {
	long := strings.Repeat("长", 100)
	got, err := normalizeDisplayName(long)
	mustV(t, err, "normalize")
	if len([]rune(got)) != MaxDisplayNameLen {
		t.Fatalf("应按字符截到 %d, got %d 字符 / %d 字节",
			MaxDisplayNameLen, len([]rune(got)), len(got))
	}
	if got != strings.Repeat("长", MaxDisplayNameLen) {
		t.Fatalf("截断内容错: %q", got)
	}
}

func TestNormalizeEmail(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},                // 留空合法:档案邮箱不是必填
		{in: "  a@b.co ", want: "a@b.co"}, // 只剪首尾
		{in: "a@b", want: "a@b"},          // 不做完整 RFC 校验(平台常回填这种)
		{in: "a b@c.co", wantErr: true},   // 中间空格 = 不是邮箱
		{in: "a\tb@c.co", wantErr: true},  // 换行/制表也算空白
		{in: "@b.co", wantErr: true},      // 本地部分为空
		{in: "a@", wantErr: true},         // 域名为空
		{in: "a@b@c.co", wantErr: true},   // 多个 @
		{in: strings.Repeat("a", MaxEmailLen-3) + "@b", want: strings.Repeat("a", MaxEmailLen-3) + "@b"},
		{in: strings.Repeat("a", MaxEmailLen) + "@b", wantErr: true}, // 超上限
	} {
		got, err := normalizeEmail(tc.in)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("%q 应被拒", tc.in)
		case !tc.wantErr && err != nil:
			t.Errorf("%q 不该被拒: %v", tc.in, err)
		case !tc.wantErr && got != tc.want:
			t.Errorf("%q 归一化为 %q, want %q", tc.in, got, tc.want)
		}
		if err != nil && !errors.Is(err, ErrValidation) {
			t.Errorf("%q 的错误应包 ErrValidation: %v", tc.in, err)
		}
	}
}

func TestSetProfile(t *testing.T) {
	svc := NewService(storetest.OpenDB(t))
	u, err := svc.Create(CreateInput{
		Username: "bob", PasswordHash: profileHash, DisplayName: "小鲍", Email: "bob@x.co",
	})
	mustV(t, err, "create")

	// 清空是合法操作(前端要能把昵称退回用户名展示)。
	mustV(t, svc.SetProfile(u.ID, "", ""), "clear")
	got, err := svc.GetByID(u.ID)
	mustV(t, err, "get")
	if got.DisplayName != "" || got.Email != "" {
		t.Fatalf("应已清空: %+v", got)
	}

	// 坏邮箱要拒,而且不能把已经写进去的显示名留下(两列一条 UPDATE,要么都改要么都不改)。
	if err := svc.SetProfile(u.ID, "临时名", "not-an-email"); err == nil {
		t.Fatal("坏邮箱应报错")
	}
	got, err = svc.GetByID(u.ID)
	mustV(t, err, "get after reject")
	if got.DisplayName != "" || got.Email != "" {
		t.Fatalf("校验失败却写脏了档案: %+v", got)
	}

	if err := svc.SetProfile("no-such-id", "n", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id 应 ErrNotFound: %v", err)
	}
}

// TestFillProfileIfEmptyIsTheThreeStateBit 锁住「回填」这条路上最容易写错的部分:
// 已有的本地值不能被平台冲掉,空的必须补上,两列各判各的。
func TestFillProfileIfEmpty(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	u, err := svc.Create(CreateInput{Username: "carol", PasswordHash: profileHash})
	mustV(t, err, "create")

	// 全空 → 两个都填。
	mustV(t, svc.FillProfileIfEmpty(u.ID, "卡罗", "carol@x.co"), "fill both")
	got, err := svc.GetByID(u.ID)
	mustV(t, err, "get")
	if got.DisplayName != "卡罗" || got.Email != "carol@x.co" {
		t.Fatalf("空档案应被填上: %+v", got)
	}

	// 本地改过显示名、邮箱留空 → 只动邮箱,名字保持本地值。
	mustV(t, svc.SetProfile(u.ID, "我自己起的名字", ""), "local edit")
	mustV(t, svc.FillProfileIfEmpty(u.ID, "平台昵称", "carol@x.co"), "fill email only")
	got, err = svc.GetByID(u.ID)
	mustV(t, err, "get")
	if got.DisplayName != "我自己起的名字" {
		t.Fatalf("回填把本地显示名冲掉了: %q", got.DisplayName)
	}
	if got.Email != "carol@x.co" {
		t.Fatalf("空的邮箱该补上: %q", got.Email)
	}

	// 两列都有值 → 一条都不动,也不白刷 updated_at(那会让「最近改动」排序抖动)。
	var before string
	mustV(t, db.QueryRow(`SELECT updated_at FROM users WHERE id = ?`, u.ID).Scan(&before), "read updated_at")
	mustV(t, svc.FillProfileIfEmpty(u.ID, "又一个平台名", "other@x.co"), "no-op fill")
	var after string
	mustV(t, db.QueryRow(`SELECT updated_at, display_name, email FROM users WHERE id = ?`, u.ID).
		Scan(&after, &got.DisplayName, &got.Email), "re-read")
	if got.DisplayName != "我自己起的名字" || got.Email != "carol@x.co" {
		t.Fatalf("都有值时不该被改: %+v", got)
	}
	if after != before {
		t.Fatalf("空操作也刷了 updated_at: %q → %q", before, after)
	}

	// 平台给不出名字/邮箱(id 为空同理)→ 直接返回,不参与写库。
	mustV(t, svc.FillProfileIfEmpty("", "名字", "x@y.co"), "empty id is a no-op")
	mustV(t, svc.FillProfileIfEmpty(u.ID, "", ""), "nothing to fill")
}
