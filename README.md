# VaultMesh

VaultMesh 是一个面向 Linux VPS、Homelab 和小型技术团队的自托管多服务器备份控制平台。控制面统一管理服务器、Restic 仓库、备份项目、计划和运行状态；Agent 在源服务器本地持久化配置、按时执行，并把加密后的备份数据直接写入 S3 兼容存储。

> 当前状态：早期可运行纵向版本。请先在测试数据和独立 Bucket/Prefix 上验证，不要直接替换现有生产备份方案。

## 已实现

- Go Control Plane 和 Go Agent；
- PostgreSQL 元数据存储，以及无需数据库的内存开发模式；
- 一次性 Agent 注册令牌和独立设备凭据；
- Repository 与数据库密码的 AES-256-GCM 信封加密基础；
- 全局备份仓库渠道，不与服务器绑定；下发时按服务器 ID 隔离 Restic 路径；
- Agent 配置版本、本地持久化、重启恢复和离线继续调度；
- 5 段 Cron、IANA 时区、随机抖动和仓库级串行执行；
- 带租约和幂等键的“立即备份”命令；
- 文件、Docker 挂载卷、MySQL 逻辑导出、PostgreSQL 逻辑导出；
- Restic 仓库自动初始化、JSON 结果解析和退出码判定；
- `succeeded`、`partial`、`failed`、`timed_out`、`unknown` 等运行状态；
- 运行 Outbox：中心离线时本地保存，恢复后幂等上报；
- Vue 3 + TypeScript 管理界面，支持 Cloudflare R2 引导，以及在一个项目中组合文件、Docker、MySQL 与 PostgreSQL 数据源；
- 日/周可视化计划、5 段高级 Cron、IANA 时区与服务端计算的下次执行时间；
- API 与 Web 独立镜像、独立端口和独立发布，Web 通过运行时配置选择 API 地址；
- Docker Compose、systemd Unit、CI 和安全部署说明。

项目边界、完整 P0/P1 范围和技术风险见 [VaultMesh 项目说明书](./VaultMesh-项目说明书.md)。

## 架构

```text
Browser → VaultMesh Web (Nginx/static) → HTTPS API → Control Plane → PostgreSQL
                                               ↑
                                               │ HTTPS polling / run reports
                                               │
                                         VaultMesh Agent → Restic → S3 / R2 / MinIO
                                               │
                                               ├─ files
                                               ├─ docker inspect + mounts
                                               ├─ mysqldump
                                               └─ pg_dump
```

Web 与 Control Plane 不共享进程、镜像或静态目录。Web 容器通过 `VAULTMESH_API_BASE_URL` 在启动时生成运行时配置；API 只接受 `VAULTMESH_ALLOWED_ORIGINS` 中精确列出的浏览器来源。两者可以使用不同域名、独立扩缩容和独立发布。

备份仓库在控制面中是独立的全局存储渠道。项目分别选择执行服务器和存储渠道；控制面在下发时把渠道基础路径扩展为 `/<server-id>`，避免不同服务器写入同一个 Restic 仓库路径。

备份正文不经过控制面。中心暂时不可用时，Agent 继续执行最后一份已应用配置，并在连接恢复后补报结果。

## 快速启动控制面

需要 Docker Compose。先创建配置：

```bash
cp .env.example .env
```

分别生成并填写三个值：

```bash
openssl rand -base64 32
openssl rand -hex 32
openssl rand -hex 32
```

然后启动：

```bash
docker compose up -d --build
```

Compose 默认将 Web 监听在 `127.0.0.1:3000`，API 监听在 `127.0.0.1:8080`。生产环境应分别使用例如 `vaultmesh.example.com` 和 `api.vaultmesh.example.com` 的可信 HTTPS 入口，并把 Web Origin 精确写入 `VAULTMESH_ALLOWED_ORIGINS`。

浏览器打开 `http://127.0.0.1:3000`，输入 `.env` 中的 `VAULTMESH_ADMIN_TOKEN`。

## 本地开发

本地开发要求 Go 1.26.5 或更高的补丁版本以及 Node.js 24；低于 1.26.5 的 Go 标准库包含本项目调用路径可触达的已知安全问题。可不安装 PostgreSQL；省略 `VAULTMESH_DATABASE_URL` 时使用内存存储，进程重启后元数据会丢失。

```bash
export VAULTMESH_ADMIN_TOKEN="$(openssl rand -hex 32)"
export VAULTMESH_MASTER_KEY="$(openssl rand -base64 32)"
export VAULTMESH_ALLOWED_ORIGINS="http://127.0.0.1:5173"
make build
./bin/vaultmesh-server
```

前端热更新：

```bash
npm --prefix web ci
npm --prefix web run dev
```

## 添加 Cloudflare R2

1. 在 Cloudflare R2 创建 Bucket。
2. 在 R2 API Tokens 中创建限制到该 Bucket 的 `Object Read & Write` Token。
3. 保存 Account ID、Access Key ID 和只显示一次的 Secret Access Key。
4. 在 VaultMesh“备份仓库”中选择 Cloudflare R2，填写上述信息、Bucket 和可选目录前缀；Region 使用 `auto`。
5. 生成并单独保存 Restic 仓库密码。它与 R2 Secret Access Key 用途不同，丢失后无法解密快照。

R2 是 Cloudflare 的对象存储，不是 AWS S3；它提供 S3 兼容 API，因此 VaultMesh/Restic 可以通过标准 S3 Endpoint 与凭据访问。

## 安装 Agent

Agent 主机需要：

- Restic；
- MySQL 项目需要 `mysqldump`；
- PostgreSQL 项目需要 `pg_dump`；
- Docker 数据源需要 Docker CLI，并要求 Agent 有权访问 Docker daemon 和所选挂载路径；
- 可出站访问控制面的 HTTPS 443 和目标对象存储。

构建 Agent：

```bash
make build
sudo install -m 0755 bin/vaultmesh-agent /usr/local/bin/vaultmesh-agent
sudo install -m 0644 deploy/systemd/vaultmesh-agent.service /etc/systemd/system/vaultmesh-agent.service
sudo install -m 0600 deploy/systemd/vaultmesh-agent.env.example /etc/vaultmesh-agent.env
```

在 Web 控制台创建服务器，把一次性令牌填入 `/etc/vaultmesh-agent.env`，然后：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now vaultmesh-agent
sudo journalctl -u vaultmesh-agent -f
```

注册成功后，从环境文件中删除 `VAULTMESH_ENROLLMENT_TOKEN` 并重启服务。设备凭据保存在 `/var/lib/vaultmesh-agent/state.json`，权限为 `0600`。

## 验证

```bash
make check
```

测试覆盖控制面纵向 API、全局仓库迁移、一次性注册、Secret 加解密、Agent 状态恢复、幂等 Run、Restic 成功/部分成功解析、数据库暂存产物和 Docker 挂载清单脱敏。

## 当前限制

- 管理端使用单个高熵管理员 Token，尚未实现多用户登录和 RBAC；
- 尚未实现快照浏览、保留/Prune、仓库 Check 和恢复 UI；
- 直接 S3 模式不提供不可变备份保证；
- Docker 挂载卷默认为崩溃一致性快照，不会自动停止容器；数据库容器应同时配置 MySQL/PostgreSQL 逻辑备份；
- Agent 当前本地状态使用受权限保护的原子 JSON 文件，后续规模化时可迁移到 SQLite；
- 尚未发布稳定版本和自动更新通道。

这些限制是显式的开发边界，不应在部署说明或产品页面中被描述为已经支持。

## 安全

请阅读 [SECURITY.md](./SECURITY.md)。不要把控制面直接以明文 HTTP 暴露到公网，不要在日志或 Issue 中提交管理员 Token、Restic 密码、数据库密码或对象存储密钥。

## API

创建仓库渠道以及文件、Docker、MySQL 和 PostgreSQL 项目的请求示例见 [API quick reference](./docs/API.md)。
