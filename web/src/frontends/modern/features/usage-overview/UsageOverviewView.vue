<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@tanstack/vue-query'

import {
  getGroupOptionsLite,
  getUsageOverview,
  type UsageOverviewPerson,
} from '@modern/api/usage-overview'
import { useApiClient } from '@shared/http/client-context'
import { usePageRefresh } from '@modern/app/page-refresh'
import { useMessageSource } from '@modern/app/messages'
import {
  AppButton,
  AppCollectionState,
  AppDateTimeRangePicker,
  AppSelect,
  AppSegmentedControl,
} from '@modern/components/ui'
import { dateRangeFor, type DateRangePreset } from '@modern/components/ui/date-time'

// 每人累计用量总览(阶段 4, 仅管理员): 筛选 + 饼图/堆叠柱/折线, 自绘 SVG 零依赖。

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
const BAR_WIDTH = 18
const CHART_HEIGHT = 180

const client = useApiClient()
const { t } = useI18n()

const preset = ref<DateRangePreset>('15d')
const from = ref('')
const to = ref('')
const groupID = ref(0)
const personID = ref(0)
const metric = ref<'requests' | 'tokens'>('requests')

const groupsQuery = useQuery({
  queryKey: ['modern', 'usage-overview', 'group-options'],
  queryFn: ({ signal }) => getGroupOptionsLite(client, signal),
  staleTime: 5 * 60_000,
})

const filters = computed(() => {
  const value: { fromMS?: number; toMS?: number; groupID?: number; accessKeyID?: number } = {}
  const fromMS = Date.parse(from.value)
  const toMS = Date.parse(to.value)
  if (!Number.isNaN(fromMS)) value.fromMS = fromMS
  if (!Number.isNaN(toMS)) value.toMS = toMS + 86_400_000
  if (groupID.value > 0) value.groupID = groupID.value
  if (personID.value > 0) value.accessKeyID = personID.value
  return value
})

const overviewQuery = useQuery({
  queryKey: ['modern', 'usage-overview', filters.value],
  queryFn: ({ signal }) => getUsageOverview(client, filters.value, signal),
})

function refreshReport(): Promise<void> {
  return overviewQuery.refetch().then(() => undefined)
}
const report = computed(() => overviewQuery.data.value)
const failed = computed(() => overviewQuery.isError.value && report.value === undefined)

usePageRefresh({
  refresh: refreshReport,
  pending: () => overviewQuery.isFetching.value,
  updatedAt: () => report.value?.observedAt,
})
useMessageSource(() =>
  overviewQuery.isError.value && report.value
    ? { tone: 'warning', text: t('usageOverview.failed'), action: { label: t('ui.retry'), run: refreshReport } }
    : undefined,
)

function applyPreset(value: unknown): void {
  preset.value = value as DateRangePreset
  const range = dateRangeFor(preset.value)
  from.value = range.from
  to.value = range.to
}

function applyCustom(): void {
  preset.value = '' as DateRangePreset
}

const people = computed(() => {
  const list = report.value?.people ?? []
  return [...list].sort((left, right) => personMetric(right) - personMetric(left))
})
const personNames = computed(() => {
  const names = new Map<number, string>()
  for (const person of people.value) {
    names.set(person.id, person.name || `#${person.id}`)
  }
  return names
})
const personColor = computed(() => {
  const colors = new Map<number, string>()
  people.value.forEach((person, index) => {
    colors.set(person.id, PALETTE[index % PALETTE.length])
  })
  return colors
})

function personMetric(person: UsageOverviewPerson): number {
  return metric.value === 'requests'
    ? person.totals.requests
    : person.totals.uncachedInputTokens +
        person.totals.outputTokens +
        person.totals.cacheReadTokens
}

function formatCompact(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}G`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return String(value)
}

// ---------- 饼图(环形) ----------
const donut = computed(() => {
  const segments = people.value
    .map((person) => ({
      id: person.id,
      label: personNames.value.get(person.id) ?? '',
      value: personMetric(person),
    }))
    .filter((segment) => segment.value > 0)
  const total = segments.reduce((sum, segment) => sum + segment.value, 0)
  if (total <= 0) return { arcs: [], total }
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
    const path =
      sweep >= Math.PI * 2 - 1e-6
        ? `M ${point(radiusOuter, -Math.PI / 2)} A ${radiusOuter} ${radiusOuter} 0 1 1 ${point(radiusOuter, Math.PI / 2)} A ${radiusOuter} ${radiusOuter} 0 1 1 ${point(radiusOuter, -Math.PI / 2)} M ${point(radiusInner, -Math.PI / 2)} A ${radiusInner} ${radiusInner} 0 1 0 ${point(radiusInner, Math.PI / 2)} A ${radiusInner} ${radiusInner} 0 1 0 ${point(radiusInner, -Math.PI / 2)}`
        : `M ${point(radiusOuter, start)} A ${radiusOuter} ${radiusOuter} 0 ${large} 1 ${point(radiusOuter, end)} L ${point(radiusInner, end)} A ${radiusInner} ${radiusInner} 0 ${large} 0 ${point(radiusInner, start)} Z`
    return {
      id: segment.id,
      label: segment.label,
      value: segment.value,
      percent: Math.round((segment.value / total) * 100),
      color: PALETTE[index % PALETTE.length],
      path,
    }
  })
  return { arcs, total }
})

// ---------- 堆叠柱 ----------
const stacked = computed(() => {
  const points = report.value?.points ?? []
  const byDay = new Map<number, Map<number, number>>()
  for (const point of points) {
    const value = metric.value === 'requests' ? point.requests : point.tokens
    if (value <= 0) continue
    if (!byDay.has(point.dayMS)) byDay.set(point.dayMS, new Map())
    const day = byDay.get(point.dayMS)!
    day.set(point.accessKeyID, (day.get(point.accessKeyID) ?? 0) + value)
  }
  const days = [...byDay.keys()].sort((left, right) => left - right)
  const dailyTotals = days.map((day) =>
    [...byDay.get(day)!.values()].reduce((sum, value) => sum + value, 0),
  )
  const peak = Math.max(1, ...dailyTotals)
  const width = Math.max(days.length * BAR_WIDTH + 8, 120)
  const bars = days.map((day, index) => {
    let offset = 0
    const parts = [...byDay.get(day)!.entries()]
      .filter(([id]) => personID.value === 0 || id === personID.value)
      .map(([id, value]) => {
        const partHeight = Math.round((value / peak) * CHART_HEIGHT)
        const top = CHART_HEIGHT - offset - partHeight
        offset += partHeight
        return { id, value, y: top, height: Math.max(partHeight, 1) }
      })
    return { day, x: index * BAR_WIDTH + 4, parts, total: dailyTotals[index] }
  })
  return { bars, width, days: days.length }
})

// ---------- 折线 ----------
const trend = computed(() => {
  const points = report.value?.points ?? []
  const byDay = new Map<number, number>()
  for (const point of points) {
    const value = metric.value === 'requests' ? point.requests : point.tokens
    byDay.set(point.dayMS, (byDay.get(point.dayMS) ?? 0) + value)
  }
  const days = [...byDay.keys()].sort((left, right) => left - right)
  const values = days.map((day) => byDay.get(day) ?? 0)
  const peak = Math.max(1, ...values)
  const height = 140
  const width = Math.max(days.length * BAR_WIDTH + 8, 120)
  const plotted = days.map((day, index) => {
    const x = index * BAR_WIDTH + 9
    const y = height - Math.round((values[index] / peak) * (height - 8)) - 4
    return { x, y, day, value: values[index] }
  })
  const path = plotted.map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x} ${point.y}`).join(' ')
  return { plotted, path, width, height }
})

function dayLabel(dayMS: number): string {
  const date = new Date(dayMS)
  return `${date.getMonth() + 1}/${date.getDate()}`
}
</script>

<template>
  <div class="modern-usage-overview">
    <header class="modern-usage-overview__filters">
      <AppDateTimeRangePicker
        v-model:from="from"
        v-model:to="to"
        :preset="preset"
        :label="t('usageOverview.timeRange')"
        size="md"
        label-hidden
        @update:preset="applyPreset"
        @apply="applyCustom"
      />
      <AppSelect
        :model-value="groupID ? String(groupID) : ''"
        :options="[
          { value: '', label: t('usageOverview.allGroups') },
          ...(groupsQuery.data.value ?? []).map((group) => ({
            value: String(group.id),
            label: group.name,
          })),
        ]"
        :label="t('usageOverview.group')"
        size="md"
        @update:model-value="groupID = Number($event) || 0"
      />
      <AppSelect
        :model-value="personID ? String(personID) : ''"
        :options="[
          { value: '', label: t('usageOverview.allPeople') },
          ...people.map((person) => ({
            value: String(person.id),
            label: personNames.get(person.id) ?? '',
          })),
        ]"
        :label="t('usageOverview.person')"
        size="md"
        @update:model-value="personID = Number($event) || 0"
      />
      <AppSegmentedControl
        :model-value="metric"
        :options="[
          { value: 'requests', label: t('usageOverview.metrics.requests') },
          { value: 'tokens', label: t('usageOverview.metrics.tokens') },
        ]"
        :label="t('usageOverview.metric')"
        size="md"
        @update:model-value="metric = $event as 'requests' | 'tokens'"
      />
      <AppButton variant="outline" size="sm" @click="refreshReport">
        {{ t('usageOverview.refresh') }}
      </AppButton>
    </header>

    <AppCollectionState v-if="!report && !failed" :title="t('usageOverview.loading')" loading />
    <AppCollectionState v-else-if="failed" :title="t('usageOverview.failed')" error
      ><AppButton @click="refreshReport">{{ t('ui.retry') }}</AppButton></AppCollectionState
    >
    <template v-else-if="report">
      <p v-if="people.length === 0" class="modern-usage-overview__empty">
        {{ t('usageOverview.empty') }}
      </p>
      <template v-else>
        <section class="modern-usage-overview__card">
          <h3>{{ t('usageOverview.shareTitle') }}</h3>
          <div class="modern-usage-overview__donut">
            <svg viewBox="0 0 200 200" role="img" :aria-label="t('usageOverview.shareTitle')">
              <path
                v-for="arc in donut.arcs"
                :key="arc.id"
                :d="arc.path"
                :fill="arc.color"
                stroke="var(--modern-color-surface, #fff)"
                stroke-width="1"
              />
            </svg>
            <ul class="modern-usage-overview__legend">
              <li v-for="arc in donut.arcs" :key="arc.id">
                <span class="modern-usage-overview__swatch" :style="{ background: arc.color }" />
                <span class="modern-usage-overview__name">{{ arc.label }}</span>
                <span class="modern-usage-overview__value">
                  {{ formatCompact(arc.value) }} · {{ arc.percent }}%
                </span>
              </li>
            </ul>
          </div>
        </section>

        <section class="modern-usage-overview__card">
          <h3>{{ t('usageOverview.dailyTitle') }}</h3>
          <div class="modern-usage-overview__scroll">
            <svg
              :viewBox="`0 0 ${stacked.width} ${CHART_HEIGHT + 18}`"
              preserveAspectRatio="xMinYMin meet"
              role="img"
              :aria-label="t('usageOverview.dailyTitle')"
            >
              <g v-for="bar in stacked.bars" :key="bar.day">
                <rect
                  v-for="part in bar.parts"
                  :key="part.id"
                  :x="bar.x"
                  :y="part.y"
                  width="12"
                  :height="part.height"
                  :fill="personColor.get(part.id) ?? PALETTE[0]"
                />
                <text
                  v-if="stacked.days <= 32"
                  :x="bar.x + 6"
                  :y="CHART_HEIGHT + 14"
                  text-anchor="middle"
                  class="modern-usage-overview__tick"
                >
                  {{ dayLabel(bar.day) }}
                </text>
              </g>
            </svg>
          </div>
        </section>

        <section class="modern-usage-overview__card">
          <h3>{{ t('usageOverview.trendTitle') }}</h3>
          <div class="modern-usage-overview__scroll">
            <svg
              :viewBox="`0 0 ${trend.width} ${trend.height + 18}`"
              preserveAspectRatio="xMinYMin meet"
              role="img"
              :aria-label="t('usageOverview.trendTitle')"
            >
              <path :d="trend.path" fill="none" stroke="var(--modern-color-action, #3b82f6)" stroke-width="2" />
              <circle
                v-for="point in trend.plotted"
                :key="point.day"
                :cx="point.x"
                :cy="point.y"
                r="2.5"
                fill="var(--modern-color-action, #3b82f6)"
              >
                <title>{{ `${dayLabel(point.day)}: ${formatCompact(point.value)}` }}</title>
              </circle>
            </svg>
          </div>
        </section>

        <section class="modern-usage-overview__card">
          <h3>{{ t('usageOverview.tableTitle') }}</h3>
          <div class="modern-usage-overview__table" role="table">
            <div class="modern-usage-overview__row modern-usage-overview__row--head" role="row">
              <span role="columnheader">{{ t('usageOverview.person') }}</span>
              <span role="columnheader">{{ t('usageOverview.requests') }}</span>
              <span role="columnheader">{{ t('usageOverview.success') }}</span>
              <span role="columnheader">{{ t('usageOverview.input') }}</span>
              <span role="columnheader">{{ t('usageOverview.output') }}</span>
              <span role="columnheader">{{ t('usageOverview.cacheRead') }}</span>
            </div>
            <div v-for="person in people" :key="person.id" class="modern-usage-overview__row" role="row">
              <span role="cell" class="modern-usage-overview__name">
                <span class="modern-usage-overview__swatch" :style="{ background: personColor.get(person.id) }" />
                {{ personNames.get(person.id) }}
              </span>
              <span role="cell" class="modern-credential-load__num">{{ formatCompact(person.totals.requests) }}</span>
              <span role="cell" class="modern-credential-load__num">{{ formatCompact(person.totals.success) }}</span>
              <span role="cell" class="modern-credential-load__num">{{ formatCompact(person.totals.uncachedInputTokens) }}</span>
              <span role="cell" class="modern-credential-load__num">{{ formatCompact(person.totals.outputTokens) }}</span>
              <span role="cell" class="modern-credential-load__num">{{ formatCompact(person.totals.cacheReadTokens) }}</span>
            </div>
          </div>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.modern-usage-overview {
  display: grid;
  gap: 16px;
}

.modern-usage-overview__filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.modern-usage-overview__card {
  display: grid;
  gap: 8px;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  border-radius: 8px;
  padding: 12px;
}

.modern-usage-overview__card h3 {
  margin: 0;
  font-size: 1rem;
  font-weight: 650;
}

.modern-usage-overview__donut {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 16px;
}

.modern-usage-overview__donut svg {
  width: 200px;
  height: 200px;
  flex: none;
}

.modern-usage-overview__legend {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 4px;
  min-width: 220px;
}

.modern-usage-overview__legend li {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.875rem;
}

.modern-usage-overview__name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.modern-usage-overview__swatch {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 2px;
  flex: none;
}

.modern-credential-load__num {
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.modern-usage-overview__scroll {
  overflow-x: auto;
}

.modern-usage-overview__scroll svg {
  width: 100%;
  min-width: 320px;
  max-width: 1200px;
  height: auto;
}

.modern-usage-overview__tick {
  font-size: 9px;
  fill: var(--modern-color-text-muted, inherit);
}

.modern-usage-overview__empty {
  margin: 0;
  color: var(--modern-color-text-muted, inherit);
}

.modern-usage-overview__table {
  display: grid;
  border: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  border-radius: 8px;
  overflow-x: auto;
}

.modern-usage-overview__row {
  display: grid;
  grid-template-columns: 1.4fr repeat(5, 1fr);
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--modern-color-border, rgba(127, 127, 127, 0.2));
  font-size: 0.875rem;
}

.modern-usage-overview__row:last-child {
  border-bottom: 0;
}

.modern-usage-overview__row--head {
  color: var(--modern-color-text-muted, inherit);
  font-weight: 600;
}
</style>
