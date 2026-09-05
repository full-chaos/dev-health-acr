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
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SaveStep is one Save call exercised as part of a Case, in order.
type SaveStep struct {
	Principal storage.Principal
	Result    contextfabric.InvestigationResult
	WantErr   bool
	// ParentResultID is the durable ancestry pointer this Save records.
	// Empty for every pre-existing case, which keeps them byte-identical to
	// their pre-ancestry behaviour.
	ParentResultID string
}

// GetStep is the Get call a Case verifies after its Save steps.
type GetStep struct {
	Principal    storage.Principal
	ResultID     string
	WantNotFound bool
	Want         *contextfabric.InvestigationResult
	// WantParentResultID, when set, asserts the ancestry pointer survived the
	// round trip. A POINTER so that "assert it is empty" is expressible and
	// distinguishable from "do not check" -- the empty-string case is the one
	// that matters for a first turn, and a plain string could not say it.
	WantParentResultID *string
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
	built := contextfabric.InvestigationResult{
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
	// The completeness block comes from ITS PRODUCER, never hand-built.
	//
	// Hand-building it means naming every required field from memory, and
	// the block's own `state` is a closed vocabulary whose Go ZERO VALUE is
	// not a member -- so a fixture that simply omits it is invalid in a way
	// a reader cannot see. That is what happened: this fixture set an empty
	// state and every parity case failed on save with `completeness state ""
	// is not a vocabulary member`, in CI's container job only, because the
	// store validates on the way in. Calling the producer means the next
	// required field this block gains arrives here for free.
	built.Completeness = contextfabric.ComputeAnswerCompleteness(built)
	return built
}

// resultWithPlanRequirements is `result` plus the plan-requirement layer: the
// derived requirement rows on the answer plan and a refinement chain on the
// outcome row that accounts for them.
//
// EVERY WIRE ROW IS BUILT THROUGH ITS PRODUCER, never by hand. Both stores
// validate on the way in, and these rows carry closed vocabularies whose Go
// zero value is not a member -- so a hand-built row is invalid in a way a
// reader cannot see. That is not a hypothetical: the completeness block above
// was hand-built once and every case in this table failed on save, in CI's
// container job only.
//
// It exists as its own fixture rather than being folded into `result` so the
// round-trip case for these arrays is NAMED in the table. A field that rides
// along inside an unrelated case is covered by accident, and an accident is
// not something a later change can be held to.
func resultWithPlanRequirements(resultID, question string) contextfabric.InvestigationResult {
	built := result(resultID, question)
	derived := planRequirementFixtureRows()

	plan := contextfabric.AnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "parity-v1",
		Budget:        contextfabric.AnswerPlanBudget{MaxItems: 30, MaxSerializedBytes: 1 << 20},
		Requirements:  contextfabric.PlanRequirementsFromDerived(derived),
	}
	built.AnswerPlan = &plan

	outcomes := contextfabric.SeedRequirementOutcomes(derived)
	// Narrow the first row and record the chain that got it there, so the
	// round trip carries a NON-EMPTY refinement array rather than only the
	// seed rows. An array that is empty in the fixture is an array the parity
	// case cannot prove round-trips.
	outcomes[0].Outcome = contractsv1.ContextFabricRequirementNarrowed
	outcomes[0].Impact = contractsv1.ContextFabricAnswerImpactScope
	outcomes[0].CauseOverrun = contractsv1.ContextFabricBudgetOverrunItems
	outcomes[0].CauseObserved = true
	outcomes[0].Declared = 4
	outcomes[0].Served = 2
	outcomes[0].Refinements = []contractsv1.ContextFabricRequirementRefinement{
		{Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 4, After: 3},
		{Stage: contractsv1.ContextFabricOutcomeStageProjection, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 3, After: 2},
	}
	built.Completeness.Outcomes = outcomes
	// DERIVE LAST: completeness is a function of the whole outcome set and is
	// recomputed after the rows are final, never carried from before them.
	built.Completeness = contextfabric.ComputeAnswerCompleteness(built)
	return built
}

// planRequirementFixtureRows is one READ row and one COMPUTED row with
// distinct coordinates -- two ARMS, so a store that dropped one would fail
// here rather than round-tripping a fixture that only ever exercised one.
func planRequirementFixtureRows() []contextfabric.DerivedRequirement {
	return []contextfabric.DerivedRequirement{
		{
			RequirementCoordinate: contextfabric.RequirementCoordinate{
				Obligation: contextfabric.ObligationState,
				Role:       contextfabric.SubjectRoleSubject,
				Subject:    contextfabric.SubjectProject,
			},
			Kind:       contextfabric.ObligationKindRead,
			FactKinds:  []contextfabric.FactKind{contextfabric.FactHealth, contextfabric.FactStatus},
			Scope:      contextfabric.CompletionScopeSingleSubject,
			Quantifier: contextfabric.CompletionQuantifierAtLeastOne,
		},
		{
			RequirementCoordinate: contextfabric.RequirementCoordinate{
				Obligation: contextfabric.ObligationCount,
				Role:       contextfabric.SubjectRoleMember,
				Subject:    contextfabric.SubjectRepository,
			},
			Kind:          contextfabric.ObligationKindComputed,
			Step:          contextfabric.ComputedStepMembershipCardinality,
			InputClass:    contextfabric.ComputedInputResolvedMemberSet,
			StepExecution: contextfabric.ComputedStepDeclaredOnly,
			Scope:         contextfabric.CompletionScopeEachMember,
			Quantifier:    contextfabric.CompletionQuantifierExact,
		},
	}
}

// Cases is the shared parity table: save+get roundtrip, cross-org
// not-found, unknown-id not-found, idempotent identical replay, and
// rejected divergent replay.
func Cases() []Case {
	roundTrip := result("result-id-roundtrip", "what shipped this week?")
	planRows := resultWithPlanRequirements("result-id-plan-rows", "which requirements did this turn plan?")
	crossOrg := result("result-id-cross-org", "who owns this service?")
	identicalA := result("result-id-idempotent", "is the rollout healthy?")
	identicalB := identicalA
	original := result("result-id-immutable", "why did the deploy fail?")
	divergent := original
	divergent.DirectJudgment = "a different judgment"
	divergent.Question = "why did the deploy fail? (mutated)"
	withParent := result("result-id-ancestry", "which repositories does the ops team own?")
	firstTurn := result("result-id-first-turn", "how is the migration going?")
	// Fixtures for the parent-validation cases below. Each needs its OWN
	// result id: a self-parent case must be able to name its own id, and the
	// bounds cases must not collide with rows another case saved.
	selfParent := result("result-id-self-parent", "does this result parent itself?")
	shortParent := result("result-id-short-parent", "is a three-character parent storable?")
	longParent := result("result-id-long-parent", "is an over-long parent storable?")
	boundParent := result("result-id-bound-parent", "is a parent at the lower bound storable?")
	wideMaxParent := result("result-id-wide-max-parent", "is a 256-rune parent storable?")
	wideMinParent := result("result-id-wide-min-parent", "is a 4-rune parent storable?")

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
			Name: "save and get round trip preserves the plan requirement rows and refinements",
			Save: []SaveStep{{Principal: orgA, Result: planRows}},
			Get:  GetStep{Principal: orgA, ResultID: planRows.ResultID, Want: &planRows},
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
			// Ancestry is store metadata, not part of the result payload, so
			// the payload comparison every other case relies on cannot see
			// it. Without these two cases a store could drop the parent
			// entirely and the whole parity suite would stay green -- and a
			// conversation would be walkable on one backend and silently not
			// on the other.
			Name: "ancestry survives the round trip",
			Save: []SaveStep{
				{Principal: orgA, Result: withParent, ParentResultID: "result-ancestry-parent"},
			},
			Get: GetStep{Principal: orgA, ResultID: withParent.ResultID, Want: &withParent, WantParentResultID: ptr("result-ancestry-parent")},
		},
		{
			// BOTH BACKENDS REFUSE A PARENT THE DATABASE'S CHECK CONSTRAINTS
			// REFUSE, and this case exists because only one of them did.
			// PostgreSQL rejected a self-parent and an out-of-bounds id via
			// ck_..._parent_not_self and ck_..._parent_result_id_len; the
			// in-memory store accepted all three shapes, so an id that failed
			// only in production round-tripped cleanly in tests and dev.
			//
			// The parity suite could not see it because every prior case fed
			// a VALID parent -- a suite that never supplies an invalid input
			// cannot detect that one backend fails to reject it. Same shape as
			// a gate tier with no positive fixture.
			Name: "a self-parent is refused by both backends",
			Save: []SaveStep{
				{Principal: orgA, Result: selfParent, ParentResultID: selfParent.ResultID, WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: selfParent.ResultID, WantNotFound: true},
		},
		{
			Name: "a parent id shorter than the bound is refused by both backends",
			Save: []SaveStep{
				{Principal: orgA, Result: shortParent, ParentResultID: "abc", WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: shortParent.ResultID, WantNotFound: true},
		},
		{
			Name: "a parent id longer than the bound is refused by both backends",
			Save: []SaveStep{
				{Principal: orgA, Result: longParent, ParentResultID: strings.Repeat("x", 257), WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: longParent.ResultID, WantNotFound: true},
		},
		{
			// MULTIBYTE BOUNDS. The bound is stated in CHARACTERS by the
			// migration (char_length) and in RUNES by the request contract
			// (utf8.RuneCountInString); a Go check measuring BYTES agrees with
			// neither the moment the id is not ASCII. Both directions are
			// exercised because they fail differently: a byte-measuring check
			// REJECTS a 256-rune id that both authorities accept (a valid
			// request would fail at Save), and ACCEPTS a 4-rune id that
			// Postgres rejects (the parity claim is simply false there).
			//
			// The ASCII cases above cannot see either: 8 ASCII characters are
			// 8 bytes, so every measurement agrees and the suite reads green
			// while the rule is wrong for any non-ASCII id.
			Name: "a parent id of 256 multibyte runes is accepted by both backends",
			Save: []SaveStep{
				{Principal: orgA, Result: wideMaxParent, ParentResultID: strings.Repeat("é", 256)},
			},
			Get: GetStep{Principal: orgA, ResultID: wideMaxParent.ResultID, Want: &wideMaxParent, WantParentResultID: ptr(strings.Repeat("é", 256))},
		},
		{
			Name: "a parent id of 4 multibyte runes is refused by both backends",
			Save: []SaveStep{
				{Principal: orgA, Result: wideMinParent, ParentResultID: strings.Repeat("é", 4), WantErr: true},
			},
			Get: GetStep{Principal: orgA, ResultID: wideMinParent.ResultID, WantNotFound: true},
		},
		{
			// CONTROL for the three above: a parent at each BOUND is accepted.
			// Without it, a backend that refused every parent outright would
			// pass all three refusal cases while breaking the mechanism.
			Name: "a parent id at the bounds is accepted by both backends",
			Save: []SaveStep{
				{Principal: orgA, Result: boundParent, ParentResultID: strings.Repeat("y", 8)},
			},
			Get: GetStep{Principal: orgA, ResultID: boundParent.ResultID, Want: &boundParent, WantParentResultID: ptr(strings.Repeat("y", 8))},
		},
		{
			// The first turn of a conversation. Empty must round-trip as
			// EMPTY, never as some placeholder: the walk fails closed on an
			// absent parent, and a store that invented a value would send it
			// hunting a result that does not exist.
			Name: "a result with no parent reads back with no parent",
			Save: []SaveStep{
				{Principal: orgA, Result: firstTurn},
			},
			Get: GetStep{Principal: orgA, ResultID: firstTurn.ResultID, Want: &firstTurn, WantParentResultID: ptr("")},
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
				err := store.Save(ctx, step.Principal, step.Result, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, step.ParentResultID)
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
	stored, err := store.Get(ctx, step.Principal, step.ResultID)
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
	// CHAOS-3898 §2.4: Get now returns the StoredInvestigationResult
	// carrier; this parity suite compares the wrapped canonical payload
	// only -- persistence metadata (GraphEpoch) is a store-implementation
	// detail, not part of what parity across stores means here.
	if step.WantParentResultID != nil && stored.ParentResultID != *step.WantParentResultID {
		t.Fatalf("get %q: ParentResultID = %q, want %q -- ancestry must survive the round trip identically in every store, or a conversation is walkable on one backend and not the other", step.ResultID, stored.ParentResultID, *step.WantParentResultID)
	}
	got := stored.Result
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
		err := store.Save(context.Background(), orgA, valid, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "")
		if err == nil {
			t.Fatal("Save() error = nil, want a replay against a stored explicit-null row to be rejected, not treated as an idempotent success")
		}
		if !strings.Contains(err.Error(), "degraded_reasons") || !strings.Contains(err.Error(), "null") {
			t.Fatalf("Save() error = %v, want it to name the offending field", err)
		}
	})
}

// ptr is the address-of helper GetStep.WantParentResultID needs so that
// "assert empty" stays expressible and distinct from "do not check".
func ptr[T any](v T) *T { return &v }
