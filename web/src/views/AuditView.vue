<script setup lang="ts">
import { computed, onBeforeUnmount, ref, type Ref } from 'vue'
import { useAuditFilters } from '../composables/auditFilters'
import { auditActionCategory, auditActionLabel, auditCategoryLabel, auditResourceLabel, formatDate } from '../display'
import type { AuditEvent } from '../types'

const props = defineProps<{
  events: AuditEvent[]
}>()

const nowEpoch = ref(Date.now())
const clock = window.setInterval(() => { nowEpoch.value = Date.now() }, 30000)
onBeforeUnmount(() => window.clearInterval(clock))

const eventsRef = computed(() => props.events) as Ref<AuditEvent[]>
const { categoryFilter, outcomeFilter, filtered, last24Hours, failed, security } = useAuditFilters(eventsRef, nowEpoch)
</script>

<template>
  <div class="metric-grid audit-metrics">
    <article class="metric"><header><span>LOADED EVENTS</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ events.length }}</strong><em>latest 200</em></div><footer>已加载审计事件 <span>追加写入</span></footer></article>
    <article class="metric"><header><span>LAST 24 HOURS</span><i class="metric-signal"></i></header><div class="metric-value"><strong>{{ last24Hours }}</strong><em>events</em></div><footer>最近 24 小时 <span>所有类别</span></footer></article>
    <article class="metric" :class="{ alert: failed > 0 }"><header><span>FAILED ACTIONS</span><i class="metric-signal" :class="failed ? 'bad' : 'good'"></i></header><div class="metric-value"><strong>{{ failed }}</strong><em>HTTP ≥ 400</em></div><footer>失败或被拒绝 <span>{{ failed ? '需要复核' : '状态正常' }}</span></footer></article>
    <article class="metric"><header><span>SECURITY EVENTS</span><i class="metric-signal warn"></i></header><div class="metric-value"><strong>{{ security }}</strong><em>auth + security</em></div><footer>认证与账号安全 <span>不记录凭据</span></footer></article>
  </div>

  <section class="panel audit-panel">
    <div class="panel-heading audit-heading">
      <div><p class="eyebrow">APPEND-ONLY AUDIT TRAIL</p><h2>最近操作证据</h2><p>记录操作类型、结果、资源、来源地址和 HTTP 状态；请求正文与 Secret 永不进入审计表。</p></div>
      <div class="audit-filters">
        <label>类别<select v-model="categoryFilter"><option value="all">全部类别</option><option value="authentication">认证</option><option value="security">安全</option><option value="configuration">配置</option><option value="backup">备份与恢复</option></select></label>
        <label>结果<select v-model="outcomeFilter"><option value="all">全部结果</option><option value="succeeded">成功</option><option value="failed">失败</option></select></label>
      </div>
    </div>
    <div v-if="!filtered.length" class="empty-state">当前筛选条件下没有审计事件。</div>
    <div v-else class="table-wrap"><table class="audit-table"><thead><tr><th>时间</th><th>类别</th><th>操作</th><th>结果</th><th>目标资源</th><th>操作者 / 来源</th><th>HTTP</th></tr></thead><tbody>
      <tr v-for="event in filtered" :key="event.id">
        <td><strong>{{ formatDate(event.created_at) }}</strong><small>{{ event.id }}</small></td>
        <td><span class="source-chip">{{ auditCategoryLabel(auditActionCategory(event.action)) }}</span></td>
        <td><strong>{{ auditActionLabel(event.action) }}</strong><small>{{ event.action }}</small></td>
        <td><span class="status-pill" :class="event.outcome === 'succeeded' ? 'succeeded' : 'failed'">{{ event.outcome === 'succeeded' ? '成功' : '失败' }}</span></td>
        <td class="audit-resource">{{ auditResourceLabel(event) }}</td>
        <td><strong>{{ event.actor }}</strong><small>{{ event.client_ip || 'unknown' }}</small></td>
        <td><code>{{ event.status_code }}</code></td>
      </tr>
    </tbody></table></div>
  </section>
</template>
