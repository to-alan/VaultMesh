import { describe, expect, it } from 'vitest'
import { createProjectFormDraft, projectFormDraftFromProject, projectWriteInput } from '../forms/project'
import { makeProject } from './fixtures'

describe('project edit round trip', () => {
  it('preserves exact schedule seconds, custom subset and a separate maintenance timezone when renaming', () => {
    const project = makeProject()
    const draft = projectFormDraftFromProject(project)
    draft.name = '新名称'
    const payload = projectWriteInput(draft)
    expect(payload.name).toBe('新名称')
    expect(payload.schedule).toEqual(project.schedule)
    expect(payload.policy?.maintenance).toEqual(project.policy?.maintenance)
    expect(payload.policy?.verification).toEqual(project.policy?.verification)
  })

  it('does not silently opt a legacy project into separately scheduled maintenance', () => {
    const project = makeProject()
    project.policy!.maintenance = { separate: false, timezone: 'UTC' }
    expect(projectWriteInput(projectFormDraftFromProject(project)).policy?.maintenance.separate).toBe(false)
  })

  it('disables prune with retention, without forgetting the draft preference', () => {
    const draft = projectFormDraftFromProject(makeProject())
    draft.retention_enabled = false
    expect(projectWriteInput(draft).policy?.retention.prune).toBe(false)
    draft.retention_enabled = true
    expect(projectWriteInput(draft).policy?.retention.prune).toBe(true)
  })

  it('uses the backup timezone for a new independent maintenance window', () => {
    const draft = createProjectFormDraft()
    draft.timezone = 'Asia/Tokyo'
    expect(projectWriteInput(draft).policy?.maintenance).toMatchObject({ separate: true, timezone: 'Asia/Tokyo' })
  })
})
