package graphrank

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestACommittedCoverageFindIsNotRefusedAsAWeakCoverageRow: the pull request
// is reached ONLY by the coverage floor's kind search (the primary search finds
// nothing), merges into the pool, and commits under a lowered gate while
// scoring under the offer floor. Its coverage row must read "committed", not
// "at_or_below_floor": the coverage row is folded first and wins the dedup.
func TestACommittedCoverageFindIsNotRefusedAsAWeakCoverageRow(t *testing.T) {
	t.Parallel()
	weakPR := candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.5, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:  true,
		searchResults:     map[string][]CandidateNode{},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{"outage": {contextfabric.SubjectPullRequest: {weakPR}}},
	}
	deps := backend.deps()
	deps.CommitGatePolicy = lowLoneGate()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "pr_1" {
		t.Fatalf("committed = %v, want pr_1 (the fixture must commit a coverage find under the floor)", resolution.Committed)
	}
	event, _ := lastEventForStage(tracer, "kind_offer")
	if event.OfferFloorRefused != 0 || len(event.OfferFloorCandidates) != 1 || !strings.HasSuffix(event.OfferFloorCandidates[0], "|committed") {
		t.Fatalf("refused=%d lines=%v, want the committed coverage find read as committed", event.OfferFloorRefused, event.OfferFloorCandidates)
	}
}

// TestThePreCountTotalMatchesTheWithheldEventsWhenMostAreRefused: two refused
// neighbours and one admitted (committed) subject, so a precount that counted
// the admitted side, or missed the refused ones, cannot equal the number of
// events actually emitted.
func TestThePreCountTotalMatchesTheWithheldEventsWhenMostAreRefused(t *testing.T) {
	t.Parallel()
	a := lexicalCandidate(contextfabric.SubjectProject, "proj_a", 0.55)
	b := lexicalCandidate(contextfabric.SubjectProject, "proj_b", 0.45)
	c := lexicalCandidate(contextfabric.SubjectProject, "proj_c", 0.40)
	tracer := &recordingTracer{}
	resolution := resolvePoolWithGate(map[string]contextfabric.SubjectCandidate{
		SubjectKey(a.Subject): a, SubjectKey(b.Subject): b, SubjectKey(c.Subject): c,
	}, lowLoneGate(), tracer)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "proj_a" {
		t.Fatalf("committed = %v, want proj_a", resolution.Committed)
	}
	details := 0
	for _, e := range tracer.events {
		if e.Stage == "offer_pool" && !e.OfferPoolSummary {
			details++
			if e.Total != 2 || e.OfferPoolDisposition != "below_floor_excluded" {
				t.Fatalf("detail event = %+v, want total 2 below_floor_excluded", e)
			}
		}
	}
	if details != 2 {
		t.Fatalf("detail events = %d, want 2", details)
	}
}
