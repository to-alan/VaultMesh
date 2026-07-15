import { requestJSON } from '../api'
import type {
  AlertIncident,
  AuditEvent,
  Dashboard,
  EnrollmentResult,
  NotificationChannel,
  NotificationDelivery,
  Passkey,
  Profile,
  Project,
  ProjectHealth,
  ProjectPolicy,
  Repository,
  Run,
  Schedule,
  Server,
  Snapshot,
} from '../types'

export interface Session {
  username: string
  expires_at: string
}

export interface LoginResult extends Partial<Session> {
  mfa_required?: boolean
}

export interface TOTPSetup {
  secret: string
  uri: string
  qr_code: string
}

export interface RecoveryCodes {
  recovery_codes: string[]
}

export interface CommandAccepted {
  id: string
}

export interface RepositoryCreateInput {
  provider: string
  name: string
  url: string
  password: string
  environment: Record<string, string>
  options: Record<string, string>
}

export interface ProjectSourceInput {
  id?: string
  type: 'files' | 'mysql' | 'postgresql' | 'docker'
  paths?: string[]
  excludes?: string[]
  database?: {
    host: string
    port: number
    username: string
    password: string
    database: string
  }
  docker?: {
    containers: string[]
    include_volumes: boolean
  }
  required: boolean
}

export interface ProjectWriteInput {
  server_id: string
  repository_id: string
  name: string
  sources: ProjectSourceInput[]
  schedule: Schedule
  policy: ProjectPolicy
}

export interface NotificationChannelWriteInput {
  name: string
  type: string
  enabled: boolean
  send_resolved: boolean
  repeat_interval_seconds: number
  event_types: NotificationChannel['event_types']
  project_ids: string[]
  config: Record<string, string>
}

export interface WebAuthnOptionsJSON {
  publicKey: unknown
}

type Items<T> = { items: T[] }

function resourceID(value: string): string {
  return encodeURIComponent(value)
}

function withLimit(path: string, limit: number): string {
  return `${path}?limit=${encodeURIComponent(String(limit))}`
}

async function list<T>(path: string): Promise<T[]> {
  const result = await requestJSON<Items<T>>(path)
  return result.items ?? []
}

export const controlPlane = {
  meta: {
    get: () => requestJSON<{ name: string; version: string; commit: string }>('/api/v1/meta'),
  },

  auth: {
    login: (username: string, password: string) => requestJSON<LoginResult>('/api/v1/auth/login', {
      method: 'POST', body: { username, password },
    }),
    completeTOTP: (code: string) => requestJSON<Session>('/api/v1/auth/totp', {
      method: 'POST', body: { code },
    }),
    beginPasskey: () => requestJSON<WebAuthnOptionsJSON>('/api/v1/auth/passkey/begin', { method: 'POST' }),
    finishPasskey: (credential: unknown) => requestJSON<Session>('/api/v1/auth/passkey/finish', {
      method: 'POST', body: credential,
    }),
    logout: () => requestJSON<void>('/api/v1/auth/logout', { method: 'POST' }),
    session: () => requestJSON<Session>('/api/v1/auth/session'),
  },

  profile: {
    get: () => requestJSON<Profile>('/api/v1/profile'),
    reauthenticate: (input: { password: string; code: string }) => requestJSON<void>('/api/v1/profile/reauthenticate', {
      method: 'POST', body: input,
    }),
    changePassword: (input: { current_password: string; new_password: string; verification_code: string }) => requestJSON<void>('/api/v1/profile/password', {
      method: 'POST', body: input,
    }),
    beginTOTP: (password: string) => requestJSON<TOTPSetup>('/api/v1/profile/totp/begin', {
      method: 'POST', body: { password },
    }),
    enableTOTP: (code: string) => requestJSON<RecoveryCodes>('/api/v1/profile/totp/enable', {
      method: 'POST', body: { code },
    }),
    disableTOTP: (input: { password: string; code: string }) => requestJSON<void>('/api/v1/profile/totp/disable', {
      method: 'POST', body: input,
    }),
    regenerateRecoveryCodes: (input: { password: string; code: string }) => requestJSON<RecoveryCodes>('/api/v1/profile/recovery-codes', {
      method: 'POST', body: input,
    }),
    beginPasskeyRegistration: (name: string) => requestJSON<WebAuthnOptionsJSON>('/api/v1/profile/passkeys/register/begin', {
      method: 'POST', body: { name },
    }),
    finishPasskeyRegistration: (credential: unknown) => requestJSON<Passkey>('/api/v1/profile/passkeys/register/finish', {
      method: 'POST', body: credential,
    }),
    deletePasskey: (passkeyID: string) => requestJSON<void>(`/api/v1/profile/passkeys/${resourceID(passkeyID)}/delete`, {
      method: 'POST',
    }),
  },

  dashboard: {
    get: () => requestJSON<Dashboard>('/api/v1/dashboard'),
  },

  servers: {
    list: () => list<Server>('/api/v1/servers'),
    create: (name: string) => requestJSON<EnrollmentResult>('/api/v1/servers', { method: 'POST', body: { name } }),
  },

  repositories: {
    list: () => list<Repository>('/api/v1/repositories'),
    create: (input: RepositoryCreateInput) => requestJSON<Repository>('/api/v1/repositories', { method: 'POST', body: input }),
  },

  projects: {
    list: () => list<Project>('/api/v1/projects'),
    health: () => list<ProjectHealth>('/api/v1/project-health'),
    create: (input: ProjectWriteInput) => requestJSON<Project>('/api/v1/projects', { method: 'POST', body: input }),
    replace: (projectID: string, input: ProjectWriteInput) => requestJSON<Project>(`/api/v1/projects/${resourceID(projectID)}`, {
      method: 'PUT', body: input,
    }),
    setEnabled: (projectID: string, enabled: boolean) => requestJSON<Project>(`/api/v1/projects/${resourceID(projectID)}`, {
      method: 'PATCH', body: { enabled },
    }),
    run: (projectID: string) => requestJSON<CommandAccepted>(`/api/v1/projects/${resourceID(projectID)}/run`, { method: 'POST' }),
    previewRetention: (projectID: string) => requestJSON<CommandAccepted>(`/api/v1/projects/${resourceID(projectID)}/retention-preview`, { method: 'POST' }),
    refreshSnapshots: (projectID: string) => requestJSON<CommandAccepted>(`/api/v1/projects/${resourceID(projectID)}/snapshots/refresh`, { method: 'POST' }),
    protectSnapshot: (projectID: string, snapshotID: string, protectedValue: boolean) => requestJSON<CommandAccepted>(
      `/api/v1/projects/${resourceID(projectID)}/snapshots/${resourceID(snapshotID)}/protect`,
      { method: 'POST', body: { protected: protectedValue } },
    ),
    browseSnapshot: (projectID: string, snapshotID: string, path: string) => requestJSON<CommandAccepted>(
      `/api/v1/projects/${resourceID(projectID)}/snapshots/${resourceID(snapshotID)}/browse`,
      { method: 'POST', body: { path } },
    ),
    restoreSnapshot: (projectID: string, snapshotID: string, path: string) => requestJSON<CommandAccepted>(
      `/api/v1/projects/${resourceID(projectID)}/snapshots/${resourceID(snapshotID)}/restore`,
      { method: 'POST', body: { path } },
    ),
  },

  runs: {
    list: (limit = 100) => list<Run>(withLimit('/api/v1/runs', limit)),
  },

  snapshots: {
    list: async (limit = 1000, projectID = '') => {
      const query = new URLSearchParams({ limit: String(limit) })
      if (projectID) query.set('project_id', projectID)
      return list<Snapshot>(`/api/v1/snapshots?${query.toString()}`)
    },
  },

  audit: {
    list: (limit = 200) => list<AuditEvent>(withLimit('/api/v1/audit-events', limit)),
  },

  notifications: {
    listChannels: () => list<NotificationChannel>('/api/v1/notification-channels'),
    createChannel: (input: NotificationChannelWriteInput) => requestJSON<NotificationChannel>('/api/v1/notification-channels', {
      method: 'POST', body: input,
    }),
    replaceChannel: (channelID: string, input: NotificationChannelWriteInput) => requestJSON<NotificationChannel>(
      `/api/v1/notification-channels/${resourceID(channelID)}`,
      { method: 'PUT', body: input },
    ),
    setChannelEnabled: (channelID: string, enabled: boolean) => requestJSON<NotificationChannel>(
      `/api/v1/notification-channels/${resourceID(channelID)}`,
      { method: 'PATCH', body: { enabled } },
    ),
    testChannel: (channelID: string) => requestJSON<void>(`/api/v1/notification-channels/${resourceID(channelID)}/test`, {
      method: 'POST',
    }),
    archiveChannel: (channelID: string) => requestJSON<void>(`/api/v1/notification-channels/${resourceID(channelID)}`, {
      method: 'DELETE',
    }),
    listIncidents: (limit = 200) => list<AlertIncident>(withLimit('/api/v1/alert-incidents', limit)),
    listDeliveries: (limit = 200) => list<NotificationDelivery>(withLimit('/api/v1/notification-deliveries', limit)),
    evaluate: () => requestJSON<void>('/api/v1/alerts/evaluate', { method: 'POST' }),
  },
} as const
