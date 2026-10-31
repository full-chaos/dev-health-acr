package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12160. A project with no measurable work of its own in the window is
// served its owning teams' roll-up mix (CHAOS-5930 decision A, a DISCLOSED
// roll-up). The fact carries the label; the served claim used to drop it, so
// the team's number read as the project's own investment. The served answer
// now carries the label, the basis and both unit counts on the claim.

const rollupBasisForTest = "team_project_ownership_via_owned_repos_work_unit_investments"

func investmentFactForTest(project SubjectRef, source string, fields map[string]FactValue) CanonicalFact {
	all := map[string]FactValue{
		"theme_quality":              NumberFactValue(0.55),
		FactFieldInvestmentMixSource: StringFactValue(source),
		"rollup_basis":               StringFactValue(rollupBasisForTest),
	}
	for name, value := range fields {
		all[name] = value
	}
	return CanonicalFact{Kind: FactInvestment, Subject: project, Fields: all, EvidenceRefIDs: []string{"evidence_investment_0001"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"}
}

// serveInvestmentClaim runs one investigation whose answer claims the
// project's theme_quality, and returns the served result and its JSON.
func serveInvestmentClaim(t *testing.T, fact CanonicalFact, claimMix *contractsv1.ContextFabricClaimedInvestmentMix) (InvestigationResult, []byte) {
	t.Helper()
	project := acceptanceProject()
	fact.Subject = project
	bundle := bootstrapFactBundle(project)
	bundle.Facts = append(bundle.Facts, fact)
	bundle.Coverage.Sources = append(bundle.Coverage.Sources, SourceObservation{Source: "canonical_fact:investment", State: SourceAvailable})
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bundle, nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	draft := bootstrapDraft(project)
	draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{
		ClaimID: "claim_investment_quality", Kind: FactInvestment, Subject: project, Field: "theme_quality",
		Value: contractsv1.ContextFabricScalarValue{Number: floatPtr(0.55)}, InvestmentMix: claimMix,
	})
	draft.Drivers[0].ClaimedFactIDs = append(draft.Drivers[0].ClaimedFactIDs, "claim_investment_quality")
	draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, "evidence_investment_0001")
	draft.Drivers[0].EvidenceRefIDs = append(draft.Drivers[0].EvidenceRefIDs, "evidence_investment_0001")
	engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), draft, newMapResultStore())
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	served, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return result, served
}

func servedClaim(t *testing.T, result InvestigationResult, claimID string) ClaimedFact {
	t.Helper()
	for _, claim := range result.ClaimedFacts {
		if claim.ClaimID == claimID {
			return claim
		}
	}
	t.Fatalf("claim %q is not served; claims = %+v", claimID, result.ClaimedFacts)
	return ClaimedFact{}
}

func floatPtr(v float64) *float64 { return &v }

func TestAServedInvestmentClaimDisclosesTheOwningTeamRollup(t *testing.T) {
	t.Parallel()
	result, served := serveInvestmentClaim(t, investmentFactForTest(SubjectRef{}, InvestmentMixSourceOwningTeamRollup, map[string]FactValue{
		"work_unit_count":             IntegerFactValue(7240),
		"project_own_work_unit_count": IntegerFactValue(0),
	}), nil)
	want := `"investment_mix":{"investment_mix_source":"owning_team_rollup","rollup_basis":"` + rollupBasisForTest + `","work_unit_count":7240,"project_own_work_unit_count":0}`
	if !bytes.Contains(served, []byte(want)) {
		t.Fatalf("served answer JSON carries no roll-up disclosure %s on the investment claim:\n%s", want, served)
	}
	if mix := servedClaim(t, result, "claim_readiness_bootstrap").InvestmentMix; mix != nil {
		t.Errorf("readiness claim carries an investment disclosure: %+v", mix)
	}
}

func TestAServedInvestmentClaimDisclosesTheProjectsOwnMix(t *testing.T) {
	t.Parallel()
	result, _ := serveInvestmentClaim(t, investmentFactForTest(SubjectRef{}, InvestmentMixSourceProjectNative, map[string]FactValue{
		"work_unit_count":                    IntegerFactValue(253),
		"owning_team_rollup_work_unit_count": IntegerFactValue(7240),
	}), nil)
	got := servedClaim(t, result, "claim_investment_quality").InvestmentMix
	want := &contractsv1.ContextFabricClaimedInvestmentMix{InvestmentMixSource: contractsv1.ContextFabricInvestmentMixSourceProjectNative, RollupBasis: rollupBasisForTest, WorkUnitCount: 253, ProjectOwnWorkUnitCount: 253}
	if got == nil || *got != *want {
		t.Fatalf("investment_mix = %+v, want %+v", got, want)
	}
}

// The disclosure comes from the grounding fact, never from the draft: a draft
// that labels a roll-up as the project's own work is overwritten, and a draft
// label on a fact that carries none is removed.
func TestTheInvestmentDisclosureIsNeverDraftAuthored(t *testing.T) {
	t.Parallel()
	forged := &contractsv1.ContextFabricClaimedInvestmentMix{InvestmentMixSource: contractsv1.ContextFabricInvestmentMixSourceProjectNative, RollupBasis: "forged", WorkUnitCount: 1, ProjectOwnWorkUnitCount: 1}
	result, _ := serveInvestmentClaim(t, investmentFactForTest(SubjectRef{}, InvestmentMixSourceOwningTeamRollup, map[string]FactValue{
		"work_unit_count":             IntegerFactValue(7240),
		"project_own_work_unit_count": IntegerFactValue(0),
	}), forged)
	got := servedClaim(t, result, "claim_investment_quality").InvestmentMix
	if got == nil || got.InvestmentMixSource != contractsv1.ContextFabricInvestmentMixSourceOwningTeamRollup || got.WorkUnitCount != 7240 || got.ProjectOwnWorkUnitCount != 0 || got.RollupBasis != rollupBasisForTest {
		t.Fatalf("investment_mix = %+v, want the fact's owning_team_rollup over 7240 units with 0 own", got)
	}

	unlabeled := investmentFactForTest(SubjectRef{}, "", nil)
	delete(unlabeled.Fields, FactFieldInvestmentMixSource)
	result, _ = serveInvestmentClaim(t, unlabeled, forged)
	if got := servedClaim(t, result, "claim_investment_quality").InvestmentMix; got != nil {
		t.Fatalf("investment_mix = %+v on a claim whose fact names no mix source, want none", got)
	}
}

func TestInvestmentMixOfReadsTheFactsDisclosure(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	cases := []struct {
		name string
		fact CanonicalFact
		want *contractsv1.ContextFabricClaimedInvestmentMix
	}{
		{"roll-up with zero-effort own units", investmentFactForTest(project, InvestmentMixSourceOwningTeamRollup, map[string]FactValue{"work_unit_count": IntegerFactValue(40), "project_own_work_unit_count": IntegerFactValue(3)}),
			&contractsv1.ContextFabricClaimedInvestmentMix{InvestmentMixSource: "owning_team_rollup", RollupBasis: rollupBasisForTest, WorkUnitCount: 40, ProjectOwnWorkUnitCount: 3}},
		{"native", investmentFactForTest(project, InvestmentMixSourceProjectNative, map[string]FactValue{"work_unit_count": IntegerFactValue(9)}),
			&contractsv1.ContextFabricClaimedInvestmentMix{InvestmentMixSource: "project_native", RollupBasis: rollupBasisForTest, WorkUnitCount: 9, ProjectOwnWorkUnitCount: 9}},
	}
	for _, tc := range cases {
		if got := investmentMixOf(tc.fact); got == nil || *got != *tc.want {
			t.Errorf("%s: investmentMixOf = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// discloseInvestmentMix decides the field alone: whatever a claim carried is
// replaced, and only an investment claim grounded on a fact that names a mix
// source gets one.
func TestDiscloseInvestmentMixSetsTheFieldFromTheFactAlone(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	forged := &contractsv1.ContextFabricClaimedInvestmentMix{InvestmentMixSource: "project_native", RollupBasis: "forged", WorkUnitCount: 1, ProjectOwnWorkUnitCount: 1}
	unlabeled := investmentFactForTest(project, "", nil)
	delete(unlabeled.Fields, FactFieldInvestmentMixSource)
	// A status fact that happens to carry the field name: only an investment
	// claim describes an investment attribution.
	status := CanonicalFact{Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress"), FactFieldInvestmentMixSource: StringFactValue(InvestmentMixSourceOwningTeamRollup)}}
	claims := []ClaimedFact{
		{ClaimID: "claim_investment_quality", Kind: FactInvestment, Subject: project, Field: "theme_quality", Value: contractsv1.ContextFabricScalarValue{Number: floatPtr(0.55)}, InvestmentMix: forged},
		{ClaimID: "claim_status_current", Kind: FactStatus, Subject: project, Field: "status", Value: contractsv1.ContextFabricScalarValue{String: stringPtr("in_progress")}, InvestmentMix: forged},
	}
	got := discloseInvestmentMix(claims, []CanonicalFact{unlabeled, status})
	for _, claim := range got {
		if claim.InvestmentMix != nil {
			t.Errorf("%s: investment_mix = %+v, want none: its fact names no investment mix source, or it is not an investment claim", claim.ClaimID, claim.InvestmentMix)
		}
	}
}

func stringPtr(v string) *string { return &v }
