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

	stored := storedResultWithEveryRefSite(t)
	auxiliary := auxiliaryRefsOf(stored)
	if len(auxiliary) < 4 {
		t.Fatalf("fixture carries only %d auxiliary refs; the property needs a set worth sampling from", len(auxiliary))
	}

	random := rand.New(rand.NewSource(0xC4A05_4831))
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
			// A refusal is always invariant-safe: nothing is served at
			// all. It is only interesting if it happens for EVERY subset,
			// which would mean the degrade never actually degrades.
			continue
		}
		for _, ref := range collectEvidenceRefs(resultEvidenceSurface(degraded)) {
			if _, gone := missing[ref]; gone {
				t.Fatalf("trial %d: served payload carries %q, which failed the recheck (missing set %v)", trial, ref, sortedRefs(missing))
			}
		}
		if counts.Total() == 0 {
			t.Fatalf("trial %d: %d refs were missing but nothing was reported as removed", trial, len(missing))
		}
		if err := ValidateStoredResult(degraded); err != nil {
			t.Fatalf("trial %d: degraded payload does not validate: %v", trial, err)
		}
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
}

// evidenceRefFieldPaths walks a type and returns a dotted path for every
// []string field named EvidenceRefIDs it can reach, with []T rendered as
// [] and pointers followed.
func evidenceRefFieldPaths(t reflect.Type, prefix string, seen map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	defer delete(seen, t)

	var found []string
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
		if fieldType.Kind() == reflect.Slice && fieldType.Elem().Kind() == reflect.String {
			if field.Name == "EvidenceRefIDs" {
				found = append(found, path)
			}
			continue
		}
		if fieldType.Kind() == reflect.Slice {
			found = append(found, evidenceRefFieldPaths(fieldType.Elem(), path+"[]", seen)...)
			continue
		}
		found = append(found, evidenceRefFieldPaths(fieldType, path, seen)...)
	}
	return found
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
		WhyRelevant:    "The project contains the repository the question is about.",
		EvidenceRefIDs: []string{"evidence_path_a"},
	}}
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
