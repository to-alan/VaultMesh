import { computed, ref, type Ref } from 'vue'
import { auditActionCategory } from '../display'
import type { AuditEvent } from '../types'

type AuditCategoryFilter = 'all' | ReturnType<typeof auditActionCategory>

// useAuditFilters owns the audit-tab category/outcome filters and the derived
// metric counts. The clock comes in as a ref so "last 24 hours" stays reactive
// to the shell's periodic time update without owning a timer itself.
export function useAuditFilters(events: Ref<AuditEvent[]>, nowEpoch: Ref<number>) {
  const categoryFilter = ref<AuditCategoryFilter>('all')
  const outcomeFilter = ref<'all' | AuditEvent['outcome']>('all')

  const filtered = computed(() => events.value.filter((event) => (
    (outcomeFilter.value === 'all' || event.outcome === outcomeFilter.value)
    && (categoryFilter.value === 'all' || auditActionCategory(event.action) === categoryFilter.value)
  )))

  const last24Hours = computed(() => {
    const cutoff = nowEpoch.value - 24 * 60 * 60 * 1000
    return events.value.filter((event) => new Date(event.created_at).getTime() >= cutoff).length
  })

  const failed = computed(() => events.value.filter((event) => event.outcome === 'failed').length)

  const security = computed(() => events.value.filter((event) => (
    ['authentication', 'security'].includes(auditActionCategory(event.action))
  )).length)

  return { categoryFilter, outcomeFilter, filtered, last24Hours, failed, security }
}
