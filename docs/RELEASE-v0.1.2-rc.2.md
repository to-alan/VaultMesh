# VaultMesh v0.1.2-rc.2

候选测试版。本次主要修复安装器默认抢占 80/443、必须提前设置域名的问题，并改进 root / sudo / less 的安装说明。RC2 不替换最新正式版，安装和升级都需要明确指定 `v0.1.2-rc.2`。

## 主要变化

- 默认仅发布 `127.0.0.1:3000`；占用时自动选择 3001–3099，也可以用 `--port 8300` 指定。检查宿主机与 Docker 已发布端口，不停止已有服务。
- 无需提前设置域名。80/443 留给你的 Nginx；只有显式选择 `--proxy managed --domain ...` 时，VaultMesh 才管理证书并使用 80/443。
- 新增 `configure-proxy --url https://你的域名`：稍后接入已有 HTTPS 反代，备份并更新入口配置，保留密码、主密钥、数据库和版本。可同时修改后端端口。
- 部署包包含 `INSTALLER_API=2`，避免新版安装器与旧 RC1 模板混装。
- 补充无域名登录、真实 Nginx HTTPS、敏感操作保护和 PostgreSQL 保留的回归测试。
- 安装文档区分下载、可选的 less 查看和真正安装；root 无需 sudo，`(END)` 不是报错，按 q 退出查看器。

## 全新安装

先准备 Linux amd64/arm64、Docker Engine + Compose v2、curl 和 `ss`（iproute2）；依赖和权限详见[安装指南](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.2/docs/INSTALL.md)。在 **root 终端、尚未安装 VaultMesh 的主机**运行：

```bash
curl -fL https://github.com/to-alan/VaultMesh/releases/download/v0.1.2-rc.2/install.sh -o vaultmesh-install.sh &&
sh vaultmesh-install.sh install --version v0.1.2-rc.2
```

安装器输出实际本地端口。将 Nginx HTTPS 站点的全部路径反代到该入口，再执行下面的命令（替换实际域名）：

```bash
vaultmesh configure-proxy --url https://backup.example.com
```

如果 Nginx 在 Docker bridge 网络中，容器的 127.0.0.1 不是宿主机；请按安装指南配置网络，不要将后端改成公网 HTTP。未配置真实 HTTPS 时，探测、备份、恢复等 Agent 操作仍受保护。

## 升级与兼容性

- 已经完整安装成功的 RC1：在维护窗口运行 `vaultmesh upgrade --version v0.1.2-rc.2`，然后逐台升级 Agent 到同版本。控制面升级先备份数据库和配置，Agent 升级保留身份和状态。
- **RC1 因 443 占用安装到一半：不要执行 install 或删除数据卷。** 先按[RC1 保留数据恢复说明](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.2/docs/UPGRADE.md#rc1-443-recovery)切换为原版 external 入口、完成安装，再正常升级。若 3000 也被占用，停止恢复并另行处理，不要停其他业务抢端口。
- 升级不会更改原代理模式或端口；原 managed 安装仍使用 80/443，需自行决定何时用 `configure-proxy` 切换。
- 相对 RC1 没有新增数据库迁移、Agent systemd unit 变更或身份迁移。不覆盖旧版本附件，不自动接管旧 Git / IP 测试部署。
- 正式版 v0.1.0 / v0.1.1 没有新发布包布局，不能直接套用上述升级命令；请先阅读[迁移和升级指南](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.2/docs/UPGRADE.md)。

## 发布产物和验收

提供 Linux amd64/arm64 的 Control、Web、Agent GHCR 镜像；5 个平台的 Agent 二进制；部署包、安装器、版本/提交记录与 SHA256 校验文件。Agent 的 Linux armv7 二进制可用；macOS 二进制仅供手工开发测试。

发布流程在完整 CI 和所有产物构建成功后才公开 Release；它不会自动部署或更新任何服务器。正式承载备份前，请保留现有备份方案，并按[首次使用指南](https://github.com/to-alan/VaultMesh/blob/v0.1.2-rc.2/docs/USAGE.md)完成真实备份和隔离恢复验收。
