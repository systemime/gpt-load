<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useApiClient } from '@shared/http/client-context'
import { useStableLoading } from '@/app/loading-state'
import { listGroupOptions } from '@/app/resources/groups'
import {
  usageOverviewQueryOptions,
  type UsageOverviewPerson,
} from '@/app/resources/usage-overview'
import QueryFeedback from '@/components/ui/QueryFeedback.vue'
import SkeletonSurface from '@/components/ui/SkeletonSurface.vue'
import { formatInteger } from '@/lib/format'

// 每人累计用量总览(阶段 4): 筛选(日期/分组/用户) + 饼图/堆叠柱/折线, 自绘 SVG 零依赖。

const PALETTE = [
  '#3b82f6',
  '#f97316',
  '#10b981',
  '#8b5cf6',
  '#ef4444',
  '#14b8a6',
  '#eab308',
  '#ec4899',
  '#06b6d4',
  '#84cc16',
]

const client = useApiClient()
const { locale, t } = useI18n()

const quickRange = ref<'all' | '30d' | '7d'>('all')
const customFrom = ref('')
const customTo = ref('')
const groupID = ref(0)
const personID = ref(0)
const metric = ref<'requests' | 'tokens'>('requests')

const filters = computed(() => {
  const value: {
    from_ms?: number
    to_ms?: number
    group_id?: number
    access_key_id?: number
  } = {}
  if (customFrom.value) {
    const ms = Date.parse(customFrom.value)
    if (!Number.isNaN(ms)) value.from_ms = ms
  } else if (quickRange.value === '30d') {
    value.from_ms = Date.now() - 30 * 86_400_000
  } else if (quickRange.value === '7d') {
    value.from_ms = Date.now() - 7 * 86_400_000
  }
  if (customTo.value) {
    const ms = Date.parse(customTo.value)
    if (!Number.isNaN(ms)) value.to_ms = ms + 86_400_000
  }
  if (groupID.value > 0) value.group_id = groupID.value
  if (personID.value > 0) value.access_key_id = personID.value
  return value
})

const query = useQuery(
  computed(() => usageOverviewQueryOptions(client, filters.value)),
)
const report = computed(() => query.data.value)
const initialLoading = useStableLoading(() => query.isPending.value)
const groupsQuery = useQuery({
  queryKey: ['monitor', 'usage-overview', 'group-options'],
  queryFn: ({ signal }) => listGroupOptions(client, signal),
  staleTime: 5 * 60_000,
})

const people = computed(() => {
  const list = report.value?.people ?? []
  return [...list].sort((left, right) => personMetric(right) - personMetric(left))
})
// 厂商凭证维度: 后端已按请求降序。
const credentials = computed(() => report.value?.credentials ?? [])
const personNames = computed(() => {
  const names = new Map<number, string>()
  for (const person of people.value) {
    names.set(person.access_key_id, person.name || `#${person.access_key_id}`)
  }
  return names
})
const personColor = computed(() => {
  const colors = new Map<number, string>()
  people.value.forEach((person, index) => {
    colors.set(person.access_key_id, PALETTE[index % PALETTE.length])
  })
  return colors
})

function personMetric(person: UsageOverviewPerson): number {
  return metric.value === 'requests'
    ? person.totals.requests
    : person.totals.uncached_input_tokens +
        person.totals.output_tokens +
        person.totals.cache_read_tokens
}

function formatCompact(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}G`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return formatInteger(value, locale.value)
}

// ---------- 饼图(环形): 按人份额 ----------
const donut = computed(() => {
  const segments = people.value
    .map((person) => ({
      id: person.access_key_id,
      label: personNames.value.get(person.access_key_id) ?? '',
      value: personMetric(person),
    }))
    .filter((segment) => segment.value > 0)
  const total = segments.reduce((sum, segment) => sum + segment.value, 0)
  if (total <= 0) return { segments: [], total }
  let angle = -Math.PI / 2
  const radiusOuter = 88
  const radiusInner = 52
  const arcs = segments.map((segment, index) => {
    const sweep = (segment.value / total) * Math.PI * 2
    const start = angle
    const end = angle + sweep
    angle = end
    const large = sweep > Math.PI ? 1 : 0
    const point = (radius: number, at: number) =>
      `${(100 + radius * Math.cos(at)).toFixed(2)},${(100 + radius * Math.sin(at)).toFixed(2)}`
    return {
      id: segment.id,
      label: segment.label,
      value: segment.value,
      percent: Math.round((segment.value / total) * 100),
      color: PALETTE[index % PALETTE.length],
      path:
        sweep >= Math.PI * 2 - 1e-6
          ? // 单人全量: 画双半圆凑整圆
            `M ${point(radiusOuter, -Math.PI / 2)} A ${radiusOuter} ${radiusOuter} 0 1 1 ${point(radiusOuter, Math.PI / 2)} A ${radiusOuter} ${radiusOuter} 0 1 1 ${point(radiusOuter, -Math.PI / 2)} M ${point(radiusInner, -Math.PI / 2)} A ${radiusInner} ${radiusInner} 0 1 0 ${point(radiusInner, Math.PI / 2)} A ${radiusInner} ${radiusInner} 0 1 0 ${point(radiusInner, -Math.PI / 2)}`
          : `M ${point(radiusOuter, start)} A ${radiusOuter} ${radiusOuter} 0 ${large} 1 ${point(radiusOuter, end)} L ${point(radiusInner, end)} A ${radiusInner} ${radiusInner} 0 ${large} 0 ${point(radiusInner, start)} Z`,
    }
  })
  return { segments: arcs, total }
})

// ---------- 堆叠柱: 按日按人 ----------
const CHART_WIDTH = 1000
const CHART_HEIGHT = 260
const PLOT_LEFT = 56
const PLOT_RIGHT = 16
const PLOT_TOP = 12
const PLOT_BOTTOM = 26
const PLOT_WIDTH = CHART_WIDTH - PLOT_LEFT - PLOT_RIGHT
const PLOT_HEIGHT = CHART_HEIGHT - PLOT_TOP - PLOT_BOTTOM

const chartDays = computed(() => {
  const days = new Set<number>()
  for (const point of report.value?.series ?? []) days.add(point.day_ms)
  return [...days].sort((left, right) => left - right)
})

const chartPeak = computed(() => {
  const totals = new Map<number, number>()
  for (const point of report.value?.series ?? []) {
    totals.set(point.day_ms, (totals.get(point.day_ms) ?? 0) + (metric.value === 'requests' ? point.requests : point.tokens))
  }
  return Math.max(1, ...totals.values())
})

const yTicks = computed(() =>
  [0, 0.25, 0.5, 0.75, 1].map((ratio) => {
    const value = Math.round(chartPeak.value * ratio)
    return { value, y: PLOT_TOP + PLOT_HEIGHT - ratio * PLOT_HEIGHT, label: formatCompact(value) }
  }),
)

const xTickValues = computed(() => {
  const days = chartDays.value
  if (days.length === 0) return []
  const step = Math.ceil(days.length / 12)
  return days.filter((_, index) => index % step === 0)
})

const stackedBars = computed(() => {
  const series = report.value?.series ?? []
  const days = chartDays.value
  const slotWidth = days.length > 0 ? PLOT_WIDTH / days.length : PLOT_WIDTH
  const barWidth = Math.min(48, Math.max(6, slotWidth * 0.62))
  const byDay = new Map<number, Map<number, number>>()
  for (const point of series) {
    const value = metric.value === 'requests' ? point.requests : point.tokens
    if (value <= 0) continue
    if (!byDay.has(point.day_ms)) byDay.set(point.day_ms, new Map())
    const day = byDay.get(point.day_ms)!
    day.set(point.access_key_id, (day.get(point.access_key_id) ?? 0) + value)
  }
  const peak = chartPeak.value
  const bars = days.map((day, index) => {
    const slotX = PLOT_LEFT + index * slotWidth
    const barX = slotX + (slotWidth - barWidth) / 2
    let offset = 0
    const dayMap = byDay.get(day) ?? new Map<number, number>()
    const parts = [...dayMap.entries()]
      .filter(([id]) => personID.value === 0 || id === personID.value)
      .map(([id, value]) => {
        const partHeight = Math.round((value / peak) * PLOT_HEIGHT)
        const top = PLOT_TOP + PLOT_HEIGHT - offset - partHeight
        offset += partHeight
        return { id, value, y: top, height: Math.max(partHeight, value > 0 ? 1 : 0) }
      })
    return { day, centerX: slotX + slotWidth / 2, barX, barWidth, parts }
  })
  return { bars }
})

// ---------- 折线: 每日总量 ----------
const trendLine = computed(() => {
  const series = report.value?.series ?? []
  const days = chartDays.value
  const byDay = new Map<number, number>()
  for (const point of series) {
    const value = metric.value === 'requests' ? point.requests : point.tokens
    byDay.set(point.day_ms, (byDay.get(point.day_ms) ?? 0) + value)
  }
  const peak = chartPeak.value
  const slotWidth = days.length > 0 ? PLOT_WIDTH / days.length : PLOT_WIDTH
  const points = days.map((day, index) => {
    const value = byDay.get(day) ?? 0
    return {
      x: PLOT_LEFT + slotWidth * index + slotWidth / 2,
      y: PLOT_TOP + PLOT_HEIGHT - Math.round((value / peak) * PLOT_HEIGHT),
      day,
      value,
    }
  })
  const path = points.map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x} ${point.y}`).join(' ')
  return { points, path }
})

function xLabelFor(dayMS: number): string {
  const date = new Date(dayMS)
  return `${date.getMonth() + 1}/${date.getDate()}`
}

function refresh(): Promise<void> {
  return query.refetch().then(() => undefined)
}
</script>

<template>
  <div class="usage-overview">
    <QueryFeedback
      v-if="initialLoading || (query.isError.value && report === undefined)"
      :state="initialLoading ? 'loading' : 'error'"
      :message="initialLoading ? t('monitor.usageOverview.loading') : t('monitor.usageOverview.loadError')"
      :retry-label="t('monitor.usageOverview.retry')"
      @retry="refresh"
    />
    <SkeletonSurface v-if="initialLoading" class="usage-overview__skeleton" :label="t('monitor.usageOverview.loading')" />

    <template v-else-if="report">
      <!-- 筛选区 -->
      <form class="usage-overview__filters" role="search" @submit.prevent>
        <div class="usage-overview__quick">
          <button
            v-for="range in (['all', '30d', '7d'] as const)"
            :key="range"
            type="button"
            :class="{ 'is-active': quickRange === range && !customFrom && !customTo }"
            @click="quickRange = range; customFrom = ''; customTo = ''"
          >
            {{ t(`monitor.usageOverview.ranges.${range}`) }}
          </button>
        </div>
        <label>
          <span>{{ t('monitor.usageOverview.filters.from') }}</span>
          <input v-model="customFrom" type="date" />
        </label>
        <label>
          <span>{{ t('monitor.usageOverview.filters.to') }}</span>
          <input v-model="customTo" type="date" />
        </label>
        <label>
          <span>{{ t('monitor.usageOverview.filters.group') }}</span>
          <select v-model.number="groupID">
            <option :value="0">{{ t('monitor.usageOverview.filters.allGroups') }}</option>
            <option v-for="group in groupsQuery.data.value ?? []" :key="group.id" :value="group.id">
              {{ group.name }}
            </option>
          </select>
        </label>
        <label>
          <span>{{ t('monitor.usageOverview.filters.person') }}</span>
          <select v-model.number="personID">
            <option :value="0">{{ t('monitor.usageOverview.filters.allPeople') }}</option>
            <option v-for="person in people" :key="person.access_key_id" :value="person.access_key_id">
              {{ personNames.get(person.access_key_id) }}
            </option>
          </select>
        </label>
        <div class="usage-overview__metric">
          <button
            v-for="option in (['requests', 'tokens'] as const)"
            :key="option"
            type="button"
            :class="{ 'is-active': metric === option }"
            @click="metric = option"
          >
            {{ t(`monitor.usageOverview.metrics.${option}`) }}
          </button>
        </div>
      </form>

      <p v-if="people.length === 0" class="usage-overview__empty">
        {{ t('monitor.usageOverview.empty') }}
      </p>

      <template v-else>
        <!-- 饼图 + 图例 -->
        <section class="usage-overview__card">
          <h3>{{ t('monitor.usageOverview.shareTitle') }}</h3>
          <div class="usage-overview__donut">
            <svg viewBox="0 0 200 200" role="img" :aria-label="t('monitor.usageOverview.shareTitle')">
              <path
                v-for="arc in donut.segments"
                :key="arc.id"
                :d="arc.path"
                :fill="arc.color"
                stroke="var(--color-surface, #fff)"
                stroke-width="1"
              />
            </svg>
            <ul class="usage-overview__legend">
              <li v-for="arc in donut.segments" :key="arc.id">
                <span class="usage-overview__swatch" :style="{ background: arc.color }" />
                <span class="usage-overview__legend-name">{{ arc.label }}</span>
                <span class="usage-overview__legend-value">
                  {{ formatCompact(arc.value) }} · {{ arc.percent }}%
                </span>
              </li>
            </ul>
          </div>
        </section>

        <!-- 堆叠柱 -->
        <section class="usage-overview__card">
          <h3>{{ t('monitor.usageOverview.dailyTitle') }}</h3>
          <div class="usage-overview__chart-scroll">
            <svg
              :viewBox="`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`"
              role="img"
              :aria-label="t('monitor.usageOverview.dailyTitle')"
            >
              <g v-for="tick in yTicks" :key="'gy' + tick.y">
                <line
                  :x1="PLOT_LEFT" :x2="CHART_WIDTH - PLOT_RIGHT"
                  :y1="tick.y" :y2="tick.y"
                  stroke="currentColor" opacity="0.12"
                />
                <text :x="PLOT_LEFT - 8" :y="tick.y + 4" text-anchor="end" class="usage-overview__tick">
                  {{ tick.label }}
                </text>
              </g>
              <line
                :x1="PLOT_LEFT" :x2="CHART_WIDTH - PLOT_RIGHT"
                :y1="PLOT_TOP + PLOT_HEIGHT" :y2="PLOT_TOP + PLOT_HEIGHT"
                stroke="currentColor" opacity="0.3"
              />
              <g v-for="bar in stackedBars.bars" :key="bar.day">
                <rect
                  v-for="part in bar.parts"
                  :key="part.id"
                  :x="bar.barX"
                  :y="part.y"
                  :width="bar.barWidth"
                  :height="part.height"
                  :fill="personColor.get(part.id) ?? PALETTE[0]"
                >
                  <title>
                    {{
                      `${personNames.get(part.id) ?? ''} · ${xLabelFor(bar.day)} · ${formatCompact(part.value)}`
                    }}
                  </title>
                </rect>
                <text
                  v-if="xTickValues.includes(bar.day)"
                  :x="bar.centerX"
                  :y="CHART_HEIGHT - 6"
                  text-anchor="middle"
                  class="usage-overview__tick"
                >
                  {{ xLabelFor(bar.day) }}
                </text>
              </g>
            </svg>
          </div>
        </section>

        <!-- 折线 -->
        <section class="usage-overview__card">
          <h3>{{ t('monitor.usageOverview.trendTitle') }}</h3>
          <div class="usage-overview__chart-scroll">
            <svg
              :viewBox="`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`"
              role="img"
              :aria-label="t('monitor.usageOverview.trendTitle')"
            >
              <g v-for="tick in yTicks" :key="'gy' + tick.y">
                <line
                  :x1="PLOT_LEFT" :x2="CHART_WIDTH - PLOT_RIGHT"
                  :y1="tick.y" :y2="tick.y"
                  stroke="currentColor" opacity="0.12"
                />
                <text :x="PLOT_LEFT - 8" :y="tick.y + 4" text-anchor="end" class="usage-overview__tick">
                  {{ tick.label }}
                </text>
              </g>
              <line
                :x1="PLOT_LEFT" :x2="CHART_WIDTH - PLOT_RIGHT"
                :y1="PLOT_TOP + PLOT_HEIGHT" :y2="PLOT_TOP + PLOT_HEIGHT"
                stroke="currentColor" opacity="0.3"
              />
              <path :d="trendLine.path" fill="none" stroke="var(--color-action, #3b82f6)" stroke-width="2" />
              <circle v-for="point in trendLine.points" :key="point.day" :cx="point.x" :cy="point.y" r="3" fill="var(--color-action, #3b82f6)">
                <title>{{ `${xLabelFor(point.day)}: ${formatCompact(point.value)}` }}</title>
              </circle>
              <circle
                v-for="point in trendLine.points"
                :key="'hit' + point.day"
                :cx="point.x"
                :cy="point.y"
                r="10"
                fill="transparent"
              >
                <title>{{ `${xLabelFor(point.day)}: ${formatCompact(point.value)}` }}</title>
              </circle>
              <text
                v-for="(point, index) in trendLine.points"
                v-show="index % Math.ceil(trendLine.points.length / 12) === 0"
                :key="'xl' + point.day"
                :x="point.x"
                :y="CHART_HEIGHT - 6"
                text-anchor="middle"
                class="usage-overview__tick"
              >
                {{ xLabelFor(point.day) }}
              </text>
            </svg>
          </div>
        </section>

        <!-- 汇总表 -->
        <section class="usage-overview__card">
          <h3>{{ t('monitor.usageOverview.tableTitle') }}</h3>
          <div class="usage-overview__table" role="table">
            <div class="usage-overview__row usage-overview__row--head" role="row">
              <span role="columnheader">{{ t('monitor.usageOverview.columns.person') }}</span>
              <span role="columnheader">{{ t('monitor.usageOverview.columns.requests') }}</span>
              <span role="columnheader">{{ t('monitor.usageOverview.columns.success') }}</span>
              <span role="columnheader">{{ t('monitor.usageOverview.columns.input') }}</span>
              <span role="columnheader">{{ t('monitor.usageOverview.columns.output') }}</span>
              <span role="columnheader">{{ t('monitor.usageOverview.columns.cacheRead') }}</span>
            </div>
            <div v-for="person in people" :key="person.access_key_id" class="usage-overview__row" role="row">
              <span role="cell" class="usage-overview__name">
                <span class="usage-overview__swatch" :style="{ background: personColor.get(person.access_key_id) }" />
                {{ personNames.get(person.access_key_id) }}
              </span>
              <span role="cell">{{ formatInteger(person.totals.requests, locale) }}</span>
              <span role="cell">{{ formatInteger(person.totals.success, locale) }}</span>
              <span role="cell">{{ formatCompact(person.totals.uncached_input_tokens) }}</span>
              <span role="cell">{{ formatCompact(person.totals.output_tokens) }}</span>
              <span role="cell">{{ formatCompact(person.totals.cache_read_tokens) }}</span>
            </div>
          </div>
        </section>

        <section v-if="credentials.length > 0" class="usage-overview__card">
          <h3>{{ t('monitor.usageOverview.credentialsTitle') }}</h3>
          <div class="usage-overview__table usage-overview__table--credentials" role="table">
            <div class="usage-overview__row usage-overview__row--head" role="row">
              <span role="columnheader">{{ t('monitor.usageOverview.columns.credential') }}</span>
              <span role="columnheader" class="usage-overview__num">{{ t('monitor.usageOverview.columns.requests') }}</span>
              <span role="columnheader" class="usage-overview__num">{{ t('monitor.usageOverview.columns.successRate') }}</span>
              <span role="columnheader" class="usage-overview__num">{{ t('monitor.usageOverview.columns.tokens') }}</span>
              <span role="columnheader" class="usage-overview__num">{{ t('monitor.usageOverview.columns.cacheRead') }}</span>
            </div>
            <div v-for="credential in credentials" :key="credential.credential_id" class="usage-overview__row" role="row">
              <span role="cell" class="usage-overview__name">{{ credential.name || `#${credential.credential_id}` }}</span>
              <span role="cell" class="usage-overview__num">{{ formatInteger(credential.totals.requests, locale) }}</span>
              <span role="cell" class="usage-overview__num">
                {{ credential.totals.requests > 0 ? `${Math.round((credential.totals.success / credential.totals.requests) * 100)}%` : '—' }}
              </span>
              <span role="cell" class="usage-overview__num">
                {{ formatCompact(credential.totals.uncached_input_tokens + credential.totals.output_tokens + credential.totals.cache_read_tokens) }}
              </span>
              <span role="cell" class="usage-overview__num">{{ formatCompact(credential.totals.cache_read_tokens) }}</span>
            </div>
          </div>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.usage-overview {
  display: grid;
  gap: var(--space-4);
}

.usage-overview__skeleton {
  height: 320px;
}

.usage-overview__filters {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: var(--space-3);
}

.usage-overview__filters label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--text-sm);
  color: var(--color-text-muted);
}

.usage-overview__filters select,
.usage-overview__filters input {
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-control);
  padding: var(--space-1) var(--space-2);
  background: var(--color-surface);
  color: inherit;
}

.usage-overview__quick,
.usage-overview__metric {
  display: inline-flex;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-control);
  overflow: hidden;
}

.usage-overview__quick button,
.usage-overview__metric button {
  border: 0;
  background: transparent;
  color: var(--color-text-muted);
  padding: var(--space-1) var(--space-3);
  cursor: pointer;
  font-size: var(--text-sm);
}

.usage-overview__quick button.is-active,
.usage-overview__metric button.is-active {
  background: var(--color-action-soft);
  color: var(--color-action);
  font-weight: 600;
}

.usage-overview__card {
  display: grid;
  gap: var(--space-2);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  padding: var(--space-3);
}

.usage-overview__card h3 {
  margin: 0;
  font-size: var(--title-section);
  font-weight: 650;
}

.usage-overview__donut {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  align-items: center;
  gap: var(--space-4);
}

.usage-overview__donut svg {
  width: 200px;
  height: 200px;
  flex: none;
}

.usage-overview__legend {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--space-1);
  min-width: 220px;
}

.usage-overview__legend li {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-sm);
}

.usage-overview__legend-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 160px;
}

.usage-overview__legend-value {
  font-family: var(--font-mono);
  color: var(--color-text-muted);
}

.usage-overview__swatch {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 2px;
  flex: none;
}

.usage-overview__chart-scroll {
  overflow-x: auto;
}

.usage-overview__chart-scroll svg {
  display: block;
}

.usage-overview__tick {
  font-size: 11px;
  fill: var(--color-text-muted);
}

.usage-overview__table {
  display: grid;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  overflow-x: auto;
}

.usage-overview__row {
  display: grid;
  grid-template-columns: 1.4fr repeat(5, 1fr);
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border-subtle);
  font-size: var(--text-sm);
}

.usage-overview__row:last-child {
  border-bottom: 0;
}

.usage-overview__row--head {
  color: var(--color-text-muted);
  font-weight: 600;
}

.usage-overview__row span:not(:first-child) {
  font-family: var(--font-mono);
  text-align: right;
}

/* 厂商消耗表: 5 列(凭证 + 4 数值) */
.usage-overview__table--credentials .usage-overview__row {
  grid-template-columns: 1.4fr repeat(4, 1fr);
}

.usage-overview__name {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-weight: 600;
}

.usage-overview__empty {
  margin: 0;
  color: var(--color-text-muted);
}
</style>
