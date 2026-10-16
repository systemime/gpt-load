<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialLoadHistoryPoint } from '@/app/resources/credential-load'

// 负载页折线图(classic): 与 modern LoadHistoryChart 同构 —— 5 分钟窗三条线,
// 左轴并发/排队, 右轴无感转移(次/秒), 虚线为总容量参考。
// 轴刻度取 1/2/2.5/5×10^k 的整数步长, 悬浮展示时间表头+三行数值卡片。
const props = defineProps<{
  samples: CredentialLoadHistoryPoint[]
  capacity: number
}>()

const { t, locale } = useI18n()

const width = 720
const height = 220
const padLeft = 44
const padRight = 44
const padTop = 14
const padBottom = 26

function niceMax(peak: number, bands = 4): number {
  const raw = Math.max(8, peak * 1.08)
  const exp = 10 ** Math.floor(Math.log10(raw / bands))
  for (const m of [1, 2, 2.5, 5, 10]) {
    const step = m * exp
    if (step * bands >= raw) return step * bands
  }
  return Math.ceil(raw)
}

interface ChartPoint {
  x: number
  y: number
}

const leftMax = computed(() => {
  let peak = 0
  for (const point of props.samples) {
    peak = Math.max(peak, point.inflight, point.queued)
  }
  return niceMax(peak)
})

// 容量高于量程时参考线会压扁整条并发线, 改为图例展示数值。
const capacityVisible = computed(() => props.capacity > 0 && props.capacity <= leftMax.value)

const silentPeak = computed(() => {
  let peak = 0
  for (const point of props.samples) {
    peak = Math.max(peak, point.silent_success, point.silent_failed)
  }
  return peak
})

const rightMax = computed(() => niceMax(Math.max(silentPeak.value, 1)))

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
  return { x, yLeft, yRight, inflight: toPoints((p) => yLeft(p.inflight)), queued: toPoints((p) => yLeft(p.queued)), silentOK: toPoints((p) => yRight(p.silent_success)), silentFail: toPoints((p) => yRight(p.silent_failed)) }
})

const path = (series: ChartPoint[]) =>
  series.length > 1
    ? series.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
    : ''

const inflightPath = computed(() => path(points.value.inflight))
const queuedPath = computed(() => path(points.value.queued))
const silentOKPath = computed(() => path(points.value.silentOK))
const silentFailPath = computed(() => path(points.value.silentFail))
const baselineY = computed(() => points.value.yLeft(0))

const inflightArea = computed(() => {
  const series = points.value.inflight
  if (series.length < 2) return ''
  const line = path(series)
  const last = series[series.length - 1]!
  const first = series[0]!
  return `${line} L${last.x.toFixed(1)},${baselineY.value.toFixed(1)} L${first.x.toFixed(1)},${baselineY.value.toFixed(1)} Z`
})

const capacityY = computed(() => points.value.yLeft(Math.min(props.capacity, leftMax.value)))

const leftTicks = computed(() => {
  const ticks: { value: number; y: number }[] = []
  for (let i = 0; i <= 4; i++) {
    const value = (leftMax.value * i) / 4
    ticks.push({ value, y: points.value.yLeft(value) })
  }
  return ticks
})

const rightTicks = computed(() => {
  if (silentPeak.value === 0) return []
  const ticks: { value: number; y: number }[] = []
  for (let i = 0; i <= 4; i++) {
    const value = (rightMax.value * i) / 4
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

const hoverX = computed(() => (hover.value ? points.value.x(hover.value.at_ms) : 0))
const hoverTopY = computed(() => {
  const point = hover.value
  if (!point) return padTop
  return Math.min(
    points.value.yLeft(point.inflight),
    points.value.yLeft(point.queued),
    Math.min(points.value.yRight(point.silent_success), points.value.yRight(point.silent_failed)),
  )
})

const tooltipStyle = computed(() => {
  const x = hoverX.value
  const left = x < width * 0.16
    ? '2px'
    : x > width * 0.84
      ? 'calc(100% - 2px)'
      : `${(x / width) * 100}%`
  return {
    left,
    top: `calc(${(hoverTopY.value / height) * 100}% - 10px)`,
  }
})

const tooltipAlignment = computed(() => {
  const x = hoverX.value
  if (x < width * 0.16) return 'start'
  if (x > width * 0.84) return 'end'
  return 'center'
})

const hoverDots = computed(() => {
  const point = hover.value
  if (!point) return []
  return [
    { cls: 'credential-load__chart-dot--inflight', cx: hoverX.value, cy: points.value.yLeft(point.inflight) },
    { cls: 'credential-load__chart-dot--queued', cx: hoverX.value, cy: points.value.yLeft(point.queued) },
    { cls: 'credential-load__chart-dot--silent-ok', cx: hoverX.value, cy: points.value.yRight(point.silent_success) },
    { cls: 'credential-load__chart-dot--silent-fail', cx: hoverX.value, cy: points.value.yRight(point.silent_failed) },
  ]
})

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
}

const hoverTime = computed(() => {
  if (!hover.value) return ''
  return new Intl.DateTimeFormat(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(new Date(hover.value.at_ms))
})

const gradientId = `credential-load-fade-${Math.random().toString(36).slice(2, 8)}`
</script>

<template>
  <section class="credential-load__chart">
    <div class="credential-load__chart-legend">
      <span><i class="credential-load__swatch credential-load__swatch--inflight" />{{ t('monitor.credentials.chartInflight') }}</span>
      <span><i class="credential-load__swatch credential-load__swatch--queued" />{{ t('monitor.credentials.chartQueued') }}</span>
      <span><i class="credential-load__swatch credential-load__swatch--silent-ok" />{{ t('monitor.credentials.chartSilentOK') }}({{ t('monitor.credentials.chartRightAxis') }})</span>
      <span><i class="credential-load__swatch credential-load__swatch--silent-fail" />{{ t('monitor.credentials.chartSilentFail') }}({{ t('monitor.credentials.chartRightAxis') }})</span>
      <span><i class="credential-load__swatch credential-load__swatch--capacity" />{{ t('monitor.credentials.chartCapacityLine') }} {{ props.capacity }}</span>
    </div>
    <div class="credential-load__chart-plot">
      <svg
        class="credential-load__chart-svg"
        :viewBox="`0 0 ${width} ${height}`"
        role="img"
        :aria-label="t('monitor.credentials.chartTitle')"
        @mousemove="onMove"
        @mouseleave="hover = null"
      >
        <defs>
          <linearGradient :id="gradientId" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" class="credential-load__chart-fade" stop-opacity="0.22" />
            <stop offset="1" class="credential-load__chart-fade" stop-opacity="0.02" />
          </linearGradient>
        </defs>
        <g v-for="tick in leftTicks" :key="`l${tick.value}`">
          <line :x1="padLeft" :x2="width - padRight" :y1="tick.y" :y2="tick.y" class="credential-load__chart-grid" />
          <text :x="padLeft - 6" :y="tick.y + 4" class="credential-load__chart-tick credential-load__chart-tick--left">{{ tick.value }}</text>
        </g>
        <g v-for="tick in rightTicks" :key="`r${tick.value}`">
          <text :x="width - padRight + 6" :y="tick.y + 4" class="credential-load__chart-tick credential-load__chart-tick--right">{{ tick.value }}</text>
        </g>
        <text v-for="tick in xTicks" :key="tick.label" :x="tick.x" :y="height - 8" class="credential-load__chart-tick">{{ tick.label }}</text>
        <line v-if="capacityVisible" :x1="padLeft" :x2="width - padRight" :y1="capacityY" :y2="capacityY" class="credential-load__chart-capacity" />
        <path v-if="inflightArea" :d="inflightArea" :fill="`url(#${gradientId})`" />
        <path v-if="queuedPath" :d="queuedPath" class="credential-load__chart-line credential-load__chart-line--queued" />
        <path v-if="silentOKPath" :d="silentOKPath" class="credential-load__chart-line credential-load__chart-line--silent-ok" />
        <path v-if="silentFailPath" :d="silentFailPath" class="credential-load__chart-line credential-load__chart-line--silent-fail" />
        <path v-if="inflightPath" :d="inflightPath" class="credential-load__chart-line credential-load__chart-line--inflight" />
        <g v-if="hover">
          <line
            :x1="hoverX" :x2="hoverX" :y1="padTop" :y2="baselineY"
            class="credential-load__chart-guide"
          />
          <circle
            v-for="dot in hoverDots"
            :key="dot.cls"
            :cx="dot.cx" :cy="dot.cy" r="3.5"
            class="credential-load__chart-dot"
            :class="dot.cls"
          />
        </g>
      </svg>
      <Transition name="credential-load__chart-tip">
        <div
          v-if="hover"
          class="credential-load__chart-tooltip"
          :class="`credential-load__chart-tooltip--${tooltipAlignment}`"
          :style="tooltipStyle"
          role="tooltip"
        >
          <time class="credential-load__chart-tooltip-time">{{ hoverTime }}</time>
          <span class="credential-load__chart-tooltip-row">
            <span class="credential-load__chart-tooltip-key">
              <i class="credential-load__swatch credential-load__swatch--inflight" />
              {{ t('monitor.credentials.chartInflight') }}
            </span>
            <strong class="credential-load__chart-tooltip-value">{{ hover.inflight }}</strong>
          </span>
          <span class="credential-load__chart-tooltip-row">
            <span class="credential-load__chart-tooltip-key">
              <i class="credential-load__swatch credential-load__swatch--queued" />
              {{ t('monitor.credentials.chartQueued') }}
            </span>
            <strong class="credential-load__chart-tooltip-value">{{ hover.queued }}</strong>
          </span>
          <span class="credential-load__chart-tooltip-row">
            <span class="credential-load__chart-tooltip-key">
              <i class="credential-load__swatch credential-load__swatch--silent-ok" />
              {{ t('monitor.credentials.chartSilentOK') }}
            </span>
            <strong class="credential-load__chart-tooltip-value">{{ hover.silent_success }}</strong>
          </span>
          <span class="credential-load__chart-tooltip-row">
            <span class="credential-load__chart-tooltip-key">
              <i class="credential-load__swatch credential-load__swatch--silent-fail" />
              {{ t('monitor.credentials.chartSilentFail') }}
            </span>
            <strong class="credential-load__chart-tooltip-value">{{ hover.silent_failed }}</strong>
          </span>
        </div>
      </Transition>
    </div>
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
.credential-load__swatch--silent-ok {
  background: var(--color-success);
}
.credential-load__swatch--silent-fail {
  background: var(--color-danger);
}
.credential-load__swatch--capacity {
  background: transparent;
  border-top: 1px dashed var(--color-text-muted);
  height: 0;
}
.credential-load__chart-plot {
  position: relative;
}
.credential-load__chart-svg {
  display: block;
  width: 100%;
  height: auto;
  cursor: crosshair;
}
.credential-load__chart-fade {
  stop-color: var(--color-info);
}
.credential-load__chart-grid {
  stroke: var(--color-border-subtle);
  stroke-width: 1;
}
.credential-load__chart-tick {
  fill: var(--color-text-muted);
  font-size: 10px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
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
.credential-load__chart-line--silent-ok {
  stroke: var(--color-success);
}
.credential-load__chart-line--silent-fail {
  stroke: var(--color-danger);
  stroke-dasharray: 4 3;
}
.credential-load__chart-guide {
  stroke: var(--color-border-strong);
}
.credential-load__chart-dot {
  stroke: var(--color-surface);
  stroke-width: 2;
}
.credential-load__chart-dot--inflight {
  fill: var(--color-info);
}
.credential-load__chart-dot--queued {
  fill: var(--color-warning);
}
.credential-load__chart-dot--silent-ok {
  fill: var(--color-success);
}
.credential-load__chart-dot--silent-fail {
  fill: var(--color-danger);
}
.credential-load__chart-tooltip {
  position: absolute;
  z-index: 2;
  min-width: 134px;
  display: grid;
  gap: 2px;
  transform: translate(-50%, -100%);
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-surface-raised);
  box-shadow: var(--shadow-chart-tooltip);
  padding: 8px 11px;
  pointer-events: none;
  white-space: nowrap;
}
.credential-load__chart-tooltip--start {
  transform: translate(0, -100%);
}
.credential-load__chart-tooltip--end {
  transform: translate(-100%, -100%);
}
.credential-load__chart-tooltip-time {
  display: block;
  color: var(--color-text-faint);
  font-family: var(--font-mono);
  font-size: 11px;
}
.credential-load__chart-tooltip-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 14px;
  font-size: 12px;
}
.credential-load__chart-tooltip-key {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--color-text-muted);
}
.credential-load__chart-tooltip-value {
  color: var(--color-text);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}
@media (prefers-reduced-motion: no-preference) {
  .credential-load__chart-tip-enter-active,
  .credential-load__chart-tip-leave-active {
    transition: opacity 120ms ease;
  }
  .credential-load__chart-tip-enter-from,
  .credential-load__chart-tip-leave-to {
    opacity: 0;
  }
}
</style>
