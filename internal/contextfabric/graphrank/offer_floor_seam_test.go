package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// lowLoneGate lets a similarity-only candidate at or below the offer floor
// COMMIT, which is the only way a committed subject can sit under the floor.
func lowLoneGate() CommitGatePolicy {
	return CommitGatePolicy{LoneFloor: 0.4, TopFloor: 0.4, TopGap: 0.04}
}

func lexicalCandidate(kind contextfabric.SubjectKind, id string, confidence float64) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id + "_padding",
		Subject:   contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id},
		State:     contextfabric.ResolutionProposed, MatchReasons: []string{"probe"},
		Confidence: confidence, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical},
	}
}

func resolvePoolWithGate(pool map[string]contextfabric.SubjectCandidate, gate CommitGatePolicy, tracer ResolutionTracer) contextfabric.SubjectResolution {
	resolution, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(pool, map[string]string{}, map[string]bool{}, 10, true, false,
		nil, 0, false, 10, 20, true, gate, nil, nil, false, tracer, "req-floor", "", false, false, nil)
	return resolution
}

// TestTheResolutionSeamNeverWithholdsACommittedSubjectAtOrBelowTheFloor: A
// commits (lowered gate) while scoring under the floor; B is a weak
// non-committed neighbour. Only B is withheld, counted and reported.
func TestTheResolutionSeamNeverWithholdsACommittedSubjectAtOrBelowTheFloor(t *testing.T) {
	t.Parallel()
	a := lexicalCandidate(contextfabric.SubjectProject, "proj_a", 0.55)
	b := lexicalCandidate(contextfabric.SubjectProject, "proj_b", 0.45)
	tracer := &recordingTracer{}
	resolution := resolvePoolWithGate(map[string]contextfabric.SubjectCandidate{SubjectKey(a.Subject): a, SubjectKey(b.Subject): b}, lowLoneGate(), tracer)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "proj_a" {
		t.Fatalf("committed = %v, want proj_a (fixture must commit a sub-floor subject)", resolution.Committed)
	}
	var summary ResolutionTraceEvent
	var details []ResolutionTraceEvent
	for _, e := range tracer.events {
		if e.Stage != "offer_pool" {
			continue
		}
		if e.OfferPoolSummary {
			summary = e
			continue
		}
		details = append(details, e)
	}
	if summary.OfferPoolBelowFloorExcluded != 1 || summary.OfferPoolVectorOnlyExcluded != 0 || summary.OfferPoolSimilarityFloor != OfferSimilarityFloor {
		t.Fatalf("summary = %+v, want exactly proj_b floor-excluded", summary)
	}
	if len(details) != 1 || details[0].Subject.CanonicalID != "proj_b" || details[0].OfferPoolDisposition != "below_floor_excluded" ||
		details[0].Total != 1 || details[0].Index != 1 {
		t.Fatalf("details = %+v, want one below_floor_excluded event for proj_b with total 1", details)
	}
	for _, c := range resolution.Candidates {
		if c.Subject.CanonicalID == "proj_b" {
			t.Fatalf("weak neighbour offered: %+v", resolution.Candidates)
		}
	}
}

// TestALoneWeakCandidateEmptiesThePool: one floor-excluded candidate empties
// the pool with the pool-emptied prompt, exactly as a vector-only exclusion
// does; the typed floor outcome travels on the offer material, never the prompt.
func TestALoneWeakCandidateEmptiesThePool(t *testing.T) {
	t.Parallel()
	weak := lexicalCandidate(contextfabric.SubjectTeam, "team_w", 0.5)
	resolution := resolvePoolWithGate(map[string]contextfabric.SubjectCandidate{SubjectKey(weak.Subject): weak}, DefaultCommitGatePolicy(), nil)
	want := contextfabric.OfferPoolEmptiedClarificationPrompt
	if resolution.ClarificationPrompt != want || len(resolution.Candidates) != 0 {
		t.Fatalf("prompt=%q candidates=%v, want %q and none", resolution.ClarificationPrompt, resolution.Candidates, want)
	}
	vec := lexicalCandidate(contextfabric.SubjectTeam, "team_v", 0.9)
	vec.MatchMechanisms = []contextfabric.MatchMechanism{contextfabric.MatchVector}
	res := resolvePoolWithGate(map[string]contextfabric.SubjectCandidate{SubjectKey(vec.Subject): vec}, DefaultCommitGatePolicy(), nil)
	if res.ClarificationPrompt != contextfabric.OfferPoolEmptiedClarificationPrompt {
		t.Fatalf("vector-only prompt = %q", res.ClarificationPrompt)
	}
}

// TestTheOfferSeamKeepsACommittedSubjectAndCountsItOnce drives the whole
// ResolveSubjects seam: the committed weak lexical hit is reached by the
// coverage floor AND the full pool, and must read "committed" on both.
func TestTheOfferSeamKeepsACommittedSubjectAndCountsItOnce(t *testing.T) {
	t.Parallel()
	weakPR := candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.5, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:  true,
		searchResults:     map[string][]CandidateNode{"outage": {weakPR}},
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
		t.Fatalf("committed = %v, want pr_1 (fixture must commit a sub-floor subject)", resolution.Committed)
	}
	event, _ := lastEventForStage(tracer, "kind_offer")
	if event.OfferFloorPoolCount != 1 || event.OfferFloorRefused != 0 || len(event.OfferFloorCandidates) != 1 ||
		!strings.HasSuffix(event.OfferFloorCandidates[0], "|committed") {
		t.Fatalf("pool=%d refused=%d lines=%v, want one committed row and nothing refused", event.OfferFloorPoolCount, event.OfferFloorRefused, event.OfferFloorCandidates)
	}
	if event.OfferFloorDecision != "no_offer" || event.OfferFloorReason != "no_offer_material" {
		t.Fatalf("decision=%s reason=%s, want no_offer/no_offer_material for a non-empty admitted pool with nothing to offer", event.OfferFloorDecision, event.OfferFloorReason)
	}
}

// TestACommittedFullPoolSubjectIsNotRefusedByTheFullPoolFilter: the committed
// weak subject is in the merged pool only (not a coverage find).
func TestACommittedFullPoolSubjectIsNotRefusedByTheFullPoolFilter(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.5, "*")}}}
	deps := backend.deps()
	deps.CommitGatePolicy = lowLoneGate()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("committed = %v, want wi_1", resolution.Committed)
	}
	event, _ := lastEventForStage(tracer, "kind_offer")
	if event.OfferFloorRefused != 0 || len(event.OfferFloorCandidates) != 1 || !strings.HasSuffix(event.OfferFloorCandidates[0], "|committed") {
		t.Fatalf("refused=%d lines=%v", event.OfferFloorRefused, event.OfferFloorCandidates)
	}
}

// TestAWeakOffOnlyCoverageFindIsCountedAsARefusedRow: a weak team find lives
// only in the coverage floor's private pool, so its row must come from the
// coverage loop.
func TestAWeakOffOnlyCoverageFindIsCountedAsARefusedRow(t *testing.T) {
	t.Parallel()
	weakTeam := candidateNode(contextfabric.SubjectTeam, "team_1", "Outage Team", 0.5, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.68, "*")}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectTeam: {weakTeam}},
		},
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	_, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, option := range material.KindOptions {
		if option.Kind == contextfabric.SubjectTeam {
			t.Fatalf("weak off-only find supplied a kind offer: %+v", material.KindOptions)
		}
	}
	event, _ := lastEventForStage(tracer, "kind_offer")
	if event.OfferFloorPoolCount != 2 || event.OfferFloorRefused != 1 {
		t.Fatalf("pool=%d refused=%d lines=%v, want the off-only find counted as a refused row", event.OfferFloorPoolCount, event.OfferFloorRefused, event.OfferFloorCandidates)
	}
}

func TestOfferFloorDedupKeysOnKindAndID(t *testing.T) {
	t.Parallel()
	rows := []OfferFloorRow{
		{Kind: "team", CanonicalID: "x", Admission: OfferAdmittedIdentity},
		{Kind: "project", CanonicalID: "x", Admission: OfferRefusedAtOrBelowFloor},
		{Kind: "team", CanonicalID: "y", Admission: OfferRefusedAtOrBelowFloor},
		{Kind: "team", CanonicalID: "x", Admission: OfferRefusedAtOrBelowFloor},
	}
	out, refused := dedupOfferFloorRows(rows)
	if len(out) != 3 || refused != 2 {
		t.Fatalf("deduped=%d refused=%d, want 3 rows (same id under two kinds stays two) and 2 refused", len(out), refused)
	}
	if out[0].Admission != OfferAdmittedIdentity {
		t.Fatalf("first row must win the fold: %+v", out[0])
	}
}

func TestOfferFloorOfferLinesAndOutcome(t *testing.T) {
	t.Parallel()
	kind := contextfabric.StructureOfferMaterial{KindOptions: []contractsv1.ContextFabricKindOption{{Kind: "team"}, {Kind: "project"}}}
	cand := contextfabric.StructureOfferMaterial{CandidateOptions: []contractsv1.ContextFabricCandidateOption{{Kind: "team", CanonicalID: "t1"}}}
	handle := contextfabric.StructureOfferMaterial{HandleOptions: []contractsv1.ContextFabricHandleOption{{Kind: "repository", Value: "v1"}}}
	got := offerFloorOfferLines(kind, cand, handle)
	want := []string{"kind|team", "kind|project", "candidate|team|t1", "handle|repository|v1"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	big := contextfabric.StructureOfferMaterial{}
	for i := 0; i < 23; i++ {
		big.KindOptions = append(big.KindOptions, contractsv1.ContextFabricKindOption{Kind: "team"})
	}
	if n := len(offerFloorOfferLines(big, contextfabric.StructureOfferMaterial{}, contextfabric.StructureOfferMaterial{})); n != 23 {
		t.Fatalf("offer lines = %d, want every one of 23", n)
	}
	exact := contextfabric.StructureOfferMaterial{}
	for i := 0; i < 20; i++ {
		exact.KindOptions = append(exact.KindOptions, contractsv1.ContextFabricKindOption{Kind: "team"})
	}
	if n := len(offerFloorOfferLines(exact, contextfabric.StructureOfferMaterial{}, contextfabric.StructureOfferMaterial{})); n != 20 {
		t.Fatalf("offer lines at the cap = %d, want 20", n)
	}
	// Each offer family alone is enough to read as "offered".
	for name, tc := range map[string][3]contextfabric.StructureOfferMaterial{
		"kind": {kind, {}, {}}, "candidate": {{}, cand, {}}, "handle": {{}, {}, handle},
	} {
		if d, r := offerFloorOutcome(2, 0, tc[0], tc[1], tc[2]); d != "offered" || r != "admitted_candidates_offered" {
			t.Fatalf("%s alone: %s/%s", name, d, r)
		}
	}
	// pool 2, nothing refused, nothing offered: material, not empty.
	if d, r := offerFloorOutcome(2, 0, contextfabric.StructureOfferMaterial{}, contextfabric.StructureOfferMaterial{}, contextfabric.StructureOfferMaterial{}); d != "no_offer" || r != "no_offer_material" {
		t.Fatalf("pool 2 refused 0: %s/%s", d, r)
	}
	if d, r := offerFloorOutcome(3, 1, contextfabric.StructureOfferMaterial{}, contextfabric.StructureOfferMaterial{}, contextfabric.StructureOfferMaterial{}); d != "no_offer" || r != "no_offer_material" {
		t.Fatalf("pool 3 refused 1: %s/%s", d, r)
	}
}

func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestTheSlogTracerCarriesEachOfferFloorFieldUnderItsOwnKey: distinct values
// per field so a swapped or dropped key cannot pass; hostile text is
// sanitized; an unset event folds to the declared defaults with empty lists.
func TestTheSlogTracerCarriesEachOfferFloorFieldUnderItsOwnKey(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	tracer.Trace(ResolutionTraceEvent{
		RequestID: "req", Stage: "kind_offer",
		OfferFloorValue: 0.625, OfferFloorPoolCount: 7, OfferFloorRefused: 3,
		OfferFloorCandidates: []string{"team|a\nFORGED|lexical|0.5000|at_or_below_floor"},
		OfferFloorDecision:   "no_offer\nFORGED", OfferFloorReason: "empty_pool\rFORGED",
		OfferFloorOffers: []string{"kind|te\nam"},
	})
	tracer.Trace(ResolutionTraceEvent{RequestID: "req", Stage: "kind_offer"})
	tracer.Trace(ResolutionTraceEvent{
		RequestID: "req", Stage: "offer_pool", OfferPoolSummary: true,
		OfferPoolVectorOnlyExcluded: 1, OfferPoolVectorOnlyDemoted: 2, OfferPoolBelowFloorExcluded: 5, OfferPoolSimilarityFloor: 0.625,
	})
	lines := decodeLogLines(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3", len(lines))
	}
	first := lines[0]
	if first["offer_floor"] != 0.625 || first["offer_floor_pool_count"] != float64(7) || first["offer_floor_refused_count"] != float64(3) {
		t.Fatalf("scalars = %v", first)
	}
	if first["offer_floor_decision"] != "no_offer?FORGED" || first["offer_floor_reason"] != "empty_pool?FORGED" {
		t.Fatalf("decision/reason not sanitized: %v / %v", first["offer_floor_decision"], first["offer_floor_reason"])
	}
	cands, _ := first["offer_floor_candidates"].([]any)
	offers, _ := first["offer_floor_offers"].([]any)
	if len(cands) != 1 || cands[0] != "team|a?FORGED|lexical|0.5000|at_or_below_floor" || len(offers) != 1 || offers[0] != "kind|te?am" {
		t.Fatalf("lists not sanitized: %v %v", cands, offers)
	}
	second := lines[1]
	if second["offer_floor_decision"] != "no_offer" || second["offer_floor_reason"] != "not_evaluated" {
		t.Fatalf("defaults = %v / %v", second["offer_floor_decision"], second["offer_floor_reason"])
	}
	for _, key := range []string{"offer_floor_candidates", "offer_floor_offers"} {
		list, ok := second[key].([]any)
		if !ok || len(list) != 0 {
			t.Fatalf("%s = %#v, want an empty list, not null", key, second[key])
		}
	}
	summary := lines[2]
	if summary["below_floor_excluded"] != float64(5) || summary["similarity_floor"] != 0.625 ||
		summary["vector_only_excluded"] != float64(1) || summary["vector_only_demoted"] != float64(2) {
		t.Fatalf("offer_pool summary = %v", summary)
	}
}

// TestAWeakCandidateOfTheDeclaredKindDoesNotMakeThePoolHoldThatKind: the frame
// declares `project`; the only project in the pool sits under the floor, so
// the pool holds no servable project and the declared kind must stay
// withheld (no need raised), exactly as if the project were absent.
func TestAWeakCandidateOfTheDeclaredKindDoesNotMakeThePoolHoldThatKind(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"CHAOS": {
		candidateNode(contextfabric.SubjectTeam, "team:CHAOS", "CHAOS", 0.68, "*"),
		candidateNode(contextfabric.SubjectPullRequest, "pr:1", "CHAOS pull request", 0.66, "*"),
		candidateNode(contextfabric.SubjectProject, "project:weak", "CHAOS project", 0.5, "*"),
	}}}
	frame := namedSubjectFrame("CHAOS", kindOf(contractsv1.ContextFabricSubjectProject))
	_, offer, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("CHAOS"), backend.deps(), nil, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(offer.KindOptions) != 0 {
		t.Fatalf("KindOptions = %+v, want none: the declared kind is held only by a sub-floor candidate", offer.KindOptions)
	}
}
