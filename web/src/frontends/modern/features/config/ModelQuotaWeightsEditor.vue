<script setup lang="ts">
import { ChevronRight, Trash2 } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  modelQuotaWeightOptions,
  type ModelQuotaWeightRow,
} from '@modern/features/settings/settings-draft'
import { AppIconButton, AppSelect, AppTextField } from '@modern/components/ui'

const props = defineProps<{
  modelValue: ModelQuotaWeightRow[]
  disabled?: boolean
  readonly?: boolean
}>()
const emit = defineEmits<{
  'update:modelValue': [value: ModelQuotaWeightRow[]]
  'update:valid': [value: boolean]
}>()
const { t } = useI18n()
const weightOptions = modelQuotaWeightOptions.map((value) => ({ value, label: value }))
const attempted = ref(false)
const rows = computed(() => props.modelValue)
const invalid = computed(() => !validRows(rows.value))
function validRows(list: ModelQuotaWeightRow[]): boolean {
  const seen = new Set<string>()
  for (const row of list) {
    const model = row.model.trim()
    if (!model || seen.has(model)) return false
    if (!(modelQuotaWeightOptions as readonly string[]).includes(row.weight)) return false
    seen.add(model)
  }
  return true
}
function duplicateModel(index: number): boolean {
  const target = rows.value[index]?.model.trim()
  if (!target) return false
  return rows.value.some((row, i) => i !== index && row.model.trim() === target)
}
watch(
  rows,
  (list) => emit('update:valid', validRows(list)),
  { immediate: true, deep: true },
)
function add(): void {
  if (props.disabled || props.readonly) return
  emit('update:modelValue', [...rows.value, { id: Date.now(), model: '', weight: '1' }])
  attempted.value = true
}
function remove(index: number): void {
  if (props.disabled || props.readonly) return
  emit(
    'update:modelValue',
    rows.value.filter((_, i) => i !== index),
  )
}
defineExpose({
  add,
  canAdd: computed(() => !props.disabled && !props.readonly),
})
</script>
<template>
  <div class="modern-quota-weights">
    <div
      v-for="(row, index) in rows"
      :key="row.id"
      class="modern-quota-weights-row"
    >
      <AppTextField
        v-model="row.model"
        :label="t('modelQuota.model')"
        label-hidden
        :placeholder="t('modelQuota.model')"
        :disabled="disabled"
        :readonly="readonly"
        :error="
          attempted && invalid && (!row.model.trim() || duplicateModel(index))
            ? t('modelQuota.invalidModel')
            : undefined
        "
      />
      <ChevronRight class="modern-quota-weights-arrow" :size="14" />
      <AppSelect
        v-model="row.weight"
        :options="weightOptions"
        :label="t('modelQuota.weight')"
        label-hidden
        :disabled="disabled"
        :readonly="readonly"
      />
      <AppIconButton
        :icon="Trash2"
        :label="t('modelQuota.remove')"
        variant="text"
        size="xs"
        :disabled="disabled"
        @click="remove(index)"
      />
    </div>
    <p v-if="!rows.length" class="modern-quota-weights-empty">
      {{ t('modelQuota.empty') }}
    </p>
    <p v-if="invalid" class="modern-quota-weights-error">
      {{ t('modelQuota.invalidRows') }}
    </p>
  </div>
</template>
<style scoped>
.modern-quota-weights {
  display: grid;
  gap: var(--modern-space-2);
}
.modern-quota-weights-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(96px, auto) auto;
  align-items: center;
  gap: var(--modern-space-2) var(--modern-space-3);
}
.modern-quota-weights-arrow {
  color: var(--modern-color-muted, #667);
}
.modern-quota-weights-empty,
.modern-quota-weights-error {
  margin: 0;
  font-size: var(--modern-font-size-sm, 13px);
}
.modern-quota-weights-error {
  color: var(--modern-color-danger, #c0392b);
}
.modern-quota-weights-empty {
  color: var(--modern-color-muted, #667);
}
</style>
