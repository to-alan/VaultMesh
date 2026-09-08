# 安装 VaultMesh

普通用户使用 GitHub Release 的预构建镜像和 Agent 二进制，不需要 Git、Go、Node.js，也不在服务器上编译。控制面管理配置，Agent 安装在每台要备份的 Linux 服务器上；备份正文由 Agent 直接写入你的存储。

> 本文使用候选版 `v0.1.2-rc.2` 的发布附件，必须显式指定版本，不能用 `latest` 代替。旧的 v0.1.0 / v0.1.1 没有新安装器附件；不要用旧包或 main 源码混搭安装。项目仍处于 1.0 之前，请保留原有备份并先完成恢复演练。

> 默认独立 HTTP 端口、`--port` 和 `configure-proxy` 从 `v0.1.2-rc.2`（`INSTALLER_API=2`）开始支持，**不在旧 v0.1.2-rc.1 中**。不要拿新版脚本搭配旧版部署包。RC1 用户见本页的旧版说明；已因 443 冲突中断的安装，见[保留数据恢复](UPGRADE.md#rc1-443-recovery)。

## 1. 准备服务器

- 控制面：Linux amd64 或 arm64；建议 2 核、2 GiB 内存，至少 5 GiB 可用磁盘，并为 PostgreSQL、升级期间的新旧镜像和本地备份留增长空间。这是起步建议，不是容量保证。
- 预先安装并启动 [Docker Engine 与 Compose 插件](https://docs.docker.com/engine/install/)，Compose 需要支持 `up --wait --wait-timeout`（建议 2.24.4+）。脚本不会替你安装/替换 Docker，也不会改动已有面板。建议 Docker Engine 28+；更旧版本存在同二层网络可访问回环发布端口的风险，参见 [Docker 端口发布说明](https://docs.docker.com/engine/network/port-publishing/)。
- 常见工具：`curl`、CA 证书、`openssl`、`tar`、`sha256sum`、GNU coreutils，以及 `ss`（Linux 通常由 iproute2 提供，用于安装前检查端口）。Agent 主机额外需要 systemd、`bzip2`，无需 Docker（除非备份容器或安装 edge 测试版）。
- 能访问 GitHub Releases、GHCR 和 Docker Hub。安装失败不会回退源码构建；受限网络需先准备合规代理/镜像访问条件。
- 默认安装不需要域名，也不占用 80/443。你可以先启动本地 HTTP 服务，稍后自己配置 Nginx HTTPS。只有显式选择 `--proxy managed` 自动 HTTPS 时，才需要提前准备域名以及空闲、可达的 TCP 80/443。

```bash
docker version
docker compose version
df -h
```

## 2. 安装控制面

### 先确认执行身份：root 不需要 sudo

下面的安装和维护命令均以 **root 终端**为例，不带 `sudo`。先运行：

```bash
id -u
```

- 输出 `0`：已经是 root，直接执行下方命令。精简系统可能没有 `sudo`，无需为了安装 VaultMesh 额外安装它。
- 输出其他数字：是普通用户。有 sudo 权限时先执行 `sudo -i`，再用 `id -u` 确认输出 `0`；如果没有 sudo，可在知道 root 密码时使用 `su -`，或通过服务商提供的 root 终端操作、联系管理员。
- 如果参考其他页面或控制台复制了带 `sudo` 的命令，**只有确认当前是 root 后**才去掉开头的 `sudo`。普通用户直接去掉它并不会获得安装权限。

只复制代码框里的命令，不要复制 `root@服务器:~#` 等终端提示符。下载地址应是代码框中的纯 URL，不是 `[地址](地址)` 形式的 Markdown 链接。

### 下载、查看与安装是不同步骤

- `curl ... -o vaultmesh-install.sh`：下载文件。出现 `100%` 只表示下载完成，尚未安装。
- **可选查看**：安装前如需审阅脚本，可先单独执行下载命令，再运行 `less vaultmesh-install.sh`。它只显示源码；`(END)` 表示文件末尾，不是报错。按 **`q`** 退出后再执行安装命令。如果系统没有 `less`，可使用其他文本查看器，不是安装依赖。
- `sh vaultmesh-install.sh install ...`：才是真正执行安装。不要把查看器里显示的整份源码复制回终端运行。

下面的快捷命令不包含 `less`，使用 `&&` 确保下载失败时不会继续执行本地旧脚本。

### 新版默认：独立 HTTP 端口，稍后接入 Nginx

以下为全新安装命令。已有完整安装按[升级指南](UPGRADE.md)操作；安装中断时不要直接重跑 install。

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/v0.1.2-rc.2/install.sh -o vaultmesh-install.sh &&
sh vaultmesh-install.sh install --version v0.1.2-rc.2
```

安装器优先选择 `127.0.0.1:3000`，占用时在 3001–3099 中找空闲端口，并持久化实际端口。也可在命令末尾加 `--port 8300` 固定端口（1024–65535）；指定端口冲突会提前退出，不停止其他服务、不初始化新数据库。检查覆盖宿主机监听和 Docker 发布端口；检查与启动之间仍可能发生其他进程抢占，若发生请保留现场。

安装会生成管理员密码、数据库密码和主密钥，拉取固定版本镜像并启动 PostgreSQL、Control、Web 和内部 HTTP 网关。只有网关的一个回环端口发布到宿主机，数据库/API 不单独公开。安装成功后输出实际本地入口、账号 `admin` 和初始密码。

域名可先留空；此时只提供本地登录/管理入口，**探测、备份、恢复等 Agent 操作仍受 HTTPS 保护**。需要远程查看时，可在自己电脑上使用 SSH 隧道，例如端口实际为 3000 时：

```bash
ssh -N -L 3000:127.0.0.1:3000 root@SERVER_IP
```

替换服务器 IP，两个端口都使用安装器输出的实际端口；保持隧道运行，在本机浏览器打开 `http://127.0.0.1:3000`。不要把后端改成公网 HTTP 来绕过 HTTPS 保护。

如果已确定公开地址，也可以首次安装时添加 `--url https://backup.example.com`（或 `--domain backup.example.com`）。默认仍然只发布回环 HTTP 端口，域名参数本身不会启动自动 HTTPS。

初始密码仅用于首次建立账号；以后在控制台改过密码，`.env` 里的初始值不是当前密码，也不是找回密码的办法。自定义目录使用 `--dir /srv/vaultmesh`；一台主机只支持一套固定 Compose 项目 `vaultmesh`，不能通过换目录部署第二套。

### 自己配置 Nginx / 1Panel 反向代理

80/443 和证书由你的现有 Nginx 管理。为 VaultMesh 建立 HTTPS 站点，将**所有路径**反代到安装器输出的地址，例如 `http://127.0.0.1:3000`，无需拆分 Web/API。现有站点的配置示例（放到对应 HTTPS `server` 内，替换该站点原有的 `location /`，不要重复添加）：

```nginx
client_max_body_size 4m;
location / {
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_cache off;
}
```

证书路径和域名沿用你该站点的设置；如果实际入口不是 3000，修改 `proxy_pass`。不要缓存 `/api/` 或 `/config.txt`。反代及 Host 头的行为见 [Nginx 官方文档](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_set_header)。宿主机 Nginx 在配置后执行 `nginx -t`，成功后再重载；1Panel 用户在面板中保存并应用该站点配置，不要盲目停止整个面板。

**127.0.0.1 指宿主机**：如果 Nginx 跑在隔离的 Docker bridge 网络里，它的 localhost 不是宿主机。使用面板的 host-network 代理，或由管理员配置专用容器互通网络并指向 HTTP gateway；不要直接把后端端口改成公网监听。

完成 Nginx HTTPS 配置后，在 VaultMesh 主机的 root 终端更新公开地址：

```bash
vaultmesh configure-proxy --url https://backup.example.com
```

这会备份原 `.env`，同步公开 URL、Cookie 与通行密钥配置，仅重新部署同版本的 Control/Web/HTTP 网关；不重新生成任何密码或主密钥，不重建数据库、不更换数据卷。已有网页登录会话可能需重新登录。以后更换后端端口，可添加 `--port 8300`，并同步修改 Nginx 的反代目标；未指定时保留原端口。

本地健康检查通过不代表公网 HTTPS 已验证。请用真实域名检查 `/healthz`、登录、页面 API 和 Agent 连接。没有实际 HTTPS 时，不要只改 URL 或设置 `VAULTMESH_HTTPS_ENABLED=true` 伪装已配置。

### 可选：让 VaultMesh 管理 HTTPS

仅适用于没有其他服务占用 80/443、并且域名已正确解析到本机的情况：

```bash
sh vaultmesh-install.sh install --proxy managed --domain backup.example.com --version v0.1.2-rc.2
```

此模式才由 Caddy 占用 80/443 并申请/续期证书。新安装器会先检查两个端口，再拉包和初始化配置。已有 Nginx 时不要选择它。

<a id="rc1-external"></a>

### 旧 v0.1.2-rc.1：显式选择 external

RC1 没有上述新端口选项。若是**全新、空目录**安装，可使用原包的 external 模式，让你的 Nginx 代理固定的 `127.0.0.1:3000`：

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/v0.1.2-rc.1/install.sh -o vaultmesh-install.sh &&
sh vaultmesh-install.sh install --proxy external --url https://backup.example.com --version v0.1.2-rc.1
```

先确认 3000 未被占用；RC1 不会自动选端口，也不能省略 HTTPS 公开地址。已经因 443 冲突安装到一半时，**不要重复 install**，按[RC1 恢复说明](UPGRADE.md#rc1-443-recovery)保留现有配置继续。不要使用 `latest` 下载 RC，旧正式版没有新部署包。

### 只用 IP 测试

新版可先无域名安装，通过上述 SSH 隧道查看本地管理入口。若需要远程 Agent 完整测试，仍需真实 HTTPS：可以自行配置 IP 证书和 HTTPS 代理，使用 `configure-proxy --url https://YOUR_IP:PORT`，并在浏览器/远程 Agent 正确建立证书信任。已有 IP 自签测试部署按[运维手册](OPERATIONS.md#仅使用-ip-的临时测试)管理，不直接覆盖安装；不要关闭 TLS 校验绕过门控。

## 3. 安装 Agent

在控制台“服务器”中新增服务器，复制一次性注册命令，**在要备份的机器上运行**。正式版控制台会生成与控制面同版本的下载链接。手工示例：

确认这台 Agent 主机也已进入 root 终端。控制台生成的命令可能带 `sudo sh`；root 用户改为 `sh` 即可，无需安装 sudo。

```bash
# vX.Y.Z 必须替换成控制台显示的真实版本
curl -fL https://github.com/to-alan/VaultMesh/releases/download/vX.Y.Z/install.sh -o vaultmesh-install.sh &&
sh vaultmesh-install.sh install-agent https://backup.example.com enroll_REPLACE_ME --version vX.Y.Z
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
systemctl status vaultmesh-agent
journalctl -u vaultmesh-agent --since '10 minutes ago'
vaultmesh-agent --version
restic version
```

## 4. 开始使用与日常管理

按[首次使用指南](USAGE.md)完成真实备份和隔离恢复，再开始实际承载备份。

```bash
vaultmesh status
vaultmesh logs
vaultmesh backup
# 候选版需明确指定；不带版本时只查找最新正式版：
vaultmesh upgrade --version v0.1.2-rc.2
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

自定义目录时，在 root 终端用 `sh /srv/vaultmesh/current/install.sh status --dir /srv/vaultmesh`；或 `VAULTMESH_INSTALL_DIR=/srv/vaultmesh vaultmesh status`。

## 常见安装问题

- `sudo: command not found`：命令在调用 sudo 时就失败了，安装器尚未启动。运行 `id -u`；如果是 `0`，去掉 sudo 后重试，否则按上面的身份说明先取得管理员权限。
- 满屏脚本、末尾 `(END)`：这是 `less` 查看界面，不是安装日志或错误。按 `q` 返回终端，再执行 `sh vaultmesh-install.sh install ...`。
- 只有 curl 的 `100%` 进度：仅完成下载，还需要执行安装命令；成功后安装器才会输出“已就绪”、访问地址和初始账号信息。
- `syntax error` 或 `unexpected token`：不要手工粘贴网页/聊天中的源码；重新下载原始 `install.sh` 附件，用 `sh -n vaultmesh-install.sh` 检查语法（不执行安装）。聊天里的转义或换行不一定反映服务器文件的真实内容；语法检查通过也不代表依赖、域名或运行环境已经通过验证。
- `404`：该版本不存在或没有新部署包；检查 Releases 附件，不要回退 main/源码。
- 镜像拉取失败：检查 GHCR/Docker Hub 网络和磁盘；首次公开包可能需要维护者设置 GHCR package 为 Public。普通用户不应需要维护者的 GitHub Token。
- HTTPS 不通：查 DNS/AAAA、防火墙、端口占用和 gateway 日志；不要停止其他业务来抢占端口，改用 external 模式。
- `bind: address already in use`：已有程序占用所选端口，先用 `ss -ltnp` 和 `docker ps` 确认。新安装器选择其他 `--port`；若旧 RC1 已创建配置/数据卷，按中断恢复说明处理，不重复 install 或删卷。
- 已有安装被拒绝：这是保护机制，旧版迁移见 [UPGRADE.md](UPGRADE.md)，不是让你删目录或删数据库。
- 维护锁：普通退出会释放；断电/SIGKILL 后先确认没有安装进程，再移除提示的**空** `.operation-lock` 目录。

安装器不提供自动卸载/清库命令，避免把“重装/升级”误变成数据删除。需要停用时先备份，再停止服务并保留数据卷和 Agent 身份。

仍有错误时，请提供实际执行的命令和**第一条报错前后数行**，不要只复制脚本源码。隐藏密码、注册令牌，不要公开 `.env`、主密钥或 Agent 身份文件。
