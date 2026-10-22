import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupsView from '@/views/admin/GroupsView.vue'

const api = vi.hoisted(() => ({
  list: vi.fn(), getAll: vi.fn(), getUsageSummary: vi.fn(), getCapacitySummary: vi.fn(),
  listCompositeRoutes: vi.fn(), createCompositeRoute: vi.fn(), updateCompositeRoute: vi.fn(),
  previewCompositeRoute: vi.fn(),
  getModelAllowlistCandidates: vi.fn(), getLiveCapability: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: api, accounts: { list: vi.fn() } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: vi.fn(() => false), nextStep: vi.fn() }),
}))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string) => key }),
}))

const route = {
  id: 1, group_id: 7, public_model: 'glm-5.3', match_type: 'exact',
  target_platform: 'openai', upstream_model: 'vision-model', endpoint: 'responses',
  request_condition: 'has_image', priority: 100, enabled: true, notes: '',
}

const mountView = () => mount(GroupsView, {
  global: {
    plugins: [createPinia()],
    stubs: {
      AppLayout: { template: '<main><slot /></main>' },
      TablePageLayout: { template: '<section><slot name="table" /></section>' },
      DataTable: {
        props: ['data'],
        template: '<div v-if="data.length"><slot name="cell-actions" :row="data[0]" /></div>',
      },
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Select: defineComponent({
        props: ['modelValue', 'options'],
        emits: ['update:modelValue'],
        template: `<select :value="modelValue" @change="$emit('update:modelValue', $event.target.value)">
          <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>`,
      }),
      Pagination: true, ConfirmDialog: true, EmptyState: true, PlatformIcon: true, Icon: true,
      GroupCapacityBadge: true, GroupRateMultipliersModal: true, GroupRPMOverridesModal: true,
      ReasoningEffortPolicyFields: true, CodexManifestAccountsField: true, PricingEntryCard: true,
      VueDraggable: true,
    },
  },
})

describe('Composite image routes', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    api.list.mockResolvedValue({
      items: [{ id: 7, name: 'composite', platform: 'composite', status: 'active', rate_multiplier: 1 }],
      total: 1, page: 1, page_size: 20, pages: 1,
    })
    api.getUsageSummary.mockResolvedValue([])
    api.getCapacitySummary.mockResolvedValue([])
    api.getModelAllowlistCandidates.mockResolvedValue([])
    api.getLiveCapability.mockResolvedValue({ supported: false })
    api.listCompositeRoutes.mockResolvedValue([route])
    api.previewCompositeRoute.mockResolvedValue({ matched: true, route })
  })

  it('saves the image condition, restores it when editing and previews image requests', async () => {
    const wrapper = mountView()
    await flushPromises()
    const open = wrapper.findAll('button').find(b => b.text().includes('admin.groups.compositeRoutes.action'))
    expect(open).toBeTruthy()
    await open!.trigger('click')
    await flushPromises()
    const condition = wrapper.get('[data-testid="composite-route-condition"]')
    expect((condition.element as HTMLSelectElement).value).toBe('any')
    await condition.setValue('has_image')
    await wrapper.get('input[placeholder="openrouter/gpt-5"]').setValue('glm-5.3')
    await wrapper.get('input[placeholder="gpt-5"]').setValue('vision-model')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createCompositeRoute).toHaveBeenCalledWith(7, expect.objectContaining({
      public_model: 'glm-5.3', upstream_model: 'vision-model', request_condition: 'has_image',
    }))
    const edit = wrapper.findAll('button').find(b => b.attributes('title') === 'common.edit')
    expect(edit).toBeTruthy()
    await edit!.trigger('click')
    await flushPromises()
    expect((wrapper.get('[data-testid="composite-route-condition"]').element as HTMLSelectElement).value).toBe('has_image')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateCompositeRoute).toHaveBeenCalledWith(7, 1, expect.objectContaining({ request_condition: 'has_image' }))
    const inputs = wrapper.findAll('input[placeholder="openrouter/gpt-5"]')
    await inputs[1]!.setValue('glm-5.3')
    await wrapper.get('[data-testid="composite-preview-image"]').setValue(true)
    await inputs[1]!.trigger('keyup.enter')
    await flushPromises()
    expect(api.previewCompositeRoute).toHaveBeenCalledWith(7, {
      model: 'glm-5.3', endpoint: 'any', has_image: true,
    })
    wrapper.unmount()
  })
})
