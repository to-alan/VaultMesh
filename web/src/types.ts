export interface Dashboard {
  servers_total: number
  servers_online: number
  projects_total: number
  runs_succeeded: number
  runs_failed: number
  runs_partial: number
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
  provider: 'cloudflare_r2' | 's3_compatible'
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
  missed_run_policy: 'skip' | 'run_once'
  concurrency_policy: 'forbid'
}

export interface Project {
  id: string
  server_id: string
  repository_id: string
  name: string
  enabled: boolean
  sources: Source[]
  schedule: Schedule
  revision: number
  next_run_at?: string
  created_at: string
  updated_at: string
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
}
