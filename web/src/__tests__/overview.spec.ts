import { describe, expect, it } from 'vitest'
import { backupTrend, projectHealthRows, summarizeBackups } from '../overview'
import { makeProject, makeRun } from './fixtures'

describe('backup statistics', () => {
  it('classifies each status without counting running or skipped as failed attempts', () => {
    const runs = ['succeeded', 'partial', 'failed', 'timed_out', 'unknown', 'canceled', 'running', 'skipped'].map((status) => makeRun(status))
    runs.push(makeRun('succeeded', { id: 'maintenance', stats: { operation: 'verification' } }))
    const summary = summarizeBackups(runs)
    expect(summary.total).toBe(8)
    expect(summary.completed).toBe(6)
    expect(summary.successRate).toBe(17)
    expect(summary.distribution.map((group) => group.count)).toEqual([1, 1, 3, 1, 1, 1])
    expect(backupTrend(runs, Date.parse('2026-09-08T12:00:00Z')).points.flatMap((point) => point.distribution).filter((group) => group.key === 'failed').reduce((total, group) => total + group.count, 0)).toBe(3)
  })

  it('shows no percentage until a backup attempt has finished', () => {
    expect(summarizeBackups([]).successRate).toBeNull()
    expect(summarizeBackups([makeRun('running'), makeRun('skipped')]).successRate).toBeNull()
    expect(summarizeBackups([makeRun('succeeded', { stats: { operation: '' } })]).successRate).toBe(100)
  })

  it('advances the seven-day window across midnight and excludes old samples', () => {
    const now = new Date(2026, 8, 8, 12).getTime()
    const runs = [makeRun('failed', { started_at: new Date(2026, 8, 1, 12).toISOString() }), makeRun('running', { started_at: new Date(now).toISOString() })]
    const trend = backupTrend(runs, now)
    expect(trend.points.map((point) => point.total)).toEqual([0, 0, 0, 0, 0, 0, 1])
    expect(backupTrend(runs, new Date(2026, 8, 9, 0).getTime()).points.at(-1)?.key).toBe('2026-09-09')
  })

  it('prioritizes backend health even when the last backup succeeded', () => {
    const rows = projectHealthRows([makeProject({ id: 'healthy' }), makeProject({ id: 'overdue' })], [
      { project_id: 'healthy', status: 'healthy', latest_run_status: 'failed' },
      { project_id: 'overdue', status: 'overdue', latest_run_status: 'succeeded' },
    ])
    expect(rows[0]?.project.id).toBe('overdue')
    expect(rows[0]?.health?.status).toBe('overdue')
  })
})
