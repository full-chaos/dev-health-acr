package contextfabric

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func numberClaim(id string, kind FactKind, field string, value float64) ClaimedFact {
	return ClaimedFact{ClaimID: id, Kind: kind, Field: field, Value: contractsv1.ContextFabricScalarValue{Number: &value},
		Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:linear:CHAOS", Label: "Fullchaos"}}
}

func integerClaim(id string, kind FactKind, field string, value int64) ClaimedFact {
	return ClaimedFact{ClaimID: id, Kind: kind, Field: field, Value: contractsv1.ContextFabricScalarValue{Integer: &value},
		Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:linear:CHAOS", Label: "Fullchaos"}}
}

func phrasingResult(claims []ClaimedFact, summaries ...string) InvestigationResult {
	result := InvestigationResult{ResultID: "result_phrasing", ClaimedFacts: claims}
	for _, summary := range summaries {
		result.Drivers = append(result.Drivers, DriverJudgment{Title: "Driver", Summary: summary})
	}
	return result
}

// TestDerivedNumberPhrasingCountsAnAssertedScore is the ticket's own example: a
// judged score stated as an observation.
func TestDerivedNumberPhrasingCountsAnAssertedScore(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{numberClaim("claim-risk-0001", FactHealth, "compounding_risk", 0.73)}
	for _, sentence := range []string{
		"The compounding risk score is 0.73 with high severity as of 2026-10-22.",
		"Fullchaos has a current compounding risk score of 0.73.",
		"Elevated compounding risk score of 0.73 for Fullchaos.",
		"The operational-deficiency record reports a compounding risk score of 0.73.",
	} {
		got := MeasureDerivedNumberPhrasing(phrasingResult(claims, sentence))
		if got.DerivedValues != 1 || got.Sentences != 1 || got.Asserted != 1 {
			t.Errorf("%q: measure = %+v, want 1 derived value, 1 sentence, 1 asserted", sentence, got)
		}
	}
}

// TestDerivedNumberPhrasingDoesNotCountAQualifiedScore: appears, leans and
// suggests state the number as derived.
func TestDerivedNumberPhrasingDoesNotCountAQualifiedScore(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{numberClaim("claim-risk-0001", FactOperationalDeficiencies, "latest_compounding_risk", 0.55)}
	for _, sentence := range []string{
		"The compounding risk score appears to be 0.55 as of 2026-10-29.",
		"Risk leans high, at a compounding risk score of 0.55.",
		"A compounding risk score of 0.55 suggests compounding pressure.",
	} {
		got := MeasureDerivedNumberPhrasing(phrasingResult(claims, sentence))
		if got.Sentences != 1 || got.Asserted != 0 {
			t.Errorf("%q: measure = %+v, want 1 sentence, 0 asserted", sentence, got)
		}
	}
}

// TestDerivedNumberPhrasingLeavesObservationsAlone: a count read from a fact is
// an observation, and stating it plainly is right.
func TestDerivedNumberPhrasingLeavesObservationsAlone(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{
		integerClaim("claim-backlog-01", FactWorkload, "backlog_size", 114),
		numberClaim("claim-coverage-1", FactReadiness, "estimate_coverage_ratio", 0.86),
	}
	got := MeasureDerivedNumberPhrasing(phrasingResult(claims, "Fullchaos has a backlog of 114 items and an estimate coverage ratio of 0.86."))
	if got != (DerivedNumberPhrasing{}) {
		t.Fatalf("measure = %+v, want nothing derived", got)
	}
}

// TestDerivedNumberPhrasingMatchesEveryDisplayForm: the prompt asks prose for
// two decimals and whole percents, so a share stated either way is the same
// derived number, and so is its verbatim value.
func TestDerivedNumberPhrasingMatchesEveryDisplayForm(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{numberClaim("claim-quality-01", FactInvestment, "theme_quality", 0.52659815879)}
	for _, sentence := range []string{
		"Quality work represents 0.53 of the measured investment mix.",
		"Investment mix is concentrated in quality work at 53%.",
		"Quality work represents 0.52659815879 of the categorized investment mix.",
	} {
		got := MeasureDerivedNumberPhrasing(phrasingResult(claims, sentence))
		if got.Sentences != 1 || got.Asserted != 1 {
			t.Errorf("%q: measure = %+v, want 1 sentence, 1 asserted", sentence, got)
		}
	}
}

// TestDerivedNumberPhrasingCountsADerivation: a value the synthesizer computed
// (derived_from) is derived whatever field it names.
func TestDerivedNumberPhrasingCountsADerivation(t *testing.T) {
	t.Parallel()
	derivation := numberClaim("claim-ratio-0001", FactWorkload, "completion_share", 0.18)
	derivation.DerivedFrom = []contractsv1.ContextFabricDerivationInput{{FactID: "fact-a", Field: "items_started"}, {FactID: "fact-b", Field: "items_completed"}}
	derivation.Operation = contractsv1.ContextFabricDerivationRatio
	got := MeasureDerivedNumberPhrasing(phrasingResult([]ClaimedFact{derivation}, "Started work is 0.18 of completed work."))
	if got.DerivedValues != 1 || got.Sentences != 1 || got.Asserted != 1 {
		t.Fatalf("measure = %+v, want the derivation counted", got)
	}
}

// TestDerivedNumberPhrasingDoesNotMatchAWholeNumberToAZeroShare: a share of 0
// must not turn every "0 repositories" into a derived number.
func TestDerivedNumberPhrasingDoesNotMatchAWholeNumberToAZeroShare(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{numberClaim("claim-risk-theme", FactInvestment, "theme_risk", 0)}
	got := MeasureDerivedNumberPhrasing(phrasingResult(claims, "There are 0 repositories with incident data."))
	if got.Sentences != 0 {
		t.Fatalf("measure = %+v, want no derived-number sentence", got)
	}
	got = MeasureDerivedNumberPhrasing(phrasingResult(claims, "Risk work is 0.00 of the mix."))
	if got.Sentences != 1 || got.Asserted != 1 {
		t.Fatalf("measure = %+v, want the two-decimal zero share counted", got)
	}
}

// TestDerivedNumberPhrasingSplitsSentencesNotDecimals: one hedge does not cover
// the next sentence, and a decimal point never ends a sentence.
func TestDerivedNumberPhrasingSplitsSentencesNotDecimals(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{numberClaim("claim-risk-0001", FactHealth, "compounding_risk", 0.55)}
	got := MeasureDerivedNumberPhrasing(phrasingResult(claims, "The compounding risk score appears to be 0.55. Last week the score was 0.55; it is unchanged"))
	if got.Sentences != 2 || got.Asserted != 1 {
		t.Fatalf("measure = %+v, want 2 sentences, 1 asserted", got)
	}
}

// TestDerivedNumberPhrasingReadsEveryReaderFacingField: each field a reader is
// given is read on its own, and current_state (a composed label: value list) is
// not.
func TestDerivedNumberPhrasingReadsEveryReaderFacingField(t *testing.T) {
	t.Parallel()
	claims := []ClaimedFact{numberClaim("claim-risk-0001", FactHealth, "compounding_risk", 0.55)}
	const asserted = "The compounding risk score is 0.55."
	fields := map[string]func(*InvestigationResult){
		"direct_judgment":      func(r *InvestigationResult) { r.DirectJudgment = asserted },
		"deterministic_answer": func(r *InvestigationResult) { r.DeterministicAnswer = asserted },
		"strongest_pressures":  func(r *InvestigationResult) { r.StrongestPressures = []string{asserted} },
		"driver title":         func(r *InvestigationResult) { r.Drivers = []DriverJudgment{{Title: asserted}} },
		"driver summary":       func(r *InvestigationResult) { r.Drivers = []DriverJudgment{{Summary: asserted}} },
		"driver qualification": func(r *InvestigationResult) { r.Drivers = []DriverJudgment{{Qualification: asserted}} },
		"remaining_work":       func(r *InvestigationResult) { r.RemainingWork = []Finding{{Summary: asserted}} },
		"readiness_gaps":       func(r *InvestigationResult) { r.ReadinessGaps = []Finding{{Summary: asserted}} },
		"conflicts":            func(r *InvestigationResult) { r.Conflicts = []Finding{{Summary: asserted}} },
		"limitations":          func(r *InvestigationResult) { r.Limitations = []string{asserted} },
		"warnings":             func(r *InvestigationResult) { r.Warnings = []string{asserted} },
	}
	for name, set := range fields {
		result := InvestigationResult{ClaimedFacts: claims}
		set(&result)
		if got := MeasureDerivedNumberPhrasing(result); got.Sentences != 1 || got.Asserted != 1 {
			t.Errorf("%s: measure = %+v, want the field read", name, got)
		}
	}
	result := InvestigationResult{ClaimedFacts: claims, CurrentState: "Current observed values: compounding risk score: 0.55 (Fullchaos)."}
	if got := MeasureDerivedNumberPhrasing(result); got.Sentences != 0 {
		t.Fatalf("current_state: measure = %+v, want it not read", got)
	}
}

// TestSavedAnswerLogsItsDerivedNumberPhrasing: every saved answer carries its
// measure on the run's own log, so a corpus run is diagnosable from its logs.
func TestSavedAnswerLogsItsDerivedNumberPhrasing(t *testing.T) {
	buf := captureDefaultJSONLogger(t)
	engine := &Engine{results: &staticResultStore{results: map[string]InvestigationResult{}}}
	result := phrasingResult([]ClaimedFact{numberClaim("claim-risk-0001", FactHealth, "compounding_risk", 0.73)},
		"The compounding risk score is 0.73.", "The compounding risk score appears to be 0.73.")
	result.Versions.SynthesisSource = SynthesisSourceServer
	_ = engine.saveResult(context.Background(), acceptancePrincipal(), BudgetAssertDecisive, result, nil, nil, "", 0, "", absentSemanticState(SemanticStateAbsenceContinuationRefused))
	var line map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(raw), &entry) == nil && entry["msg"] == "context fabric derived number phrasing" {
			line = entry
		}
	}
	if line == nil {
		t.Fatalf("no derived number phrasing line; log:\n%s", buf.String())
	}
	if line["result_id"] != "result_phrasing" || line["synthesis_source"] != string(SynthesisSourceServer) ||
		line["cf_derived_values"] != float64(1) || line["cf_derived_number_sentences"] != float64(2) || line["cf_derived_number_asserted"] != float64(1) {
		t.Fatalf("line = %v, want result_phrasing, server, 1 value, 2 sentences, 1 asserted", line)
	}
	if strings.Contains(buf.String(), "is 0.73") {
		t.Fatal("the log carries answer prose; it must carry counts only")
	}
}
