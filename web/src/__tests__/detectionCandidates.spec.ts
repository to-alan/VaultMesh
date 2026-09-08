import { describe, expect, it } from 'vitest'
import { detectionCandidates, selectedDetectionCandidates } from '../detectionCandidates'
import { detectionFixture } from './detectionFixtures'
import { makeProject } from './fixtures'

describe('backup candidate classification', () => {
  it('recommends business data and preserves original indices across all groups', () => {
    const candidates = detectionCandidates(detectionFixture, [makeProject()], 'srv_test')
    expect(candidates.filter(item => item.group === 'recommended').map(item => item.key)).toEqual(['databases:0', 'apps:1', 'containers:0'])
    expect(candidates.find(item => item.key === 'apps:2')?.group).toBe('review')
    expect(candidates.find(item => item.key === 'apps:3')?.reason).toContain('已有项目')
    expect(candidates.find(item => item.key === 'containers:2')?.group).toBe('review')
    expect(candidates.filter(item => item.group === 'excluded')).toHaveLength(3)
    expect(selectedDetectionCandidates(candidates, { apps: [0, 1, 99], databases: [1, 2], containers: [0, 1, 4] }).map(item => item.key)).toEqual(['apps:1', 'containers:0'])
  })

  it('requires a published reachable endpoint and a dump tool for database drafts', () => {
    const report = { ...detectionFixture, databases: [
      { kind: 'mysql' as const, source: 'loopback', host: '127.0.0.1', port: 3306, reachable: true },
      { kind: 'mysql' as const, source: 'docker', host: '127.0.0.1', port: 0, reachable: true, dump_tool: 'mysqldump' },
    ] }
    expect(detectionCandidates(report, [], 'srv_test').filter(item => item.kind === 'databases').every(item => !item.selectable)).toBe(true)
  })

  it('does not hide name-alike business services and keeps stopped mounts selectable for review', () => {
    const report = { generated_at: '', containers: [
      { name: 'vaultmesh-worker', image: 'custom/vaultmesh-worker:latest', running: true, mounts: ['/data/business'] },
      { name: 'redis-cache', image: 'redis:7', running: true, mounts: ['/data/redis'] },
      { name: 'old-uploads', image: 'custom/upload:latest', running: false, mounts: ['/data/old'] },
    ] }
    const candidates = detectionCandidates(report, [], 'srv_test')
    expect(candidates.map(item => item.group)).toEqual(['recommended', 'review', 'review'])
    expect(candidates.every(item => item.selectable)).toBe(true)
  })

  it('does not claim that paused, excluded or other-server projects provide protection', () => {
    const report = { generated_at: '', apps: [{ path: '/srv/application', name: 'PHP', kind: 'php', markers: [] }] }
    expect(detectionCandidates(report, [makeProject({ server_id: 'other' })], 'srv_test')[0]?.group).toBe('recommended')
    const candidate = detectionCandidates(report, [makeProject({ enabled: false })], 'srv_test')[0]!
    expect(candidate.group).toBe('review')
    expect(candidate.reason).toContain('已暂停')
    expect(candidate.reason).toContain('是否被排除')
    expect(candidate.reason).not.toContain('已保护')
  })

  it('handles empty reports and only matches ancestor paths on segment boundaries', () => {
    expect(detectionCandidates({ generated_at: '' }, [], '')).toEqual([])
    const report = { generated_at: '', apps: [{ path: '/srv/application-two', name: 'PHP', kind: 'php', markers: [] }] }
    expect(detectionCandidates(report, [makeProject()], 'srv_test')[0]?.group).toBe('recommended')
  })

  it('does not recommend runtime-only mounts or children of an excluded directory', () => {
    const report = { generated_at: '', containers: [
      { name: 'proxy', image: 'custom/proxy:latest', running: true, mounts: ['/var/run/docker.sock', '/etc/localtime'] },
    ], apps: [
      { path: '/opt/platform', name: 'Go', kind: 'go', markers: [], exclusion_reason: 'VaultMesh 自身组件' },
      { path: '/opt/platform/web', name: 'Node.js', kind: 'nodejs', markers: [] },
    ] }
    const candidates = detectionCandidates(report, [], '')
    expect(candidates.map(item => item.group)).toEqual(['excluded', 'excluded', 'unavailable'])
    expect(candidates.every(item => !item.selectable)).toBe(true)
  })
})
