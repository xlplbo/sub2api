import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { OpsDashboardOverview, OpsProxyHealth } from '@/api/admin/ops'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'
import OpsErrorDetailsModal from '../OpsErrorDetailsModal.vue'

const { listUpstreamErrors, listRequestErrors, getAllProxies } = vi.hoisted(() => ({
  listUpstreamErrors: vi.fn(),
  listRequestErrors: vi.fn(),
  getAllProxies: vi.fn(),
}))

vi.mock('@/api/admin/ops', () => ({ opsAPI: { listUpstreamErrors, listRequestErrors } }))
vi.mock('@/api', () => ({
  adminAPI: {
    groups: { getAll: vi.fn().mockResolvedValue([]) },
    proxies: { getAll: getAllProxies },
  },
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
  useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: false }),
}))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

const dialogStub = { props: ['show'], template: '<div v-if="show"><slot /></div>' }

function proxyHealth(): OpsProxyHealth {
  return {
    rules: { fault_window_minutes: 5, fault_failure_rate: 0.9, fault_min_failures: 3, error_rate: 0.05, error_rate_min_failures: 3 },
    active_proxy_count: 4,
    failed_proxy_count: 2,
    abnormal_proxy_count: 1,
    high_error_rate_proxy_count: 1,
    failed_attempts: 510,
    truncated: false,
    items: [
      {
        route: 'proxy', proxy_id: 4, proxy_name: '蜗牛', current_name: '蜗牛', current_status: 'active', status: 'fault',
        failed_attempts: 499, ok_attempts: 24, failure_rate: 499 / 523,
        fault_from: '2026-09-18T06:33:00Z', fault_to: '2026-09-18T07:31:00Z',
        affected_account_count: 1, account_names: ['1-蜗牛'],
        last_failed_at: '2026-09-18T07:29:50Z', last_error: 'socks connect tcp 127.0.0.1:10838',
      },
      {
        route: 'proxy', proxy_id: 3, proxy_name: '仇总', current_name: '仇总', current_status: 'active', status: 'high_error_rate',
        failed_attempts: 11, ok_attempts: 61, failure_rate: 11 / 72, fault_from: null, fault_to: null,
        affected_account_count: 1, account_names: ['1-仇总'],
        last_failed_at: '2026-09-18T10:03:37Z', last_error: 'TLS handshake timeout',
      },
      {
        route: 'unknown', proxy_id: null, proxy_name: 'unknown', failed_attempts: 2, ok_attempts: 0, failure_rate: 0,
        fault_from: null, fault_to: null,
        affected_account_count: 0, account_names: [], last_failed_at: '2026-09-18T06:00:00Z', last_error: 'legacy',
      },
    ],
  }
}

function mountHeader() {
  return mount(OpsDashboardHeader, {
    props: {
      overview: { proxy_health: proxyHealth() } as OpsDashboardOverview,
      platform: '', groupId: null, timeRange: '1h', queryMode: 'auto', loading: false, lastUpdated: null,
    },
    global: { stubs: { BaseDialog: dialogStub, Select: true, HelpTooltip: true, Icon: true } },
  })
}

describe('Ops proxy health card', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows abnormal proxies and drills into upstream errors by proxy', async () => {
    const wrapper = mountHeader()
    await flushPromises()

    const card = wrapper.get('[data-testid="ops-proxy-health-card"]')
    expect(card.text()).toContain('admin.ops.proxyHealth.status.abnormal')
    expect(card.text()).toContain('510')

    await card.get('button').trigger('click')
    const details = wrapper.get('[data-testid="ops-proxy-health-details"]')
    expect(details.text()).toContain('admin.ops.proxyHealth.scope')
    const rows = details.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    expect(rows[0].text()).toContain('蜗牛')
    expect(rows[0].text()).toContain('admin.ops.proxyHealth.status.fault')
    expect(rows[0].text()).toContain('admin.ops.proxyHealth.faultPeriod')
    expect(rows[0].text()).toContain('95.41%')
    expect(rows[1].text()).toContain('admin.ops.proxyHealth.status.highErrorRate')
    expect(rows[1].text()).toContain('15.28%')
    expect(rows[1].text()).not.toContain('admin.ops.proxyHealth.faultPeriod')
    expect(rows[2].find('button').exists()).toBe(false)
    expect(details.text()).toContain('admin.ops.proxyHealth.quietProxies')

    await rows[0].get('button').trigger('click')
    expect(wrapper.emitted('openErrorDetails')).toEqual([['upstream', { proxyId: 4 }]])
    expect(wrapper.find('[data-testid="ops-proxy-health-details"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('Ops upstream error list proxy filter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listUpstreamErrors.mockResolvedValue({ items: [], total: 0 })
    getAllProxies.mockResolvedValue([{ id: 4, name: '蜗牛' }])
  })

  it('applies the preset proxy on open and clears it on reset', async () => {
    const wrapper = mount(OpsErrorDetailsModal, {
      props: { show: false, timeRange: '1h', errorType: 'upstream', proxyFilter: 4 },
      global: { stubs: { BaseDialog: dialogStub, OpsErrorLogTable: true, Select: true } },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(getAllProxies).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="ops-error-proxy-filter"]').exists()).toBe(true)
    expect(listUpstreamErrors).toHaveBeenLastCalledWith(expect.objectContaining({ proxy_id: 4, phase: 'upstream' }))

    const reset = wrapper.findAll('button').find((b) => b.text() === 'common.reset')
    await reset!.trigger('click')
    await flushPromises()
    expect(listUpstreamErrors.mock.lastCall?.[0]).not.toHaveProperty('proxy_id')
    wrapper.unmount()
  })

  it('does not send proxy filters for request errors', async () => {
    listRequestErrors.mockResolvedValue({ items: [], total: 0 })
    const wrapper = mount(OpsErrorDetailsModal, {
      props: { show: false, timeRange: '1h', errorType: 'request', proxyFilter: 4 },
      global: { stubs: { BaseDialog: dialogStub, OpsErrorLogTable: true, Select: true } },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.find('[data-testid="ops-error-proxy-filter"]').exists()).toBe(false)
    expect(listRequestErrors.mock.lastCall?.[0]).not.toHaveProperty('proxy_id')
    expect(getAllProxies).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
