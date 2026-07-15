# VaultMesh 开发与扩展指南

本文说明代码边界和扩展约定。目标是让新增数据源、存储渠道、通知 Provider 或页面能力时，改动集中、可测试，并且不破坏备份与凭据安全边界。

## 依赖方向

```text
cmd/*
  ├─ internal/control ── internal/domain ── internal/store
  ├─ internal/agent   ── internal/domain
  └─ web/src          ── HTTP API
```

- `internal/domain` 只保存跨层数据结构和状态常量，不依赖 HTTP、数据库或命令执行。
- `internal/control` 负责业务规则、认证、API、告警和 Secret 封装，不执行源服务器命令。
- `internal/agent` 负责调度和强类型任务执行，不应依赖控制面的 HTTP handler。
- `internal/store` 定义持久化接口；Memory 与 PostgreSQL 实现必须具有相同的冲突、终态和事务语义。
- `web/src/api.ts` 是浏览器请求边界；页面不直接拼接 API Origin 或自行处理会话 Cookie。

## 目录职责

| 位置 | 职责 |
|---|---|
| `internal/control/service.go` | Service 组合、服务器注册、项目与命令编排 |
| `internal/control/project_validation.go` | 数据源、保留、验证和维护策略的规范化与校验 |
| `internal/control/repositories.go` | 仓库类型、字段约束、URL 和 Secret 校验 |
| `internal/control/notifications.go` | 通知渠道配置、Provider 注册表和公开字段脱敏 |
| `internal/control/notification_sender.go` | 出站通知适配器、HTTP/SMTP 安全传输 |
| `internal/control/alerts.go` | 告警事实推导、去重、恢复、投递队列和 Worker |
| `internal/control/admin_security.go` | 个人资料、重新认证和密码修改 |
| `internal/control/admin_totp.go` | TOTP 设置、验证和恢复码生命周期 |
| `internal/control/admin_passkey.go` | WebAuthn 注册、登录和 Ceremony 生命周期 |
| `web/src/App.vue` | 页面状态编排和模板；不放通用转换算法 |
| `web/src/services` | 唯一允许声明版本化 API 路径的类型化服务层 |
| `web/scripts/check-architecture.mjs` | 阻止页面直接调用 `fetch`、`requestJSON` 或声明 API 路径 |
| `web/src/forms` | 页面草稿、服务端 DTO 与编辑回填之间的纯转换；不发起网络请求 |
| `web/src/display.ts` | 纯展示、状态标签、时间与摘要格式化 |
| `web/src/webauthn.ts` | 浏览器 WebAuthn 数据转换和错误翻译 |
| `web/src/repositories.ts` | 仓库表单元数据和 Restic URL 构建 |
| `web/src/notifications.ts` | 通知渠道表单元数据 |

## 新增通知 Provider

后端 Provider 使用单一注册表 `notificationProviderDefinitions`。一条定义必须同时声明：

1. 必填字段 `RequiredFields`；
2. 可接受字段 `AllowedFields`；
3. 响应中必须隐藏的 `SecretFields`；
4. 实际投递适配器 `Send`。

然后在 `web/src/notifications.ts` 添加相同渠道的表单定义，并更新 `docs/NOTIFICATIONS.md`。提交前至少覆盖：缺失必填字段、未知字段拒绝、Secret 不回显、测试投递、SSRF 私网策略和 Provider 注册表完整性测试。

不得把 Token、Webhook URL、Authorization、SMTP 密码或自定义敏感模板写入日志、审计详情和错误响应。新增 URL 型渠道必须复用受限 DNS/拨号逻辑，不能创建默认 `http.Client` 绕过 SSRF 防护。

## 新增备份仓库类型

仓库扩展需要同时核对三层：

1. `web/src/repositories.ts`：字段、默认值、Restic URL、环境变量和选项；
2. `internal/control/repositories.go`：Provider 白名单、URL 语法、允许的环境变量和厂商约束；
3. Agent/Restic：目标 URL 和下发环境是否可直接被当前 Restic 版本识别。

凭据只允许放在加密的 `Environment`/Password 负载中，禁止嵌入 URL。S3 兼容厂商应优先复用 S3 引擎，只有协议或认证确实不同才新增独立执行路径。

## 新增数据源

新增数据源必须形成端到端闭环：

- `internal/domain` 定义强类型配置；
- Control Plane 验证并脱敏 Secret；
- Agent 在受限暂存目录准备数据，返回明确的清理函数；
- UI 表单、项目摘要和编辑回填支持新类型；
- 测试覆盖成功、部分失败、超时、取消和清理路径。

Agent 不接受任意 Shell 字符串。所有外部命令使用参数数组执行，Secret 优先通过受限环境变量或临时配置文件传递，临时文件权限必须为 `0600`，任务退出时必须清理。

## 状态与持久化不变量

- Run 终态不可回退，重复上报必须幂等。
- 仓库维护和备份共享同一把仓库锁，避免 Forget/Prune/Check 与备份并发。
- 数据库更新中涉及“消费一次性凭据、更新安全资料、写审计”等关联状态时必须使用事务。
- 通知投递通过稳定 Dedupe Key 去重；恢复通知只对应同一告警实例。
- 任何返回浏览器的 Repository、数据库来源、通知渠道或认证资料都必须经过公开模型脱敏。

## 修改检查清单

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./internal/control ./internal/agent ./internal/store/...
go vet ./...
npm --prefix web run build
git diff --check
```

涉及依赖更新时再执行 `govulncheck ./...` 和 `npm --prefix web audit --audit-level=high`；涉及镜像或部署文件时执行 Compose 配置校验和容器构建。新功能必须同步 API/运维文档，并优先增加领域级测试，避免只依赖页面手工验证。
