package contextfabric

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A continuation turn that carries a raw RequestedScope.SubjectHints entry
// of the carried anchor's own kind must reach the contest-before-injection
// path: the carried anchor is disclosed superseded_by_caller and the caller's
// hint alone reaches resolution. It must not read as a changed question that
// drops the whole remembered ledger before the contest runs.
func TestRawSubjectHintContestsTheCarriedAnchor(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_raw_hint_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	callerPicked := SubjectRef{Kind: committedAnchorRepo.Kind, CanonicalID: "repository:caller-raw-hint", Label: "caller raw hint"}
	two := needTurnRequest("request_raw_hint_two", true)
	two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	two = continuingNeedTurn(two, oneResult.ResultID)
	two.RequestedScope.SubjectHints = []contractsv1.ContextFabricSubjectHint{{
		Kind: callerPicked.Kind, ID: callerPicked.CanonicalID, Label: callerPicked.Label, Source: "caller",
	}}
	twoResponse := needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{callerPicked}},
		bases:      provenCommitBases(callerPicked),
	}
	twoResult, call := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	if len(call.request.RequestedScope.SubjectHints) != 1 || call.request.RequestedScope.SubjectHints[0].ID != callerPicked.CanonicalID {
		t.Fatalf("ResolveSubjects hints = %#v, want exactly the caller's raw hint", call.request.RequestedScope.SubjectHints)
	}
	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 {
		t.Fatalf("subject_anchor disclosure = %#v, want exactly one entry: the carried anchor, contested", got)
	}
	if got[0].Disposition != contractsv1.ContextFabricStructureDispositionSupersededByCaller || got[0].AppliedValue != committedAnchorRepo.CanonicalID {
		t.Fatalf("subject_anchor disclosure = %+v, want superseded_by_caller on %s", got[0], committedAnchorRepo.CanonicalID)
	}
}

// A hint of a DIFFERENT kind is not a contest: the carried anchor stays
// applied and reaches resolution beside the caller's hint, exactly as it does
// beside a redeemed receipt's hint.
func TestRawSubjectHintOfAnotherKindLeavesTheCarriedAnchorApplied(t *testing.T) {
	engine, graph, store := buildCommittedAnchorEngine(t)

	one := needTurnRequest("request_raw_hint_kind_one", true)
	oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
	oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

	two := needTurnRequest("request_raw_hint_kind_two", true)
	two = continuingNeedTurn(two, oneResult.ResultID)
	two.RequestedScope.SubjectHints = []contractsv1.ContextFabricSubjectHint{{
		Kind: contractsv1.ContextFabricSubjectTeam, ID: "team:raw-hint-team", Label: "raw hint team", Source: "caller",
	}}
	twoResponse := needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}},
		bases:      provenCommitBases(committedAnchorRepo),
	}
	twoResult, call := committedAnchorTurn(t, engine, graph, store, two, twoResponse)

	hints := call.request.RequestedScope.SubjectHints
	if len(hints) != 2 || hints[0].ID != "team:raw-hint-team" || hints[1].Source != string(hintsource.EngineCommittedAnchorCarry) || hints[1].ID != committedAnchorRepo.CanonicalID {
		t.Fatalf("ResolveSubjects hints = %#v, want the caller's team hint then the engine's carried repository hint", hints)
	}
	got := memberEntries(twoResult, contractsv1.ContextFabricStructureNeedSubjectAnchor)
	if len(got) != 1 || got[0].Disposition != contractsv1.ContextFabricStructureDispositionApplied || got[0].AppliedValue != committedAnchorRepo.CanonicalID {
		t.Fatalf("subject_anchor disclosure = %#v, want one entry applied on %s", got, committedAnchorRepo.CanonicalID)
	}
}

// The ledger as a whole is admitted when the only difference from the parent
// is a subject hint, in either direction, and a scope part the digest still
// covers keeps dropping it.
func TestResolveConfirmedNeedLedger_SubjectHintsAreNotPartOfTheIdentity(t *testing.T) {
	t.Parallel()
	hint := []contractsv1.ContextFabricSubjectHint{{Kind: contractsv1.ContextFabricSubjectRepository, ID: "repository:hinted", Source: "caller"}}
	want := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)}}

	cases := []struct {
		name    string
		mutate  func(parentRequest, child *InvestigationRequest)
		outcome ConfirmedNeedLedgerOutcome
	}{
		{"child adds a hint", func(_, c *InvestigationRequest) { c.RequestedScope.SubjectHints = hint }, ConfirmedNeedLedgerHit},
		{"parent had a hint, child has none", func(p, _ *InvestigationRequest) { p.RequestedScope.SubjectHints = hint }, ConfirmedNeedLedgerHit},
		{"child adds a repository scope", func(_, c *InvestigationRequest) { c.RequestedScope.RepositorySlugs = []string{"full-chaos/x"} }, ConfirmedNeedLedgerDroppedIdentityChanged},
		{"child adds a project scope", func(_, c *InvestigationRequest) { c.RequestedScope.ProjectIDs = []string{"project_x"} }, ConfirmedNeedLedgerDroppedIdentityChanged},
		{"child adds a team scope", func(_, c *InvestigationRequest) { c.RequestedScope.TeamIDs = []string{"team_x"} }, ConfirmedNeedLedgerDroppedIdentityChanged},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parentResult, parentState, child := parentAndChildForLedgerTest(t)
			parentRequest := validInvestigationRequest()
			tc.mutate(&parentRequest, &child)
			parentState.RequestIdentity = SemanticRequestIdentityOf(parentRequest, "")
			engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

			got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), child, ResolvedGraphBinding{})
			if got.Outcome != tc.outcome {
				t.Fatalf("Outcome = %q, want %q", got.Outcome, tc.outcome)
			}
			if tc.outcome == ConfirmedNeedLedgerHit && !reflect.DeepEqual(got.Entries, want) {
				t.Fatalf("Entries = %#v, want %#v", got.Entries, want)
			}
		})
	}
}

// A parent stored under the previous recipe is not compared under this one:
// its digest covered the hints, so equal bytes would prove nothing. The
// ledger is dropped as incomparable, the fresh path, never a carry.
func TestResolveConfirmedNeedLedger_APreviousRecipeParentIsIncomparable(t *testing.T) {
	t.Parallel()
	parentResult, parentState, child := parentAndChildForLedgerTest(t)
	parentState.RequestIdentity = SemanticRequestIdentity{Version: "request-identity.v1", Digest: parentState.RequestIdentity.Digest}
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), child, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerDroppedIdentityIncomparable {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerDroppedIdentityIncomparable)
	}
	if got.Entries != nil {
		t.Fatalf("Entries = %#v, want nil", got.Entries)
	}
}
