package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The field exists to make an invalid_result 500 diagnosable. These tests pin
// the two halves of that: the RULE survives, and every quoted value does not.
func TestValidationRuleKeepsTheRuleAndRedactsQuotedValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		err         error
		mustContain []string
		mustNotHave []string
	}{
		{
			name: "a subject label in a %q never reaches the line",
			err: fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
				fmt.Errorf("answer plan groups %q members by their own kind", "Platform Engineering (EMEA)")),
			mustContain: []string{"answer plan groups", "members by their own kind", "<redacted>"},
			mustNotHave: []string{"Platform", "EMEA"},
		},
		{
			name: "a field path with no interpolation survives intact",
			err: fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
				errors.New("claimed fact rows+time_series_rows combined cell count violates v1 bounds")),
			mustContain: []string{"rows+time_series_rows", "violates v1 bounds"},
			mustNotHave: []string{"<redacted>"},
		},
		{
			name: "several quoted values are all removed, not just the first",
			err: fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
				fmt.Errorf("key fact %q cites evidence %q that is not present", "acr deploy cadence", "ev_secret_123")),
			mustContain: []string{"key fact", "cites evidence", "not present"},
			mustNotHave: []string{"cadence", "ev_secret_123"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contextFabricValidationRule(tc.err)
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("rule text lost %q -- the field is useless if the rule does not survive.\n got: %s", want, got)
				}
			}
			for _, banned := range tc.mustNotHave {
				if strings.Contains(got, banned) {
					t.Fatalf("QUOTED VALUE LEAKED: %q appears in %q", banned, got)
				}
			}
		})
	}
}

// Fail CLOSED on a malformed message: an unterminated quote must redact the
// tail rather than emit it. Without this, a validator whose message ends
// mid-quote would spill everything after the last quote onto the line.
func TestValidationRuleFailsClosedOnAnUnterminatedQuote(t *testing.T) {
	t.Parallel()
	got := contextFabricValidationRule(fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
		errors.New(`bound violated for "unterminated subject label and then some`)))
	if strings.Contains(got, "unterminated subject label") {
		t.Fatalf("the tail after an unbalanced quote leaked: %q", got)
	}
	if !strings.Contains(got, "<unterminated>") {
		t.Fatalf("an unbalanced quote must be marked, got %q", got)
	}
}

// The field is a log value; a pathological validator message must not be able
// to blow up the line.
func TestValidationRuleIsBounded(t *testing.T) {
	t.Parallel()
	got := contextFabricValidationRule(fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
		errors.New(strings.Repeat("rule ", 500))))
	if len(got) > 320 {
		t.Fatalf("validation_rule is %d bytes, want it bounded", len(got))
	}
	if !strings.HasSuffix(got, "<truncated>") {
		t.Fatalf("a truncated rule must say so, got the tail %q", got[max(0, len(got)-40):])
	}
}
