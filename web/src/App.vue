<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { APIError, api, getAPIBaseURL, getToken, setToken } from './api'
import type { Dashboard, EnrollmentResult, Project, Repository, Run, Server } from './types'

type Tab = 'overview' | 'servers' | 'repositories' | 'projects' | 'runs'
type SourceType = 'files' | 'mysql' | 'postgresql'
type ScheduleMode = 'daily' | 'weekly' | 'custom'

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
}

let sourceSequence = 0

const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
const commonTimezones = ['Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Tokyo', 'UTC', 'Europe/London', 'America/New_York']
const apiBaseURL = getAPIBaseURL()

const tokenInput = ref('')
const authenticated = ref(Boolean(getToken()))
const activeTab = ref<Tab>('overview')
const loading = ref(false)
const error = ref('')
const success = ref('')

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

const serverForm = reactive({ name: '' })
const repositoryForm = reactive({
  server_id: '',
  name: '',
  url: '',
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

const repositoriesForProject = computed(() =>
  repositories.value.filter((repository) => repository.server_id === projectForm.server_id),
)

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

async function login() {
  error.value = ''
  setToken(tokenInput.value.trim())
  try {
    await loadAll()
    authenticated.value = true
    tokenInput.value = ''
  } catch (cause) {
    setToken('')
    authenticated.value = false
    showError(cause)
  }
}

function logout() {
  setToken('')
  authenticated.value = false
  tokenInput.value = ''
}

async function loadAll() {
  loading.value = true
  error.value = ''
  try {
    const [dashboardResult, serverResult, repositoryResult, projectResult, runResult] = await Promise.all([
      api<Dashboard>('/api/v1/dashboard'),
      api<{ items: Server[] }>('/api/v1/servers'),
      api<{ items: Repository[] }>('/api/v1/repositories'),
      api<{ items: Project[] }>('/api/v1/projects'),
      api<{ items: Run[] }>('/api/v1/runs?limit=100'),
    ])
    dashboard.value = dashboardResult
    servers.value = serverResult.items ?? []
    repositories.value = repositoryResult.items ?? []
    projects.value = projectResult.items ?? []
    runs.value = runResult.items ?? []
    selectDefaults()
  } finally {
    loading.value = false
  }
}

function selectDefaults() {
  if (!repositoryForm.server_id && servers.value.length) repositoryForm.server_id = servers.value[0].id
  if (!projectForm.server_id && servers.value.length) projectForm.server_id = servers.value[0].id
  if (!repositoriesForProject.value.some((repository) => repository.id === projectForm.repository_id)) {
    projectForm.repository_id = repositoriesForProject.value[0]?.id ?? ''
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
    const environment: Record<string, string> = {}
    if (repositoryForm.access_key) environment.AWS_ACCESS_KEY_ID = repositoryForm.access_key
    if (repositoryForm.secret_key) environment.AWS_SECRET_ACCESS_KEY = repositoryForm.secret_key
    if (repositoryForm.region) environment.AWS_DEFAULT_REGION = repositoryForm.region
    await api<Repository>('/api/v1/repositories', {
      method: 'POST',
      body: JSON.stringify({
        server_id: repositoryForm.server_id,
        name: repositoryForm.name,
        url: repositoryForm.url,
        password: repositoryForm.password,
        environment,
      }),
    })
    repositoryForm.name = ''
    repositoryForm.url = ''
    repositoryForm.password = ''
    repositoryForm.access_key = ''
    repositoryForm.secret_key = ''
    await loadAll()
    success.value = '仓库配置已加密保存。'
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
    port: type === 'mysql' ? 3306 : 5432,
    username: '',
    password: '',
    database: '',
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
    success.value = `已向 ${project.name} 所在 Agent 排队发送手动备份。`
  })
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
    error.value = cause.message
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

function pageDescription(tab: Tab): string {
  return {
    overview: '运行态势、成功率与风险信号',
    servers: 'Agent 在线状态与配置收敛',
    repositories: 'Restic 目标与凭据边界',
    projects: '数据源、调度策略与下次执行',
    runs: '端到端备份执行证据',
  }[tab]
}

function navBadge(tab: Tab): number {
  return {
    overview: attentionCount.value,
    servers: servers.value.length,
    repositories: repositories.value.length,
    projects: projects.value.length,
    runs: runs.value.length,
  }[tab]
}

function serverName(id: string): string {
  return servers.value.find((server) => server.id === id)?.name ?? id
}

function repositoryName(id: string): string {
  return repositories.value.find((repository) => repository.id === id)?.name ?? id
}

function sourceTypeLabel(type: SourceType): string {
  return { files: '文件与目录', mysql: 'MySQL', postgresql: 'PostgreSQL' }[type]
}

function sourceSummary(source: Project['sources'][number]): string {
  if (source.type === 'files') {
    const paths = source.paths ?? []
    const visible = paths.slice(0, 2).join(', ')
    return `文件 · ${visible || '未配置路径'}${paths.length > 2 ? ` +${paths.length - 2}` : ''}`
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

onMounted(async () => {
  if (!authenticated.value) return
  try {
    await loadAll()
  } catch (cause) {
    if (cause instanceof APIError && cause.status === 401) logout()
    showError(cause)
  }
})
</script>

<template>
  <main v-if="!authenticated" class="login-shell">
    <section class="login-card">
      <div class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></div>
      <p class="eyebrow">SELF-HOSTED BACKUP CONTROL</p>
      <h1>VaultMesh</h1>
      <p class="muted">连接每一台服务器，看见每一次备份是否真正完成。</p>
      <form class="login-form" @submit.prevent="login">
        <label for="admin-token">管理员令牌</label>
        <input id="admin-token" v-model="tokenInput" type="password" autocomplete="current-password" required placeholder="VAULTMESH_ADMIN_TOKEN" />
        <button class="primary" :disabled="loading">{{ loading ? '正在验证…' : '进入控制台' }}</button>
      </form>
      <p v-if="error" class="message error">{{ error }}</p>
      <p class="security-note">令牌只保存在当前浏览器标签页的 sessionStorage 中。</p>
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
        ] as [Tab, string, string][] )" :key="item[0]" :class="{ active: activeTab === item[0] }" @click="activeTab = item[0]">
          <span class="nav-index">{{ item[2] }}</span><span class="nav-label">{{ item[1] }}</span><span v-if="navBadge(item[0])" class="nav-badge">{{ navBadge(item[0]) }}</span>
        </button>
      </nav>
      <div class="sidebar-system">
        <div><span class="system-pulse"></span><strong>API 已连接</strong></div>
        <code>{{ apiBaseURL.replace(/^https?:\/\//, '') }}</code>
        <small>Control Plane 与 Web 独立运行</small>
      </div>
      <div class="sidebar-footer">
        <button class="ghost" @click="loadAll" :disabled="loading">{{ loading ? '同步中…' : '刷新' }}</button>
        <button class="ghost" @click="logout">退出</button>
      </div>
    </aside>

    <section class="workspace">
      <header class="topbar">
        <div>
          <p class="breadcrumb">VAULTMESH <span>/</span> {{ activeTab.toUpperCase() }}</p>
          <h1>{{ { overview: '备份总览', servers: '服务器', repositories: '备份仓库', projects: '备份项目', runs: '运行记录' }[activeTab] }}</h1>
          <p class="page-description">{{ pageDescription(activeTab) }}</p>
        </div>
        <div class="topbar-status"><span class="scope-pill">LIVE DATA · 100 RUNS</span><span class="live-indicator"><i></i>{{ dashboard.servers_online }}/{{ dashboard.servers_total }} Agent 在线</span></div>
      </header>

      <p v-if="error" class="message error">{{ error }}</p>
      <p v-if="success" class="message success">{{ success }}</p>

      <template v-if="activeTab === 'overview'">
        <div class="overview-strip">
          <div><span class="operational-dot"></span><strong>Backup operations</strong><small>基于最近 100 次运行和当前 Agent 心跳聚合</small></div>
          <span>最近刷新 · {{ new Intl.DateTimeFormat('zh-CN', { timeStyle: 'medium' }).format(new Date()) }}</span>
        </div>
        <div class="metric-grid dense-metrics">
          <article class="metric"><header><span>AGENT HEALTH</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ dashboard.servers_online }}<small>/{{ dashboard.servers_total }}</small></strong><em>{{ onlineRate }}%</em></div><footer>在线节点 <span>{{ dashboard.servers_total - dashboard.servers_online }} 异常</span></footer></article>
          <article class="metric"><header><span>SUCCESS RATE</span><i class="metric-signal" :class="successRate >= 95 ? 'good' : successRate >= 80 ? 'warn' : 'bad'"></i></header><div class="metric-value"><strong>{{ successRate }}<small>%</small></strong><em>{{ successfulRunCount }}/{{ totalRunCount }}</em></div><footer>最近 100 次 <span>{{ totalRunCount ? '有效样本' : '等待数据' }}</span></footer></article>
          <article class="metric"><header><span>24H SNAPSHOTS</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ dashboard.runs_succeeded }}</strong><em>success</em></div><footer>有效快照 <span>{{ dashboard.runs_partial }} 次部分成功</span></footer></article>
          <article class="metric"><header><span>PROTECTED SOURCES</span><i class="metric-signal"></i></header><div class="metric-value"><strong>{{ protectedSourceCount }}</strong><em>{{ projects.length }} projects</em></div><footer>文件与数据库 <span>{{ repositories.length }} 个仓库</span></footer></article>
          <article class="metric" :class="{ alert: attentionCount > 0 }"><header><span>NEEDS ATTENTION</span><i class="metric-signal" :class="attentionCount ? 'bad' : 'good'"></i></header><div class="metric-value"><strong>{{ attentionCount }}</strong><em>24 hours</em></div><footer>失败或部分成功 <span>{{ attentionCount ? '需要处理' : '状态正常' }}</span></footer></article>
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
        <div class="content-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">RESTIC TARGETS</p><h2>仓库列表</h2></div></div>
            <div v-if="!repositories.length" class="empty-state">尚未配置仓库。</div>
            <div v-else class="card-list"><article v-for="repository in repositories" :key="repository.id" class="data-card"><div><strong>{{ repository.name }}</strong><small>{{ serverName(repository.server_id) }}</small></div><code>{{ repository.url }}</code></article></div>
          </section>
          <aside class="panel form-panel wide-form"><p class="eyebrow">NEW REPOSITORY</p><h2>添加 S3 仓库</h2>
            <form @submit.prevent="createRepository">
              <label>服务器<select v-model="repositoryForm.server_id" required><option value="" disabled>选择服务器</option><option v-for="server in servers" :key="server.id" :value="server.id">{{ server.name }}</option></select></label>
              <label>名称<input v-model="repositoryForm.name" required placeholder="R2 Main" /></label>
              <label>Restic S3 URL<input v-model="repositoryForm.url" required placeholder="s3:https://ACCOUNT.r2.cloudflarestorage.com/bucket/prefix" /></label>
              <label>Restic 仓库密码<input v-model="repositoryForm.password" type="password" required autocomplete="new-password" /></label>
              <label>Access Key<input v-model="repositoryForm.access_key" autocomplete="off" /></label>
              <label>Secret Key<input v-model="repositoryForm.secret_key" type="password" autocomplete="new-password" /></label>
              <label>Region<input v-model="repositoryForm.region" placeholder="auto" /></label>
              <button class="primary" :disabled="loading || !servers.length">加密并保存</button>
            </form>
          </aside>
        </div>
      </template>

      <template v-else-if="activeTab === 'projects'">
        <div class="content-grid projects-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">DESIRED STATE</p><h2>项目列表</h2></div></div>
            <div v-if="!projects.length" class="empty-state">尚未创建备份项目。</div>
            <div v-else class="card-list">
              <article v-for="project in projects" :key="project.id" class="project-card">
                <div class="project-top">
                  <div><strong>{{ project.name }}</strong><small>{{ serverName(project.server_id) }} · {{ repositoryName(project.repository_id) }}</small></div>
                  <div class="project-actions"><span class="status-pill online">Revision {{ project.revision }}</span><button class="ghost compact" :disabled="loading" @click="runNow(project)">立即备份</button></div>
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
          <aside class="panel form-panel project-builder"><p class="eyebrow">NEW PROJECT</p><h2>创建备份项目</h2><p class="form-intro">一个项目可以组合文件、MySQL 和 PostgreSQL 数据源，并在同一个 Restic 快照中归档。</p>
            <form @submit.prevent="createProject">
              <section class="form-section">
                <div class="section-title"><span>1</span><div><strong>基础信息</strong><small>选择 Agent 和快照写入位置</small></div></div>
                <div class="form-row"><label>服务器<select v-model="projectForm.server_id" required @change="selectDefaults"><option value="" disabled>选择服务器</option><option v-for="server in servers" :key="server.id" :value="server.id">{{ server.name }}</option></select></label><label>备份仓库<select v-model="projectForm.repository_id" required><option value="" disabled>选择当前服务器的仓库</option><option v-for="repository in repositoriesForProject" :key="repository.id" :value="repository.id">{{ repository.name }}</option></select></label></div>
                <label>项目名称<input v-model="projectForm.name" required maxlength="100" placeholder="例如：应用数据每日备份" /></label>
              </section>

              <section class="form-section">
                <div class="section-title"><span>2</span><div><strong>备份内容</strong><small>可添加多个数据源；任一数据源失败会标记本次任务失败</small></div></div>
                <article v-for="(source, index) in projectForm.sources" :key="source.key" class="source-editor">
                  <header><strong>数据源 {{ index + 1 }}</strong><button v-if="projectForm.sources.length > 1" type="button" class="text-button danger-text" @click="removeProjectSource(index)">移除</button></header>
                  <label>类型<select v-model="source.type" @change="changeSourceType(source)"><option value="files">文件与目录</option><option value="mysql">MySQL 逻辑备份</option><option value="postgresql">PostgreSQL 逻辑备份</option></select></label>
                  <template v-if="source.type === 'files'">
                    <label>绝对路径（每行一个）<textarea v-model="source.paths" rows="3" required placeholder="/etc&#10;/opt/app"></textarea></label>
                    <label>排除规则（每行一个）<textarea v-model="source.excludes" rows="2" placeholder="/opt/app/cache/**"></textarea></label>
                  </template>
                  <template v-else>
                    <div class="database-note"><strong>{{ sourceTypeLabel(source.type) }} 逻辑备份</strong><span>Agent 将执行 {{ source.type === 'mysql' ? 'mysqldump' : 'pg_dump' }}，临时凭据和产物仅保存在目标服务器。</span></div>
                    <div class="form-row database-host"><label>数据库地址<input v-model="source.host" required placeholder="127.0.0.1" /></label><label>端口<input v-model.number="source.port" required type="number" min="1" max="65535" /></label></div>
                    <div class="form-row"><label>数据库名称<input v-model="source.database" required placeholder="application" /></label><label>备份用户<input v-model="source.username" required autocomplete="off" placeholder="vaultmesh_backup" /></label></div>
                    <label>数据库密码<input v-model="source.password" required type="password" autocomplete="new-password" /><small class="field-help">提交后使用主密钥加密，管理 API 不会再次返回明文。</small></label>
                  </template>
                </article>
                <div class="source-add-row"><span>添加数据源</span><button type="button" class="ghost compact" @click="addProjectSource('files')">+ 文件</button><button type="button" class="ghost compact" @click="addProjectSource('mysql')">+ MySQL</button><button type="button" class="ghost compact" @click="addProjectSource('postgresql')">+ PostgreSQL</button></div>
              </section>

              <section class="form-section">
                <div class="section-title"><span>3</span><div><strong>执行计划</strong><small>所有时间均按所选时区解释</small></div></div>
                <div class="form-row"><label>计划类型<select v-model="projectForm.schedule_mode"><option value="daily">每天</option><option value="weekly">每周</option><option value="custom">高级 Cron</option></select></label><label v-if="projectForm.schedule_mode !== 'custom'">开始时间<input v-model="projectForm.schedule_time" type="time" required /></label><label v-else>Cron（5 段）<input v-model="projectForm.custom_cron" required placeholder="0 2 * * *" /></label></div>
                <div v-if="projectForm.schedule_mode === 'weekly'" class="weekday-grid" role="group" aria-label="选择星期"><button v-for="(day, index) in weekdays" :key="day" type="button" :class="{ active: projectForm.weekday === String(index) }" @click="projectForm.weekday = String(index)">{{ day }}</button></div>
                <label>时区<input v-model="projectForm.timezone" required list="timezone-options" /><datalist id="timezone-options"><option v-for="timezone in commonTimezones" :key="timezone" :value="timezone" /></datalist></label>
                <div class="form-row"><label>随机延迟<select v-model.number="projectForm.jitter_minutes"><option :value="0">不延迟</option><option :value="5">最多 5 分钟</option><option :value="10">最多 10 分钟</option><option :value="30">最多 30 分钟</option><option :value="60">最多 60 分钟</option></select></label><label>最长运行时间<select v-model.number="projectForm.max_runtime_hours"><option :value="1">1 小时</option><option :value="3">3 小时</option><option :value="6">6 小时</option><option :value="12">12 小时</option><option :value="24">24 小时</option></select></label></div>
                <div class="schedule-preview"><span>计划预览</span><strong>{{ projectSchedulePreview }}</strong><code>{{ projectCron }}</code></div>
              </section>
              <button class="primary" :disabled="loading || !repositoriesForProject.length">创建并下发</button>
            </form>
          </aside>
        </div>
      </template>

      <template v-else>
        <section class="panel">
          <div class="panel-heading"><div><p class="eyebrow">RUN HISTORY</p><h2>最近 100 次运行</h2></div></div>
          <div v-if="!runs.length" class="empty-state">尚无运行记录。</div>
          <div v-else class="table-wrap"><table><thead><tr><th>项目</th><th>状态</th><th>开始时间</th><th>快照</th><th>错误</th></tr></thead><tbody>
            <tr v-for="run in runs" :key="run.id"><td><strong>{{ projectNames.get(run.project_id) ?? run.project_id }}</strong><small>{{ run.id }}</small></td><td><span class="status-pill" :class="run.status">{{ statusLabel(run.status) }}</span></td><td>{{ formatDate(run.started_at) }}</td><td><code>{{ run.snapshot_id ? run.snapshot_id.slice(0, 16) : '—' }}</code></td><td class="error-cell">{{ run.error_message || '—' }}</td></tr>
          </tbody></table></div>
        </section>
      </template>
    </section>
  </div>
</template>
