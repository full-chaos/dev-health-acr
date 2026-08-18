package graphrank

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ResolvedEdge is a candidate relationship edge whose endpoints the backend
// has already fully resolved to SubjectRef -- however it needed to (a
// first-hop-trusted node from its own search results, a second-hop
// fetch-and-verify against an org-agnostic UUID lookup, or a single
// whole-path query that never needed the distinction at all). AdmitEdges
// owns none of that resolution or the I/O it took -- zepgraph's
// DiscoverContext (before its CHAOS-3771 deletion) had the Zep-shaped
// second-hop machinery, deliberately NOT here: a backend where every
// lookup is already structurally scoped to one organization (e.g.
// falkorgraph's one-graph-per-org) has no second-hop concept to inject, and
// forcing one into this shared package would leak a Zep-specific shape into
// every other backend.
type ResolvedEdge struct {
	UUID string
	// Name is the raw, backend-reported relation name; AdmitEdges applies
	// NormalizeRelation itself.
	Name     string
	Fact     string
	From, To contextfabric.SubjectRef
	// Relevance is an adapter-declared, already-normalized confidence -- see
	// NormalizedRelevance for why this is a distinct type and not a float64.
	Relevance *NormalizedRelevance
	Score     *float64
	// Attributes must carry "epistemic_status" (string) and "evidence_refs"
	// ([]string, graphrank's shared convention -- see EvidenceRefs).
	Attributes map[string]interface{}
	// CreatedAt/ValidAt/InvalidAt/ExpiredAt are RFC3339Nano-formatted
	// timestamps (or empty/nil).
	CreatedAt string
	ValidAt   *string
	InvalidAt *string
	ExpiredAt *string
}

// AdmitEdgesResult is AdmitEdges' output.
type AdmitEdgesResult struct {
	Paths            []contextfabric.RelationshipPath
	Drivers          []contextfabric.DriverJudgment
	EvidenceRefIDs   []string
	FactRequirements []contextfabric.FactRequirement
	// DroppedUnknownRelationshipTypeCount and
	// DroppedUnknownRelationshipTypeNames record every candidate edge
	// AdmitEdges excluded because its Type failed
	// ContextFabricRelationshipType's closed vocabulary (CHAOS-3779 codex
	// round-1 finding H1). Before this, such an edge was dropped by the
	// same generic `path.Validate() != nil` continue as any other
	// malformed edge -- no metric, no degraded-coverage signal -- which is
	// the H4 silent-admission-failure shape, just relocated to the READ
	// path (a write-path producer bug, a partial rollback leaving legacy
	// data, or a future contract downgrade could all put an
	// out-of-vocabulary type in the graph). AdmitEdges stays pure (no I/O,
	// per its own doc comment): it only counts and names the dropped
	// types, deduplicated and sorted for determinism. The caller (e.g.
	// falkorgraph.DiscoverContext, the only I/O boundary in this call
	// chain) is responsible for marking Coverage.Partial/DegradedReasons
	// and for logging -- see that function's doc comment.
	DroppedUnknownRelationshipTypeCount int
	DroppedUnknownRelationshipTypeNames []string
	// DroppedSelfLoopCount (CHAOS-3888) counts edges excluded because
	// edge.From == edge.To, one of the two conditions the loop below folds
	// into a single `continue` alongside the internal-bookkeeping-endpoint
	// exclusion (isInternal). Counted separately, the same "return the
	// dropped count as a plain value" convention
	// DroppedUnknownRelationshipTypeCount already established just above --
	// a self-loop is a distinct, nameable reason an operator may want to
	// watch, where an internal-bookkeeping endpoint is routine and already
	// expected on every call.
	DroppedSelfLoopCount int
}

// AdmitEdges applies the evidence-budget-bounded admission decision to an
// already-endpoint-resolved candidate edge list, building
// RelationshipPaths and DriverJudgments. edges must already be in the
// backend's intended admission order (typically descending relevance,
// stable-tie-broken by edge UUID -- see ResultConfidence) since admission
// order determines which edges win a scarce evidence budget (Codex N2);
// AdmitEdges does not re-sort, so a backend that skips sorting gets
// backend-order-dependent admission, exactly as it would have before this
// extraction.
//
// Self-loop and internal-bookkeeping-endpoint exclusion happen here (pure,
// no I/O) rather than trusting the caller to have filtered them, since both
// checks need nothing but the already-resolved SubjectRefs.
//
// Ported from the admission-loop body of zepgraph.(*Adapter).DiscoverContext.
func AdmitEdges(orgID string, edges []ResolvedEdge, options contextfabric.InvestigationOptions, isInternal func(contextfabric.SubjectRef) bool) AdmitEdgesResult {
	paths := make([]contextfabric.RelationshipPath, 0, len(edges))
	drivers := make([]contextfabric.DriverJudgment, 0, len(edges))
	evidenceSet := make(map[string]struct{})
	requirements := make(map[contextfabric.FactKind]contextfabric.FactRequirement)
	droppedUnknownCount := 0
	droppedUnknownSeen := make(map[string]struct{})
	droppedSelfLoopCount := 0
	for _, edge := range edges {
		// CHAOS-3888: isSelfLoop counted separately, BEFORE the combined
		// condition below decides whether to continue -- the combined
		// condition itself is unchanged (same edges excluded, same order),
		// this only adds visibility into which of its two arms fired.
		if isSelfLoop := edge.From == edge.To; isSelfLoop || isInternal(edge.From) || isInternal(edge.To) {
			if isSelfLoop {
				droppedSelfLoopCount++
			}
			continue
		}
		evidence := UniqueSorted(EvidenceRefs(edge.Attributes))
		if len(evidence) == 0 {
			continue
		}
		// Codex finding G5: Options.MaxEvidenceRefs must bound the FINAL
		// result's entire evidence surface -- every path's and driver's own
		// EvidenceRefIDs, not just the separately truncated aggregate list
		// below. Checked here, before a path/driver is admitted at all,
		// against the *projected* size (evidenceSet is not mutated yet --
		// N3, below) so Paths, DriverCandidates, and the aggregate
		// EvidenceRefIDs stay consistent with the same bounded evidence set
		// by construction.
		if maxEvidence := options.MaxEvidenceRefs; maxEvidence > 0 {
			projected := len(evidenceSet)
			for _, id := range evidence {
				if _, exists := evidenceSet[id]; !exists {
					projected++
				}
			}
			if projected > maxEvidence {
				continue
			}
		}
		relationship := contextfabric.RelationshipEdge{
			Type: contextfabric.RelationshipType(NormalizeRelation(edge.Name)), From: edge.From, To: edge.To,
			Derivation: contextfabric.DerivationGraphAssociated, EpistemicStatus: edgeEpistemicStatus(edge),
			ObservedAt: ParseOptionalTime(edge.CreatedAt), ValidFrom: ParseOptionalTimePtr(edge.ValidAt), ValidTo: edgeValidTo(edge),
			EvidenceRefIDs: evidence,
		}
		pathID := DeterministicUUID("context-fabric-path", orgID, edge.UUID)
		path := contextfabric.RelationshipPath{
			PathID: pathID, Nodes: []contextfabric.SubjectRef{edge.From, edge.To}, Edges: []contextfabric.RelationshipEdge{relationship},
			WhyRelevant: edgeFact(edge), EvidenceRefIDs: evidence, Truncated: false,
		}
		if err := path.Validate(); err != nil {
			// H1 (CHAOS-3779 codex round-1): an edge whose Type is outside
			// the closed relationship-type vocabulary must not vanish
			// silently -- count it and remember its (normalized) name so
			// the caller can mark Coverage.Partial and log it, exactly
			// like a failed endpoint lookup already does. Every OTHER
			// path.Validate() failure (a malformed subject, an oversized
			// field, ...) still just continues, unrecorded -- unknown
			// relationship type is the one call-out this issue owns.
			if errors.Is(err, contractsv1.ErrContextFabricUnknownRelationshipType) {
				droppedUnknownCount++
				droppedUnknownSeen[NormalizeRelation(edge.Name)] = struct{}{}
			}
			continue
		}
		// N3: the shared evidence set is only mutated once the path this
		// evidence belongs to has actually been accepted. Committing it
		// earlier (before path.Validate()) meant a malformed edge could
		// permanently consume its share of a scarce evidence budget even
		// though its own path was rejected and never admitted -- silently
		// crowding out a later, genuinely valid edge.
		for _, id := range evidence {
			evidenceSet[id] = struct{}{}
		}
		paths = append(paths, path)
		if standing, category, factKind, relevant := relationMeaning(edge.Name); relevant {
			driver := contextfabric.DriverJudgment{
				DriverID: DeterministicUUID("context-fabric-driver", orgID, edge.UUID), Standing: standing, Category: category,
				Title: relationTitle(edge.Name, edge.To.Label), Summary: edgeFact(edge), AffectedSubjects: []contextfabric.SubjectRef{edge.From},
				PathIDs: []string{pathID}, EvidenceRefIDs: evidence, Derivation: contextfabric.DerivationGraphAssociated,
				EpistemicStatus: contextfabric.EpistemicInferred, Confidence: ResultConfidence(edge.Relevance, edge.Score), Current: edge.InvalidAt == nil && edge.ExpiredAt == nil,
			}
			if driver.Confidence == 0 {
				driver.Confidence = 0.55
			}
			if err := driver.Validate(); err == nil {
				drivers = append(drivers, driver)
				requirements[factKind] = contextfabric.FactRequirement{Kind: factKind}
			}
		}
	}
	if len(paths) > options.MaxRelationshipPaths {
		paths = paths[:options.MaxRelationshipPaths]
	}
	if len(drivers) > options.MaxDrivers {
		drivers = drivers[:options.MaxDrivers]
	}
	factRequirements := make([]contextfabric.FactRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		factRequirements = append(factRequirements, requirement)
	}
	sort.Slice(factRequirements, func(i, j int) bool { return factRequirements[i].Kind < factRequirements[j].Kind })
	evidence := make([]string, 0, len(evidenceSet))
	for id := range evidenceSet {
		evidence = append(evidence, id)
	}
	sort.Strings(evidence)
	droppedUnknownNames := make([]string, 0, len(droppedUnknownSeen))
	for name := range droppedUnknownSeen {
		droppedUnknownNames = append(droppedUnknownNames, name)
	}
	sort.Strings(droppedUnknownNames)
	return AdmitEdgesResult{
		Paths: paths, Drivers: drivers, EvidenceRefIDs: evidence, FactRequirements: factRequirements,
		DroppedUnknownRelationshipTypeCount: droppedUnknownCount, DroppedUnknownRelationshipTypeNames: droppedUnknownNames,
		DroppedSelfLoopCount: droppedSelfLoopCount,
	}
}

// SortEdgesByRelevance sorts edges descending by ResultConfidence, tie-broken
// by UUID for determinism -- the exact ordering AdmitEdges' admission budget
// assumes it was given (Codex N2: admission order must not depend on
// whatever order the backend happened to return edges in). A backend calls
// this on its raw search results before resolving endpoints (first-hop
// trust, second-hop fetch, or otherwise), since sorting only needs the
// relevance/score/UUID fields already present pre-resolution.
func SortEdgesByRelevance(edges []CandidateEdge) []CandidateEdge {
	sorted := append([]CandidateEdge(nil), edges...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := ResultConfidence(sorted[i].Relevance, sorted[i].Score), ResultConfidence(sorted[j].Relevance, sorted[j].Score)
		if ri != rj {
			return ri > rj
		}
		return sorted[i].UUID < sorted[j].UUID
	})
	return sorted
}

// DiscoveredCohort discovers a subjectless team/project cohort from a
// backend's first-hop search result nodes, when the interpreted question
// shape calls for one. Pure: only reads already-fetched nodes, no I/O of its
// own, so (unlike DiscoverContext's old second-hop machinery) it needed no
// change to stay shared. Ported from
// zepgraph.(*Adapter).discoveredCohort.
//
// Returns (cohort, authzDropped): authzDropped (CHAOS-3888) counts every
// node this call excluded specifically because AuthorizedAttributes denied
// it -- distinct from (and counted independently of) the unauthorized-node,
// wrong-kind, or internal-bookkeeping exclusions the loop below also makes,
// none of which is an authorization event. Mirrors AdmitEdgesResult's own
// "return the dropped count as a plain value, let the I/O-boundary caller
// decide what to do with it" convention -- this function stays pure, no
// telemetry call of its own.
func DiscoveredCohort(principal storage.Principal, discovery contextfabric.GraphDiscoveryRequest, nodes []CandidateNode, isInternal func(contextfabric.SubjectRef) bool) (*contextfabric.Cohort, int) {
	if discovery.Interpretation.Shape != contextfabric.ShapeDiscoveredCohort && discovery.Interpretation.Shape != contextfabric.ShapeExplicitCohort {
		return nil, 0
	}
	kind := interpretedCohortKind(discovery.Interpretation)
	members := make([]contextfabric.CohortMember, 0)
	seen := make(map[string]struct{})
	authzDropped := 0
	for _, node := range nodes {
		if !AuthorizedAttributes(principal, discovery.Request.RequestedScope, node.Attributes) {
			authzDropped++
			continue
		}
		subject, ok := NodeSubject(node)
		if !ok || subject.Kind != kind || isInternal(subject) {
			continue
		}
		key := SubjectKey(subject)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		members = append(members, contextfabric.CohortMember{
			Subject: subject, Rank: len(members) + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested organization-level condition."},
			EvidenceRefIDs:   EvidenceRefs(node.Attributes),
		})
		if len(members) >= discovery.Request.Options.MaxCohortMembers {
			break
		}
	}
	if len(members) == 0 {
		return nil, authzDropped
	}
	return &contextfabric.Cohort{
		Kind: kind, Members: members, Exclusions: []contextfabric.CohortExclusion{},
		Rationale: "Subjects were discovered from the authorized Context Fabric graph using the user's open-ended cohort question.",
		Complete:  len(members) < discovery.Request.Options.MaxCohortMembers, Truncated: len(members) >= discovery.Request.Options.MaxCohortMembers,
	}, authzDropped
}

func interpretedCohortKind(interpreted contextfabric.InterpretedQuestion) contextfabric.SubjectKind {
	values := append([]string{interpreted.RequestedJudgment}, interpreted.SubjectTerms...)
	for _, value := range values {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "project") || strings.Contains(lower, "initiative") {
			return contextfabric.SubjectProject
		}
		if strings.Contains(lower, "team") || strings.Contains(lower, "group") {
			return contextfabric.SubjectTeam
		}
	}
	return contextfabric.SubjectTeam
}

func edgeEpistemicStatus(edge ResolvedEdge) contextfabric.EpistemicStatus {
	value := contextfabric.EpistemicStatus(StringAttribute(edge.Attributes, "epistemic_status"))
	switch value {
	case contextfabric.EpistemicObserved, contextfabric.EpistemicSourceAsserted, contextfabric.EpistemicInferred, contextfabric.EpistemicDisputed, contextfabric.EpistemicSuperseded, contextfabric.EpistemicUnknown:
		return value
	default:
		return contextfabric.EpistemicUnknown
	}
}

func edgeValidTo(edge ResolvedEdge) *time.Time {
	if parsed := ParseOptionalTimePtr(edge.InvalidAt); parsed != nil {
		return parsed
	}
	return ParseOptionalTimePtr(edge.ExpiredAt)
}

func edgeFact(edge ResolvedEdge) string {
	if strings.TrimSpace(edge.Fact) != "" {
		return strings.TrimSpace(edge.Fact)
	}
	return "The graph associated the two subjects through " + strings.ToLower(edge.Name) + "."
}

// relationMeaning classifies a graph edge into a driver candidate's
// standing, category, and the FactKind the edge suggests is worth
// requesting from the canonical fact registry.
//
// Category is always "relationship" here, regardless of which edge type
// matched: every candidate this feeds into is
// Derivation: DerivationGraphAssociated by construction (see AdmitEdges) --
// a graph-discovered association, not a canonical-fact-backed observation.
// Before main's CHAOS-3755 closed driver-category vocabulary
// (contractsv1.ContextFabricDriverCategoryRequiresClaimedFact /
// validDriverCategory), the more descriptive per-edge-type strings this
// function originally returned ("dependency"/"pressure"/"signal", ported
// unmodified from the pre-CHAOS-3755 zepgraph.relationMeaning this package
// was extracted from) were harmless free text. Once Category started
// gating BOTH a closed-enum-membership check and a ClaimedFactID
// requirement, those exact strings became invalid category values --
// driver.Validate() rejects every candidate silently (see the
// `if err := driver.Validate(); err == nil` guard in AdmitEdges),
// degrading graph-derived driver discovery repo-wide for every backend
// that calls AdmitEdges. factKind is unaffected: it still differentiates
// by edge type and is used only to request that FactKind from the
// canonical fact registry (requirements[factKind]), which is a request,
// not a claim.
// relationMeaningTable is THE driver-admission table (AC-3779-2: this is
// the only edge-type -> standing/category/factKind mapping in the
// repository -- grep-verified during CHAOS-3779; a duplicate is a defect,
// because a rebase silently reintroduced the pre-H4 strings from a
// duplicated copy once already). It is also the AC-3779-9 producer
// cross-check: every key here MUST have a real projection producer,
// verified by TestEveryRecognizedRelationshipTypeHasAProducer in
// cmd/acr-projector.
//
// Before CHAOS-3779 this table recognized nine types
// (BLOCKS/BLOCKED_BY/REQUIRES/DEPENDS_ON/CAUSES/CONTRIBUTES_TO/PRESSURES/
// INDICATES/SYMPTOM_OF) but only BLOCKS had a producer -- the other eight
// were dead code (drift item D12), silently falling every one of those
// edge types to the default context standing forever, because nothing
// ever wrote them. CHAOS-3779 prunes the eight unproducable entries
// instead of inventing producers with no deterministic source (§19.5.3
// lists no source for CAUSES/CONTRIBUTES_TO/PRESSURES/INDICATES/
// SYMPTOM_OF, and REQUIRES/DEPENDS_ON are synonym spellings
// work_item_dependencies never emits).
//
// BLOCKED_BY specifically was checked for the inverse-direction hazard
// before pruning (review caution: dropping it would be an H4-shaped defect
// IF the read path ever surfaced a 'blocks' row under an inverted name when
// traversal reached it from the blocked side). It does not: edgesOfNode
// (falkorgraph/queries.go) reads propRelationType verbatim off the stored
// edge in BOTH directions of its UNION query, and toCandidateEdge is the
// only call site in the repository that ever constructs a
// CandidateEdge.Name from a graph-read edge (grep-verified) -- there is no
// direction-conditional rewriting anywhere. A 'blocks' row always surfaces
// as literal "BLOCKS", correctly oriented, regardless of which endpoint's
// traversal found it. See falkorgraph's
// TestDiscoverContextFromBlockedSideStillSurfacesBLOCKSNotAnInvertedName,
// which proves this end to end from the blocked side. A recognizer
// entry with no producer is a defect, not a placeholder -- see
// ContextFabricRelationshipType's doc comment for the closed vocabulary
// this table draws its keys from.
//
// PART_OF (the other CHAOS-3779 producer, work_items.parent_id hierarchy)
// is intentionally absent: it is structural (a work-item hierarchy fact),
// not itself a driver signal the way a blocker is, so it stays a plain
// graph-associated path relationship without a DriverJudgment -- exactly
// like BELONGS_TO_REPOSITORY, CORRELATED_WITH_INCIDENT, RELATED_TO,
// RELATES_TO, and DUPLICATES.
var relationMeaningTable = map[string]struct {
	standing contextfabric.DriverStanding
	category string
	factKind contextfabric.FactKind
}{
	"BLOCKS": {contextfabric.DriverPrincipal, "relationship", contextfabric.FactBlockers},
}

// RecognizedRelationshipTypes returns relationMeaningTable's keys -- see
// its doc comment. Exported for the AC-3779-9 cross-wiring test in
// cmd/acr-projector, which is the only caller today.
func RecognizedRelationshipTypes() []string {
	types := make([]string, 0, len(relationMeaningTable))
	for name := range relationMeaningTable {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

func relationMeaning(name string) (contextfabric.DriverStanding, string, contextfabric.FactKind, bool) {
	entry, ok := relationMeaningTable[NormalizeRelation(name)]
	if !ok {
		return contextfabric.DriverContext, "relationship", contextfabric.FactEvidence, false
	}
	return entry.standing, entry.category, entry.factKind, true
}

func relationTitle(name, target string) string {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(NormalizeRelation(name), "_", " ")))
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ") + ": " + target
}
