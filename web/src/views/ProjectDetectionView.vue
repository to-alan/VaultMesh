<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { formatDate, statusLabel } from '../display'
import { selectedDetectionCandidates, type CandidateGroup, type DetectionCandidate, type DetectionSelection } from '../detectionCandidates'
import type { DetectionReport, Server } from '../types'

const props = defineProps<{
  servers: Server[]
  serverId: string
  report: DetectionReport | null
  candidates: DetectionCandidate[]
  selection: DetectionSelection
  running: boolean
  loading: boolean
  reportLoading: boolean
  attempts: number
  exhausted: boolean
  warning: string
  versionBlocked: boolean
  agentWorkDisabled: boolean
  hasDraft: boolean
}>()
const emit = defineEmits<{
  'update:serverId': [value: string]
  toggle: [candidate: DetectionCandidate, checked: boolean]
  scan: []
  clear: []
  apply: [replace: boolean]
}>()
const search = ref('')
const confirmReplace = ref(false)
const selected = computed(() => selectedDetectionCandidates(props.candidates, props.selection))
const target = computed(() => props.servers.find((server) => server.id === props.serverId))
const groups: { id: CandidateGroup; name: string; hint: string }[] = [
  { id: 'recommended', name: '推荐候选', hint: '优先确认这些业务数据；不会自动创建项目。' },
  { id: 'review', name: '需确认', hint: '已有配置、嵌套目录或基础服务，按实际需要添加。' },
  { id: 'unavailable', name: '暂不可用', hint: '先解决下面的问题，再重新探测。' },
  { id: 'excluded', name: '自动排除', hint: '保留清单供核对，不作为业务备份草稿来源。' },
]
const filtered = computed(() => props.candidates.filter((candidate) => `${candidate.title} ${candidate.subtitle} ${candidate.typeLabel} ${candidate.reason}`.toLowerCase().includes(search.value.toLowerCase())))
const items = (group: CandidateGroup) => filtered.value.filter((candidate) => candidate.group === group)
const count = (group: CandidateGroup) => props.candidates.filter((candidate) => candidate.group === group).length
watch(() => [props.serverId, props.report], () => { search.value = ''; confirmReplace.value = false })
watch(() => props.selection, () => { confirmReplace.value = false }, { deep: true })
function apply() {
  if (props.hasDraft) confirmReplace.value = true
  else emit('apply', false)
}
</script>

<template>
  <section class="panel project-detection-panel" aria-labelledby="project-detection-title" :aria-busy="running || reportLoading">
    <div class="panel-heading">
      <div><p class="eyebrow">DISCOVER</p><h2 id="project-detection-title">服务器探测</h2><p>从服务器上的应用和数据中，选择要保护的内容。</p></div>
      <span class="discovery-icon" aria-hidden="true">⌕</span>
    </div>
    <div class="discovery-controls">
      <label>探测服务器<select :value="serverId" :disabled="loading || running" @change="emit('update:serverId', ($event.target as HTMLSelectElement).value)">
        <option value="">选择一台服务器</option>
        <option v-for="server in servers" :key="server.id" :value="server.id">{{ server.name }} · {{ statusLabel(server.status) }}</option>
      </select></label>
      <button type="button" class="primary" :disabled="loading || running || agentWorkDisabled || versionBlocked || target?.status !== 'online'" @click="emit('scan')">{{ running ? '正在探测…' : report ? '重新探测' : '开始探测' }}</button>
    </div>
    <p class="discovery-help">只读探测，不读取文件内容或数据库密码。结果在此展示，不自动打开编辑器。</p>
    <p v-if="agentWorkDisabled" class="message">当前连接未启用安全传输，请先完成 HTTPS 配置。</p>
    <p v-else-if="versionBlocked" class="message">Agent {{ target?.agent_version || '版本未知' }} 不支持探测，请按升级指南升级 Agent（需要 v0.1.2-rc.1 及以上版本，或 edge）。</p>
    <p v-else-if="target && target.status !== 'online'" class="message">服务器离线，可查看上次结果；上线后才能重新探测。</p>
    <div v-if="running" class="discovery-progress" role="status"><span class="system-pulse"></span><div><strong>等待 Agent 回传</strong><small>已派发给 {{ target?.name }} · 领取次数 {{ attempts }} · 最长等待两分钟</small></div></div>
    <p v-else-if="reportLoading" class="discovery-help" role="status">正在读取上次探测结果…</p>
    <div v-if="warning || exhausted" class="discovery-diagnosis" role="status">
      <strong>{{ warning ? '探测提示' : '暂未收到探测结果' }}</strong>
      <p>{{ warning || '请确认 Agent 在线，并检查服务日志中的 detection 或 reject unsupported command。' }}</p>
      <code v-if="exhausted">journalctl -u vaultmesh-agent -n 50</code>
    </div>
    <template v-if="report">
      <div class="discovery-report-heading"><span>上次探测 · {{ formatDate(report.generated_at) }}</span><button type="button" class="text-button" :disabled="loading || running" @click="emit('clear')">清空结果</button></div>
      <p v-if="!report.tools?.restic" class="message">未检测到可用的 Restic。可先配置草稿，执行备份前必须在 Agent 安装 Restic。</p>
      <div class="discovery-counts"><div v-for="group in groups" :key="group.id" :class="group.id"><strong>{{ count(group.id) }}</strong><span>{{ group.name }}</span></div></div>
      <label class="discovery-search"><span class="sr-only">搜索探测结果</span><input v-model.trim="search" type="search" placeholder="搜索名称、路径或容器…" /></label>
      <div class="discovery-results">
        <template v-for="group in groups" :key="group.id">
          <details v-if="items(group.id).length || group.id === 'recommended'" class="discovery-group" :class="group.id" :open="group.id === 'recommended' || Boolean(search)">
            <summary>{{ group.name }} <span>{{ items(group.id).length }}</span></summary>
            <p class="discovery-help">{{ group.hint }}</p>
            <p v-if="!items(group.id).length" class="discovery-empty">{{ search ? '没有匹配的推荐候选。' : '暂无推荐候选。可展开下方分类核对，或手动创建项目。' }}</p>
            <label v-for="candidate in items(group.id)" :key="candidate.key" class="discovery-candidate" :class="{ selected: selection[candidate.kind].includes(candidate.index), disabled: !candidate.selectable }">
              <input type="checkbox" :checked="selection[candidate.kind].includes(candidate.index)" :disabled="!candidate.selectable || loading || running" @change="emit('toggle', candidate, ($event.target as HTMLInputElement).checked)" />
              <span><span class="candidate-title"><strong>{{ candidate.title }}</strong><em>{{ candidate.typeLabel }}</em></span><small class="candidate-location">{{ candidate.subtitle }}</small><small class="candidate-reason">{{ candidate.reason }}</small></span>
            </label>
          </details>
        </template>
        <p v-if="search && !filtered.length" class="discovery-empty">没有匹配的结果。试试其他关键词。</p>
      </div>
      <footer class="discovery-footer">
        <div><strong>已选 {{ selected.length }} 项</strong><span>下一步：确认数据源与备份计划</span></div>
        <template v-if="confirmReplace">
          <p class="message" role="alert">你有未保存的项目草稿。替换会丢失当前草稿，但不会修改已保存的项目。</p>
          <div class="form-actions"><button type="button" class="ghost" @click="confirmReplace = false">保留原草稿</button><button type="button" class="primary" :disabled="loading || !selected.length" @click="emit('apply', true)">替换草稿并继续</button></div>
        </template>
        <button v-else type="button" class="primary" :disabled="loading || running || !selected.length" @click="apply">生成项目草稿 →</button>
      </footer>
    </template>
    <div v-else-if="!running && !reportLoading" class="discovery-start">
      <span aria-hidden="true">⌕</span><strong>{{ serverId ? '准备发现业务数据' : '先选择服务器' }}</strong><p>探测应用目录、数据库和容器挂载。<br />你决定哪些内容需要备份。</p>
      <small>没有发现目标？也可以手动创建项目。</small>
    </div>
  </section>
</template>
