package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func noMemberFoundFacts(kind SubjectKind) CanonicalFactBundle {
	bundle := emptyAffirmationFacts()
	bundle.Coverage.Details = []CoverageDetail{{
		DetailID: "cov-fact-01", Source: "context-fabric:graph", Degrading: true,
		Code: contractsv1.ContextFabricCoverageDetailGraphNoMemberFound, Kind: kind,
	}}
	return bundle
}

func scopedInputs(result InvestigationResult, facts CanonicalFactBundle, member, anchor SubjectKind) affirmationInputs {
	inputs := statisticalInputs(emptyAffirmationGraph(), facts, result)
	inputs.Frame = &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: member},
	}}
	inputs.ScopeAnchorKind = anchor
	return inputs
}

func teamCommitResult() InvestigationResult {
	result := affirmationResult()
	result.SubjectResolution.Committed = []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}}
	for i := range result.SubjectResolution.Candidates {
		result.SubjectResolution.Candidates[i].Subject = result.SubjectResolution.Committed[0]
	}
	return result
}

// A statistically committed team anchor under which the service found no
// member of the asked kind keeps its commit.
func TestCommitAffirmationKeepsTeamAnchorWhenNoMemberWasFoundUnderIt(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 1 {
		t.Fatalf("Committed = %v, want the team anchor kept", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsTeamAnchorWithoutNoMemberFoundRow(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want the unaffirmed team retracted", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsSubjectOfTheRowsOwnKind(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectTeam), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row names the subject's own kind", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsNonTeamSubjectDespiteNoMemberFoundRow(t *testing.T) {
	result := affirmationResult()
	if kind := result.SubjectResolution.Committed[0].Kind; kind == SubjectTeam {
		t.Fatalf("fixture subject kind = %q, want a non-team kind", kind)
	}
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want the non-team subject retracted", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsTeamAnchorWhenTheRowIsAnotherCode(t *testing.T) {
	result := teamCommitResult()
	facts := noMemberFoundFacts(SubjectProject)
	facts.Coverage.Details[0].Code = contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned
	applyCommitAffirmation(&result, scopedInputs(result, facts, SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row is not a none-found row", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsTeamAnchorWhenTheRowNamesNoKind(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(""), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row names no member kind", result.SubjectResolution.Committed)
	}
}

// The row names a kind the question did not declare.
func TestCommitAffirmationRetractsTeamAnchorWhenTheRowIsForAnotherKindThanDeclared(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectRepository), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row is for repository, the question declared project", result.SubjectResolution.Committed)
	}
}

// No declared scope, or a scope anchor that is not this subject's kind.
func TestCommitAffirmationRetractsTeamWhenItIsNotTheDeclaredScopeAnchor(t *testing.T) {
	noFrame := teamCommitResult()
	inputs := scopedInputs(noFrame, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam)
	inputs.Frame = nil
	applyCommitAffirmation(&noFrame, inputs)
	if len(noFrame.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted without a declared scope", noFrame.SubjectResolution.Committed)
	}
	otherAnchor := teamCommitResult()
	applyCommitAffirmation(&otherAnchor, scopedInputs(otherAnchor, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectRepository))
	if len(otherAnchor.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted when the scope anchor is a repository", otherAnchor.SubjectResolution.Committed)
	}
}

// The exemption is the team anchor's; another anchor kind keeps the gate.
func TestCommitAffirmationRetractsNonTeamAnchorDespiteItsNoMemberFoundRow(t *testing.T) {
	result := affirmationResult()
	kind := result.SubjectResolution.Committed[0].Kind
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectRepository), SubjectRepository, kind))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want a %s anchor retracted: only a team anchor is exempt", result.SubjectResolution.Committed, kind)
	}
}

// A question that declares no member kind is matched by no row.
func TestCommitAffirmationRetractsTeamAnchorWhenNoMemberKindWasDeclared(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(""), "", SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: no declared member kind", result.SubjectResolution.Committed)
	}
}
