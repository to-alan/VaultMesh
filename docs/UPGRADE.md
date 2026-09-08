# 升级、失败恢复与旧版迁移

## 发布包用户：正常升级

推送 main 不会更新你的安装。只有维护者发布正式 Release 后，你手动执行升级才会更新；`latest` 仅用于发现新版本，实际运行固定版本。RC 需显式指定，不自动跟随。

1. 阅读目标 Release 的变更、兼容性和数据库迁移说明。
2. 确认磁盘能同时放下旧镜像、新镜像和完整 PostgreSQL dump；暂停涉及重要业务的操作。
3. 先升级控制面，再逐台升级 Agent；保留原有备份方案，升级后做恢复验收。

```bash
sudo vaultmesh status
sudo vaultmesh upgrade                  # 最新正式版
# 或明确指定真实版本：
sudo vaultmesh upgrade --version vX.Y.Z
```

脚本执行：校验部署包 → 拉取 control/web/gateway 镜像 → 停止 control → 备份数据库、`.env` 和原部署文件并检查 dump 清单 → 启动新应用并等待健康检查 → 切换 `current`。

升级不更换 PostgreSQL 容器，不清理任何数据卷，不覆盖 `.env`，不自动清理历史镜像或备份，不在服务器编译。主密钥、项目和密码都保留。控制面会话不跨进程保存，重启后通常需要重新登录。

`sudo vaultmesh backup` 可以单独创建一致性 PostgreSQL dump 和配置副本，但本机备份不能防止磁盘损坏/主机丢失，必须再加密复制到异机。手工改配置时不要与备份/升级并发。

### Agent 升级

从控制面对应 Release 下载安装器，在每台 Agent 主机运行：

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/vX.Y.Z/install.sh -o vaultmesh-install.sh
sudo sh vaultmesh-install.sh upgrade-agent --version vX.Y.Z
sudo systemctl status vaultmesh-agent
vaultmesh-agent --version
```

升级不需要注册令牌。保留 `.env`、定制 systemd unit、`state.json`、Outbox、缓存与恢复目录；在私有状态目录创建 `upgrade-backup.*`，停止服务后复制状态，再原子替换二进制。不会自动把新 unit 覆盖定制服务；如果某个版本要求 unit 变更，按该 Release 的迁移说明人工合并。

停 Agent 会取消当时活跃的任务，安排在维护窗口执行。完成后从控制台确认对应服务器仍在线、版本正确、重新探测工具可用，并做一次备份/恢复。不要为了升级运行 `install-agent`，也不要删除身份文件。

## 更新失败：先确定失败阶段

| 失败阶段 | 实际状态与处理 |
| --- | --- |
| 下载、校验、拉镜像失败 | 原控制面未停止；修复网络/空间。已下载的目标目录可能保留，不能直接覆盖。 |
| 停止后、启动新应用前备份失败 | 安装器尝试启动原控制面；确认 `status`，保留失败备份诊断。 |
| 新应用已开始启动/迁移后失败 | 安装器停止新 control，不自动降级数据库；`current` 仍指最后验收版本，但不代表旧服务已在运行。按下面恢复。 |
| Agent 替换或启动失败 | 身份和现场不删除；查看 systemd 日志、备份目录和二进制版本。不要以重新注册代替故障处理。 |

保留失败现场、日志、原版本、目标版本和备份路径。不要把含 `.env` / `state.json` 的资料上传 GitHub Issue。

### 仅下载阶段失败后的重试

确认目标版本**从未启动**、当前旧服务正常后，把提示的具体目标目录移到一个未使用的名字（例如 `releases/vX.Y.Z.failed-YYYYMMDD`），不要删除，然后重新执行 upgrade。不要移动 `current` 指向的目录。若新版本已经启动过，不能仅靠这一操作继续；先判断数据库兼容性或做恢复。

### 恢复升级前的控制面（会丢失备份之后的控制面写入）

这是管理员显式执行的灾难恢复，不是普通升级。先将故障数据库也单独保存；确认选择的是**本次升级前的完整备份**，且备份包含匹配主密钥。版本不兼容时，不要直接让旧镜像连接已迁移的数据库。

以下在控制面 root Shell 内执行；替换 `BACKUP_PATH`，不要原样使用占位符。自定义安装路径也要替换：

```bash
sudo -i
cd /opt/vaultmesh
BACKUP_PATH=/opt/vaultmesh/backups/REPLACE_WITH_EXACT_BACKUP
cd "$BACKUP_PATH"
sha256sum --check SHA256SUMS
cd /opt/vaultmesh
# 此示例针对失败升级、current 仍指原版本的情况；不匹配就停在此处。
test "$(cat current/VERSION)" = "$(cat "$BACKUP_PATH/release/VERSION")"
vaultmesh compose stop control
```

在继续前确认：上面的版本检查成功；PostgreSQL 容器当前密码与备份 `.env` 相匹配；没有第二个控制面；dump 清单可读。如果升级已完成、`current` 已切到新版本，应先制定恢复原部署文件和切回链接的方案，不能继续执行下面的清库命令。新主机应在初始化数据卷前放回旧 `.env`，已有不匹配数据卷不要强行替换密码文件。

```bash
# 以下明确删除并重建目标 vaultmesh 数据库；确认数据丢失范围后再执行。
vaultmesh compose exec -T postgres dropdb -U vaultmesh --if-exists vaultmesh
vaultmesh compose exec -T postgres createdb -U vaultmesh vaultmesh
# compose 管理命令默认关闭 stdin，恢复应显式挂载只读 dump：
PG_CONTAINER=$(vaultmesh compose ps -q postgres)
test -n "$PG_CONTAINER"
docker cp "$BACKUP_PATH/postgres.dump" "$PG_CONTAINER:/tmp/vaultmesh-recovery.dump"
docker exec "$PG_CONTAINER" pg_restore -U vaultmesh -d vaultmesh --exit-on-error --no-owner --no-privileges /tmp/vaultmesh-recovery.dump
docker exec "$PG_CONTAINER" rm /tmp/vaultmesh-recovery.dump
install -m 600 "$BACKUP_PATH/.env" /opt/vaultmesh/.env
vaultmesh compose up -d --no-build --pull never --wait control web gateway
vaultmesh status
```

数据库恢复可能导致配置 Revision 回退。按[运维手册](OPERATIONS.md#控制面恢复)在 Agent 上一次性允许回滚，确认收敛后移除放行变量。检查服务器、项目、历史记录、凭据解密和恢复链。恢复不会撤销 Agent 已写入 Restic 的快照或仓库操作。

Agent 若需恢复旧二进制，先确认新状态格式与旧版本兼容；停止服务后从明确的 `upgrade-backup.*` 恢复对应 binary。不要盲目覆盖当前 `state.json`：可能丢失新增的 Outbox 和任务事实；如必须恢复状态，应离线保存当前状态并人工处理差异。

## 首次安装中断

`.env`、目标版本目录和可能已初始化的 PostgreSQL 卷会保留，不自动回到全新安装状态。不要删除目录/卷重跑 install。先查看错误和容器状态，检查 DNS、证书、网络、磁盘与版本目录；记录 `releases/vX.Y.Z/VERSION`。

确认发布包完整、配置正确后，在 root Shell 中从该目录显式恢复启动，例如：

```bash
cd /opt/vaultmesh
# 设置实际版本和已选代理模式；不能把 managed 改成 external 而不检查配置。
RECOVERY_VERSION=vX.Y.Z
RECOVERY_MODE=managed
export VAULTMESH_RELEASE_DIR="/opt/vaultmesh/releases/$RECOVERY_VERSION"
export VAULTMESH_IMAGE_TAG="$RECOVERY_VERSION"
docker compose -p vaultmesh --project-directory /opt/vaultmesh --env-file .env \
  -f "$VAULTMESH_RELEASE_DIR/compose.yaml" -f "$VAULTMESH_RELEASE_DIR/compose.$RECOVERY_MODE.yaml" \
  up -d --no-build --pull missing --wait --wait-timeout 180
```

验证健康端点及真实 HTTPS 入口后，在确认 `current` 不存在的情况下建立 `current → releases/实际版本` 链接。之后使用 `sudo sh /opt/vaultmesh/current/install.sh status`；如 `/usr/local/bin/vaultmesh` 不存在，可手动创建到此脚本的符号链接。保留原始 `.env` 中的首次初始化密码，不能重新生成主密钥。

## 旧 Git / HTTP / IP 测试安装

新安装器会拒绝覆盖旧目录，不自动迁移 `.env`、COMPOSE_FILE、TLS 证书或数据卷。这样不会破坏已有 1Panel/IP 测试入口。现有实例可继续使用旧版运维手册的 Compose 命令，固定经 CI 验证的版本镜像；不要再重复执行旧版一键安装来“升级”。

推荐在新主机演练迁移：

1. 备份原 PostgreSQL、`.env`、自定义 Compose/TLS 配置，记录 Compose 项目名、卷名和镜像版本。
2. 选择与目标版本兼容的迁移路线；先恢复同版本数据，再逐版本升级，阅读每版迁移说明。
3. 准备新发布包布局与可信 HTTPS，保持原主密钥、数据库身份和 Agent 连接地址。新 PostgreSQL 数据卷初始化前放回匹配的密码；不得仅复制正在运行的 PGDATA。
4. 停止旧控制面，恢复数据库到新控制面，验证配置与历史；禁止两套控制面同时向同一批 Agent 提供服务。
5. 切换访问入口，验证服务器心跳、备份和恢复；确认成功后再处置旧实例，保留回滚资料。

当前不提供盲目“一键接管旧部署”。复杂自定义部署需按实际端口、网络、卷名和数据库版本制定一次性迁移方案。
