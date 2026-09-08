<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { formatTimeBudget } from '../display'

const props = defineProps<{ modelValue: number; label: string; min: number; max: number }>()
const emit = defineEmits<{ 'update:modelValue': [seconds: number] }>()
const id = useId()
const unit = ref(props.modelValue > 0 && props.modelValue % 3600 === 0 ? 3600 : props.modelValue % 60 === 0 ? 60 : 1)
const amount = computed(() => Number.isFinite(props.modelValue) ? props.modelValue / unit.value : '')

function update(event: Event) {
  const value = (event.target as HTMLInputElement).valueAsNumber
  emit('update:modelValue', Number.isFinite(value) ? Math.round(value * unit.value) : NaN)
}
</script>

<template>
  <div class="duration-input">
    <label :for="id">{{ label }}</label>
    <div class="duration-fields">
      <input :id="id" type="number" :value="amount" :min="min / unit" :max="max / unit" step="any" required @input="update" />
      <select v-model.number="unit" :aria-label="`${label}单位`"><option :value="1">秒</option><option :value="60">分钟</option><option :value="3600">小时</option></select>
    </div>
    <small class="field-help">{{ formatTimeBudget(modelValue) }} · 范围 {{ formatTimeBudget(min) }} 至 {{ formatTimeBudget(max) }}</small>
  </div>
</template>
