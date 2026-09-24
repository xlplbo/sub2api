import type { OpsProxyHealth } from '@/api/admin/ops'

export type OpsProxyCardStatus = 'abnormal' | 'high_error_rate' | 'ok' | 'unused' | 'unknown'

export interface OpsProxyHealthSummary {
  status: OpsProxyCardStatus
  activeProxyCount: number
  abnormalCount: number
  failedAttempts: number
}

// 各项计数由后端在截断明细前统计，明细最多 50 条，不能从 items 反推。
export function summarizeProxyHealth(health?: OpsProxyHealth | null): OpsProxyHealthSummary {
  if (!health) {
    return { status: 'unknown', activeProxyCount: 0, abnormalCount: 0, failedAttempts: 0 }
  }
  let status: OpsProxyCardStatus = 'ok'
  if (health.abnormal_proxy_count > 0) status = 'abnormal'
  else if (health.high_error_rate_proxy_count > 0) status = 'high_error_rate'
  else if (health.active_proxy_count <= 0 && health.failed_proxy_count <= 0) status = 'unused'
  return {
    status,
    activeProxyCount: health.active_proxy_count,
    abnormalCount: health.abnormal_proxy_count,
    failedAttempts: health.failed_attempts
  }
}

// 列表被截断时无法得知其余代理是否失败，返回 null。
export function quietActiveProxyCount(health?: OpsProxyHealth | null): number | null {
  if (!health || health.truncated) return null
  const failingActive = (health.items ?? []).filter(
    (item) => item.route === 'proxy' && item.current_status === 'active'
  ).length
  return Math.max(0, health.active_proxy_count - failingActive)
}

export function formatProxyRate(rate: number): string {
  if (!Number.isFinite(rate)) return '-'
  return `${Number((rate * 100).toFixed(2))}%`
}
