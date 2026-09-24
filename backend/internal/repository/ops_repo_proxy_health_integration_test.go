//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var opsProxyHealthTestRules = service.OpsProxyHealthRules{
	FaultWindowMinutes:   5,
	FaultFailureRate:     0.9,
	FaultMinFailures:     3,
	ErrorRate:            0.05,
	ErrorRateMinFailures: 3,
}

func TestListProxyTransportFailures_GroupsByEventAttribution(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	client := testEntClient(t)

	activeBefore, err := repo.CountActiveProxies(ctx)
	require.NoError(t, err)

	insertProxy := func(name, status string, deleted bool) int64 {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			INSERT INTO proxies (name, protocol, host, port, status, deleted_at)
			VALUES ($1, 'socks5', '127.0.0.1', 10808, $2, CASE WHEN $3 THEN NOW() END)
			RETURNING id`, name, status, deleted).Scan(&id))
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE id = $1", id)
		})
		return id
	}
	alpha := insertProxy("alpha", "active", false)
	beta := insertProxy("beta", "active", true)
	gamma := insertProxy("gamma", "active", false)
	insertProxy("delta", "inactive", false)
	epsilon := insertProxy("epsilon", "active", false)
	zeta := insertProxy("zeta", "active", false)

	activeAfter, err := repo.CountActiveProxies(ctx)
	require.NoError(t, err)
	require.Equal(t, activeBefore+4, activeAfter)

	user := mustCreateUser(t, client, &service.User{})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	alphaAccount := mustCreateAccount(t, client, &service.Account{Name: "alpha-acc", Platform: service.PlatformOpenAI, ProxyID: &alpha})
	gammaAccount := mustCreateAccount(t, client, &service.Account{Name: "gamma-acc", Platform: service.PlatformOpenAI, ProxyID: &gamma})
	zetaAccount := mustCreateAccount(t, client, &service.Account{Name: "zeta-acc", Platform: service.PlatformOpenAI, ProxyID: &zeta})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = ANY($1)", []int64{alphaAccount.ID, gammaAccount.ID, zetaAccount.ID})
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	})
	insertSuccesses := func(accountID int64, at time.Time, n int) {
		for i := 0; i < n; i++ {
			_, err := integrationDB.ExecContext(ctx, `
				INSERT INTO usage_logs (user_id, api_key_id, account_id, model, created_at)
				VALUES ($1, $2, $3, 'gpt-test', $4)`, user.ID, apiKey.ID, accountID, at)
			require.NoError(t, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Minute)
	insertError := func(createdAt time.Time, platform string, countTokens bool, events ...map[string]any) {
		raw, err := json.Marshal(events)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (error_phase, error_type, severity, status_code, platform, is_count_tokens, upstream_errors, created_at)
			VALUES ('upstream', 'upstream_error', 'error', 200, $1, $2, $3::jsonb, $4)`,
			platform, countTokens, string(raw), createdAt)
		require.NoError(t, err)
	}
	transport := func(proxyID any, proxyName string, accountID int64, accountName, message string, at time.Time) map[string]any {
		ev := map[string]any{
			"kind":         "request_error",
			"proxy_id":     proxyID,
			"proxy_name":   proxyName,
			"account_id":   accountID,
			"account_name": accountName,
			"message":      message,
		}
		if !at.IsZero() {
			ev["at_unix_ms"] = at.UnixMilli()
		}
		return ev
	}

	// alpha: three failures spread out (no fault period) plus attempts that reached the upstream.
	insertError(now.Add(-2*time.Minute), "openai", false,
		transport(alpha, "alpha-old", 11, "acc-a", "socks connect old", now.Add(-50*time.Minute)),
		map[string]any{"kind": "failover", "upstream_status_code": 503, "proxy_id": alpha, "proxy_name": "alpha"},
	)
	insertError(now.Add(-time.Minute), "openai", false,
		transport(alpha, "alpha", 12, "acc-b", "socks connect new", now.Add(-time.Minute)))
	// Time is the request end (row created_at), like the error list drill-down: a row
	// ending after the window is out even if its attempt was inside, and a row ending
	// inside counts even if its attempt was earlier.
	insertError(now.Add(20*time.Minute), "openai", false,
		transport(alpha, "alpha", 18, "acc-h", "late row", now.Add(-3*time.Minute)))
	insertError(now.Add(-30*time.Minute), "openai", false,
		transport(alpha, "alpha", 19, "acc-i", "early attempt", now.Add(-2*time.Hour)))
	insertSuccesses(alphaAccount.ID, now.Add(-20*time.Minute), 20)
	insertSuccesses(alphaAccount.ID, now.Add(-2*time.Hour), 5)

	// gamma: three failures inside one minute with no success nearby is a fault period.
	faultMinute := now.Add(-30 * time.Minute)
	insertError(faultMinute.Add(40*time.Second), "openai", false,
		transport(gamma, "gamma", 21, "acc-k", "dial tcp: connection refused", faultMinute.Add(10*time.Second)),
		transport(gamma, "gamma", 21, "acc-k", "dial tcp: connection refused", faultMinute.Add(20*time.Second)),
		transport(gamma, "gamma", 21, "acc-k", "dial tcp: connection refused", faultMinute.Add(30*time.Second)),
	)
	insertSuccesses(gammaAccount.ID, faultMinute.Add(-10*time.Minute), 1)

	// zeta: failures across minute boundaries within five minutes; the timeline must
	// carry all of them plus the successes that can share a window with them.
	zetaBase := now.Add(-45 * time.Minute)
	for _, offset := range []time.Duration{50 * time.Second, 3 * time.Minute, 5*time.Minute + 10*time.Second} {
		insertError(zetaBase.Add(offset), "openai", false,
			transport(zeta, "zeta", 31, "acc-z", "socks connect", zetaBase.Add(offset)))
	}
	insertSuccesses(zetaAccount.ID, zetaBase.Add(-time.Minute), 1)
	insertSuccesses(zetaAccount.ID, zetaBase.Add(6*time.Minute), 1)

	insertError(now.Add(-10*time.Minute), "openai", false,
		transport(nil, "direct/no_proxy", 13, "acc-c", "dial tcp: i/o timeout", time.Time{}))
	insertError(now.Add(-10*time.Minute), "openai", false,
		transport(beta, "beta", 14, "acc-d", "connection refused", time.Time{}))
	insertError(now.Add(-10*time.Minute), "openai", false,
		map[string]any{"kind": "request_error", "message": "legacy event"})
	// Excluded: a request canceled by the client.
	canceled := transport(alpha, "alpha", 20, "acc-j", "context canceled", now.Add(-time.Minute))
	canceled["reason"] = "request_canceled"
	insertError(now.Add(-time.Minute), "openai", false, canceled)
	// A proxy whose only attempts were canceled requests has no failures at all.
	epsilonCanceled := transport(epsilon, "epsilon", 41, "acc-x", "context canceled", now.Add(-5*time.Minute))
	epsilonCanceled["reason"] = "request_canceled"
	for i := 0; i < 3; i++ {
		insertError(now.Add(-5*time.Minute), "openai", false, epsilonCanceled)
	}
	// Excluded: other platform, count_tokens probe, outside window, and HTTP-status attempts as failures.
	insertError(now.Add(-time.Minute), "anthropic", false, transport(alpha, "alpha", 15, "acc-e", "x", now))
	insertError(now.Add(-time.Minute), "openai", true, transport(alpha, "alpha", 16, "acc-f", "x", now))
	insertError(now.Add(-3*time.Hour), "openai", false, transport(alpha, "alpha", 17, "acc-g", "x", time.Time{}))

	filter := &service.OpsDashboardFilter{
		StartTime: now.Add(-time.Hour),
		EndTime:   now.Add(time.Minute),
		Platform:  "openai",
	}
	items, err := repo.ListProxyTransportFailures(ctx, filter, opsProxyHealthTestRules)
	require.NoError(t, err)

	byKey := map[string]*service.OpsProxyHealthItem{}
	for _, item := range items {
		key := item.Route
		if item.ProxyID != nil {
			key = item.ProxyName
		}
		byKey[key] = item
	}
	require.Len(t, byKey, 6)
	require.NotContains(t, byKey, "epsilon", "canceled attempts are not proxy failures")

	alphaItem := byKey["alpha"]
	require.NotNil(t, alphaItem)
	require.Equal(t, service.OpsProxyRouteProxy, alphaItem.Route)
	require.Equal(t, alpha, *alphaItem.ProxyID)
	require.Equal(t, "alpha", alphaItem.CurrentName)
	require.Equal(t, "active", alphaItem.CurrentStatus)
	require.Equal(t, int64(3), alphaItem.FailedAttempts)
	require.Equal(t, int64(21), alphaItem.OKAttempts, "20 in-range successes by current binding + 1 HTTP-status attempt")
	require.Nil(t, alphaItem.FaultTimeline, "spread-out failures never gather enough failures in one window")
	require.Equal(t, int64(3), alphaItem.AffectedAccountCount)
	require.ElementsMatch(t, []string{"acc-a", "acc-b", "acc-i"}, alphaItem.AccountNames)
	require.Equal(t, "socks connect new", alphaItem.LastError)
	require.WithinDuration(t, now.Add(-time.Minute), alphaItem.LastFailedAt, time.Second)

	gammaItem := byKey["gamma"]
	require.NotNil(t, gammaItem)
	require.Equal(t, int64(3), gammaItem.FailedAttempts)
	require.Equal(t, int64(1), gammaItem.OKAttempts)
	require.NotNil(t, gammaItem.FaultTimeline)
	require.Len(t, gammaItem.FaultTimeline.Failures, 3)
	for _, at := range gammaItem.FaultTimeline.Failures {
		require.True(t, faultMinute.Add(40*time.Second).Equal(at), "time is the request end: %s", at)
	}
	require.Empty(t, gammaItem.FaultTimeline.OKs, "a success ten minutes earlier cannot share a window")

	zetaItem := byKey["zeta"]
	require.NotNil(t, zetaItem)
	require.Equal(t, int64(3), zetaItem.FailedAttempts)
	require.Equal(t, int64(2), zetaItem.OKAttempts)
	require.NotNil(t, zetaItem.FaultTimeline)
	micros := func(times ...time.Time) []int64 {
		out := make([]int64, 0, len(times))
		for _, at := range times {
			out = append(out, at.UnixMicro())
		}
		return out
	}
	require.Equal(t, micros(
		zetaBase.Add(50*time.Second),
		zetaBase.Add(3*time.Minute),
		zetaBase.Add(5*time.Minute+10*time.Second),
	), micros(zetaItem.FaultTimeline.Failures...))
	require.Equal(t, micros(zetaBase.Add(6*time.Minute)), micros(zetaItem.FaultTimeline.OKs...))

	betaItem := byKey["beta"]
	require.NotNil(t, betaItem)
	require.Equal(t, "deleted", betaItem.CurrentStatus)

	direct := byKey[service.OpsProxyRouteDirect]
	require.NotNil(t, direct)
	require.Nil(t, direct.ProxyID)
	require.Equal(t, int64(1), direct.FailedAttempts)
	require.Zero(t, direct.OKAttempts)
	require.Empty(t, direct.CurrentStatus)

	unknown := byKey[service.OpsProxyRouteUnknown]
	require.NotNil(t, unknown)
	require.Equal(t, int64(1), unknown.FailedAttempts)
	require.Equal(t, int64(0), unknown.AffectedAccountCount)
	require.Empty(t, unknown.AccountNames)

	var proxyFailures int64
	for _, item := range items {
		if item.Route == service.OpsProxyRouteProxy {
			proxyFailures += item.FailedAttempts
		}
	}
	require.Positive(t, proxyFailures)
	count, err := repo.CountProxyTransportFailures(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, proxyFailures, count, "the alert count is the card's managed-proxy failures")
}
