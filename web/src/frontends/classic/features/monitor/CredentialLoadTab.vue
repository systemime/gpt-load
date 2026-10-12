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
import AppTooltip from '@/components/ui/AppTooltip.vue'
import QueryFeedback from '@/components/ui/QueryFeedback.vue'
import SegmentedControl, { type SegmentedControlOption } from '@/components/ui/SegmentedControl.vue'
import SkeletonSurface from '@/components/ui/SkeletonSurface.vue'
import { formatInteger } from '@/lib/format'

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

// 站点层汇总: 全部凭证在所选窗口的合计 + 实时准入计数。
const totals = computed(() => {
  const data = report.value
  if (!data) return null
  const stats = data.credentials.reduce(
    (acc, item) => {
      const w = item.windows[activeWindow.value]
      acc.requests += w.requests
      acc.success += w.success
      acc.affinityHits += w.affinity_hits
      acc.tokens += w.uncached_input_tokens + w.output_tokens
      acc.cacheRead += w.cache_read_tokens
      acc.inflight += item.inflight
      acc.queued += item.queued
      acc.errors5h += errorTotal(item.errors_5h)
      return acc
    },
    { requests: 0, success: 0, affinityHits: 0, tokens: 0, cacheRead: 0, inflight: 0, queued: 0, errors5h: 0 },
  )
  const capacity = data.cap > 0 ? data.cap * data.credentials.length : 0
  return { ...stats, capacity, rejects: data.local_rejects[activeWindow.value] }
})

// 用户密钥层: 按窗口请求降序, 条形相对最忙用户。
const accessRows = computed(() => {
  const data = report.value
  if (!data) return []
  const rows = data.access_keys.map((item) => ({ item, stats: item.windows[activeWindow.value] }))
  rows.sort((left, right) => right.stats.requests - left.stats.requests)
  return rows
})
const accessMax = computed(() => accessRows.value.reduce((max, row) => Math.max(max, row.stats.requests), 0))

const groupRows = computed(() => {
  const data = report.value
  if (!data) return []
  const rows = data.groups.map((item) => ({ item, stats: item.windows[activeWindow.value] }))
  rows.sort((left, right) => right.stats.requests - left.stats.requests)
  return rows
})

// 凭证层: 按窗口请求降序。
const rows = computed(() => {
  const data = report.value
  if (!data) return []
  return data.credentials
    .map((item) => ({ item, stats: item.windows[activeWindow.value] }))
    .sort((left, right) => right.stats.requests - left.stats.requests)
})
const credMax = computed(() => rows.value.reduce((max, row) => Math.max(max, row.stats.requests), 0))

const errorRows = computed(() => {
  const data = report.value
  if (!data) return []
  return data.credentials
    .map((item) => ({ item, errors: item.errors_5h }))
    .filter(({ errors }) => errorTotal(errors) > 0)
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

function displayName(item: { id: number; name: string }): string {
  return item.name !== '' ? item.name : `#${formatInteger(item.id, locale.value)}`
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

function barPercent(value: number, max: number): number {
  return max > 0 ? Math.min(100, Math.round((value / max) * 100)) : 0
}

function inflightTone(item: CredentialLoadItem): 'ok' | 'warn' | 'high' {
  const cap = report.value?.cap ?? 0
  if (cap <= 0 || item.inflight === 0) return 'ok'
  const load = item.inflight / cap
  if (load >= 0.9) return 'high'
  if (load >= 0.7) return 'warn'
  return 'ok'
}

function pressureTone(pressure: number): 'ok' | 'warn' | 'high' {
  if (pressure > 1.5) return 'high'
  if (pressure > 1.0) return 'warn'
  return 'ok'
}

// 每凭证独立额度水位(官方按席位限额): 5h 与周窗口各自对照套餐额度。
function quotaPercent(credits: number, quota: number): number {
  return quota > 0 ? Math.min(999, Math.round((credits / quota) * 100)) : 0
}

function quotaTone(ratio: number): 'ok' | 'warn' | 'high' {
  if (ratio >= 0.9) return 'high'
  if (ratio >= 0.7) return 'warn'
  return 'ok'
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
    <div class="credential-load__hint">
      <span class="credential-load__hint-title">{{ t('monitor.credentials.title') }}</span>
      <AppTooltip :content="t('monitor.credentials.pressureHelp')">
        <span class="credential-load__help-icon" aria-label="压力计算说明">?</span>
      </AppTooltip>
    </div>
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

      <!-- 站点层: 汇总卡片 -->
      <div v-if="totals" class="credential-load__cards">
        <div class="credential-load__card">
          <span class="credential-load__card-value">{{ formatTokens(totals.requests) }}</span>
          <span class="credential-load__card-label">{{ t('monitor.credentials.statRequests') }}</span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value">{{ ratio(totals.success, totals.requests) }}</span>
          <span class="credential-load__card-label">{{ t('monitor.credentials.statSuccessRate') }}</span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value">{{ formatTokens(totals.tokens) }}</span>
          <span class="credential-load__card-label">
            {{ t('monitor.credentials.statTokens') }} · {{ t('monitor.credentials.statCacheRead') }} {{ formatTokens(totals.cacheRead) }}
          </span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value">{{ ratio(totals.affinityHits, totals.requests) }}</span>
          <span class="credential-load__card-label">{{ t('monitor.credentials.statAffinity') }}</span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value">
            {{ formatInteger(totals.inflight, locale)
            }}<template v-if="totals.capacity > 0"> / {{ formatInteger(totals.capacity, locale) }}</template>
          </span>
          <span class="credential-load__card-label">
            <span class="credential-load__pulse" aria-hidden="true"></span>
            {{ t('monitor.credentials.statInflight') }}
          </span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value" :class="{ 'credential-load__danger': totals.queued > 0 }">
            {{ formatInteger(totals.queued, locale) }}
          </span>
          <span class="credential-load__card-label">
            <span class="credential-load__pulse" aria-hidden="true"></span>
            {{ t('monitor.credentials.statQueued') }}
          </span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value">{{ formatInteger(totals.rejects, locale) }}</span>
          <span class="credential-load__card-label">{{ t('monitor.credentials.statRejects') }}</span>
        </div>
        <div class="credential-load__card">
          <span class="credential-load__card-value" :class="{ 'credential-load__danger': totals.errors5h > 0 }">
            {{ formatInteger(totals.errors5h, locale) }}
          </span>
          <span class="credential-load__card-label">{{ t('monitor.credentials.statErrors') }}</span>
        </div>
      </div>

      <div class="credential-load__chain">
        <!-- 用户密钥层: 流量入口 -->
        <section class="credential-load__tier">
          <div class="credential-load__tier-head">
            <span class="credential-load__tier-badge">{{ t('monitor.credentials.tierAccessKeys') }}</span>
            <span class="credential-load__tier-agg">
              {{ formatInteger(accessRows.length, locale) }} ·
              {{ t('monitor.credentials.columns.requests') }} {{ formatTokens(totals?.requests ?? 0) }}
            </span>
          </div>
          <div
            v-if="accessRows.length > 0"
            class="credential-load__table credential-load__table--access"
            role="table"
          >
            <div class="credential-load__row credential-load__row--head" role="row">
              <span role="columnheader">{{ t('monitor.credentials.columns.person') }}</span>
              <span
                v-for="column in ['requests', 'successRate', 'affinity', 'tokens', 'cacheRead']"
                :key="column"
                role="columnheader"
                class="credential-load__number"
              >
                {{ t(`monitor.credentials.columns.${column}`) }}
              </span>
              <span role="columnheader">{{ t('monitor.credentials.columns.status') }}</span>
            </div>
            <div v-for="{ item, stats } in accessRows" :key="item.id" class="credential-load__row" role="row">
              <span role="cell" class="credential-load__name">{{ displayName(item) }}</span>
              <span role="cell" class="credential-load__number credential-load__req">
                <span class="credential-load__bar" aria-hidden="true">
                  <span class="credential-load__bar-fill" :style="{ width: `${barPercent(stats.requests, accessMax)}%` }"></span>
                </span>
                {{ formatInteger(stats.requests, locale) }}
              </span>
              <span role="cell" class="credential-load__number">{{ ratio(stats.success, stats.requests) }}</span>
              <span role="cell" class="credential-load__number">{{ ratio(stats.affinity_hits, stats.requests) }}</span>
              <span role="cell" class="credential-load__number">
                {{ formatTokens(stats.uncached_input_tokens + stats.output_tokens) }}
              </span>
              <span role="cell" class="credential-load__number">{{ formatTokens(stats.cache_read_tokens) }}</span>
              <span role="cell">
                <span
                  class="credential-load__chip"
                  :class="stats.requests > 0 ? 'credential-load__chip--ok' : 'credential-load__chip--idle'"
                >
                  {{ stats.requests > 0 ? t('monitor.credentials.keyActive') : t('monitor.credentials.keyIdle') }}
                </span>
              </span>
            </div>
          </div>
          <p v-else class="credential-load__empty">{{ t('monitor.credentials.empty') }}</p>
        </section>

        <div class="credential-load__connector" aria-hidden="true">
          <span class="credential-load__connector-arrow">↓</span>
          <span class="credential-load__connector-label">{{ t('monitor.credentials.routeHint') }}</span>
        </div>

        <!-- 分组层: 路由容器 -->
        <section class="credential-load__tier">
          <div class="credential-load__tier-head">
            <span class="credential-load__tier-badge">{{ t('monitor.credentials.tierGroups') }}</span>
            <span class="credential-load__tier-agg">
              {{ formatInteger(groupRows.length, locale) }} ·
              {{ t('monitor.credentials.columns.requests') }} {{ formatTokens(totals?.requests ?? 0) }}
            </span>
          </div>
          <div
            v-if="groupRows.length > 0"
            class="credential-load__table credential-load__table--groups"
            role="table"
          >
            <div class="credential-load__row credential-load__row--head" role="row">
              <span role="columnheader">{{ t('monitor.credentials.columns.group') }}</span>
              <span
                v-for="column in ['requests', 'successRate', 'tokens', 'cacheRead']"
                :key="column"
                role="columnheader"
                class="credential-load__number"
              >
                {{ t(`monitor.credentials.columns.${column}`) }}
              </span>
            </div>
            <div v-for="{ item, stats } in groupRows" :key="item.id" class="credential-load__row" role="row">
              <span role="cell" class="credential-load__name">{{ displayName(item) }}</span>
              <span role="cell" class="credential-load__number credential-load__req">
                <span class="credential-load__bar" aria-hidden="true">
                  <span
                    class="credential-load__bar-fill"
                    :style="{ width: `${barPercent(stats.requests, groupRows[0].stats.requests)}%` }"
                  ></span>
                </span>
                {{ formatInteger(stats.requests, locale) }}
              </span>
              <span role="cell" class="credential-load__number">{{ ratio(stats.success, stats.requests) }}</span>
              <span role="cell" class="credential-load__number">
                {{ formatTokens(stats.uncached_input_tokens + stats.output_tokens) }}
              </span>
              <span role="cell" class="credential-load__number">{{ formatTokens(stats.cache_read_tokens) }}</span>
            </div>
          </div>
          <p v-else class="credential-load__empty">{{ t('monitor.credentials.empty') }}</p>
        </section>

        <div class="credential-load__connector" aria-hidden="true">
          <span class="credential-load__connector-arrow">↓</span>
          <span class="credential-load__connector-label">{{ t('monitor.credentials.dispatchHint') }}</span>
        </div>

        <!-- 凭证层: 调度终点, 实时并发 + 窗口统计 + 压力 -->
        <section class="credential-load__tier">
          <div class="credential-load__tier-head">
            <span class="credential-load__tier-badge">{{ t('monitor.credentials.tierCredentials') }}</span>
            <span class="credential-load__tier-agg">
              {{ formatInteger(rows.length, locale) }} ·
              <template v-if="report.cap > 0">
                {{ t('monitor.credentials.statInflight') }}
                {{ formatInteger(totals?.inflight ?? 0, locale) }}/{{ formatInteger(totals?.capacity ?? 0, locale) }}
              </template>
              <template v-else>{{ t('monitor.credentials.unlimited') }}</template>
            </span>
          </div>
          <div v-if="rows.length > 0" class="credential-load__table credential-load__table--creds" role="table">
            <div class="credential-load__row credential-load__row--head" role="row">
              <span
                v-for="column in [
                  'credential',
                  'status',
                  'inflight',
                  'queued',
                  'pressure',
                  'requests',
                  'successRate',
                  'affinity',
                  'tokens',
                  'cacheRead',
                  'quota5h',
                  'quotaWeek',
                  'errors',
                ]"
                :key="column"
                role="columnheader"
                :class="{ 'credential-load__number': column !== 'credential' && column !== 'status' }"
              >
                {{ t(`monitor.credentials.columns.${column}`) }}
              </span>
            </div>
            <div v-for="{ item, stats } in rows" :key="item.id" class="credential-load__row" role="row">
              <span role="cell" class="credential-load__name">{{ displayName(item) }}</span>
              <span role="cell" :class="statusTone(item.status)">{{ item.status }}</span>
              <span role="cell" class="credential-load__number credential-load__req">
                <span class="credential-load__bar" aria-hidden="true">
                  <span
                    class="credential-load__bar-fill"
                    :class="`credential-load__bar-fill--${inflightTone(item)}`"
                    :style="{ width: `${barPercent(item.inflight, report.cap)}%` }"
                  ></span>
                </span>
                {{ formatInteger(item.inflight, locale)
                }}<template v-if="report.cap > 0"> / {{ formatInteger(report.cap, locale) }}</template>
              </span>
              <span
                role="cell"
                class="credential-load__number"
                :class="{ 'credential-load__queued': item.queued > 0 }"
              >
                {{ item.queued > 0 ? formatInteger(item.queued, locale) : '·' }}
              </span>
              <span role="cell" class="credential-load__number">
                <span
                  v-if="item.pressure > 0"
                  class="credential-load__chip"
                  :class="`credential-load__chip--${pressureTone(item.pressure)}`"
                >
                  {{ Math.round(item.pressure * 100) }}%
                </span>
                <template v-else>—</template>
              </span>
              <span role="cell" class="credential-load__number credential-load__req">
                <span class="credential-load__bar" aria-hidden="true">
                  <span
                    class="credential-load__bar-fill"
                    :style="{ width: `${barPercent(stats.requests, credMax)}%` }"
                  ></span>
                </span>
                {{ formatInteger(stats.requests, locale) }}
              </span>
              <span role="cell" class="credential-load__number">{{ ratio(stats.success, stats.requests) }}</span>
              <span role="cell" class="credential-load__number">
                {{ ratio(stats.affinity_hits, stats.requests) }}
              </span>
              <span role="cell" class="credential-load__number">
                {{ formatTokens(stats.uncached_input_tokens + stats.output_tokens) }}
              </span>
              <span role="cell" class="credential-load__number">{{ formatTokens(stats.cache_read_tokens) }}</span>
              <span
                role="cell"
                class="credential-load__number credential-load__req"
                :title="`${Math.round(item.credits_5h)} / ${report.plan.quota_credits_5h} ${t('monitor.credentials.creditsUnit')}`"
              >
                <span class="credential-load__bar" aria-hidden="true">
                  <span
                    class="credential-load__bar-fill"
                    :class="`credential-load__bar-fill--${quotaTone(item.credits_5h / (report.plan.quota_credits_5h || 1))}`"
                    :style="{ width: `${Math.min(100, quotaPercent(item.credits_5h, report.plan.quota_credits_5h))}%` }"
                  ></span>
                </span>
                {{ quotaPercent(item.credits_5h, report.plan.quota_credits_5h) }}%
              </span>
              <span
                role="cell"
                class="credential-load__number credential-load__req"
                :title="`${Math.round(item.credits_7d)} / ${report.plan.quota_credits_7d} ${t('monitor.credentials.creditsUnit')}`"
              >
                <span class="credential-load__bar" aria-hidden="true">
                  <span
                    class="credential-load__bar-fill"
                    :class="`credential-load__bar-fill--${quotaTone(item.credits_7d / (report.plan.quota_credits_7d || 1))}`"
                    :style="{ width: `${Math.min(100, quotaPercent(item.credits_7d, report.plan.quota_credits_7d))}%` }"
                  ></span>
                </span>
                {{ quotaPercent(item.credits_7d, report.plan.quota_credits_7d) }}%
              </span>
              <span
                role="cell"
                class="credential-load__number"
                :class="{ 'credential-load__danger': errorTotal(item.errors_5h) > 0 }"
              >
                {{ errorTotal(item.errors_5h) > 0 ? formatInteger(errorTotal(item.errors_5h), locale) : '·' }}
              </span>
            </div>
          </div>
          <p v-else class="credential-load__empty">{{ t('monitor.credentials.empty') }}</p>
        </section>
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
            <span
              v-for="column in errorColumns"
              :key="column.key"
              role="columnheader"
              class="credential-load__number"
            >
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

.credential-load__hint {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.credential-load__hint-title {
  font-size: var(--title-section);
  font-weight: 650;
}

.credential-load__help-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  border: 1px solid var(--color-border-subtle);
  color: var(--color-text-muted);
  font-size: 11px;
  cursor: help;
  user-select: none;
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

/* 站点层: 汇总卡片 */
.credential-load__cards {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--space-2);
}

@media (max-width: 900px) {
  .credential-load__cards {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.credential-load__card {
  display: grid;
  gap: 2px;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  background: var(--color-surface-muted, rgba(127, 127, 127, 0.04));
}

.credential-load__card-value {
  font-family: var(--font-mono);
  font-size: 1.1rem;
  font-weight: 700;
}

.credential-load__card-label {
  display: flex;
  align-items: center;
  gap: 5px;
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  white-space: nowrap;
}

.credential-load__pulse {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-status-success, var(--color-text));
  animation: credential-load-pulse 2s ease-in-out infinite;
}

@keyframes credential-load-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

/* 链路层 */
.credential-load__chain {
  display: grid;
  gap: 0;
}

.credential-load__tier {
  display: grid;
  gap: var(--space-2);
}

.credential-load__tier-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.credential-load__tier-badge {
  padding: 2px 10px;
  border-radius: 999px;
  border: 1px solid var(--color-border-subtle);
  background: var(--color-surface-muted, rgba(127, 127, 127, 0.06));
  font-size: var(--text-sm);
  font-weight: 650;
}

.credential-load__tier-agg {
  color: var(--color-text-muted);
  font-size: var(--text-sm);
  font-family: var(--font-mono);
}

.credential-load__connector {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: var(--space-1) 0;
}

.credential-load__connector-arrow {
  color: var(--color-text-muted);
  font-size: 0.9rem;
  line-height: 1;
}

.credential-load__connector-label {
  color: var(--color-text-muted);
  font-size: var(--text-xs);
}

/* 表格 */
.credential-load__table {
  display: grid;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  overflow-x: auto;
}

.credential-load__row {
  display: grid;
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

.credential-load__table--access .credential-load__row {
  grid-template-columns: minmax(88px, 1.2fr) minmax(120px, 1.5fr) repeat(4, minmax(56px, 0.8fr)) minmax(64px, 0.7fr);
}

.credential-load__table--groups .credential-load__row {
  grid-template-columns: minmax(88px, 1.4fr) minmax(120px, 1.5fr) repeat(3, minmax(56px, 0.8fr));
}

.credential-load__table--creds .credential-load__row {
  grid-template-columns: minmax(88px, 1.1fr) minmax(56px, 0.6fr) minmax(110px, 1.2fr) minmax(44px, 0.5fr) minmax(56px, 0.6fr) minmax(110px, 1.2fr) repeat(4, minmax(52px, 0.7fr)) minmax(88px, 1fr) minmax(88px, 1fr) minmax(44px, 0.5fr);
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

/* 数值 + 条形组合列: 条形贴左侧, 数值右对齐 */
.credential-load__req {
  display: flex;
  align-items: center;
  gap: 6px;
  justify-content: flex-end;
}

.credential-load__bar {
  flex: 1 1 32px;
  height: 6px;
  border-radius: 3px;
  background: var(--color-border-subtle);
  overflow: hidden;
}

.credential-load__bar-fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--color-primary, #3b82f6);
}

.credential-load__bar-fill--ok {
  background: var(--color-status-success, #22c55e);
}

.credential-load__bar-fill--warn {
  background: var(--color-status-warning, #eab308);
}

.credential-load__bar-fill--high {
  background: var(--color-status-danger, #ef4444);
}

.credential-load__chip {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: var(--text-xs);
  font-weight: 600;
  font-family: var(--font-mono);
}

.credential-load__chip--ok {
  color: var(--color-status-success, var(--color-text));
  background: rgba(34, 197, 94, 0.14);
}

.credential-load__chip--idle {
  color: var(--color-text-muted);
  background: var(--color-surface-muted, rgba(127, 127, 127, 0.12));
}

.credential-load__chip--warn {
  color: var(--color-status-warning, var(--color-text));
  background: rgba(234, 179, 8, 0.16);
}

.credential-load__chip--high {
  color: var(--color-status-danger, var(--color-text));
  background: rgba(239, 68, 68, 0.14);
}

.credential-load__status--active {
  color: var(--color-status-success, var(--color-text));
}

.credential-load__status--muted {
  color: var(--color-text-muted);
}

.credential-load__queued {
  color: var(--color-status-warning, var(--color-text-muted));
}

.credential-load__danger {
  color: var(--color-status-danger, inherit);
}

.credential-load__warn {
  color: var(--color-status-warning, inherit);
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
</style>
