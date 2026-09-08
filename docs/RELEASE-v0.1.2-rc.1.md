# VaultMesh v0.1.2-rc.1

这是候选测试版，用于验证新的预构建安装与发布链路，不替换最新正式版 v0.1.1，也不会自动升级任何服务器。正式使用前仍需在你的独立环境完成真实备份和隔离恢复，并保留现有备份。

## 主要变化

- 预构建安装：下载部署包和固定版本镜像，无需 Git、Go、Node，也不会在服务器上回退编译。支持自动 HTTPS 或已有 HTTPS 反向代理。
- 升级管理：手动升级、预拉镜像、备份数据库/主密钥配置/旧部署文件，健康检查成功后切换版本；数据库迁移不自动降级。
- Agent 单独升级：保留身份、配置、待上报结果、恢复目录和自定义 systemd 服务；缺少 Restic 时安装校验后的官方二进制。
- 备份项目页将探测与项目列表并排展示，筛选可备份项，不再自动弹出项目编辑器；候选版 Agent 可以正常执行探测。
- 修复归档统计、缺失必需数据源、Agent 自身敏感目录排除、多数据源排除规则和大快照清单上报问题。
- main 通过 CI 后产出 edge；版本标签通过完整 CI 后构建多架构镜像、Agent 和部署包，全部上传后才公开 Release。

## 安装候选版

准备 Linux amd64/arm64、已启动的 Docker Engine 与 Compose 插件，以及解析到新服务器的域名。自动 HTTPS 需要 80/443 可达且空闲；详细要求见[安装指南](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.1/docs/INSTALL.md)。

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/v0.1.2-rc.1/install.sh -o vaultmesh-install.sh
less vaultmesh-install.sh
sudo sh vaultmesh-install.sh install --domain backup.example.com --version v0.1.2-rc.1
```

将示例域名替换为你的实际域名。已有 1Panel/Nginx/Caddy 时，安装命令改为：

```bash
sudo sh vaultmesh-install.sh install --proxy external --url https://backup.example.com --version v0.1.2-rc.1
```

外部代理需将整个站点转发到宿主机 `127.0.0.1:3000`。安装器不修改已有面板或代理配置。公网纯 HTTP 不用于生产，不能通过关闭 TLS 校验绕过保护。

**不要使用 `/releases/latest/download/install.sh` 安装此候选版，也不要省略 `--version`。** `latest` 仍指向 v0.1.1，旧正式版没有新部署包。

登录后在“服务器”创建一次性注册令牌，在每台需要备份的 Linux 主机运行控制台生成的同版本 Agent 安装命令，再按[首次使用指南](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.1/docs/USAGE.md)完成探测、备份和隔离恢复。不要公开令牌、初始密码、主密钥或 Agent 身份文件。

## 升级与迁移边界

- **旧 v0.1.0/v0.1.1 的 Git/IP 部署不支持直接执行新 `upgrade` 接管。** 先备份 PostgreSQL、对应主密钥配置和 Agent 身份，按[升级与恢复指南](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.1/docs/UPGRADE.md)评估迁移；不要删除旧数据卷或重装 Agent 来“解决”拒绝安装。
- 采用新发布包布局的安装，后续可显式运行 `sudo vaultmesh upgrade --version vX.Y.Z`；控制面升级完成后，再用同版本安装器的 `upgrade-agent --version vX.Y.Z` 逐台升级 Agent。已有 Agent 不使用 `install-agent` 重装。
- 升级备份必须异机加密保存，数据库与对应主密钥配置必须配套。迁移后的数据库不承诺兼容旧程序，失败时按恢复指南人工处理。
- 此 RC 的自动检查不等于新服务器部署或旧版本迁移已经验收；真实环境的全新安装、注册、探测、备份、恢复及迁移演练仍是采用前的必要步骤。

## 发布产物

- `vaultmesh-deploy-v0.1.2-rc.1.tar.gz`：Compose、Caddy、安装器、systemd 模板和文档。
- `install.sh`、`SHA256SUMS`、单文件 `.sha256`、`VERSION`、`COMMIT`、`LICENSE`。
- Agent：Linux amd64/arm64/armv7；macOS amd64/arm64 附件仅用于手工开发测试。
- GHCR `ghcr.io/to-alan/vaultmesh/vaultmesh-{control,web,agent}:v0.1.2-rc.1`：每个组件均提供 linux/amd64 与 linux/arm64，不更新浮动 `latest`。

下载附件后可在下载目录执行 `sha256sum --check SHA256SUMS`（macOS 使用 `shasum -a 256 --check SHA256SUMS`）。完整校验要求下载该清单中的全部附件；SHA256 用于完整性检查，不是独立签名。

## 已知限制

单管理员、无 RBAC，控制面单实例；恢复到同 Agent 的全新隔离目录，不覆盖生产文件。保护标签不等于存储端 Object Lock。数据库客户端、rclone 和 Docker 需按数据源自行准备。许可证为 PolyForm Noncommercial，商业使用需另行授权。
