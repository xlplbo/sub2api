package service

import (
	"context"
	"sort"
	"time"
)

const (
	opsProxyHealthMaxItems     = 50
	opsProxyHealthQueryTimeout = 2 * time.Second
)

// 与其他系统卡片一致，判定阈值写死；随结果下发供前端展示口径。
var opsProxyHealthRules = OpsProxyHealthRules{
	FaultWindowMinutes:   5,
	FaultFailureRate:     0.9,
	FaultMinFailures:     3,
	ErrorRate:            0.05,
	ErrorRateMinFailures: 3,
}

func (s *OpsService) getProxyHealth(ctx context.Context, filter *OpsDashboardFilter) (*OpsProxyHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, opsProxyHealthQueryTimeout)
	defer cancel()

	items, err := s.opsRepo.ListProxyTransportFailures(ctx, filter, opsProxyHealthRules)
	if err != nil {
		return nil, err
	}
	activeProxyCount, err := s.opsRepo.CountActiveProxies(ctx)
	if err != nil {
		return nil, err
	}
	return buildOpsProxyHealth(items, activeProxyCount, opsProxyHealthRules, filter.StartTime, filter.EndTime), nil
}

func buildOpsProxyHealth(items []*OpsProxyHealthItem, activeProxyCount int64, rules OpsProxyHealthRules, start, end time.Time) *OpsProxyHealth {
	out := &OpsProxyHealth{
		Rules:            rules,
		ActiveProxyCount: activeProxyCount,
	}

	proxies := make([]*OpsProxyHealthItem, 0, len(items))
	others := make([]*OpsProxyHealthItem, 0, 2)
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.Route != OpsProxyRouteProxy {
			others = append(others, item)
			continue
		}
		classifyOpsProxyHealthItem(item, rules, start, end)
		switch item.Status {
		case OpsProxyStatusFault:
			out.AbnormalProxyCount++
		case OpsProxyStatusHighErrorRate:
			out.HighErrorRateProxyCount++
		}
		out.FailedAttempts += item.FailedAttempts
		proxies = append(proxies, item)
	}
	out.FailedProxyCount = len(proxies)

	sort.SliceStable(proxies, func(i, j int) bool {
		a, b := proxies[i], proxies[j]
		if ra, rb := opsProxyStatusRank(a.Status), opsProxyStatusRank(b.Status); ra != rb {
			return ra < rb
		}
		if a.FailedAttempts != b.FailedAttempts {
			return a.FailedAttempts > b.FailedAttempts
		}
		return opsProxyHealthItemID(a) < opsProxyHealthItemID(b)
	})
	if len(proxies) > opsProxyHealthMaxItems {
		proxies = proxies[:opsProxyHealthMaxItems]
		out.Truncated = true
	}
	sort.SliceStable(others, func(i, j int) bool {
		return others[i].Route == OpsProxyRouteDirect && others[j].Route != OpsProxyRouteDirect
	})

	out.Items = append(proxies, others...)
	return out
}

func classifyOpsProxyHealthItem(item *OpsProxyHealthItem, rules OpsProxyHealthRules, start, end time.Time) {
	if total := item.FailedAttempts + item.OKAttempts; total > 0 {
		item.FailureRate = float64(item.FailedAttempts) / float64(total)
	}
	item.FaultFrom, item.FaultTo = findOpsProxyFaultPeriod(item.FaultTimeline, rules, start, end)
	item.FaultTimeline = nil
	switch {
	case item.FaultFrom != nil:
		item.Status = OpsProxyStatusFault
	case item.FailedAttempts >= rules.ErrorRateMinFailures && item.FailureRate >= rules.ErrorRate:
		item.Status = OpsProxyStatusHighErrorRate
	default:
		item.Status = OpsProxyStatusOK
	}
}

// findOpsProxyFaultPeriod checks every distinct window [t, t+W) on exact times
// that lies inside the queried range [start, end); nothing outside the range was
// queried, so a window crossing either edge would miss its oks. A range shorter
// than W is a single window. An event e is inside [t, t+W) for t in (e-W, e], so
// the window content only changes at t = e-W and t = e; those t within
// [start, end-W], plus end-W itself, cover every distinct window. The period
// spans the first and last failure of all windows that meet the rules.
func findOpsProxyFaultPeriod(timeline *OpsProxyFaultTimeline, rules OpsProxyHealthRules, start, end time.Time) (from, to *time.Time) {
	if timeline == nil || len(timeline.Failures) == 0 || rules.FaultWindowMinutes <= 0 || !end.After(start) {
		return nil, nil
	}
	fails := sortedOpsTimes(timeline.Failures)
	oks := sortedOpsTimes(timeline.OKs)
	window := min(time.Duration(rules.FaultWindowMinutes)*time.Minute, end.Sub(start))
	lastStart := end.Add(-window)

	check := func(t time.Time) {
		if t.Before(start) || t.After(lastStart) {
			return
		}
		fi, fj := opsTimesInWindow(fails, t, t.Add(window))
		failed := fj - fi
		if failed < int(rules.FaultMinFailures) || failed <= 0 {
			return
		}
		oi, oj := opsTimesInWindow(oks, t, t.Add(window))
		if float64(failed) < rules.FaultFailureRate*float64(failed+oj-oi)-1e-9 {
			return
		}
		if first := fails[fi]; from == nil || first.Before(*from) {
			from = &first
		}
		if last := fails[fj-1]; to == nil || last.After(*to) {
			to = &last
		}
	}
	check(lastStart)
	for _, times := range [][]time.Time{fails, oks} {
		for _, e := range times {
			check(e.Add(-window))
			check(e)
		}
	}
	return from, to
}

// opsTimesInWindow returns the index range of sorted times within [lo, hi).
func opsTimesInWindow(times []time.Time, lo, hi time.Time) (int, int) {
	i := sort.Search(len(times), func(k int) bool { return !times[k].Before(lo) })
	j := sort.Search(len(times), func(k int) bool { return !times[k].Before(hi) })
	return i, j
}

func sortedOpsTimes(times []time.Time) []time.Time {
	out := append([]time.Time(nil), times...)
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func opsProxyStatusRank(status string) int {
	switch status {
	case OpsProxyStatusFault:
		return 0
	case OpsProxyStatusHighErrorRate:
		return 1
	default:
		return 2
	}
}

func opsProxyHealthItemID(item *OpsProxyHealthItem) int64 {
	if item == nil || item.ProxyID == nil {
		return 0
	}
	return *item.ProxyID
}
