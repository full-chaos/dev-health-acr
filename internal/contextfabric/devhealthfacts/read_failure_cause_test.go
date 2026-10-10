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

// readFailureCause names the error a failed read carried, for a test's failure
// output: the served reason is closed and never holds it.
func readFailureCause(err error) string {
	var failure *contextfabric.FactReadFailure
	if errors.As(err, &failure) && failure.Cause != nil {
		return failure.Cause.Error()
	}
	return "(no cause carried)"
}

func TestAFailedProjectMixReadCarriesItsCauseButNeverServesIt(t *testing.T) {
	t.Parallel()
	boom := errors.New("clickhouse exploded: secret-fragment")
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_membership_runs", err: boom}, {match: "sum(cityHash64(", err: boom}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-cause"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:jira:PRJ", Label: "PRJ"}},
	})
	if err == nil {
		t.Fatal("the read did not fail")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the failure does not carry its cause: %v", err)
	}
	if strings.Contains(err.Error(), "secret-fragment") {
		t.Fatalf("the served reason leaks the cause: %q", err.Error())
	}
	if got := readFailureCause(err); !strings.Contains(got, "secret-fragment") {
		t.Fatalf("readFailureCause = %q, want the cause text", got)
	}
}
