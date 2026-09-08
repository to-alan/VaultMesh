# VaultMesh 运维手册

本文面向使用 Docker Compose 部署控制面的管理员，覆盖备份、恢复、升级、回滚和常见故障。VaultMesh 当前仍处于 1.0 之前；生产部署必须保留现有备份方案并完成真实恢复验收。

新用户请从[发布包安装](INSTALL.md)开始，日常升级与失败恢复以 [UPGRADE.md](UPGRADE.md) 为准。本文直接在 `/opt/vaultmesh` 执行 `docker compose` 的手工命令针对旧版 Git/自定义部署（包含 IP 测试覆盖）；发布包布局应使用 `vaultmesh compose` 选择正确的当前版本配置，不能混用两套布局。本文保留旧版步骤是为避免覆盖既有环境，不再推荐重复运行旧安装器。

## 必须保护的资产

控制面能否完整恢复取决于两类资产，缺一不可：

1. PostgreSQL：服务器、项目、仓库、运行记录、审计事件、管理员安全资料和加密后的 Secret；
2. `/opt/vaultmesh/.env`：尤其是 `VAULTMESH_MASTER_KEY` 和 `POSTGRES_PASSWORD`。

丢失主密钥后，即使数据库仍在，仓库密码、对象存储凭据、数据库来源密码和管理员安全资料也无法解密。备份数据本身不经过控制面；Restic 仓库密码和存储凭据还应在独立的密码管理器中托管，以便控制面完全丢失时直接使用 Restic 恢复。

Agent 的 `/var/lib/vaultmesh-agent/state.json` 包含设备身份、最后有效配置、待上报事件和被控制面永久拒绝的隔离报告。可加密备份，但不得复制到另一台同时在线的主机，否则会克隆设备身份。恢复文件位于 `/var/lib/vaultmesh-agent/restores`，应按工单验收后清理。

Restic 缓存由 systemd 单元固定在 `/var/lib/vaultmesh-agent/cache`（单元以 `ProtectHome=read-only` 运行，Restic 默认的 `$HOME/.cache/restic` 不可写）。该缓存可以随时删除重建，但请保留在 StateDirectory 内；如需迁移到更大的磁盘，在 `/etc/vaultmesh-agent.env` 设置 `RESTIC_CACHE_DIR` 覆盖单元默认值。若缓存目录不可写，Restic 每次操作都要重新下载仓库元数据，会显著拖慢备份并放大网络流量。

## 备份项目探测与候选显示

项目页在宽屏采用左侧探测、右侧已保存项目的双栏布局，窄屏上下排列。选择服务器只读取上次结果（显示生成时间）；仅点击“开始探测 / 重新探测”才会下发只读任务。结果返回不会自动滚动页面或打开编辑器，必须勾选并生成草稿，检查后再保存。

候选分为四类，不预先勾选任何项：

| 分类 | 显示与选择规则 |
| --- | --- |
| 推荐候选 | 应用目录、有持久化挂载的业务容器，以及端口可达且 Agent 有导出工具的 MySQL / PostgreSQL；默认展开。 |
| 需确认 | 已有项目使用的路径或实例、嵌套目录、Compose 配置、入口/管理服务、有状态服务、带挂载的已停止容器；默认收起，可以按需选择。 |
| 暂不可用 | 数据库无主机端口、端口不可达或缺少导出工具；数据库容器的直接数据卷复制；没有识别到持久化数据的容器。保留原因，不生成自动草稿。 |
| 自动排除 | 已识别的 VaultMesh 自身组件及其项目目录、Agent 私有目录、不安全路径。默认收起，不纳入业务草稿；平台仍必须单独灾备。 |

“推荐”仅代表适合进一步配置，不保证备份成功或应用一致性。数据库探测只检查 TCP 连接，仍需填写正确的账号、密码和库名，并确认导出工具版本兼容。已有项目使用某路径不代表数据已经受保护：项目可能暂停、排除该路径或执行失败。Restic 缺失会单独提示；探测不安装依赖、不读取配置文件内容或数据库密码。

自身组件通过 `io.vaultmesh.component` 标签或官方镜像识别；旧版部署可通过已确认的 Compose 项目、工作目录和服务名识别同栈 PostgreSQL，不按容器名称包含 `vaultmesh` 等字符串粗略排除。报告仅增加可选的 `exclusion_reason` 字段，不传输 Docker 环境变量或标签全集。升级 Agent 后重新探测，才能补全旧报告中缺少的归属信息；使用私有重建镜像的部署应保留 Compose 标签。

升级顺序为控制面与 Web 在先、Agent 在后，以确保控制面能够接收新增的报告字段。没有归属标签的自定义镜像可能仍需人工判断；自动探测不是完整的资产清单，也不能替代恢复演练。

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

恢复数据库后，控制面的配置 Revision 会回退到备份时刻，Agent 会拒绝更低 Revision 的新配置并记录 `refusing configuration rollback`。此时使用一次性覆盖重启 Agent：

```bash
# 在 Agent 环境文件中临时加入，重启后生效
echo 'VAULTMESH_ACCEPT_ROLLBACK=true' | sudo tee -a /etc/vaultmesh-agent.env
sudo systemctl restart vaultmesh-agent
# 确认配置收敛后，从环境文件移除该变量并再次重启
```

该覆盖只允许下一次更低 Revision 的配置写入，成功后自动恢复严格模式；也可以用 `vaultmesh-agent --accept-rollback` 达到同样效果。恢复后还应检查每个项目的仓库凭据是否可以用当前 `VAULTMESH_MASTER_KEY` 解密：解密失败的项目不会阻塞整份配置下发，而是按项目降级，并触发一条服务器范围的 `config_error` 告警。

## 控制面数据保留

控制面按天清理已完成的事实：运行记录默认保留 90 天、已完成命令 30 天、已完成的投递记录 90 天、已恢复的告警事件 180 天、审计事件 365 天。可用 `VAULTMESH_RETENTION_*_DAYS` 环境变量调整，设置为 `0` 关闭对应范围的清理（完整变量列表见 `.env.example`）。进行中的运行、待投递的通知和 firing 中的告警事件不受保留策略影响。归档的服务器、项目和仓库不会参与保留清理。

## 升级

本节仅适用于旧 Git/自定义 Compose 布局；新发布包用户使用 `sudo vaultmesh upgrade`，详见 [UPGRADE.md](UPGRADE.md)。旧布局也必须先校验目标发布说明，不使用浮动 `latest` 或未通过 CI 的 edge。

升级前先执行控制面备份并阅读目标版本说明。标准流程：

```bash
cd /opt/vaultmesh
git status --short
git pull --ff-only
# 默认使用 GHCR 预构建镜像；先在 .env 中把 VAULTMESH_IMAGE_TAG 改为目标版本 tag
sudo docker compose up -d --pull missing
sudo docker compose ps
curl --fail http://127.0.0.1:8080/healthz
```

`VAULTMESH_IMAGE_TAG` 为 `latest` 时每次 `pull` 都可能拿到新镜像，生产环境应固定到明确的版本 tag。`up -d --build` 会在本机从源码重建镜像，用于Registry 不可达或需要运行未发布修改的场景；重建后把 `VAULTMESH_IMAGE_TAG` 指向的镜像与本机镜像区分清楚，避免下次 `pull` 又切回去。

主分支测试版本可固定为 `edge-<完整提交 SHA>`。先确认对应提交的 CI 和 Edge Images 均成功，再拉取 Control Plane、Web 与 Agent 的同一提交镜像；镜像的 `org.opencontainers.image.revision` 标签应与目标 SHA 一致。不要在工作流尚未完成时直接部署浮动 `edge` 标签。

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

还应在 UI 中检查：Agent 是否在线、是否存在活动的 Agent 离线 Incident、是否存在失败/部分成功/超时任务、下一次计划是否合理、快照索引是否近期同步、仓库 Check 是否按维护窗口完成、是否存在最终失败的通知投递、审计日志是否出现异常认证或失败操作，以及恢复目录是否积压。

Agent 主机可检查：

```bash
sudo systemctl status vaultmesh-agent
sudo journalctl -u vaultmesh-agent --since '30 minutes ago'
sudo test -s /var/lib/vaultmesh-agent/state.json
sudo find /var/lib/vaultmesh-agent/restores -mindepth 1 -maxdepth 1 -type d -print
```

## HTTPS 与 Agent 操作门控

### 仅使用 IP 的临时测试

不使用域名时，可使用 `deploy/compose.ip-test.yaml` 在 Web 端口（默认 3000）提供 IP HTTPS，并将 API 明文端口限制到宿主机回环。它需要 Docker Compose 2.24.4+。在 `/etc/vaultmesh/test-tls` 创建带实际 IP SAN 的自签名 `server.crt` 与 `server.key`，目录权限 `0700`、私钥权限 `0600`，再在 `.env` 中设置：

```dotenv
COMPOSE_FILE=compose.yaml:deploy/compose.ip-test.yaml
VAULTMESH_PUBLIC_API_URL=https://YOUR_SERVER_IP:3000
VAULTMESH_ALLOWED_ORIGINS=https://YOUR_SERVER_IP:3000
VAULTMESH_COOKIE_SECURE=true
VAULTMESH_WEBAUTHN_RP_ID=YOUR_SERVER_IP
VAULTMESH_WEBAUTHN_RP_ORIGINS=https://YOUR_SERVER_IP:3000
```

备份后执行 `docker compose config --quiet` 和 `docker compose up -d --no-build`。浏览器会提示证书不受信任；应通过独立可信渠道核对证书 SHA-256 指纹后，仅对该测试站点建立信任。自签名证书不等于受公共 CA 信任的生产 HTTPS，不应全局关闭证书验证。IP 地址下不要依赖通行密钥登录，使用密码或已配置的 TOTP。同机 Agent 可继续连接 `http://127.0.0.1:8080`，远程 Agent 必须先建立对测试证书的信任。正式上线时应切换到可信证书并移除测试覆盖配置。

### 正式环境

Control Plane 只有在 `VAULTMESH_PUBLIC_API_URL` 使用 `https://`，或显式设置 `VAULTMESH_HTTPS_ENABLED=true` 时，才允许管理员下发探测、备份、保留预览、快照同步、保护、浏览和恢复等 Agent 任务。未完成 TLS 配置时，查询页面仍可使用，相关写操作返回 `403 https_required`。

`VAULTMESH_HTTPS_ENABLED=true` 只表示 TLS 已由可信反向代理终止，并不会自行启用 HTTPS。设置前应确认公网只能访问代理的 HTTPS 入口、Control Plane 明文端口不直接暴露，并把 `VAULTMESH_ALLOWED_ORIGINS`、Cookie 和 WebAuthn 配置同步到实际域名。

升级或维护 Agent 时使用 `systemctl stop/restart vaultmesh-agent`，不要直接发送 `SIGKILL`。收到 `SIGTERM` 后，Agent 会先停止接受新的计划和手动任务，取消正在运行的 Restic/数据库进程，等待终态写入本地 Outbox，再退出；被取消的任务会以 `canceled` 上报。systemd 单元提供 30 秒停止上限，正常命令应在此时间内响应上下文取消。若最终被强制终止，下次启动会把遗留的 `running` 记录恢复为 `unknown`，避免把未确认完成的备份误报为成功。

## 故障处理

### Agent 离线

1. 确认系统时间和 DNS 正常；
2. 验证 Agent 能以 HTTPS 访问 Control Plane；
3. 检查 systemd 日志和状态文件权限；
4. 不要直接删除 `state.json`，否则会丢失设备凭据和未上报 Outbox；
5. 若凭据确实丢失，创建新的服务器注册记录并重新注册，不要复用其他主机状态文件。

已注册 Agent 连续 90 秒无心跳后，控制面会创建单个服务器级 `agent_offline` Incident；心跳恢复时自动关闭，并按渠道配置选择是否发送恢复通知。项目路由和服务器路由彼此独立，确认负责基础设施告警的渠道已订阅 Agent 离线事件并选择正确的服务器范围。

若日志出现 `run report permanently rejected and quarantined`，Agent 已将该记录从 Outbox 移到 `state.json` 的 `rejected_reports`，后续报告仍会继续上报。先复制状态文件留证，再检查其中受限的拒绝原因、Agent/Control Plane 版本、项目是否已删除以及系统时间。不要直接手改或清空状态文件；结构校验、快照清单超限或幂等冲突通常表示版本不兼容或实现缺陷，应在修复前保留该报告。启动时日志会报告现存隔离数量；最多保存最近 200 条。

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
