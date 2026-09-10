package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupReadRecorder captures EVERY canonical fact request the engine issues in
// one turn, in order, so a test can say how many reads happened and what each
// one was rooted on.
//
// Counting is the whole point. "The groups were served" and "the groups were
// READ" look identical on the answer; they differ only in whether a request
// naming the group identities was ever issued, which is a fact about the calls
// and about nothing else.
type groupReadRecorder struct {
	requests []CanonicalFactRequest
	facts    func(CanonicalFactRequest) CanonicalFactBundle
}

func (r *groupReadRecorder) ReadFacts(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	r.requests = append(r.requests, request)
	if r.facts == nil {
		return emptyFactBundle(), nil
	}
	return r.facts(request), nil
}

// rootKinds reports the distinct subject kinds a recorded request was rooted
// on, which is what separates a member read from a group read without the test
// having to know the fixture's ids.
func (r *groupReadRecorder) rootKinds(index int) []SubjectKind {
	seen := map[SubjectKind]struct{}{}
	kinds := make([]SubjectKind, 0, 2)
	for _, subject := range r.requests[index].Subjects {
		if _, known := seen[subject.Kind]; known {
			continue
		}
		seen[subject.Kind] = struct{}{}
		kinds = append(kinds, subject.Kind)
	}
	return kinds
}

// rootIDs reports the canonical ids a recorded request was rooted on.
func (r *groupReadRecorder) rootIDs(index int) []string {
	ids := make([]string, 0, len(r.requests[index].Subjects))
	for _, subject := range r.requests[index].Subjects {
		ids = append(ids, subject.CanonicalID)
	}
	return ids
}

// groupRootedRequests reports which recorded requests are rooted on subjects
// of the GROUP kind. A grouped turn that never produces one has not read its
// groups, whatever its answer claims about them.
func (r *groupReadRecorder) groupRootedRequests(groupKind SubjectKind) []int {
	indexes := make([]int, 0, 1)
	for index := range r.requests {
		for _, subject := range r.requests[index].Subjects {
			if subject.Kind == groupKind {
				indexes = append(indexes, index)
				break
			}
		}
	}
	return indexes
}

func emptyFactBundle() CanonicalFactBundle {
	return CanonicalFactBundle{
		Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
	}
}

// TestAGroupedTurnIssuesASecondFactReadRootedOnItsGroups is CHAOS-5285
// test-table row 3.
//
// A grouped answer declares an `each_group` requirement -- "the state of EACH
// team" -- and today that requirement is reported against evidence read for
// the MEMBERS. One fact request is issued, rooted on the cohort's projects; no
// request is ever rooted on the teams the answer groups by. The group axis is
// therefore a population the server never asked its providers about, and
// `each_group` is satisfiable only by projecting member evidence onto it,
// which is the exact substitution this ticket exists to remove.
//
// The assertion is on the CALLS, not on the answer. An answer can carry
// groups, group ids, per-group counts and a completeness verdict without a
// single provider ever having been asked about a group, which is precisely the
// state the parent is in -- so an assertion on the rendered answer cannot tell
// the two apart, and only a request rooted on the group kind can.
//
// RED at the parent: exactly one request, rooted on `project`, with no
// group-rooted request at any index.
func TestAGroupedTurnIssuesASecondFactReadRootedOnItsGroups(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	telemetry := &recordingTelemetry{}
	engine, request := groupReadEngineFixture(t, telemetry, recorder)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// The stage's own decision line, read back. A pin that only counted
	// requests could not say WHY none was issued, and "the stage refused for a
	// named reason" and "the stage never ran" are different defects.
	if len(telemetry.cohortGroupReads) != 1 {
		t.Errorf("group-read decisions emitted = %d, want exactly 1 on a grouped turn -- the stage must report what it decided even when it decides not to read", len(telemetry.cohortGroupReads))
	}
	for _, event := range telemetry.cohortGroupReads {
		t.Logf("group read decision: proposed=%d admitted=%d denied=%d read=%v refused=%v refusal=%q facts=%d",
			event.Proposed, event.Admitted, event.Denied, event.Read, event.Refused, event.Refusal, event.FactsReturned)
	}

	if len(recorder.requests) == 0 {
		t.Fatalf("fixture defect: the turn issued NO fact request at all, so this pin measures nothing")
	}
	for index := range recorder.requests {
		t.Logf("fact request %d: root kinds=%v roots=%v", index, recorder.rootKinds(index), recorder.rootIDs(index))
	}

	// The FIRST read stays the member read, unchanged. A pin that only
	// demanded "a group-rooted read exists" would be satisfied by a change
	// that replaced the member read with a group read, which would be a
	// different and worse defect.
	memberRooted := false
	for _, kind := range recorder.rootKinds(0) {
		if kind == SubjectProject {
			memberRooted = true
		}
	}
	if !memberRooted {
		t.Errorf("the FIRST fact read is rooted on %v, want the cohort's member kind %q -- the member read must survive unchanged",
			recorder.rootKinds(0), SubjectProject)
	}

	grouped := recorder.groupRootedRequests(SubjectTeam)
	if len(grouped) == 0 {
		t.Fatalf("the turn issued %d fact request(s) and NOT ONE was rooted on the group kind %q -- the answer declares an `each_group` requirement over a population no provider was ever asked about, so it can only be satisfied by projecting member evidence onto the groups",
			len(recorder.requests), SubjectTeam)
	}
	if len(grouped) != 1 {
		t.Errorf("group-rooted fact requests at indexes %v, want exactly ONE -- a per-group request is a fan-out this design rejects", grouped)
	}
	// Rooted on the GROUPS THEMSELVES, not on a widened member set that
	// happens to include a team.
	for _, subject := range recorder.requests[grouped[0]].Subjects {
		if subject.Kind != SubjectTeam {
			t.Errorf("the group read is rooted on %q/%q as well as the groups -- its roots must be the admitted group identities and nothing else",
				subject.Kind, subject.CanonicalID)
		}
	}
}

// TestAGroupedTurnReadsOnlyTheGroupsItAuthorized is CHAOS-5285 test-table
// row 5.
//
// It is deliberately NOT written as "a denied group was not queried". That
// assertion passes vacuously at the parent, where no group is queried at all,
// and a pin that passes on the defect is not a pin. The discriminating claim
// is the CONJUNCTION: a group read happens, AND its roots are exactly the
// admitted groups, AND the denied group's absence from the read does not
// quietly shrink the denominator the answer reports.
//
// The denominator half is what makes the denial honest. A group the principal
// may not see must still be COUNTED -- dropping it from the group list would
// turn "2 of 3 teams" into a complete-looking "2 of 2", which is a coverage
// claim about a population that was silently redefined.
func TestAGroupedTurnReadsOnlyTheGroupsItAuthorized(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	// The authorizer admits team_security and DENIES team_platform.
	engine, request := groupReadEngineFixtureDenying(t, &recordingTelemetry{}, recorder, TeamCanonicalID("team_platform"))

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	grouped := recorder.groupRootedRequests(SubjectTeam)
	if len(grouped) == 0 {
		t.Fatalf("no group-rooted fact read was issued, so the authorization this pin is about never ran -- see row 3")
	}
	roots := recorder.rootIDs(grouped[0])
	t.Logf("group read roots=%v", roots)
	for _, id := range roots {
		if id == TeamCanonicalID("team_platform") {
			t.Errorf("the group read was rooted on %q, which the authorizer denied -- a denied group must never reach a provider", id)
		}
	}
	if len(roots) != 1 || roots[0] != TeamCanonicalID("team_security") {
		t.Errorf("group read roots = %v, want exactly the ONE admitted group %q", roots, TeamCanonicalID("team_security"))
	}

	// THE DENOMINATOR. The denied group stays in the answer's group list,
	// unread, rather than vanishing from the population.
	if result.Cohort == nil {
		t.Fatalf("the served answer carries no cohort")
	}
	present := map[string]bool{}
	for _, group := range result.Cohort.Groups {
		present[group.Subject.CanonicalID] = true
	}
	t.Logf("served groups=%v", present)
	if !present[TeamCanonicalID("team_platform")] {
		t.Errorf("the denied group %q is absent from the served group list -- an unreadable group that disappears turns `2 of 3 teams` into a complete-looking `2 of 2`, which is a coverage claim about a population that was silently redefined",
			TeamCanonicalID("team_platform"))
	}
}

// TestAGroupListOverTheContractBoundIsRefusedRatherThanSliced is CHAOS-5285
// test-table row 17 (GAP C), a NEW-BEHAVIOUR row: no colour is claimed for it,
// because the parent has no group read to bound at all.
//
// The contract bounds a cohort at 250 groups. A grouped turn that discovers
// more has two honest options and one dishonest one. It may refuse, or it may
// disclose that it is answering about a subset; what it must NOT do is take
// the first 250 and answer as though that were the question, because a
// silently sliced group list is a denominator quietly reduced to fit -- the
// exact defect class this ticket exists to close.
//
// The rejection is asserted to happen BEFORE any group I/O: a bound enforced
// after the read has already paid the cost it exists to avoid, and has already
// let an over-large request reach a provider.
func TestAGroupListOverTheContractBoundIsRefusedRatherThanSliced(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadOverBoundMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixtureOverBound(t, &recordingTelemetry{}, recorder)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)

	// RED AT THE PARENT, AND NOT IN THE WAY THIS PIN WAS FIRST WRITTEN.
	// The parent does not slice and it does not refuse: it builds all 251
	// groups, carries them the whole way to result validation, and the
	// CONTRACT rejects them there -- so the turn ends as a stage ERROR
	// ("cohort violates v1 bounds") rather than as an answer. A bound
	// enforced only by the validator at the end is a bound that has already
	// let the whole turn run, and it turns an ordinary product outcome --
	// "this question groups by more teams than an answer can carry" -- into
	// a failure the caller reads as a server fault.
	if err != nil {
		t.Fatalf("Investigate() error = %v -- an over-bound group list must be REFUSED as a product outcome before any group I/O, not carried to result validation and returned as a stage error", err)
	}

	grouped := recorder.groupRootedRequests(SubjectTeam)
	if len(grouped) != 0 {
		t.Errorf("a group list over the contract bound reached a provider at request index(es) %v with %d roots -- the bound must be enforced BEFORE any group I/O, never by slicing what was already sent",
			grouped, len(recorder.requests[grouped[0]].Subjects))
	}

	if result.Cohort == nil {
		t.Fatalf("the served answer carries no cohort")
	}
	if got := len(result.Cohort.Groups); got > 0 {
		t.Errorf("the answer carries %d groups out of a 251-group discovery -- any non-empty list here is a denominator quietly reduced to fit, which answers a question nobody asked; the turn must refuse the group axis and say so", got)
	}
	if err := contractsv1.ValidateCohortGroups(result.Cohort.Groups, result.Cohort.Members); err != nil {
		t.Errorf("the served group list does not satisfy the contract it was bounded for: %v", err)
	}
}

// TestTheContractBoundIsMeasuredAtItsEdge is the CONTROL for the row-17 pin,
// and it must pass at the parent as well as at the tip.
//
// A bound tested only from far above it cannot tell `>` from `>=`, and a
// refusal that fired at 250 would silently cost every caller one group. This
// drives the SAME fixture at exactly 250 groups and asserts the turn serves
// them all.
func TestTheContractBoundIsMeasuredAtItsEdge(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadBoundedMemberFacts(250)
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixtureWith(t, &recordingTelemetry{}, recorder, groupReadCohortMembers(250), nil)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v at exactly the bound, where the turn must still answer", err)
	}
	if result.Cohort == nil {
		t.Fatalf("CONTROL BROKEN: the served answer carries no cohort")
	}
	if got := len(result.Cohort.Groups); got != 250 {
		t.Fatalf("CONTROL BROKEN: groups = %d at exactly the bound, want 250 -- a refusal that fires here costs every caller a group and the row-17 pin would be measuring an off-by-one", got)
	}
}
