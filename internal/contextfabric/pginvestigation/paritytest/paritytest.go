// Package paritytest is the single, shared table of
// contextfabric.InvestigationResultStore behavior scenarios. Both
// pginvestigation.Store and memoryinvestigation.Store run this exact same
// table (see pginvestigation's *_integration_test.go and
// memoryinvestigation's store_test.go) so their observable behavior cannot
// silently drift apart -- a change to one implementation that breaks parity
// fails here regardless of which package the change was made in.
//
// This lives under pginvestigation/ (rather than a third top-level
// package) so it stays within the CHAOS-3755 change boundary
// (pginvestigation/** and memoryinvestigation/**).
package paritytest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SaveStep is one Save call exercised as part of a Case, in order.
type SaveStep struct {
	Principal storage.Principal
	Result    contextfabric.InvestigationResult
	WantErr   bool
}

// GetStep is the Get call a Case verifies after its Save steps.
type GetStep struct {
	Principal    storage.Principal
	ResultID     string
	WantNotFound bool
	Want         *contextfabric.InvestigationResult
}

// Case is one save/get parity scenario.
type Case struct {
	Name string
	Save []SaveStep
	Get  GetStep
	// ExtraGets are additional Get checks run after Get, for cases that
	// need to verify more than one principal's view of the same
	// result_id (e.g. M1's cross-org-conflict case: org A's row is
	// intact AND org B never actually acquired it).
	ExtraGets []GetStep
}

var (
	orgA = storage.Principal{OrgID: "org-parity-a"}
	orgB = storage.Principal{OrgID: "org-parity-b"}

	generatedAt = time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
)

// ValidResult builds a FULLY VALID InvestigationResult (satisfies
// InvestigationResult.Validate() -- both stores validate on Save and on
// Get). It is exported so a store's own tests can build the same
// known-valid fixture the shared parity table uses, instead of each
// package keeping a private copy that drifts as
// InvestigationResult.Validate() tightens.
func ValidResult(resultID, question string) contextfabric.InvestigationResult {
	return result(resultID, question)
}

// result builds a FULLY VALID InvestigationResult (satisfies
// InvestigationResult.Validate() -- see M2 below, both stores now validate
// on Save and on Get). Every field Validate() requires non-nil/non-empty
// is populated, even where the specific scenario a Case exercises doesn't
// care about its content.
func result(resultID, question string) contextfabric.InvestigationResult {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-" + resultID, Label: "Project " + resultID}
	return contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      resultID,
		RequestID:     "request-" + resultID,
		GeneratedAt:   generatedAt,
		Status:        contextfabric.InvestigationComplete,
		Question:      question,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		SubjectResolution:   contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
		DirectJudgment:      "judgment for " + question,
		DeterministicAnswer: "deterministic answer for " + question,
		StrongestPressures:  []string{},
		Drivers:             []contextfabric.DriverJudgment{},
		RemainingWork:       []contextfabric.Finding{},
		ReadinessGaps:       []contextfabric.Finding{},
		Paths:               []contextfabric.RelationshipPath{},
		Conflicts:           []contextfabric.Finding{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Versions: contextfabric.VersionSet{
			ServiceVersion: "test", ContractVersion: contextfabric.InvestigationResultSchemaV1, Backend: "test",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
		},
		Warnings: []string{},
	}
}

// Cases is the shared parity table: save+get roundtrip, cross-org
// not-found, unknown-id not-found, idempotent identical replay, and
// rejected divergent replay.
func Cases() []Case {
	roundTrip := result("result-id-roundtrip", "what shipped this week?")
	crossOrg := result("result-id-cross-org", "who owns this service?")
	identicalA := result("result-id-idempotent", "is the rollout healthy?")
	identicalB := identicalA
	original := result("result-id-immutable", "why did the deploy fail?")
	divergent := original
	divergent.DirectJudgment = "a different judgment"
	divergent.Question = "why did the deploy fail? (mutated)"

	// M1 (Codex adversarial review, CHAOS-3755): a result_id collision
	// across two DIFFERENT organizations must reject the second Save
	// outright, org-scoped -- not silently succeed (idempotent) or
	// silently overwrite org A's row just because result_id (the sole
	// prior conflict target) matched. Deliberately BYTE-IDENTICAL content
	// for both orgs: InvestigationResult carries no organization
	// discriminator of its own (see the port's doc comment), so the
	// content-equality idempotent-replay path alone cannot distinguish
	// "the same org retried" from "a different org saved the exact same
	// content" -- the conflict check must be org-scoped independent of
	// content equality, or org B's save would be silently treated as a
	// successful idempotent replay while the row still belongs to org A.
	crossOrgConflictA := result("result-id-cross-org-conflict", "org A's question")
	crossOrgConflictB := crossOrgConflictA

	// M2 (Codex adversarial review, CHAOS-3755): Save must reject a
	// semantically invalid result (the same InvestigationResult.Validate()
	// the public API enforces before ever returning a result to a caller)
	// rather than persisting it -- an immutable row that fails the
	// contract it's supposed to satisfy can never be corrected later.
	invalid := contextfabric.InvestigationResult{ResultID: "result-id-invalid-0000001"}

	return []Case{
		{
			Name: "save and get round trip",
			Save: []SaveStep{{Principal: orgA, Result: roundTrip}},
			Get:  GetStep{Principal: orgA, ResultID: roundTrip.ResultID, Want: &roundTrip},
		},
		{
			Name: "get scoped to a different org returns not found",
			Save: []SaveStep{{Principal: orgA, Result: crossOrg}},
			Get:  GetStep{Principal: orgB, ResultID: crossOrg.ResultID, WantNotFound: true},
		},
		{
			Name: "get unknown result id returns not found",
			Get:  GetStep{Principal: orgA, ResultID: "unknown-result-id-00000000", WantNotFound: true},
		},
		{
			Name: "save same result id twice with identical payload is idempotent",
			Save: []SaveStep{
				{Principal: orgA, Result: identicalA},
				{Principal: orgA, Result: identicalB},
			},
			Get: GetStep{Principal: orgA, ResultID: identicalA.ResultID, Want: &identicalA},
		},
		{
			Name: "save same result id twice with different payload errors and leaves the original intact",
			Save: []SaveStep{
				{Principal: orgA, Result: original},
				{Principal: orgA, Result: divergent, WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: original.ResultID, Want: &original},
		},
		{
			Name: "save under a different org with a colliding result id is rejected and leaves org A's row intact",
			Save: []SaveStep{
				{Principal: orgA, Result: crossOrgConflictA},
				{Principal: orgB, Result: crossOrgConflictB, WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: crossOrgConflictA.ResultID, Want: &crossOrgConflictA},
			ExtraGets: []GetStep{
				// Org B's Save was rejected -- it must never be able to
				// Get this result_id either. A store that let this
				// succeed would mean org B's rejected Save nonetheless
				// left the result reachable under org B's own identity.
				{Principal: orgB, ResultID: crossOrgConflictB.ResultID, WantNotFound: true},
			},
		},
		{
			Name: "save rejects a semantically invalid result and persists nothing",
			Save: []SaveStep{
				{Principal: orgA, Result: invalid, WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: invalid.ResultID, WantNotFound: true},
		},
	}
}

// RunSuite runs Cases() against a fresh store per case. newStore must
// return a clean, empty store (or an equivalent independent scope) each
// call. isNotFound classifies a Get error as the implementation's
// not-found sentinel; the two implementations intentionally define their
// own local sentinel (pginvestigation.ErrNotFound,
// memoryinvestigation.ErrNotFound) rather than sharing one, so callers
// supply their own predicate.
func RunSuite(t *testing.T, newStore func(t *testing.T) contextfabric.InvestigationResultStore, isNotFound func(error) bool) {
	t.Helper()
	for _, testCase := range Cases() {
		t.Run(testCase.Name, func(t *testing.T) {
			store := newStore(t)
			ctx := context.Background()

			for index, step := range testCase.Save {
				err := store.Save(ctx, step.Principal, step.Result, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{})
				if step.WantErr {
					if err == nil {
						t.Fatalf("save[%d] %q: want error, got nil", index, step.Result.ResultID)
					}
					continue
				}
				if err != nil {
					t.Fatalf("save[%d] %q: unexpected error: %v", index, step.Result.ResultID, err)
				}
			}

			runGet(t, ctx, store, testCase.Get, isNotFound)
			for _, extra := range testCase.ExtraGets {
				runGet(t, ctx, store, extra, isNotFound)
			}
		})
	}
}

func runGet(t *testing.T, ctx context.Context, store contextfabric.InvestigationResultStore, step GetStep, isNotFound func(error) bool) {
	t.Helper()
	got, err := store.Get(ctx, step.Principal, step.ResultID)
	if step.WantNotFound {
		if !isNotFound(err) {
			t.Fatalf("get %q: want not-found error, got %v", step.ResultID, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("get %q: unexpected error: %v", step.ResultID, err)
	}
	if step.Want == nil {
		return
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got result: %v", err)
	}
	wantJSON, err := json.Marshal(*step.Want)
	if err != nil {
		t.Fatalf("marshal want result: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("get %q: result mismatch\n got: %s\nwant: %s", step.ResultID, gotJSON, wantJSON)
	}
}

// RawSeed plants a payload directly into a store's backing storage,
// bypassing Save. Each store supplies its own (a map write for the memory
// store, a direct INSERT for Postgres) because there is no port-level way
// to reach the state these cases need: a row that arrived by some path
// other than Save.
type RawSeed func(t *testing.T, orgID, resultID string, payload []byte)

// TaintedExplicitNullPayload serializes result and rewrites its coverage to
// carry a literal `"degraded_reasons": null`.
//
// This is the one wire shape encoding/json cannot round-trip faithfully:
// degraded_reasons is `omitempty` in Go and optional (never nullable) in
// the Coverage schema, so OMITTED is the only schema-conformant way to
// skip it -- yet an omitted field and an explicit null both decode to the
// same nil slice. Once json.Unmarshal returns, the distinction is gone, so
// a store must catch it on the raw bytes.
func TaintedExplicitNullPayload(t *testing.T, result contextfabric.InvestigationResult) []byte {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	tainted := bytes.Replace(encoded, []byte(`"sources":[]`), []byte(`"sources":[],"degraded_reasons":null`), 1)
	if bytes.Equal(tainted, encoded) {
		t.Fatal("test setup: expected substring not found in fixture JSON, so nothing was tainted")
	}
	return tainted
}

// RunExplicitNullDegradedReasonsSuite is the SHARED half of the
// explicit-null guard. Each store carries its own copy of the raw-bytes
// check, so without one table binding them, the two implementations can
// drift apart silently -- which is the exact failure mode this package
// exists to prevent. Both the read path and the write path are covered:
// a planted row must not be returned to a caller, and must not be trusted
// as the target of an idempotent replay either.
func RunExplicitNullDegradedReasonsSuite(t *testing.T, newStore func(t *testing.T) (contextfabric.InvestigationResultStore, RawSeed)) {
	t.Helper()

	t.Run("get rejects a stored result with explicit null degraded_reasons", func(t *testing.T) {
		store, seed := newStore(t)
		valid := ValidResult("result-explicit-null-get", "is the rollout healthy?")
		seed(t, orgA.OrgID, valid.ResultID, TaintedExplicitNullPayload(t, valid))

		_, err := store.Get(context.Background(), orgA, valid.ResultID)
		if err == nil {
			t.Fatal("Get() error = nil, want a stored explicit-null row to be rejected rather than returned")
		}
		if !strings.Contains(err.Error(), "degraded_reasons") || !strings.Contains(err.Error(), "null") {
			t.Fatalf("Get() error = %v, want it to name the offending field", err)
		}
	})

	t.Run("save rejects a replay against a stored explicit null result", func(t *testing.T) {
		store, seed := newStore(t)
		valid := ValidResult("result-explicit-null-save", "is the rollout healthy?")
		seed(t, orgA.OrgID, valid.ResultID, TaintedExplicitNullPayload(t, valid))

		// Saving the clean equivalent must NOT be waved through as a
		// successful idempotent replay: doing so would report success
		// while leaving the invalid stored row in place forever, since
		// these stores never overwrite.
		err := store.Save(context.Background(), orgA, valid, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{})
		if err == nil {
			t.Fatal("Save() error = nil, want a replay against a stored explicit-null row to be rejected, not treated as an idempotent success")
		}
		if !strings.Contains(err.Error(), "degraded_reasons") || !strings.Contains(err.Error(), "null") {
			t.Fatalf("Save() error = %v, want it to name the offending field", err)
		}
	})
}
