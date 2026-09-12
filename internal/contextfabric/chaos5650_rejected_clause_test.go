package contextfabric

import (
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestSynthesisRejectionNamesTheRejectedClause is the driver/claim/finding
// CLASS SWEEP for the rejected clause, executed through the REAL validator:
// each case mutates the honestly-valid draft fixture, calls
// SynthesisDraft.ValidateAgainst itself, and asserts the reason AND the
// clause the resulting error carries.
//
// Both expectations are LITERALS, never values re-derived from the thing
// under test: asserting that the clause equals whatever the diagnoser
// returns for the same value could not fail.
//
// Before this, all three reasons named the struct and nothing else -- one
// name for twenty driver clauses, twelve claim clauses and ten finding
// clauses -- so a reader of the trace could not tell an overlong title from
// an out-of-vocabulary category from a driver that cited no evidence.
func TestSynthesisRejectionNamesTheRejectedClause(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(SynthesisInput, SynthesisDraft) SynthesisDraft
		wantReason SynthesisRejectionReason
		wantClause contractsv1.ContextFabricRejectedClause
		// wantBound is the violated_bound the SAME rejection reports, to pin
		// the documented complementarity: the clause names the field and
		// rule, and violated_bound is present only where that clause is a
		// registered maximum. A case expecting "" proves the clause carries
		// diagnosis that violated_bound structurally cannot.
		wantBound string
	}{
		{
			name: "driver category outside the closed vocabulary",
			mutate: func(_ SynthesisInput, draft SynthesisDraft) SynthesisDraft {
				draft.Drivers[0].Category = "not_a_category"
				return draft
			},
			wantReason: RejectionReasonDriverInvalid,
			wantClause: contractsv1.ContextFabricClauseDriverCategory,
			wantBound:  "",
		},
		{
			name: "driver title over its maximum",
			mutate: func(_ SynthesisInput, draft SynthesisDraft) SynthesisDraft {
				draft.Drivers[0].Title = overlongText(contractsv1.ContextFabricDriverTitleMaxLength + 1)
				return draft
			},
			wantReason: RejectionReasonDriverInvalid,
			wantClause: contractsv1.ContextFabricClauseDriverTitle,
			wantBound:  "synthesis.driver.title.max_length",
		},
		{
			name: "driver withheld with no qualification",
			mutate: func(_ SynthesisInput, draft SynthesisDraft) SynthesisDraft {
				draft.Drivers[0].Standing = DriverWithheld
				draft.Drivers[0].Qualification = ""
				return draft
			},
			wantReason: RejectionReasonDriverInvalid,
			wantClause: contractsv1.ContextFabricClauseDriverWithheldRequiresQualification,
			wantBound:  "",
		},
		{
			name: "claimed fact kind outside the closed vocabulary",
			mutate: func(input SynthesisInput, draft SynthesisDraft) SynthesisDraft {
				draft.ClaimedFacts = []ClaimedFact{{
					ClaimID: "claim_12345678", Kind: "not_a_fact_kind", Field: "release_ready",
					Subject: input.Graph.Resolution.Committed[0], Value: boolScalar(false),
				}}
				return draft
			},
			wantReason: RejectionReasonClaimInvalid,
			wantClause: contractsv1.ContextFabricClauseClaimKind,
			wantBound:  "",
		},
		{
			name: "finding kind outside the closed vocabulary",
			mutate: func(input SynthesisInput, draft SynthesisDraft) SynthesisDraft {
				draft.RemainingWork = []Finding{{
					FindingID: "finding_12345678", Kind: "not_a_category", Summary: "Summary",
					Subjects:       []SubjectRef{input.Graph.Resolution.Committed[0]},
					EvidenceRefIDs: []string{"evidence_release_1234"},
				}}
				return draft
			},
			wantReason: RejectionReasonFindingInvalid,
			wantClause: contractsv1.ContextFabricClauseFindingKindOutOfVocabulary,
			wantBound:  "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := validSynthesisInputFixture()
			draft := testCase.mutate(input, validSynthesisDraftFixture(input))

			err := draft.ValidateAgainst(input)
			if err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection")
			}
			if got := SynthesisRejectionReasonOf(err); got != testCase.wantReason {
				t.Fatalf("rejection reason = %q, want %q", got, testCase.wantReason)
			}
			clause, ok := SynthesisRejectionClauseOf(err)
			if !ok {
				t.Fatalf("SynthesisRejectionClauseOf() ok = false, want true -- the rejection names no clause")
			}
			if clause != testCase.wantClause {
				t.Fatalf("rejected clause = %q, want %q", clause, testCase.wantClause)
			}
			gotBound := ""
			var violation *ModelBoundViolation
			if errors.As(ClassifySynthesisRejection(draft, input, err), &violation) {
				gotBound = violation.Bound
			}
			if gotBound != testCase.wantBound {
				t.Fatalf("violated_bound = %q, want %q (clause %q)", gotBound, testCase.wantBound, clause)
			}
		})
	}
}

// TestSynthesisRejectionClauseIsAbsentWhereNoClauseRejected is the
// non-vacuous control pair. A clause reported for a rejection that is not a
// struct-validation rejection would be a wrong name, and a clause reported
// for a draft that VALIDATES would be an instrument reporting a measurement
// that never happened.
func TestSynthesisRejectionClauseIsAbsentWhereNoClauseRejected(t *testing.T) {
	input := validSynthesisInputFixture()

	valid := validSynthesisDraftFixture(input)
	if err := valid.ValidateAgainst(input); err != nil {
		t.Fatalf("the valid fixture must validate, got %v -- every case above mutates it", err)
	}

	// A rejection from a rule that is NOT one of the three struct
	// validations: the clause must be absent, not ContextFabricClauseNone
	// dressed up as a diagnosis.
	statusInvalid := validSynthesisDraftFixture(input)
	statusInvalid.Status = "not_a_status"
	err := statusInvalid.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonStatusInvalid {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonStatusInvalid)
	}
	if clause, ok := SynthesisRejectionClauseOf(err); ok {
		t.Fatalf("SynthesisRejectionClauseOf() = (%q, true), want ok=false for a non-struct-validation rejection", clause)
	}
}

func overlongText(n int) string {
	text := make([]byte, n)
	for i := range text {
		text[i] = 'a'
	}
	return string(text)
}
