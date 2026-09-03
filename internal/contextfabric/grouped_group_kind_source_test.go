package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// A cohort group's Subject.Kind was stamped from plan.GroupKind -- the MODEL's
// own frame -- while its CanonicalID and Label came from a canonical fact row.
// The two halves of a subject identity therefore had different provenance, and
// only one of them was trustworthy.
//
// That was harmless while group subjects were uncitable. Admitting them (the
// change this file ships alongside) made it a live forgery route: a frame
// declaring group_kind=repository over rows that are unambiguously TEAM rows
// mints {kind: repository, canonical_id: team_security} -- an identity that
// exists in no source -- and a synthesis driver naming it would then pass
// validation, because the allow-set admits whatever the grouping built.
//
// The extractor already knows the true kind and always did:
// groupAssignmentsFromValue accepts a row only when its scope column says
// "team", and reads team_id/team_name. Every assignment it produces is a team
// by construction. The kind was simply discarded and re-stamped from the plan.
func teamScopedFact(memberID, teamID, teamLabel string) CanonicalFact {
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	return CanonicalFact{
		Kind:    FactMetrics,
		Subject: SubjectRef{Kind: SubjectProject, CanonicalID: memberID, Label: memberID},
		Fields: map[string]FactValue{
			"team_breakdown": {Rows: []FactValueRow{{Fields: map[string]FactValue{
				"scope": str("team"), "team_id": str(teamID), "team_name": str(teamLabel),
			}}}},
		},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

// TestGroupingRefusesWhenThePlanKindDisagreesWithTheFactSource is the pinning
// test for the forgery route. The rows are team rows; the plan says
// repository; nothing may be built.
func TestGroupingRefusesWhenThePlanKindDisagreesWithTheFactSource(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{teamScopedFact("project_a", "team_security", "Security")}
	groups, _, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectRepository}, planFixtureCohort("project_a"), facts)
	if len(groups) != 0 {
		t.Fatalf("built %d group(s) with kind %q from TEAM rows -- a subject whose kind the model chose and whose id the source chose is a forged identity",
			len(groups), groups[0].Subject.Kind)
	}
	if outcome.Refusal != CohortGroupingRefusalGroupKindSourceMismatch {
		t.Fatalf("refusal = %q, want %q -- a refusal an operator cannot see is a grouped question that silently came back flat",
			outcome.Refusal, CohortGroupingRefusalGroupKindSourceMismatch)
	}
	if outcome.PlannedKind != SubjectRepository || outcome.SourceKind != SubjectTeam {
		t.Fatalf("outcome kinds = planned %q / source %q, want repository / team -- the disclosure names both, so both must be carried",
			outcome.PlannedKind, outcome.SourceKind)
	}
}

// TestGroupingStillBuildsWhenThePlanKindMatchesTheSource is the other half:
// the refusal must be a KIND check, not a blanket refusal that would break
// every grouped answer.
func TestGroupingStillBuildsWhenThePlanKindMatchesTheSource(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{teamScopedFact("project_a", "team_security", "Security")}
	groups, _, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectTeam}, planFixtureCohort("project_a"), facts)
	if outcome.Refusal != CohortGroupingRefusalNone {
		t.Fatalf("refusal = %q on a matching kind, want none", outcome.Refusal)
	}
	if len(groups) != 1 {
		t.Fatalf("built %d groups, want 1 -- a matching kind must still group", len(groups))
	}
	if groups[0].Subject.Kind != SubjectTeam {
		t.Fatalf("group subject kind = %q, want %q from the fact source", groups[0].Subject.Kind, SubjectTeam)
	}
	if groups[0].Subject.CanonicalID != "team_security" {
		t.Fatalf("group canonical id = %q, want the source's own", groups[0].Subject.CanonicalID)
	}
}

// TestTheForgedGroupSubjectIsNotCitable closes the loop the refusal exists
// for. Refusing to BUILD the group is only half the guarantee; what matters is
// that the fabricated identity cannot reach a served answer. Because grouping
// refused, the cohort carries no groups, so the subject allow-set never learns
// {repository, team_security} and a driver naming it is rejected.
//
// Asserted end to end rather than by inspecting the allow-set, because the
// allow-set is exactly the thing under test.
func TestTheForgedGroupSubjectIsNotCitable(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{teamScopedFact("project_a", "team_security", "Security")}
	groups, _, _ := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectRepository}, planFixtureCohort("project_a"), facts)

	input, draft, _ := groupedCohortFixture()
	cohort := *input.Graph.Cohort
	cohort.Groups = groups // nil, because grouping refused
	input.Graph.Cohort = &cohort
	forged := SubjectRef{Kind: SubjectRepository, CanonicalID: "team_security", Label: "Security"}
	draft.Drivers[0].AffectedSubjects = []SubjectRef{forged}

	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil -- a subject whose kind came from the model and whose id came from a fact row must never be citable")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSubjectOutOfScope)
	}
	basis, ok := SynthesisSubjectScopeBasisOf(err)
	if !ok || basis != SubjectScopeAbsentFromPayload {
		t.Fatalf("basis = %q (present=%v), want %q -- the forged subject reaches no payload source, so it is the model's error and not an ACR display/validate defect",
			basis, ok, SubjectScopeAbsentFromPayload)
	}
}

// EQUIVALENT MUTANT, recorded rather than papered over. Reverting the stamp at
// the group-construction site to `Kind: plan.GroupKind` SURVIVES this battery,
// and that is correct rather than a gap: the refusal above returns before any
// group is built whenever an assignment's kind differs from the plan's, so on
// every path that reaches the stamp the two expressions are equal by
// construction. No test can distinguish them without first removing the
// refusal.
//
// What that means for a maintainer, and why it is written down: the safety
// property here is carried by the REFUSAL, not by the stamp. The stamp reads
// from the source because that is the honest expression of where a kind comes
// from, but it is defence in depth, not the defence. Weaken the refusal and
// the forgery route reopens, which is what
// TestGroupingRefusesWhenThePlanKindDisagreesWithTheFactSource exists to stop.

// TestAMismatchNeverYieldsAFlatCohortWithoutDisclosure is the ruling this
// change exists to satisfy, stated as the thing that must never happen rather
// than as the thing that should.
//
// A reader who asked for a per-team breakdown and silently received a flat
// list has been told something false by omission, and telemetry does not fix
// that: the operator reading logs is not the person reading the answer. So a
// refusal must reach the WIRE. The assertion is deliberately on the absence of
// silence -- any answer carrying no group axis after a mismatch must carry a
// limitation naming both kinds -- so a future composer that drops the sentence
// fails here rather than shipping a quietly different answer.
func TestAMismatchNeverYieldsAFlatCohortWithoutDisclosure(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{teamScopedFact("project_a", "team_security", "Security")}
	groups, _, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectRepository}, planFixtureCohort("project_a"), facts)
	if len(groups) != 0 {
		t.Fatal("fixture drift: the mismatch must refuse to group")
	}

	result := InvestigationResult{Status: InvestigationPartial}
	applyGroupingRefusalDisclosure(&result, outcome)

	if len(result.Limitations) == 0 {
		t.Fatal("a grouped question answered ungrouped carries NO limitation -- the caller is told nothing, which is the silent flattening this ruling forbids")
	}
	limitation := result.Limitations[0]
	for _, want := range []string{string(SubjectRepository), string(SubjectTeam)} {
		if !strings.Contains(limitation, want) {
			t.Fatalf("limitation %q does not name %q -- the disclosure must say which axis was asked for and which the facts support", limitation, want)
		}
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial is false on an answer that dropped its group axis")
	}
}

// TestAGroupedAnswerCarriesNoRefusalDisclosure is the negative control: the
// sentence must appear ONLY on a refusal, or every grouped answer acquires a
// limitation about something that did not happen.
func TestAGroupedAnswerCarriesNoRefusalDisclosure(t *testing.T) {
	t.Parallel()
	result := InvestigationResult{Status: InvestigationComplete}
	applyGroupingRefusalDisclosure(&result, CohortGroupingOutcome{})
	if len(result.Limitations) != 0 {
		t.Fatalf("limitations = %v on a request that never refused", result.Limitations)
	}
	if result.Coverage.Partial {
		t.Fatal("Coverage.Partial set on a request that never refused")
	}
}

// TestTheRefusalDisclosureIsWiredIntoAssembly pins the CALL SITE, not the
// composer.
//
// Every other test in this file drives applyGroupingRefusalDisclosure
// directly, so deleting its invocation from the assembly path left them all
// green -- proved by running that mutation, which SURVIVED. A composer no one
// calls discloses nothing, and "the function works" is not the property that
// matters to a reader of the answer.
//
// Read from source, in the manner limitation_append_closure_test.go already
// establishes for this package, because the alternative is standing up a full
// assembly fixture to observe one call.
func TestTheRefusalDisclosureIsWiredIntoAssembly(t *testing.T) {
	t.Parallel()
	const path = "chaos4636_synthesis_assembly.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	called := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "applyGroupingRefusalDisclosure" {
			called = true
		}
		return true
	})
	if !called {
		t.Fatalf("%s never calls applyGroupingRefusalDisclosure -- a grouped question answered ungrouped would disclose nothing to the caller, however well the composer itself is tested", path)
	}
	// Positive control: the checker must be able to SEE a call in this file,
	// or a rename of the sibling composers would make the assertion above
	// vacuously... unfindable rather than false.
	sibling := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "applyFactScopeDisclosure" {
			sibling = true
		}
		return true
	})
	if !sibling {
		t.Fatal("control failed: the parser found no applyFactScopeDisclosure call either, so this file is not the assembly path any more and this test is measuring nothing")
	}
}
