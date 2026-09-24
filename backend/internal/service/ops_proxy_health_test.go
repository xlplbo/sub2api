//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func opsProxyHealthTestItem(route string, id int64, failed, ok int64) *OpsProxyHealthItem {
	item := &OpsProxyHealthItem{Route: route, FailedAttempts: failed, OKAttempts: ok}
	if id > 0 {
		item.ProxyID = &id
	}
	return item
}

func withFault(item *OpsProxyHealthItem) *OpsProxyHealthItem {
	base := time.Date(2026, 9, 18, 6, 33, 0, 0, time.UTC)
	item.FaultTimeline = &OpsProxyFaultTimeline{Failures: []time.Time{base, base.Add(time.Second), base.Add(2 * time.Second)}}
	return item
}

func clock(hms string) time.Time {
	t, err := time.Parse("15:04:05", hms)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, 9, 18, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func clocks(values ...string) []time.Time {
	out := make([]time.Time, 0, len(values))
	for _, v := range values {
		out = append(out, clock(v))
	}
	return out
}

func TestFindOpsProxyFaultPeriod_RollingWindowOnExactTimes(t *testing.T) {
	tests := []struct {
		name     string
		fails    []time.Time
		oks      []time.Time
		start    string
		end      string
		wantFrom string
		wantTo   string
	}{
		{
			name:     "failures across minute boundaries within five minutes",
			fails:    clocks("12:00:50", "12:03:00", "12:05:10"),
			wantFrom: "12:00:50",
			wantTo:   "12:05:10",
		},
		{
			name:  "exactly five minutes apart is not one window",
			fails: clocks("12:00:00", "12:02:30", "12:05:00"),
		},
		{
			name:     "one second inside five minutes",
			fails:    clocks("12:00:01", "12:02:30", "12:05:00"),
			wantFrom: "12:00:01",
			wantTo:   "12:05:00",
		},
		{
			name:     "ok leaving the window start reveals the fault",
			fails:    clocks("12:01:00", "12:02:00", "12:03:00"),
			oks:      clocks("12:00:30", "12:05:45"),
			wantFrom: "12:01:00",
			wantTo:   "12:03:00",
		},
		{
			name:  "ok between the failures dilutes every window",
			fails: clocks("12:00:00", "12:01:00", "12:02:00"),
			oks:   clocks("12:01:30"),
		},
		{
			// Every window holding three or more failures also holds one of the oks.
			name:     "nine failures between two oks reach exactly ninety percent",
			fails:    clocks("12:00:01", "12:00:02", "12:00:03", "12:00:04", "12:00:05", "12:00:06", "12:00:07", "12:00:08", "12:00:09"),
			oks:      clocks("12:00:00", "12:00:10"),
			wantFrom: "12:00:01",
			wantTo:   "12:00:09",
		},
		{
			name:  "eight failures between two oks stay below ninety percent",
			fails: clocks("12:00:01", "12:00:02", "12:00:03", "12:00:04", "12:00:05", "12:00:06", "12:00:07", "12:00:08"),
			oks:   clocks("12:00:00", "12:00:09"),
		},
		{
			name:     "a sparse ok cannot dilute failures on one side of it",
			fails:    clocks("12:00:01", "12:00:02", "12:00:03", "12:00:04", "12:00:06"),
			oks:      clocks("12:00:05"),
			wantFrom: "12:00:01",
			wantTo:   "12:00:04",
		},
		{
			name:     "attempts ending at the same instant",
			fails:    clocks("12:00:00", "12:00:00", "12:00:00"),
			wantFrom: "12:00:00",
			wantTo:   "12:00:00",
		},
		{
			name:     "period spans all faulty windows",
			fails:    clocks("12:00:00", "12:01:00", "12:02:00", "12:06:30", "12:07:00", "12:08:00", "12:40:00"),
			oks:      clocks("12:20:00"),
			wantFrom: "12:00:00",
			wantTo:   "12:08:00",
		},
		{
			name:  "two failures never reach the count",
			fails: clocks("12:00:00", "12:00:01"),
		},
		{
			// Every window ending by 12:04 that holds the failures also holds the ok.
			name:  "a window past the range end cannot drop the ok",
			fails: clocks("12:01:00", "12:02:00", "12:03:00"),
			oks:   clocks("12:00:30"),
			end:   "12:04:00",
		},
		{
			name:  "the last window ending at the range end still holds the ok",
			fails: clocks("12:01:00", "12:02:00", "12:03:00"),
			oks:   clocks("12:00:30"),
			end:   "12:05:30",
		},
		{
			name:     "the ok leaves a window that ends inside the range",
			fails:    clocks("12:01:00", "12:02:00", "12:03:00"),
			oks:      clocks("12:00:30"),
			end:      "12:05:31",
			wantFrom: "12:01:00",
			wantTo:   "12:03:00",
		},
		{
			name:  "a window before the range start cannot drop the ok",
			fails: clocks("12:00:10", "12:00:20", "12:00:30"),
			oks:   clocks("12:04:50"),
			start: "12:00:00",
		},
		{
			name:     "a window starting at the range start excludes a later ok",
			fails:    clocks("12:00:10", "12:00:20", "12:00:30"),
			oks:      clocks("12:04:50"),
			start:    "11:59:50",
			wantFrom: "12:00:10",
			wantTo:   "12:00:30",
		},
		{
			name:     "a range shorter than the window is one window",
			fails:    clocks("12:00:10", "12:00:20", "12:00:30"),
			start:    "12:00:00",
			end:      "12:03:00",
			wantFrom: "12:00:10",
			wantTo:   "12:00:30",
		},
		{
			name:  "an ok anywhere in a short range dilutes it",
			fails: clocks("12:00:10", "12:00:20", "12:00:30"),
			oks:   clocks("12:02:59"),
			start: "12:00:00",
			end:   "12:03:00",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := clock("11:00:00"), clock("13:00:00")
			if tt.start != "" {
				start = clock(tt.start)
			}
			if tt.end != "" {
				end = clock(tt.end)
			}
			from, to := findOpsProxyFaultPeriod(&OpsProxyFaultTimeline{Failures: tt.fails, OKs: tt.oks}, opsProxyHealthRules, start, end)
			if tt.wantFrom == "" {
				require.Nil(t, from)
				require.Nil(t, to)
				return
			}
			require.NotNil(t, from)
			require.NotNil(t, to)
			require.Equal(t, clock(tt.wantFrom), *from)
			require.Equal(t, clock(tt.wantTo), *to)
		})
	}
}

func TestFindOpsProxyFaultPeriod_WithoutTimeline(t *testing.T) {
	from, to := findOpsProxyFaultPeriod(nil, opsProxyHealthRules, clock("11:00:00"), clock("13:00:00"))
	require.Nil(t, from)
	require.Nil(t, to)
}

func opsProxyHealthItemIDs(items []*OpsProxyHealthItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.ProxyID != nil {
			out = append(out, item.Route+":"+strconv.FormatInt(*item.ProxyID, 10))
			continue
		}
		out = append(out, item.Route)
	}
	return out
}

func TestBuildOpsProxyHealth_ClassifiesByFaultPeriodAndErrorRate(t *testing.T) {
	health := buildOpsProxyHealth([]*OpsProxyHealthItem{
		opsProxyHealthTestItem(OpsProxyRouteUnknown, 0, 5, 0),
		opsProxyHealthTestItem(OpsProxyRouteProxy, 3, 14, 46939),
		opsProxyHealthTestItem(OpsProxyRouteDirect, 0, 3, 0),
		withFault(opsProxyHealthTestItem(OpsProxyRouteProxy, 4, 815, 54057)),
		opsProxyHealthTestItem(OpsProxyRouteProxy, 2, 6, 94),
		opsProxyHealthTestItem(OpsProxyRouteProxy, 5, 2, 0),
		nil,
	}, 4, opsProxyHealthRules, clock("06:30:00"), clock("07:30:00"))

	require.Equal(t, opsProxyHealthRules, health.Rules)
	require.Equal(t, int64(4), health.ActiveProxyCount)
	require.Equal(t, 4, health.FailedProxyCount)
	require.Equal(t, 1, health.AbnormalProxyCount)
	require.Equal(t, 1, health.HighErrorRateProxyCount)
	require.Equal(t, int64(837), health.FailedAttempts, "direct and unknown routes are not proxy failures")
	require.False(t, health.Truncated)
	require.Equal(t, []string{"proxy:4", "proxy:2", "proxy:3", "proxy:5", OpsProxyRouteDirect, OpsProxyRouteUnknown}, opsProxyHealthItemIDs(health.Items))

	byID := map[int64]*OpsProxyHealthItem{}
	for _, item := range health.Items {
		if item.ProxyID != nil {
			byID[*item.ProxyID] = item
		}
	}
	require.Equal(t, OpsProxyStatusFault, byID[4].Status, "a fault period is red even when the range rate is low")
	require.Nil(t, byID[4].FaultTimeline, "cached snapshots must not keep the times")
	require.InDelta(t, 815.0/54872.0, byID[4].FailureRate, 1e-9)
	require.Equal(t, OpsProxyStatusHighErrorRate, byID[2].Status, "6% with 6 failures")
	require.Equal(t, OpsProxyStatusOK, byID[3].Status)
	require.Equal(t, OpsProxyStatusOK, byID[5].Status, "100% but below the minimum failure count")
	require.InDelta(t, 1.0, byID[5].FailureRate, 1e-9)
	require.Empty(t, health.Items[4].Status, "direct routes carry no status")
}

func TestBuildOpsProxyHealth_CountsBeforeTruncation(t *testing.T) {
	items := make([]*OpsProxyHealthItem, 0, opsProxyHealthMaxItems+6)
	for i := 1; i <= opsProxyHealthMaxItems+5; i++ {
		items = append(items, withFault(opsProxyHealthTestItem(OpsProxyRouteProxy, int64(i), int64(i), 0)))
	}
	items = append(items, opsProxyHealthTestItem(OpsProxyRouteDirect, 0, 1, 0))

	health := buildOpsProxyHealth(items, 100, opsProxyHealthRules, clock("06:30:00"), clock("07:30:00"))

	require.True(t, health.Truncated)
	require.Equal(t, opsProxyHealthMaxItems+5, health.FailedProxyCount)
	require.Equal(t, opsProxyHealthMaxItems+5, health.AbnormalProxyCount, "faults are counted before truncation")
	require.Len(t, health.Items, opsProxyHealthMaxItems+1)
	require.Equal(t, int64(opsProxyHealthMaxItems+5), *health.Items[0].ProxyID, "most failures first")
	require.Equal(t, OpsProxyRouteDirect, health.Items[len(health.Items)-1].Route)
}

func TestBuildOpsProxyHealth_EmptyItemsSerializeAsEmptySlice(t *testing.T) {
	health := buildOpsProxyHealth(nil, 0, opsProxyHealthRules, clock("06:30:00"), clock("07:30:00"))
	require.NotNil(t, health.Items)
	require.Empty(t, health.Items)
}

func TestGetDashboardOverview_AttachesProxyHealth(t *testing.T) {
	end := time.Date(2026, 9, 18, 7, 30, 0, 0, time.UTC)
	start := end.Add(-time.Hour)
	repo := &opsRepoMock{
		ListProxyTransportFailuresFn: func(_ context.Context, filter *OpsDashboardFilter, rules OpsProxyHealthRules) ([]*OpsProxyHealthItem, error) {
			require.Equal(t, start, filter.StartTime)
			require.Equal(t, end, filter.EndTime)
			require.Equal(t, opsProxyHealthRules, rules)
			return []*OpsProxyHealthItem{withFault(opsProxyHealthTestItem(OpsProxyRouteProxy, 4, 499, 0))}, nil
		},
	}
	svc := &OpsService{opsRepo: repo}

	overview, err := svc.GetDashboardOverview(context.Background(), &OpsDashboardFilter{StartTime: start, EndTime: end, QueryMode: OpsQueryModeRaw})

	require.NoError(t, err)
	require.NotNil(t, overview.ProxyHealth)
	require.Equal(t, int64(499), overview.ProxyHealth.FailedAttempts)
	require.Equal(t, 1, overview.ProxyHealth.AbnormalProxyCount)
	require.Len(t, overview.ProxyHealth.Items, 1)
}

func TestGetDashboardOverview_ProxyHealthFailureDoesNotBlockDashboard(t *testing.T) {
	end := time.Now().UTC()
	repo := &opsRepoMock{
		ListProxyTransportFailuresFn: func(context.Context, *OpsDashboardFilter, OpsProxyHealthRules) ([]*OpsProxyHealthItem, error) {
			return nil, errors.New("boom")
		},
	}
	svc := &OpsService{opsRepo: repo}

	overview, err := svc.GetDashboardOverview(context.Background(), &OpsDashboardFilter{StartTime: end.Add(-time.Hour), EndTime: end, QueryMode: OpsQueryModeRaw})

	require.NoError(t, err)
	require.Nil(t, overview.ProxyHealth)
}

func TestComputeRuleMetric_ProxyTransportErrorCount(t *testing.T) {
	end := time.Now().UTC()
	start := end.Add(-5 * time.Minute)
	groupID := int64(10)
	repo := &opsRepoMock{
		CountProxyTransportFailuresFn: func(_ context.Context, filter *OpsDashboardFilter) (int64, error) {
			require.Equal(t, start, filter.StartTime)
			require.Equal(t, end, filter.EndTime)
			require.Equal(t, "openai", filter.Platform)
			require.Equal(t, &groupID, filter.GroupID)
			return 14, nil
		},
		ListProxyTransportFailuresFn: func(context.Context, *OpsDashboardFilter, OpsProxyHealthRules) ([]*OpsProxyHealthItem, error) {
			t.Fatal("the alert only needs the failure count")
			return nil, nil
		},
	}
	svc := &OpsAlertEvaluatorService{opsRepo: repo}

	val, ok := svc.computeRuleMetric(context.Background(), &OpsAlertRule{MetricType: "proxy_transport_error_count"}, nil, start, end, "openai", &groupID)

	require.True(t, ok)
	require.InDelta(t, 14.0, val, 0.0001)
}
