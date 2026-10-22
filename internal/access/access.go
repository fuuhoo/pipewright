// Package access 是「资源分组」权限判定的唯一出口。
//
// 业务含义:v6.2 分组权限。项目与服务器归属一个可选分组(resource_groups.id),
// 分组有 public / private 两态;私有分组只有组长、成员与管理员可见可操作。
//
// 三态语义:
//   - 未归组(group_id=”)            → 全员可见可操作(存量数据即此态)
//   - public 分组                     → 同上;仅组长额外可 Manage
//   - private 分组                    → 组长/成员/admin 可见可操作;其余 403
//
// 动作分层(Act):
//   - View    看得见(列表、详情、运行记录、日志、报告)
//   - Operate 改得动(编辑/删除资源、触发运行、取消、审批、部署、SSH 执行)
//   - Manage  管归属(归组/改组、私有分组成员名册)——管理员或组长
//
// 判定写成纯函数 Decide,三态矩阵可在不碰数据库的情况下穷举;需要读库的部分
// (解析资源归属、算可见分组)收在 Service 里,依赖窄接口 Repository。
//
// 端点范围是第三把尺:API 令牌可带 Actor.GroupScope(一组分组 ID),它只在上述结果之上
// 再收一刀 —— 空 = 不限,非空 = 只碰列出的那几个组,归属人是管理员也一样。它只能收窄、
// 不能放大,所以列表(VisibleGroups)与逐资源判定(Decide)读同一份范围,不给「看得见
// 但点不进」留缝。
//
// 与 internal/vault 的 Actor 区别:vault 面向领域内系统调用,Actor=nil 视为 admin;
// 本包只服务已登录用户的请求,actor 缺失一律 fail closed(ErrUnauthenticated)。
package access

import (
	"context"
	"errors"
	"fmt"
)

// 角色与可见性枚举(DB 存字串,与 users.role / resource_groups.visibility 对齐)。
const (
	RoleAdmin = "admin"

	VisibilityPublic  = "public"
	VisibilityPrivate = "private"

	// Ungrouped 是「未归组」的 group_id 取值:projects/servers.group_id 的默认值。
	Ungrouped = ""
)

// Kind 是被判定资源的类别。
type Kind string

const (
	KindProject Kind = "project"
	KindServer  Kind = "server"
	// KindKubeCluster 是 Kubernetes 集群目标(与 server 同为「部署落点」,但归组独立判定)。
	KindKubeCluster Kind = "kube_cluster"
	// KindRun 的运行本身没有分组;归属经 run → project → group 解析。
	KindRun Kind = "run"
)

// Act 是动作档位;数值越大权限越重。
type Act int

const (
	ActView Act = iota + 1
	ActOperate
	ActManage
)

func (a Act) String() string {
	switch a {
	case ActView:
		return "view"
	case ActOperate:
		return "operate"
	case ActManage:
		return "manage"
	default:
		return fmt.Sprintf("act(%d)", int(a))
	}
}

// 领域错误。HTTP 层按 errors.Is 映射状态码,故包装一律用 %w。
var (
	// ErrUnauthenticated → 401。
	ErrUnauthenticated = errors.New("access: 未认证")
	// ErrForbidden → 403。明确告知「无权限」而非伪装成 404:分组是协作资源,
	// 让人知道资源存在但不属于自己,比隐身更符合产品预期(用户据此去申请加入)。
	ErrForbidden = errors.New("access: 无权限")
	// ErrRoleCeiling 标记「角色本身不允许这类动作」(功能轴)。它同时包装 ErrForbidden,
	// 所以既有的 errors.Is(err, ErrForbidden) 映射照旧生效;HTTP 层据它回一句真话——
	// 被角色上限拦下时资源未必属于私有分组,报「你不在名册里」会把人引去申请一个没用的加入。
	ErrRoleCeiling = errors.New("access: 角色档位不足")
	// ErrGroupNotFound 表示 group_id 指向不存在的分组(引用悬挂)。
	ErrGroupNotFound = errors.New("access: 分组不存在")
)

// Actor 是当前操作者。UserID 取 users.id(UUID v4)。
type Actor struct {
	UserID   string
	Username string
	Role     string
	// GroupScope 是「这次调用所用 API 令牌端点」的分组范围:空 = 不限;
	// 非空 = 只有列出的分组可见可操作。它是硬收窄,只在原有权限上取交集,
	// 归属人是管理员也一样(见 Decide 与 VisibleGroups)。
	// 会话 cookie 那条线这里是 nil:范围挂在令牌上,不挂在人身上。
	GroupScope []string
}

// IsAdmin 判断是否管理员。与 vault.Actor 不同:nil 不视为 admin。
func (a *Actor) IsAdmin() bool {
	return a != nil && a.Role == RoleAdmin
}

// InScope 报告该分组是否落在端点范围内(范围为空 = 不限)。
//
// 未归组的 groupID 是空串,一旦限了组就必然不在范围里 —— 这是有意口径:操作人选了
// 「只授权这几个组」,未归组资源不属于其中任何一个。范围里也不会出现空串
// (apitoken 存前已剔除空项,签发接口再校验组确实存在)。
func (a *Actor) InScope(groupID string) bool {
	if a == nil || len(a.GroupScope) == 0 {
		return true
	}
	for _, id := range a.GroupScope {
		if id == groupID {
			return true
		}
	}
	return false
}

// errOutOfScope 是「端点范围没覆盖这个分组」的判定结果。包装 ErrForbidden → 403,
// 但措辞与其他 403 不同:让人去找令牌的操作人放宽范围,而不是去申请加入分组。
func (a *Actor) errOutOfScope(groupID string) error {
	label := groupID
	if label == Ungrouped {
		label = "未归组"
	}
	return fmt.Errorf("%w:资源属于%s,不在这把令牌的端点范围内(只授权了 %d 个分组)",
		ErrForbidden, label, len(a.GroupScope))
}

// Group 是分组快照:判定所需的最小字段,不含时间戳等展示数据。
type Group struct {
	ID         string
	Visibility string
	OwnerID    string
	MemberIDs  []string
}

// IsPublic 报告分组是否公开(nil 快照 = 未归组 = 公开)。
func (g *Group) IsPublic() bool {
	return g == nil || g.Visibility == VisibilityPublic
}

// id 返回判定用的分组 ID:nil 快照(未归组)取空串,与 Ungrouped 同值。
func (g *Group) id() string {
	if g == nil {
		return Ungrouped
	}
	return g.ID
}

// HasMember 报告 userID 是否在名册里(不含组长)。
func (g *Group) HasMember(userID string) bool {
	if g == nil || userID == "" {
		return false
	}
	for _, id := range g.MemberIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// Decide 纯函数判定:actor 能否对分组 g 里的资源做 act。放行返回 nil,否则 ErrForbidden。
//
// g 为 nil 表示资源未归组。
func Decide(actor *Actor, g *Group, act Act) error {
	if actor == nil || actor.UserID == "" {
		return ErrUnauthenticated
	}
	// 端点范围排在身份之前:它是「这把令牌能碰哪些组」,管理员归属也收得到这条约束。
	// 放在这里(而不是 VisibleGroups 那一侧)是为了让三十多条路由的判定与列表口径共用
	// 同一个答案 —— 两处各写一遍必然漂移成「列表里看得见、点进去 403」。
	if !actor.InScope(g.id()) {
		return actor.errOutOfScope(g.id())
	}
	if actor.IsAdmin() {
		return nil
	}
	// 组长管自己组内的一切,含改名册与把项目移出组。
	if g != nil && g.OwnerID != "" && g.OwnerID == actor.UserID {
		return nil
	}
	if act == ActManage {
		// 未归组与公开分组都没有「组内归属」可管,只有管理员能改(上面已放行)。
		return fmt.Errorf("%w:%s 需要管理员或组长", ErrForbidden, act)
	}
	if g.IsPublic() {
		return nil
	}
	if g.HasMember(actor.UserID) {
		return nil
	}
	return fmt.Errorf("%w:分组 %s 私有,你不在名册里", ErrForbidden, g.ID)
}

// Repository 是判定所需的读接口;实现体与分组领域服务一起落地(见 internal/group)。
type Repository interface {
	// GroupIDOf 返回资源所属分组 ID;未归组返回 Ungrouped("")。资源不存在返回 ErrForbidden 之外的
	// 领域错误,由调用方映射 404。KindRun 由实现内部做 run → project 两跳。
	GroupIDOf(ctx context.Context, kind Kind, id string) (string, error)
	// GroupSnapshot 取分组可见性/组长/名册;groupID==Ungrouped 时返回 (nil, nil)。
	GroupSnapshot(ctx context.Context, groupID string) (*Group, error)
	// PublicAndJoinedGroupIDs 返回 actor 可见的**已存在**分组 ID:所有 public 组 +
	// 自己作为组长或成员的 private 组。管理员不需要调用它。
	PublicAndJoinedGroupIDs(ctx context.Context, userID string) ([]string, error)
}

// Service 把 Decide 与 Repository 组装成 HTTP/领域层可直接调用的判定入口。
type Service struct {
	repo Repository
}

// NewService 构造 Service;repo 为 nil 时判定退化为「仅管理员/未归组放行」(见 Can)。
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Can 判定 actor 能否对 kind/id 资源做 act。
func (s *Service) Can(ctx context.Context, actor *Actor, kind Kind, id string, act Act) error {
	if actor == nil || actor.UserID == "" {
		return ErrUnauthenticated
	}
	if id == "" {
		return fmt.Errorf("access: %s %s 缺资源 ID", kind, act)
	}
	// 功能轴先封顶:角色的档位上限与归属无关,查不到表就一律拦下,
	// 免得再去库里为一条注定 403 的请求解析归属。Manage 不在这一步(见 roles.go 注释)。
	if act != ActManage {
		if max := Ceiling(actor.Role, kind); act > max {
			return fmt.Errorf("%w:%s 角色对 %s 的上限是 %s,不能 %s (%w)",
				ErrForbidden, NormalizeRole(actor.Role), kind, max, act, ErrRoleCeiling)
		}
	}
	if s.repo == nil {
		// 带端点范围的令牌在这里没法判:资源归属查不到,而「查不到」不能算落在范围里。
		if len(actor.GroupScope) > 0 {
			return fmt.Errorf("%w:权限仓储未装配,带分组范围的令牌无法校验归属", ErrForbidden)
		}
		// 未装配仓储(如单机演示模式):仅管理员可操作,避免静默放行。
		if actor.IsAdmin() {
			return nil
		}
		return fmt.Errorf("%w:权限仓储未装配", ErrForbidden)
	}
	groupID, err := s.repo.GroupIDOf(ctx, kind, id)
	if err != nil {
		return err
	}
	g, err := s.repo.GroupSnapshot(ctx, groupID)
	if err != nil {
		return err
	}
	if g == nil && groupID != Ungrouped {
		// 引用悬挂:资源指向已删的组。按最私有一档处理(fail closed),
		// 等管理员重新归组;普通用户此时看到 403 而非全站放行。
		g = &Group{ID: groupID, Visibility: VisibilityPrivate}
	}
	if err := Decide(actor, g, act); err != nil {
		return fmt.Errorf("access: %s %s/%s: %w", act, kind, id, err)
	}
	return nil
}

// ForRun 判定 actor 能否访问某次运行(含其日志、报告、产物、事件流)。
// 运行信息随项目走:kind=KindRun 的归属解析由 Repository 做 run → project → group。
func (s *Service) ForRun(ctx context.Context, actor *Actor, runID string, act Act) error {
	return s.Can(ctx, actor, KindRun, runID, act)
}

// CanGroup 判定 actor 能否直接对分组本身做 act(归组时校验「新组」这一侧)。
//
// Can 只能从资源反查归属;而「把项目放进组 A」这件事要判的是 A 而非项目当前所在组,
// 所以单独开一个入口。groupID 为 Ungrouped 表示移出分组——不再校验新侧,
// 因为旧侧的 ActManage 已经保证了「只有管得了原组的人才能把它甩出去」。
func (s *Service) CanGroup(ctx context.Context, actor *Actor, groupID string, act Act) error {
	if actor == nil || actor.UserID == "" {
		return ErrUnauthenticated
	}
	// 端点范围排在「移出分组」之前:限了组的令牌连「把项目甩出分组」都不该做,
	// 因为甩出去落到 Ungrouped,而那是它范围之外的落点。
	if !actor.InScope(groupID) {
		return fmt.Errorf("access: %s group/%s: %w", act, groupID, actor.errOutOfScope(groupID))
	}
	if groupID == Ungrouped {
		return nil
	}
	if s.repo == nil {
		if actor.IsAdmin() {
			return nil
		}
		return fmt.Errorf("%w:权限仓储未装配", ErrForbidden)
	}
	g, err := s.repo.GroupSnapshot(ctx, groupID)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("%w:%s", ErrGroupNotFound, groupID)
	}
	if err := Decide(actor, g, act); err != nil {
		return fmt.Errorf("access: %s group/%s: %w", act, groupID, err)
	}
	return nil
}

// ListFilter 是可拼进列表 SQL 的分组可见性条件。
type ListFilter struct {
	// Unrestricted=true 表示无需按分组过滤(admin 且这把令牌不带端点范围)。
	Unrestricted bool
	// DenyAll=true 表示「一条都不该看见」。它必须有独立的一态:GroupIDs 为空时
	// Clause 的兜底按「只看未归组」生成条件,那等于把未归组资源端给一把明确没被
	// 授权它们的令牌(端点范围与本人可见集交集为空)—— 恰好是范围要防的事。
	DenyAll bool
	// GroupIDs 是允许的 group_id 取值;普通用户的可见集**必含 Ungrouped**,
	// 否则存量未归组资源会从列表里凭空消失。带端点分组范围的令牌是唯一例外:
	// 范围里没有 Ungrouped(空串在 apitoken 存前就被剔除),未归组资源因此不可见。
	GroupIDs []string
}

// VisibleGroups 推导 actor 的列表可见范围。admin 直接不受限,不查库 —— 除非这把令牌
// 带端点分组范围,那条线走下面的交集。
func (s *Service) VisibleGroups(ctx context.Context, actor *Actor) (ListFilter, error) {
	if actor == nil || actor.UserID == "" {
		return ListFilter{}, ErrUnauthenticated
	}
	// 端点范围是硬收窄,对管理员归属同样生效;而且列表口径必须与 Decide 给同一个答案,
	// 否则会漂成「列表里看得见、点进去 403」。范围里的组可能已被删(签发后删除),
	// 那只是让 IN 条件多一个永不匹配的值,不影响正确性。
	if len(actor.GroupScope) > 0 {
		if actor.IsAdmin() {
			return ListFilter{GroupIDs: append([]string(nil), actor.GroupScope...)}, nil
		}
		if s.repo == nil {
			return ListFilter{}, fmt.Errorf("%w:权限仓储未装配", ErrForbidden)
		}
		// 本人可见集里那一项 Ungrouped 在这里被自然滤掉:限组即「只碰这几个组」。
		ids, err := s.repo.PublicAndJoinedGroupIDs(ctx, actor.UserID)
		if err != nil {
			return ListFilter{}, err
		}
		both := intersectScope(actor.GroupScope, append([]string{Ungrouped}, ids...))
		if len(both) == 0 {
			return ListFilter{DenyAll: true}, nil
		}
		return ListFilter{GroupIDs: both}, nil
	}
	if actor.IsAdmin() {
		return ListFilter{Unrestricted: true}, nil
	}
	if s.repo == nil {
		return ListFilter{}, fmt.Errorf("%w:权限仓储未装配", ErrForbidden)
	}
	ids, err := s.repo.PublicAndJoinedGroupIDs(ctx, actor.UserID)
	if err != nil {
		return ListFilter{}, err
	}
	// 未归组资源全员可见,固定放进白名单首位。
	return ListFilter{GroupIDs: append([]string{Ungrouped}, ids...)}, nil
}

// intersectScope 取「端点范围」与「本人可见集」的交集,保持范围顺序并去重。
func intersectScope(scope, visible []string) []string {
	set := make(map[string]bool, len(visible))
	for _, id := range visible {
		set[id] = true
	}
	seen := make(map[string]bool, len(scope))
	out := make([]string, 0, len(scope))
	for _, id := range scope {
		if set[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Clause 把 ListFilter 编译成 SQL 片段(含占位符),供领域层拼进 WHERE。
// column 形如 "p.group_id"。不受限时返回 ("", nil),调用方据此跳过条件。
func (f ListFilter) Clause(column string) (string, []any) {
	if f.DenyAll {
		// 恒假条件,调用方照常拼进 WHERE。不能返回空串 —— 那在这些调用方眼里等于「不加条件」。
		return "1 = 0", nil
	}
	if f.Unrestricted {
		return "", nil
	}
	if len(f.GroupIDs) == 0 {
		// 只有未归组可见也要生成条件,不能退化成无条件放行。
		return column + " = ?", []any{Ungrouped}
	}
	placeholders := make([]byte, 0, len(f.GroupIDs)*2)
	args := make([]any, 0, len(f.GroupIDs))
	for _, id := range f.GroupIDs {
		if len(placeholders) > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	return column + " IN (" + string(placeholders) + ")", args
}
