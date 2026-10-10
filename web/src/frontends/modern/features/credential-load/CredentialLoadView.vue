<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@tanstack/vue-query'

import {
  credentialLoadWindowKeys,
  getCredentialLoad,
  type CredentialLoadErrors,
  type CredentialLoadItem,
  type CredentialLoadWindowKey,
} from '@modern/api/credential-load'
import { useApiClient } from '@shared/http/client-context'
import { usePageRefresh } from '@modern/app/page-refresh'
import { useMessageSource } from '@modern/app/messages'
import { AppButton, AppCollectionState, AppSegmentedControl } from '@modern/components/ui'

const BAR_WIDTH = 10

const client = useApiClient()
const { t } = useI18n()
const query = useQuery({
  queryKey: ['modern', 'credential-load'],
  queryFn: ({ signal }) => getCredentialLoad(client, signal),
})
const report = computed(() => query.data.value)
const failed = computed(() => query.isError.value && report.value === undefined)

usePageRefresh({
  refresh: refreshReport,
  pending: () => query.isFetching.value,
  updatedAt: () => report.value?.observedAt,
})
useMessageSource(() =>
  query.isError.value && report.value
    ? { tone: 'warning', text: t('credentialLoad.failed'), action: { label: t('ui.retry'), run: refreshReport } }
    : undefined,
)

function refreshReport(): Promise<void> {
  return query.refetch().then(() => undefined)
}

const activeWindow = ref<CredentialLoadWindowKey>('1h')
const windowOptions = computed(() =>
  credentialLoadWindowKeys.map((key) => ({
    value: key,
    label: t(`credentialLoad.windows.${key}`),
  })),
)

const rows = computed(() => {
  const data = report.value
  if (!data) return []
  const total = data.credentials.reduce(
    (sum, item) => sum + item.windows[activeWindow.value].requests,
    0,
  )
  return data.credentials
    .map((item) => ({ item, stats: item.windows[activeWindow.value], total }))
    .sort((left, right) => right.stats.requests - left.stats.requests)
})

const errorRows = computed(() => {
  const data = report.value
  if (!data) return []
  return data.credentials
    .map((item) => ({ item, errors: item.errors5h }))
    .filter(({ errors }) => errorTotal(errors) > 0)
    .sort((left, right) => errorTotal(right.errors) - errorTotal(left.errors))
})

const errorColumns: Array<{ key: keyof CredentialLoadErrors; label: string }> = [
  { key: 'rateLimited', label: 'rateLimited' },
  { key: 'connect', label: 'connect' },
  { key: 'timeout', label: 'timeout' },
  { key: 'upstream', label: 'upstream' },
  { key: 'canceled', label: 'canceled' },
  { key: 'scheduled', label: 'scheduled' },
  { key: 'other', label: 'other' },
]

function errorTotal(errors: CredentialLoadErrors): number {
  return (
    errors.rateLimited +
    errors.connect +
    errors.timeout +
    errors.upstream +
    errors.canceled +
    errors.scheduled +
    errors.other
  )
}

function displayName(item: CredentialLoadItem): string {
  return item.name !== '' ? item.name : `#${item.id}`
}

function shareBar(requests: number, total: number): string {
  const filled = total > 0 ? Math.round((requests / total) * BAR_WIDTH) : 0
  return '█'.repeat(filled) + '·'.repeat(BAR_WIDTH - filled)
}

function ratio(part: number, whole: number): string {
  return whole > 0 ? `${Math.round((part / whole) * 100)}%` : '—'
}

function formatTokens(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}G`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${Math.round(value / 1_000)}K`
  return String(value)
}
</script>

<template>
  <div class="modern-credential-load">
    <AppCollectionState
      v-if="!report && !failed"
      :title="t('credentialLoad.loading')"
      loading
    />
    <AppCollectionState v-else-if="failed" :title="t('credentialLoad.failed')" error
      ><AppButton @click="refreshReport">{{ t('ui.retry') }}</AppButton></AppCollectionState
    >
    <template v-else-if="report">
      <header class="modern-credential-load__header">
        <AppSegmentedControl
          :model-value="activeWindow"
          :options="windowOptions"
          :label="t('credentialLoad.windowLabel')"
          @update:model-value="activeWindow = $event as CredentialLoadWindowKey"
        />
        <p class="modern-credential-load__meta">
          {{
            t('credentialLoad.summary', {
              count: report.credentials.length,
              cap: report.cap > 0 ? report.cap : t('credentialLoad.unlimited'),
              rejects: report.localRejects[activeWindow],
            })
          }}
        </p>
      </header>

      <div class="modern-credential-load__table" role="table">
        <div class="modern-credential-load__row modern-credential-load__row--head" role="row">
          <span v-for="column in (['credential','status','inflight','requests','share','successRate','affinity','tokens','cacheRead','pressure'] as const)" :key="column" role="columnheader">
            {{ t(`credentialLoad.columns.${column}`) }}
          </span>
        </div>
        <div v-for="{ item, stats, total } in rows" :key="item.id" class="modern-credential-load__row" role="row">
          <span role="cell" class="modern-credential-load__name">{{ displayName(item) }}</span>
          <span role="cell" :class="item.status === 'active' ? 'modern-credential-load__ok' : 'modern-credential-load__muted'">
            {{ item.status }}
          </span>
          <span role="cell" class="modern-credential-load__num">
            {{ item.inflight }}<template v-if="report.cap > 0"> / {{ report.cap }}</template>
            <template v-if="item.queued > 0">
              <span class="modern-credential-load__queued">
                {{ t('credentialLoad.queued', { count: item.queued }) }}
              </span>
            </template>
          </span>
          <span role="cell" class="modern-credential-load__num">{{ stats.requests }}</span>
          <span role="cell" class="modern-credential-load__share">
            <span aria-hidden="true">{{ shareBar(stats.requests, total) }}</span>
            {{ ratio(stats.requests, total) }}
          </span>
          <span role="cell" class="modern-credential-load__num">{{ ratio(stats.success, stats.requests) }}</span>
          <span role="cell" class="modern-credential-load__num">{{ ratio(stats.affinityHits, stats.requests) }}</span>
          <span role="cell" class="modern-credential-load__num">
            {{ formatTokens(stats.uncachedInputTokens + stats.outputTokens) }}
          </span>
          <span role="cell" class="modern-credential-load__num">{{ formatTokens(stats.cacheReadTokens) }}</span>
          <span role="cell" class="modern-credential-load__num">
            {{ item.pressure > 0 ? `${Math.round(item.pressure * 100)}%` : '—' }}
          </span>
        </div>
        <p v-if="rows.length === 0" class="modern-credential-load__empty">
          {{ t('credentialLoad.empty') }}
        </p>
      </div>

      <section class="modern-credential-load__errors">
        <h3>{{ t('credentialLoad.errorsTitle') }}</h3>
        <div v-if="errorRows.length > 0" class="modern-credential-load__table modern-credential-load__table--errors" role="table">
          <div class="modern-credential-load__row modern-credential-load__row--head" role="row">
            <span role="columnheader">{{ t('credentialLoad.columns.credential') }}</span>
            <span v-for="column in errorColumns" :key="column.key" role="columnheader">
              {{ t(`credentialLoad.errors.${column.label}`) }}
            </span>
          </div>
          <div v-for="{ item, errors } in errorRows" :key="item.id" class="modern-credential-load__row" role="row">
            <span role="cell" class="modern-credential-load__name">{{ displayName(item) }}</span>
            <span
              v-for="column in errorColumns"
              :key="column.key"
              role="cell"
              class="modern-credential-load__num"
              :class="{ 'modern-credential-load__danger': errors[column.key] > 0 }"
            >
              {{ errors[column.key] > 0 ? errors[column.key] : '·' }}
            </span>
          </div>
        </div>
        <p v-else class="modern-credential-load__empty">{{ t('credentialLoad.noErrors') }}</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.modern-credential-load {
  display: grid;
  gap: var(--modern-space-4, 16px);
}

.modern-credential-load__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--modern-space-2, 8px);
}

.modern-credential-load__meta {
  margin: 0;
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.875rem;
}

.modern-credential-load__table {
  display: grid;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  border-radius: 8px;
  overflow-x: auto;
}

.modern-credential-load__row {
  display: grid;
  grid-template-columns: minmax(96px, 1.2fr) 0.7fr 0.8fr 0.7fr 1.4fr 0.8fr 0.8fr 0.9fr 0.9fr 0.8fr;
  gap: 8px;
  align-items: center;
  padding: 8px 12px;
  border-bottom: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  font-size: 0.875rem;
}

.modern-credential-load__row:last-child {
  border-bottom: 0;
}

.modern-credential-load__row--head {
  color: var(--modern-color-text-muted, inherit);
  font-weight: 600;
}

.modern-credential-load__table--errors .modern-credential-load__row {
  grid-template-columns: minmax(96px, 1.4fr) repeat(7, minmax(48px, 0.6fr));
}

.modern-credential-load__name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.modern-credential-load__num {
  font-variant-numeric: tabular-nums;
  text-align: right;
  white-space: nowrap;
}

.modern-credential-load__share {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.modern-credential-load__ok {
  color: var(--modern-color-success, inherit);
}

.modern-credential-load__muted {
  color: var(--modern-color-text-muted, inherit);
}

.modern-credential-load__queued {
  color: var(--modern-color-warning, inherit);
  margin-left: 4px;
}

.modern-credential-load__danger {
  color: var(--modern-color-danger, inherit);
}

.modern-credential-load__empty {
  margin: 0;
  padding: 12px;
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.875rem;
}

.modern-credential-load__errors {
  display: grid;
  gap: 8px;
}

.modern-credential-load__errors h3 {
  margin: 0;
  font-size: 1rem;
  font-weight: 650;
}
</style>
