#!/usr/bin/env bash
#
# start.sh — Pipewright 一键打包 + 启动(本地原生 / Docker Compose 双模式)。
#
# 用法:
#   ./start.sh                  # 交互式菜单(不传参数时)
#   ./start.sh local            # 本地原生:构建前端 → go build 单二进制 → 后台起服务
#   ./start.sh compose          # Docker Compose:构建本地镜像 → docker compose up -d
#   ./start.sh stop             # 停止(两种模式都会尝试)
#   ./start.sh logs [local|compose] [--follow]
#   ./start.sh status           # 查看进程 / 容器 / 健康状态
#   ./start.sh clean            # 清理构建产物(二进制 + web/dist;不影响 .env 与数据)
#
# 常用选项(local):
#   --skip-web      跳过前端构建(web/dist 已存在时用;加快迭代)
#   --port N        监听端口(默认 8080)
#   --foreground    前台运行(不后台化,适合 systemd/supervisor 接管)
#   --data-dir DIR  数据目录(默认 ./data;sqlite 库 / 产物 / 仓库缓存落此)
#
# 常用选项(compose):
#   --pull          强制从 ghcr.io 拉取镜像(而非本地构建)
#   --profile mysql 额外启用 MySQL(见 docker-compose.yml)
#
# 首次运行会生成 .env(含随机 MASTER_KEY 与随机管理员口令);.env 已被 .gitignore 忽略。
# MASTER_KEY 一旦写入凭据数据,换 key 会导致旧凭据无法解密 —— 换 key 前先备份 .env。
#
set -euo pipefail

# ---- 常量 --------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

BIN_NAME="pipewright"
BIN_PATH="$SCRIPT_DIR/$BIN_NAME"
ENV_FILE="$SCRIPT_DIR/.env"
DATA_DIR_DEFAULT="$SCRIPT_DIR/data"
LOG_DIR="$SCRIPT_DIR/logs"
PID_FILE="$LOG_DIR/pipewright.pid"
LOCAL_LOG="$LOG_DIR/pipewright.log"
COMPOSE_LOG="$LOG_DIR/compose.log"
COMPOSE_PROJECT="pipewright"
HEALTH_PATH="/healthz"
DEFAULT_PORT=8080

# 颜色(非 TTY 时自动关闭)
if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YLW=$'\033[33m'
  C_BLU=$'\033[34m'; C_DIM=$'\033[2m';  C_BLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=''; C_GRN=''; C_YLW=''; C_BLU=''; C_DIM=''; C_BLD=''; C_RST=''
fi

info()  { printf '%s==>%s %s\n'   "$C_BLU" "$C_RST" "$*"; }
ok()    { printf '%s  ✓%s %s\n'   "$C_GRN" "$C_RST" "$*"; }
warn()  { printf '%s  ! %s%s\n'   "$C_YLW" "$C_RST" "$*" >&2; }
err()   { printf '%s  ✗ %s%s\n'   "$C_RED" "$C_RST" "$*" >&2; }
dim()   { printf '%s    %s%s\n'   "$C_DIM" "$C_RST" "$*"; }
die()   { err "$*"; exit 1; }

# ---- 工具 --------------------------------------------------------------------

need_cmd() { command -v "$1" >/dev/null 2>&1; }

# 读 .env 里某个 key 的值(忽略注释与空行);不存在则空
env_get() {
  local key="$1"
  [[ -f "$ENV_FILE" ]] || return 0
  sed -n "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*//p" "$ENV_FILE" \
    | tail -n1 | sed 's/^"\(.*\)"$/\1/; s/^'"'"'\(.*\)'"'"'$/\1/'
}

# 写/改 .env 的一个 key(不存在则追加)
env_set() {
  local key="$1" val="$2"
  if [[ -f "$ENV_FILE" ]] && grep -qE "^[[:space:]]*${key}[[:space:]]*=" "$ENV_FILE"; then
    # 用 python 做安全替换,避免 sed 分隔符与特殊字符问题
    python3 - "$ENV_FILE" "$key" "$val" <<'PY'
import sys, io
path, key, val = sys.argv[1], sys.argv[2], sys.argv[3]
with io.open(path, encoding='utf-8') as f:
    lines = f.readlines()
out, done = [], False
for ln in lines:
    if ln.strip().startswith(key + '=') or ln.lstrip().startswith(key + ' ') and '=' in ln:
        out.append(f"{key}={val}\n"); done = True
    else:
        out.append(ln)
if not done:
    if out and not out[-1].endswith('\n'):
        out[-1] += '\n'
    out.append(f"{key}={val}\n")
with io.open(path, 'w', encoding='utf-8') as f:
    f.writelines(out)
PY
  else
    printf '%s=%s\n' "$key" "$val" >> "$ENV_FILE"
  fi
}

rand_b64_32() {
  if need_cmd openssl; then
    openssl rand -base64 32
  else
    # 无 openssl 时用 /dev/urandom + base64
    head -c 32 /dev/urandom | base64 | tr -d '\n'
  fi
}

rand_pass() {
  # 16 位可读口令:避开易混淆字符
  if need_cmd openssl; then
    openssl rand -base64 48 | tr -dc 'A-Za-z2-9' | head -c 16
  else
    head -c 64 /dev/urandom | base64 | tr -dc 'A-Za-z2-9' | head -c 16
  fi
}

# 等待 HTTP 健康检查就绪
wait_healthy() {
  local url="$1" timeout="${2:-60}" waited=0
  while (( waited < timeout )); do
    if need_cmd curl; then
      if curl -fsS -m 3 "$url" >/dev/null 2>&1; then return 0; fi
    else
      # 无 curl:用 bash /dev/tcp 探活
      if (exec 3<>"/dev/tcp/${url#*://}") 2>/dev/null; then exec 3>&- 3<&-; return 0; fi
    fi
    sleep 2; waited=$((waited + 2))
    printf '.'
  done
  return 1
}

docker_compose_cmd() {
  if docker compose version >/dev/null 2>&1; then
    echo "docker compose"
  elif need_cmd docker-compose; then
    echo "docker-compose"
  else
    echo ""
  fi
}

# ---- .env 初始化 -------------------------------------------------------------

ensure_env() {
  local port="${1:-$DEFAULT_PORT}"
  if [[ ! -f "$ENV_FILE" ]]; then
    info "生成 $ENV_FILE(首次运行)"
    if [[ -f "$SCRIPT_DIR/.env.example" ]]; then
      cp "$SCRIPT_DIR/.env.example" "$ENV_FILE"
      dim "已从 .env.example 复制"
    else
      : > "$ENV_FILE"
    fi

    # 管理员口令:未设置则生成随机值(避免用默认弱口令起生产)
    if [[ -z "$(env_get PIPEWRIGHT_ADMIN_PASSWORD)" || "$(env_get PIPEWRIGHT_ADMIN_PASSWORD)" == "change-me-please" ]]; then
      local pw; pw="$(rand_pass)"
      env_set PIPEWRIGHT_ADMIN_PASSWORD "$pw"
      env_set PIPEWRIGHT_PORT "$port"
      ok "已生成随机管理员口令(见 .env 的 PIPEWRIGHT_ADMIN_PASSWORD)"
      warn "请立即保存该口令;遗忘后需删库重建(无重置通道)。"
    fi

    # MASTER_KEY:未设置则生成;空 = 保险库未配置态(凭据功能不可用)
    if [[ -z "$(env_get PIPEWRIGHT_MASTER_KEY)" ]]; then
      local mk; mk="$(rand_b64_32)"
      env_set PIPEWRIGHT_MASTER_KEY "$mk"
      ok "已生成随机 PIPEWRIGHT_MASTER_KEY(32 字节 base64)"
      dim "该 key 用于加密凭据;写入凭据后换 key 将无法解密旧数据,请长期保存。"
    fi
    printf '\n'
  fi

  # 端口 CLI 覆盖
  if [[ "$port" != "$DEFAULT_PORT" ]]; then
    env_set PIPEWRIGHT_PORT "$port"
  fi
}

# ---- 本地原生模式 ------------------------------------------------------------

# 工具链定位:GOTOOLCHAIN=local 必须用本机 toolchain >= go.mod 要求。
# PATH 上的 go 可能是包管理器装的旧版,项目要求的 go 1.26.3 通常装在 /usr/local/go 或 ~/sdk。
detect_go_toolchain() {
  local cand
  for cand in \
    /usr/local/go/bin/go \
    /opt/go/bin/go \
    "$HOME"/sdk/go1.26.3/bin/go \
    "$HOME"/sdk/go1.27.0/bin/go \
    "$HOME"/sdk/go1.27.1/bin/go \
    "$HOME"/go/bin/go
  do
    [[ -x "$cand" ]] && { echo "$cand"; return 0; }
  done
  command -v go
}

# 探测 go 版本是否满足 go.mod toolchain 要求;不够则给修复建议并退出。
go_toolchain_ok() {
  local gobin="$1"
  local v; v="$(GOTOOLCHAIN=local "$gobin" env GOVERSION 2>/dev/null | sed -E 's/^go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/')"
  [[ -z "$v" ]] && { warn "无法探测 $gobin 版本"; return 1; }
  local need; need="$(grep -E '^go [0-9]' "$SCRIPT_DIR/go.mod" | head -1 | awk '{print $2}')"
  [[ -z "$need" ]] && return 0
  # semver 排序 -C 检测 v 是否 ≥ need
  if printf '%s\n%s\n' "$v" "$need" | sort -V -C 2>/dev/null; then
    dim "go ${v} ≥ go.mod 要求 ${need}"
    return 0
  else
    err "$gobin 是 go ${v},但 go.mod 要求 ${need}"
    dim "请从 https://go.dev/dl/ 下载 ≥${need} 解压到 /usr/local/go,或调高 PATH 优先级。"
    return 1
  fi
}

check_local_deps() {
  local gobin; gobin="$(detect_go_toolchain)"
  local missing=()
  [[ -x "$gobin" ]] || missing+=("go (>=1.26.3)")
  need_cmd npm || missing+=("node/npm (>=18)")
  if (( ${#missing[@]} )); then
    err "缺少依赖: ${missing[*]}"
    dim "Go:   https://go.dev/dl/  (纯 Go 构建,CGO_ENABLED=0)"
    dim "Node: https://nodejs.org/ (仅首次构建前端需要)"
    die "请先安装上述依赖后重试。"
  fi
  go_toolchain_ok "$gobin" || die "go toolchain 不满足 go.mod 要求。"
  local gov; gov="$(GOTOOLCHAIN=local "$gobin" env GOVERSION 2>/dev/null)"
  dim "${gobin} (go ${gov}) · npm $(npm --version 2>/dev/null)"
  # 导出给后续 build_bin / 命令使用(避免 PATH 顺序导致的版本回退)
  export GO_BIN="$gobin"
}

build_web() {
  if [[ ! -d "$SCRIPT_DIR/web" ]]; then
    warn "未找到 web/ 目录,跳过前端构建(将使用已有 web/dist)。"
    return 0
  fi
  info "构建前端 (web/dist,供 go:embed)"
  (
    cd "$SCRIPT_DIR/web"
    if [[ ! -d node_modules ]]; then
      dim "node_modules 不存在 → npm ci"
      npm ci
    fi
    npm run build
  )
  ok "前端构建完成"
}

build_bin() {
  info "构建单静态二进制 ($BIN_NAME)"
  local gobin="${GO_BIN:-$(command -v go)}"
  local version commit date
  version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
  commit="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
  date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  CGO_ENABLED=0 GOTOOLCHAIN=local "$gobin" build \
    -ldflags "-s -w \
      -X github.com/huangchengsir/pipewright/internal/version.Version=${version} \
      -X github.com/huangchengsir/pipewright/internal/version.Commit=${commit} \
      -X github.com/huangchengsir/pipewright/internal/version.Date=${date}" \
    -o "$BIN_PATH" ./cmd/pipewright
  ok "二进制就绪: $BIN_PATH ($(du -h "$BIN_PATH" | cut -f1))"
}

start_local() {
  local port data_dir foreground=0 skip_web=0
  port="$DEFAULT_PORT"; data_dir="$DATA_DIR_DEFAULT"

  # ---- 解析参数 ----
  while (( $# )); do
    case "$1" in
      --port)       port="${2:?--port 需要参数}"; shift 2 ;;
      --data-dir)   data_dir="${2:?--data-dir 需要参数}"; shift 2 ;;
      --skip-web)   skip_web=1; shift ;;
      --foreground) foreground=1; shift ;;
      *) die "local: 未知选项 $1(用 --port/--data-dir/--skip-web/--foreground)" ;;
    esac
  done

  check_local_deps
  ensure_env "$port"

  (( skip_web )) || build_web
  build_bin

  mkdir -p "$LOG_DIR" "$data_dir"

  local admin_pw master_key
  admin_pw="$(env_get PIPEWRIGHT_ADMIN_PASSWORD)"
  master_key="$(env_get PIPEWRIGHT_MASTER_KEY)"

  # 已在运行则先停
  if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    warn "检测到已在运行 (pid $(cat "$PID_FILE")),先停止"
    stop_local
  fi

  info "启动服务 (port=$port, data=$data_dir)"
  if (( foreground )); then
    dim "前台模式:Ctrl-C 停止"
    exec env \
      PIPEWRIGHT_ADDR=":${port}" \
      PIPEWRIGHT_DATA_DIR="$data_dir" \
      PIPEWRIGHT_ADMIN_PASSWORD="$admin_pw" \
      PIPEWRIGHT_MASTER_KEY="$master_key" \
      "$BIN_PATH"
  fi

  nohup env \
    PIPEWRIGHT_ADDR=":${port}" \
    PIPEWRIGHT_DATA_DIR="$data_dir" \
    PIPEWRIGHT_ADMIN_PASSWORD="$admin_pw" \
    PIPEWRIGHT_MASTER_KEY="$master_key" \
    "$BIN_PATH" >"$LOCAL_LOG" 2>&1 &
  local pid=$!
  echo "$pid" > "$PID_FILE"
  disown 2>/dev/null || true

  printf '    等待健康检查'
  if wait_healthy "http://127.0.0.1:${port}${HEALTH_PATH}" 90; then
    printf '\n'; ok "服务已就绪"
  else
    printf '\n'; warn "健康检查未在预期时间内通过,查看日志: $0 logs local"
  fi

  print_summary_local "$port" "$pid"
}

print_summary_local() {
  local port="$1" pid="$2"
  cat <<EOF

$(printf '%s' "$C_BLD")本地原生模式已启动$C_RST
  地址:     http://localhost:${port}
  登录:     admin / $(env_get PIPEWRIGHT_ADMIN_PASSWORD)
  进程 PID: ${pid}  ($0 stop 停止 · $0 logs local 看日志)
  数据目录: ${DATA_DIR_DEFAULT} (sqlite 库 / 构建产物 / 仓库缓存)
  配置文件: ${ENV_FILE}

$(printf '%s' "$C_DIM")提示:如需容器化部署,运行 $0 compose$C_RST
EOF
}

stop_local() {
  if [[ -f "$PID_FILE" ]]; then
    local pid; pid="$(cat "$PID_FILE")"
    if kill -0 "$pid" 2>/dev/null; then
      info "停止本地服务 (pid $pid)"
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 15); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 1
      done
      kill -9 "$pid" 2>/dev/null || true
      ok "已停止"
    else
      dim "PID 文件存在但进程已不在"
    fi
    rm -f "$PID_FILE"
  else
    dim "本地服务未在运行"
  fi
}

# ---- Docker Compose 模式 -----------------------------------------------------

check_compose_deps() {
  need_cmd docker || die "缺少 docker: https://docs.docker.com/get-docker/"
  docker info >/dev/null 2>&1 || die "docker daemon 未运行或无权限(试试 sudo 或将用户加入 docker 组)。"
  local dc; dc="$(docker_compose_cmd)"
  [[ -n "$dc" ]] || die "缺少 docker compose(v2 插件或 v1 二进制)。"
  dim "docker $(docker --version | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1) · $dc"
}

start_compose() {
  local port="$DEFAULT_PORT" use_pull=0 extra_profiles=()

  while (( $# )); do
    case "$1" in
      --port)    port="${2:?--port 需要参数}"; shift 2 ;;
      --pull)    use_pull=1; shift ;;
      --profile) extra_profiles+=("$2"); shift 2 ;;
      *) die "compose: 未知选项 '$1'(合法:--port N / --pull / --profile NAME,执行 ./start.sh help)" ;;
    esac
  done

  check_compose_deps
  ensure_env "$port"
  local dc; dc="$(docker_compose_cmd)"

  local img_args=()
  if (( use_pull )); then
    info "拉取镜像 ghcr.io/huangchengsir/pipewright"
    docker pull "ghcr.io/huangchengsir/pipewright:$(env_get PIPEWRIGHT_VERSION || echo latest)" \
      || warn "拉取失败(可能是网络或镜像不存在),将回退为本地构建"
  else
    info "从本地 Dockerfile 构建镜像(含前端构建,首次较慢)"
    dim "如需改用 ghcr 预构建镜像,加 --pull"
    # shellcheck disable=SC2086
    $dc -p "$COMPOSE_PROJECT" build ${extra_profiles[@]+"${extra_profiles[@]/#/--profile }"} \
      || die "镜像构建失败(查看上方 docker 输出)。"
  fi

  info "docker compose up -d"
  local up_cmd=($dc -p "$COMPOSE_PROJECT" up -d --remove-orphans)
  (( ${#extra_profiles[@]} )) && up_cmd+=("${extra_profiles[@]/#/--profile }")
  "${up_cmd[@]}" > >(tee "$COMPOSE_LOG") 2>&1 || {
    [[ -s "$COMPOSE_LOG" ]] && cat "$COMPOSE_LOG" >&2
    die "compose up 失败。"
  }

  printf '    等待健康检查'
  if wait_healthy "http://127.0.0.1:${port}${HEALTH_PATH}" 120; then
    printf '\n'; ok "容器已就绪"
  else
    printf '\n'; warn "健康检查未通过,查看日志: $0 logs compose"
  fi

  print_summary_compose "$port"
}

print_summary_compose() {
  local port="$1"
  cat <<EOF

$(printf '%s' "$C_BLD")Docker Compose 模式已启动$C_RST
  地址:     http://localhost:${port}
  登录:     admin / $(env_get PIPEWRIGHT_ADMIN_PASSWORD)
  容器:     $(docker ps --filter "name=$COMPOSE_PROJECT" --format '{{.Names}} ({{.Status}})' | head -1)
  数据卷:   pipewright-data → /data (sqlite 库 / 产物 / 仓库缓存)
  配置文件: ${ENV_FILE}

  常用:
    $0 logs compose --follow     # 跟踪日志
    $0 status                    # 状态总览
    $0 stop                      # 停止并移除容器
EOF
  if [[ -z "$(env_get PIPEWRIGHT_MASTER_KEY)" ]]; then
    printf '\n'; warn ".env 未设 PIPEWRIGHT_MASTER_KEY → 保险库未配置,凭据功能不可用。"
  fi
}

stop_compose() {
  local dc; dc="$(docker_compose_cmd)"
  if [[ -z "$dc" ]]; then warn "未检测到 docker compose"; return 0; fi
  if docker ps -a --format '{{.Names}}' | grep -q "^${COMPOSE_PROJECT}"; then
    info "停止并移除 compose 容器"
    $dc -p "$COMPOSE_PROJECT" down --remove-orphans || warn "compose down 返回非零"
    ok "已停止"
  else
    dim "compose 容器未运行"
  fi
}

# ---- 通用子命令 --------------------------------------------------------------

show_logs() {
  local which="${1:-}" follow=0
  shift || true
  for a in "$@"; do [[ "$a" == "--follow" || "$a" == "-f" ]] && follow=1; done
  case "$which" in
    local)
      [[ -f "$LOCAL_LOG" ]] || die "暂无本地日志: $LOCAL_LOG"
      (( follow )) && tail -f "$LOCAL_LOG" || tail -n 200 "$LOCAL_LOG"
      ;;
    compose)
      local dc; dc="$(docker_compose_cmd)"
      [[ -n "$dc" ]] || die "未检测到 docker compose"
      # shellcheck disable=SC2046
      $dc -p "$COMPOSE_PROJECT" logs --tail=200 $( (( follow )) && echo "--follow" )
      ;;
    *)  # 两者都看
      [[ -f "$LOCAL_LOG" ]] && { echo "── local ──"; tail -n 100 "$LOCAL_LOG"; }
      if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -q "^${COMPOSE_PROJECT}"; then
        local dc; dc="$(docker_compose_cmd)"
        echo "── compose ──"; $dc -p "$COMPOSE_PROJECT" logs --tail=100
      fi
      ;;
  esac
}

show_status() {
  printf '%s本地模式%s\n' "$C_BLD" "$C_RST"
  if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    ok "运行中 (pid $(cat "$PID_FILE"))"
  else
    dim "未运行"
  fi
  printf '\n%sDocker Compose%s\n' "$C_BLD" "$C_RST"
  if need_cmd docker && docker ps -a --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null | grep -i pipewright; then
    :
  else
    dim "无 pipewright 容器"
  fi
  printf '\n%s健康检查%s\n' "$C_BLD" "$C_RST"
  local p; p="$(env_get PIPEWRIGHT_PORT || echo "$DEFAULT_PORT")"
  if need_cmd curl && curl -fsS -m 3 "http://127.0.0.1:${p}${HEALTH_PATH}" 2>/dev/null; then
    printf '  ✓ :%s 健康\n' "$p"
  else
    dim "  :%s 无响应" "$p"
  fi
}

do_clean() {
  info "清理构建产物"
  rm -f "$BIN_PATH" "$BIN_PATH".old
  rm -rf "$SCRIPT_DIR/web/dist"
  rm -f "$SCRIPT_DIR"/*.db
  ok "已清理二进制 / web/dist / 根目录 sqlite"
  dim "未动:.env(配置)、data/(数据)、logs/(日志)"
  dim "如需连数据一起清:rm -rf ${DATA_DIR_DEFAULT}"
}

interactive_menu() {
  cat <<EOF

$(printf '%s' "$C_BLD")Pipewright 一键打包启动$C_RST

  1) 本地原生   — 构建前端 + go build 单二进制,直接在本机跑
  2) Docker Compose — 构建镜像并起容器(数据在具名卷,易迁移)
  3) 停止       — 停掉本地进程和/或 compose 容器
  4) 日志       — 查看本地/容器日志
  5) 状态       — 进程 / 容器 / 健康检查
  6) 清理       — 删二进制 + web/dist(不动 .env 与数据)
  0) 退出

EOF
  local choice
  read -r -p "请选择 [1]: " choice
  choice="${choice:-1}"
  case "$choice" in
    1) start_local ;;
    2) start_compose ;;
    3) stop_local; stop_compose ;;
    4) show_logs ;;
    5) show_status ;;
    6) do_clean ;;
    0) exit 0 ;;
    *) die "无效选择: $choice" ;;
  esac
}

usage() {
  sed -n '3,32p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# ---- 入口 --------------------------------------------------------------------

main() {
  local cmd="${1:-}"; shift || true
  case "$cmd" in
    local)   start_local "$@" ;;
    compose) start_compose "$@" ;;
    stop)    stop_local; stop_compose ;;
    logs)    show_logs "$@" ;;
    status)  show_status ;;
    clean)   do_clean ;;
    -h|--help|help) usage ;;
    "")      interactive_menu ;;
    *)       err "未知命令: $cmd"; echo; usage; exit 2 ;;
  esac
}

main "$@"
