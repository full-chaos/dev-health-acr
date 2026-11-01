package contextfabric

import (
	"maps"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12487. The words of the answer scope and cohort meaning fields. Each row
// sets one served field and asserts the WHOLE map, so a word with no field in
// this result fails here too. The keys are spelled out: they are the contract a
// consumer reads.
func TestEachAnswerScopeFieldServesItsWord(t *testing.T) {
	cohort := func(set func(*contractsv1.ContextFabricCohort)) func(*InvestigationResult) {
		return func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{}
			set(r.Cohort)
		}
	}
	for name, row := range map[string]struct {
		set  func(*InvestigationResult)
		want map[string]string
	}{
		"one limitation left out": {
			func(r *InvestigationResult) { r.LimitationsDisplaced = 1 },
			map[string]string{"limitations_displaced:note": "1 limitation was left out of this answer."},
		},
		"more limitations left out": {
			func(r *InvestigationResult) { r.LimitationsDisplaced = 3 },
			map[string]string{"limitations_displaced:note": "3 limitations were left out of this answer."},
		},
		"generated at": {
			func(r *InvestigationResult) { r.GeneratedAt = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
			map[string]string{"generated_at:note": "Answered"},
		},
		"reused": {
			func(r *InvestigationResult) { r.Reused = true },
			map[string]string{"reused:note": "This is a stored answer, not a new investigation."},
		},
		"retrieval degraded": {
			func(r *InvestigationResult) { r.SubjectResolution.RetrievalDegraded = true },
			map[string]string{"retrieval_degraded:note": "Subject matching ran with one method unavailable, so fewer subjects may have been considered."},
		},
		"driver source state": {
			func(r *InvestigationResult) {
				r.Drivers = []contractsv1.ContextFabricDriverJudgment{{SourceState: contractsv1.ContextFabricSourceStale}, {SourceState: contractsv1.ContextFabricSourceTruncated}}
			},
			map[string]string{"driver_source_state:stale": "may be out of date", "driver_source_state:truncated": "partly included"},
		},
		"driver summary corrected": {
			func(r *InvestigationResult) {
				r.Drivers = []contractsv1.ContextFabricDriverJudgment{{SummaryCorrected: true}}
			},
			map[string]string{"summary_corrected:note": "The service corrected this driver's source state, so its summary may not reflect it."},
		},
		"score meaning": {
			cohort(func(c *contractsv1.ContextFabricCohort) {
				c.ScoreMeaning = contractsv1.ContextFabricCohortScoreMeaningAttention
			}),
			map[string]string{"cohort_score_meaning:attention": "Attention score: higher means more pressure. It does not measure performance."},
		},
		"judgment mismatch": {
			cohort(func(c *contractsv1.ContextFabricCohort) { c.JudgmentMismatch = true }),
			map[string]string{"judgment_mismatch:note": "This ranking does not support the judgment you asked for."},
		},
		"population": {
			cohort(func(c *contractsv1.ContextFabricCohort) { c.Population = 240 }),
			map[string]string{"population:note": "The full list has 240 members."},
		},
		"population of one": {
			cohort(func(c *contractsv1.ContextFabricCohort) { c.Population = 1 }),
			map[string]string{"population:note": "The full list has 1 member."},
		},
		"population lower bound": {
			cohort(func(c *contractsv1.ContextFabricCohort) { c.Population, c.PopulationLowerBound = 240, true }),
			map[string]string{"population:note": "The full list has at least 240 members."},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var result InvestigationResult
			row.set(&result)
			if got := serveWords(t, result); !maps.Equal(got, row.want) {
				t.Fatalf("map = %v, want %v", got, row.want)
			}
		})
	}
}

// A flag that is off, a zero and an absent field have no key: missing is not
// healthy, and a consumer shows nothing and implies nothing.
func TestAFieldThatSaysNothingHasNoWord(t *testing.T) {
	result := InvestigationResult{
		Drivers: []contractsv1.ContextFabricDriverJudgment{{}},
		Cohort:  &contractsv1.ContextFabricCohort{},
	}
	if got := serveWords(t, result); len(got) != 0 {
		t.Fatalf("an empty result served %v", got)
	}
}

// A value with no entry takes the generic path, and no raw token is a label.
func TestAnUnknownDriverStateOrScoreMeaningHasNoKey(t *testing.T) {
	result := InvestigationResult{
		Drivers: []contractsv1.ContextFabricDriverJudgment{{SourceState: "a_future_state"}},
		Cohort:  &contractsv1.ContextFabricCohort{ScoreMeaning: "a_future_meaning"},
	}
	if got := serveWords(t, result); len(got) != 0 {
		t.Fatalf("an unknown value served %v", got)
	}
}

// Near the bound the mismatch sentence stays: nothing else says it.
func TestTheMismatchSentenceSurvivesTheStatusTier(t *testing.T) {
	bare := correctedFixture()
	bare.Cohort = &contractsv1.ContextFabricCohort{ScoreMeaning: contractsv1.ContextFabricCohortScoreMeaningAttention, JudgmentMismatch: true}
	labeled, _ := ServeVocabularyLabels(bare, true, 0)
	served, tier := ServeVocabularyLabels(bare, true, encodedSize(t, labeled)-1)
	if tier != contractsv1.ContextFabricVocabularyLabelsStatusOnly {
		t.Fatalf("one byte short of the full map: tier %q", tier)
	}
	if served.VocabularyLabels["judgment_mismatch:note"] == "" {
		t.Fatalf("the status tier holds %v", served.VocabularyLabels)
	}
	if _, kept := served.VocabularyLabels["cohort_score_meaning:attention"]; kept {
		t.Fatalf("the status tier holds more than the sentence: %v", served.VocabularyLabels)
	}
}
