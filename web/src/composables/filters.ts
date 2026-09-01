import { computed, ref, type Ref } from 'vue'
import { runOperationGroup, runOperationLabel, sourceSummary } from '../display'
import type { RunOperationFilter } from '../display'
import type { Project, Run, Server } from '../types'

export type RunStatusFilter = 'all' | 'active' | 'succeeded' | 'attention'

export const ACTIVE_RUN_STATUSES = ['pending', 'running']
export const ATTENTION_RUN_STATUSES = ['partial', 'failed', 'timed_out', 'canceled', 'unknown']

// useRunFilters owns the runs-tab search and status/operation filters. It
// takes label lookups as plain functions so the composable stays decoupled
// from how App.vue resolves project and server names.
export function useRunFilters(
  runs: Ref<Run[]> | (() => Run[]),
  labels: {
    projectName: (projectID: string) => string
    serverName: (serverID: string) => string
  },
) {
  const search = ref('')
  const statusFilter = ref<RunStatusFilter>('all')
  const operationFilter = ref<RunOperationFilter>('all')

  const runList = computed(() => (typeof runs === 'function' ? runs() : runs.value))
  const activeCount = computed(() => runList.value.filter((run) => ACTIVE_RUN_STATUSES.includes(run.status)).length)
  const attentionCount = computed(() => runList.value.filter((run) => ATTENTION_RUN_STATUSES.includes(run.status)).length)

  const filtered = computed(() => {
    const query = search.value.trim().toLocaleLowerCase('zh-CN')
    return runList.value.filter((run) => {
      const statusMatches = statusFilter.value === 'all'
        || (statusFilter.value === 'active' && ACTIVE_RUN_STATUSES.includes(run.status))
        || (statusFilter.value === 'succeeded' && run.status === 'succeeded')
        || (statusFilter.value === 'attention' && ATTENTION_RUN_STATUSES.includes(run.status))
      const operation = runOperationGroup(run)
      const operationMatches = operationFilter.value === 'all' || operationFilter.value === operation
      const textMatches = !query || [
        run.id,
        run.snapshot_id,
        run.error_code,
        run.error_message,
        run.status,
        labels.projectName(run.project_id),
        labels.serverName(run.server_id),
        runOperationLabel(run),
      ].some((value) => String(value || '').toLocaleLowerCase('zh-CN').includes(query))
      return statusMatches && operationMatches && textMatches
    })
  })

  return { search, statusFilter, operationFilter, activeCount, attentionCount, filtered }
}

// useProjectFilters owns the projects-tab search and state filters. Grouping
// stays in the caller because the overview tab reuses the same computations.
export function useProjectFilters(
  groups: Ref<{ id: string; name: string; server?: Server; projects: Project[] }[]>,
  health: Ref<Map<string, { status?: string }>>,
  labels: {
    repositoryName: (repositoryID: string) => string
  },
) {
  const search = ref('')
  const stateFilter = ref<'all' | 'enabled' | 'paused' | 'at_risk'>('all')

  const filteredGroups = computed(() => {
    const query = search.value.trim().toLocaleLowerCase('zh-CN')
    return groups.value.map((group) => {
      const serverMatches = !query || [group.name, group.server?.hostname, group.server?.id]
        .some((value) => value?.toLocaleLowerCase('zh-CN').includes(query))
      const items = group.projects.filter((project) => {
        const status = health.value.get(project.id)?.status
        const stateMatches = stateFilter.value === 'all'
          || (stateFilter.value === 'enabled' && project.enabled)
          || (stateFilter.value === 'paused' && !project.enabled)
          || (stateFilter.value === 'at_risk' && ['late', 'overdue'].includes(status || ''))
        const textMatches = serverMatches || [project.name, project.id, labels.repositoryName(project.repository_id), ...project.sources.map(sourceSummary)]
          .some((value) => value.toLocaleLowerCase('zh-CN').includes(query))
        return stateMatches && textMatches
      })
      return { ...group, projects: items }
    }).filter((group) => !query && stateFilter.value === 'all' ? true : group.projects.length > 0)
  })

  const filteredCount = computed(() => filteredGroups.value.reduce((total, group) => total + group.projects.length, 0))

  return { search, stateFilter, filteredGroups, filteredCount }
}
