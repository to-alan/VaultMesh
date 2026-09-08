# 更新日志

本文件记录用户可见的变更。格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [SemVer](https://semver.org/lang/zh-CN/)。项目处于 1.0 之前，次版本号变更可能包含破坏性调整。

## [Unreleased]

## [0.1.2-rc.2] - 2026-09-08

候选测试版，不替换最新正式版。默认安装不再占用 80/443；没有新增数据库迁移或 Agent systemd unit 变更。完整安装、升级边界见 [RC2 说明](docs/RELEASE-v0.1.2-rc.2.md)。

### 安装与代理

- 默认改为 external HTTP 入口，优先使用回环 3000、占用时自动选择 3001–3099，支持 `--port`；域名可后配，只有显式 `--proxy managed` 才占用 80/443。安装前检查宿主机与 Docker 端口，不停止其他服务。
- 新增 `configure-proxy`：备份原配置后更新公开地址、Cookie 与端口，仅重启同版本应用服务，保留密码、主密钥和 PostgreSQL；失败保留原 `.env` 以便恢复。
- 新端口布局标记 `INSTALLER_API=2`，拒绝与旧包混装；增加先本地安装、再接入真实 Nginx HTTPS 的集成验收，未配置 HTTPS 时仍禁用敏感 Agent 操作。

### 文档

- 安装、升级示例统一说明 root 与普通用户的权限处理，避免精简系统缺少 sudo 时无法照抄；将 less 明确为可选查看步骤并说明 q / (END)，区分下载与真正安装，补充常见报错定位。

## [0.1.2-rc.1] - 2026-09-08

候选测试版，不替换最新正式版。新安装器不直接接管旧 Git/IP 部署；安装、升级边界与验收事项见 [候选版说明](docs/RELEASE-v0.1.2-rc.1.md)。

### 发布与安装

- 发布包安装器：固定版本、SHA256 校验、无 Git/Go/Node 依赖，不再回退服务器编译；默认自动 HTTPS，并支持已有反向代理。
- 控制面显式 upgrade/status/logs/backup，升级先拉镜像再备份 PostgreSQL、主密钥配置和旧部署文件，健康检查后切换版本；不自动降级已迁移数据库。
- 独立 upgrade-agent 保留身份、Outbox、环境配置、自定义服务与恢复目录；拒绝用重装自动清空已有身份。缺少 Restic 时安装校验后的官方预编译版本。
- CI 增加安装器失败保护回归、部署包预览和真实生产代理冒烟；main 通过 CI 后才构建 edge；版本 tag 通过完整 CI 后产出 amd64/arm64 镜像、Agent 和部署包，全部上传后公开 Release。RC 不替代最新正式版。
- 新增 make push / make release，以及安装、首次使用、升级恢复、维护者发布四份指南。正式版控制台生成同版本 Agent 安装命令。
- 保留 HTTPS 操作门控；旧 IP/Git 部署不被新安装器自动接管。移除会清理注册/数据库的旧安装器模式，迁移与恢复必须明确操作。

### 其他改进

- 未接受的命令 24 小时后停止派发：版本不匹配或已下线主机的命令不再无限重派（已接受的命令不受影响）

### 新增

- 探测全链路反馈：控制台轮询显示派发状态（尝试次数），超时自动给出诊断清单（Agent 版本过旧提示、journalctl 命令）；空探测结果视为合法"无可备份项"而非错误；探测入口移至备份项目页
- 可备份项探测：服务器页一键运行只读扫描（运行中的容器、MySQL/PostgreSQL 信号、composer.json/go.mod 等应用标记），勾选后自动生成项目草稿——不再需要手工寻找网站目录和数据库位置；探测不读取文件内容、不收集任何密钥，数据库密码仍由用户填写

### 修复

- 前后端从 v0.1.2-rc.1 起允许探测，避免候选版 Agent 被误判为过旧而禁用探测；更早或未知的预发布版本继续拒绝。
- 必需数据源的路径缺失/断链在准备阶段即失败并给出精确原因，不再依赖 Restic 退出码 3 降级为 partial 快照（可选源保持跳过+警告语义）
- Dashboard 总量排除已归档的服务器与项目，归档后指标不再虚高
- Agent 拒绝把自身状态目录（含明文凭据）与恢复目录作为备份源，并向 Restic 追加无条件排除，父目录扫掠（如 /var/lib）不再泄露凭据；控制面在保存时拒绝默认状态目录 `/var/lib/vaultmesh-agent`
- 多数据源项目的排除规则按声明它的源路径锚定（`P/pattern` 与 `P/**/pattern`），单一源的相对规则不再污染其他源；以 `/`、`**/`、`*`、`!` 开头的模式保持原义
- 遗留内嵌清单通道：运行报告 body 上限提升至 4 MiB 并为超限丢弃的清单写入 `snapshot_inventory_dropped` 标记，修复约 7000 条快照即触发 invalid_json 永久拒绝、成功备份被隔离的问题

## [0.1.1] - 2026-09-02

### 修复

- 无效的 `VAULTMESH_WEBAUTHN_RP_ID`（如误填服务器 IP）不再导致控制面拒绝启动，而是降级禁用通行密钥，其余功能照常
- 一键安装自动探测公网 IP 并写入 `VAULTMESH_PUBLIC_API_URL` / `VAULTMESH_ALLOWED_ORIGINS`（可用 `VAULTMESH_PUBLIC_HOST` 显式指定），升级时自动迁移 localhost 默认值，修复公网部署浏览器 `Failed to fetch`

### 新增

- 一键安装默认绑定 `0.0.0.0` 并输出服务器实际访问地址；`VAULTMESH_BIND=127.0.0.1` 可恢复回环绑定

### 变更

- 一键安装与 Compose 默认使用 GHCR 预构建镜像（`VAULTMESH_IMAGE_TAG`，默认 `latest`），镜像不可用时自动回退本地构建；升级不再必须在本机编译 Go/Node

## [0.1.0] - 2026-09-02

首个带发布产物的版本：GitHub Release 提供 5 个平台的 Agent 预编译二进制，GHCR 提供与版本 tag 对应的容器镜像。

### 新增

- 多服务器备份控制面：服务器一次性注册、项目声明式配置、5 段 Cron + 时区 + 抖动调度、RPO 迟到/超时推导
- 数据源：文件目录、Docker 挂载清单、MySQL/PostgreSQL 逻辑导出
- 存储：Restic 原生后端、S3 厂商预设（R2/MinIO/Wasabi/OSS/COS 等）、rclone 扩展；全局仓库渠道按服务器隔离路径
- 快照索引独立上报通道（`PUT /api/v1/agent/snapshots`）：幂等交付、清单超限在源头降级、运行报告不再内嵌清单
- 运行事实：`succeeded/partial/failed/timed_out/canceled/unknown/skipped` 七态，终态不可回退；`concurrency_policy=forbid` 重叠触发记录为 `skipped`
- 健康状态含 `running`，执行中的备份不再误报迟到
- 快照浏览与安全恢复：目录浏览、永久保护、恢复到全新隔离目录、禁止覆盖
- 告警与通知：`backup_failure`/`rpo_overdue`/`agent_offline`/`config_error` 四类事件；Webhook、Telegram、Email、Slack、Discord、企业微信、钉钉、Gotify、ntfy 九类渠道；稳定指纹去重、重复提醒、恢复通知、持久化重试
- 配置降级：主密钥事故时按项目降级下发配置并告警，不再整体失败
- 生命周期：服务器/项目/仓库归档（软删除 + 从 Agent 配置摘除 + 保留历史）；五类控制面数据保留策略（`VAULTMESH_RETENTION_*_DAYS`）
- Agent：离线自治（本地持久化配置 + Outbox 延迟上报）、一次性回滚放行（`--accept-rollback`）、心跳与配置合并为单请求、周期抖动
- 账号安全：TOTP、WebAuthn 通行密钥、恢复码、渐进限速、敏感操作重新认证、持久化审计
- 部署：一键安装脚本、systemd 单元（含 Restic 缓存目录加固）、tag 触发的 release workflow（Agent 二进制 + GHCR 镜像）

### 已知限制

- 单管理员、无 RBAC；控制面单实例（会话不跨副本共享）
- 只支持 `missed_run_policy=skip`；恢复仅限同 Agent 隔离目录
- 保护标签不等于 Object Lock；仓库连通性在首次 Restic 操作时才验证

[Unreleased]: https://github.com/to-alan/VaultMesh/compare/v0.1.2-rc.2...HEAD
[0.1.2-rc.2]: https://github.com/to-alan/VaultMesh/compare/v0.1.2-rc.1...v0.1.2-rc.2
[0.1.2-rc.1]: https://github.com/to-alan/VaultMesh/compare/v0.1.1...v0.1.2-rc.1
[0.1.1]: https://github.com/to-alan/VaultMesh/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/to-alan/VaultMesh/releases/tag/v0.1.0
