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

// namedMatch is a candidate that claims the name by an identity mechanism, in
// the shape graphrank builds it: the retrieving term, the identity mechanism
// plus the lexical one the search arm adds, a reason and a confidence.
func namedMatch(subject SubjectRef, matched ...string) SubjectCandidate {
	return identityMatch(subject, MatchAlias, matched...)
}

func identityMatch(subject SubjectRef, identity MatchMechanism, matched ...string) SubjectCandidate {
	if len(matched) == 0 {
		matched = []string{"a"}
	}
	return SubjectCandidate{
		ReceiptID: "receipt_" + strings.ReplaceAll(subject.CanonicalID, ":", "_"),
		Subject:   subject, State: ResolutionProposed,
		MatchedTerms: matched, MatchReasons: []string{"Repository/project alias matched."}, Confidence: 1,
		EvidenceRefIDs: []string{"evidence_identity_1234"}, MatchMechanisms: []MatchMechanism{identity, MatchLexical},
	}
}

// lexicalNeighbour is a subject that retrieval offered for a word it shares
// with the label: it carries the term that retrieved it and only the lexical
// mechanism, as graphrank builds it for a partial match.
func lexicalNeighbour(subject SubjectRef, confidence float64) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID: "receipt_" + strings.ReplaceAll(subject.CanonicalID, ":", "_"),
		Subject:   subject, State: ResolutionProposed,
		MatchedTerms: []string{"a"}, MatchReasons: []string{"Hybrid graph search matched the subject label or indexed context."},
		Confidence: confidence, EvidenceRefIDs: []string{"evidence_identity_1234"}, MatchMechanisms: []MatchMechanism{MatchLexical},
	}
}

// prodShapeFault names the first candidate that no retrieval pass could have
// built: one with no retrieving term or no mechanism.
func prodShapeFault(resolution SubjectResolution) string {
	for _, candidate := range resolution.Candidates {
		if len(candidate.MatchedTerms) == 0 || len(candidate.MatchMechanisms) == 0 {
			return candidate.ReceiptID
		}
	}
	return ""
}

func TestProdShapeGuardRejectsACandidateWithoutTermOrMechanism(t *testing.T) {
	t.Parallel()
	subject := singleSubjectOf(SubjectRepository)
	other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}
	if fault := prodShapeFault(SubjectResolution{Candidates: []SubjectCandidate{namedMatch(subject), lexicalNeighbour(other, 0.75)}}); fault != "" {
		t.Fatalf("a prod-shaped resolution was rejected: %s", fault)
	}
	if prodShapeFault(SubjectResolution{Candidates: []SubjectCandidate{scopeCandidate(other, "receipt_other")}}) == "" {
		t.Fatal("a candidate with no term and no mechanism passed the guard")
	}
	noMechanism := lexicalNeighbour(other, 0.75)
	noMechanism.MatchMechanisms = nil
	if prodShapeFault(SubjectResolution{Candidates: []SubjectCandidate{noMechanism}}) == "" {
		t.Fatal("a candidate with an empty mechanism passed the guard")
	}
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
		identity   int
	}{
		{"repository", SubjectRepository, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s)}}
		}, single, 11, 1, 1},
		{"project", SubjectProject, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s)}}
		}, single, 4, 1, 1},
		{"team", SubjectTeam, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s)}}
		}, single, 3, 1, 1},
		{"prod shape: four lexical neighbours of the same naming family", SubjectRepository, func(s SubjectRef) SubjectResolution {
			candidates := []SubjectCandidate{namedMatch(s)}
			for index, confidence := range []float64{0.75, 0.75, 0.667, 0.667} {
				candidates = append(candidates, lexicalNeighbour(SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:NEIGHBOUR_" + string(rune('A'+index)), Label: "neighbour"}, confidence))
			}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: candidates}
		}, single, 6, 5, 1},
		{"prod shape: the full label commits by exact", SubjectRepository, func(s SubjectRef) SubjectResolution {
			other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:OTHER", Label: "other"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{identityMatch(s, MatchExact), lexicalNeighbour(other, 0.75)}}
		}, single, 6, 2, 1},
		{"prod shape: a lexical neighbour of another kind", SubjectRepository, func(s SubjectRef) SubjectResolution {
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:NEIGHBOUR", Label: "neighbour"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s), lexicalNeighbour(project, 0.667)}}
		}, single, 11, 2, 1},
		{"ambiguous label: a project carries it too", SubjectRepository, func(s SubjectRef) SubjectResolution {
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:NAMED_ONE", Label: "named one"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s), namedMatch(project)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2, 2},
		{"ambiguous label: a team carries it by provider key", SubjectRepository, func(s SubjectRef) SubjectResolution {
			team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:NAMED_ONE", Label: "named one"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s), identityMatch(team, MatchProviderKey)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2, 2},
		{"ambiguous label: a second repository carries it", SubjectRepository, func(s SubjectRef) SubjectResolution {
			twin := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:TWIN", Label: "named one"}
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{identityMatch(s, MatchExact), identityMatch(twin, MatchExact)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2, 2},
		{"two committed subjects of the member kind", SubjectRepository, func(s SubjectRef) SubjectResolution {
			second := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:SECOND", Label: "second"}
			return SubjectResolution{Committed: []SubjectRef{s, second}, Candidates: []SubjectCandidate{namedMatch(s), namedMatch(second)}}
		}, CountPopulationScopeAnchorUnresolved, 11, 2, 2},
		{"the committed subject did not match the anchor term", SubjectRepository, func(s SubjectRef) SubjectResolution {
			return SubjectResolution{Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{namedMatch(s, "b")}}
		}, CountPopulationScopeAnchorUnresolved, 11, 0, 0},
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
			if fault := prodShapeFault(cell.resolution); fault != "" {
				t.Fatalf("candidate %s has no retrieving term or no mechanism: no retrieval pass builds that", fault)
			}
			if event.Scope.AnchorIdentityMatches != row.identity {
				t.Errorf("anchor_identity_matches = %d, want %d", event.Scope.AnchorIdentityMatches, row.identity)
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
