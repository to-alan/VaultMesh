# 安装 VaultMesh

普通用户使用 GitHub Release 的预构建镜像和 Agent 二进制，不需要 Git、Go、Node.js，也不在服务器上编译。控制面管理配置，Agent 安装在每台要备份的 Linux 服务器上；备份正文由 Agent 直接写入你的存储。

> 新发布包流程从包含 `install.sh` 和 `vaultmesh-deploy-vX.Y.Z.tar.gz` 的版本开始。旧的 v0.1.0 / v0.1.1 没有这些附件。若 Releases 尚无新包，请等待维护者发布；不要用旧包或 main 源码混搭安装。项目仍处于 1.0 之前，请保留原有备份并先完成恢复演练。

## 1. 准备服务器

- 控制面：Linux amd64 或 arm64；建议 2 核、2 GiB 内存，至少 5 GiB 可用磁盘，并为 PostgreSQL、升级期间的新旧镜像和本地备份留增长空间。这是起步建议，不是容量保证。
- 预先安装并启动 [Docker Engine 与 Compose 插件](https://docs.docker.com/engine/install/)，Compose 需要支持 `up --wait --wait-timeout`（建议 2.24.4+）。脚本不会替你安装/替换 Docker，也不会改动已有面板。
- 常见工具：`curl`、CA 证书、`openssl`、`tar`、`sha256sum`、GNU coreutils。Agent 主机额外需要 systemd、`bzip2`，无需 Docker（除非备份容器或安装 edge 测试版）。
- 能访问 GitHub Releases、GHCR 和 Docker Hub。安装失败不会回退源码构建；受限网络需先准备合规代理/镜像访问条件。
- 正式部署准备一个已解析到该主机的域名。自动 HTTPS 模式需要 TCP 80/443 可达且未被其他服务占用，AAAA 记录也必须指向正确主机。

```bash
docker version
docker compose version
df -h
```

## 2. 安装控制面

先下载脚本，方便审阅，也避免下载中断时执行半个脚本：

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/latest/download/install.sh -o vaultmesh-install.sh
less vaultmesh-install.sh
sudo sh vaultmesh-install.sh install --domain backup.example.com
```

将 `backup.example.com` 换成你的实际域名。脚本解析最新**正式 Release**，校验部署包 SHA256，固定该版本镜像；生成随机管理员密码、数据库密码和主密钥，启动 PostgreSQL、Control Plane、Web、Caddy，并验证 HTTPS 健康端点。Caddy 管理证书申请与续期。浏览器和 API 使用同一 HTTPS 地址，数据库/API 不向宿主机暴露端口。

安装成功后显示地址、账号 `admin`、初始密码。初始密码仅用于首次建立账号；以后在控制台改过密码，`.env` 里的初始值不是当前密码，也不是找回密码的办法。

可明确指定版本：在命令末尾加 `--version vX.Y.Z`（替换成真实 Release 标签）。自定义目录使用 `--dir /srv/vaultmesh`；一台主机只支持这一套固定 Compose 项目 `vaultmesh`，不能通过换目录部署第二套。

### 已有 1Panel / Nginx / Caddy 占用 80/443

```bash
sudo sh vaultmesh-install.sh install \
  --proxy external --url https://backup.example.com
```

此模式仅监听 `127.0.0.1:3000`，不会占用公网 80/443。把现有 HTTPS 站点的**所有路径**转发到 `http://127.0.0.1:3000`，不要再分开代理 Web/API。保留原始 Host，允许至少 4 MiB 请求体，不缓存 `/api/` 和 `/config.txt`。随后从浏览器打开域名，确认登录和 `/healthz` 正常。

这里的 127.0.0.1 指**宿主机**：若反向代理跑在隔离 Docker bridge 网络里，它的 localhost 不是宿主机。请使用面板的 host-network 代理或由管理员配置专用互通网络；不要为了连通而把后端端口直接改为公网监听。

外部代理模式的安装成功仅代表内部服务健康；证书、公开 DNS、防火墙和公网连通性由你的现有代理负责，必须再验收。

### 只用 IP 测试

新生产安装器不把公网 HTTP 当作正式入口。已有 IP 自签证书测试部署继续按[运维手册](OPERATIONS.md#仅使用-ip-的临时测试)管理，不要直接覆盖安装。新机器建议先用域名部署；没有域名的测试需手工配置 IP 证书和 HTTPS 代理，然后使用 `--proxy external --url https://YOUR_IP:PORT`，并在浏览器/远程 Agent 正确建立证书信任。不要关闭 TLS 校验或设置“假装已启用 HTTPS”绕过门控。

## 3. 安装 Agent

在控制台“服务器”中新增服务器，复制一次性注册命令，**在要备份的机器上运行**。正式版控制台会生成与控制面同版本的下载链接。手工示例：

```bash
# vX.Y.Z 必须替换成控制台显示的真实版本
curl -fL https://github.com/to-alan/VaultMesh/releases/download/vX.Y.Z/install.sh -o vaultmesh-install.sh
sudo sh vaultmesh-install.sh install-agent https://backup.example.com enroll_REPLACE_ME --version vX.Y.Z
```

注册令牌是临时秘密；不要提交到 GitHub、截图分享或保存在工单中，注意清理含令牌的 Shell 历史。安装器校验二进制后配置 systemd，注册成功便从环境文件移除令牌。初次注册失败时保留现场，请先看日志，不要不断新增服务器/删除身份。

Agent 缺少 Restic 时，安装器自动下载并校验官方 **Restic 0.18.0** 预编译版本；已安装的 Restic 不会被替换。安装器不自动管理数据库客户端、rclone 或 Docker：

| 备份内容 | Agent 主机还需要 |
| --- | --- |
| 文件 / S3 / SFTP 等 Restic 原生仓库 | Restic，源目录和存储访问权限 |
| Docker 挂载 | Docker CLI 与本机 Docker socket 权限；Agent 默认以 root 运行 |
| MySQL | 与服务端兼容的 `mysqldump`；不能把 MariaDB 客户端一概当成 MySQL 官方客户端 |
| PostgreSQL | 与服务端兼容的 `pg_dump`（不低于服务端主版本），恢复机准备 `pg_restore` |
| WebDAV / 网盘等 rclone 仓库 | rclone 及相应受限配置 |

Linux armv7 仅提供 Agent，armv6 不支持；macOS 发布附件用于手工开发测试，没有 systemd 一键安装或生产支持承诺。

```bash
sudo systemctl status vaultmesh-agent
sudo journalctl -u vaultmesh-agent --since '10 minutes ago'
vaultmesh-agent --version
restic version
```

## 4. 开始使用与日常管理

按[首次使用指南](USAGE.md)完成真实备份和隔离恢复，再开始实际承载备份。

```bash
sudo vaultmesh status
sudo vaultmesh logs
sudo vaultmesh backup
sudo vaultmesh upgrade
```

`upgrade` 只在你手动运行时升级，不会因为维护者推送 main 就自动更新生产。升级前阅读[升级与恢复](UPGRADE.md)。

文件位置：

| 位置 | 用途 |
| --- | --- |
| `/opt/vaultmesh/.env` | 共享配置和主密钥，root-only；不要 `source` 或公开输出 |
| `/opt/vaultmesh/releases/vX.Y.Z/` | 不随本地编辑变化的版本化部署文件 |
| `/opt/vaultmesh/current` | 最后完成健康检查的版本链接 |
| `/opt/vaultmesh/backups/` | 控制面数据库、配置、原部署文件；须另做异机加密备份 |
| Docker 卷 `vaultmesh_vaultmesh-postgres` | PostgreSQL 数据；禁止 `docker compose down -v` |
| `/etc/vaultmesh-agent.env` | Agent 连接和工具配置，root-only |
| `/var/lib/vaultmesh-agent/` | 设备身份、Outbox、缓存、暂存和恢复文件，不得纳入业务备份 |

自定义目录时，用 `sudo sh /srv/vaultmesh/current/install.sh status --dir /srv/vaultmesh`；或 `sudo VAULTMESH_INSTALL_DIR=/srv/vaultmesh vaultmesh status`。

## 常见安装问题

- `404`：该版本不存在或没有新部署包；检查 Releases 附件，不要回退 main/源码。
- 镜像拉取失败：检查 GHCR/Docker Hub 网络和磁盘；首次公开包可能需要维护者设置 GHCR package 为 Public。普通用户不应需要维护者的 GitHub Token。
- HTTPS 不通：查 DNS/AAAA、防火墙、端口占用和 gateway 日志；不要停止其他业务来抢占端口，改用 external 模式。
- 已有安装被拒绝：这是保护机制，旧版迁移见 [UPGRADE.md](UPGRADE.md)，不是让你删目录或删数据库。
- 维护锁：普通退出会释放；断电/SIGKILL 后先确认没有安装进程，再移除提示的**空** `.operation-lock` 目录。

安装器不提供自动卸载/清库命令，避免把“重装/升级”误变成数据删除。需要停用时先备份，再停止服务并保留数据卷和 Agent 身份。
