# 通知与告警

VaultMesh 把“告警状态”和“消息发送”分开处理：项目事实先形成 Incident，再根据用户定义的 Contact Point 创建持久化 Delivery。通知服务故障不会把一次已经成功的备份改成失败，也不会阻塞 Agent 上报。

## 设计依据

这套模型没有自创一套陌生概念，而是采用成熟项目中已经验证的共同部分：

- [Grafana Alerting Contact points](https://grafana.com/docs/grafana/latest/alerting/fundamentals/notifications/contact-points/) 将通知目标建模为可复用联系点，并允许一个联系点包含具体集成；
- [Prometheus Alertmanager configuration](https://prometheus.io/docs/alerting/latest/configuration/) 明确定义了分组、`repeat_interval` 和 `send_resolved`，对应 VaultMesh 的稳定指纹、重复提醒周期与恢复通知；
- [Healthchecks integrations](https://healthchecks.io/docs/configuring_notifications/) 展示了备份/定时任务监控常用的 Email、Webhook、Slack、Discord、Telegram 等渠道；其 [Webhook 实现说明](https://blog.healthchecks.io/2024/10/how-healthchecks-io-sends-webhook-notifications/) 也说明了方法、Header 和 Body 模板的实际需求；
- [Apprise](https://github.com/caronc/apprise) 证明了使用统一通知层适配大量服务是成熟做法。VaultMesh 当前只原生实现最常用且可维护的子集，没有直接嵌入其代码或依赖。

VaultMesh 仍保留自己的领域边界：告警只能由控制面事实产生，渠道不能执行任意命令，消息发送也不能改变备份运行结果。

## 支持的渠道与字段

| 渠道 | 必填字段 | 可选字段 | 发送方式 |
|---|---|---|---|
| 通用 Webhook | URL | POST/PUT、Authorization、自定义 Header JSON、Body 模板、私网访问开关 | 标准 JSON envelope 或用户模板 |
| Telegram Bot | Bot Token、Chat ID | Topic / Thread ID | Bot API `sendMessage` |
| SMTP Email | Host、Port、From、To | STARTTLS/直接 TLS/无加密、用户名、密码、私网访问开关 | UTF-8 纯文本邮件 |
| Slack | Incoming Webhook URL | — | Incoming Webhook |
| Discord | Webhook URL | — | Discord Webhook |
| 企业微信 | 群机器人 Webhook URL | — | Markdown 群机器人消息 |
| 钉钉 | 自定义机器人 Webhook URL | — | Markdown 机器人消息 |
| Gotify | Server URL、Application Token | Priority、私网访问开关 | `/message` API |
| ntfy | Server URL、Topic | Access Token、私网访问开关 | Topic HTTP publish |

Webhook URL、令牌和密码通常本身就是凭据。VaultMesh 将渠道配置整体加密保存，管理 API 只返回主机名或收件人等目的地摘要。编辑同类型渠道时，敏感字段留空表示保留原值，而不是清空。

每种渠道只接受表中定义的配置字段。未知字段直接返回校验错误，不会因为“后端暂时不用”而被保存后作为普通字段回显；这避免调用方误把自定义 API Key 放进非 Secret 字段。

当前钉钉适配使用包含访问令牌的机器人 Webhook，不实现独立 HMAC 签名参数；SMTP 支持普通账号密码或 App Password，不实现 OAuth。需要其他服务时优先使用通用 Webhook，或把 VaultMesh Webhook 接到用户自己的 Apprise/API 网关。

## 路由规则

每个渠道分别设置：

| 规则 | 语义 |
|---|---|
| `enabled` | 暂停后不再创建或发送新投递 |
| `event_types` | 当前可选 `backup_failure`、`rpo_overdue` |
| `project_ids` | 空数组表示所有项目；非空表示项目 allowlist |
| `repeat_interval_seconds` | 持续异常的提醒周期，5 分钟至 7 天，默认 4 小时 |
| `send_resolved` | 异常恢复后是否发送恢复消息 |
| `allow_private_address` | 默认关闭；仅在目标是明确受信的自建内网服务时开启 |

成功备份默认不发送消息。它只会在之前存在 firing Incident 时触发一次恢复通知，避免把正常运行变成消息噪声。

## Incident、去重与恢复

当前自动产生两类 Incident：

| 事件 | 指纹 | 触发 | 恢复 |
|---|---|---|---|
| 备份失败 | `backup:<project_id>` | 最近一次备份为 `partial`、`failed`、`timed_out`、`unknown` 或 `canceled` | 出现更新的成功备份，或项目被暂停 |
| RPO 超时 | `rpo:<project_id>` | 项目健康状态进入 `overdue` | 项目不再 `overdue`，或项目被暂停 |

同一指纹在 firing 期间只对应一个 Incident。状态流转为：

```text
正常 → firing（立即通知）→ firing（抑制）→ repeat（到达周期）→ resolved（恢复通知）
```

后续失败运行会增加 Incident 的发生次数，但不会绕过去重规则制造消息风暴。Incident 恢复后再次发生，会创建新的 Incident，因此新的故障仍会立即通知。

## 持久化投递与重试

每条渠道消息先写入 `notification_deliveries`，再由 Control Plane 的后台 Worker 认领发送。唯一 dedupe key 防止相同 Incident、渠道、转换和重复时间桶被重复入队。投递使用租约认领，进程在发送中退出后可由后续 Worker 重新认领。

一次投递最多尝试五次：首次立即发送，失败后分别等待 1 分钟、5 分钟、15 分钟和 1 小时。只有 HTTP 2xx 或 SMTP 完整提交才标记为 `sent`；随后仍失败则进入终态 `failed`，由管理员在 UI 查看。渠道被停用或归档后，已经排队但未发送的投递不会继续外发。

## 安全边界

- 渠道配置使用 `VAULTMESH_MASTER_KEY` 加密；丢失主密钥后无法恢复这些凭据；
- API 不返回 URL 中的令牌、Bot Token、Authorization、自定义 Header、Body 模板或 SMTP 密码；
- HTTP 客户端 10 秒超时、不使用环境代理且不跟随重定向，错误摘要不包含目标 URL；
- Webhook 禁止内嵌 URL 用户名/密码，也禁止覆盖 `Host`、`Content-Length` 和逐跳传输 Header；
- 出站连接先解析全部 A/AAAA 地址再直接连接已验证的 IP，避免 DNS 重绑定在校验后切换地址；默认拒绝回环与私有地址，只有管理员显式开启私网访问后才允许；
- 链路本地、组播、未指定地址和云元数据地址始终拒绝，私网开关不能绕过；生产防火墙仍应只放行实际通知目标；
- 通知正文只使用受控 Incident 字段，不包含仓库 Secret、数据库密码或运行日志；
- 通知渠道属于管理员能力。生产环境应通过出口防火墙限制 Control Plane 可访问的目标。

## 当前边界

- 尚未根据 Agent 离线、仓库 Check 失败、恢复演练过期产生 Incident；
- 尚无静默时段、维护窗口、告警确认和多级升级链；
- 一个渠道就是一个联系点，尚未提供 Grafana 风格的联系点组合与路由树；
- 投递与 Incident 都保存在控制面 PostgreSQL，生产环境必须把它们纳入控制面灾备。

这些能力应在现有两类告警经过真实故障演练后扩展，不能用更多渠道掩盖错误的健康状态或未验证的恢复链路。
