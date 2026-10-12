import type { ApiClient } from '@shared/http/client'
import { integer, list, record, text } from './response'

// 每人累计用量总览(阶段 4): 数据源 usage_stats(不随日志保留清理),
// 支持全历史; 筛选 时间区间/分组/用户。

export interface UsageOverviewTotals {
  requests: number
  success: number
  uncachedInputTokens: number
  outputTokens: number
  cacheReadTokens: number
}

export interface UsageOverviewPerson {
  id: number
  name: string
  totals: UsageOverviewTotals
}

export interface UsageOverviewCredential {
  id: number
  name: string
  totals: UsageOverviewTotals
}

export interface UsageOverviewPoint {
  accessKeyID: number
  dayMS: number
  requests: number
  success: number
  tokens: number
}

export interface UsageOverviewFilters {
  fromMS?: number
  toMS?: number
  groupID?: number
  accessKeyID?: number
}

export interface UsageOverviewReport {
  observedAt: number
  people: UsageOverviewPerson[]
  credentials: UsageOverviewCredential[]
  points: UsageOverviewPoint[]
}

function readTotals(value: unknown): UsageOverviewTotals {
  const row = record(value)
  return {
    requests: integer(row.requests),
    success: integer(row.success),
    uncachedInputTokens: integer(row.uncached_input_tokens),
    outputTokens: integer(row.output_tokens),
    cacheReadTokens: integer(row.cache_read_tokens),
  }
}

function readPoint(value: unknown): UsageOverviewPoint {
  const row = record(value)
  return {
    accessKeyID: integer(row.access_key_id, 1),
    dayMS: integer(row.day_ms),
    requests: integer(row.requests),
    success: integer(row.success),
    tokens: integer(row.tokens),
  }
}

export function readUsageOverview(value: unknown): UsageOverviewReport {
  const row = record(value)
  return {
    observedAt: integer(row.observed_at_ms),
    people: list(row.people).map((item) => {
      const person = record(item)
      return {
        id: integer(person.access_key_id, 1),
        name: text(person.name),
        totals: readTotals(person.totals),
      }
    }),
    credentials: list(row.credentials ?? []).map((item) => {
      const credential = record(item)
      return {
        id: integer(credential.credential_id, 1),
        name: text(credential.name),
        totals: readTotals(credential.totals),
      }
    }),
    points: list(row.series).map(readPoint),
  }
}

export async function getUsageOverview(
  client: ApiClient,
  filters: UsageOverviewFilters,
  signal: AbortSignal,
): Promise<UsageOverviewReport> {
  const params = new URLSearchParams()
  if (filters.fromMS !== undefined) params.set('from_ms', String(filters.fromMS))
  if (filters.toMS !== undefined) params.set('to_ms', String(filters.toMS))
  if (filters.groupID !== undefined) params.set('group_id', String(filters.groupID))
  if (filters.accessKeyID !== undefined) params.set('access_key_id', String(filters.accessKeyID))
  const suffix = params.size > 0 ? `?${params.toString()}` : ''
  return readUsageOverview(await client.request(`/api/usage/overview${suffix}`, { signal }))
}

export interface GroupOptionLite {
  id: number
  name: string
}

export async function getGroupOptionsLite(
  client: ApiClient,
  signal: AbortSignal,
): Promise<GroupOptionLite[]> {
  const rows = list(await client.request('/api/groups/options', { signal }))
  return rows.map((item) => {
    const row = record(item)
    return { id: integer(row.id, 1), name: text(row.name) }
  })
}
