# VaultMesh

[![CI](https://github.com/to-alan/VaultMesh/actions/workflows/ci.yml/badge.svg)](https://github.com/to-alan/VaultMesh/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/to-alan/VaultMesh)](https://github.com/to-alan/VaultMesh/releases)
[![License](https://img.shields.io/badge/license-PolyForm%20Noncommercial-5ee9b5)](LICENSE)

自托管的多服务器备份管理平台：在一个控制台管理 Linux 服务器、备份项目、计划、Restic 仓库、告警和隔离恢复。Agent 在源服务器本地备份，加密数据直接进入你自己的存储，不经过控制面。

> VaultMesh 仍处于 1.0 之前。正式使用前请用独立测试数据完成备份、校验和真实恢复，并保留现有备份方案。许可证不授予商业用途，商业部署需另行授权。

## 从这里开始

| 你要做什么 | 文档 |
| --- | --- |
| 新服务器安装 | [安装指南](docs/INSTALL.md) |
| 配置第一份备份并验证恢复 | [首次使用](docs/USAGE.md) |
| 更新版本、处理失败、迁移旧安装 | [升级与恢复](docs/UPGRADE.md) |
| 修改代码、推送 GitHub、构建发版 | [维护者发布指南](docs/RELEASING.md) |

## 安装：使用发布包，不在服务器编译

> 以下使用候选版 `v0.1.2-rc.2`（部署包 `INSTALLER_API=2`），不会被 `latest` 自动选中。旧 RC1 没有新端口选项；已因 443 冲突中断时请先按[恢复说明](docs/UPGRADE.md#rc1-443-recovery)继续，不能重新 install 或删数据卷。完整变更见 [RC2 说明](docs/RELEASE-v0.1.2-rc.2.md)。

准备 Linux amd64/arm64、Docker Engine + Compose v2 和 `ss`（iproute2）。新版默认不要求域名、不占用 80/443；先启动本地 HTTP 入口，随后由你自己的 Nginx/1Panel 反代。以下命令仅用于全新安装；已有安装使用升级流程。

以下安装、维护命令在 **root 终端**执行：`id -u` 输出 `0` 时直接运行，不需要 sudo。普通用户需先用有权限的 `sudo -i` 或 `su -` 切换，详见[安装身份说明](docs/INSTALL.md#先确认执行身份root-不需要-sudo)。遇到 `sudo: command not found` 不代表安装器出错，root 用户去掉 sudo 即可。

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/v0.1.2-rc.2/install.sh -o vaultmesh-install.sh &&
sh vaultmesh-install.sh install --version v0.1.2-rc.2
```

`curl` 只负责下载，后面的 `sh ... install` 才执行安装。查看脚本是可选步骤：如果单独运行了 `less vaultmesh-install.sh`，看到源码或 `(END)` 不是报错，按 `q` 退出再安装。只复制代码框中的命令，不包含终端提示符。

默认使用 `127.0.0.1:3000`，占用时从 3001–3099 自动选择；也可加 `--port 8300` 指定。端口冲突提前检查，不停止现有服务。安装器下载并校验发布包，拉取固定版本镜像，生成随机密码和主密钥，输出实际入口与初始密码。普通用户无需 Git、Go、Node.js。

本地入口仅供宿主机或 SSH 隧道访问；没有 HTTPS 时，探测、备份和恢复仍受保护。配置好 Nginx HTTPS、把整个站点反代到安装器输出的 HTTP 入口后，运行：

```bash
vaultmesh configure-proxy --url https://backup.example.com
```

替换为你的实际域名。只有主动选择 `install --proxy managed --domain backup.example.com` 时才由 VaultMesh 占用 80/443、管理证书。完整依赖、Nginx 配置、容器网络与排错见[安装指南](docs/INSTALL.md)。

登录后，在“服务器”创建注册令牌，将控制台给出的同版本 Agent 命令复制到需要备份的服务器。Agent 使用预编译二进制；缺少 Restic 时安装器会下载并校验官方预编译版本。数据库导出客户端、Docker 和 rclone 按实际数据源配置。

## 升级：你决定何时更新

```bash
vaultmesh status
# 升级到本次候选版，必须指定版本：
vaultmesh upgrade --version v0.1.2-rc.2
# 以后选择升级到最新正式版时才使用不带 --version 的 upgrade。
```

升级先拉好镜像，再备份数据库、主密钥配置和旧部署文件，完成健康检查后切换版本。不重新生成密码、不删除数据卷、不自动编译。发布包安装与旧 Git 安装有不同目录结构；已有 Git/IP 测试部署不会被直接接管，见[迁移说明](docs/UPGRADE.md)。

控制面升级完成后，逐台运行同版本安装器的 `upgrade-agent --version vX.Y.Z`；不需要新令牌，保留设备身份、配置、待上报结果和恢复目录。备份须另做异机加密保存；数据库迁移不保证可逆，失败恢复请遵循[升级手册](docs/UPGRADE.md)。

## 日常开发与发布分开

```bash
make check
git add <明确的修改文件>
git commit -m "fix: describe the change"
make push

# 仅当你决定发布新版本时（替换实际版本号）：
make release VERSION=vX.Y.Z
```

main 推送触发 CI，通过后构建 `edge-完整SHA` 测试镜像，不更新用户生产安装。版本标签触发完整测试、多架构 GHCR 镜像、Agent 二进制、部署包和 SHA256 校验；全部成功后才公开 GitHub Release。候选版用 `vX.Y.Z-rc.N`，普通用户不自动跟随。

开发依赖、认证与失败处理见[贡献指南](CONTRIBUTING.md)、[开发指南](docs/DEVELOPMENT.md)和[发布指南](docs/RELEASING.md)。不把服务器私钥或个人 Token 放进仓库，发布工作流也不自动部署服务器。

## 支持哪些备份

- 文件目录、Docker 挂载与脱敏清单、MySQL/PostgreSQL 逻辑导出。
- Restic 原生 Local/SFTP/REST/S3 等存储，R2/MinIO/OSS/COS 等 S3 兼容服务，以及受控 rclone 扩展。
- Cron + 时区 + 抖动调度，运行/迟到/超时状态，离线计划与延迟补报。
- 保留预览、Forget/Prune/Check、保护标签、快照浏览与全新隔离目录恢复。
- 多渠道通知、TOTP/通行密钥、凭据加密和操作审计。

数据库不能只复制正在写入的数据卷；“运行成功”不等于恢复已验证；保护标签也不等于存储层 Object Lock。当前是单管理员、单控制面实例，不提供多管理员 RBAC、HA 或自动覆盖生产目录的恢复。

## 更多文档

| 文档 | 内容 |
| --- | --- |
| [运维手册](docs/OPERATIONS.md) | 日常检查、灾备、安全、旧版部署与故障处理 |
| [存储支持矩阵](docs/STORAGE_PROVIDERS.md) | 仓库字段、凭据和前置条件 |
| [备份项目策略](docs/BACKUP_PROJECTS.md) | 数据源、保留、Check 与维护窗口 |
| [安全恢复](docs/SNAPSHOT_RECOVERY.md) | 快照浏览、保护、隔离恢复 |
| [通知与告警](docs/NOTIFICATIONS.md) | 渠道、路由、投递与安全边界 |
| [架构](docs/ARCHITECTURE.md) / [项目说明书](VaultMesh-项目说明书.md) | 数据流、状态机与产品边界 |
| [API](docs/API.md) / [OpenAPI](docs/openapi.yaml) | 接口与机器可读契约 |
| [前端集成](docs/FRONTEND_INTEGRATION.md) | 独立前端、Cookie/CORS 与主题 |
| [更新日志](CHANGELOG.md) / [安全策略](SECURITY.md) | 版本变化与漏洞报告 |

## 授权与反馈

采用 [PolyForm Noncommercial 1.0.0](LICENSE)，属于公开源码（source-available），不是无商业用途限制的开源许可。允许的用途和再分发要求以许可证原文为准；商业部署、托管服务或商业集成需联系仓库所有者另行授权。

普通缺陷和建议通过 GitHub Issues；贡献前阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。安全问题请按 [SECURITY.md](SECURITY.md) 私下报告，不在公开 Issue 上传密钥、配置或备份。
