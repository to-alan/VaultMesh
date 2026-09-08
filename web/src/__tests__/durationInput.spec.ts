import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import DurationInput from '../components/DurationInput.vue'

describe('duration input', () => {
  it('displays an odd-second duration exactly and changes units without mutating it', async () => {
    const wrapper = mount(DurationInput, { props: { modelValue: 5401, label: '运行时限', min: 60, max: 604800 } })
    expect(wrapper.find('input').element.value).toBe('5401')
    expect(wrapper.find('select').element.value).toBe('1')
    await wrapper.find('select').setValue('60')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.find('input').setValue('30')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([1800])
  })

  it('selects hours for whole-hour budgets and exposes required input validation', () => {
    const wrapper = mount(DurationInput, { props: { modelValue: 21600, label: '运行时限', min: 60, max: 604800 } })
    expect(wrapper.find('input').element.value).toBe('6')
    expect(wrapper.find('select').element.value).toBe('3600')
    expect(wrapper.find('input').element.required).toBe(true)
  })
})
