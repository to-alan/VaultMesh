import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ProjectListView from '../ProjectListView.vue'
import type { Project, Server } from '../../types'

const server: Server = {
  id: 'srv_1',
  name: '服务器甲',
  status: 'online',
  desired_revision: 1,
  applied_revision: 1,
  created_at: '2026-09-04T00:00:00Z',
}

const project: Project = {
  id: 'prj_1',
  server_id: server.id,
  repository_id: 'repo_1',
  name: '项目甲',
  enabled: true,
  sources: [{ type: 'files', paths: ['/srv/app'], required: true }],
  schedule: {
    cron: '0 2 * * *',
    timezone: 'UTC',
    jitter_seconds: 0,
    max_runtime_seconds: 3600,
    grace_seconds: 300,
    missed_run_policy: 'skip',
    concurrency_policy: 'forbid',
  },
  policy: {
    backup: { one_file_system: false, exclude_caches: true },
    retention: {
      enabled: true,
      mode: 'count',
      keep_last: 7,
      keep_hourly: 0,
      keep_daily: 0,
      keep_weekly: 0,
      keep_monthly: 0,
      keep_yearly: 0,
      prune: false,
    },
    verification: { mode: 'off' },
    maintenance: { separate: true },
  },
  revision: 1,
  created_at: '2026-09-04T00:00:00Z',
  updated_at: '2026-09-04T00:00:00Z',
}

function mountList(agentWorkDisabled: boolean) {
  return mount(ProjectListView, {
    props: {
      projects: [project],
      servers: [server],
      health: new Map(),
      nowEpoch: Date.parse('2026-09-04T00:00:00Z'),
      repositoryName: () => '仓库甲',
      latestPreview: () => undefined,
      queuedProjectIds: new Set<string>(),
      queuedPreviewProjectIds: new Set<string>(),
      loading: false,
      agentWorkDisabled,
    },
  })
}

describe('ProjectListView Agent work gate', () => {
  it('disables Agent commands while leaving configuration actions available', () => {
    const wrapper = mountList(true)
    const button = (label: string) => wrapper.findAll('button').find((item) => item.text() === label)

    expect(button('立即备份')?.attributes('disabled')).toBeDefined()
    expect(button('清理预览')?.attributes('disabled')).toBeDefined()
    expect(button('编辑')?.attributes('disabled')).toBeUndefined()
    expect(button('暂停')?.attributes('disabled')).toBeUndefined()
  })

  it('enables Agent commands after the transport gate is ready', () => {
    const wrapper = mountList(false)
    const run = wrapper.findAll('button').find((item) => item.text() === '立即备份')
    expect(run?.attributes('disabled')).toBeUndefined()
  })

  it('does not render an in-progress retention preview as a failure or a zero-deletion result', async () => {
    const wrapper = mountList(false)
    await wrapper.setProps({ latestPreview: () => ({ status: 'running' }) })
    expect(wrapper.find('.retention-preview-result').text()).toContain('尚无结果')
    expect(wrapper.find('.retention-preview-result').text()).not.toContain('预览失败')
    expect(wrapper.find('.retention-preview-result').text()).not.toContain('将删除 0')
  })
})
