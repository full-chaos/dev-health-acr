package devhealthsource

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Unit proofs for the producer-side normalization pass (item_normalization.go).
//
// The end-to-end proofs run through the production builder and the fake row
// scanner (producer_bound_normalization_test.go); these pin the properties of
// the PASS, which no single call site can demonstrate: that it is the identity
// on anything already valid, that its two rune bounds are the validator's own,
// that its cap and trim compose in the order that actually satisfies the
// contract, and that it refuses to touch identity.

// padRune is deliberately NOT ASCII. Both bounds here are counted in RUNES
// (contracts/v1/validation_helpers.go uses utf8.RuneCountInString), so a
// fixture padded with ASCII cannot tell a rune bound from a byte bound: every
// count agrees when one byte is one rune. U+00E9 is two bytes, so a 513-rune
// label built from it is 1,026 bytes and a byte-counting implementation would
// refuse it hundreds of runes early -- which is exactly the mistake this
// padding exists to catch.
const padRune = "é"

func pad(n int) string { return strings.Repeat(padRune, n) }

func atTime(t time.Time) *time.Time { return &t }

var (
	normValidFrom = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	normValidTo   = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	normObserved  = time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	normScope     = contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}
)

func normEntity(label string, properties map[string]contractsv1.ContextFabricScalarValue, validFrom, validTo *time.Time) *contractsv1.ContextFabricEntityProjection {
	return &contractsv1.ContextFabricEntityProjection{
		Subject:        contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:S-1", Label: label},
		Properties:     properties,
		Authorization:  normScope,
		EvidenceRefIDs: []string{"acr:v1:norm:S-1"},
		ObservedAt:     normObserved, ValidFrom: validFrom, ValidTo: validTo,
		SourceVersion: ClickHouseSourceVersion,
	}
}

func normRelationship(fromLabel, toLabel string, properties map[string]contractsv1.ContextFabricScalarValue, validFrom, validTo *time.Time) *contractsv1.ContextFabricRelationshipProjection {
	return &contractsv1.ContextFabricRelationshipProjection{
		RelationshipID: "relationship:norm:S-1", Type: contractsv1.ContextFabricRelationshipBelongsToRepository,
		From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:S-1", Label: fromLabel},
		To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:R-1", Label: toLabel},
		Properties:      properties,
		Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
		Authorization:   normScope, EvidenceRefIDs: []string{"acr:v1:norm:S-1"},
		ObservedAt: normObserved, ValidFrom: validFrom, ValidTo: validTo,
		SourceVersion: ClickHouseSourceVersion,
	}
}

func stringProperty(value string) map[string]contractsv1.ContextFabricScalarValue {
	return map[string]contractsv1.ContextFabricScalarValue{"free_text": stringScalar(value)}
}

func runNormalize(candidates []candidate) []normalizationObservation {
	var observations []normalizationObservation
	normalizeCandidates(candidates, func(o normalizationObservation) { observations = append(observations, o) })
	return observations
}

func reasonCounts(observations []normalizationObservation) map[string]int {
	counts := map[string]int{}
	for _, o := range observations {
		counts[o.Reason]++
	}
	return counts
}

// TestNormalizationBoundsAreTheValidatorsOwn pins the two constants
// item_normalization.go caps against to the literals the contract's validators
// actually enforce.
//
// The bounds live in two places by necessity: the validators
// (validate_context_fabric_result.go, validate_context_fabric_projection.go)
// carry bare literals, and the constants that document them live in
// context_fabric_model_bounds.go. Normalization caps against the CONSTANTS. If
// a literal ever moves without its constant, this producer would cap to the
// wrong length and every repaired row would go straight back to quarantine --
// silently, because a capped-then-rejected row looks exactly like a row that
// was always too long.
//
// Probed at the boundary in BOTH directions, because a one-sided probe cannot
// tell "the bound is 512" from "the bound is anything >= 512".
func TestNormalizationBoundsAreTheValidatorsOwn(t *testing.T) {
	t.Parallel()
	checked := 0

	labelAt := func(n int) error {
		return contractsv1.ContextFabricSubjectRef{
			Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:S-1", Label: pad(n),
		}.Validate()
	}
	if err := labelAt(contractsv1.ContextFabricSubjectRefLabelMaxLength); err != nil {
		t.Fatalf("a label of exactly ContextFabricSubjectRefLabelMaxLength (%d) runes must validate, got %v -- normalization caps to this constant, so a smaller real bound means every capped label is still rejected",
			contractsv1.ContextFabricSubjectRefLabelMaxLength, err)
	}
	checked++
	err := labelAt(contractsv1.ContextFabricSubjectRefLabelMaxLength + 1)
	if err == nil {
		t.Fatalf("a label of %d runes must be rejected: the constant claims a bound the validator does not enforce, so normalization would cap rows that never needed it",
			contractsv1.ContextFabricSubjectRefLabelMaxLength+1)
	}
	// The REASON, not merely an error: a rejection raised by some unrelated
	// rule would satisfy `err != nil` and prove nothing about this bound.
	if !strings.Contains(err.Error(), "subject reference violates v1 bounds") {
		t.Fatalf("an oversize label must be refused by the subject-ref bound rule, got %q", err)
	}
	checked++

	scalarAt := func(n int) error { return stringScalar(pad(n)).Validate() }
	if err := scalarAt(contractsv1.ContextFabricClaimedFactValueMaxLength); err != nil {
		t.Fatalf("a scalar of exactly ContextFabricClaimedFactValueMaxLength (%d) runes must validate, got %v",
			contractsv1.ContextFabricClaimedFactValueMaxLength, err)
	}
	checked++
	err = scalarAt(contractsv1.ContextFabricClaimedFactValueMaxLength + 1)
	if err == nil {
		t.Fatalf("a scalar of %d runes must be rejected", contractsv1.ContextFabricClaimedFactValueMaxLength+1)
	}
	if !strings.Contains(err.Error(), "scalar string violates v1 bounds") {
		t.Fatalf("an oversize scalar must be refused by the scalar-string bound rule, got %q", err)
	}
	checked++

	// The bounds are RUNE bounds, and padRune proves it: a byte-counting
	// implementation would have refused the at-bound cases above, since
	// padRune is two bytes wide.
	if utf8.RuneLen([]rune(padRune)[0]) < 2 {
		t.Fatalf("padRune %q is single-byte, so every assertion above is blind to a rune/byte confusion", padRune)
	}
	checked++

	if checked != 5 {
		t.Fatalf("only %d of 5 boundary assertions ran", checked)
	}
}

// TestNormalizationIsIdentityOnItemsThatAlreadyValidate is the v6 proof.
//
// ClickHouseSourceVersion stays at v6, which asserts that no item this
// producer has ALREADY projected changes meaning. That claim is only as good
// as this test: every normalization must be the identity on an item the
// contract already accepts, because an item the contract already accepted is
// exactly an item that could have been projected before this change.
//
// Compared by MARSHALLED BYTES, not field by field. A field-by-field
// comparison covers the fields the author remembered; the bytes cover the
// struct, so a field added later that the pass starts touching fails here
// instead of silently re-writing history.
//
// The corpus sits AT the bounds on purpose -- a label of exactly the maximum,
// a scalar of exactly the maximum, a window whose bounds touch -- because the
// off-by-one that would make this pass rewrite a valid item lives at the
// boundary, not in the middle of the range.
func TestNormalizationIsIdentityOnItemsThatAlreadyValidate(t *testing.T) {
	t.Parallel()
	maxLabel := pad(contractsv1.ContextFabricSubjectRefLabelMaxLength)
	maxScalar := pad(contractsv1.ContextFabricClaimedFactValueMaxLength)
	touching := normValidFrom

	cases := []struct {
		name string
		c    candidate
	}{
		{"entity, ordinary", candidate{entity: normEntity("Fix the retry budget", stringProperty("a body"), atTime(normValidFrom), atTime(normValidTo))}},
		{"entity, label exactly at the bound", candidate{entity: normEntity(maxLabel, nil, atTime(normValidFrom), nil)}},
		{"entity, scalar exactly at the bound", candidate{entity: normEntity("ok", stringProperty(maxScalar), atTime(normValidFrom), nil)}},
		{"entity, window bounds touch (already degenerate)", candidate{entity: normEntity("ok", nil, atTime(normValidFrom), atTime(touching))}},
		{"entity, no window at all", candidate{entity: normEntity("ok", nil, nil, nil)}},
		{"entity, open-ended window", candidate{entity: normEntity("ok", nil, atTime(normValidFrom), nil)}},
		{"entity, non-string scalars", candidate{entity: normEntity("ok", map[string]contractsv1.ContextFabricScalarValue{
			"count": intScalar(7), "ratio": {Number: func() *float64 { v := 0.5; return &v }()},
			"flag": {Boolean: func() *bool { v := true; return &v }()}, "absent": {Null: true},
		}, atTime(normValidFrom), nil)}},
		{"relationship, ordinary", candidate{relationship: normRelationship("S-1", "acme/svc", stringProperty("x"), atTime(normValidFrom), atTime(normValidTo))}},
		{"relationship, both labels at the bound", candidate{relationship: normRelationship(maxLabel, maxLabel, nil, atTime(normValidFrom), nil)}},
		{"relationship, window bounds touch", candidate{relationship: normRelationship("S-1", "acme/svc", nil, atTime(normValidFrom), atTime(touching))}},
	}

	checked := 0
	for _, tc := range cases {
		// Precondition: this corpus is only evidence about ALREADY-PROJECTED
		// items if every member of it is one the contract accepts. A case that
		// silently stopped validating would turn this test into a tautology.
		if _, err := validateCandidateItem(tc.c); err != nil {
			t.Fatalf("%s: fixture does not validate (%v), so it says nothing about items that could already have projected", tc.name, err)
		}
		before, err := json.Marshal(tc.c.entity)
		if tc.c.relationship != nil {
			before, err = json.Marshal(tc.c.relationship)
		}
		if err != nil {
			t.Fatalf("%s: marshal before: %v", tc.name, err)
		}

		observations := runNormalize([]candidate{tc.c})

		after, err := json.Marshal(tc.c.entity)
		if tc.c.relationship != nil {
			after, err = json.Marshal(tc.c.relationship)
		}
		if err != nil {
			t.Fatalf("%s: marshal after: %v", tc.name, err)
		}
		if string(before) != string(after) {
			t.Fatalf("%s: normalization REWROTE an item the contract already accepts. ClickHouseSourceVersion stays v6 on the claim that no already-projected item changes meaning; this is that claim failing, and it is a rebuild decision, not a test to relax.\nbefore: %s\nafter:  %s",
				tc.name, before, after)
		}
		if len(observations) != 0 {
			t.Fatalf("%s: normalization reported %+v for an item it did not change -- a counter that fires on a no-op makes the operational signal meaningless", tc.name, observations)
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d identity cases reached the assertion", checked, len(cases))
	}
}

// TestNormalizationRepairsEachBoundAndCountsItOncePerItem walks the four
// tokens: each fires when its bound is breached, the item becomes projectable,
// and each is reported at most ONCE per item no matter how many fields on that
// item breached it.
func TestNormalizationRepairsEachBoundAndCountsItOncePerItem(t *testing.T) {
	t.Parallel()
	overLabel := pad(contractsv1.ContextFabricSubjectRefLabelMaxLength + 50)
	overScalar := pad(contractsv1.ContextFabricClaimedFactValueMaxLength + 50)

	cases := []struct {
		name string
		c    candidate
		want map[string]int
	}{
		{
			name: "untrimmed label",
			c:    candidate{entity: normEntity("  Fix the retry budget\t", nil, atTime(normValidFrom), nil)},
			want: map[string]int{normalizationLabelTrimmed: 1},
		},
		{
			name: "oversize label",
			c:    candidate{entity: normEntity(overLabel, nil, atTime(normValidFrom), nil)},
			want: map[string]int{normalizationLabelCapped: 1},
		},
		{
			name: "untrimmed AND oversize label reports both, each once",
			c:    candidate{entity: normEntity(" "+overLabel+" ", nil, atTime(normValidFrom), nil)},
			want: map[string]int{normalizationLabelTrimmed: 1, normalizationLabelCapped: 1},
		},
		{
			name: "oversize scalar",
			c:    candidate{entity: normEntity("ok", stringProperty(overScalar), atTime(normValidFrom), nil)},
			want: map[string]int{normalizationScalarCapped: 1},
		},
		{
			name: "THREE oversize scalars on one item still report scalar_capped once",
			c: candidate{entity: normEntity("ok", map[string]contractsv1.ContextFabricScalarValue{
				"a": stringScalar(overScalar), "b": stringScalar(overScalar), "c": stringScalar(overScalar),
			}, atTime(normValidFrom), nil)},
			want: map[string]int{normalizationScalarCapped: 1},
		},
		{
			name: "inverted window",
			c:    candidate{entity: normEntity("ok", nil, atTime(normValidTo), atTime(normValidFrom))},
			want: map[string]int{normalizationWindowCollapsed: 1},
		},
		{
			name: "relationship: BOTH endpoint labels oversize reports label_capped once",
			c:    candidate{relationship: normRelationship(overLabel, overLabel, nil, atTime(normValidFrom), nil)},
			want: map[string]int{normalizationLabelCapped: 1},
		},
		{
			name: "relationship: inverted window",
			c:    candidate{relationship: normRelationship("S-1", "acme/svc", nil, atTime(normValidTo), atTime(normValidFrom))},
			want: map[string]int{normalizationWindowCollapsed: 1},
		},
		{
			name: "every bound at once on one item",
			c:    candidate{entity: normEntity(" "+overLabel+" ", stringProperty(overScalar), atTime(normValidTo), atTime(normValidFrom))},
			want: map[string]int{
				normalizationLabelTrimmed: 1, normalizationLabelCapped: 1,
				normalizationScalarCapped: 1, normalizationWindowCollapsed: 1,
			},
		},
	}

	checked := 0
	for _, tc := range cases {
		// Red by construction: the fixture must be one the contract REFUSES,
		// or "it projects afterwards" proves nothing.
		if _, err := validateCandidateItem(tc.c); err == nil {
			t.Fatalf("%s: fixture already validates, so this case cannot show normalization keeping a row", tc.name)
		}
		got := reasonCounts(runNormalize([]candidate{tc.c}))
		if len(got) != len(tc.want) {
			t.Fatalf("%s: reasons = %v, want %v", tc.name, got, tc.want)
		}
		for reason, count := range tc.want {
			if got[reason] != count {
				t.Fatalf("%s: %s = %d, want %d (full: %v)", tc.name, reason, got[reason], count, got)
			}
		}
		if _, err := validateCandidateItem(tc.c); err != nil {
			t.Fatalf("%s: the item is STILL unprojectable after normalization (%v) -- the whole point is that the row is kept", tc.name, err)
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d repair cases reached the assertion", checked, len(cases))
	}
}

// TestNormalizedLabelIsTrimmedAfterCapping pins the order inside the label
// normalizer: trim, cap, then trim the tail the cap exposed.
//
// Cutting a label at the bound can land in the middle of whitespace, and the
// contract refuses an untrimmed label exactly as hard as an oversize one -- so
// a cap-then-stop trades one violation for another and the row is quarantined
// anyway, under a DIFFERENT token, which is the shape that wastes an
// operator's afternoon. The label built here has a space at exactly the cut
// position, so an implementation that skips the closing trim fails.
func TestNormalizedLabelIsTrimmedAfterCapping(t *testing.T) {
	t.Parallel()
	bound := contractsv1.ContextFabricSubjectRefLabelMaxLength
	// ... pad ..., then whitespace sitting exactly across the cut, then more.
	label := pad(bound-2) + "  " + pad(40)
	c := candidate{entity: normEntity(label, nil, atTime(normValidFrom), nil)}
	if _, err := validateCandidateItem(c); err == nil {
		t.Fatal("fixture already validates; it cannot demonstrate the cap")
	}

	runNormalize([]candidate{c})

	got := c.entity.Subject.Label
	if strings.TrimSpace(got) != got {
		t.Fatalf("the capped label is untrimmed (%q...%q) -- the cap exposed trailing whitespace and the contract refuses it under a different rule than the one just repaired",
			got[:8], got[len(got)-8:])
	}
	if n := utf8.RuneCountInString(got); n > bound {
		t.Fatalf("capped label = %d runes, want <= %d", n, bound)
	}
	if got == "" {
		t.Fatal("the capped label is empty: the contract's 1-rune minimum is breached and the row is lost to a bound the repair itself created")
	}
	if err := c.entity.Validate(); err != nil {
		t.Fatalf("the normalized entity still fails the contract: %v", err)
	}

	// The opposite extreme: a label whose only non-whitespace content is at
	// the very front. Trimming FIRST is what guarantees the cap cannot empty
	// it -- rune 0 of a trimmed non-empty string is not whitespace.
	front := candidate{entity: normEntity("  x"+strings.Repeat(" ", bound+100), nil, atTime(normValidFrom), nil)}
	runNormalize([]candidate{front})
	if front.entity.Subject.Label != "x" {
		t.Fatalf("label = %q, want %q", front.entity.Subject.Label, "x")
	}
	if err := front.entity.Validate(); err != nil {
		t.Fatalf("a label that is one rune of content and a great deal of whitespace must normalize to that rune: %v", err)
	}
}

// TestNormalizationNeverTouchesCanonicalID pins the ONE bound in this family
// the pass deliberately refuses to repair.
//
// SubjectRef.Validate rejects an untrimmed canonical id under the same rule as
// an untrimmed label, so repairing it would be a one-line extension and would
// look like an oversight to the next reader. It is not. A canonical id is
// IDENTITY: trimming it re-points the node, and every consumer that already
// stored the old id now has an orphan. That is a rebuild decision, taken by an
// operator, not a repair this producer may make on its own -- which is also
// why ClickHouseSourceVersion can stay at v6.
//
// So such a row still quarantines, and this test is what stops that from being
// quietly "fixed" later.
func TestNormalizationNeverTouchesCanonicalID(t *testing.T) {
	t.Parallel()
	entity := normEntity("Fix the retry budget", nil, atTime(normValidFrom), nil)
	entity.Subject.CanonicalID = "work_item:S-1 " // untrimmed IDENTITY
	c := candidate{entity: entity}

	observations := runNormalize([]candidate{c})

	if entity.Subject.CanonicalID != "work_item:S-1 " {
		t.Fatalf("the canonical id was normalized to %q -- identity must never be rewritten by a producer repair", entity.Subject.CanonicalID)
	}
	if len(observations) != 0 {
		t.Fatalf("normalization reported %+v for an item it must not touch", observations)
	}
	kind, err := validateCandidateItem(c)
	if err == nil {
		t.Fatal("an untrimmed canonical id must still be refused by the contract")
	}
	if reason := quarantineReason(c, err); reason != quarantineUntrimmedLabel {
		t.Fatalf("quarantine reason = %q, want %q (kind %q)", reason, quarantineUntrimmedLabel, kind)
	}
}

// TestNormalizationCollapseAgreesWithEdgeValidity keeps the single-row rule and
// the edge rule from drifting apart.
//
// edgeValidity (validity.go, CHAOS-3825) already collapses an empty
// intersection to [validFrom, validFrom) at the five EDGE sites; this pass
// applies the same rule at the single-row sites that never had it. Two
// spellings of one rule is how they diverge, so the outputs are compared
// directly rather than each being asserted against its own hand-written
// expectation.
//
// Also pins the ALIASING guard edgeValidity's own comment calls out: the
// collapsed end must be a COPY, never the valid_from pointer itself, because
// callers hold that pointer as an endpoint's window and pass the same pair to
// belongsToRepository.
func TestNormalizationCollapseAgreesWithEdgeValidity(t *testing.T) {
	t.Parallel()
	from, to := normValidTo, normValidFrom // inverted: end precedes start
	c := candidate{entity: normEntity("ok", nil, &from, &to)}

	runNormalize([]candidate{c})

	wantFrom, wantTo := edgeValidity(&from, &to, nil, nil)
	if !c.entity.ValidFrom.Equal(*wantFrom) || !c.entity.ValidTo.Equal(*wantTo) {
		t.Fatalf("single-row collapse [%s, %s) disagrees with edgeValidity's [%s, %s) for the same inverted pair",
			c.entity.ValidFrom, c.entity.ValidTo, wantFrom, wantTo)
	}
	if c.entity.ValidFrom == c.entity.ValidTo {
		t.Fatal("the collapsed end ALIASES valid_from: a later adjustment to one bound would silently move the other")
	}
	if err := c.entity.Validate(); err != nil {
		t.Fatalf("the collapsed window must satisfy the contract: %v", err)
	}

	// A window that merely TOUCHES is already the representation this
	// produces, and reporting it would inflate the counter with no-ops.
	touching := normValidFrom
	quiet := candidate{entity: normEntity("ok", nil, atTime(normValidFrom), &touching)}
	if obs := runNormalize([]candidate{quiet}); len(obs) != 0 {
		t.Fatalf("an already zero-width window was reported as collapsed: %+v", obs)
	}
}

// TestNormalizationLeavesEpisodesAndTombstonesAlone pins the scope exclusion.
//
// A tombstone carries no label, no properties and no window -- only an
// EffectiveAt, whose bound this producer must not invent a value for. Episodes
// never reach this pass at all (EpisodesProjectionSource calls buildBatch
// directly), and widening it to a source whose drops are still uncounted is
// the trade item_quarantine.go already refused there.
func TestNormalizationLeavesEpisodesAndTombstonesAlone(t *testing.T) {
	t.Parallel()
	tombstone := &contractsv1.ContextFabricProjectionTombstone{
		Kind: "incident", CanonicalID: "incident:I-1 ", Reason: "source_deleted",
		EffectiveAt: normObserved, SourceVersion: ClickHouseSourceVersion,
	}
	before, err := json.Marshal(tombstone)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	observations := runNormalize([]candidate{{tombstone: tombstone}, {sortKey: "progress-only"}})
	after, err := json.Marshal(tombstone)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("the tombstone was rewritten:\nbefore %s\nafter  %s", before, after)
	}
	if len(observations) != 0 {
		t.Fatalf("normalization reported %+v for a tombstone and a progress marker, neither of which it may touch", observations)
	}
}

// TestNormalizationTokensAreDisjointFromQuarantineTokens is the vocabulary
// fence.
//
// The two counter families answer opposite operational questions -- "how many
// rows did this producer KEEP by repairing them" and "how many rows did it
// LOSE" -- and an operator alerting on one must never be served a value from
// the other. A token appearing in both sets would merge them silently, and the
// merge would only surface as a dashboard that has quietly stopped meaning
// anything.
func TestNormalizationTokensAreDisjointFromQuarantineTokens(t *testing.T) {
	t.Parallel()
	quarantine := []string{
		quarantineUnknownRelationshipType, quarantineUntrimmedLabel, quarantineInvertedWindow,
		quarantineOversizeScalar, quarantineUnrepresentableInstant, quarantineContractBoundViolation,
		quarantineOrphanedDependent, quarantineDuplicateWithinBatch, quarantineEndpointEntityQuarantined,
	}
	normalization := []string{
		normalizationLabelTrimmed, normalizationLabelCapped,
		normalizationScalarCapped, normalizationWindowCollapsed,
	}
	seen := map[string]bool{}
	for _, token := range quarantine {
		seen[token] = true
	}
	checked := 0
	for _, token := range normalization {
		if seen[token] {
			t.Fatalf("normalization token %q is also a quarantine token: a kept row and a lost row would report the same value", token)
		}
		checked++
	}
	if checked != len(normalization) || len(normalization) != 4 {
		t.Fatalf("checked %d of %d normalization tokens (expected the 4 this change defines)", checked, len(normalization))
	}
}
