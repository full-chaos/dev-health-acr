package graphrank

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// resolveWithOfferFloor drives the real ResolveSubjects over a lexical pool
// and returns everything the decision leaves behind.
func resolveWithOfferFloor(t *testing.T, terms []string, nodes []CandidateNode) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, *captureResolutionTracer) {
	t.Helper()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{}}
	for _, term := range terms {
		backend.searchResults[term] = nodes
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	request := testRequest()
	request.Question = "how healthy is the named team?"
	resolution, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(terms...), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	return resolution, material, tracer
}

// TestAnUnresolvableNamedSubjectOffersNothing: a nonexistent team retrieves an
// unrelated team and CI runs at the bottom of the lexical band. None of them
// may become an offer of any kind, the prompt is the typed floor-emptied one,
// and the trace carries the floor, every pre-decision candidate with its
// provenance and score, and the decision with its reason.
func TestAnUnresolvableNamedSubjectOffersNothing(t *testing.T) {
	t.Parallel()
	nodes := []CandidateNode{
		candidateNode(contextfabric.SubjectTeam, "team:ops", "Ops Team", 0.5625, "*"),
		candidateNode(contractsv1.ContextFabricSubjectCIRun, "ci:1", "CI run 1", 0.5, "*"),
		candidateNode(contractsv1.ContextFabricSubjectCIRun, "ci:2", "CI run 2", 0.625, "*"),
	}
	resolution, material, tracer := resolveWithOfferFloor(t, []string{"phantom"}, nodes)
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 {
		t.Fatalf("candidates=%v committed=%v, want none", resolution.Candidates, resolution.Committed)
	}
	if len(material.CandidateOptions)+len(material.KindOptions)+len(material.HandleOptions)+len(material.AnchorOptions) != 0 {
		t.Fatalf("material = %+v, want zero offers of every kind", material)
	}
	if !strings.HasPrefix(resolution.ClarificationPrompt, contextfabric.OfferFloorEmptiedClarificationPrompt(nil)[:40]) ||
		!strings.Contains(resolution.ClarificationPrompt, "ci_pipeline_run, team") {
		t.Fatalf("prompt = %q, want the floor-emptied prompt naming the kinds searched", resolution.ClarificationPrompt)
	}
	event, ok := lastEventForStage(tracer, "kind_offer")
	if !ok {
		t.Fatal("no kind_offer event")
	}
	if event.OfferFloorValue != OfferSimilarityFloor || event.OfferFloorPoolCount != 3 || event.OfferFloorRefused != 3 ||
		event.OfferFloorDecision != "no_offer" || event.OfferFloorReason != "every_candidate_below_floor" || len(event.OfferFloorOffers) != 0 {
		t.Fatalf("kind_offer floor fields = %+v", event)
	}
	if len(event.OfferFloorCandidates) != 3 {
		t.Fatalf("candidate lines = %v, want all three pre-decision candidates", event.OfferFloorCandidates)
	}
	for _, line := range event.OfferFloorCandidates {
		if fields := strings.Split(line, "|"); len(fields) != 5 || fields[4] != "at_or_below_floor" {
			t.Fatalf("line %q is not kind|id|mechanisms|confidence|admission at_or_below_floor", line)
		}
	}
	var summary ResolutionTraceEvent
	for _, e := range tracer.eventsForStage("offer_pool") {
		if e.OfferPoolSummary {
			summary = e
		}
	}
	if summary.OfferPoolBelowFloorExcluded != 3 || summary.OfferPoolSimilarityFloor != OfferSimilarityFloor || !summary.OfferPoolEmptiedByExclusion {
		t.Fatalf("offer_pool summary = %+v", summary)
	}
}

// TestAStrongCandidateBesideWeakOnesIsTheOnlyOffer: the floor withholds the
// weak candidates and keeps the one whose similarity clears it; the trace
// lists the offer.
func TestAStrongCandidateBesideWeakOnesIsTheOnlyOffer(t *testing.T) {
	t.Parallel()
	nodes := []CandidateNode{
		candidateNode(contextfabric.SubjectTeam, "team:platform", "Platform Team", 0.68, "*"),
		candidateNode(contractsv1.ContextFabricSubjectCIRun, "ci:1", "CI run 1", 0.5, "*"),
	}
	resolution, material, tracer := resolveWithOfferFloor(t, []string{"platform"}, nodes)
	if got := offeredIDs(resolution); len(got) != 1 || got[0] != "team:platform" {
		t.Fatalf("offered = %v, want only the candidate above the floor", got)
	}
	for _, option := range material.CandidateOptions {
		if option.CanonicalID == "ci:1" {
			t.Fatalf("weak candidate offered as an option: %+v", option)
		}
	}
	for _, option := range material.KindOptions {
		if option.Kind == contractsv1.ContextFabricSubjectCIRun {
			t.Fatalf("weak candidate's kind offered: %+v", option)
		}
	}
	event, _ := lastEventForStage(tracer, "kind_offer")
	if event.OfferFloorDecision != "offered" || event.OfferFloorRefused != 1 || len(event.OfferFloorOffers) == 0 {
		t.Fatalf("kind_offer floor fields = %+v", event)
	}
}

// TestAnIdentityMatchIsOfferedAtAnyScore: an alias/exact match is stronger
// provenance than similarity, so the floor never applies to it.
func TestAnIdentityMatchIsOfferedAtAnyScore(t *testing.T) {
	t.Parallel()
	for _, mechanism := range []contextfabric.MatchMechanism{contextfabric.MatchExact, contextfabric.MatchAlias, contextfabric.MatchProviderKey} {
		candidate := contextfabric.SubjectCandidate{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:x"},
			State:   contextfabric.ResolutionProposed, Confidence: 0.5, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical, mechanism},
		}
		if !ClassifyOffer(candidate).Admitted() {
			t.Fatalf("%s-matched candidate refused", mechanism)
		}
	}
}

// TestAWeakCoverageFloorFindDoesNotBecomeAKindOffer: the coverage floor's
// finds reach the offer builders outside the ranked pool, so they are held
// to the same floor -- a weak pull_request find adds no second kind to offer.
func TestAWeakCoverageFloorFindDoesNotBecomeAKindOffer(t *testing.T) {
	t.Parallel()
	weakPR := candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.5, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults: map[string][]CandidateNode{
			"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.68, "*")},
		},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectPullRequest: {weakPR}},
		},
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	resolution, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if got := offeredIDs(resolution); len(got) != 1 || got[0] != "wi_1" {
		t.Fatalf("offered = %v, want only the work item", got)
	}
	if len(material.KindOptions) != 0 {
		t.Fatalf("KindOptions = %+v, want none: the weak find must not supply the second kind", material.KindOptions)
	}
	for _, option := range material.CandidateOptions {
		if option.CanonicalID == "pr_1" {
			t.Fatalf("weak coverage find offered: %+v", option)
		}
	}
	event, _ := lastEventForStage(tracer, "kind_offer")
	if event.OfferFloorPoolCount != 2 || event.OfferFloorRefused != 1 {
		t.Fatalf("pool=%d refused=%d, want the two candidates counted once each with the weak one refused", event.OfferFloorPoolCount, event.OfferFloorRefused)
	}
}

// TestAWeakSecondKindInTheFullPoolDoesNotBecomeAKindOffer: the untruncated
// pool's kinds count only through the floor.
func TestAWeakSecondKindInTheFullPoolDoesNotBecomeAKindOffer(t *testing.T) {
	t.Parallel()
	nodes := []CandidateNode{
		candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.68, "*"),
		candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.6, "*"),
	}
	_, material, _ := resolveWithOfferFloor(t, []string{"outage"}, nodes)
	if len(material.KindOptions) != 0 {
		t.Fatalf("KindOptions = %+v, want none: only one kind clears the floor", material.KindOptions)
	}
	strong := []CandidateNode{
		candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.68, "*"),
		candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.66, "*"),
	}
	_, material, _ = resolveWithOfferFloor(t, []string{"outage"}, strong)
	if len(material.KindOptions) != 2 {
		t.Fatalf("control: KindOptions = %+v, want both kinds when both clear the floor", material.KindOptions)
	}
}

// TestACommittedSubjectIsNeverWithheldByTheFloor: a subject the decision
// committed stays in the offered list even when its score sits at or below the
// floor (a kind-scoped commit does not pass through the lone-candidate gate).
func TestACommittedSubjectIsNeverWithheldByTheFloor(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:c"}
	candidate := contextfabric.SubjectCandidate{Subject: subject, State: contextfabric.ResolutionCommitted, Confidence: 0.5, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical}}
	if got := offerAdmissionOf(candidate, map[string]bool{SubjectKey(subject): true}); got != OfferAdmittedCommitted {
		t.Fatalf("committed-by-key admission = %s", got)
	}
	candidate.State = contextfabric.ResolutionProposed
	if got := offerAdmissionOf(candidate, map[string]bool{SubjectKey(subject): true}); got != OfferAdmittedCommitted {
		t.Fatalf("a subject in the committed set must be admitted whatever its state field says, got %s", got)
	}
	if got := offerAdmissionOf(candidate, nil); got != OfferRefusedAtOrBelowFloor {
		t.Fatalf("control: %s", got)
	}
}

// TestOfferFloorTraceHelpers pins the small renderers the kind_offer line
// carries: dedup by subject, the bounded confidence-ordered list, the offers
// list and the fold defaults.
func TestOfferFloorTraceHelpers(t *testing.T) {
	t.Parallel()
	rows := []OfferFloorRow{
		{Kind: "team", CanonicalID: "a", Confidence: 0.5, Admission: OfferRefusedAtOrBelowFloor},
		{Kind: "team", CanonicalID: "a", Confidence: 0.5, Admission: OfferRefusedAtOrBelowFloor},
		{Kind: "team", CanonicalID: "b", Mechanisms: "exact", Confidence: 1, Admission: OfferAdmittedIdentity},
	}
	deduped, refused := dedupOfferFloorRows(rows)
	if len(deduped) != 2 || refused != 1 {
		t.Fatalf("deduped=%d refused=%d", len(deduped), refused)
	}
	lines := offerFloorCandidateLines(deduped)
	if len(lines) != 2 || lines[0] != "team|b|exact|1.0000|identity" || lines[1] != "team|a||0.5000|at_or_below_floor" {
		t.Fatalf("lines = %v", lines)
	}
	many := make([]OfferFloorRow, 0, offerFloorRowCap+5)
	for i := 0; i < offerFloorRowCap+5; i++ {
		many = append(many, OfferFloorRow{Kind: "team", CanonicalID: string(rune('a' + i)), Confidence: 0.5})
	}
	if got := len(offerFloorCandidateLines(many)); got != offerFloorRowCap {
		t.Fatalf("bounded lines = %d, want %d", got, offerFloorRowCap)
	}
	if offerFloorDecisionOrDefault("") != "no_offer" || offerFloorReasonOrDefault("") != "not_evaluated" ||
		offerFloorDecisionOrDefault("offered") != "offered" || offerFloorReasonOrDefault("empty_pool") != "empty_pool" {
		t.Fatal("fold defaults wrong")
	}
	for _, tc := range []struct {
		pool, refused int
		offers        bool
		decision      string
		reason        string
	}{
		{0, 0, false, "no_offer", "empty_pool"},
		{3, 3, false, "no_offer", "every_candidate_below_floor"},
		{3, 1, false, "no_offer", "no_offer_material"},
		{3, 1, true, "offered", "admitted_candidates_offered"},
	} {
		material := contextfabric.StructureOfferMaterial{}
		if tc.offers {
			material.CandidateOptions = make([]contractsv1.ContextFabricCandidateOption, 1)
		}
		decision, reason := offerFloorOutcome(tc.pool, tc.refused, contextfabric.StructureOfferMaterial{}, material, contextfabric.StructureOfferMaterial{})
		if decision != tc.decision || reason != tc.reason {
			t.Fatalf("%+v: got %s/%s", tc, decision, reason)
		}
	}
	if got := searchedOfferKinds([]contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: "team"}}, {Subject: contextfabric.SubjectRef{Kind: "project"}}, {Subject: contextfabric.SubjectRef{Kind: "team"}}, {},
	}); len(got) != 2 || got[0] != "project" || got[1] != "team" {
		t.Fatalf("searched kinds = %v", got)
	}
}
