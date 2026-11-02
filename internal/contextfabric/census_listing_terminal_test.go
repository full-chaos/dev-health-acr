package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12467: a census count never reads as "found nothing", on any arm of the
// empty-pool terminal. Each false sentence is named: the two the ticket's trace
// found, and the floor's.
func TestACountNeverReadsAsNothingFound(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricCensusUnlistedLimitation(10)
	for name, resolution := range map[string]SubjectResolution{
		"an empty pool":          {Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence},
		"the exclusion's prompt": {Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt},
	} {
		for _, allowClarification := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				request := validInvestigationRequest()
				request.Options.AllowClarification = allowClarification
				engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: resolution}, &recordingTelemetry{})
				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if result.Status != InvestigationNoMatch {
					t.Fatalf("status = %q, want no_match: nothing is listed to confirm", result.Status)
				}
				if !slices.Contains(result.Limitations, sentence) {
					t.Fatalf("limitations %q lack the count", result.Limitations)
				}
				for _, limitation := range result.Limitations {
					switch limitation {
					case noMatchLimitationUnproven, clarificationRequiredLimitationOne, noMatchLimitationOfferPoolEmptied, noMatchLimitationOfferFloorEmptied:
						t.Fatalf("limitations %q say nothing was found", result.Limitations)
					}
				}
			})
		}
	}
}

// flooredGraphReader is a reader whose offer floor withheld every candidate.
type flooredGraphReader struct{ authzDroppingGraphReader }

func (g flooredGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection, *QuestionFrame, SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return g.resolution, StructureOfferMaterial{SubjectFloor: OfferFloorOutcome{Refused: true}}, nil, nil, nil
}

// The floor's "no subject matching the name" is false of a counted subject too.
func TestACountIsStatedAheadOfTheFloor(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricCensusUnlistedLimitation(10)
	engine := mustEngineForTerminalReasonTest(t, flooredGraphReader{authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence}}}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch || !slices.Contains(result.Limitations, sentence) {
		t.Fatalf("status %q limitations %q", result.Status, result.Limitations)
	}
	for _, limitation := range result.Limitations {
		if strings.HasPrefix(limitation, "No subject matched") || limitation == noMatchLimitationOfferFloorEmptied {
			t.Fatalf("limitations %q say nothing matched", result.Limitations)
		}
	}
}

// With a listing cut at the bound, the terminal asks and says how many it holds.
func TestAPartListingIsStatedOnTheClarification(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricCensusListedLimitation(10, 2)
	subject := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:r:12", Label: "pull request #12 in acme/alpha"}
	other := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:s:12", Label: "pull request #12 in acme/beta"}
	candidates := []SubjectCandidate{
		{ReceiptID: "receipt_census_a1", Subject: subject, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5},
		{ReceiptID: "receipt_census_b1", Subject: other, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5},
	}
	engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}, CensusLimitation: sentence, ClarificationPrompt: "Which one?"}}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha", "acme/beta"}}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q", result.Status)
	}
	if !slices.Contains(result.Limitations, sentence) {
		t.Fatalf("limitations %q lack the partial count", result.Limitations)
	}
	if !slices.Contains(result.Limitations, clarificationRequiredLimitation) {
		t.Fatalf("limitations %q lack the terminal's own sentence: several candidates are listed", result.Limitations)
	}
}

// The terminal reason names the state, so a count is not an empty pool in the
// logs either.
func TestACountIsNotAnEmptyPoolInTheTerminalReason(t *testing.T) {
	t.Parallel()
	resolution := SubjectResolution{CensusLimitation: contractsv1.ContextFabricCensusUnlistedLimitation(10), ClarificationPrompt: OfferPoolEmptiedClarificationPrompt}
	if got := subjectlessTerminalReason(FrameGate{}, resolution, 0, declaredKindDecision{}); got != censusUnlistedTerminalReason {
		t.Fatalf("reason = %q", got)
	}
	if !slices.Contains(SubjectlessTerminalReasonVocabulary(), censusUnlistedTerminalReason) {
		t.Fatalf("%q is outside the vocabulary", censusUnlistedTerminalReason)
	}
}

// One candidate beside a census count of more: "one subject matched" is false of
// it, on both terminals, in the limitation, the prompt and the answer.
func TestOneCandidateBesideACountSaysTheCount(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:r:12", Label: "pull request #12 in acme/alpha"}
	candidate := SubjectCandidate{ReceiptID: "receipt_census_a1", Subject: subject, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5}
	for name, sentence := range map[string]string{
		"it is one of them":     contractsv1.ContextFabricCensusListedLimitation(2, 1),
		"it is none of them":    contractsv1.ContextFabricCensusUnlistedLimitation(2),
		"it is beside a lone 1": contractsv1.ContextFabricCensusUnlistedLimitation(1),
	} {
		for allowClarification, status := range map[bool]InvestigationStatus{true: InvestigationClarificationRequired, false: InvestigationNoMatch} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				request := validInvestigationRequest()
				request.Options.AllowClarification = allowClarification
				engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{candidate}, Committed: []SubjectRef{}, CensusLimitation: sentence}}, &recordingTelemetry{})
				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha", "acme/beta"}}, request)
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if result.Status != status {
					t.Fatalf("status = %q, want %q", result.Status, status)
				}
				if !slices.Contains(result.Limitations, sentence) {
					t.Fatalf("limitations %q lack the count", result.Limitations)
				}
				for _, text := range append(slices.Clone(result.Limitations), result.DeterministicAnswer) {
					if strings.Contains(text, "One authorized subject") || strings.Contains(text, "more than one authorized subject") {
						t.Fatalf("%q contradicts the count: %q", text, result.Limitations)
					}
				}
			})
		}
	}
}

var provenAbsences = map[string]string{
	"all you can read":    contractsv1.ContextFabricCensusAbsentLimitation,
	"the requested scope": contractsv1.ContextFabricCensusAbsentInScopeLimitation,
	"a restricted item":   contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation,
}

// CHAOS-12498: a complete zero is the terminal's own sentence on every arm of an
// empty pool. The ticket's three false statements are named.
func TestAProvenAbsenceIsTheTerminalSentence(t *testing.T) {
	t.Parallel()
	for name, sentence := range provenAbsences {
		for arm, resolution := range map[string]SubjectResolution{
			"an empty pool":          {Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence},
			"the exclusion's prompt": {Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt},
		} {
			for _, allowClarification := range []bool{true, false} {
				t.Run(name+" "+arm, func(t *testing.T) {
					t.Parallel()
					request := validInvestigationRequest()
					request.Options.AllowClarification = allowClarification
					engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: resolution}, &recordingTelemetry{})
					result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
					if err != nil {
						t.Fatalf("Investigate() error = %v", err)
					}
					if result.Status != InvestigationNoMatch {
						t.Fatalf("status = %q, want no_match: nothing holds the handle", result.Status)
					}
					if !slices.Equal(result.Limitations, []string{sentence}) {
						t.Fatalf("limitations = %q, want the sentence alone", result.Limitations)
					}
					if strings.Contains(result.DeterministicAnswer, "semantic similarity") || strings.Contains(result.DeterministicAnswer, "authorized subject") {
						t.Fatalf("answer = %q", result.DeterministicAnswer)
					}
				})
			}
		}
	}
}

func TestAProvenAbsenceGoesAheadOfTheFloor(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricCensusAbsentLimitation
	engine := mustEngineForTerminalReasonTest(t, flooredGraphReader{authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence}}}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch || !slices.Equal(result.Limitations, []string{sentence}) {
		t.Fatalf("status %q limitations %q", result.Status, result.Limitations)
	}
}

// Near matches stay beside a zero (ruling 1), and "the question matched" stays
// away from them: the census proved that no subject holds the handle.
func TestNearMatchesBesideAZeroStateTheZero(t *testing.T) {
	t.Parallel()
	near := func(id, label string) SubjectCandidate {
		return SubjectCandidate{ReceiptID: "receipt_" + id + "_a1", Subject: SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:r:" + id, Label: label}, State: ResolutionAmbiguous, MatchReasons: []string{"search"}, Confidence: 0.5}
	}
	for name, candidates := range map[string][]SubjectCandidate{
		"one":     {near("5", "Release prep")},
		"several": {near("5", "Release prep"), near("6", "Release notes")},
	} {
		for allowClarification, status := range map[bool]InvestigationStatus{true: InvestigationClarificationRequired, false: InvestigationNoMatch} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sentence := contractsv1.ContextFabricCensusAbsentLimitation
				request := validInvestigationRequest()
				request.Options.AllowClarification = allowClarification
				engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}, CensusLimitation: sentence}}, &recordingTelemetry{})
				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if result.Status != status || len(result.SubjectResolution.Candidates) != len(candidates) {
					t.Fatalf("status %q candidates %d, want %q and %d", result.Status, len(result.SubjectResolution.Candidates), status, len(candidates))
				}
				if !slices.Equal(result.Limitations, []string{sentence}) {
					t.Fatalf("limitations = %q, want the sentence alone", result.Limitations)
				}
				if strings.Contains(result.DeterministicAnswer, "authorized subject") {
					t.Fatalf("answer = %q", result.DeterministicAnswer)
				}
			})
		}
	}
}

// A count is listed or it is not, so several candidates beside one keep their sentence.
func TestThePartListingKeepsItsTerminalSentence(t *testing.T) {
	t.Parallel()
	first := SubjectCandidate{ReceiptID: "receipt_a_a1", Subject: SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:r:12", Label: "pull request #12 in acme/alpha"}, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5}
	second := SubjectCandidate{ReceiptID: "receipt_b_a1", Subject: SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:s:12", Label: "pull request #12 in acme/beta"}, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5}
	request := validInvestigationRequest()
	request.Options.AllowClarification = false
	engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{first, second}, Committed: []SubjectRef{}, CensusLimitation: contractsv1.ContextFabricCensusListedLimitation(10, 2)}}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha", "acme/beta"}}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !slices.Contains(result.Limitations, ambiguousNoClarificationLimitation) || !strings.Contains(result.DeterministicAnswer, "more than one authorized subject matched") {
		t.Fatalf("limitations %q answer %q", result.Limitations, result.DeterministicAnswer)
	}
}

// A graph that was never projected is a stronger claim: retrieval did not run.
func TestAnUnprojectedGraphGoesAheadOfAnAbsence(t *testing.T) {
	t.Parallel()
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, GraphNotProjected: true, CensusLimitation: contractsv1.ContextFabricCensusAbsentLimitation}
	engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: resolution}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !slices.Contains(result.Limitations, noMatchLimitationGraphNotProjected) {
		t.Fatalf("limitations = %q", result.Limitations)
	}
	if got := subjectlessTerminalReason(FrameGate{}, resolution, 0, declaredKindDecision{}); got != "graph_not_projected" {
		t.Fatalf("reason = %q", got)
	}
}

// The logs tell a proven absence from a count nothing could list.
func TestAnAbsenceHasItsOwnTerminalReason(t *testing.T) {
	t.Parallel()
	for name, sentence := range provenAbsences {
		resolution := SubjectResolution{CensusLimitation: sentence, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt}
		if got := subjectlessTerminalReason(FrameGate{}, resolution, 0, declaredKindDecision{}); got != censusAbsentTerminalReason {
			t.Fatalf("%s: reason = %q", name, got)
		}
	}
	if !slices.Contains(SubjectlessTerminalReasonVocabulary(), censusAbsentTerminalReason) {
		t.Fatalf("%q is outside the vocabulary", censusAbsentTerminalReason)
	}
}
