package devhealthfacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The age is an addition to the counts: when its statement fails (a stack
// without the first-seen table), the counts are still served, and the age is
// absent with a reason.
func TestFailedAgeStatementServesTheCountsAndAbsentAgeWithAReason(t *testing.T) {
	failed := errors.New("table does not exist")
	teamTables := []fakeTable{
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "00000000-0000-4000-8000-000000000001", "acme/repo"}}},
		{match: "is_open AND start_unix IS NOT NULL", err: failed},
		{match: "SELECT toInt64(uniqExact(d.target_work_item_id))", rows: [][]any{{int64(0)}}},
		{match: "arrayJoin({team_repo_pairs", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0)}}},
		{match: "FROM work_item_dependencies", rows: [][]any{{"00000000-0000-4000-8000-000000000001", int64(4), int64(5), int64(0), int64(3)}}},
	}
	projectTables := []fakeTable{
		{match: "is_open = 1 AND start_unix IS NOT NULL", err: failed},
		{match: "uniqExactIf(tgt, is_open = 1)", rows: [][]any{{"linear:proj-1", int64(2), int64(3), int64(1), int64(0)}}},
	}
	for _, c := range []struct {
		name     string
		tables   []fakeTable
		subject  contextfabric.SubjectRef
		wantBlkd int64
	}{
		{"team", teamTables, teamSubject("CHAOS"), 4},
		{"project", projectTables, projectSubject("linear", "proj-1"), 2},
	} {
		provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: c.tables}), contextfabric.FactBlockers)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{c.subject},
		})
		if err != nil {
			t.Fatalf("%s: a failed age statement failed the read: %v", c.name, err)
		}
		fields := factFor(t, result, c.subject).Fields
		wantInt(t, fields, "blocked_work_items_current", c.wantBlkd)
		if _, has := fields["oldest_blocked_age_days"]; has {
			t.Errorf("%s: an age is served after its statement failed", c.name)
		}
		if reason := fields["oldest_blocked_age_unavailable_reason"]; reason.String == nil || *reason.String != "start_unavailable" {
			t.Errorf("%s: reason = %+v, want start_unavailable", c.name, reason)
		}
	}
}
