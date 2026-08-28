package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// TestDiscoverContextDiscoveredCohortWithCommittedSubjectNeverCallsExactNameCandidates
// is codex round-2 finding P1: Shape alone is not enough to gate the
// exact-name fetch. request.Resolution is upstream of DiscoverContext and
// not something it validates -- a discovered_cohort request can still
// carry a committed subject (an exact hint, a prior-turn carry-over).
// Appending the org-wide census onto an already-anchored request would
// widen a subject-specific investigation into an organization-wide
// cohort, so the fetch requires BOTH the shape AND a genuinely empty
// Resolution.Committed.
func TestDiscoverContextDiscoveredCohortWithCommittedSubjectNeverCallsExactNameCandidates(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_anchor", Label: "Anchor"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("chaos4348ExactNameCandidates must not be called when Resolution.Committed is non-empty, even for ShapeDiscoveredCohort")
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			return nil, nil
		default:
			return []row{fakeSubjectNodeRow("team", "team_anchor", "Anchor")}, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Resolution.Committed = []contextfabric.SubjectRef{origin}

	if _, err := adapter.DiscoverContext(context.Background(), principal, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
}

// TestDiscoverContextTruncatedExactNameForcesCohortIncomplete is codex
// round-2 finding P2: DiscoveredCohort computes Cohort.Complete purely
// from len(members) vs. MaxCohortMembers, with no way to know its own node
// source was truncated upstream. A truncated census with fewer than
// MaxCohortMembers matching members would otherwise report Complete=true
// despite genuinely missing some -- Coverage.Partial alone is not enough
// if a caller trusts the Cohort field directly.
func TestDiscoverContextTruncatedExactNameForcesCohortIncomplete(t *testing.T) {
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
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.Options.MaxCohortMembers = exactNameCandidateQueryLimit + 100 // above the census size, so len(members) < MaxCohortMembers

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, want members from the (truncated) census")
	}
	if result.Cohort.Complete {
		t.Fatal("Cohort.Complete = true, want false -- the census that built this cohort was truncated, so it cannot claim completeness even though len(members) < MaxCohortMembers")
	}
}

// TestDiscoverContextExactNameCohortMembershipIsDeterministic is codex
// round-2 finding P2: chaos4348ExactNameCandidates' Cypher carries no
// ORDER BY, so FalkorDB's own return order is unspecified. Two identical
// calls whose underlying fake returns the SAME node set in a DIFFERENT
// order must still select the SAME cohort member once bounded by
// MaxCohortMembers.
func TestDiscoverContextExactNameCohortMembershipIsDeterministic(t *testing.T) {
	makeRows := func(reversed bool) []row {
		names := []string{"team_alpha", "team_beta", "team_gamma"}
		if reversed {
			names = []string{"team_gamma", "team_beta", "team_alpha"}
		}
		rows := make([]row, len(names))
		for i, name := range names {
			rows[i] = fakeSubjectNodeRow("team", name, name)
			rows[i]["n"].(*node).Properties["authorization_repositories"] = "*"
		}
		return rows
	}
	runOnce := func(reversed bool) string {
		fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			switch {
			case strings.Contains(cypher, "fulltext"):
				return nil, nil
			case strings.Contains(cypher, "$kinds"):
				return makeRows(reversed), nil
			default:
				return nil, nil
			}
		}}
		adapter := newFakeAdapter(t, fake)
		principal := storage.Principal{OrgID: "org-1"}
		request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
		request.Request.Options.MaxCohortMembers = 1
		result, err := adapter.DiscoverContext(context.Background(), principal, request)
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		if result.Cohort == nil || len(result.Cohort.Members) != 1 {
			t.Fatalf("Cohort = %#v, want exactly 1 member", result.Cohort)
		}
		return result.Cohort.Members[0].Subject.CanonicalID
	}
	forward := runOnce(false)
	backward := runOnce(true)
	if forward != backward {
		t.Fatalf("selected member differs by input order: forward=%q backward=%q -- exact-name cohort membership is not deterministic", forward, backward)
	}
}

// TestDiscoverContextCountsUnboundedValidityForExactNameOnlyCohortMembers
// is codex round-2 finding P1: on a historical axis, countUnboundedValidity
// used to receive only resolvedNodes, never the exact-name additions
// merged into cohortNodes -- an exact-name-only cohort (nothing from
// fulltext/hopWalk) built on a historical question could return members
// carrying no validity window without ever disclosing that in
// Coverage.Sources' "context-fabric:graph-validity-windows" entry.
func TestDiscoverContextCountsUnboundedValidityForExactNameOnlyCohortMembers(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			// Nothing from fulltext -- the cohort rests ENTIRELY on the
			// exact-name arm.
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			r := fakeSubjectNodeRow("team", "team_unbounded", "Unbounded")
			r["n"].(*node).Properties["authorization_repositories"] = "*"
			// Deliberately no propValidFromNs/propValidToNs -- hasUnboundedValidity's
			// exact "no window at all" case.
			return []row{r}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	asOf := time.Now().UTC()
	request.Interpretation.TimeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want exactly 1 member sourced entirely from the exact-name arm", result.Cohort)
	}
	var disclosed bool
	for _, source := range result.Coverage.Sources {
		if source.Source == "context-fabric:graph-validity-windows" {
			disclosed = true
		}
	}
	if !disclosed {
		t.Error("an exact-name-only cohort member carrying no validity window was admitted but never disclosed in Coverage.Sources")
	}
}
