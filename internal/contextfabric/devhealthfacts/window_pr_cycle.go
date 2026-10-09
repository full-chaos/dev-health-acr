package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// The window PR cycle time is the median of (merged_at - created_at) over
// EVERY pull request merged inside the window, one weight per pull request.
// It is not derived from the repo-day medians in repo_metrics_daily: a
// median of daily medians, or a mean of them, weights a day with one pull
// request the same as a day with fifty, so a client must never average the
// daily median_pr_cycle_hours into a weekly or monthly value. A window in
// which no pull request merged has no value at all: the field is absent,
// never 0 and never carried from an earlier window.
const (
	windowPRCycleMedianField = "window_pr_cycle_hours_median"
	windowPRCountField       = "window_pr_count"
)

type windowPRCycle struct {
	count  int64
	median float64
}

// readWindowPRCycle runs one statement over git_pull_requests. grouped
// returns one entry per repository id; otherwise a single entry under the
// empty key covers every repository in repoKeys together (a team's owned
// set), so the median is over the union of their pull requests and not a
// combination of per-repository medians.
func readWindowPRCycle(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, repoKeys []string, window rollupWindow, grouped bool) (map[string]windowPRCycle, error) {
	out := map[string]windowPRCycle{}
	if len(repoKeys) == 0 {
		return out, nil
	}
	keyExpr, groupBy := "''", ""
	if grouped {
		keyExpr, groupBy = "toString(repo_id)", "\nGROUP BY repo_id"
	}
	statement := `SELECT ` + keyExpr + `,
	toInt64(count()),
	toFloat64(quantileExactInclusive(0.5)((toUnixTimestamp64Milli(assumeNotNull(merged_at)) - toUnixTimestamp64Milli(created_at)) / 3600000.0))
FROM git_pull_requests FINAL
WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
	AND merged_at IS NOT NULL AND ` + window.timestampExpr("merged_at") + groupBy
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadWindowPullRequestCycle", statement, orgID, repoKeys, func(row contextpacket.ClickHouseRowScanner) error {
		var key string
		var c windowPRCycle
		if err := row.Scan(&key, &c.count, &c.median); err != nil {
			return err
		}
		if c.count > 0 {
			out[key] = c
		}
		return nil
	}, window.bindings()...)
	return out, err
}

// setWindowPRCycle adds the window fields to a metrics fact's fields.
func setWindowPRCycle(fields map[string]contextfabric.FactValue, c windowPRCycle) {
	fields[windowPRCycleMedianField] = contextfabric.NumberFactValue(c.median)
	fields[windowPRCountField] = contextfabric.IntegerFactValue(c.count)
}
