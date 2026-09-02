package answerprojection

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4809 PATH 1: the projection clamp runs a real overlap-aware
// set-cover selection and DISCARDS the basis it returns
// (`allowed, _ := contractsv1.SelectGroupCoverMembers(...)`).
//
// This package is PURE by a binding constraint enforced by
// TestPackageImportsStayPure -- it cannot import a telemetry sink -- so the
// disclosure on this path is an ARTIFACT FIELD, not an event. That is the
// right home anyway: ProjectionBudget already exists to declare what the
// projection did to the answer, and "by which order it chose the survivors"
// is the one thing it declared nothing about.
//
// SCOPE, stated so nobody widens it later: on this path the selection's
// before, after and KEPT MEMBER IDS are ALREADY disclosed, and this test
// asserts all three against the existing fields precisely to record that.
// cohort.total is before, len(cohort.members) is after,
// projection_budget.cohort_members_omitted is the delta, and the kept ids
// are the projected members' own canonical ids. Only the basis was missing,
// so only the basis is added.
//
// RED ON origin/main (57091487) BY ASSERTION, not by symbol absence: the
// projection is read back as decoded JSON, so this file compiles at the
// parent and fails there on the key being absent.

// chaos4809OverlappingProjectionCohort builds a five-member cohort whose two
// groups OVERLAP on b1, so the overlap-aware selection has something to
// exploit: the minimum cover is {b1} alone, one member covering both groups.
// Two groups is inside ContextFabricSetCoverGroupGuard, so the basis the
// selection reports is overlap_aware_set_cover rather than the beyond-guard
// largest_group_round_robin fallback -- which is what makes the asserted
// value discriminating rather than merely present.
func chaos4809OverlappingProjectionCohort() *contractsv1.ContextFabricCohort {
	ids := []string{"a1", "a2", "b1", "c1", "c2"}
	cohort := &contractsv1.ContextFabricCohort{
		Kind:      contractsv1.ContextFabricSubjectProject,
		Rationale: "overlapping grouped fixture",
		Complete:  true,
	}
	for rank, id := range ids {
		cohort.Members = append(cohort.Members, contractsv1.ContextFabricCohortMember{
			Subject:          subject(contractsv1.ContextFabricSubjectProject, id, id),
			Rank:             rank + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
	}
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{
			Subject:            subject(contractsv1.ContextFabricSubjectTeam, "team_a", "team_a"),
			MemberCanonicalIDs: []string{"a1", "a2", "b1"},
			Complete:           true,
			Total:              3,
		},
		{
			Subject:            subject(contractsv1.ContextFabricSubjectTeam, "team_b", "team_b"),
			MemberCanonicalIDs: []string{"b1", "c1", "c2"},
			Complete:           true,
			Total:              3,
		},
	}
	return cohort
}

// chaos4809ProjectionBudgetJSON marshals the projection and returns its
// projection_budget object decoded as a map. Reading the WIRE rather than
// the Go struct is what keeps this test compilable at the parent, and it
// also asserts the thing that actually matters: a field a consumer cannot
// see in the serialized document discloses nothing, however well-named the
// Go field is.
func chaos4809ProjectionBudgetJSON(t *testing.T, projection contractsv1.ContextFabricAnswerProjection) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	var decoded struct {
		ProjectionBudget map[string]any `json:"projection_budget"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode projection: %v", err)
	}
	return decoded.ProjectionBudget
}

// TestCHAOS4809ProjectionDisclosesTheGroupCoverBasis is PATH 1's red-first
// proof.
func TestCHAOS4809ProjectionDisclosesTheGroupCoverBasis(t *testing.T) {
	t.Parallel()
	result := richResult()
	result.Cohort = chaos4809OverlappingProjectionCohort()
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 3

	projection := Project(result, bounds)
	if projection.Cohort == nil {
		t.Fatal("projection dropped the cohort entirely")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection.Validate() = %v", err)
	}

	// The three facts that were ALREADY disclosed, asserted so the scope of
	// this fix is recorded in an executable form rather than in prose.
	if projection.Cohort.Total != 5 {
		t.Fatalf("Cohort.Total = %d, want 5 -- this is the selection's BEFORE, already on the artifact", projection.Cohort.Total)
	}
	if len(projection.Cohort.Members) != 3 {
		t.Fatalf("projected %d members, want 3 -- this is the selection's AFTER, already on the artifact", len(projection.Cohort.Members))
	}
	if projection.ProjectionBudget.CohortMembersOmitted != 2 {
		t.Fatalf("CohortMembersOmitted = %d, want 2", projection.ProjectionBudget.CohortMembersOmitted)
	}
	for _, member := range projection.Cohort.Members {
		if member.Subject.CanonicalID == "" {
			t.Fatal("a projected member carries no canonical id -- the KEPT MEMBER IDS are this artifact's own member list, and an empty one would mean the set is not recoverable after all")
		}
	}
	// Every group survives with at least one member: the D2 floor. Asserted
	// here because a basis that named set cover over a cohort that had
	// silently lost a group would be a disclosure of the wrong thing.
	if len(projection.Cohort.Groups) != 2 {
		t.Fatalf("projected %d groups, want both", len(projection.Cohort.Groups))
	}

	// THE DEFECT. main discards the second return value at the call site, so
	// no artifact anywhere names the order that chose these three members.
	budget := chaos4809ProjectionBudgetJSON(t, projection)
	got, present := budget["cohort_member_selection_basis"]
	if !present {
		t.Fatal("projection_budget carries no cohort_member_selection_basis: the clamp ran an overlap-aware set-cover selection and discarded the basis it returned, so the order that chose these members is unrecoverable from the artifact")
	}
	if got != "overlap_aware_set_cover" {
		t.Fatalf("cohort_member_selection_basis = %#v, want overlap_aware_set_cover -- two groups is inside the set-cover guard, so the beyond-guard fallback order did not run", got)
	}
}

// TestCHAOS4809FlatProjectionDeclaresNoSelectionBasis is the discriminating
// control, and it is the one that stops the fix from being a rubber stamp.
//
// A flat cohort runs NO group-aware selection at all -- groupAwareMemberAllowance
// returns nil before it reaches SelectGroupCoverMembers -- so there is no
// basis to report and the field must be ABSENT, never a default value
// standing in for an order that never executed. Emitting a basis here would
// be the same defect this ticket closes, pointing the other way: an artifact
// naming a selection that did not happen.
func TestCHAOS4809FlatProjectionDeclaresNoSelectionBasis(t *testing.T) {
	t.Parallel()
	result := richResult()
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 1

	projection := Project(result, bounds)
	if projection.Cohort == nil {
		t.Fatal("projection dropped the cohort")
	}
	if len(projection.Cohort.Groups) != 0 {
		t.Fatalf("the flat fixture gained %d groups", len(projection.Cohort.Groups))
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection.Validate() = %v", err)
	}

	budget := chaos4809ProjectionBudgetJSON(t, projection)
	if got, present := budget["cohort_member_selection_basis"]; present {
		t.Fatalf("cohort_member_selection_basis = %#v on a FLAT cohort, want the key absent -- no group-aware selection ran, so naming an order would invent one", got)
	}
}

// TestCHAOS4809ProjectedBasisValidatesAgainstThePublishedSchema closes the
// gap that a Go-side field and a JSON Schema can drift apart silently.
//
// contractcheck validates every PUBLISHED EXAMPLE against its schema, and
// none of the three published projection examples exercises a grouped
// squeeze -- their cohorts fit the default budget, so not one of them ever
// carries this field. Without this test the schema property would be
// unexercised by anything: `make contract-test` passed with the Go field
// present and the schema property missing, which is precisely the shape of
// "a measurement that did not happen reads as coverage".
//
// So this validates what the PRODUCER actually emits, on the exact path
// that produces the field, against the exact document the repository
// publishes. Note what it would catch that Validate() cannot: the schema
// sets additionalProperties:false, so a Go field absent from the schema
// makes every real grouped-squeeze projection INVALID for any consumer
// validating against the published contract, while every Go-side test still
// passes.
func TestCHAOS4809ProjectedBasisValidatesAgainstThePublishedSchema(t *testing.T) {
	t.Parallel()
	result := richResult()
	result.Cohort = chaos4809OverlappingProjectionCohort()
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 3

	projection := Project(result, bounds)
	if projection.ProjectionBudget.CohortMemberSelectionBasis == "" {
		t.Fatal("fixture produced no selection basis, so this test would validate a document that does not carry the field under test")
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}

	schemaPath, err := filepath.Abs(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_answer_projection.v1.schema.json"))
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}
	report, err := gojsonschema.Validate(
		gojsonschema.NewReferenceLoader("file://"+schemaPath),
		gojsonschema.NewBytesLoader(encoded),
	)
	if err != nil {
		t.Fatalf("validate against published schema: %v", err)
	}
	if !report.Valid() {
		for _, failure := range report.Errors() {
			t.Errorf("schema violation: %s", failure)
		}
		t.Fatal("the projection this producer emits does not validate against the published schema")
	}
}

// chaos4809DisjointSingletonCohort is the fixture for the case where the
// group-aware selection does NOT get the last word.
//
// Twelve groups, each holding one member of its own, nothing shared. The
// minimum cover of twelve disjoint groups is all twelve members -- overlap
// can only ever LOWER what the floor costs, and there is none here -- and
// SelectGroupCoverMembers computes that floor UNCONDITIONALLY, ignoring the
// budget on purpose (its own doc comment: "a budget too small even for the
// floor comes back OVER budget rather than dropping a group"). Twelve is
// exactly ContextFabricSetCoverGroupGuard, so the exact solve runs and the
// basis reported is overlap_aware_set_cover.
//
// Against a six-member budget the allowance therefore admits twelve, and the
// projection's own cap then takes the first six in canonical order.
func chaos4809DisjointSingletonCohort() *contractsv1.ContextFabricCohort {
	cohort := &contractsv1.ContextFabricCohort{
		Kind:      contractsv1.ContextFabricSubjectProject,
		Rationale: "disjoint singleton groups",
		Complete:  true,
	}
	for index := 0; index < 12; index++ {
		id := fmt.Sprintf("project_%02d", index)
		cohort.Members = append(cohort.Members, contractsv1.ContextFabricCohortMember{
			Subject:          subject(contractsv1.ContextFabricSubjectProject, id, id),
			Rank:             index + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
		})
		cohort.Groups = append(cohort.Groups, contractsv1.ContextFabricCohortGroup{
			Subject:            subject(contractsv1.ContextFabricSubjectTeam, fmt.Sprintf("team_%02d", index), fmt.Sprintf("team_%02d", index)),
			MemberCanonicalIDs: []string{id},
			Complete:           true,
			Total:              1,
		})
	}
	return cohort
}

// TestCHAOS4809BasisIsAbsentWhenTheCapChoseTheSurvivors pins the last way
// this field can lie, and it is the same lie the ticket exists to remove,
// one boundary further down.
//
// The group-aware allowance is NOT the last cut in this function. Its
// unconditional floor can admit MORE members than the budget, and the loop
// below it then stops at the first MaxCohortMembers in canonical order. When
// that happens the survivors were chosen by a canonical-order prefix, not by
// the overlap-aware cover -- so publishing overlap_aware_set_cover states
// that an order chose these members when a different rule did.
//
// A basis is a claim about WHICH MEMBERS SURVIVED, not about which function
// ran. When the selection did not determine the survivor set, no order can
// honestly be named, and the field's own contract already gives absence a
// meaning that covers it: no group-aware selection chose this set.
func TestCHAOS4809BasisIsAbsentWhenTheCapChoseTheSurvivors(t *testing.T) {
	t.Parallel()
	result := richResult()
	result.Cohort = chaos4809DisjointSingletonCohort()
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 6

	projection := Project(result, bounds)
	if projection.Cohort == nil {
		t.Fatal("projection dropped the cohort entirely")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection.Validate() = %v", err)
	}

	// Confirm the fixture really reaches the branch under test: the cap, not
	// the selection, is what cut the cohort to six. Without this the test
	// could pass against a projection that never exceeded its budget.
	if len(projection.Cohort.Members) != 6 {
		t.Fatalf("projected %d members, want the 6 the cap admits", len(projection.Cohort.Members))
	}
	admissible, basis := groupAwareMemberAllowance(*result.Cohort, bounds.MaxCohortMembers)
	if len(admissible) <= bounds.MaxCohortMembers {
		t.Fatalf("the allowance admitted %d members, which fits the %d budget -- this fixture no longer exercises the post-selection cap",
			len(admissible), bounds.MaxCohortMembers)
	}
	if basis != contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("the selection reported %q, want overlap_aware_set_cover -- the fixture must reach the exact solve", basis)
	}

	// THE DEFECT. The survivors are a canonical-order prefix of the twelve
	// the cover admitted; naming the cover as the order that chose them is
	// false.
	budget := chaos4809ProjectionBudgetJSON(t, projection)
	if got, present := budget["cohort_member_selection_basis"]; present {
		t.Fatalf("cohort_member_selection_basis = %#v, want the key ABSENT -- the overlap-aware cover admitted %d members and the projection's own cap then kept the first %d in canonical order, so the cover did not choose this survivor set",
			got, len(admissible), len(projection.Cohort.Members))
	}
}
