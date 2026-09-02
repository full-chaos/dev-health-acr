package devhealthsource

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Property test for the candidate pipeline this change set introduced:
// per-item quarantine -> orphaned-dependent sweep -> batch-identity dedup.
//
// WHY THIS EXISTS RATHER THAN MORE FIXTURES. Those three passes produced FIVE
// real defects across three adversarial rounds and the lane's own review:
// orphan stub entities, an all-quarantined tail that stalled the cursor,
// duplicate relationship ids, entity-subject uniqueness missed by a
// per-instance sweep, and a pass-ORDERING interaction that lost the valid stub
// of a shared target. Every one was found by constructing the single input
// that broke it, and every fix was verified by a fixture aimed at that same
// input. That is the shape that keeps producing defect number six: a
// hand-built primitive re-derived one case at a time.
//
// So the passes are pinned here by their INVARIANTS over GENERATED inputs
// instead. The assertions run after the WHOLE pipeline and are indifferent to
// pass order, which is exactly the property the fifth defect violated -- an
// ordering bug cannot hide behind an assertion that only inspects one pass.
//
// A failure prints its seed; re-running with that seed reproduces the case.
const pipelineInvariantCases = 400

type generatedCase struct {
	candidates []candidate
	// dependents maps a stub entity's canonical id to the relationship id it
	// exists to serve, so the test can check the sweep without reading the
	// production code's own bookkeeping.
	dependents map[string]string
}

func generateCandidates(rng *rand.Rand) generatedCase {
	observed := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	valid := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC) // past Go's epoch-nanosecond range
	scope := contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}

	types := []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBlocks,
		contractsv1.ContextFabricRelationshipRelatesTo,
		contractsv1.ContextFabricRelationshipDuplicates,
		"EXTERNAL_ISSUE_KEY", // outside the vocabulary
		"NOT_A_REAL_TYPE",    // outside the vocabulary
	}

	// Resolution is decided PER TARGET, not per row, because that is what the
	// producer does: whether a target resolves is a property of the target
	// work item, read through the same LEFT JOIN for every row naming it. An
	// earlier generator chose per row, which let one batch both ASSERT a
	// ref-form edge and emit the healing tombstone that retracts it -- a
	// combination the producer cannot emit, and one the contract deliberately
	// rejects rather than silently resolving (see
	// validateProjectionRelationshipTombstoneCollision's own comment: a loud
	// wedge is not the worst outcome available). Generating impossible inputs
	// there would have argued for a guard the contract authors explicitly
	// refused.
	resolved := map[string]bool{}
	for i := 0; i < 3; i++ {
		resolved[fmt.Sprintf("T-%d", i)] = rng.Intn(2) == 0
	}

	out := generatedCase{dependents: map[string]string{}}
	n := 3 + rng.Intn(8)
	for i := 0; i < n; i++ {
		at := observed.Add(time.Duration(rng.Intn(5)) * time.Second)
		// Deliberately a SMALL id space so pairs, spellings and targets
		// collide often -- a fixture whose identifiers are all distinct hides
		// every aliasing defect, which is how three of the five known defects
		// were missed.
		target := fmt.Sprintf("T-%d", rng.Intn(3))
		source := fmt.Sprintf("S-%d", rng.Intn(3))
		relType := types[rng.Intn(len(types))]
		sortKey := source + ":" + target

		label := "item " + target
		if rng.Intn(6) == 0 {
			label += " " // untrimmed: normalization trims it
		}
		if rng.Intn(9) == 0 {
			// Oversize: normalization caps it. Distinct from untrimmed on
			// purpose -- they are different tokens and one can hide the other
			// if the generator only ever produces the pair together.
			label = strings.Repeat("l", contractsv1.ContextFabricSubjectRefLabelMaxLength+30)
		}
		// The ref-form producer builds the stub entity and its edge from the
		// SAME refSubject, observedAt, window, evidence and authorization
		// (tables.go's ref-form branch), so they cannot fail validation
		// independently. TestRefStubAndItsEdgeShareAValidationFate pins that
		// coupling, so if a future field breaks it this assumption fails
		// loudly instead of silently going stale.
		observedForPair := at
		if rng.Intn(7) == 0 {
			observedForPair = beyondGo // unrepresentable instant, for BOTH
		}
		validFrom := valid
		// The producer hands the ref stub and its edge the SAME window, so an
		// inverted pair inverts BOTH -- generating it on one side only would
		// be an input this producer cannot emit.
		var pairValidTo *time.Time
		if rng.Intn(8) == 0 {
			inverted := valid.Add(-24 * time.Hour) // end BEFORE start
			pairValidTo = &inverted
		}
		// The third normalizable bound, an oversize free-text property, is
		// generated on the CANONICAL work_items entity below rather than here.
		//
		// A first version put it on the ref stub and seed 32 immediately
		// failed invariant (a): the stub carried a property its edge did not,
		// so the two stopped sharing a validation fate, and a batch appeared
		// in which an edge outlived the endpoint entity nothing else supplied.
		// The PRODUCER CANNOT EMIT THAT. tables.go's ref-form branch builds
		// the stub and its edge from one refSubject, one window, one evidence
		// ref and no properties at all -- the coupling
		// TestRefStubAndItsEdgeShareAValidationFate exists to pin. So the
		// GENERATOR was wrong, not the production sweep; this is the third
		// time on this branch that a generated input has argued for a guard
		// against something that cannot happen.

		refID := "work_item_ref:" + target
		refRelID := "relationship.v2:gen:ref:" + source + ":" + target + ":" + string(relType)
		resolvedRelID := "relationship.v2:gen:res:" + source + ":" + target + ":" + string(relType)

		refSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: refID, Label: label}
		fromSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + source, Label: source}

		newEdge := func(id string, to contractsv1.ContextFabricSubjectRef) *contractsv1.ContextFabricRelationshipProjection {
			return &contractsv1.ContextFabricRelationshipProjection{
				RelationshipID: id, Type: relType, From: fromSubject, To: to,
				Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
				EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
				Authorization:   scope, EvidenceRefIDs: []string{"acr:v1:gen:" + source + ":" + target},
				ObservedAt: observedForPair, ValidFrom: &validFrom, ValidTo: pairValidTo, SourceVersion: "v1",
			}
		}

		if !resolved[target] {
			// Ref-form: a stub entity plus the edge it exists to serve.
			stub := contractsv1.ContextFabricEntityProjection{
				Subject: refSubject, Authorization: scope,
				EvidenceRefIDs: []string{"acr:v1:gen:" + source + ":" + target},
				ObservedAt:     observedForPair, ValidFrom: &validFrom, ValidTo: pairValidTo, SourceVersion: "v1",
			}
			out.candidates = append(out.candidates,
				candidate{observedAt: at, sortKey: sortKey, entity: &stub, supports: refRelID},
				candidate{observedAt: at, sortKey: sortKey, relationship: newEdge(refRelID, refSubject)})
			out.dependents[refID] = refRelID
			continue
		}

		// The work_items table's OWN entity for the source, whose label comes
		// from the row title and so can breach a bound the dependency edge
		// does not -- that edge labels its endpoints by work item ID. This is
		// the CROSS-TABLE shape: entity and edge from different producers,
		// failing independently, which no `supports` link can express.
		canonicalLabel := "title " + source
		if rng.Intn(6) == 0 {
			canonicalLabel += " " // untrimmed: normalization trims it
		}
		if rng.Intn(9) == 0 {
			canonicalLabel = strings.Repeat("c", contractsv1.ContextFabricSubjectRefLabelMaxLength+30)
		}
		canonicalValidFrom := valid
		// The work_items entity is where an oversize free-text property really
		// lives (type, native_team_key, project_name, status ...), and it is
		// the CROSS-TABLE shape: this entity can fail while the dependency
		// edge naming the same work item stays valid, because that edge labels
		// its endpoints by id. No coupling to break.
		var canonicalProperties map[string]contractsv1.ContextFabricScalarValue
		if rng.Intn(9) == 0 {
			canonicalProperties = map[string]contractsv1.ContextFabricScalarValue{
				"free_text": stringScalar(strings.Repeat("s", contractsv1.ContextFabricClaimedFactValueMaxLength+30)),
			}
		}
		var canonicalValidTo *time.Time
		if rng.Intn(8) == 0 {
			inverted := valid.Add(-24 * time.Hour) // end BEFORE start
			canonicalValidTo = &inverted
		}
		canonical := contractsv1.ContextFabricEntityProjection{
			Subject:       contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + source, Label: canonicalLabel},
			Authorization: scope, Properties: canonicalProperties,
			EvidenceRefIDs: []string{"acr:v1:gen:wi:" + source},
			ObservedAt:     at, ValidFrom: &canonicalValidFrom, ValidTo: canonicalValidTo, SourceVersion: "v1",
		}
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: "wi:" + source, entity: &canonical})

		// The OTHER cross-table endpoint kind, and the most common one in
		// production: every belongsToRepository edge points at a Repository
		// entity supplied by the `repos` table, whose label is the repo slug
		// and so varies independently of any edge naming it. Round 4's finding
		// was the work_item form of exactly this shape; generating only that
		// form would leave the repository form unasserted for the same reason
		// the ref-stub-only invariant left the work_item form unasserted.
		repoSlug := fmt.Sprintf("acme/repo-%d", rng.Intn(2))
		repoLabel := repoSlug
		if rng.Intn(6) == 0 {
			repoLabel += " " // untrimmed: normalization trims it
		}
		if rng.Intn(9) == 0 {
			repoLabel = strings.Repeat("r", contractsv1.ContextFabricSubjectRefLabelMaxLength+30)
		}
		repoValidFrom := valid
		repoEntity := contractsv1.ContextFabricEntityProjection{
			Subject:        contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + repoSlug, Label: repoLabel},
			Authorization:  scope,
			EvidenceRefIDs: []string{"acr:v1:gen:repo:" + repoSlug},
			ObservedAt:     at, ValidFrom: &repoValidFrom, SourceVersion: "v1",
		}
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: "repo:" + repoSlug, entity: &repoEntity})
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: "wi:" + source,
			relationship: &contractsv1.ContextFabricRelationshipProjection{
				RelationshipID:  "relationship.v2:gen:btr:" + source + ":" + repoSlug,
				Type:            contractsv1.ContextFabricRelationshipBelongsToRepository,
				From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + source, Label: source},
				To:              contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + repoSlug, Label: repoSlug},
				Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
				EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
				Authorization:   scope, EvidenceRefIDs: []string{"acr:v1:gen:btr:" + source},
				ObservedAt: at, ValidFrom: &repoValidFrom, SourceVersion: "v1",
			}})

		// Resolved: the real edge, plus the healing tombstones that retire the
		// ref-form ids this row WOULD have minted. The asserted id and the
		// tombstoned id are different by construction, exactly as in tables.go.
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: sortKey,
			relationship: newEdge(resolvedRelID, contractsv1.ContextFabricSubjectRef{
				Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:" + target, Label: label})})
		effective := at
		if rng.Intn(8) == 0 {
			effective = beyondGo
		}
		out.candidates = append(out.candidates, candidate{observedAt: at, sortKey: sortKey,
			tombstone: &contractsv1.ContextFabricProjectionTombstone{
				Kind: "relationship", CanonicalID: refRelID,
				Reason: "healing the ref-form edge", EffectiveAt: effective, SourceVersion: "v1",
			}})
	}
	sortCandidates(out.candidates)
	return out
}

func TestPipelineInvariantsOverGeneratedCandidates(t *testing.T) {
	t.Parallel()
	for i := 0; i < pipelineInvariantCases; i++ {
		seed := int64(i)
		rng := rand.New(rand.NewSource(seed))
		gen := generateCandidates(rng)

		var observations []quarantineObservation
		kept := partitionProjectableCandidates(gen.candidates, func(o quarantineObservation) {
			observations = append(observations, o)
		})

		fail := func(format string, args ...any) {
			t.Fatalf("SEED=%d: "+format, append([]any{seed}, args...)...)
		}

		// (d) every dropped item is observed exactly once, with a reason from
		// the CLOSED vocabulary. Counting rather than inspecting is what makes
		// a silent drop impossible to miss.
		if want := len(gen.candidates) - len(kept); len(observations) != want {
			fail("observations = %d, want %d (one per dropped item, never silent)", len(observations), want)
		}
		closed := map[string]bool{
			quarantineUnknownRelationshipType: true, quarantineUntrimmedLabel: true,
			quarantineInvertedWindow: true, quarantineOversizeScalar: true,
			quarantineUnrepresentableInstant: true, quarantineContractBoundViolation: true,
			quarantineOrphanedDependent: true, quarantineDuplicateWithinBatch: true,
			quarantineEndpointEntityQuarantined: true,
		}
		for _, o := range observations {
			if !closed[o.Reason] {
				fail("quarantine reason %q is outside the closed vocabulary", o.Reason)
			}
			if o.Kind == "" {
				fail("observation with reason %q carries no item kind", o.Reason)
			}
		}

		keptRelIDs := map[string]struct{}{}
		keptEntityIDs := map[string]struct{}{}
		keptEntityKeys := map[string]struct{}{}
		for _, c := range kept {
			switch {
			case c.relationship != nil:
				keptRelIDs[c.relationship.RelationshipID] = struct{}{}
			case c.entity != nil:
				keptEntityIDs[c.entity.Subject.CanonicalID] = struct{}{}
				keptEntityKeys[subjectIdentityKey(c.entity.Subject)] = struct{}{}
			}
		}

		// (b) nothing survives that exists only to support something dropped.
		for _, c := range kept {
			if c.supports == "" {
				continue
			}
			if _, ok := keptRelIDs[c.supports]; !ok {
				fail("a candidate supporting dropped relationship %q survived", c.supports)
			}
		}

		// (a) no surviving relationship may name an endpoint whose
		// AUTHORITATIVE entity this same consumed set refused -- otherwise the
		// backend mints an implicit stub with NO validity window, admitted at
		// every requested time, and the cursor advances past the rejection so
		// the corruption is durable and silent.
		//
		// Stated over EVERY endpoint of EVERY surviving relationship, not just
		// ref-stub `To` endpoints. The narrower earlier form missed the
		// cross-table case where a work_items entity and a
		// work_item_dependencies edge fail independently, which is exactly the
		// gap a review found: an invariant scoped to one shape proves nothing
		// about the population.
		quarantinedSubjects := map[string]struct{}{}
		for _, c := range gen.candidates {
			if c.entity == nil {
				continue
			}
			if _, err := validateCandidateItem(c); err != nil {
				quarantinedSubjects[subjectIdentityKey(c.entity.Subject)] = struct{}{}
			}
		}
		for _, c := range kept {
			if c.relationship == nil {
				continue
			}
			for _, endpoint := range []contractsv1.ContextFabricSubjectRef{c.relationship.From, c.relationship.To} {
				key := subjectIdentityKey(endpoint)
				if _, refused := quarantinedSubjects[key]; !refused {
					continue
				}
				// A refused entity is only a problem when NOTHING supplies
				// that subject: two rows can mint the same subject and one
				// survive, in which case the endpoint is genuinely backed.
				// Stating it as "was refused" rather than "is unsupplied"
				// would forbid a correct batch -- the same dropped-is-not-
				// no-survivor confusion the production passes had to unlearn.
				if _, supplied := keptEntityKeys[key]; supplied {
					continue
				}
				fail("relationship %q survived while the entity for its endpoint %q was refused and nothing else supplied it -- the backend would mint an unbounded implicit stub",
					c.relationship.RelationshipID, endpoint.CanonicalID)
			}
			// A ref-stub endpoint must additionally be PRESENT, since nothing
			// else in the batch can supply it.
			if c.relationship.To.Kind == contractsv1.ContextFabricSubjectWorkItemRef {
				if _, ok := keptEntityIDs[c.relationship.To.CanonicalID]; !ok {
					fail("relationship %q survived but its ref-stub endpoint %q was not projected",
						c.relationship.RelationshipID, c.relationship.To.CanonicalID)
				}
			}
		}

		if !carriesPayload(kept) {
			continue // nothing publishable; buildBatch is not reached
		}

		// (c) all four batch-level uniqueness rules hold, checked by the
		// contract's OWN validator rather than by restating them here.
		batch, err := buildBatch("org-1", SourceName, ClickHouseSourceVersion, "", gen.candidates, kept, false, false, time.Unix(1, 0).UTC())
		if err != nil {
			fail("buildBatch rejected a fully-swept candidate set: %v", err)
		}
		if err := batch.Validate(); err != nil {
			fail("built batch fails contract validation: %v", err)
		}
		// The cursor comes from every candidate CONSUMED, never the survivors.
		last := gen.candidates[len(gen.candidates)-1]
		wantCursor, encErr := encodeCursor(cursorState{Since: last.observedAt, After: last.sortKey})
		if encErr != nil {
			fail("encode cursor: %v", encErr)
		}
		if batch.NextCursor != wantCursor {
			fail("NextCursor was derived from the survivors, not from every consumed candidate")
		}
	}
}

// TestNormalizationInvariantFOverGeneratedCandidates is invariant (f), the one
// this change adds to the five the quarantine passes already carry:
//
//	a row whose ONLY defect is one of the three normalizable bounds is
//	PROJECTED, never dropped.
//
// Stated over the same generated candidate sets as the invariants above, and
// for the same reason: the three bounds interact with three existing passes
// (endpoint sweep, orphan sweep, identity dedup), and an interaction is
// exactly what a hand-built fixture aimed at one bound cannot see. The first
// version of the generator extension proved the point immediately by
// producing an input the producer cannot emit -- see the note beside
// canonicalProperties.
//
// Checked DIFFERENTIALLY: the same seed is generated twice, run once through
// the pipeline as it was before this change and once as production now runs
// it. Two separate generations because normalizeCandidates mutates in place,
// and one copy cannot be both the control and the subject.
func TestNormalizationInvariantFOverGeneratedCandidates(t *testing.T) {
	t.Parallel()
	// The tokens that mean "this row was LOST to a bound". None may appear in
	// a run that normalizes first; that is invariant (f) in counter form.
	normalized := map[string]bool{
		quarantineUntrimmedLabel: true,
		quarantineOversizeScalar: true,
		quarantineInvertedWindow: true,
	}
	// Everything a normalized run may still legitimately report. A row can
	// still be dropped -- for a bound this pass does not own (an
	// unrepresentable instant, an unknown relationship type) or by a
	// BATCH-level pass -- and those drops are not this change's business.
	permitted := map[string]bool{
		quarantineUnknownRelationshipType:   true,
		quarantineUnrepresentableInstant:    true,
		quarantineContractBoundViolation:    true,
		quarantineOrphanedDependent:         true,
		quarantineDuplicateWithinBatch:      true,
		quarantineEndpointEntityQuarantined: true,
	}

	repairedTotal, keptDelta := 0, 0
	for i := 0; i < pipelineInvariantCases; i++ {
		seed := int64(i)
		control := generateCandidates(rand.New(rand.NewSource(seed)))
		subject := generateCandidates(rand.New(rand.NewSource(seed)))
		fail := func(format string, args ...any) {
			t.Fatalf("SEED=%d: "+format, append([]any{seed}, args...)...)
		}
		if len(control.candidates) != len(subject.candidates) {
			fail("the generator is not deterministic for one seed (%d vs %d), so the differential below compares two different inputs",
				len(control.candidates), len(subject.candidates))
		}

		// How many items were refused ONLY for a bound this pass repairs:
		// invalid before, valid after. Computed from the inputs themselves,
		// never by asking the pass under test what it thinks it fixed.
		repairedHere := 0
		invalidBefore := make([]bool, len(control.candidates))
		for index, c := range control.candidates {
			_, err := validateCandidateItem(c)
			invalidBefore[index] = err != nil
		}
		normalizeCandidates(subject.candidates, nil)
		for index, c := range subject.candidates {
			if _, err := validateCandidateItem(c); invalidBefore[index] && err == nil {
				repairedHere++
			}
		}
		repairedTotal += repairedHere

		controlKept := partitionProjectableCandidates(control.candidates, nil)
		var observations []quarantineObservation
		subjectKept := partitionProjectableCandidates(subject.candidates, func(o quarantineObservation) {
			observations = append(observations, o)
		})

		// (f.1) NO drop is attributed to one of the three bounds any more.
		for _, o := range observations {
			if normalized[o.Reason] {
				fail("a row was still quarantined as %q after normalization: the bound is supposed to cost no rows at all", o.Reason)
			}
			if !permitted[o.Reason] {
				fail("quarantine reason %q is outside the vocabulary a normalized run may report", o.Reason)
			}
		}

		// (f.2) MONOTONICITY, stated over IDENTITIES SUPPLIED rather than over
		// individual candidates.
		//
		// DROPPED IS NOT NO SURVIVOR, in its mirror form -- the fourth time
		// this branch has had to state that rule. A stricter pointer-level
		// version of this invariant ("no candidate that survived may now be
		// dropped") fails on seed 3, and correctly: candidate 14 is a
		// work_item_ref:T-1 stub, and its untrimmed twin used to be
		// quarantined. Normalization makes BOTH twins valid, so they collide,
		// and dropDuplicateIdentities drops the second under the documented
		// first-occurrence-wins rule -- while the identity stays supplied by
		// the survivor and the total kept RISES (8 -> 12 on that seed). No
		// node or edge is lost; one duplicate's evidence ref is, which is the
		// cost that pass already documents and counts.
		//
		// So the property worth pinning is that the BATCH still supplies every
		// identity it supplied before. That is what a consumer of the graph
		// can observe, and it is what a repair must never take away.
		suppliedBefore := suppliedIdentities(controlKept)
		suppliedAfter := suppliedIdentities(subjectKept)
		for key := range suppliedBefore {
			if !suppliedAfter[key] {
				fail("identity %q was supplied by the pipeline WITHOUT normalization and is unsupplied WITH it -- a repair must never cost the graph a node or an edge", key)
			}
		}
		keptDelta += len(subjectKept) - len(controlKept)
	}

	// Non-vacuity, both halves. Without repairs there is nothing to be
	// invariant about, and without a kept-count delta the repairs could all
	// have been on rows some other pass dropped anyway.
	if repairedTotal == 0 {
		t.Fatalf("the generator produced ZERO rows whose only defect is a normalizable bound across %d cases: invariant (f) never ran", pipelineInvariantCases)
	}
	if keptDelta <= 0 {
		t.Fatalf("normalization kept no additional rows across %d cases (delta %d): the repairs are not reaching the batch", pipelineInvariantCases, keptDelta)
	}
	t.Logf("invariant (f) reach: %d rows repaired, %d more rows kept than without normalization, over %d cases", repairedTotal, keptDelta, pipelineInvariantCases)
}

// suppliedIdentities is the set of node and edge identities a kept candidate
// set actually puts into the graph. Keyed the way the backend keys them -- an
// entity by kind + canonical id, a relationship by its relationship id -- so
// two candidates that mint one identity count once, which is the whole point
// at the call site.
func suppliedIdentities(kept []candidate) map[string]bool {
	supplied := map[string]bool{}
	for _, c := range kept {
		switch {
		case c.entity != nil:
			supplied["entity\x00"+subjectIdentityKey(c.entity.Subject)] = true
		case c.relationship != nil:
			supplied["relationship\x00"+c.relationship.RelationshipID] = true
		case c.episode != nil:
			supplied["episode\x00"+c.episode.EpisodeID] = true
		case c.tombstone != nil:
			supplied["tombstone\x00"+string(c.tombstone.Kind)+"\x00"+c.tombstone.CanonicalID] = true
		}
	}
	return supplied
}

// TestPipelineInvariantsReachTheirAssertions guards the property test itself:
// a generator that stopped producing quarantinable or colliding inputs would
// leave every assertion above trivially satisfied and the suite would stay
// green while proving nothing. Same family as the vacuous-assertion trap that
// already bit this lane once.
func TestPipelineInvariantsReachTheirAssertions(t *testing.T) {
	t.Parallel()
	var dropped, duplicates, orphans, stubs, published, endpointDrops int
	var untrimmedLabels, oversizeLabels, oversizeScalars, invertedWindows int
	for i := 0; i < pipelineInvariantCases; i++ {
		gen := generateCandidates(rand.New(rand.NewSource(int64(i))))
		// The three normalizable shapes, counted on the RAW generated input
		// rather than on what any pass reports about it -- an expectation must
		// never be computed by the thing it checks.
		for _, c := range gen.candidates {
			for _, s := range subjectsOf(c) {
				if strings.TrimSpace(s.Label) != s.Label {
					untrimmedLabels++
				}
				if utf8.RuneCountInString(s.Label) > contractsv1.ContextFabricSubjectRefLabelMaxLength {
					oversizeLabels++
				}
			}
			for _, properties := range propertiesOf(c) {
				for _, value := range properties {
					if value.String != nil && utf8.RuneCountInString(*value.String) > contractsv1.ContextFabricClaimedFactValueMaxLength {
						oversizeScalars++
					}
				}
			}
			from, to := windowOf(c)
			if from != nil && to != nil && to.Before(*from) {
				invertedWindows++
			}
		}
		var obs []quarantineObservation
		kept := partitionProjectableCandidates(gen.candidates, func(o quarantineObservation) { obs = append(obs, o) })
		dropped += len(obs)
		for _, o := range obs {
			switch o.Reason {
			case quarantineDuplicateWithinBatch:
				duplicates++
			case quarantineOrphanedDependent:
				orphans++
			case quarantineEndpointEntityQuarantined:
				endpointDrops++
			}
		}
		for _, c := range kept {
			if c.entity != nil && strings.HasPrefix(c.entity.Subject.CanonicalID, "work_item_ref:") {
				stubs++
			}
		}
		if carriesPayload(kept) {
			published++
		}
	}
	for name, count := range map[string]int{
		"dropped items": dropped, "duplicate_within_batch": duplicates,
		"orphaned_dependent": orphans, "surviving ref stubs": stubs, "publishable sets": published,
		"endpoint_entity_quarantined": endpointDrops,
		// The three normalizable shapes. Each is counted separately because
		// they are separate tokens and a generator that produced only the
		// untrimmed one would leave the other two invariants unexercised while
		// every assertion stayed green.
		"untrimmed labels": untrimmedLabels, "oversize labels": oversizeLabels,
		"oversize scalars": oversizeScalars, "inverted windows": invertedWindows,
	} {
		if count == 0 {
			t.Fatalf("the generator produced ZERO %s across %d cases: the invariant assertions never exercised that path and are proving nothing", name, pipelineInvariantCases)
		}
	}
	t.Logf("reach: dropped=%d duplicates=%d orphans=%d stubs=%d publishable=%d endpointDrops=%d over %d cases",
		dropped, duplicates, orphans, stubs, published, endpointDrops, pipelineInvariantCases)
	t.Logf("normalizable reach: untrimmedLabels=%d oversizeLabels=%d oversizeScalars=%d invertedWindows=%d",
		untrimmedLabels, oversizeLabels, oversizeScalars, invertedWindows)
}

// subjectsOf, propertiesOf and windowOf read the fields the normalization pass
// touches, for the reach counters above. Deliberately a SECOND reader rather
// than a call into itemNormalizer: a non-vacuity check that asks the code under
// test what it repaired is decided by the very mutation it exists to catch.
func subjectsOf(c candidate) []contractsv1.ContextFabricSubjectRef {
	switch {
	case c.entity != nil:
		return []contractsv1.ContextFabricSubjectRef{c.entity.Subject}
	case c.relationship != nil:
		return []contractsv1.ContextFabricSubjectRef{c.relationship.From, c.relationship.To}
	}
	return nil
}

func propertiesOf(c candidate) []map[string]contractsv1.ContextFabricScalarValue {
	switch {
	case c.entity != nil:
		return []map[string]contractsv1.ContextFabricScalarValue{c.entity.Properties}
	case c.relationship != nil:
		return []map[string]contractsv1.ContextFabricScalarValue{c.relationship.Properties}
	}
	return nil
}

func windowOf(c candidate) (*time.Time, *time.Time) {
	switch {
	case c.entity != nil:
		return c.entity.ValidFrom, c.entity.ValidTo
	case c.relationship != nil:
		return c.relationship.ValidFrom, c.relationship.ValidTo
	}
	return nil, nil
}

// TestRefStubAndItsEdgeShareAValidationFate pins the coupling the property
// test's generator relies on, and that invariant (a) holds by.
//
// The ref-form producer builds the stub entity and its edge from the SAME
// refSubject, observedAt, validity window, evidence refs and authorization,
// so every bound the entity can breach the edge breaches too: the edge
// validates that same subject as its To endpoint. A surviving edge therefore
// implies a valid stub, which is why a ref-stub endpoint can never be missing
// for a reason other than being dropped by a pipeline pass.
//
// That is currently true by SHARED FIELDS, not by construction -- add a
// property to the stub alone and it stops holding silently. This test is what
// makes that stop being silent.
func TestRefStubAndItsEdgeShareAValidationFate(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	valid := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC)
	scope := contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}

	build := func(label string, at time.Time) (contractsv1.ContextFabricEntityProjection, contractsv1.ContextFabricRelationshipProjection) {
		refSubject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: "work_item_ref:T-1", Label: label}
		validFrom := valid
		stub := contractsv1.ContextFabricEntityProjection{
			Subject: refSubject, Authorization: scope, EvidenceRefIDs: []string{"acr:v1:gen:S-1:T-1"},
			ObservedAt: at, ValidFrom: &validFrom, SourceVersion: "v1",
		}
		edge := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: "relationship.v2:gen:S-1:T-1", Type: contractsv1.ContextFabricRelationshipBlocks,
			From:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item:S-1", Label: "S-1"},
			To:              refSubject,
			Derivation:      contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization:   scope, EvidenceRefIDs: []string{"acr:v1:gen:S-1:T-1"},
			ObservedAt: at, ValidFrom: &validFrom, SourceVersion: "v1",
		}
		return stub, edge
	}

	cases := []struct {
		name  string
		label string
		at    time.Time
	}{
		{"well formed", "T-1", observed},
		{"untrimmed label", "T-1 ", observed},
		{"unrepresentable instant", "T-1", beyondGo},
		{"oversize label", strings.Repeat("t", 513), observed},
	}
	checked := 0
	for _, tc := range cases {
		stub, edge := build(tc.label, tc.at)
		stubOK, edgeOK := stub.Validate() == nil, edge.Validate() == nil
		if stubOK != edgeOK {
			t.Fatalf("%s: stub valid=%v but edge valid=%v -- the pair no longer shares a validation fate, so an edge can outlive its endpoint and the backend's implicit stub (no validity window, admitted at every time) takes its place",
				tc.name, stubOK, edgeOK)
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d cases reached the assertion", checked, len(cases))
	}
}
