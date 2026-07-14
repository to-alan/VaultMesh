# 快照浏览与安全恢复

VaultMesh 的恢复功能直接编排 Restic 官方命令，不在控制面重新实现仓库读取器。设计目标是先让管理员回答“有哪些恢复点、里面有什么、恢复到了哪里”，再考虑自动恢复演练和原路径回写。

Agent 必须安装 Restic 0.17.0 或更高版本；隔离恢复依赖该版本已经提供的 `restore --overwrite never`。

## 开源依据与命令映射

| 能力 | Restic 原生命令 | VaultMesh 行为 |
|---|---|---|
| 快照索引 | `snapshots --json` | Agent 按自身 host 和 `vaultmesh.project_id=<project-id>` 标签读取；控制面只缓存元数据 |
| 文件浏览 | `ls --json <snapshot> <path>` | 目录读取在项目所属 Agent 执行；单次最多接受 5,000 个条目 |
| 永久保护 | `tag --add vaultmesh.protected=true` | 标签修改后立即重新索引；Forget 固定携带 `--keep-tag vaultmesh.protected=true` |
| 隔离恢复 | `restore --json --target <new-dir> --overwrite never <snapshot>:<path>` | 每个命令使用新的目标目录；禁止原路径写回和已有目录复用 |

规范来源：

- [Restic working with repositories](https://restic.readthedocs.io/en/latest/045_working_with_repos.html)：快照列举、文件列表与仓库操作；
- [Restic restore](https://restic.readthedocs.io/en/v0.19.1/050_restore.html)：按路径恢复、目标目录、dry-run 与覆盖策略；
- [Restic scripting](https://restic.readthedocs.io/en/latest/075_scripting.html)：JSON 输出与自动化消费边界；
- [Restic forget](https://restic.readthedocs.io/en/stable/060_forget.html)：`--keep-tag` 与多条保留规则的组合；
- [Restic backup](https://restic.readthedocs.io/en/stable/040_backup.html)：快照标签语义。

## 数据流

```text
管理员浏览器
  │  创建异步命令 / 读取缓存索引
  ▼
Control Plane ── PostgreSQL: snapshot metadata + command payload + run audit
  │
  │ Agent 设备认证、轮询命令
  ▼
项目所属 Agent ── Restic ── 备份仓库
  │
  └── /var/lib/vaultmesh-agent/restores/<command-id>
```

备份数据和恢复文件不经过 Control Plane。控制面保存快照 ID、时间、路径、标签、文件数、逻辑字节数、保护状态和最后同步时间；目录条目与恢复结果作为 Run 审计数据上报。

快照索引使用 Agent 上报的完成时间保持迟到报告的顺序，但只信任控制面接收时间前后 5 分钟内的值；超出窗口时改用服务端接收时间。这样可以防止 Agent 时钟严重超前后压制后续快照同步。

## 操作流程

1. 在“快照恢复”页按项目筛选，点击“从 Agent 同步”。
2. 选择一个恢复点。VaultMesh 投递目录读取命令，并自动轮询运行结果。
3. 进入目录或选择单个文件，点击“恢复”。
4. 在二次确认中核对快照、路径和安全边界。
5. Agent 创建 `VAULTMESH_RESTORE_ROOT/<command-id>`，确认目录此前不存在，然后执行 Restic 恢复。
6. 页面显示实际目标路径、恢复文件数和字节数。管理员在 Agent 上核验内容并取回文件。
7. 核验结束后由管理员删除恢复目录；当前版本不会自动清理恢复产物。

## 安全约束

- 只接受缓存索引中存在的项目/快照组合，不接受 `latest`、短 ID 或任意仓库选择器。
- 快照 ID 必须是 64 位小写十六进制 Restic 完整 ID；路径必须是 POSIX 绝对路径，拒绝 NUL 和换行。
- 恢复根目录必须是绝对路径、不能是文件系统根或符号链接，权限会收紧为 `0700`。
- 目标目录由受约束的命令 ID生成；如果已存在，任务直接失败。
- Restic 固定使用 `--overwrite never`。即使管理员重复提交，也不会覆盖另一个任务的恢复文件。
- 目录读取限制为 5,000 条或约 512 KiB 条目元数据、Agent 上报请求限制为 1 MiB，避免把巨大目录一次性送入控制面。
- 快照操作和备份/Forget/Prune/Check 共享仓库互斥锁，避免同一个 Agent 同时对同一仓库执行冲突操作。

## 保护快照的语义

“永久保护”表示自动保留策略不会删除这份快照，不等于对象存储不可变性。VaultMesh 使用 Restic 标签 `vaultmesh.protected=true`，并把它编译为 Forget 的 `--keep-tag` 规则。取消保护后，快照会重新进入项目的正常保留计算。

Restic 修改标签时会创建新的快照元数据对象，因此快照 ID 可能变化。VaultMesh 在标签命令成功后重新读取完整项目索引，避免 UI 继续持有旧 ID。

R2/S3 端的 Object Lock、版本控制、生命周期规则与凭据隔离仍需单独配置；它们属于存储层防篡改，不由“永久保护”按钮替代。

## 当前边界

- 只支持人工触发的同 Agent 隔离恢复，不直接覆盖业务目录。
- 尚未支持跨仓库复制、跨 Agent 恢复、自动恢复演练和恢复结果校验。
- 尚未为恢复产物配置 TTL 或磁盘配额；管理员必须监控 `VAULTMESH_RESTORE_ROOT` 容量。
- 快照目录当前按需读取，不在控制面构建完整文件索引或全文搜索。
