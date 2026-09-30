package v1

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"strings"
	"testing"
)

// CHAOS-4690 item 4's enforcement: the display-label registries are TOTAL
// over their closed vocabularies, so a new enum value cannot ship without
// its label — "unmapped" is unrepresentable, by CI rather than by review.

func TestFactKindLabelsAreTotal(t *testing.T) {
	for _, kind := range ContextFabricFactKindVocabulary() {
		label, ok := contextFabricFactKindLabels[kind]
		if !ok {
			t.Errorf("fact kind %q has no display label; add it to contextFabricFactKindLabels in the SAME change that adds the kind", kind)
			continue
		}
		if strings.TrimSpace(label) == "" {
			t.Errorf("fact kind %q has an empty display label", kind)
		}
	}
	if len(contextFabricFactKindLabels) != ContextFabricFactKindCount {
		t.Errorf("label registry has %d entries, vocabulary has %d — a stale label outlives its kind", len(contextFabricFactKindLabels), ContextFabricFactKindCount)
	}
}

func TestSourceStateLabelsAreTotal(t *testing.T) {
	states := []ContextFabricSourceState{
		ContextFabricSourceAvailable, ContextFabricSourceStale, ContextFabricSourceUnavailable,
		ContextFabricSourceUnconfigured, ContextFabricSourceUnauthorized, ContextFabricSourceNoData,
		ContextFabricSourceTruncated, ContextFabricSourceConflicted, ContextFabricSourceNotApplicable,
		ContextFabricSourcePruned,
	}
	for _, state := range states {
		if !validSourceState(state) {
			t.Fatalf("test fixture out of date: %q is not a valid state", state)
		}
		label, ok := contextFabricSourceStateLabels[state]
		if !ok || strings.TrimSpace(label) == "" {
			t.Errorf("source state %q has no display label", state)
		}
	}
	if len(contextFabricSourceStateLabels) != len(states) {
		t.Errorf("state label registry has %d entries, vocabulary has %d", len(contextFabricSourceStateLabels), len(states))
	}
}

// TestEvidenceEntityLabelsAreTotal pins CHAOS-4698's totality discipline in
// BOTH directions, mirroring TestFactKindLabelsAreTotal:
//  1. every enum member has a label (mutate: remove a member's entry from
//     contextFabricEvidenceEntityLabels -> this loop fails);
//  2. every label registry entry names a real enum member (mutate: insert
//     an entry keyed by a string outside the vocabulary -> this loop fails
//     via validEvidenceEntityType, independent of the count check).
func TestEvidenceEntityLabelsAreTotal(t *testing.T) {
	for _, entityType := range ContextFabricEvidenceEntityTypeVocabulary() {
		label, ok := contextFabricEvidenceEntityLabels[entityType]
		if !ok {
			t.Errorf("evidence entity type %q has no display label; add it to contextFabricEvidenceEntityLabels in the SAME change that adds the type", entityType)
			continue
		}
		if strings.TrimSpace(label) == "" {
			t.Errorf("evidence entity type %q has an empty display label", entityType)
		}
	}
	for entityType := range contextFabricEvidenceEntityLabels {
		if !validEvidenceEntityType(entityType) {
			t.Errorf("label registry has entry %q with no corresponding enum member — a stale label outlives its type", entityType)
		}
	}
	if len(contextFabricEvidenceEntityLabels) != ContextFabricEvidenceEntityTypeCount {
		t.Errorf("label registry has %d entries, vocabulary has %d", len(contextFabricEvidenceEntityLabels), ContextFabricEvidenceEntityTypeCount)
	}
}

// TestEvidenceRefIDIsTypedAndTotal pins the OTHER half of CHAOS-4698: every
// producer constructor is reachable only through the closed enum, so
// EvidenceRefID can never observably mint a ref whose segment misses the
// registry. If this ever went false, ContextFabricEvidenceRefLabel would
// silently start falling back on an acr-minted ref -- the exact defect
// class this ticket closes.
//
// Asserts the label matches the ENTITY TYPE's OWN registered label, not
// just that some label was known (merge-gate round P3, ARGUED): a mutant
// EvidenceRefID that always minted "acr:v1:commit:x" regardless of
// entityType would pass a known-only check for every member, since
// "commit" is itself a registered segment, while every non-commit member
// silently mislabeled as Commit.
func TestEvidenceRefIDIsTypedAndTotal(t *testing.T) {
	for _, entityType := range ContextFabricEvidenceEntityTypeVocabulary() {
		if _, retired := RetiredEvidenceEntityType(entityType); retired {
			// Retired kinds are refused at mint time
			// (TestEvidenceRefIDRefusesRetiredKinds) and labeled from the
			// stored refs that still carry them.
			ref := ContextFabricEvidenceRefPrefix + string(entityType) + ":x"
			if label, known := ContextFabricEvidenceRefLabel(ref); !known || label != contextFabricEvidenceEntityLabels[entityType]+": x" {
				t.Errorf("retired %q labels as (%q, %v)", entityType, label, known)
			}
			continue
		}
		ref := EvidenceRefID(entityType, "x")
		label, known := ContextFabricEvidenceRefLabel(ref)
		if !known {
			t.Errorf("EvidenceRefID(%q, ...) produced a ref that ContextFabricEvidenceRefLabel does not recognize: %q", entityType, ref)
			continue
		}
		wantLabel := contextFabricEvidenceEntityLabels[entityType] + ": x"
		if label != wantLabel {
			t.Errorf("EvidenceRefID(%q, ...) round-tripped to label %q, want %q -- the segment does not match the entity type that constructed it", entityType, label, wantLabel)
		}
	}
}

// TestEvidenceRefIDPanicsOnUnregisteredEntityType pins codex round 2's P2
// (EXECUTED): ContextFabricEvidenceEntityType is a named string, so an
// UNTYPED string constant coerces to it silently -- EvidenceRefID("service",
// id) type-checks and compiles even though "service" names no declared
// member. Before EvidenceRefID's runtime guard was added, this call
// returned "acr:v1:service:id" with no error and no panic (confirmed by
// running this exact repro against the pre-fix code). The guard is what
// makes "cannot mint a segment outside the registry" true in practice, not
// only in the type signature -- red on the pre-fix code, green here.
func TestEvidenceRefIDPanicsOnUnregisteredEntityType(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("EvidenceRefID(\"service\", ...) did not panic; an untyped string literal outside the closed vocabulary must not silently mint a ref")
		}
		message, ok := r.(string)
		if !ok || !strings.Contains(message, "service") {
			t.Fatalf("panic value %v does not name the offending entity type", r)
		}
	}()
	_ = EvidenceRefID("service", "42")
}

func TestSourceObservationLabels(t *testing.T) {
	for _, kind := range ContextFabricFactKindVocabulary() {
		label := ContextFabricSourceObservationLabel("canonical_fact:" + string(kind))
		if label == "Source" || label == "Canonical facts" {
			t.Errorf("canonical_fact:%s fell back to the generic label", kind)
		}
		if !strings.HasPrefix(label, "Canonical facts — ") {
			t.Errorf("canonical_fact:%s label %q lacks the taxonomy prefix", kind, label)
		}
	}
	cases := map[string]string{
		"context-fabric:graph":                  "Relationship graph",
		"context-fabric:graph-validity-windows": "Relationship graph — undated elements",
		"dev-health-ops:work_items":             "Dev Health — work items",
		"dev-health-ops:":                       "Dev Health",
		"canonical_fact:not_a_kind":             "Canonical facts",
		"something-else":                        "Source",
	}
	for source, want := range cases {
		if got := ContextFabricSourceObservationLabel(source); got != want {
			t.Errorf("%q: got %q, want %q", source, got, want)
		}
	}
}

func TestEvidenceRefLabels(t *testing.T) {
	cases := []struct {
		ref   string
		want  string
		known bool
	}{
		{"acr:v1:team:gh:ops-team", "Team: gh:ops-team", true},
		{"acr:v1:pull-request:42", "Pull request: 42", true},
		{"acr:v1:commit:deadbeef", "Commit: deadbeef", true},
		{"acr:v1:ci:123", "CI run: 123", true},
		// Unknown segment: the generic floor, and the caller counts it.
		// CHAOS-4698 closed this at the PRODUCER signature (EvidenceRefID
		// takes the closed enum, see TestEvidenceRefIDIsTypedAndTotal) --
		// this read-time path stays open on purpose, for a ref that
		// predates the enum or was minted by another system.
		{"acr:v1:service:api", "Evidence: api", false},
		{"not-an-acr-ref", "Evidence", false},
		{"acr:v1:team", "Evidence", false},
	}
	for _, tc := range cases {
		got, known := ContextFabricEvidenceRefLabel(tc.ref)
		if got != tc.want || known != tc.known {
			t.Errorf("%q: got (%q,%v), want (%q,%v)", tc.ref, got, known, tc.want, tc.known)
		}
	}
	for segment, label := range contextFabricEvidenceEntityLabels {
		if strings.TrimSpace(label) == "" {
			t.Errorf("entity segment %q has an empty label", segment)
		}
	}
}

// TestCoverageDetailLabelQuantities pins the digit rule's other half: the
// deterministic Label is the ONLY surface that states a quantity, so its
// count phrasing must be right in both number forms and in the absent case.
func TestCoverageDetailLabelQuantities(t *testing.T) {
	detail := validDetailForCode(ContextFabricCoverageDetailGraphEndpointLookupFailed)
	detail.Count = intPtr(1)
	if got := ComposeCoverageDetailLabel(detail); got != "1 relationship link could not be resolved" {
		t.Errorf("singular: %q", got)
	}
	detail.Count = intPtr(3)
	if got := ComposeCoverageDetailLabel(detail); got != "3 relationship links could not be resolved" {
		t.Errorf("plural: %q", got)
	}
	detail.Count = nil
	if got := ComposeCoverageDetailLabel(detail); got != "some relationship links could not be resolved" {
		t.Errorf("absent count: %q", got)
	}
	narrowed := validDetailForCode(ContextFabricCoverageDetailFactProviderReported)
	narrowed.Narrowed = true
	narrowed.SkippedKinds = []ContextFabricSubjectKind{ContextFabricSubjectTeam}
	if got := ComposeCoverageDetailLabel(narrowed); !strings.HasSuffix(got, "; some subjects were skipped") {
		t.Errorf("narrowing rider missing: %q", got)
	}
}

// TestEvidenceRefLabelClampsAtMaxLengthRef pins terra r2's P1 (EXECUTED):
// a contract-VALID evidence ref at the full 256-rune id bound must compose
// a label that still satisfies the 160-rune label bound and the exact-
// closure validator — an unclamped label failed the whole investigation
// (500) on a legal ref. Red on 37df311d (label 250 runes, validator
// rejects); green with the clamp.
func TestEvidenceRefLabelClampsAtMaxLengthRef(t *testing.T) {
	longRef := "acr:v1:team:" + strings.Repeat("x", ContextFabricEvidenceRefIDMaxLength-len("acr:v1:team:"))
	if got := len([]rune(longRef)); got != ContextFabricEvidenceRefIDMaxLength {
		t.Fatalf("fixture ref is %d runes, want the exact bound %d", got, ContextFabricEvidenceRefIDMaxLength)
	}
	for _, ref := range []string{longRef, "acr:v1:unregistered-segment:" + strings.Repeat("y", 240)} {
		label, _ := ContextFabricEvidenceRefLabel(ref)
		if got := len([]rune(label)); got > ContextFabricCoverageDetailLabelMaxLength {
			t.Fatalf("label for %d-rune ref is %d runes, exceeds the %d bound", len([]rune(ref)), got, ContextFabricCoverageDetailLabelMaxLength)
		}
		result := ContextFabricInvestigationResult{
			EvidenceRefIDs:    []string{ref},
			EvidenceRefLabels: map[string]string{ref: label},
		}
		if err := validateEvidenceRefLabels(result); err != nil {
			t.Fatalf("derived label for a contract-valid ref must validate, got: %v", err)
		}
	}
}

// CHAOS-7252: the five retired kinds (bare-':' joins of colon-capable ids)
// cannot be minted any more; each names an encoded ".v2" successor that
// can. Red before the retirement guard: EvidenceRefID minted them silently.
func TestEvidenceRefIDRefusesRetiredKinds(t *testing.T) {
	retired := 0
	for _, entityType := range ContextFabricEvidenceEntityTypeVocabulary() {
		successor, isRetired := RetiredEvidenceEntityType(entityType)
		if !isRetired {
			continue
		}
		retired++
		if !EncodedEvidenceEntityType(successor) || !validEvidenceEntityType(successor) {
			t.Errorf("%q names successor %q, which is not an encoded vocabulary member", entityType, successor)
		}
		func() {
			defer func() {
				message, _ := recover().(string)
				if !strings.Contains(message, string(entityType)) || !strings.Contains(message, string(successor)) {
					t.Errorf("EvidenceRefID(%q) did not refuse the retired kind naming its successor: %q", entityType, message)
				}
			}()
			_ = EvidenceRefID(entityType, "x")
		}()
		if EvidenceRefID(successor, "x") != ContextFabricEvidenceRefPrefix+string(successor)+":x" {
			t.Errorf("successor %q does not mint", successor)
		}
	}
	if retired != 5 {
		t.Fatalf("%d retired kinds, want 5", retired)
	}
}

// D5 (CHAOS-7252): an encoded (".v2") id labels as its unescaped segments,
// so a label reads as the ids did before the escaping. contracts/v1 carries
// its own two-line decode (it imports nothing internal); this pins it to the
// identity codec it mirrors over the codec's adversarial values, and pins
// that a non-encoded kind's id is shown verbatim.
func TestDecodedEvidenceIDMatchesTheIdentityCodec(t *testing.T) {
	values := []string{"", "a", ":", "%", "%3A", "%25", "%3a", "%%3A", "%253A", "a:b", "jira:A:B", "ari:cloud:identity::team/1", "é:ü"}
	for _, first := range values {
		for _, second := range values {
			id := identity.JoinSegments(first, second)
			if got, want := decodedEvidenceID(id), first+":"+second; got != want {
				t.Errorf("decodedEvidenceID(%q) = %q, want %q", id, got, want)
			}
		}
	}
	label, known := ContextFabricEvidenceRefLabel("acr:v1:work-item-dependency.v2:jira%3AA%3AB:jira%3AC:blocks%3Afwd")
	if !known || label != "Work item dependency: jira:A:B:jira:C:blocks:fwd" {
		t.Errorf("encoded label = (%q, %v)", label, known)
	}
	if label, _ := ContextFabricEvidenceRefLabel("acr:v1:work-item:repo:jira%3AA"); label != "Work item: repo:jira%3AA" {
		t.Errorf("a non-encoded kind's id was decoded: %q", label)
	}
}
