import type { Dashboard, Profile, Project, Run, Server } from '../types'

export const testServer: Server = {
  id: 'srv_test', name: '香港节点', hostname: 'backup-hk', status: 'online', agent_version: 'v0.1.2',
  desired_revision: 3, applied_revision: 3, created_at: '2026-09-01T00:00:00Z',
}
export const testProfile: Profile = {
  username: 'admin', totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], webauthn_available: false, webauthn_rp_id: '',
}
export const testDashboard: Dashboard = {
  servers_total: 1, servers_online: 1, projects_total: 1, runs_succeeded: 12, runs_failed: 2, runs_partial: 1, projects_late: 0, projects_overdue: 1,
}

export function makeProject(overrides: Partial<Project> = {}): Project {
  return {
    id: 'prj_test', name: '业务数据库', server_id: testServer.id, repository_id: 'repo_test', enabled: true,
    sources: [{ id: 'src_test', type: 'files', paths: ['/srv/application'], required: true }],
    schedule: { cron: '0 2 * * *', timezone: 'Asia/Shanghai', jitter_seconds: 61, max_runtime_seconds: 5401, grace_seconds: 301, missed_run_policy: 'skip', concurrency_policy: 'forbid' },
    policy: {
      backup: { one_file_system: true, exclude_caches: true },
      retention: { enabled: true, mode: 'count', keep_last: 14, keep_hourly: 0, keep_daily: 0, keep_weekly: 0, keep_monthly: 0, keep_yearly: 0, prune: true },
      verification: { mode: 'subset', read_data_subset: '37%' },
      maintenance: { separate: true, timezone: 'UTC', retention_cron: '30 3 * * *', prune_cron: '0 4 * * 0', verification_cron: '0 5 * * 0' },
    },
    revision: 3, created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z',
    ...overrides,
  }
}

export function makeRun(status = 'succeeded', overrides: Partial<Run> = {}): Run {
  return {
    id: `run_${status}`, idempotency_key: `test:${status}`, project_id: 'prj_test', server_id: testServer.id, status,
    scheduled_at: '2026-09-08T02:00:00Z',
    started_at: '2026-09-08T02:00:00Z', finished_at: status === 'running' ? undefined : '2026-09-08T02:05:00Z',
    stats: { operation: 'backup' }, ...overrides,
  }
}
