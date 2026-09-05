package contextfabric

import (
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file holds the answer-reuse containment recheck's ref-collection
// surface and its partial-miss degrade path.
//
// THE INVARIANT, stated once and enforced by construction below:
// NEVER SERVE A REF THE CALLER CAN NO LONGER SEE.
//
// A reused result is served WHOLE -- tryReuse sets Reused=true on an
// in-memory copy and strips nothing -- so the invariant covers every ref
// the payload carries anywhere, not only the top-level citations. Two
// mechanisms enforce it together:
//
//  1. evidenceRefSurface + collectEvidenceRefs: ONE traversal used for
//     BOTH sides of the containment check. The demanded set (what the
//     stored payload would serve) and the visible set (what a fresh
//     discovery proves this principal can see right now) are collected by
//     the same code over the same field list, so the two cannot be drawn
//     from different surfaces. They previously were, which is the entire
//     reason answer reuse never hit on real data -- see the surface
//     adapters' own comments.
//
//  2. stripUnverifiedEvidenceRefs: whatever containment still cannot
//     prove is REMOVED from the served copy rather than served, unless it
//     is a top-level citation, in which case reuse refuses outright.
//
// The property test re-collects the demanded set FROM THE SERVED PAYLOAD
// with collectEvidenceRefs and asserts it is disjoint from the missing
// set. That is why the collector is shared rather than duplicated: an
// invariant asserted with the same traversal that builds the payload's
// obligation cannot be passed by a strip that quietly skips a site.

// evidenceRefSurface names every place an evidence reference can ride on
// something a caller is shown. It is the SINGLE declaration of that field
// list; both a stored InvestigationResult and a freshly discovered
// GraphContext are projected onto it, and collectEvidenceRefs walks only
// this shape.
//
// Adding a ref-bearing field to InvestigationResult without adding it
// here is caught by the reflection census test, not by review.
type evidenceRefSurface struct {
	// TopLevel is the payload's own closure/citation list -- the refs a
	// caller reads as "this answer's evidence". Missing refs here are the
	// ONLY ones that refuse a reuse outright; see partitionMissingRefs.
	TopLevel []string
	// Candidates are subject candidates, INCLUDING ones considered and
	// not chosen. Their refs come from graph NODE attributes.
	Candidates []SubjectCandidate
	Cohort     *Cohort
	Drivers    []DriverJudgment
	// Findings groups the three finding lists (remaining work, readiness
	// gaps, conflicts) so the traversal has one loop, not three that can
	// drift apart.
	Findings [][]Finding
	Paths    []RelationshipPath
	// RefLabels is the served display-label map, keyed by evidence ref id.
	// It is a CARRIER, not decoration: a caller reads its keys, so a key
	// naming a ref the recheck could not prove is exactly the leak this
	// whole file exists to prevent. It was missed on the first pass --
	// the collector walked []string fields and a map is not one -- and a
	// reviewer constructed the leak. Present here so the map can never be
	// forgotten again by the same reasoning.
	RefLabels map[string]string
}

// resultEvidenceSurface projects a stored result onto the shared surface.
// This is the DEMANDED side: everything a reused payload would serve.
func resultEvidenceSurface(result InvestigationResult) evidenceRefSurface {
	surface := evidenceRefSurface{
		TopLevel:   result.EvidenceRefIDs,
		Candidates: result.SubjectResolution.Candidates,
		Cohort:     result.Cohort,
		Drivers:    result.Drivers,
		Findings:   [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts},
		Paths:      result.Paths,
		RefLabels:  result.EvidenceRefLabels,
	}
	return surface
}

// graphContextEvidenceSurface projects a fresh discovery onto the SAME
// surface. This is the VISIBLE side.
//
// This adapter is the fix for the defect that made answer reuse dead on
// real data. The recheck used to build its visible set from
// GraphContext.EvidenceRefIDs alone. That field is AdmitEdges' output and
// is built from graph EDGE attributes only, while a subject candidate's
// refs come from graph NODE attributes (graphrank/discover.go's
// CandidateNode construction). Node refs could therefore NEVER appear in
// the visible set, so any stored answer carrying a subject candidate with
// its own refs failed containment with certainty -- not probabilistically,
// and not because of any cap or ranking. Measured on the live org: 10
// candidate refs, 9 of them outside the edge closure, demanded 12 against
// an obtainable 3, hit rate 0/8.
//
// Reading the candidates and cohort members off the FRESH GraphContext is
// what makes their refs visible, and it is authorization-safe by
// construction: this GraphContext is what DiscoverContext returned for
// THIS principal moments ago, so every ref on it is one the caller can see
// right now. The check is widened by proving more visible, never by
// demanding less.
//
// Findings is empty here: findings are synthesis-authored and have no
// discovery counterpart. Their refs are still DEMANDED (they are served),
// and are proven visible through the other sites like any other ref.
func graphContextEvidenceSurface(graphContext GraphContext) evidenceRefSurface {
	return evidenceRefSurface{
		TopLevel:   graphContext.EvidenceRefIDs,
		Candidates: graphContext.Resolution.Candidates,
		Cohort:     graphContext.Cohort,
		Drivers:    graphContext.DriverCandidates,
		Paths:      graphContext.Paths,
	}
}

// collectEvidenceRefs walks the surface once and returns every distinct
// ref in a stable order (first appearance). Order is deterministic given
// the input so a caller may use it in telemetry without re-sorting.
func collectEvidenceRefs(surface evidenceRefSurface) []string {
	seen := make(map[string]struct{}, len(surface.TopLevel))
	refs := make([]string, 0, len(surface.TopLevel))
	add := func(ids []string) {
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			refs = append(refs, id)
		}
	}
	add(surface.TopLevel)
	for _, candidate := range surface.Candidates {
		add(candidate.EvidenceRefIDs)
	}
	if surface.Cohort != nil {
		for _, member := range surface.Cohort.Members {
			add(member.EvidenceRefIDs)
		}
	}
	for _, driver := range surface.Drivers {
		add(driver.EvidenceRefIDs)
	}
	for _, findings := range surface.Findings {
		for _, finding := range findings {
			add(finding.EvidenceRefIDs)
		}
	}
	for _, path := range surface.Paths {
		add(path.EvidenceRefIDs)
		// A path's own EvidenceRefIDs is not guaranteed to be a superset
		// of its edges' -- a result citing evidence ONLY at edge level
		// would otherwise escape both the recheck and the strip.
		for _, edge := range path.Edges {
			add(edge.EvidenceRefIDs)
		}
	}
	// Map keys LAST and SORTED. Sorted because Go randomizes map iteration
	// and this function's output order is asserted to be stable; last
	// because on a well-formed payload every key is already present from a
	// site above, so this only ever contributes a ref the map alone
	// carries -- which is precisely the case worth catching.
	if len(surface.RefLabels) > 0 {
		labelled := make([]string, 0, len(surface.RefLabels))
		for ref := range surface.RefLabels {
			labelled = append(labelled, ref)
		}
		sort.Strings(labelled)
		add(labelled)
	}
	return refs
}

// reuseContainmentPartition splits the refs a stored payload would serve
// into what a fresh discovery proved visible and what it did not, and
// records whether any unproven ref is a TOP-LEVEL citation.
type reuseContainmentPartition struct {
	// Demanded is every distinct ref the stored payload would serve.
	Demanded []string
	// Missing is the subset no fresh discovery proved visible.
	Missing map[string]struct{}
	// MissingCitation is true when at least one missing ref is a
	// top-level citation. That is the refuse condition: an answer whose
	// own cited evidence is no longer visible is not a narrowed answer,
	// it is a different answer.
	MissingCitation bool
	// VisibleCount is how many distinct refs the fresh discovery proved.
	// Telemetry only -- it is the number that says whether a miss is real
	// authorization narrowing or a recheck that could not see enough.
	VisibleCount int
}

// partitionMissingRefs is the containment check itself. It never returns
// early: the FULL missing set is needed to decide what to strip, and the
// per-class counts are what let an operator attribute a miss instead of
// guessing at it.
func partitionMissingRefs(stored InvestigationResult, graphContext GraphContext) reuseContainmentPartition {
	visibleRefs := collectEvidenceRefs(graphContextEvidenceSurface(graphContext))
	visible := make(map[string]struct{}, len(visibleRefs))
	for _, ref := range visibleRefs {
		visible[ref] = struct{}{}
	}
	citations := make(map[string]struct{}, len(stored.EvidenceRefIDs))
	for _, ref := range stored.EvidenceRefIDs {
		citations[ref] = struct{}{}
	}
	partition := reuseContainmentPartition{
		Demanded:     collectEvidenceRefs(resultEvidenceSurface(stored)),
		Missing:      map[string]struct{}{},
		VisibleCount: len(visible),
	}
	for _, ref := range partition.Demanded {
		if _, ok := visible[ref]; ok {
			continue
		}
		partition.Missing[ref] = struct{}{}
		if _, isCitation := citations[ref]; isCitation {
			partition.MissingCitation = true
		}
	}
	return partition
}

// reuseStripCounts reports what the degrade removed. Every field is
// disclosed: a reused answer that quietly lost members or drivers would be
// a narrowed answer presenting itself as a whole one.
type reuseStripCounts struct {
	// RemovedRefs is the SET of distinct reference ids removed anywhere in
	// the payload -- every list carrier and the display-label map. A set,
	// not a tally, because one reference can ride at several carriers at
	// once and the caller lost ONE reference, not one per carrier.
	RemovedRefs map[string]struct{}
	// The object counts are entries dropped ENTIRELY because stripping
	// their refs left them invalid under the contract -- a driver with
	// neither paths nor evidence, for instance, is not a driver.
	DroppedCandidates int
	DroppedMembers    int
	DroppedDrivers    int
	DroppedFindings   int
	DroppedPaths      int
	// StrippedLabels is how many display-label entries the rebuild
	// dropped. Kept as a DIAGNOSTIC beside the reference set, never added
	// into it: a label entry is a second way the same reference reaches a
	// caller (a strip that cleared every list and left the labels behind
	// removed nothing at all), but the reference it names is already in
	// RemovedRefs, so adding it would double-count the same loss in the
	// number the caller reads.
	StrippedLabels int
}

// Refs is how many DISTINCT evidence references the degrade removed. This
// is the number the caller is told and the number telemetry reports.
func (c reuseStripCounts) Refs() int { return len(c.RemovedRefs) }

// recordRemoved adds ids to the removed set, allocating on first use.
func (c *reuseStripCounts) recordRemoved(ids []string) {
	if len(ids) == 0 {
		return
	}
	if c.RemovedRefs == nil {
		c.RemovedRefs = make(map[string]struct{}, len(ids))
	}
	for _, id := range ids {
		c.RemovedRefs[id] = struct{}{}
	}
}

// objectDrops is how many whole entries were dropped because stripping
// left them invalid. Distinct from references by construction: these are
// objects, and each is dropped at most once.
func (c reuseStripCounts) objectDrops() int {
	return c.DroppedCandidates + c.DroppedMembers + c.DroppedDrivers + c.DroppedFindings + c.DroppedPaths
}

// Total is every removal the caller should be told about.
// Total is every removal the caller should be told about: distinct
// references plus whole entries dropped. StrippedLabels is deliberately
// NOT summed -- the reference a dropped label names is already counted in
// the reference set, and adding it would report one loss twice.
func (c reuseStripCounts) Total() int {
	return c.Refs() + c.objectDrops()
}

// empty reports whether NOTHING was removed. Deliberately checked
// component by component rather than as `Total() == 0`: a sum can be zero
// because two terms cancelled, and the one thing this predicate gates --
// whether the caller is told the answer was narrowed -- must never turn on
// arithmetic that can cancel. Every component is non-negative by
// construction; this makes it not matter if one day one is not.
func (c reuseStripCounts) empty() bool {
	return len(c.RemovedRefs) == 0 &&
		c.DroppedCandidates == 0 && c.DroppedMembers == 0 && c.DroppedDrivers == 0 &&
		c.DroppedFindings == 0 && c.DroppedPaths == 0 && c.StrippedLabels == 0
}

// keepRefs returns ids minus missing, and THE IDS IT REMOVED -- not a count.
//
// Returning ids rather than a tally is what makes the caller able to count
// DISTINCT references across the whole payload. One reference may ride at
// several carriers at once (a path's own list and one of its edges', a
// driver and a finding, a member and a candidate): each slice is validated
// on its own and the contract's closure deduplicates globally, so that is a
// legal stored shape. Summing per-site tallies counted such a reference once
// per carrier and told the caller more references had become invisible than
// reference IDs actually had.
//
// It always returns a NEW slice when anything changed, so a stored result's
// own backing arrays are never mutated -- the reused candidate is a shallow
// value copy and shares every slice with whatever else holds it.
func keepRefs(ids []string, missing map[string]struct{}) ([]string, []string) {
	var removed []string
	for _, id := range ids {
		if _, gone := missing[id]; gone {
			removed = append(removed, id)
		}
	}
	if len(removed) == 0 {
		return ids, nil
	}
	kept := make([]string, 0, len(ids)-len(removed))
	for _, id := range ids {
		if _, gone := missing[id]; gone {
			continue
		}
		kept = append(kept, id)
	}
	return kept, removed
}

// strippingBrokeIt reports whether removing references is what made an
// object invalid, by comparing before against after.
//
// The comparison matters and a bare "is it valid now" check would be
// wrong. A reused payload is a STORED result: it may legitimately carry an
// object that already fails the strict write validator, because stored
// rows are immutable and are read back under deliberately looser legacy
// bounds. Testing the stripped object alone would drop such an object for
// a reason this strip did not cause -- narrowing the answer further than
// the missing references require, and blaming the narrowing on
// authorization. Only a valid-then-invalid transition is attributable to
// the strip.
func strippingBrokeIt(validBefore bool, after error) bool {
	return validBefore && after != nil
}

// stripUnverifiedEvidenceRefs removes every missing ref from every site
// the shared surface knows about, on a copy, and drops any object that
// stripping left invalid.
//
// Validity is decided by calling the object's REAL Validate(), never by a
// second copy of the contract's rules here. A local re-statement of "a
// driver needs paths or evidence" would be one more thing to drift from
// the validator it is supposed to agree with.
//
// The caller is responsible for refusing when MissingCitation is set;
// this function assumes it has already been told to degrade.
func stripUnverifiedEvidenceRefs(result InvestigationResult, missing map[string]struct{}) (InvestigationResult, reuseStripCounts) {
	var counts reuseStripCounts
	if len(missing) == 0 {
		return result, counts
	}
	// The reference set is computed from the payload BEFORE and AFTER,
	// using the same collector that defines what the payload serves --
	// never by tallying per site.
	//
	// Per-site tallies were wrong in BOTH directions and each direction
	// was found the hard way. They OVER-counted, because one reference can
	// ride at several carriers and each removal was counted separately.
	// They also UNDER-counted: when stripping empties an object the
	// contract requires to carry evidence, the whole object is dropped and
	// the OTHER references it held leave with it — losses no site's own
	// tally ever saw. A before/after difference cannot miss either case,
	// and because it is taken with the collector it cannot drift from what
	// the payload actually exposes.
	before := collectEvidenceRefs(resultEvidenceSurface(result))

	if len(result.SubjectResolution.Candidates) > 0 {
		kept := make([]SubjectCandidate, 0, len(result.SubjectResolution.Candidates))
		for _, candidate := range result.SubjectResolution.Candidates {
			refs, removed := keepRefs(candidate.EvidenceRefIDs, missing)
			if len(removed) > 0 {
				validBefore := candidate.Validate() == nil
				candidate.EvidenceRefIDs = refs
				if strippingBrokeIt(validBefore, candidate.Validate()) {
					counts.DroppedCandidates++
					continue
				}
			}
			kept = append(kept, candidate)
		}
		result.SubjectResolution.Candidates = kept
	}

	if result.Cohort != nil && len(result.Cohort.Members) > 0 {
		cohort := *result.Cohort
		kept := make([]CohortMember, 0, len(cohort.Members))
		for _, member := range cohort.Members {
			refs, removed := keepRefs(member.EvidenceRefIDs, missing)
			if len(removed) > 0 {
				validBefore := member.Validate() == nil
				member.EvidenceRefIDs = refs
				if strippingBrokeIt(validBefore, member.Validate()) {
					counts.DroppedMembers++
					continue
				}
			}
			kept = append(kept, member)
		}
		if counts.DroppedMembers > 0 {
			// A cohort that lost members is no longer complete and says
			// so, exactly like every other narrowing path in the engine.
			cohort.Complete = false
			cohort.Truncated = true
		}
		cohort.Members = kept
		result.Cohort = &cohort
	}

	if len(result.Drivers) > 0 {
		kept := make([]DriverJudgment, 0, len(result.Drivers))
		for _, driver := range result.Drivers {
			refs, removed := keepRefs(driver.EvidenceRefIDs, missing)
			if len(removed) > 0 {
				validBefore := driver.Validate() == nil
				driver.EvidenceRefIDs = refs
				if strippingBrokeIt(validBefore, driver.Validate()) {
					counts.DroppedDrivers++
					continue
				}
			}
			kept = append(kept, driver)
		}
		result.Drivers = kept
	}

	for _, findings := range []*[]Finding{&result.RemainingWork, &result.ReadinessGaps, &result.Conflicts} {
		if len(*findings) == 0 {
			continue
		}
		kept := make([]Finding, 0, len(*findings))
		for _, finding := range *findings {
			refs, removed := keepRefs(finding.EvidenceRefIDs, missing)
			if len(removed) > 0 {
				validBefore := finding.Validate() == nil
				finding.EvidenceRefIDs = refs
				if strippingBrokeIt(validBefore, finding.Validate()) {
					counts.DroppedFindings++
					continue
				}
			}
			kept = append(kept, finding)
		}
		*findings = kept
	}

	if len(result.Paths) > 0 {
		kept := make([]RelationshipPath, 0, len(result.Paths))
		for _, path := range result.Paths {
			refs, removed := keepRefs(path.EvidenceRefIDs, missing)
			edgesChanged := false
			edges := make([]RelationshipEdge, 0, len(path.Edges))
			for _, edge := range path.Edges {
				edgeRefs, edgeRemoved := keepRefs(edge.EvidenceRefIDs, missing)
				if len(edgeRemoved) > 0 {
					edge.EvidenceRefIDs = edgeRefs
					edgesChanged = true
				}
				edges = append(edges, edge)
			}
			if len(removed) > 0 || edgesChanged {
				validBefore := path.Validate() == nil
				path.EvidenceRefIDs = refs
				path.Edges = edges
				if strippingBrokeIt(validBefore, path.Validate()) {
					counts.DroppedPaths++
					continue
				}
			}
			kept = append(kept, path)
		}
		result.Paths = kept
	}

	// The display-label map is REBUILT from the served closure, never
	// subtracted from. Subtracting would remove the keys this strip knows
	// about and leave anything else the map happened to carry -- which is
	// how the leak got in: the map was simply not walked, and "remove what
	// we removed" repeats that mistake one level up. Rebuilding makes the
	// map a FUNCTION of what is actually served, so a key the served
	// closure does not contain is unrepresentable rather than merely
	// unlikely.
	//
	// nil stays nil: a legacy stored row that predates this field is the
	// ruled exception, and inventing a map for it would change the shape
	// of what that caller has always been served.
	if result.EvidenceRefLabels != nil {
		served := contractsv1.ContextFabricEvidenceRefClosure(result)
		rebuilt := make(map[string]string, len(served))
		for ref := range served {
			if label, known := result.EvidenceRefLabels[ref]; known {
				rebuilt[ref] = label
				continue
			}
			// The stored map was already missing a label this closure
			// needs (an under-labelled legacy row). Compose the same
			// deterministic label a fresh result would have carried
			// rather than serve a closure the map cannot describe.
			label, _ := contractsv1.ContextFabricEvidenceRefLabel(ref)
			rebuilt[ref] = label
		}
		// Count the keys that were actually REMOVED, never a difference of
		// lengths. The rebuild can legitimately GROW the map: a stored row
		// may be under-labelled (stored validation does not enforce exact
		// label/closure equality, only writes do), and the rebuild supplies
		// the missing labels. A signed length difference then goes
		// NEGATIVE, and a negative removal count is not merely a wrong
		// number -- summed into the total it CANCELS a real reference
		// removal, and a genuinely narrowed answer is served with no
		// disclosure at all. Counting removals directly cannot do that.
		for ref := range result.EvidenceRefLabels {
			if _, kept := rebuilt[ref]; !kept {
				counts.StrippedLabels++
			}
		}
		result.EvidenceRefLabels = rebuilt
	}

	// Top-level citations are NEVER stripped: a missing citation refuses
	// the whole reuse before this function is reached, so by construction
	// no top-level ref is in the missing set here. Asserted by the
	// property test rather than assumed.
	served := make(map[string]struct{})
	for _, ref := range collectEvidenceRefs(resultEvidenceSurface(result)) {
		served[ref] = struct{}{}
	}
	for _, ref := range before {
		if _, stillServed := served[ref]; !stillServed {
			counts.recordRemoved([]string{ref})
		}
	}
	return result, counts
}

// reuseAuxiliaryRefsStrippedSource is the coverage source string the
// degrade's disclosure carries. It names the mechanism, not the ticket.
const reuseAuxiliaryRefsStrippedSource = "context-fabric:answer-reuse"

// reuseDegradeDisclosure is which form of disclosure the degraded copy
// actually carried. A stored result written before structured coverage
// details existed cannot receive one without breaking the contract's
// 1:1 pairing between degrading details and degraded_reasons, so the
// branch that falls back to the composed string alone is REPORTED rather
// than taken silently.
type reuseDegradeDisclosure string

const (
	// reuseDegradeDisclosureStructured: both the structured coverage
	// detail and its composed reason string were attached.
	reuseDegradeDisclosureStructured reuseDegradeDisclosure = "structured"
	// reuseDegradeDisclosureReasonOnly: the stored payload predates
	// structured coverage details (details absent, reasons present), so
	// only the composed reason string was attached. The caller is still
	// told the answer was narrowed; a consumer keying on the structured
	// code will not see this one.
	reuseDegradeDisclosureReasonOnly reuseDegradeDisclosure = "reason_only"
)

// discloseReuseNarrowing records the narrowing on the degraded copy's
// coverage. Coverage.Partial is forced true: an answer that lost evidence
// is partial regardless of what the stored row claimed when it was whole.
func discloseReuseNarrowing(result InvestigationResult, counts reuseStripCounts) (InvestigationResult, reuseDegradeDisclosure) {
	if counts.empty() {
		return result, reuseDegradeDisclosureStructured
	}
	coverage := result.Coverage
	reason := composeReuseNarrowingReason(counts)

	// The contract pairs DEGRADING details 1:1 with degraded_reasons, in
	// order. Appending to the end of both preserves that pairing -- but
	// only when a details array exists to append to. A legacy payload
	// with reasons and no details would become 1 detail against N+1
	// reasons, which is unrepresentable, so that case takes the composed
	// string alone and says so.
	disclosure := reuseDegradeDisclosureStructured
	if len(coverage.Details) == 0 && len(coverage.DegradedReasons) > 0 {
		disclosure = reuseDegradeDisclosureReasonOnly
	}

	reasons := make([]string, 0, len(coverage.DegradedReasons)+1)
	reasons = append(reasons, coverage.DegradedReasons...)
	reasons = append(reasons, reason)
	coverage.DegradedReasons = reasons

	if disclosure == reuseDegradeDisclosureStructured {
		// The caller-facing quantity is DISTINCT references removed.
		total := counts.Refs()
		detail := contractsv1.ContextFabricCoverageDetail{
			DetailID:  "cov-reuse-01",
			Source:    reuseAuxiliaryRefsStrippedSource,
			Code:      contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped,
			Degrading: true,
			Count:     &total,
			Raw:       reason,
		}
		detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
		details := make([]contractsv1.ContextFabricCoverageDetail, 0, len(coverage.Details)+1)
		details = append(details, coverage.Details...)
		details = append(details, detail)
		coverage.Details = details
	}

	coverage.Partial = true
	result.Coverage = coverage

	// APPEND. The reuse degrade is a narrowing stage between planning and
	// the served document, so it owes the outcome set a row of its own.
	//
	// Without one, a stored answer whose requirements all read `satisfied`
	// served a genuinely smaller document while still claiming `complete`:
	// the serve path re-derives completeness from the CARRIED rows, and
	// carried rows know nothing about a cut made after they were written.
	// That is measure-then-shrink, on the one surface this layer originally
	// did not cover, and it is worse than the assembly case this seam
	// started from -- assembly at least refused, and this path serves.
	//
	// The coverage detail above and this row are NOT redundant. The detail
	// says how much evidence went; the row says which REQUIREMENT is now
	// only partly met and what the reader loses by it, which is the whole
	// distinction between a counter and a named outcome.
	result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes,
		reuseNarrowingOutcomeRow(result, counts))
	// DERIVE LAST, and derive it HERE as well as at the serving surface.
	//
	// The serving surface re-derives too, so this looks redundant and is
	// not: degradeReusedResult validates the degraded payload before
	// anything is served, and a block whose state still says `complete`
	// while its own rows now say otherwise fails that validation -- the
	// single-authority check is enforced at the wire boundary. Appending
	// without re-deriving would turn every degraded reuse into a REFUSAL,
	// which is a silent regression dressed as caution: the caller loses a
	// usable narrowed answer to fix a disclosure gap.
	result.Completeness = ComputeAnswerCompleteness(result)
	return result, disclosure
}

// reuseNarrowingOutcomeRow names what the reuse degrade removed.
//
// CAUSE: the shipped `reuse_auxiliary_refs_stripped` coverage code, the same
// one the structured disclosure beside it carries. No new cause vocabulary,
// and the row cannot drift from the detail because both name one token.
//
// IMPACT is decided by WHAT was lost, not by how much. References alone
// means the same subjects reached the caller carrying less evidence behind
// them -- `depth`. A dropped candidate, member, driver, finding or path
// means the caller is shown FEWER things -- `scope`. Reporting one for the
// other would tell a reader to look for a loss of the wrong kind.
//
// SERVED/DECLARED count the evidence-reference set before and after, which
// is the quantity the strip actually computed. It is a real reduction by
// construction: this function is only reached when counts are non-empty.
func reuseNarrowingOutcomeRow(result InvestigationResult, counts reuseStripCounts) RequirementOutcomeRow {
	served := len(collectEvidenceRefs(resultEvidenceSurface(result)))
	requirement, obligation := subjectScopeRequirement(result.Completeness.Outcomes)
	impact := contractsv1.ContextFabricAnswerImpactDepth
	if counts.objectDrops() > 0 {
		impact = contractsv1.ContextFabricAnswerImpactScope
	}
	row := RequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageReuse,
		Requirement:   requirement,
		Obligation:    obligation,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        impact,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailReuseAuxiliaryRefsStripped,
		// Observed: the authorization recheck itself established that these
		// references are no longer visible. Nothing here defaulted.
		CauseObserved: true,
		Served:        served,
		Declared:      served + counts.Refs(),
	}
	// The reduction step, derived from this row's own counts and cause. The
	// cause here is a COVERAGE code -- no ceiling and no ordering -- which is
	// why the refinement mirrors all three of the row's cause fields rather
	// than only the two the assembly stage happens to use.
	return contractsv1.ContextFabricWithReductionRefinement(row)
}

// composeReuseNarrowingReason is the composed legacy string. It states the
// quantity and the cause in the engine's own deterministic words -- the
// fail-closed floor, never model-authored.
func composeReuseNarrowingReason(counts reuseStripCounts) string {
	if counts.objectDrops() == 0 {
		return fmt.Sprintf(
			"reused answer narrowed: %d evidence reference(s) are no longer visible to you and were removed",
			counts.Refs(),
		)
	}
	return fmt.Sprintf(
		"reused answer narrowed: %d evidence reference(s) are no longer visible to you and were removed, "+
			"together with %d item(s) left without any evidence",
		counts.Refs(),
		counts.objectDrops(),
	)
}

// degradeReusedResult applies the strip, discloses it, and re-validates
// the whole degraded payload before anything is served.
//
// ok=false means the degrade could not produce a payload that satisfies
// the stored-result contract. Reuse then REFUSES rather than serve a
// malformed answer -- a narrowed answer is useful, an invalid one is a
// 500 for the caller and a silent contract break for every consumer.
func degradeReusedResult(result InvestigationResult, missing map[string]struct{}) (InvestigationResult, reuseStripCounts, reuseDegradeDisclosure, bool) {
	degraded, counts := stripUnverifiedEvidenceRefs(result, missing)
	degraded, disclosure := discloseReuseNarrowing(degraded, counts)
	if err := ValidateStoredResult(degraded); err != nil {
		return InvestigationResult{}, counts, disclosure, false
	}
	if !servedLabelsAreWithinTheClosure(degraded) {
		return InvestigationResult{}, counts, disclosure, false
	}
	return degraded, counts, disclosure, true
}

// servedLabelsAreWithinTheClosure enforces, ON THE SERVE PATH, the rule the
// contract enforces only on writes: a display-label key must name a
// reference the result actually carries.
//
// It is checked here rather than inside ValidateStored for two reasons.
// First, stored results are immutable and read back under deliberately
// looser bounds, so tightening the shared stored validator would reject
// legacy rows that were correct when written and are nobody's leak.
// Second, the degrade is the ONLY thing that can turn a well-formed stored
// payload into one whose label map outruns its closure -- so this is where
// the obligation belongs, and keeping it here keeps the invariant in one
// place instead of two.
//
// Deliberately one-directional. An UNDER-labelled map (a closure ref with
// no label) is an old row that discloses less than it could; that is not an
// authorization leak and must not refuse a reuse. An OVER-labelled map is a
// reference reaching a caller that the recheck never proved, which is
// exactly what must never be served.
func servedLabelsAreWithinTheClosure(result InvestigationResult) bool {
	if len(result.EvidenceRefLabels) == 0 {
		return true
	}
	closure := contractsv1.ContextFabricEvidenceRefClosure(result)
	for ref := range result.EvidenceRefLabels {
		if _, present := closure[ref]; !present {
			return false
		}
	}
	return true
}
