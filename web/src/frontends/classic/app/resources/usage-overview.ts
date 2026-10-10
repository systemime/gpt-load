import { queryOptions } from '@tanstack/vue-query'

import type { ApiClient } from '@shared/http/client'
import { controlQueryKeys } from '@/app/query-keys'

import { assertNoSecretLikeFields, projectArray, projectRecord, projectSafeInteger, projectString } from './projector'

// 每人累计用量总览(阶段 4): 数据源 usage_stats, 支持全历史。
// 筛选: 时间区间(可单边)、分组、单个用户。

export interface UsageOverviewPerson {
  access_key_id: number
  name: string
  totals: {
    requests: number
    success: number
    uncached_input_tokens: number
    output_tokens: number
    cache_read_tokens: number
  }
}

export interface UsageOverviewPoint {
  access_key_id: number
  day_ms: number
  requests: number
  success: number
  tokens: number
}

export interface UsageOverviewFilters {
  from_ms?: number
  to_ms?: number
  group_id?: number
  access_key_id?: number
}

export interface UsageOverviewReport {
  observed_at_ms: number
  people: UsageOverviewPerson[]
  series: UsageOverviewPoint[]
}

const totalsFields = [
  'requests',
  'success',
  'uncached_input_tokens',
  'output_tokens',
  'cache_read_tokens',
] as const
const personFields = ['access_key_id', 'name', 'totals'] as const
const pointFields = ['access_key_id', 'day_ms', 'requests', 'success', 'tokens'] as const
const reportFields = ['observed_at_ms', 'filter', 'people', 'series'] as const

function projectTotals(value: unknown): UsageOverviewPerson['totals'] {
  const record = projectRecord(value)
  assertNoSecretLikeFields(record, totalsFields)
  return {
    requests: projectSafeInteger(record.requests, { minimum: 0 }),
    success: projectSafeInteger(record.success, { minimum: 0 }),
    uncached_input_tokens: projectSafeInteger(record.uncached_input_tokens, { minimum: 0 }),
    output_tokens: projectSafeInteger(record.output_tokens, { minimum: 0 }),
    cache_read_tokens: projectSafeInteger(record.cache_read_tokens, { minimum: 0 }),
  }
}

export function projectUsageOverview(value: unknown): UsageOverviewReport {
  const record = projectRecord(value)
  assertNoSecretLikeFields(record, reportFields)
  return {
    observed_at_ms: projectSafeInteger(record.observed_at_ms, { minimum: 0 }),
    people: projectArray(record.people, (item): UsageOverviewPerson => {
      const row = projectRecord(item)
      assertNoSecretLikeFields(row, personFields)
      return {
        access_key_id: projectSafeInteger(row.access_key_id, { minimum: 1 }),
        name: projectString(row.name, { allowEmpty: true }),
        totals: projectTotals(row.totals),
      }
    }),
    series: projectArray(record.series, (item): UsageOverviewPoint => {
      const row = projectRecord(item)
      assertNoSecretLikeFields(row, pointFields)
      return {
        access_key_id: projectSafeInteger(row.access_key_id, { minimum: 1 }),
        day_ms: projectSafeInteger(row.day_ms, { minimum: 0 }),
        requests: projectSafeInteger(row.requests, { minimum: 0 }),
        success: projectSafeInteger(row.success, { minimum: 0 }),
        tokens: projectSafeInteger(row.tokens, { minimum: 0 }),
      }
    }),
  }
}

export function usageOverviewQueryOptions(
  client: ApiClient,
  filters: UsageOverviewFilters,
) {
  const params = new URLSearchParams()
  if (filters.from_ms !== undefined) params.set('from_ms', String(filters.from_ms))
  if (filters.to_ms !== undefined) params.set('to_ms', String(filters.to_ms))
  if (filters.group_id !== undefined) params.set('group_id', String(filters.group_id))
  if (filters.access_key_id !== undefined) params.set('access_key_id', String(filters.access_key_id))
  const suffix = params.size > 0 ? `?${params.toString()}` : ''
  return queryOptions({
    queryKey: controlQueryKeys.usageOverview(filters),
    queryFn: ({ signal }) =>
      projectUsageOverview(
        client.request(`/api/usage/overview${suffix}`, { method: 'GET', signal }),
      ),
    refetchOnWindowFocus: false,
    placeholderData: (previous) => previous,
  })
}
