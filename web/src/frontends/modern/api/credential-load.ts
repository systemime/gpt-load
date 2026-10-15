import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { integer, list, record, text } from './response'

export const credentialLoadWindowKeys = ['1h', '5h', '24h'] as const
export type CredentialLoadWindowKey = (typeof credentialLoadWindowKeys)[number]

export interface CredentialLoadWindow {
  requests: number
  success: number
  affinityHits: number
  uncachedInputTokens: number
  outputTokens: number
  cacheReadTokens: number
}

export interface CredentialLoadErrors {
  rateLimited: number
  connect: number
  timeout: number
  upstream: number
  canceled: number
  scheduled: number
  other: number
}

export interface CredentialLoadUsage {
  requests5h: number
  tokens5h: number
  requests7d: number
  tokens7d: number
}

export interface CredentialLoadItem {
  id: number
  name: string
  status: string
  // runtimeState 只有 available 才参与调度, 冷却/停用/隔离期间不占容量。
  runtimeState: string
  inflight: number
  queued: number
  usage: CredentialLoadUsage
  pressure: number
  credits5h: number
  credits7d: number
  windows: Record<CredentialLoadWindowKey, CredentialLoadWindow>
  errors5h: CredentialLoadErrors
}

export interface CredentialLoadPlan {
  plan: string
  quotaCredits5h: number
  quotaCredits7d: number
  teamCredits5h: number
  teamCredits7d: number
}

export interface CredentialLoadDimension {
  id: number
  name: string
  windows: Record<CredentialLoadWindowKey, CredentialLoadWindow>
}

export interface CredentialLoadHistoryPoint {
  atMs: number
  inflight: number
  queued: number
  silentTransfers: number
}

export interface CredentialLoadReport {
  observedAt: number
  cap: number
  plan: CredentialLoadPlan
  credentials: CredentialLoadItem[]
  localRejects: Record<CredentialLoadWindowKey, number>
  accessKeys: CredentialLoadDimension[]
  groups: CredentialLoadDimension[]
  // history 是服务端 1s 采样的 5 分钟滚动窗, 图表数据不依赖页面轮询。
  history: CredentialLoadHistoryPoint[]
}

function readDecimal(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0)
    throw new InvalidResponseError()
  return value
}

function readWindow(value: unknown): CredentialLoadWindow {
  const row = record(value)
  return {
    requests: integer(row.requests),
    success: integer(row.success),
    affinityHits: integer(row.affinity_hits),
    uncachedInputTokens: integer(row.uncached_input_tokens),
    outputTokens: integer(row.output_tokens),
    cacheReadTokens: integer(row.cache_read_tokens),
  }
}

function readErrors(value: unknown): CredentialLoadErrors {
  const row = record(value)
  return {
    rateLimited: integer(row.rate_limited),
    connect: integer(row.connect),
    timeout: integer(row.timeout),
    upstream: integer(row.upstream),
    canceled: integer(row.canceled),
    scheduled: integer(row.scheduled),
    other: integer(row.other),
  }
}

function readItem(value: unknown): CredentialLoadItem {
  const row = record(value)
  const windows = record(row.windows)
  return {
    id: integer(row.id, 1),
    name: text(row.name),
    status: text(row.status),
    runtimeState: text(row.runtime_state ?? 'active'),
    inflight: integer(row.inflight),
    queued: integer(row.queued),
    usage: (() => {
      const usageRow = record(row.usage)
      return {
        requests5h: integer(usageRow.requests_5h),
        tokens5h: integer(usageRow.tokens_5h),
        requests7d: integer(usageRow.requests_7d),
        tokens7d: integer(usageRow.tokens_7d),
      }
    })(),
    pressure: typeof row.pressure === 'number' && Number.isFinite(row.pressure) ? row.pressure : 0,
    credits5h: readDecimal(row.credits_5h),
    credits7d: readDecimal(row.credits_7d),
    windows: Object.fromEntries(
      credentialLoadWindowKeys.map((key) => [key, readWindow(windows[key])]),
    ) as Record<CredentialLoadWindowKey, CredentialLoadWindow>,
    errors5h: readErrors(row.errors_5h),
  }
}

function parseDimensions(value: unknown): CredentialLoadDimension[] {
  try {
    if (!Array.isArray(value)) return []
    return value.map((item) => {
      const row = record(item)
      const windows = record(row.windows ?? {})
      return {
        id: integer(row.id, 1),
        name: text(row.name),
        windows: Object.fromEntries(
          credentialLoadWindowKeys.map((key) => {
            try {
              return [key, readWindow(windows[key] ?? {})]
            } catch {
              return [key, { requests: 0, success: 0, affinityHits: 0, uncachedInputTokens: 0, outputTokens: 0, cacheReadTokens: 0 }]
            }
          }),
        ) as Record<CredentialLoadWindowKey, CredentialLoadWindow>,
      }
    })
  } catch {
    return []
  }
}

export function readCredentialLoad(value: unknown): CredentialLoadReport {
  const row = record(value)
  const rejects = record(row.local_rejects)
  const planRow = record(row.plan)
  return {
    observedAt: integer(row.observed_at_ms),
    cap: integer(row.cap),
    plan: {
      plan: text(planRow.plan),
      quotaCredits5h: readDecimal(planRow.quota_credits_5h),
      quotaCredits7d: readDecimal(planRow.quota_credits_7d),
      teamCredits5h: readDecimal(planRow.team_credits_5h),
      teamCredits7d: readDecimal(planRow.team_credits_7d),
    },
    credentials: list(row.credentials).map(readItem),
    history: list(row.history ?? []).map((value) => {
      const point = record(value)
      return {
        atMs: integer(point.at_ms),
        inflight: integer(point.inflight),
        queued: integer(point.queued),
        silentTransfers: integer(point.silent_transfers),
      }
    }),
    localRejects: Object.fromEntries(
      credentialLoadWindowKeys.map((key) => [key, integer(rejects[key])]),
    ) as Record<CredentialLoadWindowKey, number>,
    accessKeys: parseDimensions(row.access_keys),
    groups: parseDimensions(row.groups),
  }
}

export async function getCredentialLoad(
  client: ApiClient,
  signal: AbortSignal,
): Promise<CredentialLoadReport> {
  return readCredentialLoad(await client.request('/api/credentials/load', { signal }))
}
