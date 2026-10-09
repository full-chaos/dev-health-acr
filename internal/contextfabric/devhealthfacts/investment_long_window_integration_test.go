package devhealthfacts_test

// A 180-day investment read is ONE true window over the stored work units. A
// unit that spans the boundary of two 60-day windows counts in both of them,
// so the three stitched 60-day reads overcount it; a window that starts before
// the earliest stored unit is served over the available span with a
// limitation row, never zero-filled. EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestLongInvestmentWindowIsOneTrueWindowAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-long-window"
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	owned := end.Add(-400 * day)

	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-l"), orgID, "acme/repo-l", "github", owned); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-long", repoUUID("repo-l"), "acme/repo-l", "exact", "native", uint8(1), uint16(100), int32(0), owned, nil, owned); err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	seed := func(id string, from, to time.Time, effort float64) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, from, to, effort, map[string]float64{"feature_delivery": 1.0}, map[string]float64{},
			fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID("repo-l"), prNumber(id)), to, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	// Window A = [end-180d, end-120d), B = [end-120d, end-60d), C = [end-60d, end).
	seed("in-a", end.Add(-181*day), end.Add(-165*day), 10)
	seed("in-b", end.Add(-100*day), end.Add(-95*day), 7)
	seed("spans-bc", end.Add(-70*day), end.Add(-50*day), 20)
	seed("in-c", end.Add(-20*day), end.Add(-15*day), 10)

	// A second team on its own repository whose earliest unit is far later: the
	// span is per subject, so the same 180-day read names it for this team only.
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-m"), orgID, "acme/repo-m", "github", owned); err != nil {
		t.Fatalf("seed repo m: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-late", repoUUID("repo-m"), "acme/repo-m", "exact", "native", uint8(1), uint16(100), int32(0), owned, nil, owned); err != nil {
		t.Fatalf("seed ownership m: %v", err)
	}
	if err := direct.Exec(ctx,
		`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		"late", end.Add(-100*day), end.Add(-95*day), 5.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{},
		fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID("repo-m"), 424242), end.Add(-95*day), orgID); err != nil {
		t.Fatalf("seed late wu: %v", err)
	}

	read := func(days int) (contextfabric.FactProviderResult, float64) {
		t.Helper()
		start := end.Add(-time.Duration(days) * day)
		stop := end
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &stop},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-long")},
		})
		if err != nil {
			t.Fatalf("ReadFacts %dd: %v", days, err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("%dd facts = %d, want 1 (reason %q)", days, len(result.Facts), result.Reason)
		}
		total := 0.0
		for _, row := range result.Facts[0].Fields["theme_breakdown"].Table.Rows {
			total += *row.Fields["weighted_effort"].Number
		}
		return result, total
	}
	// Stitch three 60-day windows by reading each as a range.
	stitched := 0.0
	for i := 0; i < 3; i++ {
		start := end.Add(-time.Duration(180-60*i) * day)
		stop := start.Add(60 * day)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &stop},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-long")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("stitch window %d: err=%v facts=%d", i, err, len(result.Facts))
		}
		for _, row := range result.Facts[0].Fields["theme_breakdown"].Table.Rows {
			stitched += *row.Fields["weighted_effort"].Number
		}
	}
	whole, wholeTotal := read(180)
	if math.Abs(wholeTotal-47) > 1e-9 {
		t.Fatalf("180d total = %v, want 47 (each unit once)", wholeTotal)
	}
	if math.Abs(stitched-67) > 1e-9 {
		t.Fatalf("stitched total = %v, want 67 (the boundary-spanning unit counted in both windows)", stitched)
	}
	if strings.Contains(whole.Reason, "investment_window_beyond_stored_history") {
		t.Fatalf("a window inside the stored history carries a span limitation: %q", whole.Reason)
	}

	// Window longer than the stored history: earliest stored unit starts at end-181d.
	long, longTotal := read(365)
	if math.Abs(longTotal-47) > 1e-9 {
		t.Fatalf("365d total = %v, want 47 (days before the history are not zero-filled into the mix)", longTotal)
	}
	if !strings.Contains(long.Reason, "investment_window_beyond_stored_history") || !strings.Contains(long.Reason, end.Add(-181*day).Format(time.RFC3339)) {
		t.Fatalf("365d reason = %q, want the span limitation naming the earliest stored unit", long.Reason)
	}
	// Two teams, one read: only the team whose own history starts after the window start is named.
	startSix := end.Add(-180 * day)
	both, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &startSix, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-long"), teamSubject("team-late")},
	})
	if err != nil || len(both.Facts) != 2 {
		t.Fatalf("two-team read: err=%v facts=%d reason=%q", err, len(both.Facts), both.Reason)
	}
	if !strings.Contains(both.Reason, "team:team-late: the window starts") || !strings.Contains(both.Reason, end.Add(-100*day).Format(time.RFC3339)) {
		t.Fatalf("team-late reason = %q, want its own earliest unit", both.Reason)
	}
	if strings.Contains(both.Reason, "team:team-long") {
		t.Fatalf("team-long (history starts before the window) carries a span reason: %q", both.Reason)
	}

	// A 60-day window inside the stored history carries no span limitation.
	short, _ := read(60)
	if strings.Contains(short.Reason, "investment_window_beyond_stored_history") {
		t.Fatalf("60d reason = %q", short.Reason)
	}
}

// A 60-day window over 30 days of history is the same partial truth.
func TestShortInvestmentWindowBeyondHistoryNamesTheSpanAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-short-history"
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	owned := end.Add(-400 * day)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-s"), orgID, "acme/repo-s", "github", owned); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-short", repoUUID("repo-s"), "acme/repo-s", "exact", "native", uint8(1), uint16(100), int32(0), owned, nil, owned); err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	from, to := end.Add(-30*day), end.Add(-10*day)
	if err := direct.Exec(ctx,
		`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		"only", from, to, 9.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{},
		fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, repoUUID("repo-s")), to, orgID); err != nil {
		t.Fatalf("seed wu: %v", err)
	}
	start, stop := end.Add(-60*day), end
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &stop},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-short")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("read: err=%v facts=%d", err, len(result.Facts))
	}
	total := 0.0
	for _, row := range result.Facts[0].Fields["theme_breakdown"].Table.Rows {
		total += *row.Fields["weighted_effort"].Number
	}
	if math.Abs(total-9) > 1e-9 {
		t.Fatalf("total = %v, want 9", total)
	}
	if !strings.Contains(result.Reason, "investment_window_beyond_stored_history") || !strings.Contains(result.Reason, from.Format(time.RFC3339)) {
		t.Fatalf("reason = %q, want the span limitation naming %s", result.Reason, from.Format(time.RFC3339))
	}
}

// prNumber gives each seeded work unit its own pull request number: a ref
// resolves to a repository only in the shape <repo uuid>#pr<number>.
func prNumber(id string) int {
	sum := 0
	for _, r := range id {
		sum = sum*31 + int(r)
	}
	if sum < 0 {
		sum = -sum
	}
	return sum%100000 + 1
}
