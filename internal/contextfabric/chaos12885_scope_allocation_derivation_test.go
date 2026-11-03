package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12885, the derivation half: the route is a typed input the engine sets
// on its requirement-frame copy, and the pure derivation reads it.

// scopeAllocationOutcome is the served outcome of the stored turn: the family
// the CHAOS-12606 route serves, over the frame, under a team anchor.
func scopeAllocationOutcome(frame *QuestionFrame, anchorKind SubjectKind) QuestionFamilyOutcome {
	return QuestionFamilyOutcome{
		Family: QuestionFamilyInvestmentAllocation, Frame: frame,
		WinningSampleIndex: 0, WinningSample: FamilySample{ScopeAnchorKind: anchorKind, ScopeAnchorTerm: "Fullchaos"},
	}
}

func requirementIdentitiesOf(rows []DerivedRequirement) []string {
	identities := make([]string, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, requirementIdentity(row))
	}
	return identities
}

func onlyIdentity(t *testing.T, rows []DerivedRequirement, want string) DerivedRequirement {
	t.Helper()
	if len(rows) != 1 || requirementIdentity(rows[0]) != want {
		t.Fatalf("derived %v, want exactly [%s]", requirementIdentitiesOf(rows), want)
	}
	return rows[0]
}

// The stored turn's frame, derived alone and under the route. Alone it is the
// member requirement the stored turn carried; under the route it is the anchor
// team's, served by the investment fact, and the finalization seed derives the
// same row the plan does.
func TestTheScopeAllocationRouteDerivesTheAnchorTeamsAllocation(t *testing.T) {
	t.Parallel()
	deriver := registryDeriver{capabilities: scopeAllocationCapabilities()}
	frame := fullchaosChildrenFrame()

	alone := onlyIdentity(t, deriveTurnRequirements(&frame, deriver), scopeAllocationMemberRequirement)
	if alone.Scope != CompletionScopeEachMember || !alone.Served() {
		t.Fatalf("frame-alone row %+v, want the served each_member row the stored turn carried", alone)
	}

	routed := scopeAllocationRequirementFrame(&frame, scopeAllocationOutcome(&frame, SubjectTeam))
	if routed == &frame || frame.requirementRoute != RequirementRouteFrame {
		t.Fatal("the route was written onto the canonical frame; it belongs on a copy")
	}
	anchor := onlyIdentity(t, deriveTurnRequirements(routed, deriver), scopeAllocationAnchorRequirement)
	if anchor.Role != SubjectRoleSubject || anchor.Subject != SubjectTeam || anchor.Scope != CompletionScopeSingleSubject ||
		anchor.Quantifier != CompletionQuantifierAtLeastOne || len(anchor.FactKinds) != 1 || anchor.FactKinds[0] != FactInvestment {
		t.Fatalf("routed row %+v, want subject/team single_subject at_least_one over investment", anchor)
	}
	seeded := seedRequirementOutcomes(routed, deriver)
	if len(seeded) != 1 || seeded[0].Requirement != scopeAllocationAnchorRequirement {
		t.Fatalf("finalization seed %+v, want the plan's own row %s", seeded, scopeAllocationAnchorRequirement)
	}
}

// The route moves a coordinate only on the turn and the frame it is decided
// for. A turn the route does not serve gets its frame back untouched, and a
// route found on a frame it was not decided for changes nothing.
func TestTheScopeAllocationRouteIsTheRoutesAlone(t *testing.T) {
	t.Parallel()
	deriver := registryDeriver{capabilities: scopeAllocationCapabilities()}

	t.Run("a turn the route does not serve", func(t *testing.T) {
		t.Parallel()
		for name, outcome := range map[string]func(*QuestionFrame) QuestionFamilyOutcome{
			"another family": func(frame *QuestionFrame) QuestionFamilyOutcome {
				outcome := scopeAllocationOutcome(frame, SubjectTeam)
				outcome.Family = QuestionFamilyScopedCohortStatus
				return outcome
			},
			"a project anchor": func(frame *QuestionFrame) QuestionFamilyOutcome {
				return scopeAllocationOutcome(frame, contractsv1.ContextFabricSubjectProject)
			},
		} {
			frame := fullchaosChildrenFrame()
			if got := scopeAllocationRequirementFrame(&frame, outcome(&frame)); got != &frame {
				t.Fatalf("%s: the requirement frame is a routed copy", name)
			}
		}
	})

	t.Run("a route on a frame it was not decided for", func(t *testing.T) {
		t.Parallel()
		twoGoals := fullchaosChildrenFrame(GoalAllocateInvestment, GoalAssessState)
		twoGoals.Obligations = append(twoGoals.Obligations, ObligationState)
		named := fullchaosChildrenFrame()
		team := SubjectTeam
		named.SubjectExpression = SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"Fullchaos"}, ExpectedKind: &team}}
		// The organization frame passes scopeAllocationFrame too, so only the
		// topology clause keeps its subject/organization row.
		organization := fullchaosChildrenFrame()
		organization.SubjectExpression = SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}}
		for name, frame := range map[string]QuestionFrame{"a second goal": twoGoals, "a named subject": named, "the organization": organization} {
			alone := requirementIdentitiesOf(DeriveRequirements(frame, GenerateObligationSeed(deriver.capabilities), deriver.capabilities))
			misplaced := frame
			misplaced.requirementRoute = RequirementRouteScopeAllocationTeam
			routed := requirementIdentitiesOf(DeriveRequirements(misplaced, GenerateObligationSeed(deriver.capabilities), deriver.capabilities))
			if len(alone) == 0 || len(alone) != len(routed) {
				t.Fatalf("%s: alone %v, with a misplaced route %v", name, alone, routed)
			}
			for index := range alone {
				if alone[index] != routed[index] {
					t.Fatalf("%s: alone %v, with a misplaced route %v", name, alone, routed)
				}
			}
		}
		if got := requirementIdentitiesOf(DeriveRequirements(twoGoals, GenerateObligationSeed(deriver.capabilities), deriver.capabilities)); !scopeAllocationHasIdentity(got, scopeAllocationMemberRequirement) {
			t.Fatalf("a second goal derived %v, want the member requirement it has always derived", got)
		}
	})
}

func scopeAllocationHasIdentity(identities []string, want string) bool {
	for _, identity := range identities {
		if identity == want {
			return true
		}
	}
	return false
}

// The lead's residual pin, the case the current code closes: the question asks
// for the split across the team's repositories, so an answer that leaves the
// team's repository_theme_breakdown table out is not complete. When the
// answer-size row cut withholds the table whole (CHAOS-12582), the answer is
// PARTIAL and says why: applyFactRowTruncation appends its lever row (narrowed,
// depth) and ContextFabricRepositoryFlowWithheldLimitation, and the anchor
// requirement's own row stays satisfied, because the team's fact WAS read. The
// same answer with the table kept is complete and draws its sankey, so the pin
// can fail.
func TestAScopeAllocationAnswerWithItsSplitWithheldIsPartial(t *testing.T) {
	t.Parallel()
	frame := fullchaosChildrenFrame()
	routed := scopeAllocationRequirementFrame(&frame, scopeAllocationOutcome(&frame, SubjectTeam))
	deriver := registryDeriver{capabilities: scopeAllocationCapabilities()}
	engine := &Engine{requirements: deriver, renderShapeOptions: sankeyOn}
	published := PlanRequirementsFromDerived(deriveTurnRequirements(routed, deriver))
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	facts := factsFor(team, kindList(FactInvestment))

	base := repositoryFlowResult(repositoryFlowLinks())
	base.Status = InvestigationComplete
	base.Limitations = []string{}
	base.Coverage = factCoverage(FactInvestment, SourceAvailable)
	base.Completeness = contractsv1.ContextFabricAnswerCompleteness{}
	plan := *base.AnswerPlan
	plan.Requirements = published
	plan.FamilySource = QuestionFamilySourceModelConsensus

	whole := engine.finalizeResult(context.Background(), storage.Principal{}, base, plan, routed, facts, &assemblyTelemetry{}, answerPassFirst, MembershipCardinality{})
	if whole.Status != InvestigationComplete || whole.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete || len(sankeysOf(whole.RenderShapes)) != 1 {
		t.Fatalf("with the table kept: status %q, state %q, %d sankeys; want complete with its sankey", whole.Status, whole.Completeness.State, len(sankeysOf(whole.RenderShapes)))
	}

	declared := len(whole.ClaimedFacts[0].Rows)
	cut, _, rules, applied := applyFactRowTruncation(whole, 5, declared)
	if !applied || rules.RepositoryFlowWithheld != 1 {
		t.Fatalf("applied %v, rules %+v: the row cut did not withhold the table", applied, rules)
	}
	served := ApplyServedStatusFloors(engine.finalizeResult(context.Background(), storage.Principal{}, cut, plan, routed, facts, &assemblyTelemetry{}, answerPassSecond, MembershipCardinality{}))
	if served.Status != InvestigationPartial || served.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial || !served.Coverage.Partial {
		t.Fatalf("withheld: status %q, state %q, coverage partial %v; want partial", served.Status, served.Completeness.State, served.Coverage.Partial)
	}
	if !roleContains(served.Limitations, contractsv1.ContextFabricRepositoryFlowWithheldLimitation) {
		t.Fatalf("limitations %q do not say the table was left out", served.Limitations)
	}
	if len(sankeysOf(served.RenderShapes)) != 0 {
		t.Fatal("a withheld table still drew a sankey")
	}
	row, found := scopeAllocationAssembledRow(served, scopeAllocationAnchorRequirement)
	if !found || row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("anchor row %+v (found %v), want satisfied: the team's fact was read", row, found)
	}
	if err := served.Validate(); err != nil {
		t.Fatalf("the withheld answer is not contract-valid: %v", err)
	}
}
