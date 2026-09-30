# 贡献指南 / Contributing to Pipewright

感谢你愿意为 Pipewright 出力!本文件说明如何搭环境、改代码、跑测试、提交 PR。
Thanks for helping out! This guide covers how to set up, change, test, and submit.

> 中文在前,English follows each section.

---

## 行为准则 / Code of Conduct

参与本项目即表示你同意遵守 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
By participating you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

---

## 开始之前 / Before you start

- **小改动直接发 PR**(typo、文档、明显 bug)。
- **大改动先开 Issue 讨论**(新功能、架构调整、破坏性变更),避免白做。
- 安全漏洞 **不要** 走公开 Issue,见 [SECURITY.md](SECURITY.md)。

- **Small changes** (typos, docs, obvious bugs): open a PR directly.
- **Large changes** (features, architecture, breaking changes): open an Issue first to align before you build.
- **Security issues**: do NOT open a public Issue — see [SECURITY.md](SECURITY.md).

---

## 环境要求 / Prerequisites

| 工具 / Tool | 版本 / Version |
|---|---|
| Go | 见 `go.mod`(当前 1.26+)/ see `go.mod` (currently 1.26+) |
| Node.js | 20 LTS+ |
| npm | 随 Node 附带 / bundled with Node |
| Docker | 隔离构建相关功能需要 / required for isolated-build features |

后端纯 Go、无 CGO;前端经 `go:embed` 内嵌进单一二进制。
Pure Go backend (no CGO); the frontend is embedded into a single binary via `go:embed`.

---

## 本地开发 / Local development

```bash
# 克隆 / clone
git clone https://github.com/fuuhoo/pipewright.git
cd pipewright

# 构建单一二进制(含前端)/ build the single binary (frontend included)
make build        # -> ./pipewright

# 运行(首次启动引导管理员)/ run (first launch bootstraps the admin)
./pipewright ./pipewright.yml
```

前端单独开发(热更新)/ Frontend dev with HMR:

```bash
cd web
npm ci
npm run dev
```

---

## 提交前自检 / Pre-submit checklist

PR 必须本地全绿才提。CI 会再跑一遍同样的检查。
Your PR must pass all of these locally; CI runs the same checks.

**后端 / Backend**

```bash
go vet ./...
go build ./...
go test ./...
gofmt -l .          # 应无输出 / should print nothing
```

**前端 / Frontend**(在 `web/` 下 / from `web/`)

```bash
npm run lint
npm run typecheck
npm run test        # vitest
# 视改动跑端到端 / run when relevant:
npm run e2e         # playwright
```

---

## 代码风格 / Code style

- **Go**:遵循标准 `gofmt`/`go vet`,惯用错误处理(`if err != nil`),避免无谓抽象。
- **前端**:ESLint + Prettier(`npm run format`),Vue 3 `<script setup>` + TypeScript,组件 PascalCase。
- **铁律**:CI/CD 关键路径绝不依赖 AI —— AI 不可用时必须优雅降级。任何让核心构建/部署强依赖 AI 的改动都会被拒。

- **Go**: standard `gofmt`/`go vet`, idiomatic error handling, no needless abstraction.
- **Frontend**: ESLint + Prettier (`npm run format`), Vue 3 `<script setup>` + TypeScript, PascalCase components.
- **Hard rule**: the CI/CD critical path must never depend on AI — it must degrade gracefully when AI is unavailable. PRs that make core build/deploy hard-depend on AI will be rejected.

---

## 提交信息 / Commit messages

使用 [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <简短描述>

type: feat | fix | docs | test | refactor | chore | perf | ci | build
```

示例 / Examples:

```
feat(pipeline): add branch→environment mapping validation
fix(deploy): roll back on health-gate timeout
docs(readme): clarify master key handling
```

---

## 分支规范 / Branching model

主干为 `master`,**永远保持可构建、CI 全绿**;**禁止直接 push 到 `master`**,一切变更经 PR 合入。
`master` is the trunk and must always build with green CI; **never push directly to `master`** — all changes land via PR.

**一改动 = 一分支 = 一 PR**,合并后删分支;下次从最新 `master` 重新切。
**One change = one branch = one PR.** Delete the branch after merge; branch off the latest `master` next time.

分支命名(`<type>/<简短描述>`,kebab-case)/ Branch naming (`<type>/<short-desc>`, kebab-case):

| 前缀 / Prefix | 用途 / Use |
|---|---|
| `feat/` | 新功能 / new feature |
| `fix/` | bug 修复 / bug fix |
| `docs/` | 文档 / docs only |
| `refactor/` | 重构(无行为变化)/ refactor, no behavior change |
| `chore/` | 构建 / CI / 依赖 / 杂项 / build, CI, deps, misc |

示例 / Example:`feat/branch-env-mapping`、`fix/deploy-health-gate-timeout`。

规则 / Rules:
- 不要在一条分支上堆多个不相关改动;不要复用已合并的分支继续开发。
  Don't pile unrelated changes onto one branch; don't keep developing on an already-merged branch.
- 长期分支落后时用 rebase 跟上 `master`(`git pull --rebase origin master`)。
  Rebase long-lived branches onto `master` (`git pull --rebase origin master`).

## Pull Request 流程 / PR workflow

1. 从 `master` 切分支:`feat/...`、`fix/...`、`docs/...`。
   Branch off `master`: `feat/...`, `fix/...`, `docs/...`.
2. 保持 PR 聚焦单一关注点,小而可评审。
   Keep PRs focused and small enough to review.
3. 关联 Issue(`Closes #123`),填好 PR 模板。
   Link the Issue (`Closes #123`) and fill in the PR template.
4. 确保 CI 全绿;有行为变化要补/改测试。
   Make CI green; add or update tests for behavior changes.
5. 至少一位维护者评审通过后合并(squash merge)。
   At least one maintainer approval is required; we squash-merge.

---

## 发版 / Releasing(维护者)

版本由 git tag 驱动,推送 `v*` tag 即触发 [`release` workflow](.github/workflows/release.yml):
GoReleaser 跨平台构建二进制 + 校验和 + changelog 发到 GitHub Release,并推多架构镜像到 ghcr.io。

```bash
# 1. 确保 master 全绿、本地与 origin 同步
git checkout master && git pull
# 2. 打 SemVer tag(预发布用 -rc.1 / -beta.1 等后缀,自动标记 prerelease 且不动 latest)
git tag -a v1.2.3 -m "v1.2.3"
git push origin v1.2.3
# 3. 改配置后本地干跑验证(不发布):
#    goreleaser check && goreleaser release --snapshot --clean --skip=publish,docker
```

版本元数据经 `-ldflags` 注入 `internal/version`,运行期可由 `pipewright --version` 或 `GET /version` 读取。

### 镜像另发阿里云 ACR(`aliyun-image.yml`)

ghcr 在国内经常拉不动,所以推 `v*` tag 时另有 [`aliyun-image.yml`](.github/workflows/aliyun-image.yml) 把镜像发到
`registry.cn-qingdao.aliyuncs.com/fubin/pipewright`,一次出**两档**多架构标签:`<版本>` / `latest`(distroless 默认档)
与 `<版本>-docker` / `latest-docker`(带 docker CLI,供容器内打包镜像;取舍见 README 部署章节)。
预发布版(`-rc`/`-beta`)不碰 `latest`。也可手动 `workflow_dispatch` 补推某版本(勾 `push_latest` 才覆盖 latest)。

它刻意独立于 GoReleaser:ghcr 只是 `.goreleaser.yaml` 写死的镜像目标,再往同一套配置里挂外部仓库凭据没必要;
这里顺手把 `-docker` 档一起出(ghcr 上只有默认档)。二进制按架构原生交叉编译,镜像阶段只 `COPY` 二进制 + 装 CLI,
所以不在 QEMU 里跑 Go 编译。

一次性前置(人工,缺一步流水线就在推送阶段报错):
1. ACR 控制台建命名空间 `fubin` 与仓库 `pipewright`(个人版不自动建仓库,否则报 `insufficient_scope`);
2. 仓库访问凭证设为「固定密码」;
3. GitHub → Settings → Secrets and variables → Actions 加 `ALIYUN_ACR_USERNAME` / `ALIYUN_ACR_PASSWORD`。
   **口令绝不进仓库文件、日志或镜像。**

本地验证镜像两档(不需要仓库凭据):

```bash
# 默认档
docker build -t pw-local:test .
# 带 CLI 档;国内把上游镜像换成 daocloud 前缀:--build-arg DOCKER_CLI_IMAGE=docker.m.daocloud.io/library/docker:cli
docker build --target with-docker -t pw-local:test-docker .
```

---

## 许可 / Licensing

提交即表示你的贡献以本项目的 [MIT License](LICENSE) 授权。
By contributing, you agree your contributions are licensed under the project's [MIT License](LICENSE).
