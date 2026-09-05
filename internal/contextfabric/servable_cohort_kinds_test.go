package contextfabric

import (
	"sort"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The allow-list is DELIBERATELY NARROWER than the wire contract, and this
// file is what makes that a decision rather than an oversight.
//
// Widening ContextFabricCohort.validate to the full published subject-kind
// vocabulary removes the reason a repository cohort could not be CARRIED. It
// does not create a discovery arm that can BUILD one. Those are separate
// facts, and conflating them is precisely the failure mode this predicate
// exists to prevent: before it, a repository question was answered with a
// wrong-kind team cohort, and the first honest attempt to carry the declared
// kind produced an HTTP 500.
//
// So the rule is: the allow-list admits exactly the kinds a discovery arm can
// actually serve. It grows only in the same change that proves the arm, never
// as a tidy-up to "match the contract". A future reader who sees the contract
// admitting 15 kinds and this table admitting 3 is looking at the intended
// state, not at drift.
//
// THIS FILE MOVED WITH THE TABLE, in the same change. The table used to live
// in the discovery package; the requirement derivation needed the same answer
// and could not import that package, so it rebuilt the predicate by hand and
// disagreed for twelve of the fifteen published kinds. A pin left behind in
// the old package would pin a symbol that no longer exists; a pin re-created
// in a later commit is not a pin.

// TestServableCohortKindsAdmitsExactlyTheProvenArms pins the allow-list's
// membership in both directions -- nothing missing, nothing extra.
//
// Written against the map rather than through the predicate so that a kind
// added to the map is caught even if no expression fixture happens to reach
// it. The behavioural halves are the predicate tests below and the seam tests
// in the discovery package.
func TestServableCohortKindsAdmitsExactlyTheProvenArms(t *testing.T) {
	t.Parallel()
	want := []string{
		string(SubjectProject),
		string(SubjectRepository),
		string(SubjectTeam),
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

// TestServableCohortKindsForAuditReportsTheTable pins the audit accessor
// against the table it reads, in both directions.
//
// The accessor is what three packages outside this one quantify over, so a
// drift between it and the map would be invisible at every one of those call
// sites: they would agree with the accessor by construction.
func TestServableCohortKindsForAuditReportsTheTable(t *testing.T) {
	t.Parallel()
	audited := ServableCohortKindsForAudit()
	if len(audited) == 0 {
		t.Fatal("ServableCohortKindsForAudit() returned nothing -- every comparison below would then be vacuous")
	}
	if len(audited) != len(servableCohortKinds) {
		t.Fatalf("ServableCohortKindsForAudit() reported %d kinds, the table holds %d", len(audited), len(servableCohortKinds))
	}
	for _, kind := range audited {
		if !servableCohortKinds[kind] {
			t.Errorf("ServableCohortKindsForAudit() reported %q, which the table does not admit", kind)
		}
	}
	// Sorted, so a caller iterating the result never depends on Go's
	// randomized map order.
	for i := 1; i < len(audited); i++ {
		if audited[i-1] >= audited[i] {
			t.Fatalf("ServableCohortKindsForAudit() returned %v, which is not strictly sorted; a caller iterating it would see map order", audited)
		}
	}
}

// TestCohortMemberKindForRefusesEveryKindWithoutAProvenArm is the predicate's
// negative half, quantified over the PUBLISHED vocabulary rather than over a
// hand-picked list.
//
// The count assertion is not decoration. If the allow-list were ever widened
// to the whole contract this set would be empty and a test that only looped
// over it would pass while asserting nothing at all.
func TestCohortMemberKindForRefusesEveryKindWithoutAProvenArm(t *testing.T) {
	t.Parallel()
	carriedNotServable := 0
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := SubjectKind(published)
		if servableCohortKinds[kind] {
			continue
		}
		carriedNotServable++
		expression := SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: kind},
		}
		servable, declared, reason := CohortMemberKindFor(expression)
		if reason != CohortMemberKindUnservable {
			t.Errorf("kind %q has no proven arm but CohortMemberKindFor returned reason %q, want %q", kind, reason, CohortMemberKindUnservable)
		}
		if servable != "" {
			t.Errorf("kind %q was refused but still yielded servable kind %q; a refused kind must yield no kind at all, or a caller can build the cohort the refusal exists to prevent", kind, servable)
		}
		// The REFUSED kind is still reported, and that is the half a refusal
		// used to lose: it could say a member kind was unservable but never
		// which one, so the kind got inferred from question text instead.
		if declared != kind {
			t.Errorf("kind %q was refused but reported declared kind %q; a refusal that cannot name the kind it refused is a refusal someone will attribute by guessing", kind, declared)
		}
		if CohortMemberSetResolvable(expression) {
			t.Errorf("kind %q was refused by CohortMemberKindFor but CohortMemberSetResolvable reported true; the two must be one decision", kind)
		}
	}
	if carriedNotServable == 0 {
		t.Fatal("every kind the wire contract carries also has a proven arm -- either the allow-list was widened without proving one, or this test ran over an empty set and proved nothing")
	}
	t.Logf("kinds carried by the wire contract but deliberately without a proven arm: %d", carriedNotServable)
}

// TestCohortMemberKindForServesEveryKindWithAProvenArm is the positive half. A
// pin that only proved refusals would be satisfied by a predicate that refused
// everything.
func TestCohortMemberKindForServesEveryKindWithAProvenArm(t *testing.T) {
	t.Parallel()
	served := 0
	for _, kind := range ServableCohortKindsForAudit() {
		expression := SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: kind},
		}
		servable, declared, reason := CohortMemberKindFor(expression)
		if reason != CohortDiscoverable || servable != kind {
			t.Errorf("kind %q: CohortMemberKindFor = (%q, %q, %q), want servable %q with reason %q", kind, servable, declared, reason, kind, CohortDiscoverable)
		}
		if declared != kind {
			t.Errorf("kind %q: CohortMemberKindFor reported declared kind %q; on a served expression the declared and servable kinds are the same value", kind, declared)
		}
		if !CohortMemberSetResolvable(expression) {
			t.Errorf("kind %q: CohortMemberKindFor reported discoverable but CohortMemberSetResolvable reported false; the two must be one decision", kind)
		}
		served++
	}
	if served == 0 {
		t.Fatal("no kind has a proven arm -- this test ran over an empty set and proved nothing")
	}
}

// TestCohortMemberKindForRefusesExpressionsThatCanNeverEnumerate is the
// condition a kind lookup alone cannot decide, and the one a naive
// simplification of this predicate deletes.
//
// `SubjectExpression.MemberKind()` answers `ok` for `named_subject` -- it
// reads ExpectedKind, so the kind-hinted pool search stops treating a named
// subject as kindless -- and for `organization_scope`, which carries an
// optional member kind. BOTH can declare a kind that has a proven arm, and
// NEITHER can ever produce a cohort: `IsCohortVariant()` is false for both.
//
// Deciding discoverability on the declared kind alone therefore serves a
// ranking row for "rank team X" and for "how many teams are in the
// organization", which is exactly the defect the change before this one
// shipped a red-at-parent proof against. This test is what stops that
// simplification from looking safe.
func TestCohortMemberKindForRefusesExpressionsThatCanNeverEnumerate(t *testing.T) {
	t.Parallel()
	// A kind with a PROVEN ARM in every fixture, so the only thing that can
	// refuse them is the variant test.
	kind := SubjectTeam
	if !servableCohortKinds[kind] {
		t.Fatalf("fixture kind %q has no proven arm, so these cases would refuse on the kind instead of the variant and prove nothing", kind)
	}
	for _, testCase := range []struct {
		name       string
		expression SubjectExpression
	}{
		{
			name: "named_subject declaring an expected kind",
			expression: SubjectExpression{
				Kind:  SubjectExpressionNamed,
				Named: &NamedSubjectExpression{ExpectedKind: &kind},
			},
		},
		{
			name: "organization_scope declaring a member kind",
			expression: SubjectExpression{
				Kind: SubjectExpressionOrganizationScope,
				Org:  &OrganizationScopeExpression{MemberKind: &kind},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The premise: this expression DOES declare the kind. Without
			// this the case could pass because the fixture was malformed
			// rather than because the variant test fired.
			if declaredKind, ok := testCase.expression.MemberKind(); !ok || declaredKind != kind {
				t.Fatalf("fixture declares MemberKind() = (%q, %v), want (%q, true) -- this case exists to prove the variant test fires DESPITE a servable declared kind", declaredKind, ok, kind)
			}
			servable, declared, reason := CohortMemberKindFor(testCase.expression)
			if reason != CohortNotACohortVariant {
				t.Errorf("CohortMemberKindFor reason = %q, want %q", reason, CohortNotACohortVariant)
			}
			if servable != "" || declared != "" {
				t.Errorf("CohortMemberKindFor = (%q, %q), want both empty: an expression that enumerates nothing has no member kind to report", servable, declared)
			}
			if CohortMemberSetResolvable(testCase.expression) {
				t.Error("CohortMemberSetResolvable reported true for an expression that can never enumerate a member set")
			}
		})
	}
}

// TestCohortDiscoverabilityVocabularyIsClosedAndTotal pins the vocabulary and
// the predicate's totality over it.
func TestCohortDiscoverabilityVocabularyIsClosedAndTotal(t *testing.T) {
	t.Parallel()
	seen := make(map[CohortDiscoverability]bool, CohortDiscoverabilityCount)
	for _, member := range CohortDiscoverabilityVocabulary() {
		if member == "" {
			t.Error("the vocabulary carries an empty member; the empty value is not a reason")
		}
		if seen[member] {
			t.Errorf("the vocabulary carries %q twice", member)
		}
		seen[member] = true
		if !ValidCohortDiscoverability(member) {
			t.Errorf("ValidCohortDiscoverability(%q) is false for a declared member", member)
		}
	}
	if len(seen) != CohortDiscoverabilityCount {
		t.Fatalf("the vocabulary declares %d distinct members, CohortDiscoverabilityCount is %d", len(seen), CohortDiscoverabilityCount)
	}
	if ValidCohortDiscoverability("") {
		t.Error("ValidCohortDiscoverability(\"\") is true; the empty value is not a member")
	}
	if ValidCohortDiscoverability("not_a_declared_reason") {
		t.Error("ValidCohortDiscoverability admitted a value the vocabulary never declared")
	}
	// TOTAL: every expression variant yields a declared reason, including the
	// zero expression, which is what a caller gets from an unset frame field.
	kind := SubjectTeam
	for _, expression := range []SubjectExpression{
		{},
		{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{}},
		{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}},
		{Kind: SubjectExpressionExplicitSet},
		{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{}},
		{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: kind}},
		{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: SubjectIncident}},
		{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{MemberKind: kind}},
		{Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{MemberKind: kind, GroupKind: SubjectProject}},
	} {
		_, _, reason := CohortMemberKindFor(expression)
		if !ValidCohortDiscoverability(reason) {
			t.Errorf("expression kind %q yielded reason %q, which the vocabulary does not declare", expression.Kind, reason)
		}
	}
}
