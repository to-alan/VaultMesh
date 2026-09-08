import { localDateKey } from './display'
import type { Project, ProjectHealth, Run } from './types'

export const backupOutcomeGroups = [
  { key: 'succeeded', label: '成功', color: '#5df0a8', statuses: ['succeeded'] },
  { key: 'partial', label: '部分成功', color: '#f6c85f', statuses: ['partial'] },
  { key: 'failed', label: '失败 / 超时 / 未知', color: '#ff6b73', statuses: ['failed', 'timed_out', 'unknown'] },
  { key: 'canceled', label: '已取消', color: '#bc8cff', statuses: ['canceled'] },
  { key: 'running', label: '等待 / 执行中', color: '#65b8ff', statuses: ['pending', 'running'] },
  { key: 'skipped', label: '已跳过', color: '#758590', statuses: ['skipped'] },
] as const

export function backupRuns(runs: Run[]): Run[] {
  return runs.filter((run) => (run.stats?.operation || 'backup') === 'backup')
}

export function summarizeBackups(runs: Run[]) {
  const backups = backupRuns(runs)
  const distribution = backupOutcomeGroups.map((group) => ({
    ...group, count: backups.filter((run) => (group.statuses as readonly string[]).includes(run.status)).length,
  }))
  const succeeded = distribution[0]!.count
  // Pending, running and skipped triggers did not produce a completed attempt.
  const completed = distribution.slice(0, 4).reduce((total, group) => total + group.count, 0)
  return { total: backups.length, completed, succeeded, successRate: completed ? Math.round(succeeded / completed * 100) : null, distribution }
}

export function backupTrend(runs: Run[], nowEpoch: number) {
  const points = Array.from({ length: 7 }, (_, index) => {
    const date = new Date(nowEpoch)
    date.setHours(0, 0, 0, 0)
    date.setDate(date.getDate() - (6 - index))
    return { key: localDateKey(date), label: `${date.getMonth() + 1}/${date.getDate()}`, runs: [] as Run[] }
  })
  const byDate = new Map(points.map((point) => [point.key, point]))
  for (const run of backupRuns(runs)) byDate.get(localDateKey(new Date(run.started_at)))?.runs.push(run)
  const max = Math.max(1, ...points.map((point) => point.runs.length))
  return { max, points: points.map((point) => ({ ...point, ...summarizeBackups(point.runs) })) }
}

const healthPriority: Record<string, number> = { overdue: 0, invalid: 1, late: 2, running: 3, pending: 4, healthy: 5, paused: 6 }

export function projectHealthRows(projects: Project[], health: ProjectHealth[]) {
  const byID = new Map(health.map((item) => [item.project_id, item]))
  return projects.map((project) => ({ project, health: byID.get(project.id) }))
    .sort((a, b) => (healthPriority[a.health?.status || ''] ?? 4) - (healthPriority[b.health?.status || ''] ?? 4)
      || a.project.name.localeCompare(b.project.name, 'zh-CN'))
}
