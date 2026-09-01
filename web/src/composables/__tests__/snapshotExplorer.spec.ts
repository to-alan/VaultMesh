import { ref } from 'vue'
import { describe, expect, it } from 'vitest'
import { useSnapshotExplorer, type RunLike } from '../snapshotExplorer'
import type { Snapshot } from '../../types'

function snapshot(overrides: Partial<Snapshot> = {}): Snapshot {
  return {
    id: 'a'.repeat(64),
    project_id: 'prj_1',
    server_id: 'srv_1',
    time: '2026-01-01T00:00:00Z',
    hostname: 'host',
    paths: ['/etc'],
    tags: [],
    protected: false,
    last_synced_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

import type { Run } from '../../types'

function makeExplorer(runs: RunLike[] = [], options: { browse?: (commandID: string) => void; restore?: (commandID: string) => void } = {}) {
  const issued: { kind: string; projectID: string; snapshotID: string; path: string }[] = []
  const explorer = useSnapshotExplorer({
    runs: ref(runs),
    browse: async (projectID, snapshotID, path) => {
      const command = { id: `cmd_browse_${issued.length}` }
      issued.push({ kind: 'browse', projectID, snapshotID, path })
      options.browse?.(command.id)
      return command
    },
    restore: async (projectID, snapshotID, path) => {
      const command = { id: `cmd_restore_${issued.length}` }
      issued.push({ kind: 'restore', projectID, snapshotID, path })
      options.restore?.(command.id)
      return command
    },
  })
  return { explorer, issued }
}

describe('useSnapshotExplorer', () => {
  it('selecting a snapshot filters the project and issues a root browse', async () => {
    const { explorer, issued } = makeExplorer()
    explorer.snapshots.value = [snapshot()]
    await explorer.select(snapshot())
    expect(explorer.projectFilter.value).toBe('prj_1')
    expect(explorer.browsePath.value).toBe('/')
    expect(issued).toEqual([{ kind: 'browse', projectID: 'prj_1', snapshotID: 'a'.repeat(64), path: '/' }])
  })

  it('derives entries from the matching succeeded browse run', async () => {
    const runs = [{
      idempotency_key: 'manual:cmd_1',
      project_id: 'prj_1',
      status: 'succeeded',
      stats: { operation: 'snapshot_browse', snapshot_id: 'a'.repeat(64), path: '/', entries: [
        { path: '/b', type: 'dir', name: 'b' },
        { path: '/a.txt', type: 'file', name: 'a.txt' },
      ] },
    }]
    const { explorer } = makeExplorer(runs)
    explorer.snapshots.value = [snapshot()]
    await explorer.select(snapshot())
    explorer.browseCommandID.value = 'cmd_1'
    // Directories sort first, then locale order.
    expect(explorer.entries.value.map((entry) => entry.name)).toEqual(['b', 'a.txt'])
    expect(explorer.browsePending.value).toBe(false)
  })

  it('stays pending until the browse run appears', async () => {
    let resolve!: (value: { id: string }) => void
    const explorer = useSnapshotExplorer({
      runs: ref([]),
      browse: () => new Promise((r) => { resolve = r }),
      restore: async () => null,
    })
    explorer.snapshots.value = [snapshot()]
    const selecting = explorer.select(snapshot())
    // Nothing is in flight yet: no command has been issued.
    expect(explorer.browseCommandID.value).toBe('')
    expect(explorer.browsePending.value).toBe(false)
    resolve({ id: 'cmd_1' })
    await selecting
    expect(explorer.browseCommandID.value).toBe('cmd_1')
    // The command exists but no matching run report has arrived: keep waiting.
    expect(explorer.browsePending.value).toBe(true)
  })

  it('skips the state update when the browse callback reports failure', async () => {
    const failing = useSnapshotExplorer({
      runs: ref([]),
      browse: async () => null,
      restore: async () => null,
    })
    failing.snapshots.value = [snapshot()]
    await failing.select(snapshot())
    await failing.browse('/etc')
    expect(failing.browsePath.value).toBe('/')
  })

  it('confirmRestore clears the pending path and records the command', async () => {
    const { explorer } = makeExplorer()
    explorer.snapshots.value = [snapshot()]
    await explorer.select(snapshot())
    explorer.requestRestore('/etc/nginx')
    expect(explorer.pendingRestorePath.value).toBe('/etc/nginx')
    await explorer.confirmRestore()
    expect(explorer.restoreCommandID.value).toContain('cmd_restore')
    expect(explorer.pendingRestorePath.value).toBeNull()
  })

  it('prunes stale selections when the inventory refreshes', async () => {
    const { explorer } = makeExplorer()
    explorer.snapshots.value = [snapshot()]
    await explorer.select(snapshot())
    explorer.snapshots.value = []
    explorer.pruneInvalidSelection(['prj_1'])
    expect(explorer.selectedID.value).toBe('')
    expect(explorer.browsePath.value).toBe('/')
  })
})
