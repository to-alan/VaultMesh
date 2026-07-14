# VaultMesh

VaultMesh 是一个面向 Linux VPS、Homelab 和小型技术团队的自托管多服务器备份控制平台。控制面统一管理服务器、Restic 仓库、备份项目、计划和运行状态；Agent 在源服务器本地持久化配置、按时执行，并把加密后的备份数据直接写入 S3 兼容存储。

> 当前状态：早期可运行纵向版本。请先在测试数据和独立 Bucket/Prefix 上验证，不要直接替换现有生产备份方案。

## 已实现

- Go Control Plane 和 Go Agent；
- PostgreSQL 元数据存储，以及无需数据库的内存开发模式；
- 一次性 Agent 注册令牌和独立设备凭据；
- Repository 与数据库密码的 AES-256-GCM 信封加密基础；
- Agent 配置版本、本地持久化、重启恢复和离线继续调度；
- 5 段 Cron、IANA 时区、随机抖动和仓库级串行执行；
- 带租约和幂等键的“立即备份”命令；
- 文件、MySQL 逻辑导出、PostgreSQL 逻辑导出；
- Restic 仓库自动初始化、JSON 结果解析和退出码判定；
- `succeeded`、`partial`、`failed`、`timed_out`、`unknown` 等运行状态；
- 运行 Outbox：中心离线时本地保存，恢复后幂等上报；
- Vue 3 + TypeScript 管理界面；
- Docker Compose、systemd Unit、CI 和安全部署说明。

项目边界、完整 P0/P1 范围和技术风险见 [VaultMesh 项目说明书](./VaultMesh-项目说明书.md)。

## 架构

```text
Browser → VaultMesh Control Plane → PostgreSQL
                     ↑
                     │ HTTPS polling / run reports
                     │
               VaultMesh Agent → Restic → S3 / R2 / MinIO
                     │
                     ├─ files
                     ├─ mysqldump
                     └─ pg_dump
```

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

Compose 默认只监听 `127.0.0.1:8080`。生产环境必须通过 Caddy、Nginx 或 Traefik 提供可信 HTTPS，再让远端 Agent 访问。

浏览器打开控制面后，输入 `.env` 中的 `VAULTMESH_ADMIN_TOKEN`。

## 本地开发

本地开发要求 Go 1.26.5 或更高的补丁版本以及 Node.js 24；低于 1.26.5 的 Go 标准库包含本项目调用路径可触达的已知安全问题。可不安装 PostgreSQL；省略 `VAULTMESH_DATABASE_URL` 时使用内存存储，进程重启后元数据会丢失。

```bash
export VAULTMESH_ADMIN_TOKEN="$(openssl rand -hex 32)"
export VAULTMESH_MASTER_KEY="$(openssl rand -base64 32)"
make build
./bin/vaultmesh-server
```

前端热更新：

```bash
npm --prefix web ci
npm --prefix web run dev
```

## 安装 Agent

Agent 主机需要：

- Restic；
- MySQL 项目需要 `mysqldump`；
- PostgreSQL 项目需要 `pg_dump`；
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

测试覆盖控制面纵向 API、一次性注册、Secret 加解密、Agent 状态恢复、幂等 Run、Restic 成功/部分成功解析和 MySQL 暂存产物清理。

## 当前限制

- 管理端使用单个高熵管理员 Token，尚未实现多用户登录和 RBAC；
- Web UI 当前创建文件来源；MySQL/PostgreSQL 来源可通过版本化 API 创建；
- 尚未实现快照浏览、保留/Prune、仓库 Check 和恢复 UI；
- 直接 S3 模式不提供不可变备份保证；
- Agent 当前本地状态使用受权限保护的原子 JSON 文件，后续规模化时可迁移到 SQLite；
- 尚未发布稳定版本和自动更新通道。

这些限制是显式的开发边界，不应在部署说明或产品页面中被描述为已经支持。

## 安全

请阅读 [SECURITY.md](./SECURITY.md)。不要把控制面直接以明文 HTTP 暴露到公网，不要在日志或 Issue 中提交管理员 Token、Restic 密码、数据库密码或对象存储密钥。

## API

创建文件、MySQL 和 PostgreSQL 项目的请求示例见 [API quick reference](./docs/API.md)。
