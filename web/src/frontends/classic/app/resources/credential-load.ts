import { queryOptions } from '@tanstack/vue-query'

import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { controlQueryKeys } from '@/app/query-keys'

import { assertNoSecretLikeFields, projectArray, projectRecord, projectSafeInteger, projectString } from './projector'

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

export interface CredentialLoadItem {
  id: number
  name: string
  status: string
  inflight: number
  queued: number
  windows: Record<CredentialLoadWindowKey, CredentialLoadWindow>
  errors_5h: CredentialLoadErrors
}

export interface CredentialLoadReport {
  observed_at_ms: number
  cap: number
  credentials: CredentialLoadItem[]
  local_rejects: Record<CredentialLoadWindowKey, number>
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
const itemFields = ['id', 'name', 'status', 'inflight', 'queued', 'windows', 'errors_5h'] as const
const reportFields = ['observed_at_ms', 'cap', 'credentials', 'local_rejects'] as const

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
    inflight: projectCount(record.inflight),
    queued: projectCount(record.queued),
    windows: mapped,
    errors_5h: projectErrors(record.errors_5h),
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
  return {
    observed_at_ms: projectSafeInteger(record.observed_at_ms, { minimum: 0 }),
    cap: projectSafeInteger(record.cap, { minimum: 0 }),
    credentials: projectArray(record.credentials, projectItem),
    local_rejects: mapped,
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
