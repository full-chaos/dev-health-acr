package v1

import "testing"

// TestSemanticReadingOverItsWholeInputDomain executes every cell of the
// disclosure's domain: each field absent, empty, out of vocabulary, a case
// variant, a near miss and canonical, crossed with the statuses that may and
// may not carry it, and the absent pointer.
func TestSemanticReadingOverItsWholeInputDomain(t *testing.T) {
	t.Parallel()
	unavailable := ContextFabricSemanticReadingUnavailable
	absent, unreadable := ContextFabricSemanticReadingStateAbsent, ContextFabricSemanticReadingStateUnreadable
	for _, testCase := range []struct {
		cell    string
		reading ContextFabricSemanticReading
		valid   bool
	}{
		{"canonical absent", ContextFabricSemanticReading{Status: unavailable, Reason: absent}, true},
		{"canonical unreadable", ContextFabricSemanticReading{Status: unavailable, Reason: unreadable}, true},
		{"status empty", ContextFabricSemanticReading{Reason: absent}, false},
		{"status out of vocabulary", ContextFabricSemanticReading{Status: "available", Reason: absent}, false},
		{"status case variant", ContextFabricSemanticReading{Status: "UNAVAILABLE", Reason: absent}, false},
		{"reason empty", ContextFabricSemanticReading{Status: unavailable}, false},
		{"reason out of vocabulary", ContextFabricSemanticReading{Status: unavailable, Reason: "pre_semantic_state"}, false},
		{"reason near miss", ContextFabricSemanticReading{Status: unavailable, Reason: "semantic_state_absent_"}, false},
		{"reason case variant", ContextFabricSemanticReading{Status: unavailable, Reason: "SEMANTIC_STATE_ABSENT"}, false},
		{"both empty", ContextFabricSemanticReading{}, false},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			if err := testCase.reading.Validate(); (err == nil) != testCase.valid {
				t.Fatalf("Validate() = %v, want valid=%v", err, testCase.valid)
			}
			reading := testCase.reading
			for status, allowed := range map[ContextFabricInvestigationStatus]bool{
				ContextFabricInvestigationClarificationRequired: true,
				ContextFabricInvestigationComplete:              false,
				ContextFabricInvestigationNoMatch:               false,
			} {
				err := validateSemanticReadingOnResult(&reading, status)
				if want := testCase.valid && allowed; (err == nil) != want {
					t.Fatalf("on status %q: err = %v, want valid=%v", status, err, want)
				}
			}
		})
	}
	if err := validateSemanticReadingOnResult(nil, ContextFabricInvestigationComplete); err != nil {
		t.Fatalf("an absent disclosure was refused: %v", err)
	}
	if len(ContextFabricSemanticReadingStatuses()) != 1 || len(ContextFabricSemanticReadingReasons()) != 2 {
		t.Fatal("the closed vocabularies moved without this domain moving with them")
	}
}
