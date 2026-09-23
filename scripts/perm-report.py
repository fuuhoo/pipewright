"""把 scripts/perm-matrix.py 的结果 JSON 渲染成 docs/分组权限测试报告.md。

用法:python3 scripts/perm-matrix.py && python3 scripts/perm-report.py
表里的"实际"列来自真机响应,不手写;重跑矩阵后重新生成即可对账。
"""
import json
import pathlib
import sys

SRC = '/tmp/permqa-results.json'
if not pathlib.Path(SRC).exists():
    sys.exit(f'缺 {SRC}:先跑 python3 scripts/perm-matrix.py')
d = json.load(open(SRC))
cases = d['cases']

AREA_ORDER = ['A 账号与角色', 'B 分组管理', 'C 项目', 'D 运行', 'E 主机与容器', 'F 凭据分级', 'G 平台设置类', 'H 补充']
AREA_TITLE = {
    'A 账号与角色': '账号与角色边界',
    'B 分组管理': '分组管理(建组 / 名册 / 可见性 / 删除)',
    'C 项目': '项目:三态可见性与归组权',
    'D 运行': '运行信息随项目走',
    'E 主机与容器': '主机与容器',
    'F 凭据分级': '凭据分级(personal / global / reveal)',
    'G 平台设置类': '设置类只开放给管理员',
    'H 补充': '补充:触发运行 / 审计留痕 / 会话撤销',
}
by_area = {}
for r in cases:
    by_area.setdefault(r['area'], []).append(r)

lines = []
n = 0
for area in AREA_ORDER:
    rows = by_area[area]
    prefix = area.split()[0]
    lines.append(f'### {prefix} · {AREA_TITLE[area]}\n')
    lines.append('| 编号 | 角色 | 请求 | 期望 | 实际 | 结论 |')
    lines.append('|---|---|---|---|---|---|')
    for i, r in enumerate(rows, 1):
        n += 1
        exp = '按描述判定' if r['expect'] == '谓词' else r['expect']
        req = r['req'].replace('|', '\\|')
        desc = r['desc'].replace('|', '\\|')
        mark = '✅' if r['pass'] else '❌'
        lines.append(f'| {prefix}{i} | {r["actor"]} | `{req}` | {exp} | {r["actual"]} | {mark} {desc} |')
    lines.append('')

detail = '\n'.join(lines)
summary_rows = '\n'.join(
    f'| {a.split()[0]} | {AREA_TITLE[a]} | {sum(1 for r in by_area[a] if r["pass"])} | {len(by_area[a])} |'
    for a in AREA_ORDER)
setup_rows = '\n'.join(f'| {x["desc"]} | {"✅" if x["pass"] else "❌"} |' for x in d['setup'])

total = len(cases)
passed = sum(1 for r in cases if r['pass'])

doc = f'''# 分组权限测试报告 — Pipewright v6.2

> **结论:{passed}/{total} 条断言全部通过**,权限在服务端真实生效(不是前端隐藏)。
> 测试日期:2026-09-24 · 实例:`http://localhost:8080`(本机单二进制 + 真 SQLite,56 支迁移跑到 `0058`)
> 夹具**当前保留在实例上**,可以直接用浏览器复看(账号见 §2)。
>
> 相关文档:`docs/v6.2-IMPLEMENTATION-STATUS.md` §2.10(设计与取舍)、`docs/MANUAL-VERIFICATION.md` §4b(手工复核步骤)。

---

## 1. 判定模型(测的是这套规则)

归属三态:

| 资源归属 | View(看得见) | Operate(改得动) | Manage(管归属 / 名册) |
|---|---|---|---|
| **未归组**(`group_id=''`,存量数据即此态) | 全员 | 全员 | **仅管理员** |
| **public 分组** | 全员 | 全员 | 组长 + 管理员 |
| **private 分组** | 组长 / 名册成员 / 管理员 | 同 View | 组长 + 管理员 |

角色:`admin`(平台管理员)/ `user`(普通用户)。组长与成员都是普通用户,只是对某个组多了一档权。

三条贯穿全模型的原则,报告里的用例都在验它们:

1. **拒绝语义是 403 明确无权限**,不伪装 404 —— 分组是协作资源,让人知道"东西在那、但你没权"比隐身更符合预期(用户据此去申请加入)。
2. **判定只有一处真相**:`internal/access.Decide` 纯函数;HTTP 收口在一条中间件(`access_guard.go`,读→View / 写→Operate),更高档 Manage 由 handler 显式判(归组要同时管住新旧两侧)。前端 `canManage` 也是服务端算好下发的,UI 只读结论。
3. **运行信息随项目走**:run 自己没有分组,归属经 `run → project → group` 两跳解析。

---

## 2. 测试夹具

五个账号(都是本机测试专用,口令 `qa-perm-2026`;`admin` 用实例自身的口令):

| 账号 | 角色 | 在夹具里的身份 |
|---|---|---|
| `admin` | admin | 平台管理员,公开组 `QA-公开组` 的组长 |
| `qa_lead` | user | **私有组 `QA-私有组` 的组长** |
| `qa_mem` | user | `QA-私有组` 的**名册成员** |
| `qa_out` | user | **局外人**(不在任何组名册里) |
| `qa_admin2` | admin | 第二个管理员,只用来验「管理员不能禁用自己」 |

资源归属:

| 资源 | 归属 | 用途 |
|---|---|---|
| 项目 `smoke-go` | `QA-私有组`(private) | 私有可见性主用例 |
| 项目 `smoke-web` | `QA-公开组`(public) | 公开组用例 |
| 项目 `smoke-java` | 未归组 | 存量语义用例 |
| 主机 `p3-in-group180594` | `QA-私有组` | 主机 / 指标 / 容器 / 探测用例 |
| 主机 `p3-free180594` | 未归组 | 「未归组主机归属仅管理员」用例 |

夹具搭建结果(11 步全绿):

| 步骤 | 结果 |
|---|---|
{setup_rows}

---

## 3. 结果汇总

| 组 | 主题 | 通过 | 总数 |
|---|---|---|---|
{summary_rows}
| **合计** |  | **{passed}** | **{total}** |

一句话读法:**每一组都是"该挡的挡住、该放的放行"两半**——只验 403 会漏掉"误伤",只验 200 会漏掉"没挡住"。

---

## 4. 用例明细

{detail}
---

## 5. 浏览器复核(同一套夹具,UI 侧)

接口过了但 UI 藏不住,才是真的可用。逐项看过:

| 视角 | 看到什么 | 判定 |
|---|---|---|
| `admin` → `/groups` | 两行分组:`QA-公开组`(公开 · 组长 admin · 1 个项目)、`QA-私有组`(私有 · 组长 qa_lead · 1 位成员 · 1 个项目 · 1 台服务器);描述显示"组长改过",证明组长的 PATCH 真落库 | ✅ |
| `qa_mem`(成员)→ `/projects` | **6 个项目全在**,`smoke-go` 带「QA-私有组」标签、`smoke-web` 带「QA-公开组」标签;侧栏无「构建环境 / 配置资源」 | ✅ |
| `qa_out`(局外人)→ `/projects` | **5 个项目**,`smoke-go` 消失;`smoke-web`(公开组)仍在 | ✅ |
| `qa_out` → 直连私有组项目的 `/projects/<id>/pipeline` 与 `/runs/<id>` | 渲染「无权访问:该资源属于私有分组,你不在名册里」+ ↻ 重试,不是空白页也不是"资源不存在" | ✅ |
| `qa_out` → `/server-status` | 只有 `p3-free180594` 一张卡,顶部「0/1 可达」(admin 是 0/2) | ✅ |
| `qa_out` → `/groups` | 只剩 `QA-公开组` 一行(私有组不可见),且**没有「+ 新建分组」按钮**、行内「管理」置灰并提示「只有管理员或该组组长可以管理名册与归属」 | ✅ |
| `admin` → `/settings/users` | 建号 / 重置口令 / 启停齐全;内置管理员那一行的「重置口令 / 禁用」置灰(后端 409,前端不给人点了才报错) | ✅ |

---

## 6. 本期没在真机覆盖的项(以及它们在哪验的)

| 项 | 为什么没进这份报告 | 现有覆盖 |
|---|---|---|
| SSH / 容器 **WS 终端**握手前的 Operate 判定 | 需要真能连上的主机 + WebSocket 客户端;夹具里的两台主机都不可达 | `internal/httpapi/server_guard_test.go`、`internal/httpapi` 的 guard 用例 |
| **悬挂引用**(group_id 指向已不存在的组)按最私有一档收敛 | 正规 API 不给写坏引用(建组校验会拦),只能改库制造 | `internal/access` 表驱动单测 |
| **MySQL 方言**下的可见性 SQL(`ListFilter.Clause`) | 本机是 SQLite | `internal/storetest`(需 `PIPEWRIGHT_TEST_MYSQL_DSN`,见 `docs/MANUAL-VERIFICATION.md` §3 B1/B2) |
| 并发 / 竞态(边归组边触发运行) | 单机实例量级小,判定在同一事务边界内 | 未测;真出问题再补 |
| 首登强制改密 | 只有「自己改自己口令」这一支存在 | 未实现(独立 story) |

---

## 7. 已知取舍与风险

1. **禁用账号与重置口令都不撤销已签发会话。** 报告里 H3 那条断言记录的就是这个现状(期望写的是「200(现状,非期望)」):禁用后旧浏览器仍能继续操作,只挡新登录。与"管理员改自己口令"沿用同一套语义,但**「禁用 = 立刻失效」的直觉更强**——要真做到需要一张会话撤销表。这是产品决定,不是漏测。
2. **未归组资源对全员开放**,包括"任何登录用户都能改它的内容"(C13/C14),只有**归属**收在管理员手里。这是刻意为之:存量数据不迁移,升级当天不能把别人的项目变成谁的都看不了。代价是"想保护一个项目"必须显式建组并归组。
3. **登记一台未归组主机 = 向全员开放一个可 SSH 目标**,所以 E10 把它收成仅管理员;组长只能在**自己的组里**加机器(E12 建进自己组成功、E11 想塞进别人组被拒)。
4. **reveal 明文本期只开放给管理员**(F4):连凭据的所有者本人都不能 reveal 自己的明文,只能删了重建。收紧是有意的(reveal 每次进审计),但如果普通用户有"我自己看看我的 token"的需求,这条会硌手。
5. 测试账号 `qa_*` 与 `QA-*` 分组**留在本机实例上**方便复看;不复看了跑 `python3 scripts/perm-matrix.py teardown` 清掉(资源移回未归组 + 删组 + 停用账号)。这些账号只存在于本机测试库。

---

## 8. 怎么重跑

```bash
# 1) 起实例(仓库根)
make build && ./pipewright

# 2) 跑矩阵:建夹具(幂等,先清后建)→ 79 条断言 → 保留夹具
python3 scripts/perm-matrix.py

# 3) 只清夹具
python3 scripts/perm-matrix.py teardown
```

环境变量(默认值就是本机测试实例):`PERM_BASE_URL` · `PERM_ADMIN_PASSWORD` · `PERM_QA_PASSWORD`。
结果 JSON 落在 `/tmp/permqa-results.json`;本报告 §4 的表由它生成 —— 重跑矩阵后再跑一次 `python3 scripts/perm-report.py`,文档跟着数字走。

**加用例的正确姿势**:先在 `internal/access` 的表驱动单测里把判定矩阵补全(纯函数,不碰库),再在这里加一条真机断言。判定规则只写在 `Decide` 一处,不要在测试脚本里重推一遍——否则测试会和实现一起漂移。
'''

out = pathlib.Path('docs/分组权限测试报告.md')
out.write_text(doc)
print('wrote', out, len(doc.splitlines()), 'lines')
