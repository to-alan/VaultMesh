<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { APIError, api, getToken, setToken } from './api'
import type { Dashboard, EnrollmentResult, Project, Repository, Run, Server } from './types'

type Tab = 'overview' | 'servers' | 'repositories' | 'projects' | 'runs'

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
  paths: '/etc',
  excludes: '',
  cron: '0 2 * * *',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
  jitter_seconds: 300,
})

const repositoriesForProject = computed(() =>
  repositories.value.filter((repository) => repository.server_id === projectForm.server_id),
)

const projectNames = computed(() => new Map(projects.value.map((project) => [project.id, project.name])))

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
    const paths = lines(projectForm.paths)
    const excludes = lines(projectForm.excludes)
    await api<Project>('/api/v1/projects', {
      method: 'POST',
      body: JSON.stringify({
        server_id: projectForm.server_id,
        repository_id: projectForm.repository_id,
        name: projectForm.name,
        sources: [{ type: 'files', paths, excludes, required: true }],
        schedule: {
          cron: projectForm.cron,
          timezone: projectForm.timezone,
          jitter_seconds: Number(projectForm.jitter_seconds),
          max_runtime_seconds: 21600,
          missed_run_policy: 'skip',
          concurrency_policy: 'forbid',
        },
      }),
    })
    projectForm.name = ''
    await loadAll()
    success.value = '项目已创建，Agent 将在下一次同步时应用配置。'
  })
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

function serverName(id: string): string {
  return servers.value.find((server) => server.id === id)?.name ?? id
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
  return `sudo vaultmesh-agent --server ${window.location.origin} --enrollment-token ${result.enrollment_token}`
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
        <div><strong>VaultMesh</strong><small>Control Plane</small></div>
      </div>
      <nav aria-label="主导航">
        <button v-for="item in ([
          ['overview', '总览'], ['servers', '服务器'], ['repositories', '备份仓库'],
          ['projects', '备份项目'], ['runs', '运行记录'],
        ] as [Tab, string][] )" :key="item[0]" :class="{ active: activeTab === item[0] }" @click="activeTab = item[0]">
          <span class="nav-dot"></span>{{ item[1] }}
        </button>
      </nav>
      <div class="sidebar-footer">
        <button class="ghost" @click="loadAll" :disabled="loading">刷新数据</button>
        <button class="ghost" @click="logout">退出</button>
      </div>
    </aside>

    <section class="workspace">
      <header class="topbar">
        <div>
          <p class="eyebrow">BACKUP HEALTH</p>
          <h1>{{ { overview: '备份总览', servers: '服务器', repositories: '备份仓库', projects: '备份项目', runs: '运行记录' }[activeTab] }}</h1>
        </div>
        <span class="live-indicator"><i></i>{{ dashboard.servers_online }}/{{ dashboard.servers_total }} Agent 在线</span>
      </header>

      <p v-if="error" class="message error">{{ error }}</p>
      <p v-if="success" class="message success">{{ success }}</p>

      <template v-if="activeTab === 'overview'">
        <div class="metric-grid">
          <article class="metric"><span>服务器</span><strong>{{ dashboard.servers_total }}</strong><small>{{ dashboard.servers_online }} 台在线</small></article>
          <article class="metric"><span>备份项目</span><strong>{{ dashboard.projects_total }}</strong><small>已纳入统一策略</small></article>
          <article class="metric good"><span>24 小时成功</span><strong>{{ dashboard.runs_succeeded }}</strong><small>形成有效快照</small></article>
          <article class="metric bad"><span>需要关注</span><strong>{{ dashboard.runs_failed + dashboard.runs_partial }}</strong><small>{{ dashboard.runs_partial }} 次部分成功</small></article>
        </div>
        <section class="panel">
          <div class="panel-heading"><div><p class="eyebrow">LATEST ACTIVITY</p><h2>最近运行</h2></div><button class="text-button" @click="activeTab = 'runs'">查看全部</button></div>
          <div v-if="!runs.length" class="empty-state">尚无运行记录。创建服务器、仓库和项目后，Agent 将按计划执行。</div>
          <div v-else class="run-list">
            <article v-for="run in runs.slice(0, 6)" :key="run.id" class="run-row">
              <span class="status-pill" :class="run.status">{{ statusLabel(run.status) }}</span>
              <div><strong>{{ projectNames.get(run.project_id) ?? run.project_id }}</strong><small>{{ formatDate(run.started_at) }}</small></div>
              <code>{{ run.snapshot_id ? run.snapshot_id.slice(0, 12) : run.error_code || '—' }}</code>
            </article>
          </div>
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
        <div class="content-grid">
          <section class="panel">
            <div class="panel-heading"><div><p class="eyebrow">DESIRED STATE</p><h2>项目列表</h2></div></div>
            <div v-if="!projects.length" class="empty-state">尚未创建备份项目。</div>
            <div v-else class="card-list"><article v-for="project in projects" :key="project.id" class="project-card"><div class="project-top"><div><strong>{{ project.name }}</strong><small>{{ serverName(project.server_id) }}</small></div><div class="project-actions"><span class="status-pill online">Revision {{ project.revision }}</span><button class="ghost compact" :disabled="loading" @click="runNow(project)">立即备份</button></div></div><div class="project-meta"><span>{{ project.schedule.cron }}</span><span>{{ project.schedule.timezone }}</span><span>{{ project.sources.flatMap((source) => source.paths ?? []).join(', ') || '数据库逻辑备份' }}</span></div></article></div>
          </section>
          <aside class="panel form-panel wide-form"><p class="eyebrow">NEW PROJECT</p><h2>创建文件备份</h2>
            <form @submit.prevent="createProject">
              <label>服务器<select v-model="projectForm.server_id" required @change="selectDefaults"><option value="" disabled>选择服务器</option><option v-for="server in servers" :key="server.id" :value="server.id">{{ server.name }}</option></select></label>
              <label>仓库<select v-model="projectForm.repository_id" required><option value="" disabled>选择当前服务器的仓库</option><option v-for="repository in repositoriesForProject" :key="repository.id" :value="repository.id">{{ repository.name }}</option></select></label>
              <label>项目名称<input v-model="projectForm.name" required placeholder="System Configuration" /></label>
              <label>绝对路径（每行一个）<textarea v-model="projectForm.paths" rows="3" required></textarea></label>
              <label>排除规则（每行一个）<textarea v-model="projectForm.excludes" rows="2" placeholder="/etc/ssl/private/**"></textarea></label>
              <div class="form-row"><label>Cron<input v-model="projectForm.cron" required /></label><label>时区<input v-model="projectForm.timezone" required /></label></div>
              <label>随机延迟（秒）<input v-model.number="projectForm.jitter_seconds" type="number" min="0" max="3600" /></label>
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
