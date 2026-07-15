# VaultMesh 运维手册

本文面向使用 Docker Compose 部署控制面的管理员，覆盖备份、恢复、升级、回滚和常见故障。VaultMesh 当前仍处于 1.0 之前；生产部署必须保留现有备份方案并完成真实恢复验收。

## 必须保护的资产

控制面能否完整恢复取决于两类资产，缺一不可：

1. PostgreSQL：服务器、项目、仓库、运行记录、审计事件、管理员安全资料和加密后的 Secret；
2. `/opt/vaultmesh/.env`：尤其是 `VAULTMESH_MASTER_KEY` 和 `POSTGRES_PASSWORD`。

丢失主密钥后，即使数据库仍在，仓库密码、对象存储凭据、数据库来源密码和管理员安全资料也无法解密。备份数据本身不经过控制面；Restic 仓库密码和存储凭据还应在独立的密码管理器中托管，以便控制面完全丢失时直接使用 Restic 恢复。

Agent 的 `/var/lib/vaultmesh-agent/state.json` 包含设备身份、最后有效配置和待上报事件。可加密备份，但不得复制到另一台同时在线的主机，否则会克隆设备身份。恢复文件位于 `/var/lib/vaultmesh-agent/restores`，应按工单验收后清理。

## 控制面备份

以下示例在控制面主机上执行，并把结果写入仅 root 可读的目录：

```bash
sudo install -d -m 0700 /var/backups/vaultmesh
cd /opt/vaultmesh
stamp=$(date -u +%Y%m%dT%H%M%SZ)
sudo docker compose exec -T postgres \
  pg_dump -U vaultmesh -d vaultmesh --format=custom \
  | sudo tee "/var/backups/vaultmesh/postgres-${stamp}.dump" >/dev/null
sudo install -m 0600 .env "/var/backups/vaultmesh/env-${stamp}"
sudo sha256sum "/var/backups/vaultmesh/postgres-${stamp}.dump" \
  "/var/backups/vaultmesh/env-${stamp}" \
  | sudo tee "/var/backups/vaultmesh/SHA256SUMS-${stamp}" >/dev/null
```

把该目录再复制到控制面主机之外的加密存储，并制定独立保留策略。至少定期确认：dump 非空、校验和匹配、`.env` 权限为 `0600`，并在隔离环境做一次恢复演练。

## 控制面恢复

恢复会替换目标数据库。先停止 Control Plane，并确认选择了正确的备份和目标环境：

```bash
cd /opt/vaultmesh
sudo docker compose stop control
sudo install -m 0600 /var/backups/vaultmesh/env-YYYYMMDDTHHMMSSZ /opt/vaultmesh/.env
sudo docker compose up -d postgres
sudo docker compose exec -T postgres dropdb -U vaultmesh --if-exists vaultmesh
sudo docker compose exec -T postgres createdb -U vaultmesh vaultmesh
sudo cat /var/backups/vaultmesh/postgres-YYYYMMDDTHHMMSSZ.dump \
  | sudo docker compose exec -T postgres \
    pg_restore -U vaultmesh -d vaultmesh --no-owner --no-privileges
sudo docker compose up -d control web
```

新主机恢复必须在第一次创建 PostgreSQL 数据卷之前放回匹配的 `.env`。如果目标已有使用另一密码初始化的数据卷，不要只替换 `.env`；应使用干净数据卷恢复，或由 PostgreSQL 管理员同步更新 `vaultmesh` 角色密码。随后检查健康端点、登录、服务器/项目数量和一条历史运行记录。不要在原 Control Plane 仍在线时启动恢复副本；两个控制面共享 Agent 身份和项目配置会产生不确定行为。

## 升级

升级前先执行控制面备份并阅读目标版本说明。标准流程：

```bash
cd /opt/vaultmesh
git status --short
git pull --ff-only
sudo docker compose up -d --build
sudo docker compose ps
curl --fail http://127.0.0.1:8080/healthz
```

`git status --short` 必须为空；不要让一键更新覆盖本地修改。当前数据库迁移由 Control Plane 启动时自动执行。出现问题时先保存日志和数据库备份，再决定回滚。

## 回滚

应用镜像可以切回已验证的 Git 提交，但数据库迁移不保证向后兼容。安全回滚顺序是：停止 Control Plane、恢复升级前的 PostgreSQL dump 和匹配的 `.env`、切回原提交、重新构建并启动。不要只切换代码后继续使用已经升级的数据结构。

Agent 独立持有最后一份有效配置，控制面升级期间仍会按本地计划运行；运行结果会进入 Outbox，并在连接恢复后上报。

## 日常检查

```bash
cd /opt/vaultmesh
sudo docker compose ps
sudo docker compose logs --since=30m control
curl --fail http://127.0.0.1:8080/healthz
```

还应在 UI 中检查：Agent 是否在线、是否存在失败/部分成功/超时任务、下一次计划是否合理、快照索引是否近期同步、仓库 Check 是否按维护窗口完成、是否存在最终失败的通知投递、审计日志是否出现异常认证或失败操作，以及恢复目录是否积压。

Agent 主机可检查：

```bash
sudo systemctl status vaultmesh-agent
sudo journalctl -u vaultmesh-agent --since '30 minutes ago'
sudo test -s /var/lib/vaultmesh-agent/state.json
sudo find /var/lib/vaultmesh-agent/restores -mindepth 1 -maxdepth 1 -type d -print
```

## 故障处理

### Agent 离线

1. 确认系统时间和 DNS 正常；
2. 验证 Agent 能以 HTTPS 访问 Control Plane；
3. 检查 systemd 日志和状态文件权限；
4. 不要直接删除 `state.json`，否则会丢失设备凭据和未上报 Outbox；
5. 若凭据确实丢失，创建新的服务器注册记录并重新注册，不要复用其他主机状态文件。

### 备份失败

先区分来源准备、Restic、仓库锁、凭据、网络和空间错误。数据库任务还要确认 `mysqldump`/`pg_dump` 版本与服务端兼容。不要因一次备份失败立即执行 Forget 或 Prune；先保留运行记录并验证最近成功快照仍可读取。

### 仓库锁定

先确认没有同仓库的备份、恢复、Check、Forget 或 Prune 正在运行。只有在确认原进程已经终止后，才按 Restic 官方流程检查并清理 stale lock。不要把自动解锁作为常规重试步骤。

### 恢复任务

恢复始终写入新的 `<restore-root>/<command-id>` 并禁止覆盖。任务成功后在 Agent 上校验文件权限、哈希或应用级数据，再取回内容。VaultMesh 当前不自动清理恢复目录，也不自动回写生产路径。

### 通知发送失败

1. 在“通知与告警”确认渠道启用、事件类型和项目范围匹配，并先发送测试通知；
2. 确认 Control Plane 容器能解析并访问 SMTP 或目标 HTTPS 域名；通知由控制面发送，不经过 Agent；
3. 检查投递历史的状态、尝试次数和受限错误摘要。失败会持久化，并最多尝试五次，间隔依次为 1 分钟、5 分钟、15 分钟和 1 小时；
4. 目标端返回 2xx 才算成功。重定向不会被跟随，避免令牌随跳转泄漏；
5. 修改 Secret 时填写新值；普通编辑留空会保留旧值。API 和日志不会回显 Webhook URL、Token 或 SMTP 密码；
6. 自建 Gotify、ntfy、SMTP 或 Webhook 如果解析到回环/RFC1918 地址，需要在渠道中显式开启“允许访问私有网络地址”；链路本地和云元数据地址始终拒绝；
7. 最终失败后，修复配置并使用“发送测试”。现有投递不会自动重开，后续新事件或重复提醒将使用新配置。

生产防火墙应只允许 Control Plane 访问实际使用的通知域名和 SMTP 端口。通知客户端不读取环境代理，避免代理绕过目标地址校验；高安全环境应使用网络层 allowlist。只有可信管理员可以管理通知渠道。

## 上线前清单

- Web 和 API 均经可信 HTTPS 反向代理，API 不直接暴露明文端口；
- `VAULTMESH_ALLOWED_ORIGINS`、公开 API URL 和 WebAuthn RP 配置与实际域名完全一致；
- `VAULTMESH_COOKIE_SECURE=true`；`VAULTMESH_COOKIE_SAME_SITE` 与前端部署关系一致；登录接口在反向代理/WAF 有速率限制；
- `.env` 权限为 `0600`，主密钥、Restic 密码和对象存储凭据已异机托管；
- 存储凭据限制到专用 Bucket/Prefix；生产环境评估版本控制、Object Lock 或独立删除身份；
- 每个数据库来源已完成一次从逻辑导出到新实例的恢复验证；
- 已从真实 Restic 快照完成一次隔离恢复，而不只检查“任务成功”；
- PostgreSQL 与 `.env` 的备份、恢复和保留策略已经演练；
- 已知限制已被接受，现有生产备份方案尚未被提前移除。
