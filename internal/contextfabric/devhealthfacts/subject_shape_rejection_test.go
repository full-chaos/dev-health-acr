package devhealthfacts_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestInvestmentProviderReportsSubjectIDShapeRejectionNotNoData is CHAOS-5026's
// red-first pin. subjectIndex used to strip the "team:" prefix off a
// subject's CanonicalID and silently skip any subject that never carried it,
// so a caller passing "CHAOS" where "team:CHAOS" was required read back
// facts=0 state=no_data -- indistinguishable from a team that genuinely has
// no investment data (lane-s7b-ii-pr3, 2026-09-04, executing
// InvestmentProvider).
//
// This drives the PUBLIC provider entry point (InvestmentProvider.ReadFacts
// via devhealthfacts.NewProviders), never a struct literal or a direct call
// to an unexported helper, and it asserts the two states DIFFER before
// asserting anything about what the rejected one actually is -- a red that
// only checked the rejected case in isolation could pass by coincidence if
// the fix also broke the positive control.
func TestInvestmentProviderReportsSubjectIDShapeRejectionNotNoData(t *testing.T) {
	handler := &recordingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	client := &fakeClient{}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)

	// Positive control: a correctly-shaped team subject with genuinely no
	// rows (fakeClient's default response for a statement with no
	// registered table match is an empty scanner). This is the TRUE
	// no_data case the shape-rejected read must remain distinguishable
	// from.
	noDataResult, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("real-team-with-no-data")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() (well-shaped, no data) error = %v", err)
	}
	if noDataResult.State != contextfabric.SourceNoData {
		t.Fatalf("positive control broke: well-shaped no-data read State = %q, want %q", noDataResult.State, contextfabric.SourceNoData)
	}
	if strings.Contains(noDataResult.Reason, "subject_id_shape_rejected") {
		t.Fatalf("positive control broke: well-shaped no-data read Reason = %q must not carry the shape-rejection token", noDataResult.Reason)
	}

	// The actual case: a team subject whose CanonicalID never got the
	// "team:" prefix -- CHAOS-5026's own example.
	shapeRejected := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "CHAOS", Label: "CHAOS"}
	rejectedResult, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{shapeRejected},
	})
	if err != nil {
		t.Fatalf("ReadFacts() (shape-rejected) error = %v", err)
	}

	// Assert the two states/reasons differ FIRST: this is the harm this
	// ticket exists to fix, stated as its own assertion before anything
	// else about the rejected case is checked.
	if rejectedResult.State == noDataResult.State && rejectedResult.Reason == noDataResult.Reason {
		t.Fatalf("CHAOS-5026 regression: shape-rejected read (State=%q Reason=%q) is INDISTINGUISHABLE from a genuinely empty well-shaped read (State=%q Reason=%q)",
			rejectedResult.State, rejectedResult.Reason, noDataResult.State, noDataResult.Reason)
	}
	if rejectedResult.State == contextfabric.SourceNoData {
		t.Fatalf("shape-rejected read State = %q, want anything other than no_data: a wrongly-shaped subject id must never present as an ordinary empty read", rejectedResult.State)
	}
	if !strings.Contains(rejectedResult.Reason, "subject_id_shape_rejected") {
		t.Fatalf("shape-rejected read Reason = %q, want it to contain the positive reason %q", rejectedResult.Reason, "subject_id_shape_rejected")
	}
	if rejectedResult.OmittedCount != 1 {
		t.Fatalf("shape-rejected read OmittedCount = %d, want 1: the count must travel with the reason", rejectedResult.OmittedCount)
	}

	// Telemetry, same change: the reason and count must reach an actually
	// EMITTED slog record, verified at this same public entry point --
	// never a direct call to the unexported recorder.
	var found bool
	for _, record := range handler.snapshot() {
		if record.Message != "context_fabric_subject_id_shape_rejected" {
			continue
		}
		attrs := recordAttrs(record)
		producer, ok := attrs["producer"]
		if !ok || producer.String() != "devhealthfacts.investment" {
			continue
		}
		kind, ok := attrs["kind"]
		if !ok || kind.String() != string(contextfabric.FactInvestment) {
			continue
		}
		rejected, ok := attrs["rejected"]
		if !ok || rejected.Int64() != 1 {
			continue
		}
		found = true
	}
	if !found {
		t.Fatalf("no context_fabric_subject_id_shape_rejected slog record emitted with producer=devhealthfacts.investment kind=%s rejected=1", contextfabric.FactInvestment)
	}
}

// TestStatusProviderKeepsRealFactsWhileDisclosingAShapeRejectedSibling covers
// the v2Index half of the same mechanism (subjectIndex's investment test
// above only exercises the prefix-strip half) AND the partial-coverage case
// subjectIndex/v2Index's own "omit rather than guess" doc comments promise:
// a wrongly-shaped subject requested ALONGSIDE a well-shaped one must not
// cost the well-shaped subject its fact. Disclosure is additive, never a
// refusal of the whole read.
func TestStatusProviderKeepsRealFactsWhileDisclosingAShapeRejectedSibling(t *testing.T) {
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "in_progress", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)

	// A well-shaped work item (real data) alongside a work item subject
	// whose CanonicalID never got the "work_item.v2:" form at all -- the
	// v2Index analogue of CHAOS-5026's "CHAOS" example.
	malformed := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "WIDGET-999", Label: "WIDGET-999"}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactStatus, Subjects: []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101"), malformed},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}

	// The real subject's fact must survive: shape rejection of its sibling
	// is disclosed ADDITIVELY, never as a refusal of the whole read.
	if len(result.Facts) != 1 || result.Facts[0].Subject.CanonicalID != "work_item.v2:repo-1:WIDGET-101" {
		t.Fatalf("Facts = %#v, want exactly the well-shaped subject's fact to survive", result.Facts)
	}
	if result.State != contextfabric.SourceTruncated {
		t.Fatalf("State = %q, want %q: a partially shape-rejected read is a disclosed partial coverage, not a clean available/no_data read", result.State, contextfabric.SourceTruncated)
	}
	if !strings.Contains(result.Reason, "subject_id_shape_rejected") {
		t.Fatalf("Reason = %q, want it to contain %q", result.Reason, "subject_id_shape_rejected")
	}
	if result.OmittedCount != 1 {
		t.Fatalf("OmittedCount = %d, want 1", result.OmittedCount)
	}
}

// TestSourceHealthProviderDisclosesRejectionOnTheQueriedBranch pins a codex
// terra xhigh r1 finding (EXECUTED-confirmed by the lane): SourceHealthProvider.ReadFacts
// has THREE separate success-shaped return points (no org subjects survived
// shape filtering; the survivor isn't the caller's own org; the real query
// ran) -- unlike every other provider in this package, which aggregates into
// ONE final return. Each of the three previously needed its OWN
// applySubjectShapeRejection call, so deleting only the THIRD one (the one
// on the branch that actually queries ClickHouse) is a compiling mutation
// that survives every other test in this package: it only manifests when a
// well-shaped organization subject (which reaches the real query) is
// requested ALONGSIDE a shape-rejected one, and no other test in this
// package constructs that combination for SourceHealthProvider.
func TestSourceHealthProviderDisclosesRejectionOnTheQueriedBranch(t *testing.T) {
	client := &fakeClient{}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactSourceHealth)

	shapeRejected := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "org-2", Label: "org-2"}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1"), shapeRejected},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}

	if result.State == contextfabric.SourceNoData {
		t.Fatalf("State = %q, want anything other than no_data: the well-shaped org-1 subject's empty read must not silently absorb its shape-rejected sibling", result.State)
	}
	if !strings.Contains(result.Reason, "subject_id_shape_rejected") {
		t.Fatalf("Reason = %q, want it to contain %q", result.Reason, "subject_id_shape_rejected")
	}
	if result.OmittedCount != 1 {
		t.Fatalf("OmittedCount = %d, want 1", result.OmittedCount)
	}
}
