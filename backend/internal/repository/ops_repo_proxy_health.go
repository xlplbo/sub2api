package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const opsProxyHealthLastErrorMaxChars = 300

const opsProxyAttemptStatusSQL = `COALESCE(CASE WHEN jsonb_typeof(ev->'upstream_status_code') = 'number' THEN (ev->>'upstream_status_code')::int END, 0)`

// opsProxyAttemptsFrom expands the upstream attempts of the error rows matching
// where into ev: transport failures (no status) and HTTP-status attempts,
// excluding attempts ended by the request itself.
func opsProxyAttemptsFrom(where string) string {
	return `
  FROM ops_error_logs
  CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(upstream_errors) = 'array' THEN upstream_errors ELSE '[]'::jsonb END
  ) AS ev
  ` + where + `
    AND upstream_errors IS NOT NULL
    AND COALESCE(ev->>'reason', '') <> 'request_canceled'
    AND (
      split_part(ev->>'kind', ':', 1) LIKE '%request_error'
      OR ` + opsProxyAttemptStatusSQL + ` > 0
    )`
}

// ListProxyTransportFailures groups transport-level upstream attempt failures by
// the proxy attribution recorded on each event (see
// dev-docs/upstream-error-proxy-attribution.md): never by the account's current proxy.
//
// Time is the request end (the error row's created_at), the same basis as the
// upstream error list drill-down and the usage logs; attempt times only order
// attempts within one row.
//
// For managed proxies with failures it also counts attempts that reached the
// upstream: HTTP-status attempts by event attribution plus successful requests
// by the account's current proxy binding (usage logs carry no proxy, so this part
// is a current-binding estimate). Around failure clusters it returns the exact
// failure and ok times; the service evaluates the rolling fault windows on them.
func (r *opsRepository) ListProxyTransportFailures(ctx context.Context, filter *service.OpsDashboardFilter, rules service.OpsProxyHealthRules) ([]*service.OpsProxyHealthItem, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}

	start, end := filter.StartTime.UTC(), filter.EndTime.UTC()
	where, args, next := buildErrorWhere(filter, start, end, 1)
	args = append(args, rules.FaultMinFailures)
	minFailuresArg := fmt.Sprintf("$%d", next)
	usageJoin, usageWhere, usageArgs, _ := buildUsageWhere(filter, start, end, next+1)
	args = append(args, usageArgs...)
	window := fmt.Sprintf("interval '%d minutes'", max(rules.FaultWindowMinutes, 1))

	q := `
WITH events AS (
  SELECT
    CASE WHEN jsonb_typeof(ev->'proxy_id') = 'number' THEN (ev->>'proxy_id')::bigint END AS proxy_id,
    COALESCE(ev->>'proxy_name', '') AS proxy_name,
    CASE WHEN jsonb_typeof(ev->'account_id') = 'number' THEN (ev->>'account_id')::bigint END AS account_id,
    COALESCE(ev->>'account_name', '') AS account_name,
    COALESCE(ev->>'message', '') AS message,
    created_at AS at,
    COALESCE(
      CASE WHEN jsonb_typeof(ev->'at_unix_ms') = 'number' THEN to_timestamp((ev->>'at_unix_ms')::double precision / 1000.0) END,
      created_at
    ) AS attempt_at,
    ` + opsProxyAttemptStatusSQL + ` = 0 AS is_failure` + opsProxyAttemptsFrom(where) + `
),
failures AS (
  SELECT
    CASE
      WHEN proxy_id IS NOT NULL THEN 'proxy'
      WHEN proxy_name = 'direct/no_proxy' THEN 'direct'
      ELSE 'unknown'
    END AS route,
    proxy_id, proxy_name, account_id, account_name, message, at, attempt_at
  FROM events
  WHERE is_failure
),
grouped AS (
  SELECT
    route,
    proxy_id,
    (array_agg(proxy_name ORDER BY at DESC, attempt_at DESC))[1] AS proxy_name,
    COUNT(*) AS failed_attempts,
    COUNT(DISTINCT account_id) AS affected_account_count,
    COALESCE((array_agg(DISTINCT account_name) FILTER (WHERE account_name <> ''))[1:10], '{}') AS account_names,
    MAX(at) AS last_failed_at,
    left((array_agg(message ORDER BY at DESC, attempt_at DESC))[1], ` + fmt.Sprint(opsProxyHealthLastErrorMaxChars) + `) AS last_error
  FROM failures
  GROUP BY route, proxy_id
),
failing_proxies AS (
  SELECT proxy_id FROM grouped WHERE proxy_id IS NOT NULL
),
proxy_accounts AS (
  SELECT id AS account_id, proxy_id FROM accounts
  WHERE proxy_id IN (SELECT proxy_id FROM failing_proxies)
),
ok_totals AS (
  SELECT proxy_id, SUM(n) AS ok_attempts FROM (
    SELECT proxy_id, COUNT(*) AS n
    FROM events
    WHERE NOT is_failure AND proxy_id IN (SELECT proxy_id FROM failing_proxies)
    GROUP BY 1
    UNION ALL
    SELECT pa.proxy_id, COUNT(*)
    FROM usage_logs ul
    JOIN proxy_accounts pa ON pa.account_id = ul.account_id
    ` + usageJoin + `
    ` + usageWhere + `
    GROUP BY 1
  ) t
  GROUP BY proxy_id
),
-- Any rolling window holding enough failures ends at or before a failure that
-- has that many failures in the window before it, and starts no earlier than
-- one window ahead of it; only those regions go to the exact evaluation.
cluster_ends AS (
  SELECT proxy_id, at FROM (
    SELECT proxy_id, at,
           COUNT(*) OVER (PARTITION BY proxy_id ORDER BY at RANGE BETWEEN ` + window + ` PRECEDING AND CURRENT ROW) AS window_fail
    FROM failures
    WHERE proxy_id IS NOT NULL
  ) w
  WHERE window_fail >= ` + minFailuresArg + `
),
cluster_islands AS (
  SELECT proxy_id, at, SUM(new_island) OVER (PARTITION BY proxy_id ORDER BY at) AS island
  FROM (
    SELECT proxy_id, at,
           CASE WHEN at - LAG(at) OVER (PARTITION BY proxy_id ORDER BY at) <= 2 * ` + window + ` THEN 0 ELSE 1 END AS new_island
    FROM cluster_ends
  ) s
),
regions AS (
  SELECT proxy_id, MIN(at) - ` + window + ` AS lo, MAX(at) + ` + window + ` AS hi
  FROM cluster_islands
  GROUP BY proxy_id, island
),
fault_fails AS (
  SELECT f.proxy_id, f.at
  FROM failures f
  JOIN regions r ON r.proxy_id = f.proxy_id AND f.at BETWEEN r.lo AND r.hi
),
fault_oks AS (
  SELECT e.proxy_id, e.at
  FROM events e
  JOIN regions r ON r.proxy_id = e.proxy_id AND e.at BETWEEN r.lo AND r.hi
  WHERE NOT e.is_failure
  UNION ALL
  SELECT r.proxy_id, ul.created_at
  FROM regions r
  JOIN proxy_accounts pa ON pa.proxy_id = r.proxy_id
  JOIN usage_logs ul ON ul.account_id = pa.account_id
  ` + usageJoin + `
  ` + usageWhere + `
    AND ul.created_at BETWEEN r.lo AND r.hi
),
fail_timelines AS (
  SELECT proxy_id, array_agg((EXTRACT(EPOCH FROM at) * 1000000)::bigint ORDER BY at) AS times
  FROM fault_fails
  GROUP BY proxy_id
),
ok_timelines AS (
  SELECT proxy_id, array_agg((EXTRACT(EPOCH FROM at) * 1000000)::bigint ORDER BY at) AS times
  FROM fault_oks
  GROUP BY proxy_id
)
SELECT
  g.route,
  g.proxy_id,
  g.proxy_name,
  COALESCE(p.name, ''),
  CASE
    WHEN g.proxy_id IS NULL THEN ''
    WHEN p.id IS NULL OR p.deleted_at IS NOT NULL THEN 'deleted'
    ELSE COALESCE(p.status, '')
  END,
  g.failed_attempts,
  COALESCE(ot.ok_attempts, 0)::bigint,
  COALESCE(ft.times, '{}'),
  COALESCE(okt.times, '{}'),
  g.affected_account_count,
  g.account_names,
  g.last_failed_at,
  g.last_error
FROM grouped g
LEFT JOIN ok_totals ot ON ot.proxy_id = g.proxy_id AND g.route = 'proxy'
LEFT JOIN fail_timelines ft ON ft.proxy_id = g.proxy_id AND g.route = 'proxy'
LEFT JOIN ok_timelines okt ON okt.proxy_id = g.proxy_id AND g.route = 'proxy'
LEFT JOIN proxies p ON p.id = g.proxy_id`

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Long ranges push the usage_logs estimate past jit_above_cost, and compiling
	// this multi-CTE query then takes seconds while executing it takes ~100ms.
	if _, err := tx.ExecContext(ctx, "SET LOCAL jit = off"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsProxyHealthItem, 0, 8)
	for rows.Next() {
		var (
			item               service.OpsProxyHealthItem
			proxyID            sql.NullInt64
			failTimes, okTimes pq.Int64Array
		)
		if err := rows.Scan(
			&item.Route,
			&proxyID,
			&item.ProxyName,
			&item.CurrentName,
			&item.CurrentStatus,
			&item.FailedAttempts,
			&item.OKAttempts,
			&failTimes,
			&okTimes,
			&item.AffectedAccountCount,
			pq.Array(&item.AccountNames),
			&item.LastFailedAt,
			&item.LastError,
		); err != nil {
			return nil, err
		}
		if proxyID.Valid {
			id := proxyID.Int64
			item.ProxyID = &id
		}
		if len(failTimes) > 0 {
			item.FaultTimeline = &service.OpsProxyFaultTimeline{
				Failures: opsUnixMicrosToTimes(failTimes),
				OKs:      opsUnixMicrosToTimes(okTimes),
			}
		}
		out = append(out, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CountProxyTransportFailures counts what ListProxyTransportFailures reports as
// FailedAttempts summed over managed proxies, without the success counts and
// fault timelines.
func (r *opsRepository) CountProxyTransportFailures(ctx context.Context, filter *service.OpsDashboardFilter) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return 0, fmt.Errorf("nil filter")
	}

	where, args, _ := buildErrorWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), 1)
	q := `SELECT COUNT(*)` + opsProxyAttemptsFrom(where) + `
    AND jsonb_typeof(ev->'proxy_id') = 'number'
    AND ` + opsProxyAttemptStatusSQL + ` = 0`

	var n int64
	err := r.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

func opsUnixMicrosToTimes(values []int64) []time.Time {
	out := make([]time.Time, 0, len(values))
	for _, v := range values {
		out = append(out, time.UnixMicro(v).UTC())
	}
	return out
}

func (r *opsRepository) CountActiveProxies(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	var n int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxies WHERE deleted_at IS NULL AND status = 'active'`).Scan(&n)
	return n, err
}
