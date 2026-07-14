<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { APIError, api, getAPIBaseURL } from './api'
import type { Dashboard, EnrollmentResult, Passkey, Profile, Project, Repository, Run, Server } from './types'

type Tab = 'overview' | 'servers' | 'repositories' | 'projects' | 'runs' | 'profile'
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
let clockTimer: number | undefined
let refreshTimer: number | undefined

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
  provider: 'cloudflare_r2' as 'cloudflare_r2' | 's3_compatible',
  name: '',
  account_id: '',
  jurisdiction: 'default' as 'default' | 'eu' | 'fedramp',
  endpoint: '',
  bucket: '',
  prefix: 'vaultmesh',
  password: '',
  access_key: '',
  secret_key: '',
  region: 'auto',
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
})

const repositoryURL = computed(buildRepositoryURL)
const repositoryReady = computed(() => Boolean(
  repositoryForm.name.trim() && repositoryURL.value && repositoryForm.password &&
  repositoryForm.access_key && repositoryForm.secret_key,
))

const projectNames = computed(() => new Map(projects.value.map((project) => [project.id, project.name])))
const projectCron = computed(buildProjectCron)
const projectSchedulePreview = computed(() => {
  const jitter = projectForm.jitter_minutes > 0 ? `，最多随机延后 ${projectForm.jitter_minutes} 分钟` : ''
  return `${cronDescription(projectCron.value)} · ${projectForm.timezone}${jitter}`
})
const totalRunCount = computed(() => runs.value.length)
const successfulRunCount = computed(() => runs.value.filter((run) => run.status === 'succeeded').length)
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
  for (const run of runs.value) {
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
  { key: 'succeeded', label: '成功', count: runs.value.filter((run) => run.status === 'succeeded').length, color: '#5df0a8' },
  { key: 'partial', label: '部分成功', count: runs.value.filter((run) => run.status === 'partial').length, color: '#f6c85f' },
  { key: 'failed', label: '失败/超时', count: runs.value.filter((run) => ['failed', 'timed_out', 'canceled', 'unknown'].includes(run.status)).length, color: '#ff6b73' },
  { key: 'running', label: '执行中', count: runs.value.filter((run) => run.status === 'running').length, color: '#65b8ff' },
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
  const latest = runs.value.find((run) => run.project_id === project.id)
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
    const [dashboardResult, serverResult, repositoryResult, projectResult, runResult, profileResult] = await Promise.all([
      api<Dashboard>('/api/v1/dashboard'),
      api<{ items: Server[] }>('/api/v1/servers'),
      api<{ items: Repository[] }>('/api/v1/repositories'),
      api<{ items: Project[] }>('/api/v1/projects'),
      api<{ items: Run[] }>('/api/v1/runs?limit=100'),
      api<Profile>('/api/v1/profile'),
    ])
    dashboard.value = dashboardResult
    servers.value = serverResult.items ?? []
    repositories.value = repositoryResult.items ?? []
    projects.value = projectResult.items ?? []
    runs.value = runResult.items ?? []
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
    if (!repositoryReady.value) throw new Error('请完整填写仓库、Bucket 和访问凭据')
    const environment: Record<string, string> = {}
    if (repositoryForm.access_key) environment.AWS_ACCESS_KEY_ID = repositoryForm.access_key
    if (repositoryForm.secret_key) environment.AWS_SECRET_ACCESS_KEY = repositoryForm.secret_key
    if (repositoryForm.region) environment.AWS_DEFAULT_REGION = repositoryForm.region
    await api<Repository>('/api/v1/repositories', {
      method: 'POST',
      body: JSON.stringify({
        provider: repositoryForm.provider,
        name: repositoryForm.name,
        url: repositoryURL.value,
        password: repositoryForm.password,
        environment,
      }),
    })
    repositoryForm.name = ''
    repositoryForm.account_id = ''
    repositoryForm.endpoint = ''
    repositoryForm.bucket = ''
    repositoryForm.prefix = 'vaultmesh'
    repositoryForm.password = ''
    repositoryForm.access_key = ''
    repositoryForm.secret_key = ''
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

function buildRepositoryURL(): string {
  const bucket = repositoryForm.bucket.trim()
  if (!bucket) return ''
  let endpoint = repositoryForm.endpoint.trim().replace(/\/+$/, '')
  if (repositoryForm.provider === 'cloudflare_r2') {
    const accountID = repositoryForm.account_id.trim()
    if (!accountID) return ''
    const jurisdiction = repositoryForm.jurisdiction === 'default' ? '' : `.${repositoryForm.jurisdiction}`
    endpoint = `https://${accountID}${jurisdiction}.r2.cloudflarestorage.com`
  }
  if (!/^https?:\/\//.test(endpoint)) return ''
  const prefix = repositoryForm.prefix.trim().replace(/^\/+|\/+$/g, '')
  return `s3:${endpoint}/${bucket}${prefix ? `/${prefix}` : ''}`
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
    error.value = '配置尚不完整：请检查名称、Account ID/Endpoint、Bucket、仓库密码和访问密钥。'
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
  return provider === 'cloudflare_r2' ? 'Cloudflare R2' : 'S3 兼容存储'
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
          ['projects', '备份项目', '04'], ['runs', '运行记录', '05'],
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
          <h1>{{ { overview: '备份总览', servers: '服务器', repositories: '备份仓库', projects: '备份项目', runs: '运行记录', profile: '个人中心' }[activeTab] }}</h1>
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
          <div class="panel-heading"><div><p class="eyebrow">CLOUDFLARE R2 ONBOARDING</p><h2>先在 Cloudflare 创建专用凭据</h2></div><a class="doc-link" href="https://dash.cloudflare.com/?to=/:account/r2/api-tokens" target="_blank" rel="noreferrer">打开 R2 API Tokens ↗</a></div>
          <div class="guide-steps">
            <article><span>1</span><div><strong>创建 Bucket</strong><small>Storage &amp; databases → R2 → Create bucket。建议每个环境使用独立 Bucket。</small></div></article>
            <article><span>2</span><div><strong>创建 R2 API Token</strong><small>选择 Object Read &amp; Write，并限制到这个 Bucket；Restic 初始化、备份和清理都需要读写权限。</small></div></article>
            <article><span>3</span><div><strong>只复制一次凭据</strong><small>保存 Access Key ID、Secret Access Key 和 Account ID。Secret 创建后无法再次查看。</small></div></article>
          </div>
          <p class="guide-note">R2 不是 AWS S3，但提供 S3 兼容 API。VaultMesh 使用标准 S3 凭据和 Restic S3 后端连接，R2 的 Region 固定使用 <code>auto</code>。</p>
        </section>
        <div class="content-grid repository-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">STORAGE CHANNELS</p><h2>独立备份仓库</h2></div><span class="sample-size">{{ repositories.length }} CHANNELS</span></div>
            <div v-if="!repositories.length" class="empty-state">尚未配置仓库。</div>
            <div v-else class="card-list"><article v-for="repository in repositories" :key="repository.id" class="data-card repository-card"><div><strong>{{ repository.name }}</strong><small>{{ providerLabel(repository.provider) }} · 被 {{ repositoryUsageCount(repository.id) }} 个项目使用</small></div><span class="status-pill online">全局渠道</span><code>{{ repository.url }}</code></article></div>
          </section>
          <aside class="panel form-panel wide-form"><p class="eyebrow">NEW STORAGE CHANNEL</p><h2>添加备份仓库</h2><p class="form-intro">仓库是全局存储渠道，不绑定服务器。创建项目时再选择由哪个 Agent 写入。</p>
            <form @submit.prevent="createRepository">
              <div class="form-row"><label>存储类型<select v-model="repositoryForm.provider"><option value="cloudflare_r2">Cloudflare R2</option><option value="s3_compatible">其他 S3 兼容存储</option></select></label><label>渠道名称<input v-model="repositoryForm.name" required maxlength="100" placeholder="生产环境 R2" /></label></div>
              <template v-if="repositoryForm.provider === 'cloudflare_r2'">
                <div class="form-row"><label>Cloudflare Account ID<input v-model.trim="repositoryForm.account_id" required maxlength="64" autocomplete="off" placeholder="32 位 Account ID" /></label><label>数据管辖区<select v-model="repositoryForm.jurisdiction"><option value="default">默认 / Global</option><option value="eu">European Union (EU)</option><option value="fedramp">FedRAMP</option></select></label></div>
              </template>
              <label v-else>S3 Endpoint<input v-model.trim="repositoryForm.endpoint" required placeholder="https://s3.example.com" /></label>
              <div class="form-row"><label>Bucket<input v-model.trim="repositoryForm.bucket" required placeholder="vaultmesh-backups" /></label><label>仓库目录前缀<input v-model.trim="repositoryForm.prefix" placeholder="vaultmesh" /><small class="field-help">可留空；用于在 Bucket 内隔离 Restic 数据。</small></label></div>
              <div class="repository-preview"><span>RESTIC CHANNEL BASE</span><code>{{ repositoryURL || '填写 Account ID / Endpoint 与 Bucket 后自动生成' }}</code><small>下发给 Agent 时自动追加 <code>/&lt;server-id&gt;</code>，同一渠道中的不同服务器使用独立 Restic 仓库路径。</small></div>
              <div class="form-row"><label>Access Key ID<input v-model.trim="repositoryForm.access_key" required autocomplete="off" /></label><label>Secret Access Key<input v-model="repositoryForm.secret_key" required type="password" autocomplete="new-password" /></label></div>
              <label>Region<input v-model="repositoryForm.region" required :readonly="repositoryForm.provider === 'cloudflare_r2'" placeholder="auto" /><small class="field-help">Cloudflare R2 使用 <code>auto</code>；其他厂商按其 S3 文档填写。</small></label>
              <label>Restic 仓库密码<div class="field-action"><input v-model="repositoryForm.password" type="password" required autocomplete="new-password" /><button type="button" class="ghost" @click="generateRepositoryPassword">安全生成</button></div><small class="field-help">它用于 Restic 端到端加密，和 R2 Secret Key 不是同一个密码；丢失后无法恢复快照。</small></label>
              <div class="form-actions"><button type="button" class="ghost" @click="checkRepositoryConfiguration">检查配置</button><button class="primary" :disabled="loading || !repositoryReady">加密并保存渠道</button></div>
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
              <article v-for="project in group.projects" :key="project.id" class="project-card">
                <div class="project-top">
                  <div><strong>{{ project.name }}</strong><small>{{ serverName(project.server_id) }} · {{ repositoryName(project.repository_id) }}</small></div>
                  <div class="project-actions"><span class="status-pill online">Revision {{ project.revision }}</span><button type="button" class="ghost compact" :disabled="loading || queuedProjectIDs.has(project.id)" @click="runNow(project)">{{ queuedProjectIDs.has(project.id) ? '已排队 ✓' : '立即备份' }}</button></div>
                </div>
                <div class="project-source-list">
                  <span v-for="source in project.sources" :key="source.id" class="source-chip" :class="source.type">{{ sourceSummary(source) }}</span>
                </div>
                <div class="schedule-overview">
                  <div><small>执行计划</small><strong>{{ cronDescription(project.schedule.cron) }}</strong><span>{{ project.schedule.timezone }}<template v-if="project.schedule.jitter_seconds"> · 最多延后 {{ Math.round(project.schedule.jitter_seconds / 60) }} 分钟</template></span></div>
                  <div class="next-run"><small>下次计划</small><strong>{{ formatNextRun(project) }}</strong><span>最长运行 {{ Math.round(project.schedule.max_runtime_seconds / 3600) }} 小时</span></div>
                </div>
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
                <div class="section-title"><span>2</span><div><strong>备份内容</strong><small>可添加多个数据源；任一数据源失败会标记本次任务失败</small></div></div>
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
              </section>

              <section class="form-section">
                <div class="section-title"><span>3</span><div><strong>执行计划</strong><small>所有时间均按所选时区解释</small></div></div>
                <div class="form-row"><label>计划类型<select v-model="projectForm.schedule_mode"><option value="daily">每天</option><option value="weekly">每周</option><option value="custom">高级 Cron</option></select></label><label v-if="projectForm.schedule_mode !== 'custom'">开始时间<input v-model="projectForm.schedule_time" type="time" required /></label><label v-else>Cron（5 段）<input v-model="projectForm.custom_cron" required placeholder="0 2 * * *" /></label></div>
                <div v-if="projectForm.schedule_mode === 'weekly'" class="weekday-grid" role="group" aria-label="选择星期"><button v-for="(day, index) in weekdays" :key="day" type="button" :class="{ active: projectForm.weekday === String(index) }" @click="projectForm.weekday = String(index)">{{ day }}</button></div>
                <label>时区<input v-model="projectForm.timezone" required list="timezone-options" /><datalist id="timezone-options"><option v-for="timezone in commonTimezones" :key="timezone" :value="timezone" /></datalist></label>
                <div class="form-row"><label>随机延迟<select v-model.number="projectForm.jitter_minutes"><option :value="0">不延迟</option><option :value="5">最多 5 分钟</option><option :value="10">最多 10 分钟</option><option :value="30">最多 30 分钟</option><option :value="60">最多 60 分钟</option></select></label><label>最长运行时间<select v-model.number="projectForm.max_runtime_hours"><option :value="1">1 小时</option><option :value="3">3 小时</option><option :value="6">6 小时</option><option :value="12">12 小时</option><option :value="24">24 小时</option></select></label></div>
                <div class="schedule-preview"><span>计划预览</span><strong>{{ projectSchedulePreview }}</strong><code>{{ projectCron }}</code></div>
              </section>
              <button class="primary" :disabled="loading || !servers.length || !repositories.length">创建并下发</button>
            </form>
          </aside>
        </div>
      </template>

      <template v-else-if="activeTab === 'runs'">
        <section class="panel">
          <div class="panel-heading"><div><p class="eyebrow">RUN HISTORY</p><h2>最近 100 次运行</h2></div></div>
          <div v-if="!runs.length" class="empty-state">尚无运行记录。</div>
          <div v-else class="table-wrap"><table><thead><tr><th>项目</th><th>状态</th><th>开始时间</th><th>快照</th><th>错误</th></tr></thead><tbody>
            <tr v-for="run in runs" :key="run.id"><td><strong>{{ projectNames.get(run.project_id) ?? run.project_id }}</strong><small>{{ run.id }}</small></td><td><span class="status-pill" :class="run.status">{{ statusLabel(run.status) }}</span></td><td>{{ formatDate(run.started_at) }}</td><td><code>{{ run.snapshot_id ? run.snapshot_id.slice(0, 16) : '—' }}</code></td><td class="error-cell">{{ run.error_message || '—' }}</td></tr>
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
