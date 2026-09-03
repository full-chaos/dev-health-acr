package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A REPOSITORY COHORT, DRIVEN THROUGH THE PRODUCTION ENTRY POINT.
//
// Every test in this file calls (*Adapter).DiscoverContext -- the same
// method the engine calls -- and reads what it returned. None of them calls
// graphrank.DiscoveredCohort directly and none of them constructs a Cohort.
// That is deliberate and it is the lesson from seam 7's round 3: a
// regression test that BUILDS the decision it asserts on is vacuous, because
// re-introducing the production bug leaves it green.
//
// THREE VARIANTS, BECAUSE THREE CAN CARRY THE KIND. SubjectExpression has
// four cohort variants (frame.go IsCohortVariant); explicit_set is not one of
// these three because its operands are NAMED, so MemberKind() has no case for
// it and it reaches basis no_member_kind instead. The remaining three --
// grouped_members, children_of_scope, discovered_kind -- each reach the seam
// through a DIFFERENT accessor path and, more importantly, through a
// different candidate-pool decision (cohortExactNameCensusEligibility). A
// single-shape fixture would prove one of them and look like it had proved
// all three.
//
// WHAT MAKES THESE RED AT THE BASE COMMIT: servableCohortKinds admits exactly
// {team, project}, so cohortKindFromFrame returns ("", member_kind_unservable)
// for every frame below and DiscoveredCohort returns a nil cohort before it
// looks at a single node. The red is exactly the size of the gap -- no ranked
// members, no scores, no drivers, so nothing here can red for a reason other
// than the kind bound.

// repositoryNodeRow is an AUTHORIZED repository node in the shape
// chaos4348ExactNameCandidates and the fulltext arm both return.
//
// Carries evidence_refs because DiscoveredCohort copies them onto the member
// verbatim, and a fixture that omitted them could not tell a member built
// with evidence from one built without.
func repositoryNodeRow(canonicalID, label string) row {
	nodeRow := fakeSubjectNodeRow("repository", canonicalID, label)
	properties := nodeRow["n"].(*node).Properties
	properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
	properties["evidence_refs"] = []string{"evidence_" + canonicalID}
	return nodeRow
}

// repositoryFulltextRow is the same authorized repository node in the shape
// the FULLTEXT arm returns, which is not the shape the exact-name census
// returns: fulltext rows are keyed "node" and carry a search_text the server
// scores against, the census keys them "n".
//
// Found by a failing test rather than by reading, and worth recording: the
// first version of the scoped fixture built a census-shaped row, handed it to
// the fulltext arm, and the parser skipped it -- so the test failed with
// "Cohort = nil", byte-identical to the message the seam refusal produces.
// A fixture defect wearing a production defect's clothes. The two cases are
// distinguished by the census-gate assertion in that test, which passed
// throughout.
func repositoryFulltextRow(canonicalID, label, searchText string) row {
	nodeRow := row{"node": &node{Properties: map[string]interface{}{
		propKind: "repository", propCanonicalID: canonicalID, propLabel: label,
		propSearchText: searchText,
	}}}
	properties := nodeRow["node"].(*node).Properties
	properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
	properties["evidence_refs"] = []string{"evidence_" + canonicalID}
	return nodeRow
}

// teamFulltextDecoyRow is teamDecoyRow in the fulltext shape: differs from
// repositoryFulltextRow in SUBJECT KIND ALONE.
func teamFulltextDecoyRow(canonicalID, label, searchText string) row {
	nodeRow := row{"node": &node{Properties: map[string]interface{}{
		propKind: "team", propCanonicalID: canonicalID, propLabel: label,
		propSearchText: searchText,
	}}}
	properties := nodeRow["node"].(*node).Properties
	properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
	properties["evidence_refs"] = []string{"evidence_" + canonicalID}
	return nodeRow
}

// teamDecoyRow differs from repositoryNodeRow in SUBJECT KIND ALONE -- same
// authorization, same evidence shape, same label style.
//
// This is the decoy for DiscoveredCohort's `subject.Kind == kind` conjunct.
// A decoy that also differed in authorization or in id would be excluded by
// a different rule, and the kind conjunct would stay unpinned while the test
// looked like it covered it (seam 7's two-field decoy, #402 R3).
func teamDecoyRow(canonicalID, label string) row {
	nodeRow := fakeSubjectNodeRow("team", canonicalID, label)
	properties := nodeRow["n"].(*node).Properties
	properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
	properties["evidence_refs"] = []string{"evidence_" + canonicalID}
	return nodeRow
}

// unauthorizedRepositoryRow differs from repositoryNodeRow in AUTHORIZATION
// ALONE: same kind, same evidence shape. It is the decoy for the
// AuthorizedAttributes conjunct, and it must be counted as an authz drop
// rather than silently vanish.
func unauthorizedRepositoryRow(canonicalID, label string) row {
	nodeRow := fakeSubjectNodeRow("repository", canonicalID, label)
	properties := nodeRow["n"].(*node).Properties
	properties["authorization_repositories"] = []string{"some-other-org/private"}
	properties["evidence_refs"] = []string{"evidence_" + canonicalID}
	return nodeRow
}

// repositoryCohortRequest builds a DiscoverContext request whose frame
// declares the given cohort expression, with no committed subject (the
// census-eligible state) and generous option bounds.
//
// maxCohortMembers is a parameter and not a constant because Complete and
// Truncated are computed from it, and a boolean asserted in only one
// direction is a boolean nothing pins.
func repositoryCohortRequest(expression contextfabric.SubjectExpression, question string, maxCohortMembers int) contextfabric.GraphDiscoveryRequest {
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: question,
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: maxCohortMembers, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape:       contextfabric.ShapeDiscoveredCohort,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{},
		},
		Frame: &contextfabric.QuestionFrame{
			Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
			SubjectExpression: expression,
			Temporal:          contextfabric.TemporalIntentCurrent,
			Version:           contextfabric.QuestionFrameVersion,
		},
	}
}

// groupedRepositoryExpression is variant 1: members of one kind grouped BY
// another.
//
// GroupKind is TEAM, not repository. Invariant I6 (frame_invariants.go
// checkI6) refuses a grouped expression whose GroupKind equals its
// MemberKind -- "grouping a kind by itself is not a grouping" -- so
// {group_kind: repository, member_kind: repository} is not a frame this
// system can produce, and a fixture built that way would never reach the
// seam at all. The legal grouped shape that carries a repository member kind
// groups repositories by their owning team.
func groupedRepositoryExpression() contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionGroupedMembers,
		Grouped: &contextfabric.GroupedSetExpression{
			GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectRepository,
		},
	}
}

// scopedRepositoryExpression is variant 2: the members of a kind UNDER a
// named anchor -- "which repositories does the platform team own".
func scopedRepositoryExpression() contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionChildrenOfScope,
		Scoped: &contextfabric.ScopedSetExpression{
			AnchorTerms: []string{"platform"}, MemberKind: contextfabric.SubjectRepository,
		},
	}
}

// discoveredRepositoryExpression is variant 3: a bare kind census -- "which
// repositories need attention".
func discoveredRepositoryExpression() contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectRepository},
	}
}

// censusServingConn answers the exact-name census (`$kinds`) with the given
// rows and the fulltext arm with nothing -- the pool shape for a variant the
// census gate ADMITS.
func censusServingConn(rows []row) *fakeConn {
	return &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			return rows, nil
		default:
			return nil, nil
		}
	}}
}

// fulltextServingConn answers the FULLTEXT arm with the given rows and fails
// the test if the exact-name census runs at all.
//
// This is the pool shape for a variant the census gate DENIES -- a scoped
// expression whose anchor resolved. The t.Fatal is the point: if the census
// ran here, a question that named one team would have been widened into the
// whole organization's repository census, which is the carve-out CHAOS-4395
// exists to protect.
func fulltextServingConn(t *testing.T, rows []row) *fakeConn {
	t.Helper()
	return &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return rows, nil
		case strings.Contains(cypher, "$kinds"):
			t.Error("the exact-name org-wide census ran for a scoped expression whose anchor resolved -- that widens a question that named one team into every repository in the organization")
			return nil, nil
		default:
			return nil, nil
		}
	}}
}

func repositoryPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
}

// assertRepositoryCohort asserts EVERY field DiscoveredCohort writes onto the
// cohort it builds, not the two or three a green test could get away with.
//
// Seam 7's round 4 is the reason: its R3 fix wrote four route fields and its
// test asserted two, so two wrong fields stayed green through a full review
// round. The fields written here are Kind, Members (with Rank,
// InclusionReasons and EvidenceRefIDs per member), Exclusions, Rationale,
// Complete and Truncated -- all seven are checked below.
func assertRepositoryCohort(t *testing.T, cohort *contextfabric.Cohort, wantIDs []string, wantComplete, wantTruncated bool) {
	t.Helper()
	if cohort == nil {
		t.Fatalf("Cohort = nil, want a repository cohort with %d member(s) -- the seam refused the declared member kind before discovery ever looked at a node", len(wantIDs))
	}
	if cohort.Kind != contextfabric.SubjectRepository {
		t.Errorf("Cohort.Kind = %q, want %q", cohort.Kind, contextfabric.SubjectRepository)
	}
	if len(cohort.Members) != len(wantIDs) {
		t.Fatalf("Cohort.Members has %d member(s), want %d: %+v", len(cohort.Members), len(wantIDs), cohort.Members)
	}
	for index, member := range cohort.Members {
		if member.Subject.Kind != contextfabric.SubjectRepository {
			t.Errorf("member %d has subject kind %q, want %q -- a cohort must never carry a member of a kind it did not ask for", index, member.Subject.Kind, contextfabric.SubjectRepository)
		}
		if member.Subject.CanonicalID != wantIDs[index] {
			t.Errorf("member %d is %q, want %q (rank order is input order)", index, member.Subject.CanonicalID, wantIDs[index])
		}
		if member.Rank != index+1 {
			t.Errorf("member %d has Rank %d, want %d -- ranks are 1-based and dense", index, member.Rank, index+1)
		}
		if len(member.InclusionReasons) == 0 {
			t.Errorf("member %d carries no InclusionReasons; a member the user cannot see a reason for is an unexplained assertion", index)
		}
		wantEvidence := "evidence_" + wantIDs[index]
		if len(member.EvidenceRefIDs) != 1 || member.EvidenceRefIDs[0] != wantEvidence {
			t.Errorf("member %d has EvidenceRefIDs %v, want exactly [%q]", index, member.EvidenceRefIDs, wantEvidence)
		}
	}
	if cohort.Exclusions == nil {
		t.Error("Cohort.Exclusions is nil, want an empty non-nil slice -- nil and empty are different on the wire (null vs [])")
	}
	if cohort.Rationale == "" {
		t.Error("Cohort.Rationale is empty; the cohort must say how it was assembled")
	}
	if cohort.Complete != wantComplete {
		t.Errorf("Cohort.Complete = %v, want %v", cohort.Complete, wantComplete)
	}
	if cohort.Truncated != wantTruncated {
		t.Errorf("Cohort.Truncated = %v, want %v", cohort.Truncated, wantTruncated)
	}
}

// assertServedBasis reads the cohort-kind basis off the telemetry the
// production path emitted, joined to this one call.
//
// The basis is asserted separately from the cohort because they are two
// different claims: "a cohort came back" and "the seam admitted the declared
// kind, rather than something downstream inventing one".
func assertServedBasis(t *testing.T, telemetry *recordingTelemetry, wantDiscovered bool) {
	t.Helper()
	if len(telemetry.cohortKindBases) != 1 {
		t.Fatalf("cohortKindBases = %+v, want exactly 1 basis recorded for one DiscoverContext call", telemetry.cohortKindBases)
	}
	got := telemetry.cohortKindBases[0]
	if got.basis != graphrank.CohortKindFromFrameMemberKind {
		t.Errorf("cohort kind basis = %q, want %q", got.basis, graphrank.CohortKindFromFrameMemberKind)
	}
	if got.discovered != wantDiscovered {
		t.Errorf("cohort kind basis reported discovered=%v, want %v", got.discovered, wantDiscovered)
	}
}

// TestGroupedRepositoryCohortIsDiscovered is variant 1: grouped_members with
// a repository member kind, grouped by team.
//
// The census gate admits it (no anchor resolved), so the repository nodes
// arrive through chaos4348ExactNameCandidates' org-wide kind census.
func TestGroupedRepositoryCohortIsDiscovered(t *testing.T) {
	fake := censusServingConn([]row{
		repositoryNodeRow("repo_acr", "dev-health-acr"),
		teamDecoyRow("team_platform", "Platform"),
		repositoryNodeRow("repo_ops", "dev-health-ops"),
	})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		repositoryCohortRequest(groupedRepositoryExpression(), "show me the repositories each team owns", 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertRepositoryCohort(t, result.Cohort, []string{"repo_acr", "repo_ops"}, true, false)
	assertServedBasis(t, telemetry, true)
}

// TestScopedRepositoryCohortIsDiscoveredWithoutTheOrgWideCensus is variant 2,
// and it is the acceptance row `pos-scoped-repositories` in fixture form:
// "which repositories does the platform team own".
//
// ScopeAnchorResolved is TRUE, which is the live production state for a
// question that names a team -- and it means cohortExactNameCensusEligibility
// DENIES the org-wide census. So this variant's members can only come from
// the anchored fulltext/hop-walk pool, and proving the grouped variant above
// proves nothing about this one.
func TestScopedRepositoryCohortIsDiscoveredWithoutTheOrgWideCensus(t *testing.T) {
	fake := fulltextServingConn(t, []row{
		repositoryFulltextRow("repo_acr", "dev-health-acr", "platform team repositories dev-health-acr"),
		teamFulltextDecoyRow("team_platform", "Platform", "platform team repositories"),
	})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := repositoryCohortRequest(scopedRepositoryExpression(), "which repositories does the platform team own", 10)
	request.ScopeAnchorResolved = true

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertRepositoryCohort(t, result.Cohort, []string{"repo_acr"}, true, false)
	assertServedBasis(t, telemetry, true)

	if len(telemetry.cohortExactNameCensusGates) != 1 {
		t.Fatalf("cohortExactNameCensusGates = %+v, want exactly 1 gate decision", telemetry.cohortExactNameCensusGates)
	}
	if gate := telemetry.cohortExactNameCensusGates[0]; gate.admitted || gate.basis != CohortExactNameCensusBasisAnchorSet {
		t.Errorf("census gate = %+v, want {admitted:false basis:%q}", gate, CohortExactNameCensusBasisAnchorSet)
	}
}

// TestScopedRepositoryCohortWithoutAnAnchorTakesTheCensus is variant 2's
// MIRROR on the anchor boolean: the same scoped expression with no resolved
// anchor is census-eligible and finds its members there.
//
// Without this leg the anchor half of the census rule is asserted in one
// direction only, and a gate that denied the census unconditionally would
// pass the test above.
func TestScopedRepositoryCohortWithoutAnAnchorTakesTheCensus(t *testing.T) {
	fake := censusServingConn([]row{repositoryNodeRow("repo_acr", "dev-health-acr")})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	request := repositoryCohortRequest(scopedRepositoryExpression(), "which repositories need attention", 10)
	request.ScopeAnchorResolved = false

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(), request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertRepositoryCohort(t, result.Cohort, []string{"repo_acr"}, true, false)
	assertServedBasis(t, telemetry, true)

	if len(telemetry.cohortExactNameCensusGates) != 1 {
		t.Fatalf("cohortExactNameCensusGates = %+v, want exactly 1 gate decision", telemetry.cohortExactNameCensusGates)
	}
	if gate := telemetry.cohortExactNameCensusGates[0]; !gate.admitted || gate.basis != CohortExactNameCensusBasisAnchorUnset {
		t.Errorf("census gate = %+v, want {admitted:true basis:%q}", gate, CohortExactNameCensusBasisAnchorUnset)
	}
}

// TestDiscoveredRepositoryCohortIsDiscovered is variant 3: a bare repository
// kind census.
//
// No rig acceptance row exercises this shape, which is exactly why it has a
// fixture: an untested variant reaching the same widened seam is the
// wrong-kind answer class returning through a different door.
func TestDiscoveredRepositoryCohortIsDiscovered(t *testing.T) {
	fake := censusServingConn([]row{
		repositoryNodeRow("repo_acr", "dev-health-acr"),
		unauthorizedRepositoryRow("repo_secret", "private-thing"),
		repositoryNodeRow("repo_ops", "dev-health-ops"),
	})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		repositoryCohortRequest(discoveredRepositoryExpression(), "which repositories need attention", 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertRepositoryCohort(t, result.Cohort, []string{"repo_acr", "repo_ops"}, true, false)
	assertServedBasis(t, telemetry, true)

	// The unauthorized repository differs from the admitted ones in
	// AUTHORIZATION ALONE, so it pins AuthorizedAttributes rather than the
	// kind filter, and it must be COUNTED, not silently dropped.
	if telemetry.cohortMembersAuthzDropped != 1 {
		t.Errorf("cohortMembersAuthzDropped = %d, want 1 -- an authorization exclusion that is not counted is indistinguishable from a subject that was never there", telemetry.cohortMembersAuthzDropped)
	}
}

// TestRepositoryCohortTruncatesAtTheMemberBound is the MIRROR CASE for
// Complete and Truncated.
//
// Every test above asserts Complete=true/Truncated=false. Both are computed
// from the same comparison against MaxCohortMembers, so asserting only that
// side leaves a fix that hard-coded either boolean completely unpinned.
func TestRepositoryCohortTruncatesAtTheMemberBound(t *testing.T) {
	fake := censusServingConn([]row{
		repositoryNodeRow("repo_acr", "dev-health-acr"),
		repositoryNodeRow("repo_ops", "dev-health-ops"),
		repositoryNodeRow("repo_web", "dev-health-web"),
	})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		repositoryCohortRequest(discoveredRepositoryExpression(), "which repositories need attention", 2))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertRepositoryCohort(t, result.Cohort, []string{"repo_acr", "repo_ops"}, false, true)
	assertServedBasis(t, telemetry, true)
}

// TestRepositoryCohortDedupesTheSameRepositoryFoundTwice pins the `seen`
// conjunct: a repository returned by both the fulltext arm and the census
// contributes one member, not two, and its rank stays dense.
func TestRepositoryCohortDedupesTheSameRepositoryFoundTwice(t *testing.T) {
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return []row{repositoryNodeRow("repo_acr", "dev-health-acr")}, nil
		case strings.Contains(cypher, "$kinds"):
			return []row{
				repositoryNodeRow("repo_acr", "dev-health-acr"),
				repositoryNodeRow("repo_ops", "dev-health-ops"),
			}, nil
		default:
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		repositoryCohortRequest(discoveredRepositoryExpression(), "which repositories need attention", 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	assertRepositoryCohort(t, result.Cohort, []string{"repo_acr", "repo_ops"}, true, false)
	assertServedBasis(t, telemetry, true)
}

// TestRepositoryCohortAsksOnlyForFactKindsAProducerCanServe is the
// claimed-fact half of this slice, and it is a SEPARATE claim from "a cohort
// came back".
//
// reader.go merges FactHealth and FactWorkload onto every discovered cohort
// with no read of the cohort's kind. FactHealth declares repository
// (devhealthfacts/health.go); FactWorkload declares team and project only
// (devhealthfacts/workload.go). So a repository cohort asks for a fact kind
// no registered producer can serve for it -- the planner prunes it with
// `pruned:subject_kind_unsupported` rather than failing, so this is a
// disclosure defect and not a crash, but a requirement that is guaranteed to
// prune is not a requirement, it is noise in the coverage record.
//
// This test asserts the requirement SET, not a count, so a change that swaps
// one kind for another cannot pass it.
func TestRepositoryCohortAsksOnlyForFactKindsAProducerCanServe(t *testing.T) {
	fake := censusServingConn([]row{repositoryNodeRow("repo_acr", "dev-health-acr")})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		repositoryCohortRequest(discoveredRepositoryExpression(), "which repositories need attention", 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil, so no cohort fact requirements were merged at all")
	}

	got := map[contextfabric.FactKind]bool{}
	for _, requirement := range result.FactRequirements {
		got[requirement.Kind] = true
	}
	if !got[contextfabric.FactHealth] {
		t.Errorf("FactRequirements %+v omits %q, which devhealthfacts/health.go declares for repository", result.FactRequirements, contextfabric.FactHealth)
	}
	if got[contextfabric.FactWorkload] {
		t.Errorf("FactRequirements %+v asks for %q on a repository cohort; devhealthfacts/workload.go declares only team and project, so this requirement can only ever be pruned", result.FactRequirements, contextfabric.FactWorkload)
	}
}

// TestTeamCohortStillAsksForWorkload is the POSITIVE CONTROL for the test
// above, and it is not optional.
//
// A capability-aware merge that returned an empty set for every kind would
// satisfy the repository assertion perfectly. This is the fixture that lands
// in the other tier: team is a kind FactWorkload does declare, and a team
// cohort must still ask for it.
func TestTeamCohortStillAsksForWorkload(t *testing.T) {
	fake := censusServingConn([]row{func() row {
		nodeRow := fakeSubjectNodeRow("team", "team_platform", "Platform")
		properties := nodeRow["n"].(*node).Properties
		properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
		properties["evidence_refs"] = []string{"evidence_team_platform"}
		return nodeRow
	}()})
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	result, err := adapter.DiscoverContext(context.Background(), repositoryPrincipal(),
		repositoryCohortRequest(contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
		}, "which teams are struggling", 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil {
		t.Fatal("Cohort = nil for a team cohort -- the kind that has always been servable stopped being discovered")
	}

	got := map[contextfabric.FactKind]bool{}
	for _, requirement := range result.FactRequirements {
		got[requirement.Kind] = true
	}
	if !got[contextfabric.FactHealth] || !got[contextfabric.FactWorkload] {
		t.Errorf("FactRequirements %+v for a TEAM cohort must still carry both %q and %q; a merge that served nothing for every kind would pass the repository assertion and break every team answer", result.FactRequirements, contextfabric.FactHealth, contextfabric.FactWorkload)
	}
}
