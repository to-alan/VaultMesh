import { describe, expect, it } from 'vitest'
import {
  auditActionCategory,
  formatBytes,
  healthOrder,
  localDateKey,
  projectHealthLabel,
  runOperationGroup,
  runOperationLabel,
  sourceSummary,
  statusLabel,
  describeProjectHealth,
} from '../display'
import type { ProjectHealth } from '../types'

describe('formatBytes', () => {
  it('formats byte scales with human units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(formatBytes(5 * 1024 * 1024 * 1024)).toBe('5.0 GB')
  })
})

describe('run operation grouping', () => {
  it('maps operations to the filter groups used by the runs tab', () => {
    expect(runOperationGroup({ stats: { operation: 'backup' } } as never)).toBe('backup')
    expect(runOperationGroup({ stats: { operation: 'prune' } } as never)).toBe('maintenance')
    expect(runOperationGroup({ stats: { operation: 'snapshot_restore' } } as never)).toBe('recovery')
    expect(runOperationGroup({} as never)).toBe('backup')
  })

  it('labels maintenance operations instead of defaulting to backup', () => {
    expect(runOperationLabel({ stats: { operation: 'retention_preview' } } as never)).toBe('清理预览')
    expect(runOperationLabel({ stats: { operation: 'snapshot_sync' } } as never)).toBe('快照同步')
    expect(runOperationLabel({} as never)).toBe('备份')
  })
})

describe('audit action categorization', () => {
  it('maps every audited action to a filter category', () => {
    expect(auditActionCategory('auth.password')).toBe('authentication')
    expect(auditActionCategory('security.totp.enable')).toBe('security')
    expect(auditActionCategory('backup.run')).toBe('backup')
    expect(auditActionCategory('snapshot.restore')).toBe('backup')
    expect(auditActionCategory('server.archive')).toBe('configuration')
    expect(auditActionCategory('agent.enroll')).toBe('authentication')
  })
})

describe('project health display', () => {
  it('labels every derived health status', () => {
    const statuses = ['healthy', 'pending', 'running', 'late', 'overdue', 'paused', 'invalid']
    for (const status of statuses) {
      const health = { status } as ProjectHealth
      expect(projectHealthLabel(health)).not.toBe('状态计算中')
    }
    // describeProjectHealth owns the undefined case; the label helper falls
    // back to the neutral placeholder.
    expect(projectHealthLabel(undefined)).toBe('状态计算中')
  })

  it('explains a running backup without a countdown', () => {
    const health = { status: 'running', deadline_at: new Date().toISOString() } as unknown as ProjectHealth
    expect(describeProjectHealth(health, Date.now())).toContain('正在执行')
  })
})

describe('run health ordering', () => {
  it('ranks failures before partials before healthy', () => {
    expect(healthOrder('failed')).toBeLessThan(healthOrder('partial'))
    expect(healthOrder('partial')).toBeLessThan(healthOrder('succeeded'))
  })
})

describe('status labels', () => {
  it('translates run and skipped statuses', () => {
    expect(statusLabel('skipped')).toBe('已跳过')
    expect(statusLabel('partial')).toBe('部分成功')
    expect(statusLabel('mystery')).toBe('mystery')
  })
})

describe('localDateKey', () => {
  it('zero-pads month and day', () => {
    expect(localDateKey(new Date(2026, 0, 5))).toBe('2026-01-05')
  })
})

describe('sourceSummary', () => {
  it('summarizes file and database sources', () => {
    const files = { type: 'files', paths: ['/etc', '/var'] } as never
    expect(sourceSummary(files)).toContain('/etc')
    const database = { type: 'mysql', database: { database: 'app', host: 'db', port: 3306 } } as never
    expect(sourceSummary(database)).toContain('app@db:3306')
  })
})
