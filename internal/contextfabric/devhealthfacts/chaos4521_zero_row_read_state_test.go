package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4521, Run J wall A. A project-status question committed a real
// Linear project subject, Coverage reported SIX of nine capabilities
// `available`, and the bundle carried ZERO canonical facts -- so synthesis
// succeeded and minted zero claims, and the answer said "No canonical facts
// were observed" beside a coverage block claiming the sources answered.
//
// The contradiction is minted here, not in synthesis: on the current time
// axis every provider reported SourceAvailable for a read that returned no
// rows at all. `available` asserts the source ANSWERED; a source that was
// reached and held nothing for the requested subjects has answered
// "nothing", which the closed SourceState vocabulary already spells
// no_data. Reporting the two identically is exactly North Star check 12
// ("missing is not healthy -- unknown/stale/sparse/not-applicable/zero are
// distinct"), and it is what made the live failure undiagnosable from the
// run's own artifacts: nothing in the completed result distinguished "six
// sources answered and the model ignored them" from "six sources returned
// no rows".
//
// The live shape this pins, executed read-only against the kiac acr-local
// trial plane on 2026-08-29 (org 70d529e0-3c06-4597-8480-794fd02328b6):
// every Linear row in `projects` carries project_key NULL, while
// team_project_ownership only carries project_key 'CHAOS'. Every project
// rollup joins projects->team_project_ownership on project_key, so the
// committed subject is dropped inside the SQL and each provider returns
// zero rows -- and then calls that `available`.
func TestChaos4521_AZeroRowCurrentAxisReadIsNoDataNotAvailable(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1"}
	cases := []struct {
		name    string
		kind    contextfabric.FactKind
		subject contextfabric.SubjectRef
	}{
		{"health/project", contextfabric.FactHealth, projectSubject("linear", "6241316a")},
		{"flow/project", contextfabric.FactFlow, projectSubject("linear", "6241316a")},
		{"investment/project", contextfabric.FactInvestment, projectSubject("linear", "6241316a")},
		{"landscape/project", contextfabric.FactLandscape, projectSubject("linear", "6241316a")},
		{"readiness/project", contextfabric.FactReadiness, projectSubject("linear", "6241316a")},
		{"workload/project", contextfabric.FactWorkload, projectSubject("linear", "6241316a")},
		{"health/repository", contextfabric.FactHealth, repoSubject("repo-1")},
		{"metrics/repository", contextfabric.FactMetrics, repoSubject("repo-1")},
		{"workload/team", contextfabric.FactWorkload, teamSubject("CHAOS")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// No fakeTable entries at all: every query this provider runs
			// returns an empty scanner, which is precisely the live shape.
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), testCase.kind)
			result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: testCase.kind, Subjects: []contextfabric.SubjectRef{testCase.subject},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if len(result.Facts) != 0 {
				t.Fatalf("precondition: expected a zero-fact read, got %d facts", len(result.Facts))
			}
			if len(client.queries) == 0 {
				t.Fatalf("precondition: expected the provider to actually query, got no queries")
			}
			if result.State != contextfabric.SourceNoData {
				t.Errorf("zero-row read reported state %q, want %q -- a source that was reached and held nothing must not claim it answered",
					result.State, contextfabric.SourceNoData)
			}
			if strings.TrimSpace(result.Reason) == "" {
				t.Errorf("zero-row read reported an empty reason; a non-available state must say why")
			}
		})
	}
}
