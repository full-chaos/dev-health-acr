package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A metric count over one named subject has no members to count. The reading
// "members of kind K under an anchor", whose only committed subject is itself
// of kind K, is that question mis-framed, so it is answered as a question about
// the one subject and never as a member count or an unresolved anchor.

func singleSubjectOf(kind SubjectKind) SubjectRef {
	return SubjectRef{Kind: kind, CanonicalID: string(kind) + ":NAMED_ONE", Label: "named one"}
}

func namedMatch(subject SubjectRef, matched ...string) SubjectCandidate {
	candidate := scopeAnchorMatch(subject, matched...)
	candidate.ReceiptID = "receipt_" + strings.ReplaceAll(subject.CanonicalID, ":", "_")
	return candidate
}

func TestAMetricCountOverOneNamedSubjectIsNotAMemberCount(t *testing.T) {
	t.Parallel()
	const single = CountPopulationScopeDecision("single_subject")
	rows := []struct {
		name       string
		kind       SubjectKind
		resolution func(SubjectRef) SubjectResolution
		want       CountPopulationScopeDecision
		cohortSize int
		matches    int
	}{
		{"repository", SubjectRepository, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s)}}
		}, single, 11, 1},
		{"project", SubjectProject, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s)}}
		}, single, 4, 1},
		{"team", SubjectTeam, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s)}}
		}, single, 3, 1},
		{"prod shape: other repositories offered, none matched the label", SubjectRepository, func(s SubjectRef) SubjectResolution {
			other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s), scopeCandidate(other, "receipt_other")}}
		}, single, 6, 1},
		{"ambiguous label: a project carries it too", SubjectRepository, func(s SubjectRef) SubjectResolution {
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:NAMED_ONE", Label: "named one"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s), namedMatch(project)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2},
		{"ambiguous label: a second repository carries it", SubjectRepository, func(s SubjectRef) SubjectResolution {
			twin := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:TWIN", Label: "named one"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s), namedMatch(twin)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2},
		{"two committed subjects of the member kind", SubjectRepository, func(s SubjectRef) SubjectResolution {
			second := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:SECOND", Label: "second"}
			return SubjectResolution{Committed: []SubjectRef{s, second}, Candidates: []SubjectCandidate{namedMatch(s), namedMatch(second)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2},
		{"the committed subject did not match the anchor term", SubjectRepository, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s, "b")}}
		}, CountPopulationScopeAnchorUnresolved, 11, 0},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			subject := singleSubjectOf(row.kind)
			cell := scopeCell{
				frame: countingFrame(row.kind), family: QuestionFamilyScopedCohortStatus,
				resolution: row.resolution(subject), cohort: kindCohort(row.kind, row.cohortSize), status: InvestigationComplete,
			}
			telemetry := &recordingTelemetry{}
			result := runScopeCell(t, context.Background(), newScopeEngine(t, cell, telemetry))

			if len(telemetry.countPopulationScopes) != 1 {
				t.Fatalf("count population scope events = %d, want 1", len(telemetry.countPopulationScopes))
			}
			event := telemetry.countPopulationScopes[0]
			if event.Scope.Decision != row.want {
				t.Fatalf("decision = %q, want %q (%+v)", event.Scope.Decision, row.want, event.Scope)
			}
			if event.Scope.AnchorTermMatches != row.matches {
				t.Errorf("anchor_term_matches = %d, want %d", event.Scope.AnchorTermMatches, row.matches)
			}
			if claim := cardinalityClaimOf(result); claim != nil {
				t.Errorf("a member count claim was served: %+v", *claim)
			}
			if strings.Contains(result.DeterministicAnswer, "Counted ") {
				t.Errorf("the answer states a member count: %q", result.DeterministicAnswer)
			}
			rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
			if len(rows) != 1 || rows[0].Outcome != contractsv1.ContextFabricRequirementUnavailable {
				t.Errorf("assembled count rows = %+v, want one unavailable row", rows)
			}
			named := false
			for _, limitation := range result.Limitations {
				if strings.Contains(limitation, "one named subject") && contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) {
					named = true
				}
			}
			if named != (row.want == single) {
				t.Errorf("named-reason limitation present = %t, want %t (limitations %q)", named, row.want == single, result.Limitations)
			}
		})
	}
}
