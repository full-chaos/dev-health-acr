package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type fakeOperationCaller struct {
	outcome    devhealthfacts.OperationOutcome
	err        error
	calls      int
	operation  string
	principals []storage.Principal
}

func (f *fakeOperationCaller) CallOperation(_ context.Context, principal storage.Principal, operation string) (devhealthfacts.OperationOutcome, error) {
	f.calls++
	f.operation = operation
	f.principals = append(f.principals, principal)
	return f.outcome, f.err
}

func sourceHealthHolder(caller devhealthfacts.OperationCaller) *devhealthfacts.OperationHolder {
	holder := devhealthfacts.NewOperationHolder()
	holder.Set(caller)
	return holder
}

func sourceHealthData(rows ...string) json.RawMessage {
	return json.RawMessage(`{"sourceHealth":[` + strings.Join(rows, ",") + `]}`)
}

func sourceHealthProvider(t *testing.T, holder *devhealthfacts.OperationHolder) contextfabric.FactProvider {
	t.Helper()
	return findProvider(t, devhealthfacts.NewProvidersWithOperations(&fakeClient{}, holder), contextfabric.FactSourceHealth)
}

func readSourceHealth(t *testing.T, provider contextfabric.FactProvider, principal storage.Principal, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactSourceHealth, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

func fieldString(t *testing.T, fact contextfabric.CanonicalFact, name string) string {
	t.Helper()
	value, ok := fact.Fields[name]
	if !ok || value.String == nil {
		t.Fatalf("field %q = %#v, want a string", name, value)
	}
	return *value.String
}

func fieldNull(fact contextfabric.CanonicalFact, name string) bool {
	value, ok := fact.Fields[name]
	return ok && value.String == nil
}

func TestSourceHealthProviderServesOneFactPerSource(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
		`{"provider":"github","scope":"git, prs","lastSyncAt":"2026-10-08T01:00:00Z","lastFailure":null}`,
		`{"provider":"gitlab","scope":"all","lastSyncAt":null,"lastFailure":{"occurredAt":"2026-10-08T02:00:00Z","stage":"provider_rate_limited"}}`,
		`{"provider":"jira","scope":"other","lastSyncAt":null,"lastFailure":null}`)}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if result.State != contextfabric.SourceAvailable || len(result.Facts) != 3 {
		t.Fatalf("state = %q, facts = %d, want available and 3", result.State, len(result.Facts))
	}
	if caller.operation != devhealthfacts.SourceHealthOperationName || caller.calls != 1 {
		t.Fatalf("operation = %q calls = %d", caller.operation, caller.calls)
	}
	healthy, failed, never := result.Facts[0], result.Facts[1], result.Facts[2]
	if fieldString(t, healthy, "provider") != "github" || fieldString(t, healthy, "scope") != "git, prs" || fieldString(t, healthy, "last_sync_at") != "2026-10-08T01:00:00Z" {
		t.Fatalf("healthy = %#v", healthy.Fields)
	}
	if !fieldNull(healthy, "last_failure_occurred_at") || !fieldNull(healthy, "last_failure_stage") {
		t.Fatalf("healthy failure fields = %#v, want null", healthy.Fields)
	}
	if !fieldNull(failed, "last_sync_at") || fieldString(t, failed, "last_failure_occurred_at") != "2026-10-08T02:00:00Z" || fieldString(t, failed, "last_failure_stage") != "provider_rate_limited" {
		t.Fatalf("failed = %#v", failed.Fields)
	}
	if !fieldNull(never, "last_sync_at") || !fieldNull(never, "last_failure_occurred_at") || !fieldNull(never, "last_failure_stage") {
		t.Fatalf("never synced = %#v, want all three null", never.Fields)
	}
}

func TestSourceHealthProviderRootNotEnabledIsALimitationNotComplete(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Reason: "operation_unavailable"}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 0 || result.State == contextfabric.SourceAvailable || result.State == contextfabric.SourceNoData {
		t.Fatalf("result = %+v, want no facts and a state that is neither available nor no_data", result)
	}
	if !strings.Contains(result.Reason, "operation_unavailable") {
		t.Fatalf("reason = %q, want the closed call status", result.Reason)
	}
}

func TestSourceHealthProviderCallErrorIsALimitation(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{err: errors.New("boom")}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 0 || result.State != contextfabric.SourceUnavailable {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(result.Reason, "boom") {
		t.Fatalf("reason = %q leaks the error text", result.Reason)
	}
}

func TestSourceHealthProviderUnreadableAnswerIsALimitation(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: json.RawMessage(`{"sourceHealth":"nope"}`)}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 0 || result.State != contextfabric.SourceUnavailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestSourceHealthProviderEmptyListIsNoData(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData()}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestSourceHealthProviderIncompleteAnswerIsTruncated(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: false, Data: sourceHealthData(
		`{"provider":"github","scope":"all","lastSyncAt":null,"lastFailure":null}`)}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
	if len(result.Facts) != 1 || result.State != contextfabric.SourceTruncated || !result.Truncated || result.Reason == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSourceHealthProviderRepositoryScopedCallerGetsALimitationAndNoCall(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
		`{"provider":"github","scope":"all","lastSyncAt":null,"lastFailure":null}`)}}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"repo-1"}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), principal, organizationSubject("org-1"))
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNotApplicable {
		t.Fatalf("result = %+v, want no facts and not_applicable", result)
	}
	if result.Reason != "org-level fact; caller scope is repository-bound" {
		t.Fatalf("reason = %q", result.Reason)
	}
	if caller.calls != 0 {
		t.Fatalf("calls = %d, want the runner never reached for a repository-scoped caller", caller.calls)
	}
}

func TestSourceHealthProviderUniversalScopeIsServed(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
		`{"provider":"github","scope":"all","lastSyncAt":null,"lastFailure":null}`)}}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), principal, organizationSubject("org-1"))
	if len(result.Facts) != 1 || caller.calls != 1 {
		t.Fatalf("result = %+v calls = %d", result, caller.calls)
	}
}

func TestSourceHealthProviderUnsetHolderIsALimitation(t *testing.T) {
	t.Parallel()
	for name, holder := range map[string]*devhealthfacts.OperationHolder{"nil": nil, "unset": devhealthfacts.NewOperationHolder()} {
		result := readSourceHealth(t, sourceHealthProvider(t, holder), storage.Principal{OrgID: "org-1"}, organizationSubject("org-1"))
		if len(result.Facts) != 0 || result.State != contextfabric.SourceUnconfigured || result.Reason == "" {
			t.Fatalf("%s: result = %+v", name, result)
		}
	}
}

func TestSourceHealthProviderRejectsMismatchedOrganizationSubject(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(
		`{"provider":"github","scope":"all","lastSyncAt":null,"lastFailure":null}`)}}
	result := readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-1"}, organizationSubject("org-2"))
	if len(result.Facts) != 0 || caller.calls != 0 {
		t.Fatalf("facts = %#v calls = %d, want no facts and no call for another organization", result.Facts, caller.calls)
	}
}

func TestSourceHealthProviderCallsAsTheCallerPrincipal(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData()}}
	readSourceHealth(t, sourceHealthProvider(t, sourceHealthHolder(caller)), storage.Principal{OrgID: "org-8"}, organizationSubject("org-8"))
	if len(caller.principals) != 1 || caller.principals[0].OrgID != "org-8" {
		t.Fatalf("principals = %#v, want the caller's own org", caller.principals)
	}
}

func TestSourceHealthProviderNonCurrentAxisIsNotApplicableAndNoCall(t *testing.T) {
	t.Parallel()
	caller := &fakeOperationCaller{}
	provider := sourceHealthProvider(t, sourceHealthHolder(caller))
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalObservedTime},
		Kind: contextfabric.FactSourceHealth, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
	})
	if err != nil || result.State != contextfabric.SourceNotApplicable || caller.calls != 0 {
		t.Fatalf("result = %+v err = %v calls = %d", result, err, caller.calls)
	}
}
