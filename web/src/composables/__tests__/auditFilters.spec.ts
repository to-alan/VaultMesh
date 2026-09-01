import { ref } from 'vue'
import { describe, expect, it } from 'vitest'
import { useAuditFilters } from '../auditFilters'
import type { AuditEvent } from '../../types'

function event(overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'aud_1',
    actor: 'admin',
    action: 'auth.password',
    outcome: 'succeeded',
    client_ip: '127.0.0.1',
    status_code: 200,
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

describe('useAuditFilters', () => {
  it('counts last-24-hour events against the injected clock', () => {
    const now = Date.now()
    const events = [
      event(),
      event({ id: 'aud_2', created_at: new Date(now - 48 * 60 * 60 * 1000).toISOString() }),
    ]
    const nowEpoch = ref(now)
    const filters = useAuditFilters(ref(events), nowEpoch)
    expect(filters.last24Hours.value).toBe(1)

    // The clock ref is respected reactively: moving the clock forward 25
    // hours ages every event out of the window.
    nowEpoch.value = now + 25 * 60 * 60 * 1000
    expect(filters.last24Hours.value).toBe(0)
  })

  it('filters by category and outcome together', () => {
    const events = [
      event({ id: 'aud_ok', action: 'auth.password', outcome: 'succeeded' }),
      event({ id: 'aud_bad', action: 'auth.password', outcome: 'failed' }),
      event({ id: 'aud_backup', action: 'backup.run', outcome: 'succeeded' }),
    ]
    const filters = useAuditFilters(ref(events), ref(Date.now()))
    filters.categoryFilter.value = 'authentication'
    expect(filters.filtered.value.map((item) => item.id)).toEqual(['aud_ok', 'aud_bad'])
    filters.outcomeFilter.value = 'failed'
    expect(filters.filtered.value.map((item) => item.id)).toEqual(['aud_bad'])
  })

  it('derives failed and security metrics', () => {
    const events = [
      event({ outcome: 'failed' }),
      event({ id: 'aud_2', action: 'security.totp.enable' }),
      event({ id: 'aud_3', action: 'project.create' }),
    ]
    const filters = useAuditFilters(ref(events), ref(Date.now()))
    expect(filters.failed.value).toBe(1)
    expect(filters.security.value).toBe(2)
  })
})
