import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import UsageCleanupDialog from '../UsageCleanupDialog.vue'
import Pagination from '@/components/common/Pagination.vue'

const { listCleanupTasks, showError } = vi.hoisted(() => ({ listCleanupTasks: vi.fn(), showError: vi.fn() }))
vi.mock('@/components/admin/usage/UsageFilters.vue', () => ({
  default: { name: 'UsageFilters', props: ['modelValue', 'startDate', 'endDate'], template: '<div />' },
}))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { listCleanupTasks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.clearAllMocks(); vi.spyOn(console, 'error').mockImplementation(() => {}) })
afterEach(() => { vi.restoreAllMocks() })
const page = (number: number) => ({ items: [{ id: number, status: 'succeeded', deleted_rows: number, filters: {} }], total: 15, page: number, page_size: 5 })
async function openDialog() {
  listCleanupTasks.mockResolvedValueOnce(page(1))
  const wrapper = mount(UsageCleanupDialog, {
    props: { show: false, filters: {}, startDate: '2026-09-01', endDate: '2026-09-02' },
    global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, UsageFilters: true, ConfirmDialog: true, Pagination: true } },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('cleanup task pagination', () => {
  it.each(['success', 'failure'])('ignores an older page request that completes with %s', async (outcome) => {
    const wrapper = await openDialog()
    let resolve!: (value: object) => void
    let reject!: (error: Error) => void
    listCleanupTasks.mockImplementationOnce(() => new Promise((res, rej) => { resolve = res; reject = rej }))
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    listCleanupTasks.mockResolvedValueOnce(page(3))
    wrapper.findComponent(Pagination).vm.$emit('update:page', 3)
    await flushPromises()
    if (outcome === 'success') resolve(page(2))
    else reject(new Error('old failure'))
    await flushPromises()
    expect(wrapper.findComponent(Pagination).props('page')).toBe(3)
    expect(wrapper.text()).toContain('#3')
    expect(showError).not.toHaveBeenCalled()
  })

  it('ignores a pending result after the dialog is closed and reopened', async () => {
    const wrapper = await openDialog()
    let resolve!: (value: object) => void
    listCleanupTasks.mockImplementationOnce(() => new Promise(res => { resolve = res }))
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    await wrapper.setProps({ show: false })
    listCleanupTasks.mockResolvedValueOnce(page(1))
    await wrapper.setProps({ show: true })
    await flushPromises()
    resolve(page(2))
    await flushPromises()
    expect(wrapper.findComponent(Pagination).props('page')).toBe(1)
    expect(wrapper.text()).toContain('#1')
  })
})

const pageFilters = () => ({
  user_id: 7,
  group_id: 3,
  model: 'gpt-6-astra',
  request_type: null,
  stream: null,
  billing_type: 1,
  billing_mode: 'token',
  upstream_model_mismatch: true,
  native_compaction_v2: true,
  start_date: '2026-10-01',
  end_date: '2026-10-08',
})
async function openDialogWithPageFilters() {
  listCleanupTasks.mockResolvedValueOnce(page(1))
  const wrapper = mount(UsageCleanupDialog, {
    props: { show: false, filters: pageFilters(), startDate: '2026-10-01', endDate: '2026-10-08' },
    global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, ConfirmDialog: true, Pagination: true } },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('copying page filters on open', () => {
  it('drops the filters the cleanup API does not support', async () => {
    const wrapper = await openDialogWithPageFilters()
    const local = wrapper.findComponent({ name: 'UsageFilters' }).props('modelValue')
    expect(local).not.toHaveProperty('upstream_model_mismatch')
    expect(local).not.toHaveProperty('billing_mode')
    expect(local).not.toHaveProperty('native_compaction_v2')
  })

  it('keeps the supported filters and the date range', async () => {
    const wrapper = await openDialogWithPageFilters()
    const local = wrapper.findComponent({ name: 'UsageFilters' }).props('modelValue')
    expect(local).toMatchObject({
      user_id: 7,
      group_id: 3,
      model: 'gpt-6-astra',
      billing_type: 1,
      start_date: '2026-10-01',
      end_date: '2026-10-08',
    })
  })
})
