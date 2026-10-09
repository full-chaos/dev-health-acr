package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readInvestmentWithFailingMix(t *testing.T, mixErr error) error {
	t.Helper()
	repo := repoUUID("refusal-a")
	client := &fakeClient{tables: []fakeTable{
		{match: scopeMixMatch, err: mixErr},
		{match: scopeRunMatch, rows: [][]any{{""}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-refusal"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repo, Label: "refusal-a"}},
	})
	return err
}

func TestReadBudgetRefusalNamesTheActionTheCapAndWhatWasMeasured(t *testing.T) {
	t.Parallel()
	err := readInvestmentWithFailingMix(t, &devhealthfacts.BudgetExceededError{
		Code: 307, CapBytes: 134217728, ReadRows: 288509, ReadBytes: 67158099, Cause: errors.New("server text with SELECT secret FROM x"),
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a FactReadFailure", err)
	}
	want := "devhealthfacts: query repository theme mix exceeded the read budget (limit 134217728 bytes; the server had read 288509 rows and 67158099 bytes); narrow the window or the subject"
	if failure.Reason != want {
		t.Fatalf("reason = %q, want %q", failure.Reason, want)
	}
	if failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("state = %q, want %q", failure.State, contextfabric.SourceUnavailable)
	}
	if strings.Contains(failure.Reason, "SELECT") || strings.Contains(failure.Reason, "secret") {
		t.Fatalf("reason %q leaks the server's exception text", failure.Reason)
	}
}

func TestReadBudgetRefusalWithoutProgressStillNamesTheCap(t *testing.T) {
	t.Parallel()
	err := readInvestmentWithFailingMix(t, &devhealthfacts.BudgetExceededError{Code: 307, CapBytes: 268435456, Cause: errors.New("x")})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a FactReadFailure", err)
	}
	if want := "devhealthfacts: query repository theme mix exceeded the read budget (limit 268435456 bytes); narrow the window or the subject"; failure.Reason != want {
		t.Fatalf("reason = %q, want %q", failure.Reason, want)
	}
}

func TestReadBudgetRefusalWithoutMeasurementStillNamesTheBudget(t *testing.T) {
	t.Parallel()
	err := readInvestmentWithFailingMix(t, &devhealthfacts.BudgetExceededError{Code: 158, Cause: errors.New("x")})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a FactReadFailure", err)
	}
	if want := "devhealthfacts: query repository theme mix exceeded the read budget (a row limit); narrow the window or the subject"; failure.Reason != want {
		t.Fatalf("reason = %q, want %q", failure.Reason, want)
	}
}

func TestOtherQueryErrorsKeepTheBareFailureReason(t *testing.T) {
	t.Parallel()
	err := readInvestmentWithFailingMix(t, errors.New("connection reset"))
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a FactReadFailure", err)
	}
	if want := "devhealthfacts: query repository theme mix failed"; failure.Reason != want {
		t.Fatalf("reason = %q, want %q", failure.Reason, want)
	}
}

func TestReadBudgetRefusalForARowLimitNamesARowLimitNotTheByteCap(t *testing.T) {
	t.Parallel()
	err := readInvestmentWithFailingMix(t, &devhealthfacts.BudgetExceededError{Code: 158, CapBytes: 268435456, ReadRows: 1001, ReadBytes: 9000, Cause: errors.New("x")})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a FactReadFailure", err)
	}
	want := "devhealthfacts: query repository theme mix exceeded the read budget (a row limit; the server had read 1001 rows and 9000 bytes); narrow the window or the subject"
	if failure.Reason != want {
		t.Fatalf("reason = %q, want %q", failure.Reason, want)
	}
	if strings.Contains(failure.Reason, "268435456") {
		t.Fatalf("reason %q names the byte cap for a row-limit refusal", failure.Reason)
	}
}
