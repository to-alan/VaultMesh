export interface NotificationField {
  key: string
  label: string
  type: 'text' | 'password' | 'number' | 'select' | 'textarea'
  required?: boolean
  placeholder?: string
  help?: string
  options?: { label: string; value: string }[]
}

export interface NotificationProvider {
  id: string
  label: string
  group: string
  summary: string
  fields: NotificationField[]
}

const webhookField = (label: string): NotificationField => ({
  key: 'webhook_url', label, type: 'password', required: true,
  placeholder: 'https://…', help: 'URL 通常包含访问令牌，VaultMesh 会整体加密且不会再次返回。',
})

export const notificationProviders: NotificationProvider[] = [
  {
    id: 'webhook', label: '通用 Webhook', group: '通用协议', summary: '自定义 POST/PUT、Header 和消息模板，适合接入自动化平台与自建服务。',
    fields: [
      { key: 'url', label: '目标 URL', type: 'password', required: true, placeholder: 'https://hooks.example.com/vaultmesh' },
      { key: 'method', label: 'HTTP 方法', type: 'select', options: [{ label: 'POST', value: 'POST' }, { label: 'PUT', value: 'PUT' }] },
      { key: 'authorization', label: 'Authorization', type: 'password', placeholder: 'Bearer …', help: '可选；作为 Authorization Header 加密保存。' },
      { key: 'headers', label: '自定义 Headers（JSON）', type: 'textarea', placeholder: '{"X-Environment":"production"}' },
      { key: 'body_template', label: 'Body 模板', type: 'textarea', placeholder: '留空使用标准 JSON；支持 {{title}}、{{message}}、{{transition}}、{{project_name}}、{{severity}}。' },
    ],
  },
  {
    id: 'telegram', label: 'Telegram Bot', group: '即时通讯', summary: '通过 Bot API 发送到私聊、群组或 Topic。',
    fields: [
      { key: 'bot_token', label: 'Bot Token', type: 'password', required: true, placeholder: '123456:ABC…' },
      { key: 'chat_id', label: 'Chat ID', type: 'text', required: true, placeholder: '-1001234567890' },
      { key: 'message_thread_id', label: 'Topic / Thread ID', type: 'number', placeholder: '可选' },
    ],
  },
  {
    id: 'email', label: 'SMTP Email', group: '邮件', summary: '使用用户自己的 SMTP 服务器，可配置 STARTTLS 或直接 TLS。',
    fields: [
      { key: 'smtp_host', label: 'SMTP 主机', type: 'text', required: true, placeholder: 'smtp.example.com' },
      { key: 'smtp_port', label: '端口', type: 'number', required: true, placeholder: '587' },
      { key: 'security', label: '连接安全', type: 'select', options: [{ label: 'STARTTLS', value: 'starttls' }, { label: '直接 TLS', value: 'tls' }, { label: '无加密（仅可信内网）', value: 'none' }] },
      { key: 'username', label: '用户名', type: 'text', placeholder: 'alerts@example.com' },
      { key: 'password', label: '密码 / App Password', type: 'password', placeholder: '留空保持当前密码' },
      { key: 'from', label: '发件人', type: 'text', required: true, placeholder: 'VaultMesh <alerts@example.com>' },
      { key: 'to', label: '收件人', type: 'text', required: true, placeholder: 'ops@example.com, owner@example.com' },
    ],
  },
  { id: 'slack', label: 'Slack Incoming Webhook', group: '即时通讯', summary: '向 Slack Channel 发送结构化文本。', fields: [webhookField('Incoming Webhook URL')] },
  { id: 'discord', label: 'Discord Webhook', group: '即时通讯', summary: '向 Discord Channel 发送故障与恢复消息。', fields: [webhookField('Webhook URL')] },
  { id: 'wecom', label: '企业微信机器人', group: '即时通讯', summary: '使用群机器人 Webhook 发送 Markdown 消息。', fields: [webhookField('机器人 Webhook URL')] },
  { id: 'dingtalk', label: '钉钉机器人', group: '即时通讯', summary: '使用自定义机器人 Webhook 发送 Markdown 消息。', fields: [webhookField('机器人 Webhook URL')] },
  {
    id: 'gotify', label: 'Gotify', group: '自托管推送', summary: '接入自托管 Gotify 服务，支持消息优先级。',
    fields: [
      { key: 'server_url', label: '服务器 URL', type: 'text', required: true, placeholder: 'https://push.example.com' },
      { key: 'token', label: 'Application Token', type: 'password', required: true },
      { key: 'priority', label: '优先级', type: 'number', placeholder: '5' },
    ],
  },
  {
    id: 'ntfy', label: 'ntfy', group: '自托管推送', summary: '发布到 ntfy Topic，可使用 Bearer Token 保护。',
    fields: [
      { key: 'server_url', label: '服务器 URL', type: 'text', required: true, placeholder: 'https://ntfy.sh' },
      { key: 'topic', label: 'Topic', type: 'text', required: true, placeholder: 'vaultmesh-alerts' },
      { key: 'token', label: 'Access Token', type: 'password', placeholder: '可选' },
    ],
  },
]

export const notificationProviderGroups = [...new Set(notificationProviders.map((provider) => provider.group))]

export function notificationProvider(id: string): NotificationProvider {
  return notificationProviders.find((provider) => provider.id === id) ?? notificationProviders[0]
}

export function notificationDefaults(type: string): Record<string, string> {
  const values: Record<string, string> = {}
  for (const field of notificationProvider(type).fields) {
    if (field.key === 'method') values[field.key] = 'POST'
    if (field.key === 'smtp_port') values[field.key] = '587'
    if (field.key === 'security') values[field.key] = 'starttls'
    if (field.key === 'priority') values[field.key] = '5'
  }
  return values
}
