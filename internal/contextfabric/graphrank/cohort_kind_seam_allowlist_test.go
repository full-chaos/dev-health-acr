package graphrank

import (
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The seam's allow-list is DELIBERATELY NARROWER than the wire contract, and
// this file is what makes that a decision rather than an oversight.
//
// Widening ContextFabricCohort.validate to the full published subject-kind
// vocabulary removes the reason a repository cohort could not be CARRIED. It
// does not create a discovery arm that can BUILD one. Those are separate
// facts, and conflating them is precisely the failure mode the seam exists to
// prevent: before the seam, a repository question was answered with a
// wrong-kind team cohort, and the first honest attempt to carry the declared
// kind produced an HTTP 500.
//
// So the rule is: servableCohortKinds admits exactly the kinds a discovery
// arm can actually serve. It grows only in the same change that proves the
// arm, never as a tidy-up to "match the contract". A future reader who sees
// the contract admitting 15 kinds and this map admitting 2 is looking at the
// intended state, not at drift.
//
// The contract widening left this map at two kinds on purpose; the change
// that ADDED `repository` is the one that proved a discovery arm for it, and
// these tests moved in that same commit rather than after it. A pin that is
// relaxed in a later tidy-up commit is not a pin.

// TestSeamAllowListAdmitsExactlyTheServableKinds pins the allow-list's
// membership in both directions -- nothing missing, nothing extra.
//
// Written against the map rather than through cohortKindFromFrame so that a
// kind added to the map is caught even if no frame fixture happens to reach
// it; the behavioural half is the test below.
func TestSeamAllowListAdmitsExactlyTheServableKinds(t *testing.T) {
	t.Parallel()
	want := []string{
		string(contextfabric.SubjectProject),
		string(contextfabric.SubjectRepository),
		string(contextfabric.SubjectTeam),
	}
	sort.Strings(want)

	got := make([]string, 0, len(servableCohortKinds))
	for kind, admitted := range servableCohortKinds {
		if !admitted {
			t.Errorf("servableCohortKinds maps %q to false; the map is a membership set, so a false entry is a contradiction -- remove the key instead", kind)
			continue
		}
		got = append(got, string(kind))
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("servableCohortKinds admits %v, want exactly %v -- if a discovery arm was proven for a new kind, this pin moves in THAT change and says so", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("servableCohortKinds admits %v, want exactly %v", got, want)
		}
	}
}

// TestSeamStaysNarrowerThanTheWireContract states the divergence as an
// enforced property.
//
// Every kind the contract carries but the seam does not must refuse at the
// seam with member_kind_unservable -- a NAMED decline, which is the outcome
// this seam was built to produce in place of a wrong-kind answer or a 500.
//
// The count assertion is not decoration. Before the contract widening the
// contract admitted 2 kinds and this set was empty, so a test that only
// looped over it would have passed while asserting nothing at all. Requiring
// the set to be non-empty is what converts this from a loop that might run
// into a loop that must.
func TestSeamStaysNarrowerThanTheWireContract(t *testing.T) {
	t.Parallel()
	carriedNotServable := 0
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := contextfabric.SubjectKind(published)
		if servableCohortKinds[kind] {
			continue
		}
		carriedNotServable++
		frame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
		}}
		got, declared, basis := cohortKindFromFrame(frame)
		if basis != CohortKindMemberKindUnservable {
			t.Errorf("kind %q is outside the seam allow-list but cohortKindFromFrame returned basis %q, want %q", kind, basis, CohortKindMemberKindUnservable)
		}
		if got != "" {
			t.Errorf("kind %q refused at the seam but still yielded cohort kind %q; a refused kind must yield no kind at all, or a caller can build the cohort the refusal exists to prevent", kind, got)
		}
		// The REFUSED kind is still reported, and that is the half a
		// refusal used to lose: the seam could say a member kind was
		// unservable but never which one, so the kind got inferred from
		// question text instead.
		if declared != kind {
			t.Errorf("kind %q was refused at the seam but reported declared kind %q; a refusal that cannot name the kind it refused is a refusal someone will attribute by guessing", kind, declared)
		}
	}
	if carriedNotServable == 0 {
		t.Fatal("every kind the wire contract carries is also servable at the seam -- either the allow-list was widened without proving a discovery arm, or this test ran over an empty set and proved nothing")
	}
	t.Logf("kinds carried by the wire contract but deliberately unservable at the seam: %d", carriedNotServable)
}

// TestSeamServesEveryKindWithAProvenArm is the positive half: the kinds that
// DO have a discovery arm keep returning frame_member_kind. A pin that only
// proved refusals would be satisfied by a seam that refused everything.
//
// It was named ...ItsTwoKinds while there were two. Renaming it with the
// third rather than leaving the count in the name is deliberate: a test whose
// name states a number it no longer checks is a comment that lies, and this
// file exists to stop exactly that kind of drift.
func TestSeamServesEveryKindWithAProvenArm(t *testing.T) {
	t.Parallel()
	for _, kind := range []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository,
	} {
		frame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
		}}
		got, declared, basis := cohortKindFromFrame(frame)
		if basis != CohortKindFromFrameMemberKind || got != kind {
			t.Errorf("kind %q: cohortKindFromFrame = (%q, %q, %q), want servable %q with basis %q", kind, got, declared, basis, kind, CohortKindFromFrameMemberKind)
		}
		if declared != kind {
			t.Errorf("kind %q: cohortKindFromFrame reported declared kind %q; on a served turn the declared and servable kinds are the same value", kind, declared)
		}
	}
}
