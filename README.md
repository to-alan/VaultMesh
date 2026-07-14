# VaultMesh

VaultMesh 是一个面向 Linux VPS、Homelab 和小型技术团队的自托管多服务器备份控制平台。控制面统一管理服务器、Restic 仓库、备份项目、计划和运行状态；Agent 在源服务器本地持久化配置、按时执行，并把加密后的备份数据直接写入所选存储后端。

> 当前状态：早期可运行纵向版本。请先在测试数据和独立 Bucket/Prefix 上验证，不要直接替换现有生产备份方案。

## 已实现

- Go Control Plane 和 Go Agent；
- PostgreSQL 元数据存储，以及无需数据库的内存开发模式；
- 一次性 Agent 注册令牌和独立设备凭据；
- Repository 与数据库密码的 AES-256-GCM 信封加密基础；
- 全局备份仓库渠道，不与服务器绑定；下发时按服务器 ID 隔离 Restic 路径；
- Restic 原生 Local、SFTP、REST、S3、Swift、B2、Azure Blob、GCS 后端，常用 S3 厂商预设，以及 rclone 扩展；
- Agent 配置版本、本地持久化、重启恢复和离线继续调度；
- 5 段 Cron、IANA 时区、随机抖动和仓库级串行执行；
- 带租约和幂等键的“立即备份”命令；
- 文件、Docker 挂载卷、MySQL 逻辑导出、PostgreSQL 逻辑导出；
- Restic 仓库自动初始化、JSON 结果解析和退出码判定；
- `succeeded`、`partial`、`failed`、`timed_out`、`unknown` 等运行状态；
- 运行 Outbox：中心离线时本地保存，恢复后幂等上报；
- Vue 3 + TypeScript 管理界面，支持 Cloudflare R2 引导，以及在一个项目中组合文件、Docker、MySQL 与 PostgreSQL 数据源；
- 个人中心、持久化管理员密码、TOTP 二步验证、一次性恢复码与 WebAuthn 通行密钥；
- 按服务器分组的备份项目视图，每台 Agent 的任务、数据源和下次执行时间独立呈现；
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
                                         VaultMesh Agent → Restic → Local / SFTP / REST / Object Storage / rclone
                                               │
                                               ├─ files
                                               ├─ docker inspect + mounts
                                               ├─ mysqldump
                                               └─ pg_dump
```

Web 与 Control Plane 不共享进程、镜像或静态目录。Web 容器通过 `VAULTMESH_API_BASE_URL` 在启动时生成运行时配置；API 只接受 `VAULTMESH_ALLOWED_ORIGINS` 中精确列出的浏览器来源。两者可以使用同一站点下的不同域名（例如 `vaultmesh.example.com` 与 `api.example.com`），并独立扩缩容和发布。

备份仓库在控制面中是独立的全局存储渠道。项目分别选择执行服务器和存储渠道；控制面在下发时把渠道基础路径扩展为 `/<server-id>`，避免不同服务器写入同一个 Restic 仓库路径。

仓库类型、认证字段、Agent 前置条件及其开源依据见 [存储仓库支持矩阵](./docs/STORAGE_PROVIDERS.md)。类型模型以 Restic 官方后端为准，1Panel 与 Kopia 用于产品字段和分类的交叉验证。

备份正文不经过控制面。中心暂时不可用时，Agent 继续执行最后一份已应用配置，并在连接恢复后补报结果。

## 一键部署

适用于已安装 Git、OpenSSL、Docker Engine 和 Docker Compose v2 的 Linux 主机：

```bash
curl -fsSL https://raw.githubusercontent.com/to-alan/VaultMesh/main/install.sh | sudo sh
```

脚本会把公开仓库安装到 `/opt/vaultmesh`，首次运行生成主密钥、PostgreSQL 密码和管理员密码，以权限 `0600` 写入 `/opt/vaultmesh/.env`，随后构建并启动 Control Plane、Web 和 PostgreSQL。重复执行会更新 Git 仓库、保留现有 `.env` 和数据库卷，并重新构建服务；旧版 Token 配置会自动补齐新的账号密码字段，旧 Token 不再参与认证。

默认只监听服务器回环地址。若部署在远程 VPS，先从本机建立隧道：

```bash
ssh -L 3000:127.0.0.1:3000 -L 8080:127.0.0.1:8080 user@your-server
```

然后浏览器打开 `http://localhost:3000`，使用脚本输出的用户名和密码登录。生产环境应配置可信 HTTPS 反向代理，把 Web Origin 精确写入 `VAULTMESH_ALLOWED_ORIGINS`，把浏览器可访问的 API URL 写入 `VAULTMESH_PUBLIC_API_URL`，并设置 `VAULTMESH_COOKIE_SECURE=true`。

如果不希望直接执行远程脚本，可先下载审阅：

```bash
curl -fsSLo vaultmesh-install.sh https://raw.githubusercontent.com/to-alan/VaultMesh/main/install.sh
less vaultmesh-install.sh
sudo sh vaultmesh-install.sh
```

## 手动启动控制面

复制配置并填写 `VAULTMESH_MASTER_KEY`、`VAULTMESH_ADMIN_PASSWORD` 和 `POSTGRES_PASSWORD`；对应的生成命令已经写在 `.env.example` 中：

```bash
cp .env.example .env
$EDITOR .env
docker compose up -d --build
```

管理员首次启动时由环境变量引导创建，随后密码哈希和安全资料持久化到 PostgreSQL；再次修改 `.env` 不会覆盖数据库中的现有密码。登录后可从控制台左下角账户入口进入“个人中心”，在按需弹出的安全步骤中修改密码、启用 TOTP 二步验证、生成一次性恢复码并注册通行密钥，页面不会常驻展示敏感输入框。修改密码或停用二步验证会撤销全部会话；添加或删除通行密钥要求最近 10 分钟内完成过身份验证，超时后才会单独要求当前密码和二步验证码。浏览器使用不可由 JavaScript 读取的 HttpOnly 会话 Cookie，前端不保存管理员认证凭据；当前会话最长 12 小时，Control Plane 重启后需要重新登录。

通行密钥要求浏览器访问的 Origin 与 WebAuthn 配置严格匹配。本地默认值可直接使用；生产环境请配置：

```bash
VAULTMESH_WEBAUTHN_RP_ID=vaultmesh.example.com
VAULTMESH_WEBAUTHN_RP_ORIGINS=https://vaultmesh.example.com
VAULTMESH_WEBAUTHN_RP_NAME=VaultMesh
```

`RP_ID` 必须是域名格式，不能是 IP 地址，也不能包含协议或端口。本地 HTTP 通行密钥必须通过 `localhost` 访问；生产环境必须部署在 HTTPS 域名下。

## 本地开发

本地开发要求 Go 1.26.5 或更高的补丁版本以及 Node.js 24；低于 1.26.5 的 Go 标准库包含本项目调用路径可触达的已知安全问题。可不安装 PostgreSQL；省略 `VAULTMESH_DATABASE_URL` 时使用内存存储，进程重启后元数据会丢失。

```bash
export VAULTMESH_ADMIN_USERNAME="admin"
export VAULTMESH_ADMIN_PASSWORD="$(openssl rand -hex 16)"
export VAULTMESH_MASTER_KEY="$(openssl rand -base64 32)"
export VAULTMESH_ALLOWED_ORIGINS="http://localhost:5173"
export VAULTMESH_COOKIE_SECURE="false"
# 未显式设置时，WebAuthn RP ID/Origin 会从 ALLOWED_ORIGINS 推导。
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
- 使用 WebDAV、OneDrive、Google Drive、Dropbox 或通用 rclone 渠道时需要 rclone，并在每台相关 Agent 上配置同名 remote；
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

测试覆盖控制面纵向 API、管理员密码持久化与会话撤销、TOTP/一次性恢复码、全局仓库迁移、一次性 Agent 注册、Secret 加解密、Agent 状态恢复、幂等 Run、Restic 成功/部分成功解析、数据库暂存产物和 Docker 挂载清单脱敏。WebAuthn 的协议校验由 Go WebAuthn 库完成，真实设备注册仍应在目标 HTTPS 域名上做一次验收。

## 当前限制

- 管理端目前只有一个本地管理员账号，尚未实现多用户、RBAC、密码找回和登录审计；
- 管理会话保存在单个 Control Plane 进程内，进程重启后需要重新登录，尚不支持多副本共享会话；
- 应用内尚未实现登录限速；公网入口必须在反向代理或 WAF 对登录接口实施限速；
- 尚未实现快照浏览、保留/Prune、仓库 Check 和恢复 UI；
- 直接 S3 模式不提供不可变备份保证；
- Docker 挂载卷默认为崩溃一致性快照，不会自动停止容器；数据库容器应同时配置 MySQL/PostgreSQL 逻辑备份；
- Agent 当前本地状态使用受权限保护的原子 JSON 文件，后续规模化时可迁移到 SQLite；
- 尚未发布稳定版本和自动更新通道。

这些限制是显式的开发边界，不应在部署说明或产品页面中被描述为已经支持。

## 安全

请阅读 [SECURITY.md](./SECURITY.md)。不要把控制面直接以明文 HTTP 暴露到公网，不要在日志或 Issue 中提交管理员密码、Agent 设备凭据、Restic 密码、数据库密码或对象存储密钥。

## API

创建仓库渠道以及文件、Docker、MySQL 和 PostgreSQL 项目的请求示例见 [API quick reference](./docs/API.md)。
