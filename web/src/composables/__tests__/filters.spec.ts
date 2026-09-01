import { describe, expect, it } from 'vitest'
import { useRunFilters } from '../filters'
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

const labels = { projectName: () => '项目甲', serverName: () => '服务器甲' }

describe('useRunFilters', () => {
  it('returns every run without filters', () => {
    const runs = [run(), run({ id: 'run_2', status: 'failed' })]
    const { filtered } = useRunFilters(() => runs, labels)
    expect(filtered.value).toHaveLength(2)
    expect(activeOf(useRunFilters(() => runs, labels))).toBe(0)
  })

  it('counts active and attention runs', () => {
    const runs = [
      run({ status: 'running' }),
      run({ status: 'pending' }),
      run({ status: 'failed' }),
      run({ status: 'timed_out' }),
      run({ status: 'succeeded' }),
    ]
    const filters = useRunFilters(() => runs, labels)
    expect(filters.activeCount.value).toBe(2)
    expect(filters.attentionCount.value).toBe(2)
  })

  it('filters by status', () => {
    const runs = [run({ status: 'running' }), run({ status: 'succeeded' }), run({ status: 'failed' })]
    const filters = useRunFilters(() => runs, labels)
    filters.statusFilter.value = 'active'
    expect(filters.filtered.value.map((item) => item.status)).toEqual(['running'])
    filters.statusFilter.value = 'attention'
    expect(filters.filtered.value.map((item) => item.status)).toEqual(['failed'])
    filters.statusFilter.value = 'succeeded'
    expect(filters.filtered.value.map((item) => item.status)).toEqual(['succeeded'])
  })

  it('filters by operation group', () => {
    const runs = [
      run({ stats: { operation: 'backup' } }),
      run({ stats: { operation: 'prune' } }),
      run({ stats: { operation: 'snapshot_restore' } }),
    ]
    const filters = useRunFilters(() => runs, labels)
    filters.operationFilter.value = 'maintenance'
    expect(filters.filtered.value.map((item) => item.stats?.operation)).toEqual(['prune'])
  })

  it('matches search against project name, error text, and status', () => {
    const runs = [
      run({ error_message: 'restic 仓库连接失败' }),
      run({ id: 'run_other' }),
    ]
    const filters = useRunFilters(() => runs, labels)
    filters.search.value = '仓库连接'
    expect(filters.filtered.value).toHaveLength(1)
    filters.search.value = '项目甲'
    expect(filters.filtered.value).toHaveLength(2)
    filters.search.value = 'RUN_OTHER'
    expect(filters.filtered.value.map((item) => item.id)).toEqual(['run_other'])
  })

  it('combines status and search filters', () => {
    const runs = [run({ status: 'failed', error_message: '磁盘已满' }), run({ status: 'failed', error_message: '网络中断' })]
    const filters = useRunFilters(() => runs, labels)
    filters.statusFilter.value = 'attention'
    filters.search.value = '网络'
    expect(filters.filtered.value.map((item) => item.error_message)).toEqual(['网络中断'])
  })
})

function activeOf(filters: ReturnType<typeof useRunFilters>): number {
  return filters.activeCount.value
}
