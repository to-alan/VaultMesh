# 备份项目策略说明

VaultMesh 的项目模型不自创备份术语。实际执行引擎是 Restic，因此命令语义以 Restic 官方文档为规范；其他成熟开源项目用于核对“一个可用的备份项目”还必须覆盖哪些策略维度。

## 开源依据

- [Restic backup](https://restic.readthedocs.io/en/stable/040_backup.html)：路径、排除规则、`--one-file-system`、`--exclude-caches`、`--exclude-if-present` 和 `--exclude-larger-than` 的规范来源。
- [Restic forget/prune](https://restic.readthedocs.io/en/stable/060_forget.html)：最近、小时、日、周、月、年保留规则，以及 Prune 的仓库锁定和高成本特征。
- [Restic repository check](https://restic.readthedocs.io/en/stable/045_working_with_repos.html)：结构检查、抽样读取和完整数据读取的执行语义。
- [resticprofile](https://creativeprojects.github.io/resticprofile/)：把备份、保留、Prune、Check 和计划组合成配置 Profile；其 retention-after-backup 模式用于核对当前执行顺序。
- [Kopia policy](https://kopia.io/docs/reference/command-line/common/policy-set/)：用于交叉验证扫描边界、分层保留、计划和动作是项目策略的独立维度。
- [Backrest](https://github.com/garethgeorge/backrest) 与 [borgmatic](https://torsion.org/borgmatic/reference/configuration/consistency-checks/)：用于核对 Web 管理端应展示维护任务、校验和失败状态，而不是只展示一个 Cron 输入框。

这些项目没有共同的、可直接复制的 JSON Schema；它们共享的是相同的策略分层。VaultMesh 采用该分层，并把能够准确映射到当前 Restic Agent 的部分做成强类型配置。

## 项目组成

| 层 | 当前字段 | Agent 行为 |
|---|---|---|
| 数据源 | files、Docker、MySQL、PostgreSQL | 文件直接读取；Docker 生成脱敏清单并解析挂载；数据库先生成受保护的逻辑导出 |
| 扫描边界 | 文件系统边界、缓存、目录标记、大文件、逐源排除规则 | 转换为 Restic 原生参数，不经过 Shell 解释 |
| 快照计划 | 5 段 Cron、IANA 时区、随机延迟、最长运行时间 | Agent 离线持有最后一份配置并本地调度 |
| 保留 | 最近、小时、日、周、月、年、可选 Prune | `forget` 同时按 Agent host 与项目标签过滤 |
| 校验 | 关闭、仓库结构、抽样数据、完整数据 | 对应 `check`、`--read-data-subset`、`--read-data` |
| 运行控制 | 启用、暂停、立即备份 | 暂停会提升服务器配置版本，并从下一份 Agent 配置中移除项目 |

管理界面的初始保留模板为最近 3、每日 7、每周 4、每月 12、每年 3。它只是新建表单的安全起点，不是服务端强制默认值；API 调用方可以明确关闭保留。

## 执行与状态

一次计划或手动运行按以下顺序执行：

1. 验证仓库；仓库不存在时执行一次初始化。
2. 在 Agent 本机生成数据库导出和 Docker 清单。
3. 执行 Restic backup；只有返回完整快照后才进入维护阶段。
4. 若启用保留，执行带 `--host <agent-id>` 与 `--tag vaultmesh.project_id=<project-id>` 的 forget；只有显式启用时才追加 `--prune`。
5. 若启用校验，执行结构检查、抽样读取或完整数据读取。
6. 清理本地暂存文件并上报结果。

备份阶段失败时不会执行 Forget、Prune 或 Check。备份已经成功、但后续维护失败时，运行状态为 `partial`，同时保留 `snapshot_id` 和明确的 `retention_failed` 或 `repository_verification_failed` 错误码。这样不会把真实存在的快照误报为完全失败。

## 安全和成本边界

- Forget 必须同时带 host 与项目标签；仅按时间清理会误删共享仓库中其他项目的快照。
- Prune 会独占锁定仓库并可能产生大量读写，默认关闭。当前版本跟随成功备份执行；独立维护时间窗仍是后续能力。
- 完整数据校验可能读取整个仓库。常规自动化更适合结构检查或小比例抽样，完整读取应在低峰期人工启用。
- 不提供任意 Shell Hook。虽然 resticprofile、Kopia、borgmatic 等支持动作或 Hook，但在中心化 Agent 产品中直接开放会形成远程命令执行面；后续需要强类型、可审计的动作适配器。
- Docker 挂载数据默认只有崩溃一致性。数据库容器仍应额外配置 MySQL 或 PostgreSQL 逻辑数据源。
