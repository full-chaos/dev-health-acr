package api

import (
	"errors"
	"fmt"
	"os"
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
			// THE CASE EVERY FIXTURE HERE ORIGINALLY MISSED. `%q` of a value
			// containing a quote emits an ESCAPED quote; if the scanner treats
			// that as a terminator, redaction ends early and the rest of the
			// value is printed. Every other fixture in this table uses
			// quote-free values, so all of them passed while this shape leaked.
			// Found by adversarial review and reproduced before it was fixed.
			name: "a value containing a quote does not end redaction early",
			err: fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
				fmt.Errorf("cohort group %q has kind %q", `a"SECRET`, "team")),
			mustContain: []string{"cohort group", "has kind"},
			mustNotHave: []string{"SECRET", `a\"`},
		},
		{
			name: "a trailing backslash inside a quoted value cannot escape the closing quote",
			err: fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
				fmt.Errorf("subject %q rejected", `ends-with-backslash\`)),
			mustContain: []string{"rejected"},
			mustNotHave: []string{"ends-with-backslash"},
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

// The REPORTED limit, enforced by name so it cannot rot into a comment
// nobody checks: redaction covers quoted runs, and the two validator rules
// that interpolate OUTSIDE quotes must keep interpolating bounded values.
//
// A disclosure nothing verifies is indistinguishable from an omission. If a
// validator ever swaps one of these for a request-derived string, this test
// is what says so.
func TestValidationRuleUnquotedInterpolationsAreBounded(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("../contracts/v1/validate_context_fabric_result.go")
	if err != nil {
		t.Fatalf("cannot read the validator source: %v", err)
	}
	// The two known unquoted-interpolation rules. Each must still format a
	// BOOL (a bounded value), never %q-less string content.
	for _, want := range []string{
		`group-derived complete=%v)", groupComplete`,
		`group-derived truncated=%v)", groupTruncated`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("the reported limit named this unquoted interpolation and it is gone or changed: %q\nIf a validator now interpolates something else there, re-check whether it can carry request-derived content before updating this test.", want)
		}
	}
	// And the redactor's own behaviour on that shape, so the limit is
	// demonstrated rather than only asserted about the source.
	got := contextFabricValidationRule(fmt.Errorf("%w: %w", contextfabric.ErrInvalidResult,
		errors.New("cohort claims complete=true but its groups do not (group-derived complete=false)")))
	if !strings.Contains(got, "group-derived complete=false") {
		t.Fatalf("the documented limit says this bounded value IS emitted; it was not: %q", got)
	}
}
