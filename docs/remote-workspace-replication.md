# 服务器「远程」弹窗 —— 完整复刻指南

> 本文以 Pipewright 当前实现为准（分支 `fu-dev`，提交 `d36dd1c`）。每一条契约、每一个常量、
> 每一处"为什么这样写"都来自仓库里的代码，标注了源文件与行号，便于你在另一个项目里逐条核对。
> 目标：**照着这份文档能重写出一模一样的能力**，而不是只能做个大概。

---

## 0. 这个功能是什么

在服务器列表/状态页的每张主机卡片上点「远程」，弹出一个可全屏的窗口：

```
┌─ 远程工作区   <服务器名> · user@host:22      [在大屏终端打开] [⤢全屏] [×] ─┐
│ ┌ 终端条: hostLabel | Shell▾ | LIVE·123ms | 断开/重连 ─────────────────┐ │
│ │  xterm 终端(SSH PTY,默认自动挑 bash/zsh)                            │ │  ← 上半屏(比例可拖)
│ └──────────────────────────────────────────────────────────────────────┘ │
│ ═══════════════ 可拖分隔条(↑/↓ 也能调) ══════════════════════════════════ │
│ ┌ 工具栏 ▤ ↑ ⟳ | ＋ ⬆ ⬆folder | ≡ ⇅(3) [路径输入] [sftp] ───────────────┐ │
│ │ / ▸ home ▸ fubin          ← 面包屑                                    │ │
│ │ ┌ 目录树(宽可拖) ─┬─┬ 文件名 / 大小 / 类型 / 修改时间 / 权限 / 操作 ──┐ │ │  ← 下半屏
│ │ │ ▸ fubin         │ │ a.txt      1.2 KiB  TXT  2026-09-28 21:03 rw-… │ │ │
│ │ └─────────────────┴─┴────────────────────────────────────────────────┘ │ │
│ │ [上传队列: 总进度 + 逐文件行(取消/重试)]  [传输记录(本次会话)]           │ │
│ └────────────────────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────────┘
```

一条能力线的两面：**上半屏在远端跑命令，下半屏读写这台机上的文件**，两者靠 cwd 双向联动。
每个弹窗 = 一台服务器的一个浏览会话；弹窗卸载即断连（不给后台留 PTY，也不留 SSH 连接）。

---

## 1. 文件地图（职责 → 源文件）

### 后端（Go）

| 层 | 文件 | 职责 |
|---|---|---|
| 能力层 | `internal/target/fs.go` | `Workspace` 接口 + **SFTP 实现** + 领域错误 + `FileStat` |
| 能力层 | `internal/target/fs_exec.go` | **exec(shell 兜底)实现**：同一批 POSIX 命令凑出接口 |
| 能力层 | `internal/target/ssh.go:330-445` | PTY 交互会话（`RequestPty` + stdin/stdout 合流 + `WindowChange`） |
| 能力层 | `internal/target/target.go:685` | `ExecInteractive(ctx, serverID, cmd []string)` |
| HTTP | `internal/httpapi/server_fs.go` | 列表 / 读正文 / 写正文 / 下载 / `op(mkdir·remove·rename)` |
| HTTP | `internal/httpapi/fs_upload.go` | 会话式分块上传（begin/chunk/status/complete/abort） |
| HTTP | `internal/httpapi/fs_zip.go` | 目录流式打包 + "静默即断"看门狗 |
| HTTP | `internal/httpapi/container_terminal.go:214-269` | 主机 shell 终端 WS（`makeServerTerminalHandler`） |
| HTTP | `internal/httpapi/terminal_recorder.go` | 命令级留痕：提示符钩子脚本 + OSC 5522 解析 + 会话审计（`server_command`） |
| HTTP | `internal/httpapi/access_guard.go:131` | `requireTerminalOperate`（GET 也按"操作"档判） |
| 路由 | `internal/httpapi/router.go:919-943` | 挂上述端点 |

### 前端（Vue 3 + TS）

| 层 | 文件 | 职责 |
|---|---|---|
| 接口层 | `web/src/api/serverFs.ts` | 每个端点一个函数；**逐块上传用 XHR 不用 fetch** |
| 接口层 | `web/src/api/servers.ts:485-540` | `openServerTerminal` + 共用 WS 泵（`openTerminalWS`） |
| 纯逻辑 | `web/src/lib/serverFs.ts` | 路径/排序/字节/时间/权限串/`shellQuote`/`cdCommand`/OSC7 解析 |
| 纯逻辑 | `web/src/lib/fsUpload.ts` | **上传会话驱动器**：切块、并发、退避、续传、取消、重试 |
| 纯逻辑 | `web/src/lib/fsTransfers.ts` | 传输记录行折算 + 会话内（内存）存储 |
| 组件 | `web/src/components/ops/RemoteWorkspaceModal.vue` | 外壳：布局、上下分隔条、全屏、Esc、双向联动接线 |
| 组件 | `web/src/components/ops/TerminalPane.vue` | xterm + WS + OSC7 解析 + `runCd`（expose） |
| 组件 | `web/src/components/ops/RemoteFilePanel.vue` | 工具栏/面包屑/目录树/表格/内联编辑器/上传队列/传输记录 |
| 入口 | `web/src/views/ServerStatus.vue:50-64,319-337` | 「远程」按钮 → `remoteId` → `v-if` 挂载弹窗 |
| 文案 | `web/src/i18n/locales/<8 语种>/remoteWorkspace.ts` | 全部界面文案（见 §12） |

---

## 2. 分层与连接生命周期

```
浏览器 ──HTTP/WS──> httpapi(鉴权 + 审计 + DTO + 消毒) ──> target.Workspace / Session
                                                          (一条 SSH 连接)  ──> sshd
```

三条纪律（照抄就行，都是踩出来的）：

1. **一次请求/一个批次 = 一条 SSH 连接，用完即弃，不做连接池。**
   `OpenWorkspace` 每次拨一条（`fs.go:108-123`）。理由写在 `fs.go:26-27`：并发量小，
   池化的"失效检测"复杂度不值当。只有**上传批次**例外地把一条连接握久一点（见 §6）。
2. **凭据即用即弃**：`sshTarget` 取出明文私钥/口令 → 建连 → `defer` 立刻清空本地引用
   （`fs.go:120`）。凭据绝不进 WS 帧、日志、审计。
3. **鉴权顺序固定**：先认服务器（404）→ 再判能力（501）→ 最后才解密取凭据。
   "这台机不存在"不该被伪装成"连不上"，不支持的部署也不该白解密一次私钥（`fs.go:106-107`, `fs_upload.go:363-396`）。

---

## 3. 能力层：`Workspace` 接口

`internal/target/fs.go:60-89`，14 个方法。**复刻时先定这层接口，再写两个实现**，
后面 HTTP 层与界面都只认这层。

| 方法 | 语义要点（两实现必须一致） |
|---|---|
| `Realpath(ctx,p)` | 归一 + 解析 `.` → **该 SSH 会话登录后的工作目录**（家目录）。不存在也可返回（协议允许）。 |
| `Stat(ctx,p)` | **跟随符号链接**取"点开要看的东西"的属性；`IsLink`/`LinkTarget` 另记链接自身；**断链仍按链接呈现**，不让整份列表失败。 |
| `ReadDir(ctx,dir)` | 名升序；不返回 `.`/`..`；**目录在前由界面决定**，这层只给稳定原始序。 |
| `ReadFile(ctx,p,limit)` | 读到 `limit` 为止，多读一字节判断 `truncated`。 |
| `OpenRead(ctx,p)` | 流式读句柄（下载用），调用方必须 `Close`。 |
| `WriteFile(ctx,p,r)` | **覆盖写走"临时名 + 原子改名"**，并把原文件权限位搬过去。 |
| `AppendChunk(ctx,p,off,r)` | 从 `off` 追加，返回**已确认落地的末尾偏移**；出错也带回已写好的偏移。 |
| `Chmod(ctx,p,mode)` | 低 9 位。上传覆盖已存在文件时用来保住原来的 600。 |
| `Mkdir(ctx,p)` | 递归（等价 `mkdir -p`）。 |
| `Remove(ctx,p)` | **只删文件或空目录**；非空目录 → `ErrRemoteNotEmpty`。**这层刻意不提供递归删除。** |
| `Rename(ctx,from,to)` | 覆盖式改名（像 `mv`）。 |
| `Backend()` | 回 `"sftp"` / `"exec"`，界面如实显示能力范围。 |
| `Close()` | 幂等（`sync.Once`）。 |

### 3.1 为什么主通道是 SFTP 而不是继续 `Exec`

写在 `fs.go:19-24`，理由要一起复刻：

- 文件面板一次会话要连打十几条「列目录/读文件/改权限」，逐条 Exec = 每次都付一整轮
  TCP + KEX + 认证，点一下等半秒。
- 解析 `ls`/`stat` 在 GNU / BSD / BusyBox 下字段与日期格式都不同（本仓的指标采集已经为此
  写过一堆平台回退）。
- SFTP 是 sshd 自带的子系统，给**结构化属性**，不碰 shell，天然没有注入面 —— 路径只作协议参数。

### 3.2 没有 sftp-server 才降级到 exec（同一连接，不重拨）

`fs.go:129-148`：`sftp.NewClient` 失败时，用**已经拨好的那条连接**去建 `execWorkspace`
（最小镜像 / dropbear 常见）。复用不重拨的理由写在注释里：SFTP 握手失败时连接是好的，
再拨一次只是白付一轮握手。

exec 实现的三条纪律（`fs_exec.go:24-29`）：

1. 复用连接，每个操作只开一条 session。
2. **路径一律作 array 参数经 `quoteArgs` 转义**；脚本正文只认 `$1/$2`，绝不把用户输入插值进 shell 字符串。
3. 进程内不保留 `SSHConfig`，工作区只握连接句柄。

它的几个关键设计，值得原样搬：

- **哨兵字符串**代替匹配 stderr：`PIPEWRIGHT:NOT_FOUND` / `NOT_A_DIR` / `PERMISSION_DENIED` /
  `NOT_EMPTY`（`fs_exec.go:40-49`）。各家实现的错误文案措辞不同，靠 stderr 匹配必误判；
  脚本自己打印哨兵是确定且与 locale 无关的。脚本首行统一 `LC_ALL=C; export LC_ALL`。
- **开机先探测 `stat` 方言**：`stat -c %Y` (gnu) / `stat -f %m` (bsd) / none
  （`fs_exec.go:65-83`）。mtime 只有 stat 能可靠给；两者皆无 → 回 0，界面时间列**留空**（不显示 1970 骗人）。
- **一行式属性协议**：`emit_entry` 按 `kind \t mode \t size \t mtime \t link \t name` 输出，
  `name` 放最后一列配 `SplitN`，这样文件名里有空格也不翻车（`fs_exec.go:97-125`, `445-464`）。
- **非普通文件不取大小**：对 fifo 做 `wc -c <` 会挂死整条 session。
- **`ls -l` 权限串逐位解析**（`parseModeString`，`fs_exec.go:472-489`）：`s/t` 表示特殊位+执行位都置了，
  `S/T` 表示特殊位置了但执行位空 —— 直接取字符比对恰好都判对；认不出的字符按 0（宁可少显示权限，
  也不凭空造一个可写位）。
- **列目录枚举点文件的 POSIX 写法**：`for ent in * .[!.]* ..?*`（`.[!.]*` 排掉 `.`/`..`，`..?*` 补 `...`）。
- **流式下载 = session 的 stdout**：`cat -- "$1"`，`Close` 时先 `Signal(SIGKILL)` 再关 session
  （中途断开否则远端进程驻留），**绝不关连接**（连接是共用的，`fs_exec.go:230-269`）。
- 已知边界要在文档里如实写明：文件名含换行/制表符会破坏行式解析；一次列目录 fork 多个进程，明显慢于 SFTP。

### 3.3 两个坑（实测出来的，务必照做）

**坑 1：SFTP 的 rename 不覆盖。** `renameOver`（`fs.go:397-408`）：本机实测 OpenSSH 9.x + macOS
sftp-server 上 `SSH_FXP_RENAME` 对已存在目标直接回 `SSH_FX_FAILURE` —— 编辑器第二次保存就报错。
顺序：先试 `posix-rename@openssh.com`（原子覆盖）→ 扩展不被认同时，目标不存在走普通 rename →
目标存在则先删再 rename（中间有一瞬"文件不在"，但绝不会让人读到半截正文）。

**坑 2：`os.IsNotExist` 判不出 SFTP 的"不存在"。** `mapSFSErr`（`fs.go:448-469`）必须
`errors.As(&sftp.StatusError)` 再比 `FxCode()`（`ErrSSHFxNoSuchFile` / `ErrSSHFxPermissionDenied`），
否则 404 会被报成 500。

**坑 3：`pkg/sftp` 的调用不吃 ctx。** 它阻塞在 SSH channel 上。所有方法都经 `call[T]` 包装
（`fs.go:473-491`）：ctx 一取消就**拆掉整条连接**，让阻塞中的调用以错误返回，goroutine 不悬挂。
`OpenRead` 返回的句柄只关文件不关连接。

### 3.4 领域错误 → HTTP 契约码

`fs.go:33-44` + `server_fs.go:118-139`，**错误文本绝不带远端 stderr 细节**（诊断留在 `%s` 里给日志，不给响应）：

| 领域错误 | HTTP | code | 文案（按 UI 语言回） |
|---|---|---|---|
| `ErrRemoteNotFound` | 404 | `remote_not_found` | 远程路径不存在 |
| `ErrRemoteNotDirectory` | 400 | `not_a_directory` | 远程路径不是目录 |
| `ErrRemoteNotEmpty` | 409 | `directory_not_empty` | 远程目录非空（平台不提供递归删除） |
| `ErrRemotePermission` | 403 | `remote_permission_denied` | 远程路径权限不足 |
| `ErrFSUnsupported` | 501 | `fs_unsupported` | 该服务器没有 SFTP 子系统，也无法用命令兜底 |
| `context.DeadlineExceeded` | 504 | `fs_timeout` | 远程文件操作超时 |
| `ErrUnreachable` | 502 | `server_unreachable` | 无法连接服务器：端口未开放、主机不可达或超时 |
| `ErrAuth` | 502 | `ssh_auth_failed` | SSH 认证失败：密钥或口令无效，或无登录权限 |
| 其余 | — | 走既有 `writeServerError` | 服务器不存在(404)/凭据/vault |

---

## 4. HTTP 契约（文件面板）

所有端点都在 `/api/servers/{id}/fs*` 下，**GET 也按"操作"档鉴权**（理由见 §9）。
超时口径固定在 `server_fs.go:38-49`：

| 常量 | 值 | 用途 |
|---|---|---|
| `fsTextReadLimit` | 1 MiB | 编辑器一次取回的正文上限，超出截断并如实标 `truncated` |
| `fsJSONBodyLimit` | 8 MiB | 保存正文 / op / 上传控制请求的 body 上限 |
| `fsPathMax` | 4096 字符 | 路径长度上限（只为挡住"把整份文件当路径提交"的误操作） |
| `fsMetaTimeout` | 30 s | 列目录 / stat / op 这类轻操作 |
| `fsDataTimeout` | 5 min | 读写正文、上传批次建立 |
| `fsStreamIdle` | 2 min | 下载流的**静默**上限（不是总时长） |

### 4.1 `GET /api/servers/{id}/fs?path=`

`path` 可为空 = 家目录。响应（`fsListDTO`，`server_fs.go:55-62`）：

```json
{
  "path": "/home/fubin",       // 后端 Realpath 后的真实绝对路径
  "backend": "sftp",           // 或 "exec"
  "isDir": true,
  "entries": [ /* FileStat[] */ ],   // isDir=true 时有
  "entry": { /* FileStat */ },       // isDir=false 时有(界面据此画预览而不是报错)
  "serverId": "…"
}
```

`FileStat`（`fs.go:47-56`，前端 `FsEntry` 与之逐字段对齐）：

```json
{ "name":"a.txt", "path":"/home/fubin/a.txt", "isDir":false, "isLink":false,
  "linkTarget":"b.txt", "size":1234, "mode":420, "mtime":1759000000 }
```

`mtime` 是 Unix 秒，**0 = 对端没给时间**；`mode` 只有低 9 位权限。

### 4.2 `GET .../fs/content?path=`

`path` 必填。响应：`{ path, content, size, truncated, binary }`。
`binary` 由服务端判（`looksBinary`，`server_fs.go:472-481`：**前 8 KiB 有 NUL，或整体不是合法 UTF-8**），
为 true 时 `content` 恒为 `""`（不把镜像/压缩包灌进编辑器，也白占响应体）。
`size` 是**本次返回的正文字节数**，不是远端文件大小。

### 4.3 `POST .../fs/content`（覆盖写）

请求 `{ path, content }`；响应 `fsResultDTO { ok, path, bytes, backend }`。
审计一条 `{op:"save", path, bytes, ok}` —— **只记动作/路径/体积，绝不记正文**
（面板里读的可能是密钥，`server_fs.go:282-291`）。

### 4.4 `POST .../fs/op`

请求 `{ op, path, to }`。**op 是枚举白名单**，只有 `mkdir | remove | rename`（`fsOps`，
`server_fs.go:94-98`），其余 400 `invalid_op`。`rename` 必须给 `to`。用 `ws.Remove/Rename/Mkdir`，
审计 `{op,path,to,ok}`。

### 4.5 `GET .../fs/download?path=`（文件 / 目录两用）

`server_fs.go:310-403`。界面**不用 fetch**，直接 `<a :href>` 交给浏览器（见 §8.3）。

- 先 `Realpath` + `Stat` 判类型（错误还能回状态码；一旦开写就只能掐连接）。
- **文件**：`application/octet-stream` + `Content-Length`（有长度才给）+ `Content-Disposition: attachment; filename="<基名>"`。
  基名经 `sanitizeRemoteName` 去过换行/引号 —— 否则那是一个**响应头注入点**。
- **目录**：`application/zip` + `filename=<目录名>.zip`，然后流式打包（§7）。
- 超时用"静默看门狗"：`newIdleWatch(2min, cancel+ws.Close())`，每有字节流动就 `touch`。
  理由写在 `fs_zip.go:28-30`：**总时长上限是错的口径** —— 10 GiB 该传多久取决于链路带宽，平台猜不出来；
  真正该断的是"一秒字节都不动"。
- 审计：文件 `download {bytes}`；目录 `download_zip {entries, bytes, skippedLinks}`。

### 4.6 名字与相对路径的消毒（两处，规则要一致）

- `sanitizeRemoteName`（`server_fs.go:155-165`）：折成**基名**（替换 `\`→`/`、取 `path.Base`、去尾斜杠），
  拒空 / `.` / `..`，长度 ≤255。**点开头是合法文件名**（`.env`、`.gitignore`），不能连点一起削 ——
  否则传文件夹会变成"整批静默改名，用户看到的是我传的配置呢"。
- `sanitizeRemoteRel`（`fs_upload.go:334-361`）：客户端只能给相对路径；逐段套用上面的规则，
  丢空段与 `.`，**拒 `..` 与绝对路径**，深度 ≤ `fsUploadMaxDepth=32`，总长 ≤4096。
  文件夹的子目录结构由此保留，路径穿越由此挡掉。
- **绝对落点由服务端算**：`dest = path.Join(批次dir, rel)`，界面再怎么写也塞不进任意路径。

---

## 5. 终端 WebSocket（主机 shell）

`GET /api/servers/{id}/terminal?shell=&locale=`（`container_terminal.go:214-269`）。
**这是全平台唯一的 WS 升级点**，其余实时流都走 SSE —— 复刻时保持这个约束，
WS 只服务交互式终端。

### 5.1 握手与安全

- 升级是 GET，先过会话鉴权（未登录 → 401，不会升级），再过 `requireTerminalOperate`
  （终端 = 在目标机跑任意命令，按**操作**档，不是查看档）。
- **同源校验代替 CSRF**：GET 豁免 CSRF，因此以 `OriginPatterns: [r.Host]` 拒绝跨站 Origin，防跨站 WS 劫持
  （`originPatterns`，`container_terminal.go:387-393`）。
- `shell` 走**枚举白名单** `allowedShells`（`/bin/sh|/bin/bash|/bin/ash|/bin/zsh|/usr/bin/{sh,bash,zsh}|sh|bash`）；
  不在表内 → 400 `invalid_shell`（升级前用普通 HTTP 状态码拒绝）。
- **命令 array 化**：`cmd = []string{shell}`，绝不拼 shell 字符串。
- 语言：浏览器不能给 WS 升级请求设自定义头，所以前端用 `?locale=` 带；服务端
  回退 `Accept-Language` → 默认语言（`terminalLocale`）。
- 握手成功（PTY 已建立）后写审计 `server_terminal {shell, kind:"host", op:"start"}`；
  **自动模式审计里如实记 `"auto"`**，别记成 `/bin/sh` 骗过后面的追责（`container_terminal.go:247-251` + `259-261`）。
  这场会话里每执行掉一条命令再补一行 `server_command`，收尾补一条 `op:"end"` 汇总 —— 细节见 §8.4。

### 5.2 未指定 shell 时"在远端现挑"

`autoHostShellArgv()`（`container_terminal.go:200-205`）—— 整段是**常量脚本**，候选路径写死、
不含任何用户输入、也不含单引号，因此过 `quoteArgs` 后仍是一个 argv：

```sh
/bin/sh -c 'for s in bash /bin/bash /usr/bin/bash zsh /bin/zsh /usr/bin/zsh; do
  command -v "$s" >/dev/null 2>&1 && exec "$s"; done;
  if [ -n "$SHELL" ] && [ -x "$SHELL" ]; then exec "$SHELL"; fi; exec /bin/sh'
```

三个要点：
1. 优先 bash/zsh —— **只有这两个有提示符钩子**，上下联动与补全靠它们；其余 shell 一条命令也报不上来
   （见 §8.4，审计里以 `hook=silent` 如实留痕）。
2. 起手解释器是 `/bin/sh`：它比 bash 普遍得多（Alpine 上就是 ash），不引入新的前置要求。
3. 用 `exec` 起 shell：PTY 的前台进程就是 shell 本身，退出即会话结束，中间不剩一层 sh。

（容器终端同理，只是候选少一档、且是 `docker exec -it <id> /bin/sh -c '…exec bash…'`，
容器 ID 还额外过 `^[\w][\w.-]*$` 白名单——首字符强制 `\w` 是为了**防 flag 注入**。）

### 5.3 帧协议

| 方向 | 类型 | 内容 |
|---|---|---|
| 浏览器 → 服务端 | 文本帧 | 普通终端输入（原样写入 PTY stdin，含 `\r`） |
| 浏览器 → 服务端 | 文本帧 | **resize 控制帧**：`{"type":"resize","cols":N,"rows":M}` |
| 服务端 → 浏览器 | 二进制帧 | PTY 输出，按 32 KiB 块（`ptyReadChunk`）转发 |

- `parseResize`（`container_terminal.go:338-352`）：必须以 `{` 起头且 `type=="resize"` 才算控制帧，
  否则当普通输入 —— 这个"快速短路 + 宽容失败"的顺序别改，不然用户敲 `{` 会被吞。
- 双向泵 `pumpTerminal`：任一方向出错/结束就 `cancel`，解除另一方向的阻塞读/写；
  `defer` 关 WS + 关会话（不泄漏 goroutine）。远端异常中止以 close 帧人读告知（`session ended`），
  错误文案经 i18n（`humanTerminalError`，映射到 `ssh_auth_failed` 等四类），**不泄密、不带栈**。
- 单帧上限 `wsReadLimit = 1 MiB`；close reason 截到 120 字节（协议上限 125）。

### 5.4 SSH 侧 PTY 细节（`ssh.go:345-445`）

- `RequestPty("xterm-256color", 24, 80, {ECHO:1, ISPEED/OSPEED:14400})`，初值随后被前端 `fit` 的
  `WindowChange` 覆盖。
- **stdout 与 stderr 合流到同一个 `io.Pipe`**（交互终端里二者本就交织呈现）。
- 远端进程退出 → `session.Wait()` 返回 → 关 pipe 写端，读侧得到 EOF（否则 `Read` 永久阻塞）。
- `ctx.Done()` → 主动 `Close`（客户端断开/超时都不留悬挂会话）。
- `Resize(cols,rows)`：非法尺寸归一为 1；`WindowChange(rows, cols)` 失败**忽略**（非致命）。

### 5.5 前端 WS（`api/servers.ts:495-540`）

- URL：`${wss:|ws:}//${location.host}${path}`，`binaryType='arraybuffer'`；同源自动带会话 Cookie。
- `onmessage` 把 ArrayBuffer / string 都转成 `Uint8Array` 交给 `onData`（组件里用**一个常驻
  `TextDecoder` 且 `decode(chunk,{stream:true})`** —— 逐块新建 decoder 会把多字节字符和 ANSI 序列切碎）。
- `onerror` 故意什么都不做：浏览器会先抛一个笼统 error 再抛 close，人读文案统一在 `onClose(reason)` 给。
- 句柄只暴露 `send / resize / close`；`close()` 之后服务端拆 SSH PTY。

---

## 6. 分块上传协议（一个文件从选中到落地）

### 6.1 为什么换掉 multipart（`fs_upload.go:28-44`，这段论证要一起搬）

- multipart 超出内存上限的部分会先落**平台本机磁盘**再转发远端 —— 平台盘成了上传缓存，
  几十 GB 的镜像一丢就见底，还白付一次落盘 + 读回。
- 一次 POST 要么整份成要么全废：断线、刷新页面、慢链路撞总时长上限，前面传的全部作废。
  **分块 + 服务端记账才谈得上"续传"。**
- 分块顺带给界面逐文件进度与逐块重试，不用把整份文件读进内存才算进度。
- 界面因此不 preventDefault、也不用 fetch：**逐块要真实进度，fetch 报不出上传进度**，
  只有 `xhr.upload.onprogress` 给得出（`serverFs.ts:145-209`）。

### 6.2 端点与时序

```
POST /fs/upload/begin   { dir, files:[{rel,size}] }
   → { ok, batchId, dir, backend, files:[{rel, uploadId, path, offset, size}] }
      · 服务端在这一次握手里把批次需要的父目录一次性建齐(按深度升序,父先于子 → 子层省掉重复往返)
      · 逐 rel 消毒 + 算 dest;有一个非法 → 整批 400(不给"半批落地")
      · dest 重复 → 409 duplicate_file;files 数量须在 1..5000
      · uploadId = 16 字节随机 hex(crypto/rand);取不到宁可 500 也不发一个可猜的 id
PUT  /fs/upload/chunk?uploadId=&offset=     请求体=裸字节(不是 multipart)
   → 200 { ok:true,  batchId, offset, files:[{uploadId, offset}] }
   → 409 { ok:false, batchId, offset, error:{code:"offset_mismatch"} }   ← 带权威偏移
   → 409 chunk_in_flight(同文件前一块还在飞) / upload_closed(已落地或会话结束)
   → 404 upload_not_found / 403 forbidden(不是发起人) / 413 chunk_too_large / 408 upload_timeout
GET  /fs/upload/status?uploadId=   → 偏移一律取**远端临时文件的实测长度**(不拿进程簿记糊人)
POST /fs/upload/complete  {uploadId} → 临时名落成正式名(先搬权限位再 renameOver)
   → 409 incomplete_upload {offset}   // 只收到 offset/total,请续传
POST /fs/upload/abort     {uploadId} → 删掉远端半成品(几 GB 的截文件留在盘上没人认领,比传失败更糟)
```

### 6.3 服务端状态机（`fsUploadStore` / `fsUploadBatch` / `fsUploadTask`）

| 常量 | 值 | 说明 |
|---|---|---|
| `fsChunkMaxBytes` | 64 MiB | 单块请求体硬上限；超了 413，客户端该切小重发 |
| `fsUploadMaxFiles` | 5000 | 一个批次的文件数上限（文件夹上传的实际约束） |
| `fsUploadMaxBatches` | 64 | 存活批次上限（每批握一条 SSH 连接，得有天花板） |
| `fsUploadIdleTTL` | 30 min | 批次静默多久回收（关弹窗/刷新页都算正常弃传） |
| `fsChunkTimeout` | 5 min | **单块**超时，不是整场上传 |
| `fsUploadMaxDepth` | 32 | 相对落点目录深度 |

- **一个批次共用一条 SSH 连接**：逐文件重拨的话，500 个文件的文件夹要先付 500 轮握手。
- 远端半成品是**隐藏临时名** `.pipewright-up-<uploadId前12位>.tmp`（点前缀 → 列目录看不见半成品，
  用户也手抖删不到它）。
- `alignOffset`（`fs_upload.go:646-675`）：簿记与客户端申报不一致时以**远端实测长度**为准，
  并把差值如实回给用户（409 + offset）。半截块不回收 —— 它已落在 EOF 之前，下一块接在其后仍拼得回完整原文。
- 一块出错就 `dropWS()`（无法确定对端连接还剩什么状态，下一块重拨一条）。
- `context.WithoutCancel(r.Context())`（`fs_upload.go:594-597`）：客户端断开**不该拆掉整条批次连接**
  （同批别的文件还在传）；请求体一断，读侧自然报错，块就停在已落地那儿。
- 零字节文件：客户端一块都不会发，`complete` 里补一次空 `AppendChunk` 把文件建出来，后面的 rename 才有主体。
- **覆盖已存在文件先保权限**：`Stat(dest)` 拿到原 mode → `Chmod(tmp, mode)` → `Rename(tmp,dest)`；
  搬权限失败不足以让整次上传失败（内容才是这次动作的主体）。
- 批次归属：`batch.userID` 与请求者比对 —— 换个人拿着同一个 `uploadId` 也不能接着传。
- 回收（`expire`）：拨不上就别在后台反复重拨"那是用户已经关掉的那次上传"；能拨上就把所有未完成
  任务的临时名删掉。
- 审计：每个文件落地一条 `upload {path,bytes,ok[,error]}`，abort 一条 `upload_abort`；
  **逐块不记**（大文件几百块会把审计表刷满，"谁传了个什么上去"才是该留的那条事实）。

### 6.4 客户端驱动器（`lib/fsUpload.ts`）

界面只喂 `File` 与渲染进度，全部算术在这一个纯逻辑模块里（所以断线重连、退避、逐块推进
都能在单测里跑，见 §15）。

- **块大小按文件体积分档**（`chunkSizeFor`）：≤32 MiB → 4 MiB；≤1 GiB → 8 MiB；≤8 GiB → 16 MiB；
  更大 → 32 MiB。理由：块越大往返越少、越压得出带宽，但浏览器一次要交出 `chunkSize × 并发数` 的内存。
- 并发 `parallel` 默认 2（夹在 1..4）：同批次共用一条连接，两路能把往返 latency 藏掉一半。
- `maxRetry` 默认 4；退避 `backoffDelay(attempt, base=400ms, cap=6s)` = 指数封顶。
- **409 不是失败，是重新定位**：`uploadResumeHint` 只在 `offset_mismatch` / `incomplete_upload`
  时读 `body.offset`，读到就接着推，**不消耗重试次数**。
- 可重试判定（`isRetryableUploadError`）：`chunk_in_flight` / `upload_timeout` / `upload_busy` /
  `status 0` / 408 / 429 / 5xx。**本地异常（没有明确 status）不重试** —— 否则切片越界这类代码 bug
  会被洗成"网络不好"。
- 退避醒来后**先问一次 `status`** 拿真实长度：断过线就不信本地簿记。
- 状态机：`queued → uploading → done | error | canceled`；`retry(key)` 把 `error/canceled`
  拉回 `queued` 并插到队首；`cancel(key)` 对排队项直接摘队，对**在飞项 abort 那个 XHR**，
  pump 的 catch 走取消分支并 `abort` 远端半成品。
- 进度：`onLoaded(loaded)` 由 `xhr.upload.onprogress` 驱动（末块报不满 100%，所以界面在
  `status==='done'` 时**强制显示 100%**，`RemoteFilePanel.vue:644-648`）；
  总进度**按字节加权**，不按文件数（传 10 GB 和传 10 个空文件不等价，`sessionProgress`）。
- 服务端不认识浏览器给的相对路径之外的任何东西：`relOfPickedFile` 用 `webkitRelativePath`
  （含顶层目录名，形如 `proj/src/a.c`），没有它才退回 `file.name`，**旧浏览器不猜路径**。

---

## 7. 目录下载 = 平台侧流式 zip（`fs_zip.go`）

**为什么不在远端 tar 再管道出来**（`fs_zip.go:18-21`）：那要求对端有 tar，还要把路径拼进一条命令；
面板本来就在没有 sftp-server 的机器上用 sh 兜底，再叠一层"远端打包"等于把下载绑死在远端工具链上
（BusyBox 的 tar 与 GNU 的 tar 连报错措辞都不同）。现在用的 `ReadDir + OpenRead` 是两条实现都具备的能力。

| 常量 | 值 | 作用 |
|---|---|---|
| `fsStreamIdle` | 2 min | 一条流允许静默多久 |
| `fsZipMaxEntries` | 20000 | 条目上限（文件+目录） |
| `fsZipMaxDepth` | 64 | 递归深度上限（软链已不跟随，这道保险挡病态深树与环形挂载） |
| `zipCopyBuf` | 256 KiB | 逐块搬运缓冲 |

四条规则：

1. **符号链接一律不跟随也不写入**，计数进审计 `skippedLinks`，界面老实说明
   "这个目录里的链接没打进包"（跟随时软链能把一次下载变成环；不跟随又会在包里凭空少东西）。
2. **每个目录都写一条目录项** —— 空目录否则解压后不存在。目录项的 `Modified` 留空（远端没给时间时不编一个）。
3. 按扩展名选压缩法：`storedExt`（zip/gz/tgz/bz2/xz/zst/7z/rar/jar/war/apk/deb/rpm/图片/视频/音频/pdf/wasm/so/dylib/whl）
   直接 `Store` —— 远端已经压过，再 deflate 只是拿 CPU 换一点几乎不存在的收益还拖慢整条链；其余 `Deflate`。
4. **撞上限就掐断连接**（`panic(http.ErrAbortHandler)`）：响应头早已发出，状态码改不了；
   一个看起来完整其实少一半的压缩包，比一次失败的下载危险得多。
5. `archive/zip` 流式写（长度未知的条目用 data descriptor 收尾），**不预设 Content-Length**
   （小目录恰好落进响应缓冲时 Go 会自己补一个 —— 不能依赖），所以界面按"下载中"而不是百分比显示。
6. 列到目录就 `touch()` 一次：全是空文件的大目录，正文侧几乎没字节流过，只按字节算"静默"会把这种正常打包掐掉。

---

## 8. 前端三个组件的细节（照着抄才对得起"完整复刻"）

### 8.1 外壳 `RemoteWorkspaceModal.vue`

- props：`serverId / serverName / hostLabel(user@host:port) / shell('' = 自动)`；
  只 emit `close`。**挂载即连、卸载即断**。
- 尺寸：`width:min(1180px,100%)`，`height:min(92vh,1000px)`，圆角 14px；全屏时把
  padding 归 0、外壳铺满视口、去边框圆角阴影（**写在 `.rw-modal` 之后**，同特异度靠顺序覆盖）。
- 上下分屏：`split` 比例存 localStorage 键 **`pw-remote-split`**，默认 0.46，夹在 0.18..0.82。
  拖动用 `pointerdown/move/up`（只认左键），拖动期间给 backdrop 加 `--dragging`（禁 `user-select`，
  光标 `row-resize`），松手写存储并 `termPane.focus()` 触发重新 fit。
  分隔条是 `role="separator" aria-orientation="horizontal" tabindex="0"`，**↑/↓ 各调一档（±0.04）**
  —— 鼠标不便用的场景不该被一条拖不动的分隔条锁死。
- 全屏开关（⤢/⤡）与 × 同尺寸（30×30），避免顶栏两个按钮一高一低。
- Esc 规则（三条，都是必要的）：
  1. 事件目标在 `.term-pane` 内 → **不处理**（终端里 Esc 是 shell/vi 的按键）；
  2. 全屏中 → 先退回卡片大小（铺满整屏后"关掉"和"缩回去"都该有反悔的路）；
  3. 否则才 `emit('close')`。
- 点 backdrop 自身（`@click.self`）关闭。
- 联动接线只有两行：`@cwd="terminalCwd=$event"`（终端 → 面板，作为 prop 传下）、
  `@cd="termPane.runCd($event)"`（面板 → 终端）。

### 8.2 终端 `TerminalPane.vue`

- 定位：**只做弹窗需要的最小闭环**（连上、能打字、能复制、尺寸跟着分屏拖动重算）。
  AI 补全/右键菜单/氛围样式属于全屏驾驶舱页，挤进弹窗只会糊。
- xterm **动态 import**（`await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')])` + css）；
  `Terminal` 实例用 `shallowRef`（深代理会拖垮终端）。
- 配置：`fontSize 13`、`fontFamily "JetBrains Mono", ui-monospace, …`、`lineHeight 1.15`、
  `scrollback 4000`、`cursorBlink`，主题底色 `#0b0d10`、前景 `#e6e6ea`、光标 `#7fe3f0`。
- **字体晚到的处理（必抄）**：xterm 在 canvas 上一次性测量单元格，字体晚到会把行数算多、末行溢出容器外。
  做法：`open` 前 `document.fonts.load('13px "JetBrains Mono"')`，`open` 后 `refit()`，
  再在 `document.fonts.ready` 后把 `fontSize` 改成 14 再改回 13（强制重测）并 `refit()`。
- `refit()`：`fit.fit()` 之后再按真实几何收敛 —— `FitAddon` 偶尔多算半行，末行会被容器裁掉；
  循环最多 4 次把 `rows` 减到屏幕底边越过容器底之前，然后 `conn.resize(cols,rows)`。
- 容器尺寸变化靠 **`ResizeObserver(termHost)`**；分屏拖动/全屏切换时另外主动 `focus()` 双保险。
- 复制粘贴（弹窗里终端不是唯一焦点来源，下面就是文件面板）：
  **选中即复制**；`⌘/Ctrl+C` 有选区时复制选区、无选区时透传成 SIGINT；`⌘/Ctrl+V` 粘贴剪贴板文本。
  仅在 `window.isSecureContext && navigator.clipboard` 时启用；用户拒绝授权就安静地什么都不做。
- 状态条：`LIVE · <握手毫秒>` / `连接中…` / 错误文案；右侧 断开 / 重连 / 连接 按钮。
  握手毫秒用 `performance.now()` 在 `onOpen` 里算。
- `onClose` 时往终端里补打一行淡色的 `── <原因> ──`，让用户看见会话为什么没了。
- 切换 shell 下拉（含"自动(优先 bash)"）时，若已连接则**整条重连**。
- expose：`runCd / connect / disconnect / focus`。

### 8.3 文件面板 `RemoteFilePanel.vue`

**工具栏（一行小图标按钮，顺序即分组）**：
`▤ 收起/展开目录树` · `↑ 上一级`(根时禁用) `⟳ 刷新` | `＋ 新建目录` `⬆ 上传文件` `⬆folder 上传文件夹` |
`≡ 队列折叠`(队列有内容才可点) `⇅ 传输记录` + 条数徽标 | 路径输入框(回车=导航) | backend 徽标(`sftp`/`exec`)。
两个隐藏 `<input type=file>`：一个 `multiple`，一个 `multiple webkitdirectory`。
**选完必须把 `input.value=''` 清掉**，否则同一个文件再选一次不触发 change（`RemoteFilePanel.vue:604`）。

**面包屑**：`pathCrumbs` 把路径摊成"每段都能点"的按钮（首段是 `/`），当前段高亮。

**左侧目录树**（FinalShell 右半屏的版式）：
- 节点 `{name,path,children:null|[],loading,expanded}`；`children===null` = 还没加载过。
- **只列目录、点开哪层才问哪一层**（`loadChildren` 过滤 `isDir`）：一次列目录若顺手把整棵子树拉平，
  慢机器上首屏要等几十次 SSH。
- 读失败**保持 `children=null`**：下次点开还能重试；主列表已经如实报过错，树不该把同一个错误再演一遍。
- `treeRows` 深度优先摊平（折叠节点连子树一起跳过），行上用 `--depth` 变量做缩进。
- `revealInTree(path)` 导航后展开到当前位置，带一个 **`treeSeq` 递增号**：后一次导航作废前一次，深路径不会串位。
- `invalidateTree(path)`：建目录/改名/删除/上传落地后让对应那一层 `children=null` 并重新加载，
  不然树停在旧快照上骗人。
- 树宽可拖：localStorage 键 **`pw-remote-tree-w`**，默认 190px，夹 140..520，
  **同时受"右列至少留 260px"约束**（`clampTreeWidth`）；`ResizeObserver(.fs-cols)` 在容器变窄
  （退出全屏）时把宽度夹回去，别把列表挤没；拖完写存储；`←/→` 各调 16px；树收起时**分隔条一起消失**。
  CSS 用 `flex:0 0 17px; margin:0 -8px` —— 视觉上线条 1px，命中区 17px；`.fs-tree{flex:0 0 190px}`
  保留作无 JS 兜底，运行时用 `:style="{flex: '0 0 Npx'}"` 覆盖。

**右侧表格**：列 = `文件名 / 大小 / 类型 / 修改时间 / 权限 / 操作`。
- 目录**不给体积**（两路后端回的都是 0 或块数，画成 `0 B` 是假数据，FinalShell 同样留空）。
- 名称列：目录/文件/链接三种内联 SVG 图标；链接在名字后跟 `→ <linkTarget>`；点目录=导航，点文件=编辑器。
- 类型列是中文名：`文件夹 / 链接 / {EXT} 文件 / 文件`（`fileExt` 对 `.bashrc` 这种点开头隐藏文件回空串，
  否则会显示成"BASHRC 文件"）。
- 权限列 `modeToLs(entry)` = `ls -l` 式 10 位串（**类型首字符问 isDir/isLink，不从 mode 里猜**，
  因为后端两路都只回低 9 位）。
- 操作列：`下载`（**`<a :href>`**，目录也能下 → 服务端自己判并走 zip）`编辑`(非目录) `重命名` `删除`。
  下载点击时 `noteDownload(e)` 只补一条记录，**不 preventDefault**（拦了就等于把用户要的文件拦掉了）。

**内联编辑层**（不是第二层弹窗）：理由写在头注释 —— 叠层弹窗的焦点/滚动/Esc 管理在窄分屏里最容易出错，
而这里只需要"看正文 + 改 + 存"。
- `editing` 与列表层 `v-if/v-else` 互斥；导航/发起 op 前先 `closeEditor()`（两层的 Enter 会打架）。
- 头：完整路径 + `truncated` 警告徽标 + 关闭按钮；body：`textarea`（`binary` 时显示"二进制文件"提示而不是
  一个空框；加载中 `readonly` + placeholder）；脚：脏标记 + 保存按钮（`binary|loading|!dirty|saving` 时禁用）。
- 保存成功后把 `editorOriginal` 对齐当前正文（脏判定随之复位），并**静默重列当前目录**（大小/时间变了）。

**建目录/改名 = 列表区顶部一条内联表单**（不是 `prompt()`）：`autofocus`、Esc 收掉、提交前
`badName` 校验（空 / `.` / `..` / 含 `/`）—— **与后端 `sanitizeRemoteName` 同口径**，别让用户撞 400 才知道。

**删除**：走平台的二次确认弹窗（danger 变体）。文案按类型分开：目录那条必须说清"只有空目录才删得掉"，
不给"会连带删干净"的错觉。**界面上根本没有递归删除按钮** —— 远程 `rm -rf` 不该是弹窗里顺手能点到的东西。

**上传队列**：头部一行 = `已 n / 共 m` + 失败数 + **总进度条（按字节加权）** + `已传 X / 共 Y` +
（在传时）取消全部 / （结束后）清空。逐文件行 = `rel + 进度条 + 状态文案 + offset/size + 取消|重试`。
只渲染前 **200 行**（`QUEUE_ROWS`），整批计数照常算，DOM 不为 5000 个文件排队；
超出显示"还有 n 条未显示"。批次期间**不给再选**（两个批次抢同一条连接池会把进度搅乱）。
错误文案按**错误码**映射（`uploadErrorText`：network/upload_timeout/chunk_in_flight→网络；
upload_busy→对端忙；upload_not_found/upload_closed/incomplete_upload→会话已过期；forbidden→无权；
fs_unsupported→不支持；invalid_path/bad_request/duplicate_file/chunk_too_large→被拒绝；其余→其他），
**不把后端英文原文糊上去**。

**传输记录**（默认收起，靠工具栏那个数字徽标做入口 —— 功能不该藏在没人发现的角落里）：
行 = `↑/↓ + 名字 + (上传才有)进度条 + 状态 + 字节 + 时刻(时:分:秒)`，只渲染前 **120 行**。
两条口径是用户定的，代码必须如实反映（`lib/fsTransfers.ts:4-13`）：
- **下载只记「已发起」**（`status:'started'`）。字节流不经过 JS，成功与否平台无从得知，画成"已完成"就是撒谎。
  目录 zip 体积发起时未知 → `size:null`，界面画不出条也只写体积空着。
- **只活在当前标签页**：存储是模块内存表（`Map<serverId, rows>`），关弹窗还在、刷新即空；
  **不写 localStorage、也不问服务端**（远端路径本身是敏感信息，没必要留在盘上；持久账目那是审计的活）。
- 记录行身份是 `批次号#批次内序号`：驱动器的 `key` 只是序号，跨批次会撞。
  `at`（发起时刻）由调用方在**发起批次时取一次**并原样传进每一帧 —— 每帧现取 `Date.now()` 会让
  "发起时刻"一路漂到最新。
- 并进记录时**已有 id 原位覆盖、新 id 放最前**：若按"整批提到最前"排，后面新起的下载会被在传的批次
  反复压下去，界面就一直跳。
- 上限 500 行，裁剪时**先丢落定的旧行、再丢在传的**（半路进度条凭空消失比丢几条历史糟糕得多）。

### 8.4 cwd 双向联动（这是这个功能最"值钱"的地方）

```
终端 → 面板:  远端 shell 每次出提示符打一条 OSC 7 → 解析成绝对路径 → emit('cwd') → 面板列那份目录
面板 → 终端:  点目录/面包屑/手填路径 → emit('cd') → 往 PTY 打 cd '<转义后的路径>'\r
```

- 钩子**由服务端注入**（`internal/httpapi/terminal_recorder.go` 的 `commandHookScript`，在 SSH 会话
  建好之后、开始双向泵之前往 PTY stdin 写那一行）。前端不参与：谁连上来都同一份钩子，也不会
  因为前端漏发就录不到。（早期版本是前端 `onOpen` 里 `send(cwdReportScript())`，注入脚本还散在
  `lib/serverFs.ts` 里 —— 移回服务端后这两处都删了。）
- 一次注入干两件事：① OSC 7 报 cwd（文件面板联动）；② 私有 OSC 5522 报
  「刚才那条命令 / 当时的目录 / 退出码 / 当时生效的账号」，服务端在输出流上顺路解出来落审计。
  字节**只读不改写**，转给前端的流一个不动。
- 脚本正文的形状（细节见该文件的注释）：

  ```sh
  __pw_cmd(){ … }; __pw_report(){ local e=$? c u; c=$(__pw_cmd); …;
    printf '\033]7;file://%s%s\007' "${HOSTNAME:-local}" "$PWD";
    printf '\033]5522;%s\007' "$(printf '%s\037%s\037%s\037%s' "$u" "$PWD" "$c" "$e" | base64 | tr -d '\n')";
    [ -n "${__pw_prev:-}" ] && eval "$__pw_prev"; return $e; };
  PROMPT_COMMAND=__pw_report   # bash；zsh 改成 precmd(){ __pw_report; }
  ```

  踩过的四条，缺一条钩子就静默不工作：
  1. **整段是一行，且以真 CR(0x0D) 结尾**：PTY 是行编辑，写成字面 `\r` 的话 shell 收到的是
     普通字符，这行永远不提交，钩子装不上还看不出报错。
  2. `PROMPT_COMMAND` 赋的是**函数名**，值里不留 `$PWD` —— 内联表达式的坑还在（双引号会在赋值
     那一刻展开，之后每次报的都是登录目录），换成函数名等于把求值推到每次提示符。
  3. bash 分支先把原 `PROMPT_COMMAND` 存进 `__pw_prev` 再在钩子尾部 `eval` 回去，并 `return $e`
     原样交回上一条的退出码 —— 直接覆盖会静默废掉用户自己的提示符钩子（macOS bash 3.2 实测）。
  4. **整段绝不能出现裸 `!`**：交互式 zsh 会对它做历史展开，整行注入直接 `event not found` 而
     不执行，钩子于是静默失效。去掉 `history` 行首编号用 `sed`，不是 `${h%%[![:space:]]*}`。
- 「刚执行的那条」按 shell 分开取（pty 实测，别想当然）：bash 3.2 在 `PROMPT_COMMAND` 里
  `fc -ln -1` 拿到的是**上上一条**（事件号已经往前走），`history 1` 才对；zsh 反过来 ——
  `fc -ln -1` 正是刚跑完那条，而它的 `history` 是 `fc -l` 的别名。`HISTTIMEFORMAT` 要在子 shell
  里清掉，否则用户设了它时首行是 `#时间戳`。
- 载荷走 **base64**：命令行可能是任意 UTF-8（中文路径很常见），而 base64 字母表不含
  ESC/BEL/US，框架字符绝不会被内容伪造。分隔符用 `\037`(US)。
- 已知边界（都写在审计里，不假装全知）：只有 bash/zsh 有提示符钩子，dash/ash 一条都报不上来，
  会话结束的汇总行记 `hook=silent`；用户 `unset PROMPT_COMMAND` / `set +o history` 可以回避回报；
  远端把命令排除在历史之外（`HISTCONTROL=ignorespace`/`HISTIGNORE`）时报上来的可能是上一条。
- 解析端：`parser.registerOscHandler(7, …)`，`pathFromOsc7` 容忍 `OSC 7 ; file://host/path` 与
  `file://host/path` 两种形状，取 `file://` 之后第一个 `/` 起的路径并 `decodeURIComponent`
  （**解失败就用原文**，不让一个坏转义把整条联动打死）；handler 返回 `true` = 已消费，
  不再落到终端渲染（OSC 7 本来也不显示成文本，用户看不到噪声）。
- **防回声（最关键）**：面板 `watch(terminalCwd)` 时先归一再比对 `currentPath`，
  相同就直接返回。不比较就是自己触发自己：面板 cd → 终端回报同一路径 → 面板重新列目录，
  白多一次 SSH、列表还闪一次。同理 `navigate()` 里 `target===currentPath` 也不动。
- **自动模式没法预先知道远端挑了哪个 shell**：唯一能证明"这台机的 shell 有提示符钩子"的证据
  就是它真的报过 cwd。于是连接后**给 4 秒探测窗**，4 秒内没收到过 OSC 7 才亮出那条提示
  （`cdLinkUnsupported`：当前 shell 无提示符钩子，终端里的 cd 不会同步到下方文件面板）；
  显式选了非 bash/zsh 则立刻提示。一连上就喊"不支持"是错的。
- `cdCommand` 用 **`\r` 而不是 `\n`**（PTY 行编辑认 CR 回车）；路径经 `shellQuote`
  （整串包进单引号，内部单引号换成 `'\''`）—— 面板里点的是远端给的字符串，里面什么都可能有一一
  不转义就是一个命令注入点。
- 换 shell（下拉）时整条重连，服务端随之重注钩子。注入失败不致命：会话照常能用，只是汇总行
  记 `hook=silent`（写日志告警，不把终端换成报错页）。

---

## 9. 权限、审计与安全姿态（把"为什么"一起复刻）

1. **文件面板的所有路由（含 GET）都按 `ActOperate` 把关**（`server_fs.go:28-30` + `requireFSAccess`）。
   中间件对 GET 只判到 `ActView`，而"读主机上任意文件"不是平台语义的查看 —— 它是与开终端等价的一条通道。
   因此**没有"只读用户可看"的降级路径**：没权限就是 403，界面按"无远程能力"处理。
2. **路径不做"越界拦截"**（`fs.go:434-441` 的 `cleanRemote` 只做 `path.Clean`；`server_fs.go:35-36` 同调）。
   能在终端里 `cd /etc` 的会话，拦它只会给假的安全感。真正的边界是操作权限本身。
   —— 但**上传的相对落点必须消毒**（§4.6），因为那批请求不允许"任意路径"这层含义。
3. **命令一律 array 化 + 逐参数转义**，shell 与容器 ID 走白名单；WS 用同源校验代替 CSRF。
4. **凭据即用即弃**，绝不进 WS 帧/日志/审计；错误响应只给人读文案，不带远端 stderr 原文与栈。
5. **文件类审计只记动作/路径/字节数，绝不记正文**（`server_fs.go:283-288`）；上传逐块不记，逐文件记。
   动作名：`server_fs`（op 在 detail 里：save/upload/upload_abort/mkdir/remove/rename/download/download_zip）、
   `server_terminal`、`container_terminal`、`server_command`。
   —— 分界线是"**正文**"而不是"字符串"：文件正文可能是密钥、配置或业务数据，一律不进审计；
   而**命令行本身就是终端审计的对象**，它记的是"执行掉了什么"，与文件正文是两类东西（§8.4）。
6. **终端记的是 shell 报上来的那条命令，不是用户敲进 WS 的字节**。理由：敲进的字节里混着口令
   （sudo/ssh/passwd 的交互输入走同一条通道），录下来等于把手输口令写进 append-only 审计表；
   而 `mask.Masker` 只替换登记过的密钥值，猜不到用户当场输了什么。
   回报里带的 `cwd/exit/user` 是白送的上下文；`actor`（平台登录用户）与 `loginUser`（登记的 SSH 用户）
   与 `user`（当时生效的 OS 账号）三个身份各记各的，`su` / 进容器之后也一眼分得清是谁做的。
7. **不给递归删除**：能力层只删空目录（409），界面因此也不放按钮。
8. **下载文件名过消毒**再进 `Content-Disposition`（响应头注入面）。
9. 隐藏半成品用点前缀临时名，用户列目录看不见、也手抖删不到；覆盖写/落地都走"临时名 + 原子改名"。

---

## 10. 常量总表（照抄即可）

| 位置 | 键/常量 | 值 |
|---|---|---|
| localStorage | `pw-remote-split` | 0.18..0.82，默认 0.46 |
| localStorage | `pw-remote-tree-w` | 140..520，默认 190；右列保底 260 |
| 前端 | `QUEUE_ROWS / TX_ROWS / TRANSFER_MAX_ROWS` | 200 / 120 / 500 |
| 前端 | `chunkSizeFor` | 4/8/16/32 MiB 分档；`FS_CHUNK_MAX_BYTES=64MiB` |
| 前端 | `parallel / maxRetry / backoff` | 2(1..4) / 4 / 400ms 起，封顶 6s |
| 前端 | xterm | 13px JetBrains Mono，lineHeight 1.15，scrollback 4000 |
| 前端 | cwd 探测窗口 | 4000 ms |
| 后端 | `fsTextReadLimit / fsJSONBodyLimit / fsPathMax` | 1 MiB / 8 MiB / 4096 |
| 后端 | `fsMetaTimeout / fsDataTimeout / fsChunkTimeout` | 30s / 5min / 5min |
| 后端 | `fsStreamIdle` | 2min（静默，不是总时长） |
| 后端 | `fsZipMaxEntries / fsZipMaxDepth / zipCopyBuf` | 20000 / 64 / 256 KiB |
| 后端 | `fsChunkMaxBytes / fsUploadMaxFiles / fsUploadMaxBatches / fsUploadIdleTTL / fsUploadMaxDepth` | 64 MiB / 5000 / 64 / 30min / 32 |
| 后端 | `ptyReadChunk / wsReadLimit / close reason 截断` | 32 KiB / 1 MiB / 120 字节 |
| 后端 | OSC 号 | cwd 用既有的 `OSC 7`；命令回报用私有 `OSC 5522`（不撞任何既有约定，前端 xterm 遇到不认识的就丢） |
| 后端 | `cmdPayloadMax / cmdTextMax / cmdPerSessionMax` | 8 KiB / 1000 字节（截断打 `truncated`）/ 2000 条·场 |
| 后端 | `cmdFieldSep / cmdSentinel` | `\x1f`(US，base64 前分隔) / `__pw_report`（钩子自报那一行，吞掉不入库） |
| 后端 | `auditDetachedTimeout` | 3s（hijack 之后写审计用的独立 ctx 超时） |
| 后端 | 临时名 | 覆盖写 `p + ".pipewright-tmp"`；上传 `.pipewright-up-<uploadId[:12]>.tmp` |

---

## 11. 需要装的东西

- Go：`golang.org/x/crypto/ssh`、`github.com/pkg/sftp`、`github.com/coder/websocket`、chi（或任意支持
  `{id}` 路由参数的 mux）。审计/权限/凭据(vault)/服务器注册表按你项目的对应物替换即可 ——
  本功能只依赖"取 SSH 凭据"和"记一条审计"这两个抽象。
- 前端：`@xterm/xterm` + `@xterm/addon-fit`，Vue 3（或任意框架，照 §8 的**状态与规则**搬，
  组件边界是 `终端/文件面板/外壳` 三个就够）。
- 远端要求：能 SSH 登录即可；有 sftp-server 走 SFTP，没有则 exec 兜底（需要 `sh/ls/stat|wc/cat/mv/mkdir/rmdir/chmod`，
  连 `sh` 都起不来时整个面板回 501 `fs_unsupported`，**终端仍可用**）。

---

## 12. 界面文案清单（i18n）

命名空间 `remoteWorkspace`，8 语种（zh-CN/zh-TW/en/ja/ko/de/es/fr）都要有同键。键分两组：

- 顶层（外壳+终端）：`button 远程` `title 远程工作区` `close` `openFullscreen 在大屏终端打开`
  `enterFullscreen 全屏` `exitFullscreen 退出全屏` `resize 拖动调整终端与文件面板的比例`
  `connecting/connect/reconnect/disconnect/disconnected/connectFailed/sessionEnded`
  `terminalAria` `shellAuto 自动(优先 bash)`
  `cdLinkUnsupported`（无提示符钩子的说明）
- `panel.*`（文件面板）：`crumbsAria pathAria backendTitle up refresh uploadFiles uploadFolder mkdir
  expandTree collapseTree treeAria treeResize treeExpand treeCollapse`
  `loading empty loadFailed unsupported`
  `colName colSize colType colMode colMtime colOps typeDir typeLink typeFile typeFileExt`
  `download downloadTitle downloadDirTitle edit rename remove`
  `uploading uploading?(队列相关：queueAria queueTitle queueCollapse queueExpand queueClear
  queueCancelAll queueMore uploadDone uploadSomeFailed uploadFailed uploadRetry
  uploadState{Done,Canceled,Queued,Uploading} uploadErr{Network,Busy,Expired,Forbidden,Unsupported,Rejected,Other}
  badName mkdirIn mkdirDone renameTo renameDone removeTitle removeBodyDir removeBodyFile removeConfirm
  removeDone opFailed saved saveFailed save saving unsaved binaryHint truncated closeEditor editorAria
  ok cancel loadFailed`
  `txAria txTitle txCounts txFailed txEmpty txClear txCollapse txExpand txMore txUpload txDownload
  txStateStarted txDownloadNote`
  `pathInput` 相关：`submitPathInput` 无独立键（回车即导航）；`fs_unsupported` 的整段文案见 `panel.unsupported`
  （必须明说"终端仍可使用"）。

---

## 13. 复刻实施 checklist（按里程碑）

**M0 能力层（后端，可独立测）**
1. 定 `FileStat` + `Workspace` 接口（§3 的 14 个方法 + `Backend()` + `Close()`）。
2. 定领域错误 5 个 + 错误→HTTP 映射表（§3.4）。
3. SFTP 实现：`call[T]` ctx 包装、`renameOver`（posix-rename 优先）、`mapSFSErr` 用 `StatusError.FxCode`、
   `WriteFile` 临时名+搬权限、`AppendChunk` 按 `WriteAt` 偏移、`Stat` 软链跟随+断链不失败。
4. exec 实现：哨兵 + `LC_ALL=C` + `stat` 方言探测 + `emit_entry` 六字段 + 点文件 glob +
   `cat -- "$1"` 流式读（Close 先 SIGKILL）+ 全参数走 array 转义。
5. 打开工作区的顺序：服务器存在 → 能力可用 → 取凭据 →（SFTP 失败）同连接降级 exec。

**M1 HTTP 契约**
6. `GET /fs`（空 path=家目录；目录 entries / 文件 entry）。
7. `GET|POST /fs/content`（1 MiB 截断 + `binary` 判定）。
8. `POST /fs/op`（op 白名单三件；rename 要 to）。
9. `GET /fs/download`（文件原样流 + Content-Length；目录 zip；静默看门狗；文件名消毒）。
10. 鉴权：所有 `/fs*`（含 GET）按 operate 档；WS 握手前判完并同源校验。
11. 审计：写动作 + 每种下载各一条，detail 只放 op/path/bytes/ok/error。

**M2 上传会话**
12. 批次注册表（进程内 + TTL 回收 + 批次上限）；`begin` 建目录树 + 逐 rel 消毒 + 查重。
13. `chunk`：`alignOffset`（不一致就 409+实测偏移）→ `AppendChunk` → 出错 `dropWS`；
    `WithoutCancel` + 单块超时；`busy` 防同文件并发。
14. `status` 一律实测长度；`complete`（空文件补一次空追加、搬权限、renameOver）；`abort`（删半成品）。

**M3 终端 WS**
15. 主机 shell：白名单 + auto 脚本（bash/zsh → $SHELL → /bin/sh，全用 `exec`）。
16. 双向泵 + resize 控制帧 + 32 KiB 块 + close 帧人读文案（i18n）+ 握手后写审计（auto 记 `auto`）。
17. 命令级留痕：起会话后往 PTY stdin 注入一行钩子（`terminal_recorder.go` 的 `commandHookScript`），
    在 PTY→WS 的字节流上**只读不改写**地解 OSC 5522 → 每条命令落 `server_command`（detail 带
    cmd/cwd/exit/user/loginUser/kind/containerId/session），会话收尾再补一行汇总（`hook`/`commands`/`ms`）。

**M4 前端接口与纯逻辑**
18. `serverFs.ts`：端点一一映射；逐块 XHR（进度 + CSRF 头 + locale 头 + AbortSignal）。
19. `lib/serverFs.ts`：路径归一/parent/join/crumbs/排序/字节/mtime(0→空串)/`modeToLs`/`fileExt`/
    `shellQuote`/`cdCommand`(`\r`)/`pathFromOsc7`。（提示符钩子在服务端，见 M3 第 17 步 ——
    前端曾经有个 `cwdReportScript`，已删；别再把它加回来。）
20. `lib/fsUpload.ts`：驱动器（§6.4 的 9 条行为，逐条写单测）。
21. `lib/fsTransfers.ts`：记录行折算 + 内存存储 + 上限裁剪（先丢落定再丢在传）。

**M5 组件**
22. `TerminalPane`（xterm 动态加载 + 字体两段式重测 + refit 收敛 + OSC7 + 选中即复制 + expose runCd）。
23. `RemoteFilePanel`（工具栏/面包屑/树(懒+reveal seq+invalidate)/表格/内联编辑/内联 op 表单/队列/记录）。
24. `RemoteWorkspaceModal`（上下分屏 + 两条可拖 + 全屏 + Esc 三规则 + 双向联动接线 + 防回声）。
25. 入口按钮（卡片/列表行 → `remoteId` → `v-if` 挂载；`hostLabel = user@host:port`）。

**M6 文案与测试**
26. i18n 全键 ×8 语种（有 parity 测试就先补键再补翻译）。
27. 跑 §15 的测试集；再跑 §16 的真机冒烟。

---

## 14. 踩过的坑（照做，别自己再踩一遍）

| # | 坑 | 正确做法 |
|---|---|---|
| 1 | SFTP rename 不覆盖已存在目标，编辑器第二次保存就失败 | `posix-rename@openssh.com` → 不存在则普通 rename → 存在则先删后 rename |
| 2 | "不存在"被判成 500 | `errors.As(*sftp.StatusError)` 比 `FxCode()`，`os.IsNotExist` 对它无效 |
| 3 | SFTP 调用不吃 ctx，取消请求后 goroutine 悬挂 | 泛型 `call`：ctx.Done → **关整条连接** → 阻塞调用以错误返回 |
| 4 | 网页编辑器把线上文件写成半截 | 一律"临时名 + 原子改名"，并把原权限位搬到临时名（600 的密钥不该被放宽成 644） |
| 5 | 上传把平台本机磁盘当缓存 / 断线全废 | 分块 + 服务端记账 + `offset` 由**远端实测长度**决定 |
| 6 | 逐文件重拨 SSH，500 个文件的文件夹先付 500 轮握手 | **一个批次共用一条连接**；出错才 `dropWS` 重拨 |
| 7 | fetch 报不出真实上传进度 | 逐块用 `XMLHttpRequest` + `xhr.upload.onprogress` |
| 8 | 客户端断开把整条批次连接拆了，别的文件全挂 | `context.WithoutCancel(r.Context())` + 单块超时 |
| 9 | 下载用总时长封顶，大文件必死 | **静默看门狗**（2 分钟没字节流动才断）；列到目录也算一次进展 |
| 10 | 目录打包中途撞上限，给了个"看起来完整"的半截 zip | 掐断连接（`http.ErrAbortHandler`）+ 审计记 `entries/bytes/error` |
| 11 | 软链让打包成环 | 一律不跟随不写入，审计 `skippedLinks`，界面明说"链接没打进包" |
| 12 | 空目录 zip 后消失 | 每个目录写一条目录项 |
| 13 | 已压缩文件再 deflate 白费 CPU 还拖慢 | `storedExt` 表命中直接 `Store` |
| 14 | 点文件是"整棵子树拉平"→ 首屏等几十次 SSH | 目录树只列目录、点开哪层问哪层；失败保持 `null` 可重试 |
| 15 | 树停在旧快照骗人 | 建目录/改名/删除/上传后 `invalidateTree(那一层)` |
| 16 | 面板 cd → 终端回报同一路径 → 面板又列一次（自激） | 双向都先归一比对，**路径真的变了才动** |
| 17 | `PROMPT_COMMAND` 里直接写 printf（还用了双引号）→ 永远上报登录时的旧目录 | 赋**函数名**，把求值推到每次提示符；函数里 `printf` 的格式串用**单引号** |
| 18 | 一连上就弹"该 shell 不支持联动" | 自动模式给 4 秒窗口，以"是否真的报过 cwd"为唯一证据 |
| 19 | 往 PTY 打 `cd "路径"` → 注入面 / 回车不生效 | 单引号 POSIX 引用（内部 `'`→`'\''`），结尾 **`\r`** 不是 `\n` |
| 20 | 逐块新建 TextDecoder 把 ANSI 序列切碎 | 常驻一个 decoder + `{stream:true}` |
| 21 | 字体晚到 → 行数算多、末行溢出 | 先 `fonts.load` 再 `open`，`fonts.ready` 后改一次 fontSize 重测 |
| 22 | `FitAddon` 多算半行 | `refit` 里按真实几何把 rows 逐行减回去（带 guard 次数） |
| 23 | 把 xterm 实例塞进 `ref` 深代理 | `shallowRef`；驱动器句柄只留在普通变量里（不响应式） |
| 24 | 下载走 fetch 把 10 GB 读进内存 | `<a :href>` 交浏览器；只在点击时补一条"已发起"记录，**不 preventDefault** |
| 25 | 完成态看着像差一块（末块进度报不满） | `status==='done'` 时强制 100% |
| 26 | 总进度按文件数算，10 GB 与 10 个空文件等价 | **按字节加权** |
| 27 | retry 后增量计数说谎 | 所有计数一律现算（`tallyNow`/`transferSummary`） |
| 28 | 批次内序号 `key` 跨批次撞车 | 记录行 id = `批次号#序号`；`at` 在发起时取一次 |
| 29 | 每帧重算记录时现取 `Date.now()` → "发起时刻"漂到最新 | 发起时刻由调用方钉住并原样传入 |
| 30 | 整批提到最前 → 新起的下载被反复压下去，界面一直跳 | 已有 id 原位覆盖，仅新 id 插到最前 |
| 31 | 上传队列 + 记录都常驻，把表格挤没 | 记录默认收起，靠徽标做入口；两处渲染都有行数上限 |
| 32 | 同一个文件再选一次不触发 | 选完把 `input.value` 清空 |
| 33 | 点开头隐藏文件被当后缀显示成"BASHRC 文件" | `fileExt` 对 `dot<=0` 回空串 |
| 34 | 目录大小画成 `0 B` 是假数据 | 目录留空（两路后端给的是 0/块数） |
| 35 | `mtime=0` 显示 1970 | 回空串，整列不显示 |
| 36 | 把 5000 行队列塞进 DOM | `slice(0,200)` + "还有 n 条"（计数照常算） |
| 37 | 上传半成品几 GB 留在盘上没人认领 | 取消/回收都要 `Remove(tmp)`；临时名用点前缀 |
| 38 | 逐块刷满审计表 | 只逐文件记 `complete/abort` |
| 39 | GET 被中间件判成"查看"档，等于开放读任意文件 | 文件路由显式按 operate 判 |
| 40 | 跨站 WS 劫持 | GET 豁免 CSRF ⇒ 用 `OriginPatterns=[Host]` 同源校验，且握手前完成鉴权 |
| 41 | 注入脚本里出现裸 `!` → 交互式 zsh 做历史展开，整行 `event not found` **不执行**，钩子静默失效 | 不用 `${h%%[![:space:]]*}` 这类写法；折行首编号交给 `sed` |
| 42 | 注入行结尾写成字面 `\r`（两个字符）→ shell 收到普通字符，这行永远不提交，钩子装不上且不报错 | 结尾必须是**真 CR(0x0D)** |
| 43 | bash 里用 `fc -ln -1` 取"刚执行那条"（zsh 的习惯）→ 拿到的是**上上一条**，审计全滞后一格 | 按 shell 分开取：bash `HISTTIMEFORMAT='' history 1 \| sed 's/^ *[0-9][0-9]*  //'`，zsh `fc -ln -1` |
| 44 | 录"用户敲进 WS 的字节"当命令 → sudo/ssh 的交互口令一起进 append-only 审计表 | 只录 shell 在提示符上回报的那条；字节流**只读不改写**地捎带解 OSC，转给前端的流一个不动 |

---

## 15. 现有测试（可直接当移植用例）

| 文件 | 覆盖点 |
|---|---|
| `internal/target/fs_test.go` | 接口契约（假拨号器）：错误映射、Backend 名 |
| `internal/target/fs_exec_test.go` | 兜底脚本的哨兵/解析（不依赖真机） |
| `internal/target/fs_realsftp_e2e_test.go` | 真 SFTP（含 renameOver 覆盖语义） |
| `internal/target/fs_exec_realssh_e2e_test.go` | 真 sshd + 禁 sftp 子系统的兜底路径 |
| `internal/target/interactive_*_test.go` | PTY 会话、Resize、ctx 取消不泄漏 |
| `internal/httpapi/server_fs_test.go` | 每个端点的状态码/DTO/鉴权档 |
| `internal/httpapi/fs_upload_test.go` | begin 整批拒绝、409 offset_mismatch 续传、空文件、权限保留、回收 |
| `internal/httpapi/fs_zip_test.go` | 流式 zip：软链跳过、空目录、条目上限掐断、store/deflate |
| `internal/httpapi/container_terminal_test.go` | 两条终端 WS 的鉴权/白名单/命令 array 化/审计 |
| `internal/httpapi/terminal_recorder_test.go` | 钩子脚本形状（无裸 `!`、真 CR 结尾）、OSC 5522 解码（脏载荷/自报/截断）、跨块拼接、每会话上限、`cutUTF8`、端到端命令落审计与 `hook=silent` |
| `web/src/lib/serverFs.test.ts` | 路径/排序/字节/时间/权限串/`shellQuote`/OSC7 解析 |
| `web/src/lib/fsUpload.test.ts` | 注入 deps 跑断线重连、退避、409 重定位、取消清半成品、retry |
| `web/src/lib/fsTransfers.test.ts` | 行折算、批次身份、原位覆盖、上限裁剪先丢落定 |

---

## 16. 真机冒烟清单（顺序即依赖）

1. 空 `path` → 家目录（不是 `/root`/`/home` 猜一个）。
2. `pwd` 切目录 → 面板 1 秒内跟随；面板点目录 → 终端里出现 `cd '<路径>'` 且 `pwd` 对得上；
   来回点**不出现**重复列目录（防回声生效）。
3. 用 `dash`/`ash` 登录 shell（显式选 `/bin/sh`）→ 4 秒后出现"无提示符钩子"提示，面板仍可自己导航；
   这场会话的 end 汇总行记 `hook=silent`、`commands=0`（"没录到"这件事本身要留痕）。
4. 无 sftp-server 的机器（dropbear / 最小镜像）→ backend 徽标显示 `exec`，时间列按 `stat` 方言有无给值。
5. 上传：①单个 4 MiB 以下走满 4 MiB 档；②传一个 >64 MiB 的分块文件，中途拔网 → 状态变 `upload_timeout`/
   网络 → 点重试，从 status 的实测偏移接着推，落地字节与原文一致；③选整个含子目录的文件夹 →
   远端目录树按 `rel` 重建；④零字节文件能落地；⑤覆盖一个 600 权限的文件后权限仍是 600。
6. 下载：单文件百分比（有 Content-Length）；目录 → `<名>.zip`，空子目录在包里有，软链不在包里。
7. 删除一个非空目录 → 409，界面文案说的是"空目录才删得掉"。
8. 关弹窗 → `lsof` 看平台侧没有残留到该机的 SSH 连接；`/api/audit` 里有 `server_terminal`、`server_command`
   与 `server_fs` 行：`server_command` 的 detail 带 `cmd/cwd/exit/user`，`server_*` 的 detail 里**没有文件正文**；
   同一次连接的三行（start/命令/end）用同一个 `session` 串得起来，end 行的 `commands` 与命令行数对得上。
9. 全屏 / 退出全屏：分屏比例保持；退出全屏时目录树宽度被夹回，不被挤没。
10. 键盘：分隔条 Tab 聚焦后 ↑/↓；目录树分隔条 ←/→；Esc 在终端里不关窗、全屏时先缩回。

---

### 一句话总结这套设计

**能力层只承认结构化协议（SFTP），承认不才用 shell 兜底；HTTP 层只做消毒、鉴权、审计和 DTO；
界面只做选择器与渲染，所有会算错的东西（切块、续传、偏移、进度、路径、注入）都搬到有单测的纯逻辑里。**
每一处"看着多余"的细节，背后都是一次实测出来的失败 —— 复刻时把它们一起搬过去，
才会得到同一个行为；只搬界面截图，会得到一个"在好的机器上能用"的版本。
