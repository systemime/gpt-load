<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useApiClient } from '@shared/http/client-context'
import { useStableLoading } from '@/app/loading-state'
import {
  credentialLoadQueryOptions,
  credentialLoadWindowKeys,
  type CredentialLoadItem,
  type CredentialLoadWindowKey,
} from '@/app/resources/credential-load'
import QueryFeedback from '@/components/ui/QueryFeedback.vue'
import SegmentedControl, { type SegmentedControlOption } from '@/components/ui/SegmentedControl.vue'
import SkeletonSurface from '@/components/ui/SkeletonSurface.vue'
import { formatInteger } from '@/lib/format'

const BAR_WIDTH = 10

const client = useApiClient()
const { locale, t } = useI18n()
const query = useQuery(credentialLoadQueryOptions(client))
const initialLoading = useStableLoading(() => query.isPending.value)

const activeWindow = ref<CredentialLoadWindowKey>('1h')
const windowOptions = computed<SegmentedControlOption[]>(() =>
  credentialLoadWindowKeys.map((key) => ({
    value: key,
    label: t(`monitor.credentials.windows.${key}`),
  })),
)

const report = computed(() => query.data.value)
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
    .map((item) => ({ item, errors: item.errors_5h }))
    .filter(({ errors }) =>
      [errors.rate_limited, errors.connect, errors.timeout, errors.upstream, errors.canceled, errors.scheduled, errors.other]
        .some((count) => count > 0),
    )
    .sort((left, right) => errorTotal(right.errors) - errorTotal(left.errors))
})

function errorTotal(errors: CredentialLoadItem['errors_5h']): number {
  return (
    errors.rate_limited +
    errors.connect +
    errors.timeout +
    errors.upstream +
    errors.canceled +
    errors.scheduled +
    errors.other
  )
}

function displayName(item: CredentialLoadItem): string {
  return item.name !== '' ? item.name : `#${formatInteger(item.id, locale.value)}`
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
  return formatInteger(value, locale.value)
}

function statusTone(status: string): string {
  return status === 'active' ? 'credential-load__status--active' : 'credential-load__status--muted'
}

const errorColumns: Array<{ key: keyof CredentialLoadItem['errors_5h']; label: string }> = [
  { key: 'rate_limited', label: 'rateLimited' },
  { key: 'connect', label: 'connect' },
  { key: 'timeout', label: 'timeout' },
  { key: 'upstream', label: 'upstream' },
  { key: 'canceled', label: 'canceled' },
  { key: 'scheduled', label: 'scheduled' },
  { key: 'other', label: 'other' },
]
</script>

<template>
  <div class="credential-load">
    <QueryFeedback
      v-if="initialLoading || (query.isError.value && report === undefined)"
      :state="initialLoading ? 'loading' : 'error'"
      :message="initialLoading ? t('monitor.credentials.loading') : t('monitor.credentials.loadError')"
      :retry-label="t('monitor.credentials.retry')"
      @retry="query.refetch()"
    />
    <SkeletonSurface
      v-if="initialLoading"
      class="credential-load__skeleton"
      :label="t('monitor.credentials.loading')"
    />
    <template v-else-if="report">
      <header class="credential-load__header">
        <SegmentedControl
          :model-value="activeWindow"
          :options="windowOptions"
          :label="t('monitor.credentials.windowLabel')"
          size="compact"
          @update:model-value="activeWindow = $event as CredentialLoadWindowKey"
        />
        <p class="credential-load__meta">
          {{
            t('monitor.credentials.summary', {
              count: formatInteger(report.credentials.length, locale),
              cap: report.cap > 0 ? formatInteger(report.cap, locale) : t('monitor.credentials.unlimited'),
              rejects: formatInteger(report.local_rejects[activeWindow], locale),
            })
          }}
        </p>
      </header>

      <div class="credential-load__table" role="table" :aria-label="t('monitor.credentials.table')">
        <div class="credential-load__row credential-load__row--head" role="row">
          <span
            v-for="column in [
              'credential',
              'status',
              'inflight',
              'requests',
              'share',
              'successRate',
              'affinity',
              'tokens',
              'cacheRead',
              'pressure',
            ]"
            :key="column"
            role="columnheader"
          >
            {{ t(`monitor.credentials.columns.${column}`) }}
          </span>
        </div>
        <div
          v-for="{ item, stats, total } in rows"
          :key="item.id"
          class="credential-load__row"
          role="row"
        >
          <span role="cell" class="credential-load__name">{{ displayName(item) }}</span>
          <span role="cell" :class="statusTone(item.status)">{{ item.status }}</span>
          <span role="cell" class="credential-load__number">
            {{ formatInteger(item.inflight, locale)
            }}<template v-if="report.cap > 0"> / {{ formatInteger(report.cap, locale) }}</template>
          </span>
          <span role="cell" class="credential-load__number">
            {{ formatInteger(stats.requests, locale) }}
          </span>
          <span role="cell" class="credential-load__share">
            <span aria-hidden="true">{{ shareBar(stats.requests, total) }}</span>
            {{ ratio(stats.requests, total) }}
          </span>
          <span role="cell" class="credential-load__number">{{ ratio(stats.success, stats.requests) }}</span>
          <span role="cell" class="credential-load__number">
            {{ ratio(stats.affinity_hits, stats.requests) }}
          </span>
          <span role="cell" class="credential-load__number">
            {{ formatTokens(stats.uncached_input_tokens + stats.output_tokens) }}
          </span>
          <span role="cell" class="credential-load__number">{{ formatTokens(stats.cache_read_tokens) }}</span>
          <span role="cell" class="credential-load__number">
            {{ item.pressure > 0 ? `${Math.round(item.pressure * 100)}%` : '—' }}
          </span>
        </div>
        <p v-if="rows.length === 0" class="credential-load__empty">
          {{ t('monitor.credentials.empty') }}
        </p>
      </div>

      <section class="credential-load__errors" :aria-label="t('monitor.credentials.errorsTitle')">
        <h3>{{ t('monitor.credentials.errorsTitle') }}</h3>
        <div
          v-if="errorRows.length > 0"
          class="credential-load__table credential-load__table--errors"
          role="table"
        >
          <div class="credential-load__row credential-load__row--head" role="row">
            <span role="columnheader">{{ t('monitor.credentials.columns.credential') }}</span>
            <span v-for="column in errorColumns" :key="column.key" role="columnheader">
              {{ t(`monitor.credentials.errors.${column.label}`) }}
            </span>
          </div>
          <div v-for="{ item, errors } in errorRows" :key="item.id" class="credential-load__row" role="row">
            <span role="cell" class="credential-load__name">{{ displayName(item) }}</span>
            <span
              v-for="column in errorColumns"
              :key="column.key"
              role="cell"
              class="credential-load__number"
              :class="{ 'credential-load__danger': errors[column.key] > 0 }"
            >
              {{ errors[column.key] > 0 ? formatInteger(errors[column.key], locale) : '·' }}
            </span>
          </div>
        </div>
        <p v-else class="credential-load__empty">{{ t('monitor.credentials.noErrors') }}</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.credential-load {
  display: grid;
  gap: var(--space-4);
}

.credential-load__skeleton {
  height: 320px;
}

.credential-load__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.credential-load__meta {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}

.credential-load__table {
  display: grid;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  overflow-x: auto;
}

.credential-load__row {
  display: grid;
  grid-template-columns: minmax(96px, 1.2fr) 0.7fr 0.8fr 0.7fr 1.4fr 0.8fr 0.8fr 0.9fr 0.9fr 0.8fr;
  gap: var(--space-2);
  align-items: center;
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border-subtle);
  font-size: var(--text-sm);
}

.credential-load__row:last-child {
  border-bottom: 0;
}

.credential-load__row--head {
  color: var(--color-text-muted);
  background: var(--color-surface-muted, rgba(127, 127, 127, 0.06));
  font-weight: 600;
}

.credential-load__table--errors .credential-load__row {
  grid-template-columns: minmax(96px, 1.4fr) repeat(7, minmax(48px, 0.6fr));
}

.credential-load__name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.credential-load__number {
  font-family: var(--font-mono);
  text-align: right;
  white-space: nowrap;
}

.credential-load__share {
  font-family: var(--font-mono);
  white-space: nowrap;
}

.credential-load__status--active {
  color: var(--color-status-success, var(--color-text));
}

.credential-load__status--muted {
  color: var(--color-text-muted);
}

.credential-load__queued {
  color: var(--color-status-warning, var(--color-text-muted));
  margin-left: 4px;
}

.credential-load__danger {
  color: var(--color-status-danger, inherit);
}

.credential-load__empty {
  margin: 0;
  padding: var(--space-3);
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}

.credential-load__errors {
  display: grid;
  gap: var(--space-2);
}

.credential-load__errors h3 {
  margin: 0;
  font-size: var(--title-section);
  font-weight: 650;
}

@media (max-width: 800px) {
  .credential-load__row {
    grid-template-columns: repeat(4, minmax(64px, 1fr));
  }
}
</style>
