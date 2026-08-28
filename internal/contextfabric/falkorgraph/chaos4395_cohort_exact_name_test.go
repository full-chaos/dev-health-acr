package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDiscoverContextCohortFindsTeamsWithNoLexicalMatchInTheQuestion is
// CHAOS-4395. DiscoveredCohort's only node source used to be
// fulltextSearchNodes -- a lexical full-text search over the raw question
// TEXT. "Which teams are struggling" names no team by label, alias, or key,
// so that search legitimately returns nothing, and the cohort stayed empty
// (graphContext.Cohort == nil) even when Shape was correctly interpreted as
// discovered_cohort and authorization would otherwise allow every member.
//
// CHAOS-4348's chaos4348ExactNameCandidates is the kind-exhaustive,
// term-free fetch that already existed for exactly this problem on the
// single-subject resolution path (graphrank.applyExactNameArm) but was never
// wired into DiscoverContext's cohort path. This proves it now is: with the
// fulltext arm returning nothing, the cohort must still be found through the
// exact-name arm alone.
func TestDiscoverContextCohortFindsTeamsWithNoLexicalMatchInTheQuestion(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			// The lexical search over "which teams are struggling" finds
			// nothing: no team is named by label, alias, or provider key.
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			// chaos4348ExactNameCandidates: the kind-exhaustive, term-free
			// fetch -- one real team, authorized under the principal's own
			// repository scope (this test is about RETRIEVAL, not
			// authorization; CHAOS-4390's ownership-based authorization fix
			// is proved separately in devhealthsource).
			teamRow := fakeSubjectNodeRow("team", "team_platform", "Platform")
			teamRow["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
			teamRow["n"].(*node).Properties["authorization_teams"] = []string{"team_platform"}
			return []row{teamRow}, nil
		default:
			t.Fatalf("unexpected query for a subjectless cohort request with no committed origin: %s", cypher)
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which teams are struggling",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 || result.Cohort.Members[0].Subject.CanonicalID != "team_platform" {
		t.Fatalf("Cohort = %#v, want exactly one member (team_platform) discovered through the exact-name arm", result.Cohort)
	}
}

// TestDiscoverContextNonCohortRequestNeverCallsExactNameCandidates proves
// the new fetch is scoped to cohort shapes only: an ordinary single-subject
// investigation (the overwhelming majority of traffic) must not pay for, or
// be affected by, the exact-name org-wide fetch.
func TestDiscoverContextNonCohortRequestNeverCallsExactNameCandidates(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("chaos4348ExactNameCandidates must not be called for a non-cohort Shape")
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			return nil, nil
		default: // nodeByKindID for the committed origin
			return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	if _, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 10)); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
}

// cohortDiscoveryRequest is TestDiscoverContextCohortFindsTeamsWithNoLexicalMatchInTheQuestion's
// request builder, factored out so the round-1 regression tests below can
// vary only the Shape.
func cohortDiscoveryRequest(shape contextfabric.InvestigationShape) contextfabric.GraphDiscoveryRequest {
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which teams are struggling",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: shape, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
	}
}

// TestDiscoverContextExplicitCohortNeverCallsExactNameCandidates is codex
// round-1 finding P1: explicit_cohort means the question NAMES specific
// members ("compare the frontend and backend teams"), while
// chaos4348ExactNameCandidates returns the WHOLE org-wide kind census with
// no term filtering at all. Admitting it for explicit_cohort would widen a
// question naming two teams into a cohort containing every team in the org
// -- the exact-name fetch is wired ONLY for discovered_cohort.
func TestDiscoverContextExplicitCohortNeverCallsExactNameCandidates(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("chaos4348ExactNameCandidates must not be called for ShapeExplicitCohort -- only ShapeDiscoveredCohort names a termless census")
			return nil, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	if _, err := adapter.DiscoverContext(context.Background(), principal, cohortDiscoveryRequest(contextfabric.ShapeExplicitCohort)); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
}

// TestDiscoverContextDisclosesExactNameTruncation is codex round-1 finding
// P1: chaos4348ExactNameCandidates reports its own truncation (the bounded
// org-wide census was cut off before finishing), but that signal used to be
// discarded entirely at the call site -- a cohort built from an incomplete
// census could report Complete=true while genuinely missing members. This
// proves the signal now reaches Coverage.Partial/DegradedReasons.
func TestDiscoverContextDisclosesExactNameTruncation(t *testing.T) {
	overLimitRows := make([]row, exactNameCandidateQueryLimit+1)
	for i := range overLimitRows {
		overLimitRows[i] = fakeSubjectNodeRow("team", fmt.Sprintf("team_%d", i), fmt.Sprintf("Team %d", i))
		overLimitRows[i]["n"].(*node).Properties["authorization_repositories"] = "*"
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			return overLimitRows, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}

	result, err := adapter.DiscoverContext(context.Background(), principal, cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: the exact-name census was truncated before the cohort was built from it")
	}
	found := false
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.Contains(reason, "exact_name_candidates_truncated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("DegradedReasons = %v, want an exact_name_candidates_truncated entry", result.Coverage.DegradedReasons)
	}
}

// TestDiscoverContextCohortAuthzDroppedNotInflatedByOverlappingArms is
// codex round-1 finding P2: a subject BOTH fulltext and exact-name return
// must contribute to cohortAuthzDropped exactly once, never once per arm
// that found it. Seeds the SAME unauthorized team from both the fulltext
// and the exact-name query.
func TestDiscoverContextCohortAuthzDroppedNotInflatedByOverlappingArms(t *testing.T) {
	// The SAME subject (team_foreign), shaped the way EACH arm's own query
	// actually decodes a row: fulltextSearchNodes reads a "node" key
	// (fulltextRow), chaos4348ExactNameCandidates reads an "n" key
	// (fakeSubjectNodeRow) -- see runFulltextQuery vs. that function's own
	// `r["n"].(*node)` scan.
	unauthorizedAttrs := map[string]interface{}{"authorization_repositories": []string{"other/private"}, "authorization_teams": []string{"team_foreign"}}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			r := fulltextRow("team", "team_foreign", "Foreign", "Foreign", nil)
			for k, v := range unauthorizedAttrs {
				r["node"].(*node).Properties[k] = v
			}
			return []row{r}, nil
		case strings.Contains(cypher, "$kinds"):
			r := fakeSubjectNodeRow("team", "team_foreign", "Foreign")
			for k, v := range unauthorizedAttrs {
				r["n"].(*node).Properties[k] = v
			}
			return []row{r}, nil
		default:
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	if _, err := adapter.DiscoverContext(context.Background(), principal, cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if telemetry.cohortMembersAuthzDropped != 1 {
		t.Fatalf("cohortMembersAuthzDropped = %d, want exactly 1 -- the same unauthorized subject returned by both fulltext and exact-name must not be double-counted", telemetry.cohortMembersAuthzDropped)
	}
}
