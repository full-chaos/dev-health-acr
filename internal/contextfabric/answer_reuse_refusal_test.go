package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// resolveFailingGraph is a graph whose subject recheck returns an error.
type resolveFailingGraph struct{ reuseSubjectGraph }

func (g resolveFailingGraph) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection, *QuestionFrame, SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, errors.New("graph read failed")
}

// reuseSubjects returns n distinct project subjects, the first being the
// subject reusableCandidate names.
func reuseSubjects(n int) []SubjectRef {
	subjects := make([]SubjectRef, 0, n)
	for index := 0; index < n; index++ {
		subjects = append(subjects, SubjectRef{Kind: SubjectProject, CanonicalID: fmt.Sprintf("project_ask_dev_%03d", index), Label: fmt.Sprintf("Ask Dev %03d", index)})
	}
	return subjects
}

// TestEveryAnswerReuseRefusalNamesItself (CHAOS-12511) runs the real
// Engine.Investigate -> tryReuse path once per refusal and requires the
// outcome to stay miss_authorization while the reason tells the refusals
// apart. The stored-result gate admits the subject cap case: only the reason
// separates a count limit from a grant decision.
func TestEveryAnswerReuseRefusalNamesItself(t *testing.T) {
	t.Parallel()
	reader := ownerPrincipal("user_a", "acme/tools")
	other := ownerPrincipal("user_b", "acme/tools")
	type scenario struct {
		name      string
		subjects  int
		committed int
		submitted bool
		owner     *SubmissionOwner
		graph     func(graphReaderStub) GraphReader
		hit       bool
		want      AnswerReuseRefusal
	}
	admitted := func(g graphReaderStub) GraphReader {
		return reuseSubjectGraph{graphReaderStub: g, outcome: StoredSubjectAdmitted}
	}
	scenarios := []scenario{
		{name: "50 subjects are rechecked and served", subjects: 50, committed: 50, graph: admitted, hit: true},
		{name: "51 subjects exceed the recheck cap", subjects: 51, committed: 51, graph: admitted, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectCapExceeded, SubjectCount: 51}},
		{name: "54 subjects exceed the recheck cap", subjects: 54, committed: 54, graph: admitted, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectCapExceeded, SubjectCount: 54}},
		{name: "gate denies the candidate", subjects: 1, committed: 1, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalGateDenied},
			graph: func(g graphReaderStub) GraphReader {
				return reuseSubjectGraph{graphReaderStub: g, outcome: StoredSubjectDenied}
			}},
		{name: "submission turn, candidate has no owner", subjects: 1, committed: 1, submitted: true, graph: admitted, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalSubmissionOwnerMissing}},
		{name: "submission turn, candidate has another owner", subjects: 1, committed: 1, submitted: true, owner: ownerOf(t, other), graph: admitted, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalSubmissionOwnerMismatch}},
		{name: "subject recheck fails", subjects: 1, committed: 1, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectRecheckFailed},
			graph: func(g graphReaderStub) GraphReader {
				return resolveFailingGraph{reuseSubjectGraph{graphReaderStub: g, outcome: StoredSubjectAdmitted}}
			}},
		{name: "a subject is not committed under the current grant", subjects: 3, committed: 2, graph: admitted, want: AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectNotCommitted}},
	}
	seen := map[AnswerReuseRefusalReason]string{}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			_, candidate := reusableCandidate()
			subjects := reuseSubjects(tc.subjects)
			candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: subjects}
			candidate.SubjectResolution.CommitDecisionDigests = identityProvenDigests(subjects...)
			telemetry := &recordingTelemetry{}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					return InterpretedQuestion{}, errors.New("fresh investigation reached")
				}),
				Graph:     tc.graph(graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: subjects[:tc.committed]}}),
				Results:   &resultStoreStub{},
				Telemetry: telemetry,
				ReuseGate: optionalOwnerReuseGate{candidate: candidate, owner: tc.owner},
			})
			ctx := context.Background()
			if tc.submitted {
				ctx = WithReservedResultID(ctx, "result_reserved_refusal")
			}
			result, err := engine.Investigate(ctx, reader, validInvestigationRequest())
			if tc.hit != (err == nil && result.Reused) {
				t.Fatalf("reused %t err %v, want reused %t", result.Reused, err, tc.hit)
			}
			wantOutcome := AnswerReuseMissAuthorization
			if tc.hit {
				wantOutcome = AnswerReuseHit
			}
			if len(telemetry.answerReuseOutcomes) != 1 || telemetry.answerReuseOutcomes[0] != wantOutcome {
				t.Fatalf("outcomes = %v, want exactly [%s]", telemetry.answerReuseOutcomes, wantOutcome)
			}
			if len(telemetry.answerReuseRefusals) != 1 || telemetry.answerReuseRefusals[0] != tc.want {
				t.Fatalf("refusals = %+v, want exactly [%+v]", telemetry.answerReuseRefusals, tc.want)
			}
			if tc.want.Reason != "" {
				seen[tc.want.Reason] = tc.name
			}
		})
	}
	// Every reason in the closed set has a scenario above, so none can be
	// added to the vocabulary without a test that tells it from the rest.
	t.Cleanup(func() {
		for _, reason := range AnswerReuseRefusalReasonVocabulary() {
			if _, ok := seen[AnswerReuseRefusalReason(reason)]; !ok {
				t.Errorf("refusal reason %q has no scenario on the real path", reason)
			}
		}
	})
}
