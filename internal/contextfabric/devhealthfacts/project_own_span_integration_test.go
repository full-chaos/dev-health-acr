package devhealthfacts_test

// A project's own earliest linked unit among the units overlapping the window,
// served through the provider on a real ClickHouse: the roll-up path reads it
// from the repo arm, the native path from the unit values, both without a
// second scan of work_unit_investments.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestProjectOwnEarliestLinkedUnitIsServedAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-own-span"
	at := seedCHAOS7257Parity(t, ctx, direct, orgID)
	day := 24 * time.Hour
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	read := func(start time.Time) contextfabric.FactProviderResult {
		t.Helper()
		end := at
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}

	// proj-1's earliest unit overlapping the window is the old unit, which starts
	// 40 days before the seed instant (seed fixture: from = at-40d); the
	// organization's earliest unit starts 60 days before it (the proj-6 unit),
	// so a window starting 50 days back is inside the organization's span.
	own := at.Add(-40 * day).UTC().Format(time.RFC3339)
	result := read(at.Add(-50 * day))
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want the project's mix (reason %q)", len(result.Facts), result.Reason)
	}
	if !strings.Contains(result.Reason, "investment_project_window_first_unit") || !strings.Contains(result.Reason, own) {
		t.Errorf("reason %q, want the project's own earliest linked unit %s", result.Reason, own)
	}
	if strings.Contains(result.Reason, "investment_window_beyond_stored_history") {
		t.Errorf("reason %q claims the organization's history starts after the window", result.Reason)
	}
	// A window that starts after that unit names nothing for the project.
	if reason := read(at.Add(-10 * day)).Reason; strings.Contains(reason, "investment_project_window_first_unit") {
		t.Errorf("window after the project's first unit carries its own-span reason: %q", reason)
	}
}
