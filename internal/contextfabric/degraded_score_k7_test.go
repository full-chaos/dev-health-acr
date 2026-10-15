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
