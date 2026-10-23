# vfox-full 叠加层:免 hook 的版本切换 + Maven + pnpm。
#
# 基底是 1.0.12(SDK 全量下载那一套在另一台构建机上完成,重跑一次要拉十几 GB),这一层只加
# 四样轻改动,所以能在本机几十秒内重建,也能在服务器上直接 docker pull:
#
#   1) /usr/local/bin/vfox-switch —— 不依赖 vfox 交互式 hook 的 SDK 切换(重指 sdks 软链)。
#   2) /usr/local/bin/vfox 换成包装层 —— 让脚本里的 `vfox use java@17` 在一次性容器里直接生效,
#      于是流水线命令不用再写 `eval "$(vfox activate bash)"` 或整段包 bash heredoc(平台脚本步是 sh/dash)。
#   3) Maven —— 基底只有 JDK/Node/Go/Python 本体,没有构建器,`mvn` 之前是 command not found。
#   4) pnpm —— 基底各 Node SDK 只带 npm/npx(部分带 corepack),pnpm 一律没有。装到 /opt/pnpm 并
#      链到 /usr/local/bin,不落进某一个 Node SDK 的 bin,`vfox use nodejs@18/22/24/26` 切换后仍可用;
#      版本取 12.x(node >=18,覆盖镜像内所有 Node,也符合平台缓存文档对 pnpm 12 的实测口径)。
#
# 构建(两种架构各一次,再在仓库侧合成中性多架构清单;基底必须是 1.0.12,理由见下面的防呆):
#   docker build --platform linux/arm64 -f deploy/vfox/Dockerfile \
#     --build-arg BASE_IMAGE=registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.12-arm64 \
#     -t registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14-arm64 deploy/vfox
#   docker build --platform linux/amd64 -f deploy/vfox/Dockerfile \
#     --build-arg BASE_IMAGE=registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.12-amd64 \
#     -t registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14-amd64 deploy/vfox
#   docker buildx imagetools create -t registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14 \
#     registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14-arm64 \
#     registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14-amd64
#
# 合成中性 tag 只能用 buildx imagetools(仓库侧合成):`docker manifest create` 会直接报
# "…:1.0.14-arm64 is a manifest list" —— push 时 buildx 给每个架构 tag 都附了 attestation,
# 本地 tag 本身已是清单而非单镜像。imagetools 撞 ACR 速率限制(429 toomanyrequests)时冷却
# 一两分钟再试(1.0.14 发布时三次 20 秒重试全失败,冷却后一次通过)。
#
# 只加不改:1.0.13 的 tag 不动,新 tag 出问题直接回退旧行。
#
# 不想过 ACR 也行:把 deploy/vfox/ 这三个文件 scp 到 amd64 服务器,在上面执行上面那条 amd64 构建
# (BASE_IMAGE 填服务器上 `docker pull` 得到的 1.0.12-amd64;叠加层只多下载 Maven 与 pnpm),
# 再把构建环境登记行的 image 填成那个本地 tag(平台执行是 docker run,宿主有镜像就不拉)。

ARG BASE_IMAGE=registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.12
FROM ${BASE_IMAGE}

ARG MAVEN_VERSION=3.9.16
ARG MAVEN_ALIYUN=https://mirrors.aliyun.com/apache/maven/maven-3
ARG MAVEN_APACHE=https://dlcdn.apache.org/maven/maven-3
# pnpm 12 的 engines 是 node >=18,镜像里 18/22/24/26 都能跑;npm 源先官方后国内镜像。
ARG PNPM_VERSION=12.14.0
ARG NPM_REGISTRY=https://registry.npmjs.org
ARG NPM_MIRROR=https://registry.npmmirror.com

LABEL org.opencontainers.image.description="Ubuntu + vfox CLI with preinstalled SDKs, hook-free switching, Maven and pnpm" \
      org.opencontainers.image.version="1.0.14"

# Maven 是纯 Java 包,与架构无关;aliyun 镜像在 CN 网络上更稳,失败再回源 apache。
RUN set -eu; \
    pkg="apache-maven-${MAVEN_VERSION}-bin.tar.gz"; \
    ok=0; \
    for base in "${MAVEN_ALIYUN}/${MAVEN_VERSION}/binaries" "${MAVEN_APACHE}/${MAVEN_VERSION}/binaries"; do \
      echo "[maven] try ${base}/${pkg}"; \
      if curl -fsSL --retry 8 --retry-delay 5 --connect-timeout 20 --max-time 600 -o "/tmp/${pkg}" "${base}/${pkg}"; then ok=1; break; fi; \
    done; \
    [ "$ok" = 1 ] || { echo "[maven] all mirrors failed" >&2; exit 1; }; \
    tar -xzf "/tmp/${pkg}" -C /opt; \
    rm -f "/tmp/${pkg}"; \
    ln -sfn "/opt/apache-maven-${MAVEN_VERSION}" /opt/maven; \
    ln -sf /opt/maven/bin/mvn /usr/local/bin/mvn

# pnpm 装在 /opt/pnpm 而不是某个 Node SDK 的 bin 下:后者是 `vfox use` 重指的软链目标,
# 切一次 Node 版本 pnpm 就跟着消失。npm 用默认 Node 版本执行,装完即与 Node 版本解耦。
RUN set -eu; \
    node_ver=$(sed -n 's/^nodejs //p' /opt/vfox/default.versions); \
    [ -n "$node_ver" ] || { echo "[pnpm] no default nodejs version" >&2; exit 1; }; \
    node_bin=$(find "/root/.vfox/cache/nodejs/v-${node_ver}" -maxdepth 3 -type f -path '*/bin/node' | head -1); \
    [ -n "$node_bin" ] || { echo "[pnpm] node ${node_ver} not found" >&2; exit 1; }; \
    export PATH="$(dirname "$node_bin"):$PATH"; \
    echo "[pnpm] node $(node -v), installing pnpm@${PNPM_VERSION}"; \
    ok=0; \
    for reg in "$NPM_REGISTRY" "$NPM_MIRROR"; do \
      echo "[pnpm] registry ${reg}"; \
      if npm install -g "pnpm@${PNPM_VERSION}" --prefix /opt/pnpm --registry="$reg" \
           --cache /tmp/npm-cache --no-audit --no-fund --fetch-retries=5; then ok=1; break; fi; \
      rm -rf /opt/pnpm; \
    done; \
    [ "$ok" = 1 ] || { echo "[pnpm] all registries failed" >&2; exit 1; }; \
    rm -rf /tmp/npm-cache /root/.npm; \
    ln -sf /opt/pnpm/bin/pnpm /usr/local/bin/pnpm; \
    ln -sf /opt/pnpm/bin/pnpx /usr/local/bin/pnpx; \
    pnpm -v; \
    du -sh /opt/pnpm

COPY vfox-switch vfox-wrapper /opt/vfox/shim/

# 冒烟测试之后必须把 sdks 软链与 .tool-versions 抹回构建期状态:这两个是容器启动时 entrypoint
# 用 `vfox env` 铺的,留一份烘进镜像的旧软链会让默认档变得不可预期(基底 Dockerfile 同样清掉了它们)。
# 防呆:这一层只能叠在 1.0.12 上。若基底已含本叠加层(/opt/vfox/bin/vfox 存在),下面的 mv 会把
# 1.0.13 的 vfox 包装脚本当成真二进制搬到 /opt/vfox/bin,包装层再 exec 它自己 —— `vfox env`
# 直接死循环(实测卡死)。所以宁可在构建期报错,也不要产出这种镜像。
RUN set -eu; \
    if [ -e /opt/vfox/bin/vfox ]; then \
      echo "[overlay] base image already contains this overlay; build from 1.0.12" >&2; \
      exit 1; \
    fi; \
    chmod +x /opt/vfox/shim/vfox-switch /opt/vfox/shim/vfox-wrapper; \
    mkdir -p /opt/vfox/bin; \
    mv /usr/local/bin/vfox /opt/vfox/bin/vfox; \
    install -m 0755 /opt/vfox/shim/vfox-switch /usr/local/bin/vfox-switch; \
    install -m 0755 /opt/vfox/shim/vfox-wrapper /usr/local/bin/vfox; \
    ln -sf /usr/local/bin/vfox-switch /usr/local/bin/vfox-use; \
    VFOX_HOME=/root/.vfox; \
    export VFOX_HOME; \
    PATH="$VFOX_HOME/sdks/nodejs/bin:$VFOX_HOME/sdks/java/bin:/opt/maven/bin:$PATH"; \
    export PATH; \
    vfox use java@21; \
    java -version 2>&1 | head -1; \
    vfox-switch nodejs@18; \
    node -v; \
    pnpm -v; \
    JAVA_HOME="$VFOX_HOME/sdks/java"; export JAVA_HOME; \
    mvn -v | head -2; \
    if vfox use java@99 >/dev/null 2>&1; then echo "[smoke] uninstalled version must fail loudly" >&2; exit 1; fi; \
    rm -rf "$VFOX_HOME/sdks"/* ; \
    cp /opt/vfox/default.versions "$VFOX_HOME/.tool-versions"; \
    echo "[ok] overlay layers built"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["bash"]
