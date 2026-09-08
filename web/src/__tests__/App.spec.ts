import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../App.vue'
import ProjectListView from '../views/ProjectListView.vue'
import NotificationsView from '../views/NotificationsView.vue'
import { controlPlane } from '../services'
import { makeProject, testDashboard, testProfile, testServer } from './fixtures'

let wrapper: VueWrapper | undefined
const warnings = vi.fn()

beforeEach(() => {
  window.history.replaceState(null, '', '#projects')
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { callback(0); return 0 })
  Element.prototype.scrollIntoView = vi.fn()
  vi.spyOn(controlPlane.meta, 'get').mockResolvedValue({ name: 'VaultMesh', version: 'v0.1.2', commit: 'test', https_ready: true })
  vi.spyOn(controlPlane.auth, 'session').mockResolvedValue({ username: 'admin', expires_at: '2026-09-09T00:00:00Z' })
  vi.spyOn(controlPlane.dashboard, 'get').mockResolvedValue(testDashboard)
  vi.spyOn(controlPlane.servers, 'list').mockResolvedValue([testServer])
  vi.spyOn(controlPlane.repositories, 'list').mockResolvedValue([{ id: 'repo_test', name: '测试仓库', provider: 'local', url: '/backup', created_at: '2026-09-01T00:00:00Z' }])
  vi.spyOn(controlPlane.projects, 'list').mockResolvedValue([makeProject()])
  vi.spyOn(controlPlane.projects, 'health').mockResolvedValue([{ project_id: 'prj_test', status: 'overdue', latest_run_status: 'succeeded' }])
  vi.spyOn(controlPlane.runs, 'list').mockResolvedValue([])
  vi.spyOn(controlPlane.profile, 'get').mockResolvedValue(testProfile)
  vi.spyOn(controlPlane.notifications, 'listChannels').mockResolvedValue([])
  vi.spyOn(controlPlane.notifications, 'listIncidents').mockResolvedValue([])
  vi.spyOn(controlPlane.notifications, 'listDeliveries').mockResolvedValue([])
  warnings.mockClear()
})

afterEach(() => {
  wrapper?.unmount()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

async function openApp() {
  wrapper = mount(App, { global: { config: { warnHandler: warnings } } })
  await flushPromises()
  return wrapper
}

describe('complete application pages', () => {
  it('renders the actual project list with queued-action props, then opens an editor without losing its draft', async () => {
    const app = await openApp()
    expect(app.findComponent(ProjectListView).exists()).toBe(true)
    expect(app.text()).toContain('业务数据库')
    expect(app.find('#project-builder').exists()).toBe(false)
    await app.findComponent(ProjectListView).findAll('button').find((button) => button.text() === '编辑')!.trigger('click')
    expect(app.find('#project-builder').exists()).toBe(true)
    const name = app.find<HTMLInputElement>('#project-builder input[maxlength="100"]')
    await name.setValue('尚未保存的修改')
    await app.findAll('button').find((button) => button.text() === '返回项目列表')!.trigger('click')
    expect(app.find('#project-builder').exists()).toBe(false)
    await app.findAll('button').find((button) => button.text() === '继续编辑草稿')!.trigger('click')
    expect(app.find<HTMLInputElement>('#project-builder input[maxlength="100"]').element.value).toBe('尚未保存的修改')
    expect(warnings).not.toHaveBeenCalled()
  })

  it('renders the notifications component from the navigation with no unresolved components', async () => {
    const app = await openApp()
    await app.findAll('nav button').find((button) => button.text().includes('通知与告警'))!.trigger('click')
    await flushPromises()
    expect(app.findComponent(NotificationsView).exists()).toBe(true)
    expect(app.findComponent(NotificationsView).find('form').exists()).toBe(true)
    expect(warnings).not.toHaveBeenCalled()
  })

  it('locks the editor during save and sends the original schedule without implicit policy changes', async () => {
    let complete!: (project: ReturnType<typeof makeProject>) => void
    const replace = vi.spyOn(controlPlane.projects, 'replace').mockImplementation(() => new Promise((resolve) => { complete = resolve }))
    const app = await openApp()
    await app.findComponent(ProjectListView).findAll('button').find((button) => button.text() === '编辑')!.trigger('click')
    await app.find('#project-builder form').trigger('submit')
    expect(app.find('#project-builder fieldset').attributes('disabled')).toBeDefined()
    expect(replace).toHaveBeenCalledWith('prj_test', expect.objectContaining({ schedule: makeProject().schedule, policy: expect.objectContaining({ maintenance: makeProject().policy!.maintenance }) }))
    complete(makeProject())
    await flushPromises()
    expect(app.find('#project-builder').exists()).toBe(false)
    expect(app.text()).toContain('项目配置已更新')
  })

  it('keeps the editor and its values when saving fails', async () => {
    vi.spyOn(controlPlane.projects, 'replace').mockRejectedValue(new Error('保存失败，请重试'))
    const app = await openApp()
    await app.findComponent(ProjectListView).findAll('button').find((button) => button.text() === '编辑')!.trigger('click')
    await app.find('#project-builder input[maxlength="100"]').setValue('保留此修改')
    await app.find('#project-builder form').trigger('submit')
    await flushPromises()
    expect(app.find<HTMLInputElement>('#project-builder input[maxlength="100"]').element.value).toBe('保留此修改')
    expect(app.find('#project-builder fieldset').attributes('disabled')).toBeUndefined()
    expect(app.text()).toContain('保存失败，请重试')
  })

  it('shows authoritative RPO health and 24h statistics even when the recent sample is empty', async () => {
    window.history.replaceState(null, '', '#overview')
    const app = await openApp()
    expect(app.find('.health-panel').text()).toContain('RPO 已超时')
    expect(app.find('.overview-metrics').text()).toContain('80%')
    expect(app.find('.overview-metrics').text()).toContain('12 次成功 / 15 次已结束备份')
    expect(warnings).not.toHaveBeenCalled()
  })
})
