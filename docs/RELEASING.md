# 维护者：推送代码、测试、发布

代码仓库：[to-alan/VaultMesh](https://github.com/to-alan/VaultMesh)。日常推送与用户升级是两回事：

```text
修改 → 本地检查 → commit → make push → GitHub CI → edge-完整SHA（测试）
                              ↓ 你选择发版时
                    make release VERSION=vX.Y.Z
                              ↓
                  完整 CI → 多架构镜像/二进制/部署包
                              ↓
                    上传 Draft → 全部成功才公开 Release
                              ↓
                  用户手动安装/upgrade（固定该版本）
```

## 一次性检查 GitHub 连接

```bash
git remote -v
gh auth status
# 如尚未登录，交互式登录自己的账号；不要把 Token 写入 remote URL：
gh auth login
gh auth setup-git
git push --dry-run origin main
```

在仓库 Settings → Actions 中允许工作流执行。发布工作流使用当前仓库的 `GITHUB_TOKEN` 并仅给需要的 job 分配 `contents: write` / `packages: write`，不需要服务器 SSH 密钥或个人 Token。首次创建的 GHCR package 要确认 Public 且关联本仓库，普通用户应能匿名拉取。

这套设置不能保证永远免认证：登录过期、仓库权限变化、网络中断、组织策略仍可能导致推送失败。不要为了“能推送”而取消安全限制或使用强制推送。

## 每次修改后推送

```bash
make web-install                 # 第一次或依赖改变时
make check                       # 包含安装器回归测试
git status --short
git diff --check
git add <本次修改的明确文件>       # 不盲目添加整个目录
git commit -m "fix: describe the change"
make push
```

`make push` 只推送已提交的当前分支：拒绝脏工作区、detached HEAD 和远端分叉，不自动提交、不强推、不打标签。main 和 PR 执行 CI；个人开发建议使用 `codex/描述` 或其他明确的功能分支，再提交 PR。普通功能分支推送后创建 PR 才运行 PR CI。

检查 Actions：CI 包括 Go lint/race/vet/漏洞、PostgreSQL 集成测试、前端测试/类型/构建/审计、安装器回归和三镜像冒烟。CI 还上传一个 `deployment-kit-preview` 附件，仅供检查结构，**不是可部署的正式版本**（没有对应 v0.0.0-rc.0 镜像）。

main 的 push CI 成功后才触发 Edge Images；镜像固定为 `edge-完整提交SHA`（当前 edge 仅 linux/amd64），三个构建完成后才更新浮动 `edge`。部署测试请固定完整 SHA，检查三个镜像均存在，不用 `edge` 当生产升级通道。

## 准备发布

1. 更新 CHANGELOG 的 Unreleased，整理该版本变更、修复、已知限制、数据库/配置/Agent unit 迁移说明。
2. 先在独立环境做：全新安装 → 注册 Agent → 探测 → 备份 → 隔离恢复 → 从上一正式版升级。
3. 明确版本号。普通修复递增 patch；1.0 前可能有破坏性的 minor 变更。发布候选使用 `vX.Y.Z-rc.N`。
4. 提交并推送，确认 GitHub main 与本地相同、CI 成功。首次上线先发 RC 验证完整分发链，再发正式版本。

```bash
# 仅当你决定发版时执行；这里的版本号是占位符。
make release VERSION=vX.Y.Z-rc.1
# 验证候选版本后，选择实际正式版本：
make release VERSION=vX.Y.Z
```

脚本只允许从干净且与 origin/main 一致的 main 创建新 annotated tag；已有 tag 拒绝覆盖。推送标签只是启动发布，不代表发布成功。若推送被网络打断，先用 `git ls-remote --tags origin` 确认远端，再推送同一个本地 tag；不要重建/移动它。

## GitHub 会产出什么

| 产物 | 用途 |
| --- | --- |
| GHCR `vaultmesh-control:vX.Y.Z` / `vaultmesh-web:vX.Y.Z` / `vaultmesh-agent:vX.Y.Z` | linux/amd64 + linux/arm64；正式部署按版本固定 |
| `vaultmesh-deploy-vX.Y.Z.tar.gz` | Compose、Caddy、安装管理器、systemd 模板、文档、许可证 |
| `install.sh` 与 `.sha256` | 新用户下载入口；正式 Release 的 latest 链接不跟随 main |
| `vaultmesh-agent-linux-{amd64,arm64,armv7}` | 静态 Agent 二进制，可直接安装，无需编译 |
| `vaultmesh-agent-darwin-{amd64,arm64}` | 手工开发测试，不承诺生产支持 |
| `SHA256SUMS`、单文件 `.sha256`、`VERSION`、`COMMIT`、`LICENSE` | 完整性、版本溯源和授权说明 |

版本标签先通过同一套完整 CI，才允许构建/推送。所有产物就绪后先上传 Draft，再一次性公开 Release；RC 标为 prerelease，不能取代最新正式版。旧版本补发也不把最新正式版指针向后移动。

不再发布/更新 GHCR 的浮动 `latest`，避免预发布或部分失败污染生产；旧安装若仍使用 `:latest`，须改成明确版本。不要重用已公开的版本标签；发现问题发新 patch，禁止覆盖已发布包。SHA256 可发现传输/文件不匹配，但不是独立签名；本流程信任 GitHub/GHCR 和仓库发布权限，不宣称离线签名或供应链证明已完成。

## 发布失败与验收

- CI 失败：没有正式发布，修复后用新的提交和新标签；不要移动公开 tag。
- 构建/上传失败：可重跑同一 tag 的失败 job，已有 Draft 保持未公开；部分 GHCR 版本镜像可能已存在，但用户安装入口仍不会选中 Draft。
- 并发发布：工作流串行执行且不取消正在发布的版本；GitHub 并发队列只保留有限待运行任务，不要连续推送大量 tag，逐个确认完成。
- 公开后检查 Release 附件齐全、checksum 可用、匿名 GHCR pull（amd64/arm64）、全新机器安装，以及上一版升级后的数据/身份未丢失。
- 发布说明不要只依赖自动汇总的提交列表；数据库迁移、升级顺序和风险必须补充清楚。

工作流不会自动更新你的服务器，不保存服务器私钥。发布与部署保持分离；测试服务器更新要另外明确执行。
