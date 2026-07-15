import type { AuditEvent, Project, ProjectHealth, Run, SnapshotEntry } from './types'

export type Tab = 'overview' | 'servers' | 'repositories' | 'projects' | 'snapshots' | 'runs' | 'notifications' | 'audit' | 'profile'
export type SourceType = 'files' | 'mysql' | 'postgresql' | 'docker'
export type AuditCategory = 'all' | 'authentication' | 'security' | 'configuration' | 'backup'
export type RunOperationFilter = 'all' | 'backup' | 'maintenance' | 'recovery'

const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

export function lines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
}

export function formatDate(value?: string): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value))
}

export function formatBytes(value?: number): string {
  const bytes = Number(value || 0)
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  const size = bytes / 1024 ** index
  return `${size >= 10 || index === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[index]}`
}

export function snapshotEntryName(entry: SnapshotEntry): string {
  return entry.name || entry.path.split('/').filter(Boolean).at(-1) || '/'
}

export function snapshotEntryIcon(entry: SnapshotEntry): string {
  if (entry.type === 'dir') return '▰'
  if (entry.type === 'symlink') return '↗'
  return '▪'
}

export function localDateKey(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

export function healthOrder(status: string): number {
  if (['failed', 'timed_out', 'unknown'].includes(status)) return 0
  if (['partial', 'running'].includes(status)) return 1
  return 2
}

export function formatDuration(run: Run): string {
  if (!run.finished_at) return run.status === 'running' ? '执行中' : '—'
  const seconds = Math.max(0, Math.round((new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  return `${minutes}m ${seconds % 60}s`
}

export function formatCountdown(milliseconds: number): string {
  const totalSeconds = Math.floor(milliseconds / 1000)
  const days = Math.floor(totalSeconds / 86400)
  const hours = Math.floor(totalSeconds % 86400 / 3600)
  const minutes = Math.floor(totalSeconds % 3600 / 60)
  const seconds = totalSeconds % 60
  const clock = [hours, minutes, seconds].map((value) => String(value).padStart(2, '0')).join(':')
  return days ? `${days}天 ${clock}` : clock
}

export function projectHealthLabel(health?: ProjectHealth): string {
  const labels: Record<string, string> = {
    healthy: 'RPO 正常',
    pending: '等待首次备份',
    late: '备份迟到',
    overdue: 'RPO 已超时',
    paused: '监控已暂停',
    invalid: '计划无效',
  }
  return labels[health?.status || ''] || '状态计算中'
}

export function describeProjectHealth(health: ProjectHealth | undefined, nowEpoch: number): string {
  if (!health) return '正在计算计划健康状态'
  if (health.status === 'paused') return '项目暂停后不计算 RPO'
  if (health.status === 'invalid') return 'Cron 或时区无法解析，请编辑项目修复'
  if (health.status === 'overdue' && health.deadline_at) {
    return `超过完成时限 ${formatCountdown(Math.max(0, nowEpoch - new Date(health.deadline_at).getTime()))}`
  }
  if (health.status === 'late' && health.deadline_at) {
    return `宽限窗口剩余 ${formatCountdown(Math.max(0, new Date(health.deadline_at).getTime() - nowEpoch))}`
  }
  if (health.expected_at) return `${health.status === 'pending' ? '首次应执行' : '下次应执行'} ${formatDate(health.expected_at)}`
  return '等待有效的执行计划'
}

export function pageDescription(tab: Tab): string {
  return {
    overview: '运行态势、成功率与风险信号',
    servers: 'Agent 在线状态与配置收敛',
    repositories: 'Restic 目标与凭据边界',
    projects: '数据源、调度策略与下次执行',
    snapshots: '快照索引、文件浏览与隔离恢复',
    runs: '端到端备份执行证据',
    notifications: '联系点、告警去重、重复提醒与恢复通知',
    audit: '管理员、安全与恢复操作的持久化证据',
    profile: '密码、二步验证与通行密钥',
  }[tab]
}

export function auditActionCategory(action: string): Exclude<AuditCategory, 'all'> {
  if (action.startsWith('auth.') || action === 'agent.enroll') return 'authentication'
  if (action.startsWith('security.')) return 'security'
  if (action.startsWith('backup.') || action.startsWith('retention.') || action.startsWith('snapshot.')) return 'backup'
  return 'configuration'
}

export function auditActionLabel(action: string): string {
  return {
    'auth.password': '密码登录',
    'auth.second_factor': '二步验证登录',
    'auth.passkey': '通行密钥登录',
    'auth.logout': '退出登录',
    'agent.enroll': 'Agent 注册',
    'security.reauthenticate': '敏感操作重新认证',
    'security.password.change': '修改管理员密码',
    'security.totp.setup.begin': '开始设置二步验证',
    'security.totp.enable': '启用二步验证',
    'security.totp.disable': '停用二步验证',
    'security.recovery_codes.regenerate': '重新生成恢复码',
    'security.passkey.register.begin': '开始注册通行密钥',
    'security.passkey.register': '注册通行密钥',
    'security.passkey.delete': '移除通行密钥',
    'server.create': '创建服务器',
    'repository.create': '创建备份仓库',
    'project.create': '创建备份项目',
    'project.update': '更新备份项目',
    'notification.channel.create': '创建通知渠道',
    'notification.channel.update': '更新通知渠道',
    'notification.channel.archive': '归档通知渠道',
    'notification.channel.test': '测试通知渠道',
    'notification.alert.evaluate': '手动评估告警',
    'backup.run': '立即执行备份',
    'retention.preview': '预览保留策略',
    'snapshot.refresh': '同步快照索引',
    'snapshot.protect': '修改快照保护',
    'snapshot.browse': '浏览快照',
    'snapshot.restore': '创建恢复任务',
  }[action] ?? action
}

export function auditCategoryLabel(category: ReturnType<typeof auditActionCategory>): string {
  return { authentication: '认证', security: '安全', configuration: '配置', backup: '备份与恢复' }[category]
}

export function auditResourceLabel(event: AuditEvent): string {
  const type = {
    account: '管理员账号',
    server: '服务器',
    repository: '备份仓库',
    project: '备份项目',
    snapshot: '快照',
    passkey: '通行密钥',
    notification_channel: '通知渠道',
  }[event.resource_type || '']
  if (!type) return '—'
  return event.resource_id ? `${type} · ${event.resource_id}` : type
}

export function sourceTypeLabel(type: SourceType): string {
  return { files: '文件与目录', mysql: 'MySQL', postgresql: 'PostgreSQL', docker: 'Docker' }[type]
}

export function sourceSummary(source: Project['sources'][number]): string {
  if (source.type === 'files') {
    const paths = source.paths ?? []
    const visible = paths.slice(0, 2).join(', ')
    return `文件 · ${visible || '未配置路径'}${paths.length > 2 ? ` +${paths.length - 2}` : ''}`
  }
  if (source.type === 'docker') {
    const containers = source.docker?.containers ?? []
    return `Docker · ${containers.slice(0, 2).join(', ') || '未配置容器'}${containers.length > 2 ? ` +${containers.length - 2}` : ''}`
  }
  const database = source.database
  if (!database) return sourceTypeLabel(source.type)
  return `${sourceTypeLabel(source.type)} · ${database.database}@${database.host}:${database.port}`
}

export function retentionSummary(project: Project): string {
  const retention = project.policy?.retention
  if (!retention?.enabled) return '不自动清理'
  switch (retention.mode || 'gfs') {
    case 'count': return `最多 ${retention.keep_last} 份`
    case 'smart': return '智能：日 7 / 周 4 / 月 12'
    case 'age': return `保留最近 ${retention.keep_within}`
    default: return `GFS · 最近 ${retention.keep_last || 0} / 日 ${retention.keep_daily || 0} / 周 ${retention.keep_weekly || 0} / 月 ${retention.keep_monthly || 0}`
  }
}

export function runOperationLabel(run: Run): string {
  return {
    retention_preview: '清理预览',
    retention: '快照清理',
    prune: '空间回收',
    verification: '仓库校验',
    snapshot_sync: '快照同步',
    snapshot_protect: '快照保护',
    snapshot_browse: '目录浏览',
    snapshot_restore: '安全恢复',
  }[String(run.stats?.operation || '')] || '备份'
}

export function runOperationGroup(run: Run): Exclude<RunOperationFilter, 'all'> {
  const operation = String(run.stats?.operation || 'backup')
  if (['retention_preview', 'retention', 'prune', 'verification'].includes(operation)) return 'maintenance'
  if (['snapshot_browse', 'snapshot_restore'].includes(operation)) return 'recovery'
  return 'backup'
}

export function maintenanceSummary(project: Project): string {
  const maintenance = project.policy?.maintenance
  if (!maintenance?.separate) return '备份后执行（兼容模式）'
  const tasks = []
  if (project.policy?.retention.enabled) tasks.push(`清理 ${maintenance.retention_cron || '—'}`)
  if (project.policy?.retention.prune) tasks.push(`Prune ${maintenance.prune_cron || '—'}`)
  if (project.policy?.verification.mode && project.policy.verification.mode !== 'off') tasks.push(`校验 ${maintenance.verification_cron || '—'}`)
  return tasks.join(' · ') || '无维护任务'
}

export function verificationSummary(project: Project): string {
  const verification = project.policy?.verification
  if (!verification || verification.mode === 'off') return '关闭'
  if (verification.mode === 'metadata') return '仓库结构'
  if (verification.mode === 'subset') return `抽样 ${verification.read_data_subset || '—'}`
  return '完整数据'
}

export function scanSummary(project: Project): string {
  const backup = project.policy?.backup
  return `${backup?.one_file_system ? '不跨文件系统' : '允许跨文件系统'} · ${backup?.exclude_caches ? '忽略缓存' : '包含缓存'}`
}

export function cronDescription(cron: string): string {
  const daily = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(cron)
  if (daily) return `每天 ${clock(daily[2], daily[1])}`
  const weekly = /^(\d{1,2}) (\d{1,2}) \* \* ([0-6])$/.exec(cron)
  if (weekly) return `每${weekdays[Number(weekly[3])]} ${clock(weekly[2], weekly[1])}`
  return `Cron ${cron}`
}

export function clock(hour: string, minute: string): string {
  return `${hour.padStart(2, '0')}:${minute.padStart(2, '0')}`
}

export function formatNextRun(project: Project): string {
  if (!project.enabled) return '项目已暂停'
  if (!project.next_run_at) return '等待 Agent 应用计划'
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: project.schedule.timezone,
      month: 'numeric',
      day: 'numeric',
      weekday: 'short',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    }).format(new Date(project.next_run_at))
  } catch {
    return formatDate(project.next_run_at)
  }
}

export function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    pending: '待注册', online: '在线', offline: '离线', running: '执行中',
    succeeded: '成功', partial: '部分成功', failed: '失败', timed_out: '超时',
    canceled: '已取消', unknown: '状态未知',
  }
  return labels[status] ?? status
}

export function alertKindLabel(kind: string): string {
  return kind === 'rpo_overdue' ? 'RPO 超时' : kind === 'backup_failure' ? '备份失败' : kind
}

export function notificationTransitionLabel(transition: string): string {
  return transition === 'resolved' ? '恢复' : transition === 'repeat' ? '重复提醒' : '首次告警'
}

export function repeatIntervalLabel(seconds: number): string {
  const minutes = Math.round(seconds / 60)
  if (minutes % 1440 === 0) return `${minutes / 1440} 天`
  if (minutes % 60 === 0) return `${minutes / 60} 小时`
  return `${minutes} 分钟`
}
