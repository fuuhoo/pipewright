package target

import (
	"errors"
	"strings"
	"testing"
)

// 兜底实现的纯逻辑单测:行协议解析、权限串折位、哨兵折领域错误。
// 这三处在真机上最容易「看着能用其实错位」,和 shell 解耦后单独钉住。

func TestParseModeString(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"rw-r--r--", 0o644},
		{"rwxr-xr-x", 0o755},
		{"rwx------", 0o700},
		{"--x--x--x", 0o111},
		{"rw-rw-rw-", 0o666},
		{"rwsr-xr-x", 0o755}, // 小写 s:特殊位 + 执行位都置了
		{"rwSr-xr-x", 0o655}, // 大写 S:特殊位置了但执行位是空的
		{"rwxr-xr-t", 0o755},
		{"rwxr-xr-T", 0o754},
		{"?????????", 0o000}, // 认不出的字符宁可显示成无权限
		{"rw-r--r", 0o000},
		{"", 0o000},
	}
	for _, tc := range cases {
		if got := parseModeString(tc.in); got != tc.want {
			t.Errorf("parseModeString(%q) = %04o, want %04o", tc.in, got, tc.want)
		}
	}
}

func TestParseStatLine(t *testing.T) {
	// 字段序:kind mode size mtime link name
	st, err := parseStatLine("f\trw-r--r--\t1234\t1700000000\t\trelease tar.tgz")
	if err != nil {
		t.Fatalf("parseStatLine: %v", err)
	}
	if st.Name != "release tar.tgz" {
		t.Errorf("Name = %q, want %q", st.Name, "release tar.tgz")
	}
	if st.Size != 1234 || st.MtimeUnix != 1700000000 || st.Mode != 0o644 {
		t.Errorf("Size/Mtime/Mode = %d/%d/%04o, want 1234/1700000000/644", st.Size, st.MtimeUnix, st.Mode)
	}
	if st.IsDir || st.IsLink {
		t.Errorf("普通文件不该带 IsDir/IsLink: %+v", st)
	}

	d, err := parseStatLine("d\tdrwxrwxrwx\t0\t1\t\tapp")
	if err != nil {
		t.Fatalf("parseStatLine(dir): %v", err)
	}
	if !d.IsDir || d.Name != "app" || d.Size != 0 {
		t.Errorf("dir 解析 = %+v, want IsDir name=app size=0", d)
	}

	l, err := parseStatLine("l\tlrwxrwxrwx\t0\t2\t/var/log\tlogs")
	if err != nil {
		t.Fatalf("parseStatLine(link): %v", err)
	}
	if !l.IsLink || l.LinkTarget != "/var/log" {
		t.Errorf("link 解析 = %+v, want IsLink + LinkTarget=/var/log", l)
	}

	for _, bad := range []string{"", "f\trw-r--r--\t1", "f\t644\t1\t2\t3"} {
		if _, err := parseStatLine(bad); err == nil {
			t.Errorf("parseStatLine(%q) 应报错(字段不足)", bad)
		}
	}
}

func TestCheckExecSentinels(t *testing.T) {
	cases := []struct {
		res  *ExecResult
		want error
	}{
		{&ExecResult{Stderr: markNotFound, ExitCode: 1}, ErrRemoteNotFound},
		{&ExecResult{Stderr: markNotPerm, ExitCode: 1}, ErrRemotePermission},
		{&ExecResult{Stderr: markNotDir, ExitCode: 1}, ErrRemoteNotDirectory},
		{&ExecResult{Stderr: markNotEmpty, ExitCode: 1}, ErrRemoteNotEmpty},
	}
	for _, tc := range cases {
		err := checkExec(tc.res)
		if !errors.Is(err, tc.want) {
			t.Errorf("checkExec(%q) = %v, want %v", tc.res.Stderr, err, tc.want)
		}
	}

	// 无哨兵但非零退出:回通用失败 —— 既不能当成功,也不能误判成「不存在」。
	if err := checkExec(&ExecResult{Stderr: "sh: mv: not found", ExitCode: 127}); err == nil ||
		errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("127 应报通用失败,得到 %v", err)
	}
	if err := checkExec(&ExecResult{Stdout: "ok"}); err != nil {
		t.Errorf("零退出应无错,得到 %v", err)
	}
	if err := checkExec(nil); err == nil {
		t.Error("nil 结果应报错")
	}
}

// 哨兵之间必须互不包含:checkExec 用 strings.Contains 判定,互相命中就会把
// 「权限不足」报成「不存在」。
func TestSentinelsAreDistinct(t *testing.T) {
	all := []string{markNotFound, markNotDir, markNotPerm, markNotEmpty}
	for i, a := range all {
		for j, b := range all {
			if i != j && strings.Contains(a, b) {
				t.Errorf("哨兵 %q 包含 %q,错误映射会串", a, b)
			}
		}
	}
}
