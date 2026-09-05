package contextfabric

// IS THE REUSED COUNT ROW EVER STALE? THE PREMISE, MEASURED RATHER THAN READ.
//
// The reported defect is this: the reuse degrade drops cohort members and
// sets `Complete=false, Truncated=true`, while `hasAssembledOutcome` sees the
// STORED document's assembled-result `count` row and returns early -- so the
// backfill never re-states the cardinality and the served answer claims an
// exact census over a population it no longer carries.
//
// The mechanism is real; the FIRST STEP OF IT IS NOT REACHABLE. These tests
// establish that by execution rather than by reading, because an unreachable
// premise and a live defect look identical in a source trace, and a fix for
// dead code is a fix nothing can red.
//
// THE STRUCTURAL REASON, in one sentence: the degrade only ever assigns
// `member.EvidenceRefIDs`, evidence references are OPTIONAL on a cohort
// member under both the write and the legacy bounds, and `keepRefs` returns a
// non-nil empty slice -- so `strippingBrokeIt` is false for every member on
// every input, and the member-drop branch beside it cannot run.
//
// WHAT AN EARLIER VERSION OF THIS FILE GOT WRONG, and why the shape below is
// what it is. Review round 1 found two real gaps, both executed as surviving
// mutants before this was written:
//
//	THE CONTROL PROVED THE WRONG ARM. A `Finding` dropping proves the STRIP
//	ran; it says nothing about whether the COHORT loop ran. Disabling the
//	entire cohort block left every assertion green. So the cohort arm now
//	proves itself: a member's reference is asserted PRESENT before the
//	degrade and ABSENT after, which no other arm can satisfy.
//
//	THE SERVING PATH WAS NEVER DRIVEN. Both tests called `degradeReusedResult`
//	directly, so a reduction applied in `tryReuse` AFTER the degrade -- the
//	real shape of the reported defect -- was invisible. One test now drives
//	`Engine.Investigate` and reads the SERVED document.
//
//	THE FIXTURE COULD NOT SEE A RANKED MEMBER, and asserted only counts, so a
//	mutant that dropped ranked members, and one that corrupted a served
//	member's identity, both survived. The fixture now carries a ranked member
//	and every member's identity is asserted field by field.
//
// EVERY ASSERTION HERE CARRIES A POSITIVE CONTROL IN THE SAME RUN. A test
// that only shows a counter staying at zero cannot tell "the branch is
// unreachable" apart from "the fixture never reached the code at all". The
// control is a `Finding`, whose evidence references are REQUIRED
// (allowEmpty=false) and which therefore does break when stripped -- same
// degrade, same run, same missing set.

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The references the probes remove. DISTINCT per carrier so a per-carrier
// over-count cannot hide, and all legal ref ids (8-256 chars, trimmed, no '|').
// EVERY member carries a matched PAIR: one reference the recheck cannot prove
// (removed) and one it can (RETAINED). The symmetry is the point --
// round 3 found that proving removal on one member and preservation on one
// member left both `= nil` (erase everything) and `append(refs, extra)` (serve
// an unproven reference) undetectable on the members that had neither.
const (
	countCurrencyGoneA = "evidence_member_currency_gone_a"
	countCurrencyKeptA = "evidence_member_currency_kept_a"
	countCurrencyGoneB = "evidence_member_currency_gone_b"
	countCurrencyKeptB = "evidence_member_currency_kept_b"
	countCurrencyGoneC = "evidence_member_currency_gone_c"
	countCurrencyKeptC = "evidence_member_currency_kept_c"

	countCurrencyFindingRef = "evidence_finding_currency_a"
)

// currencyKeptRefs maps a member's canonical id to the reference that must
// SURVIVE the degrade. The served list is asserted EXACTLY equal to it -- not
// "contains", which cannot see an added reference.
func currencyKeptRefs() map[string]string {
	return map[string]string{
		"team:CURRENCY_A": countCurrencyKeptA,
		"team:CURRENCY_B": countCurrencyKeptB,
		"team:CURRENCY_C": countCurrencyKeptC,
	}
}

// currencyMissingRefs is every reference the recheck cannot prove, plus the
// control finding's own.
func currencyMissingRefs() map[string]struct{} {
	return map[string]struct{}{
		countCurrencyGoneA:      {},
		countCurrencyGoneB:      {},
		countCurrencyGoneC:      {},
		countCurrencyFindingRef: {},
	}
}

// currencyVisibleRefs is what the graph context proves visible: the answer's
// own citation plus every RETAINED member reference.
func currencyVisibleRefs() []string {
	return []string{reuseCitationRef, countCurrencyKeptA, countCurrencyKeptB, countCurrencyKeptC}
}

func currencyScore(v float64) *float64 { return &v }

// currencyUnrankedMember is the ordinary member shape: no ranking fields.
func currencyUnrankedMember(refs ...string) CohortMember {
	return CohortMember{
		Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_A", Label: "Currency A"},
		Rank:             1,
		InclusionReasons: []string{"matched"},
		EvidenceRefIDs:   append([]string(nil), refs...),
	}
}

// currencyRankedDriverMember is a ranked member carrying REAL driver metadata,
// which round 3 found missing: the fixture had only the zero-signal legal
// variant (`Score == nil`, no drivers), so any mutant keyed on `len(Drivers)`
// could never fire.
//
// The numbers are not decorative -- the contract cross-checks all of them
// (`validateDrivers`): the signal's Weight must be its canonical formula weight
// (25 for health.compounding_risk), `WeightContributed` must equal
// `100*Weight*Value/availableWeight` (100*25*0.5/25 = 50), the drivers must sum
// to Score, every driver's family must appear in RankingBasis and vice versa,
// DataCompleteness must be `degraded` for two drivers or fewer, and a non-nil
// Score forces a qualified-or-provisional Outcome with MissingSignals non-empty
// unless qualified. A fixture that gets any of these wrong is a document the
// real validator rejects, so it could prove nothing about a real answer.
func currencyRankedDriverMember(refs ...string) CohortMember {
	return CohortMember{
		Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_C", Label: "Currency C"},
		Rank:             3,
		InclusionReasons: []string{"matched"},
		EvidenceRefIDs:   append([]string(nil), refs...),
		RankingComputed:  true,
		AttentionRank:    2,
		DataCompleteness: contractsv1.ContextFabricCohortDataDegraded,
		Outcome:          contractsv1.ContextFabricCohortOutcomeProvisional,
		Score:            currencyScore(50),
		RankingBasis:     []string{"health.compounding_risk"},
		MissingSignals: []string{
			"investment_mix",
			"operational_deficiencies.severity",
			"readiness.coverage_gap",
			"workload.forecast_pressure",
		},
		Drivers: []contractsv1.ContextFabricCohortMemberDriver{{
			Signal:            "health.compounding_risk",
			Weight:            25,
			Value:             0.5,
			WeightContributed: 50,
			Window:            contractsv1.ContextFabricCohortMemberDriverWindowCurrent,
		}},
	}
}

// currencyGraphReader answers ResolveSubjects from the REQUEST'S OWN subject
// hints, intersected with an authorized set.
//
// The shared `graphReaderStub` returns a fixed committed set without ever
// reading the request (`engine_test.go`), which makes the reuse recheck
// untestable from the outside: production could stop asking about cohort
// members entirely and the stub would still commit them. Answering from the
// hints is what lets `TestAReusedAnswerWithAnUnauthorizedCohortMemberIsRefused`
// below mean anything.
type currencyGraphReader struct {
	authorized map[string]struct{}
	context    GraphContext
}

func currencyAuthorizedKey(kind SubjectKind, id string) string { return string(kind) + "/" + id }

func (g currencyGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "currency-key", Epoch: 0}, nil
}

func (g currencyGraphReader) ResolveSubjects(_ context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, _ *QuestionFrame, _ SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	committed := make([]SubjectRef, 0, len(request.RequestedScope.SubjectHints))
	for _, hint := range request.RequestedScope.SubjectHints {
		if _, ok := g.authorized[currencyAuthorizedKey(hint.Kind, hint.ID)]; !ok {
			continue
		}
		committed = append(committed, SubjectRef{Kind: hint.Kind, CanonicalID: hint.ID, Label: hint.Label})
	}
	return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}, StructureOfferMaterial{}, nil, nil, nil
}

func (g currencyGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return g.context, nil
}

// currencyRankedMember is the shape round 1 found missing: `RankingComputed`
// true, which a mutant can key on to drop a member the unranked fixture could
// never expose. Score is nil, so the outcome is `insufficient_evidence` and
// the ranking basis and drivers stay empty -- the one legal ranked shape that
// needs no scored driver set.
func currencyRankedMember(refs ...string) CohortMember {
	return CohortMember{
		Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_B", Label: "Currency B"},
		Rank:             2,
		InclusionReasons: []string{"matched"},
		EvidenceRefIDs:   append([]string(nil), refs...),
		RankingComputed:  true,
		AttentionRank:    1,
		DataCompleteness: contractsv1.ContextFabricCohortDataPartial,
		Outcome:          contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
		MissingSignals:   []string{"investment_mix"},
	}
}

// currencyCohort is the two-member population both degrade probes use: one
// unranked, one ranked, each carrying its own distinct reference.
func currencyCohort() *Cohort {
	return &Cohort{
		Kind: SubjectTeam, Rationale: "reuse currency probe", Complete: true,
		// THREE members, each with a gone/kept reference pair: unranked, the
		// zero-signal ranked variant, and a ranked member carrying real driver
		// metadata. All three ranked shapes are legal and the degrade must
		// treat them identically.
		Members: []CohortMember{
			currencyUnrankedMember(countCurrencyGoneA, countCurrencyKeptA),
			currencyRankedMember(countCurrencyGoneB, countCurrencyKeptB),
			currencyRankedDriverMember(countCurrencyGoneC, countCurrencyKeptC),
		},
		// AN EXCLUSION, because `reuseSubjectsToRecheck` collects exclusion
		// subjects too (`answer_reuse.go:900`) and a fixture without one cannot
		// see that arm being skipped -- a stored answer naming an excluded
		// subject this principal can no longer resolve would then be served
		// without the subject ever being rechecked.
		Exclusions: []CohortExclusion{{
			Subject: currencyExcludedSubject(),
			Reason:  "outside the requested window",
		}},
	}
}

// currencyExcludedSubject is the cohort's excluded subject. It is a SUBJECT the
// recheck must re-authorize even though it is not a member.
func currencyExcludedSubject() SubjectRef {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_X", Label: "Currency X"}
}

// currencyRecheckedSubjects is every subject the reuse recheck must ask about:
// each member plus each exclusion. The refusal test denies each in turn.
func currencyRecheckedSubjects(cohort *Cohort) []SubjectRef {
	subjects := make([]SubjectRef, 0, len(cohort.Members)+len(cohort.Exclusions))
	for _, member := range cohort.Members {
		subjects = append(subjects, member.Subject)
	}
	for _, exclusion := range cohort.Exclusions {
		subjects = append(subjects, exclusion.Subject)
	}
	return subjects
}

// assertServedMemberRefsAreExactlyTheKeptSet is the EXACT-equality assertion
// round 3 asked for.
//
// "the missing ones are gone" plus "one retained one survives" is satisfied by
// `append(refs, "evidence_unverified_extra")` -- a served reference the recheck
// never proved, which is precisely what the degrade exists to prevent. Only
// equality against the expected set catches an ADDED reference.
func assertServedMemberRefsAreExactlyTheKeptSet(t *testing.T, where string, members []CohortMember) {
	t.Helper()
	want := currencyKeptRefs()
	for _, member := range members {
		expected, known := want[member.Subject.CanonicalID]
		if !known {
			t.Fatalf("%s: served member %s is not in the fixture's kept-reference table; the table and the "+
				"cohort have drifted apart", where, member.Subject.CanonicalID)
		}
		if len(member.EvidenceRefIDs) != 1 || member.EvidenceRefIDs[0] != expected {
			t.Errorf("%s: served member %s carries refs %v, want exactly [%s] -- anything else means the "+
				"degrade removed a reference the recheck proved, or served one it did not",
				where, member.Subject.CanonicalID, member.EvidenceRefIDs, expected)
		}
	}
}

// currencyReuseEngine builds an engine whose recheck can actually SERVE a
// cohort-bearing stored answer.
//
// IT EXISTS BECAUSE THE SHARED `reuseDegradeEngine` CANNOT, and the reason is
// worth stating: `reuseSubjectsToRecheck` (answer_reuse.go) collects every
// COHORT MEMBER's subject into the recheck set, and the recheck REFUSES
// (`AnswerReuseMissAuthorization`) unless every one of them comes back in
// `resolution.Committed`. The shared helper commits one subject, so a
// cohort-bearing candidate misses reuse entirely and the engine falls through
// to a fresh investigation -- which is precisely what the first version of
// this test did, and it failed loudly on the interpreter stub rather than
// passing while proving nothing.
//
// The shared helper is left ALONE rather than widened: other tests depend on
// its exact committed set, and widening a shared fixture to fit one test is
// how a fixture stops meaning what its other callers assume.
func currencyReuseEngine(t *testing.T, stored InvestigationResult, telemetry *recordingTelemetry, unauthorized ...SubjectRef) *Engine {
	t.Helper()
	denied := make(map[string]struct{}, len(unauthorized))
	for _, subject := range unauthorized {
		denied[currencyAuthorizedKey(subject.Kind, subject.CanonicalID)] = struct{}{}
	}
	authorized := map[string]struct{}{}
	authorize := func(subject SubjectRef) {
		key := currencyAuthorizedKey(subject.Kind, subject.CanonicalID)
		if _, isDenied := denied[key]; isDenied {
			return
		}
		authorized[key] = struct{}{}
	}
	authorize(reuseDegradeSubject())
	if stored.Cohort != nil {
		for _, subject := range currencyRecheckedSubjects(stored.Cohort) {
			authorize(subject)
		}
	}
	return mustReuseTestEngine(t, EngineDependencies{
		// A NON-FATAL interpreter, deliberately. The shared default calls
		// t.Fatal("interpreter should not be reached"), which is right for a
		// test that must hit reuse and wrong for one that must observe a
		// REFUSAL: a refused reuse legitimately falls through to a fresh
		// investigation, and a fatal there would fail the test instead of
		// letting it assert. The served tests lose nothing -- they assert
		// result.Reused, so a fall-through fails them at this error instead.
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{}, errors.New("fresh investigation reached: reuse did not serve")
		}),
		Graph: currencyGraphReader{
			authorized: authorized,
			// The stored answer's own citation and the RETAINED member
			// reference stay visible; the candidate's node ref and the other
			// member references do not.
			context: productionShapedGraphContext(currencyVisibleRefs(), nil),
		},
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return stored, true, nil
		}),
	})
}

// currencyCountRequirement is the count requirement's identity, spelled once.
func currencyCountRequirement() string {
	return string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam)
}

// currencyCountRows is the stored answer's count history: the PLANNING seed
// plus the assembled-result row, satisfied over the two members it carries.
//
// THE PLANNING ROW IS LOAD-BEARING, not decoration. `countRequirement`
// (`membership_cardinality.go:179-188`) scans PLANNING rows only, and returns
// empty when there is none -- so a fixture carrying just the assembled row
// makes `appendMembershipCardinality` return before it ever reaches
// `hasAssembledOutcome`. That guard is the whole subject of the reported
// defect, and without a planning row this test cannot reach it: disabling the
// guard would change nothing observable here. Round 2 found exactly that.
func currencyCountRows(served int) []RequirementOutcomeRow {
	return []RequirementOutcomeRow{
		{
			Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
			Requirement: currencyCountRequirement(),
			Obligation:  string(ObligationCount),
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
		},
		{
			Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
			Requirement: currencyCountRequirement(),
			Obligation:  string(ObligationCount),
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
			Served:      served, Declared: served,
		},
	}
}

// assertMemberIdentityPreserved checks every field of a served member EXCEPT
// its evidence references, which the degrade is allowed to change.
//
// Round 1: asserting only counts let a mutant corrupt a served member's
// identity metadata while keeping the population the same size.
func assertMemberIdentityPreserved(t *testing.T, where string, want, got CohortMember) {
	t.Helper()
	if got.Subject.Kind != want.Subject.Kind ||
		got.Subject.CanonicalID != want.Subject.CanonicalID ||
		got.Subject.Label != want.Subject.Label {
		t.Errorf("%s: served member subject = %+v, want %+v -- the degrade may remove a member's "+
			"evidence references and nothing else", where, got.Subject, want.Subject)
	}
	if got.Rank != want.Rank {
		t.Errorf("%s: served member rank = %d, want %d", where, got.Rank, want.Rank)
	}
	if len(got.InclusionReasons) != len(want.InclusionReasons) {
		t.Errorf("%s: served member inclusion reasons = %v, want %v", where, got.InclusionReasons, want.InclusionReasons)
	} else {
		for i := range want.InclusionReasons {
			if got.InclusionReasons[i] != want.InclusionReasons[i] {
				t.Errorf("%s: served member inclusion reason %d = %q, want %q",
					where, i, got.InclusionReasons[i], want.InclusionReasons[i])
			}
		}
	}
	// CONTENTS, not lengths. A length-only comparison cannot see a changed
	// driver value or score, and the `want` side must be a DEEP copy or it
	// aliases the very storage production mutated.
	if !currencyFloatPtrEqual(got.Score, want.Score) ||
		!currencyStringsEqual(got.RankingBasis, want.RankingBasis) ||
		!currencyDriversEqual(got.Drivers, want.Drivers) ||
		!currencyStringsEqual(got.MissingSignals, want.MissingSignals) {
		t.Errorf("%s: served member ranking detail = {score:%v basis:%v drivers:%+v missing:%v}, "+
			"want {score:%v basis:%v drivers:%+v missing:%v} -- these are contract fields a reader "+
			"acts on, and the degrade may change none of them",
			where, got.Score, got.RankingBasis, got.Drivers, got.MissingSignals,
			want.Score, want.RankingBasis, want.Drivers, want.MissingSignals)
	}
	if got.RankingComputed != want.RankingComputed ||
		got.AttentionRank != want.AttentionRank ||
		got.DataCompleteness != want.DataCompleteness ||
		got.Outcome != want.Outcome {
		t.Errorf("%s: served member ranking fields = {computed:%v rank:%d completeness:%q outcome:%q}, "+
			"want {computed:%v rank:%d completeness:%q outcome:%q}",
			where, got.RankingComputed, got.AttentionRank, got.DataCompleteness, got.Outcome,
			want.RankingComputed, want.AttentionRank, want.DataCompleteness, want.Outcome)
	}
}

// currencyDeepCopyMembers is what `append([]CohortMember(nil), ...)` is NOT.
//
// A plain append copies the STRUCTS but not what their slice fields point at,
// so `want` and `got` share one `Drivers` backing array: production mutating
// `member.Drivers[0]` changes BOTH sides and the comparison cannot see it.
// Confirmed by execution -- a mutant setting `Drivers[0].Value` and `Score`
// survived the shallow version.
func currencyDeepCopyMembers(members []CohortMember) []CohortMember {
	out := make([]CohortMember, 0, len(members))
	for _, member := range members {
		member.EvidenceRefIDs = append([]string(nil), member.EvidenceRefIDs...)
		member.InclusionReasons = append([]string(nil), member.InclusionReasons...)
		member.RankingBasis = append([]string(nil), member.RankingBasis...)
		member.MissingSignals = append([]string(nil), member.MissingSignals...)
		member.Drivers = append([]contractsv1.ContextFabricCohortMemberDriver(nil), member.Drivers...)
		if member.Score != nil {
			score := *member.Score
			member.Score = &score
		}
		out = append(out, member)
	}
	return out
}

func currencyDriversEqual(a, b []contractsv1.ContextFabricCohortMemberDriver) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Signal != b[i].Signal || a[i].Weight != b[i].Weight ||
			a[i].Value != b[i].Value || a[i].WeightContributed != b[i].WeightContributed ||
			a[i].Window != b[i].Window {
			return false
		}
	}
	return true
}

func currencyStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func currencyFloatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestStrippingEveryEvidenceRefLeavesACohortMemberValid isolates the bound
// that makes the member-drop branch unreachable.
//
// It drives the CONTRACT's own validators, never a local restatement of the
// rule, and it takes the stripped value from `keepRefs` ITSELF rather than
// hand-building an empty slice -- round 1's point that a hand-built value
// proves nothing about what the production helper actually returns.
//
// Both bound sets are exercised: `Validate()` is the WRITE path, and
// `ValidateStoredResult` reaches the LEGACY path the reuse degrade actually
// re-validates against.
func TestStrippingEveryEvidenceRefLeavesACohortMemberValid(t *testing.T) {
	t.Parallel()

	missing := currencyMissingRefs()

	for _, member := range []CohortMember{
		currencyUnrankedMember(countCurrencyGoneA),
		currencyRankedMember(countCurrencyGoneB),
		currencyRankedDriverMember(countCurrencyGoneC),
	} {
		name := member.Subject.CanonicalID
		// NON-VACUITY: valid BEFORE the strip, or `strippingBrokeIt` would be
		// false for a reason this probe is not about.
		if err := member.Validate(); err != nil {
			t.Fatalf("%s: fixture member does not validate before stripping: %v -- this probe measures a "+
				"valid-then-invalid transition and there is no valid state to start from", name, err)
		}

		// THE PRODUCTION HELPER'S OWN RETURN VALUE, not a hand-built empty slice.
		kept, removed := keepRefs(member.EvidenceRefIDs, missing)
		if len(removed) != 1 {
			t.Fatalf("%s: keepRefs removed %d refs, want 1 -- the fixture is not exercising the strip", name, len(removed))
		}
		if kept == nil {
			t.Fatalf("%s: keepRefs returned a NIL slice; the unreachability argument rests on it returning "+
				"a non-nil empty slice, so this is the argument's second step failing", name)
		}
		if len(kept) != 0 {
			t.Fatalf("%s: keepRefs kept %d refs, want 0", name, len(kept))
		}

		stripped := member
		stripped.EvidenceRefIDs = kept
		if err := stripped.Validate(); err != nil {
			t.Fatalf("%s: a cohort member stripped of every evidence reference no longer validates under the "+
				"WRITE bounds: %v -- if this is now true, members ARE droppable on the reuse degrade and the "+
				"count row goes stale silently", name, err)
		}

		// And nil, the other shape a stored row can legitimately carry.
		nilled := member
		nilled.EvidenceRefIDs = nil
		if err := nilled.Validate(); err != nil {
			t.Fatalf("%s: a cohort member with nil evidence refs no longer validates: %v", name, err)
		}
	}

	// THE LEGACY BOUNDS, reached through the validator the degrade itself
	// calls. Round 1: the write-path `Validate()` alone does not establish the
	// "under BOTH bound sets" half of the claim.
	stored := storedResultWithCandidateEvidence()
	stored.Cohort = &Cohort{
		Kind: SubjectTeam, Rationale: "legacy bound probe", Complete: true,
		// Both carry NO evidence references at all -- the shape the legacy
		// bounds must accept for the "under both bound sets" claim to hold.
		Members: []CohortMember{
			currencyUnrankedMember(),
			currencyRankedMember(),
		},
	}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if err := ValidateStoredResult(stored); err != nil {
		t.Fatalf("a stored result whose cohort members carry NO evidence references is rejected under the "+
			"LEGACY bounds: %v -- the unreachability argument claims refs are optional under both bound "+
			"sets, and this is the legacy half", err)
	}

	// POSITIVE CONTROL: a Finding's evidence references are REQUIRED, so the
	// identical strip DOES break it. Without this the assertions above are
	// indistinguishable from a validator that accepts everything.
	finding := Finding{
		FindingID:      "finding_currency_01",
		Kind:           string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary:        "Acceptance work remains open.",
		Subjects:       []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_A", Label: "Currency A"}},
		EvidenceRefIDs: []string{countCurrencyFindingRef},
	}
	if err := finding.Validate(); err != nil {
		t.Fatalf("the control finding does not validate before stripping: %v", err)
	}
	strippedFinding := finding
	strippedFinding.EvidenceRefIDs = make([]string, 0, 1)
	if strippedFinding.Validate() == nil {
		t.Fatal("the CONTROL did not fire: a finding stripped of every evidence reference still validates, " +
			"so this file cannot tell an unreachable branch apart from a validator that accepts anything")
	}
}

// TestTheReuseDegradeNeverDropsACohortMember drives the whole degrade and
// reads the counter the reported defect depends on.
//
// Three things make the zero meaningful, and round 1 showed that the first
// alone is not enough:
//
//	the FINDING control fires   -- the missing set reached the strip;
//	the COHORT ARM proves itself -- a member's reference is present before and
//	                                absent after, which no other arm can do;
//	member IDENTITY is asserted  -- so a mutant cannot corrupt a served member
//	                                while keeping the population the same size.
func TestTheReuseDegradeNeverDropsACohortMember(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.Cohort = currencyCohort()
	stored.RemainingWork = []Finding{{
		FindingID: "finding_currency_01",
		Kind:      string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary:   "Acceptance work remains open.",
		Subjects:  []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_A", Label: "Currency A"}},
		// The CONTROL carrier: required refs, so this one breaks.
		EvidenceRefIDs: []string{countCurrencyFindingRef},
	}}
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes, currencyCountRows(len(stored.Cohort.Members))...)
	stored.Completeness = ComputeAnswerCompleteness(stored)

	wantMembers := currencyDeepCopyMembers(stored.Cohort.Members)

	// PRE-STATE for the cohort-arm controls: every member carries at least the
	// reference that must disappear, and one carries a reference that must
	// SURVIVE.
	for _, member := range stored.Cohort.Members {
		if len(member.EvidenceRefIDs) == 0 {
			t.Fatalf("fixture member %s carries no refs -- the cohort-arm control needs a reference to "+
				"watch disappear", member.Subject.CanonicalID)
		}
	}
	kept := currencyKeptRefs()
	for _, member := range stored.Cohort.Members {
		if !containsRef(member.EvidenceRefIDs, kept[member.Subject.CanonicalID]) {
			t.Fatalf("fixture member %s carries no RETAINED reference; without one on EVERY member, "+
				"proving the missing refs are gone cannot tell correct stripping apart from erasing "+
				"every member reference", member.Subject.CanonicalID)
		}
	}

	missing := currencyMissingRefs()
	degraded, counts, _, ok := degradeReusedResult(stored, missing)
	if !ok {
		t.Fatal("degradeReusedResult() refused; this fixture is meant to degrade")
	}

	// CONTROL 1, read FIRST: the missing set really did reach the strip and
	// really did drop an object.
	if counts.DroppedFindings == 0 {
		t.Fatalf("the CONTROL did not fire: the degrade dropped no finding even though its only evidence "+
			"reference was in the missing set. counts = %+v", counts)
	}
	if counts.Refs() == 0 {
		t.Fatalf("the degrade removed no references at all; counts = %+v", counts)
	}
	if degraded.Cohort == nil {
		t.Fatal("the degraded answer carries no cohort")
	}

	// CONTROL 2 -- THE COHORT ARM PROVES ITSELF. Round 1: the finding control
	// above is satisfied with the entire cohort block disabled, so it cannot
	// stand for the cohort loop having run. This can: both members' references
	// were present before and must be gone now, and only the cohort arm
	// removes them.
	for _, member := range degraded.Cohort.Members {
		for _, ref := range member.EvidenceRefIDs {
			if _, gone := missing[ref]; gone {
				t.Fatalf("the COHORT ARM did not run: served member %s still carries %q, a reference the "+
					"recheck could not prove visible -- so a zero member drop below says nothing about "+
					"the cohort loop", member.Subject.CanonicalID, ref)
			}
		}
	}

	// CONTROL 3 -- PRESERVATION, not just removal. The recheck CAN prove this
	// reference, so correct stripping keeps it. `member.EvidenceRefIDs = nil`
	// erases everything and satisfies the removal control above; only this
	// assertion tells the two apart.
	assertServedMemberRefsAreExactlyTheKeptSet(t, "degrade", degraded.Cohort.Members)

	// THE PREMISE OF THE REPORTED DEFECT, measured.
	if counts.DroppedMembers != 0 {
		t.Fatalf("the reuse degrade dropped %d cohort member(s). The reported stale-count defect becomes "+
			"REACHABLE the moment this is non-zero: `hasAssembledOutcome` would then return early over a stored "+
			"row describing a population the served answer no longer carries", counts.DroppedMembers)
	}
	if got, want := len(degraded.Cohort.Members), len(wantMembers); got != want {
		t.Fatalf("the degraded cohort carries %d members, want %d -- the population did not change", got, want)
	}
	for i := range wantMembers {
		assertMemberIdentityPreserved(t, "degrade", wantMembers[i], degraded.Cohort.Members[i])
	}

	// The cohort's coverage flags are untouched, which is the OTHER half of
	// the same fact: the branch that flips them is the member-drop branch.
	if !degraded.Cohort.Complete || degraded.Cohort.Truncated {
		t.Fatalf("the degraded cohort reports complete=%v truncated=%v; both move only inside the "+
			"member-drop branch", degraded.Cohort.Complete, degraded.Cohort.Truncated)
	}

	// THE REUSE-STAGE NARROWING ROW'S OWN NUMBERS. Nothing else in the suite
	// pins them (checked), so an overstated `Declared` on that row -- a
	// narrowing claiming to have removed more than it did -- was invisible to
	// every test here while remaining internally consistent enough for the
	// validator to accept it.
	var reuseRows int
	for _, row := range degraded.Completeness.Outcomes {
		if row.Stage != contractsv1.ContextFabricOutcomeStageReuse {
			continue
		}
		reuseRows++
		if got, want := row.Declared-row.Served, counts.Refs(); got != want {
			t.Errorf("the reuse-stage narrowing row claims %d references were removed (declared %d, "+
				"served %d) while the degrade removed %d -- the row a reader consults for the size of "+
				"the narrowing does not describe the narrowing that happened",
				got, row.Declared, row.Served, want)
		}
	}
	if reuseRows != 1 {
		t.Fatalf("reuse-stage outcome rows = %d, want exactly 1", reuseRows)
	}

	// And therefore the served count still describes the members served.
	rows := countOutcomeRows(degraded, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows after degrade = %d, want exactly 1", len(rows))
	}
	if got, want := rows[0].Served, len(degraded.Cohort.Members); got != want {
		t.Fatalf("the served count says %d and the served answer carries %d members", got, want)
	}
}

// TestAServedReusedAnswersCountDescribesTheMembersItServes drives the PUBLIC
// entry point and reads the SERVED document.
//
// Round 1's sharpest finding: both tests above stop at `degradeReusedResult`,
// while the real serving path continues through `tryReuse` and the engine's
// reuse arm. A reduction applied anywhere after the degrade -- which is the
// exact shape of the reported defect -- is invisible to them. This test cannot
// be satisfied by a document the caller never receives.
func TestAServedReusedAnswersCountDescribesTheMembersItServes(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.Cohort = currencyCohort()
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes, currencyCountRows(len(stored.Cohort.Members))...)
	stored.Completeness = ComputeAnswerCompleteness(stored)
	wantMembers := currencyDeepCopyMembers(stored.Cohort.Members)

	// A missing top-level citation would REFUSE, which is a different test --
	// the refs this drops are all auxiliary, so this degrades.
	telemetry := &recordingTelemetry{}
	engine := currencyReuseEngine(t, stored, telemetry)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// REACH GUARD, before any assertion about the served document. A reuse
	// MISS falls through to a fresh investigation, and a fresh result would
	// satisfy several assertions below for reasons that have nothing to do
	// with reuse. This test is only meaningful on the reuse path.
	if !result.Reused {
		t.Fatal("result.Reused = false: the engine did not serve from the store, so nothing below is " +
			"about the reuse path -- check that every cohort member's subject is in the recheck's " +
			"committed set (reuseSubjectsToRecheck refuses on any that is not)")
	}
	if got := lastReuseOutcome(t, telemetry); got != AnswerReuseHitDegraded {
		t.Fatalf("reuse outcome = %q, want %q -- this test needs the DEGRADED arm", got, AnswerReuseHitDegraded)
	}
	if result.Cohort == nil {
		t.Fatal("the served answer carries no cohort")
	}

	// THE HARM ASSERTION: the served count row describes the served member set.
	// A reduction applied after the count row was written -- in the degrade, in
	// tryReuse, or anywhere else on this path -- fails here.
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("served assembled-result `count` rows = %d, want exactly 1", len(rows))
	}
	if got, want := rows[0].Served, len(result.Cohort.Members); got != want {
		t.Fatalf("the SERVED answer states a count of %d over a cohort of %d members: the caller is told a "+
			"cardinality for a population they did not receive", got, want)
	}

	// The cohort arm ran on this path too -- no member may serve a reference
	// the recheck could not prove.
	gone := currencyMissingRefs()
	for _, member := range result.Cohort.Members {
		for _, ref := range member.EvidenceRefIDs {
			if _, unproven := gone[ref]; unproven {
				t.Fatalf("served member %s still carries %q, a reference the recheck could not prove "+
					"visible; the cohort arm did not run on the serving path",
					member.Subject.CanonicalID, ref)
			}
		}
	}
	assertServedMemberRefsAreExactlyTheKeptSet(t, "served", result.Cohort.Members)
	if got, want := len(result.Cohort.Members), len(wantMembers); got != want {
		t.Fatalf("the SERVED cohort carries %d members, want %d", got, want)
	}
	for i := range wantMembers {
		assertMemberIdentityPreserved(t, "served", wantMembers[i], result.Cohort.Members[i])
	}

	// THE OPERATOR'S VIEW, asserted too. The containment event is what an
	// operator reads to know what the degrade removed; a served answer that is
	// correct while its telemetry says otherwise is the "telemetry describes a
	// different artifact than the one served" class, and nothing else here
	// reads this event.
	if len(telemetry.answerReuseContainment) != 1 {
		t.Fatalf("containment events = %d, want exactly 1", len(telemetry.answerReuseContainment))
	}
	containment := telemetry.answerReuseContainment[0]
	if containment.DroppedMembers != 0 {
		t.Errorf("containment telemetry reports %d dropped member(s) while the served cohort is intact; "+
			"an operator would read a member loss that did not happen", containment.DroppedMembers)
	}
	if containment.StrippedRefs == 0 {
		t.Error("containment telemetry reports 0 stripped refs on a degraded reuse; the operator's own " +
			"record of the narrowing is empty while the served answer really did lose references")
	}
}

// TestAReusedAnswerWithAnUnauthorizedCohortMemberIsRefused pins that cohort
// members PARTICIPATE in the reuse authorization recheck.
//
// Round 2's point: the shared resolver commits whatever it is handed without
// reading the request, so production could stop asking about cohort members
// entirely and every test above would still pass. This one cannot pass that
// way -- the resolver here answers from the request's OWN subject hints, so a
// member subject that production never puts in the recheck set is a member
// whose authorization is never checked, and this test says so.
//
// The harm is concrete: serving a stored answer containing a subject this
// principal can no longer resolve.
func TestAReusedAnswerWithAnUnauthorizedCohortMemberIsRefused(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.Cohort = currencyCohort()
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes, currencyCountRows(len(stored.Cohort.Members))...)
	stored.Completeness = ComputeAnswerCompleteness(stored)

	// EVERY RECHECKED SUBJECT is denied in turn, one subtest each -- members
	// AND exclusions, because `reuseSubjectsToRecheck` collects both.
	//
	// Denying only ONE subject cannot see a production loop that skips a
	// DIFFERENT one: `range candidate.Cohort.Members[1:]` leaves member A
	// unchecked while the denied member B is still checked, and dropping the
	// exclusions loop entirely is invisible to a fixture with no exclusions.
	// Both survived until this was table-driven over the whole recheck set.
	rechecked := currencyRecheckedSubjects(stored.Cohort)
	if len(stored.Cohort.Members) < 3 || len(stored.Cohort.Exclusions) < 1 {
		t.Fatalf("fixture carries %d members and %d exclusions; this test needs every RECHECKED subject "+
			"denied in turn, and the exclusion arm is invisible without at least one exclusion",
			len(stored.Cohort.Members), len(stored.Cohort.Exclusions))
	}

	for _, subject := range rechecked {
		denied := subject
		t.Run(denied.CanonicalID, func(t *testing.T) {
			t.Parallel()
			// A FRESH stored result per subtest. The parallel subtests must not
			// share one, or a mutation on the serving path in any of them would
			// make the others' results depend on scheduling order.
			stored := storedResultWithCandidateEvidence()
			stored.Cohort = currencyCohort()
			stored.Completeness.Outcomes = append(stored.Completeness.Outcomes,
				currencyCountRows(len(stored.Cohort.Members))...)
			stored.Completeness = ComputeAnswerCompleteness(stored)

			telemetry := &recordingTelemetry{}
			engine := currencyReuseEngine(t, stored, telemetry, denied)

			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err == nil && result.Reused {
				t.Fatalf("a stored answer whose cohort subject %s no longer resolves for this principal was "+
					"SERVED from the store; the recheck must refuse rather than serve a subject it could "+
					"not re-authorize -- members AND exclusions are both in the recheck set", denied.CanonicalID)
			}
			if got := lastReuseOutcome(t, telemetry); got != AnswerReuseMissAuthorization {
				t.Fatalf("reuse outcome = %q, want %q -- the miss must be attributed to authorization, or "+
					"an operator cannot tell it from an ordinary cache miss", got, AnswerReuseMissAuthorization)
			}
		})
	}
}
