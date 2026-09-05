package contextfabric

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE DEFECT THESE PIN. BuildCohortGroups returned a ZERO-VALUED
// CohortGroupingOutcome when the plan declared a group axis and not one member
// could be placed on it, and the engine branch that consumed that case emitted
// NO TELEMETRY AT ALL -- not a line with an empty field, no line. The only
// surviving trace was `plan.GroupKind = ""`, which is exactly what a plan that
// never asked for a group axis also looks like. A reader of the persisted
// artifact therefore could not tell "the members carried no group-scoped rows"
// from "grouping was never attempted", and an operator reading logs could not
// tell either.
//
// WHAT SEPARATES THIS FROM A PARTIAL PLACEMENT, and why it needs saying twice.
// BuildCohortGroups leaving SOME members unplaced while building groups is
// deliberate, documented and pinned elsewhere in this package
// (TestBuildCohortGroupsLeavesAnUnplaceableMemberUngrouped). It is a served
// grouped answer, not a refusal. The firing condition is ZERO GROUPS BUILT,
// and TestAPartiallyGroupedCohortIsNotARefusal below is the control that keeps
// it that way -- a mutant that widens the condition to "any unplaced member"
// relabels a served answer as a refusal and puts a sentence about a failure
// that did not happen in front of the reader.

// ungroupableFact is a canonical fact whose rows carry NO group-naming column
// of any kind -- no team_id, no scope_id, no scope discriminator.
//
// This is the SILENT source, and it is the real-data shape the case exists
// for: the providers that carry the team association join on compounding risk,
// so a member whose facts came back without those rows genuinely has no
// derivable group. It is not a malformed fixture and not a mismatch; there is
// simply nothing to read, which is why the outcome it produces can name a
// planned kind but no source kind.
func ungroupableFact(memberID string) CanonicalFact {
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	return CanonicalFact{
		Kind:    FactMetrics,
		Subject: SubjectRef{Kind: SubjectProject, CanonicalID: memberID, Label: memberID},
		Fields: map[string]FactValue{
			"throughput_breakdown": {Rows: []FactValueRow{{Fields: map[string]FactValue{
				"day": str("2026-09-01"), "delivered": str("3"),
			}}}},
		},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

// TestNoPlacementIsAMemberOfTheClosedGroupingVocabulary walks the vocabulary
// rather than asserting the one new token, so a future member is covered the
// day it is added.
//
// The canonical map is what the telemetry emitter consults before writing the
// value to a log field, so a member missing from it is not a cosmetic gap: the
// emitter reports it as `unclassified` and the operator sees a refusal with no
// name. Asserting each member maps to ITSELF is what catches a copy-paste
// entry pointing at its neighbour.
func TestNoPlacementIsAMemberOfTheClosedGroupingVocabulary(t *testing.T) {
	t.Parallel()
	members := []CohortGroupingRefusal{
		CohortGroupingRefusalNone,
		CohortGroupingRefusalGroupKindSourceMismatch,
		CohortGroupingRefusalNoMemberPlaced,
	}
	if len(members) == 0 {
		t.Fatal("the vocabulary under test is empty, so every assertion below is vacuous")
	}
	if len(canonicalCohortGroupingRefusals) != len(members) {
		t.Fatalf("canonicalCohortGroupingRefusals has %d entries, this test enumerates %d -- a member was added without extending this walk, which is how a token reaches a log field as `unclassified`",
			len(canonicalCohortGroupingRefusals), len(members))
	}
	for _, member := range members {
		if !ValidCohortGroupingRefusal(member) {
			t.Errorf("ValidCohortGroupingRefusal(%q) = false; the telemetry emitter will report it as unclassified", member)
		}
		if got := canonicalCohortGroupingRefusals[member]; got != member {
			t.Errorf("canonicalCohortGroupingRefusals[%q] = %q, want the member itself", member, got)
		}
	}
}

// TestAnInventedGroupingRefusalIsNotAMember is the attribution control for the
// walk above: without it, a mutant making ValidCohortGroupingRefusal return
// true unconditionally passes every assertion there.
func TestAnInventedGroupingRefusalIsNotAMember(t *testing.T) {
	t.Parallel()
	for _, outsider := range []CohortGroupingRefusal{
		"no_members_placed",  // plural: a near-miss of the real token
		"member_not_placed",  //
		"NO_MEMBER_PLACED",   // case
		" no_member_placed ", // whitespace
	} {
		if ValidCohortGroupingRefusal(outsider) {
			t.Errorf("ValidCohortGroupingRefusal(%q) = true; the vocabulary admits a value no producer writes", outsider)
		}
	}
}

// TestGroupingWithNoPlaceableMemberNamesItsReason is the producer-level harm:
// the outcome must say WHY, not merely how many.
//
// The harm assertion is outcome.Refusal. The count was never the missing part
// -- BuildCohortGroups has always returned `ungrouped` -- so a test that
// asserted only the count would have been green on the defect.
func TestGroupingWithNoPlaceableMemberNamesItsReason(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("project_a", "project_b")
	groups, ungrouped, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectTeam}, cohort,
		[]CanonicalFact{ungroupableFact("project_a"), ungroupableFact("project_b")})

	if len(groups) != 0 {
		t.Fatalf("fixture drift: groups = %#v, want none -- the facts name no group axis", groups)
	}
	if ungrouped != 2 {
		t.Fatalf("ungrouped = %d, want 2", ungrouped)
	}
	// THE HARM: on the parent this reads "" and the case is indistinguishable
	// from grouping never having been attempted.
	if outcome.Refusal != CohortGroupingRefusalNoMemberPlaced {
		t.Errorf("outcome.Refusal = %q, want %q: a reader of the persisted artifact cannot tell a silent source from a plan that never asked for a group axis",
			outcome.Refusal, CohortGroupingRefusalNoMemberPlaced)
	}
	if outcome.PlannedKind != SubjectTeam {
		t.Errorf("outcome.PlannedKind = %q, want %q", outcome.PlannedKind, SubjectTeam)
	}
	if outcome.Ungrouped != 2 {
		t.Errorf("outcome.Ungrouped = %d, want 2", outcome.Ungrouped)
	}
	// The source named nothing, so there is no kind to carry. Asserted
	// positively rather than left unstated: a later edit that stamps the
	// planned kind here would invent a source agreement that does not exist.
	if outcome.SourceKind != "" {
		t.Errorf("outcome.SourceKind = %q, want empty: nothing in the facts named a kind, so naming one is an invention", outcome.SourceKind)
	}
	// The members are not dropped, only unplaced -- the same contract the
	// partial-placement case holds.
	if len(cohort.Members) != 2 {
		t.Errorf("cohort lost a member it could not group: %d remain", len(cohort.Members))
	}
}

// TestAPartiallyGroupedCohortIsNotARefusal is THE WIDENING CONTROL.
//
// One member placed, one not, is a served grouped answer. If the firing
// condition ever loosens from "zero groups built" to "any member unplaced",
// this reddens -- and so does the shipped
// TestBuildCohortGroupsLeavesAnUnplaceableMemberUngrouped, deliberately, so
// the mutant is caught by two independent tests rather than one.
func TestAPartiallyGroupedCohortIsNotARefusal(t *testing.T) {
	t.Parallel()
	groups, ungrouped, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectTeam}, planFixtureCohort("project_a", "project_orphan"),
		[]CanonicalFact{planFixtureFacts("project_a", "team_1", "Platform")})

	if len(groups) != 1 || ungrouped != 1 {
		t.Fatalf("fixture drift: groups = %d, ungrouped = %d, want 1 and 1", len(groups), ungrouped)
	}
	if outcome.Refusal != CohortGroupingRefusalNone {
		t.Errorf("outcome.Refusal = %q on a PARTIALLY grouped cohort, want none: a served grouped answer would be relabelled a refusal and the reader told about a failure that did not happen",
			outcome.Refusal)
	}
	if outcome.Ungrouped != 0 {
		t.Errorf("outcome.Ungrouped = %d on a non-refusal, want 0: a count on a zero-valued refusal is a number with no sentence", outcome.Ungrouped)
	}
}

// TestAFullyGroupedCohortIsNotARefusal is the second control: every member
// placed must leave the outcome zero-valued, or every grouped answer acquires
// a disclosure.
func TestAFullyGroupedCohortIsNotARefusal(t *testing.T) {
	t.Parallel()
	_, ungrouped, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectTeam}, planFixtureCohort("project_a"),
		[]CanonicalFact{planFixtureFacts("project_a", "team_1", "Platform")})
	if ungrouped != 0 {
		t.Fatalf("fixture drift: ungrouped = %d, want 0", ungrouped)
	}
	if outcome.Refusal != CohortGroupingRefusalNone {
		t.Errorf("outcome.Refusal = %q on a fully grouped cohort, want none", outcome.Refusal)
	}
}

// TestAKindMismatchStillNamesTheMismatch pins that the new arm did not swallow
// the existing one. The mismatch returns before `order` is ever read, so its
// Ungrouped must stay zero -- if it ever reads non-zero, the two arms have
// been merged and the more serious reason is being reported as the milder one.
func TestAKindMismatchStillNamesTheMismatch(t *testing.T) {
	t.Parallel()
	_, _, outcome := BuildCohortGroups(
		AnswerPlan{GroupKind: SubjectRepository}, planFixtureCohort("project_a"),
		[]CanonicalFact{teamScopedFact("project_a", "team_security", "Security")})

	if outcome.Refusal != CohortGroupingRefusalGroupKindSourceMismatch {
		t.Fatalf("outcome.Refusal = %q, want %q", outcome.Refusal, CohortGroupingRefusalGroupKindSourceMismatch)
	}
	if outcome.SourceKind != SubjectTeam {
		t.Errorf("outcome.SourceKind = %q, want %q", outcome.SourceKind, SubjectTeam)
	}
	if outcome.Ungrouped != 0 {
		t.Errorf("outcome.Ungrouped = %d on a kind mismatch, want 0: the mismatch refuses before any member is counted, and a non-zero count here means the two arms have been merged",
			outcome.Ungrouped)
	}
}

// TestANoPlacementAnswerCarriesItsDisclosure drives the composer, and is the
// weaker half of the pair -- TestNoPlacementReachesBothTheOperatorAndTheReader
// below is what proves the composer is actually reached.
func TestANoPlacementAnswerCarriesItsDisclosure(t *testing.T) {
	t.Parallel()
	outcome := CohortGroupingOutcome{
		Refusal: CohortGroupingRefusalNoMemberPlaced, PlannedKind: SubjectTeam, Ungrouped: 2,
	}
	result := InvestigationResult{Status: InvestigationPartial}
	applyGroupingRefusalDisclosure(&result, outcome)

	want := contractsv1.ContextFabricGroupingUnplaceableLimitation(SubjectTeam)
	if !hasLimitation(result.Limitations, want) {
		t.Errorf("limitations = %#v, want %q: a grouped question answered flat with no statement of it is the silent flattening the ruling forbids",
			result.Limitations, want)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial is false on an answer that dropped its group axis")
	}
}

// TestAGroupingRefusalOutsideTheVocabularyDisclosesNothing is the FAIL-CLOSED
// arm of the D10 allow-list, and the only test that can catch the deny-list
// mutant while the vocabulary still has just two disclosing members.
//
// With `!= None` in place of the switch, an out-of-vocabulary value composes
// the neighbouring member's sentence -- a disclosure about a disagreement that
// never happened -- or panics on a missing case. The allow-list's default arm
// says nothing instead, and the emitter's own `unclassified` fallback is what
// surfaces the unknown value to an operator.
func TestAGroupingRefusalOutsideTheVocabularyDisclosesNothing(t *testing.T) {
	t.Parallel()
	result := InvestigationResult{Status: InvestigationPartial}
	applyGroupingRefusalDisclosure(&result, CohortGroupingOutcome{
		Refusal: CohortGroupingRefusal("something_invented"), PlannedKind: SubjectTeam,
	})
	if len(result.Limitations) != 0 {
		t.Errorf("limitations = %#v on an out-of-vocabulary refusal; a deny-list gate would give the next member a sentence nobody wrote", result.Limitations)
	}
	if result.Coverage.Partial {
		t.Error("Coverage.Partial set from an out-of-vocabulary refusal")
	}
	// Positive control in the same test: the mechanism CAN disclose, so the
	// zero above is a refusal to disclose rather than a broken call.
	control := InvestigationResult{Status: InvestigationPartial}
	applyGroupingRefusalDisclosure(&control, CohortGroupingOutcome{
		Refusal: CohortGroupingRefusalNoMemberPlaced, PlannedKind: SubjectTeam, Ungrouped: 1,
	})
	if len(control.Limitations) == 0 {
		t.Fatal("control failed: the composer disclosed nothing for a real vocabulary member either, so the assertion above measured nothing")
	}
}

// TestNoPlacementReachesBothTheOperatorAndTheReader is the CONSUMER test, and
// the one that matters.
//
// It drives Engine.Investigate and asserts the emitted telemetry event AND the
// served answer's limitation in the SAME run, because they are two halves of
// one decision: this package has already recorded three separate mutants of
// the engine -> telemetry -> assembly path that survived a suite whose tests
// all drove the composer directly. On the parent the whole branch emits no
// event at all, so the first assertion here is red before any field is read.
func TestNoPlacementReachesBothTheOperatorAndTheReader(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine, request := groupingEngineFixture(t, telemetry, SubjectTeam,
		[]CanonicalFact{ungroupableFact("project_a"), ungroupableFact("project_b")})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a flat answer rather than a failure", err)
	}

	// THE OPERATOR. Red on the parent because the branch emits nothing.
	if len(telemetry.groupedCohortCompletenesses) != 1 {
		t.Fatalf("groupedCohortCompletenesses = %#v, want exactly one event: on the parent this branch emits none, and a grouped question answered flat leaves no line at all",
			telemetry.groupedCohortCompletenesses)
	}
	event := telemetry.groupedCohortCompletenesses[0]
	if event.Refusal != CohortGroupingRefusalNoMemberPlaced {
		t.Errorf("event.Refusal = %q, want %q", event.Refusal, CohortGroupingRefusalNoMemberPlaced)
	}
	if event.PlannedGroupKind != SubjectTeam {
		t.Errorf("event.PlannedGroupKind = %q, want %q", event.PlannedGroupKind, SubjectTeam)
	}
	if event.GroupCount != 0 {
		t.Errorf("event.GroupCount = %d, want 0", event.GroupCount)
	}
	if event.UngroupedMembers != 2 {
		t.Errorf("event.UngroupedMembers = %d, want 2: the count is what separates a silent source from a disagreeing one without opening the artifact",
			event.UngroupedMembers)
	}

	// THE READER. Kills the mutant that keeps the telemetry and drops
	// `groupingRefusalForDisclosure = groupingOutcome`, which is invisible to
	// every assertion above.
	want := contractsv1.ContextFabricGroupingUnplaceableLimitation(SubjectTeam)
	if !hasLimitation(result.Limitations, want) {
		t.Errorf("served limitations = %#v, want %q: the reader asked for a breakdown, got a flat answer, and was told nothing",
			result.Limitations, want)
	}
	if !result.Coverage.Partial {
		t.Error("Coverage.Partial is false on an answer that dropped its group axis")
	}
	if result.Cohort != nil && len(result.Cohort.Groups) != 0 {
		t.Errorf("result cohort carries %d groups after a no-placement refusal", len(result.Cohort.Groups))
	}
}

// TestAGroupedAnswerCarriesNoUnplaceableDisclosure is the attribution control
// for the consumer test: without it, a mutant that disclosed unconditionally
// would satisfy the assertions above while putting the sentence on every
// grouped answer.
func TestAGroupedAnswerCarriesNoUnplaceableDisclosure(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	// Same wiring, same planned kind; only the FACTS differ -- these name a
	// team, so every member places.
	engine, request := groupingEngineFixture(t, telemetry, SubjectTeam, []CanonicalFact{
		teamScopedFact("project_a", "team_security", "Security"),
		teamScopedFact("project_b", "team_security", "Security"),
	})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricGroupingUnplaceableLimitation(limitation) {
			t.Fatalf("a fully grouped answer carries an unplaceable disclosure: %q", limitation)
		}
	}
	for _, event := range telemetry.groupedCohortCompletenesses {
		if event.Refusal != CohortGroupingRefusalNone {
			t.Fatalf("event.Refusal = %q on a fully grouped answer, want none", event.Refusal)
		}
		if event.UngroupedMembers != 0 {
			t.Fatalf("event.UngroupedMembers = %d on a fully grouped answer, want 0", event.UngroupedMembers)
		}
	}
}

// TestTheNoPlacementEmitIsWiredIntoAssembly is the CALL-SITE pin.
//
// It exists because this exact deletion has already survived once in this
// package: every composer-level test stayed green when the invocation was
// removed, and only a source-reading pin caught it. The same reasoning applies
// one layer up -- an engine branch that computes an outcome and hands it to
// nobody is a branch that discloses nothing, however well the composer is
// tested.
//
// Read from source rather than by standing up a second full assembly fixture,
// in the manner this package already establishes.
func TestTheNoPlacementEmitIsWiredIntoAssembly(t *testing.T) {
	t.Parallel()
	const path = "engine.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// Anchor on the ENCLOSING branch, not on a whole-file grep: the mismatch
	// arm above it calls the same emitter, so a file-wide search for the call
	// would be satisfied by the arm this test is not about.
	var branch *ast.IfStmt
	ast.Inspect(file, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		binary, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		left, ok := binary.X.(*ast.Ident)
		if !ok || left.Name != "ungrouped" {
			return true
		}
		branch = ifStmt
		return false
	})
	if branch == nil {
		t.Fatal("control failed: no `if ungrouped > 0` branch found in engine.go, so this test is measuring nothing -- if the branch was renamed, re-anchor it rather than deleting this test")
	}

	var emits, carries bool
	ast.Inspect(branch.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "recordGroupedCohortCompleteness" {
				emits = true
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "groupingRefusalForDisclosure" {
					carries = true
				}
			}
		}
		return true
	})
	if !emits {
		t.Error("the no-placement branch never calls recordGroupedCohortCompleteness -- a grouped question answered flat leaves an operator no line at all, which is the defect this change exists to remove")
	}
	if !carries {
		t.Error("the no-placement branch never assigns groupingRefusalForDisclosure -- the telemetry would survive and the READER would still be told nothing, and no composer-level test can see that")
	}
}

// TestTheNoPlacementDisclosureSurvivesAFullLimitationList is the property the
// §10b sub-diagram was drawn for, applied to the new family.
//
// A disclosure that is composed and then dropped is worse than one never
// written: the reader gets a flat answer with no statement, and the artifact
// records a displacement that removed the very sentence it was made for. That
// is the shipped defect the mismatch family already hit once, and the ONLY
// thing standing between this family and a repeat is its registration in the
// service-authored predicate -- which no test that drives the composer with an
// empty list can see, because nothing is ever displaced there.
func TestTheNoPlacementDisclosureSurvivesAFullLimitationList(t *testing.T) {
	t.Parallel()
	full := make([]string, 0, contractsv1.ContextFabricLimitationsMaxCount)
	for index := 0; index < contractsv1.ContextFabricLimitationsMaxCount; index++ {
		// Distinct model-authored caveats: the appender dedups first, so
		// identical entries would shorten the list before the cap is measured
		// and nothing would be displaced.
		full = append(full, "model caveat "+string(rune('a'+index%26))+string(rune('a'+index/26)))
	}
	result := InvestigationResult{Status: InvestigationPartial, Limitations: full}

	applyGroupingRefusalDisclosure(&result, CohortGroupingOutcome{
		Refusal: CohortGroupingRefusalNoMemberPlaced, PlannedKind: SubjectTeam, Ungrouped: 3,
	})

	want := contractsv1.ContextFabricGroupingUnplaceableLimitation(SubjectTeam)
	if !hasLimitation(result.Limitations, want) {
		t.Fatalf("the disclosure was composed and then DROPPED from a full list -- the reader gets a flat answer and no statement of it. Registering the sentence in IsContextFabricServiceAuthoredLimitation is what prevents this; limitations end: %#v",
			result.Limitations[len(result.Limitations)-3:])
	}
	if len(result.Limitations) != contractsv1.ContextFabricLimitationsMaxCount {
		t.Errorf("limitations = %d entries, want the cap %d", len(result.Limitations), contractsv1.ContextFabricLimitationsMaxCount)
	}
	// The displaced caveat is accounted. A displacement nothing counts is a
	// caveat that vanished from the stored answer with no record it existed,
	// and it also puts the result at odds with the validator's coherence rule.
	if result.LimitationsDisplaced != 1 {
		t.Errorf("LimitationsDisplaced = %d, want 1: one model caveat made room for this disclosure and the count is the only record it was there",
			result.LimitationsDisplaced)
	}
}
