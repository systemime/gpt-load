import { queryOptions } from '@tanstack/vue-query'

import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { controlQueryKeys } from '@/app/query-keys'

import { assertNoSecretLikeFields, projectArray, projectFiniteNumber, projectRecord, projectSafeInteger, projectString } from './projector'

export type CredentialLoadWindowKey = '1h' | '5h' | '24h'
export const credentialLoadWindowKeys: readonly CredentialLoadWindowKey[] = ['1h', '5h', '24h']

export interface CredentialLoadWindow {
  requests: number
  success: number
  affinity_hits: number
  uncached_input_tokens: number
  output_tokens: number
  cache_read_tokens: number
}

export interface CredentialLoadErrors {
  rate_limited: number
  connect: number
  timeout: number
  upstream: number
  canceled: number
  scheduled: number
  other: number
}

export interface CredentialLoadUsage {
  requests_5h: number
  tokens_5h: number
  requests_7d: number
  tokens_7d: number
}

export interface CredentialLoadItem {
  id: number
  name: string
  status: string
  // runtime_state 才是可调度口径(available/cooldown/blacklisted/disabled),
  // 与调度器一致; status 只是配置面开关。
  runtime_state: string
  inflight: number
  queued: number
  usage: CredentialLoadUsage
  pressure: number
  credits_5h: number
  credits_7d: number
  windows: Record<CredentialLoadWindowKey, CredentialLoadWindow>
  errors_5h: CredentialLoadErrors
}

export interface CredentialLoadPlan {
  plan: string
  quota_credits_5h: number
  quota_credits_7d: number
  team_credits_5h: number
  team_credits_7d: number
}

export interface CredentialLoadDimension {
  id: number
  name: string
  windows: Record<CredentialLoadWindowKey, CredentialLoadWindow>
}

export interface CredentialLoadHistoryPoint {
  at_ms: number
  inflight: number
  queued: number
  silent_success: number
  silent_failed: number
}

export interface CredentialLoadReport {
  observed_at_ms: number
  cap: number
  plan: CredentialLoadPlan
  credentials: CredentialLoadItem[]
  local_rejects: Record<CredentialLoadWindowKey, number>
  access_keys: CredentialLoadDimension[]
  groups: CredentialLoadDimension[]
  // 服务端 1s 采样 × 5 分钟滚动窗, 折线图数据不依赖页面轮询。
  history: CredentialLoadHistoryPoint[]
}

const windowFields = [
  'requests',
  'success',
  'affinity_hits',
  'uncached_input_tokens',
  'output_tokens',
  'cache_read_tokens',
] as const
const errorFields = [
  'rate_limited',
  'connect',
  'timeout',
  'upstream',
  'canceled',
  'scheduled',
  'other',
] as const
const itemFields = ['id', 'name', 'status', 'inflight', 'queued', 'usage', 'pressure', 'credits_5h', 'credits_7d', 'windows', 'errors_5h'] as const
const planFields = ['plan', 'quota_credits_5h', 'quota_credits_7d', 'team_credits_5h', 'team_credits_7d'] as const
const reportFields = ['observed_at_ms', 'cap', 'plan', 'quota_credits_5h', 'quota_credits_7d', 'team_credits_5h', 'team_credits_7d', 'credentials', 'local_rejects', 'access_keys', 'groups', 'history'] as const

function invalidResponse(): never {
  throw new InvalidResponseError()
}

function projectCount(value: unknown): number {
  return projectSafeInteger(value, { minimum: 0 })
}

function projectWindow(value: unknown): CredentialLoadWindow {
  const record = projectRecord(value)
  assertNoSecretLikeFields(record, windowFields)
  return {
    requests: projectCount(record.requests),
    success: projectCount(record.success),
    affinity_hits: projectCount(record.affinity_hits),
    uncached_input_tokens: projectCount(record.uncached_input_tokens),
    output_tokens: projectCount(record.output_tokens),
    cache_read_tokens: projectCount(record.cache_read_tokens),
  }
}

function projectErrors(value: unknown): CredentialLoadErrors {
  const record = projectRecord(value)
  assertNoSecretLikeFields(record, errorFields)
  return {
    rate_limited: projectCount(record.rate_limited),
    connect: projectCount(record.connect),
    timeout: projectCount(record.timeout),
    upstream: projectCount(record.upstream),
    canceled: projectCount(record.canceled),
    scheduled: projectCount(record.scheduled),
    other: projectCount(record.other),
  }
}

function projectItem(value: unknown): CredentialLoadItem {
  const record = projectRecord(value)
  assertNoSecretLikeFields(record, itemFields)
  const windows = projectRecord(record.windows)
  const mapped = {} as Record<CredentialLoadWindowKey, CredentialLoadWindow>
  for (const key of credentialLoadWindowKeys) {
    if (windows[key] === undefined) invalidResponse()
    mapped[key] = projectWindow(windows[key])
  }
  return {
    id: projectSafeInteger(record.id, { minimum: 1 }),
    name: projectString(record.name, { allowEmpty: true }),
    status: projectString(record.status),
    runtime_state: projectString(record.runtime_state, { allowEmpty: true }) || 'active',
    inflight: projectCount(record.inflight),
    queued: projectCount(record.queued),
    usage: ((): CredentialLoadUsage => {
      const usageRecord = projectRecord(record.usage)
      return {
        requests_5h: projectCount(usageRecord.requests_5h),
        tokens_5h: projectCount(usageRecord.tokens_5h),
        requests_7d: projectCount(usageRecord.requests_7d),
        tokens_7d: projectCount(usageRecord.tokens_7d),
      }
    })(),
    pressure:
      typeof record.pressure === 'number' && Number.isFinite(record.pressure) && record.pressure >= 0
        ? record.pressure
        : 0,
    credits_5h: projectFiniteNumber(record.credits_5h, { minimum: 0 }),
    credits_7d: projectFiniteNumber(record.credits_7d, { minimum: 0 }),
    windows: mapped,
    errors_5h: projectErrors(record.errors_5h),
  }
}

function projectPlan(value: unknown): CredentialLoadPlan {
  const row = projectRecord(value)
  assertNoSecretLikeFields(row, planFields)
  return {
    plan: projectString(row.plan, { allowEmpty: true }),
    quota_credits_5h: projectFiniteNumber(row.quota_credits_5h, { minimum: 0 }),
    quota_credits_7d: projectFiniteNumber(row.quota_credits_7d, { minimum: 0 }),
    team_credits_5h: projectFiniteNumber(row.team_credits_5h, { minimum: 0 }),
    team_credits_7d: projectFiniteNumber(row.team_credits_7d, { minimum: 0 }),
  }
}

export function projectCredentialLoad(value: unknown): CredentialLoadReport {
  const record = projectRecord(value)
  assertNoSecretLikeFields(record, reportFields)
  const rejects = projectRecord(record.local_rejects)
  const mapped = {} as Record<CredentialLoadWindowKey, number>
  for (const key of credentialLoadWindowKeys) {
    mapped[key] = projectCount(rejects[key])
  }
  const readDimension = (value: unknown): CredentialLoadDimension => {
    const row = projectRecord(value)
    return {
      id: projectSafeInteger(row.id, { minimum: 1 }),
      name: projectString(row.name, { allowEmpty: true }),
      windows: (() => {
        const windows = projectRecord(row.windows)
        const mapped = {} as Record<CredentialLoadWindowKey, CredentialLoadWindow>
        for (const key of credentialLoadWindowKeys) {
          if (windows[key] === undefined) invalidResponse()
          mapped[key] = projectWindow(windows[key])
        }
        return mapped
      })(),
    }
  }
  const accessKeys = projectArray(record.access_keys, readDimension)
  const groups = projectArray(record.groups, readDimension)
  const history = projectArray(record.history ?? [], (value) => {
    const row = projectRecord(value)
    return {
      at_ms: projectSafeInteger(row.at_ms, { minimum: 0 }),
      inflight: projectSafeInteger(row.inflight, { minimum: 0 }),
      queued: projectSafeInteger(row.queued, { minimum: 0 }),
      silent_success: projectSafeInteger(row.silent_success, { minimum: 0 }),
      silent_failed: projectSafeInteger(row.silent_failed, { minimum: 0 }),
    }
  })
  return {
    observed_at_ms: projectSafeInteger(record.observed_at_ms, { minimum: 0 }),
    cap: projectSafeInteger(record.cap, { minimum: 0 }),
    plan: projectPlan(record.plan),
    credentials: projectArray(record.credentials, projectItem),
    local_rejects: mapped,
    access_keys: accessKeys,
    groups: groups,
    history,
  }
}

export async function getCredentialLoad(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<CredentialLoadReport> {
  return projectCredentialLoad(await client.request('/api/credentials/load', { method: 'GET', signal }))
}

export function credentialLoadQueryOptions(client: ApiClient, intervalMs = 10_000) {
  return queryOptions({
    queryKey: controlQueryKeys.credentialLoad(),
    queryFn: ({ signal }) => getCredentialLoad(client, signal),
    refetchInterval: intervalMs,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
  })
}
