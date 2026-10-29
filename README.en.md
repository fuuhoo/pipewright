<div align="center">

# Pipewright

**A lightweight, self-hosted CI/CD + deployment + ops platform.**
A single static Go binary (frontend embedded, zero runtime dependencies) —
one tool replacing the "CI + Ansible/Kamal + Portainer" trio.

[![Release](https://img.shields.io/github/v/release/fuuhoo/pipewright)](https://github.com/fuuhoo/pipewright/releases)
[![CI](https://github.com/fuuhoo/pipewright/actions/workflows/ci.yml/badge.svg)](https://github.com/fuuhoo/pipewright/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[简体中文](README.md) | English

</div>

---

## Why Pipewright

Mainstream options are either heavy (Jenkins with a pile of plugins + JVM) or a three-tool assembly (Woodpecker/Drone + Ansible/Kamal + Portainer). Pipewright packs "continuous integration, multi-server deployment, and server/container ops" into **one static binary**: download, start, open the browser — that's the entire install.

It has since grown past that trio: publishing a deployed service on an HTTPS domain (the nginx + certbot step) and giving every pull request its own live preview URL are built in too.

| | Pipewright | Jenkins | Drone + Ansible + Portainer |
|---|:---:|:---:|:---:|
| Single-binary deploy | ✅ | ❌ JVM + plugins | ❌ three-tool assembly |
| Visual pipeline orchestration (DAG) | ✅ built-in canvas | plugin | hand-written YAML |
| Pipeline as code (per-branch YAML) | ✅ | plugin | ✅ |
| Isolated builds | ✅ | ✅ | ✅ |
| Multi-server deploy (SSH, agentless) | ✅ built-in | plugin | Ansible |
| Server / container ops | ✅ built-in | ❌ | Portainer |
| Image zero-downtime + failure rollback | ✅ | plugin | DIY |
| Auto HTTPS + domain reverse proxy | ✅ built-in | ❌ | ❌ |
| Per-PR preview environments | ✅ built-in | ❌ | ❌ |
| DORA metrics | ✅ built-in | plugin | ❌ |
| AI failure diagnosis | ✅ optional | ❌ | ❌ |
| One-click self-update | ✅ | ❌ | ❌ |

## Screenshots

**Global overview** — projects, run success rate, environment deployment status, server health, and DORA metrics, all on one screen:

![Dashboard](docs/screenshots/dashboard.png)

**Visual pipeline orchestration** — a two-level (stage/job) DAG canvas: horizontal links for serial, vertical side-by-side for true parallel. Supports matrix builds, manual approval gates, sidecar services, and post-stage steps; the canvas and YAML round-trip both ways:

![Pipeline canvas](docs/screenshots/pipeline-canvas.png)

**Run detail** — stage transitions, live logs (SSE push + history replay), build artifacts and image references, and per-step status:

![Run detail](docs/screenshots/run-detail.png)

**Container management** — one-stop management of containers/images/Stacks/volumes/networks across hosts, with lifecycle operations, live stats, logs, and an interactive terminal:

![Container management](docs/screenshots/containers.png)

## Feature Overview

- **🔐 Security foundation** — single-admin auth (argon2id + CSRF) · encrypted credential vault (NaCl secretbox, masked display, never plaintext) · OAuth app onboarding for Gitee / GitHub / GitLab / self-hosted instances (the access token lands straight in the vault as a reusable credential) · append-only audit (SQLite triggers hard-block UPDATE/DELETE) with an optional remote sink · per-run secret redaction across logs, diagnostics, and notifications.
- **🧩 Projects & pipelines** — visual orchestration canvas (stage DAG + intra-stage job-level DAG) · matrix builds · manual approval gates (approve straight from a notification via signed link) · sidecar services (attach DB/Redis for tests) · stage `when` conditions + post-stage steps · typed run parameters (enum/bool/number, validated at trigger time) · triggers: webhook, branch→environment mapping, 5-field cron, and upstream→downstream pipeline chaining (loop-safe by depth + path guards) · per-project concurrency caps with FIFO queueing · a reuse library of pipeline templates, variable groups, and custom nodes · server-authoritative validation.
- **📝 Pipeline as code** — commit the pipeline structure to `.pipewright.yml` and let it evolve per branch, with the canvas as the always-available fallback ([details below](#pipeline-as-code-gitops)).
- **🏗 Isolated builds & artifacts** — version-pinned isolated builds inside containers (docker/nerdctl/podman) · a local bare-mirror repo cache (incremental fetch, then a workspace in seconds) · per-project dependency cache: one fixed directory bind-mounted into the build container, so Maven/npm/Go/pip artifacts survive across runs with **zero pipeline configuration** · a per-project cache inspector with one-click clear (see where the bare mirror and the dependency cache live and how big they are; drop them when you suspect a stale checkout or an old SNAPSHOT, every clear is audited) · build dependency caching keyed by branch + lockfile hash · a content-addressed artifact store that keeps the **real bytes** of jar/dist for deployment (not just a placeholder reference) · image build + push to private registries with image GC · an optional remote build machine per project (build is offloaded over SSH; tokens stay on the control node, and by design nothing is left cached on the remote box) · JUnit + Cobertura test reports feeding quality gates that fail the stage and block downstream deploys · live terminal logs (SSE) + history replay · read-only code browsing (Monaco).
- **🚀 Multi-server deployment** — agentless deploy over SSH · artifacts laid directly into the target directory you name (`jar`/`dist` contents land as-is) + restart command + health gating · image deploys swap the running container and roll back to the previous image on failure · **whole compose stacks** (a `docker-compose.yml` from your repo handed to the target machine's compose CLI, the same stack the Containers page manages) · **Kubernetes releases** (the platform talks to the cluster API directly, no jump host) · parallel fan-out across hosts + visible partial failures · command-style deploys (restart a service with no artifact) · **environments as first-class objects**: per-environment deployment timeline, current active version, and one-click rollback to the last fully successful deploy · environment promotion chains (dev→staging→prod) with per-environment variables/secrets and approval gates.
- **🌐 Auto HTTPS + domain reverse proxy** — one managed Caddy container per target host, orchestrated over the same SSH + docker path as container ops (render Caddyfile → `docker cp` → graceful reload). Certificates are issued and renewed automatically by Let's Encrypt over HTTP-01, or over **DNS-01 with Cloudflare / DNSPod / Alibaba Cloud DNS** for wildcards. Plus: multi-domain aliases, path routing (`/api`→A, `/`→B), redirects, access control (basic auth, IP allow/deny CIDR), HSTS / security headers / compression, load balancing across upstreams with active health-check failover, WebSocket / gRPC (h2c) / TCP passthrough (caddy-l4), a certificate dashboard that probes the real 443 handshake, and one-click subdomain allocation.
- **🔎 Per-PR preview environments** — when a PR's run deploys successfully, it automatically gets a throwaway `pr-<n>-<proj>.<base>` domain with its own certificate and route, so reviewers open one link and see that PR actually running. Idempotent per PR, and reclaimed automatically — but **only** once the PR is provably closed or merged.
- **📣 Notifications** — WeCom / DingTalk / Lark (Feishu) / Slack / email / custom webhook · fine-grained event→channel routing · templates + custom variables · rich Lark cards with approve/detail action buttons and a release summary · in-pipeline notification nodes.
- **🖥 Server & container ops** — multi-host status overview (CPU/memory/disk) plus time-series trend charts · container/image/Stacks/volume/network management · container create/inspect/prune · live + historical service logs · live stats · interactive container terminal · web ops terminal (host shell, full copy-paste/signal support) · configurable anomaly detection that runs on a timer, dedupes by cooldown, and routes alerts to your notification channels.
- **🤖 AI assist (optional, fully degradable)** — bring your own Claude / OpenAI / Ollama endpoint (API key encrypted in the vault). Automatic root-cause diagnosis when a build or deploy fails, with a 👍/👎 feedback loop and accuracy stats · repo analysis → generated pipeline draft · success-vs-failure commit diff · script risk annotation · natural-language→shell assistant and container diagnosis in the ops terminal. The core CI/CD path never depends on any of it (NFR-10).
- **📈 Metrics** — the four DORA metrics (deployment frequency / lead time for changes / change failure rate / mean time to restore) out of the box, with Elite/High/Medium/Low performance bands.
- **🧹 Housekeeping & platform** — configurable run-data retention sweeper (off by default; never touches in-flight runs) · SQLite (pure Go) or MySQL · 8 UI languages (zh-CN / zh-TW / en / ja / ko / de / fr / es) including server-side localization of API error messages.
- **🔄 Update check + one-click self-update** — Settings → System checks GitHub for the latest release with semantic comparison; binary deployments can **auto-update with one click** from the UI (download + checksum verification + atomic replace + self-restart), while Docker deployments get the exact upgrade command.

> Security is non-negotiable: credentials stored as ciphertext only, commands arrayified against injection, outbound SSRF locked down, logs redacted.

## Install / Deploy

Pick any of three form factors. The platform itself is a single static binary with **zero runtime dependencies** (no Go/Node required).

> **Docker prerequisite**: the platform itself doesn't depend on Docker, but **"isolated builds / container deployment" do require Docker** (without it, it degrades to a stub runner and performs no real builds). The console / SSH deployment / notifications don't need Docker. The one-click script **detects Docker** and prompts if it's missing; on Linux you can set `INSTALL_DOCKER=1` to auto-install it (via the official get.docker.com); on macOS, install Docker Desktop.

### ① One-click script (Linux / macOS)

Downloads the static binary for your platform from GitHub Releases and installs it to `/usr/local/bin` (with checksum verification + Docker detection):

```bash
curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh | sh

# Pin a version / custom dir / auto-install Docker on Linux too:
VERSION=v0.15.0 INSTALL_DIR=$HOME/.local/bin INSTALL_DOCKER=1 \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"

# Run (first launch bootstraps the admin; master key is for the credential vault)
PIPEWRIGHT_MASTER_KEY=$(openssl rand -base64 32) \
PIPEWRIGHT_ADMIN_PASSWORD=change-me \
  pipewright          # open http://localhost:8080, log in with admin / change-me
```

**Recommended: install as a systemd service** (auto-start on boot + restart on crash + one-click self-update available; Linux, requires root). The script persists the master key to `/etc/pipewright/master.key`, stores data in `/var/lib/pipewright`, and writes config to `/etc/pipewright/pipewright.env`:

```bash
SETUP_SERVICE=1 sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"
# Status / logs: systemctl status pipewright  ·  journalctl -u pipewright -f
# Change port etc.: edit /etc/pipewright/pipewright.env then systemctl restart pipewright

# Use MySQL instead of the default SQLite (DSN is go-sql-driver format; parseTime=true is required):
SETUP_SERVICE=1 PIPEWRIGHT_DB_DRIVER=mysql \
  PIPEWRIGHT_DB_DSN='user:pw@tcp(host:3306)/pipewright?parseTime=true&charset=utf8mb4' \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"
```

**Portable mode: the binary and all of its data land in the directory you run the command from** (no root, no system paths; copying the directory is the migration). `cd` into it and run one command — the script also generates the master key, the runtime config and a launcher:

```bash
mkdir -p ~/pw && cd ~/pw
LOCAL_INSTALL=1 sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"

PIPEWRIGHT_ADMIN_PASSWORD=change-me ./run.sh   # listens on :8080; the password is only used to bootstrap the admin account
```

Everything sits under the instance root (pass `INSTALL_DIR` to put it elsewhere): `./pipewright` binary · `./data/` (SQLite DB, with `artifacts/ repos/ cache/ config_profiles/` alongside it) · `./master.key` (0600) · `./pipewright.env` (0600 — change the port or switch to MySQL here; reinstalling never overwrites it) · `./run.sh` launcher. All data paths are written absolute, so launching `./run.sh` from another directory still opens the same database. Mutually exclusive with `SETUP_SERVICE=1` (portable mode writes no systemd unit); when migrating, always bring `master.key` along — a new key makes existing credentials undecryptable.

**To keep even the throwaway build workspaces inside that one directory**, add `TMPDIR` yourself: the generated `pipewright.env` does not contain it, while workspaces are created by `os.MkdirTemp`, which follows `TMPDIR` (`internal/build/cloner.go:141`) — without it they land in the system `/tmp`.

```bash
echo "TMPDIR=$(pwd)/work/tmp" >> ./pipewright.env && mkdir -p ./work/tmp
```

The dependency cache needs no extra step: in process form it defaults to `data/project-cache` next to the DB (`internal/projectcache/root.go:46`). Measured on the box where we did this (CentOS 7): after the change the only writable files the process holds are `pipewright.db{,-wal,-shm}` and its log inside the instance directory, and nothing matching `pipewright*` remains under `/tmp`, `/var/tmp` or `~/.cache`. The only thing still outside is docker itself — builds drive the host daemon, so images live in `/var/lib/docker`.

> Windows users: download the `.zip` from [Releases](https://github.com/fuuhoo/pipewright/releases).

### ② docker compose (recommended for self-hosting)

```bash
curl -fsSLO https://raw.githubusercontent.com/fuuhoo/pipewright/master/docker-compose.yml
curl -fsSLO https://raw.githubusercontent.com/fuuhoo/pipewright/master/.env.example
cp .env.example .env       # at minimum set PIPEWRIGHT_ADMIN_PASSWORD, and openssl rand -base64 32 for MASTER_KEY
docker login registry.cn-qingdao.aliyuncs.com   # both ACR tags are in a **private** repo — an anonymous pull gets 401
docker compose up -d       # data persists in the named volume pipewright-data; see .env comments to switch to MySQL
```

> Do not skip the login: requesting `.../v2/fubin/pipewright/manifests/latest` with an anonymous token returns HTTP 401, so `docker compose up -d` stalls on `pull access denied`. The ACR credentials are the **access credentials / fixed password** you set in the Container Registry console — *not* your Aliyun account password; a read-only RAM user is enough.
> Switching `PIPEWRIGHT_IMAGE` to `ghcr.io/fuuhoo/pipewright` may require a login too, depending on whether that package is public or private (we have not tested an anonymous ghcr pull; if the pull fails, `docker login ghcr.io` with your GitHub username and a PAT scoped `read:packages`).

The compose file itself only reads these (the app's own variables are in the [configuration](#configuration-environment-variables) table below):

| Variable | Purpose | Default |
|---|---|---|
| `PIPEWRIGHT_IMAGE` | Image repository prefix | `registry.cn-qingdao.aliyuncs.com/fubin/pipewright` (Aliyun ACR, Qingdao — both tiers); `ghcr.io/fuuhoo/pipewright` works too but ships only the default tier |
| `PIPEWRIGHT_VERSION` | Image tag — **two tiers**, see below | `latest` |
| `PIPEWRIGHT_PORT` | Host-side published port | `8080` |
| `PIPEWRIGHT_WORK_DIR` | Build workspace directory (absolute host path). It is also where the per-project dependency cache lands (`pipewright-project-cache/` under it), so mapping this one path is all you need for cached builds inside the container | empty (in-container isolated builds unavailable, cache falls back to the container's `/tmp`) |
| `PIPEWRIGHT_RUNNER` | Empty = the default DAG runner; `legacy` falls back to the old fixed flow | empty |
| `PIPEWRIGHT_DATA_DIR` | Where config-profile files are stored (`config_profiles/` beneath it). To build inside the container *and* tick a config profile, this too must be a host-same-path directory — `PIPEWRIGHT_WORK_DIR/data` reuses the one existing bind, no extra volumes line | `./data` (under the container WORKDIR, i.e. inside the named volume) |
| `PIPEWRIGHT_BIN_DIR` | Host directory holding the binary — only used by the "swap just the binary" override below | `./bin` |

Everything the app stores (SQLite DB, artifact library, repo cache, build dependency cache) lives under `/data` in the container, persisted by the named volume `pipewright-data`. Keep the master key forever once credentials exist — rotating it makes stored credentials undecryptable.

If you would rather have a plain directory you can `rsync` or browse, replace that volume with a bind (`- /srv/pipewright/data:/data`). One catch: **the default tier runs as non-root (uid 65532)** — a named volume inherits the owner of `/data` baked into the image (`Dockerfile:72-74`), whereas a host directory is normally root-owned, so the very first start cannot write the SQLite DB. Either `chown 65532:65532` it, or use the `-docker` tier (which runs as root on purpose, because it needs the host socket).

**One exception**: the per-project dependency cache does **not** live under `/data`. It is bind-mounted into build containers via `docker run -v`, and the source path of `-v` is resolved by the **host** daemon — `/data` on the host is really `/var/lib/docker/volumes/pipewright-data/_data`, so using `/data/...` as the source would just create an empty directory at the host root. It therefore follows `PIPEWRIGHT_WORK_DIR` (the one path that must exist at the same absolute location on host and container); see the next section.

Config-profile files (`config_profiles/` under `PIPEWRIGHT_DATA_DIR`) are the second casualty of the same rule: a ticked profile is bind-mounted read-only at its `target_path`, and when that path only exists inside the container docker now fails loudly with `bind source path does not exist` — instead of the old behaviour of mounting an empty directory and letting Maven complain `Is a directory`. Point `PIPEWRIGHT_DATA_DIR` at `PIPEWRIGHT_WORK_DIR/data` to share that one same-path bind. Leaving the default logs a `[configprofile]` warning at startup saying exactly this.

#### Two image tiers: building images inside the container

| Tag | Base | Can build images in-container |
|---|---|---|
| `<version>` / `latest` | distroless: non-root, no shell, no container CLI | **No.** Startup logs say the builder fell back to the stub |
| `<version>-docker` / `latest-docker` | alpine + docker CLI, runs as root | Yes, once you also mount the host socket (below) |

CI publishes both tiers as multi-arch images to Aliyun Container Registry (Qingdao) via `.github/workflows/aliyun-image.yml` — that is also what `docker-compose.yml` and `.env.example` default to. GoReleaser additionally publishes the default tier to `ghcr.io/fuuhoo/pipewright` (`latest` / `<version>`) when you push a `v*` tag; ghcr ships no `-docker` tier, so pull `-docker` from ACR, or build it yourself from the root `Dockerfile` with `--target with-docker`. The default tier is deliberate: smallest attack surface. Upgrade to `-docker` only if the platform really has to build/push images from inside its own container.

#### Let the platform build images inside its container (change all three, none optional — plus a fourth for private registries)

1. switch `.env` to the `-docker` tier: `PIPEWRIGHT_VERSION=latest-docker` (the default `PIPEWRIGHT_IMAGE` is already ACR, which has both tiers — if you set it to `ghcr.io/fuuhoo/pipewright`, ghcr has no `-docker` tag and the pull will fail);
2. uncomment `- /var/run/docker.sock:/var/run/docker.sock` in `docker-compose.yml`. **Mounting the socket is equivalent to granting the container root-level access to the host** — only do it on a machine you trust;
3. uncomment the workspace self-bind and point `TMPDIR` at the same directory — all three values come from `PIPEWRIGHT_WORK_DIR` in `.env`.

Step 3 is not busywork: an isolated script-job build issues `docker run -v <workspace>:<mount>`, and the daemon is the **host's** — it resolves the `-v` source as a host path. When the paths differ the daemon silently creates an empty directory on the host and the build gets an empty workspace; the symptom is a log full of `no such file or directory` while the platform looks healthy. The `volumes` comments in `docker-compose.yml` spell it out line by line.

**Add a fourth change only when private images are involved** (your base images live in your own ACR / Harbor, or `pushImage` targets a private registry). Once the socket is mounted, builds and pushes are issued by the **docker CLI inside the container**, which reads *its own* `~/.docker/config.json` — and that file is empty, so the host's login state does not carry over. The measured symptom is not "image not found" but `denied` / `unauthorized: authentication required` for every private image, which is easy to misread as the tag not existing. Fix: bind the host credential file in read-only.

```yaml
volumes:
  # whichever user logged in on the host decides the source path; :ro is deliberate —
  # the container has no business writing credentials to the host
  - ${PIPEWRIGHT_HOST_DOCKER_CONFIG:-/root/.docker/config.json}:/root/.docker/config.json:ro
```

The `-docker` tier runs as root, so the path lines up; set `PIPEWRIGHT_HOST_DOCKER_CONFIG` on machines where you logged in as another user or use a custom `DOCKER_CONFIG`. This is unnecessary on the default tier or with public registries, which is why the line stays commented out in `docker-compose.yml`.

One command proves it works (the `-docker` tier has a shell):

```bash
docker exec pipewright sh -c 'mkdir -p "$TMPDIR/probe" && printf "FROM alpine:3.20\nRUN echo ok\n" > "$TMPDIR/probe/Dockerfile" && docker build -q "$TMPDIR/probe"'
```

A `sha256:…` on the last line means success. The `DEPRECATED: The legacy builder is deprecated` notice in between is expected: the `-docker` tier ships no buildx plugin on purpose (with buildx installed, `docker build` hands off to buildx, which puts requirements on the host daemon version; without it any daemon works).

#### Already running a bare binary on this machine? Use the `docker-compose.devtest.yml` overlay

Those three edits are already packaged as an overlay in the repo root, which also frees up the port and container name so the container tier can sit next to the bare instance on :8080:

```bash
# In .env set: PIPEWRIGHT_VERSION=<ver>-docker, PIPEWRIGHT_PORT=8081, PIPEWRIGHT_WORK_DIR=<absolute host path>,
#              PIPEWRIGHT_DATA_DIR=$PIPEWRIGHT_WORK_DIR/data (config profiles need the same host-visible path)
mkdir -p "$PIPEWRIGHT_WORK_DIR"      # the dir must exist on the host first, otherwise the same-path bind is pointless
docker compose -p pwtest -f docker-compose.yml -f docker-compose.devtest.yml up -d
```

The overlay does exactly three things: renames the container to `pipewright-composetest`; declares `ports` with `!override` so it **replaces** instead of appends (compose appends lists by default — without this it requests both 8080 and 8081 and fails to start next to the bare instance); and enables the docker.sock plus same-path workspace bind. Data lives in the `pwtest_pipewright-data` volume, dependency cache under `PIPEWRIGHT_WORK_DIR`, and config profiles under its `data/` subdirectory (the same one bind — that is what lets a build node actually mount a ticked profile), all namespaced by the project name `pwtest`, so nothing touches the default stack; `down` without `-v` keeps the data. Mounting the socket grants the container host-level root — this file is for a trusted local machine, don't copy it into production.

#### Seeing the host's images / containers from inside the container

Those screens use SSH, not the socket — the image and container panels always SSH to the target machine, there is no "read my local socket" shortcut. And `localhost` inside a container is the container itself, so compose ships:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

which resolves `host.docker.internal` to the host gateway. The remaining step is in the UI: add a server in **Settings → Servers** with host `host.docker.internal` (port 22, authenticated with the host's SSH key or password). That server's "Images" and "Containers" screens then show the host daemon's content, and container deploys go there too.

> Building in-container and registering the host over SSH are independent: the first only affects whether pipelines can produce images inside the platform container, the second only whether the UI can list host images. If you only deploy, the distroless tier without a socket is enough.

#### Swap just the binary: mount the program into the container

Only one ~40MB static binary in the published image is actually the app — everything else is runtime scaffolding (alpine / distroless, docker CLI, CA certificates, `/data`). If pulling images on your host is slow, or you simply want "upgrade" to mean "replace a file", let the image be the runtime and mount the binary from the host.

Drop an override file next to `docker-compose.yml`:

```yaml
# docker-compose.binary.yml — image = runtime only, program = mounted from the host
services:
  pipewright:
    # run the mounted binary, not the one baked into the image at /pipewright
    entrypoint: ["/opt/pipewright/bin/pipewright"]
    volumes:
      # mount a directory, not a single file (why, below); relative to this file, or put an absolute host path in .env
      - ${PIPEWRIGHT_BIN_DIR:-./bin}:/opt/pipewright/bin:ro
    healthcheck:
      # must move to the new path too: the image's own /pipewright is still there, so leaving this checks the stale binary
      test: ["CMD", "/opt/pipewright/bin/pipewright", "--version"]
```

When compose merges overrides, `volumes` **appends** (the `pipewright-data:/data` above still applies) while `entrypoint` and `healthcheck` are replaced. Start it:

```bash
mkdir -p bin
# Two ways to get the binary — it has to match the **container's** platform, not the host's:
#   a) install right on the target machine (drops into ./bin only, writes no systemd unit;
#      the variables belong to sh, not to curl)
curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh |
  INSTALL_DIR=./bin SETUP_SERVICE=0 sh
#   b) cross-compile here (GOARCH = the container's architecture; BIN= keeps ./pipewright intact)
GOOS=linux GOARCH=amd64 make go-build BIN=bin/pipewright

docker compose -f docker-compose.yml -f docker-compose.binary.yml up -d
```

Upgrading afterwards is replace-the-file plus restart, with no image pull at all:

```bash
curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh |
  VERSION=v0.5.2 INSTALL_DIR=./bin SETUP_SERVICE=0 sh
docker compose -f docker-compose.yml -f docker-compose.binary.yml restart
curl -s localhost:8080/version    # {"version":"v0.5.2","commit":"dfe5f43",…} — platform is whatever arch you compiled for
```

Key points; the first two are measured conclusions:

- **Architecture follows the container, not the host.** A `make build` artifact from macOS is a darwin binary and the linux container will answer `exec format error`. The binary is `CGO_ENABLED=0` and fully static, so one file runs in both tiers — verified locally (arm64) on `latest` (distroless, nonroot) and `latest-docker` (alpine, root): inside the container the image's own `/pipewright --version` said `0.5.0`, the mounted one said `v0.5.2`, and `/version` returned the latter; login and API worked.
- **Mount a directory, not a single file.** The safe way to replace a binary is "write a temp file, then rename" (`install.sh` and the platform's own updater both do it that way), and a rename swaps the inode. A directory mount tracks the directory, so `restart` picks up the new version; a single-file mount is bound to the inode resolved when the container was created, and whether it then sees the new file depends on how the runtime re-resolves the host path (OrbStack here did pick it up on `restart`, but don't bet that behaviour on Linux dockerd). If you do mount one file, use `up -d --force-recreate` after replacing it.
- **Permissions.** Binary `0755`, and the host directory must be traversable — the default tier runs as nonroot (uid 65532) and executes via the others bits. On SELinux-enforcing hosts (RHEL / Fedora family) a bind mount can fail with `permission denied`; add `:z` to the mount and retry.
- **Don't downgrade.** The data volume is untouched and a new version migrates on first boot, but an old binary against a schema that already moved forward will not work — migrations only go up. To roll back, roll the file back too (keep the previous binary around).
- **The UI's "Update now" does not apply here.** `/.dockerenv` exists in the container, so the platform reports the Docker form and hands you `docker compose pull && docker compose up -d` (`internal/version/selfupdate.go:29-43`) — and pulling replaces the image layer, not the mounted file that is actually running. For one-click updates use the binary form in ①.

### ③ docker run (fastest trial)

```bash
docker run -d -p 8080:8080 -v pipewright-data:/data \
  -e PIPEWRIGHT_ADMIN_PASSWORD=change-me \
  -e PIPEWRIGHT_MASTER_KEY=$(openssl rand -base64 32) \
  registry.cn-qingdao.aliyuncs.com/fubin/pipewright:latest
```

### ④ Air-gapped hosts: no GitHub, no Docker Hub

This is the route we actually got green on a CentOS 7 box inside a private network (it times out against `raw.githubusercontent.com`, `registry-1.docker.io` and `ghcr.io`, while its own ACR is reachable). Work through four layers: the platform binary, the build-environment images, the source repo, then dependencies.

**The platform itself.** The compose form follows ② (the default registry is already ACR — just `docker login`). For the portable form, when Releases are unreachable, lift the binary out of an image you already pulled — it is a `CGO_ENABLED=0` static file and runs fine on old systems like CentOS 7:

```bash
D=/home/pipewright && mkdir -p $D/data $D/work/tmp
cid=$(docker create registry.cn-qingdao.aliyuncs.com/fubin/pipewright:latest-docker)
docker cp $cid:/pipewright $D/pipewright && docker rm -f $cid && chmod +x $D/pipewright
```

This is ①'s portable form done by hand instead of by the script: generate `master.key` once (`openssl rand -base64 32 > master.key && chmod 600 master.key` — never regenerate it once credentials exist), write the `pipewright.env` lines listed in ① (include `TMPDIR`), and copy the `run.sh` launcher from ①.

**Build-environment images.** The preset catalogue ships Docker Hub names (`node:26-alpine`, `golang:1.22-alpine`, … see `internal/buildenv/seed.go`), which an air-gapped host cannot pull at all — the first build node fails on a pull timeout. Two options:

- Sync the images you need into your own ACR / Harbor, then edit the **image** field per row under **Settings → Build environments**; with many rows, export the catalogue on a connected machine, rewrite the prefix in bulk and import it back (build environments support export / import).
- Afterwards use **Check all** on that page (or select rows and use **Check selected**): each row should read **Available (local)** (already on the build machine) or **Pullable**. **A row whose image address you just edited drops back to "Unchecked" and can only be enabled after a successful check**; anything still marked **Unavailable** will fail the moment a job picks it. On an air-gapped host, use the page's **Pull** action to land Pullable images on the build machine ahead of time, sparing the pipeline a pull that would only time out.

**The source repository.** Self-hosted Git works, under two hard conditions plus one credential quirk:

- The transport must be **smart HTTP** (`git-http-backend` CGI) or SSH. Serving a bare repo as static files (e.g. `python3 -m http.server` over a `--bare` directory) is dumb HTTP, which go-git rejects as `repo_unreachable`.
- The URL cannot be `127.0.0.1` / `localhost` / `file://` — loopback, link-local (including the cloud metadata address `169.254.169.254`) fall in the SSRF blocklist (`internal/giturl/giturl.go:145`) and are rejected. **RFC1918 space and internal DNS names are allowed**, so use that machine's LAN IP or hostname.
- Even an anonymously pullable repository needs a credential attached — validation requires one whenever a repo URL is set (`internal/project/project.go:204-205`). The vault rejects an empty secret (`internal/vault/vault.go:215-216`), so create a `git_token` with any placeholder value that will never be used; an anonymous HTTP repo never carries it along.

**Dependency sources.** When npm / Maven / pip / Go cannot reach the public internet, mount `.npmrc` / `settings.xml` / `pip.conf` into the build container via **Settings → Config profiles** (that dialog offers the common target paths as chips), and put things like `GOPROXY` into the pipeline command or environment. In the container form remember that config profiles also need `PIPEWRIGHT_DATA_DIR` to be a host-same-path directory, otherwise docker reports `bind source path does not exist` (see note ④ in the `volumes` block of `docker-compose.yml`).

### After deploying: prove the path actually works

A reachable UI does not mean the platform works — the two things most likely to be broken ("builds get an empty workspace", "private images cannot be pulled") never make the platform itself report an error. Run one minimal pipeline that covers both legs, artifact and image:

1. Create a project pointing at your git URL (on an air-gapped host, per the section above);
2. Build a three-node pipeline: **source** → **build artifact** (artifact tier: build command plus `artifactPath`) → **build image** (image tier, `pushImage` off, and a `Dockerfile` whose `FROM` is an image already present on the build machine, so nothing has to be pulled from the public internet);
3. Run it. The run detail page should list **two** artifacts: an `archive` (its size is the packed `artifactPath`) and an `image`;
4. On the build machine run `docker images | grep '^pipewright/'` — local images are named `pipewright/<project slug>:<short commit>` (`internal/build/builder.go:35,462`). Seeing it proves the image really landed in the host daemon instead of the builder spinning its wheels;
5. Back on the run page, download that `archive` — getting bytes back means the artifact store is readable.

The standalone form builds on its own machine without registering it under **Settings → Servers**; the container form needs the three socket/workspace changes from ② first.

Reference numbers from the actual run (CentOS 7, standalone form, a Go example that only uses the standard library, base image already present on the build machine): `archive` 4,763,800 bytes, local image 13,185,840 bytes; the cold run took 17 seconds and the second run of the same project 2 seconds. That example downloads no dependencies, so the gap only reflects clone and packaging overhead — for dependency-heavy projects the warm/cold difference lives in `data/project-cache` (the platform's zero-config per-project cache).

### Build from source

```bash
make build          # frontend build → go:embed → single static binary ./pipewright (pure Go, no CGO)
./pipewright --version
```

### Updating

Open **Settings → System** and click "Check for updates" to query the latest release; when a new version is available:

- **Binary deployment**: click "Update now" to auto-download the new version + verify checksum + replace + restart (requires write permission to the binary file; installing to `$HOME/.local/bin` avoids sudo, and a root systemd service installed via `SETUP_SERVICE=1` also satisfies this).
- **Docker deployment**: the container doesn't replace its own image; follow the prompt to run `docker compose pull && docker compose up -d` on the host (the data volume is preserved).
- **Compose with a mounted binary** (see the section above): replace `./bin/pipewright`, then `docker compose -f docker-compose.yml -f docker-compose.binary.yml restart` — no image pull; confirm with `curl :8080/version`.
- **Portable / standalone form** (see ①): the binary sits in your instance directory and belongs to you, so "Update now" always meets its write-permission condition — but the download still goes to GitHub Releases. With no external network, follow ④: `docker cp` the binary out of the new image over `./pipewright` and re-run `./run.sh`; neither `pipewright.db` nor `master.key` has to move.

### Configuration (environment variables)

A normal install only needs the first two (plus `PIPEWRIGHT_PUBLIC_URL` if you run behind a reverse proxy); everything else has a sane default.

The listen address can also be set on the command line, which wins over `PIPEWRIGHT_ADDR` (works the same on Linux/macOS/Windows; both `--flag value` and `--flag=value`, and a single dash, are accepted):

| Flag | Description | Example |
|---|---|---|
| `--addr` | Full listen address; leave the host empty to bind every interface | `pipewright --addr :9090`, `pipewright --addr 127.0.0.1:9090` |
| `--port` | Port only, keeping the host part of the current address | `pipewright.exe --port 9090` |
| `--version` | Print version/commit/build date and exit (before any side effects) | `pipewright --version` |

**Core**

| Variable | Description | Default |
|---|---|---|
| `PIPEWRIGHT_ADMIN_PASSWORD` | Admin password on first launch (ignored once the DB already has an admin; if it is lost, see "Lost the admin password" below) | none (must be set) |
| `PIPEWRIGHT_MASTER_KEY` | Credential vault master key (base64-encoded 32 bytes); or use `PIPEWRIGHT_MASTER_KEY_FILE` to point to a file | vault disabled if unset |
| `PIPEWRIGHT_ADDR` | HTTP listen address | `:8080` |
| `PIPEWRIGHT_PUBLIC_URL` | Externally reachable base URL (e.g. `https://ci.example.com`). Required for webhook callbacks, OAuth redirects, signed approval links in notifications, and PR status links | none |
| `PIPEWRIGHT_ADMIN_USERNAME` | Admin username on first launch | `admin` |
| `PIPEWRIGHT_TRUST_PROXY` | Trust the first `X-Forwarded-For` hop as the audit client IP (`1`/`true`/`yes`/`on`). Leave off unless a trusted reverse proxy sits in front — otherwise anyone can forge audit source IPs | off |
| `PIPEWRIGHT_RELEASE_REPO` | GitHub repo queried for update checks (change it for a fork) | `fuuhoo/pipewright` |
| `PIPEWRIGHT_RUNTIME` | Set `docker` to declare a container deployment (affects self-update mode); otherwise auto-detected via `/.dockerenv` | auto-detect |
| `PIPEWRIGHT_AUDIT_SINK` | Remote audit sink: an `http(s)://` endpoint, or any other value as a second local JSON Lines file path. Keeps audit records complete even if the local DB is wiped | none |

**Lost the admin password**: `PIPEWRIGHT_ADMIN_PASSWORD` only takes effect on the one launch where the DB has no admin yet; changing it later never touches the account. Run one subcommand **on the machine hosting the platform** — it opens the local DB only, binds no port, and the new password is in effect at the next login:

```bash
pipewright reset-admin-password --stdin < newpass.txt   # also supports --password-file / --generate
docker exec -it <container> /pipewright reset-admin-password --stdin   # compose form: the binary sits at /pipewright in the image root
```

The password is only accepted from stdin / a file / `--generate`. There is deliberately no `--password` flag (`ps` and shell history would both keep a copy), and the command never re-reads `PIPEWRIGHT_ADMIN_PASSWORD`. It handles the built-in admin only; for other accounts, sign in as a different admin and reset them under **User management**.

> **The directory you run it from decides which database you change.** The database path defaults to `pipewright.db`, relative to the current directory, and this command only ever touches the one it resolves to. `docker exec` lands in the image's `/data` (where the DB lives); if you have `cd`-ed into a subdirectory (e.g. `/data/artifacts`) the resolved path is a file that does not exist there — the command now **stops with an error** instead of creating an empty database and reporting success. Go back to `/data`, or spell the path out:
>
> ```bash
> docker exec -it <container> sh -c 'cd /data && /pipewright reset-admin-password --stdin'
> docker exec -it -e PIPEWRIGHT_DB=/data/pipewright.db <container> /pipewright reset-admin-password --stdin
> ```
>
> The first error line prints the absolute path it actually resolved to plus the current directory, so you can confirm which database you would have changed; on success it echoes `数据库=<path>` (the database it wrote). To really cold-start from an empty database, add `--create-missing`.

**Database**

| Variable | Description | Default |
|---|---|---|
| `PIPEWRIGHT_DB_DRIVER` | Database driver: `sqlite` or `mysql` | `sqlite` |
| `PIPEWRIGHT_DB` | SQLite database path (when driver=sqlite). The default is relative to the current directory: whichever directory starts the service is the database it uses — do not start it from a subdirectory | `pipewright.db` |
| `PIPEWRIGHT_DB_DSN` | Connection string: required when driver=mysql (it carries the credentials), and for sqlite it can replace `PIPEWRIGHT_DB` with a file path | none |

**Runs & builds**

| Variable | Description | Default |
|---|---|---|
| `PIPEWRIGHT_RUNNER` | Run executor: default DAG (orchestrates stages/script/deploy_ssh/notify per the canvas); set `legacy` to fall back to the old fixed flow | `dag` |
| `PIPEWRIGHT_BUILDER` | `auto` uses a real container build when docker/nerdctl/podman is found and falls back to a stub otherwise; `real` refuses to start without a container CLI; `stub` never touches containers | `auto` |
| `PIPEWRIGHT_MAX_CONCURRENT` | Global cap on simultaneously running runs (excess stays queued, FIFO). Per-project caps are configured in the UI | worker count (4) |
| `PIPEWRIGHT_ARTIFACT_DIR` | Artifact store directory (real jar/dist bytes) | `<db dir>/artifacts` |
| `PIPEWRIGHT_REPO_CACHE_DIR` | Local bare-mirror repo cache directory | `<db dir>/repos` |
| `PIPEWRIGHT_NO_REPO_CACHE` | `1` disables the repo cache (every build clones over the network) | off |
| `PIPEWRIGHT_CACHE_DIR` | Build dependency cache directory (script nodes that configure `cachePaths`; archives **workspace-relative** directories keyed by branch + lockfile) | `<db dir>/cache` |
| `PIPEWRIGHT_NO_BUILD_CACHE` | `1` disables build dependency caching (always a cold build) | off |
| `PIPEWRIGHT_PROJECT_CACHE_DIR` | Per-project dependency cache root: one fixed directory per project, bind-mounted into build containers (Maven/npm/Go/pip/Gradle homes) with zero pipeline configuration; in container form this path must exist at the same absolute location on the host | process form: `<db dir>/project-cache`; container form: `<TMPDIR>/pipewright-project-cache` |
| `PIPEWRIGHT_NO_PROJECT_CACHE` | `1` disables the per-project dependency cache (dependencies re-downloaded every run) | off |
| `PIPEWRIGHT_NO_IMAGE_GC` | `1` keeps built images instead of garbage-collecting them | off |
| `PIPEWRIGHT_PAC_RUNTIME` | `1` forces pipeline-as-code on for **all** projects, ignoring the per-project toggle | off |
| `PIPEWRIGHT_CHAIN_MAX_DEPTH` | Max pipeline-chaining depth (hard stop against runaway chains) | `5` |

**Integrations**

| Variable | Description | Default |
|---|---|---|
| `PIPEWRIGHT_PR_STATUS` | `1` forces PR status reporting on for **all** projects, ignoring the per-project toggle | off |
| `PIPEWRIGHT_PR_STATUS_GITHUB_BASE` | GitHub API base URL (for GitHub Enterprise) | public GitHub |
| `PIPEWRIGHT_PR_STATUS_GITEE_BASE` | Gitee API base URL (for self-hosted Gitee) | public Gitee |
| `PIPEWRIGHT_CADDY_IMAGE` | Reverse-proxy image. The default is a self-built Caddy bundling the DNS-01, ratelimit, and layer4 plugins; stock `caddy:2` works but loses DNS-01/wildcard/TCP support | `ghcr.io/fuuhoo/pipewright-caddy:latest` |
| `PIPEWRIGHT_PREVIEW_SWEEP_INTERVAL` | How often to check whether preview environments can be reclaimed (Go duration, e.g. `10m`) | `5m` |

**Ops monitoring**

| Variable | Description | Default |
|---|---|---|
| `PIPEWRIGHT_ANOMALY_INTERVAL` | Anomaly detection interval in seconds; `0` disables the timer (manual checks still work) | `60` |
| `PIPEWRIGHT_ANOMALY_COOLDOWN` | Minimum seconds between repeat alerts for the same server×rule | `600` |
| `PIPEWRIGHT_METRICS_SAMPLE_INTERVAL` | Server metrics sampling interval in seconds (source of the trend charts); `0` disables sampling | `60` |
| `PIPEWRIGHT_METRICS_RETENTION_DAYS` | How many days of metric samples to keep | `7` |

## Delivery forms: which deploy node to reach for

Every deploy node runs over SSH with **zero agents on the target**; commands are arrayified (never interpolated into a shell string), and artifacts and file bodies land as files only.

| Node | What it delivers | Key config | On failure |
|---|---|---|---|
| `deploy_ssh` | File artifacts (jar / dist) laid straight into the directory you name | `serverIds` + `deployPath` (+ `restartCommand`) | Overwrites in place, no auto-rollback (re-run the previous deploy to go back) |
| `deploy_docker` · `run` | One container: pull the upstream image, stop old / start new | `containerName` / `ports` / `runArgs` | Automatically rolls back to the previous image |
| `deploy_docker` · `compose` | A whole stack: one `docker-compose.yml` handed to the target's compose CLI | `stackName` + compose body source (**takes no artifact**) | That host is marked failed, the others continue; retry only the failed host |
| `deploy_k8s` | An image or a manifest in a cluster | `clusterId` + `manifestSource` | A stalled rollout backfills the previous image by default |

### Whole-stack compose deploys (`deploy_docker` with `dockerMode: compose`)

Install docker plus a compose CLI on the target and you can deliver a set of services in one step.

- **Two ways to supply the body**: `composeSource: repo` + `composeFile: deploy/docker-compose.yml` (read the file from your project's repo, so it evolves with the code through PR review and per branch), or `composeSource: paste` + `composeYaml` (keep the body in the node). Body capped at 512 KiB, `stackName` at 128 characters.
- **It is the same stack the Containers page manages**: the body is uploaded verbatim as `/opt/pipewright/stacks/<stackName>/docker-compose.yml` on the target, then `docker compose -p <stackName> up -d` runs. A stack released by a pipeline therefore shows up in that host's Stacks list on the Containers page, where you can keep reading its logs, restart it, or `down` it — not two deploys that cannot see each other.
- **8-minute budget** for the whole chain (mkdir + upload + `up`, since `up` pulls images on the target). This mode **ignores `strategy`**: batching is about rolling out to many hosts, and a single stack has nothing to batch; multiple hosts are still handled one after another.
- **How it divides work with `run`**: only the image changes and the topology stays put → `run` (it reuses the image-artifact path, complete with stop-old/start-new and rollback on failure); services, networks, or volumes change together → `compose`.

Health gating is optional for both `deploy_ssh` and `deploy_docker`: `healthProbe: http` + `healthUrl` (the URL is from the **deploy host's own point of view**, e.g. `http://localhost:8080/healthz`), or `healthProbe: command` + `healthCommand`. A failing probe marks that deploy task failed and blocks downstream stages.

```yaml
version: 1
stages:
  - id: stg_src
    name: Source
    kind: source
    jobs:
      - name: Repo source
        type: git_source
  - id: stg_deploy
    name: Deploy
    kind: deploy
    needs: [stg_src]
    jobs:
      - name: Compose stack
        type: deploy_docker
        config:
          serverIds: "1"
          dockerMode: compose
          composeSource: repo
          composeFile: deploy/docker-compose.yml
          stackName: shop-web
          healthProbe: http
          healthUrl: http://localhost:8080/healthz
```

## Pipeline as code (GitOps)

Commit your pipeline structure to `.pipewright.yml` in the repo — **same source of truth as your code, reviewable in a PR, evolving per branch** — instead of relying on implicit drift in the canvas.

- **Enable**: flip the "Pipeline as code" toggle on the project's pipeline page (per project).
- **How it works**: once enabled, every run reads `.pipewright.yml` from the **repo root** on the **branch being built** (falling back to the project default branch when the branch is empty), and the pipeline spec in that file drives the run. Different branches can carry different `.pipewright.yml`. The file is fetched with the project's bound repo credential (ephemeral; no new exposure).
- **Never breaks a run**: if the file is **missing** → falls back to the pipeline configured in the canvas (UI); if it exists but is **invalid YAML** → also falls back to the stored canvas config.
- **Scope**: the YAML controls **pipeline structure only** (stages / jobs / `needs` / DAG layout). **Variables & cache, environments & credentials, and trigger rules** still come from the canvas (UI) settings — they are **not** in the YAML.
- **Schema** is the same one used by the platform's "Import from YAML" (`version` + `stages` → `jobs`; a job uses a nested `script:` block for `image`/`commands`/`env`/`workdir`).
- **Job types** available in both the canvas and the YAML: `git_source`, `script`, `build_backend`, `build_frontend`, `build_image`, `push_image`, `deploy_ssh`, `deploy_docker`, `deploy_k8s`, `deploy_frontend`, `notify`, `templated`, `custom`.

```yaml
version: 1
stages:
  - id: stg_src             # needs references stages by id, so cross-stage deps need an explicit id
    name: Source
    kind: source
    jobs:
      - name: Git source
        type: git_source
  - id: stg_build
    name: Build
    kind: build
    needs: [stg_src]
    jobs:
      - name: Run tests
        type: script
        script:
          image: golang:1.23
          commands:
            - go vet ./...
            - go test ./...
          env:
            CGO_ENABLED: "0"
          workdir: src/app
  - id: stg_deploy
    name: Deploy
    kind: deploy
    needs: [stg_build]
    gate: true              # manual approval gate
    when:
      branches: [main, release/*]
    jobs:
      - name: SSH deploy
        type: deploy_ssh
        config:
          targetEnv: prod
```

> You can also force pipeline-as-code on for **all projects** (ignoring the per-project toggle) via the global env var `PIPEWRIGHT_PAC_RUNTIME=1`, for back-compat / power users.

## Tech Stack

- **Backend**: Go · Chi (routing) · modernc/sqlite (pure Go, no CGO) + go-sql-driver/mysql · go-git (no host git dependency) · NaCl secretbox (vault) · argon2id + bcrypt · golang.org/x/crypto/ssh (agentless deployment) · coder/websocket (terminals) · Caddy (orchestrated on target hosts for auto HTTPS)
- **Frontend**: Vue 3 `<script setup>` · Vite · naive-ui · OKLCH dual theme · Monaco (read-only code browsing) · Vitest + Playwright · embedded into the binary via `go:embed`

Deliberately dependency-lean: the cron parser, DAG engine, DNS provider clients, artifact/build caches, and Caddyfile renderer are all in-tree rather than pulled in as libraries.

## Architecture

One process, ~45 domain packages. `internal/httpapi` is the only package that touches HTTP; every domain package is injected into it, and `cmd/pipewright/main.go` is the single wiring layer (including the adapters that keep otherwise-circular packages pointing one way).

```
single static binary (cmd/pipewright)
│
├─ foundation
│  ├── internal/config        env config + master key loading
│  ├── internal/store         DB open + migrations (sqlite / mysql dialects)
│  ├── internal/auth          auth + sessions + CSRF
│  ├── internal/vault         encrypted credential vault (secretbox)
│  ├── internal/oauth         OAuth app onboarding → token stored as a vault credential
│  ├── internal/audit         append-only audit + optional remote sink
│  ├── internal/mask          secret redaction (logs / diagnostics / notifications)
│  ├── internal/i18n          server-side localization of user-facing messages
│  └── internal/version       release check + one-click self-update
│
├─ pipelines
│  ├── internal/project       project onboarding + repo detection
│  ├── internal/pipeline      pipeline spec + build/deploy config + validation
│  ├── internal/pipelineyaml  YAML ↔ spec round-trip (import / export)
│  ├── internal/pacloader     pipeline-as-code loader (.pipewright.yml, per branch)
│  ├── internal/library       reusable templates + variable groups + custom nodes
│  ├── internal/trigger       webhook + branch→environment mapping
│  ├── internal/cron          5-field cron parser + minute-granularity scheduler
│  └── internal/chain         upstream→downstream chaining (loop-safe)
│
├─ execution
│  ├── internal/run           run model + worker pool + logs + artifacts + parameters
│  ├── internal/dag           pure DAG scheduling kernel (no I/O)
│  ├── internal/dagrun        DAG run orchestration (stage + job level, matrix expansion)
│  ├── internal/build         isolated container builds + images + stage executor
│  ├── internal/runner        per-project remote build machine config
│  ├── internal/repocache     local bare-mirror repo cache (incremental fetch)
│  ├── internal/buildcache    build dependency cache (branch + lockfile keyed)
│  ├── internal/projectcache  per-project dependency cache (fixed dir bind-mounted into build containers)
│  ├── internal/artifactstore content-addressed artifact bytes
│  ├── internal/testreport    JUnit + Cobertura parsing
│  ├── internal/qualitygate   pure quality-gate evaluation
│  ├── internal/approval      manual approval gates (coordinator + persistence)
│  └── internal/retention     run data retention sweeper
│
├─ delivery
│  ├── internal/target        generic SSH exec/session layer (shared by deploy + ops)
│  ├── internal/deploy        SSH deploy execution + health gating + rollback
│  ├── internal/environments  environments as first-class objects + rollback targets
│  ├── internal/promotion     environment promotion chains + per-env vars
│  ├── internal/prstatus      PR / commit status reporting (GitHub / Gitee)
│  ├── internal/proxy         auto HTTPS + domain reverse proxy (Caddy orchestration)
│  ├── internal/dnsprovider   Cloudflare / DNSPod / Alibaba DNS (DNS-01 + subdomains)
│  └── internal/previewenv    per-PR preview environments + auto reclamation
│
├─ observability
│  ├── internal/notify        multi-channel notifications + event routing + templates
│  ├── internal/anomaly       configurable threshold detection + alerts
│  ├── internal/metrics       server metric time series (trend charts)
│  ├── internal/dora          DORA metrics + performance bands
│  └── internal/ai            optional AI: diagnosis, repo analysis, run diff, risk
│
├── internal/httpapi          the sole outward HTTP surface (~188 routes)
└── web/                      Vue 3 frontend (embedded via go:embed)
```

## Project Status

✅ **Officially released** and under active iteration — see the latest version in [Releases](https://github.com/fuuhoo/pipewright/releases) (tag-driven releases: 6-platform binaries + multi-arch images on both ghcr and Aliyun ACR). Already running in real production, carrying builds, deployments, and daily ops for multiple projects.

## Contributing

PRs and issues welcome! Before you start, please read [CONTRIBUTING.md](CONTRIBUTING.md) (environment setup / testing / commit conventions) and follow the [Code of Conduct](CODE_OF_CONDUCT.md). For security vulnerabilities, please use the private channel described in [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).

---

<div align="center">
<sub>Pipewright — CI, deployment, and ops in a single binary.</sub>
</div>
