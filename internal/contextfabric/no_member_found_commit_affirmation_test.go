package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
		result.SubjectResolution.Candidates[i].MatchedTerms = []string{"Platform"}
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

// Two teams committed, one matched the anchor term: only that one is kept.
func TestCommitAffirmationKeepsOnlyTheAnchorMatchedTeam(t *testing.T) {
	result := teamCommitResult()
	other := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:other", Label: "Other"}
	otherCandidate := result.SubjectResolution.Candidates[0]
	otherCandidate.Subject = other
	otherCandidate.MatchedTerms = []string{"unrelated"}
	result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, otherCandidate)
	result.SubjectResolution.Committed = append(result.SubjectResolution.Committed, other)
	var kept []SubjectRef
	inputs := scopedInputs(result, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam)
	inputs.KeptByNoMemberFound = &kept
	applyCommitAffirmation(&result, inputs)
	if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0].CanonicalID != "team:platform" {
		t.Fatalf("Committed = %v, want only team:platform", result.SubjectResolution.Committed)
	}
	if len(kept) != 1 || kept[0].CanonicalID != "team:platform" {
		t.Fatalf("kept = %v, want exactly the anchor-matched team", kept)
	}
}

// One committed team no candidate matched by term is the anchor by being sole.
func TestCommitAffirmationKeepsTheSoleCommittedTeamWhenNoTermMatched(t *testing.T) {
	result := teamCommitResult()
	for i := range result.SubjectResolution.Candidates {
		result.SubjectResolution.Candidates[i].MatchedTerms = nil
	}
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 1 {
		t.Fatalf("Committed = %v, want the sole team kept", result.SubjectResolution.Committed)
	}
}

// Several committed teams, none matched: none is the anchor.
func TestCommitAffirmationRetractsEveryTeamWhenSeveralAndNoneMatched(t *testing.T) {
	result := teamCommitResult()
	other := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:other", Label: "Other"}
	second := result.SubjectResolution.Candidates[0]
	second.Subject = other
	result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, second)
	for i := range result.SubjectResolution.Candidates {
		result.SubjectResolution.Candidates[i].MatchedTerms = nil
	}
	result.SubjectResolution.Committed = append(result.SubjectResolution.Committed, other)
	applyCommitAffirmation(&result, scopedInputs(result, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam))
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want both retracted", result.SubjectResolution.Committed)
	}
}

func TestCommitGateVersionNamesTheNoMemberFoundRule(t *testing.T) {
	if CommitGateVersion != "cg_v5" {
		t.Fatalf("CommitGateVersion = %q, want cg_v5", CommitGateVersion)
	}
}

type keptSink struct {
	SlogEngineTelemetry
	kinds []SubjectKind
}

func (k *keptSink) RecordCommitKeptByNoMemberFound(_ context.Context, _ storage.Principal, kind SubjectKind) {
	k.kinds = append(k.kinds, kind)
}

func TestRecordCommitKeptByNoMemberFoundEmitsOneEventPerKeptSubject(t *testing.T) {
	sink := &keptSink{}
	engine := &Engine{telemetry: sink}
	engine.recordCommitKeptByNoMemberFound(context.Background(), storage.Principal{OrgID: "o"}, []SubjectRef{{Kind: SubjectTeam, CanonicalID: "t"}})
	engine.recordCommitKeptByNoMemberFound(context.Background(), storage.Principal{OrgID: "o"}, nil)
	if len(sink.kinds) != 1 || sink.kinds[0] != SubjectTeam {
		t.Fatalf("kinds = %v, want one team event", sink.kinds)
	}
}

// A committed non-team subject matched by the anchor term does not make the
// one unmatched committed team a non-anchor.
func TestScopeAnchorTeamsIgnoresNonTeamSubjectsMatchedByTheAnchorTerm(t *testing.T) {
	result := teamCommitResult()
	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:platform", Label: "platform"}
	candidate := result.SubjectResolution.Candidates[0]
	candidate.Subject = repo
	result.SubjectResolution.Candidates[0].MatchedTerms = nil
	result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, candidate)
	result.SubjectResolution.Committed = append(result.SubjectResolution.Committed, repo)
	inputs := scopedInputs(result, noMemberFoundFacts(SubjectProject), SubjectProject, SubjectTeam)
	anchors := ScopeAnchorTeams(inputs.Frame, inputs.ScopeAnchorKind, result.SubjectResolution)
	if len(anchors) != 1 || anchors[0].Kind != SubjectTeam {
		t.Fatalf("anchors = %v, want the one committed team", anchors)
	}
}
