package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangchengsir/pipewright/internal/pipeline"
)

// TestWritePipelineErrorMapsEveryJobTypeSentinel 守住一条容易被忘的对应关系:
// pipeline 新增一个校验哨兵,这里就得有一映射 —— 漏掉不会编译失败,只会把用户自己填错的
// 配置报成「服务器内部错误」(500),把本该指导改配的那句话吞掉。
// (ErrK8sDeployInvalid 就这么漏过一次:K8s 节点的保存期校验全在跑,消息却永远看不见。)
func TestWritePipelineErrorMapsEveryJobTypeSentinel(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"retired 类型", pipeline.ErrJobTypeRetired},
		{"健康探测非法", pipeline.ErrHealthProbeInvalid},
		{"构建任务非法", pipeline.ErrBuildTaskInvalid},
		{"产物来源非法", pipeline.ErrArtifactSourceInvalid},
		{"docker 部署非法", pipeline.ErrDockerDeployInvalid},
		{"k8s 发布非法", pipeline.ErrK8sDeployInvalid},
		{"落点与分批非法", pipeline.ErrDeployTargetInvalid},
		{"构建环境必选", pipeline.ErrBuildEnvRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 带用户可读消息的包装错误(校验侧真实形状:issuef 把文案放外层)。
			wrapped := &jobIssue{detail: "阶段「部署」任务「K8s 发布」:K8s 发布未选目标集群", sentinel: tc.err}
			rec := httptest.NewRecorder()
			writePipelineError(rec, wrapped)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422(哨兵未映射会掉进 500 分支)", rec.Code)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			if body.Error.Message != wrapped.detail {
				t.Errorf("消息应原样回显以便改配,got %q", body.Error.Message)
			}
		})
	}
}

// jobIssue 复刻 pipeline 内部的包装形状(errors.Is 只认内层哨兵)。
type jobIssue struct {
	detail   string
	sentinel error
}

func (e *jobIssue) Error() string { return e.detail }
func (e *jobIssue) Unwrap() error { return e.sentinel }

// 哨兵本身必须仍可被 errors.Is 分派(包装形状变了就会静默失配)。
func TestSentinelsAreComparable(t *testing.T) {
	if !errors.Is(&jobIssue{detail: "x", sentinel: pipeline.ErrK8sDeployInvalid}, pipeline.ErrK8sDeployInvalid) {
		t.Fatal("ErrK8sDeployInvalid 无法经 Unwrap 分派")
	}
}
