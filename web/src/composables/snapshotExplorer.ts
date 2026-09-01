import { computed, ref, type Ref } from 'vue'
import type { Snapshot, SnapshotEntry } from '../types'

interface CommandLike {
  id: string
}

// useSnapshotExplorer owns the snapshots-tab state machine: project filter,
// selected snapshot, browse path, pending restore confirmation, and the
// derived browse/restore run lookups. Network actions are injected as
// callbacks so the composable stays free of App.vue error handling and
// polling concerns. A callback returning null means the operation failed and
// was surfaced to the administrator; the state machine then skips its update.
export function useSnapshotExplorer(deps: {
  runs: Ref<RunLike[]>
  browse: (projectID: string, snapshotID: string, path: string) => Promise<CommandLike | null>
  restore: (projectID: string, snapshotID: string, path: string) => Promise<CommandLike | null>
}) {
  const snapshots = ref<Snapshot[]>([])
  const projectFilter = ref('')
  const selectedID = ref('')
  const selectedProjectID = ref('')
  const browsePath = ref('/')
  const pendingRestorePath = ref<string | null>(null)
  const browseCommandID = ref('')
  const restoreCommandID = ref('')

  const filtered = computed(() => snapshots.value.filter((snapshot) => (
    !projectFilter.value || snapshot.project_id === projectFilter.value
  )))
  const selected = computed(() => snapshots.value.find((snapshot) => (
    snapshot.id === selectedID.value && snapshot.project_id === selectedProjectID.value
  )))
  const protectedCount = computed(() => snapshots.value.filter((snapshot) => snapshot.protected).length)
  const storageBytes = computed(() => snapshots.value.reduce((total, snapshot) => total + Number(snapshot.total_bytes || 0), 0))

  const currentBrowseRun = computed(() => {
    if (browseCommandID.value) {
      return deps.runs.value.find((run) => run.idempotency_key === `manual:${browseCommandID.value}`)
    }
    if (!selected.value) return undefined
    return deps.runs.value.find((run) => run.project_id === selected.value?.project_id
      && run.stats?.operation === 'snapshot_browse'
      && run.stats?.snapshot_id === selected.value?.id
      && run.stats?.path === browsePath.value)
  })

  const currentRestoreRun = computed(() => {
    if (restoreCommandID.value) {
      return deps.runs.value.find((run) => run.idempotency_key === `manual:${restoreCommandID.value}`)
    }
    if (!selected.value) return undefined
    return deps.runs.value.find((run) => run.project_id === selected.value?.project_id
      && run.stats?.operation === 'snapshot_restore'
      && run.stats?.snapshot_id === selected.value?.id)
  })

  const entries = computed<SnapshotEntry[]>(() => {
    if (currentBrowseRun.value?.status !== 'succeeded') return []
    const value = currentBrowseRun.value.stats?.entries
    if (!Array.isArray(value)) return []
    return value.filter((entry): entry is SnapshotEntry => Boolean(
      entry && typeof entry === 'object' && typeof entry.path === 'string' && typeof entry.type === 'string',
    )).sort((left, right) => {
      if (left.type === 'dir' && right.type !== 'dir') return -1
      if (right.type === 'dir' && left.type !== 'dir') return 1
      return left.name.localeCompare(right.name, 'zh-CN')
    })
  })

  const breadcrumbs = computed(() => {
    const segments = browsePath.value.split('/').filter(Boolean)
    return [
      { label: '/', path: '/' },
      ...segments.map((segment, index) => ({ label: segment, path: `/${segments.slice(0, index + 1).join('/')}` })),
    ]
  })

  const browsePending = computed(() => Boolean(browseCommandID.value && !currentBrowseRun.value))
  const restorePending = computed(() => Boolean(restoreCommandID.value && !currentRestoreRun.value))

  async function select(snapshot: Snapshot) {
    selectedID.value = snapshot.id
    selectedProjectID.value = snapshot.project_id
    projectFilter.value = snapshot.project_id
    pendingRestorePath.value = null
    restoreCommandID.value = ''
    await browse('/')
  }

  function browse(path: string) {
    const snapshot = selected.value
    if (!snapshot) return Promise.resolve()
    return deps.browse(snapshot.project_id, snapshot.id, path).then((command) => {
      if (!command) return
      browsePath.value = path
      browseCommandID.value = command.id
      pendingRestorePath.value = null
    })
  }

  function requestRestore(path: string) {
    pendingRestorePath.value = path
    restoreCommandID.value = ''
  }

  function confirmRestore() {
    const snapshot = selected.value
    const path = pendingRestorePath.value
    if (!snapshot || !path) return Promise.resolve()
    return deps.restore(snapshot.project_id, snapshot.id, path).then((command) => {
      if (!command) return
      restoreCommandID.value = command.id
      pendingRestorePath.value = null
    })
  }

  // pruneInvalidSelection drops a selected snapshot that no longer exists in
  // the refreshed inventory, mirroring the previous selectDefaults behavior.
  function pruneInvalidSelection(projectIDs: Iterable<string>) {
    const known = new Set(projectIDs)
    if (projectFilter.value && !known.has(projectFilter.value)) projectFilter.value = ''
    if (selectedID.value && !snapshots.value.some((snapshot) => (
      snapshot.id === selectedID.value && snapshot.project_id === selectedProjectID.value
    ))) {
      resetSelection()
    }
  }

  function resetSelection() {
    selectedID.value = ''
    selectedProjectID.value = ''
    browsePath.value = '/'
    browseCommandID.value = ''
    restoreCommandID.value = ''
    pendingRestorePath.value = null
  }

  function clear() {
    snapshots.value = []
    projectFilter.value = ''
    resetSelection()
  }

  return {
    snapshots,
    projectFilter,
    selectedID,
    selectedProjectID,
    browsePath,
    pendingRestorePath,
    browseCommandID,
    restoreCommandID,
    filtered,
    selected,
    protectedCount,
    storageBytes,
    currentBrowseRun,
    currentRestoreRun,
    entries,
    breadcrumbs,
    browsePending,
    restorePending,
    select,
    browse,
    requestRestore,
    confirmRestore,
    pruneInvalidSelection,
    resetSelection,
    clear,
  }
}

export interface RunLike {
  idempotency_key?: string
  project_id: string
  status: string
  error_message?: string
  stats?: {
    operation?: string
    snapshot_id?: string
    path?: string
    restore_target?: string
    files_restored?: number
    bytes_restored?: number
    entries?: unknown
  }
}
