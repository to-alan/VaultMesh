export interface Dashboard {
  servers_total: number
  servers_online: number
  projects_total: number
  runs_succeeded: number
  runs_failed: number
  runs_partial: number
  projects_late: number
  projects_overdue: number
}

export interface Server {
  id: string
  name: string
  hostname?: string
  os?: string
  arch?: string
  agent_version?: string
  status: 'pending' | 'online' | 'offline'
  last_seen_at?: string
  desired_revision: number
  applied_revision: number
  created_at: string
}

export interface Repository {
  id: string
  provider: string
  name: string
  url: string
  created_at: string
}

export interface Source {
  id?: string
  type: 'files' | 'mysql' | 'postgresql' | 'docker'
  paths?: string[]
  excludes?: string[]
  database?: {
    host: string
    port: number
    username: string
    database: string
  }
  docker?: {
    containers: string[]
    include_volumes: boolean
  }
  required: boolean
}

export interface Schedule {
  cron: string
  timezone: string
  jitter_seconds: number
  max_runtime_seconds: number
  grace_seconds: number
  missed_run_policy: 'skip'
  concurrency_policy: 'forbid'
}

export interface ProjectPolicy {
  backup: {
    one_file_system: boolean
    exclude_caches: boolean
    exclude_if_present?: string[]
    exclude_larger_than?: string
  }
  retention: {
    enabled: boolean
    mode: 'count' | 'smart' | 'gfs' | 'age'
    keep_last: number
    keep_hourly: number
    keep_daily: number
    keep_weekly: number
    keep_monthly: number
    keep_yearly: number
    keep_within?: string
    prune: boolean
  }
  verification: {
    mode: 'off' | 'metadata' | 'subset' | 'full'
    read_data_subset?: string
  }
  maintenance: {
    separate: boolean
    timezone?: string
    retention_cron?: string
    prune_cron?: string
    verification_cron?: string
  }
}

export interface Project {
  id: string
  server_id: string
  repository_id: string
  name: string
  enabled: boolean
  sources: Source[]
  schedule: Schedule
  policy?: ProjectPolicy
  revision: number
  next_run_at?: string
  created_at: string
  updated_at: string
}

export interface ProjectHealth {
  project_id: string
  status: 'healthy' | 'pending' | 'late' | 'overdue' | 'paused' | 'invalid'
  latest_run_status?: string
  latest_run_at?: string
  last_successful_at?: string
  expected_at?: string
  deadline_at?: string
}

export interface Run {
  id: string
  idempotency_key: string
  project_id: string
  server_id: string
  scheduled_at: string
  started_at: string
  finished_at?: string
  status: string
  snapshot_id?: string
  error_code?: string
  error_message?: string
  stats?: Record<string, unknown>
}

export interface AuditEvent {
  id: string
  actor: string
  action: string
  resource_type?: string
  resource_id?: string
  outcome: 'succeeded' | 'failed'
  client_ip: string
  status_code: number
  created_at: string
}

export interface NotificationChannel {
  id: string
  name: string
  type: string
  enabled: boolean
  send_resolved: boolean
  repeat_interval_seconds: number
  event_types: ('backup_failure' | 'rpo_overdue')[]
  project_ids?: string[]
  config?: Record<string, string>
  destination?: string
  configured: boolean
  created_at: string
  updated_at: string
}

export interface AlertIncident {
  id: string
  fingerprint: string
  kind: 'backup_failure' | 'rpo_overdue'
  project_id: string
  project_name: string
  status: 'firing' | 'resolved'
  severity: 'warning' | 'critical' | string
  summary: string
  description: string
  source_event_id: string
  occurrence_count: number
  started_at: string
  updated_at: string
  resolved_at?: string
}

export interface NotificationDelivery {
  id: string
  alert_id: string
  channel_id: string
  channel_name?: string
  transition: 'firing' | 'repeat' | 'resolved'
  status: 'pending' | 'delivering' | 'sent' | 'failed'
  attempt_count: number
  next_attempt_at: string
  last_error?: string
  created_at: string
  sent_at?: string
}

export interface Snapshot {
  id: string
  project_id: string
  server_id: string
  time: string
  hostname: string
  username?: string
  paths: string[]
  tags: string[]
  total_files?: number
  total_bytes?: number
  protected: boolean
  last_synced_at: string
}

export interface SnapshotEntry {
  name: string
  path: string
  type: 'dir' | 'file' | 'symlink' | string
  size: number
  mode?: number
  permissions?: string
  modified_at?: string
}

export interface EnrollmentResult {
  server: Server
  enrollment_token: string
  expires_at: string
}

export interface Passkey {
  id: string
  name: string
  created_at: string
  last_used_at?: string
}

export interface Profile {
  username: string
  totp_enabled: boolean
  recovery_codes_remaining: number
  passkeys: Passkey[]
  webauthn_available: boolean
  webauthn_rp_id: string
}
