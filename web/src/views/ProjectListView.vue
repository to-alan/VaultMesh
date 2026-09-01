<script setup lang="ts">
import { computed } from 'vue'
import { useProjectFilters } from '../composables/filters'
import {
  cronDescription,
  describeProjectHealth,
  formatDate,
  formatNextRun,
  maintenanceSummary,
  projectHealthLabel,
  retentionSummary,
  scanSummary,
  sourceSummary,
  statusLabel,
  verificationSummary,
} from '../display'
import type { Project, ProjectHealth, Server } from '../types'

interface PreviewRun {
  status?: string
  finished_at?: string
  started_at?: string
  error_message?: string
  stats?: { snapshots_kept?: number; snapshots_removed?: number }
}

const props = defineProps<{
  projects: Project[]
  servers: Server[]
  health: Map<string, ProjectHealth>
  nowEpoch: number
  repositoryName: (repositoryID: string) => string
  latestPreview: (projectID: string) => PreviewRun | undefined
  queuedProjectIDs: Set<string>
  queuedPreviewProjectIDs: Set<string>
  loading: boolean
}>()

const emit = defineEmits<{
  edit: [project: Project]
  toggle: [project: Project]
  preview: [project: Project]
  run: [project: Project]
  archive: [project: Project]
}>()

// useProjectFilters owns the search/state filters; grouping mirrors the
// server-grouped layout so the overview tab can adopt the same composable.
const groups = computed(() => {
  const assigned = new Set<string>()
  const result: { id: string; name: string; server?: Server; projects: Project[] }[] = props.servers.map((server) => {
    const items = props.projects.filter((project) => project.server_id === server.id)
    items.forEach((project) => assigned.add(project.id))
    return { id: server.id, name: server.name, server, projects: items }
  })
  const detached = props.projects.filter((project) => !assigned.has(project.id))
  if (detached.length) result.push({ id: 'unknown', name: '未知服务器', projects: detached })
  return result
})

const { search, stateFilter, filteredGroups, filteredCount } = useProjectFilters(
  groups,
  computed(() => props.health),
  { repositoryName: props.repositoryName },
)

function healthOf(project: Project): ProjectHealth | undefined {
  return props.health.get(project.id)
}

function healthSummary(project: Project): string {
  return describeProjectHealth(healthOf(project), props.nowEpoch)
}

function previewOf(project: Project): PreviewRun | undefined {
  return props.latestPreview(project.id)
}

function previewSummary(project: Project): string {
  const preview = previewOf(project)
  if (!preview) return ''
  if (preview.status !== 'succeeded') return `预览失败：${preview.error_message || 'Agent 未返回有效结果'}`
  return `保留 ${Number(preview.stats?.snapshots_kept || 0)} 份 · 将删除 ${Number(preview.stats?.snapshots_removed || 0)} 份 · 未执行删除`
}
</script>

<template>
  <div class="content-grid projects-grid">
    <section class="panel">
      <div class="panel-heading filter-heading">
        <div><p class="eyebrow">DESIRED STATE</p><h2>项目列表</h2></div>
        <div class="data-toolbar project-toolbar">
          <label class="search-field"><span>搜索</span><input v-model.trim="search" type="search" placeholder="项目、服务器、仓库或数据源" /></label>
          <label><span>状态</span><select v-model="stateFilter"><option value="all">全部状态</option><option value="enabled">运行中</option><option value="at_risk">RPO 风险</option><option value="paused">已暂停</option></select></label>
          <strong>{{ filteredCount }}/{{ projects.length }}</strong>
        </div>
      </div>
      <div v-if="!servers.length" class="empty-state">请先添加服务器，再为对应 Agent 创建备份项目。</div>
      <div v-else-if="!filteredGroups.length" class="empty-state">没有匹配当前条件的备份项目。</div>
      <div v-else class="project-server-list">
        <section v-for="group in filteredGroups" :key="group.id" class="project-server-group">
          <header class="project-server-heading">
            <div><span class="server-state" :class="group.server?.status"></span><div><strong>{{ group.name }}</strong><small>{{ group.server?.hostname || 'Agent 尚未注册' }} · {{ group.projects.length }} 个项目</small></div></div>
            <span v-if="group.server" class="status-pill" :class="group.server.status">{{ statusLabel(group.server.status) }}</span>
          </header>
          <div v-if="!group.projects.length" class="server-empty">这台服务器还没有备份项目。</div>
          <div v-else class="card-list">
            <article v-for="project in group.projects" :key="project.id" class="project-card" :class="{ 'project-disabled': !project.enabled }">
              <div class="project-top">
                <div><strong>{{ project.name }}</strong><small>{{ project.server_id && servers.find((server) => server.id === project.server_id)?.name || project.server_id }} · {{ repositoryName(project.repository_id) }}</small></div>
                <div class="project-actions"><span class="status-pill" :class="project.enabled ? 'online' : 'neutral'">{{ project.enabled ? `Revision ${project.revision}` : '已暂停' }}</span><button type="button" class="text-button" :disabled="loading" @click="emit('edit', project)">编辑</button><button type="button" class="text-button" :disabled="loading" @click="emit('toggle', project)">{{ project.enabled ? '暂停' : '恢复' }}</button><button v-if="project.policy?.retention.enabled" type="button" class="ghost compact" :disabled="loading || !project.enabled || queuedPreviewProjectIDs.has(project.id)" @click="emit('preview', project)">{{ queuedPreviewProjectIDs.has(project.id) ? '预览排队中' : '清理预览' }}</button><button type="button" class="ghost compact" :disabled="loading || !project.enabled || queuedProjectIDs.has(project.id)" @click="emit('run', project)">{{ queuedProjectIDs.has(project.id) ? '已排队 ✓' : '立即备份' }}</button><button type="button" class="text-button danger-text" :disabled="loading" @click="emit('archive', project)">归档</button></div>
              </div>
              <div class="project-source-list">
                <span v-for="source in project.sources" :key="source.id" class="source-chip" :class="source.type">{{ sourceSummary(source) }}</span>
              </div>
              <div class="rpo-strip" :class="healthOf(project)?.status || 'unknown'">
                <span class="rpo-state-dot"></span>
                <div><small>RECOVERY POINT OBJECTIVE</small><strong>{{ projectHealthLabel(healthOf(project)) }}</strong></div>
                <span>{{ healthSummary(project) }}</span>
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
              <div v-if="previewOf(project)" class="retention-preview-result" :class="previewOf(project)?.status"><span>DRY RUN</span><strong>{{ previewSummary(project) }}</strong><small>{{ formatDate(previewOf(project)?.finished_at || previewOf(project)?.started_at || '') }}</small></div>
            </article>
          </div>
        </section>
      </div>
    </section>
  </div>
</template>
