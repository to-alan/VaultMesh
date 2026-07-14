<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { APIError, api, getAPIBaseURL } from './api'
import {
  buildRepositoryEnvironment,
  buildRepositoryOptions,
  buildRepositoryURL as buildRepositoryTarget,
  engineLabel,
  missingRepositoryFields,
  repositoryDefaults,
  repositoryFieldVisible,
  repositoryProvider,
  repositoryProviderGroups,
  repositoryProviders,
} from './repositories'
import type { Dashboard, EnrollmentResult, Passkey, Profile, Project, Repository, Run, Server, Snapshot, SnapshotEntry } from './types'

type Tab = 'overview' | 'servers' | 'repositories' | 'projects' | 'snapshots' | 'runs' | 'profile'
type SourceType = 'files' | 'mysql' | 'postgresql' | 'docker'
type ScheduleMode = 'daily' | 'weekly' | 'custom'
type SecurityModal = 'password' | 'totp-setup' | 'totp-manage' | 'passkey-add' | 'passkey-delete' | 'reauthenticate' | null
type PendingPasskeyAction = 'add' | 'delete' | null

interface ProjectSourceDraft {
  key: number
  type: SourceType
  required: boolean
  paths: string
  excludes: string
  host: string
  port: number
  username: string
  password: string
  database: string
  containers: string
  include_volumes: boolean
}

let sourceSequence = 0

const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
const commonTimezones = ['Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Tokyo', 'UTC', 'Europe/London', 'America/New_York']
const apiBaseURL = getAPIBaseURL()
const currentOrigin = window.location.origin

const usernameInput = ref('admin')
const passwordInput = ref('')
const mfaCode = ref('')
const mfaLoginRequired = ref(false)
const authenticated = ref(false)
const authReady = ref(false)
const activeTab = ref<Tab>('overview')
const loading = ref(false)
const error = ref('')
const success = ref('')
const nowEpoch = ref(Date.now())
const lastUpdatedAt = ref<number | null>(null)
const queuedProjectIDs = ref<Set<string>>(new Set())
const queuedPreviewProjectIDs = ref<Set<string>>(new Set())
let clockTimer: number | undefined
let refreshTimer: number | undefined
const snapshotPollTimers = new Set<number>()

const dashboard = ref<Dashboard>({
  servers_total: 0,
  servers_online: 0,
  projects_total: 0,
  runs_succeeded: 0,
  runs_failed: 0,
  runs_partial: 0,
})
const servers = ref<Server[]>([])
const repositories = ref<Repository[]>([])
const projects = ref<Project[]>([])
const runs = ref<Run[]>([])
const snapshots = ref<Snapshot[]>([])
const snapshotProjectFilter = ref('')
const selectedSnapshotID = ref('')
const selectedSnapshotProjectID = ref('')
const snapshotBrowsePath = ref('/')
const pendingRestorePath = ref<string | null>(null)
const snapshotBrowseCommandID = ref('')
const snapshotRestoreCommandID = ref('')
const enrollment = ref<EnrollmentResult | null>(null)
const profile = ref<Profile>({ username: '', totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], webauthn_available: false, webauthn_rp_id: '' })
const totpSetup = ref<{ secret: string; qr_code: string } | null>(null)
const recoveryCodes = ref<string[]>([])
const totpActivationCode = ref('')
const passkeyName = ref('')
const securityModal = ref<SecurityModal>(null)
const securityModalStep = ref<'start' | 'scan' | 'recovery'>('start')
const selectedPasskey = ref<Passkey | null>(null)
const pendingPasskeyAction = ref<PendingPasskeyAction>(null)
const reauthenticationForm = reactive({ password: '', code: '' })

const passwordForm = reactive({ current_password: '', new_password: '', confirm_password: '', verification_code: '' })
const securityForm = reactive({ password: '', code: '' })

const serverForm = reactive({ name: '' })
const repositoryForm = reactive({
  provider: 'cloudflare_r2',
  name: '',
  prefix: 'vaultmesh',
  password: '',
  values: repositoryDefaults('cloudflare_r2') as Record<string, string>,
})
const projectForm = reactive({
  server_id: '',
  repository_id: '',
  name: '',
  sources: [newProjectSource('files')] as ProjectSourceDraft[],
  schedule_mode: 'daily' as ScheduleMode,
  schedule_time: '02:00',
  weekday: '1',
  custom_cron: '0 2 * * *',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
  jitter_minutes: 5,
  max_runtime_hours: 6,
  one_file_system: true,
  exclude_caches: true,
  exclude_if_present: '.nobackup',
  exclude_larger_than: '',
  retention_enabled: true,
  retention_mode: 'count' as 'count' | 'smart' | 'gfs' | 'age',
  keep_last: 14,
  keep_hourly: 0,
  keep_daily: 7,
  keep_weekly: 4,
  keep_monthly: 12,
  keep_yearly: 3,
  keep_within: '90d',
  prune: false,
  verification_mode: 'off' as 'off' | 'metadata' | 'subset' | 'full',
  read_data_subset: '1%',
  retention_cron: '30 3 * * *',
  prune_cron: '0 4 * * 0',
  verification_cron: '0 5 * * 0',
})

const activeRepositoryProvider = computed(() => repositoryProvider(repositoryForm.provider))
const repositoryFields = computed(() => activeRepositoryProvider.value.fields.filter((field) => repositoryFieldVisible(field, repositoryForm.values)))
const repositoryURL = computed(() => buildRepositoryTarget(repositoryForm.provider, repositoryForm.values, repositoryForm.prefix))
const repositoryMissing = computed(() => missingRepositoryFields(repositoryForm.provider, repositoryForm.values))
const repositoryReady = computed(() => Boolean(
  repositoryForm.name.trim() && repositoryURL.value && repositoryForm.password && !repositoryMissing.value.length,
))

watch(() => repositoryForm.provider, (provider) => {
  repositoryForm.values = repositoryDefaults(provider)
})

watch(snapshotProjectFilter, (projectID) => {
  if (!projectID || selectedSnapshotProjectID.value === projectID) return
  selectedSnapshotID.value = ''
  selectedSnapshotProjectID.value = ''
  snapshotBrowsePath.value = '/'
  snapshotBrowseCommandID.value = ''
  snapshotRestoreCommandID.value = ''
  pendingRestorePath.value = null
})

const projectNames = computed(() => new Map(projects.value.map((project) => [project.id, project.name])))
const backupRuns = computed(() => runs.value.filter((run) => {
  const operation = String(run.stats?.operation || 'backup')
  return operation === 'backup'
}))
const projectCron = computed(buildProjectCron)
const projectSchedulePreview = computed(() => {
  const jitter = projectForm.jitter_minutes > 0 ? `，最多随机延后 ${projectForm.jitter_minutes} 分钟` : ''
  return `${cronDescription(projectCron.value)} · ${projectForm.timezone}${jitter}`
})
const totalRunCount = computed(() => backupRuns.value.length)
const successfulRunCount = computed(() => backupRuns.value.filter((run) => run.status === 'succeeded').length)
const successRate = computed(() => totalRunCount.value ? Math.round(successfulRunCount.value / totalRunCount.value * 100) : 0)
const protectedSourceCount = computed(() => projects.value.reduce((total, project) => total + project.sources.length, 0))
const attentionCount = computed(() => dashboard.value.runs_failed + dashboard.value.runs_partial)
const onlineRate = computed(() => dashboard.value.servers_total
  ? Math.round(dashboard.value.servers_online / dashboard.value.servers_total * 100)
  : 0)
const passkeyEnvironmentReady = computed(() => Boolean(
  profile.value.webauthn_available && window.isSecureContext && window.PublicKeyCredential && navigator.credentials,
))
const nextScheduledProject = computed(() => projects.value
  .filter((project) => project.next_run_at)
  .map((project) => ({ project, at: new Date(project.next_run_at!).getTime() }))
  .filter((item) => Number.isFinite(item.at) && item.at >= nowEpoch.value - 1000)
  .sort((left, right) => left.at - right.at)[0])
const nextBackupCountdown = computed(() => {
  if (!nextScheduledProject.value) return '--:--:--'
  return formatCountdown(Math.max(0, nextScheduledProject.value.at - nowEpoch.value))
})

const runTrend = computed(() => {
  const points = Array.from({ length: 7 }, (_, index) => {
    const date = new Date()
    date.setHours(0, 0, 0, 0)
    date.setDate(date.getDate() - (6 - index))
    return {
      key: localDateKey(date),
      label: `${date.getMonth() + 1}/${date.getDate()}`,
      succeeded: 0,
      partial: 0,
      failed: 0,
      total: 0,
    }
  })
  const byDate = new Map(points.map((point) => [point.key, point]))
  for (const run of backupRuns.value) {
    const point = byDate.get(localDateKey(new Date(run.started_at)))
    if (!point) continue
    point.total += 1
    if (run.status === 'succeeded') point.succeeded += 1
    else if (run.status === 'partial') point.partial += 1
    else point.failed += 1
  }
  const max = Math.max(1, ...points.map((point) => point.total))
  return points.map((point) => ({
    ...point,
    succeededHeight: point.succeeded / max * 100,
    partialHeight: point.partial / max * 100,
    failedHeight: point.failed / max * 100,
    totalHeight: point.total / max * 100,
  }))
})

const runDistribution = computed(() => [
  { key: 'succeeded', label: '成功', count: backupRuns.value.filter((run) => run.status === 'succeeded').length, color: '#5df0a8' },
  { key: 'partial', label: '部分成功', count: backupRuns.value.filter((run) => run.status === 'partial').length, color: '#f6c85f' },
  { key: 'failed', label: '失败/超时', count: backupRuns.value.filter((run) => ['failed', 'timed_out', 'canceled', 'unknown'].includes(run.status)).length, color: '#ff6b73' },
  { key: 'running', label: '执行中', count: backupRuns.value.filter((run) => run.status === 'running').length, color: '#65b8ff' },
])

const runDonutBackground = computed(() => {
  const total = runDistribution.value.reduce((sum, item) => sum + item.count, 0)
  if (!total) return 'conic-gradient(#22312d 0 100%)'
  let cursor = 0
  const segments = runDistribution.value.filter((item) => item.count > 0).map((item) => {
    const start = cursor
    cursor += item.count / total * 100
    return `${item.color} ${start}% ${cursor}%`
  })
  return `conic-gradient(${segments.join(', ')})`
})

const projectHealth = computed(() => projects.value.map((project) => {
  const latest = backupRuns.value.find((run) => run.project_id === project.id)
  return { project, latest, status: latest?.status ?? 'unknown' }
}).sort((left, right) => healthOrder(left.status) - healthOrder(right.status)))

const projectGroups = computed(() => {
  const assigned = new Set<string>()
  const groups: { id: string; name: string; server?: Server; projects: Project[] }[] = servers.value.map((server) => {
    const items = projects.value.filter((project) => project.server_id === server.id)
    items.forEach((project) => assigned.add(project.id))
    return { id: server.id, name: server.name, server, projects: items }
  })
  const detached = projects.value.filter((project) => !assigned.has(project.id))
  if (detached.length) groups.push({ id: 'unknown', name: '未知服务器', projects: detached })
  return groups
})

const filteredSnapshots = computed(() => snapshots.value.filter((snapshot) => (
  !snapshotProjectFilter.value || snapshot.project_id === snapshotProjectFilter.value
)))
const selectedSnapshot = computed(() => snapshots.value.find((snapshot) => (
  snapshot.id === selectedSnapshotID.value && snapshot.project_id === selectedSnapshotProjectID.value
)))
const protectedSnapshotCount = computed(() => snapshots.value.filter((snapshot) => snapshot.protected).length)
const snapshotStorageBytes = computed(() => snapshots.value.reduce((total, snapshot) => total + Number(snapshot.total_bytes || 0), 0))
const currentBrowseRun = computed(() => {
  if (snapshotBrowseCommandID.value) {
    return runs.value.find((run) => run.idempotency_key === `manual:${snapshotBrowseCommandID.value}`)
  }
  if (!selectedSnapshot.value) return undefined
  return runs.value.find((run) => run.project_id === selectedSnapshot.value?.project_id
    && run.stats?.operation === 'snapshot_browse'
    && run.stats?.snapshot_id === selectedSnapshot.value?.id
    && run.stats?.path === snapshotBrowsePath.value)
})
const currentRestoreRun = computed(() => {
  if (snapshotRestoreCommandID.value) {
    return runs.value.find((run) => run.idempotency_key === `manual:${snapshotRestoreCommandID.value}`)
  }
  if (!selectedSnapshot.value) return undefined
  return runs.value.find((run) => run.project_id === selectedSnapshot.value?.project_id
    && run.stats?.operation === 'snapshot_restore'
    && run.stats?.snapshot_id === selectedSnapshot.value?.id)
})
const snapshotEntries = computed<SnapshotEntry[]>(() => {
  if (currentBrowseRun.value?.status !== 'succeeded') return []
  const value = currentBrowseRun.value.stats?.entries
  if (!Array.isArray(value)) return []
  return value.filter((entry): entry is SnapshotEntry => Boolean(
    entry && typeof entry === 'object' && typeof entry.path === 'string' && typeof entry.type === 'string',
  )).sort((left, right) => {
    if (left.type === 'dir' && right.type !== 'dir') return -1
    if (right.type === 'dir' && left.type !== 'dir') return 1
    return left.name.localeCompare(right.name, 'zh-CN')
  })
})
const snapshotBreadcrumbs = computed(() => {
  const segments = snapshotBrowsePath.value.split('/').filter(Boolean)
  return [
    { label: '/', path: '/' },
    ...segments.map((segment, index) => ({ label: segment, path: `/${segments.slice(0, index + 1).join('/')}` })),
  ]
})
const snapshotBrowsePending = computed(() => Boolean(snapshotBrowseCommandID.value && !currentBrowseRun.value))
const snapshotRestorePending = computed(() => Boolean(snapshotRestoreCommandID.value && !currentRestoreRun.value))

async function login() {
  error.value = ''
  loading.value = true
  try {
    const result = await api<{ mfa_required?: boolean }>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username: usernameInput.value.trim(), password: passwordInput.value }),
    })
    if (result.mfa_required) {
      mfaLoginRequired.value = true
      passwordInput.value = ''
      return
    }
    authenticated.value = true
    mfaLoginRequired.value = false
    passwordInput.value = ''
    await loadAll()
  } catch (cause) {
    authenticated.value = false
    showError(cause)
  } finally {
    loading.value = false
  }
}

async function completeMFA() {
  error.value = ''
  loading.value = true
  try {
    await api('/api/v1/auth/totp', { method: 'POST', body: JSON.stringify({ code: mfaCode.value }) })
    authenticated.value = true
    mfaLoginRequired.value = false
    mfaCode.value = ''
    await loadAll()
  } catch (cause) {
    showError(cause)
  } finally {
    loading.value = false
  }
}

function cancelMFA() {
  mfaLoginRequired.value = false
  mfaCode.value = ''
  error.value = ''
}

async function loginWithPasskey() {
  error.value = ''
  loading.value = true
  try {
    if (!window.PublicKeyCredential || !navigator.credentials) throw new Error('当前浏览器不支持通行密钥')
    const options = await api<any>('/api/v1/auth/passkey/begin', { method: 'POST' })
    const credential = await navigator.credentials.get({ publicKey: parseRequestOptions(options.publicKey) }) as PublicKeyCredential | null
    if (!credential) throw new Error('通行密钥验证已取消')
    await api('/api/v1/auth/passkey/finish', { method: 'POST', body: JSON.stringify(serializeAssertion(credential)) })
    authenticated.value = true
    mfaLoginRequired.value = false
    await loadAll()
  } catch (cause) {
    showError(cause)
  } finally {
    loading.value = false
  }
}

async function logout() {
  try {
    await api('/api/v1/auth/logout', { method: 'POST' })
  } catch {
    // Clear the local UI state even if the API is temporarily unreachable.
  } finally {
    authenticated.value = false
    mfaLoginRequired.value = false
    passwordInput.value = ''
    error.value = ''
  }
}

async function loadAll() {
  loading.value = true
  error.value = ''
  try {
    const [dashboardResult, serverResult, repositoryResult, projectResult, runResult, snapshotResult, profileResult] = await Promise.all([
      api<Dashboard>('/api/v1/dashboard'),
      api<{ items: Server[] }>('/api/v1/servers'),
      api<{ items: Repository[] }>('/api/v1/repositories'),
      api<{ items: Project[] }>('/api/v1/projects'),
      api<{ items: Run[] }>('/api/v1/runs?limit=100'),
      api<{ items: Snapshot[] }>('/api/v1/snapshots?limit=1000'),
      api<Profile>('/api/v1/profile'),
    ])
    dashboard.value = dashboardResult
    servers.value = serverResult.items ?? []
    repositories.value = repositoryResult.items ?? []
    projects.value = projectResult.items ?? []
    runs.value = runResult.items ?? []
    snapshots.value = (snapshotResult.items ?? []).map((snapshot) => ({
      ...snapshot,
      paths: snapshot.paths ?? [],
      tags: snapshot.tags ?? [],
    }))
    profile.value = profileResult
    lastUpdatedAt.value = Date.now()
    selectDefaults()
  } finally {
    loading.value = false
  }
}

async function changePassword() {
  await perform(async () => {
    if (passwordForm.new_password !== passwordForm.confirm_password) throw new Error('两次输入的新密码不一致')
    await api('/api/v1/profile/password', {
      method: 'POST',
      body: JSON.stringify({
        current_password: passwordForm.current_password,
        new_password: passwordForm.new_password,
        verification_code: passwordForm.verification_code,
      }),
    })
    authenticated.value = false
    passwordForm.current_password = ''
    passwordForm.new_password = ''
    passwordForm.confirm_password = ''
    passwordForm.verification_code = ''
    success.value = '密码已修改，所有会话均已撤销，请使用新密码重新登录。'
    securityModal.value = null
  })
}

async function startTOTPSetup() {
  await perform(async () => {
    totpSetup.value = await api('/api/v1/profile/totp/begin', {
      method: 'POST', body: JSON.stringify({ password: securityForm.password }),
    })
    recoveryCodes.value = []
    securityModalStep.value = 'scan'
    success.value = '请用验证器扫描二维码，然后输入当前 6 位验证码完成启用。'
  })
}

async function enableTOTP() {
  await perform(async () => {
    const result = await api<{ recovery_codes: string[] }>('/api/v1/profile/totp/enable', {
      method: 'POST', body: JSON.stringify({ code: totpActivationCode.value }),
    })
    recoveryCodes.value = result.recovery_codes
    totpSetup.value = null
    totpActivationCode.value = ''
    await loadAll()
    securityModalStep.value = 'recovery'
    success.value = '二步验证已启用。恢复码只展示这一次，请立即离线保存。'
  })
}

async function disableTOTP() {
  await perform(async () => {
    if (!securityForm.password || !securityForm.code) throw new Error('请填写当前密码和验证器动态码')
    await api('/api/v1/profile/totp/disable', {
      method: 'POST', body: JSON.stringify(securityForm),
    })
    authenticated.value = false
    securityModal.value = null
    success.value = '二步验证已停用，所有会话均已撤销，请重新登录。'
  })
}

async function regenerateRecoveryCodes() {
  await perform(async () => {
    const result = await api<{ recovery_codes: string[] }>('/api/v1/profile/recovery-codes', {
      method: 'POST', body: JSON.stringify(securityForm),
    })
    recoveryCodes.value = result.recovery_codes
    await loadAll()
    securityModalStep.value = 'recovery'
    success.value = '旧恢复码已失效。请立即保存这组新恢复码。'
  })
}

async function registerPasskey() {
  await perform(async () => {
    if (!window.PublicKeyCredential || !navigator.credentials) throw new Error('当前浏览器不支持通行密钥')
    let options: any
    try {
      options = await api<any>('/api/v1/profile/passkeys/register/begin', {
        method: 'POST', body: JSON.stringify({ name: passkeyName.value || suggestedPasskeyName() }),
      })
    } catch (cause) {
      if (cause instanceof APIError && cause.code === 'reauthentication_required') {
        pendingPasskeyAction.value = 'add'
        securityModal.value = 'reauthenticate'
        return
      }
      throw cause
    }
    let credential: PublicKeyCredential | null
    try {
      credential = await navigator.credentials.create({ publicKey: parseCreationOptions(options.publicKey) }) as PublicKeyCredential | null
    } catch (cause) {
      throw friendlyPasskeyError(cause)
    }
    if (!credential) throw new Error('通行密钥注册已取消')
    await api<Passkey>('/api/v1/profile/passkeys/register/finish', {
      method: 'POST', body: JSON.stringify(serializeRegistration(credential)),
    })
    passkeyName.value = ''
    await loadAll()
    securityModal.value = null
    success.value = '通行密钥已注册，可在登录页直接使用。'
  })
}

async function deletePasskey(passkey: Passkey) {
  await perform(async () => {
    try {
      await api(`/api/v1/profile/passkeys/${encodeURIComponent(passkey.id)}/delete`, { method: 'POST' })
    } catch (cause) {
      if (cause instanceof APIError && cause.code === 'reauthentication_required') {
        selectedPasskey.value = passkey
        pendingPasskeyAction.value = 'delete'
        securityModal.value = 'reauthenticate'
        return
      }
      throw cause
    }
    await loadAll()
    securityModal.value = null
    success.value = `已删除通行密钥“${passkey.name}”。`
  })
}

function openSecurityModal(modal: Exclude<SecurityModal, null>) {
  error.value = ''
  success.value = ''
  securityModal.value = modal
  securityModalStep.value = 'start'
  if (modal === 'password') {
    passwordForm.current_password = ''
    passwordForm.new_password = ''
    passwordForm.confirm_password = ''
    passwordForm.verification_code = ''
  }
  if (modal === 'totp-setup') {
    securityForm.password = ''
    totpSetup.value = null
    totpActivationCode.value = ''
    recoveryCodes.value = []
  }
  if (modal === 'totp-manage') {
    securityForm.password = ''
    securityForm.code = ''
    recoveryCodes.value = []
  }
  if (modal === 'passkey-add') passkeyName.value = suggestedPasskeyName()
}

function closeSecurityModal() {
  if (loading.value) return
  securityModal.value = null
  selectedPasskey.value = null
  pendingPasskeyAction.value = null
  reauthenticationForm.password = ''
  reauthenticationForm.code = ''
  error.value = ''
}

function confirmPasskeyDeletion(passkey: Passkey) {
  selectedPasskey.value = passkey
  openSecurityModal('passkey-delete')
}

async function completeReauthentication() {
  await perform(async () => {
    await api('/api/v1/profile/reauthenticate', {
      method: 'POST', body: JSON.stringify(reauthenticationForm),
    })
    const action = pendingPasskeyAction.value
    const passkey = selectedPasskey.value
    reauthenticationForm.password = ''
    reauthenticationForm.code = ''
    pendingPasskeyAction.value = null
    if (action === 'add') {
      securityModal.value = 'passkey-add'
      await registerPasskey()
    } else if (action === 'delete' && passkey) {
      securityModal.value = 'passkey-delete'
      await deletePasskey(passkey)
    }
  })
}

function suggestedPasskeyName(): string {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  const platform = nav.userAgentData?.platform || navigator.platform || '当前设备'
  return `${platform} · ${new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric' }).format(new Date())}`
}

function friendlyPasskeyError(cause: unknown): Error {
  if (!(cause instanceof DOMException)) return cause instanceof Error ? cause : new Error('通行密钥注册失败')
  if (cause.name === 'InvalidStateError') return new Error('这台设备已经为 VaultMesh 注册过通行密钥，请使用现有密钥或换一台设备。')
  if (cause.name === 'SecurityError') return new Error(`通行密钥域名校验失败：当前页面是 ${window.location.origin}，服务器 RP ID 是 ${profile.value.webauthn_rp_id || '未配置'}。`)
  if (cause.name === 'NotAllowedError') return new Error('系统没有完成通行密钥创建：可能是你取消了操作、设备未设置锁屏，或浏览器等待超时。')
  return new Error(`通行密钥注册失败：${cause.message || cause.name}`)
}

async function copyRecoveryCodes() {
  await navigator.clipboard.writeText(recoveryCodes.value.join('\n'))
  success.value = '恢复码已复制。请将它们保存到密码管理器或离线介质。'
}

function selectDefaults() {
  if (!projectForm.server_id && servers.value.length) projectForm.server_id = servers.value[0].id
  if (!repositories.value.some((repository) => repository.id === projectForm.repository_id)) {
    projectForm.repository_id = repositories.value[0]?.id ?? ''
  }
  if (snapshotProjectFilter.value && !projects.value.some((project) => project.id === snapshotProjectFilter.value)) {
    snapshotProjectFilter.value = ''
  }
  if (selectedSnapshotID.value && !snapshots.value.some((snapshot) => (
    snapshot.id === selectedSnapshotID.value && snapshot.project_id === selectedSnapshotProjectID.value
  ))) {
    selectedSnapshotID.value = ''
    selectedSnapshotProjectID.value = ''
    snapshotBrowsePath.value = '/'
    snapshotBrowseCommandID.value = ''
    snapshotRestoreCommandID.value = ''
    pendingRestorePath.value = null
  }
}

async function createServer() {
  await perform(async () => {
    enrollment.value = await api<EnrollmentResult>('/api/v1/servers', {
      method: 'POST',
      body: JSON.stringify({ name: serverForm.name }),
    })
    serverForm.name = ''
    await loadAll()
    activeTab.value = 'servers'
    success.value = '服务器已创建。注册令牌只会完整显示这一次。'
  })
}

async function createRepository() {
  await perform(async () => {
    if (!repositoryReady.value) throw new Error(`请完成仓库配置${repositoryMissing.value.length ? `：${repositoryMissing.value.join('、')}` : ''}`)
    await api<Repository>('/api/v1/repositories', {
      method: 'POST',
      body: JSON.stringify({
        provider: repositoryForm.provider,
        name: repositoryForm.name,
        url: repositoryURL.value,
        password: repositoryForm.password,
        environment: buildRepositoryEnvironment(repositoryForm.provider, repositoryForm.values),
        options: buildRepositoryOptions(repositoryForm.provider, repositoryForm.values),
      }),
    })
    repositoryForm.name = ''
    repositoryForm.prefix = 'vaultmesh'
    repositoryForm.password = ''
    repositoryForm.values = repositoryDefaults(repositoryForm.provider)
    await loadAll()
    success.value = '独立备份仓库已加密保存，可分配给任意服务器上的项目。'
  })
}

async function createProject() {
  await perform(async () => {
    const sources = projectForm.sources.map((source) => {
      if (source.type === 'files') {
        return {
          type: source.type,
          paths: lines(source.paths),
          excludes: lines(source.excludes),
          required: source.required,
        }
      }
      if (source.type === 'docker') {
        return {
          type: source.type,
          docker: {
            containers: lines(source.containers),
            include_volumes: source.include_volumes,
          },
          required: source.required,
        }
      }
      return {
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
    })
    await api<Project>('/api/v1/projects', {
      method: 'POST',
      body: JSON.stringify({
        server_id: projectForm.server_id,
        repository_id: projectForm.repository_id,
        name: projectForm.name,
        sources,
        schedule: {
          cron: projectCron.value,
          timezone: projectForm.timezone,
          jitter_seconds: Number(projectForm.jitter_minutes) * 60,
          max_runtime_seconds: Number(projectForm.max_runtime_hours) * 3600,
          missed_run_policy: 'skip',
          concurrency_policy: 'forbid',
        },
        policy: {
          backup: {
            one_file_system: projectForm.one_file_system,
            exclude_caches: projectForm.exclude_caches,
            exclude_if_present: lines(projectForm.exclude_if_present),
            exclude_larger_than: projectForm.exclude_larger_than.trim(),
          },
          retention: {
            enabled: projectForm.retention_enabled,
            mode: projectForm.retention_mode,
            keep_last: Number(projectForm.keep_last),
            keep_hourly: Number(projectForm.keep_hourly),
            keep_daily: Number(projectForm.keep_daily),
            keep_weekly: Number(projectForm.keep_weekly),
            keep_monthly: Number(projectForm.keep_monthly),
            keep_yearly: Number(projectForm.keep_yearly),
            keep_within: projectForm.keep_within.trim(),
            prune: projectForm.prune,
          },
          verification: {
            mode: projectForm.verification_mode,
            read_data_subset: projectForm.verification_mode === 'subset' ? projectForm.read_data_subset : '',
          },
          maintenance: {
            separate: true,
            timezone: projectForm.timezone,
            retention_cron: projectForm.retention_enabled ? projectForm.retention_cron.trim() : '',
            prune_cron: projectForm.retention_enabled && projectForm.prune ? projectForm.prune_cron.trim() : '',
            verification_cron: projectForm.verification_mode !== 'off' ? projectForm.verification_cron.trim() : '',
          },
        },
      }),
    })
    projectForm.name = ''
    projectForm.sources = [newProjectSource('files')]
    await loadAll()
    success.value = '项目已创建，Agent 将在下一次同步时应用配置。'
  })
}

function newProjectSource(type: SourceType): ProjectSourceDraft {
  sourceSequence += 1
  return {
    key: sourceSequence,
    type,
    required: true,
    paths: '/etc',
    excludes: '',
    host: '127.0.0.1',
    port: type === 'mysql' ? 3306 : type === 'postgresql' ? 5432 : 0,
    username: '',
    password: '',
    database: '',
    containers: '',
    include_volumes: true,
  }
}

function addProjectSource(type: SourceType) {
  projectForm.sources.push(newProjectSource(type))
}

function removeProjectSource(index: number) {
  if (projectForm.sources.length > 1) projectForm.sources.splice(index, 1)
}

function changeSourceType(source: ProjectSourceDraft) {
  source.port = source.type === 'mysql' ? 3306 : source.type === 'postgresql' ? 5432 : 0
}

function generateRepositoryPassword() {
  error.value = ''
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  repositoryForm.password = btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  success.value = '已生成 256 位 Restic 仓库密码。请另行安全保存，恢复时必须使用。'
}

function checkRepositoryConfiguration() {
  error.value = ''
  success.value = ''
  if (!repositoryReady.value) {
    const missing = repositoryMissing.value.length ? ` 缺少：${repositoryMissing.value.join('、')}。` : ''
    error.value = `配置尚不完整：请检查渠道名称、仓库密码与目标地址。${missing}`
    return
  }
  success.value = `配置格式有效，Restic 目标为 ${repositoryURL.value}`
}

function buildProjectCron(): string {
  if (projectForm.schedule_mode === 'custom') return projectForm.custom_cron.trim()
  const match = /^(\d{2}):(\d{2})$/.exec(projectForm.schedule_time)
  const hour = Number(match?.[1] ?? 2)
  const minute = Number(match?.[2] ?? 0)
  if (projectForm.schedule_mode === 'weekly') return `${minute} ${hour} * * ${projectForm.weekday}`
  return `${minute} ${hour} * * *`
}

async function runNow(project: Project) {
  await perform(async () => {
    await api(`/api/v1/projects/${encodeURIComponent(project.id)}/run`, { method: 'POST' })
    queuedProjectIDs.value = new Set([...queuedProjectIDs.value, project.id])
    success.value = `已向 ${project.name} 所在 Agent 排队发送手动备份。`
    window.setTimeout(() => {
      const next = new Set(queuedProjectIDs.value)
      next.delete(project.id)
      queuedProjectIDs.value = next
    }, 5000)
  })
}

async function previewRetention(project: Project) {
  await perform(async () => {
    await api(`/api/v1/projects/${encodeURIComponent(project.id)}/retention-preview`, { method: 'POST' })
    queuedPreviewProjectIDs.value = new Set([...queuedPreviewProjectIDs.value, project.id])
    success.value = `已向 ${project.name} 所在 Agent 排队发送只读清理预览；结果会在运行记录和项目卡片中显示。`
    window.setTimeout(() => {
      const next = new Set(queuedPreviewProjectIDs.value)
      next.delete(project.id)
      queuedPreviewProjectIDs.value = next
    }, 15000)
  })
}

async function refreshSnapshotInventory() {
  await perform(async () => {
    const targets = projects.value.filter((project) => project.enabled
      && (!snapshotProjectFilter.value || project.id === snapshotProjectFilter.value))
    if (!targets.length) throw new Error('当前筛选下没有可同步的已启用项目')
    await Promise.all(targets.map((project) => api(`/api/v1/projects/${encodeURIComponent(project.id)}/snapshots/refresh`, {
      method: 'POST',
    })))
    success.value = `已向 ${targets.length} 个项目的 Agent 排队同步快照索引，页面会自动获取结果。`
    queueSnapshotPolls()
  })
}

async function selectSnapshot(snapshot: Snapshot) {
  selectedSnapshotID.value = snapshot.id
  selectedSnapshotProjectID.value = snapshot.project_id
  snapshotProjectFilter.value = snapshot.project_id
  pendingRestorePath.value = null
  snapshotRestoreCommandID.value = ''
  await browseSnapshotPath('/')
}

async function browseSnapshotPath(path: string) {
  const snapshot = selectedSnapshot.value
  if (!snapshot) return
  await perform(async () => {
    const command = await api<{ id: string }>(`/api/v1/projects/${encodeURIComponent(snapshot.project_id)}/snapshots/${encodeURIComponent(snapshot.id)}/browse`, {
      method: 'POST',
      body: JSON.stringify({ path }),
    })
    snapshotBrowsePath.value = path
    snapshotBrowseCommandID.value = command.id
    pendingRestorePath.value = null
    success.value = `目录读取已发送给 ${serverName(snapshot.server_id)}，Agent 返回后会自动展示。`
    queueSnapshotPolls()
  })
}

async function toggleSnapshotProtection(snapshot: Snapshot) {
  await perform(async () => {
    await api(`/api/v1/projects/${encodeURIComponent(snapshot.project_id)}/snapshots/${encodeURIComponent(snapshot.id)}/protect`, {
      method: 'POST',
      body: JSON.stringify({ protected: !snapshot.protected }),
    })
    success.value = snapshot.protected
      ? '已排队取消保护；新的快照索引返回后生效。'
      : '已排队永久保护；保留策略将跳过这份快照。'
    queueSnapshotPolls()
  })
}

function requestSnapshotRestore(path: string) {
  pendingRestorePath.value = path
  snapshotRestoreCommandID.value = ''
}

async function confirmSnapshotRestore() {
  const snapshot = selectedSnapshot.value
  const path = pendingRestorePath.value
  if (!snapshot || !path) return
  await perform(async () => {
    const command = await api<{ id: string }>(`/api/v1/projects/${encodeURIComponent(snapshot.project_id)}/snapshots/${encodeURIComponent(snapshot.id)}/restore`, {
      method: 'POST',
      body: JSON.stringify({ path }),
    })
    snapshotRestoreCommandID.value = command.id
    pendingRestorePath.value = null
    success.value = '安全恢复已排队：Agent 只会写入新的隔离目录，并强制禁止覆盖已有文件。'
    queueSnapshotPolls()
  })
}

function queueSnapshotPolls() {
  for (const delay of [12000, 24000, 36000]) {
    const timer = window.setTimeout(() => {
      snapshotPollTimers.delete(timer)
      if (authenticated.value && !loading.value) void loadAll().catch(showError)
    }, delay)
    snapshotPollTimers.add(timer)
  }
}

async function toggleProject(project: Project) {
  await perform(async () => {
    const enabled = !project.enabled
    await api<Project>(`/api/v1/projects/${encodeURIComponent(project.id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled }),
    })
    await loadAll()
    success.value = enabled ? `${project.name} 已恢复，Agent 将重新加载执行计划。` : `${project.name} 已暂停，不再接受计划或手动备份。`
  })
}

async function refreshData() {
  try {
    await loadAll()
    success.value = '运行数据已刷新。'
  } catch (cause) {
    showError(cause)
  }
}

async function perform(operation: () => Promise<void>) {
  loading.value = true
  error.value = ''
  success.value = ''
  try {
    await operation()
  } catch (cause) {
    showError(cause)
  } finally {
    loading.value = false
  }
}

function showError(cause: unknown) {
  if (cause instanceof APIError) {
    if (cause.status === 401 && cause.code === 'unauthorized') {
      authenticated.value = false
      passwordInput.value = ''
      error.value = '登录已过期，请重新登录'
      return
    }
    error.value = cause.code === 'invalid_credentials' && !authenticated.value ? '用户名或密码错误' : cause.message
  } else if (cause instanceof Error) {
    error.value = cause.message
  } else {
    error.value = '发生未知错误'
  }
}

function lines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
}

function formatDate(value?: string): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value))
}

function formatBytes(value?: number): string {
  const bytes = Number(value || 0)
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  const size = bytes / 1024 ** index
  return `${size >= 10 || index === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[index]}`
}

function snapshotEntryName(entry: SnapshotEntry): string {
  return entry.name || entry.path.split('/').filter(Boolean).at(-1) || '/'
}

function snapshotEntryIcon(entry: SnapshotEntry): string {
  if (entry.type === 'dir') return '▰'
  if (entry.type === 'symlink') return '↗'
  return '▪'
}

function localDateKey(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function healthOrder(status: string): number {
  if (['failed', 'timed_out', 'unknown'].includes(status)) return 0
  if (['partial', 'running'].includes(status)) return 1
  return 2
}

function formatDuration(run: Run): string {
  if (!run.finished_at) return run.status === 'running' ? '执行中' : '—'
  const seconds = Math.max(0, Math.round((new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  return `${minutes}m ${seconds % 60}s`
}

function formatCountdown(milliseconds: number): string {
  const totalSeconds = Math.floor(milliseconds / 1000)
  const days = Math.floor(totalSeconds / 86400)
  const hours = Math.floor(totalSeconds % 86400 / 3600)
  const minutes = Math.floor(totalSeconds % 3600 / 60)
  const seconds = totalSeconds % 60
  const clock = [hours, minutes, seconds].map((value) => String(value).padStart(2, '0')).join(':')
  return days ? `${days}天 ${clock}` : clock
}

function pageDescription(tab: Tab): string {
  return {
    overview: '运行态势、成功率与风险信号',
    servers: 'Agent 在线状态与配置收敛',
    repositories: 'Restic 目标与凭据边界',
    projects: '数据源、调度策略与下次执行',
    snapshots: '快照索引、文件浏览与隔离恢复',
    runs: '端到端备份执行证据',
    profile: '密码、二步验证与通行密钥',
  }[tab]
}

function navBadge(tab: Tab): number {
  return {
    overview: attentionCount.value,
    servers: servers.value.length,
    repositories: repositories.value.length,
    projects: projects.value.length,
    snapshots: snapshots.value.length,
    runs: runs.value.length,
    profile: profile.value.passkeys.length,
  }[tab]
}

function serverName(id: string): string {
  return servers.value.find((server) => server.id === id)?.name ?? id
}

function repositoryName(id: string): string {
  return repositories.value.find((repository) => repository.id === id)?.name ?? id
}

function repositoryUsageCount(id: string): number {
  return projects.value.filter((project) => project.repository_id === id).length
}

function providerLabel(provider: Repository['provider']): string {
  return repositoryProvider(provider).label
}

function sourceTypeLabel(type: SourceType): string {
  return { files: '文件与目录', mysql: 'MySQL', postgresql: 'PostgreSQL', docker: 'Docker' }[type]
}

function sourceSummary(source: Project['sources'][number]): string {
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

function retentionSummary(project: Project): string {
  const retention = project.policy?.retention
  if (!retention?.enabled) return '不自动清理'
  switch (retention.mode || 'gfs') {
    case 'count': return `最多 ${retention.keep_last} 份`
    case 'smart': return '智能：日 7 / 周 4 / 月 12'
    case 'age': return `保留最近 ${retention.keep_within}`
    default: return `GFS · 最近 ${retention.keep_last || 0} / 日 ${retention.keep_daily || 0} / 周 ${retention.keep_weekly || 0} / 月 ${retention.keep_monthly || 0}`
  }
}

function latestRetentionPreview(projectID: string): Run | undefined {
  return runs.value.find((run) => run.project_id === projectID && run.stats?.operation === 'retention_preview')
}

function retentionPreviewSummary(projectID: string): string {
  const preview = latestRetentionPreview(projectID)
  if (!preview) return ''
  if (preview.status !== 'succeeded') return `预览失败：${preview.error_message || 'Agent 未返回有效结果'}`
  return `保留 ${Number(preview.stats?.snapshots_kept || 0)} 份 · 将删除 ${Number(preview.stats?.snapshots_removed || 0)} 份 · 未执行删除`
}

function runOperationLabel(run: Run): string {
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

function maintenanceSummary(project: Project): string {
  const maintenance = project.policy?.maintenance
  if (!maintenance?.separate) return '备份后执行（兼容模式）'
  const tasks = []
  if (project.policy?.retention.enabled) tasks.push(`清理 ${maintenance.retention_cron || '—'}`)
  if (project.policy?.retention.prune) tasks.push(`Prune ${maintenance.prune_cron || '—'}`)
  if (project.policy?.verification.mode && project.policy.verification.mode !== 'off') tasks.push(`校验 ${maintenance.verification_cron || '—'}`)
  return tasks.join(' · ') || '无维护任务'
}

function verificationSummary(project: Project): string {
  const verification = project.policy?.verification
  if (!verification || verification.mode === 'off') return '关闭'
  if (verification.mode === 'metadata') return '仓库结构'
  if (verification.mode === 'subset') return `抽样 ${verification.read_data_subset || '—'}`
  return '完整数据'
}

function scanSummary(project: Project): string {
  const backup = project.policy?.backup
  return `${backup?.one_file_system ? '不跨文件系统' : '允许跨文件系统'} · ${backup?.exclude_caches ? '忽略缓存' : '包含缓存'}`
}

function cronDescription(cron: string): string {
  const daily = /^(\d{1,2}) (\d{1,2}) \* \* \*$/.exec(cron)
  if (daily) return `每天 ${clock(daily[2], daily[1])}`
  const weekly = /^(\d{1,2}) (\d{1,2}) \* \* ([0-6])$/.exec(cron)
  if (weekly) return `每${weekdays[Number(weekly[3])]} ${clock(weekly[2], weekly[1])}`
  return `Cron ${cron}`
}

function clock(hour: string, minute: string): string {
  return `${hour.padStart(2, '0')}:${minute.padStart(2, '0')}`
}

function formatNextRun(project: Project): string {
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

function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    pending: '待注册', online: '在线', offline: '离线', running: '执行中',
    succeeded: '成功', partial: '部分成功', failed: '失败', timed_out: '超时',
    canceled: '已取消', unknown: '状态未知',
  }
  return labels[status] ?? status
}

function installCommand(result: EnrollmentResult): string {
  return `sudo vaultmesh-agent --server ${apiBaseURL} --enrollment-token ${result.enrollment_token}`
}

function base64urlToBuffer(value: string): ArrayBuffer {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4)
  const binary = atob(padded)
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0))
  return bytes.buffer
}

function bufferToBase64url(value: ArrayBuffer | null): string | null {
  if (!value) return null
  const bytes = new Uint8Array(value)
  let binary = ''
  bytes.forEach((byte) => { binary += String.fromCharCode(byte) })
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function parseCreationOptions(input: any): PublicKeyCredentialCreationOptions {
  return {
    ...input,
    challenge: base64urlToBuffer(input.challenge),
    user: { ...input.user, id: base64urlToBuffer(input.user.id) },
    excludeCredentials: (input.excludeCredentials ?? []).map((item: any) => ({ ...item, id: base64urlToBuffer(item.id) })),
  }
}

function parseRequestOptions(input: any): PublicKeyCredentialRequestOptions {
  return {
    ...input,
    challenge: base64urlToBuffer(input.challenge),
    allowCredentials: (input.allowCredentials ?? []).map((item: any) => ({ ...item, id: base64urlToBuffer(item.id) })),
  }
}

function serializeRegistration(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      attestationObject: bufferToBase64url(response.attestationObject),
      transports: typeof response.getTransports === 'function' ? response.getTransports() : [],
    },
  }
}

function serializeAssertion(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      authenticatorData: bufferToBase64url(response.authenticatorData),
      signature: bufferToBase64url(response.signature),
      userHandle: bufferToBase64url(response.userHandle),
    },
  }
}

onMounted(async () => {
  clockTimer = window.setInterval(() => { nowEpoch.value = Date.now() }, 1000)
  refreshTimer = window.setInterval(() => {
    if (authenticated.value && !loading.value) void loadAll().catch(showError)
  }, 30000)
  try {
    await api('/api/v1/auth/session')
    authenticated.value = true
    await loadAll()
  } catch (cause) {
    authenticated.value = false
    if (!(cause instanceof APIError && cause.status === 401)) showError(cause)
  } finally {
    authReady.value = true
  }
})

onBeforeUnmount(() => {
  if (clockTimer) window.clearInterval(clockTimer)
  if (refreshTimer) window.clearInterval(refreshTimer)
  snapshotPollTimers.forEach((timer) => window.clearTimeout(timer))
  snapshotPollTimers.clear()
})
</script>

<template>
  <main v-if="!authReady" class="login-shell">
    <section class="login-card">
      <div class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></div>
      <p class="eyebrow">SELF-HOSTED BACKUP CONTROL</p>
      <h1>VaultMesh</h1>
      <p class="muted">正在确认登录状态…</p>
    </section>
  </main>

  <main v-else-if="!authenticated" class="login-shell">
    <section class="login-card">
      <div class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></div>
      <p class="eyebrow">SELF-HOSTED BACKUP CONTROL</p>
      <h1>VaultMesh</h1>
      <p class="muted">连接每一台服务器，看见每一次备份是否真正完成。</p>
      <form v-if="!mfaLoginRequired" class="login-form" @submit.prevent="login">
        <label for="admin-username">用户名</label>
        <input id="admin-username" v-model="usernameInput" type="text" autocomplete="username" required placeholder="admin" />
        <label for="admin-password">密码</label>
        <input id="admin-password" v-model="passwordInput" type="password" autocomplete="current-password" required placeholder="请输入管理员密码" />
        <button class="primary" :disabled="loading">{{ loading ? '正在验证…' : '进入控制台' }}</button>
        <button type="button" class="ghost passkey-login" :disabled="loading" @click="loginWithPasskey">使用通行密钥登录</button>
      </form>
      <form v-else class="login-form" @submit.prevent="completeMFA">
        <div class="login-stage"><span>02</span><div><strong>二步验证</strong><small>输入验证器中的 6 位动态码，或一枚恢复码。</small></div></div>
        <label for="admin-mfa">验证码或恢复码</label>
        <input id="admin-mfa" v-model.trim="mfaCode" type="text" inputmode="numeric" autocomplete="one-time-code" required autofocus placeholder="123456" />
        <button class="primary" :disabled="loading">{{ loading ? '正在验证…' : '完成登录' }}</button>
        <button type="button" class="ghost" @click="cancelMFA">返回密码登录</button>
      </form>
      <p v-if="error" class="message error">{{ error }}</p>
      <p class="security-note">登录成功后使用 HttpOnly 会话 Cookie，前端不会读取或保存密码与会话凭据。</p>
    </section>
  </main>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand-row">
        <div class="brand-mark small" aria-hidden="true"><span></span><span></span><span></span></div>
        <div><strong>VaultMesh</strong><small>Operations Console</small></div>
      </div>
      <p class="nav-caption">WORKSPACE</p>
      <nav aria-label="主导航">
        <button v-for="item in ([
          ['overview', '运行总览', '01'], ['servers', '服务器', '02'], ['repositories', '备份仓库', '03'],
          ['projects', '备份项目', '04'], ['snapshots', '快照恢复', '05'], ['runs', '运行记录', '06'],
        ] as [Tab, string, string][] )" :key="item[0]" type="button" :class="{ active: activeTab === item[0] }" @click="activeTab = item[0]">
          <span class="nav-index">{{ item[2] }}</span><span class="nav-label">{{ item[1] }}</span><span v-if="navBadge(item[0])" class="nav-badge">{{ navBadge(item[0]) }}</span>
        </button>
      </nav>
      <div class="sidebar-system">
        <div><span class="system-pulse"></span><strong>API 已连接</strong></div>
        <code>{{ apiBaseURL.replace(/^https?:\/\//, '') }}</code>
        <small>Control Plane 与 Web 独立运行</small>
      </div>
      <button type="button" class="sidebar-account" :class="{ active: activeTab === 'profile' }" @click="activeTab = 'profile'">
        <span class="account-avatar">{{ (profile.username || 'A').slice(0, 1).toUpperCase() }}</span>
        <span><strong>{{ profile.username || '管理员' }}</strong><small>个人中心与安全</small></span>
        <span class="account-chevron">›</span>
      </button>
      <div class="sidebar-footer">
        <button type="button" class="ghost" @click="refreshData" :disabled="loading">{{ loading ? '同步中…' : '刷新' }}</button>
        <button type="button" class="ghost" @click="logout">退出</button>
      </div>
    </aside>

    <section class="workspace">
      <header class="topbar">
        <div>
          <p class="breadcrumb">VAULTMESH <span>/</span> {{ activeTab.toUpperCase() }}</p>
          <h1>{{ { overview: '备份总览', servers: '服务器', repositories: '备份仓库', projects: '备份项目', snapshots: '快照恢复', runs: '运行记录', profile: '个人中心' }[activeTab] }}</h1>
          <p class="page-description">{{ pageDescription(activeTab) }}</p>
        </div>
        <div class="topbar-status"><button type="button" class="scope-pill interactive" :disabled="loading" @click="refreshData">{{ loading ? 'SYNCING…' : '刷新实时数据' }}</button><button type="button" class="live-indicator interactive" @click="activeTab = 'servers'"><i></i>{{ dashboard.servers_online }}/{{ dashboard.servers_total }} Agent 在线</button></div>
      </header>

      <p v-if="error" class="message error" role="alert">{{ error }}</p>
      <p v-if="success" class="message success" role="status" aria-live="polite">{{ success }}</p>

      <template v-if="activeTab === 'overview'">
        <div class="overview-strip">
          <div><span class="operational-dot"></span><strong>Backup operations</strong><small>基于最近 100 次运行和当前 Agent 心跳聚合</small></div>
          <span>最近刷新 · {{ lastUpdatedAt ? new Intl.DateTimeFormat('zh-CN', { timeStyle: 'medium' }).format(new Date(lastUpdatedAt)) : '等待同步' }}</span>
        </div>
        <div class="metric-grid dense-metrics">
          <article class="metric"><header><span>AGENT HEALTH</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ dashboard.servers_online }}<small>/{{ dashboard.servers_total }}</small></strong><em>{{ onlineRate }}%</em></div><footer>在线节点 <span>{{ dashboard.servers_total - dashboard.servers_online }} 异常</span></footer></article>
          <article class="metric"><header><span>SUCCESS RATE</span><i class="metric-signal" :class="successRate >= 95 ? 'good' : successRate >= 80 ? 'warn' : 'bad'"></i></header><div class="metric-value"><strong>{{ successRate }}<small>%</small></strong><em>{{ successfulRunCount }}/{{ totalRunCount }}</em></div><footer>最近 100 次 <span>{{ totalRunCount ? '有效样本' : '等待数据' }}</span></footer></article>
          <article class="metric"><header><span>24H SNAPSHOTS</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ dashboard.runs_succeeded }}</strong><em>success</em></div><footer>有效快照 <span>{{ dashboard.runs_partial }} 次部分成功</span></footer></article>
          <article class="metric"><header><span>PROTECTED SOURCES</span><i class="metric-signal"></i></header><div class="metric-value"><strong>{{ protectedSourceCount }}</strong><em>{{ projects.length }} projects</em></div><footer>文件与数据库 <span>{{ repositories.length }} 个仓库</span></footer></article>
          <article class="metric" :class="{ alert: attentionCount > 0 }"><header><span>NEEDS ATTENTION</span><i class="metric-signal" :class="attentionCount ? 'bad' : 'good'"></i></header><div class="metric-value"><strong>{{ attentionCount }}</strong><em>24 hours</em></div><footer>失败或部分成功 <span>{{ attentionCount ? '需要处理' : '状态正常' }}</span></footer></article>
          <article class="metric countdown-metric"><header><span>NEXT BACKUP</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ nextBackupCountdown }}</strong></div><footer><template v-if="nextScheduledProject"><span>{{ nextScheduledProject.project.name }}</span><span>{{ formatNextRun(nextScheduledProject.project) }}</span></template><template v-else><span>暂无已启用计划</span></template></footer></article>
        </div>

        <div class="dashboard-grid primary-dashboard">
          <section class="panel trend-panel">
            <div class="panel-heading compact-heading"><div><p class="eyebrow">RUN TELEMETRY</p><h2>7 日运行趋势</h2></div><div class="chart-legend"><span class="succeeded">成功</span><span class="partial">部分</span><span class="failed">失败</span></div></div>
            <div class="stacked-chart">
              <div class="chart-axis"><span>MAX</span><span>50%</span><span>0</span></div>
              <div class="chart-plot">
                <div class="chart-grid-lines"><i></i><i></i><i></i></div>
                <div v-for="point in runTrend" :key="point.key" class="chart-column">
                  <div class="bar-total" :title="`${point.label}: ${point.total} 次`">
                    <span class="bar-segment failed" :style="{ height: `${point.failedHeight}%` }"></span>
                    <span class="bar-segment partial" :style="{ height: `${point.partialHeight}%` }"></span>
                    <span class="bar-segment succeeded" :style="{ height: `${point.succeededHeight}%` }"></span>
                  </div>
                  <strong>{{ point.total }}</strong><small>{{ point.label }}</small>
                </div>
              </div>
            </div>
            <div class="chart-summary"><span>7 日总运行 <strong>{{ runTrend.reduce((sum, point) => sum + point.total, 0) }}</strong></span><span>成功 <strong class="good-text">{{ runTrend.reduce((sum, point) => sum + point.succeeded, 0) }}</strong></span><span>异常 <strong class="bad-text">{{ runTrend.reduce((sum, point) => sum + point.failed + point.partial, 0) }}</strong></span></div>
          </section>

          <section class="panel distribution-panel">
            <div class="panel-heading compact-heading"><div><p class="eyebrow">OUTCOME MIX</p><h2>运行结果</h2></div><span class="sample-size">N={{ totalRunCount }}</span></div>
            <div class="donut-layout">
              <div class="donut-chart" :style="{ background: runDonutBackground }"><div><strong>{{ successRate }}%</strong><small>成功率</small></div></div>
              <div class="distribution-list"><div v-for="item in runDistribution" :key="item.key"><i :style="{ background: item.color }"></i><span>{{ item.label }}</span><strong>{{ item.count }}</strong><small>{{ totalRunCount ? Math.round(item.count / totalRunCount * 100) : 0 }}%</small></div></div>
            </div>
            <div class="micro-sparkline" aria-label="每日运行量"><i v-for="point in runTrend" :key="point.key" :style="{ height: `${Math.max(point.total ? 10 : 3, point.totalHeight)}%` }"></i></div>
          </section>
        </div>

        <div class="dashboard-grid secondary-dashboard">
          <section class="panel health-panel">
            <div class="panel-heading compact-heading"><div><p class="eyebrow">PROTECTION MATRIX</p><h2>项目健康度</h2></div><button class="text-button" @click="activeTab = 'projects'">管理项目 →</button></div>
            <div v-if="!projectHealth.length" class="empty-state compact-empty">尚未创建备份项目。</div>
            <div v-else class="table-wrap"><table class="dense-table"><thead><tr><th>项目</th><th>数据源</th><th>最近结果</th><th>耗时</th><th>下次计划</th></tr></thead><tbody>
              <tr v-for="row in projectHealth" :key="row.project.id"><td><strong>{{ row.project.name }}</strong><small>{{ serverName(row.project.server_id) }}</small></td><td><div class="source-dots"><i v-for="source in row.project.sources" :key="source.id" :class="source.type" :title="sourceSummary(source)"></i><span>{{ row.project.sources.length }}</span></div></td><td><span class="status-pill" :class="row.status">{{ row.latest ? statusLabel(row.status) : '无运行' }}</span><small>{{ row.latest ? formatDate(row.latest.started_at) : '等待首次执行' }}</small></td><td>{{ row.latest ? formatDuration(row.latest) : '—' }}</td><td><strong>{{ formatNextRun(row.project) }}</strong><small>{{ cronDescription(row.project.schedule.cron) }}</small></td></tr>
            </tbody></table></div>
          </section>

          <section class="panel infrastructure-panel">
            <div class="panel-heading compact-heading"><div><p class="eyebrow">INFRASTRUCTURE</p><h2>Agent 状态</h2></div><button class="text-button" @click="activeTab = 'servers'">全部 →</button></div>
            <div v-if="!servers.length" class="empty-state compact-empty">暂无 Agent。</div>
            <div v-else class="server-density-list"><article v-for="server in servers.slice(0, 6)" :key="server.id"><span class="server-state" :class="server.status"></span><div><strong>{{ server.name }}</strong><small>{{ server.hostname || '尚未注册' }} · {{ server.agent_version || '—' }}</small></div><div class="revision-meter"><span><i :style="{ width: `${server.desired_revision ? Math.min(100, server.applied_revision / server.desired_revision * 100) : 100}%` }"></i></span><small>rev {{ server.applied_revision }}/{{ server.desired_revision }}</small></div></article></div>
          </section>
        </div>

        <section class="panel recent-panel">
          <div class="panel-heading compact-heading"><div><p class="eyebrow">EVENT STREAM</p><h2>最近运行</h2></div><button class="text-button" @click="activeTab = 'runs'">查看 100 条记录 →</button></div>
          <div v-if="!runs.length" class="empty-state compact-empty">尚无运行记录。图表会在 Agent 上报首个结果后自动生成。</div>
          <div v-else class="recent-run-grid"><article v-for="run in runs.slice(0, 8)" :key="run.id"><span class="status-line" :class="run.status"></span><div><strong>{{ projectNames.get(run.project_id) ?? run.project_id }}</strong><small>{{ formatDate(run.started_at) }}</small></div><span class="status-copy">{{ statusLabel(run.status) }}</span><code>{{ formatDuration(run) }}</code></article></div>
        </section>
      </template>

      <template v-else-if="activeTab === 'servers'">
        <div class="content-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">AGENTS</p><h2>已接入服务器</h2></div></div>
            <div v-if="!servers.length" class="empty-state">还没有服务器。</div>
            <div v-else class="table-wrap"><table><thead><tr><th>名称</th><th>状态</th><th>主机</th><th>版本</th><th>配置</th><th>最后心跳</th></tr></thead><tbody>
              <tr v-for="server in servers" :key="server.id"><td><strong>{{ server.name }}</strong><small>{{ server.id }}</small></td><td><span class="status-pill" :class="server.status">{{ statusLabel(server.status) }}</span></td><td>{{ server.hostname || '—' }}<small>{{ server.os }} {{ server.arch }}</small></td><td>{{ server.agent_version || '—' }}</td><td>{{ server.applied_revision }}/{{ server.desired_revision }}</td><td>{{ formatDate(server.last_seen_at) }}</td></tr>
            </tbody></table></div>
          </section>
          <aside class="panel form-panel">
            <p class="eyebrow">ENROLL</p><h2>添加服务器</h2>
            <form @submit.prevent="createServer"><label>显示名称<input v-model="serverForm.name" required maxlength="100" placeholder="Hong Kong VPS" /></label><button class="primary" :disabled="loading">创建注册码</button></form>
          </aside>
        </div>
        <section v-if="enrollment" class="panel enrollment-card">
          <div><p class="eyebrow">ONE-TIME TOKEN</p><h2>在 {{ enrollment.server.name }} 上运行</h2></div>
          <code>{{ installCommand(enrollment) }}</code>
          <p>令牌将在 {{ formatDate(enrollment.expires_at) }} 过期，并且只能使用一次。关闭本提示后无法再次查看完整令牌。</p>
          <button class="ghost" @click="enrollment = null">我已保存</button>
        </section>
      </template>

      <template v-else-if="activeTab === 'repositories'">
        <section class="panel repository-guide">
          <div class="panel-heading"><div><p class="eyebrow">INDUSTRY-BACKED STORAGE MODEL</p><h2>仓库类型来自成熟项目的真实实现</h2></div><span class="sample-size">{{ repositoryProviders.length }} TYPES</span></div>
          <div class="guide-steps">
            <article><span>1</span><div><strong>Restic 是协议标准</strong><small>VaultMesh 的执行引擎就是 Restic，因此原生后端、URL 格式和环境变量以官方文档为准。</small><a href="https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html" target="_blank" rel="noreferrer">官方后端列表 ↗</a></div></article>
            <article><span>2</span><div><strong>1Panel 提供产品字段参考</strong><small>S3、OSS、COS、SFTP、WebDAV 与网盘的字段分组参考其公开实现，不凭空发明表单。</small><a href="https://github.com/1Panel-dev/1Panel/blob/dev-v2/frontend/src/views/setting/backup-account/operate/index.vue" target="_blank" rel="noreferrer">查看源码 ↗</a></div></article>
            <article><span>3</span><div><strong>Kopia 用于交叉验证</strong><small>用另一套成熟备份系统核对 S3、Azure、B2、GCS、SFTP、WebDAV 与 rclone 的分类边界。</small><a href="https://kopia.io/docs/repositories/" target="_blank" rel="noreferrer">仓库文档 ↗</a></div></article>
          </div>
          <p class="guide-note">没有跨所有备份软件的“万能表单”：SFTP、S3、Swift、Azure 和 OAuth 网盘的认证模型不同。VaultMesh 采用统一的三层模板：<code>Restic 原生协议</code>、<code>S3 厂商预设</code>、<code>rclone 扩展</code>。</p>
        </section>
        <div class="content-grid repository-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">STORAGE CHANNELS</p><h2>独立备份仓库</h2></div><span class="sample-size">{{ repositories.length }} CHANNELS</span></div>
            <div v-if="!repositories.length" class="empty-state">尚未配置仓库。</div>
            <div v-else class="card-list"><article v-for="repository in repositories" :key="repository.id" class="data-card repository-card"><div><strong>{{ repository.name }}</strong><small>{{ providerLabel(repository.provider) }} · 被 {{ repositoryUsageCount(repository.id) }} 个项目使用</small></div><span class="status-pill online">全局渠道</span><code>{{ repository.url }}</code></article></div>
          </section>
          <aside class="panel form-panel wide-form"><p class="eyebrow">NEW STORAGE CHANNEL</p><h2>添加备份仓库</h2><p class="form-intro">仓库是全局存储渠道，不绑定服务器。创建项目时再选择由哪个 Agent 写入。</p>
            <form @submit.prevent="createRepository">
              <div class="form-row"><label>存储类型<select v-model="repositoryForm.provider"><optgroup v-for="group in repositoryProviderGroups" :key="group" :label="group"><option v-for="provider in repositoryProviders.filter((item) => item.group === group)" :key="provider.id" :value="provider.id">{{ provider.label }}</option></optgroup></select></label><label>渠道名称<input v-model="repositoryForm.name" required maxlength="100" :placeholder="`${activeRepositoryProvider.label} · 生产环境`" /></label></div>
              <div class="repository-kind"><div><span class="engine-badge" :class="activeRepositoryProvider.engine">{{ engineLabel(activeRepositoryProvider.engine) }}</span><strong>{{ activeRepositoryProvider.label }}</strong></div><p>{{ activeRepositoryProvider.summary }}</p><small v-if="activeRepositoryProvider.warning">{{ activeRepositoryProvider.warning }}</small></div>
              <div class="dynamic-fields">
                <label v-for="field in repositoryFields" :key="field.key">{{ field.label }}<select v-if="field.type === 'select'" v-model="repositoryForm.values[field.key]" :required="field.required"><option v-for="option in field.options" :key="option.value" :value="option.value">{{ option.label }}</option></select><input v-else v-model="repositoryForm.values[field.key]" :type="field.type" :required="field.required" :placeholder="field.placeholder" :autocomplete="field.type === 'password' ? 'new-password' : 'off'" /><small v-if="field.help" class="field-help">{{ field.help }}</small></label>
              </div>
              <label v-if="!['local', 'sftp'].includes(repositoryForm.provider) && activeRepositoryProvider.engine !== 'rclone'">仓库目录前缀<input v-model.trim="repositoryForm.prefix" placeholder="vaultmesh" /><small class="field-help">可留空；用于在 Bucket、Container 或 REST 服务内隔离 VaultMesh 数据。</small></label>
              <div class="repository-preview"><span>RESTIC CHANNEL BASE</span><code>{{ repositoryURL || '填写 Account ID / Endpoint 与 Bucket 后自动生成' }}</code><small>下发给 Agent 时自动追加 <code>/&lt;server-id&gt;</code>，同一渠道中的不同服务器使用独立 Restic 仓库路径。</small></div>
              <label>Restic 仓库密码<div class="field-action"><input v-model="repositoryForm.password" type="password" required autocomplete="new-password" /><button type="button" class="ghost" @click="generateRepositoryPassword">安全生成</button></div><small class="field-help">它用于 Restic 端到端加密，和云存储密钥不是同一个密码；丢失后无法恢复快照。</small></label>
              <div class="form-actions"><button type="button" class="ghost" @click="checkRepositoryConfiguration">校验并预览</button><button class="primary" :disabled="loading || !repositoryReady">加密并保存渠道</button></div>
            </form>
          </aside>
        </div>
      </template>

      <template v-else-if="activeTab === 'projects'">
        <div class="content-grid projects-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">DESIRED STATE</p><h2>项目列表</h2></div></div>
            <div v-if="!servers.length" class="empty-state">请先添加服务器，再为对应 Agent 创建备份项目。</div>
            <div v-else class="project-server-list">
              <section v-for="group in projectGroups" :key="group.id" class="project-server-group">
                <header class="project-server-heading">
                  <div><span class="server-state" :class="group.server?.status"></span><div><strong>{{ group.name }}</strong><small>{{ group.server?.hostname || 'Agent 尚未注册' }} · {{ group.projects.length }} 个项目</small></div></div>
                  <span v-if="group.server" class="status-pill" :class="group.server.status">{{ statusLabel(group.server.status) }}</span>
                </header>
                <div v-if="!group.projects.length" class="server-empty">这台服务器还没有备份项目。</div>
                <div v-else class="card-list">
              <article v-for="project in group.projects" :key="project.id" class="project-card" :class="{ 'project-disabled': !project.enabled }">
                <div class="project-top">
                  <div><strong>{{ project.name }}</strong><small>{{ serverName(project.server_id) }} · {{ repositoryName(project.repository_id) }}</small></div>
                  <div class="project-actions"><span class="status-pill" :class="project.enabled ? 'online' : 'neutral'">{{ project.enabled ? `Revision ${project.revision}` : '已暂停' }}</span><button type="button" class="text-button" :disabled="loading" @click="toggleProject(project)">{{ project.enabled ? '暂停' : '恢复' }}</button><button v-if="project.policy?.retention.enabled" type="button" class="ghost compact" :disabled="loading || !project.enabled || queuedPreviewProjectIDs.has(project.id)" @click="previewRetention(project)">{{ queuedPreviewProjectIDs.has(project.id) ? '预览排队中' : '清理预览' }}</button><button type="button" class="ghost compact" :disabled="loading || !project.enabled || queuedProjectIDs.has(project.id)" @click="runNow(project)">{{ queuedProjectIDs.has(project.id) ? '已排队 ✓' : '立即备份' }}</button></div>
                </div>
                <div class="project-source-list">
                  <span v-for="source in project.sources" :key="source.id" class="source-chip" :class="source.type">{{ sourceSummary(source) }}</span>
                </div>
                <div class="schedule-overview">
                  <div><small>执行计划</small><strong>{{ cronDescription(project.schedule.cron) }}</strong><span>{{ project.schedule.timezone }}<template v-if="project.schedule.jitter_seconds"> · 最多延后 {{ Math.round(project.schedule.jitter_seconds / 60) }} 分钟</template></span></div>
                  <div class="next-run"><small>下次计划</small><strong>{{ formatNextRun(project) }}</strong><span>最长运行 {{ Math.round(project.schedule.max_runtime_seconds / 3600) }} 小时</span></div>
                </div>
                <div class="policy-strip">
                  <span><small>保留</small><strong>{{ retentionSummary(project) }}</strong></span>
                  <span><small>校验</small><strong>{{ verificationSummary(project) }}</strong></span>
                  <span><small>扫描</small><strong>{{ scanSummary(project) }}</strong></span>
                </div>
                <div class="maintenance-strip"><small>独立维护窗口</small><strong>{{ maintenanceSummary(project) }}</strong></div>
                <div v-if="latestRetentionPreview(project.id)" class="retention-preview-result" :class="latestRetentionPreview(project.id)?.status"><span>DRY RUN</span><strong>{{ retentionPreviewSummary(project.id) }}</strong><small>{{ formatDate(latestRetentionPreview(project.id)?.finished_at || latestRetentionPreview(project.id)?.started_at || '') }}</small></div>
              </article>
                </div>
              </section>
            </div>
          </section>
          <aside class="panel form-panel project-builder"><p class="eyebrow">NEW PROJECT</p><h2>创建备份项目</h2><p class="form-intro">一个项目可以组合文件、Docker、MySQL 和 PostgreSQL 数据源，并在同一个 Restic 快照中归档。</p>
            <form @submit.prevent="createProject">
              <section class="form-section">
                <div class="section-title"><span>1</span><div><strong>基础信息</strong><small>选择 Agent 和快照写入位置</small></div></div>
                <div class="form-row"><label>执行服务器<select v-model="projectForm.server_id" required @change="selectDefaults"><option value="" disabled>选择运行备份的 Agent</option><option v-for="server in servers" :key="server.id" :value="server.id">{{ server.name }}</option></select></label><label>备份仓库<select v-model="projectForm.repository_id" required><option value="" disabled>选择独立存储渠道</option><option v-for="repository in repositories" :key="repository.id" :value="repository.id">{{ repository.name }} · {{ providerLabel(repository.provider) }}</option></select></label></div>
                <label>项目名称<input v-model="projectForm.name" required maxlength="100" placeholder="例如：应用数据每日备份" /></label>
              </section>

              <section class="form-section">
                <div class="section-title"><span>2</span><div><strong>备份内容与扫描边界</strong><small>组合数据源，并使用 Restic 原生过滤选项控制遍历范围</small></div></div>
                <article v-for="(source, index) in projectForm.sources" :key="source.key" class="source-editor">
                  <header><strong>数据源 {{ index + 1 }}</strong><button v-if="projectForm.sources.length > 1" type="button" class="text-button danger-text" @click="removeProjectSource(index)">移除</button></header>
                  <label>类型<select v-model="source.type" @change="changeSourceType(source)"><option value="files">文件与目录</option><option value="docker">Docker 容器与挂载卷</option><option value="mysql">MySQL 逻辑备份</option><option value="postgresql">PostgreSQL 逻辑备份</option></select></label>
                  <template v-if="source.type === 'files'">
                    <label>绝对路径（每行一个）<textarea v-model="source.paths" rows="3" required placeholder="/etc&#10;/opt/app"></textarea></label>
                    <label>排除规则（每行一个）<textarea v-model="source.excludes" rows="2" placeholder="/opt/app/cache/**"></textarea></label>
                  </template>
                  <template v-else-if="source.type === 'docker'">
                    <div class="database-note docker-note"><strong>Docker 挂载数据备份</strong><span>Agent 使用 <code>docker inspect</code> 解析 bind mounts 和 named volumes，并保存不含环境变量的容器清单。数据库容器仍建议同时添加数据库逻辑备份。</span></div>
                    <label>容器名称或 ID（每行一个）<textarea v-model="source.containers" rows="3" required placeholder="nginx&#10;application&#10;redis"></textarea></label>
                    <label class="check-row"><input v-model="source.include_volumes" type="checkbox" /><span><strong>包含挂载卷数据</strong><small>备份容器的 bind mounts 与 Docker named volumes；不会备份容器可写层，也不会自动停止容器。</small></span></label>
                  </template>
                  <template v-else>
                    <div class="database-note"><strong>{{ sourceTypeLabel(source.type) }} 逻辑备份</strong><span>Agent 将执行 {{ source.type === 'mysql' ? 'mysqldump' : 'pg_dump' }}，临时凭据和产物仅保存在目标服务器。</span></div>
                    <div class="form-row database-host"><label>数据库地址<input v-model="source.host" required placeholder="127.0.0.1" /></label><label>端口<input v-model.number="source.port" required type="number" min="1" max="65535" /></label></div>
                    <div class="form-row"><label>数据库名称<input v-model="source.database" required placeholder="application" /></label><label>备份用户<input v-model="source.username" required autocomplete="off" placeholder="vaultmesh_backup" /></label></div>
                    <label>数据库密码<input v-model="source.password" required type="password" autocomplete="new-password" /><small class="field-help">提交后使用主密钥加密，管理 API 不会再次返回明文。</small></label>
                  </template>
                </article>
                <div class="source-add-row"><span>添加数据源</span><button type="button" class="ghost compact" @click="addProjectSource('files')">+ 文件</button><button type="button" class="ghost compact" @click="addProjectSource('docker')">+ Docker</button><button type="button" class="ghost compact" @click="addProjectSource('mysql')">+ MySQL</button><button type="button" class="ghost compact" @click="addProjectSource('postgresql')">+ PostgreSQL</button></div>
                <div class="policy-option-grid">
                  <label class="check-row"><input v-model="projectForm.one_file_system" type="checkbox" /><span><strong>不跨越文件系统</strong><small>对应 Restic <code>--one-file-system</code>，避免误扫额外挂载盘。</small></span></label>
                  <label class="check-row"><input v-model="projectForm.exclude_caches" type="checkbox" /><span><strong>排除标准缓存目录</strong><small>识别有效的 <code>CACHEDIR.TAG</code>。</small></span></label>
                </div>
                <div class="form-row"><label>目录忽略标记（每行一个）<textarea v-model="projectForm.exclude_if_present" rows="2" placeholder=".nobackup"></textarea><small class="field-help">目录内出现该文件时跳过整个目录。</small></label><label>排除大于<input v-model.trim="projectForm.exclude_larger_than" placeholder="例如 2G；留空不限制" /><small class="field-help">Restic 大小格式，例如 500M、2G。</small></label></div>
              </section>

              <section class="form-section">
                <div class="section-title"><span>3</span><div><strong>执行计划</strong><small>所有时间均按所选时区解释</small></div></div>
                <div class="form-row"><label>计划类型<select v-model="projectForm.schedule_mode"><option value="daily">每天</option><option value="weekly">每周</option><option value="custom">高级 Cron</option></select></label><label v-if="projectForm.schedule_mode !== 'custom'">开始时间<input v-model="projectForm.schedule_time" type="time" required /></label><label v-else>Cron（5 段）<input v-model="projectForm.custom_cron" required placeholder="0 2 * * *" /></label></div>
                <div v-if="projectForm.schedule_mode === 'weekly'" class="weekday-grid" role="group" aria-label="选择星期"><button v-for="(day, index) in weekdays" :key="day" type="button" :class="{ active: projectForm.weekday === String(index) }" @click="projectForm.weekday = String(index)">{{ day }}</button></div>
                <label>时区<input v-model="projectForm.timezone" required list="timezone-options" /><datalist id="timezone-options"><option v-for="timezone in commonTimezones" :key="timezone" :value="timezone" /></datalist></label>
                <div class="form-row"><label>随机延迟<select v-model.number="projectForm.jitter_minutes"><option :value="0">不延迟</option><option :value="5">最多 5 分钟</option><option :value="10">最多 10 分钟</option><option :value="30">最多 30 分钟</option><option :value="60">最多 60 分钟</option></select></label><label>最长运行时间<select v-model.number="projectForm.max_runtime_hours"><option :value="1">1 小时</option><option :value="3">3 小时</option><option :value="6">6 小时</option><option :value="12">12 小时</option><option :value="24">24 小时</option></select></label></div>
                <div class="schedule-preview"><span>计划预览</span><strong>{{ projectSchedulePreview }}</strong><code>{{ projectCron }}</code></div>
              </section>
              <section class="form-section">
                <div class="section-title"><span>4</span><div><strong>保留与完整性策略</strong><small>使用成熟备份软件的保留模型；创建后可在项目卡片执行只读清理预览</small></div></div>
                <label class="check-row"><input v-model="projectForm.retention_enabled" type="checkbox" /><span><strong>自动应用快照保留策略</strong><small>仅匹配当前服务器与当前项目标签，不影响仓库内其他项目。</small></span></label>
                <template v-if="projectForm.retention_enabled">
                  <div class="retention-mode-grid" role="radiogroup" aria-label="保留策略模式">
                    <label :class="{ active: projectForm.retention_mode === 'count' }"><input v-model="projectForm.retention_mode" type="radio" value="count" /><span><strong>最多 N 份</strong><small>简单且可预测，适合高频备份</small></span></label>
                    <label :class="{ active: projectForm.retention_mode === 'smart' }"><input v-model="projectForm.retention_mode" type="radio" value="smart" /><span><strong>智能保留</strong><small>每日 7、每周 4、每月 12</small></span></label>
                    <label :class="{ active: projectForm.retention_mode === 'gfs' }"><input v-model="projectForm.retention_mode" type="radio" value="gfs" /><span><strong>高级 GFS</strong><small>按小时/日/周/月/年分层</small></span></label>
                    <label :class="{ active: projectForm.retention_mode === 'age' }"><input v-model="projectForm.retention_mode" type="radio" value="age" /><span><strong>按时间保留</strong><small>保留指定时间范围内的全部快照</small></span></label>
                  </div>
                  <label v-if="projectForm.retention_mode === 'count'">最多保留快照数<input v-model.number="projectForm.keep_last" type="number" min="1" max="100000" required /><small class="field-help">当前项目无论备份路径是否变化，最终最多保留最近 N 份快照。</small></label>
                  <div v-else-if="projectForm.retention_mode === 'smart'" class="smart-retention-note"><strong>Duplicati Smart Retention</strong><span>最近一周每天一份、最近四周每周一份、最近十二个月每月一份。规则按“或”组合，重叠快照只计算一次。</span></div>
                  <div v-else-if="projectForm.retention_mode === 'gfs'" class="retention-grid">
                    <label>最近<input v-model.number="projectForm.keep_last" type="number" min="0" max="100000" /></label><label>每小时<input v-model.number="projectForm.keep_hourly" type="number" min="0" max="100000" /></label><label>每天<input v-model.number="projectForm.keep_daily" type="number" min="0" max="100000" /></label><label>每周<input v-model.number="projectForm.keep_weekly" type="number" min="0" max="100000" /></label><label>每月<input v-model.number="projectForm.keep_monthly" type="number" min="0" max="100000" /></label><label>每年<input v-model.number="projectForm.keep_yearly" type="number" min="0" max="100000" /></label>
                  </div>
                  <label v-else>保留时间范围<input v-model.trim="projectForm.keep_within" required pattern="(?:[1-9][0-9]*[ymdh])+" placeholder="例如 90d、6m、1y" /><small class="field-help">Restic duration：<code>h</code> 小时、<code>d</code> 天、<code>m</code> 月、<code>y</code> 年，可组合为 <code>1y6m</code>。</small></label>
                </template>
                <label v-if="projectForm.retention_enabled" class="check-row caution-row"><input v-model="projectForm.prune" type="checkbox" /><span><strong>定期回收未引用空间</strong><small><code>prune</code> 会锁定仓库并重写数据，启用后只在下方独立维护窗口执行，不阻塞备份。</small></span></label>
                <div class="form-row"><label>仓库校验<select v-model="projectForm.verification_mode"><option value="off">关闭</option><option value="metadata">检查仓库结构</option><option value="subset">抽样读取数据</option><option value="full">读取全部数据（高成本）</option></select></label><label v-if="projectForm.verification_mode === 'subset'">抽样比例<select v-model="projectForm.read_data_subset"><option value="1%">1%</option><option value="5%">5%</option><option value="10%">10%</option><option value="25%">25%</option></select></label></div>
                <div class="maintenance-window">
                  <div><strong>独立维护窗口</strong><small>Forget、Prune、Check 使用项目时区单独调度；仓库级互斥锁会避免它们与备份并发。</small></div>
                  <div class="maintenance-cron-grid">
                    <label v-if="projectForm.retention_enabled">清理快照 Cron<input v-model.trim="projectForm.retention_cron" required placeholder="30 3 * * *" /><small class="field-help">只执行 Forget，不回收数据块。</small></label>
                    <label v-if="projectForm.retention_enabled && projectForm.prune">空间回收 Cron<input v-model.trim="projectForm.prune_cron" required placeholder="0 4 * * 0" /><small class="field-help">建议每周低峰期执行。</small></label>
                    <label v-if="projectForm.verification_mode !== 'off'">仓库校验 Cron<input v-model.trim="projectForm.verification_cron" required placeholder="0 5 * * 0" /><small class="field-help">完整读取应安排在流量低峰。</small></label>
                  </div>
                  <code>{{ projectForm.timezone }}</code>
                </div>
                <p class="policy-reference">字段与执行语义直接采用 <a href="https://restic.readthedocs.io/en/stable/040_backup.html" target="_blank" rel="noreferrer">Restic backup</a>、<a href="https://restic.readthedocs.io/en/stable/060_forget.html" target="_blank" rel="noreferrer">forget/prune</a> 和 <a href="https://kopia.io/docs/reference/command-line/common/policy-set/" target="_blank" rel="noreferrer">Kopia policy</a> 的成熟模型。</p>
              </section>
              <button class="primary" :disabled="loading || !servers.length || !repositories.length">创建并下发</button>
            </form>
          </aside>
        </div>
      </template>

      <template v-else-if="activeTab === 'snapshots'">
        <section class="snapshot-overview panel">
          <div class="panel-heading compact-heading">
            <div><p class="eyebrow">RECOVERY INDEX</p><h2>可恢复快照</h2></div>
            <div class="snapshot-toolbar">
              <label>项目筛选<select v-model="snapshotProjectFilter"><option value="">全部项目</option><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.name }} · {{ serverName(project.server_id) }}</option></select></label>
              <button type="button" class="primary compact-action" :disabled="loading || !projects.length" @click="refreshSnapshotInventory">从 Agent 同步</button>
            </div>
          </div>
          <div class="snapshot-metrics">
            <article><span>INDEXED</span><strong>{{ snapshots.length }}</strong><small>已索引快照</small></article>
            <article><span>PROTECTED</span><strong>{{ protectedSnapshotCount }}</strong><small>不受自动清理影响</small></article>
            <article><span>LOGICAL SIZE</span><strong>{{ formatBytes(snapshotStorageBytes) }}</strong><small>快照摘要中的原始数据量</small></article>
            <article><span>PROJECTS</span><strong>{{ new Set(snapshots.map((snapshot) => snapshot.project_id)).size }}</strong><small>已有恢复点的项目</small></article>
          </div>
        </section>

        <div class="snapshot-workspace">
          <section class="panel snapshot-catalog">
            <div class="panel-heading compact-heading"><div><p class="eyebrow">SNAPSHOT CATALOG</p><h2>恢复点</h2></div><span class="sample-size">{{ filteredSnapshots.length }} ITEMS</span></div>
            <div v-if="!filteredSnapshots.length" class="empty-state snapshot-empty">
              <strong>还没有快照索引</strong>
              <span>点击“从 Agent 同步”。索引只保存快照元数据，备份内容仍留在 Restic 仓库。</span>
            </div>
            <div v-else class="snapshot-list">
              <article v-for="snapshot in filteredSnapshots" :key="`${snapshot.project_id}:${snapshot.id}`" :class="{ active: selectedSnapshotID === snapshot.id && selectedSnapshotProjectID === snapshot.project_id }">
                <button type="button" class="snapshot-select" @click="selectSnapshot(snapshot)">
                  <span class="snapshot-state" :class="{ protected: snapshot.protected }">{{ snapshot.protected ? '◆' : '●' }}</span>
                  <span><strong>{{ formatDate(snapshot.time) }}</strong><small>{{ projectNames.get(snapshot.project_id) ?? snapshot.project_id }} · {{ serverName(snapshot.server_id) }}</small></span>
                  <code>{{ snapshot.id.slice(0, 12) }}</code>
                </button>
                <div class="snapshot-facts"><span>{{ Number(snapshot.total_files || 0).toLocaleString() }} 文件</span><span>{{ formatBytes(snapshot.total_bytes) }}</span><span>{{ snapshot.paths.length }} 根路径</span></div>
                <div class="snapshot-card-footer"><span :title="snapshot.paths.join(', ')">{{ snapshot.paths.join(', ') || '/' }}</span><button type="button" class="text-button" :disabled="loading" @click="toggleSnapshotProtection(snapshot)">{{ snapshot.protected ? '取消保护' : '永久保护' }}</button></div>
              </article>
            </div>
          </section>

          <section class="panel snapshot-browser">
            <div v-if="!selectedSnapshot" class="snapshot-browser-placeholder">
              <span class="restore-glyph">↺</span><h2>选择一个恢复点</h2><p>选择左侧快照后，VaultMesh 会让对应 Agent 读取仓库目录。控制面不会下载或缓存备份内容。</p>
            </div>
            <template v-else>
              <header class="snapshot-browser-header">
                <div><p class="eyebrow">POINT-IN-TIME BROWSER</p><h2>{{ projectNames.get(selectedSnapshot.project_id) }} <code>{{ selectedSnapshot.id.slice(0, 12) }}</code></h2><small>{{ formatDate(selectedSnapshot.time) }} · {{ selectedSnapshot.hostname || serverName(selectedSnapshot.server_id) }}</small></div>
                <div class="browser-actions"><span class="protection-badge" :class="{ active: selectedSnapshot.protected }">{{ selectedSnapshot.protected ? '◆ 已保护' : '普通快照' }}</span><button type="button" class="ghost compact" :disabled="loading" @click="browseSnapshotPath(snapshotBrowsePath)">重新读取</button><button type="button" class="primary compact-action" :disabled="loading" @click="requestSnapshotRestore(snapshotBrowsePath)">恢复当前目录</button></div>
              </header>

              <div class="snapshot-breadcrumbs" role="navigation" aria-label="快照路径">
                <button v-for="(crumb, index) in snapshotBreadcrumbs" :key="crumb.path" type="button" :disabled="loading || index === snapshotBreadcrumbs.length - 1" @click="browseSnapshotPath(crumb.path)">{{ crumb.label }}</button>
              </div>

              <div v-if="snapshotBrowsePending" class="operation-state running"><i></i><div><strong>Agent 正在读取仓库目录</strong><small>命令 {{ snapshotBrowseCommandID }} · 通常在 10–30 秒内返回</small></div></div>
              <div v-else-if="currentBrowseRun?.status && currentBrowseRun.status !== 'succeeded'" class="operation-state failed"><i></i><div><strong>目录读取失败</strong><small>{{ currentBrowseRun.error_message || 'Agent 未返回可解析的 Restic 目录结果' }}</small></div></div>
              <div v-else-if="!currentBrowseRun" class="empty-state compact-empty">点击“重新读取”获取当前目录。</div>
              <div v-else-if="!snapshotEntries.length" class="empty-state compact-empty">这个目录是空的。</div>
              <div v-else class="table-wrap snapshot-file-table"><table><thead><tr><th>名称</th><th>类型</th><th>大小</th><th>权限</th><th>修改时间</th><th></th></tr></thead><tbody>
                <tr v-for="entry in snapshotEntries" :key="entry.path"><td><button v-if="entry.type === 'dir'" type="button" class="file-name directory" :disabled="loading" @click="browseSnapshotPath(entry.path)"><span>{{ snapshotEntryIcon(entry) }}</span>{{ snapshotEntryName(entry) }}</button><span v-else class="file-name"><span>{{ snapshotEntryIcon(entry) }}</span>{{ snapshotEntryName(entry) }}</span></td><td><span class="source-chip">{{ entry.type === 'dir' ? '目录' : entry.type === 'symlink' ? '链接' : '文件' }}</span></td><td>{{ entry.type === 'dir' ? '—' : formatBytes(entry.size) }}</td><td><code>{{ entry.permissions || '—' }}</code></td><td>{{ formatDate(entry.modified_at) }}</td><td><button type="button" class="text-button" @click="requestSnapshotRestore(entry.path)">恢复</button></td></tr>
              </tbody></table></div>

              <section v-if="pendingRestorePath" class="restore-confirmation">
                <div><span class="warning-symbol small">!</span><div><strong>确认隔离恢复</strong><p>将 <code>{{ pendingRestorePath }}</code> 恢复到该 Agent 的新任务目录。系统强制使用 <code>--overwrite never</code>，不会写回原始路径，也不会覆盖已有恢复任务。</p></div></div>
                <div><button type="button" class="ghost" @click="pendingRestorePath = null">取消</button><button type="button" class="primary" :disabled="loading" @click="confirmSnapshotRestore">确认创建恢复任务</button></div>
              </section>

              <div v-if="snapshotRestorePending" class="operation-state running restore-state"><i></i><div><strong>安全恢复正在 Agent 上执行</strong><small>命令 {{ snapshotRestoreCommandID }} · 完成后这里会显示实际隔离目录</small></div></div>
              <div v-else-if="currentRestoreRun?.status === 'succeeded'" class="restore-result"><span>✓</span><div><strong>恢复完成</strong><small>{{ Number(currentRestoreRun.stats?.files_restored || 0).toLocaleString() }} 个文件 · {{ formatBytes(Number(currentRestoreRun.stats?.bytes_restored || 0)) }}</small><code>{{ currentRestoreRun.stats?.restore_target }}</code></div></div>
              <div v-else-if="currentRestoreRun?.status && currentRestoreRun.status !== 'succeeded'" class="operation-state failed restore-state"><i></i><div><strong>恢复失败</strong><small>{{ currentRestoreRun.error_message || '请检查 Agent 与仓库日志' }}</small></div></div>

              <footer class="snapshot-safety-note"><strong>安全边界</strong><span>目录读取和恢复均在项目所属 Agent 执行；控制面仅保存索引、命令状态和审计结果。恢复始终进入 <code>VAULTMESH_RESTORE_ROOT/&lt;command-id&gt;</code>。</span></footer>
            </template>
          </section>
        </div>
      </template>

      <template v-else-if="activeTab === 'runs'">
        <section class="panel">
          <div class="panel-heading"><div><p class="eyebrow">RUN HISTORY</p><h2>最近 100 次运行</h2></div></div>
          <div v-if="!runs.length" class="empty-state">尚无运行记录。</div>
          <div v-else class="table-wrap"><table><thead><tr><th>项目</th><th>类型</th><th>状态</th><th>开始时间</th><th>快照 / 预览结果</th><th>错误</th></tr></thead><tbody>
            <tr v-for="run in runs" :key="run.id"><td><strong>{{ projectNames.get(run.project_id) ?? run.project_id }}</strong><small>{{ run.id }}</small></td><td><span class="source-chip">{{ runOperationLabel(run) }}</span></td><td><span class="status-pill" :class="run.status">{{ statusLabel(run.status) }}</span></td><td>{{ formatDate(run.started_at) }}</td><td><code v-if="run.snapshot_id">{{ run.snapshot_id.slice(0, 16) }}</code><span v-else-if="run.stats?.operation === 'retention_preview'">保留 {{ Number(run.stats?.snapshots_kept || 0) }} · 删除 {{ Number(run.stats?.snapshots_removed || 0) }}</span><span v-else-if="run.stats?.operation === 'snapshot_restore'">{{ run.stats?.restore_target || run.stats?.path }}</span><span v-else-if="run.stats?.snapshot_id"><code>{{ String(run.stats.snapshot_id).slice(0, 16) }}</code></span><span v-else>—</span></td><td class="error-cell">{{ run.error_message || '—' }}</td></tr>
          </tbody></table></div>
        </section>
      </template>

      <template v-else>
        <section class="profile-identity panel">
          <span class="profile-avatar">{{ (profile.username || 'A').slice(0, 1).toUpperCase() }}</span>
          <div><p class="eyebrow">ADMINISTRATOR</p><h2>{{ profile.username }}</h2><small>VaultMesh 单管理员控制面账号</small></div>
          <span class="account-state"><i></i>账号正常</span>
        </section>

        <section class="panel settings-panel">
          <div class="settings-heading"><div><p class="eyebrow">SIGN-IN &amp; SECURITY</p><h2>登录与安全</h2><p>管理用于登录和确认敏感操作的方式。</p></div></div>
          <div class="settings-list">
            <article class="setting-row">
              <span class="setting-icon">••</span>
              <div><strong>登录密码</strong><small>建议使用密码管理器生成并保存独立密码。</small></div>
              <span class="setting-status neutral">已设置</span>
              <button type="button" class="ghost" @click="openSecurityModal('password')">修改</button>
            </article>
            <article class="setting-row">
              <span class="setting-icon shield">◇</span>
              <div><strong>验证器二步验证</strong><small>{{ profile.totp_enabled ? `登录时需要动态码，剩余 ${profile.recovery_codes_remaining} 枚恢复码。` : '使用验证器动态码保护密码登录。' }}</small></div>
              <span class="setting-status" :class="profile.totp_enabled ? 'enabled' : 'warning'">{{ profile.totp_enabled ? '已启用' : '建议启用' }}</span>
              <button type="button" class="ghost" @click="openSecurityModal(profile.totp_enabled ? 'totp-manage' : 'totp-setup')">{{ profile.totp_enabled ? '管理' : '设置' }}</button>
            </article>
            <article class="setting-row passkey-setting">
              <span class="setting-icon passkey">◆</span>
              <div><strong>通行密钥</strong><small>使用 Touch ID、Windows Hello、设备锁屏或 FIDO2 安全密钥登录。</small></div>
              <span class="setting-status" :class="profile.passkeys.length ? 'enabled' : 'neutral'">{{ profile.passkeys.length ? `${profile.passkeys.length} 个` : '未设置' }}</span>
              <button type="button" class="primary compact-action" :disabled="!passkeyEnvironmentReady" @click="openSecurityModal('passkey-add')">添加通行密钥</button>
              <div v-if="profile.passkeys.length" class="credential-list">
                <div v-for="passkey in profile.passkeys" :key="passkey.id">
                  <span class="credential-device">◆</span><span><strong>{{ passkey.name }}</strong><small>{{ passkey.last_used_at ? `最近使用 ${formatDate(passkey.last_used_at)}` : `创建于 ${formatDate(passkey.created_at)}` }}</small></span><button type="button" class="text-button danger-text" @click="confirmPasskeyDeletion(passkey)">移除</button>
                </div>
              </div>
              <p v-if="!passkeyEnvironmentReady" class="setting-warning">{{ profile.webauthn_available ? '当前页面不是安全上下文，通行密钥需要 HTTPS 或本机回环地址。' : '服务器尚未配置 WebAuthn RP ID 与可信 Origin。' }}</p>
            </article>
          </div>
        </section>

        <div v-if="securityModal" class="modal-backdrop" @click.self="closeSecurityModal">
          <section class="security-dialog" role="dialog" aria-modal="true">
            <header><div><p class="eyebrow">ACCOUNT SECURITY</p><h2>{{ securityModal === 'password' ? '修改密码' : securityModal === 'totp-setup' ? '设置二步验证' : securityModal === 'totp-manage' ? '管理二步验证' : securityModal === 'passkey-delete' ? '移除通行密钥' : securityModal === 'reauthenticate' ? '确认是你本人' : '添加通行密钥' }}</h2></div><button type="button" class="dialog-close" aria-label="关闭" @click="closeSecurityModal">×</button></header>
            <p v-if="error" class="dialog-error">{{ error }}</p>

            <form v-if="securityModal === 'password'" class="dialog-form" @submit.prevent="changePassword">
              <label>当前密码<input v-model="passwordForm.current_password" type="password" required autocomplete="current-password" /></label>
              <label>新密码<input v-model="passwordForm.new_password" type="password" minlength="12" maxlength="72" required autocomplete="new-password" /></label>
              <label>确认新密码<input v-model="passwordForm.confirm_password" type="password" minlength="12" maxlength="72" required autocomplete="new-password" /></label>
              <label v-if="profile.totp_enabled">动态码或恢复码<input v-model.trim="passwordForm.verification_code" required autocomplete="one-time-code" /></label>
              <div class="dialog-actions"><button type="button" class="ghost" @click="closeSecurityModal">取消</button><button class="primary" :disabled="loading">保存并退出所有会话</button></div>
            </form>

            <template v-else-if="securityModal === 'totp-setup'">
              <form v-if="securityModalStep === 'start'" class="dialog-form" @submit.prevent="startTOTPSetup"><p class="dialog-copy">使用 1Password、Bitwarden、Google Authenticator 等验证器。先确认当前密码，然后扫描二维码。</p><label>当前密码<input v-model="securityForm.password" type="password" required autocomplete="current-password" /></label><div class="dialog-actions"><button type="button" class="ghost" @click="closeSecurityModal">取消</button><button class="primary" :disabled="loading">继续</button></div></form>
              <div v-else-if="securityModalStep === 'scan' && totpSetup" class="totp-wizard"><img :src="totpSetup.qr_code" alt="TOTP 设置二维码" /><div><strong>用验证器扫描二维码</strong><p>无法扫描时，手动输入下面的密钥：</p><code>{{ totpSetup.secret }}</code><form class="dialog-form" @submit.prevent="enableTOTP"><label>验证器中的 6 位动态码<input v-model.trim="totpActivationCode" required inputmode="numeric" autocomplete="one-time-code" placeholder="000000" /></label><button class="primary" :disabled="loading">验证并启用</button></form></div></div>
              <div v-else class="recovery-dialog"><span class="success-symbol">✓</span><h3>二步验证已启用</h3><p>恢复码只展示一次。每枚只能使用一次，请保存到密码管理器或离线介质。</p><div class="recovery-code-grid"><code v-for="code in recoveryCodes" :key="code">{{ code }}</code></div><div class="dialog-actions"><button type="button" class="ghost" @click="copyRecoveryCodes">复制全部</button><button type="button" class="primary" @click="closeSecurityModal">完成</button></div></div>
            </template>

            <template v-else-if="securityModal === 'totp-manage'">
              <div v-if="securityModalStep === 'recovery'" class="recovery-dialog"><h3>新的恢复码</h3><p>旧恢复码已失效，请立即保存新恢复码。</p><div class="recovery-code-grid"><code v-for="code in recoveryCodes" :key="code">{{ code }}</code></div><div class="dialog-actions"><button type="button" class="ghost" @click="copyRecoveryCodes">复制全部</button><button type="button" class="primary" @click="closeSecurityModal">完成</button></div></div>
              <form v-else class="dialog-form" @submit.prevent="regenerateRecoveryCodes"><p class="dialog-copy">生成新恢复码或停用二步验证前，需要再次确认身份。</p><label>当前密码<input v-model="securityForm.password" type="password" required autocomplete="current-password" /></label><label>验证器动态码<input v-model.trim="securityForm.code" required autocomplete="one-time-code" placeholder="000000" /></label><div class="dialog-actions split"><button type="button" class="ghost danger-ghost" :disabled="loading" @click="disableTOTP">停用二步验证</button><button class="primary" :disabled="loading">更换恢复码</button></div></form>
            </template>

            <div v-else-if="securityModal === 'passkey-add'" class="passkey-dialog"><span class="large-passkey-icon">◆</span><h3>在这台设备上创建通行密钥</h3><p>继续后，浏览器会打开系统安全窗口。使用 Touch ID、Windows Hello、设备 PIN 或安全密钥完成确认。</p><dl><div><dt>名称</dt><dd>{{ passkeyName }}</dd></div><div><dt>当前 Origin</dt><dd>{{ currentOrigin }}</dd></div><div><dt>RP ID</dt><dd>{{ profile.webauthn_rp_id }}</dd></div></dl><div class="dialog-actions"><button type="button" class="ghost" @click="closeSecurityModal">取消</button><button type="button" class="primary" :disabled="loading" @click="registerPasskey">继续创建</button></div></div>

            <div v-else-if="securityModal === 'passkey-delete' && selectedPasskey" class="confirm-dialog"><span class="warning-symbol">!</span><h3>移除“{{ selectedPasskey.name }}”？</h3><p>该设备将不能再使用这枚通行密钥登录，但设备密码管理器中的本地记录可能仍需单独删除。</p><div class="dialog-actions"><button type="button" class="ghost" @click="closeSecurityModal">取消</button><button type="button" class="primary danger-button" :disabled="loading" @click="deletePasskey(selectedPasskey)">确认移除</button></div></div>

            <form v-else class="dialog-form" @submit.prevent="completeReauthentication"><p class="dialog-copy">上次身份确认已超过 10 分钟。继续修改通行密钥前，请重新验证。</p><label>当前密码<input v-model="reauthenticationForm.password" type="password" required autocomplete="current-password" /></label><label v-if="profile.totp_enabled">动态码或恢复码<input v-model.trim="reauthenticationForm.code" required autocomplete="one-time-code" placeholder="000000" /></label><div class="dialog-actions"><button type="button" class="ghost" @click="closeSecurityModal">取消</button><button class="primary" :disabled="loading">确认并继续</button></div></form>
          </section>
        </div>
      </template>
    </section>
  </div>
</template>
