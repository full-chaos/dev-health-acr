package contextfabric_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The plan table is total over the closed evidence-entity vocabulary, names
// only statements contextpacket declares, and routes exactly the kinds
// CHAOS-7226 ruled after r2 plus CHAOS-7227's team and project (9 source
// kinds, 15 on the record). A kind moved between routes must move here too.
func TestSourceRowPlansAreTotal(t *testing.T) {
	plans := contextfabric.SourceRowPlans()
	vocabulary := contractsv1.ContextFabricEvidenceEntityTypeVocabulary()
	if len(plans) != len(vocabulary) {
		t.Fatalf("plans = %d kinds, vocabulary = %d", len(plans), len(vocabulary))
	}
	queries := contextpacket.SourceRowQueryIDs()
	want := map[contractsv1.ContextFabricEvidenceEntityType]contextfabric.SourceRowRoute{
		contractsv1.ContextFabricEvidenceEntityRepository:  contextfabric.SourceRowRouteRepository,
		contractsv1.ContextFabricEvidenceEntityWorkItem:    contextfabric.SourceRowRouteRepository,
		contractsv1.ContextFabricEvidenceEntityPullRequest: contextfabric.SourceRowRouteRepository,
		contractsv1.ContextFabricEvidenceEntityReview:      contextfabric.SourceRowRouteRepository,
		contractsv1.ContextFabricEvidenceEntityCI:          contextfabric.SourceRowRouteRepository,
		contractsv1.ContextFabricEvidenceEntityDeployment:  contextfabric.SourceRowRouteRepository,
		contractsv1.ContextFabricEvidenceEntityIncident:    contextfabric.SourceRowRouteRowRepository,
		contractsv1.ContextFabricEvidenceEntityTeam:        contextfabric.SourceRowRouteOwnership,
		contractsv1.ContextFabricEvidenceEntityProject:     contextfabric.SourceRowRouteOwnership,
	}
	records := 0
	for _, kind := range vocabulary {
		plan, ok := plans[kind]
		if !ok {
			t.Fatalf("kind %s has no plan", kind)
		}
		if plan != contextfabric.SourceRowPlanFor(string(kind)) {
			t.Fatalf("SourceRowPlanFor(%s) disagrees with the table", kind)
		}
		route, source := want[kind]
		if !source {
			route = contextfabric.SourceRowRouteRecord
		}
		if plan.Route != route {
			t.Fatalf("%s route = %s, want %s", kind, plan.Route, route)
		}
		switch plan.Route {
		case contextfabric.SourceRowRouteRecord:
			records++
			if plan.Query != "" {
				t.Fatalf("record kind %s names statement %q", kind, plan.Query)
			}
		default:
			if !slices.Contains(queries, plan.Query) {
				t.Fatalf("%s names statement %q that contextpacket does not declare", kind, plan.Query)
			}
		}
	}
	if records != 15 || len(want) != 9 {
		t.Fatalf("records = %d, source kinds = %d; want 15 and 9", records, len(want))
	}
	if got := contextfabric.SourceRowPlanFor("not-a-kind"); got.Route != contextfabric.SourceRowRouteRecord {
		t.Fatalf("an unregistered segment routes to %s", got.Route)
	}
}

// fakeSource answers with a fixed expansion and decision, and counts calls.
type fakeSource struct {
	expanded contractsv1.ExpandedEvidence
	decision contextfabric.SourceRowDecision
	calls    int
}

func (f *fakeSource) ResolveSourceRow(context.Context, storage.Principal, string, string) (contractsv1.ExpandedEvidence, contextfabric.SourceRowDecision) {
	f.calls++
	return f.expanded, f.decision
}

// recordingLookup finds no citing result and counts searches: a search
// proves the persisted-record path ran.
type recordingLookup struct{ searches int }

func (l *recordingLookup) ResultIDsCitingEvidence(context.Context, storage.Principal, string, int, int) ([]string, error) {
	l.searches++
	return nil, nil
}

type admitAll struct{}

func (admitAll) Authorize(context.Context, storage.Principal, contextfabric.StoredInvestigationResult, contextfabric.StoredResultSurface) contextfabric.StoredResultAuthorization {
	return contextfabric.StoredResultAuthorization{Decision: contextfabric.StoredResultAdmitted}
}

var expandNow = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

func servedSourceRow(ref string) contractsv1.ExpandedEvidence {
	return contractsv1.ExpandedEvidence{
		SchemaVersion: contractsv1.ExpandedEvidenceSchema, ResolvedAt: expandNow, Availability: contractsv1.EvidenceAvailable, Structured: map[string]any{},
		Evidence: contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: ref,
			Source:     contractsv1.EvidenceSource{System: contextfabric.SourceRowSystem, EntityType: "pull_request", EntityID: "532", DisplayLabel: "PR 532"},
			Provenance: "native", Confidence: 1, Citation: "open", ObservedAt: expandNow, Availability: contractsv1.EvidenceAvailable,
			Metadata: map[string]any{"record": "source_row", "row_state": "current"},
		},
	}
}

func expand(source contextfabric.SourceRowResolver, ref string) (contractsv1.ExpandedEvidence, contextfabric.EvidenceExpansionDecision, *recordingLookup) {
	lookup := &recordingLookup{}
	expanded, decision := contextfabric.ExpandEvidence(context.Background(), storage.Principal{OrgID: "org_1"}, ref, source, lookup, memoryinvestigation.NewStore(), admitAll{}, expandNow)
	return expanded, decision, lookup
}

const pullRequestRef = "acr:v1:pull-request:20000000-0000-4000-8000-000000000002:532"

func TestExpandEvidenceServesTheSourceRowFirst(t *testing.T) {
	source := &fakeSource{expanded: servedSourceRow(pullRequestRef), decision: contextfabric.SourceRowDecision{Reason: contextfabric.SourceRowServed, Query: "pull_requests.v1"}}
	expanded, decision, lookup := expand(source, pullRequestRef)
	if decision.Reason != contextfabric.EvidenceExpansionSourceRowServed || !decision.Found() || decision.ServingError() != nil {
		t.Fatalf("decision = %+v", decision)
	}
	if expanded.Evidence.EvidenceRefID != pullRequestRef || lookup.searches != 0 || decision.Source.Query != "pull_requests.v1" {
		t.Fatalf("expanded %+v, searches %d", expanded.Evidence, lookup.searches)
	}
}

// P5: no row the caller may read falls back to the persisted record, which
// runs as before (here: nothing cites the ref, so not found).
func TestExpandEvidenceFallsBackToTheRecordWithoutARow(t *testing.T) {
	for _, reason := range []contextfabric.SourceRowReason{contextfabric.SourceRowNoRow, contextfabric.SourceRowAmbiguous, contextfabric.SourceRowIDMalformed, contextfabric.SourceRowInvalid} {
		source := &fakeSource{decision: contextfabric.SourceRowDecision{Reason: reason, Query: "pull_requests.v1"}}
		_, decision, lookup := expand(source, pullRequestRef)
		if lookup.searches != 1 || decision.Reason != contextfabric.EvidenceExpansionNotCited || decision.Source.Reason != reason {
			t.Fatalf("%s: decision %+v, searches %d", reason, decision, lookup.searches)
		}
		if !errors.Is(decision.ServingError(), storage.ErrNotFound) {
			t.Fatalf("%s: serving error %v", reason, decision.ServingError())
		}
	}
}

// P6: a failed source read is a retryable 503, and the record is NOT tried:
// a fallback would answer as if the row were absent.
func TestExpandEvidenceDoesNotFallBackOnAFailedRead(t *testing.T) {
	boom := errors.New("clickhouse down")
	source := &fakeSource{decision: contextfabric.SourceRowDecision{Reason: contextfabric.SourceRowUnavailable, Query: "pull_requests.v1", Err: boom}}
	_, decision, lookup := expand(source, pullRequestRef)
	if decision.Reason != contextfabric.EvidenceExpansionSourceRowUnavailable || lookup.searches != 0 {
		t.Fatalf("decision %+v, searches %d", decision, lookup.searches)
	}
	if err := decision.ServingError(); !errors.Is(err, contextfabric.ErrUnavailable) || !errors.Is(err, boom) {
		t.Fatalf("serving error = %v", err)
	}
}

// A served expansion that does not name the ref, or names another system, is
// refused as invalid rather than served under the wrong label.
func TestExpandEvidenceRefusesAMislabeledSourceRow(t *testing.T) {
	for name, mutate := range map[string]func(*contractsv1.ExpandedEvidence){
		"other ref": func(e *contractsv1.ExpandedEvidence) { e.Evidence.EvidenceRefID = "acr:v1:pull-request:x" },
		"other system": func(e *contractsv1.ExpandedEvidence) {
			e.Evidence.Source.System = contextfabric.ContextFabricEvidenceSystem
		},
		"invalid": func(e *contractsv1.ExpandedEvidence) { e.Evidence.Provenance = "made_up" },
	} {
		expanded := servedSourceRow(pullRequestRef)
		mutate(&expanded)
		source := &fakeSource{expanded: expanded, decision: contextfabric.SourceRowDecision{Reason: contextfabric.SourceRowServed}}
		_, decision, _ := expand(source, pullRequestRef)
		if decision.Reason != contextfabric.EvidenceExpansionInvalid || decision.Found() {
			t.Fatalf("%s: decision %+v", name, decision)
		}
	}
}

// A record kind never reaches the resolver; a missing resolver says so.
func TestExpandEvidenceSkipsTheResolverForRecordKindsAndAbsence(t *testing.T) {
	source := &fakeSource{}
	_, decision, lookup := expand(source, "acr:v1:team:team-a")
	if source.calls != 0 || decision.Source.Reason != contextfabric.SourceRowKindOnRecord || lookup.searches != 1 {
		t.Fatalf("record kind: calls %d, decision %+v", source.calls, decision)
	}
	var typedNil *fakeSource
	_, decision, lookup = expand(typedNil, pullRequestRef)
	if decision.Source.Reason != contextfabric.SourceRowBackendAbsent || decision.Source.Query != "pull_requests.v1" || lookup.searches != 1 {
		t.Fatalf("no resolver: decision %+v", decision)
	}
	_, decision, _ = expand(source, "acr:v1:pull-request")
	if source.calls != 0 || decision.Reason != contextfabric.EvidenceExpansionMalformedRef || decision.Source.Reason != contextfabric.SourceRowIDMalformed {
		t.Fatalf("malformed ref: decision %+v", decision)
	}
}

// The trace line carries the source-row fields only when they are known.
func TestEvidenceExpansionLogArgsCarryTheSourceRow(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	fields := func(decision contextfabric.EvidenceExpansionDecision) map[string]any {
		args := contextfabric.EvidenceExpansionLogArgs(principal, decision)
		out := map[string]any{}
		for i := 0; i+1 < len(args); i += 2 {
			out[args[i].(string)] = args[i+1]
		}
		return out
	}
	read := fields(contextfabric.EvidenceExpansionDecision{Reason: contextfabric.EvidenceExpansionNotCited, Source: contextfabric.SourceRowDecision{
		Reason: contextfabric.SourceRowNoRow, Query: "work_items.v1", Grammar: contextfabric.SourceRowGrammarRepoAnchored, Repositories: 1, Admitted: 0,
	}})
	for key, want := range map[string]any{"source_reason": "no_row", "source_query": "work_items.v1", "source_grammar": "repo_anchored", "source_repositories": 1, "source_admitted": 0, "source_rows": 0} {
		if read[key] != want {
			t.Fatalf("%s = %v, want %v (%v)", key, read[key], want, read)
		}
	}
	record := fields(contextfabric.EvidenceExpansionDecision{Reason: contextfabric.EvidenceExpansionNotCited, Source: contextfabric.SourceRowDecision{Reason: contextfabric.SourceRowKindOnRecord}})
	if record["source_reason"] != "kind_on_record" {
		t.Fatalf("record = %v", record)
	}
	for _, key := range []string{"source_query", "source_grammar", "source_repositories", "source_admitted", "source_rows"} {
		if _, ok := record[key]; ok {
			t.Fatalf("record kind writes %s", key)
		}
	}
	if zero := fields(contextfabric.EvidenceExpansionDecision{Reason: contextfabric.EvidenceExpansionNotCited}); zero["source_reason"] != "backend_absent" {
		t.Fatalf("zero decision = %v", zero)
	}
}
