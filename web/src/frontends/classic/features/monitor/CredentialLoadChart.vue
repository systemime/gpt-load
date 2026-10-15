<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialLoadHistoryPoint } from '@/app/resources/credential-load'

// 负载页折线图(classic): 与 modern LoadHistoryChart 同构 —— 5 分钟窗三条线,
// 左轴并发/排队, 右轴无感转移(次/秒), 虚线为总容量参考。
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

interface ChartPoint {
  x: number
  y: number
}

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
    peak = Math.max(peak, point.silent_transfers)
  }
  return Math.max(4, Math.ceil(peak * 1.15))
})

const points = computed(() => {
  const list = props.samples
  const first = list.length > 0 ? list[0]!.at_ms : 0
  const span = Math.max(1, list.length > 0 ? list[list.length - 1]!.at_ms - first : 1)
  const innerW = width - padLeft - padRight
  const innerH = height - padTop - padBottom
  const x = (atMs: number) => padLeft + (innerW * (atMs - first)) / span
  const yLeft = (value: number) => padTop + innerH * (1 - value / leftMax.value)
  const yRight = (value: number) => padTop + innerH * (1 - value / rightMax.value)
  const toPoints = (pick: (point: CredentialLoadHistoryPoint) => number): ChartPoint[] =>
    list.map((point) => ({ x: x(point.at_ms), y: pick(point) }))
  return { x, yLeft, yRight, inflight: toPoints((p) => yLeft(p.inflight)), queued: toPoints((p) => yLeft(p.queued)), silent: toPoints((p) => yRight(p.silent_transfers)) }
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
  const formatter = new Intl.DateTimeFormat(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' })
  const ticks: { label: string; x: number }[] = []
  const seen = new Set<number>()
  for (const point of list) {
    const second = Math.floor(point.at_ms / 1000)
    if (second % 60 !== 0 || seen.has(second)) continue
    seen.add(second)
    ticks.push({ label: formatter.format(new Date(point.at_ms)), x: points.value.x(point.at_ms) })
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
  let nearest: CredentialLoadHistoryPoint | null = null
  let best = Number.POSITIVE_INFINITY
  for (const point of list) {
    const distance = Math.abs(points.value.x(point.at_ms) - svgX)
    if (distance < best) {
      best = distance
      nearest = point
    }
  }
  hover.value = nearest
  hoverPos.value = { x: points.value.x(nearest!.at_ms), y: points.value.yLeft(nearest!.inflight) }
}

const hoverLabel = computed(() => {
  const point = hover.value
  if (!point) return ''
  const time = new Intl.DateTimeFormat(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(new Date(point.at_ms))
  return `${time} · ${t('monitor.credentials.chartInflight')} ${point.inflight} · ${t('monitor.credentials.chartQueued')} ${point.queued} · ${t('monitor.credentials.chartSilent')} ${point.silent_transfers}`
})
</script>

<template>
  <section class="credential-load__chart">
    <div class="credential-load__chart-legend">
      <span><i class="credential-load__swatch credential-load__swatch--inflight" />{{ t('monitor.credentials.chartInflight') }}</span>
      <span><i class="credential-load__swatch credential-load__swatch--queued" />{{ t('monitor.credentials.chartQueued') }}</span>
      <span><i class="credential-load__swatch credential-load__swatch--silent" />{{ t('monitor.credentials.chartSilent') }}({{ t('monitor.credentials.chartRightAxis') }})</span>
      <span><i class="credential-load__swatch credential-load__swatch--capacity" />{{ t('monitor.credentials.chartCapacityLine') }}</span>
      <AppTooltip v-if="hover" :label="hoverLabel">
        <span class="credential-load__chart-hint">{{ t('monitor.credentials.chartHover') }}</span>
      </AppTooltip>
    </div>
    <svg
      class="credential-load__chart-svg"
      :viewBox="`0 0 ${width} ${height}`"
      role="img"
      :aria-label="t('monitor.credentials.chartTitle')"
      @mousemove="onMove"
      @mouseleave="hover = null"
    >
      <g v-for="tick in leftTicks" :key="`l${tick.value}`">
        <line :x1="padLeft" :x2="width - padRight" :y1="tick.y" :y2="tick.y" class="credential-load__chart-grid" />
        <text :x="padLeft - 6" :y="tick.y + 4" class="credential-load__chart-tick credential-load__chart-tick--left">{{ tick.value }}</text>
      </g>
      <g v-for="tick in rightTicks" :key="`r${tick.value}`">
        <text :x="width - padRight + 6" :y="tick.y + 4" class="credential-load__chart-tick credential-load__chart-tick--right">{{ tick.value }}</text>
      </g>
      <text v-for="tick in xTicks" :key="tick.label" :x="tick.x" :y="height - 8" class="credential-load__chart-tick">{{ tick.label }}</text>
      <line :x1="padLeft" :x2="width - padRight" :y1="capacityY" :y2="capacityY" class="credential-load__chart-capacity" />
      <path v-if="queuedPath" :d="queuedPath" class="credential-load__chart-line credential-load__chart-line--queued" />
      <path v-if="silentPath" :d="silentPath" class="credential-load__chart-line credential-load__chart-line--silent" />
      <path v-if="inflightPath" :d="inflightPath" class="credential-load__chart-line credential-load__chart-line--inflight" />
    </svg>
    <p v-if="samples.length === 0" class="credential-load__empty">{{ t('monitor.credentials.chartEmpty') }}</p>
  </section>
</template>

<style scoped>
.credential-load__chart {
  display: grid;
  gap: 8px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  padding: 12px 16px;
}
.credential-load__chart-legend {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
.credential-load__chart-legend > span {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.credential-load__swatch {
  width: 10px;
  height: 3px;
  border-radius: 999px;
  display: inline-block;
}
.credential-load__swatch--inflight {
  background: var(--color-info);
}
.credential-load__swatch--queued {
  background: var(--color-warning);
}
.credential-load__swatch--silent {
  background: var(--color-danger);
}
.credential-load__swatch--capacity {
  background: transparent;
  border-top: 1px dashed var(--color-text-muted);
  height: 0;
}
.credential-load__chart-hint {
  color: var(--color-text-muted);
}
.credential-load__chart-svg {
  display: block;
  width: 100%;
  height: auto;
}
.credential-load__chart-grid {
  stroke: var(--color-border-subtle);
  stroke-width: 1;
}
.credential-load__chart-tick {
  fill: var(--color-text-muted);
  font-size: 10px;
  text-anchor: middle;
}
.credential-load__chart-tick--left {
  text-anchor: end;
}
.credential-load__chart-tick--right {
  text-anchor: start;
}
.credential-load__chart-capacity {
  stroke: var(--color-text-muted);
  stroke-dasharray: 6 4;
  stroke-width: 1;
}
.credential-load__chart-line {
  fill: none;
  stroke-width: 2;
  stroke-linejoin: round;
  stroke-linecap: round;
}
.credential-load__chart-line--inflight {
  stroke: var(--color-info);
}
.credential-load__chart-line--queued {
  stroke: var(--color-warning);
}
.credential-load__chart-line--silent {
  stroke: var(--color-danger);
  stroke-dasharray: 4 3;
}
</style>
