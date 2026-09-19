package memoryinvestigation

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This is a white-box test (package memoryinvestigation, not
// memoryinvestigation_test) because it must plant a stored payload that
// never went through Save. Save validates on write, so the only way to
// reach Get's own validation is to write s.results directly -- which is
// exactly the situation Get's check defends against: a row that got into
// storage some other way (a different binary, a hand-edited row, a future
// schema). Testing through the public API alone cannot construct that
// state, so the guard would sit unprobed. The repository already uses
// same-package test files where a test must observe internals (see
// internal/contextpacket).
func TestGetRejectsStoredResultThatFailsValidation(t *testing.T) {
	t.Parallel()
	const resultID = "result-id-corrupt"
	principal := storage.Principal{OrgID: "org-1"}
	store := NewStore()

	// A syntactically valid but SEMANTICALLY invalid stored result: it
	// decodes cleanly into an InvestigationResult, so json.Unmarshal
	// cannot catch it -- only InvestigationResult.Validate() can. Missing
	// schema version, status, interpretation, coverage and every required
	// collection.
	store.results[resultID] = entry{
		orgID:   principal.OrgID,
		payload: []byte(`{"result_id":"result-id-corrupt","question":"what happened?"}`),
	}

	_, err := store.Get(context.Background(), principal, resultID)
	if err == nil {
		t.Fatal("Get() error = nil, want a stored result that fails Validate() to be rejected rather than returned to a caller")
	}
	if !strings.Contains(err.Error(), "stored investigation result is invalid") {
		t.Fatalf("Get() error = %v, want it to identify the stored result as invalid", err)
	}
}

// TestGetReturnsStoredResultThatPassesValidation is the over-blocking
// guard for the test above: Get's validation must not reject a legitimately
// stored result. Without this, deleting the decode step entirely (or making
// Validate() always fail) would still leave the test above green.
func TestGetReturnsStoredResultThatPassesValidation(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org-1"}
	store := NewStore()
	valid := paritytest.ValidResult("result-id-valid", "is the rollout healthy?")

	if err := store.Save(context.Background(), principal, valid, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Get(context.Background(), principal, valid.ResultID)
	if err != nil {
		t.Fatalf("Get() error = %v, want a valid stored result to be returned", err)
	}
	if got.Result.ResultID != valid.ResultID {
		t.Fatalf("Get() result_id = %q, want %q", got.Result.ResultID, valid.ResultID)
	}
}

// TestGetRejectsStoredResultWithExplicitNullDegradedReasons is the P2 fix
// (Codex delta review, CHAOS-3755): Coverage.DegradedReasons is `omitempty`
// in Go and optional (not `null`) in the JSON Schema -- when PRESENT it
// must be an array. encoding/json collapses an OMITTED field and an
// EXPLICIT `null` to the identical Go nil slice on Unmarshal, so
// Validate()'s relaxed nil-check (which correctly accepts the omitted
// case) cannot tell them apart post-decode. A stored row with literal
// `"degraded_reasons": null` is wire-invalid and must be rejected before
// or independent of the struct decode, not silently accepted because it
// happens to decode to the same value as a legitimately absent field.
func TestGetRejectsStoredResultWithExplicitNullDegradedReasons(t *testing.T) {
	t.Parallel()
	const resultID = "result-id-explicit-null"
	principal := storage.Principal{OrgID: "org-1"}
	store := NewStore()
	valid := paritytest.ValidResult(resultID, "is the rollout healthy?")
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	// Plant the EXACT same payload but with degraded_reasons forced to
	// explicit null, simulating a row written by a binary/path that
	// serializes differently (or a hand-edited row).
	tainted := bytes.Replace(encoded, []byte(`"sources":[]`), []byte(`"sources":[],"degraded_reasons":null`), 1)
	if bytes.Equal(tainted, encoded) {
		t.Fatal("test setup: expected substring not found in fixture JSON")
	}
	store.results[resultID] = entry{orgID: principal.OrgID, payload: tainted}

	_, err = store.Get(context.Background(), principal, resultID)
	if err == nil {
		t.Fatal("Get() error = nil, want a stored result with explicit degraded_reasons:null to be rejected")
	}
	if !strings.Contains(err.Error(), "degraded_reasons") || !strings.Contains(err.Error(), "null") {
		t.Fatalf("Get() error = %v, want it to identify the explicit-null degraded_reasons field", err)
	}
}

// TestStore_explicitNullDegradedReasonsParity runs the SHARED explicit-null
// table. Each store carries its own copy of the raw-bytes check, so this
// is what stops the two from drifting apart -- the per-store tests above
// prove this store's behavior, but only the shared table proves both
// stores agree. It lives in the white-box file because seeding a raw row
// needs access to s.results.
func TestStore_explicitNullDegradedReasonsParity(t *testing.T) {
	t.Parallel()
	paritytest.RunExplicitNullDegradedReasonsSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.RawSeed) {
		store := NewStore()
		return store, func(t *testing.T, orgID, resultID string, payload []byte) {
			t.Helper()
			store.mu.Lock()
			defer store.mu.Unlock()
			store.results[resultID] = entry{orgID: orgID, payload: payload}
		}
	})
}

// TestStore_semanticStateReadParity runs the SHARED semantic-snapshot read
// table: a stored column this build cannot read comes back unavailable with
// the status that names why, and a replay against it is a conflict.
func TestStore_semanticStateReadParity(t *testing.T) {
	t.Parallel()
	paritytest.RunSemanticStateReadSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.SemanticSeed) {
		store := NewStore()
		return store, func(t *testing.T, orgID, resultID string, payload, semanticState []byte) {
			t.Helper()
			store.mu.Lock()
			defer store.mu.Unlock()
			store.results[resultID] = entry{orgID: orgID, payload: payload, semanticState: semanticState}
		}
	})
}

// TestStore_semanticStateIsNeverAliased: the store keeps its own encoded
// bytes, so neither the snapshot a caller saved nor the one Get returned can
// reach back into what is stored.
func TestStore_semanticStateIsNeverAliased(t *testing.T) {
	t.Parallel()
	store := NewStore()
	principal := storage.Principal{OrgID: "org-alias"}
	saved := paritytest.SemanticStateFixture(contextfabric.SubjectTeam, contextfabric.SubjectRepository)
	row := paritytest.ValidResult("result-semantic-alias-01", "is the stored reading independent?")
	if err := store.Save(context.Background(), principal, row, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateOf(saved)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want := paritytest.SemanticStateFixture(contextfabric.SubjectTeam, contextfabric.SubjectRepository)
	saved.Family = contextfabric.QuestionFamilyDiscoveredCohortRanking
	saved.Frame.Goals[0] = contextfabric.GoalCompare
	first, err := store.Get(context.Background(), principal, row.ResultID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	first.SemanticState.Roles[0].SlotID = "mutated"
	second, err := store.Get(context.Background(), principal, row.ResultID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !contextfabric.SemanticStatesEqual(second.SemanticState, want) {
		t.Fatalf("a caller's mutation reached the stored snapshot")
	}
}

// TestStore_substitutionParentRead runs the SHARED parent-read domain for the
// subject-substitution guard through the real engine, seeding the parent row
// and its raw snapshot directly.
func TestStore_substitutionParentRead(t *testing.T) {
	paritytest.RunSubstitutionParentReadSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.SemanticSeed) {
		store := NewStore()
		return store, func(t *testing.T, orgID, resultID string, payload, semanticState []byte) {
			t.Helper()
			store.mu.Lock()
			defer store.mu.Unlock()
			store.results[resultID] = entry{orgID: orgID, payload: payload, semanticState: semanticState}
		}
	})
}

// TestStore_carriedParentChain runs the SHARED carried-parent chain domain:
// every chain is built by the real engine over this store, and every receipt
// state is re-seeded through the store's own raw seed.
func TestStore_carriedParentChain(t *testing.T) {
	cells := paritytest.RunCarriedParentChainSuite(t, func(t *testing.T) (contextfabric.InvestigationResultStore, paritytest.SemanticSeed) {
		store := NewStore()
		return store, func(t *testing.T, orgID, resultID string, payload, semanticState []byte) {
			t.Helper()
			store.mu.Lock()
			defer store.mu.Unlock()
			store.results[resultID] = entry{orgID: orgID, payload: payload, semanticState: semanticState}
		}
	})
	for _, cell := range cells {
		t.Logf("%-96s guard=%-34s kind=%-20s chain=%-18s depth=%d parent=%q served=%s", cell.Name(), cell.Guard, cell.LineKind, cell.LineChain, cell.LineDepth, cell.LineParentID, cell.Served)
	}
}

// TestCarriedParentChainTableCoversTheGeneratedDomain pins the decision table
// to the domain generated from the two closed vocabularies: every
// (parent result kind, chain) pair has exactly one row or one inapplicable
// entry, and nothing else is named. A new member of either vocabulary turns
// this red until the table decides it.
func TestCarriedParentChainTableCoversTheGeneratedDomain(t *testing.T) {
	domain := paritytest.ChainDomain()
	covered, duplicates := paritytest.ChainTableCoverage()
	sorted := append([]string(nil), domain...)
	sort.Strings(sorted)
	if len(duplicates) != 0 {
		t.Errorf("pairs named twice: %v", duplicates)
	}
	if strings.Join(sorted, ",") != strings.Join(covered, ",") {
		t.Errorf("table covers %v\ndomain is %v", covered, sorted)
	}
	if got, want := len(domain), len(contextfabric.SubjectSubstitutionParentResultKindVocabulary())*len(contextfabric.SubjectSubstitutionParentChainVocabulary()); got != want {
		t.Errorf("domain = %d pairs, want %d", got, want)
	}
	for _, row := range paritytest.ChainDecisionTable() {
		for _, followUp := range paritytest.ChainFollowUps() {
			if outcome, ok := row.Want[followUp]; !ok || !contextfabric.ValidSubjectSubstitutionOutcome(outcome) {
				t.Errorf("%s/%s: follow-up %s has no typed outcome", row.Kind, row.Chain, followUp)
			}
		}
		if row.Reason == "" {
			t.Errorf("%s/%s: no reason", row.Kind, row.Chain)
		}
	}
}
