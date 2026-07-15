import { clock, lines } from '../display'
import type { SourceType } from '../display'
import type { ProjectWriteInput } from '../services'
import type { Project } from '../types'

export type ScheduleMode = 'daily' | 'weekly' | 'custom'

export interface ProjectSourceDraft {
  key: number
  id?: string
  type: SourceType
  required: boolean
  paths: string
  excludes: string
  host: string
  port: number
  username: string
  password: string
  database: string
  password_configured: boolean
  containers: string
  include_volumes: boolean
}

export interface ProjectFormDraft {
  server_id: string
  repository_id: string
  name: string
  sources: ProjectSourceDraft[]
  schedule_mode: ScheduleMode
  schedule_time: string
  weekday: string
  custom_cron: string
  timezone: string
  jitter_minutes: number
  max_runtime_hours: number
  grace_minutes: number
  one_file_system: boolean
  exclude_caches: boolean
  exclude_if_present: string
  exclude_larger_than: string
  retention_enabled: boolean
  retention_mode: 'count' | 'smart' | 'gfs' | 'age'
  keep_last: number
  keep_hourly: number
  keep_daily: number
  keep_weekly: number
  keep_monthly: number
  keep_yearly: number
  keep_within: string
  prune: boolean
  verification_mode: 'off' | 'metadata' | 'subset' | 'full'
  read_data_subset: string
  retention_cron: string
  prune_cron: string
  verification_cron: string
}

let sourceSequence = 0

export function createProjectSourceDraft(type: SourceType): ProjectSourceDraft {
  return {
    key: ++sourceSequence,
    type,
    required: true,
    paths: '/etc',
    excludes: '',
    host: '127.0.0.1',
    port: databasePort(type),
    username: '',
    password: '',
    database: '',
    password_configured: false,
    containers: '',
    include_volumes: true,
  }
}

export function createProjectFormDraft(serverID = '', repositoryID = ''): ProjectFormDraft {
  return {
    server_id: serverID,
    repository_id: repositoryID,
    name: '',
    sources: [createProjectSourceDraft('files')],
    schedule_mode: 'daily',
    schedule_time: '02:00',
    weekday: '1',
    custom_cron: '0 2 * * *',
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    jitter_minutes: 5,
    max_runtime_hours: 6,
    grace_minutes: 60,
    one_file_system: true,
    exclude_caches: true,
    exclude_if_present: '.nobackup',
    exclude_larger_than: '',
    retention_enabled: true,
    retention_mode: 'count',
    keep_last: 14,
    keep_hourly: 0,
    keep_daily: 7,
    keep_weekly: 4,
    keep_monthly: 12,
    keep_yearly: 3,
    keep_within: '90d',
    prune: false,
    verification_mode: 'off',
    read_data_subset: '1%',
    retention_cron: '30 3 * * *',
    prune_cron: '0 4 * * 0',
    verification_cron: '0 5 * * 0',
  }
}

export function projectFormDraftFromProject(project: Project): ProjectFormDraft {
  const daily = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(project.schedule.cron)
  const weekly = /^(\d{1,2}) (\d{1,2}) \* \* ([0-6])$/.exec(project.schedule.cron)
  const retention = project.policy?.retention
  const verification = project.policy?.verification
  const maintenance = project.policy?.maintenance
  const backup = project.policy?.backup
  return {
    server_id: project.server_id,
    repository_id: project.repository_id,
    name: project.name,
    sources: project.sources.map((source) => ({
      key: ++sourceSequence,
      id: source.id,
      type: source.type,
      required: source.required,
      paths: source.paths?.join('\n') || '',
      excludes: source.excludes?.join('\n') || '',
      host: source.database?.host || '127.0.0.1',
      port: source.database?.port || databasePort(source.type),
      username: source.database?.username || '',
      password: '',
      database: source.database?.database || '',
      password_configured: Boolean(source.database),
      containers: source.docker?.containers.join('\n') || '',
      include_volumes: source.docker?.include_volumes ?? true,
    })),
    schedule_mode: daily ? 'daily' : weekly ? 'weekly' : 'custom',
    schedule_time: daily ? clock(daily[2], daily[1]) : weekly ? clock(weekly[2], weekly[1]) : '02:00',
    weekday: weekly?.[3] || '1',
    custom_cron: project.schedule.cron,
    timezone: project.schedule.timezone,
    jitter_minutes: Math.round(project.schedule.jitter_seconds / 60),
    max_runtime_hours: Math.max(1, Math.round(project.schedule.max_runtime_seconds / 3600)),
    grace_minutes: Math.max(1, Math.round((project.schedule.grace_seconds || 3600) / 60)),
    one_file_system: backup?.one_file_system ?? true,
    exclude_caches: backup?.exclude_caches ?? true,
    exclude_if_present: backup?.exclude_if_present?.join('\n') || '',
    exclude_larger_than: backup?.exclude_larger_than || '',
    retention_enabled: retention?.enabled ?? false,
    retention_mode: retention?.mode || 'gfs',
    keep_last: retention?.keep_last ?? 0,
    keep_hourly: retention?.keep_hourly ?? 0,
    keep_daily: retention?.keep_daily ?? 0,
    keep_weekly: retention?.keep_weekly ?? 0,
    keep_monthly: retention?.keep_monthly ?? 0,
    keep_yearly: retention?.keep_yearly ?? 0,
    keep_within: retention?.keep_within || '90d',
    prune: retention?.prune ?? false,
    verification_mode: verification?.mode || 'off',
    read_data_subset: verification?.read_data_subset || '1%',
    retention_cron: maintenance?.retention_cron || '30 3 * * *',
    prune_cron: maintenance?.prune_cron || '0 4 * * 0',
    verification_cron: maintenance?.verification_cron || '0 5 * * 0',
  }
}

export function buildProjectCron(form: ProjectFormDraft): string {
  if (form.schedule_mode === 'custom') return form.custom_cron.trim()
  const match = /^(\d{2}):(\d{2})$/.exec(form.schedule_time)
  const hour = Number(match?.[1] ?? 2)
  const minute = Number(match?.[2] ?? 0)
  if (form.schedule_mode === 'weekly') return `${minute} ${hour} * * ${form.weekday}`
  return `${minute} ${hour} * * *`
}

export function projectWriteInput(form: ProjectFormDraft): ProjectWriteInput {
  return {
    server_id: form.server_id,
    repository_id: form.repository_id,
    name: form.name,
    sources: form.sources.map((source) => {
      if (source.type === 'files') {
        return {
          id: source.id, type: source.type, paths: lines(source.paths), excludes: lines(source.excludes), required: source.required,
        }
      }
      if (source.type === 'docker') {
        return {
          id: source.id, type: source.type,
          docker: { containers: lines(source.containers), include_volumes: source.include_volumes },
          required: source.required,
        }
      }
      return {
        id: source.id,
        type: source.type,
        database: {
          host: source.host,
          port: Number(source.port),
          username: source.username,
          password: source.password,
          database: source.database,
        },
        required: source.required,
      }
    }),
    schedule: {
      cron: buildProjectCron(form),
      timezone: form.timezone,
      jitter_seconds: Number(form.jitter_minutes) * 60,
      max_runtime_seconds: Number(form.max_runtime_hours) * 3600,
      grace_seconds: Number(form.grace_minutes) * 60,
      missed_run_policy: 'skip',
      concurrency_policy: 'forbid',
    },
    policy: {
      backup: {
        one_file_system: form.one_file_system,
        exclude_caches: form.exclude_caches,
        exclude_if_present: lines(form.exclude_if_present),
        exclude_larger_than: form.exclude_larger_than.trim(),
      },
      retention: {
        enabled: form.retention_enabled,
        mode: form.retention_mode,
        keep_last: Number(form.keep_last),
        keep_hourly: Number(form.keep_hourly),
        keep_daily: Number(form.keep_daily),
        keep_weekly: Number(form.keep_weekly),
        keep_monthly: Number(form.keep_monthly),
        keep_yearly: Number(form.keep_yearly),
        keep_within: form.keep_within.trim(),
        prune: form.prune,
      },
      verification: {
        mode: form.verification_mode,
        read_data_subset: form.verification_mode === 'subset' ? form.read_data_subset : '',
      },
      maintenance: {
        separate: true,
        timezone: form.timezone,
        retention_cron: form.retention_enabled ? form.retention_cron.trim() : '',
        prune_cron: form.retention_enabled && form.prune ? form.prune_cron.trim() : '',
        verification_cron: form.verification_mode !== 'off' ? form.verification_cron.trim() : '',
      },
    },
  }
}

export function changeProjectSourceType(source: ProjectSourceDraft): void {
  source.port = databasePort(source.type)
  source.password = ''
  source.password_configured = false
}

function databasePort(type: SourceType): number {
  return type === 'mysql' ? 3306 : type === 'postgresql' ? 5432 : 0
}
