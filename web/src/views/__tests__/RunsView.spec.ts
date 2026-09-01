import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import RunsView from '../RunsView.vue'
import type { Run } from '../../types'

function run(overrides: Partial<Run> = {}): Run {
  return {
    id: 'run_1',
    idempotency_key: 'k1',
    project_id: 'prj_1',
    server_id: 'srv_1',
    scheduled_at: '2026-01-01T00:00:00Z',
    started_at: '2026-01-01T00:00:00Z',
    status: 'succeeded',
    ...overrides,
  }
}

describe('RunsView', () => {
  it('renders runs and reacts to the status filter', async () => {
    const wrapper = mount(RunsView, {
      props: {
        runs: [
          run(),
          run({ id: 'run_2', idempotency_key: 'k2', status: 'failed', error_message: '磁盘已满' }),
          run({ id: 'run_3', idempotency_key: 'k3', status: 'running' }),
        ],
        projectName: () => '项目甲',
        serverName: () => '服务器甲',
      },
    })
    expect(wrapper.text()).toContain('3 条')
    expect(wrapper.text()).toContain('磁盘已满')

    await wrapper.find('[aria-label="按运行状态筛选"]').findAll('button')[1].trigger('click')
    expect(wrapper.text()).toContain('1 条')
    expect(wrapper.text()).not.toContain('磁盘已满')
  })

  it('shows an empty state when nothing matches the search', async () => {
    const wrapper = mount(RunsView, {
      props: { runs: [run()], projectName: () => '项目甲', serverName: () => '服务器甲' },
    })
    const search = wrapper.find('input[type="search"]')
    ;(search.element as HTMLInputElement).value = '不存在的关键词'
    await search.trigger('input')
    expect(wrapper.text()).toContain('没有匹配当前筛选条件的运行记录')
  })
})
