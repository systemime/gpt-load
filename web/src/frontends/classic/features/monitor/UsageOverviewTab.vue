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
import { formatInteger, formatLocalInstant } from '@/lib/format'

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
const stackedBars = computed(() => {
  const series = report.value?.series ?? []
  const byDay = new Map<number, Map<number, number>>()
  for (const point of series) {
    const value = metric.value === 'requests' ? point.requests : point.tokens
    if (value <= 0) continue
    if (!byDay.has(point.day_ms)) byDay.set(point.day_ms, new Map())
    const day = byDay.get(point.day_ms)!
    day.set(point.access_key_id, (day.get(point.access_key_id) ?? 0) + value)
  }
  const days = [...byDay.keys()].sort((left, right) => left - right)
  const height = 180
  const width = Math.max(days.length * 18, 120)
  const dailyTotals = days.map((day) =>
    [...byDay.get(day)!.values()].reduce((sum, value) => sum + value, 0),
  )
  const peak = Math.max(1, ...dailyTotals)
  const bars = days.map((day, index) => {
    const total = dailyTotals[index]
    let offset = 0
    const parts = [...byDay.get(day)!.entries()]
      .filter(([id]) => personID.value === 0 || id === personID.value)
      .map(([id, value]) => {
        const partHeight = Math.round((value / peak) * height)
        const top = height - offset - partHeight
        offset += partHeight
        return { id, value, y: top, height: partHeight }
      })
    return { day, x: index * 18 + 4, parts, total }
  })
  return { bars, width, height, peak, days: days.length }
})

// ---------- 折线: 每日总量 ----------
const trendLine = computed(() => {
  const series = report.value?.series ?? []
  const byDay = new Map<number, number>()
  for (const point of series) {
    const value = metric.value === 'requests' ? point.requests : point.tokens
    byDay.set(point.day_ms, (byDay.get(point.day_ms) ?? 0) + value)
  }
  const days = [...byDay.keys()].sort((left, right) => left - right)
  const values = days.map((day) => byDay.get(day) ?? 0)
  const peak = Math.max(1, ...values)
  const height = 140
  const width = Math.max(days.length * 18, 120)
  const points = days.map((day, index) => {
    const x = index * 18 + 9
    const y = height - Math.round((values[index] / peak) * (height - 8)) - 4
    return { x, y, day, value: values[index] }
  })
  const path = points.map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x} ${point.y}`).join(' ')
  return { points, path, width, height, peak }
})

function dayLabel(dayMS: number): string {
  return formatLocalInstant(dayMS, locale.value, { month: 'numeric', day: 'numeric' })
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
              :viewBox="`0 0 ${stackedBars.width} ${stackedBars.height + 18}`"
              preserveAspectRatio="xMinYMin meet"
              role="img"
              :aria-label="t('monitor.usageOverview.dailyTitle')"
            >
              <g v-for="bar in stackedBars.bars" :key="bar.day">
                <rect
                  v-for="(part, partIndex) in bar.parts"
                  :key="part.id"
                  :x="bar.x"
                  :y="part.y"
                  :width="12"
                  :height="Math.max(part.height, part.value > 0 ? 1 : 0)"
                  :fill="personColor.get(part.id) ?? PALETTE[partIndex % PALETTE.length]"
                />
                <text v-if="stackedBars.days <= 32" :x="bar.x + 6" :y="stackedBars.height + 14" text-anchor="middle" class="usage-overview__tick">
                  {{ dayLabel(bar.day) }}
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
              :viewBox="`0 0 ${trendLine.width} ${trendLine.height + 18}`"
              preserveAspectRatio="xMinYMin meet"
              role="img"
              :aria-label="t('monitor.usageOverview.trendTitle')"
            >
              <path :d="trendLine.path" fill="none" stroke="var(--color-action, #3b82f6)" stroke-width="2" />
              <circle v-for="point in trendLine.points" :key="point.day" :cx="point.x" :cy="point.y" r="2.5" fill="var(--color-action, #3b82f6)">
                <title>{{ `${dayLabel(point.day)}: ${formatCompact(point.value)}` }}</title>
              </circle>
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
  width: 100%;
  min-width: 320px;
  max-width: 1200px;
  height: auto;
}

.usage-overview__tick {
  font-size: 9px;
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
