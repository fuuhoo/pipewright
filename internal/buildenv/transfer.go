// transfer.go — 构建环境整表导入/导出(v6.2 §3.1 之外的运维能力)。
//
// 文件形态:version + buildEnvs 列表,字段与 HTTP DTO 同名(camelCase),
// **不含 credential_id** —— 凭据是密文引用,跨实例搬运只会指向不存在的行;
// 也不含 image_check_*(检查结论由本机 docker/registry 决定,导过去毫无意义)。
//
// 导入语义:
//   - 匹配键是 (language, version),与库里 UNIQUE 一致;
//   - mode=skip(默认)保留现有行,mode=overwrite 原地更新(保住 id,流水线引用不断);
//   - overwrite 不动已绑定的 credentialId(文件里没有这个概念);
//   - 文件里写 enabled: true 只表达意图,最终仍走 P0 #4 三态门:
//     未检查/不可用 → 保持禁用并在结果里说明原因。
package buildenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// 导入/导出文件格式。
const (
	TransferFormatYAML = "yaml"
	TransferFormatJSON = "json"
)

// TransferVersion 文件结构版本;导入只接受 ≤ 该值。
const TransferVersion = 1

// 冲突处理策略。
const (
	ImportModeSkip      = "skip"
	ImportModeOverwrite = "overwrite"
)

// 每行处理动作(dryRun 为计划值,实际应用为过去式)。
const (
	PlanActionCreate  = "create"
	PlanActionUpdate  = "update"
	PlanActionSkip    = "skip"
	PlanActionInvalid = "invalid"

	ResultActionCreated = "created"
	ResultActionUpdated = "updated"
	ResultActionSkipped = "skipped"
	ResultActionFailed  = "failed"
)

// TransferItem 是文件里的一条构建环境(可移植字段集;凭据与检查态不在其中)。
type TransferItem struct {
	Language    string `yaml:"language" json:"language"`
	Version     string `yaml:"version" json:"version"`
	DisplayName string `yaml:"displayName" json:"displayName"`
	Description string `yaml:"description" json:"description"`
	SourceType  string `yaml:"sourceType" json:"sourceType"`
	Image       string `yaml:"image" json:"image"`
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	SortOrder   int    `yaml:"sortOrder" json:"sortOrder"`
}

// TransferDoc 是文件顶层结构。
type TransferDoc struct {
	Version   int            `yaml:"version" json:"version"`
	BuildEnvs []TransferItem `yaml:"buildEnvs" json:"buildEnvs"`
}

// LangVersionKey 把 (language, version) 拼成对照键(库里 UNIQUE 键,大小写敏感)。
func LangVersionKey(language, version string) string {
	return language + "\x00" + version
}

// toEnv 把文件条目转成领域实体;时间/检查态由调用方与领域层补齐。
func (i TransferItem) toEnv() *BuildEnv {
	return &BuildEnv{
		Language:    i.Language,
		Version:     i.Version,
		DisplayName: i.DisplayName,
		Description: i.Description,
		SourceType:  i.SourceType,
		Image:       i.Image,
		SortOrder:   i.SortOrder,
	}
}

// normalize 去空白并在 sourceType 缺省时补 official;返回自身副本,便于纯函数测试。
func (i TransferItem) normalize() TransferItem {
	i.Language = strings.TrimSpace(i.Language)
	i.Version = strings.TrimSpace(i.Version)
	i.DisplayName = strings.TrimSpace(i.DisplayName)
	i.Description = strings.TrimSpace(i.Description)
	i.SourceType = strings.TrimSpace(i.SourceType)
	i.Image = strings.TrimSpace(i.Image)
	if i.SourceType == "" {
		i.SourceType = SourceOfficial
	}
	return i
}

// checkRequired 复刻 BuildEnv.Validate 的必填/枚举要求,返回人读原因(""=通过)。
// 与领域校验分开写,是因为导入要把原因落到「这一行」上,而不是整单 400。
func (i TransferItem) checkRequired() string {
	switch {
	case i.Language == "":
		return "language 必填"
	case i.Version == "":
		return "version 必填"
	case i.DisplayName == "":
		return "displayName 必填"
	case i.Image == "":
		return "image 必填"
	case i.SourceType != SourceOfficial && i.SourceType != SourceCustom:
		return "sourceType 只能是 official 或 custom"
	}
	return ""
}

// Marshal 序列化整表为导出文件内容(yaml / json)。
func Marshal(items []TransferItem, format string) ([]byte, error) {
	doc := TransferDoc{Version: TransferVersion, BuildEnvs: items}
	if doc.BuildEnvs == nil {
		doc.BuildEnvs = []TransferItem{}
	}
	switch format {
	case TransferFormatYAML:
		out, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("buildenv: marshal yaml: %w", err)
		}
		return out, nil
	case TransferFormatJSON:
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("buildenv: marshal json: %w", err)
		}
		return append(out, '\n'), nil
	default:
		return nil, fmt.Errorf("%w: 不支持的格式 %q,只能用 yaml 或 json", ErrInvalidInput, format)
	}
}

// ParseTransfer 解析导入内容;未知字段一律报错(手写文件里 display_name 这类
// 拼错若被静默忽略,会伪装成「displayName 必填」,更难查)。
func ParseTransfer(content, format string) ([]TransferItem, error) {
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("%w: 导入内容为空", ErrInvalidInput)
	}
	var doc TransferDoc
	switch format {
	case TransferFormatJSON:
		dec := json.NewDecoder(strings.NewReader(content))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %w", unwrapDecodeErr(err))
		}
	case TransferFormatYAML:
		dec := yaml.NewDecoder(strings.NewReader(content))
		dec.KnownFields(true)
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w: 文件里没有内容", ErrInvalidInput)
			}
			return nil, fmt.Errorf("YAML 解析失败: %w", unwrapDecodeErr(err))
		}
		// 允许单文档文件末尾跟一个「...」;第二个文档直接拒绝,避免只导了一半。
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: 只支持单个 YAML 文档", ErrInvalidInput)
		}
	default:
		return nil, fmt.Errorf("%w: 不支持的格式 %q,只能用 yaml 或 json", ErrInvalidInput, format)
	}
	if doc.Version > TransferVersion {
		return nil, fmt.Errorf("%w: 文件版本 %d 高于当前支持版本 %d",
			ErrInvalidInput, doc.Version, TransferVersion)
	}
	if len(doc.BuildEnvs) == 0 {
		return nil, fmt.Errorf("%w: 文件里没有 buildEnvs 条目", ErrInvalidInput)
	}
	if len(doc.BuildEnvs) > maxTransferItems {
		return nil, fmt.Errorf("%w: 一次最多导入 %d 条(当前 %d 条)",
			ErrInvalidInput, maxTransferItems, len(doc.BuildEnvs))
	}
	return doc.BuildEnvs, nil
}

const maxTransferItems = 500

// TransferMessage 从导入/导出错误里取出给用户看的那句:
// 哨兵 ErrInvalidInput 的 Error() 是英文前缀("buildenv: invalid input"),
// 包装后的完整消息形如 "buildenv: invalid input: 导入内容为空",这里剥掉前缀。
func TransferMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if prefix := ErrInvalidInput.Error(); strings.HasPrefix(msg, prefix) {
		msg = strings.TrimSpace(strings.TrimPrefix(msg, prefix))
		msg = strings.TrimSpace(strings.TrimPrefix(msg, ":"))
	}
	if msg == "" {
		return "导入内容不合法"
	}
	return msg
}

// unwrapDecodeErr 去掉 encoding/json 与 yaml 包装里的类型名前缀噪声,保留字段路径。
func unwrapDecodeErr(err error) error {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return errors.New("内容格式不正确")
	}
	return errors.New(msg)
}

// PlanItem 是「这一行打算怎么处理」的计划(dryRun 直接展示它)。
type PlanItem struct {
	Index    int
	Item     TransferItem
	Existing *BuildEnv
	Action   string
	Reason   string
}

// Plan 对照现有行算出每行的处理策略;纯函数,不做 I/O。
// existing 的键由 LangVersionKey(language, version) 构造 —— 调用方从
// List(IncludeDisabled:true) 得到全部行(含禁用,否则禁用行会被当新条目重复插入)。
func Plan(items []TransferItem, existing map[string]*BuildEnv, mode string) []PlanItem {
	if mode == "" {
		mode = ImportModeSkip
	}
	if mode != ImportModeSkip && mode != ImportModeOverwrite {
		mode = ImportModeSkip
	}
	out := make([]PlanItem, 0, len(items))
	seen := map[string]int{}
	for idx, raw := range items {
		item := raw.normalize()
		key := LangVersionKey(item.Language, item.Version)
		reason := item.checkRequired()
		if reason != "" {
			out = append(out, PlanItem{Index: idx, Item: item, Action: PlanActionInvalid, Reason: reason})
			continue
		}
		if first, dup := seen[key]; dup {
			out = append(out, PlanItem{
				Index: idx, Item: item, Action: PlanActionInvalid,
				Reason: fmt.Sprintf("文件内重复:与第 %d 行同为 %s/%s", first+1, item.Language, item.Version),
			})
			continue
		}
		seen[key] = idx

		old := existing[key]
		switch {
		case old == nil:
			out = append(out, PlanItem{Index: idx, Item: item, Action: PlanActionCreate,
				Reason: enableReason(item)})
		case mode == ImportModeSkip:
			out = append(out, PlanItem{Index: idx, Item: item, Existing: old, Action: PlanActionSkip,
				Reason: fmt.Sprintf("已存在(第 %d 行同 %s/%s),按「跳过」策略保留现有配置", idx+1, item.Language, item.Version)})
		default:
			plan := PlanItem{Index: idx, Item: item, Existing: old, Action: PlanActionUpdate,
				Reason: transferUpdateReason(old, item)}
			out = append(out, plan)
		}
	}
	return out
}

// enableReason 说明「文件里想启用」最终会怎么落地(三态门仍然守着)。
func enableReason(item TransferItem) string {
	if !item.Enabled {
		return ""
	}
	return "导入后会检查镜像可用性,可用则自动启用"
}

// transferUpdateReason 把覆盖更新的后果写清楚(检查态、凭据、启用意图)。
func transferUpdateReason(old *BuildEnv, item TransferItem) string {
	notes := []string{}
	if old.Image != item.Image || old.SourceType != item.SourceType {
		notes = append(notes, "镜像来源变化,检查结论重置为未检查")
	}
	if old.CredentialID != "" {
		notes = append(notes, "保留现有凭据引用(不随导出传递)")
	}
	if r := enableReason(item); r != "" {
		notes = append(notes, r)
	}
	return strings.Join(notes, ";")
}

// ImportRowResult 是一行的处理结果(前端表格直接渲染)。
type ImportRowResult struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Version  string `json:"version"`
	Action   string `json:"action"`
	Enabled  bool   `json:"enabled"`
	ID       string `json:"id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ImportSummary 是汇总计数。
type ImportSummary struct {
	Total   int `json:"total"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

// ImportReport 是导入响应体。
type ImportReport struct {
	DryRun  bool              `json:"dryRun"`
	Mode    string            `json:"mode"`
	Summary ImportSummary     `json:"summary"`
	Results []ImportRowResult `json:"results"`
}

// Import 执行导入:dryRun 只出计划,落库路径逐行 create/update 并过三态门。
// checker 为 nil 时不做镜像检查,想启用的新行保持禁用并说明原因。
func (s *Service) Import(ctx context.Context, items []TransferItem, mode string, dryRun bool) (*ImportReport, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: 导入条目为空", ErrInvalidInput)
	}
	if mode == "" {
		mode = ImportModeSkip
	}
	envs, err := s.repo.List(ListFilter{IncludeDisabled: true})
	if err != nil {
		return nil, err
	}
	existing := make(map[string]*BuildEnv, len(envs))
	for _, e := range envs {
		existing[LangVersionKey(e.Language, e.Version)] = e
	}

	report := &ImportReport{DryRun: dryRun, Mode: mode}
	report.Summary.Total = len(items)
	for _, plan := range Plan(items, existing, mode) {
		row := ImportRowResult{
			Index:    plan.Index,
			Language: plan.Item.Language,
			Version:  plan.Item.Version,
			Enabled:  plan.Item.Enabled,
		}
		if dryRun {
			row.Action = planActionToResult(plan.Action)
			row.Reason = plan.Reason
			switch row.Action {
			case ResultActionCreated:
				report.Summary.Created++
			case ResultActionUpdated:
				report.Summary.Updated++
			case ResultActionSkipped:
				report.Summary.Skipped++
			default:
				report.Summary.Failed++
			}
			report.Results = append(report.Results, row)
			continue
		}

		switch plan.Action {
		case PlanActionInvalid:
			row.Action = ResultActionFailed
			row.Enabled = false
			row.Reason = plan.Reason
			report.Summary.Failed++
		case PlanActionSkip:
			row.Action = ResultActionSkipped
			row.Enabled = plan.Existing.Enabled
			row.ID = plan.Existing.ID
			row.Reason = plan.Reason
			report.Summary.Skipped++
		default:
			out, reason, err := s.applyPlan(ctx, plan)
			if err != nil {
				row.Action = ResultActionFailed
				row.Enabled = false
				row.Reason = humanError(err)
				report.Summary.Failed++
				if out != nil {
					row.ID = out.ID
				}
			} else {
				row.Action = ResultActionCreated
				if plan.Action == PlanActionUpdate {
					row.Action = ResultActionUpdated
					report.Summary.Updated++
				} else {
					report.Summary.Created++
				}
				row.ID = out.ID
				row.Enabled = out.Enabled
				row.Reason = reason
				if !out.Enabled && plan.Item.Enabled && reason == "" {
					row.Reason = "保持禁用"
				}
			}
		}
		report.Results = append(report.Results, row)
	}
	return report, nil
}

// applyPlan 落一行:create/update 都以 enabled=false 写库(绕过不了三态门),
// 再按文件的启用意图走 SetEnabled 门;门挡住且有 checker 就检查一次再争取一轮。
// 返回(落库后的行, 人读原因, 错误)。
func (s *Service) applyPlan(ctx context.Context, plan PlanItem) (*BuildEnv, string, error) {
	env := plan.Item.toEnv()
	notes := []string{}
	var (
		out *BuildEnv
		err error
	)
	if plan.Action == PlanActionUpdate {
		// 原地更新:沿用现有行 id(流水线 buildEnvId 引用不能断);
		// 凭据引用不属于文件,原样保住;Update 按镜像是否变化决定重置检查态。
		env.ID = plan.Existing.ID
		env.CredentialID = plan.Existing.CredentialID
		out, err = s.Update(env)
		if plan.Existing.CredentialID != "" {
			notes = append(notes, "凭据引用保持不变")
		}
	} else {
		out, err = s.Create(env)
	}
	if err != nil {
		return nil, "", err
	}
	if !plan.Item.Enabled {
		return out, strings.Join(notes, ";"), nil
	}

	if setErr := s.SetEnabled(out.ID, true); setErr != nil {
		var ve *ValidationError
		if !errors.As(setErr, &ve) {
			return out, "", setErr
		}
		// 未检查 → 有机会靠一次真检查翻盘;不可用 → 直接放弃启用。
		if s.checker != nil && ve.Code == "IMAGE_NOT_CHECKED" {
			res, chkErr := s.checker.Check(ctx, out)
			if chkErr != nil {
				notes = append(notes, "镜像检查失败:"+humanError(chkErr))
			} else if res.Status == StatusAvailable || res.Status == StatusPullable {
				if enableErr := s.SetEnabled(out.ID, true); enableErr == nil {
					out.Enabled = true
					notes = append(notes, "镜像可用,已启用")
				} else {
					notes = append(notes, "检查通过但启用失败:"+humanError(enableErr))
				}
			} else {
				notes = append(notes, "镜像不可用,保持禁用:"+res.Error)
			}
		} else {
			notes = append(notes, ve.Message)
		}
		out, getErr := s.repo.GetByID(out.ID)
		if getErr != nil {
			return nil, "", getErr
		}
		return out, strings.Join(notes, ";"), nil
	}
	out.Enabled = true
	notes = append(notes, "已启用")
	return out, strings.Join(notes, ";"), nil
}

func planActionToResult(action string) string {
	switch action {
	case PlanActionCreate:
		return ResultActionCreated
	case PlanActionUpdate:
		return ResultActionUpdated
	case PlanActionSkip:
		return ResultActionSkipped
	default:
		return ResultActionFailed
	}
}

// humanError 提取错误的人读部分:wrappedErr 的 message 已在最前,直接用 Error();
// JSON/YAML 解码错误去掉类型前缀噪声由 unwrapDecodeErr 负责。
func humanError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "未知错误"
	}
	return msg
}

// ExportItems 取整表(可含禁用)并转成文件条目;顺序沿用 repo 的 sort_order 排序。
func (s *Service) ExportItems(includeDisabled bool) ([]TransferItem, error) {
	envs, err := s.repo.List(ListFilter{IncludeDisabled: includeDisabled})
	if err != nil {
		return nil, err
	}
	out := make([]TransferItem, 0, len(envs))
	for _, e := range envs {
		out = append(out, TransferItem{
			Language:    e.Language,
			Version:     e.Version,
			DisplayName: e.DisplayName,
			Description: e.Description,
			SourceType:  e.SourceType,
			Image:       e.Image,
			Enabled:     e.Enabled,
			SortOrder:   e.SortOrder,
		})
	}
	return out, nil
}
