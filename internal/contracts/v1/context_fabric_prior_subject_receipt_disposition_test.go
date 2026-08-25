package v1

import (
	"encoding/json"
	"testing"
)

// Round-trip and fail-closed pins for
// ContextFabricSubjectResolution.PriorSubjectReceiptDispositions
// (CHAOS-3478/CHAOS-3813), mirroring
// context_fabric_commit_decision_digest_test.go's exact shape -- an
// additive-optional v1 addition to the same contract unit.

func resolutionWithPriorSubjectReceiptDispositions(entries []ContextFabricPriorSubjectReceiptEntry) ContextFabricSubjectResolution {
	return ContextFabricSubjectResolution{
		Candidates:                      []ContextFabricSubjectCandidate{},
		Committed:                       []ContextFabricSubjectRef{},
		PriorSubjectReceiptDispositions: entries,
	}
}

func TestCHAOS3478_PriorSubjectReceiptDispositionsRoundTripsBothWays(t *testing.T) {
	entries := []ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: ContextFabricPriorSubjectReceiptApplied},
	}
	resolution := resolutionWithPriorSubjectReceiptDispositions(entries)
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ContextFabricSubjectResolution
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.PriorSubjectReceiptDispositions) != 1 {
		t.Fatalf("PriorSubjectReceiptDispositions round-tripped %d entries, want 1", len(decoded.PriorSubjectReceiptDispositions))
	}
	got := decoded.PriorSubjectReceiptDispositions[0]
	if got != entries[0] {
		t.Fatalf("entry round-tripped as %+v, want %+v", got, entries[0])
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("a resolution with a valid disposition entry must validate: %v", err)
	}
}

// nil (absent) must round-trip as absent, not as an empty array --
// "never attempted / nothing sent" must stay distinguishable from
// "attempted, echoed zero entries" (which cannot actually happen by
// construction, but the wire form must not blur the two).
func TestCHAOS3478_AbsentPriorSubjectReceiptDispositionsOmitTheFieldEntirely(t *testing.T) {
	resolution := resolutionWithPriorSubjectReceiptDispositions(nil)
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["prior_subject_receipt_dispositions"]; present {
		t.Fatalf("a resolution with nil dispositions must omit prior_subject_receipt_dispositions from the wire form, got %s", encoded)
	}
}

// A resolution persisted before CHAOS-3478 has no such key and must still
// decode and validate on replay.
func TestCHAOS3478_PreCHAOS3478SnapshotDecodesAndValidates(t *testing.T) {
	legacy := `{"candidates":[],"committed":[]}`
	var replayed ContextFabricSubjectResolution
	if err := json.Unmarshal([]byte(legacy), &replayed); err != nil {
		t.Fatalf("a pre-CHAOS-3478 resolution must decode: %v", err)
	}
	if replayed.PriorSubjectReceiptDispositions != nil {
		t.Fatal("an absent prior_subject_receipt_dispositions must decode as nil")
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("a pre-CHAOS-3478 resolution must still validate: %v", err)
	}
}

// Disposition is a closed vocabulary -- an unrecognized value must be
// rejected, not silently accepted as a new, undocumented outcome.
func TestCHAOS3478_UnrecognizedDispositionIsRejected(t *testing.T) {
	resolution := resolutionWithPriorSubjectReceiptDispositions([]ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: "not_a_real_disposition"},
	})
	if err := resolution.Validate(); err == nil {
		t.Fatal("an unrecognized disposition value must fail validation")
	}
}

// PriorResultID/ReceiptID share ContextFabricBoundSubjectReceipt's own
// 8-256 character bound -- an entry that violates it must be rejected,
// the same discipline every other identity-bearing field in this contract
// gets.
func TestCHAOS3478_PriorSubjectReceiptDispositionEntryBoundsAreEnforced(t *testing.T) {
	cases := []struct {
		name  string
		entry ContextFabricPriorSubjectReceiptEntry
	}{
		{"blank prior_result_id", ContextFabricPriorSubjectReceiptEntry{PriorResultID: "", ReceiptID: "receipt_abc12345", Disposition: ContextFabricPriorSubjectReceiptApplied}},
		{"blank receipt_id", ContextFabricPriorSubjectReceiptEntry{PriorResultID: "result_prior_1", ReceiptID: "", Disposition: ContextFabricPriorSubjectReceiptApplied}},
		{"too-short receipt_id", ContextFabricPriorSubjectReceiptEntry{PriorResultID: "result_prior_1", ReceiptID: "r1", Disposition: ContextFabricPriorSubjectReceiptApplied}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolution := resolutionWithPriorSubjectReceiptDispositions([]ContextFabricPriorSubjectReceiptEntry{tc.entry})
			if err := resolution.Validate(); err == nil {
				t.Fatalf("entry %+v violates v1 bounds and must fail validation", tc.entry)
			}
		})
	}
}

// The response can never echo more entries than a request could have
// carried (validateStructureReceiptField's own 20-receipt cap on the
// request side) -- a response exceeding that bound is a defect, not a
// forward-compat gap to tolerate.
func TestCHAOS3478_PriorSubjectReceiptDispositionsExceedingCapIsRejected(t *testing.T) {
	entries := make([]ContextFabricPriorSubjectReceiptEntry, 0, 21)
	for i := 0; i < 21; i++ {
		entries = append(entries, ContextFabricPriorSubjectReceiptEntry{
			PriorResultID: "result_prior_00000001", ReceiptID: "receipt_abc12345", Disposition: ContextFabricPriorSubjectReceiptApplied,
		})
	}
	resolution := resolutionWithPriorSubjectReceiptDispositions(entries)
	if err := resolution.Validate(); err == nil {
		t.Fatal("21 disposition entries exceeds the 20-receipt v1 bound and must fail validation")
	}
}
