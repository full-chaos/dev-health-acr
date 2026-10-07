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
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), noMemberFoundFacts(SubjectProject), result))
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
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), noMemberFoundFacts(SubjectTeam), result))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row names the subject's own kind", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsNonTeamSubjectDespiteNoMemberFoundRow(t *testing.T) {
	result := affirmationResult()
	if kind := result.SubjectResolution.Committed[0].Kind; kind == SubjectTeam {
		t.Fatalf("fixture subject kind = %q, want a non-team kind", kind)
	}
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), noMemberFoundFacts(SubjectProject), result))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want the non-team subject retracted", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsTeamAnchorWhenTheRowIsAnotherCode(t *testing.T) {
	result := teamCommitResult()
	facts := noMemberFoundFacts(SubjectProject)
	facts.Coverage.Details[0].Code = contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), facts, result))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row is not a none-found row", result.SubjectResolution.Committed)
	}
}

func TestCommitAffirmationRetractsTeamAnchorWhenTheRowNamesNoKind(t *testing.T) {
	result := teamCommitResult()
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), noMemberFoundFacts(""), result))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want retracted: the row names no member kind", result.SubjectResolution.Committed)
	}
}
