// 构建环境导入/导出的单测:纯逻辑(Plan)+ 编解码(Marshal/ParseTransfer)+ 落库(Service.Import)。
// 复用 buildenv_test.go 里的 sharedDB/newService 夹具(同一测试包)。
package buildenv_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/buildenv"
)

func item(lang, ver, image string) buildenv.TransferItem {
	return buildenv.TransferItem{
		Language:    lang,
		Version:     ver,
		DisplayName: lang + " " + ver,
		SourceType:  buildenv.SourceOfficial,
		Image:       image,
	}
}

// existingOf 构造 Plan 用的对照表(纯函数测试不需要真库)。
func existingOf(items ...buildenv.TransferItem) map[string]*buildenv.BuildEnv {
	out := map[string]*buildenv.BuildEnv{}
	for _, it := range items {
		env := &buildenv.BuildEnv{
			Language: it.Language, Version: it.Version, DisplayName: it.DisplayName,
			Description: it.Description, SourceType: it.SourceType, Image: it.Image,
			SortOrder: it.SortOrder,
		}
		out[buildenv.LangVersionKey(env.Language, env.Version)] = env
	}
	return out
}

// TestMarshalParseRoundTrip 两种格式都能原样回读,且文件里没有凭据/检查态字段。
func TestMarshalParseRoundTrip(t *testing.T) {
	items := []buildenv.TransferItem{
		{Language: "nodejs", Version: "22", DisplayName: "Node 22", Description: "LTS",
			SourceType: buildenv.SourceOfficial, Image: "node:22-bookworm", Enabled: true, SortOrder: 10},
		{Language: "java", Version: "11-amzn", DisplayName: "Java 11",
			SourceType: buildenv.SourceCustom, Image: "registry.example.com/java:11", SortOrder: 20},
	}
	for _, format := range []string{buildenv.TransferFormatYAML, buildenv.TransferFormatJSON} {
		content, err := buildenv.Marshal(items, format)
		if err != nil {
			t.Fatalf("%s 导出: %v", format, err)
		}
		text := string(content)
		for _, banned := range []string{"credential", "imageCheck", "image_check", "createdBy", "id:"} {
			if strings.Contains(text, banned) {
				t.Fatalf("%s 导出不应含 %q:\n%s", format, banned, text)
			}
		}
		back, err := buildenv.ParseTransfer(text, format)
		if err != nil {
			t.Fatalf("%s 回读: %v", format, err)
		}
		if len(back) != len(items) {
			t.Fatalf("条目数不符: got %d want %d", len(back), len(items))
		}
		if back[0].Image != "node:22-bookworm" || !back[0].Enabled || back[0].SortOrder != 10 {
			t.Fatalf("首条字段丢失: %+v", back[0])
		}
		if back[1].SourceType != buildenv.SourceCustom || back[1].Enabled {
			t.Fatalf("次条字段不符: %+v", back[1])
		}
	}
}

// TestParseTransferRejectsBadInput 覆盖:空内容、未知字段、高版本、多文档、坏格式。
func TestParseTransferRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		content string
		format  string
		want    string
	}{
		{"空内容", "   ", buildenv.TransferFormatYAML, "导入内容为空"},
		{"未知字段", "version: 1\nbuildEnvs:\n  - language: nodejs\n    version: \"22\"\n    display_name: Node 22\n", buildenv.TransferFormatYAML, "display_name"},
		{"JSON 未知字段", `{"version":1,"buildEnvs":[{"language":"nodejs","version":"22","displayName":"n","image":"i","oops":1}]}`, buildenv.TransferFormatJSON, "oops"},
		{"版本过高", "version: 99\nbuildEnvs:\n  - language: nodejs\n    version: \"22\"\n    displayName: Node\n    image: node:22\n", buildenv.TransferFormatYAML, "高于当前支持版本"},
		{"多文档", "version: 1\nbuildEnvs:\n  - language: nodejs\n    version: \"22\"\n    displayName: Node\n    image: node:22\n---\nversion: 1\nbuildEnvs: []\n", buildenv.TransferFormatYAML, "单个 YAML 文档"},
		{"坏格式名", "whatever", "toml", "不支持的格式"},
		{"语法错", "version: 1\nbuildEnvs:\n\t- language: x\n", buildenv.TransferFormatYAML, "YAML 解析失败"},
	}
	for _, c := range cases {
		_, err := buildenv.ParseTransfer(c.content, c.format)
		if err == nil {
			t.Fatalf("%s: 应报错", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 错误信息应含 %q,实际 %v", c.name, c.want, err)
		}
	}
}

// TestPlanDecidesPerMode 纯函数:新建/跳过/覆盖/文件内重复/必填缺失。
func TestPlanDecidesPerMode(t *testing.T) {
	exist := existingOf(item("java", "11", "eclipse-temurin:11-jdk"))

	items := []buildenv.TransferItem{
		item("nodejs", "22", "node:22"),                           // 库里没有 → create
		item("java", "11", "eclipse-temurin:11"),                  // 库里已有
		item("java", "11", "temurin:11"),                          // 文件内重复
		{Language: "go", Version: "1.22", SourceType: "official"}, // 缺 displayName/image
		{Language: "py", Version: "3.12", DisplayName: "Py", Image: "python:3.12", SourceType: "weird"},
	}

	skip := buildenv.Plan(items, exist, buildenv.ImportModeSkip)
	if len(skip) != len(items) {
		t.Fatalf("行数不符: %d", len(skip))
	}
	wantSkip := []string{
		buildenv.PlanActionCreate, buildenv.PlanActionSkip,
		buildenv.PlanActionInvalid, buildenv.PlanActionInvalid, buildenv.PlanActionInvalid,
	}
	for i, want := range wantSkip {
		if skip[i].Action != want {
			t.Fatalf("第 %d 行 action: got %q want %q(%s)", i, skip[i].Action, want, skip[i].Reason)
		}
	}
	if !strings.Contains(skip[3].Reason, "displayName") {
		t.Fatalf("缺字段的行要说清缺什么: %q", skip[3].Reason)
	}
	if !strings.Contains(skip[4].Reason, "sourceType") {
		t.Fatalf("枚举不符的行要说清: %q", skip[4].Reason)
	}
	if !strings.Contains(skip[2].Reason, "文件内重复") {
		t.Fatalf("重复行要说清与第几行重复: %q", skip[2].Reason)
	}

	over := buildenv.Plan(items[:2], exist, buildenv.ImportModeOverwrite)
	if over[1].Action != buildenv.PlanActionUpdate {
		t.Fatalf("覆盖模式第二行应为 update: %q", over[1].Action)
	}
	if !strings.Contains(over[1].Reason, "重置为未检查") {
		t.Fatalf("改镜像要提示检查态失效: %q", over[1].Reason)
	}

	// sourceType 缺省补 official;空 mode 等同 skip。
	dflt := buildenv.Plan([]buildenv.TransferItem{{Language: "go", Version: "1.22", DisplayName: "Go", Image: "golang:1.22"}}, nil, "")
	if dflt[0].Action != buildenv.PlanActionCreate || dflt[0].Item.SourceType != buildenv.SourceOfficial {
		t.Fatalf("默认值处理不符: %+v", dflt[0])
	}
}

// TestServiceImportDryRunWritesNothing 预览不落库。
func TestServiceImportDryRunWritesNothing(t *testing.T) {
	svc := newService(t)
	items := []buildenv.TransferItem{item("nodejs", "22", "node:22")}

	report, err := svc.Import(context.Background(), items, buildenv.ImportModeSkip, true)
	if err != nil {
		t.Fatalf("dryRun: %v", err)
	}
	if !report.DryRun || report.Summary.Total != 1 || report.Results[0].Action != buildenv.ResultActionCreated {
		t.Fatalf("预览结果不符: %+v", report)
	}
	if n := countEnvs(t, svc); n != 0 {
		t.Fatalf("dryRun 不该写库,现有 %d 行", n)
	}
}

// TestServiceImportSkipKeepsExisting 跳过模式保住现有行(id、凭据引用都不动)。
func TestServiceImportSkipKeepsExisting(t *testing.T) {
	svc := newService(t)
	old := mustCreateWithCred(t, svc, item("java", "11", "eclipse-temurin:11-jdk"), "cred-keep")

	report, err := svc.Import(context.Background(),
		[]buildenv.TransferItem{{Language: "java", Version: "11", DisplayName: "完全不同的名字",
			SourceType: buildenv.SourceCustom, Image: "attacker.example.com/x:1"}},
		buildenv.ImportModeSkip, false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.Summary.Skipped != 1 || report.Results[0].ID != old.ID {
		t.Fatalf("应为 skipped 且回带现有 id: %+v", report.Results[0])
	}
	got := mustGet(t, svc, old.ID)
	if got.DisplayName != old.DisplayName || got.Image != old.Image || got.CredentialID != "cred-keep" {
		t.Fatalf("跳过模式改动了现有行: %+v", got)
	}
}

// TestServiceImportOverwriteKeepsCredentialAndID 覆盖模式保住 id 与凭据引用;
// 紧急放行开关下,文件的启用意图能直接落库。
func TestServiceImportOverwriteKeepsCredentialAndID(t *testing.T) {
	t.Setenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE", "true")
	svc := newService(t)
	old := mustCreateWithCred(t, svc, item("java", "11", "eclipse-temurin:11-jdk"), "cred-x")

	updated := item("java", "11", "eclipse-temurin:11-jdk")
	updated.DisplayName = "Java 11 (Amazon)"
	updated.Description = "改过的描述"
	updated.Enabled = true
	report, err := svc.Import(context.Background(), []buildenv.TransferItem{updated}, buildenv.ImportModeOverwrite, false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.Summary.Updated != 1 || report.Results[0].ID != old.ID {
		t.Fatalf("应为 updated 且 id 不变(流水线引用不断): %+v", report.Results[0])
	}
	if !report.Results[0].Enabled {
		t.Fatalf("开关放行时文件的启用意图应落地: %+v", report.Results[0])
	}
	got := mustGet(t, svc, old.ID)
	if got.DisplayName != "Java 11 (Amazon)" || got.Description != "改过的描述" {
		t.Fatalf("覆盖更新未落库: %+v", got)
	}
	if got.CredentialID != "cred-x" {
		t.Fatalf("凭据引用应保住: %q", got.CredentialID)
	}
}

// TestServiceImportEnabledRespectsGate 文件想启用,但无 checker 时三态门必须挡住(P0 #4)。
func TestServiceImportEnabledRespectsGate(t *testing.T) {
	svc := newService(t)
	it := item("nodejs", "24", "node:24")
	it.Enabled = true

	report, err := svc.Import(context.Background(), []buildenv.TransferItem{it}, buildenv.ImportModeSkip, false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	row := report.Results[0]
	if row.Action != buildenv.ResultActionCreated || row.Enabled {
		t.Fatalf("未检查的镜像不得被导入直接启用: %+v", row)
	}
	if !strings.Contains(row.Reason, "未检查") {
		t.Fatalf("要说明为什么没启用: %q", row.Reason)
	}
	if got := mustGet(t, svc, row.ID); got.Enabled {
		t.Fatalf("库里也不该是启用态: %+v", got)
	}
}

// TestServiceImportReportsFailureRows 坏行记 failed,好行照常入库(不因一行的错整单失败)。
func TestServiceImportReportsFailureRows(t *testing.T) {
	svc := newService(t)
	items := []buildenv.TransferItem{
		{Language: "", Version: "1", DisplayName: "x", Image: "y"},
		item("python", "3.12", "python:3.12"),
	}
	report, err := svc.Import(context.Background(), items, buildenv.ImportModeSkip, false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.Summary.Failed != 1 || report.Summary.Created != 1 {
		t.Fatalf("汇总不符: %+v", report.Summary)
	}
	if report.Results[0].Action != buildenv.ResultActionFailed || report.Results[1].Action != buildenv.ResultActionCreated {
		t.Fatalf("行结果不符: %+v", report.Results)
	}
	if n := countEnvs(t, svc); n != 1 {
		t.Fatalf("只应入库 1 行,实际 %d", n)
	}
}

// TestExportItemsRoundTripsThroughService 导出条目直接可被导入解析(自产自销)。
func TestExportItemsRoundTripsThroughService(t *testing.T) {
	t.Setenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE", "true")
	svc := newService(t)
	enabled := mustCreateWithCred(t, svc, item("nodejs", "22", "node:22"), "cred-a")
	if err := svc.SetEnabled(enabled.ID, true); err != nil {
		t.Fatalf("夹具启用: %v", err)
	}
	created := mustCreateWithCred(t, svc, item("java", "8", "eclipse-temurin:8-jdk"), "")
	if err := svc.SetEnabled(created.ID, false); err != nil {
		t.Fatalf("夹具禁用: %v", err)
	}

	only, err := svc.ExportItems(false)
	if err != nil {
		t.Fatalf("导出(仅启用): %v", err)
	}
	if len(only) != 1 || only[0].Language != "nodejs" {
		t.Fatalf("仅启用导出不符: %+v", only)
	}
	all, err := svc.ExportItems(true)
	if err != nil {
		t.Fatalf("导出(含禁用): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("含禁用导出应有 2 条: %+v", all)
	}
	content, err := buildenv.Marshal(all, buildenv.TransferFormatYAML)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := buildenv.ParseTransfer(string(content), buildenv.TransferFormatYAML)
	if err != nil {
		t.Fatalf("回读: %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("回读条目不符: %+v", back)
	}
}

func countEnvs(t *testing.T, svc *buildenv.Service) int {
	t.Helper()
	envs, err := svc.List(buildenv.ListFilter{IncludeDisabled: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(envs)
}

func mustCreateWithCred(t *testing.T, svc *buildenv.Service, it buildenv.TransferItem, credID string) *buildenv.BuildEnv {
	t.Helper()
	env := &buildenv.BuildEnv{
		Language: it.Language, Version: it.Version, DisplayName: it.DisplayName,
		Description: it.Description, SourceType: it.SourceType, Image: it.Image,
		CredentialID: credID, SortOrder: it.SortOrder,
	}
	out, err := svc.Create(env)
	if err != nil {
		t.Fatalf("create %s/%s: %v", it.Language, it.Version, err)
	}
	return out
}

func mustGet(t *testing.T, svc *buildenv.Service, id string) *buildenv.BuildEnv {
	t.Helper()
	got, err := svc.GetByID(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if got == nil {
		t.Fatalf("get %s: 不存在", id)
	}
	return got
}
