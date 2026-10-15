<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialLoadHistoryPoint } from '@modern/api/credential-load'
import { AppSvg, AppTooltip } from '@modern/components/ui'
import { dateFormatter } from '@modern/components/ui/intl-formatters'

// 负载页折线图: 5 分钟滚动窗, 三条线 = 实时并发/排队(左轴) + 无感转移(右轴)。
// 左轴上沿取「容量参考线」与实际峰值的较大者, 保证并发线永远有容量对照。
const props = defineProps<{
  samples: CredentialLoadHistoryPoint[]
  capacity: number
}>()

const { t, locale } = useI18n()

const width = 720
const height = 220
const padLeft = 44
const padRight = 44
const padTop = 12
const padBottom = 26

const leftMax = computed(() => {
  let peak = props.capacity > 0 ? props.capacity : 0
  for (const point of props.samples) {
    peak = Math.max(peak, point.inflight, point.queued)
  }
  return Math.max(10, Math.ceil(peak * 1.15))
})

const rightMax = computed(() => {
  let peak = 1
  for (const point of props.samples) {
    peak = Math.max(peak, point.silentTransfers)
  }
  return Math.max(4, Math.ceil(peak * 1.15))
})

interface ChartPoint { x: number; y: number }

const points = computed(() => {
  const list = props.samples
  const first = list.length > 0 ? list[0]!.atMs : 0
  const span = Math.max(1, list.length > 0 ? list[list.length - 1]!.atMs - first : 1)
  const innerW = width - padLeft - padRight
  const innerH = height - padTop - padBottom
  const x = (atMs: number) =>
    padLeft + (innerW * (atMs - first)) / span
  const yLeft = (value: number) => padTop + innerH * (1 - value / leftMax.value)
  const yRight = (value: number) => padTop + innerH * (1 - value / rightMax.value)
  const toPoints = (pick: (point: CredentialLoadHistoryPoint) => number): ChartPoint[] =>
    list.map((point) => ({ x: x(point.atMs), y: pick(point) }))
  return {
    x,
    yLeft,
    yRight,
    inflight: toPoints((point) => yLeft(point.inflight)),
    queued: toPoints((point) => yLeft(point.queued)),
    silent: toPoints((point) => yRight(point.silentTransfers)),
  }
})

const path = (series: ChartPoint[]) =>
  series.length > 1
    ? series.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
    : ''

const inflightPath = computed(() => path(points.value.inflight))
const queuedPath = computed(() => path(points.value.queued))
const silentPath = computed(() => path(points.value.silent))

const capacityY = computed(() => points.value.yLeft(props.capacity))

const leftTicks = computed(() => {
  const ticks: { value: number; y: number }[] = []
  for (let i = 0; i <= 4; i++) {
    const value = Math.round((leftMax.value * i) / 4)
    ticks.push({ value, y: points.value.yLeft(value) })
  }
  return ticks
})

const rightTicks = computed(() => {
  const ticks: { value: number; y: number }[] = []
  for (let i = 0; i <= 4; i++) {
    const value = Math.round((rightMax.value * i) / 4)
    ticks.push({ value, y: points.value.yRight(value) })
  }
  return ticks
})

const xTicks = computed(() => {
  const list = props.samples
  if (list.length === 0) return []
  const formatter = dateFormatter(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' })
  const ticks: { label: string; x: number }[] = []
  const stepSec = 60
  const seen = new Set<number>()
  for (const point of list) {
    const second = Math.floor(point.atMs / 1000)
    if (second % stepSec !== 0 || seen.has(second)) continue
    seen.add(second)
    ticks.push({
      label: formatter.format(point.atMs),
      x: points.value.x(point.atMs),
    })
  }
  return ticks
})

const hover = ref<CredentialLoadHistoryPoint | null>(null)
const hoverPos = ref({ x: 0, y: 0 })

function onMove(event: MouseEvent): void {
  const list = props.samples
  if (list.length === 0) {
    hover.value = null
    return
  }
  const target = event.currentTarget as SVGSVGElement
  const rect = target.getBoundingClientRect()
  const svgX = ((event.clientX - rect.left) / rect.width) * width
  const inner = points.value
  let nearest: CredentialLoadHistoryPoint | null = null
  let best = Number.POSITIVE_INFINITY
  for (const point of list) {
    const distance = Math.abs(inner.x(point.atMs) - svgX)
    if (distance < best) {
      best = distance
      nearest = point
    }
  }
  hover.value = nearest
  hoverPos.value = {
    x: inner.x(nearest!.atMs),
    y: inner.yLeft(nearest!.inflight),
  }
}

const hoverLabel = computed(() => {
  const point = hover.value
  if (!point) return ''
  const time = dateFormatter(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(point.atMs)
  return `${time} · ${t('credentialLoad.chartInflight')} ${point.inflight} · ${t('credentialLoad.chartQueued')} ${point.queued} · ${t('credentialLoad.chartSilent')} ${point.silentTransfers}`
})
</script>

<template>
  <section class="modern-load-chart">
    <div class="modern-load-chart__legend">
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--inflight" />{{ t('credentialLoad.chartInflight') }}</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--queued" />{{ t('credentialLoad.chartQueued') }}</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--silent" />{{ t('credentialLoad.chartSilent') }}({{ t('credentialLoad.chartRightAxis') }})</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--capacity" />{{ t('credentialLoad.chartCapacityLine') }}</span>
    </div>
    <div class="modern-load-chart__plot">
      <AppTooltip v-if="hover" :label="hoverLabel">
        <span
          class="modern-load-chart__cursor"
          :style="{ left: `${(hoverPos.x / width) * 100}%`, top: `${(hoverPos.y / height) * 100}%` }"
        />
      </AppTooltip>
      <AppSvg
        :viewBox="`0 0 ${width} ${height}`"
        class="modern-load-chart__svg"
        role="img"
        :aria-label="t('credentialLoad.chartTitle')"
        @mousemove="onMove"
        @mouseleave="hover = null"
      >
        <g v-for="tick in leftTicks" :key="`l${tick.value}`">
          <line :x1="padLeft" :x2="width - padRight" :y1="tick.y" :y2="tick.y" class="modern-load-chart__grid" />
          <text :x="padLeft - 6" :y="tick.y + 4" class="modern-load-chart__tick modern-load-chart__tick--left">{{ tick.value }}</text>
        </g>
        <g v-for="tick in rightTicks" :key="`r${tick.value}`">
          <text :x="width - padRight + 6" :y="tick.y + 4" class="modern-load-chart__tick modern-load-chart__tick--right">{{ tick.value }}</text>
        </g>
        <text v-for="tick in xTicks" :key="tick.label" :x="tick.x" :y="height - 8" class="modern-load-chart__tick">{{ tick.label }}</text>
        <line
          :x1="padLeft" :x2="width - padRight" :y1="capacityY" :y2="capacityY"
          class="modern-load-chart__capacity"
        />
        <path v-if="queuedPath" :d="queuedPath" class="modern-load-chart__line modern-load-chart__line--queued" />
        <path v-if="silentPath" :d="silentPath" class="modern-load-chart__line modern-load-chart__line--silent" />
        <path v-if="inflightPath" :d="inflightPath" class="modern-load-chart__line modern-load-chart__line--inflight" />
      </AppSvg>
    </div>
    <p v-if="samples.length === 0" class="modern-load-chart__empty">{{ t('credentialLoad.chartEmpty') }}</p>
  </section>
</template>

<style scoped>
.modern-load-chart {
  display: grid;
  gap: var(--modern-space-2);
  border: var(--modern-line-width) solid var(--modern-border);
  border-radius: var(--modern-radius-panel);
  background: var(--modern-surface);
  padding: var(--modern-space-3) var(--modern-space-4);
}
.modern-load-chart__legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--modern-space-3);
  font-size: var(--modern-font-size-small);
  color: var(--modern-muted);
}
.modern-load-chart__legend-item {
  display: inline-flex;
  align-items: center;
  gap: var(--modern-space-1);
}
.modern-load-chart__swatch {
  width: var(--modern-space-2);
  height: var(--modern-space-1);
  border-radius: var(--modern-radius-full);
}
.modern-load-chart__swatch--inflight {
  background: var(--modern-chart-input);
}
.modern-load-chart__swatch--queued {
  background: var(--modern-warning);
}
.modern-load-chart__swatch--silent {
  background: var(--modern-danger);
}
.modern-load-chart__swatch--capacity {
  background: transparent;
  border-top: var(--modern-line-width) dashed var(--modern-muted);
  height: 0;
}
.modern-load-chart__plot {
  position: relative;
}
.modern-load-chart__svg {
  display: block;
  width: 100%;
  height: auto;
}
.modern-load-chart__grid {
  stroke: var(--modern-border);
  stroke-width: 1;
}
.modern-load-chart__tick {
  fill: var(--modern-muted);
  font-size: 10px;
  text-anchor: middle;
}
.modern-load-chart__tick--left {
  text-anchor: end;
}
.modern-load-chart__tick--right {
  text-anchor: start;
}
.modern-load-chart__capacity {
  stroke: var(--modern-muted);
  stroke-dasharray: 6 4;
  stroke-width: 1;
}
.modern-load-chart__line {
  fill: none;
  stroke-width: 2;
  stroke-linejoin: round;
  stroke-linecap: round;
}
.modern-load-chart__line--inflight {
  stroke: var(--modern-chart-input);
}
.modern-load-chart__line--queued {
  stroke: var(--modern-warning);
}
.modern-load-chart__line--silent {
  stroke: var(--modern-danger);
  stroke-dasharray: 4 3;
}
.modern-load-chart__cursor {
  position: absolute;
  width: 8px;
  height: 8px;
  border-radius: var(--modern-radius-full);
  background: var(--modern-chart-input);
  transform: translate(-50%, -50%);
  pointer-events: none;
}
.modern-load-chart__empty {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
</style>
