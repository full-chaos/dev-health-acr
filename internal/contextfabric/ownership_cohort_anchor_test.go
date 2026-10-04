package contextfabric

import (
	"fmt"
	"testing"
)

// The ownership anchor's truth table is the specification of OwnershipAnchor.
// The rule in words (the function's own doc comment states it in full):
//
//   - Only a team-members frame whose reading declares no anchor kind or
//     declares repository can have an ownership anchor.
//   - The anchor is the one committed repository an anchor term names, when
//     its commit is an identity proof (routed, bound) or when it is the only
//     committed subject (routed, sole_commit).
//   - With no repository named by an anchor term, a repository the caller
//     named by its id is the anchor (routed, bound) when it is the only
//     committed repository and nothing committed matched an anchor term.
//   - Two named repositories, or a non-repository subject an anchor term
//     names under a reading that declares no kind, mean no anchor.
//
// Every row of the cross product below is listed. A repository in a committed
// set carries the row's commit and term match; the project in the
// "repository + project" set is the project the question's anchor term names,
// committed by its label.

// ownershipVariant is how every repository of a row was committed.
type ownershipVariant struct {
	name   string
	basis  CommitBasis
	hint   bool
	termed bool
}

// ownershipVariants are the commit shapes a repository reaches the committed
// set in. A commit that came from the caller's hint is always on the caller's
// id, so "statistical from a hint" does not exist and is not a row.
var ownershipVariants = []ownershipVariant{
	{"hint, term matched", CommitBasisCallerCanonicalID, true, true},
	{"hint, no term matched", CommitBasisCallerCanonicalID, true, false},
	{"identity, term matched", CommitBasisAuthoritativeIdentity, false, true},
	{"identity, no term matched", CommitBasisAuthoritativeIdentity, false, false},
	{"statistical, term matched", CommitBasisStatistical, false, true},
	{"statistical, no term matched", CommitBasisStatistical, false, false},
}

// ownershipTruthTable holds, per declared anchor kind and committed set, one
// character per variant in ownershipVariants' order: 'B' routed to the first
// repository on a bound basis, 'S' routed to it as the sole commit, '-' not
// routed.
var ownershipTruthTable = map[string]map[string]string{
	"":           {"none": "------", "repository": "BBB-S-", "project": "------", "repository + project": "------", "two repositories": "------"},
	"repository": {"none": "------", "repository": "BBB-S-", "project": "------", "repository + project": "B-B---", "two repositories": "------"},
	"project":    {"none": "------", "repository": "------", "project": "------", "repository + project": "------", "two repositories": "------"},
	"team":       {"none": "------", "repository": "------", "project": "------", "repository + project": "------", "two repositories": "------"},
	"incident":   {"none": "------", "repository": "------", "project": "------", "repository + project": "------", "two repositories": "------"},
}

func TestOwnershipAnchorTruthTable(t *testing.T) {
	frame := frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, func() SubjectExpression {
		expression := scopedExpression(SubjectTeam)
		expression.Scoped.AnchorTerms = []string{"Acme/API"}
		return expression
	}())
	repository := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:github:acme/api", Label: "acme/api"}
	other := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:github:acme/web", Label: "acme/web"}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:api", Label: "api"}
	sets := map[string][]SubjectRef{
		"none": nil, "repository": {repository}, "project": {project},
		"repository + project": {repository, project}, "two repositories": {repository, other},
	}
	rows := 0
	for declared, bySet := range ownershipTruthTable {
		for setName, expected := range bySet {
			if len(expected) != len(ownershipVariants) {
				t.Fatalf("table row %q/%q has %d cells for %d variants", declared, setName, len(expected), len(ownershipVariants))
			}
			for i, variant := range ownershipVariants {
				rows++
				resolution := SubjectResolution{Committed: append([]SubjectRef(nil), sets[setName]...)}
				bases := CommitBasisSet{}
				for _, subject := range resolution.Committed {
					terms := []string{"acme/api"}
					basis := variant.basis
					if subject.Kind == SubjectProject {
						basis = CommitBasisStatistical
					} else if !variant.termed {
						terms = []string{"something else"}
					}
					bases.Record(subject, basis)
					resolution.Candidates = append(resolution.Candidates, SubjectCandidate{Subject: subject, State: ResolutionCommitted, MatchedTerms: terms})
				}
				anchor, basis := OwnershipAnchor(frame, SubjectKind(declared), resolution, bases)
				var got byte = '-'
				switch basis {
				case OwnershipAnchorBound:
					got = 'B'
				case OwnershipAnchorSoleCommit:
					got = 'S'
				}
				row := fmt.Sprintf("declared %q, committed %s, %s", declared, setName, variant.name)
				if got != expected[i] {
					t.Errorf("%s: got %q, want %q", row, got, expected[i])
					continue
				}
				if got != '-' && anchor != repository {
					t.Errorf("%s: anchor = %+v, want the first repository", row, anchor)
				}
				if got == '-' && anchor != (SubjectRef{}) {
					t.Errorf("%s: anchor = %+v with no routing", row, anchor)
				}
			}
		}
	}
	if want := 5 * 5 * len(ownershipVariants); rows != want {
		t.Fatalf("checked %d rows, want the whole cross product of %d", rows, want)
	}
}

// TestOwnershipAnchorNeedsATeamMembersFrameAndALabel covers the frame and the
// label, which the table holds fixed.
func TestOwnershipAnchorNeedsATeamMembersFrameAndALabel(t *testing.T) {
	repository := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:github:acme/api", Label: "acme/api"}
	resolution := func(subject SubjectRef) SubjectResolution {
		return SubjectResolution{Committed: []SubjectRef{subject}, Candidates: []SubjectCandidate{{Subject: subject, State: ResolutionCommitted, MatchedTerms: []string{"acme/api"}}}}
	}
	scoped := func(member SubjectKind) *QuestionFrame {
		expression := scopedExpression(member)
		expression.Scoped.AnchorTerms = []string{"acme/api"}
		return frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, expression)
	}
	discovered := frameWithPointer([]InvestigationGoal{GoalCountOrAggregate}, SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}})
	// A scoped payload under another expression kind is not a scoped frame.
	stray := scoped(SubjectTeam)
	stray.SubjectExpression.Kind = SubjectExpressionDiscoveredKind
	unlabelled := repository
	unlabelled.Label = ""
	for name, c := range map[string]struct {
		frame   *QuestionFrame
		subject SubjectRef
	}{
		"nil frame":              {nil, repository},
		"deployment member kind": {scoped(SubjectDeployment), repository},
		"discovered team kind":   {discovered, repository},
		"scoped payload under another expression kind": {stray, repository},
		"repository with no label":                     {scoped(SubjectTeam), unlabelled},
	} {
		if _, basis := OwnershipAnchor(c.frame, "", resolution(c.subject), nil); basis != OwnershipAnchorNone {
			t.Errorf("%s: basis = %q, want none", name, basis)
		}
	}
	if anchor, basis := OwnershipAnchor(scoped(SubjectTeam), "", resolution(repository), nil); basis != OwnershipAnchorSoleCommit || anchor != repository {
		t.Errorf("control: anchor = %+v basis = %q, want the labelled repository as the sole commit", anchor, basis)
	}
}

func TestOwnershipRoutingVersionMovedPastTheBoundOnlyRule(t *testing.T) {
	t.Parallel()
	const versionBeforeALabelNamedRepositoryRouted = "ownership-routing.v1"
	if OwnershipRoutingVersion == versionBeforeALabelNamedRepositoryRouted {
		t.Fatalf("OwnershipRoutingVersion = %q, want it moved past %q -- an answer saved when a repository named by its label never routed through ownership holds teams that do not own it and must not be reused", OwnershipRoutingVersion, versionBeforeALabelNamedRepositoryRouted)
	}
}
