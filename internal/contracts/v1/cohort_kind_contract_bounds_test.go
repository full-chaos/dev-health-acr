package v1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The cohort wire contract's SUBJECT-KIND bound, checked against the
// vocabulary the published schemas PROMISE rather than against the Go type
// the validator is written beside.
//
// WHY THIS FILE EXISTS, and why it is not a second copy of
// schema_go_field_parity_test.go:
//
// TestPublishedEnumsMatchGoVocabularies (schema_go_field_parity_test.go)
// already binds context_fabric_common.v1's /$defs/Cohort/properties/kind and
// it PASSED for the whole life of the two-kind bound. It compares a published
// enum against the Go TYPE's constant set -- ContextFabricSubjectKind's 15
// constants against the schema's 15 members -- and never calls
// ContextFabricCohort.validate. A validator predicate NARROWER THAN ITS OWN
// TYPE is invisible to it by construction. That is the class this file
// closes: the schema promised 15 cohort kinds, the validator admitted 2, both
// artifacts were "in parity" by every test in the tree, and the divergence
// surfaced as an HTTP 500 on a real question the first time a caller carried
// a third kind.
//
// So every assertion here drives the VALIDATOR, and the set it iterates is
// read out of the published document at run time. A test that iterated
// ContextFabricSubjectKindVocabulary() instead would agree with the Go side
// by construction and could never fail for this reason again.

// cohortKindSchemaPointer names the published node that IS the cohort-kind
// promise: the wire result's cohort.
const (
	cohortKindSchemaDocument = "context_fabric_common.v1.schema.json"
	cohortKindSchemaPointer  = "/$defs/Cohort/properties/kind"
)

// publishedCohortKinds reads the cohort-kind vocabulary out of the published
// schema document -- the PROMISE, which is the authority for what the
// validator must accept.
//
// It deliberately does not consult ContextFabricSubjectKindVocabulary(): an
// oracle that asks the Go side what to expect cannot detect the Go side being
// wrong, which is the entire defect this file pins.
//
// THE LIMIT OF THAT CHOICE, stated because it is real and was measured rather
// than reasoned about: an authority read at run time moves when the document
// moves. Deleting a kind from $defs.Cohort.properties.kind narrows `want` too,
// and TestCohortValidateAcceptsEveryPublishedCohortKind then passes over the
// smaller set -- verified by mutation, not assumed. Two assertions close that
// direction, so the pair is complete without re-coupling the oracle to the Go
// side: TestEveryPublishedSubjectKindEnumAgrees below fails because the other
// published sites still carry the full vocabulary, and
// TestCohortKindAuthorityHasNotNarrowed fails because the authority no longer
// matches the Go type. Both were confirmed red under that mutation.
func publishedCohortKinds(t *testing.T) []string {
	t.Helper()
	root := moduleRootForParity(t)
	path := filepath.Join(append(append([]string{root}, schemaDirParts...), cohortKindSchemaDocument)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	node, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no $defs object", cohortKindSchemaDocument)
	}
	cohort, ok := node["Cohort"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no $defs.Cohort", cohortKindSchemaDocument)
	}
	properties, ok := cohort["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s $defs.Cohort has no properties", cohortKindSchemaDocument)
	}
	kind, ok := properties["kind"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s is absent", cohortKindSchemaDocument, cohortKindSchemaPointer)
	}
	values := schemaEnumValues(kind)
	if len(values) == 0 {
		t.Fatalf("%s %s publishes no enum -- the promise this file checks against does not exist, so every assertion below would be vacuous", cohortKindSchemaDocument, cohortKindSchemaPointer)
	}
	return values
}

// validCohortOfKind builds a cohort that is valid in EVERY respect except
// possibly its kind: two uniquely-identified members in strictly increasing
// Rank, bounded rationale, and Complete/Truncated not both set.
//
// The members are deliberately UNRANKED (RankingComputed false, Score nil).
// That is a first-class legal shape -- ContextFabricCohortMember's own
// contract calls it "an offers-only discovery, or a request that never
// confirmed a window" -- and choosing it keeps the ranking sub-contract out
// of a fixture whose only subject is the kind bound. A ranked member drags in
// the Drivers invariant (Sum(WeightContributed) must reconstruct *Score to
// float64 rounding, and the driver set must equal the family-name subset of
// RankingBasis), none of which this file is about; the first draft of this
// fixture was ranked and was refused for a missing Drivers list, which made
// team and project fail for a reason that had nothing to do with kind.
//
// The attribution assertion in the refusal test below is what proves this
// fixture is valid-but-for-the-kind, rather than leaving it as a claim.
func validCohortOfKind(kind ContextFabricSubjectKind) ContextFabricCohort {
	member := func(rank int, id, label string) ContextFabricCohortMember {
		return ContextFabricCohortMember{
			Subject:          ContextFabricSubjectRef{Kind: kind, CanonicalID: id, Label: label},
			Rank:             rank,
			InclusionReasons: []string{"matched the confirmed window"},
		}
	}
	return ContextFabricCohort{
		Kind: kind,
		Members: []ContextFabricCohortMember{
			member(1, string(kind)+":alpha", "Alpha"),
			member(2, string(kind)+":beta", "Beta"),
		},
		Rationale: "every member of the declared kind that the window authorized",
		Complete:  true,
		Truncated: false,
	}
}

// TestCohortValidateAcceptsEveryPublishedCohortKind is the bound this ticket
// moves, stated against the published promise.
//
// RED at the base commit for every kind but team and project.
//
// The reached counter is not decoration: the loop body can only assert on a
// kind the schema actually published, so a document that lost its enum, or a
// filter that silently excluded every case, would otherwise read as a pass
// over zero inputs.
func TestCohortValidateAcceptsEveryPublishedCohortKind(t *testing.T) {
	t.Parallel()
	kinds := publishedCohortKinds(t)
	reached := 0
	var refused []string
	for _, published := range kinds {
		kind := ContextFabricSubjectKind(published)
		cohort := validCohortOfKind(kind)
		reached++
		err := cohort.Validate()
		if err == nil {
			continue
		}
		// Report the refusal ATTRIBUTABLY, per kind. A bare list of kinds
		// and errors is not enough to tell "this predicate rejects the
		// kind" from "this fixture is malformed for an unrelated reason":
		// the first draft of validCohortOfKind was refused on the ranking
		// sub-contract's `cohort member has a score but no drivers`, which
		// would have read as a larger and more convincing red while proving
		// nothing about the kind bound.
		//
		// So each refusal carries the message it actually red on, and
		// whether the SAME fixture becomes valid when only the kind is
		// swapped for one the validator is known to accept. attributable
		// means the kind is the sole cause.
		attribution := "attributable=kind"
		if control := validCohortOfKind(ContextFabricSubjectTeam); control.Validate() != nil {
			attribution = "attributable=UNKNOWN (the control fixture is itself invalid, so this refusal says nothing about the kind bound)"
		}
		refused = append(refused, fmt.Sprintf("%s red on %q, %s", published, err.Error(), attribution))
	}
	if reached != len(kinds) {
		t.Fatalf("reached %d kinds, want %d -- the loop did not assert on every published kind", reached, len(kinds))
	}
	if reached == 0 {
		t.Fatal("no published cohort kind was checked -- the assertion never ran")
	}
	if len(refused) > 0 {
		t.Fatalf("ContextFabricCohort.Validate refuses %d of %d kinds the published schema %s advertises:\n  %s",
			len(refused), len(kinds), cohortKindSchemaPointer, strings.Join(refused, "\n  "))
	}
}

// TestCohortKindAuthorityHasNotNarrowed guards the document this file trusts.
//
// Every other assertion here treats the published schema as the authority on
// what the validator must accept. That is the right authority -- it is the
// promise callers were given -- but it makes the document itself a place a
// narrowing could hide: shrink the enum and the oracle shrinks with it.
//
// So the authority is itself pinned, against the Go closed vocabulary. This is
// the ONE place in this file that consults ContextFabricSubjectKindVocabulary,
// and it is not the oracle for any validator assertion; it exists so that a
// silently narrowed promise fails here by name.
//
// TestPublishedEnumsMatchGoVocabularies (schema_go_field_parity_test.go) also
// covers this document generically. The overlap is deliberate: that test's
// scope is every bound enum in the tree and it can be re-scoped by work that
// has nothing to do with cohorts, whereas this one fails with the cohort
// bound's own name attached.
func TestCohortKindAuthorityHasNotNarrowed(t *testing.T) {
	t.Parallel()
	published := publishedCohortKinds(t)

	vocabulary := ContextFabricSubjectKindVocabulary()
	want := make([]string, 0, len(vocabulary))
	for _, kind := range vocabulary {
		want = append(want, string(kind))
	}
	sort.Strings(want)

	if len(published) != len(want) {
		t.Fatalf("%s %s publishes %d kinds, but the Go closed vocabulary has %d: published=%v go=%v -- the authority this file reads its expectations from has drifted, so every other assertion in this file is measuring against the wrong set",
			cohortKindSchemaDocument, cohortKindSchemaPointer, len(published), len(want), published, want)
	}
	for i := range want {
		if published[i] != want[i] {
			t.Fatalf("%s %s publishes %v, Go closed vocabulary is %v", cohortKindSchemaDocument, cohortKindSchemaPointer, published, want)
		}
	}
}

// TestCohortValidateRefusesKindOutsideThePublishedVocabulary is the negative
// control: widening the bound to the published vocabulary must not open it to
// anything else. A 16th, unpublished kind stays refused by the validator.
//
// It carries BOTH halves of a real breach proof, because either alone has a
// hole on this predicate:
//
//  1. REASON -- the error must name the cohort bound. Catches a rejection
//     raised somewhere else entirely.
//  2. ATTRIBUTION -- restoring ONLY the kind must make the same fixture
//     valid. This is the half the message check cannot supply here:
//     ContextFabricCohort.validate refuses kind, nil members, member count,
//     exclusion count, rationale bounds and the complete/truncated
//     contradiction through ONE compound predicate emitting ONE message, so a
//     fixture that were malformed in some other field would produce a
//     byte-identical error and pass check 1 while proving nothing about kind.
func TestCohortValidateRefusesKindOutsideThePublishedVocabulary(t *testing.T) {
	t.Parallel()
	published := publishedCohortKinds(t)
	publishedSet := make(map[string]struct{}, len(published))
	for _, value := range published {
		publishedSet[value] = struct{}{}
	}
	// MANY values, not one. A single literal is not a vocabulary check: a
	// predicate that special-cases exactly the one value the test uses
	// satisfies it while admitting everything else. Round 1 of review
	// demonstrated this -- replacing the whole closed-vocabulary predicate
	// with `c.Kind == "squad"` (the sole value this test used) left all six
	// assertions in this file green while `department`, `guild` and every
	// other unpublished string validated.
	//
	// The values below vary along the axes a narrow guard is most likely to
	// get wrong: an ordinary unknown noun, a plural of a real kind, a case
	// variant, a whitespace variant, a near-miss on a real kind's spelling,
	// and the empty string.
	unpublished := []ContextFabricSubjectKind{
		"squad", "department", "guild",
		"repositories", "Team", "team ", " team",
		"work-item", "workitem", "",
	}

	// EMPTY, non-nil members. The cohort-kind conjunct is evaluated before
	// the member loop, but a member whose Subject.Kind mirrors an
	// out-of-vocabulary cohort kind would ALSO be rejected by
	// ContextFabricSubjectRef.Validate -- so a fixture carrying members
	// cannot distinguish "the cohort kind was refused" from "a member's
	// subject kind was refused". Zero members removes that second rule from
	// the fixture entirely, leaving the cohort-kind bound as the only rule
	// that can fire.
	//
	// This rests on empty cohorts being contract-valid, which is asserted
	// directly below rather than assumed: `members` carries no minItems in
	// context_fabric_common.v1, and validate() only requires it to be
	// non-nil. If that ever changes, this test fails on the control rather
	// than silently proving nothing.
	emptyCohortOfKind := func(kind ContextFabricSubjectKind) ContextFabricCohort {
		return ContextFabricCohort{
			Kind:      kind,
			Members:   []ContextFabricCohortMember{},
			Rationale: "no member of the declared kind was authorized by the window",
			Complete:  true,
		}
	}

	// Attribution control, and the stated assumption's own proof: the same
	// shape with a PUBLISHED kind must validate. If it does not, every
	// refusal below is unattributable and this test proves nothing.
	control := emptyCohortOfKind(ContextFabricSubjectTeam)
	if err := control.Validate(); err != nil {
		t.Fatalf("the empty-cohort control is invalid for a reason other than its kind: %v -- every refusal below is therefore unattributable, and the assumption that empty cohorts are contract-valid no longer holds", err)
	}

	checked := 0
	for _, kind := range unpublished {
		if _, exists := publishedSet[string(kind)]; exists {
			t.Errorf("%q is published, so it cannot serve as an out-of-vocabulary control", kind)
			continue
		}
		checked++
		err := emptyCohortOfKind(kind).Validate()
		if err == nil {
			t.Errorf("Validate() = nil for unpublished kind %q, want a refusal", kind)
			continue
		}
		if !strings.Contains(err.Error(), "cohort violates v1 bounds") {
			t.Errorf("Validate() for unpublished kind %q = %q, want the cohort bound's own message -- a refusal from a different rule would prove nothing about the kind bound", kind, err)
		}
	}
	if checked != len(unpublished) {
		t.Fatalf("checked %d of %d out-of-vocabulary values -- the loop did not reach them all", checked, len(unpublished))
	}
	if checked < 2 {
		t.Fatal("a single out-of-vocabulary value cannot distinguish a vocabulary check from a special case for that one value")
	}
}

// TestProjectedCohortValidateAcceptsEveryPublishedCohortKind pins the SECOND
// Go cohort sink, the answer projection's, which already admits the full
// vocabulary. It is green at the base commit and exists so that a later
// narrowing of the projection validator cannot pass unnoticed the way the
// wire validator's did.
func TestProjectedCohortValidateAcceptsEveryPublishedCohortKind(t *testing.T) {
	t.Parallel()
	kinds := publishedCohortKinds(t)
	reached := 0
	var refused []string
	for _, published := range kinds {
		kind := ContextFabricSubjectKind(published)
		projected := ContextFabricProjectedCohort{
			Kind: kind,
			Members: []ContextFabricProjectedCohortMember{{
				Subject:          ContextFabricSubjectRef{Kind: kind, CanonicalID: string(kind) + ":alpha", Label: "Alpha"},
				Rank:             1,
				InclusionReasons: []string{"matched the confirmed window"},
			}},
			Total:     1,
			Rationale: "every member of the declared kind that the window authorized",
		}
		reached++
		if err := projected.Validate(); err != nil {
			refused = append(refused, fmt.Sprintf("%s (%v)", published, err))
		}
	}
	if reached != len(kinds) || reached == 0 {
		t.Fatalf("reached %d kinds, want %d and non-zero", reached, len(kinds))
	}
	if len(refused) > 0 {
		t.Fatalf("ContextFabricProjectedCohort.Validate refuses %d of %d published kinds: %s", len(refused), len(kinds), strings.Join(refused, "; "))
	}
}

// TestEveryPublishedSubjectKindEnumAgrees sweeps EVERY canonical schema
// document for a subject-kind enum and requires each to publish the same
// vocabulary.
//
// It is named for SUBJECT kinds rather than cohort kinds because that is the
// population it actually covers. Cohort, ProjectedCohort, SubjectRef,
// SubjectHint and the standalone SubjectKind defs all publish one vocabulary
// on one axis, and a narrowing at any of them contradicts the rest. Scoping
// the sweep to nodes whose pointer spells "Cohort" was round 1's finding: it
// covered 5 of the 19 sites that publish this vocabulary and reported the
// subset as the population.
//
// The sites are found by WALKING the documents and testing each enum's
// CONTENT, not from a list written here and not from a node's name: a list
// would be correct on the day it was written and silently incomplete
// afterwards, and a name is not the population.
func TestEveryPublishedSubjectKindEnumAgrees(t *testing.T) {
	t.Parallel()
	want := publishedCohortKinds(t)
	wantSet := make(map[string]struct{}, len(want))
	for _, value := range want {
		wantSet[value] = struct{}{}
	}

	root := moduleRootForParity(t)
	schemas := loadCanonicalSchemas(t, root)

	type site struct {
		document string
		pointer  string
		values   []string
	}
	var sites []site
	for name, document := range schemas.documents {
		walkSchemaEnums(document, "", func(pointer string, node map[string]any) {
			values := schemaEnumValues(node)
			if len(values) == 0 {
				return
			}
			// A cohort-kind enum is one whose members are drawn from the
			// subject-kind vocabulary. Intersection, not equality, is the
			// selector: an enum that has DRIFTED NARROW is exactly what this
			// test must catch, and an equality selector would filter it out
			// of its own population.
			if !publishesSubjectKinds(values, wantSet) {
				return
			}
			sites = append(sites, site{document: name, pointer: pointer, values: values})
		})
	}

	if len(sites) == 0 {
		t.Fatalf("no subject-kind enum was found across %d canonical schema documents -- the sweep proved nothing", len(schemas.documents))
	}
	// A FLOOR on the population, because the failure this sweep exists to
	// catch is a selector that finds too FEW sites. Round 1 of review found
	// exactly that: the selector used to require the literal string "Cohort"
	// in the JSON pointer, which matched 5 of the 19 sites that actually
	// publish this vocabulary, and a narrowed enum introduced at any of the
	// other 14 passed unnoticed. A bare "len > 0" check would not have
	// caught it, because 5 is greater than 0.
	//
	// Adding a schema that publishes the vocabulary raises this number and
	// the pin moves in that commit, deliberately. Losing sites means the
	// selector regressed.
	const knownSubjectKindEnumSites = 19
	if len(sites) < knownSubjectKindEnumSites {
		t.Errorf("swept %d subject-kind enum sites, expected at least %d -- the selector has regressed and is covering a SUBSET of the publishers; if a schema was deliberately removed, lower this pin in that commit and say so", len(sites), knownSubjectKindEnumSites)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].document != sites[j].document {
			return sites[i].document < sites[j].document
		}
		return sites[i].pointer < sites[j].pointer
	})
	for _, s := range sites {
		if len(s.values) != len(want) {
			t.Errorf("%s %s publishes %d cohort kinds, want %d: %v", s.document, s.pointer, len(s.values), len(want), s.values)
			continue
		}
		for i := range want {
			if s.values[i] != want[i] {
				t.Errorf("%s %s publishes %v, want %v", s.document, s.pointer, s.values, want)
				break
			}
		}
	}
	t.Logf("subject-kind enum sites swept: %d across %d canonical documents", len(sites), len(schemas.documents))
}

// TestOpenAPIPublishesNoIndependentSubjectKindVocabulary pins the OpenAPI
// sink's DERIVED nature.
//
// Measured at the base commit: contracts/openapi/acr-v1.json carries no
// subject-kind enum of its own and reaches every cohort-bearing shape through
// $ref into contracts/jsonschema/v1. Widening the vocabulary therefore
// changes no OpenAPI byte -- but only for as long as that stays true. If
// someone later inlines a kind enum into the OpenAPI document, it becomes a
// fourth place the vocabulary can drift, and this test fails then rather than
// on a rig run.
//
// The positive control matters: an assertion that a document does NOT contain
// something is worthless unless the document was actually read.
func TestOpenAPIPublishesNoIndependentSubjectKindVocabulary(t *testing.T) {
	t.Parallel()
	want := publishedCohortKinds(t)
	wantSet := make(map[string]struct{}, len(want))
	for _, value := range want {
		wantSet[value] = struct{}{}
	}

	root := moduleRootForParity(t)
	path := filepath.Join(root, "contracts", "openapi", "acr-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	// Positive control: the document is the one we mean, and the walker
	// reaches into it. Without this, an empty or unparsed document would
	// satisfy every assertion below.
	refs := 0
	walkSchemaEnums(document, "", func(string, map[string]any) {})
	walkSchemaRefs(document, func(ref string) {
		if strings.Contains(ref, "jsonschema/v1/") {
			refs++
		}
	})
	if refs == 0 {
		t.Fatalf("%s contains no $ref into jsonschema/v1 -- either the document changed shape or it was not read, so the negative assertion below cannot be trusted", path)
	}

	var offenders []string
	walkSchemaEnums(document, "", func(pointer string, node map[string]any) {
		for _, value := range schemaEnumValues(node) {
			if _, ok := wantSet[value]; ok {
				offenders = append(offenders, fmt.Sprintf("%s (%v)", pointer, schemaEnumValues(node)))
				return
			}
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("%s now inlines a subject-kind vocabulary at %d site(s): %s -- OpenAPI must keep deriving the vocabulary through $ref, or it becomes another place it can drift", path, len(offenders), strings.Join(offenders, "; "))
	}
	t.Logf("OpenAPI derives its cohort vocabulary through %d $ref(s) into jsonschema/v1 and inlines none", refs)
}

// publishesSubjectKinds decides whether an enum is a publisher of the
// subject-kind vocabulary, BY ITS CONTENT.
//
// The rule: a non-empty enum every one of whose members is a subject kind is
// publishing that vocabulary, and must therefore publish all of it. An enum
// that merely overlaps (a mixed vocabulary that happens to contain "team")
// is not one, and is left alone.
//
// This replaces a selector that required the literal string "Cohort" in the
// node's JSON pointer. That selector was keyed on a NOUN rather than on the
// population it had to cover: it matched 5 sites while 19 documents' nodes
// actually publish this vocabulary, so a narrowed enum introduced at any of
// the other 14 -- or at any new node whose name nobody thought to spell
// "Cohort" -- was excluded from the sweep before it was ever compared. Round
// 1 of review demonstrated it with a `$defs/EntitySet/properties/kind` enum
// restricted to ["repository"], which the sweep did not see.
//
// Measured when this was written: all 19 sites carry the full vocabulary and
// none is deliberately narrow, so this predicate needs no exception list. If
// a legitimately narrow publisher is ever added, it needs a declared
// exception here with its reason -- not a quiet loosening of the predicate.
func publishesSubjectKinds(values []string, vocabulary map[string]struct{}) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if _, ok := vocabulary[value]; !ok {
			return false
		}
	}
	return true
}

// TestSubjectKindEnumSelectorIsNotKeyedOnANodeName is the positive control
// for the selector above, and it is the regression guard for round 1's
// finding.
//
// A sweep is only as good as its selector, and a selector's blind spot is
// invisible from the sweep's own green result -- the sweep reported "5 sites"
// with total confidence while missing 14. So the selector is tested directly,
// against a node whose pointer deliberately contains no "Cohort" and no
// "Subject": under the old name-keyed rule this case was invisible; under the
// content-keyed rule it is caught.
func TestSubjectKindEnumSelectorIsNotKeyedOnANodeName(t *testing.T) {
	t.Parallel()
	published := publishedCohortKinds(t)
	vocabulary := make(map[string]struct{}, len(published))
	for _, value := range published {
		vocabulary[value] = struct{}{}
	}

	cases := []struct {
		name   string
		enum   []string
		want   bool
		reason string
	}{
		{"narrowed publisher at an unrelated node name", []string{"repository"}, true,
			"round 1's witness: $defs/EntitySet/properties/kind, enum ['repository'] -- a real narrowing the name-keyed selector could not see"},
		{"full vocabulary", published, true,
			"the ordinary case"},
		{"single published kind", []string{"team"}, true,
			"a one-member narrowing is the most dangerous shape, not the least"},
		{"mixed vocabulary that merely overlaps", []string{"team", "not_a_subject_kind"}, false,
			"an enum containing a non-kind is a different vocabulary that happens to share a word"},
		{"unrelated vocabulary", []string{"open", "closed"}, false,
			"no overlap at all"},
		{"empty enum", nil, false,
			"nothing is published, so there is nothing to compare"},
	}
	for _, tc := range cases {
		if got := publishesSubjectKinds(tc.enum, vocabulary); got != tc.want {
			t.Errorf("publishesSubjectKinds(%v) = %v, want %v -- %s", tc.enum, got, tc.want, tc.reason)
		}
	}
}

// walkSchemaEnums visits every object node in a decoded JSON document,
// reporting a JSON-pointer-ish path for each. It does not resolve $ref: the
// callers above are asking what a DOCUMENT literally contains, which is the
// question a resolving walk would erase.
func walkSchemaEnums(node any, pointer string, visit func(pointer string, node map[string]any)) {
	switch typed := node.(type) {
	case map[string]any:
		visit(pointer, typed)
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkSchemaEnums(typed[key], pointer+"/"+key, visit)
		}
	case []any:
		for i, item := range typed {
			walkSchemaEnums(item, fmt.Sprintf("%s/%d", pointer, i), visit)
		}
	}
}

func walkSchemaRefs(node any, visit func(ref string)) {
	switch typed := node.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			visit(ref)
		}
		for _, value := range typed {
			walkSchemaRefs(value, visit)
		}
	case []any:
		for _, item := range typed {
			walkSchemaRefs(item, visit)
		}
	}
}
