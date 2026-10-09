package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/target"
)

// fs_zip_test.go —— 目录流式打包与「静默即断」的契约测试。
//
// 钉的是:目录下载给出的是**能解开的完整包**(不是悄悄少一半),符号链接一律不进包,
// 读不到的文件宁可让这包解不开也不假装成功,撞条目/深度上限时如实失败,
// 以及相对落点的消毒规则(路径穿越、绝对路径、深层、超长)。

// zipEntries 把响应字节解成「包名 → 正文」(解不开就直接失败)。
func zipEntries(t *testing.T, data []byte) (map[string]string, []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("解包失败(响应不是完整 zip): %v", err)
	}
	out := map[string]string{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("开条目 %s: %v", f.Name, err)
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(body)
		names = append(names, f.Name)
	}
	return out, names
}

func TestFSDownloadDirectoryAsZip(t *testing.T) {
	f := setupFSAPIFull(t)

	// 造一棵有层级的树:文件、子目录、子目录里的文件,外加一个符号链接。
	f.ws.dirs["/app/sub"] = true
	f.ws.files["/app/sub/deep.txt"] = []byte("deep-body")
	f.ws.links["/app/link-to-secret"] = true

	resp := download(t, f.client, f.srv.URL+"/api/servers/"+f.id+"/fs/download?path=/app")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("目录下载 = %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="app.zip"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	// 目录包不是靠 Content-Length 画进度的(响应是流式写出,长度只在写完才知道)。
	entries, names := zipEntries(t, body)
	for _, want := range []string{"app/", "app/a.txt", "app/b.log", "app/blob.bin", "app/sub/", "app/sub/deep.txt"} {
		if _, ok := entries[want]; !ok {
			t.Errorf("包里没有 %q(%v)", want, names)
		}
	}
	if entries["app/a.txt"] != "hello" {
		t.Errorf("app/a.txt = %q, want hello", entries["app/a.txt"])
	}
	if entries["app/sub/deep.txt"] != "deep-body" {
		t.Errorf("深层条目正文 = %q", entries["app/sub/deep.txt"])
	}
	// 符号链接不进包:跟随时整棵树能成环,不跟随又不能在包里假装它存在。
	for name := range entries {
		if strings.Contains(name, "link-to-secret") {
			t.Errorf("符号链接竟进了包: %q", name)
		}
	}
	// 目录条目必须在:空目录否则解压后压根不存在。
	if entries["app/sub/"] != "" {
		t.Errorf("目录条目不该有正文: %q", entries["app/sub/"])
	}

	var d map[string]any
	for _, e := range listAudit(t, f.rec, audit.ActionServerFS) {
		if e.Detail["op"] == "download_zip" {
			d = e.Detail
		}
	}
	if d == nil {
		t.Fatal("目录下载没留审计")
	}
	if d["path"] != "/app" || auditInt(d["entries"]) < 6 {
		t.Errorf("打包审计 = %v", d)
	}
	if auditInt(d["skippedLinks"]) != 1 {
		t.Errorf("跳过的链接数 = %v, want 1(界面上「链接没打进包」要查得到)", d["skippedLinks"])
	}
}

// 读不到的文件不许被悄悄跳过:头已经发出去了,那就让这包解不开,而不是给人一个
// 「看起来完整其实少了文件」的压缩包。
func TestFSDownloadZipFailsLoudlyOnUnreadableFile(t *testing.T) {
	f := setupFSAPIFull(t)

	resp := download(t, f.client, f.srv.URL+"/api/servers/"+f.id+"/fs/download?path=/root")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err == nil {
		t.Fatal("缺文件的包竟被解开了 —— 用户会以为目录是完整的")
	}

	var d map[string]any
	for _, e := range listAudit(t, f.rec, audit.ActionServerFS) {
		if e.Detail["op"] == "download_zip" {
			d = e.Detail
		}
	}
	if d == nil || d["error"] == nil {
		t.Fatalf("失败的打包也要留痕,得 %v", d)
	}
}

// fanoutWorkspace 一列就是 n 个零字节文件:只用来撞条目上限,不真占内存。
type fanoutWorkspace struct {
	*memWorkspace
	n int
}

func (w fanoutWorkspace) ReadDir(_ context.Context, dir string) ([]target.FileStat, error) {
	if path.Clean(dir) != "/big" {
		return nil, nil
	}
	out := make([]target.FileStat, 0, w.n)
	for i := 0; i < w.n; i++ {
		name := fmt.Sprintf("f%06d.txt", i)
		out = append(out, target.FileStat{Name: name, Path: "/big/" + name})
	}
	return out, nil
}

func (w fanoutWorkspace) OpenRead(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func TestStreamZipDirEntryCap(t *testing.T) {
	ws := fanoutWorkspace{memWorkspace: newMemWorkspace(), n: fsZipMaxEntries}
	var touched int
	// 上限是「宁可失败也不给半个包」:必须报 errZipTooMany,而不是静默收尾。
	_, err := streamZipDir(context.Background(), io.Discard, ws, "/big", func() { touched++ })
	if !errors.Is(err, errZipTooMany) {
		t.Fatalf("撞条目上限应 errZipTooMany,得 %v", err)
	}
	if touched == 0 {
		t.Error("全是零字节文件时也该算「在推进」(逐目录 touch),否则看门狗会掐掉正常打包")
	}
}

// deepWorkspace 每个目录里只有一个子目录,无限往下:只用来撞深度上限。
type deepWorkspace struct {
	*memWorkspace
}

func (w deepWorkspace) ReadDir(_ context.Context, dir string) ([]target.FileStat, error) {
	if strings.Count(dir, "/") > fsZipMaxDepth+8 {
		return nil, nil // 保险:真无限下去就把测试跑挂了
	}
	name := "n"
	return []target.FileStat{{Name: name, Path: path.Join(dir, name), IsDir: true}}, nil
}

func TestStreamZipDirDepthCap(t *testing.T) {
	ws := deepWorkspace{memWorkspace: newMemWorkspace()}
	_, err := streamZipDir(context.Background(), io.Discard, ws, "/d", func() {})
	if !errors.Is(err, errZipTooMany) {
		t.Fatalf("撞深度上限应 errZipTooMany,得 %v", err)
	}
}

func TestIdleWatchFiresOnlyOnSilence(t *testing.T) {
	var fired atomic.Int32

	// 一直有字节流动(不断 touch)就不该到期。
	w := newIdleWatch(30*time.Millisecond, func() { fired.Add(1) })
	for i := 0; i < 12; i++ {
		w.touch()
		time.Sleep(10 * time.Millisecond)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("活跃流被看门狗掐了 %d 次", got)
	}
	w.stop()
	time.Sleep(60 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("stop 之后仍回调了 %d 次", got)
	}

	// 静默到点就该拆流。
	w2 := newIdleWatch(20*time.Millisecond, func() { fired.Add(1) })
	deadline := time.Now().Add(time.Second)
	for fired.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fired.Load() == 0 {
		t.Error("静默到期没触发拆流")
	}
	w2.stop()
}

func TestMethodForKnownCompressedTypes(t *testing.T) {
	// 已压缩过的格式(镜像、压缩包、媒体)再 deflate 只烧 CPU:直接存。
	for _, name := range []string{"a.png", "B.JPG", "movie.mp4", "lib.so", "app.jar", "data.tar.gz"} {
		if got := methodFor(name); got != zip.Store {
			t.Errorf("%s 方法 = %d, want Store", name, got)
		}
	}
	if got := methodFor("app.js"); got != zip.Deflate {
		t.Errorf("文本应 deflate,得 %d", got)
	}
	if got := methodFor("noext"); got != zip.Deflate {
		t.Errorf("无扩展名应 deflate,得 %d", got)
	}
}

func TestSanitizeRemoteRel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "a.txt", want: "a.txt"},
		{in: "notes/deep/a.txt", want: "notes/deep/a.txt"},
		{in: "demo/./two.txt", want: "demo/two.txt"},
		{in: "demo//two.txt", want: "demo/two.txt"},
		{in: `win\path\a.txt`, want: "win/path/a.txt"},
		{in: ".env", want: ".env"},
		{in: "cfg/.env", want: "cfg/.env"},
		{in: "../evil", wantErr: true},
		{in: "a/../../evil", wantErr: true},
		{in: "/etc/passwd", wantErr: true},
		{in: "", wantErr: true},
		{in: ".", wantErr: true},
		{in: "./", wantErr: true},
		{in: strings.Repeat("x", 256) + ".txt", wantErr: true},
		{in: strings.Repeat("d/", fsUploadMaxDepth+1) + "a.txt", wantErr: true},
	}
	for _, c := range cases {
		got, err := sanitizeRemoteRel(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("sanitizeRemoteRel(%q) = %q, 应报错", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("sanitizeRemoteRel(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("sanitizeRemoteRel(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// 深度刚好合规时,总长也有上限(32 段 × 128 字符 > fsPathMax)。
	segs := make([]string, fsUploadMaxDepth)
	for i := range segs {
		segs[i] = strings.Repeat("c", 128)
	}
	if _, err := sanitizeRemoteRel(strings.Join(segs, "/")); err == nil || !strings.Contains(err.Error(), "过长") {
		t.Errorf("超长相对路径应报「过长」,得 %v", err)
	}
}
