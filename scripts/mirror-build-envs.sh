#!/usr/bin/env bash
# 把构建环境用的上游镜像按 amd64+arm64 两档镜像到青岛 ACR(仓库自有命名空间 fubin/)。
# 来源走 docker.m.daocloud.io 镜像站:本机直连 registry-1.docker.io 不通,而 imagetools 是
# registry→registry 复制,不必把两层全架构的 blob 拉到本地。
set -uo pipefail
# 来源可覆盖:openjdk 那批在 daocloud 上直接 denied,只能换 ECR Public 源
# (SRC_BASE=public.ecr.aws/docker/library bash scripts/mirror-build-envs.sh openjdk:17-jdk)。
SRC_BASE=${SRC_BASE:-docker.m.daocloud.io/library}
DST_BASE=${DST_BASE:-registry.cn-qingdao.aliyuncs.com/fubin}
IMAGES=(
  # ACR 上的 alpine:latest 是完整多架构档,不要在这里重列:imagetools 只会重建 amd64+arm64,
  # 再跑一次反而把现有 tag 收窄了。
  golang:1.24
  golang:1.22-alpine
  node:16-alpine
  node:18-alpine
  node:20-alpine
  node:22-alpine
  node:24-alpine
  node:26-alpine
  python:3.11-alpine
  python:3.12-alpine
  eclipse-temurin:8-jdk
  eclipse-temurin:11-jdk
  eclipse-temurin:17-jdk-alpine
  eclipse-temurin:21-jdk-alpine
  maven:3.9-eclipse-temurin-21
  ruby:3.3-alpine
  php:8.3-cli-alpine
  rust:1.79-alpine
)

# 命令行可以只点几个 tag(重跑缺的那几个,不必把全表再推一遍)。
if [ $# -gt 0 ]; then
  IMAGES=("$@")
fi

digest_for() { # <repo:tag> <arch>
  docker manifest inspect "$1" 2>/dev/null |
    python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit()
for m in d.get("manifests", []):
    p = m.get("platform", {})
    if p.get("os") == "linux" and p.get("architecture") == sys.argv[1] and "unknown" not in json.dumps(p):
        print(m["digest"]); break' "$2"
}

# 镜像站的 blob CDN 会偶发 5xx,导致 manifest 拉不全;重试比放弃更划算。
digest_retry() { # <repo:tag> <arch>
  local d=""
  for try in 1 2 3 4 5; do
    d=$(digest_for "$1" "$2")
    [ -n "$d" ] && { echo "$d"; return; }
    sleep 6
  done
}

fail=0
for img in "${IMAGES[@]}"; do
  repo=${img%%:*}
  tag=${img#*:}
  src="$SRC_BASE/$img"
  dst="$DST_BASE/$repo:$tag"
  amd=$(digest_retry "$src" amd64)
  arm=$(digest_retry "$src" arm64)
  # 上游有些档就是单架构(如 eclipse-temurin:17-jdk-alpine 只有 amd64),这时按能拿到的那档推,
  # 别因为「凑不齐两档」就整个跳过。
  refs=()
  [ -n "$amd" ] && refs+=("$SRC_BASE/$repo@$amd")
  [ -n "$arm" ] && refs+=("$SRC_BASE/$repo@$arm")
  if [ ${#refs[@]} -eq 0 ]; then
    echo "SKIP $img: 两档 digest 都拿不到"
    fail=1
    continue
  fi
  legs=""
  [ -n "$amd" ] && legs="amd64"
  [ -n "$arm" ] && legs="${legs:+$legs+}arm64"
  echo "== $img -> $dst ($legs)"
  ok=0
  for try in 1 2 3; do
    # 不用管道接 tail:pipefail 之外,tail 的退出码会盖掉 docker 的。
    if docker buildx imagetools create -t "$dst" "${refs[@]}" >/tmp/mirror-out 2>&1; then
      tail -1 /tmp/mirror-out
      ok=1
      break
    fi
    tail -1 /tmp/mirror-out
    echo "   第 $try 次失败(多为 ACR 429),8s 后重试"; sleep 8
  done
  [ "$ok" = 1 ] || { echo "FAIL $img"; fail=1; }
  sleep 3
done
exit $fail
