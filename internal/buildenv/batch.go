// batch.go — 构建环境的批量写(批量新增 / 批量删除)。
//
// 与整表导入(transfer.go)的分工:导入面向「一次搬一整套目录」,字段是文件里那套
// 可移植字段;批量写面向列表页上的多行操作 —— 新增要能带 credentialId(导出文件
// 里刻意不含),删除只认行标识。
//
// 两者共用同一套逐行语义:坏行只记在自己那一行上,整单不回滚,已成功的不撤销。
// 批量新增同样不能成为绕过 P0 #4 三态门的后门:新行一律先以 disabled 落库,
// 再按「想启用」的意图过门(与导入的 applyPlan 走同一个 tryEnable)。
package buildenv

import (
	"context"
	"errors"
	"fmt"
)

// MaxBatchRows 一次批量写的行数上限(与批量检查同一档;再大就该走导入/一键检查)。
const MaxBatchRows = 200

// 批量删除的行动作(新增侧沿用导入的 created/skipped/failed)。
const ResultActionDeleted = "deleted"

// BatchRowResult 是批量写的单行结果。
// 形状与导入逐行结果完全一致,前端因此能复用同一套标签、色块和表格。
type BatchRowResult = ImportRowResult

// BatchSummary 是批量写的汇总计数。
type BatchSummary struct {
	Total   int `json:"total"`
	Success int `json:"success"` // 新增=实际建行数,删除=实际删行数
	Skipped int `json:"skipped"` // 新增=「已存在所以没动」,删除=「已经不在了」
	Failed  int `json:"failed"`
}

// BatchReport 是批量写响应体。
type BatchReport struct {
	Summary BatchSummary     `json:"summary"`
	Results []BatchRowResult `json:"results"`
}

// newBatchReport 起一个总数已知的空报告;Results 预分配,逐行只 append。
func newBatchReport(total int) *BatchReport {
	return &BatchReport{
		Summary: BatchSummary{Total: total},
		Results: make([]BatchRowResult, 0, total),
	}
}

// BatchCreate 逐行新建构建环境。
//   - 校验不过 → failed(原因取中文那句,不带哨兵前缀)
//   - (language, version) 与现有行或同批前一行冲突 → skipped(现有行不动)
//   - enabled 只表达意图:行先以 disabled 落库,再走 tryEnable 过三态门,
//     没过去就保持 disabled 并在 reason 里写清原因(行仍算 created)
//
// 返回的 report 里 index 与入参下标一一对应,前端按它对齐预览表。
func (s *Service) BatchCreate(ctx context.Context, in []*BuildEnv) *BatchReport {
	report := newBatchReport(len(in))
	for idx, env := range in {
		row := BatchRowResult{Index: idx}
		if env == nil {
			row.Action = ResultActionFailed
			row.Reason = "这一条是空的"
			report.Summary.Failed++
			report.Results = append(report.Results, row)
			continue
		}
		row.Language, row.Version = env.Language, env.Version
		wantEnabled := env.Enabled
		// 启用状态一律由三态门决定,先落一条 disabled 的行。
		env.Enabled = false

		out, err := s.Create(env)
		if err != nil {
			switch {
			case errors.Is(err, ErrConflict):
				// 带回现有行 id:前端能把「已存在」这一行直接指到现有环境上。
				kept, _ := s.GetByLanguageVersion(env.Language, env.Version)
				row.Action = ResultActionSkipped
				row.Reason = fmt.Sprintf("已存在 %s/%s,这一行没有改动", env.Language, env.Version)
				if kept != nil {
					row.ID = kept.ID
				}
				report.Summary.Skipped++
			case errors.Is(err, ErrInvalidInput):
				row.Action = ResultActionFailed
				row.Reason = invalidReason(err)
				report.Summary.Failed++
			default:
				row.Action = ResultActionFailed
				row.Reason = humanError(err)
				report.Summary.Failed++
			}
			report.Results = append(report.Results, row)
			continue
		}

		row.Action = ResultActionCreated
		row.ID = out.ID
		row.Enabled = out.Enabled
		report.Summary.Success++
		if wantEnabled {
			latest, reason, enableErr := s.tryEnable(ctx, out, nil)
			if enableErr != nil {
				// 行已经建好,只是争取启用这一步出了错:仍算 created,把错误写进原因。
				row.Reason = humanError(enableErr)
			} else {
				if latest != nil {
					row.Enabled = latest.Enabled
				}
				row.Reason = reason
				if !row.Enabled && reason == "" {
					row.Reason = "保持禁用"
				}
			}
		}
		report.Results = append(report.Results, row)
	}
	return report
}

// BatchDelete 逐行删除。行不存在记 skipped(列表很可能已被别的会话动过,
// 这不是错误);其余失败记在行上,后面继续删。
//
// 删除只看行标识,不看启用状态,也不处理流水线里的 buildEnvId 引用 ——
// 与单行 DELETE 同一语义(引用失效由运行时的白名单校验报出来)。
func (s *Service) BatchDelete(ids []string) *BatchReport {
	report := newBatchReport(len(ids))
	for idx, id := range ids {
		row := BatchRowResult{Index: idx, ID: id}
		env, err := s.repo.GetByID(id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				row.Action = ResultActionSkipped
				row.Reason = "这一行已经不存在了"
				report.Summary.Skipped++
			} else {
				row.Action = ResultActionFailed
				row.Reason = humanError(err)
				report.Summary.Failed++
			}
			report.Results = append(report.Results, row)
			continue
		}
		row.Language, row.Version = env.Language, env.Version
		if err := s.repo.Delete(id); err != nil {
			row.Action = ResultActionFailed
			row.Reason = humanError(err)
			report.Summary.Failed++
			report.Results = append(report.Results, row)
			continue
		}
		row.Action = ResultActionDeleted
		report.Summary.Success++
		report.Results = append(report.Results, row)
	}
	return report
}

// invalidReason 取校验错误里给人看的那句:wrappedErr 把中文原因放在最前,
// 尾巴上的哨兵名("buildenv: invalid input")对管理员没有意义。
func invalidReason(err error) string {
	var w *wrappedErr
	if errors.As(err, &w) {
		return w.msg
	}
	return humanError(err)
}
