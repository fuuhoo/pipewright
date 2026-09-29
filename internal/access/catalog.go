// catalog.go —— 功能轴的「自定义角色」读取入口。
//
// 分工:内置五档(admin / user / developer / ops / viewer)的点集是 perms.go 里的代码表,
// 库里不复制一份 —— 它们是模板,升级时新加的功能点自动跟着走,内置 admin 也永远改不掉、
// 删不掉(_self-lockout 兜底)。自定义角色存在 0062 的 roles / role_perms 两张表里,由装配时
// SetRoleStore + ReloadRoles 拉进这份进程内缓存。
//
// 为什么是进程内缓存而不是每次查库:判定入口是 RequireAdmin、登录响应、users 校验这类
// 「只有角色字符串、没有仓储句柄」的纯函数(见 docs/权限架构说明.md §2.4),给它们统统
// 加上 ctx 与仓储会把功能轴的调用形状打散。角色定义属于「一年改不了几次」的读多写少数据,
// 写侧在每次成功写入后立刻 ReloadRoles,所以同进程内的可见性是即时的。
// 代价:另一个进程改了库,本进程要到下次重载才看见 —— 本机单进程部署不存在这条路径,
// 真要多实例得改成 TTL 刷新或通知,已记进 §11。
//
// fail closed:仓储未装配 / 重载失败 / 库里查不到这个角色 → 点集为空、Ceiling 落 ActView。
// 一个都别给,比「按最像的内置角色凑一个档位」安全 —— 后者会让一条脏数据变成静默提权。
package access

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// CustomRole 是一个自定义角色参与判定的最小字段(展示名只给设置页用,判定不读它)。
type CustomRole struct {
	ID    string
	Name  string
	Perms []string
}

// RoleStore 是 catalog 的读端窄接口,由 internal/role 的仓储实现。
type RoleStore interface {
	ListCustomRoles(ctx context.Context) ([]CustomRole, error)
}

// roleCatalog 是被 RWMutex 保护的自定义角色快照。整批替换:读侧永远看到某一次成功重载的
// 完整结果,不会看到「角色已更新但点集还是旧的」这种半套状态。
type roleCatalog struct {
	mu     sync.RWMutex
	store  RoleStore
	custom map[string]CustomRole
}

var catalog = roleCatalog{custom: make(map[string]CustomRole)}

// SetRoleStore 装配自定义角色仓储并清空缓存(装配方随后要自己调一次 ReloadRoles)。
// 传 nil 退回「只有内置档」——单机演示模式与没接库的单测都走这条路。
func SetRoleStore(s RoleStore) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.store = s
	catalog.custom = make(map[string]CustomRole)
}

// ReloadRoles 从仓储全量刷新自定义角色。仓储未装配时是 no-op(内置档照旧可用)。
// 两道清理:①与内置角色同名的行直接忽略(代码表才是内置档权威,不许被库里的数据顶掉);
// ②字典里已不存在的点 ID 丢掉(代码删了某个功能点,残留引用不该继续算数)。
func ReloadRoles(ctx context.Context) error {
	catalog.mu.RLock()
	s := catalog.store
	catalog.mu.RUnlock()
	if s == nil {
		return nil
	}
	rows, err := s.ListCustomRoles(ctx)
	if err != nil {
		return fmt.Errorf("access: 重载自定义角色: %w", err)
	}
	next := make(map[string]CustomRole, len(rows))
	for _, r := range rows {
		if r.ID == "" || isBuiltinRole(r.ID) {
			continue
		}
		perms := make([]string, 0, len(r.Perms))
		for _, id := range r.Perms {
			if _, known := permByID[id]; known {
				perms = append(perms, id)
			}
		}
		next[r.ID] = CustomRole{ID: r.ID, Name: r.Name, Perms: perms}
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.custom = next
	return nil
}

// isBuiltinRole 报告该 ID 是否命中内置档(内置档只读,不可被自定义数据覆盖)。
func isBuiltinRole(id string) bool {
	_, ok := rolePerms[id]
	return ok
}

// pointsFor 给出角色的功能点集:先查代码表(内置模板),再查进程内缓存(自定义角色)。
// ok=false 表示这个角色谁也不认 —— 调用方一律 fail closed。
func pointsFor(role string) ([]string, bool) {
	if perms, ok := rolePerms[role]; ok {
		return perms, true
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	r, ok := catalog.custom[role]
	if !ok {
		return nil, false
	}
	return r.Perms, true
}

// CustomRoleIDs 返回当前缓存里的自定义角色 ID(按字串序),供测试与诊断用。
func CustomRoleIDs() []string {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	out := make([]string, 0, len(catalog.custom))
	for id := range catalog.custom {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
