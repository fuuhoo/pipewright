# 双运行模式之容器形态。此 Dockerfile 自包含从源码构建(docker build 全链路);发版流水线用
# Dockerfile.goreleaser 直接包装 GoReleaser 已交叉编译好的二进制(避免每架构重跑前端构建)。
#
# 两个运行时,按需选 --target(默认=最后一个=distroless):
#   runtime       distroless static + 单静态二进制(前端已 go:embed)。最小攻击面,但**不含容器 CLI**,
#                 所以这一档在容器内不能打包镜像(构建器会诚实降级为桩)。
#   with-docker   alpine + docker CLI。挂上宿主 /var/run/docker.sock 后,容器内就能真做镜像构建/推送,
#                 同时 UI 的「构建环境镜像检查」也走这条 CLI。见 README 部署章节的取舍说明。
#
# 国内构建:docker.io 直连常失败,把 node/golang/alpine 与下面的 DOCKER_CLI_IMAGE 一起换前缀,如
#   docker.m.daocloud.io/library/alpine:3.20、…/library/node:22-alpine、
#   --build-arg DOCKER_CLI_IMAGE=docker.m.daocloud.io/library/docker:cli

# 容器 CLI 的来源镜像。只取其中一个静态二进制(不跑 apk,构建过程零额外网络依赖)。
# 注:必须走「全局 ARG → 命名阶段」这条路,COPY --from 不做变量展开。
ARG DOCKER_CLI_IMAGE=docker:cli
FROM ${DOCKER_CLI_IMAGE} AS dockercli

# ---- 前端构建 ----
FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com && npm ci
COPY web/ ./
RUN npm run build

# ---- Go 静态构建 ----
FROM golang:1.26-alpine AS build
ENV CGO_ENABLED=0 \
    GOPROXY=https://goproxy.cn,direct \
    GOSUMDB=off \
    GOTOOLCHAIN=local
WORKDIR /app
# 版本元数据由构建方传入(docker build --build-arg VERSION=...);缺省为开发态。
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./web/dist
RUN go build \
    -ldflags "-s -w \
      -X github.com/huangchengsir/pipewright/internal/version.Version=${VERSION} \
      -X github.com/huangchengsir/pipewright/internal/version.Commit=${COMMIT} \
      -X github.com/huangchengsir/pipewright/internal/version.Date=${DATE}" \
    -o /pipewright ./cmd/pipewright
# 预建数据目录,归 nonroot(65532)所有:具名卷挂到 /data 会继承此属主,免手动 chown。
RUN mkdir -p /data

# ---- 带容器 CLI 的运行镜像(--target with-docker)----
# 取 docker:cli 里的 docker 二进制:它是**全静态**链接(docker:cli 里 ldd 报 "Not a valid dynamic
# program"),所以直接搬进 alpine 就能跑,不需要 apk add、也不看基础镜像的 libc 脸色。
# compose 插件刻意不装:平台的 compose/stacks 部署一律 SSH 到目标机执行(见 internal/deploy/
# docker_deploy.go 的 detectComposeBin),容器内用不上。
# buildx 插件同样不装:装了它,docker 29 的 `docker build` 就转给 buildx 的 docker 驱动,那要求
# 宿主 daemon 支持内嵌 BuildKit(较老版本不支持),而平台的构建器只会原样发 `docker build`
# (internal/build/shell_driver.go),不会自己降级。不装则走经典构建器:任何 daemon 版本都能跑,
# 代价只是日志里多一行 "legacy builder is deprecated" 提示。
FROM alpine:3.20 AS with-docker
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /pipewright /pipewright
# 工作目录 /data 同上;这一档以 root 运行是刻意的:要访问宿主 docker.sock,而 socket 属主是
# root:docker —— 挂 socket 本身等同授予宿主 root 级权限,再套 nonroot 只是自我安慰。
# 不需要容器内构建的人请用默认的 runtime 档(非 root、无 shell、无 CLI)。
RUN mkdir -p /data
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/pipewright"]

# ---- 运行镜像(默认档)----
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /pipewright /pipewright
COPY --from=build --chown=65532:65532 /data /data
# 工作目录设为 /data:sqlite(pipewright.db)与相对路径产物默认落此,持久化卷一挂即生效。
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/pipewright"]
