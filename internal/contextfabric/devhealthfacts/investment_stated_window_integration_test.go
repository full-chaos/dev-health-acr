package devhealthfacts_test

// CHAOS-12142, EXECUTED against a real ClickHouse. A current-axis investment
// read with a stated evidence window counts each work unit whole, in the window
// that holds its end stamp: a unit ending at the window start counts, a unit
// ending at the window end does not, and a unit that starts before the window
// and ends inside it counts whole. The read equals the range read of the same
// [start, end). An inferred window and no window read all history.

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAStatedCurrentAxisWindowBoundsInvestmentByEndStampAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-stated-window"
	day := 24 * time.Hour
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	start := end.Add(-30 * day)
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
	seed("ends-at-start", start.Add(-2*day), start)                 // in: its end stamp is the window start
	seed("spans-start", start.Add(-10*day), start.Add(5*day))       // in, whole: it ends inside the window
	seed("ends-before-end", end.Add(-3*day), end.Add(-time.Second)) // in
	seed("ends-before-start", start.Add(-5*day), start.Add(-time.Second))
	seed("ends-at-end", end.Add(-2*day), end) // out: the window end is exclusive
	seed("old", end.Add(-200*day), end.Add(-190*day))

	trailing := &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D}
	read := func(name string, q contextfabric.FactQuery) float64 {
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
		return total
	}
	current := func(window *contractsv1.ContextFabricRequestedEvidenceWindow, stated bool) contextfabric.FactQuery {
		return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: window}, EvidenceWindowStated: stated}
	}

	stated := read("stated window", current(trailing, true))
	if math.Abs(stated-3) > 1e-9 {
		t.Errorf("stated window total = %v, want 3 (ends-at-start, spans-start and ends-before-end, each once)", stated)
	}
	ranged := read("range", contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, TimeEndExclusive: true})
	if math.Abs(stated-ranged) > 1e-9 {
		t.Errorf("stated window total = %v, range total = %v; want the same read", stated, ranged)
	}
	for name, q := range map[string]contextfabric.FactQuery{
		"inferred window": current(trailing, false),
		"no window":       current(nil, false),
	} {
		if all := read(name, q); math.Abs(all-6) > 1e-9 {
			t.Errorf("%s total = %v, want 6 (all history)", name, all)
		}
	}
}
