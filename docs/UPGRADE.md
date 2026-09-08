# 升级、失败恢复与旧版迁移

本文命令均在 **root 终端**执行，不需要 sudo。用 `id -u` 确认输出 `0`；普通用户先取得管理员权限，参见[安装身份说明](INSTALL.md#先确认执行身份root-不需要-sudo)。root 用户遇到 `sudo: command not found` 时，去掉命令中的 sudo 即可。

## 发布包用户：正常升级

推送 main 不会更新你的安装。只有维护者发布正式 Release 后，你手动执行升级才会更新；`latest` 仅用于发现新版本，实际运行固定版本。RC 需显式指定，不自动跟随。

1. 阅读目标 Release 的变更、兼容性和数据库迁移说明。
2. 确认磁盘能同时放下旧镜像、新镜像和完整 PostgreSQL dump；暂停涉及重要业务的操作。
3. 先升级控制面，再逐台升级 Agent；保留原有备份方案，升级后做恢复验收。

```bash
vaultmesh status
vaultmesh upgrade                  # 最新正式版
# 或明确指定真实版本：
vaultmesh upgrade --version vX.Y.Z
```

脚本执行：校验部署包 → 拉取 control/web/gateway 镜像 → 停止 control → 备份数据库、`.env` 和原部署文件并检查 dump 清单 → 启动新应用并等待健康检查 → 切换 `current`。

升级不更换 PostgreSQL 容器，不清理任何数据卷，不覆盖 `.env`，不自动清理历史镜像或备份，不在服务器编译。主密钥、项目和密码都保留。控制面会话不跨进程保存，重启后通常需要重新登录。

`vaultmesh backup` 可以单独创建一致性 PostgreSQL dump 和配置副本，但本机备份不能防止磁盘损坏/主机丢失，必须再加密复制到异机。手工改配置时不要与备份/升级并发。

### 从完整 RC1 安装升级到 RC2

仅适用于已经安装成功、`vaultmesh status` 正常的发布包安装。若 RC1 因 443 冲突中断，先完成[保留数据恢复](#rc1-443-recovery)，不要直接重装 RC2。

```bash
vaultmesh status
vaultmesh upgrade --version v0.1.2-rc.2
```

本次没有新增数据库迁移，不更换 Agent systemd unit。升级保留原代理模式和端口：原来使用 managed 的安装仍使用 80/443，不会静默切换。要交给自己的 Nginx，升级成功后按[反代指南](INSTALL.md#自己配置-nginx--1panel-反向代理)使用 `configure-proxy`，并协调 Nginx 启动和端口切换。Agent 随后按下节升级到同一 `v0.1.2-rc.2`。

### Agent 升级

从控制面对应 Release 下载安装器，在每台 Agent 主机运行：

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/vX.Y.Z/install.sh -o vaultmesh-install.sh &&
sh vaultmesh-install.sh upgrade-agent --version vX.Y.Z
systemctl status vaultmesh-agent
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

### 新版 configure-proxy 失败

仅适用于部署包带 `INSTALLER_API=2` 的版本。这个命令不升级应用或数据库：参数、端口、Compose 校验失败时不改原 `.env`；应用重新启动/健康检查失败时，新入口配置可能已经生效，原文件保存在输出的 `backups/proxy-时间.随机值/.env`。修复端口或代理后可以重新运行 `configure-proxy`。

若需恢复原入口，先确认旧端口可用，在 root 终端将**这次操作前**的 `.env` 备份恢复到安装目录（保持 600 权限），再执行 `vaultmesh compose up -d --no-deps --no-build --pull never --wait control web gateway`，检查原入口。不会自动停止占用旧端口的其他服务，也不要套用数据库清库恢复步骤。

### 仅下载阶段失败后的重试

确认目标版本**从未启动**、当前旧服务正常后，把提示的具体目标目录移到一个未使用的名字（例如 `releases/vX.Y.Z.failed-YYYYMMDD`），不要删除，然后重新执行 upgrade。不要移动 `current` 指向的目录。若新版本已经启动过，不能仅靠这一操作继续；先判断数据库兼容性或做恢复。

### 恢复升级前的控制面（会丢失备份之后的控制面写入）

这是管理员显式执行的灾难恢复，不是普通升级。先将故障数据库也单独保存；确认选择的是**本次升级前的完整备份**，且备份包含匹配主密钥。版本不兼容时，不要直接让旧镜像连接已迁移的数据库。

以下在控制面 root Shell 内执行；替换 `BACKUP_PATH`，不要原样使用占位符。自定义安装路径也要替换：

```bash
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

<a id="rc1-443-recovery"></a>

### RC1 仅因 443 冲突中断：保留数据切换 external

适用范围：原安装目录是 `/opt/vaultmesh`，版本为 `v0.1.2-rc.1`，`current` 尚未建立，原模式是 managed，PostgreSQL/Web/Control 均已健康，只有 gateway 因 80/443 被占用而失败。**以下不会重新安装、生成新密码、删卷或停止已有 Nginx，只重建 VaultMesh 的 gateway。** 其他失败原因或自定义目录不要照搬。

先在 root 终端检查端口。旧 RC1 的 external 端口固定为 3000；若它也被占用，请停止在这里，不要停其他业务抢端口，不要给旧安装器传尚不支持的 `--port`：

```bash
ss -ltnp '( sport = :80 or sport = :443 or sport = :3000 )'
docker ps --format 'table {{.Names}}\t{{.Ports}}'
```

确认符合上述范围、3000 空闲后执行。整个小括号块会在检查失败时退出；原 `.env` 会先另存备份，不会修改公开 HTTPS 域名、数据库密码或主密钥：

```bash
(
set -eu
umask 077
cd /opt/vaultmesh
test -f .vaultmesh-installation
test ! -d .git
test ! -e current
test ! -L current
test "$(cat releases/v0.1.2-rc.1/VERSION)" = v0.1.2-rc.1
grep -qx 'VAULTMESH_PROXY_MODE=managed' .env
listeners=$(ss -H -ltn 'sport = :3000')
test -z "$listeners"
published=$(docker ps --format '{{.Ports}}')
if printf '%s\n' "$published" | grep -q ':3000->'; then
    printf '3000 已被 Docker 占用，停止恢复。\n' >&2
    exit 1
fi
export VAULTMESH_RELEASE_DIR=/opt/vaultmesh/releases/v0.1.2-rc.1
export VAULTMESH_IMAGE_TAG=v0.1.2-rc.1
rc_compose() {
    docker compose -p vaultmesh --project-directory /opt/vaultmesh --env-file /opt/vaultmesh/.env \
        -f "$VAULTMESH_RELEASE_DIR/compose.yaml" -f "$VAULTMESH_RELEASE_DIR/compose.external.yaml" "$@" </dev/null
}
for service in postgres control web; do
    container=$(rc_compose ps -q "$service")
    test -n "$container"
    test "$(docker inspect --format '{{.State.Health.Status}}' "$container")" = healthy
done
mkdir -p backups
rc_backup=$(mktemp -d /opt/vaultmesh/backups/rc1-proxy.XXXXXX)
cp -p .env "$rc_backup/.env"
printf '原配置备份：%s/.env\n' "$rc_backup"
rc_stage=$(mktemp /opt/vaultmesh/.rc1-proxy-env.XXXXXX)
awk '!/^(VAULTMESH_PROXY_MODE|VAULTMESH_SITE_ADDRESS)=/' .env > "$rc_stage"
printf 'VAULTMESH_PROXY_MODE=external\nVAULTMESH_SITE_ADDRESS=http://:80\n' >> "$rc_stage"
chmod 600 "$rc_stage"
mv -f "$rc_stage" .env
rc_compose config --quiet
rc_compose up -d --no-deps --no-build --pull never --wait --wait-timeout 180 gateway
curl -fSs --retry 5 --retry-connrefused --connect-timeout 5 --max-time 10 http://127.0.0.1:3000/healthz
ln -sT releases/v0.1.2-rc.1 current
if [ ! -e /usr/local/bin/vaultmesh ] && [ ! -L /usr/local/bin/vaultmesh ]; then
    ln -sT /opt/vaultmesh/current/install.sh /usr/local/bin/vaultmesh
fi
printf '\n本地 HTTP 入口已就绪：http://127.0.0.1:3000；请继续配置 Nginx，尚未验证公网 HTTPS。\n'
)
```

随后让已有 Nginx 的对应 HTTPS 站点把所有路径反代到 `http://127.0.0.1:3000`，公开域名必须与原安装时的域名一致。按[反代指南](INSTALL.md#自己配置-nginx--1panel-反向代理)设置并验收，但**RC1 不运行新版 `configure-proxy`**。初始管理员密码仍保存在原 `.env` 的 `VAULTMESH_ADMIN_PASSWORD` 中，只在服务器本机查看，不要发到聊天或工单。

如果中途失败，保留打印出的 `.env` 备份和当前数据卷；不要重复整段恢复或 install。查看 gateway 日志，按失败阶段继续处理；需要回到 managed 时必须先确认 80/443 可用，再恢复原 `.env`。不要因为网关错误去清理 PostgreSQL。

### 其他首次安装中断

确认发布包完整、配置正确后，在 root Shell 中从该目录显式恢复启动，例如：

```bash
cd /opt/vaultmesh
# 设置实际版本和已选代理模式；不能把 managed 改成 external 而不检查配置。
RECOVERY_VERSION=vX.Y.Z
RECOVERY_MODE=external
export VAULTMESH_RELEASE_DIR="/opt/vaultmesh/releases/$RECOVERY_VERSION"
export VAULTMESH_IMAGE_TAG="$RECOVERY_VERSION"
docker compose -p vaultmesh --project-directory /opt/vaultmesh --env-file .env \
  -f "$VAULTMESH_RELEASE_DIR/compose.yaml" -f "$VAULTMESH_RELEASE_DIR/compose.$RECOVERY_MODE.yaml" \
  up -d --no-build --pull missing --wait --wait-timeout 180
```

验证健康端点及真实 HTTPS 入口后，在确认 `current` 不存在的情况下建立 `current → releases/实际版本` 链接。之后使用 `sh /opt/vaultmesh/current/install.sh status`；如 `/usr/local/bin/vaultmesh` 不存在，可手动创建到此脚本的符号链接。保留原始 `.env` 中的首次初始化密码，不能重新生成主密钥。

## 旧 Git / HTTP / IP 测试安装

新安装器会拒绝覆盖旧目录，不自动迁移 `.env`、COMPOSE_FILE、TLS 证书或数据卷。这样不会破坏已有 1Panel/IP 测试入口。现有实例可继续使用旧版运维手册的 Compose 命令，固定经 CI 验证的版本镜像；不要再重复执行旧版一键安装来“升级”。

推荐在新主机演练迁移：

1. 备份原 PostgreSQL、`.env`、自定义 Compose/TLS 配置，记录 Compose 项目名、卷名和镜像版本。
2. 选择与目标版本兼容的迁移路线；先恢复同版本数据，再逐版本升级，阅读每版迁移说明。
3. 准备新发布包布局与可信 HTTPS，保持原主密钥、数据库身份和 Agent 连接地址。新 PostgreSQL 数据卷初始化前放回匹配的密码；不得仅复制正在运行的 PGDATA。
4. 停止旧控制面，恢复数据库到新控制面，验证配置与历史；禁止两套控制面同时向同一批 Agent 提供服务。
5. 切换访问入口，验证服务器心跳、备份和恢复；确认成功后再处置旧实例，保留回滚资料。

当前不提供盲目“一键接管旧部署”。复杂自定义部署需按实际端口、网络、卷名和数据库版本制定一次性迁移方案。
