import { describe, expect, it } from 'vitest'
import type { OpsProxyHealth, OpsProxyHealthItem } from '@/api/admin/ops'
import { formatProxyRate, quietActiveProxyCount, summarizeProxyHealth } from '../opsProxyHealth'

function item(overrides: Partial<OpsProxyHealthItem>): OpsProxyHealthItem {
  return {
    route: 'proxy',
    proxy_id: 4,
    proxy_name: '蜗牛',
    current_name: '蜗牛',
    current_status: 'active',
    status: 'ok',
    failed_attempts: 1,
    ok_attempts: 100,
    failure_rate: 0.0099,
    fault_from: null,
    fault_to: null,
    affected_account_count: 1,
    account_names: ['1-蜗牛'],
    last_failed_at: '2026-09-18T07:29:50Z',
    last_error: 'socks connect',
    ...overrides
  }
}

function health(overrides: Partial<OpsProxyHealth>): OpsProxyHealth {
  return {
    rules: {
      fault_window_minutes: 5,
      fault_failure_rate: 0.9,
      fault_min_failures: 3,
      error_rate: 0.05,
      error_rate_min_failures: 3
    },
    active_proxy_count: 4,
    failed_proxy_count: 0,
    abnormal_proxy_count: 0,
    high_error_rate_proxy_count: 0,
    failed_attempts: 0,
    items: [],
    truncated: false,
    ...overrides
  }
}

describe('summarizeProxyHealth', () => {
  it('reports unknown without data', () => {
    expect(summarizeProxyHealth(null).status).toBe('unknown')
  })

  it('is red when any proxy had a fault period', () => {
    const summary = summarizeProxyHealth(
      health({ failed_proxy_count: 2, abnormal_proxy_count: 1, high_error_rate_proxy_count: 1, failed_attempts: 834 })
    )
    expect(summary).toEqual({ status: 'abnormal', activeProxyCount: 4, abnormalCount: 1, failedAttempts: 834 })
  })

  it('uses the backend totals even when details are truncated', () => {
    const items = Array.from({ length: 50 }, (_, i) => item({ proxy_id: i + 1, status: 'fault' }))
    const summary = summarizeProxyHealth(
      health({ active_proxy_count: 60, failed_proxy_count: 51, abnormal_proxy_count: 51, items, truncated: true })
    )
    expect(summary.abnormalCount).toBe(51)
    expect(summary.status).toBe('abnormal')
  })

  it('is yellow for a high error rate without faults', () => {
    expect(summarizeProxyHealth(health({ failed_proxy_count: 1, high_error_rate_proxy_count: 1 })).status).toBe(
      'high_error_rate'
    )
  })

  it('is green when failures stay below both rules', () => {
    expect(summarizeProxyHealth(health({ failed_proxy_count: 3, failed_attempts: 21 })).status).toBe('ok')
  })

  it('reports unused without active or failing proxies', () => {
    expect(summarizeProxyHealth(health({ active_proxy_count: 0 })).status).toBe('unused')
  })
})

describe('quietActiveProxyCount', () => {
  it('counts active proxies without failures', () => {
    const h = health({
      items: [
        item({ proxy_id: 4 }),
        item({ proxy_id: 9, current_status: 'deleted' }),
        item({ route: 'direct', proxy_id: null, current_status: '' })
      ]
    })
    expect(quietActiveProxyCount(h)).toBe(3)
  })

  it('is unknown when the list is truncated', () => {
    expect(quietActiveProxyCount(health({ truncated: true }))).toBeNull()
  })
})

describe('formatProxyRate', () => {
  it('keeps up to two decimals without trailing zeros', () => {
    expect(formatProxyRate(0.9)).toBe('90%')
    expect(formatProxyRate(0.05)).toBe('5%')
    expect(formatProxyRate(815 / 54872)).toBe('1.49%')
    expect(formatProxyRate(0)).toBe('0%')
  })
})
