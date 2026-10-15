package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// k7Capabilities declares a health fact with a judged compounding_risk score, whose drivers table the degraded
// composition does not serve, and a plain severity scalar.
func k7Capabilities() []FactCapability {
	return []FactCapability{{
		Kind: FactHealth, SupportedSubjectKinds: []SubjectKind{SubjectTeam},
		Fields: []FactFieldDeclaration{
			{Name: "compounding_risk", Type: FactFieldNumber, Score: true, DriversTable: "compounding_risk_drivers"},
			{Name: "compounding_risk_drivers", Type: FactFieldTable},
			{Name: "severity", Type: FactFieldString},
		},
	}}
}

func k7Fact(withTable bool) CanonicalFact {
	risk, severity := 0.993773746751932, "high"
	fields := map[string]FactValue{"compounding_risk": {Number: &risk}, "severity": {String: &severity}}
	return CanonicalFact{Kind: FactHealth, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "Team A"},
		Fields: fields, EvidenceRefIDs: []string{"ref_1"}, Source: "devhealthfacts.health", SourceVersion: "v1"}
}

// Decision K7: never a bare score. The degraded composition serves no drivers table, so a declared score is withheld
// and the plain scalar beside it is still claimed. On main the score was claimed ("compounding risk score: 0.99").
func TestADegradedAnswerNeverClaimsABareScore(t *testing.T) {
	for _, class := range []SynthesisFailureClass{SynthesisFailureCallNotCompleted, SynthesisFailureModelOutputInvalid} {
		failure := &SynthesisFailure{Class: class, DeferEvent: true, cause: fmt.Errorf("model failed")}
		synthesizer := RuntimeAnswerSynthesizer{FactCapabilities: k7Capabilities()}

		result, err := synthesizer.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, SynthesisInput{Facts: CanonicalFactBundle{Facts: []CanonicalFact{k7Fact(false)}}}, failure)

		if err != nil {
			t.Fatal(err)
		}
		fields := []string{}
		for _, claim := range result.ClaimedFacts {
			fields = append(fields, claim.Field)
		}
		if strings.Join(fields, ",") != "severity" {
			t.Fatalf("class %s: claimed fields %v, want only severity", class, fields)
		}
		details := []CoverageDetail{}
		for _, detail := range result.Coverage.Details {
			if detail.Code == contractsv1.ContextFabricCoverageDetailFactScoreWithheld {
				details = append(details, detail)
			}
		}
		if len(details) != 1 || details[0].FactKind != FactHealth || details[0].Degrading || details[0].Label != "Health scores withheld: their drivers were not served" {
			t.Fatalf("class %s: fact_score_withheld details %+v, want one for health, not degrading, with the approved label", class, details)
		}
		if strings.Contains(strings.ToLower(result.CurrentState), "risk") {
			t.Fatalf("class %s: current_state %q names the withheld score", class, result.CurrentState)
		}
	}
}

// Without field declarations the synthesizer cannot know a score from a scalar and claims every scalar, as it always has
// (a hand-built synthesizer in a test); production wiring passes the registry's capabilities.
func TestADegradedAnswerWithoutDeclarationsClaimsEveryScalar(t *testing.T) {
	failure := &SynthesisFailure{Class: SynthesisFailureCallNotCompleted, DeferEvent: true, cause: fmt.Errorf("model failed")}

	result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, SynthesisInput{Facts: CanonicalFactBundle{Facts: []CanonicalFact{k7Fact(false)}}}, failure)

	if err != nil || len(result.ClaimedFacts) != 2 {
		t.Fatalf("err %v claims %d, want both scalars", err, len(result.ClaimedFacts))
	}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailFactScoreWithheld {
			t.Fatalf("a fact_score_withheld detail %+v for an answer that withheld nothing", detail)
		}
	}
}

func TestScoreLookupNamesTheDriversTableAWithheldScoreNeeded(t *testing.T) {
	lookup := scoreLookup(k7Capabilities())
	fact := k7Fact(false)
	if table, isScore := lookup(fact, "compounding_risk"); !isScore || table != "compounding_risk_drivers" {
		t.Fatalf("compounding_risk: table %q score %v", table, isScore)
	}
	if _, isScore := lookup(fact, "severity"); isScore {
		t.Fatal("severity is not a score")
	}
	if _, isScore := lookup(CanonicalFact{Kind: FactWorkload, Subject: fact.Subject}, "compounding_risk"); isScore {
		t.Fatal("a fact kind with no declaration has no score")
	}
}

// A score field can declare a different drivers table per subject kind (a team's and a project's health): the trace names
// each withheld field with its subject kind and the table that subject needed, not only the first one.
func TestTheWithheldScoreTraceNamesTheDriversTableOfEachSubjectKind(t *testing.T) {
	capabilities := []FactCapability{{
		Kind: FactHealth, SupportedSubjectKinds: []SubjectKind{SubjectTeam, SubjectProject},
		Fields: []FactFieldDeclaration{
			{Name: "compounding_risk", Type: FactFieldNumber, Score: true, DriversTable: "risk_rules", SubjectKinds: []SubjectKind{SubjectTeam}},
			{Name: "compounding_risk", Type: FactFieldNumber, Score: true, DriversTable: "risk_breakdown", SubjectKinds: []SubjectKind{SubjectProject}},
			{Name: "risk_rules", Type: FactFieldTable, SubjectKinds: []SubjectKind{SubjectTeam}},
			{Name: "risk_breakdown", Type: FactFieldTable, SubjectKinds: []SubjectKind{SubjectProject}},
		},
	}}
	risk := 0.5
	team := CanonicalFact{Kind: FactHealth, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"}, Fields: map[string]FactValue{"compounding_risk": {Number: &risk}}}
	project := CanonicalFact{Kind: FactHealth, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "P"}, Fields: map[string]FactValue{"compounding_risk": {Number: &risk}}}

	claims, _, _, withheld := mintDegradedFactClaimsWithholding([]CanonicalFact{team, project}, 100, scoreLookup(capabilities))

	if len(claims) != 0 || len(withheld) != 2 || withheld[0].DriversTable != "risk_rules" || withheld[1].DriversTable != "risk_breakdown" || withheld[1].SubjectKind != SubjectProject {
		t.Fatalf("claims %d withheld %+v, want no claims and one withheld score per subject kind with its own drivers table", len(claims), withheld)
	}
	if kinds := withheldKinds(withheld); len(kinds) != 1 || kinds[0] != FactHealth {
		t.Fatalf("withheld kinds %v, want health once", kinds)
	}
}

// The claim limit stops claims, not the search for withheld scores: with room for one claim, the score of a later fact
// is still named on the trace (the answer is truncated and the score is not claimed either way).
func TestTheClaimLimitDoesNotStopTheSearchForWithheldScores(t *testing.T) {
	capabilities := k7Capabilities()
	plain := CanonicalFact{Kind: FactStatus, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"},
		Fields: map[string]FactValue{"a": {String: ptrString("x")}, "b": {String: ptrString("y")}}, EvidenceRefIDs: []string{"ref_1"}}
	risk, severity := 0.5, "high"
	health := CanonicalFact{Kind: FactHealth, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"},
		Fields: map[string]FactValue{"compounding_risk": {Number: &risk}, "severity": {String: &severity}}, EvidenceRefIDs: []string{"ref_2"}}

	claims, _, truncated, withheld := mintDegradedFactClaimsWithholding([]CanonicalFact{plain, health}, 1, scoreLookup(capabilities))

	if len(claims) != 1 || !truncated {
		t.Fatalf("claims %d truncated %v, want one claim and a truncated answer", len(claims), truncated)
	}
	if len(withheld) != 1 || withheld[0].Field != "compounding_risk" || withheld[0].DriversTable != "compounding_risk_drivers" {
		t.Fatalf("withheld %+v, want the later fact's compounding_risk named", withheld)
	}
}
