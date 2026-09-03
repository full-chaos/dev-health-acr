package contextfabric

import (
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The served-answer line's KEY SET, pinned in BOTH DIRECTIONS.
//
// One direction alone is half a guard, and this program has been bitten by
// each half separately. An allow-list with no required set lets a key be
// DELETED silently -- a field mutation survived a whole package that way. A
// required set with no allow-list lets a key be ADDED silently, which is how
// free-text reaches a log line that operators, and their log store, treat as
// content-safe by construction.

// TestCohortRankedLineCarriesTheServedCohortKind is the REQUIRED half: the
// served-answer line must name the kind of cohort it served.
//
// This is the direction that was missing, and its absence had a cost worth
// recording. The graph side reported a refusal basis with no kind and the
// served side reported counts with no kind, so no artifact of a run said
// which member kind a question actually declared. That question then got
// answered by reading the question TEXT, in three separate documents, and the
// reading was wrong: "open incidents per repository" declares repository as
// its grouping AXIS and `incident` as its member kind.
func TestCohortRankedLineCarriesTheServedCohortKind(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCohortRanked(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			CohortRankedEvent{
				CohortKind: SubjectRepository, MemberCount: 3,
				FormulaVersion: RankingFormulaVersion,
				SignalsAvailable: map[string]int{}, OutcomeCounts: map[string]int{},
			})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if got := records[0]["cohort_kind"]; got != string(SubjectRepository) {
		t.Errorf("cohort_kind = %v, want %q -- a served-answer line that cannot name the kind it served leaves the question to be answered from question text", got, SubjectRepository)
	}
	// The keys this line already carried must not have been displaced by
	// the new one.
	for _, required := range []string{"org_id", "member_count", "formula_version", "degraded_member_count"} {
		if _, present := records[0][required]; !present {
			t.Errorf("the cohort ranked line lost key %q", required)
		}
	}
}

// TestCohortRankedLineCarriesNoKeyOutsideItsAllowList is the ALLOW-LIST half.
//
// Every member is a count, a closed-vocabulary enum, or a server-authored
// version constant. Nothing here may carry a subject label, a question, or a
// score value -- which is the rule RecordCohortRanked's own doc comment
// states and which this test is what enforces.
func TestCohortRankedLineCarriesNoKeyOutsideItsAllowList(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCohortRanked(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			CohortRankedEvent{
				CohortKind: SubjectTeam, MemberCount: 2,
				FormulaVersion: RankingFormulaVersion, DegradedMemberCount: 1,
				SignalsAvailable: map[string]int{"workload": 2},
				OutcomeCounts:    map[string]int{"qualified": 2},
			})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "request_id": true,
		"org_id": true, "member_count": true, "formula_version": true,
		"degraded_member_count": true, "signals_available": true,
		"outcome_counts": true,
		// The served cohort kind: one member of the published
		// fifteen-value subject-kind vocabulary, content-free by the same
		// reasoning that admits every other key here.
		"cohort_kind": true,
	}
	for key := range records[0] {
		if !allowed[key] {
			t.Errorf("unexpected key %q on the cohort ranked line -- every field must be an explicitly allowed closed-vocabulary value or a count", key)
		}
	}
}

// TestCohortRankedKindIsEmptyWhenNothingWasRanked is the MIRROR on the kind
// itself.
//
// RankCohort sets CohortKind after its nil/empty guard, so an event for a
// cohort that was never ranked carries no kind. Asserting only the populated
// direction would leave a version that stamped a kind onto every event --
// including the ones where no cohort existed -- completely unpinned, and an
// operator counting served repository cohorts would count turns that served
// nothing.
func TestCohortRankedKindIsEmptyWhenNothingWasRanked(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		cohort *Cohort
	}{
		{"no cohort at all", nil},
		{"a cohort with no members", &Cohort{Kind: SubjectRepository, Members: nil}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, event, _ := RankCohort(testCase.cohort, nil, Coverage{})
			if event.CohortKind != "" {
				t.Errorf("CohortKind = %q for %s, want empty -- a kind on an event nothing ranked reads as a served cohort", event.CohortKind, testCase.name)
			}
		})
	}
}

// TestCohortRankedKindIsTheCohortsOwnKind is that mirror's positive control.
//
// A guard that reported the empty kind unconditionally would satisfy the test
// above perfectly, and this is the fixture that lands in the other tier.
func TestCohortRankedKindIsTheCohortsOwnKind(t *testing.T) {
	for _, kind := range []SubjectKind{SubjectTeam, SubjectProject, SubjectRepository} {
		t.Run(string(kind), func(t *testing.T) {
			cohort := &Cohort{
				Kind: kind,
				Members: []CohortMember{{
					Subject: SubjectRef{Kind: kind, CanonicalID: "node_a", Label: "Node A"},
					Rank:    1,
				}},
			}
			_, event, _ := RankCohort(cohort, nil, Coverage{})
			if event.CohortKind != kind {
				t.Errorf("CohortKind = %q, want %q -- the event must report the kind the cohort actually carried, not a constant", event.CohortKind, kind)
			}
		})
	}
}
