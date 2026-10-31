package devhealthfacts_test

// CHAOS-12142, EXECUTED against a real ClickHouse. On the current axis a
// committed evidence window bounds the investment read, and with no window the
// read takes the platform default of the trailing 90 days. Each work unit
// counts whole, in the window that holds its end stamp: a unit ending at the
// window start counts, a unit ending at the window end does not, and a unit
// that starts before the window and ends inside it counts whole. Each read
// equals the range read of the same [start, end); all_time reads all history.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Not parallel: it sets the package clock.
func TestTheCurrentAxisInvestmentWindowCountsByEndStampAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	restore := devhealthfacts.SetClockForTest(func() time.Time { return time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC) })
	defer restore()
	const orgID = "org-current-window"
	day := 24 * time.Hour
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) // today 00:00 UTC by the clock above
	start := end.Add(-30 * day)                         // the committed trailing_30d window
	defaultStart := end.Add(-90 * day)                  // the platform default window
	owned := end.Add(-400 * day)

	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-w"), orgID, "acme/repo-w", "github", owned); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-w", repoUUID("repo-w"), "acme/repo-w", "exact", "native", uint8(1), uint16(100), int32(0), owned, nil, owned); err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	seed := func(id string, from, to time.Time) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, from, to, 10.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{},
			fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID("repo-w"), prNumber(id)), to, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	// 30-day window: in, in (whole), in. 90-day window: those three, plus
	// ends-before-start and ends-at-default-start.
	seed("ends-at-start", start.Add(-2*day), start)
	seed("spans-start", start.Add(-10*day), start.Add(5*day))
	seed("ends-before-end", end.Add(-3*day), end.Add(-time.Second))
	seed("ends-before-start", start.Add(-5*day), start.Add(-time.Second))
	seed("ends-at-default-start", defaultStart.Add(-4*day), defaultStart)
	seed("ends-at-end", end.Add(-2*day), end) // out of every window: the end is exclusive
	seed("ends-before-default-start", defaultStart.Add(-4*day), defaultStart.Add(-time.Second))
	seed("old", end.Add(-200*day), end.Add(-190*day))

	read := func(name string, q contextfabric.FactQuery) (float64, contextfabric.FactProviderResult) {
		t.Helper()
		q.Kind, q.Subjects = contextfabric.FactInvestment, []contextfabric.SubjectRef{teamSubject("team-w")}
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, q)
		if err != nil {
			t.Fatalf("%s: ReadFacts: %v", name, err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("%s: facts = %d, want 1 (reason %q)", name, len(result.Facts), result.Reason)
		}
		total := 0.0
		for _, row := range result.Facts[0].Fields["theme_breakdown"].Table.Rows {
			total += *row.Fields["weighted_effort"].Number
		}
		return total, result
	}
	current := func(window *contractsv1.ContextFabricRequestedEvidenceWindow) contextfabric.FactQuery {
		return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: window}}
	}
	ranged := func(from, to time.Time) contextfabric.FactQuery {
		return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &from, End: &to}, TimeEndExclusive: true}
	}
	check := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s total = %v, want %v", name, got, want)
		}
	}

	committed, _ := read("committed trailing_30d", current(&contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D}))
	check("committed trailing_30d (ends-at-start, spans-start, ends-before-end)", committed, 3)
	committedRange, _ := read("range 30d", ranged(start, end))
	check("range [start, end) beside the committed window", committedRange, committed)

	defaulted, defaultedResult := read("no window", current(nil))
	check("no window (the 30-day three, ends-before-start, ends-at-default-start)", defaulted, 5)
	defaultRange, _ := read("range 90d", ranged(defaultStart, end))
	check("range [today-90d, today) beside the default window", defaultRange, defaulted)
	// The note names the period, never its clock-derived dates.
	if !strings.Contains(defaultedResult.RequestNote, "investment_default_window: no period was given") || !strings.Contains(defaultedResult.RequestNote, "the trailing 90 days ending at 00:00 UTC today") || strings.Contains(defaultedResult.RequestNote, defaultStart.Format(time.RFC3339)) {
		t.Errorf("no-window RequestNote = %q, want the default-window note naming the trailing 90 days and no dates", defaultedResult.RequestNote)
	}

	all, _ := read("all_time", current(&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowAllTime}))
	check("all_time (every seeded unit)", all, 8)
}
