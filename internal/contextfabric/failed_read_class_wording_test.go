package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"
)

const (
	failedReadGeneric   = "The blockers read failed; the facts for it are not shown."
	failedReadTimedOut  = "The blockers read timed out; the facts for it are not shown."
	failedReadMemory    = "The blockers read stopped at its memory limit; the facts for it are not shown."
	failedReadBudget    = "The blockers read needed more data than one answer may read; narrow the window or the subject; the facts for it are not shown."
	failedReadContended = "The blockers read did not run because the system was busy; try again later; the facts for it are not shown."
	failedReadRejected  = "canonical fact provider returned a result that was rejected"

	failedReadProviderReason = "devhealthfacts: query team blockers exceeded the read budget (limit 134217728 bytes; the server had read 1000 rows and 2000 bytes); narrow the window or the subject"
)

func failedReadCases() []struct {
	name    string
	failure *FactReadFailure
	want    string
} {
	return []struct {
		name    string
		failure *FactReadFailure
		want    string
	}{
		{"budget class 307", &FactReadFailure{State: SourceUnavailable, Reason: failedReadProviderReason, Class: FactReadFailureBudgetExceeded}, failedReadBudget},
		{"budget class 158", &FactReadFailure{State: SourceUnavailable, Reason: failedReadProviderReason, Class: FactReadFailureBudgetExceeded}, failedReadBudget},
		{"read failed code 307", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed", Class: FactReadFailureReadFailed, SourceCode: 307}, failedReadBudget},
		{"read failed code 159", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed", Class: FactReadFailureReadFailed, SourceCode: 159}, failedReadTimedOut},
		{"contended", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query project mix contended: its inputs changed during every read attempt; retry", Class: FactReadFailureContended}, failedReadContended},
		{"control memory 241", &FactReadFailure{State: SourceUnavailable, Reason: FactReadMemoryLimitReason, Class: FactReadFailureReadFailed, SourceCode: 241}, failedReadMemory},
		{"control rejected", &FactReadFailure{State: SourceUnavailable, Reason: factResultRejectedReason, Class: FactReadFailureResultRejected}, failedReadRejected},
		{"control organization required", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: authenticated organization is required", Class: FactReadFailureOrganizationRequired}, failedReadGeneric},
		{"control result rows 396", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed", Class: FactReadFailureReadFailed, SourceCode: 396}, failedReadGeneric},
		{"control other code", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed", Class: FactReadFailureReadFailed, SourceCode: 999}, failedReadGeneric},
		{"control no code", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed", Class: FactReadFailureReadFailed}, failedReadGeneric},
		{"control unclassified", &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query team blockers failed"}, failedReadGeneric},
		// The class is the key, never the provider's wording: budget text in the
		// Reason of a read_failed (or unclassified) failure with no code is generic.
		{"mutant budget text without the class", &FactReadFailure{State: SourceUnavailable, Reason: failedReadProviderReason, Class: FactReadFailureReadFailed}, failedReadGeneric},
		{"mutant budget text unclassified", &FactReadFailure{State: SourceUnavailable, Reason: failedReadProviderReason}, failedReadGeneric},
		{"mutant class budget with a timeout code", &FactReadFailure{State: SourceUnavailable, Reason: failedReadProviderReason, Class: FactReadFailureBudgetExceeded, SourceCode: 159}, failedReadBudget},
	}
}

// A failed fact read is served by its class and ClickHouse code, not as a
// generic failure.
func TestAFailedReadIsServedByItsClass(t *testing.T) {
	t.Parallel()
	for _, tc := range failedReadCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, reason := classifyFactReadError(FactBlockers, tc.failure)
			if state != SourceUnavailable {
				t.Fatalf("state %q, want %q", state, SourceUnavailable)
			}
			if reason != tc.want {
				t.Fatalf("reason %q, want %q", reason, tc.want)
			}
		})
	}
}

// The five sentences of the family are different from each other.
func TestAFailedReadSentenceFamilyIsDistinct(t *testing.T) {
	t.Parallel()
	sentences := map[string]string{
		"budget": failedReadBudget, "contended": failedReadContended, "timed out": failedReadTimedOut,
		"memory": failedReadMemory, "generic": failedReadGeneric,
	}
	served := map[string]string{}
	for _, tc := range failedReadCases() {
		switch tc.name {
		case "budget class 307", "contended", "read failed code 159", "control memory 241", "control no code":
			_, reason := classifyFactReadError(FactBlockers, tc.failure)
			served[tc.name] = reason
		}
	}
	seen := map[string]string{}
	for name, reason := range served {
		if other, dup := seen[reason]; dup {
			t.Errorf("%s and %s are served the same sentence %q", name, other, reason)
		}
		seen[reason] = name
	}
	if len(seen) != len(sentences) {
		t.Errorf("served %d distinct sentences, want %d", len(seen), len(sentences))
	}
}

// M6: no served sentence of the failed-read family carries a digit, the
// provider's Reason, or the Cause text.
func TestAFailedReadSentenceCarriesNoProviderDetail(t *testing.T) {
	t.Parallel()
	const secretCause = "SECRETCAUSE 424242 select secret_column"
	for _, tc := range failedReadCases() {
		failure := *tc.failure
		failure.Cause = errors.New(secretCause)
		_, reason := classifyFactReadError(FactBlockers, &failure)
		for _, r := range reason {
			if unicode.IsDigit(r) {
				t.Errorf("%s: sentence %q carries a digit", tc.name, reason)
				break
			}
		}
		for _, banned := range []string{"devhealthfacts", "134217728", "SECRETCAUSE", "secret_column", "rows and", "bytes"} {
			if strings.Contains(reason, banned) {
				t.Errorf("%s: sentence %q carries %q", tc.name, reason, banned)
			}
		}
	}
	if _, plain := classifyFactReadError(FactBlockers, errors.New("x")); plain != failedReadGeneric {
		t.Errorf("plain error sentence %q", plain)
	}
	if _, deadline := classifyFactReadError(FactBlockers, context.DeadlineExceeded); deadline != failedReadTimedOut {
		t.Errorf("deadline sentence %q", deadline)
	}
}
