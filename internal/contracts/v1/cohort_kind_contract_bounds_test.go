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
		if err := cohort.Validate(); err != nil {
			refused = append(refused, fmt.Sprintf("%s (%v)", published, err))
		}
	}
	if reached != len(kinds) {
		t.Fatalf("reached %d kinds, want %d -- the loop did not assert on every published kind", reached, len(kinds))
	}
	if reached == 0 {
		t.Fatal("no published cohort kind was checked -- the assertion never ran")
	}
	if len(refused) > 0 {
		t.Fatalf("ContextFabricCohort.Validate refuses %d of %d kinds the published schema %s advertises: %s",
			len(refused), len(kinds), cohortKindSchemaPointer, strings.Join(refused, "; "))
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
	const unpublished ContextFabricSubjectKind = "squad"
	if _, exists := publishedSet[string(unpublished)]; exists {
		t.Fatalf("%q is published, so it cannot serve as the out-of-vocabulary control", unpublished)
	}

	cohort := validCohortOfKind(unpublished)
	err := cohort.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for a kind outside the published vocabulary, want a refusal")
	}
	if !strings.Contains(err.Error(), "cohort violates v1 bounds") {
		t.Fatalf("Validate() = %q, want the cohort bound's own message -- a refusal from a different rule would prove nothing about the kind bound", err)
	}

	// Attribution: the ONLY difference is the kind.
	repaired := validCohortOfKind(ContextFabricSubjectTeam)
	if err := repaired.Validate(); err != nil {
		t.Fatalf("the control fixture is invalid for a reason other than its kind: %v -- the refusal above is therefore unattributable", err)
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

// TestEveryPublishedCohortKindEnumAgrees sweeps EVERY canonical schema
// document for a cohort-kind enum and requires each to publish the same
// vocabulary.
//
// The sites are found by WALKING the documents, not from a list written here:
// a list would be correct on the day it was written and silently incomplete
// afterwards, which is the enumeration failure this program has hit
// repeatedly. A new document carrying a cohort is covered the moment it
// lands.
func TestEveryPublishedCohortKindEnumAgrees(t *testing.T) {
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
			overlap := 0
			for _, value := range values {
				if _, ok := wantSet[value]; ok {
					overlap++
				}
			}
			if overlap == 0 {
				return
			}
			if !strings.Contains(pointer, "Cohort") {
				return
			}
			sites = append(sites, site{document: name, pointer: pointer, values: values})
		})
	}

	if len(sites) == 0 {
		t.Fatalf("no cohort-kind enum was found across %d canonical schema documents -- the sweep proved nothing", len(schemas.documents))
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
	t.Logf("cohort-kind enum sites swept: %d across %d canonical documents", len(sites), len(schemas.documents))
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
