<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { routeStrategies } from '@/api/control/types'
import type { RuntimeSettingKey, SettingsResource } from '@/app/resources/settings'
import { glmCodingPlans } from '@/app/resources/settings'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import CompactFieldError from '@/components/ui/CompactFieldError.vue'
import SegmentedControl, { type SegmentedControlOption } from '@/components/ui/SegmentedControl.vue'
import { formatInteger } from '@/lib/format'

import SettingRow from '@/components/config/SettingRow.vue'
import {
  createSettingsDraft,
  isValidAffinityCapacity,
  isValidCredentialConcurrencyLimit,
  isValidCredentialQueueTimeout,
  isValidTimeout,
  setSettingsOverride,
  type SettingsDraft,
} from './settings-patch'
import type { SettingsDraftChange } from './use-settings-controller'

const props = defineProps<{
  base: SettingsResource
  draft: SettingsDraft
  disabled: boolean
}>()
const emit = defineEmits<{
  change: [change: SettingsDraftChange]
}>()
const { locale, t } = useI18n()
const numericKeys = ['affinity_ttl', 'affinity_capacity', 'credential_concurrency_limit', 'credential_queue_timeout', 'glm_week_reset_anchor_ms'] as const
const numericUnits: Record<(typeof numericKeys)[number], string> = {
  affinity_ttl: 'settings.runtime.seconds',
  affinity_capacity: 'settings.affinity.entries',
  credential_concurrency_limit: 'settings.affinity.requests',
  credential_queue_timeout: 'settings.runtime.seconds',
  glm_week_reset_anchor_ms: 'settings.runtime.milliseconds',
}
const numericMaximums: Record<(typeof numericKeys)[number], string> = {
  affinity_ttl: '9223372036',
  affinity_capacity: '1000000',
  credential_concurrency_limit: '64',
  credential_queue_timeout: '600',
  glm_week_reset_anchor_ms: '9007199254740991',
}
const numericMinimums: Record<(typeof numericKeys)[number], string> = {
  affinity_ttl: '1',
  affinity_capacity: '1',
  credential_concurrency_limit: '0',
  credential_queue_timeout: '1',
  glm_week_reset_anchor_ms: '0',
}
const routeStrategyOptions = computed<SegmentedControlOption[]>(() =>
  routeStrategies.map((value) => ({
    value,
    label: t(`settings.runtime.routeStrategies.${value}`),
    disabled: props.disabled,
  })),
)
const enabledValue = computed(() =>
  props.base.settings.values.affinity_enabled
    ? t('settings.runtime.enabled')
    : t('settings.runtime.disabled'),
)

function cloneDraft(): SettingsDraft {
  return createSettingsDraft({
    values: props.draft.values,
    overrides: [...props.draft.overrides],
    read_only: [...props.draft.readOnly],
  })
}

function publish(key: RuntimeSettingKey, draft: SettingsDraft): void {
  emit('change', { key, draft })
}

function hasOverride(key: RuntimeSettingKey): boolean {
  return props.draft.overrides.has(key)
}

function isPendingRestore(key: RuntimeSettingKey): boolean {
  return !hasOverride(key) && props.base.settings.overrides.includes(key)
}

function toggleOverride(key: RuntimeSettingKey): void {
  publish(key, setSettingsOverride(props.base.settings, props.draft, key, !hasOverride(key)))
}

function sourceLabel(key: RuntimeSettingKey): string {
  if (hasOverride(key)) return t('settings.runtime.overrideSource')
  if (isPendingRestore(key)) return t('settings.runtime.pendingRestoreSource')
  return t('settings.runtime.defaultSource')
}

function actionLabel(key: RuntimeSettingKey): string {
  return hasOverride(key) ? t('settings.runtime.restoreDefault') : t('settings.runtime.override')
}

function setRouteStrategy(value: string): void {
  const strategy = routeStrategies.find((candidate) => candidate === value)
  if (strategy === undefined) return
  const draft = cloneDraft()
  draft.values.route_strategy = strategy
  publish('route_strategy', draft)
}

function setEnabled(value: boolean): void {
  const draft = cloneDraft()
  draft.values.affinity_enabled = value
  publish('affinity_enabled', draft)
}

const queueEnabledValue = computed(() =>
  props.base.settings.values.credential_queue_enabled
    ? t('settings.runtime.enabled')
    : t('settings.runtime.disabled'),
)

function setQueueEnabled(value: boolean): void {
  const draft = cloneDraft()
  draft.values.credential_queue_enabled = value
  publish('credential_queue_enabled', draft)
}

const usageBalanceValue = computed(() =>
  props.base.settings.values.credential_usage_balance_enabled
    ? t('settings.runtime.enabled')
    : t('settings.runtime.disabled'),
)

function setUsageBalance(value: boolean): void {
  const draft = cloneDraft()
  draft.values.credential_usage_balance_enabled = value
  publish('credential_usage_balance_enabled', draft)
}

const glmCodingPlanOptions = computed<SegmentedControlOption[]>(() =>
  glmCodingPlans.map((plan) => ({
    value: plan,
    label: t(`settings.runtime.glmPlans.${plan}`),
    disabled: props.disabled,
  })),
)

function setGlmCodingPlan(value: string): void {
  if (value !== 'standard' && value !== 'premium') return
  const draft = cloneDraft()
  draft.values.glm_coding_plan = value
  publish('glm_coding_plan', draft)
}

function setNumber(key: (typeof numericKeys)[number], value: string): void {
  const draft = cloneDraft()
  draft.values[key] = value.trim() === '' ? Number.NaN : Number(value)
  publish(key, draft)
}

function numberFieldError(key: (typeof numericKeys)[number]): string | undefined {
  if (!hasOverride(key)) return undefined
  const valid =
    key === 'affinity_ttl'
      ? isValidTimeout(props.draft.values[key])
      : key === 'affinity_capacity'
        ? isValidAffinityCapacity(props.draft.values[key])
        : key === 'credential_concurrency_limit'
          ? isValidCredentialConcurrencyLimit(props.draft.values[key])
          : key === 'glm_week_reset_anchor_ms'
            ? Number.isInteger(props.draft.values[key]) && props.draft.values[key] >= 0
            : isValidCredentialQueueTimeout(props.draft.values[key])
  return valid ? undefined : t(`settings.affinity.${key}Error`)
}

function numberFieldValue(key: (typeof numericKeys)[number]): string {
  if (isPendingRestore(key)) return t('settings.runtime.resetPending')
  return t(`settings.affinity.${key}Effective`, {
    value: formatInteger(props.base.settings.values[key], locale.value),
  })
}
</script>

<template>
  <section id="settings-routing" class="settings-section" tabindex="-1">
    <header class="settings-section__heading">
      <h2>{{ t('settings.affinity.title') }}</h2>
      <p>{{ t('settings.affinity.description') }}</p>
    </header>

    <div class="settings-routing__rows">
      <SettingRow
        :label="t('settings.runtime.route_strategy')"
        :value="
          isPendingRestore('route_strategy')
            ? t('settings.runtime.resetPending')
            : t(`settings.runtime.routeStrategies.${base.settings.values.route_strategy}`)
        "
        :help="t('settings.runtime.routeStrategyHelp')"
        :source-label="sourceLabel('route_strategy')"
        :action-label="actionLabel('route_strategy')"
        :overridden="hasOverride('route_strategy')"
        :pending-restore="isPendingRestore('route_strategy')"
        :disabled="disabled"
        @toggle="toggleOverride('route_strategy')"
      >
        <template #control>
          <SegmentedControl
            :model-value="draft.values.route_strategy"
            :options="routeStrategyOptions"
            :label="t('settings.runtime.route_strategy')"
            size="compact"
            @update:model-value="setRouteStrategy"
          />
        </template>
      </SettingRow>

      <SettingRow
        :label="t('settings.affinity.affinity_enabled')"
        :value="
          isPendingRestore('affinity_enabled') ? t('settings.runtime.resetPending') : enabledValue
        "
        :help="t('settings.affinity.enabledHelp')"
        :source-label="sourceLabel('affinity_enabled')"
        :action-label="actionLabel('affinity_enabled')"
        :overridden="hasOverride('affinity_enabled')"
        :pending-restore="isPendingRestore('affinity_enabled')"
        :disabled="disabled"
        @toggle="toggleOverride('affinity_enabled')"
      >
        <template #control>
          <AppSwitch
            :model-value="draft.values.affinity_enabled"
            :disabled="disabled"
            :label="t('settings.affinity.affinity_enabled')"
            @update:model-value="setEnabled"
          />
        </template>
      </SettingRow>

      <SettingRow
        :label="t('settings.affinity.credential_queue_enabled')"
        :value="
          isPendingRestore('credential_queue_enabled')
            ? t('settings.runtime.resetPending')
            : queueEnabledValue
        "
        :help="t('settings.affinity.credential_queue_enabledHelp')"
        :source-label="sourceLabel('credential_queue_enabled')"
        :action-label="actionLabel('credential_queue_enabled')"
        :overridden="hasOverride('credential_queue_enabled')"
        :pending-restore="isPendingRestore('credential_queue_enabled')"
        :disabled="disabled"
        @toggle="toggleOverride('credential_queue_enabled')"
      >
        <template #control>
          <AppSwitch
            :model-value="draft.values.credential_queue_enabled"
            :disabled="disabled"
            :label="t('settings.affinity.credential_queue_enabled')"
            @update:model-value="setQueueEnabled"
          />
        </template>
      </SettingRow>

      <SettingRow
        :label="t('settings.affinity.credential_usage_balance_enabled')"
        :value="
          isPendingRestore('credential_usage_balance_enabled')
            ? t('settings.runtime.resetPending')
            : usageBalanceValue
        "
        :help="t('settings.affinity.credential_usage_balance_enabledHelp')"
        :source-label="sourceLabel('credential_usage_balance_enabled')"
        :action-label="actionLabel('credential_usage_balance_enabled')"
        :overridden="hasOverride('credential_usage_balance_enabled')"
        :pending-restore="isPendingRestore('credential_usage_balance_enabled')"
        :disabled="disabled"
        @toggle="toggleOverride('credential_usage_balance_enabled')"
      >
        <template #control>
          <AppSwitch
            :model-value="draft.values.credential_usage_balance_enabled"
            :disabled="disabled"
            :label="t('settings.affinity.credential_usage_balance_enabled')"
            @update:model-value="setUsageBalance"
          />
        </template>
      </SettingRow>

      <SettingRow
        :label="t('settings.runtime.glm_coding_plan')"
        :value="
          isPendingRestore('glm_coding_plan')
            ? t('settings.runtime.resetPending')
            : t(`settings.runtime.glmPlans.${base.settings.values.glm_coding_plan}`)
        "
        :help="t('settings.runtime.glm_coding_planHelp')"
        :source-label="sourceLabel('glm_coding_plan')"
        :action-label="actionLabel('glm_coding_plan')"
        :overridden="hasOverride('glm_coding_plan')"
        :pending-restore="isPendingRestore('glm_coding_plan')"
        :disabled="disabled"
        @toggle="toggleOverride('glm_coding_plan')"
      >
        <template #control>
          <SegmentedControl
            :model-value="draft.values.glm_coding_plan"
            :options="glmCodingPlanOptions"
            :label="t('settings.runtime.glm_coding_plan')"
            size="compact"
            @update:model-value="setGlmCodingPlan"
          />
        </template>
      </SettingRow>

      <SettingRow
        v-for="key in numericKeys"
        :key="key"
        :label="t(`settings.affinity.${key}`)"
        :value="numberFieldValue(key)"
        :help="t(`settings.affinity.${key}Help`)"
        :source-label="sourceLabel(key)"
        :action-label="actionLabel(key)"
        :overridden="hasOverride(key)"
        :pending-restore="isPendingRestore(key)"
        :divided="key !== 'affinity_capacity'"
        :disabled="disabled"
        @toggle="toggleOverride(key)"
      >
        <template #control>
          <div class="settings-routing__input">
            <CompactFieldError :id="`settings-value-${key}`" :error="numberFieldError(key)">
              <template #default="{ invalid, describedBy }">
                <AppTextInput
                  :id="`settings-value-${key}`"
                  type="number"
                  :model-value="String(draft.values[key])"
                  :label="t('settings.runtime.valueFor', { field: t(`settings.affinity.${key}`) })"
                  appearance="surface"
                  size="compact"
                  monospace
                  :min="numericMinimums[key]"
                  :max="numericMaximums[key]"
                  step="1"
                  inputmode="numeric"
                  :disabled="disabled"
                  :invalid="invalid"
                  :described-by="describedBy"
                  @update:model-value="setNumber(key, $event)"
                />
              </template>
            </CompactFieldError>
            <span aria-hidden="true">
              {{ t(numericUnits[key]) }}
            </span>
          </div>
        </template>
      </SettingRow>
    </div>
  </section>
</template>

<style scoped>
.settings-section,
.settings-section__heading,
.settings-routing__rows {
  display: grid;
}

.settings-section {
  gap: var(--space-4);
  scroll-margin-top: 76px;
}

.settings-routing__rows {
  gap: var(--space-1);
}

.settings-section__heading h2,
.settings-section__heading p {
  margin: 0;
}

.settings-section__heading h2 {
  font-size: var(--title-section);
  font-weight: 650;
}

.settings-section__heading p {
  margin-top: var(--space-1);
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}

.settings-routing__input {
  display: inline-grid;
  grid-template-columns: minmax(0, 112px) auto;
  align-items: center;
  gap: var(--space-2);
}
</style>
