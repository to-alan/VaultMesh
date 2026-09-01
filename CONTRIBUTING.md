# 贡献指南

感谢关注 VaultMesh。提交贡献前请先阅读 [LICENSE](./LICENSE)：本项目采用 PolyForm Noncommercial 1.0.0，提交即表示你的贡献按同等许可分发。

## 开发环境

| 依赖 | 版本 |
|---|---|
| Go | 1.26.6 |
| Node.js | 24 |
| Docker + Compose v2 | 可选（容器冒烟测试） |

本地无需 PostgreSQL 即可开发：省略 `VAULTMESH_DATABASE_URL` 时控制面使用内存存储。

```bash
npm --prefix web install   # 或 make web-install
make check                 # Go 测试 + 前端构建 + go vet
make build                 # 产出 bin/vaultmesh-server、bin/vaultmesh-agent
```

本地跑 PostgreSQL 集成测试（`internal/store/postgres/postgres_integration_test.go`）：

```bash
docker run --rm --name vaultmesh-pg-test -p 5432:5432 \
  -e POSTGRES_DB=vaultmesh_test -e POSTGRES_USER=vaultmesh -e POSTGRES_PASSWORD=vaultmesh_test \
  postgres:17-alpine &
export VAULTMESH_TEST_DATABASE_URL='postgres://vaultmesh:vaultmesh_test@127.0.0.1:5432/vaultmesh_test?sslmode=disable'
go test ./internal/store/postgres/ -count=1
```

## 提交前检查

- `make check` 通过（CI 会跑：Go 全量测试 + 竞态检测、govulncheck、前端 vitest + typecheck + build、三个容器镜像冒烟测试）；
- 新增/修改行为有对应测试；
- 涉及 API 路由时同步 `docs/openapi.yaml`（契约测试会强制路由与文档对齐）；
- 涉及用户可见行为时更新 README 或 `docs/` 对应文档，并在 `CHANGELOG.md` 的 Unreleased 段落追加条目。

## 测试约定

Go 侧：

- `internal/store` 的 memory 与 postgres 实现**语义必须对齐**：冲突、终态不可回退、租约行为一致；改动 Store 接口时两个实现与测试同步更新；
- 控制面 HTTP 行为通过 `internal/control/http_test.go` 的 handler 级测试覆盖，不直连数据库；
- 告警、保留清理等后台逻辑以 Service 级测试覆盖（可用 `service.now` 注入时钟）。

前端：

- 三层测试：纯函数（`display.ts` 等）→ Composable 状态机 → View 挂载测试，详见 `docs/DEVELOPMENT.md`；
- 架构约束由 `npm run check:architecture` 强制：`src/` 下只有 `services/` 与 `api.ts` 允许触及网络。

## 提交规范

- 使用 Conventional Commits：`feat:`、`fix:`、`refactor:`、`docs:`、`chore:`；
- 一个提交一个主题；行为变更与纯重构分开提交；
- 提交信息说明"为什么"，正文可引用 issue 或需求背景。

## 安全问题

不要为安全问题创建公开 Issue，按 [SECURITY.md](./SECURITY.md) 私下报告。

## 边界

- 不接受把 VaultMesh 变为托管云盘/数据中转的改动；
- 不引入与目标规模不匹配的重型依赖（消息队列、Redis 等）——除非先在 Issue 中达成共识；
- 商业授权相关问题请直接联系仓库所有者。
