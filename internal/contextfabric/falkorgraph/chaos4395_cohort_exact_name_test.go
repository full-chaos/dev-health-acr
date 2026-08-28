package falkorgraph

import (
	"context"
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
