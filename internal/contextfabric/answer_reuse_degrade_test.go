package contextfabric

import (
	"context"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Tests for the answer-reuse containment recheck and its partial-miss
// degrade path.
//
// Every test in this file is RED on the parent commit for a stated reason,
// and the reasons are different: (a) fails because the visible set was
// collected from a narrower surface than the demanded set; (b) and (d)
// fail to build there at all, because degrading instead of refusing did
// not exist; (c) passes there and is here as the guard that the refusal
// this change is careful to PRESERVE was in fact preserved.

// reuseCitationRef is a top-level citation: the answer's own evidence, and
// the only class whose disappearance refuses a reuse.
const reuseCitationRef = "evidence_cited_by_the_answer"

// reuseNodeRef rides on a SUBJECT CANDIDATE. In production these come from
// graph NODE attributes, and the aggregate GraphContext.EvidenceRefIDs is
// built from EDGE attributes -- so a node ref is never a member of it.
// That asymmetry is the defect; this constant is what makes the fixture
// model it rather than assert it abstractly.
const reuseNodeRef = "evidence_on_an_unchosen_candidate_node"

func reuseDegradeSubject() SubjectRef {
	return SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
}

func reuseDegradeCandidate(refs ...string) SubjectCandidate {
	return reuseDegradeCandidateNamed("receipt_degrade01", refs...)
}

func reuseDegradeCandidateNamed(receiptID string, refs ...string) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID:      receiptID,
		Subject:        reuseDegradeSubject(),
		State:          ResolutionCommitted,
		MatchReasons:   []string{"Exact canonical subject hint matched the organization graph."},
		Confidence:     1,
		EvidenceRefIDs: append([]string{}, refs...),
	}
}

// storedResultWithCandidateEvidence is the shape the live measurement
// found: a decisive answer citing a small set of refs, carrying a subject
// candidate whose own ref is NOT among them.
func storedResultWithCandidateEvidence() InvestigationResult {
	candidate := validInvestigationResult()
	candidate.ResultID = "result_reused_degrade"
	candidate.RequestID = "request_original_dg"
	candidate.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{reuseDegradeCandidate(reuseNodeRef)},
		Committed:  []SubjectRef{reuseDegradeSubject()},
	}
	candidate.EvidenceRefIDs = []string{reuseCitationRef}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	return candidate
}

// productionShapedGraphContext models what DiscoverContext actually
// returns: the aggregate ref list is the EDGE closure (citations only),
// while candidate refs ride on the resolution. visibleNodeRefs controls
// whether the candidate is still discoverable with its evidence.
func productionShapedGraphContext(citations []string, visibleNodeRefs []string) GraphContext {
	return GraphContext{
		Resolution: SubjectResolution{
			Candidates: []SubjectCandidate{reuseDegradeCandidate(visibleNodeRefs...)},
			Committed:  []SubjectRef{reuseDegradeSubject()},
		},
		Paths:            []RelationshipPath{},
		DriverCandidates: []DriverJudgment{},
		EvidenceRefIDs:   append([]string{}, citations...),
	}
}

func reuseDegradeEngine(t *testing.T, stored InvestigationResult, graphContext GraphContext, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	return mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{reuseDegradeSubject()}},
			context:    graphContext,
		},
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return stored, true, nil
		}),
	})
}

// TestReuseServesAWarmIdenticalRepeatWhenEveryRefIsStillVisible is test
// (a): the case that is RED on the parent.
//
// Nothing has changed between the two asks. The stored answer's citation
// is still discoverable and so is its candidate's node ref. On the parent
// this still MISSES, because the visible set is GraphContext.EvidenceRefIDs
// alone -- the edge closure -- which structurally cannot contain a node
// ref. That is the whole of the measured 0/8: not a cap, not ranking, not
// staleness, just two sets collected from different places.
func TestReuseServesAWarmIdenticalRepeatWhenEveryRefIsStillVisible(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	telemetry := &recordingTelemetry{}
	engine := reuseDegradeEngine(t, stored,
		productionShapedGraphContext([]string{reuseCitationRef}, []string{reuseNodeRef}), telemetry)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want true: an identical warm repeat with every ref still visible must be served from the store")
	}
	if got := lastReuseOutcome(t, telemetry); got != AnswerReuseHit {
		t.Fatalf("reuse outcome = %q, want %q -- nothing was missing, so nothing should have been degraded", got, AnswerReuseHit)
	}
	// The served payload is the stored one, unchanged.
	if len(result.SubjectResolution.Candidates) != 1 || len(result.SubjectResolution.Candidates[0].EvidenceRefIDs) != 1 {
		t.Fatalf("a clean hit must serve the stored payload untouched; candidates = %+v", result.SubjectResolution.Candidates)
	}
}

// TestReuseDegradesWhenOnlyAnAuxiliaryRefIsNoLongerVisible is test (b).
//
// The candidate's node ref is gone; the answer's own citation is not. The
// ruled remedy serves the answer with the unverifiable ref REMOVED and
// discloses the narrowing, rather than refusing outright.
func TestReuseDegradesWhenOnlyAnAuxiliaryRefIsNoLongerVisible(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	telemetry := &recordingTelemetry{}
	engine := reuseDegradeEngine(t, stored,
		productionShapedGraphContext([]string{reuseCitationRef}, nil), telemetry)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want true: a partial miss degrades, it does not refuse")
	}
	if got := lastReuseOutcome(t, telemetry); got != AnswerReuseHitDegraded {
		t.Fatalf("reuse outcome = %q, want %q", got, AnswerReuseHitDegraded)
	}

	// THE INVARIANT: the ref that could not be proven is not served.
	for _, ref := range collectEvidenceRefs(resultEvidenceSurface(result)) {
		if ref == reuseNodeRef {
			t.Fatalf("served payload still carries %q, a reference the recheck could not prove visible", reuseNodeRef)
		}
	}
	// The citation the recheck DID prove is still served -- degrading must
	// not quietly cost the caller evidence that is still visible.
	if !containsRef(result.EvidenceRefIDs, reuseCitationRef) {
		t.Fatalf("served payload lost the still-visible citation %q; evidence_ref_ids = %v", reuseCitationRef, result.EvidenceRefIDs)
	}

	// Disclosure: coverage says so, structurally and in words.
	if !result.Coverage.Partial {
		t.Error("coverage.partial = false; an answer narrower than the one stored is partial")
	}
	if !hasCoverageDetailCode(result.Coverage, contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped) {
		t.Errorf("coverage carries no %q detail; details = %+v", contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped, result.Coverage.Details)
	}
	if len(result.Coverage.DegradedReasons) == 0 {
		t.Error("coverage.degraded_reasons is empty; the narrowing must reach a consumer that reads only the legacy strings")
	}

	// Telemetry: the strip is counted, not merely implied by an outcome
	// label.
	if len(telemetry.answerReuseContainment) != 1 {
		t.Fatalf("containment events = %d, want 1", len(telemetry.answerReuseContainment))
	}
	event := telemetry.answerReuseContainment[0]
	if event.StrippedRefs != 1 || event.MissingCount != 1 || event.MissingCitation {
		t.Errorf("containment event = %+v, want 1 stripped, 1 missing, no missing citation", event)
	}
	if event.DemandedCount != 2 {
		t.Errorf("containment event demanded %d refs, want 2 (one citation + one candidate node ref)", event.DemandedCount)
	}
}

// TestReuseRefusesWhenATopLevelCitationIsNoLongerVisible is test (c): the
// behaviour this change deliberately does NOT relax.
//
// An answer whose own cited evidence has become invisible is not a
// narrowed answer, it is a different answer. It must still refuse, and it
// must still refuse with the containment outcome so the refusal stays
// readable in the same telemetry stream as before.
func TestReuseRefusesWhenATopLevelCitationIsNoLongerVisible(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{reuseDegradeSubject()}},
			context:    productionShapedGraphContext(nil, []string{reuseNodeRef}),
		},
	})

	verdict := engine.reuseAuthorizationStillHolds(context.Background(), reusePrincipal(),
		validInvestigationRequest(), stored, ResolvedGraphBinding{GraphKey: "stub-key", Epoch: 0})

	if !verdict.Refused {
		t.Fatal("recheck did not refuse; a stored answer whose own cited evidence is no longer visible must never be served")
	}
	if verdict.Outcome != AnswerReuseMissEvidenceContainment {
		t.Fatalf("outcome = %q, want %q -- the refusal must stay readable as a containment refusal", verdict.Outcome, AnswerReuseMissEvidenceContainment)
	}
	if !verdict.Partition.MissingCitation {
		t.Fatal("verdict does not report a missing citation, so the refusal happened for the wrong reason")
	}
	if !verdict.ContainmentRan {
		t.Fatal("verdict reports the containment leg did not run; the refusal must come from the evidence check, not an earlier one")
	}
}

// TestServedReusedPayloadNeverCarriesARefThatFailedTheRecheck is test (d):
// THE INVARIANT, as a property over random miss subsets.
//
// It is asserted by re-collecting the demanded set FROM THE SERVED PAYLOAD
// with the same collector that built the obligation in the first place.
// That is deliberate: a checklist of "did we strip candidates, members,
// drivers..." can only ever cover the sites whoever wrote it thought of,
// and is silently wrong the day a ref-bearing field is added. Asserting
// with the shared traversal makes the test as complete as the collector
// is, and the census test below makes the collector as complete as the
// type is.
func TestServedReusedPayloadNeverCarriesARefThatFailedTheRecheck(t *testing.T) {
	t.Parallel()

	// TWO label shapes, not one. The first version of this test always
	// built a COMPLETE label map, so it never drove an under-labelled
	// stored row -- which stored validation legitimately accepts -- through
	// a partial recheck, and a reviewer found a real defect in exactly that
	// gap. A property that samples only well-formed inputs is a property
	// about well-formed inputs.
	for _, shape := range []struct {
		name  string
		build func(*testing.T) InvestigationResult
	}{
		{"complete label map", storedResultWithEveryRefSite},
		{"under-labelled legacy label map", storedResultWithUnderLabelledMap},
	} {
		t.Run(shape.name, func(t *testing.T) {
			servedReusedPayloadNeverCarriesAFailedRef(t, shape.build(t))
		})
	}
}

func servedReusedPayloadNeverCarriesAFailedRef(t *testing.T, stored InvestigationResult) {
	t.Helper()
	auxiliary := auxiliaryRefsOf(stored)
	if len(auxiliary) < 4 {
		t.Fatalf("fixture carries only %d auxiliary refs; the property needs a set worth sampling from", len(auxiliary))
	}

	random := rand.New(rand.NewSource(0xC4A05_4831))
	served, refused := 0, 0
	for trial := 0; trial < 200; trial++ {
		missing := map[string]struct{}{}
		for _, ref := range auxiliary {
			if random.Intn(2) == 0 {
				missing[ref] = struct{}{}
			}
		}
		if len(missing) == 0 {
			continue
		}

		degraded, counts, _, ok := degradeReusedResult(stored, missing)
		if !ok {
			// A refusal is invariant-safe on its own -- nothing is served
			// at all -- so it is not a failure here. It is counted,
			// because a version of this code that refused EVERY subset
			// would make every assertion below unreachable and this test
			// would pass while proving nothing. That is not hypothetical:
			// removing the display-label rebuild sends every trial down
			// this branch.
			refused++
			continue
		}
		served++
		for _, ref := range collectEvidenceRefs(resultEvidenceSurface(degraded)) {
			if _, gone := missing[ref]; gone {
				t.Fatalf("trial %d: served payload carries %q, which failed the recheck (missing set %v)", trial, ref, sortedRefs(missing))
			}
		}
		if counts.empty() {
			t.Fatalf("trial %d: %d refs were missing but nothing was reported as removed", trial, len(missing))
		}
		// Every component is a COUNT OF REMOVALS and can never be
		// negative. A negative one would cancel a real removal inside any
		// aggregate and silently suppress the disclosure below.
		for label, value := range map[string]int{
			"Refs": counts.Refs(), "DroppedCandidates": counts.DroppedCandidates,
			"DroppedMembers": counts.DroppedMembers, "DroppedDrivers": counts.DroppedDrivers,
			"DroppedFindings": counts.DroppedFindings, "DroppedPaths": counts.DroppedPaths,
			"StrippedLabels": counts.StrippedLabels,
		} {
			if value < 0 {
				t.Fatalf("trial %d: %s = %d; a removal count must never be negative", trial, label, value)
			}
		}
		// The removal set must be EXACTLY the references that left the
		// payload: everything the stored result carried and the served one
		// does not.
		//
		// An earlier version of this assertion bounded the count by
		// len(missing) and was wrong — this test caught it. A reference
		// can leave the payload without ever being missing: when stripping
		// empties an object the contract requires to carry evidence, the
		// whole object is dropped, and the OTHER references it carried go
		// with it. Those are real losses to disclose, so the honest bound
		// is not "how many were unprovable" but "how many actually went
		// away".
		//
		// Stated as set equality it catches both directions at once: a
		// per-carrier tally over-reports, and a strip that forgets a site
		// under-reports.
		servedRefs := map[string]struct{}{}
		for _, ref := range collectEvidenceRefs(resultEvidenceSurface(degraded)) {
			servedRefs[ref] = struct{}{}
		}
		departed := map[string]struct{}{}
		for _, ref := range collectEvidenceRefs(resultEvidenceSurface(stored)) {
			if _, stillServed := servedRefs[ref]; !stillServed {
				departed[ref] = struct{}{}
			}
		}
		if counts.Refs() != len(departed) {
			t.Fatalf("trial %d: reported %d references removed, but %d actually left the payload (missing set was %d)",
				trial, counts.Refs(), len(departed), len(missing))
		}
		for ref := range departed {
			if _, recorded := counts.RemovedRefs[ref]; !recorded {
				t.Fatalf("trial %d: %q left the served payload but is not in the removal set, so it is not disclosed", trial, ref)
			}
		}
		// Anything removed MUST be disclosed. This is the caller-facing
		// half of the invariant: an answer narrower than the one stored,
		// served as whole, is its own defect even when nothing unproven
		// is served.
		if !degraded.Coverage.Partial {
			t.Fatalf("trial %d: %d removals were made and coverage.partial is false; the narrowing was not disclosed", trial, counts.Total())
		}
		if len(degraded.Coverage.DegradedReasons) == 0 {
			t.Fatalf("trial %d: %d removals were made and degraded_reasons is empty", trial, counts.Total())
		}
		if err := ValidateStoredResult(degraded); err != nil {
			t.Fatalf("trial %d: degraded payload does not validate: %v", trial, err)
		}
	}
	if served == 0 {
		t.Fatalf("every one of %d trials refused; the invariant assertions above never ran, so this test proved nothing about what gets served", refused)
	}
	if served*4 < refused {
		t.Errorf("only %d of %d trials degraded (%d refused); the degrade is meant to be the ORDINARY outcome of a partial miss, and a refusal rate this high means the assertions are sampling a narrow corner", served, served+refused, refused)
	}
}

// TestRecheckIsOrderStableGivenAFixedInterpretation is the determinism
// half of this work, and it is deliberately a DISPROOF.
//
// The sibling defect was reported as "the auxiliary reference set is
// run-varying on an identical question", with map-iteration ties, time
// windows and cap ordering named as the suspects -- all of which would
// live here, in acr's own consumer. Reading the stored rows showed
// otherwise: two rows written by the SAME binary for the same question
// differed because their model-authored INTERPRETATIONS differed (one
// carried subject_terms, the other did not), which is upstream of every
// ordering surface in this package. The remedy for that is the canonical
// subject expression, and it is tracked on its own ticket.
//
// This test is what makes that attribution safe to act on: it pins the
// claim the diagnosis rests on. Given a fixed interpretation and a fixed
// discovery, everything this package derives -- what is demanded, what is
// missing, and the exact payload that gets served -- is identical run to
// run and independent of the order the discovery happens to present its
// candidates in. If this ever goes red, the attribution is wrong and the
// nondeterminism IS here after all.
//
// It is not vacuous: the collector, the partition and the strip all build
// and read maps, and Go randomizes map iteration on every run.
func TestRecheckIsOrderStableGivenAFixedInterpretation(t *testing.T) {
	t.Parallel()

	stored := storedResultWithEveryRefSite(t)
	// A discovery that proves SOME of the demanded references, so the
	// missing set is non-trivial and the degrade actually runs.
	discovery := GraphContext{
		Resolution: SubjectResolution{
			Candidates: []SubjectCandidate{
				reuseDegradeCandidateNamed("receipt_degrade_a1", "evidence_candidate_a"),
				reuseDegradeCandidateNamed("receipt_degrade_b1", "evidence_candidate_b"),
			},
			Committed: []SubjectRef{reuseDegradeSubject()},
		},
		Paths:            []RelationshipPath{},
		DriverCandidates: []DriverJudgment{},
		EvidenceRefIDs:   []string{reuseCitationRef, "evidence_path_a"},
	}

	var firstDemanded, firstMissing, firstServed string
	for run := 0; run < 50; run++ {
		shuffled := discovery
		if run%2 == 1 {
			// Same SET, different presentation order.
			shuffled.Resolution.Candidates = []SubjectCandidate{
				discovery.Resolution.Candidates[1], discovery.Resolution.Candidates[0],
			}
		}
		partition := partitionMissingRefs(stored, shuffled)
		degraded, _, _, ok := degradeReusedResult(stored, partition.Missing)
		if !ok {
			t.Fatalf("run %d: degrade refused; the fixture is meant to degrade", run)
		}

		demanded := strings.Join(partition.Demanded, ",")
		missing := sortedRefs(partition.Missing)
		served := strings.Join(collectEvidenceRefs(resultEvidenceSurface(degraded)), ",")

		if run == 0 {
			firstDemanded, firstMissing, firstServed = demanded, missing, served
			continue
		}
		if demanded != firstDemanded {
			t.Fatalf("run %d: demanded set is not stable\nfirst: %s\n  now: %s", run, firstDemanded, demanded)
		}
		if missing != firstMissing {
			t.Fatalf("run %d: missing set is not stable\nfirst: %s\n  now: %s", run, firstMissing, missing)
		}
		if served != firstServed {
			t.Fatalf("run %d: served payload is not stable\nfirst: %s\n  now: %s", run, firstServed, served)
		}
	}
	if firstMissing == "" {
		t.Fatal("nothing was missing in any run; this fixture proves stability over an empty set, which proves nothing")
	}
}

// TestDegradeNeverMutatesTheStoredCandidate guards the sharpest way this
// could go wrong in production without any test noticing: the reused
// candidate is a shallow value copy, so a strip that wrote through a
// shared slice would corrupt whatever else holds that result -- including,
// for an in-memory store, the stored row itself.
func TestDegradeNeverMutatesTheStoredCandidate(t *testing.T) {
	t.Parallel()

	stored := storedResultWithEveryRefSite(t)
	before := collectEvidenceRefs(resultEvidenceSurface(stored))

	missing := map[string]struct{}{}
	for _, ref := range auxiliaryRefsOf(stored) {
		missing[ref] = struct{}{}
	}
	if _, _, _, ok := degradeReusedResult(stored, missing); !ok {
		t.Fatal("degradeReusedResult() refused; this fixture is meant to degrade")
	}

	after := collectEvidenceRefs(resultEvidenceSurface(stored))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the stored candidate was mutated by the degrade\nbefore: %v\n after: %v", before, after)
	}
}

// TestEvidenceRefCollectorVisitsEveryRefBearingFieldOfAResult is the
// census that keeps the invariant honest as the type grows.
//
// The property test above is only as complete as collectEvidenceRefs. If
// someone adds a ref-bearing field to InvestigationResult and does not add
// it to evidenceRefSurface, the property test keeps passing while the
// degrade quietly serves unverified references from the new field. This
// walks the real type by reflection and fails on any evidence-ref field
// the collector does not reach.
func TestEvidenceRefCollectorVisitsEveryRefBearingFieldOfAResult(t *testing.T) {
	t.Parallel()

	paths := evidenceRefFieldPaths(reflect.TypeOf(InvestigationResult{}), "InvestigationResult", map[reflect.Type]bool{})
	if len(paths) == 0 {
		t.Fatal("census found no evidence-ref fields at all; the walk is broken, not the type")
	}
	for _, path := range paths {
		if _, known := censusCoveredRefPaths[path]; !known {
			t.Errorf("evidence-ref field %s is not accounted for.\nEither add it to evidenceRefSurface/collectEvidenceRefs (so it is rechecked AND stripped), or add it to censusCoveredRefPaths with a comment saying why it can never be served to a caller.", path)
		}
	}
	for path := range censusCoveredRefPaths {
		if !containsRef(paths, path) {
			t.Errorf("censusCoveredRefPaths names %s, which the type no longer has; remove the stale entry rather than leaving the census claiming coverage it cannot have", path)
		}
	}
}

// censusCoveredRefPaths is the reviewed list of every evidence-ref field
// on InvestigationResult, with what happens to it. "collected" means
// collectEvidenceRefs reaches it, so it is both rechecked and stripped.
var censusCoveredRefPaths = map[string]string{
	"InvestigationResult.EvidenceRefIDs":                                "collected (top-level citations; a missing one refuses reuse)",
	"InvestigationResult.SubjectResolution.Candidates[].EvidenceRefIDs": "collected (the class that made containment unsatisfiable)",
	"InvestigationResult.Cohort.Members[].EvidenceRefIDs":               "collected",
	"InvestigationResult.Drivers[].EvidenceRefIDs":                      "collected",
	"InvestigationResult.RemainingWork[].EvidenceRefIDs":                "collected",
	"InvestigationResult.ReadinessGaps[].EvidenceRefIDs":                "collected",
	"InvestigationResult.Conflicts[].EvidenceRefIDs":                    "collected",
	"InvestigationResult.Paths[].EvidenceRefIDs":                        "collected",
	"InvestigationResult.Paths[].Edges[].EvidenceRefIDs":                "collected (a path may cite evidence ONLY at edge level)",
	"InvestigationResult.EvidenceRefLabels":                             "collected, and REBUILT from the served closure after the strip",
}

// refCarrierCensus is what a walk of a type reports: the evidence-ref
// CARRIERS it found, and every composite reflect.Kind it declined to
// descend into.
//
// SkippedKinds is the part that matters. The first version of this walker
// handled structs, pointers and slices and silently ignored maps -- so it
// reported "every carrier accounted for" while a map[string]string keyed by
// evidence ref sat one field away, and a reviewer constructed the leak
// through it. A census that cannot report its own blind spots is not a
// census; it is a claim. Recording skipped kinds is what makes the blind
// spot assertable.
type refCarrierCensus struct {
	Paths        []string
	SkippedKinds map[reflect.Kind]string
}

// evidenceRefCarrierCensus walks a type and reports every field that can
// CARRY evidence reference strings -- a []string, a map keyed or valued by
// string, or a bare string -- whose name marks it as an evidence-ref field,
// descending through structs, pointers, slices, arrays and maps.
//
// Detection is by field NAME (any field mentioning "EvidenceRef") rather
// than by type, deliberately: the question this test asks is "is there a
// place a reference can reach a caller that the collector does not walk",
// and the type alone cannot answer it -- map[string]string is both a
// label map keyed by references and a perfectly ordinary dictionary.
func evidenceRefCarrierCensus(t reflect.Type, prefix string, seen map[reflect.Type]bool, census *refCarrierCensus) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	if seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		path := prefix + "." + field.Name
		fieldType := field.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		if strings.Contains(field.Name, "EvidenceRef") && carriesRefStrings(fieldType) {
			census.Paths = append(census.Paths, path)
			continue
		}
		switch fieldType.Kind() {
		case reflect.Slice, reflect.Array:
			evidenceRefCarrierCensus(fieldType.Elem(), path+"[]", seen, census)
		case reflect.Map:
			// Keys cannot be structs worth walking in this contract, but
			// values can be, and BOTH are reachable by a caller.
			evidenceRefCarrierCensus(fieldType.Key(), path+"{key}", seen, census)
			evidenceRefCarrierCensus(fieldType.Elem(), path+"{}", seen, census)
		case reflect.Struct:
			evidenceRefCarrierCensus(fieldType, path, seen, census)
		case reflect.Interface, reflect.Chan, reflect.Func, reflect.UnsafePointer:
			// Genuinely opaque to a static walk. Recorded, not ignored:
			// if the contract ever grows one of these, this census must
			// stop claiming completeness until someone rules on it.
			census.SkippedKinds[fieldType.Kind()] = path
		}
	}
}

// carriesRefStrings reports whether a type can hold evidence reference
// strings in any position a caller can read.
func carriesRefStrings(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String:
		return true
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() == reflect.String
	case reflect.Map:
		return t.Key().Kind() == reflect.String || t.Elem().Kind() == reflect.String
	default:
		return false
	}
}

func evidenceRefFieldPaths(t reflect.Type, prefix string, seen map[reflect.Type]bool) []string {
	census := refCarrierCensus{SkippedKinds: map[reflect.Kind]string{}}
	evidenceRefCarrierCensus(t, prefix, seen, &census)
	return census.Paths
}

// --- fixtures and small helpers -------------------------------------------

// storedResultWithEveryRefSite builds a stored result carrying a distinct
// evidence ref at every site the collector walks, so a property trial can
// remove any combination of them.
func storedResultWithEveryRefSite(t *testing.T) InvestigationResult {
	t.Helper()
	subject := reuseDegradeSubject()
	result := validInvestigationResult()
	result.ResultID = "result_every_site1"
	result.RequestID = "request_every_st1"
	result.EvidenceRefIDs = []string{reuseCitationRef}
	result.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{
			reuseDegradeCandidateNamed("receipt_degrade_a1", "evidence_candidate_a"),
			reuseDegradeCandidateNamed("receipt_degrade_b1", "evidence_candidate_b"),
		},
		Committed: []SubjectRef{subject},
	}
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_degrade_001", Standing: contractsv1.ContextFabricDriverPrincipal,
		Category:         string(contractsv1.ContextFabricDriverCategoryRelationship),
		Title:            "Review latency is the binding constraint",
		Summary:          "Review turnaround dominates the elapsed time on this project.",
		AffectedSubjects: []SubjectRef{subject}, Derivation: DerivationGraphAssociated,
		EpistemicStatus: EpistemicInferred, Confidence: 0.7,
		EvidenceRefIDs: []string{"evidence_driver_a"},
	}}
	result.RemainingWork = []Finding{{
		FindingID: "finding_degrade_01", Kind: string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary: "Acceptance work remains open.", Subjects: []SubjectRef{subject},
		EvidenceRefIDs: []string{"evidence_finding_a"},
	}}
	result.Paths = []RelationshipPath{{
		PathID: "11111111-2222-3333-4444-555555555555",
		Nodes:  []SubjectRef{subject, {Kind: SubjectRepository, CanonicalID: "repo_ask_dev", Label: "ask-dev"}},
		Edges: []RelationshipEdge{{
			Type: contractsv1.ContextFabricRelationshipBelongsToRepository, From: subject,
			To:         SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_ask_dev", Label: "ask-dev"},
			Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicInferred,
			EvidenceRefIDs: []string{"evidence_edge_a"},
		}},
		WhyRelevant: "The project contains the repository the question is about.",
		// ALIASED ON PURPOSE: "evidence_edge_a" is carried by BOTH this
		// path and its edge above. The earlier fixture gave every site a
		// distinct id, which is tidy and which structurally excluded the
		// one shape where a reference is removed at two carriers at once
		// -- so the property could not have caught a per-carrier
		// over-count. A fixture whose values are all distinct is a
		// fixture that cannot observe aliasing.
		EvidenceRefIDs: []string{"evidence_path_a", "evidence_edge_a"},
	}}
	// The display-label map, keyed by the result's own closure exactly as a
	// fresh result carries it. Without this the property test would never
	// exercise the carrier that leaked.
	result.EvidenceRefLabels = map[string]string{}
	for ref := range contractsv1.ContextFabricEvidenceRefClosure(result) {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		result.EvidenceRefLabels[ref] = label
	}
	result.Completeness = ComputeAnswerCompleteness(result)
	if err := ValidateStoredResult(result); err != nil {
		t.Fatalf("fixture does not validate as a stored result: %v", err)
	}
	return result
}

// auxiliaryRefsOf is every ref the fixture carries EXCEPT the top-level
// citations -- the set a partial miss may legally sample from. A missing
// citation refuses, which is a different test.
func auxiliaryRefsOf(result InvestigationResult) []string {
	citations := map[string]struct{}{}
	for _, ref := range result.EvidenceRefIDs {
		citations[ref] = struct{}{}
	}
	var auxiliary []string
	for _, ref := range collectEvidenceRefs(resultEvidenceSurface(result)) {
		if _, isCitation := citations[ref]; !isCitation {
			auxiliary = append(auxiliary, ref)
		}
	}
	return auxiliary
}

func lastReuseOutcome(t *testing.T, telemetry *recordingTelemetry) AnswerReuseOutcome {
	t.Helper()
	if len(telemetry.answerReuseOutcomes) == 0 {
		t.Fatal("no answer-reuse outcome was recorded")
	}
	return telemetry.answerReuseOutcomes[len(telemetry.answerReuseOutcomes)-1]
}

func hasCoverageDetailCode(coverage Coverage, code contractsv1.ContextFabricCoverageDetailCode) bool {
	for _, detail := range coverage.Details {
		if detail.Code == code {
			return true
		}
	}
	return false
}

func containsRef(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// sortedRefs renders a ref set in a canonical order. Sorting is the point:
// the caller is asserting that the SET is stable, and map iteration order
// is not part of that claim.
func sortedRefs(set map[string]struct{}) string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// --- the census's own completeness, and the closure parity it rests on ---

// censusProbeNested is reached only through a POINTER field, so a walker
// that does not dereference pointers reports nothing for it.
type censusProbeNested struct {
	NestedEvidenceRefIDs []string
}

// censusProbe carries an evidence reference in each of the three shapes a
// walker can plausibly miss: a map (the shape that actually leaked), a
// slice, and a struct reached through a pointer.
type censusProbe struct {
	EvidenceRefLabels map[string]string
	EvidenceRefIDs    []string
	Nested            *censusProbeNested
	Unrelated         map[string]int
}

// TestCensusWalkerReportsEveryCarrierShapeAndAdmitsWhatItSkips is the guard
// on the guard.
//
// The census exists so the invariant property test stays as complete as the
// result type. That only holds if the census itself is complete, and the
// first version was not: it walked structs, pointers and slices, ignored
// maps without saying so, and therefore reported full coverage of a type
// whose label map it had never looked at. A reviewer built the leak through
// exactly that gap.
//
// So this test does two things a "does it find the fields" test would not.
// It drives all three carrier shapes through a synthetic type, and it
// asserts the walker reports NOTHING as skipped for a type built only from
// kinds it claims to handle. A future walker that quietly stops descending
// into some kind fails here rather than going on to under-report in
// silence.
func TestCensusWalkerReportsEveryCarrierShapeAndAdmitsWhatItSkips(t *testing.T) {
	t.Parallel()

	census := refCarrierCensus{SkippedKinds: map[reflect.Kind]string{}}
	evidenceRefCarrierCensus(reflect.TypeOf(censusProbe{}), "censusProbe", map[reflect.Type]bool{}, &census)

	want := []string{
		"censusProbe.EvidenceRefLabels",
		"censusProbe.EvidenceRefIDs",
		"censusProbe.Nested.NestedEvidenceRefIDs",
	}
	for _, path := range want {
		if !containsRef(census.Paths, path) {
			t.Errorf("census missed the carrier %s; found %v", path, census.Paths)
		}
	}
	if len(census.Paths) != len(want) {
		t.Errorf("census reported %v, want exactly %v -- an unrelated map must not be counted as a carrier", census.Paths, want)
	}
	if len(census.SkippedKinds) != 0 {
		t.Errorf("census skipped %v on a type built only from kinds it claims to handle", census.SkippedKinds)
	}

	// And the same admission for the type that actually matters.
	real := refCarrierCensus{SkippedKinds: map[reflect.Kind]string{}}
	evidenceRefCarrierCensus(reflect.TypeOf(InvestigationResult{}), "InvestigationResult", map[reflect.Type]bool{}, &real)
	if len(real.SkippedKinds) != 0 {
		t.Fatalf("the census cannot see %v on the result type; the invariant property test is only as complete as this walk, so resolve it rather than leaving the census claiming coverage it lacks", real.SkippedKinds)
	}
}

// TestCollectorAgreesWithTheContractsEvidenceRefClosure keeps two walks of
// the same thing from drifting.
//
// The contract already owns a canonical evidence-ref closure, and it is
// what the display-label map and the write-path validator are built from.
// collectEvidenceRefs is necessarily a second walk -- it also has to run
// over a GraphContext, which the contract's function cannot take -- and two
// copies of one traversal is the shape that produced this whole defect in
// the first place. Rather than leave them to drift, this pins them equal on
// a result carrying a reference at every site.
func TestCollectorAgreesWithTheContractsEvidenceRefClosure(t *testing.T) {
	t.Parallel()

	result := storedResultWithEveryRefSite(t)
	closure := contractsv1.ContextFabricEvidenceRefClosure(result)

	collected := map[string]struct{}{}
	for _, ref := range collectEvidenceRefs(resultEvidenceSurface(result)) {
		collected[ref] = struct{}{}
	}
	// The collector is a SUPERSET by design: it also walks the label map,
	// which the contract closure does not (the closure is what the label
	// map is derived FROM). Every contract-closure ref must be collected.
	for ref := range closure {
		if _, ok := collected[ref]; !ok {
			t.Errorf("the contract's closure carries %q and the collector does not; the recheck would never demand it and the strip would never remove it", ref)
		}
	}
	for ref := range collected {
		if _, ok := closure[ref]; ok {
			continue
		}
		if _, labelled := result.EvidenceRefLabels[ref]; !labelled {
			t.Errorf("the collector carries %q, which is neither in the contract's closure nor in the label map; one of the two walks has grown a site the other has not", ref)
		}
	}
}

// TestAStrippedRefIsNeverLeftBehindInTheDisplayLabelMap is the permanent
// form of a review finding: the first version of this change removed a
// reference from every list that carried it and left it as a KEY in
// `evidence_ref_labels`, with its human-readable label attached. The caller
// was served both the identifier and a description of a reference the
// recheck had refused.
//
// It is written against the map specifically, rather than folded into the
// property test, because the property test would have caught it only once
// the collector walked maps -- and the whole point is that it did not.
func TestAStrippedRefIsNeverLeftBehindInTheDisplayLabelMap(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.EvidenceRefLabels = map[string]string{
		reuseCitationRef: "Citation",
		reuseNodeRef:     "Candidate evidence",
	}

	degraded, counts, _, ok := degradeReusedResult(stored, map[string]struct{}{reuseNodeRef: {}})
	if !ok {
		t.Fatal("degrade refused; this fixture is meant to degrade")
	}
	if label, present := degraded.EvidenceRefLabels[reuseNodeRef]; present {
		t.Fatalf("served payload still exposes the stripped ref %q through evidence_ref_labels (label %q)", reuseNodeRef, label)
	}
	if _, present := degraded.EvidenceRefLabels[reuseCitationRef]; !present {
		t.Error("the still-visible citation lost its label; the rebuild must keep everything the served closure carries")
	}
	if counts.StrippedLabels != 1 {
		t.Errorf("StrippedLabels = %d, want 1 -- a label removal the caller is not told about is an undisclosed narrowing", counts.StrippedLabels)
	}
}

// TestDegradeRefusesRatherThanServeALabelMapWiderThanItsClosure pins the
// serve-path enforcement of a rule the contract enforces only on writes.
//
// Stored results are immutable and read back under looser bounds, so the
// shared stored validator cannot be tightened without rejecting legacy rows
// that are nobody's leak. The degrade is the only thing that can turn a
// well-formed stored payload into one whose label map outruns its closure,
// so the obligation lives with the degrade. If the rebuild is ever changed
// in a way that leaves an orphan key, reuse refuses instead of serving it.
func TestDegradeRefusesRatherThanServeALabelMapWiderThanItsClosure(t *testing.T) {
	t.Parallel()

	// A payload in the exact state a broken rebuild would leave: the
	// candidate's reference is gone from every list, and its label key is
	// still there.
	served := storedResultWithCandidateEvidence()
	served.SubjectResolution.Candidates = []SubjectCandidate{reuseDegradeCandidate()}
	served.EvidenceRefLabels = map[string]string{
		reuseCitationRef: "Citation",
		reuseNodeRef:     "Candidate evidence",
	}
	if servedLabelsAreWithinTheClosure(served) {
		t.Fatal("guard accepted a label map naming a ref the payload does not carry; it would not have caught the leak it exists for")
	}

	// And the ordinary direction is explicitly NOT refused: an
	// under-labelled legacy row discloses less than it could, which is not
	// an authorization problem and must not cost the caller a reuse.
	underLabelled := storedResultWithCandidateEvidence()
	underLabelled.EvidenceRefLabels = map[string]string{reuseCitationRef: "Citation"}
	if !servedLabelsAreWithinTheClosure(underLabelled) {
		t.Error("guard refused an under-labelled payload; a missing label is not a leak")
	}
}

// storedResultWithUnderLabelledMap is a stored row whose label map covers
// only the top-level citation, leaving every auxiliary reference
// unlabelled.
//
// This is a LEGAL stored shape, not a corrupt one: exact label/closure
// equality is enforced on writes, and stored reads deliberately do not
// re-enforce it, because stored results are immutable and an older binary
// may have written a narrower map. So this is what a real legacy row looks
// like, and the degrade has to behave correctly on it.
func storedResultWithUnderLabelledMap(t *testing.T) InvestigationResult {
	t.Helper()
	result := storedResultWithEveryRefSite(t)
	labels := map[string]string{}
	for _, ref := range result.EvidenceRefIDs {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		labels[ref] = label
	}
	result.EvidenceRefLabels = labels
	if err := ValidateStoredResult(result); err != nil {
		t.Fatalf("an under-labelled stored row must be a legal stored shape, but: %v", err)
	}
	return result
}

// TestAnUnderLabelledStoredRowStillDisclosesItsNarrowing is the permanent
// form of the second review finding.
//
// The label-map rebuild can legitimately GROW the map: an under-labelled
// legacy row gets its missing labels supplied. The removal count was
// computed as a difference of map lengths, so on that input it went
// NEGATIVE — and the negative did not stay confined to telemetry. Summed
// into the total, it CANCELLED a real reference removal, the total read as
// zero, the disclosure branch returned early, and a genuinely narrowed
// answer was served as a degraded hit with `coverage.partial` false and no
// degraded reason at all.
//
// That is the shape worth remembering: an aggregate that can cancel, gating
// a disclosure. The count now counts removals directly and can only be
// non-negative, and emptiness is decided component by component so no
// future signed field can cancel one either.
func TestAnUnderLabelledStoredRowStillDisclosesItsNarrowing(t *testing.T) {
	t.Parallel()

	stored := storedResultWithUnderLabelledMap(t)
	// Exactly one auxiliary reference goes missing. The old arithmetic
	// scored this as Refs=1, StrippedLabels=-1, total 0.
	auxiliary := auxiliaryRefsOf(stored)
	if len(auxiliary) == 0 {
		t.Fatal("fixture carries no auxiliary refs")
	}
	missing := map[string]struct{}{auxiliary[0]: {}}

	degraded, counts, _, ok := degradeReusedResult(stored, missing)
	if !ok {
		t.Fatal("degrade refused; an under-labelled stored row is legal and must still degrade")
	}
	if counts.StrippedLabels < 0 {
		t.Errorf("StrippedLabels = %d; a removal count must never be negative", counts.StrippedLabels)
	}
	if counts.Refs() != 1 {
		t.Errorf("Refs = %d, want 1", counts.Refs())
	}
	if counts.empty() {
		t.Fatalf("a reference was removed but the counts read as empty: %+v", counts)
	}
	if !degraded.Coverage.Partial {
		t.Error("coverage.partial = false; a narrowed answer served as whole is its own defect, even when nothing unproven is served")
	}
	if len(degraded.Coverage.DegradedReasons) == 0 {
		t.Error("degraded_reasons is empty; the caller is not told the answer was narrowed")
	}
	if !hasCoverageDetailCode(degraded.Coverage, contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped) {
		t.Error("no structured coverage detail for the narrowing")
	}
	// And the rebuild did what it is for: the map now covers the served
	// closure rather than staying stuck at its legacy width.
	if _, present := degraded.EvidenceRefLabels[auxiliary[0]]; present {
		t.Errorf("the removed ref %q is still labelled", auxiliary[0])
	}
	if !servedLabelsAreWithinTheClosure(degraded) {
		t.Error("the rebuilt label map is not within the served closure")
	}
}

// TestTheExactCancellingCaseStillDiscloses pins the HARM rather than its
// proxy.
//
// The sibling test above catches a negative removal count, which is the
// symptom. The defect was what the negative did downstream: summed into an
// aggregate it cancelled a real reference removal to exactly zero, the
// disclosure branch read that as "nothing was removed", and a narrowed
// answer was served with `coverage.partial` false and no reason.
//
// Cancellation to exactly zero needs the numbers to line up, so this builds
// them deliberately: ONE reference removed, and a label map whose rebuild
// grows by exactly one. A fixture that merely happens to be under-labelled
// produces a large negative that does not cancel, and would let the real
// defect through while looking like coverage of it.
func TestTheExactCancellingCaseStillDiscloses(t *testing.T) {
	t.Parallel()

	stillVisible := "evidence_candidate_a"
	nowMissing := "evidence_candidate_b"

	stored := storedResultWithEveryRefSite(t)
	// Trim to exactly the shape that cancels: two candidate refs, and a
	// label map covering only the citation, so the rebuild supplies one
	// missing label while one reference is removed.
	stored.Drivers = []DriverJudgment{}
	stored.RemainingWork = []Finding{}
	stored.Paths = []RelationshipPath{}
	stored.SubjectResolution.Candidates = []SubjectCandidate{
		reuseDegradeCandidateNamed("receipt_degrade_a1", stillVisible),
		reuseDegradeCandidateNamed("receipt_degrade_b1", nowMissing),
	}
	citation := stored.EvidenceRefIDs[0]
	label, _ := contractsv1.ContextFabricEvidenceRefLabel(citation)
	stored.EvidenceRefLabels = map[string]string{citation: label}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if err := ValidateStoredResult(stored); err != nil {
		t.Fatalf("fixture is not a legal stored row: %v", err)
	}

	degraded, counts, _, ok := degradeReusedResult(stored, map[string]struct{}{nowMissing: {}})
	if !ok {
		t.Fatal("degrade refused; this fixture is meant to degrade")
	}

	// The harm, asserted directly.
	if !degraded.Coverage.Partial {
		t.Errorf("coverage.partial = false after removing a reference; counts = %+v", counts)
	}
	if len(degraded.Coverage.DegradedReasons) == 0 {
		t.Errorf("degraded_reasons is empty after removing a reference; counts = %+v", counts)
	}
	if !hasCoverageDetailCode(degraded.Coverage, contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped) {
		t.Errorf("no structured coverage detail after removing a reference; counts = %+v", counts)
	}
	// And the numbers that produced the cancellation.
	if counts.Refs() != 1 {
		t.Errorf("Refs = %d, want 1", counts.Refs())
	}
	if counts.StrippedLabels != 0 {
		t.Errorf("StrippedLabels = %d, want 0 -- the removed ref was never labelled, so nothing was removed FROM the map", counts.StrippedLabels)
	}
	if counts.empty() {
		t.Error("counts read as empty after a real removal; this is the cancellation the disclosure branch used to trust")
	}
}

// TestARefRemovedAtTwoCarriersIsCountedOnce is the permanent form of the
// third review finding.
//
// One reference may legally ride at several carriers at once — a path's own
// list and one of its edges', a driver and a finding, a candidate and a
// cohort member. Each slice is validated on its own, and the contract's
// evidence closure deduplicates globally, so the shape is valid stored data
// rather than corruption.
//
// The strip counted per carrier. Removing one such reference reported TWO,
// and that number is not internal bookkeeping: it is what telemetry emits,
// what the structured coverage detail carries, and what the caller-facing
// sentence quotes. A caller was told two pieces of evidence had become
// invisible when one had.
//
// Not an authorization defect — nothing unproven was ever served. It is the
// same class as the cancelling-total finding above: the disclosure was
// wrong, this time overstating the loss.
func TestARefRemovedAtTwoCarriersIsCountedOnce(t *testing.T) {
	t.Parallel()

	const shared = "evidence_shared_across_two_carriers"

	stored := storedResultWithEveryRefSite(t)
	path := stored.Paths[0]
	path.EvidenceRefIDs = []string{shared}
	path.Edges[0].EvidenceRefIDs = []string{shared}
	stored.Paths = []RelationshipPath{path}
	stored.EvidenceRefLabels = map[string]string{}
	for ref := range contractsv1.ContextFabricEvidenceRefClosure(stored) {
		label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
		stored.EvidenceRefLabels[ref] = label
	}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if err := ValidateStoredResult(stored); err != nil {
		t.Fatalf("a reference carried at two sites must be a LEGAL stored shape, but: %v", err)
	}

	degraded, counts, _, ok := degradeReusedResult(stored, map[string]struct{}{shared: {}})
	if !ok {
		t.Fatalf("degrade refused; counts = %+v", counts)
	}

	// ONE reference id went away, however many carriers held it.
	if counts.Refs() != 1 {
		t.Errorf("Refs() = %d, want 1 — one reference id was removed, at two carriers", counts.Refs())
	}
	// The three places that number reaches someone.
	if got := coverageDetailCount(degraded.Coverage, contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped); got != 1 {
		t.Errorf("structured coverage Count = %d, want 1", got)
	}
	if reason := composeReuseNarrowingReason(counts); !strings.Contains(reason, "1 evidence reference(s)") {
		t.Errorf("caller-facing reason overstates the loss: %q", reason)
	}
	// And the reference really is gone from every carrier that held it.
	for _, ref := range collectEvidenceRefs(resultEvidenceSurface(degraded)) {
		if ref == shared {
			t.Fatalf("the served payload still carries %q", shared)
		}
	}
}

// coverageDetailCount returns the Count a coverage detail carries, or -1.
func coverageDetailCount(coverage Coverage, code contractsv1.ContextFabricCoverageDetailCode) int {
	for _, detail := range coverage.Details {
		if detail.Code == code && detail.Count != nil {
			return *detail.Count
		}
	}
	return -1
}
