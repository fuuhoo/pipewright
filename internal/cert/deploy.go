package cert

// deploy.go —— 「发布」这条线:把库里的证书正文与私钥铺到目标机的指定路径,再跑用户自己填的
// 生效指令。
//
// 只支持 SSH 落点(先把一条路走实):云厂商的证书托管(ACM / SSL 证书控制台)各家一套 API 与
// 权限模型,收益不如「你自己的机器上 nginx/caddy/java 想怎么用就怎么用」来得直接。
//
// 三条纪律:
//   - **平台不发明命令**。落到目标机上执行的只有三类:上传文件、给刚上传的文件改名与设权限、
//     以及用户逐字填的 post_command。既没有「探测这台机器装的是 nginx 还是 apache」这种自动判断,
//     也没有替你猜一条 reload —— 猜错的服务名会把别人的服务重启掉。
//   - 每个文件都**先落临时名再同目录 mv 到位**。上传经 SSH 通道直写最终路径时,一次超时或断流
//     会在部署目录里留下半个 PEM,而服务下一次 reload 直接读到坏文件(部署侧的制品踩过同一个坑)。
//   - 权限在改名**之前**设好。私钥只要以可见形态出现过哪怕一瞬就不该算成功。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/google/uuid"
)

// ErrPublisherUnconfigured 表示平台没接上目标机执行能力(装配缺 target.Service)。
// 与签发缺 TXTWriter 同理:必须当场说清楚,而不是等一个网络超时。
// 「证书还没正文就来发布」由 Bundle() 统一挡(ErrNoCertBlock / ErrBadKeyPEM),这里不重复定义。
var ErrPublisherUnconfigured = errors.New("cert: SSH publisher is not configured")

// filePermCert / filePermKey 是落到目标机上的文件权限。
// 证书正文是公开的(浏览器本来就拿得到),644 便于别的进程读;私钥 600 —— nginx 以 root 读它,
// 收紧到属主可读写不会弄坏任何正常配置,但不收紧就是给同一台机器上的其它账号留了一份密钥材料。
const (
	filePermCert uint32 = 0o644
	filePermKey  uint32 = 0o600
)

// 单步额度。证书包只有几 KB,60s 足够;真正可能慢的是目标机自己那条 reload
// (systemctl 卡住并不罕见),它慢了就该失败并把原因写在这一行,而不是让整条发布挂死。
const (
	deployUploadTimeout = 60 * time.Second
	deployExecTimeout   = 60 * time.Second
	deployCleanupWait   = 15 * time.Second
)

// Publisher 是发布所需的目标机能力(由 target.Service 满足)。
//
// 同样收成窄接口:cert 层不该看见 SSH 凭据、主机 CRUD 这些与「把两个文件放到那台机器上并跑一条
// 命令」无关的东西;窄接口也让单测能塞一个记账用的假实现,不必真拨号。
type Publisher interface {
	// Upload 把内容流式写到远端 remotePath(实现侧会建好父目录)。
	Upload(ctx context.Context, serverID string, content io.Reader, remotePath string) error
	// Exec 在远端跑一条 array 命令(程序 + 参数;AC-SEC-02 不拼 shell)。
	Exec(ctx context.Context, serverID string, cmd []string) (*target.ExecResult, error)
}

// WithPublisher 接上目标机执行能力。不接则 Deploy 直接报 ErrPublisherUnconfigured。
func WithPublisher(p Publisher) Option {
	return func(s *service) {
		s.pub = p
	}
}

// DeployResult 是一个落点这一次发布的结果。视图字段全带上,页面不必再回查配置。
type DeployResult struct {
	TargetID   string
	ServerID   string
	CertPath   string
	KeyPath    string
	OK         bool
	Error      string // 人话原因(失败时非空;绝不含私钥内容)
	DeployedAt time.Time
}

// DeployReport 是一次发布的全量结果(逐落点)。
type DeployReport struct {
	CertID  string
	Results []DeployResult
}

// Failed 返回失败的落点数。全部成功为 0。
func (r *DeployReport) Failed() int {
	n := 0
	for _, res := range r.Results {
		if !res.OK {
			n++
		}
	}
	return n
}

// Deployed 返回成功的落点数。
func (r *DeployReport) Deployed() int { return len(r.Results) - r.Failed() }

// Deploy 把一张证书铺到它的**全部**落点(页面上的「发布」)。
// 逐落点相互独立:一台机器连不上不该让其它机器也跟着不发,所以失败不回滚也不中断,
// 全部跑完后由 report 说话(返回值里的 error 只用于证书层面前置条件不成立)。
func (s *service) Deploy(ctx context.Context, certID string) (*DeployReport, error) {
	return s.deploy(ctx, certID, false)
}

// DeployAuto 只铺 auto_deploy 为真的落点。
//
// 为什么和手动发布分开:auto_deploy 关掉的就是「不许平台自己往这台机器写文件」的意思,
// 续期后自动铺发不能把它当成没说过。
func (s *service) DeployAuto(ctx context.Context, certID string) (*DeployReport, error) {
	return s.deploy(ctx, certID, true)
}

func (s *service) deploy(ctx context.Context, certID string, onlyAuto bool) (*DeployReport, error) {
	if s.pub == nil {
		return nil, ErrPublisherUnconfigured
	}
	bundle, err := s.Bundle(ctx, certID)
	if err != nil {
		return nil, err
	}
	targets, err := s.store.listTargets(ctx, certID)
	if err != nil {
		return nil, err
	}
	report := &DeployReport{CertID: certID}
	for _, t := range targets {
		if onlyAuto && !t.AutoDeploy {
			continue
		}
		// 私钥明文只活在这一行调用栈里:publish 内部只用它写流,不留副本、不进错误。
		res := s.publishTo(ctx, t, bundle)
		report.Results = append(report.Results, res)
	}
	return report, nil
}

// publishTo 跑完一个落点的发布并回写结果。
func (s *service) publishTo(ctx context.Context, t *DeployTarget, bundle *Bundle) DeployResult {
	res := DeployResult{
		TargetID: t.ID, ServerID: t.ServerID, CertPath: t.CertPath, KeyPath: t.KeyPath,
	}
	fail := func(cause error) DeployResult {
		res.OK = false
		res.Error = humanErr(cause)
		// 失败也要落库:页面与告警读的就是这一行的 last_status / last_error。
		_ = s.store.markTarget(ctx, t.ID, DeployFailed, res.Error, time.Time{})
		return res
	}
	ok := func() DeployResult {
		now := s.now().UTC()
		res.OK = true
		res.DeployedAt = now
		if err := s.store.markTarget(ctx, t.ID, DeployOK, "", now); err != nil {
			// 文件已经铺好、reload 也已成功,只有「记这一笔」失败:如实报出来,但状态按成功算。
			res.Error = "发布已生效,但结果回写失败:" + humanErr(err)
		}
		return res
	}

	// 正文与私钥一起换:先私钥后正文。反过来的话,中间窗口里 nginx 若被别的动作 reload 一次,
	// 读到的是「新正文 + 旧私钥」直接不匹配;先换私钥,旧正文配新私钥仍能继续服务。
	if err := s.placeFile(ctx, t.ServerID, t.KeyPath, bundle.KeyPEM, filePermKey); err != nil {
		return fail(fmt.Errorf("%w:%s 私钥落位失败:%s", ErrDeployFailed, t.KeyPath, humanErr(err)))
	}
	if err := s.placeFile(ctx, t.ServerID, t.CertPath, bundle.Fullchain(), filePermCert); err != nil {
		return fail(fmt.Errorf("%w:%s 证书落位失败:%s", ErrDeployFailed, t.CertPath, humanErr(err)))
	}
	if cmd := strings.TrimSpace(t.PostCommand); cmd != "" {
		if err := s.runPostCommand(ctx, t.ServerID, cmd); err != nil {
			return fail(fmt.Errorf("%w:生效指令失败:%s", ErrDeployFailed, humanErr(err)))
		}
	}
	// post_command 为空是完全合法的配置:平台绝不代替用户猜一条 reload。
	return ok()
}

// placeFile 把内容以「临时名 → 设权限 → 同目录 mv 到位」三步落到 remotePath。
//
// 临时名带一段随机后缀:同一台机器上并发发两张证书(手动点一次、续期 ticker 一次)是可能的,
// 而两条流撞在同一个临时名上会互相截掉对方的半截文件。
func (s *service) placeFile(ctx context.Context, serverID, remotePath, content string, mode uint32) error {
	dir, base := path.Split(remotePath)
	tmp := path.Join(dir, "."+base+".pipewright-"+uuid.NewString()[:8]+".tmp")

	// 从这一步起目标机上就可能有半个文件了:上传断流、远端磁盘满、chmod 被拒、mv 撞名,
	// 任一条失败都要把临时名清掉 —— 名字以点开头,没人 ls 得到它,留着就是无声的密钥材料。
	// 走到位(mv 成功)之后 tmp 本身已不存在,不必再 rm。
	placed := false
	defer func() {
		if !placed {
			s.dropTemp(ctx, serverID, tmp)
		}
	}()

	upCtx, cancelUpload := context.WithTimeout(ctx, deployUploadTimeout)
	err := s.pub.Upload(upCtx, serverID, strings.NewReader(content), tmp)
	cancelUpload()
	if err != nil {
		return err
	}

	// 权限在改名之前设:改名是原子的,于最终路径上「先以宽权限出现再收紧」这个窗口根本不存在。
	if err := s.run(ctx, serverID, []string{"chmod", fmt.Sprintf("%04o", mode), tmp}); err != nil {
		return fmt.Errorf("设权限失败:%s", humanErr(err))
	}
	if err := s.run(ctx, serverID, []string{"mv", "-f", tmp, remotePath}); err != nil {
		return fmt.Errorf("落位失败:%s", humanErr(err))
	}
	placed = true
	return nil
}

// runPostCommand 跑用户填的生效指令。
//
// 与部署侧的 restartCommand 逐字同形:指令体作为 sh 的脚本参数、set -e 让多行里任意一条失败即失败,
// 而**没有任何东西被插进脚本字符串**(路径与参数一律走 array)。
func (s *service) runPostCommand(ctx context.Context, serverID, command string) error {
	return s.run(ctx, serverID, []string{"sh", "-c", "set -e\n" + command})
}

// run 执行一条命令并把非零退出翻译成人话(带 stderr 摘要,绝不含证书与私钥内容)。
func (s *service) run(ctx context.Context, serverID string, cmd []string) error {
	execCtx, cancel := context.WithTimeout(ctx, deployExecTimeout)
	defer cancel()
	res, err := s.pub.Exec(execCtx, serverID, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("退出码 %d:%s", res.ExitCode, humanErr(errors.New(res.Stderr)))
	}
	return nil
}

// dropTemp 尽力清掉没落位的临时文件。清不掉就忽略:那是目标机的临时故障,
// 名字里带随机段,不会跟下一次的临时名撞上。
func (s *service) dropTemp(ctx context.Context, serverID, tmp string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployCleanupWait)
	defer cancel()
	_ = s.run(cleanupCtx, serverID, []string{"rm", "-f", tmp})
}
