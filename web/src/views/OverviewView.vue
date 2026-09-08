<script setup lang="ts">
import { computed } from 'vue'
import { backupOutcomeGroups, backupTrend, projectHealthRows, summarizeBackups } from '../overview'
import { cronDescription, describeProjectHealth, formatCountdown, formatDate, formatNextRun, projectHealthLabel, runOperationLabel, statusLabel } from '../display'
import type { Tab } from '../display'
import type { Dashboard, Project, ProjectHealth, Run, Server } from '../types'

const props = defineProps<{
  dashboard: Dashboard
  projects: Project[]
  health: ProjectHealth[]
  servers: Server[]
  runs: Run[]
  repositoryCount: number
  nowEpoch: number
  lastUpdatedAt: number | null
  syncError: string
}>()
const emit = defineEmits<{ navigate: [tab: Tab] }>()
const summary = computed(() => summarizeBackups(props.runs))
const trend = computed(() => backupTrend(props.runs, props.nowEpoch))
const healthRows = computed(() => projectHealthRows(props.projects, props.health))
const attentionRuns = computed(() => props.dashboard.runs_failed + props.dashboard.runs_partial)
const riskProjects = computed(() => props.health.filter((item) => ['late', 'overdue', 'invalid'].includes(item.status)).length)
const completedToday = computed(() => props.dashboard.runs_succeeded + attentionRuns.value)
const successRateToday = computed(() => completedToday.value ? Math.round(props.dashboard.runs_succeeded / completedToday.value * 100) : null)
const serverNames = computed(() => new Map(props.servers.map((server) => [server.id, server.name])))
const projectNames = computed(() => new Map(props.projects.map((project) => [project.id, project.name])))
const next = computed(() => props.projects.filter((project) => project.enabled && project.next_run_at)
  .map((project) => ({ project, at: Date.parse(project.next_run_at!) }))
  .filter((item) => Number.isFinite(item.at) && item.at >= props.nowEpoch)
  .sort((a, b) => a.at - b.at)[0])

function pointTitle(point: typeof trend.value.points[number]): string {
  return `${point.label} · ${point.total} 次：${point.distribution.map((group) => `${group.label} ${group.count}`).join('，')}`
}
</script>

<template>
  <div class="overview-strip">
    <div><strong>备份运行状态</strong><small>统计窗口：最近 24 小时 · 按运行开始时间</small></div>
    <span>{{ syncError ? '同步失败，正在显示上次数据' : lastUpdatedAt ? `更新于 ${formatDate(new Date(lastUpdatedAt).toISOString())}` : '等待首次同步' }}</span>
  </div>

  <section v-if="!projects.length" class="panel setup-guide">
    <div><h2>建立第一条备份链路</h2><p class="muted">接入服务器、配置存储，再创建备份项目。完成后请用一次文件恢复验证备份。</p></div>
    <div class="form-actions">
      <button class="ghost" @click="emit('navigate', 'servers')">1. {{ servers.length ? '查看服务器' : '添加服务器' }}</button>
      <button class="ghost" @click="emit('navigate', 'repositories')">2. {{ repositoryCount ? '查看仓库' : '配置仓库' }}</button>
      <button class="primary" @click="emit('navigate', 'projects')">3. 创建项目</button>
    </div>
  </section>

  <div class="metric-grid overview-metrics">
    <article class="metric">
      <header><span>在线服务器</span></header>
      <div class="metric-value"><strong>{{ dashboard.servers_online }}<small>/ {{ dashboard.servers_total }}</small></strong></div>
      <footer>{{ servers.filter((server) => server.status === 'offline').length }} 台离线 · {{ servers.filter((server) => server.status === 'pending').length }} 台待注册 <button class="text-button" @click="emit('navigate', 'servers')">查看</button></footer>
    </article>
    <article class="metric">
      <header><span>24 小时成功率</span></header>
      <div class="metric-value"><strong>{{ successRateToday === null ? '—' : `${successRateToday}%` }}</strong></div>
      <footer>{{ dashboard.runs_succeeded }} 次成功 / {{ completedToday }} 次已结束备份</footer>
    </article>
    <article class="metric" :class="{ alert: attentionRuns > 0 }">
      <header><span>24 小时异常备份</span></header>
      <div class="metric-value"><strong>{{ attentionRuns }}<small>次</small></strong></div>
      <footer>含部分成功、失败、超时、取消、未知 <button class="text-button" @click="emit('navigate', 'runs')">查看</button></footer>
    </article>
    <article class="metric" :class="{ alert: riskProjects > 0 }">
      <header><span>需要关注的项目</span></header>
      <div class="metric-value"><strong>{{ riskProjects }}<small>/ {{ projects.length }}</small></strong></div>
      <footer>迟到、RPO 超时或计划无效 <button class="text-button" @click="emit('navigate', 'projects')">处理</button></footer>
    </article>
  </div>

  <section class="panel health-panel">
    <div class="panel-heading compact-heading"><div><p class="eyebrow">PROJECT HEALTH</p><h2>项目保护状态</h2></div><button class="text-button" @click="emit('navigate', 'projects')">管理项目 →</button></div>
    <p class="field-help">优先显示风险项目。最近一次成功不代表当前计划仍然健康。</p>
    <div v-if="!healthRows.length" class="empty-state compact-empty">创建项目后，这里会显示备份时限与最近成功时间。</div>
    <div v-else class="table-wrap"><table><thead><tr><th>项目 / 服务器</th><th>当前保护状态</th><th>最近成功完成</th><th>最近运行结果</th><th>下次计划</th></tr></thead><tbody>
      <tr v-for="row in healthRows" :key="row.project.id">
        <td><strong>{{ row.project.name }}</strong><small>{{ serverNames.get(row.project.server_id) || row.project.server_id }}</small></td>
        <td><span class="status-pill" :class="row.health?.status || 'neutral'">{{ projectHealthLabel(row.health) }}</span><small>{{ describeProjectHealth(row.health, nowEpoch) }}</small></td>
        <td>{{ formatDate(row.health?.last_successful_at) }}</td>
        <td><span v-if="row.health?.latest_run_status" class="status-pill" :class="row.health.latest_run_status">{{ statusLabel(row.health.latest_run_status) }}</span><span v-else>尚无运行</span><small>{{ formatDate(row.health?.latest_run_at) }}</small></td>
        <td><strong>{{ formatNextRun(row.project) }}</strong><small>{{ cronDescription(row.project.schedule.cron) }} · {{ row.project.schedule.timezone }}</small></td>
      </tr>
    </tbody></table></div>
  </section>

  <div class="dashboard-grid primary-dashboard overview-charts">
    <section class="panel trend-panel">
      <div class="panel-heading compact-heading"><div><p class="eyebrow">RECENT SAMPLE</p><h2>近 7 日备份分布</h2></div><span class="sample-size">最近 {{ runs.length }} 条运行中的备份样本</span></div>
      <p class="field-help">按浏览器本地日期展示；最多取最近 100 条运行，不代表完整的 7 日统计。</p>
      <div class="chart-legend overview-legend"><span v-for="group in backupOutcomeGroups" :key="group.key"><i :style="{ background: group.color }"></i>{{ group.label }}</span></div>
      <div class="stacked-chart">
        <div class="chart-axis"><span>{{ trend.max }} 次</span><span>{{ Math.floor(trend.max / 2) }}</span><span>0</span></div>
        <div class="chart-plot">
          <div class="chart-grid-lines"><i></i><i></i><i></i></div>
          <div v-for="point in trend.points" :key="point.key" class="chart-column">
            <div class="bar-total" role="img" :aria-label="pointTitle(point)" :title="pointTitle(point)">
              <span v-for="group in [...point.distribution].reverse()" :key="group.key" class="bar-segment" :style="{ height: `${group.count / trend.max * 100}%`, background: group.color }"></span>
            </div><strong>{{ point.total }}</strong><small>{{ point.label }}</small>
          </div>
        </div>
      </div>
    </section>
    <section class="panel distribution-panel">
      <div class="panel-heading compact-heading"><div><p class="eyebrow">BACKUP SAMPLE</p><h2>备份样本结果</h2></div><span class="sample-size">{{ summary.total }} 次备份</span></div>
      <div class="sample-rate"><strong>{{ summary.successRate === null ? '—' : `${summary.successRate}%` }}</strong><span>已结束备份成功率 · {{ summary.completed }} 次</span></div>
      <div class="distribution-list"><div v-for="group in summary.distribution" :key="group.key"><i :style="{ background: group.color }"></i><span>{{ group.label }}</span><strong>{{ group.count }}</strong></div></div>
      <p class="field-help">执行中与已跳过不计入成功率；维护、索引和恢复操作不计为备份。</p>
      <div class="next-backup-summary"><small>下一次备份</small><template v-if="next"><strong>{{ next.project.name }}</strong><span>{{ formatCountdown(next.at - nowEpoch) }} 后 · {{ formatNextRun(next.project) }}</span></template><span v-else>暂无未来计划，等待同步</span></div>
    </section>
  </div>

  <section class="panel recent-panel">
    <div class="panel-heading compact-heading"><div><p class="eyebrow">RECENT ACTIVITY</p><h2>最近操作</h2></div><button class="text-button" @click="emit('navigate', 'runs')">全部运行 →</button></div>
    <div v-if="!runs.length" class="empty-state compact-empty">尚无运行记录。</div>
    <div v-else class="recent-run-grid"><article v-for="run in runs.slice(0, 8)" :key="run.id"><span class="status-line" :class="run.status"></span><div><strong>{{ projectNames.get(run.project_id) || run.project_id }}</strong><small>{{ runOperationLabel(run) }} · {{ formatDate(run.started_at) }}</small></div><span class="status-copy">{{ statusLabel(run.status) }}</span></article></div>
  </section>
</template>
