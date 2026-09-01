# 更新日志

本文件记录用户可见的变更。格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [SemVer](https://semver.org/lang/zh-CN/)。项目处于 1.0 之前，次版本号变更可能包含破坏性调整。

## [Unreleased]

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

[Unreleased]: https://github.com/to-alan/VaultMesh/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/to-alan/VaultMesh/releases/tag/v0.1.0
