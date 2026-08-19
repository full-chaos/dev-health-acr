package v1

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestContextFabricRequestedEvidenceWindow_Validate(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		window  ContextFabricRequestedEvidenceWindow
		wantErr bool
	}{
		{"relative id alone is legal (server derives bounds later)", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D}, false},
		{"relative id with agreeing-shaped explicit bounds", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start, End: &end}, false},
		{"all_time alone is legal", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime}, false},
		{"all_time with explicit bounds is rejected", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime, Start: &start, End: &end}, true},
		{"explicit bounds alone, no relative id, is legal", ContextFabricRequestedEvidenceWindow{Start: &start, End: &end}, false},
		{"neither relative id nor bounds is rejected", ContextFabricRequestedEvidenceWindow{}, true},
		{"unordered bounds are rejected", ContextFabricRequestedEvidenceWindow{Start: &end, End: &start}, true},
		{"unknown relative id is rejected", ContextFabricRequestedEvidenceWindow{RelativeID: "bogus"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.window.validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestContextFabricTimeContext_EvidenceWindowAxisLegality(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	window := &ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D}

	cases := []struct {
		name        string
		tc          ContextFabricTimeContext
		wantErr     bool
		wantAxisErr bool
	}{
		{"current axis with a window is legal", ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent, EvidenceWindow: window}, false, false},
		{"valid_time axis with a window is rejected", ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf, EvidenceWindow: window}, true, true},
		{"range axis with a window is rejected", ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &asOf, End: &asOf, EvidenceWindow: window}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tc.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantAxisErr && !errors.Is(err, ErrEvidenceWindowAxisInvalid) {
				t.Errorf("Validate() error = %v, want errors.Is(err, ErrEvidenceWindowAxisInvalid)", err)
			}
		})
	}
}

// TestContextFabricWindowOption_RequiresFrozenBoundsForNonAllTime is the
// codex review (W1 round 5) regression test: a WindowOption's own contract
// is that Start/End are the FROZEN bounds computed at offer time (unlike
// a bare ContextFabricRequestedEvidenceWindow, where a caller may legally
// send a RelativeID alone and let the server compute bounds later) -- a
// non-all_time option missing either bound must be rejected, or
// internal/contextfabric's own windowKeyComponent(effective, windowKeyFrozen)
// would silently fall back to a RelativeID-only key, reopening the
// cross-receipt collision round 4 closed for the well-formed case.
func TestContextFabricWindowOption_RequiresFrozenBoundsForNonAllTime(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	base := ContextFabricWindowOption{
		ReceiptID: "winr_confirm00001", OptionID: "opt_90d", Label: "the last 90 days",
		RelativeID: ContextFabricRelativeWindowTrailing90D,
	}

	t.Run("missing both bounds is rejected", func(t *testing.T) {
		opt := base
		if err := opt.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: a non-all_time option with no frozen bounds must be rejected")
		}
	})
	t.Run("missing end is rejected", func(t *testing.T) {
		opt := base
		opt.Start = &start
		if err := opt.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: a partially-frozen option must be rejected")
		}
	})
	t.Run("both bounds present is accepted", func(t *testing.T) {
		opt := base
		opt.Start, opt.End = &start, &end
		if err := opt.Validate(); err != nil {
			t.Errorf("Validate() error = %v, want nil", err)
		}
	})
	t.Run("all_time with no bounds is accepted", func(t *testing.T) {
		opt := base
		opt.RelativeID = ContextFabricRelativeWindowAllTime
		if err := opt.Validate(); err != nil {
			t.Errorf("Validate() error = %v, want nil (all_time has no bounds to freeze)", err)
		}
	})
	t.Run("receipt id without the winr_ prefix is rejected", func(t *testing.T) {
		opt := base
		opt.Start, opt.End = &start, &end
		opt.ReceiptID = "subr_confirm00001"
		if err := opt.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: receipt_id must carry the winr_ prefix")
		}
	})
}

// TestContextFabricWindowClarification_RoundTrip is a contract round-trip
// test (W1 acceptance criterion): a validly-constructed WindowClarification
// survives a JSON marshal/unmarshal cycle byte-for-byte in shape and
// re-validates.
func TestContextFabricWindowClarification_RoundTrip(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	original := ContextFabricWindowClarification{Options: []ContextFabricWindowOption{
		{ReceiptID: "winr_confirm00001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start, End: &end},
		{ReceiptID: "winr_confirm00002", OptionID: "opt_all", Label: "all time", RelativeID: ContextFabricRelativeWindowAllTime},
	}}
	if err := original.Validate(); err != nil {
		t.Fatalf("Validate() on the original error = %v", err)
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded ContextFabricWindowClarification
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("Validate() on the round-tripped value error = %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-Marshal() error = %v", err)
	}
	if string(encoded) != string(reencoded) {
		t.Errorf("round trip is not byte-identical:\n original = %s\n reencoded = %s", encoded, reencoded)
	}
}

// TestContextFabricWindowClarification_DuplicateReceiptOrOptionIDRejected
// pins design brief §5's own "m5" uniqueness invariant.
func TestContextFabricWindowClarification_DuplicateReceiptOrOptionIDRejected(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	t.Run("duplicate receipt_id", func(t *testing.T) {
		c := ContextFabricWindowClarification{Options: []ContextFabricWindowOption{
			{ReceiptID: "winr_confirm00001", OptionID: "opt_a", Label: "a", RelativeID: ContextFabricRelativeWindowTrailing30D, Start: &start, End: &end},
			{ReceiptID: "winr_confirm00001", OptionID: "opt_b", Label: "b", RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start, End: &end},
		}}
		if err := c.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error for duplicate receipt_id")
		}
	})
	t.Run("duplicate option_id", func(t *testing.T) {
		c := ContextFabricWindowClarification{Options: []ContextFabricWindowOption{
			{ReceiptID: "winr_confirm00001", OptionID: "opt_a", Label: "a", RelativeID: ContextFabricRelativeWindowTrailing30D, Start: &start, End: &end},
			{ReceiptID: "winr_confirm00002", OptionID: "opt_a", Label: "b", RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start, End: &end},
		}}
		if err := c.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error for duplicate option_id")
		}
	})
}
