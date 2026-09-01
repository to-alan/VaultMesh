<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import {
  alertKindLabel,
  formatDate,
  notificationTransitionLabel,
  repeatIntervalLabel,
  statusLabel,
} from '../display'
import { notificationDefaults, notificationProvider, notificationProviderGroups, notificationProviders } from '../notifications'
import type { NotificationChannelWriteInput } from '../services'
import type { AlertIncident, NotificationChannel, NotificationDelivery, Project, Server } from '../types'

const props = defineProps<{
  channels: NotificationChannel[]
  incidents: AlertIncident[]
  deliveries: NotificationDelivery[]
  projects: Project[]
  servers: Server[]
  loading: boolean
  serverName: (serverID: string) => string
}>()

const emit = defineEmits<{
  save: [input: NotificationChannelWriteInput, editingID: string]
  archive: [channel: NotificationChannel]
  toggle: [channel: NotificationChannel]
  test: [channel: NotificationChannel]
  evaluate: []
}>()

const firingCount = computed(() => props.incidents.filter((item) => item.status === 'firing').length)
const failedDeliveries = computed(() => props.deliveries.filter((item) => item.status === 'failed').length)

// Tab-local form state lives here; App.vue wraps the emits in perform() and
// refreshes the shared channel/incident/delivery lists after each action.
const editingNotificationID = ref('')
const testingChannelIDs = ref<Set<string>>(new Set())
const notificationForm = reactive({
  type: 'webhook',
  name: '',
  enabled: true,
  send_resolved: true,
  repeat_minutes: 240,
  backup_failure: true,
  rpo_overdue: true,
  agent_offline: true,
  config_error: true,
  allow_private_address: false,
  project_ids: [] as string[],
  server_ids: [] as string[],
  config: notificationDefaults('webhook') as Record<string, string>,
})

const activeNotificationProvider = computed(() => notificationProvider(notificationForm.type))

function notificationTypeLabel(type: string): string {
  return notificationProvider(type).label
}

function notificationRoutingLabel(channel: NotificationChannel): string {
  const projects = channel.project_ids?.length ? `${channel.project_ids.length} 项目` : '全部项目'
  const servers = channel.server_ids?.length ? `${channel.server_ids.length} 服务器` : '全部服务器'
  return `${projects} · ${servers}`
}

function changeNotificationType() {
  notificationForm.config = notificationDefaults(notificationForm.type)
}

function resetNotificationForm() {
  editingNotificationID.value = ''
  Object.assign(notificationForm, {
    type: 'webhook', name: '', enabled: true, send_resolved: true, repeat_minutes: 240,
    backup_failure: true, rpo_overdue: true, agent_offline: true, config_error: true, allow_private_address: false,
    project_ids: [], server_ids: [], config: notificationDefaults('webhook'),
  })
}

function editNotificationChannel(channel: NotificationChannel) {
  editingNotificationID.value = channel.id
  Object.assign(notificationForm, {
    type: channel.type,
    name: channel.name,
    enabled: channel.enabled,
    send_resolved: channel.send_resolved,
    repeat_minutes: Math.round(channel.repeat_interval_seconds / 60),
    backup_failure: channel.event_types.includes('backup_failure'),
    rpo_overdue: channel.event_types.includes('rpo_overdue'),
    agent_offline: channel.event_types.includes('agent_offline'),
    config_error: channel.event_types.includes('config_error'),
    allow_private_address: channel.config?.allow_private_address === 'true',
    project_ids: [...(channel.project_ids ?? [])],
    server_ids: [...(channel.server_ids ?? [])],
    config: { ...notificationDefaults(channel.type), ...(channel.config ?? {}) },
  })
  window.requestAnimationFrame(() => document.getElementById('notification-builder')?.scrollIntoView({ behavior: 'smooth', block: 'start' }))
}

function saveNotificationChannel() {
  const eventTypes = [
    notificationForm.backup_failure ? 'backup_failure' : '',
    notificationForm.rpo_overdue ? 'rpo_overdue' : '',
    notificationForm.agent_offline ? 'agent_offline' : '',
    notificationForm.config_error ? 'config_error' : '',
  ].filter(Boolean) as NotificationChannelWriteInput['event_types']
  if (!eventTypes.length) throw new Error('至少选择一种告警事件。')
  const input: NotificationChannelWriteInput = {
    name: notificationForm.name,
    type: notificationForm.type,
    enabled: notificationForm.enabled,
    send_resolved: notificationForm.send_resolved,
    repeat_interval_seconds: Number(notificationForm.repeat_minutes) * 60,
    event_types: eventTypes,
    project_ids: notificationForm.project_ids,
    server_ids: notificationForm.server_ids,
    config: { ...notificationForm.config, allow_private_address: String(notificationForm.allow_private_address) },
  }
  emit('save', input, editingNotificationID.value)
}

function archiveNotificationChannel(channel: NotificationChannel) {
  if (!window.confirm(`归档通知渠道“${channel.name}”？历史投递记录会保留。`)) return
  if (editingNotificationID.value === channel.id) resetNotificationForm()
  emit('archive', channel)
}
</script>

<template>
  <div class="metric-grid notification-metrics">
    <article class="metric"><header><span>CONTACT POINTS</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ channels.length }}</strong><em>{{ channels.filter((item) => item.enabled).length }} enabled</em></div><footer>用户自定义投递渠道 <span>Secret 加密</span></footer></article>
    <article class="metric" :class="{ alert: firingCount > 0 }"><header><span>FIRING INCIDENTS</span><i class="metric-signal" :class="firingCount ? 'bad' : 'good'"></i></header><div class="metric-value"><strong>{{ firingCount }}</strong><em>deduplicated</em></div><footer>稳定指纹聚合 <span>{{ firingCount ? '需要处理' : '无活动告警' }}</span></footer></article>
    <article class="metric"><header><span>RESOLVED</span><i class="metric-signal good"></i></header><div class="metric-value"><strong>{{ incidents.filter((item) => item.status === 'resolved').length }}</strong><em>latest 200</em></div><footer>已恢复事件 <span>保留历史</span></footer></article>
    <article class="metric" :class="{ alert: failedDeliveries > 0 }"><header><span>DELIVERY FAILURES</span><i class="metric-signal" :class="failedDeliveries ? 'bad' : 'good'"></i></header><div class="metric-value"><strong>{{ failedDeliveries }}</strong><em>after retries</em></div><footer>最多重试 5 次 <span>{{ failedDeliveries ? '检查渠道' : '投递正常' }}</span></footer></article>
  </div>

  <section class="panel notification-reference">
    <div><p class="eyebrow">ALERTMANAGER-STYLE DELIVERY</p><h2>状态变化通知，不按轮询刷屏</h2></div>
    <div class="notification-flow"><span>运行 / RPO / 心跳</span><i>→</i><span>稳定指纹 Incident</span><i>→</i><span>持久化 Outbox</span><i>→</i><span>用户 Contact Point</span></div>
    <button type="button" class="primary compact-action" :disabled="loading" @click="emit('evaluate')">立即评估并投递</button>
  </section>

  <div class="content-grid notification-grid">
    <section class="panel">
      <div class="panel-heading"><div><p class="eyebrow">CONTACT POINTS</p><h2>通知渠道</h2></div><span class="sample-size">{{ channels.length }} CHANNELS</span></div>
      <div v-if="!channels.length" class="empty-state">尚未添加通知渠道。告警事件仍会被记录，但不会向外投递。</div>
      <div v-else class="notification-channel-list">
        <article v-for="channel in channels" :key="channel.id" class="notification-channel-card" :class="{ disabled: !channel.enabled }">
          <header><span class="channel-symbol">{{ channel.type === 'email' ? '@' : channel.type === 'telegram' ? '✈' : channel.type === 'webhook' ? '↗' : '◆' }}</span><div><strong>{{ channel.name }}</strong><small>{{ notificationTypeLabel(channel.type) }} · {{ channel.destination }}</small></div><span class="status-pill" :class="channel.enabled ? 'online' : 'neutral'">{{ channel.enabled ? '已启用' : '已停用' }}</span></header>
          <div class="channel-policy"><span><small>EVENTS</small><strong>{{ channel.event_types.map(alertKindLabel).join(' · ') }}</strong></span><span><small>ROUTING</small><strong>{{ notificationRoutingLabel(channel) }}</strong></span><span><small>REPEAT</small><strong>{{ repeatIntervalLabel(channel.repeat_interval_seconds) }}</strong></span><span><small>RESOLVED</small><strong>{{ channel.send_resolved ? '发送' : '不发送' }}</strong></span></div>
          <footer><button type="button" class="text-button" :disabled="loading" @click="editNotificationChannel(channel)">编辑</button><button type="button" class="text-button" :disabled="loading" @click="emit('toggle', channel)">{{ channel.enabled ? '停用' : '启用' }}</button><button type="button" class="ghost compact" :disabled="loading || testingChannelIDs.has(channel.id)" @click="emit('test', channel)">{{ testingChannelIDs.has(channel.id) ? '测试中…' : '发送测试' }}</button><button type="button" class="text-button danger-text" :disabled="loading" @click="archiveNotificationChannel(channel)">归档</button></footer>
        </article>
      </div>
    </section>

    <aside id="notification-builder" class="panel form-panel notification-builder" :class="{ editing: editingNotificationID }">
      <div class="builder-heading"><div><p class="eyebrow">{{ editingNotificationID ? 'EDIT CONTACT POINT' : 'NEW CONTACT POINT' }}</p><h2>{{ editingNotificationID ? '编辑通知渠道' : '添加通知渠道' }}</h2></div><button v-if="editingNotificationID" type="button" class="ghost compact" @click="resetNotificationForm">取消编辑</button></div>
      <p class="form-intro">渠道是全局 Contact Point；项目事件和服务器事件可以分别设置路由范围。敏感字段只写不读。</p>
      <form @submit.prevent="saveNotificationChannel">
        <div class="form-row"><label>渠道类型<select v-model="notificationForm.type" :disabled="Boolean(editingNotificationID)" @change="changeNotificationType"><optgroup v-for="group in notificationProviderGroups" :key="group" :label="group"><option v-for="provider in notificationProviders.filter((item) => item.group === group)" :key="provider.id" :value="provider.id">{{ provider.label }}</option></optgroup></select></label><label>渠道名称<input v-model.trim="notificationForm.name" required maxlength="100" placeholder="例如：运维 Telegram 群" /></label></div>
        <div class="repository-kind notification-kind"><div><span class="engine-badge">CONTACT</span><strong>{{ activeNotificationProvider.label }}</strong></div><p>{{ activeNotificationProvider.summary }}</p></div>
        <div class="dynamic-fields notification-fields">
          <label v-for="field in activeNotificationProvider.fields" :key="field.key">{{ field.label }}
            <select v-if="field.type === 'select'" v-model="notificationForm.config[field.key]" :required="field.required"><option v-for="option in field.options" :key="option.value" :value="option.value">{{ option.label }}</option></select>
            <textarea v-else-if="field.type === 'textarea'" v-model="notificationForm.config[field.key]" rows="3" :required="field.required && !editingNotificationID" :placeholder="field.placeholder"></textarea>
            <input v-else v-model="notificationForm.config[field.key]" :type="field.type" :required="field.required && !editingNotificationID" :placeholder="editingNotificationID && field.type === 'password' ? '留空保持当前加密配置' : field.placeholder" :autocomplete="field.type === 'password' ? 'new-password' : 'off'" />
            <small v-if="field.help" class="field-help">{{ field.help }}</small>
          </label>
        </div>
        <section class="form-section compact-section"><div class="section-title"><span>1</span><div><strong>事件与恢复</strong><small>选择这个渠道关注的状态变化</small></div></div>
          <div class="policy-option-grid"><label class="check-row"><input v-model="notificationForm.backup_failure" type="checkbox" /><span><strong>备份失败</strong><small>部分成功、失败、超时、取消或状态未知。</small></span></label><label class="check-row"><input v-model="notificationForm.rpo_overdue" type="checkbox" /><span><strong>RPO 超时</strong><small>没有失败上报也能发现备份静默中断。</small></span></label><label class="check-row"><input v-model="notificationForm.agent_offline" type="checkbox" /><span><strong>Agent 离线</strong><small>心跳中断超过 90 秒时告警，恢复上线后自动关闭。</small></span></label><label class="check-row"><input v-model="notificationForm.config_error" type="checkbox" /><span><strong>配置降级</strong><small>仓库或数据库凭据无法解密，Agent 配置缺失项目时立即告警。</small></span></label></div>
          <label class="check-row"><input v-model="notificationForm.send_resolved" type="checkbox" /><span><strong>发送恢复通知</strong><small>事件从 firing 变为 resolved 时发送一次，确认故障已经结束。</small></span></label>
        </section>
        <section class="form-section compact-section"><div class="section-title"><span>2</span><div><strong>去重与路由</strong><small>持续故障只按周期重复提醒</small></div></div>
          <label>重复提醒间隔（分钟）<input v-model.number="notificationForm.repeat_minutes" type="number" min="5" max="10080" required /><small class="field-help">默认 240 分钟。相同 Incident 在此期间不会重复发送。</small></label>
          <label class="check-row security-choice"><input v-model="notificationForm.allow_private_address" type="checkbox" /><span><strong>允许访问私有网络地址</strong><small>仅用于自建 Gotify、ntfy、SMTP 或内网 Webhook。默认阻止回环与 RFC1918 地址；链路本地和云元数据地址始终禁止。</small></span></label>
          <div class="project-routing"><strong>项目事件范围</strong><small>作用于备份失败和 RPO；不勾选表示所有项目。</small><div><label v-for="project in projects" :key="project.id"><input v-model="notificationForm.project_ids" type="checkbox" :value="project.id" /><span>{{ project.name }}<small>{{ serverName(project.server_id) }}</small></span></label></div></div>
          <div class="project-routing"><strong>服务器事件范围</strong><small>作用于 Agent 离线；不勾选表示所有服务器。</small><div><label v-for="server in servers" :key="server.id"><input v-model="notificationForm.server_ids" type="checkbox" :value="server.id" /><span>{{ server.name }}<small>{{ server.hostname || statusLabel(server.status) }}</small></span></label></div></div>
        </section>
        <div class="form-actions"><button v-if="editingNotificationID" type="button" class="ghost" @click="resetNotificationForm">取消</button><button class="primary" :disabled="loading">{{ editingNotificationID ? '保存渠道' : '加密并创建渠道' }}</button></div>
      </form>
    </aside>
  </div>

  <section class="panel alert-history-panel">
    <div class="panel-heading"><div><p class="eyebrow">INCIDENT TIMELINE</p><h2>告警事件</h2></div><span class="sample-size">{{ incidents.length }} INCIDENTS</span></div>
    <div v-if="!incidents.length" class="empty-state">暂无告警事件。后台每 30 秒评估一次，也可以手动立即评估。</div>
    <div v-else class="table-wrap"><table><thead><tr><th>状态</th><th>事件 / 对象</th><th>首次发生</th><th>最近变化</th><th>次数</th><th>说明</th></tr></thead><tbody><tr v-for="incident in incidents" :key="incident.id"><td><span class="status-pill" :class="incident.status === 'firing' ? 'overdue' : 'succeeded'">{{ incident.status === 'firing' ? '告警中' : '已恢复' }}</span></td><td><strong>{{ alertKindLabel(incident.kind) }}</strong><small>{{ incident.resource_name || incident.project_name || incident.resource_id }}</small></td><td>{{ formatDate(incident.started_at) }}</td><td>{{ formatDate(incident.resolved_at || incident.updated_at) }}</td><td><strong>{{ incident.occurrence_count }}</strong></td><td class="notification-description">{{ incident.description }}</td></tr></tbody></table></div>
  </section>

  <section class="panel delivery-history-panel">
    <div class="panel-heading"><div><p class="eyebrow">DELIVERY OUTBOX</p><h2>最近投递</h2></div><span class="sample-size">{{ deliveries.length }} DELIVERIES</span></div>
    <div v-if="!deliveries.length" class="empty-state">尚无通知投递记录。</div>
    <div v-else class="table-wrap"><table><thead><tr><th>创建时间</th><th>渠道</th><th>类型</th><th>状态</th><th>尝试</th><th>错误</th></tr></thead><tbody><tr v-for="delivery in deliveries" :key="delivery.id"><td>{{ formatDate(delivery.created_at) }}</td><td><strong>{{ delivery.channel_name || delivery.channel_id }}</strong></td><td>{{ notificationTransitionLabel(delivery.transition) }}</td><td><span class="status-pill" :class="delivery.status === 'sent' ? 'succeeded' : delivery.status === 'failed' ? 'failed' : 'pending'">{{ delivery.status === 'sent' ? '已发送' : delivery.status === 'failed' ? '最终失败' : delivery.status === 'delivering' ? '发送中' : '等待重试' }}</span></td><td>{{ delivery.attempt_count }}/5</td><td class="error-cell">{{ delivery.last_error || '—' }}</td></tr></tbody></table></div>
  </section>
</template>
