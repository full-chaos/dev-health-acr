package devhealthfacts_test

// The stored-history span counts every persisted work unit, an evidence-gated
// one included: a gated unit is stored data even though the mix leaves it out
// (CHAOS-11083). One repository, owned by team-a, holds a gated unit on Jan 10
// and a categorized unit on Mar 10. EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestStoredHistorySpanCountsGatedUnitsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-stored-history-gated"
	at := ts(2026, 9, 18, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	a := repoUUID("repo-a")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, a, orgID, "acme/repo-a", "github", at)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-a", "team-a", at, orgID, "linear", uint8(1))
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-a", a, "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2025, 1, 1, 0, 0, 0), nil, at)
	prior := map[string]float64{"feature_delivery": 0.2, "operational": 0.2, "maintenance": 0.2, "quality": 0.2, "risk": 0.2}
	seed := func(id string, themes map[string]float64, pr int, status string, day time.Time) {
		t.Helper()
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, categorization_status, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, day, day, 1.0, themes, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, a, pr), status, at, orgID)
	}
	seed("g1", prior, 1, "insufficient_evidence", ts(2026, 1, 10, 0, 0, 0))
	seed("c1", map[string]float64{"feature_delivery": 1}, 2, "ok", ts(2026, 3, 10, 0, 0, 0))

	end := ts(2026, 4, 1, 0, 0, 0)
	read := func(start time.Time, subject contextfabric.SubjectRef) (reasons string, mixFacts int) {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		for _, f := range result.Facts {
			if _, mix := f.Fields["theme_feature_delivery"]; mix && contextfabric.FactSubjectKey(f.Subject) == contextfabric.FactSubjectKey(subject) {
				mixFacts++
			}
		}
		return result.Reason + " | " + result.RequestNote + " | " + result.SubjectReasons[contextfabric.FactSubjectKey(subject)], mixFacts
	}
	for _, subject := range []contextfabric.SubjectRef{repositorySubject("repo-a"), teamSubject("team-a"), organizationSubject(orgID)} {
		// The window starts before the gated unit: the span is the gated unit's start, named as stored history.
		got, mixFacts := read(ts(2026, 1, 1, 0, 0, 0), subject)
		if mixFacts != 1 || !strings.Contains(got, "1 of 2 work units in the window") {
			t.Fatalf("%s: the read did not see the gated and the categorized unit: mix facts %d, reasons %q", subject.CanonicalID, mixFacts, got)
		}
		if !strings.Contains(got, "investment_window_beyond_stored_history: "+subject.CanonicalID+": the window starts 2026-01-01T00:00:00Z but its earliest persisted work unit starts 2026-01-10T00:00:00Z") ||
			!strings.Contains(got, "the stored history covers 2026-01-10T00:00:00Z to 2026-04-01T00:00:00Z only") {
			t.Errorf("%s from 2026-01-01: want the stored-history reason at the gated unit's start, named as stored history; got %q", subject.CanonicalID, got)
		}
		if strings.Contains(got, "the mix covers") {
			t.Errorf("%s from 2026-01-01: the reason says the mix covers a span that starts at a gated unit: %q", subject.CanonicalID, got)
		}
		// The window starts after the gated unit and before the first categorized one: the days are stored, no reason.
		got, mixFacts = read(ts(2026, 2, 1, 0, 0, 0), subject)
		if mixFacts != 1 {
			t.Fatalf("%s from 2026-02-01: mix facts %d, want 1 (reasons %q)", subject.CanonicalID, mixFacts, got)
		}
		if strings.Contains(got, "investment_window_beyond_stored_history") {
			t.Errorf("%s from 2026-02-01: a stored gated unit precedes the window, yet the answer says the days are not stored: %q", subject.CanonicalID, got)
		}
	}
}
