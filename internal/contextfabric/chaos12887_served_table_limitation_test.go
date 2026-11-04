package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const servedSentence = contractsv1.ContextFabricDetailTableServedLimitation

// subcategoryServed is a result whose one claim carries the subcategory detail
// table (3 rows) attached the production way, with the fact it came from.
func subcategoryServed(limitations ...string) (InvestigationResult, []CanonicalFact) {
	fact := detailFact(true)
	claim := ClaimedFact{ClaimID: "claim_detail", Kind: FactInvestment, Subject: fact.Subject, Field: "subcategory_breakdown", Value: ScalarValue{Null: true}, FactID: fact.ID}
	attached, _, _, _ := attachCanonicalRows([]ClaimedFact{claim}, []CanonicalFact{fact})
	result := validInvestigationResult()
	result.ClaimedFacts = attached
	result.Status = InvestigationPartial
	result.Limitations = limitations
	return result, []CanonicalFact{fact}
}

func TestServedDetailTableLimitationReplacesAModelSentenceThatCitesTheTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, prose string
	}{
		{"by display noun", "The input lists a subcategory breakdown but does not include its row contents."},
		{"by field name", "subcategory_breakdown rows are not supplied."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := "The read of linked subjects was cut at its limit."
			result, facts := subcategoryServed(tc.prose, other)
			got := withServedDetailTableLimitation(result, facts)
			if slices.Contains(got.Limitations, tc.prose) || !slices.Contains(got.Limitations, servedSentence) || !slices.Contains(got.Limitations, other) {
				t.Fatalf("limitations = %q; want the cited sentence replaced, the unrelated one kept", got.Limitations)
			}
			if got.Status != InvestigationPartial {
				t.Fatalf("status = %q; the replacement never changes the model's status", got.Status)
			}
			again := withServedDetailTableLimitation(got, facts)
			if !slices.Equal(again.Limitations, got.Limitations) {
				t.Fatalf("second pass changed %q to %q; want idempotent", got.Limitations, again.Limitations)
			}
		})
	}
}

// A row count alone never identifies a table: another table can hold the same
// number of rows (Codex r2 P2). Only the table's own name or noun does.
func TestServedDetailTableLimitationNeverMatchesOnARowCountAlone(t *testing.T) {
	t.Parallel()
	for _, prose := range []string{"13 rows were skipped.", "It lists 3 rows without contents.", "The repository theme breakdown has 3 rows that were not supplied."} {
		result, facts := subcategoryServed(prose)
		if got := withServedDetailTableLimitation(result, facts); !slices.Equal(got.Limitations, []string{prose}) {
			t.Fatalf("%q became %q; a row count or another table's name is not a citation of the served table", prose, got.Limitations)
		}
	}
}

func TestServedDetailTableLimitationLeavesWhatDoesNotCiteAServedDetailTable(t *testing.T) {
	t.Parallel()
	unrelated := "The read of linked subjects was cut at its limit."
	result, facts := subcategoryServed(unrelated)
	if got := withServedDetailTableLimitation(result, facts); !slices.Equal(got.Limitations, []string{unrelated}) {
		t.Fatalf("limitations = %q; a sentence naming no served table must stand alone", got.Limitations)
	}
	// A table that is not declared detail is not this rule's.
	pick := "The input lists a subcategory breakdown but does not include its row contents."
	result, _ = subcategoryServed(pick)
	undeclared := detailFact(false)
	if got := withServedDetailTableLimitation(result, []CanonicalFact{undeclared}); !slices.Equal(got.Limitations, []string{pick}) {
		t.Fatalf("limitations = %q; a table the fact does not declare detail is never replaced", got.Limitations)
	}
}

func TestServedDetailTableLimitationKeepsTheModelSentenceOfAWithheldTable(t *testing.T) {
	t.Parallel()
	prose := "The input lists a subcategory breakdown but does not include its row contents."
	result, facts := subcategoryServed(prose)
	result.ClaimedFacts[0].Rows, result.ClaimedFacts[0].Table = nil, nil
	got := withServedDetailTableLimitation(result, facts)
	if !slices.Equal(got.Limitations, []string{prose}) {
		t.Fatalf("limitations = %q; a withheld table is not served: keep the sentence, add none", got.Limitations)
	}
	result, facts = subcategoryServed(prose)
	result.ClaimedFacts[0].Table = nil
	if got := withServedDetailTableLimitation(result, facts); !slices.Equal(got.Limitations, []string{prose}) {
		t.Fatalf("limitations = %q; a claim with rows and no declared table is not served", got.Limitations)
	}
	result, facts = subcategoryServed(prose)
	result.ClaimedFacts[0].Rows = nil
	if got := withServedDetailTableLimitation(result, facts); !slices.Equal(got.Limitations, []string{prose}) {
		t.Fatalf("limitations = %q; a claim with its table declared but no rows left is not served", got.Limitations)
	}
}

func TestServedDetailTableLimitationIsRemovedWhenTheCutWithholdsTheTable(t *testing.T) {
	t.Parallel()
	prose := "The input lists a subcategory breakdown but does not include its row contents."
	result, facts := subcategoryServed(prose)
	finalized := withServedDetailTableLimitation(result, facts)
	if !slices.Contains(finalized.Limitations, servedSentence) {
		t.Fatalf("limitations = %q; want the sentence before the cut", finalized.Limitations)
	}
	finalized.ClaimedFacts[0].Rows, finalized.ClaimedFacts[0].Table = nil, nil
	got := withServedDetailTableLimitation(finalized, facts)
	if slices.Contains(got.Limitations, servedSentence) {
		t.Fatalf("limitations = %q; no table is served after the cut, so no sentence says it is shown", got.Limitations)
	}
}

func TestServedDetailTableLimitationIsServiceAuthored(t *testing.T) {
	t.Parallel()
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(servedSentence) {
		t.Fatal("the served-table sentence is not in the service-authored vocabulary, so a model could copy it")
	}
	if kept, dropped, _ := stripServiceAuthoredLookalikes([]string{servedSentence}); dropped != 1 || len(kept) != 0 {
		t.Fatalf("a model copy of the sentence was kept: %q", kept)
	}
	if strings.Contains(strings.ToLower(servedSentence), "null") {
		t.Fatal("the sentence must not say null")
	}
}

// The wiring: finalizeResult runs the rule over the claims it is handed, and
// again over the document the size cut leaves.
func TestFinalizeResultReplacesAndThenRetractsTheServedTableSentence(t *testing.T) {
	t.Parallel()
	frame := fullchaosChildrenFrame()
	routed := scopeAllocationRequirementFrame(&frame, scopeAllocationOutcome(&frame, SubjectTeam))
	deriver := registryDeriver{capabilities: scopeAllocationCapabilities()}
	engine := &Engine{requirements: deriver, renderShapeOptions: sankeyOn}
	published := PlanRequirementsFromDerived(deriveTurnRequirements(routed, deriver))
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}

	base := repositoryFlowResult(repositoryFlowLinks())
	base.Status = InvestigationComplete
	prose := "The supplied data lists a repository theme breakdown but does not include the row contents."
	base.Limitations = []string{prose}
	base.Coverage = factCoverage(FactInvestment, SourceAvailable)
	base.Completeness = contractsv1.ContextFabricAnswerCompleteness{}
	plan := *base.AnswerPlan
	plan.Requirements = published
	plan.FamilySource = QuestionFamilySourceModelConsensus

	rows := make([]FactValueRow, 0, len(base.ClaimedFacts[0].Rows))
	for range base.ClaimedFacts[0].Rows {
		rows = append(rows, FactValueRow{})
	}
	table := FactTable{Shape: FactTableBreakdown, Key: []string{"repository", "theme"}, Measures: []string{"weighted_effort", "theme_weighted_effort"}, Detail: true, Rows: rows}
	facts := factsFor(team, kindList(FactInvestment))
	facts.Facts[0].Fields = map[string]FactValue{contractsv1.ContextFabricRenderRepositoryFlowTableField: TableFactValue(table)}

	whole := engine.finalizeResult(context.Background(), storage.Principal{}, base, plan, routed, facts, &assemblyTelemetry{}, answerPassFirst, MembershipCardinality{})
	if slices.Contains(whole.Limitations, prose) || !slices.Contains(whole.Limitations, servedSentence) {
		t.Fatalf("limitations = %q; want the model sentence replaced by the served-table sentence", whole.Limitations)
	}
	declared := len(whole.ClaimedFacts[0].Rows)
	cut, _, rules, applied := applyFactRowTruncation(whole, 5, declared)
	if !applied || rules.RepositoryFlowWithheld != 1 {
		t.Fatalf("applied %v, rules %+v: the row cut did not withhold the table", applied, rules)
	}
	served := engine.finalizeResult(context.Background(), storage.Principal{}, cut, plan, routed, facts, &assemblyTelemetry{}, answerPassSecond, MembershipCardinality{})
	if slices.Contains(served.Limitations, servedSentence) || !slices.Contains(served.Limitations, contractsv1.ContextFabricRepositoryFlowWithheldLimitation) {
		t.Fatalf("limitations = %q; a withheld table keeps its own sentence and no served-table sentence", served.Limitations)
	}
}
