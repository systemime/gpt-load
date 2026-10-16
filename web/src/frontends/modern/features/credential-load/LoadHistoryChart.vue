<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialLoadHistoryPoint } from '@modern/api/credential-load'
import { AppSvg } from '@modern/components/ui'
import { dateFormatter } from '@modern/components/ui/intl-formatters'

// 负载页折线图: 5 分钟滚动窗, 三条线 = 实时并发/排队(左轴) + 无感转移(右轴)。
// 左轴随实际负载峰值自适应(不硬塞容量上限), 容量在量程内时才画参考线,
// 超出量程则在图例里直接给数值。轴刻度取 1/2/2.5/5×10^k 的整数步长。
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

// 把峰值向上取整到 bands 段的整齐刻度上限。
function niceMax(peak: number, bands = 4): number {
  const raw = Math.max(8, peak * 1.08)
  const exp = 10 ** Math.floor(Math.log10(raw / bands))
  for (const m of [1, 2, 2.5, 5, 10]) {
    const step = m * exp
    if (step * bands >= raw) return step * bands
  }
  return Math.ceil(raw)
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
    peak = Math.max(peak, point.silentSuccess, point.silentFailed)
  }
  return peak
})

const rightMax = computed(() => niceMax(Math.max(silentPeak.value, 1)))

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
    silentOK: toPoints((point) => yRight(point.silentSuccess)),
    silentFail: toPoints((point) => yRight(point.silentFailed)),
  }
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

const hoverX = computed(() => (hover.value ? points.value.x(hover.value.atMs) : 0))
// 悬浮点在三条线中的最高处, 提示卡片挂在它上方。
const hoverTopY = computed(() => {
  const point = hover.value
  if (!point) return padTop
  return Math.min(
    points.value.yLeft(point.inflight),
    points.value.yLeft(point.queued),
    points.value.yRight(point.silentSuccess),
    points.value.yRight(point.silentFailed),
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
    { cls: 'modern-load-chart__dot--inflight', cx: hoverX.value, cy: points.value.yLeft(point.inflight) },
    { cls: 'modern-load-chart__dot--queued', cx: hoverX.value, cy: points.value.yLeft(point.queued) },
    { cls: 'modern-load-chart__dot--silent-ok', cx: hoverX.value, cy: points.value.yRight(point.silentSuccess) },
    { cls: 'modern-load-chart__dot--silent-fail', cx: hoverX.value, cy: points.value.yRight(point.silentFailed) },
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
}

const hoverTime = computed(() => {
  if (!hover.value) return ''
  return dateFormatter(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(hover.value.atMs)
})

const gradientId = `load-history-fade-${Math.random().toString(36).slice(2, 8)}`
</script>

<template>
  <section class="modern-load-chart">
    <div class="modern-load-chart__legend">
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--inflight" />{{ t('credentialLoad.chartInflight') }}</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--queued" />{{ t('credentialLoad.chartQueued') }}</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--silent-ok" />{{ t('credentialLoad.chartSilentOK') }}({{ t('credentialLoad.chartRightAxis') }})</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--silent-fail" />{{ t('credentialLoad.chartSilentFail') }}({{ t('credentialLoad.chartRightAxis') }})</span>
      <span class="modern-load-chart__legend-item"><i class="modern-load-chart__swatch modern-load-chart__swatch--capacity" />{{ t('credentialLoad.chartCapacityLine') }} {{ props.capacity }}</span>
    </div>
    <div class="modern-load-chart__plot">
      <AppSvg
        :viewBox="`0 0 ${width} ${height}`"
        class="modern-load-chart__svg"
        role="img"
        :aria-label="t('credentialLoad.chartTitle')"
        @mousemove="onMove"
        @mouseleave="hover = null"
      >
        <defs>
          <linearGradient :id="gradientId" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" class="modern-load-chart__fade" stop-opacity="0.22" />
            <stop offset="1" class="modern-load-chart__fade" stop-opacity="0.02" />
          </linearGradient>
        </defs>
        <g v-for="tick in leftTicks" :key="`l${tick.value}`">
          <line :x1="padLeft" :x2="width - padRight" :y1="tick.y" :y2="tick.y" class="modern-load-chart__grid" />
          <text :x="padLeft - 6" :y="tick.y + 4" class="modern-load-chart__tick modern-load-chart__tick--left">{{ tick.value }}</text>
        </g>
        <g v-for="tick in rightTicks" :key="`r${tick.value}`">
          <text :x="width - padRight + 6" :y="tick.y + 4" class="modern-load-chart__tick modern-load-chart__tick--right">{{ tick.value }}</text>
        </g>
        <text v-for="tick in xTicks" :key="tick.label" :x="tick.x" :y="height - 8" class="modern-load-chart__tick">{{ tick.label }}</text>
        <line
          v-if="capacityVisible"
          :x1="padLeft" :x2="width - padRight" :y1="capacityY" :y2="capacityY"
          class="modern-load-chart__capacity"
        />
        <path v-if="inflightArea" :d="inflightArea" :fill="`url(#${gradientId})`" />
        <path v-if="queuedPath" :d="queuedPath" class="modern-load-chart__line modern-load-chart__line--queued" />
        <path v-if="silentOKPath" :d="silentOKPath" class="modern-load-chart__line modern-load-chart__line--silent-ok" />
        <path v-if="silentFailPath" :d="silentFailPath" class="modern-load-chart__line modern-load-chart__line--silent-fail" />
        <path v-if="inflightPath" :d="inflightPath" class="modern-load-chart__line modern-load-chart__line--inflight" />
        <g v-if="hover">
          <line
            :x1="hoverX" :x2="hoverX" :y1="padTop" :y2="baselineY"
            class="modern-load-chart__guide"
          />
          <circle
            v-for="dot in hoverDots"
            :key="dot.cls"
            :cx="dot.cx" :cy="dot.cy" r="3.5"
            class="modern-load-chart__dot"
            :class="dot.cls"
          />
        </g>
      </AppSvg>
      <div
        v-if="hover"
        class="modern-load-chart__tooltip"
        :class="`modern-load-chart__tooltip--${tooltipAlignment}`"
        :style="tooltipStyle"
        role="tooltip"
      >
          <time class="modern-load-chart__tooltip-time">{{ hoverTime }}</time>
          <span class="modern-load-chart__tooltip-row">
            <span class="modern-load-chart__tooltip-key">
              <i class="modern-load-chart__swatch modern-load-chart__swatch--inflight" />
              {{ t('credentialLoad.chartInflight') }}
            </span>
            <strong class="modern-load-chart__tooltip-value">{{ hover.inflight }}</strong>
          </span>
          <span class="modern-load-chart__tooltip-row">
            <span class="modern-load-chart__tooltip-key">
              <i class="modern-load-chart__swatch modern-load-chart__swatch--queued" />
              {{ t('credentialLoad.chartQueued') }}
            </span>
            <strong class="modern-load-chart__tooltip-value">{{ hover.queued }}</strong>
          </span>
          <span class="modern-load-chart__tooltip-row">
            <span class="modern-load-chart__tooltip-key">
              <i class="modern-load-chart__swatch modern-load-chart__swatch--silent-ok" />
              {{ t('credentialLoad.chartSilentOK') }}
            </span>
            <strong class="modern-load-chart__tooltip-value">{{ hover.silentSuccess }}</strong>
          </span>
          <span class="modern-load-chart__tooltip-row">
            <span class="modern-load-chart__tooltip-key">
              <i class="modern-load-chart__swatch modern-load-chart__swatch--silent-fail" />
              {{ t('credentialLoad.chartSilentFail') }}
            </span>
            <strong class="modern-load-chart__tooltip-value">{{ hover.silentFailed }}</strong>
          </span>
        </div>
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
  border-radius: var(--modern-radius-round);
}
.modern-load-chart__swatch--inflight {
  background: var(--modern-chart-input);
}
.modern-load-chart__swatch--queued {
  background: var(--modern-warning);
}
.modern-load-chart__swatch--silent-ok {
  background: var(--modern-chart-cache);
}
.modern-load-chart__swatch--silent-fail {
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
  cursor: crosshair;
}
.modern-load-chart__fade {
  stop-color: var(--modern-chart-input);
}
.modern-load-chart__grid {
  stroke: var(--modern-border);
  stroke-width: var(--modern-line-width);
}
.modern-load-chart__tick {
  fill: var(--modern-muted);
  font-size: var(--modern-font-size-caption);
  font-family: var(--modern-font-mono);
  font-variant-numeric: tabular-nums;
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
  stroke-width: var(--modern-line-width);
}
.modern-load-chart__line {
  fill: none;
  stroke-width: var(--modern-trend-stroke);
  stroke-linejoin: round;
  stroke-linecap: round;
}
.modern-load-chart__line--inflight {
  stroke: var(--modern-chart-input);
}
.modern-load-chart__line--queued {
  stroke: var(--modern-warning);
}
.modern-load-chart__line--silent-ok {
  stroke: var(--modern-chart-cache);
}
.modern-load-chart__line--silent-fail {
  stroke: var(--modern-danger);
  stroke-dasharray: 4 3;
}
.modern-load-chart__guide {
  stroke: var(--modern-tooltip-border);
}
.modern-load-chart__dot {
  stroke: var(--modern-surface);
  stroke-width: var(--modern-line-width);
}
.modern-load-chart__dot--inflight {
  fill: var(--modern-chart-input);
}
.modern-load-chart__dot--queued {
  fill: var(--modern-warning);
}
.modern-load-chart__dot--silent-ok {
  fill: var(--modern-chart-cache);
}
.modern-load-chart__dot--silent-fail {
  fill: var(--modern-danger);
}
.modern-load-chart__tooltip {
  position: absolute;
  z-index: var(--modern-layer-tooltip, 60);
  min-width: 150px;
  display: grid;
  gap: var(--modern-space-1);
  transform: translate(-50%, -100%);
  border: var(--modern-line-width) solid var(--modern-tooltip-border);
  border-radius: var(--modern-radius-small);
  background: var(--modern-tooltip-surface);
  box-shadow: var(--modern-shadow-tooltip);
  padding: var(--modern-space-2) var(--modern-space-3);
  pointer-events: none;
  white-space: nowrap;
}
.modern-load-chart__tooltip--start {
  transform: translate(0, -100%);
}
.modern-load-chart__tooltip--end {
  transform: translate(-100%, -100%);
}
.modern-load-chart__tooltip-time {
  color: var(--modern-muted);
  font-family: var(--modern-font-mono);
  font-size: var(--modern-font-size-caption);
}
.modern-load-chart__tooltip-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--modern-space-4);
  font-size: var(--modern-font-size-small);
}
.modern-load-chart__tooltip-key {
  display: inline-flex;
  align-items: center;
  gap: var(--modern-space-1);
  color: var(--modern-muted);
}
.modern-load-chart__tooltip-value {
  color: var(--modern-text);
  font-family: var(--modern-font-mono);
  font-variant-numeric: tabular-nums;
  font-weight: var(--modern-weight-semibold);
}
.modern-load-chart__empty {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
</style>
