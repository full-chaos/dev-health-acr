package zepgraph

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	zep "github.com/getzep/zep-go/v3"
)

func (a *Adapter) ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion) (contextfabric.SubjectResolution, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.SubjectResolution{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.SubjectResolution{}, err
	}
	terms := subjectTerms(request, interpreted)
	candidatesBySubject := make(map[string]contextfabric.SubjectCandidate)
	// callerSourced marks which resolved subjects came from a
	// caller-explicit hint -- any SubjectHint.Source other than
	// "prior_subject_receipt" (Engine's own marker for a
	// prior-subject-receipt expansion; see resolvePriorSubjectHints).
	// Round-2 findings N4/N5 both hinge on this distinction: a
	// caller-explicit hint is an authoritative, direct ask and keeps the
	// short-circuit/truncation-priority behavior below; a receipt-derived
	// hint is not -- it is Engine's best guess at what a conversational
	// reference bound to previously, and the current question may name a
	// different subject entirely.
	callerSourced := make(map[string]bool)
	for _, hint := range request.RequestedScope.SubjectHints {
		if strings.TrimSpace(hint.ID) == "" || hint.Kind == "" {
			continue
		}
		subject := contextfabric.SubjectRef{Kind: hint.Kind, CanonicalID: strings.TrimSpace(hint.ID), Label: strings.TrimSpace(hint.Label)}
		if subject.Label == "" {
			subject.Label = subject.CanonicalID
		}
		if strings.TrimSpace(hint.Source) != "prior_subject_receipt" {
			callerSourced[subjectKey(subject)] = true
		}
		node, err := a.api.GetNode(ctx, nodeUUID(principal.OrgID, subject))
		if err != nil {
			if zepStatusCode(err) == 404 {
				continue
			}
			return contextfabric.SubjectResolution{}, safeDependencyError("resolve exact subject hint", err)
		}
		candidate, ok := nodeCandidate(principal, request.RequestedScope, subject.Label, node)
		if !ok {
			continue
		}
		candidate.Confidence = 1
		candidate.State = contextfabric.ResolutionCommitted
		candidate.MatchReasons = []string{"Exact canonical subject hint matched the organization graph."}
		candidatesBySubject[subjectKey(candidate.Subject)] = candidate
	}
	// A caller-explicit hint that resolved is authoritative and
	// short-circuits here, exactly as before. A receipt-only resolution
	// (candidatesBySubject may be non-empty, but nothing in it came from a
	// caller-explicit hint) must NOT short-circuit -- N5 -- it falls
	// through to hybrid search below, which merges into the same map, so a
	// conversational follow-up naming a different subject than the one a
	// prior receipt bound can still be found and compete on its own terms.
	if anyCallerSourced(candidatesBySubject, callerSourced) {
		return finalizeExactResolution(candidatesBySubject, callerSourced, request.Options.MaxSubjectCandidates), nil
	}
	// observationHasParent tracks, per observation (document/episode)
	// subject key, whether traverseObservationToSubject found it a
	// canonical parent candidate this call. See the N1 commit-eligibility
	// rule below.
	observationHasParent := make(map[string]bool)
	for _, term := range terms {
		results, err := a.search(ctx, principal.OrgID, term, zep.GraphSearchScopeNodes, nil, request.Options.MaxSubjectCandidates)
		if err != nil {
			return contextfabric.SubjectResolution{}, err
		}
		for _, node := range results.Nodes {
			candidate, ok := nodeCandidate(principal, request.RequestedScope, term, node)
			if !ok {
				continue
			}
			key := subjectKey(candidate.Subject)
			if current, exists := candidatesBySubject[key]; !exists || candidate.Confidence > current.Confidence {
				candidatesBySubject[key] = candidate
			}
			// Observation-to-entity traversal: a hybrid match on a document
			// or episode node means the term appeared in text *about* some
			// canonical entity, not necessarily that the caller is asking
			// about the document/episode itself. Walk back to whichever
			// entity that observation is attached to and propose it as an
			// additional candidate (never a replacement -- a caller may
			// genuinely mean the document or episode).
			if isObservationSubjectKind(candidate.Subject.Kind) {
				if traversed, ok := a.traverseObservationToSubject(ctx, principal, request.RequestedScope, term, node); ok {
					observationHasParent[key] = true
					traversedKey := subjectKey(traversed.Subject)
					if current, exists := candidatesBySubject[traversedKey]; !exists || traversed.Confidence > current.Confidence {
						candidatesBySubject[traversedKey] = traversed
					}
				}
			}
		}
	}
	candidates := make([]contextfabric.SubjectCandidate, 0, len(candidatesBySubject))
	for _, candidate := range candidatesBySubject {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Confidence == candidates[j].Confidence {
			return subjectKey(candidates[i].Subject) < subjectKey(candidates[j].Subject)
		}
		return candidates[i].Confidence > candidates[j].Confidence
	})
	if len(candidates) > request.Options.MaxSubjectCandidates {
		candidates = candidates[:request.Options.MaxSubjectCandidates]
	}
	resolution := contextfabric.SubjectResolution{Candidates: candidates, Committed: []contextfabric.SubjectRef{}}
	for _, candidate := range candidates {
		// A receipt-derived hint (State=Committed, Confidence=1) that
		// survived into this merged set is trusted exactly like any other
		// exact match here -- this is the same fast path a caller-explicit
		// hint would have taken via the short-circuit above, just reached
		// after hybrid search also had a chance to run and merge in
		// anything else the interpretation named.
		if candidate.State == contextfabric.ResolutionCommitted && candidate.Confidence == 1 {
			resolution.Committed = append(resolution.Committed, candidate.Subject)
		}
	}
	if len(resolution.Committed) > 0 {
		return resolution, nil
	}
	if len(candidates) == 0 {
		return resolution, nil
	}
	// Observation-kind subjects (documents, episodes) are auto-commit-
	// eligible via the confidence heuristics below ONLY when traversal
	// found no canonical parent for them (N1): if none exists, the
	// document/episode itself is the best -- often only -- answer, and
	// must be able to resolve a question genuinely about it. When a parent
	// candidate DOES exist, the observation stays excluded so the parent
	// (necessarily lower-confidence, being one hop removed) still gets the
	// chance to compete and win on its own terms, rather than the
	// (higher-relevance-by-construction) observation always outranking it
	// by raw score alone -- see traverseObservationToSubject.
	commitIndex := make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		if isObservationSubjectKind(candidate.Subject.Kind) && observationHasParent[subjectKey(candidate.Subject)] {
			continue
		}
		commitIndex = append(commitIndex, index)
	}
	commit := func(index int) contextfabric.SubjectResolution {
		candidates[index].State = contextfabric.ResolutionCommitted
		resolution.Candidates = candidates
		resolution.Committed = []contextfabric.SubjectRef{candidates[index].Subject}
		return resolution
	}
	if len(commitIndex) == 1 && candidates[commitIndex[0]].Confidence >= 0.72 {
		return commit(commitIndex[0]), nil
	}
	if len(commitIndex) >= 2 {
		top, second := candidates[commitIndex[0]], candidates[commitIndex[1]]
		if gap := top.Confidence - second.Confidence; top.Confidence >= 0.88 && gap >= 0.12 {
			return commit(commitIndex[0]), nil
		}
	}
	for index := range candidates {
		candidates[index].State = contextfabric.ResolutionAmbiguous
	}
	resolution.Candidates = candidates
	if request.Options.AllowClarification {
		resolution.ClarificationPrompt = clarificationPrompt(candidates)
	}
	return resolution, nil
}

// anyCallerSourced reports whether any resolved candidate came from a
// caller-explicit hint (see callerSourced in ResolveSubjects).
func anyCallerSourced(candidatesBySubject map[string]contextfabric.SubjectCandidate, callerSourced map[string]bool) bool {
	for key := range candidatesBySubject {
		if callerSourced[key] {
			return true
		}
	}
	return false
}

// finalizeExactResolution implements N4's two-class truncation: when the
// resolved exact-hint candidates exceed Options.MaxSubjectCandidates,
// caller-explicit hints are retained first (all of them, up to the bound);
// receipt-derived hints fill only the remaining room, never displacing a
// caller-explicit one. Order is otherwise deterministic (subjectKey) within
// each class.
func finalizeExactResolution(candidatesBySubject map[string]contextfabric.SubjectCandidate, callerSourced map[string]bool, max int) contextfabric.SubjectResolution {
	candidates := make([]contextfabric.SubjectCandidate, 0, len(candidatesBySubject))
	for _, candidate := range candidatesBySubject {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		iCaller, jCaller := callerSourced[subjectKey(candidates[i].Subject)], callerSourced[subjectKey(candidates[j].Subject)]
		if iCaller != jCaller {
			return iCaller
		}
		return subjectKey(candidates[i].Subject) < subjectKey(candidates[j].Subject)
	})
	if max > 0 && len(candidates) > max {
		candidates = candidates[:max]
	}
	committed := make([]contextfabric.SubjectRef, 0, len(candidates))
	for _, candidate := range candidates {
		committed = append(committed, candidate.Subject)
	}
	return contextfabric.SubjectResolution{Candidates: candidates, Committed: committed}
}

func (a *Adapter) DiscoverContext(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.GraphContext{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.GraphContext{}, err
	}
	originIDs := make([]string, 0, len(request.Resolution.Committed))
	for _, subject := range request.Resolution.Committed {
		originIDs = append(originIDs, nodeUUID(principal.OrgID, subject))
	}
	limit := request.Request.Options.MaxRelationshipPaths
	if limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	results, err := a.search(ctx, principal.OrgID, request.Request.Question, zep.GraphSearchScopeAuto, originIDs, limit)
	if err != nil {
		return contextfabric.GraphContext{}, err
	}
	nodes := make(map[string]*zep.EntityNode)
	for _, node := range results.Nodes {
		if node != nil && authorizedAttributes(principal, request.Request.RequestedScope, node.Attributes) {
			nodes[node.UUID] = node
		}
	}
	// N2: admission order must not depend on whatever order the backend
	// happened to return edges in. Under a scarce evidence budget, edge
	// order otherwise determines which edges win purely by being first,
	// not by relevance -- normalize by sorting on relevance (descending),
	// tie-broken by the edge's own stable UUID, before the admission loop
	// below ever runs.
	edges := make([]*zep.EntityEdge, 0, len(results.Edges))
	for _, edge := range results.Edges {
		if edge != nil {
			edges = append(edges, edge)
		}
	}
	sort.SliceStable(edges, func(i, j int) bool {
		ri, rj := resultConfidence(edges[i].Relevance, edges[i].Score), resultConfidence(edges[j].Relevance, edges[j].Score)
		if ri != rj {
			return ri > rj
		}
		return edges[i].UUID < edges[j].UUID
	})
	paths := make([]contextfabric.RelationshipPath, 0, len(edges))
	drivers := make([]contextfabric.DriverJudgment, 0, len(edges))
	evidenceSet := make(map[string]struct{})
	requirements := make(map[contextfabric.FactKind]contextfabric.FactRequirement)
	// secondHopVerificationFailures counts edges dropped because a
	// second-hop node (see verifiedNodeSubject) did not verify as
	// belonging to this organization's graph -- N6. The count (never the
	// node/edge content) is surfaced through Coverage below so a caller
	// can tell degradation happened instead of an investigation silently
	// looking complete with fewer paths/drivers than the graph actually
	// held.
	secondHopVerificationFailures := 0
	for _, edge := range edges {
		if !authorizedAttributes(principal, request.Request.RequestedScope, edge.Attributes) {
			continue
		}
		// from/to found directly in nodes are first-hop results from
		// Search, which is itself scoped by GraphID -- trusted as
		// belonging to principal.OrgID's graph. from/to reached only
		// through the fallback GetNode call are second-hop: GetNode has no
		// per-call graph/organization parameter, so those additionally
		// require verifiedNodeSubject below before being trusted.
		from, fromFirstHop := nodes[edge.SourceNodeUUID]
		if !fromFirstHop {
			from, _ = a.api.GetNode(ctx, edge.SourceNodeUUID)
			if from == nil || !authorizedAttributes(principal, request.Request.RequestedScope, from.Attributes) {
				continue
			}
		}
		to, toFirstHop := nodes[edge.TargetNodeUUID]
		if !toFirstHop {
			to, _ = a.api.GetNode(ctx, edge.TargetNodeUUID)
			if to == nil || !authorizedAttributes(principal, request.Request.RequestedScope, to.Attributes) {
				continue
			}
		}
		var fromSubject, toSubject contextfabric.SubjectRef
		var ok bool
		if fromFirstHop {
			fromSubject, ok = nodeSubject(from)
		} else {
			fromSubject, ok = verifiedNodeSubject(principal.OrgID, edge.SourceNodeUUID, from)
			if !ok {
				secondHopVerificationFailures++
			}
		}
		if !ok || isInternalBookkeepingSubject(fromSubject) {
			continue
		}
		if toFirstHop {
			toSubject, ok = nodeSubject(to)
		} else {
			toSubject, ok = verifiedNodeSubject(principal.OrgID, edge.TargetNodeUUID, to)
			if !ok {
				secondHopVerificationFailures++
			}
		}
		if !ok || fromSubject == toSubject || isInternalBookkeepingSubject(toSubject) {
			continue
		}
		evidence := edgeEvidence(edge)
		if len(evidence) == 0 {
			continue
		}
		// Codex finding G5: Options.MaxEvidenceRefs must bound the FINAL
		// result's entire evidence surface -- every path's and driver's
		// own EvidenceRefIDs, not just the separately truncated aggregate
		// list below. Checked here, before a path/driver is admitted at
		// all, against the *projected* size (evidenceSet is not mutated
		// yet -- N3, below) so Paths, DriverCandidates, and the aggregate
		// EvidenceRefIDs stay consistent with the same bounded evidence
		// set by construction.
		if maxEvidence := request.Request.Options.MaxEvidenceRefs; maxEvidence > 0 {
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
			Type: normalizeRelation(edge.Name), From: fromSubject, To: toSubject,
			Derivation: contextfabric.DerivationGraphAssociated, EpistemicStatus: edgeEpistemicStatus(edge),
			ObservedAt: parseOptionalTime(edge.CreatedAt), ValidFrom: parseOptionalTimePtr(edge.ValidAt), ValidTo: edgeValidTo(edge),
			EvidenceRefIDs: evidence,
		}
		pathID := deterministicUUID("context-fabric-path", principal.OrgID, edge.UUID)
		path := contextfabric.RelationshipPath{
			PathID: pathID, Nodes: []contextfabric.SubjectRef{fromSubject, toSubject}, Edges: []contextfabric.RelationshipEdge{relationship},
			WhyRelevant: edgeFact(edge), EvidenceRefIDs: evidence, Truncated: false,
		}
		if err := path.Validate(); err != nil {
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
				DriverID: deterministicUUID("context-fabric-driver", principal.OrgID, edge.UUID), Standing: standing, Category: category,
				Title: relationTitle(edge.Name, toSubject.Label), Summary: edgeFact(edge), AffectedSubjects: []contextfabric.SubjectRef{fromSubject},
				PathIDs: []string{pathID}, EvidenceRefIDs: evidence, Derivation: contextfabric.DerivationGraphAssociated,
				EpistemicStatus: contextfabric.EpistemicInferred, Confidence: resultConfidence(edge.Relevance, edge.Score), Current: edge.InvalidAt == nil && edge.ExpiredAt == nil,
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
	if len(paths) > request.Request.Options.MaxRelationshipPaths {
		paths = paths[:request.Request.Options.MaxRelationshipPaths]
	}
	if len(drivers) > request.Request.Options.MaxDrivers {
		drivers = drivers[:request.Request.Options.MaxDrivers]
	}
	cohort := a.discoveredCohort(principal, request, results.Nodes)
	if cohort != nil {
		requirements[contextfabric.FactHealth] = contextfabric.FactRequirement{Kind: contextfabric.FactHealth}
		requirements[contextfabric.FactWorkload] = contextfabric.FactRequirement{Kind: contextfabric.FactWorkload}
	}
	factRequirements := make([]contextfabric.FactRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		factRequirements = append(factRequirements, requirement)
	}
	sort.Slice(factRequirements, func(i, j int) bool { return factRequirements[i].Kind < factRequirements[j].Kind })
	// evidenceSet is already within Options.MaxEvidenceRefs by
	// construction (see the admission check above); no further truncation
	// is needed or correct here -- truncating post hoc would desync the
	// aggregate list from the paths/drivers that were actually admitted.
	evidence := make([]string, 0, len(evidenceSet))
	for id := range evidenceSet {
		evidence = append(evidence, id)
	}
	sort.Strings(evidence)
	degradedReasons := []string{}
	partial := false
	if secondHopVerificationFailures > 0 {
		// N6: content-safe (count + fixed reason token, never node/edge
		// content) signal that some second-hop graph data was dropped
		// rather than trusted, so a later synthesis stage (Reset 1C) can
		// surface this as a limitation instead of the investigation
		// silently looking complete with fewer paths/drivers than the
		// graph actually held.
		partial = true
		degradedReasons = []string{fmt.Sprintf("second_hop_node_unverified:%d", secondHopVerificationFailures)}
	}
	return contextfabric.GraphContext{
		Resolution: request.Resolution, Cohort: cohort, Paths: paths, DriverCandidates: drivers,
		EvidenceRefIDs: evidence, FactRequirements: factRequirements,
		Coverage: contextfabric.Coverage{
			// Source and Watermark land verbatim in the public
			// InvestigationResult.Coverage, so neither may name the backing
			// graph vendor or leak its internal graph identifier: "graph" is
			// the vendor-neutral source name, and Watermark stays empty
			// until a real, non-identifying watermark value exists.
			Sources:         []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptr(a.now().UTC())}},
			Partial:         partial,
			DegradedReasons: degradedReasons,
		},
	}, nil
}

func (a *Adapter) search(ctx context.Context, orgID, query string, scope zep.GraphSearchScope, origins []string, limit int) (*zep.GraphSearchResults, error) {
	if limit < 1 {
		limit = a.config.MaxResults
	}
	if limit > 50 {
		limit = 50
	}
	reranker := zep.RerankerRrf
	returnRaw := true
	request := &zep.GraphSearchQuery{
		GraphID: ptr(graphID(a.config.GraphPrefix, orgID)), Query: strings.TrimSpace(query), Scope: &scope,
		Limit: &limit, Reranker: &reranker, ReturnRawResults: &returnRaw, BfsOriginNodeUUIDs: append([]string(nil), origins...),
	}
	if request.Query == "" {
		request.Query = "current engineering context"
	}
	results, err := a.api.Search(ctx, request)
	if err != nil {
		return nil, safeDependencyError("search context graph", err)
	}
	if results == nil {
		return &zep.GraphSearchResults{}, nil
	}
	return results, nil
}

func (a *Adapter) discoveredCohort(principal storage.Principal, request contextfabric.GraphDiscoveryRequest, nodes []*zep.EntityNode) *contextfabric.Cohort {
	if request.Interpretation.Shape != contextfabric.ShapeDiscoveredCohort && request.Interpretation.Shape != contextfabric.ShapeExplicitCohort {
		return nil
	}
	kind := interpretedCohortKind(request.Interpretation)
	members := make([]contextfabric.CohortMember, 0)
	seen := make(map[string]struct{})
	for _, node := range nodes {
		if node == nil || !authorizedAttributes(principal, request.Request.RequestedScope, node.Attributes) {
			continue
		}
		subject, ok := nodeSubject(node)
		if !ok || subject.Kind != kind || isInternalBookkeepingSubject(subject) {
			continue
		}
		key := subjectKey(subject)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		members = append(members, contextfabric.CohortMember{
			Subject: subject, Rank: len(members) + 1,
			InclusionReasons: []string{"Graph retrieval associated this subject with the requested organization-level condition."},
			EvidenceRefIDs:   decodeScope(stringAttribute(node.Attributes, "evidence_refs")),
		})
		if len(members) >= request.Request.Options.MaxCohortMembers {
			break
		}
	}
	if len(members) == 0 {
		return nil
	}
	return &contextfabric.Cohort{
		Kind: kind, Members: members, Exclusions: []contextfabric.CohortExclusion{},
		Rationale: "Subjects were discovered from the authorized Context Fabric graph using the user's open-ended cohort question.",
		Complete:  len(members) < request.Request.Options.MaxCohortMembers, Truncated: len(members) >= request.Request.Options.MaxCohortMembers,
	}
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

func subjectTerms(request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion) []string {
	values := append([]string(nil), interpreted.SubjectTerms...)
	for _, hint := range request.RequestedScope.SubjectHints {
		if strings.TrimSpace(hint.Label) != "" {
			values = append(values, hint.Label)
		}
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func nodeCandidate(principal storage.Principal, scope contextfabric.RequestedScope, term string, node *zep.EntityNode) (contextfabric.SubjectCandidate, bool) {
	if node == nil || !authorizedAttributes(principal, scope, node.Attributes) {
		return contextfabric.SubjectCandidate{}, false
	}
	subject, ok := nodeSubject(node)
	if !ok || isInternalBookkeepingSubject(subject) {
		return contextfabric.SubjectCandidate{}, false
	}
	confidence := resultConfidence(node.Relevance, node.Score)
	matched := strings.EqualFold(strings.TrimSpace(term), node.Name) || strings.EqualFold(strings.TrimSpace(term), subject.Label)
	if matched {
		confidence = 1
	}
	if confidence == 0 {
		confidence = 0.5
	}
	reason := "Hybrid graph search matched the subject label or indexed context."
	if matched {
		reason = "Exact canonical subject label match."
	}
	return contextfabric.SubjectCandidate{
		ReceiptID: deterministicUUID("context-fabric-subject-receipt", node.UUID, strings.ToLower(term)),
		Subject:   subject, State: contextfabric.ResolutionProposed,
		MatchedTerms: []string{term}, MatchReasons: []string{reason}, Confidence: confidence,
		EvidenceRefIDs: decodeScope(stringAttribute(node.Attributes, "evidence_refs")),
	}, true
}

func nodeSubject(node *zep.EntityNode) (contextfabric.SubjectRef, bool) {
	kind := contextfabric.SubjectKind(stringAttribute(node.Attributes, "subject_kind"))
	canonicalID := strings.TrimSpace(stringAttribute(node.Attributes, "canonical_id"))
	label := strings.TrimSpace(stringAttribute(node.Attributes, "label"))
	if label == "" {
		label = strings.TrimSpace(node.Name)
	}
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: canonicalID, Label: label}
	if err := subject.Validate(); err != nil {
		return contextfabric.SubjectRef{}, false
	}
	return subject, true
}

// verifiedNodeSubject is nodeSubject plus organization-identity
// verification for a "second-hop" node: one fetched by a UUID this adapter
// did not itself derive (e.g. edge.SourceNodeUUID/TargetNodeUUID from a
// search result, or GetNodeEdges), as opposed to a UUID this adapter
// computed itself via nodeUUID(orgID, subject) (which is trivially always
// correct for orgID, since the adapter chose it).
//
// GetNode and GetNodeEdges are UUID-only lookups with no per-call
// graph/organization parameter, unlike Search, which is scoped by GraphID.
// That makes a second-hop lookup the one place a node genuinely belonging
// to a different organization's graph -- reached through a compromised or
// misbehaving backend response, not through any fault of Search's own
// GraphID scoping -- could be trusted without this check. Because
// nodeUUID is a keyed SHA-256 digest of organization ID + subject kind +
// canonical ID, only a node whose own reported attributes hash back to the
// UUID it was actually fetched under can pass.
func verifiedNodeSubject(orgID, fetchedUUID string, node *zep.EntityNode) (contextfabric.SubjectRef, bool) {
	subject, ok := nodeSubject(node)
	if !ok {
		return contextfabric.SubjectRef{}, false
	}
	if nodeUUID(orgID, subject) != fetchedUUID {
		return contextfabric.SubjectRef{}, false
	}
	return subject, true
}

// isInternalBookkeepingSubject reports whether subject is one of the
// adapter's own internal marker nodes (organizationRoot, markerSubject in
// identity.go) rather than a real canonical entity. These nodes exist only
// so projection has an anchor node for organization-scoped triples (the
// "HAS_SUBJECT" root edge, projection watermarks); a caller can never
// usefully mean one of them by name, and surfacing them as a subject
// candidate or a relationship endpoint would leak adapter-internal
// bookkeeping into a public result.
func isInternalBookkeepingSubject(subject contextfabric.SubjectRef) bool {
	// Matched on CanonicalID alone, not gated on the reported Kind also
	// being Organization/Metric. organizationRoot/markerSubject
	// (identity.go) only ever write these reserved canonical_id values
	// paired with those kinds -- but a node's own subject_kind is just
	// another attribute read back off the wire (see nodeSubject), not
	// something this adapter can independently verify. Requiring an exact
	// Kind match here would let a node that reports some OTHER kind (a
	// bug in a differently-configured write path, or a deliberately
	// malformed one) bypass the exclusion while still carrying one of
	// these reserved identifiers, so the identifier itself is treated as
	// reserved regardless of what kind accompanies it. Normalized
	// case-insensitively for the same reason: the write path never
	// legitimately produces anything but the exact lowercase form, but
	// nothing structurally prevents a differently-cased value from
	// reaching this check.
	canonicalID := strings.ToLower(subject.CanonicalID)
	if canonicalID == "organization-root" {
		return true
	}
	if strings.HasPrefix(canonicalID, "projection-watermark:") {
		return true
	}
	return false
}

// isObservationSubjectKind reports whether kind describes an observation
// about a canonical entity (a document or episode) rather than a
// first-class subject in its own right. See traverseObservationToSubject.
func isObservationSubjectKind(kind contextfabric.SubjectKind) bool {
	return kind == contextfabric.SubjectDocument || kind == contextfabric.SubjectEpisode
}

// isObservationAttributionRelation reports whether name is one of the
// specific relation kinds projectContent/projectEpisode use to attach a
// document or episode to the canonical subject it is authoritatively about
// ("DOCUMENTED_BY", "HAS_EPISODE"). traverseObservationToSubject must not
// follow any other edge that happens to point at an observation node --
// e.g. a generic MENTIONS/REFERENCES/SUPERSEDES relationship between two
// documents -- since those describe a much weaker, not-necessarily-singular
// association than "this is the entity's own document".
func isObservationAttributionRelation(name string) bool {
	switch normalizeRelation(name) {
	case "DOCUMENTED_BY", "HAS_EPISODE":
		return true
	default:
		return false
	}
}

// traverseObservationToSubject implements observation-to-entity traversal:
// given a document/episode node that a hybrid search matched on its text
// (title, body, goal, outcome, summary), it walks the node's incoming edge
// back to the canonical entity that document/episode is projected against
// (projectContent/projectEpisode always set that entity as the edge's
// source and the document/episode as the target) and proposes that entity
// as an additional subject candidate. This lets a term that only appears
// inside a document body or episode summary still resolve to the subject
// the question is actually about, without requiring the term to match the
// subject's own label, alias, or previous name directly.
//
// The traversed entity is independently re-authorized (authorizedAttributes
// via nodeCandidate) before it can become a candidate -- the caller's
// authorization to see the observation never carries over to the entity it
// describes.
func (a *Adapter) traverseObservationToSubject(ctx context.Context, principal storage.Principal, scope contextfabric.RequestedScope, term string, observation *zep.EntityNode) (contextfabric.SubjectCandidate, bool) {
	if observation == nil || strings.TrimSpace(observation.UUID) == "" {
		return contextfabric.SubjectCandidate{}, false
	}
	edges, err := a.api.GetNodeEdges(ctx, observation.UUID)
	if err != nil {
		return contextfabric.SubjectCandidate{}, false
	}
	for _, edge := range edges {
		if edge == nil || edge.TargetNodeUUID != observation.UUID || strings.TrimSpace(edge.SourceNodeUUID) == "" {
			continue
		}
		if !isObservationAttributionRelation(edge.Name) {
			continue
		}
		// The attribution edge is its own authorization boundary,
		// independent of either endpoint's own scope: a source node and a
		// document can each be individually unrestricted while the fact
		// "this document belongs to this subject" is itself scoped more
		// narrowly. Skipping this check would let a principal who can see
		// both nodes separately learn the relationship between them
		// regardless of whether they are authorized to see that
		// relationship specifically.
		if !authorizedAttributes(principal, scope, edge.Attributes) {
			continue
		}
		source, err := a.api.GetNode(ctx, edge.SourceNodeUUID)
		if err != nil || source == nil {
			continue
		}
		// GetNode is a second-hop, UUID-only lookup (see
		// verifiedNodeSubject's doc comment); verify the fetched node
		// actually belongs to this organization before trusting it as the
		// document/episode's canonical subject.
		if _, verified := verifiedNodeSubject(principal.OrgID, edge.SourceNodeUUID, source); !verified {
			continue
		}
		candidate, ok := nodeCandidate(principal, scope, term, source)
		if !ok || isObservationSubjectKind(candidate.Subject.Kind) {
			continue
		}
		// One hop removed from a direct label/alias/text match, so the
		// traversed candidate never outranks a subject the search matched
		// directly.
		candidate.Confidence *= 0.85
		candidate.MatchReasons = []string{"Matched an associated document or episode that references this subject."}
		return candidate, true
	}
	return contextfabric.SubjectCandidate{}, false
}

func authorizedAttributes(principal storage.Principal, requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	if len(principal.RepositoryScopes) > 0 {
		encoded := stringAttribute(attributes, "authorization_repositories")
		allowed := false
		for _, repository := range principal.RepositoryScopes {
			if scopeContains(encoded, repository) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if len(requested.RepositorySlugs) > 0 {
		encoded := stringAttribute(attributes, "authorization_repositories")
		if !anyScope(encoded, requested.RepositorySlugs) {
			return false
		}
	}
	if len(requested.ProjectIDs) > 0 {
		encoded := stringAttribute(attributes, "authorization_projects")
		if !anyScope(encoded, requested.ProjectIDs) {
			return false
		}
	}
	if len(requested.TeamIDs) > 0 {
		encoded := stringAttribute(attributes, "authorization_teams")
		if !anyScope(encoded, requested.TeamIDs) {
			return false
		}
	}
	return true
}

func anyScope(encoded string, values []string) bool {
	for _, value := range values {
		if scopeContains(encoded, value) {
			return true
		}
	}
	return false
}

func clarificationPrompt(candidates []contextfabric.SubjectCandidate) string {
	labels := make([]string, 0, min(3, len(candidates)))
	for _, candidate := range candidates {
		labels = append(labels, candidate.Subject.Label)
		if len(labels) == 3 {
			break
		}
	}
	return "Which subject did you mean: " + strings.Join(labels, ", ") + "?"
}

func edgeEvidence(edge *zep.EntityEdge) []string {
	// Zep episode UUIDs are backend-native provenance and are never promoted to
	// canonical Dev Health evidence identifiers. Only ACR-projected evidence
	// references may close a public relationship or driver claim.
	return uniqueSorted(decodeScope(stringAttribute(edge.Attributes, "evidence_refs")))
}

func edgeFact(edge *zep.EntityEdge) string {
	if strings.TrimSpace(edge.Fact) != "" {
		return strings.TrimSpace(edge.Fact)
	}
	return "The graph associated the two subjects through " + strings.ToLower(edge.Name) + "."
}

func edgeEpistemicStatus(edge *zep.EntityEdge) contextfabric.EpistemicStatus {
	value := contextfabric.EpistemicStatus(stringAttribute(edge.Attributes, "epistemic_status"))
	switch value {
	case contextfabric.EpistemicObserved, contextfabric.EpistemicSourceAsserted, contextfabric.EpistemicInferred, contextfabric.EpistemicDisputed, contextfabric.EpistemicSuperseded, contextfabric.EpistemicUnknown:
		return value
	default:
		return contextfabric.EpistemicUnknown
	}
}

func edgeValidTo(edge *zep.EntityEdge) *time.Time {
	if parsed := parseOptionalTimePtr(edge.InvalidAt); parsed != nil {
		return parsed
	}
	return parseOptionalTimePtr(edge.ExpiredAt)
}

func parseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func parseOptionalTimePtr(value *string) *time.Time {
	if value == nil {
		return nil
	}
	return parseOptionalTime(*value)
}

func resultConfidence(relevance, score *float64) float64 {
	if relevance != nil && !math.IsNaN(*relevance) && !math.IsInf(*relevance, 0) {
		return clamp(*relevance)
	}
	if score != nil && !math.IsNaN(*score) && !math.IsInf(*score, 0) {
		if *score >= 0 && *score <= 1 {
			return *score
		}
		if *score > 1 {
			return clamp(1 / *score)
		}
	}
	return 0
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func relationMeaning(name string) (contextfabric.DriverStanding, string, contextfabric.FactKind, bool) {
	normalized := normalizeRelation(name)
	switch normalized {
	case "BLOCKS", "BLOCKED_BY", "REQUIRES", "DEPENDS_ON":
		return contextfabric.DriverPrincipal, "dependency", contextfabric.FactBlockers, true
	case "CAUSES", "CONTRIBUTES_TO", "PRESSURES":
		return contextfabric.DriverContributing, "pressure", contextfabric.FactHealth, true
	case "INDICATES", "SYMPTOM_OF":
		return contextfabric.DriverSymptom, "signal", contextfabric.FactMetrics, true
	default:
		return contextfabric.DriverContext, "relationship", contextfabric.FactEvidence, false
	}
}

func relationTitle(name, target string) string {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(normalizeRelation(name), "_", " ")))
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ") + ": " + target
}

func decodeScope(encoded string) []string {
	if encoded == "" || encoded == "*" || encoded == scopeDeniedSentinel {
		return []string{}
	}
	parts := strings.Split(encoded, scopeSeparator)
	return uniqueSorted(parts)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "*" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ contextfabric.GraphReader = (*Adapter)(nil)
