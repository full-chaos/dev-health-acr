package graphrank

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjects_KindBoundaryRepairCausalFixture is the CHAOS-4183
// phase 3 (sol design consult, team-lead ratified 2026-08-23) validation
// step 1 causal unit fixture: a Shape-A reproduction end to end through
// ResolveSubjects, NOT merely projectKindOfferKinds in isolation.
//
// Two candidates share ONE ordinary Search term ("outage") -- a
// higher-confidence work_item and a lower-confidence pull_request --
// exactly like TestResolveSubjects_SearchKindOfferSurvivesFinalRankedTruncation
// above, but WITHOUT ever wiring SearchKind: pull_request is present ONLY
// because ordinary Search found it, then MaxSubjectCandidates=1 truncates
// it out of resolution.Candidates before either offer builder's own input
// (kindOfferCandidates) ever sees it. This is Shape A precisely --
// "present in the full merged pool, absent at the offer boundary via
// ranking/truncation, not a coverage-floor or retrieval-recall failure" --
// the re-smoke's own taxonomy this phase's remedy targets.
func TestResolveSubjects_KindBoundaryRepairCausalFixture(t *testing.T) {
	t.Parallel()
	// Two comparable-strength work_item candidates keep the final ranked
	// pair genuinely ambiguous (nothing auto-commits via the lone-floor
	// gate) while the weaker pull_request is the one MaxSubjectCandidates=2
	// truncates away -- a STALLED resolution, which this phase's own
	// "stalled resolutions ONLY" scope requires for the repair to fire at
	// all (projectKindOfferKinds skips repair whenever committedCount > 0).
	workItemA := candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item A", 0.95, "*")
	workItemB := candidateNode(contextfabric.SubjectWorkItem, "wi_2", "Outage work item B", 0.90, "*")
	weakPR := candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.5, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"outage": {workItemA, workItemB, weakPR}},
	}
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	request := testRequest()
	request.Options.MaxSubjectCandidates = 2
	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	// No coverage-floor SearchKind calls at all -- SearchKind was never
	// wired (deps.SearchKind is nil, "skipped silently" per
	// TestResolveSubjects_SearchKindNilIsSkippedSilently), so this repair
	// cannot be mistaken for CHAOS-4038's own mechanism.
	if len(backend.searchKindCalls) != 0 {
		t.Fatalf("searchKindCalls = %#v, want none -- this fixture proves the ranking/truncation-only Shape A path, not the coverage floor", backend.searchKindCalls)
	}

	// resolution.Candidates is byte-unchanged: MaxSubjectCandidates=2 still
	// caps the final ranked set to the two stronger work_item survivors,
	// exactly as it did before this phase existed -- pull_request never
	// enters the commit-decision axis at all.
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want exactly 2 (both work_item) -- the commit-decision axis must be untouched by this phase", resolution.Candidates)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != contextfabric.SubjectWorkItem {
			t.Fatalf("resolution.Candidates = %#v, want both work_item -- pull_request must not reach the commit-decision axis", resolution.Candidates)
		}
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none -- two comparable work_item candidates must stay ambiguous (stalled), the exact scope this phase's repair requires", resolution.Committed)
	}

	// CandidateOptions (candidate-list axis, top-5): byte-unchanged --
	// candidateOfferMaterial still reads the SAME kindOfferCandidates union
	// this phase leaves untouched, so it sees only the two truncated
	// work_item survivors, same as before this phase existed.
	if len(offer.CandidateOptions) != 2 {
		t.Fatalf("offer.CandidateOptions = %#v, want exactly the two work_item candidates, unchanged", offer.CandidateOptions)
	}
	for _, opt := range offer.CandidateOptions {
		if opt.Kind != contextfabric.SubjectWorkItem {
			t.Fatalf("offer.CandidateOptions = %#v, want both work_item -- the candidate-list axis must be untouched by this phase", offer.CandidateOptions)
		}
	}

	// The kind_offer trace event is where the repair is actually provable:
	// pre-repair boundary lacks pull_request (the Shape-A gap itself);
	// post-repair boundary AND KindOptions both contain it.
	kindOfferEvents := tracer.eventsForStage("kind_offer")
	if len(kindOfferEvents) != 1 {
		t.Fatalf("kind_offer trace events = %d, want exactly 1", len(kindOfferEvents))
	}
	event := kindOfferEvents[0]
	if len(event.KindOfferBoundaryKindsBeforeRepair) != 1 || event.KindOfferBoundaryKindsBeforeRepair[0] != "work_item" {
		t.Fatalf("KindOfferBoundaryKindsBeforeRepair = %v, want [work_item] only -- pull_request must be ABSENT pre-repair, the Shape-A gap itself", event.KindOfferBoundaryKindsBeforeRepair)
	}
	if !event.KindOfferSuppressedByCardinalityBeforeRepair || event.KindOfferDistinctKindCountBeforeRepair != 1 {
		t.Fatalf("pre-repair diagnostics = {suppressed=%v, distinct=%d}, want {true, 1} -- a single pre-repair kind offers nothing to disambiguate", event.KindOfferSuppressedByCardinalityBeforeRepair, event.KindOfferDistinctKindCountBeforeRepair)
	}
	if len(event.KindOfferBoundaryKinds) != 2 {
		t.Fatalf("KindOfferBoundaryKinds (post-repair) = %v, want both kinds -- pull_request repaired in", event.KindOfferBoundaryKinds)
	}
	sawWorkItem, sawPR := false, false
	for _, kind := range event.KindOfferBoundaryKinds {
		switch kind {
		case "work_item":
			sawWorkItem = true
		case "pull_request":
			sawPR = true
		}
	}
	if !sawWorkItem || !sawPR {
		t.Fatalf("KindOfferBoundaryKinds (post-repair) = %v, want [work_item pull_request]", event.KindOfferBoundaryKinds)
	}

	// KindOptions (the actual disclosure a caller receives) must carry the
	// repair too, not just the internal trace diagnostics.
	if len(offer.KindOptions) != 2 {
		t.Fatalf("offer.KindOptions = %#v, want expected_kind offered across BOTH kinds -- pull_request repaired in even though it never survived final truncation", offer.KindOptions)
	}
	sawWorkItemOpt, sawPROpt := false, false
	for _, opt := range offer.KindOptions {
		switch opt.Kind {
		case contextfabric.SubjectWorkItem:
			sawWorkItemOpt = true
		case contextfabric.SubjectPullRequest:
			sawPROpt = true
		}
	}
	if !sawWorkItemOpt || !sawPROpt {
		t.Fatalf("offer.KindOptions = %#v, want both work_item and pull_request offered", offer.KindOptions)
	}

	// codex CHAOS-4183 phase-3 review round 1, finding 2 (LOW): the
	// length/kind checks above do not, by themselves, prove BYTE equality
	// (IDs, labels, confidence, mechanisms, ordering) -- exactly what the
	// design's own validation step 1 requires ("assert resolution.Candidates/
	// .../top-5 CandidateOptions byte-unchanged"). A true differential proof
	// beats a field-by-field manual comparison: run the IDENTICAL two
	// work_item candidates through ResolveSubjects a second time, this time
	// with NO pull_request anywhere in the pool -- nothing for this phase to
	// repair at all. If phase 3 has zero effect on the commit-decision and
	// candidate-list axes (as designed), the two runs' own
	// resolution.Candidates/offer.CandidateOptions must be reflect.DeepEqual,
	// repair-triggering or not.
	controlBackend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"outage": {workItemA, workItemB}},
	}
	controlResolution, controlOffer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), controlBackend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("control ResolveSubjects() error = %v", err)
	}
	if !reflect.DeepEqual(resolution.Candidates, controlResolution.Candidates) {
		t.Fatalf("resolution.Candidates diverged from the no-repair control:\nrepaired: %#v\ncontrol:  %#v", resolution.Candidates, controlResolution.Candidates)
	}
	if !reflect.DeepEqual(resolution.Committed, controlResolution.Committed) {
		t.Fatalf("resolution.Committed diverged from the no-repair control:\nrepaired: %#v\ncontrol:  %#v", resolution.Committed, controlResolution.Committed)
	}
	if !reflect.DeepEqual(offer.CandidateOptions, controlOffer.CandidateOptions) {
		t.Fatalf("offer.CandidateOptions diverged from the no-repair control:\nrepaired: %#v\ncontrol:  %#v", offer.CandidateOptions, controlOffer.CandidateOptions)
	}
}

// TestResolveSubjects_KindOfferBoundaryKindsStaysUnfilteredWhenCommitted is
// codex CHAOS-4183 phase-3 review round 1, finding 1's own regression
// (MEDIUM): a committed resolution must report the trace event's
// KindOfferBoundaryKinds byte-identical to the field's own pre-phase-3
// (UNFILTERED) computation -- including a NON-offerable kind like document,
// which distinctOfferableKinds/projectKindOfferKinds' own `after` value
// (correctly) never admits, but which the pre-phase-3 field always showed.
// An earlier version of the resolve.go call site emitted afterKinds
// directly, silently dropping document from a committed row's own boundary
// telemetry -- exactly the "committed resolutions get the pre-repair
// boundary unchanged" violation this fixture pins.
func TestResolveSubjects_KindOfferBoundaryKindsStaysUnfilteredWhenCommitted(t *testing.T) {
	t.Parallel()
	strongWorkItem := candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.95, "*")
	weakDocument := candidateNode(contextfabric.SubjectDocument, "doc_1", "Outage doc", 0.1, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"outage": {strongWorkItem, weakDocument}},
	}
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	request := testRequest()
	request.Options.MaxSubjectCandidates = 2
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) == 0 {
		t.Fatal("resolution.Committed is empty -- this fixture requires a committed resolution to exercise the committed no-repair path; adjust the candidate scores")
	}

	kindOfferEvents := tracer.eventsForStage("kind_offer")
	if len(kindOfferEvents) != 1 {
		t.Fatalf("kind_offer trace events = %d, want exactly 1", len(kindOfferEvents))
	}
	event := kindOfferEvents[0]
	if len(event.KindOfferBoundaryKinds) != 2 {
		t.Fatalf("KindOfferBoundaryKinds = %v, want [work_item document] -- a committed resolution must report the SAME unfiltered boundary this field always reported pre-phase-3, document included", event.KindOfferBoundaryKinds)
	}
	sawWorkItem, sawDocument := false, false
	for _, kind := range event.KindOfferBoundaryKinds {
		switch kind {
		case "work_item":
			sawWorkItem = true
		case "document":
			sawDocument = true
		}
	}
	if !sawWorkItem || !sawDocument {
		t.Fatalf("KindOfferBoundaryKinds = %v, want both work_item and document -- document must NOT be silently dropped just because it is not an offerable kind", event.KindOfferBoundaryKinds)
	}
	// The committed no-repair path must be byte-identical to BeforeRepair --
	// nothing was, or could be, repaired here.
	if len(event.KindOfferBoundaryKindsBeforeRepair) != len(event.KindOfferBoundaryKinds) {
		t.Fatalf("KindOfferBoundaryKinds = %v, KindOfferBoundaryKindsBeforeRepair = %v, want identical on a committed resolution", event.KindOfferBoundaryKinds, event.KindOfferBoundaryKindsBeforeRepair)
	}
}
