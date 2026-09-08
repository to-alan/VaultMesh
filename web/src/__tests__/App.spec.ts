import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../App.vue'
import ProjectListView from '../views/ProjectListView.vue'
import NotificationsView from '../views/NotificationsView.vue'
import ProjectDetectionView from '../views/ProjectDetectionView.vue'
import { detectionFixture } from './detectionFixtures'
import type { DetectionStatus } from '../types'
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
  vi.spyOn(controlPlane.servers, 'detection').mockResolvedValue({ available: true, report: detectionFixture })
  vi.spyOn(controlPlane.servers, 'detect').mockResolvedValue({ command: { id: 'cmd_new', server_id: testServer.id, type: 'detect', created_at: '2026-09-08T03:00:00Z' } })
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
  it('shows discovery and projects together without auto-dispatch, scrolling or opening a draft', async () => {
    const app = await openApp()
    const panel = app.findComponent(ProjectDetectionView)
    expect(app.find('.project-discovery-workspace').isVisible()).toBe(true)
    expect(panel.isVisible()).toBe(true)
    expect(app.findComponent(ProjectListView).isVisible()).toBe(true)
    await panel.find('select').setValue(testServer.id)
    await flushPromises()
    expect(panel.text()).toContain('上次探测')
    expect(controlPlane.servers.detect).not.toHaveBeenCalled()
    expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
    expect(app.find('#project-builder').exists()).toBe(false)
    expect(panel.find('.discovery-group.excluded').attributes('open')).toBeUndefined()
    expect(panel.findAll('.discovery-group.excluded input').every(input => input.attributes('disabled') !== undefined)).toBe(true)
    const appCandidate = panel.findAll('.discovery-group.recommended label').find(label => label.text().includes('/srv/shop'))!
    await appCandidate.find('input').setValue(true)
    await panel.findAll('button').find(button => button.text() === '生成项目草稿 →')!.trigger('click')
    expect(app.find('#project-builder').exists()).toBe(true)
    expect(app.find<HTMLTextAreaElement>('#project-builder textarea').element.value).toBe('/srv/shop')
    expect(warnings).not.toHaveBeenCalled()
  })

  it('asks inline before replacing an unfinished draft', async () => {
    const app = await openApp()
    await app.findComponent(ProjectListView).findAll('button').find(button => button.text() === '编辑')!.trigger('click')
    await app.find('#project-builder input[maxlength="100"]').setValue('保留我的草稿')
    await app.findAll('button').find(button => button.text() === '返回项目列表')!.trigger('click')
    const panel = app.findComponent(ProjectDetectionView)
    await panel.find('select').setValue(testServer.id)
    await flushPromises()
    await panel.find('.discovery-group.recommended input').setValue(true)
    await panel.findAll('button').find(button => button.text() === '生成项目草稿 →')!.trigger('click')
    expect(app.find('#project-builder').exists()).toBe(false)
    expect(panel.text()).toContain('替换会丢失当前草稿')
    await panel.findAll('button').find(button => button.text() === '保留原草稿')!.trigger('click')
    await app.findAll('button').find(button => button.text() === '继续编辑草稿')!.trigger('click')
    expect(app.find<HTMLInputElement>('#project-builder input[maxlength="100"]').element.value).toBe('保留我的草稿')
  })

  it('ignores a late cached report after switching servers', async () => {
    const other = { ...testServer, id: 'srv_other', name: '其他节点' }
    vi.mocked(controlPlane.servers.list).mockResolvedValue([testServer, other])
    let resolveOld!: (value: DetectionStatus) => void
    vi.mocked(controlPlane.servers.detection).mockImplementation(id => id === testServer.id ? new Promise(resolve => { resolveOld = resolve }) : Promise.resolve({ available: true, report: { generated_at: '', apps: [{ path: '/srv/other', name: 'PHP', kind: 'php', markers: [] }] } }))
    const app = await openApp()
    const panel = app.findComponent(ProjectDetectionView)
    await panel.find('select').setValue(testServer.id)
    await panel.find('select').setValue(other.id)
    await flushPromises()
    resolveOld({ available: true, report: detectionFixture })
    await flushPromises()
    expect(panel.text()).toContain('/srv/other')
    expect(panel.text()).not.toContain('/srv/shop')
    expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
  })

  it('keeps search-filtered selections and only dispatches on an explicit scan', async () => {
    const app = await openApp()
    const panel = app.findComponent(ProjectDetectionView)
    await panel.find('select').setValue(testServer.id)
    await flushPromises()
    await panel.find('.discovery-group.recommended input').setValue(true)
    await panel.find('input[type="search"]').setValue('gateway')
    expect(panel.text()).toContain('已选 1 项')
    expect(panel.find('.discovery-group.review').attributes('open')).toBeDefined()
    await panel.findAll('button').find(button => button.text() === '重新探测')!.trigger('click')
    await flushPromises()
    expect(controlPlane.servers.detect).toHaveBeenCalledExactlyOnceWith(testServer.id)
    expect(panel.find('select').attributes('disabled')).toBeDefined()
    expect(app.findComponent(ProjectListView).isVisible()).toBe(true)
    expect(app.find('#project-builder').exists()).toBe(false)
  })

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
