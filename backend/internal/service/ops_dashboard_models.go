package service

import "time"

type OpsDashboardFilter struct {
	StartTime time.Time
	EndTime   time.Time

	Platform string
	GroupID  *int64

	// QueryMode controls whether dashboard queries should use raw logs or pre-aggregated tables.
	// Expected values: auto/raw/preagg (see OpsQueryMode).
	QueryMode OpsQueryMode
}

type OpsRateSummary struct {
	Current float64 `json:"current"`
	Peak    float64 `json:"peak"`
	Avg     float64 `json:"avg"`
}

type OpsPercentiles struct {
	P50 *int `json:"p50_ms"`
	P90 *int `json:"p90_ms"`
	P95 *int `json:"p95_ms"`
	P99 *int `json:"p99_ms"`
	Avg *int `json:"avg_ms"`
	Max *int `json:"max_ms"`
}

type OpsDashboardOverview struct {
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Platform  string    `json:"platform"`
	GroupID   *int64    `json:"group_id"`

	// HealthScore is a backend-computed overall health score (0-100).
	// It is derived from the monitored metrics in this overview, plus best-effort system metrics/job heartbeats.
	HealthScore int `json:"health_score"`

	// Latest system-level snapshot (window=1m, global).
	SystemMetrics *OpsSystemMetricsSnapshot `json:"system_metrics"`

	// Background jobs health (heartbeats).
	JobHeartbeats []*OpsJobHeartbeat `json:"job_heartbeats"`

	// Transport-level upstream attempt failures grouped by event-time proxy attribution.
	ProxyHealth *OpsProxyHealth `json:"proxy_health"`

	SuccessCount         int64 `json:"success_count"`
	ErrorCountTotal      int64 `json:"error_count_total"`
	BusinessLimitedCount int64 `json:"business_limited_count"`

	ErrorCountSLA     int64 `json:"error_count_sla"`
	RequestCountTotal int64 `json:"request_count_total"`
	RequestCountSLA   int64 `json:"request_count_sla"`

	TokenConsumed int64 `json:"token_consumed"`

	SLA                          float64 `json:"sla"`
	ErrorRate                    float64 `json:"error_rate"`
	UpstreamErrorRate            float64 `json:"upstream_error_rate"`
	UpstreamErrorCountExcl429529 int64   `json:"upstream_error_count_excl_429_529"`
	Upstream429Count             int64   `json:"upstream_429_count"`
	Upstream529Count             int64   `json:"upstream_529_count"`

	QPS OpsRateSummary `json:"qps"`
	TPS OpsRateSummary `json:"tps"`

	Duration OpsPercentiles `json:"duration"`
	TTFT     OpsPercentiles `json:"ttft"`
}

const (
	OpsProxyRouteProxy   = "proxy"
	OpsProxyRouteDirect  = "direct"
	OpsProxyRouteUnknown = "unknown"
)

const (
	OpsProxyStatusFault         = "fault"
	OpsProxyStatusHighErrorRate = "high_error_rate"
	OpsProxyStatusOK            = "ok"
)

// OpsProxyHealthRules classify managed proxies over the selected range. A fault
// episode is any sliding window of FaultWindowMinutes with at least
// FaultMinFailures transport failures making up at least FaultFailureRate of the
// attempts through the proxy; a high error rate is the same ratio over the whole
// range reaching ErrorRate with at least ErrorRateMinFailures failures.
type OpsProxyHealthRules struct {
	FaultWindowMinutes   int     `json:"fault_window_minutes"`
	FaultFailureRate     float64 `json:"fault_failure_rate"`
	FaultMinFailures     int64   `json:"fault_min_failures"`
	ErrorRate            float64 `json:"error_rate"`
	ErrorRateMinFailures int64   `json:"error_rate_min_failures"`
}

// OpsProxyHealthItem is one route bucket of transport-level upstream attempt
// failures (no upstream HTTP status). ProxyName is the label recorded on the
// latest event; CurrentName/CurrentStatus come from the proxies table.
// OKAttempts, FailureRate, Status and the fault period apply to managed proxies
// only; OKAttempts counts successful requests by the account's current proxy
// binding, so it is an estimate labeled as such in the UI.
type OpsProxyHealthItem struct {
	Route                string     `json:"route"`
	ProxyID              *int64     `json:"proxy_id"`
	ProxyName            string     `json:"proxy_name"`
	CurrentName          string     `json:"current_name,omitempty"`
	CurrentStatus        string     `json:"current_status,omitempty"`
	Status               string     `json:"status,omitempty"`
	FailedAttempts       int64      `json:"failed_attempts"`
	OKAttempts           int64      `json:"ok_attempts"`
	FailureRate          float64    `json:"failure_rate"`
	FaultFrom            *time.Time `json:"fault_from"`
	FaultTo              *time.Time `json:"fault_to"`
	AffectedAccountCount int64      `json:"affected_account_count"`
	AccountNames         []string   `json:"account_names"`
	LastFailedAt         time.Time  `json:"last_failed_at"`
	LastError            string     `json:"last_error"`

	FaultTimeline *OpsProxyFaultTimeline `json:"-"`
}

// OpsProxyFaultTimeline holds the attempt times (request end) of one proxy
// around its failure clusters: every rolling window that can reach the fault
// failure count lies within it. Classification drops it, so cached snapshots
// never hold the times.
type OpsProxyFaultTimeline struct {
	Failures []time.Time
	OKs      []time.Time
}

type OpsProxyHealth struct {
	Rules            OpsProxyHealthRules `json:"rules"`
	ActiveProxyCount int64               `json:"active_proxy_count"`
	// The counts and FailedAttempts cover managed proxies only, before truncation;
	// AbnormalProxyCount is the number of proxies with a fault episode.
	FailedProxyCount        int                   `json:"failed_proxy_count"`
	AbnormalProxyCount      int                   `json:"abnormal_proxy_count"`
	HighErrorRateProxyCount int                   `json:"high_error_rate_proxy_count"`
	FailedAttempts          int64                 `json:"failed_attempts"`
	Items                   []*OpsProxyHealthItem `json:"items"`
	Truncated               bool                  `json:"truncated"`
}

type OpsLatencyHistogramBucket struct {
	Range string `json:"range"`
	Count int64  `json:"count"`
}

// OpsLatencyHistogramResponse is a coarse latency distribution histogram (success requests only).
// It is used by the Ops dashboard to quickly identify tail latency regressions.
type OpsLatencyHistogramResponse struct {
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Platform  string    `json:"platform"`
	GroupID   *int64    `json:"group_id"`

	TotalRequests int64                        `json:"total_requests"`
	Buckets       []*OpsLatencyHistogramBucket `json:"buckets"`
}
