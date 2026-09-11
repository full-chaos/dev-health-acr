package devhealthfacts_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupIdentityRawTeamKey is the raw producer key a grouping source row
// carries. It is deliberately one of the keys the deployed store actually
// holds, so this pin describes real data rather than a shape invented for
// the test.
const groupIdentityRawTeamKey = "AUTH"

// groupIdentityGroupingFacts builds the ONE member fact whose team_breakdown
// rows the grouping path reads to derive a group. The column names are the
// producer's own (team_id / team_name), which is what groupAssignmentsFromValue
// looks for.
func groupIdentityGroupingFacts(member contextfabric.SubjectRef) []contextfabric.CanonicalFact {
	return []contextfabric.CanonicalFact{{
		Kind:    contextfabric.FactWorkload,
		Subject: member,
		Fields: map[string]contextfabric.FactValue{
			"team_breakdown": {Rows: []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{
				"team_id":   contextfabric.StringFactValue(groupIdentityRawTeamKey),
				"team_name": contextfabric.StringFactValue("Auth"),
			}}}},
		},
	}}
}

func groupIdentityCohort(member contextfabric.SubjectRef) *contextfabric.Cohort {
	return &contextfabric.Cohort{
		Kind:      contextfabric.SubjectProject,
		Members:   []contextfabric.CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"matched"}}},
		Rationale: "grouped cohort identity pin",
		Complete:  true,
	}
}

// TestGroupedCohortIdentityReachesTheTeamFactReader is CHAOS-5285 test-table
// row 2: ONE raw key must yield ONE identity across the group OWNER (the
// grouping path that publishes the group), the PRODUCER (the canonical form
// every team subject is minted in) and the READER (the fact providers that
// resolve a team subject back to its source-row key).
//
// This is the ticket's whole premise made executable. The published group id
// is stamped from the source row verbatim, while every team fact provider
// strips a canonical prefix off the subject it is handed and rejects anything
// that does not carry one -- so a grouped answer's group subjects are, today,
// unresolvable BY CONSTRUCTION and `each_group` cannot be satisfied from them.
//
// THE ASSERTION IS NOT CIRCULAR. It does not compare the group id to a
// canonicaliser the change under test also owns -- that would be an
// expectation computed from the thing under test, which cannot fail. It hands
// the published group subject to the REAL WorkloadProvider and reads the
// `ids` binding the provider put on its ClickHouse query. That binding is the
// value the deployed SQL matches against `capacity_forecasts.team_id`, so the
// only way it can equal the raw key is if the identity genuinely round-tripped
// producer -> owner -> reader.
//
// RED at the parent: the group is published as the bare raw key, subjectIndex
// finds no prefix to strip, the subject is REJECTED, and the provider issues
// its query with an empty id set -- the grouped answer asks the database about
// no team at all.
func TestGroupedCohortIdentityReachesTheTeamFactReader(t *testing.T) {
	t.Parallel()

	member := contextfabric.SubjectRef{
		Kind:        contextfabric.SubjectProject,
		CanonicalID: "project.v2:jira:acme:AUTH",
		Label:       "Auth",
	}
	plan := contextfabric.AnswerPlan{GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectProject}

	groups, ungrouped, outcome := contextfabric.BuildCohortGroups(plan, groupIdentityCohort(member), groupIdentityGroupingFacts(member))
	if outcome.Refusal != "" {
		t.Fatalf("fixture defect: grouping refused with %q (planned=%q source=%q) -- this pin needs a grouping that SUCCEEDS",
			outcome.Refusal, outcome.PlannedKind, outcome.SourceKind)
	}
	if ungrouped != 0 || len(groups) != 1 {
		t.Fatalf("fixture defect: groups=%d ungrouped=%d, want exactly 1 group and 0 ungrouped", len(groups), ungrouped)
	}
	published := groups[0].Subject
	t.Logf("owner published group subject kind=%q canonical_id=%q label=%q", published.Kind, published.CanonicalID, published.Label)

	// THE READER, for real. No hand-written expectation of what the
	// canonical form looks like: the provider decides whether it can use
	// this subject, and the captured binding says what it asked the
	// database about.
	client := &fakeClient{tables: []fakeTable{{
		match: workloadBaseQueryMatch,
		rows:  [][]any{workloadRow(groupIdentityRawTeamKey, "scope-a")},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactWorkload,
		Subjects: []contextfabric.SubjectRef{published},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}

	got := client.idsBinding()
	want := []string{groupIdentityRawTeamKey}
	t.Logf("reader queried ids=%#v, facts returned=%d", got, len(result.Facts))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the team fact reader queried ids=%#v, want %#v -- the published group identity %q did not round-trip to the producer's source-row key, so every team fact provider is dark for this group and `each_group` cannot be satisfied from it",
			got, want, published.CanonicalID)
	}
	if len(result.Facts) == 0 {
		t.Errorf("the reader returned no facts for the published group subject %q -- a grouped answer that declares a group axis it can never read reports coverage for a population nobody asked the database about",
			published.CanonicalID)
	}
}

// TestGroupedCohortIdentityControl_AProducerMintedTeamSubjectIsAlreadyRead is
// the DISCRIMINATING CONTROL for the pin above, and it must pass at the parent
// as well as at the tip.
//
// It drives the SAME provider, the SAME fake client and the SAME source row
// through a subject minted the way every OTHER team subject in the system is
// minted. If this failed, the pin above would be measuring a broken provider
// fixture rather than the identity defect, and its red would prove nothing.
func TestGroupedCohortIdentityControl_AProducerMintedTeamSubjectIsAlreadyRead(t *testing.T) {
	t.Parallel()

	client := &fakeClient{tables: []fakeTable{{
		match: workloadBaseQueryMatch,
		rows:  [][]any{workloadRow(groupIdentityRawTeamKey, "scope-a")},
	}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactWorkload,
		Subjects: []contextfabric.SubjectRef{teamSubject(groupIdentityRawTeamKey)},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	got := client.idsBinding()
	want := []string{groupIdentityRawTeamKey}
	t.Logf("control queried ids=%#v, facts returned=%d", got, len(result.Facts))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CONTROL BROKEN: a producer-minted team subject queried ids=%#v, want %#v -- the fixture, not the identity, is wrong", got, want)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("CONTROL BROKEN: facts=%d, want 1 for a producer-minted team subject", len(result.Facts))
	}
}
