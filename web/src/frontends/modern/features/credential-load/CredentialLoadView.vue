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
import { AppButton, AppCollectionState, AppSegmentedControl, AppTooltip } from '@modern/components/ui'

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

function formatTokens(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}G`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${Math.round(value / 1_000)}K`
  return String(value)
}
const windowOptions = computed(() =>
  credentialLoadWindowKeys.map((key) => ({
    value: key,
    label: t(`credentialLoad.windows.${key}`),
  })),
)

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

function displayName(item: { id: number; name: string }): string {
  return item.name !== '' ? item.name : `#${item.id}`
}

function ratio(part: number, whole: number): string {
  return whole > 0 ? `${Math.round((part / whole) * 100)}%` : '—'
}

// 站点层汇总: 全部凭证在所选窗口的合计 + 实时准入计数。
const totals = computed(() => {
  const data = report.value
  if (!data) return null
  const stats = data.credentials.reduce(
    (acc, item) => {
      const w = item.windows[activeWindow.value]
      acc.requests += w.requests
      acc.success += w.success
      acc.affinityHits += w.affinityHits
      acc.tokens += w.uncachedInputTokens + w.outputTokens
      acc.cacheRead += w.cacheReadTokens
      acc.inflight += item.inflight
      acc.queued += item.queued
      acc.errors5h += errorTotal(item.errors5h)
      return acc
    },
    { requests: 0, success: 0, affinityHits: 0, tokens: 0, cacheRead: 0, inflight: 0, queued: 0, errors5h: 0 },
  )
  // 容量只统计可调度凭证; 停用/冷却/隔离期间同样不参与准入, 计入会虚增总容量。
  const capacity =
    data.cap > 0
      ? data.cap * data.credentials.filter((item) => item.runtimeState === 'available').length
      : 0
  return { ...stats, capacity, rejects: data.localRejects[activeWindow.value] }
})

// 用户密钥层: 按窗口请求降序, 份额相对最忙用户(条形可读性优于绝对份额)。
const accessRows = computed(() => {
  const data = report.value
  if (!data) return []
  const rows = data.accessKeys.map((item) => ({ item, stats: item.windows[activeWindow.value] }))
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

// 凭证层: 沿用 totals 同窗口, 条形相对最忙凭证。
const credRows = computed(() => {
  const data = report.value
  if (!data) return []
  const rows = data.credentials.map((item) => ({ item, stats: item.windows[activeWindow.value] }))
  rows.sort((left, right) => right.stats.requests - left.stats.requests)
  return rows
})
const credMax = computed(() => credRows.value.reduce((max, row) => Math.max(max, row.stats.requests), 0))

const errorRows = computed(() => {
  const data = report.value
  if (!data) return []
  return data.credentials
    .map((item) => ({ item, errors: item.errors5h }))
    .filter(({ errors }) => errorTotal(errors) > 0)
    .sort((left, right) => errorTotal(right.errors) - errorTotal(left.errors))
})

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
        <h2 class="modern-credential-load__title">
          {{ t('credentialLoad.title') }}
          <AppTooltip :label="t('credentialLoad.pressureHelp')" side="right">
            <span class="modern-credential-load__help" aria-hidden="true">?</span>
          </AppTooltip>
        </h2>
        <AppSegmentedControl
          :model-value="activeWindow"
          :options="windowOptions"
          :label="t('credentialLoad.windowLabel')"
          @update:model-value="activeWindow = $event as CredentialLoadWindowKey"
        />
      </header>

      <!-- 站点层: 汇总卡片 -->
      <div v-if="totals" class="modern-credential-load__cards">
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value">{{ formatTokens(totals.requests) }}</span>
          <span class="modern-credential-load__card-label">{{ t('credentialLoad.statRequests') }}</span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value">{{ ratio(totals.success, totals.requests) }}</span>
          <span class="modern-credential-load__card-label">{{ t('credentialLoad.statSuccessRate') }}</span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value">{{ formatTokens(totals.tokens) }}</span>
          <span class="modern-credential-load__card-label">
            {{ t('credentialLoad.statTokens') }} · {{ t('credentialLoad.statCacheRead') }} {{ formatTokens(totals.cacheRead) }}
          </span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value">{{ ratio(totals.affinityHits, totals.requests) }}</span>
          <span class="modern-credential-load__card-label">{{ t('credentialLoad.statAffinity') }}</span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value">
            {{ totals.inflight }}<template v-if="totals.capacity > 0"> / {{ totals.capacity }}</template>
          </span>
          <span class="modern-credential-load__card-label">
            <span class="modern-credential-load__pulse" aria-hidden="true"></span>
            {{ t('credentialLoad.statInflight') }}
          </span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value" :class="{ 'modern-credential-load__danger': totals.queued > 0 }">
            {{ totals.queued }}
          </span>
          <span class="modern-credential-load__card-label">
            <span class="modern-credential-load__pulse" aria-hidden="true"></span>
            {{ t('credentialLoad.statQueued') }}
          </span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value">{{ totals.rejects }}</span>
          <span class="modern-credential-load__card-label">{{ t('credentialLoad.statRejects') }}</span>
        </div>
        <div class="modern-credential-load__card">
          <span class="modern-credential-load__card-value" :class="{ 'modern-credential-load__danger': totals.errors5h > 0 }">
            {{ totals.errors5h }}
          </span>
          <span class="modern-credential-load__card-label">{{ t('credentialLoad.statErrors') }}</span>
        </div>
      </div>

      <div class="modern-credential-load__chain">
        <!-- 用户密钥层: 流量入口 -->
        <section class="modern-credential-load__tier">
          <div class="modern-credential-load__tier-head">
            <span class="modern-credential-load__tier-badge">{{ t('credentialLoad.tierAccessKeys') }}</span>
            <span class="modern-credential-load__tier-agg">
              {{ accessRows.length }} · {{ t('credentialLoad.columns.requests') }} {{ formatTokens(totals?.requests ?? 0) }}
            </span>
          </div>
          <div
            v-if="accessRows.length > 0"
            class="modern-credential-load__table modern-credential-load__table--access"
            role="table"
          >
            <div class="modern-credential-load__row modern-credential-load__row--head" role="row">
              <span role="columnheader">{{ t('credentialLoad.person') }}</span>
              <span
                v-for="column in (['requests','successRate','affinity','tokens','cacheRead'] as const)"
                :key="column"
                role="columnheader"
                class="modern-credential-load__num"
              >
                {{ t(`credentialLoad.columns.${column}`) }}
              </span>
              <span role="columnheader">{{ t('credentialLoad.columns.status') }}</span>
            </div>
            <div v-for="{ item, stats } in accessRows" :key="item.id" class="modern-credential-load__row" role="row">
              <span role="cell" class="modern-credential-load__name">{{ displayName(item) }}</span>
              <span role="cell" class="modern-credential-load__num modern-credential-load__req">
                <span class="modern-credential-load__bar" aria-hidden="true">
                  <span class="modern-credential-load__bar-fill" :style="{ width: `${barPercent(stats.requests, accessMax)}%` }"></span>
                </span>
                {{ stats.requests }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ ratio(stats.success, stats.requests) }}</span>
              <span role="cell" class="modern-credential-load__num">{{ ratio(stats.affinityHits, stats.requests) }}</span>
              <span role="cell" class="modern-credential-load__num">
                {{ formatTokens(stats.uncachedInputTokens + stats.outputTokens) }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ formatTokens(stats.cacheReadTokens) }}</span>
              <span role="cell">
                <span
                  class="modern-credential-load__chip"
                  :class="stats.requests > 0 ? 'modern-credential-load__chip--ok' : 'modern-credential-load__chip--idle'"
                >
                  {{ stats.requests > 0 ? t('credentialLoad.keyActive') : t('credentialLoad.keyIdle') }}
                </span>
              </span>
            </div>
          </div>
          <p v-else class="modern-credential-load__empty">{{ t('credentialLoad.empty') }}</p>
        </section>

        <div class="modern-credential-load__connector" aria-hidden="true">
          <span class="modern-credential-load__connector-arrow">↓</span>
          <span class="modern-credential-load__connector-label">{{ t('credentialLoad.routeHint') }}</span>
        </div>

        <!-- 分组层: 路由容器 -->
        <section class="modern-credential-load__tier">
          <div class="modern-credential-load__tier-head">
            <span class="modern-credential-load__tier-badge">{{ t('credentialLoad.tierGroups') }}</span>
            <span class="modern-credential-load__tier-agg">
              {{ groupRows.length }} · {{ t('credentialLoad.columns.requests') }} {{ formatTokens(totals?.requests ?? 0) }}
            </span>
          </div>
          <div
            v-if="groupRows.length > 0"
            class="modern-credential-load__table modern-credential-load__table--groups"
            role="table"
          >
            <div class="modern-credential-load__row modern-credential-load__row--head" role="row">
              <span role="columnheader">{{ t('credentialLoad.group') }}</span>
              <span
                v-for="column in (['requests','successRate','tokens','cacheRead'] as const)"
                :key="column"
                role="columnheader"
                class="modern-credential-load__num"
              >
                {{ t(`credentialLoad.columns.${column}`) }}
              </span>
            </div>
            <div v-for="{ item, stats } in groupRows" :key="item.id" class="modern-credential-load__row" role="row">
              <span role="cell" class="modern-credential-load__name">{{ displayName(item) }}</span>
              <span role="cell" class="modern-credential-load__num modern-credential-load__req">
                <span class="modern-credential-load__bar" aria-hidden="true">
                  <span class="modern-credential-load__bar-fill" :style="{ width: `${barPercent(stats.requests, groupRows[0].stats.requests)}%` }"></span>
                </span>
                {{ stats.requests }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ ratio(stats.success, stats.requests) }}</span>
              <span role="cell" class="modern-credential-load__num">
                {{ formatTokens(stats.uncachedInputTokens + stats.outputTokens) }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ formatTokens(stats.cacheReadTokens) }}</span>
            </div>
          </div>
          <p v-else class="modern-credential-load__empty">{{ t('credentialLoad.empty') }}</p>
        </section>

        <div class="modern-credential-load__connector" aria-hidden="true">
          <span class="modern-credential-load__connector-arrow">↓</span>
          <span class="modern-credential-load__connector-label">{{ t('credentialLoad.dispatchHint') }}</span>
        </div>

        <!-- 凭证层: 调度终点, 实时并发 + 窗口统计 + 压力 -->
        <section class="modern-credential-load__tier">
          <div class="modern-credential-load__tier-head">
            <span class="modern-credential-load__tier-badge">{{ t('credentialLoad.tierCredentials') }}</span>
            <span class="modern-credential-load__tier-agg">
              {{ credRows.length }} ·
              {{ report.cap > 0 ? `${t('credentialLoad.statInflight')} ${totals?.inflight ?? 0}/${totals?.capacity ?? 0}` : t('credentialLoad.unlimited') }}
            </span>
          </div>
          <div
            v-if="credRows.length > 0"
            class="modern-credential-load__table modern-credential-load__table--creds"
            role="table"
          >
            <div class="modern-credential-load__row modern-credential-load__row--head" role="row">
              <span role="columnheader">{{ t('credentialLoad.columns.credential') }}</span>
              <span role="columnheader">{{ t('credentialLoad.columns.status') }}</span>
              <span
                v-for="column in (['inflight','queued','pressure','requests','successRate','affinity','tokens','cacheRead','quota5h'] as const)"
                :key="column"
                role="columnheader"
                class="modern-credential-load__num"
              >
                {{ t(`credentialLoad.columns.${column}`) }}
              </span>
              <span role="columnheader" class="modern-credential-load__num">{{ t('credentialLoad.columns.errors') }}</span>
            </div>
            <div v-for="{ item, stats } in credRows" :key="item.id" class="modern-credential-load__row" role="row">
              <span role="cell" class="modern-credential-load__name">{{ displayName(item) }}</span>
              <span role="cell" :class="item.runtimeState === 'available' ? 'modern-credential-load__ok' : 'modern-credential-load__muted'">
                {{ item.runtimeState === 'available' ? item.status : item.runtimeState }}
              </span>
              <span role="cell" class="modern-credential-load__num modern-credential-load__req">
                <span class="modern-credential-load__bar" aria-hidden="true">
                  <span
                    class="modern-credential-load__bar-fill"
                    :class="`modern-credential-load__bar-fill--${inflightTone(item)}`"
                    :style="{ width: `${barPercent(item.inflight, report.cap)}%` }"
                  ></span>
                </span>
                {{ item.inflight }}<template v-if="report.cap > 0"> / {{ report.cap }}</template>
              </span>
              <span role="cell" class="modern-credential-load__num" :class="{ 'modern-credential-load__queued': item.queued > 0 }">
                {{ item.queued > 0 ? item.queued : '·' }}
              </span>
              <span role="cell" class="modern-credential-load__num">
                <span
                  v-if="item.pressure > 0"
                  class="modern-credential-load__chip"
                  :class="`modern-credential-load__chip--${pressureTone(item.pressure)}`"
                >
                  {{ Math.round(item.pressure * 100) }}%
                </span>
                <template v-else>—</template>
              </span>
              <span role="cell" class="modern-credential-load__num modern-credential-load__req">
                <span class="modern-credential-load__bar" aria-hidden="true">
                  <span class="modern-credential-load__bar-fill" :style="{ width: `${barPercent(stats.requests, credMax)}%` }"></span>
                </span>
                {{ stats.requests }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ ratio(stats.success, stats.requests) }}</span>
              <span role="cell" class="modern-credential-load__num">{{ ratio(stats.affinityHits, stats.requests) }}</span>
              <span role="cell" class="modern-credential-load__num">
                {{ formatTokens(stats.uncachedInputTokens + stats.outputTokens) }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ formatTokens(stats.cacheReadTokens) }}</span>
              <span
                role="cell"
                class="modern-credential-load__num modern-credential-load__req"
                :title="`${Math.round(item.credits5h)} / ${report.plan.quotaCredits5h} ${t('credentialLoad.creditsUnit')}`"
              >
                <span class="modern-credential-load__bar" aria-hidden="true">
                  <span
                    class="modern-credential-load__bar-fill"
                    :class="`modern-credential-load__bar-fill--${quotaTone(item.credits5h / (report.plan.quotaCredits5h || 1))}`"
                    :style="{ width: `${Math.min(100, quotaPercent(item.credits5h, report.plan.quotaCredits5h))}%` }"
                  ></span>
                </span>
                {{ quotaPercent(item.credits5h, report.plan.quotaCredits5h) }}%
              </span>
              <span
                role="cell"
                class="modern-credential-load__num"
                :class="{ 'modern-credential-load__danger': errorTotal(item.errors5h) > 0 }"
              >
                {{ errorTotal(item.errors5h) > 0 ? errorTotal(item.errors5h) : '·' }}
              </span>
            </div>
          </div>
          <p v-else class="modern-credential-load__empty">{{ t('credentialLoad.empty') }}</p>
        </section>
      </div>

      <section class="modern-credential-load__errors">
        <h3>{{ t('credentialLoad.errorsTitle') }}</h3>
        <div v-if="errorRows.length > 0" class="modern-credential-load__table modern-credential-load__table--errors" role="table">
          <div class="modern-credential-load__row modern-credential-load__row--head" role="row">
            <span role="columnheader">{{ t('credentialLoad.columns.credential') }}</span>
            <span
              v-for="column in errorColumns"
              :key="column.key"
              role="columnheader"
              class="modern-credential-load__num"
            >
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

      <p class="modern-credential-load__meta">
        {{
          t('credentialLoad.summary', {
            count: report.credentials.length,
            cap: report.cap > 0 ? report.cap : t('credentialLoad.unlimited'),
            rejects: report.localRejects[activeWindow],
          })
        }}
      </p>
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

.modern-credential-load__title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 1.25rem;
  font-weight: 700;
}

.modern-credential-load__help {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.4));
  border-radius: 50%;
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.75rem;
  font-weight: 600;
  cursor: help;
}

.modern-credential-load__meta {
  margin: 0;
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.875rem;
}

/* 站点层: 汇总卡片 */
.modern-credential-load__cards {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
}

@media (max-width: 900px) {
  .modern-credential-load__cards {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.modern-credential-load__card {
  display: grid;
  gap: 2px;
  padding: 10px 14px;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  border-radius: 8px;
  background: var(--modern-color-surface-raised, transparent);
}

.modern-credential-load__card-value {
  font-size: 1.15rem;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.modern-credential-load__card-label {
  display: flex;
  align-items: center;
  gap: 5px;
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.75rem;
  white-space: nowrap;
}

.modern-credential-load__pulse {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--modern-color-success, #22c55e);
  animation: modern-credential-load-pulse 2s ease-in-out infinite;
}

@keyframes modern-credential-load-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

/* 链路层 */
.modern-credential-load__chain {
  display: grid;
  gap: 0;
}

.modern-credential-load__tier {
  display: grid;
  gap: 8px;
}

.modern-credential-load__tier-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
}

.modern-credential-load__tier-badge {
  padding: 2px 10px;
  border-radius: 999px;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.35));
  background: var(--modern-color-surface-raised, transparent);
  font-size: 0.8125rem;
  font-weight: 650;
}

.modern-credential-load__tier-agg {
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
}

.modern-credential-load__connector {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 6px 0;
}

.modern-credential-load__connector-arrow {
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.9rem;
  line-height: 1;
}

.modern-credential-load__connector-label {
  color: var(--modern-color-text-muted, inherit);
  font-size: 0.75rem;
}

/* 表格 */
.modern-credential-load__table {
  display: grid;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  border-radius: 8px;
  overflow-x: auto;
}

.modern-credential-load__row {
  display: grid;
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

.modern-credential-load__table--access .modern-credential-load__row {
  grid-template-columns: minmax(88px, 1.2fr) minmax(120px, 1.5fr) repeat(4, minmax(56px, 0.8fr)) minmax(64px, 0.7fr);
}

.modern-credential-load__table--groups .modern-credential-load__row {
  grid-template-columns: minmax(88px, 1.4fr) minmax(120px, 1.5fr) repeat(3, minmax(56px, 0.8fr));
}

.modern-credential-load__table--creds .modern-credential-load__row {
  grid-template-columns: minmax(88px, 1.1fr) minmax(56px, 0.6fr) minmax(110px, 1.2fr) minmax(44px, 0.5fr) minmax(56px, 0.6fr) minmax(110px, 1.2fr) repeat(4, minmax(52px, 0.7fr)) minmax(88px, 1fr) minmax(44px, 0.5fr);
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

/* 数值 + 条形组合列: 条形贴左侧, 数值右对齐 */
.modern-credential-load__req {
  display: flex;
  align-items: center;
  gap: 6px;
  justify-content: flex-end;
}

.modern-credential-load__bar {
  flex: 1 1 32px;
  height: 6px;
  border-radius: 3px;
  background: var(--modern-color-border, rgba(127, 127, 127, 0.18));
  overflow: hidden;
}

.modern-credential-load__bar-fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--modern-color-accent, #3b82f6);
}

.modern-credential-load__bar-fill--ok {
  background: var(--modern-color-success, #22c55e);
}

.modern-credential-load__bar-fill--warn {
  background: var(--modern-color-warning, #eab308);
}

.modern-credential-load__bar-fill--high {
  background: var(--modern-color-danger, #ef4444);
}

.modern-credential-load__chip {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.modern-credential-load__chip--ok {
  color: var(--modern-color-success, #16a34a);
  background: color-mix(in srgb, var(--modern-color-success, #22c55e) 14%, transparent);
}

.modern-credential-load__chip--idle {
  color: var(--modern-color-text-muted, inherit);
  background: var(--modern-color-border, rgba(127, 127, 127, 0.15));
}

.modern-credential-load__chip--warn {
  color: var(--modern-color-warning, #a16207);
  background: color-mix(in srgb, var(--modern-color-warning, #eab308) 16%, transparent);
}

.modern-credential-load__chip--high {
  color: var(--modern-color-danger, #dc2626);
  background: color-mix(in srgb, var(--modern-color-danger, #ef4444) 14%, transparent);
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

.modern-credential-load__warn {
  color: var(--modern-color-warning, inherit);
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
