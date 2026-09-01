import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import NotificationsView from '../NotificationsView.vue'
import type { NotificationChannelWriteInput } from '../../services'
import type { AlertIncident, NotificationChannel, NotificationDelivery, Project, Server } from '../../types'

const channel: NotificationChannel = {
  id: 'chn_1',
  name: '运维 Telegram 群',
  type: 'telegram',
  enabled: true,
  send_resolved: true,
  repeat_interval_seconds: 14400,
  event_types: ['backup_failure', 'rpo_overdue', 'agent_offline', 'config_error'],
  destination: 'Chat -100200300',
  configured: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const incident: AlertIncident = {
  id: 'alt_1',
  fingerprint: 'backup:prj_1',
  kind: 'backup_failure',
  resource_type: 'project',
  resource_id: 'prj_1',
  resource_name: '项目甲',
  project_id: 'prj_1',
  project_name: '项目甲',
  status: 'firing',
  severity: 'critical',
  summary: '备份运行未成功',
  description: '最近一次备份运行状态为 failed。',
  source_event_id: 'run_1',
  occurrence_count: 2,
  started_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:10:00Z',
}

const server: Server = { id: 'srv_1', name: '测试服务器', status: 'online', desired_revision: 1, applied_revision: 1, created_at: '2026-01-01T00:00:00Z' }
const project: Project = { id: 'prj_1', server_id: 'srv_1', repository_id: 'repo_1', name: '项目甲', enabled: true, sources: [], schedule: { cron: '0 2 * * *', timezone: 'UTC', jitter_seconds: 0, max_runtime_seconds: 21600, grace_seconds: 3600, missed_run_policy: 'skip', concurrency_policy: 'forbid' }, revision: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }

interface ViewProps {
  channels: NotificationChannel[]
  incidents: AlertIncident[]
  deliveries: NotificationDelivery[]
  projects: Project[]
  servers: Server[]
  loading: boolean
  serverName: (serverID: string) => string
}

function mountView(overrides: Partial<ViewProps> = {}) {
  return mount(NotificationsView, {
    props: {
      channels: [channel],
      incidents: [incident],
      deliveries: [] as NotificationDelivery[],
      projects: [project],
      servers: [server],
      loading: false,
      serverName: () => '测试服务器',
      ...overrides,
    },
  })
}

describe('NotificationsView', () => {
  it('renders channels, incidents, and delivery metrics', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('运维 Telegram 群')
    expect(wrapper.text()).toContain('告警中')
    expect(wrapper.text()).toContain('备份失败 · RPO 超时 · Agent 离线 · 配置降级')
  })

  it('keeps the form hidden state consistent while editing and emits save inputs', async () => {
    const wrapper = mountView()
    await wrapper.find('input[placeholder="例如：运维 Telegram 群"]').setValue('值班钉钉')
    await wrapper.find('form').trigger('submit')
    const save = wrapper.emitted('save')
    expect(save).toHaveLength(1)
    const [input, editingID] = save![0] as [NotificationChannelWriteInput, string]
    expect(input.name).toBe('值班钉钉')
    expect(editingID).toBe('')
    expect(input.event_types).toContain('config_error')
  })

  it('emits archive after the confirmation dialog', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await wrapper.findAll('button').find((button) => button.text() === '归档')!.trigger('click')
    expect(confirm).toHaveBeenCalledOnce()
    expect(wrapper.emitted('archive')).toHaveLength(1)
    confirm.mockRestore()
  })
})
