<script setup lang="ts">
import { formatDate, formatDuration, runOperationLabel, statusLabel } from '../display'
import { useRunFilters } from '../composables/filters'
import type { Run } from '../types'

const props = defineProps<{
  runs: Run[]
  projectName: (projectID: string) => string
  serverName: (serverID: string) => string
}>()

const { search, statusFilter, operationFilter, activeCount, attentionCount, filtered } = useRunFilters(
  () => props.runs,
  { projectName: props.projectName, serverName: props.serverName },
)
</script>

<template>
  <div class="run-kpi-grid" role="group" aria-label="按运行状态筛选">
    <button type="button" :class="{ active: statusFilter === 'all' }" @click="statusFilter = 'all'"><span>ALL RUNS</span><strong>{{ runs.length }}</strong><small>当前样本</small></button>
    <button type="button" :class="{ active: statusFilter === 'succeeded' }" @click="statusFilter = 'succeeded'"><span>SUCCEEDED</span><strong>{{ runs.filter((run) => run.status === 'succeeded').length }}</strong><small>成功完成</small></button>
    <button type="button" :class="{ active: statusFilter === 'active' }" @click="statusFilter = 'active'"><span>IN PROGRESS</span><strong>{{ activeCount }}</strong><small>等待或执行中</small></button>
    <button type="button" class="attention" :class="{ active: statusFilter === 'attention' }" @click="statusFilter = 'attention'"><span>NEEDS ATTENTION</span><strong>{{ attentionCount }}</strong><small>部分成功、失败或超时</small></button>
  </div>
  <section class="panel run-history-panel">
    <div class="panel-heading filter-heading">
      <div><p class="eyebrow">RUN HISTORY</p><h2>最近 100 次运行</h2></div>
      <div class="data-toolbar run-toolbar">
        <label class="search-field"><span>搜索</span><input v-model.trim="search" type="search" placeholder="项目、Agent、运行 ID 或错误" /></label>
        <label><span>任务类型</span><select v-model="operationFilter"><option value="all">全部任务</option><option value="backup">备份与索引</option><option value="maintenance">维护与校验</option><option value="recovery">浏览与恢复</option></select></label>
        <strong>{{ filtered.length }} 条</strong>
      </div>
    </div>
    <div v-if="!runs.length" class="empty-state">尚无运行记录。</div>
    <div v-else-if="!filtered.length" class="empty-state">没有匹配当前筛选条件的运行记录。</div>
    <div v-else class="table-wrap run-table-wrap"><table><thead><tr><th>项目 / Agent</th><th>类型</th><th>状态</th><th>开始 / 耗时</th><th>快照 / 预览结果</th><th>错误</th></tr></thead><tbody>
      <tr v-for="run in filtered" :key="run.id"><td><strong>{{ projectName(run.project_id) ?? run.project_id }}</strong><small>{{ serverName(run.server_id) }} · {{ run.id }}</small></td><td><span class="source-chip">{{ runOperationLabel(run) }}</span></td><td><span class="status-pill" :class="run.status">{{ statusLabel(run.status) }}</span></td><td><strong>{{ formatDate(run.started_at) }}</strong><small>{{ formatDuration(run) }}</small></td><td><template v-if="run.snapshot_id"><code>{{ run.snapshot_id.slice(0, 16) }}</code><small v-if="Number(run.stats?.optional_source_count || 0)">跳过 {{ Number(run.stats?.optional_source_count) }} 个可选源</small></template><span v-else-if="run.stats?.operation === 'retention_preview'">保留 {{ Number(run.stats?.snapshots_kept || 0) }} · 删除 {{ Number(run.stats?.snapshots_removed || 0) }}</span><span v-else-if="run.stats?.operation === 'snapshot_restore'">{{ run.stats?.restore_target || run.stats?.path }}</span><span v-else-if="run.stats?.snapshot_id"><code>{{ String(run.stats.snapshot_id).slice(0, 16) }}</code></span><span v-else>—</span></td><td class="error-cell" :title="run.error_message">{{ run.error_message || '—' }}</td></tr>
    </tbody></table></div>
  </section>
</template>
