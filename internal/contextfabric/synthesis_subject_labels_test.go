package contextfabric

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// labelPlacement is one place a subject can occur in the synthesis payload.
type labelPlacement struct {
	name string
	add  func(input *SynthesisInput, subject SubjectRef)
}

// labelPlacements lists every place a subject occurs in the payload the model
// reads. The census test below checks the list against the serialized payload
// by shape, so a source added to the payload and missing here fails there.
func labelPlacements() []labelPlacement {
	cohort := func(input *SynthesisInput) *Cohort {
		if input.Graph.Cohort == nil {
			input.Graph.Cohort = &Cohort{Kind: SubjectProject, Rationale: "fixture", Complete: true}
		}
		return input.Graph.Cohort
	}
	path := func(input *SynthesisInput) *RelationshipPath {
		if len(input.Graph.Paths) == 0 {
			input.Graph.Paths = []RelationshipPath{{PathID: "path_12345678", EvidenceRefIDs: []string{"evidence_release_1234"}}}
		}
		return &input.Graph.Paths[0]
	}
	return []labelPlacement{
		{"Resolution.Committed", func(in *SynthesisInput, s SubjectRef) {
			in.Graph.Resolution.Committed = append(in.Graph.Resolution.Committed, s)
		}},
		{"Resolution.Candidates", func(in *SynthesisInput, s SubjectRef) {
			in.Graph.Resolution.Candidates = append(in.Graph.Resolution.Candidates, SubjectCandidate{ReceiptID: "receipt_12345678", Subject: s, MatchReasons: []string{"lexical"}, Confidence: 0.4})
		}},
		{"Cohort.Members", func(in *SynthesisInput, s SubjectRef) {
			c := cohort(in)
			c.Members = append(c.Members, CohortMember{Subject: s, Rank: len(c.Members) + 1, InclusionReasons: []string{"fixture"}})
		}},
		{"Cohort.Groups", func(in *SynthesisInput, s SubjectRef) {
			c := cohort(in)
			c.Groups = append(c.Groups, groupFor(s))
		}},
		{"Cohort.Exclusions", func(in *SynthesisInput, s SubjectRef) {
			c := cohort(in)
			c.Exclusions = append(c.Exclusions, CohortExclusion{Subject: s})
		}},
		{"Paths[].Nodes", func(in *SynthesisInput, s SubjectRef) {
			p := path(in)
			p.Nodes = append(p.Nodes, s)
		}},
		{"Paths[].Edges[].From", func(in *SynthesisInput, s SubjectRef) {
			p := path(in)
			p.Edges = append(p.Edges, RelationshipEdge{Type: "BLOCKS", From: s, To: SubjectRef{Kind: SubjectProject, CanonicalID: "project_edge_peer", Label: "Edge Peer"}, EvidenceRefIDs: []string{"evidence_release_1234"}})
		}},
		{"Paths[].Edges[].To", func(in *SynthesisInput, s SubjectRef) {
			p := path(in)
			p.Edges = append(p.Edges, RelationshipEdge{Type: "BLOCKS", From: SubjectRef{Kind: SubjectProject, CanonicalID: "project_edge_peer", Label: "Edge Peer"}, To: s, EvidenceRefIDs: []string{"evidence_release_1234"}})
		}},
		{"Facts[].Subject", func(in *SynthesisInput, s SubjectRef) {
			in.Facts.Facts = append(in.Facts.Facts, CanonicalFact{
				Kind: FactReadiness, Subject: s, Fields: map[string]FactValue{"release_ready": BooleanFactValue(false)},
				EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
			})
		}},
		{"DriverCandidates[].AffectedSubjects", func(in *SynthesisInput, s SubjectRef) {
			in.Graph.DriverCandidates = append(in.Graph.DriverCandidates, DriverJudgment{
				DriverID: "driver_87654321", Standing: DriverPrincipal, Category: "relationship", Title: "Engine candidate",
				Summary: "Engine-minted candidate driver.", AffectedSubjects: []SubjectRef{s}, PathIDs: []string{"path_12345678"},
				EvidenceRefIDs: []string{"evidence_release_1234"}, Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
				Confidence: 0.8, Current: true,
			})
		}},
	}
}

func groupFor(s SubjectRef) contractsv1.ContextFabricCohortGroup {
	return contractsv1.ContextFabricCohortGroup{Subject: s, MemberCanonicalIDs: []string{"project_ask_dev"}, Complete: true, Total: 1}
}

func labelsOfKey(t *testing.T, input SynthesisInput, subject SubjectRef) map[string]struct{} {
	t.Helper()
	census, ok := synthesisPayloadSubjectLabels(input)
	if !ok {
		t.Fatal("payload census unavailable")
	}
	return census[subjectKeyForModel(subject)]
}

// TestEverySubjectSourcePairServesOneLabelPerKey enumerates every ordered pair
// of payload sources x every subject kind: the same key is placed in source A
// under one label and in source B under another. The canonical input must show
// one label for the key, the label the validator binds, and the validator must
// still reject the other label.
func TestEverySubjectSourcePairServesOneLabelPerKey(t *testing.T) {
	t.Parallel()
	kinds := []SubjectKind{SubjectWorkItem, SubjectProject, SubjectTeam, SubjectRepository}
	places := labelPlacements()
	cells := 0
	for _, kind := range kinds {
		for _, a := range places {
			for _, b := range places {
				if a.name == b.name {
					continue
				}
				kind, a, b := kind, a, b
				cells++
				t.Run(fmt.Sprintf("%s/%s+%s", kind, a.name, b.name), func(t *testing.T) {
					t.Parallel()
					first := SubjectRef{Kind: kind, CanonicalID: "subject_under_test", Label: "Label A"}
					second := SubjectRef{Kind: kind, CanonicalID: "subject_under_test", Label: "Label B"}
					input, _ := closureFixture()
					input.Graph.Cohort = nil
					a.add(&input, first)
					b.add(&input, second)
					before, _ := synthesisPayloadDecoded(input)

					if got := labelsOfKey(t, input, first); len(got) != 2 {
						t.Fatalf("fixture: key shown under %d labels before the pass, want 2", len(got))
					}
					bound := canonicalSubjectLabels(input)[subjectKeyForModel(first)]

					out, report := canonicalizeSynthesisSubjectLabels(input)

					if afterCall, _ := synthesisPayloadDecoded(input); !reflect.DeepEqual(before, afterCall) {
						t.Fatal("the caller's input was mutated")
					}
					got := labelsOfKey(t, out, first)
					if len(got) != 1 {
						t.Fatalf("key shown under %d labels after the pass, want 1: %v", len(got), got)
					}
					if _, ok := got[bound]; !ok {
						t.Fatalf("shown label %v is not the validator-bound label %q", got, bound)
					}
					if report.KeysCollapsed != 1 || report.KeysResidual != 0 || !report.Measured || report.Outcome() != LabelCanonicalizationCollapsed {
						t.Fatalf("report = %+v (%s), want one key collapsed, none residual", report, report.Outcome())
					}
					if out.LabelCanonicalization != report {
						t.Fatal("the report is not carried on the returned input")
					}
					if canonicalSubjectLabels(out)[subjectKeyForModel(first)] != bound {
						t.Fatal("the bound label moved")
					}
					// The validator is not widened: the bound label passes and
					// the other label is rejected.
					other := first
					if bound == first.Label {
						other = second
					}
					if err := requireBoundLabel("claimed fact", other, canonicalSubjectLabels(out)); err == nil {
						t.Fatalf("the label %q that is not bound was accepted", other.Label)
					}
					sameKey := SubjectRef{Kind: kind, CanonicalID: "subject_under_test", Label: bound}
					if err := requireBoundLabel("claimed fact", sameKey, canonicalSubjectLabels(out)); err != nil {
						t.Fatalf("the bound label was rejected: %v", err)
					}
					// Idempotent: a second pass finds nothing to collapse.
					_, again := canonicalizeSynthesisSubjectLabels(out)
					if again.KeysCollapsed != 0 || again.Outcome() != LabelCanonicalizationUnchanged {
						t.Fatalf("second pass = %+v, want unchanged", again)
					}
				})
			}
		}
	}
	if want := len(kinds) * len(places) * (len(places) - 1); cells != want {
		t.Fatalf("enumerated %d cells, want %d", cells, want)
	}
}

// TestSubjectLabelPlacementsCoverEverySerializedSubjectSource fills every
// placement with a distinct subject and checks each is found by the payload
// census, so a payload source missing from labelPlacements is caught by
// shape rather than by list.
func TestSubjectLabelPlacementsCoverEverySerializedSubjectSource(t *testing.T) {
	t.Parallel()
	input, _ := closureFixture()
	input.Graph.Cohort = nil
	want := map[string]struct{}{}
	for i, place := range labelPlacements() {
		subject := SubjectRef{Kind: SubjectWorkItem, CanonicalID: fmt.Sprintf("subject_%02d", i), Label: place.name}
		place.add(&input, subject)
		want[subjectKeyForModel(subject)] = struct{}{}
	}
	census, ok := synthesisPayloadSubjectLabels(input)
	if !ok {
		t.Fatal("payload census unavailable")
	}
	var missing []string
	for key := range want {
		if _, found := census[key]; !found {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("placements not found in the serialized payload: %v", missing)
	}
	// Every subject the payload serializes must be one a placement can carry
	// or one the fixture itself names; anything else is a source this list
	// does not know about.
	fixture, _ := closureFixture()
	known := map[string]struct{}{}
	for key := range want {
		known[key] = struct{}{}
	}
	fixtureCensus, _ := synthesisPayloadSubjectLabels(fixture)
	for key := range fixtureCensus {
		known[key] = struct{}{}
	}
	known[subjectKeyForModel(SubjectRef{Kind: SubjectProject, CanonicalID: "project_edge_peer"})] = struct{}{}
	known[subjectKeyForModel(SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev"})] = struct{}{}
	for key := range census {
		if _, ok := known[key]; !ok {
			t.Fatalf("the payload serializes subject %q from a source labelPlacements does not cover", key)
		}
	}
}

// TestEngineServesOneLabelPerKeyAndTheModelDraftValidates drives the real
// engine assembly: a work item is a cohort member under one label and a fact
// subject under another. The synthesizer receives one label, a claim and a
// driver written with the label the model was shown validate, and the label
// the fact carried before the pass is still rejected.
func TestEngineServesOneLabelPerKeyAndTheModelDraftValidates(t *testing.T) {
	t.Parallel()
	base, draft := closureFixture()
	item := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item_7", Label: "Member Label"}
	factItem := item
	factItem.Label = "Fact Label"
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	graph := GraphContext{
		Resolution: resolution,
		Cohort: &Cohort{
			Kind: SubjectWorkItem, Rationale: "fixture", Complete: true,
			Members: []CohortMember{{Subject: item, Rank: 1, InclusionReasons: []string{"fixture"}}},
		},
		Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
		EvidenceRefIDs: []string{"evidence_release_1234"},
		Coverage:       Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
	}
	fact := base.Facts.Facts[0]
	fact.Subject = factItem
	var captured SynthesisInput
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return base.Interpretation, nil
		}),
		Graph: graphReaderStub{resolution: resolution, context: graph},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{fact}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			captured = input
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "j", CurrentState: "s", StrongestPressures: []string{},
				Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, DeterministicAnswer: "d", Warnings: []string{},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "p", QueryVersion: "q", InterpretationVersion: "i", SynthesisVersion: "s"},
			}, nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(captured.Facts.Facts) != 1 || captured.Graph.Cohort == nil {
		t.Fatalf("synthesizer input: %d facts, cohort %v", len(captured.Facts.Facts), captured.Graph.Cohort)
	}
	if got := captured.Facts.Facts[0].Subject.Label; got != "Member Label" {
		t.Fatalf("the fact is shown under %q, want the cohort member label", got)
	}
	if report := captured.LabelCanonicalization; report.KeysCollapsed != 1 || report.KeysResidual != 0 || !report.Measured {
		t.Fatalf("report = %+v, want one collapsed key", report)
	}

	shown := captured.Facts.Facts[0].Subject
	draft.Drivers[0].AffectedSubjects = []SubjectRef{shown}
	draft.Drivers[0].Category = "readiness"
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_readiness_1"}
	draft.Drivers[0].PathIDs = nil
	draft.ClaimedFacts = []ClaimedFact{{ClaimID: "claim_readiness_1", Kind: FactReadiness, Subject: shown, Field: "release_ready", Value: boolScalar(false)}}
	if err := draft.ValidateAgainst(captured); err != nil {
		t.Fatalf("a draft written with the label shown to the model was rejected: %v", err)
	}

	stale := draft
	stale.ClaimedFacts = []ClaimedFact{{ClaimID: "claim_readiness_1", Kind: FactReadiness, Subject: factItem, Field: "release_ready", Value: boolScalar(false)}}
	stale.Drivers = append([]DriverJudgment(nil), draft.Drivers...)
	stale.Drivers[0].AffectedSubjects = []SubjectRef{shown}
	err = stale.ValidateAgainst(captured)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimSubjectLabelMismatch {
		t.Fatalf("a claim under the label that is not bound: reason %q (err %v), want %q", got, err, RejectionReasonClaimSubjectLabelMismatch)
	}
}

// TestAKeyOutsideTheBindingWalkIsReportedResidual: two exclusions name one key
// under two labels and no citable source carries it, so no binding exists to
// collapse them to. The line must say so instead of reporting a clean pass.
func TestAKeyOutsideTheBindingWalkIsReportedResidual(t *testing.T) {
	t.Parallel()
	input, _ := closureFixture()
	input.Graph.Cohort = nil
	places := labelPlacements()
	var exclusion labelPlacement
	for _, place := range places {
		if place.name == "Cohort.Exclusions" {
			exclusion = place
		}
	}
	exclusion.add(&input, SubjectRef{Kind: SubjectWorkItem, CanonicalID: "excluded_only", Label: "One"})
	exclusion.add(&input, SubjectRef{Kind: SubjectWorkItem, CanonicalID: "excluded_only", Label: "Two"})
	_, report := canonicalizeSynthesisSubjectLabels(input)
	if report.KeysCollapsed != 1 || report.KeysResidual != 1 || report.Outcome() != LabelCanonicalizationResidual {
		t.Fatalf("report = %+v (%s), want the key counted collapsed-not and residual", report, report.Outcome())
	}
}

// TestUnchangedInputReportsUnchanged: one label per key needs no collapse.
func TestUnchangedInputReportsUnchanged(t *testing.T) {
	t.Parallel()
	input, _, _ := groupedCohortFixture()
	out, report := canonicalizeSynthesisSubjectLabels(input)
	if out.Graph.Cohort != input.Graph.Cohort {
		t.Fatal("an input with one label per key was copied instead of passed through")
	}
	if report.KeysCollapsed != 0 || report.KeysResidual != 0 || report.Outcome() != LabelCanonicalizationUnchanged {
		t.Fatalf("report = %+v (%s), want unchanged", report, report.Outcome())
	}
	if !reflect.DeepEqual(out.Facts.Facts, input.Facts.Facts) || !reflect.DeepEqual(out.Graph.Paths, input.Graph.Paths) {
		t.Fatal("an input with one label per key was altered")
	}
}

// TestUnserializablePayloadReportsUnmeasured: a payload that cannot be
// serialized is reported unmeasured, never as zero collapses.
func TestUnserializablePayloadReportsUnmeasured(t *testing.T) {
	t.Parallel()
	input, _ := closureFixture()
	input.Facts.Facts[0].Fields["ratio"] = NumberFactValue(math.NaN())
	_, report := canonicalizeSynthesisSubjectLabels(input)
	if report.Measured || report.Outcome() != LabelCanonicalizationUnmeasured {
		t.Fatalf("report = %+v (%s), want unmeasured", report, report.Outcome())
	}
}

// TestTwoCandidatesForOneKeyCollapseToTheFirstLabel: candidates are the first
// binding source, so a second candidate for the same key is rewritten too.
func TestTwoCandidatesForOneKeyCollapseToTheFirstLabel(t *testing.T) {
	t.Parallel()
	input, _ := closureFixture()
	place := labelPlacements()[1]
	if place.name != "Resolution.Candidates" {
		t.Fatalf("placement 1 = %s", place.name)
	}
	first := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "candidate_key", Label: "First"}
	second := first
	second.Label = "Second"
	place.add(&input, first)
	place.add(&input, second)
	out, report := canonicalizeSynthesisSubjectLabels(input)
	if got := labelsOfKey(t, out, first); len(got) != 1 {
		t.Fatalf("labels after the pass = %v, want one", got)
	}
	if _, ok := labelsOfKey(t, out, first)["First"]; !ok || report.KeysCollapsed != 1 || report.KeysResidual != 0 {
		t.Fatalf("report = %+v, want the first label kept", report)
	}
}

// TestACohortWhoseLabelsAreBoundKeepsItsIdentity: a collision elsewhere (a fact
// under another label) rewrites the fact and leaves the cohort, the value the
// served answer carries, as the same object.
func TestACohortWhoseLabelsAreBoundKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	input, _, _ := groupedCohortFixture()
	member := input.Graph.Cohort.Members[0].Subject
	fact := member
	fact.Label = "Fact Label"
	labelPlacements()[7].add(&input, fact)
	out, report := canonicalizeSynthesisSubjectLabels(input)
	if report.KeysCollapsed != 1 {
		t.Fatalf("report = %+v, want one collapsed key", report)
	}
	if out.Graph.Cohort != input.Graph.Cohort {
		t.Fatal("a cohort needing no rewrite was copied")
	}
	if got := out.Facts.Facts[len(out.Facts.Facts)-1].Subject.Label; got != member.Label {
		t.Fatalf("fact label = %q, want the cohort member's %q", got, member.Label)
	}
}
