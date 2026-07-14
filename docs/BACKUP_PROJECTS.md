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
| 保留 | 最多 N 份、智能、GFS、按时间、可选 Prune | `forget` 按 Agent host 与项目标签过滤，并固定 `--group-by host` |
| 校验 | 关闭、仓库结构、抽样数据、完整数据 | 在独立维护窗口执行 `check`、`--read-data-subset`、`--read-data` |
| 维护窗口 | Forget、Prune、Check 各自的 5 段 Cron | 与备份使用相同仓库互斥锁，但失败不会把成功备份标记为 partial |
| 运行控制 | 启用、暂停、立即备份 | 暂停会提升服务器配置版本，并从下一份 Agent 配置中移除项目 |

管理界面的初始策略是“最多 14 份”。用户也可以选择 Duplicati Smart Retention 对应的每日 7 天、每周 28 天、每月 1 年，使用六层 GFS 计数，或按 Restic duration 保留一段时间内的全部快照。Restic 的多条 keep 规则按“或”组合，GFS 各字段相加并不等于最终快照总数。

数据库导出与 Docker 清单位于每次运行新建的受保护暂存目录。Restic 默认按 host 与 paths 分组，会让变化的暂存路径形成不同保留组；VaultMesh 因此先用项目标签选择快照，再显式使用 `--group-by host`。在一个项目对应一个 Agent 的约束下，“最多 N 份”不会因暂存路径变化而失效。

## 执行与状态

新建项目把备份与仓库维护拆成独立任务：

1. 验证仓库；仓库不存在时执行一次初始化。
2. 在 Agent 本机生成数据库导出和 Docker 清单。
3. 执行 Restic backup，取得快照 ID 后清理本地暂存文件并上报结果。
4. 到达清理窗口时，执行带 `--host <agent-id>`、`--tag vaultmesh.project_id=<project-id>` 与 `--group-by host` 的 Forget。
5. 到达空间回收窗口时，独立执行 Prune；到达校验窗口时，独立执行结构检查、抽样读取或完整数据读取。

四类操作分别写入运行记录，并通过 `stats.operation` 区分 backup、retention、prune 与 verification。维护任务失败不会改变已经成功的备份状态，也不会污染 Dashboard 的备份成功率。缺少 `maintenance.separate` 的历史项目仍沿用备份后 Forget/Prune/Check，并在维护失败时标记 `partial`，以保证升级兼容。

## 安全和成本边界

- Forget 必须同时带 host 与项目标签；仅按时间清理会误删共享仓库中其他项目的快照。
- 项目卡片的“清理预览”由目标 Agent 对真实仓库执行 `forget --dry-run --json`，只上报将保留/将删除数量，不执行 Forget 或 Prune。它不是浏览器根据日期做的近似估算。
- Prune 会独占锁定仓库并可能产生大量读写，默认关闭；启用后应安排在每周低峰窗口。
- 完整数据校验可能读取整个仓库。常规自动化更适合结构检查或小比例抽样，完整读取应在低峰期人工启用。
- 不提供任意 Shell Hook。虽然 resticprofile、Kopia、borgmatic 等支持动作或 Hook，但在中心化 Agent 产品中直接开放会形成远程命令执行面；后续需要强类型、可审计的动作适配器。
- Docker 挂载数据默认只有崩溃一致性。数据库容器仍应额外配置 MySQL 或 PostgreSQL 逻辑数据源。
