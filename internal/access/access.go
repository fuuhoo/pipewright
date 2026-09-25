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
	// ErrGroupNotFound 表示 group_id 指向不存在的分组(引用悬挂)。
	ErrGroupNotFound = errors.New("access: 分组不存在")
)

// Actor 是当前操作者。UserID 取 users.id(UUID v4)。
type Actor struct {
	UserID   string
	Username string
	Role     string
}

// IsAdmin 判断是否管理员。与 vault.Actor 不同:nil 不视为 admin。
func (a *Actor) IsAdmin() bool {
	return a != nil && a.Role == RoleAdmin
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
	if s.repo == nil {
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
	// Unrestricted=true 表示无需按分组过滤(admin)。
	Unrestricted bool
	// GroupIDs 是允许的 group_id 取值;非受限模式下**必含 Ungrouped**,
	// 否则存量未归组资源会从列表里凭空消失。
	GroupIDs []string
}

// VisibleGroups 推导 actor 的列表可见范围。admin 直接不受限,不查库。
func (s *Service) VisibleGroups(ctx context.Context, actor *Actor) (ListFilter, error) {
	if actor == nil || actor.UserID == "" {
		return ListFilter{}, ErrUnauthenticated
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

// Clause 把 ListFilter 编译成 SQL 片段(含占位符),供领域层拼进 WHERE。
// column 形如 "p.group_id"。不受限时返回 ("", nil),调用方据此跳过条件。
func (f ListFilter) Clause(column string) (string, []any) {
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
