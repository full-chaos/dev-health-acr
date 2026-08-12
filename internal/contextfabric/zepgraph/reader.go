package zepgraph

import (
	"context"
	"errors"
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
	terms := subjectTerms(request, interpreted)
	candidatesBySubject := make(map[string]contextfabric.SubjectCandidate)
	for _, hint := range request.RequestedScope.SubjectHints {
		if strings.TrimSpace(hint.ID) == "" || hint.Kind == "" {
			continue
		}
		subject := contextfabric.SubjectRef{Kind: hint.Kind, CanonicalID: strings.TrimSpace(hint.ID), Label: strings.TrimSpace(hint.Label)}
		if subject.Label == "" {
			subject.Label = subject.CanonicalID
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
	if len(candidatesBySubject) > 0 {
		candidates := make([]contextfabric.SubjectCandidate, 0, len(candidatesBySubject))
		committed := make([]contextfabric.SubjectRef, 0, len(candidatesBySubject))
		for _, candidate := range candidatesBySubject {
			candidates = append(candidates, candidate)
			committed = append(committed, candidate.Subject)
		}
		sort.Slice(candidates, func(i, j int) bool { return subjectKey(candidates[i].Subject) < subjectKey(candidates[j].Subject) })
		sort.Slice(committed, func(i, j int) bool { return subjectKey(committed[i]) < subjectKey(committed[j]) })
		return contextfabric.SubjectResolution{Candidates: candidates, Committed: committed}, nil
	}
	if len(terms) == 0 {
		return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}, nil
	}
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
	if len(candidates) == 1 && candidates[0].Confidence >= 0.72 {
		candidates[0].State = contextfabric.ResolutionCommitted
		resolution.Candidates = candidates
		resolution.Committed = []contextfabric.SubjectRef{candidates[0].Subject}
		return resolution, nil
	}
	gap := candidates[0].Confidence - candidates[1].Confidence
	if candidates[0].Confidence >= 0.88 && gap >= 0.12 {
		candidates[0].State = contextfabric.ResolutionCommitted
		resolution.Candidates = candidates
		resolution.Committed = []contextfabric.SubjectRef{candidates[0].Subject}
		return resolution, nil
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

func (a *Adapter) DiscoverContext(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.GraphContext{}, errors.New("authenticated organization is required")
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
	paths := make([]contextfabric.RelationshipPath, 0, len(results.Edges))
	drivers := make([]contextfabric.DriverJudgment, 0, len(results.Edges))
	evidenceSet := make(map[string]struct{})
	requirements := make(map[contextfabric.FactKind]contextfabric.FactRequirement)
	for _, edge := range results.Edges {
		if edge == nil || !authorizedAttributes(principal, request.Request.RequestedScope, edge.Attributes) {
			continue
		}
		from, ok := nodes[edge.SourceNodeUUID]
		if !ok {
			from, _ = a.api.GetNode(ctx, edge.SourceNodeUUID)
			if from == nil || !authorizedAttributes(principal, request.Request.RequestedScope, from.Attributes) {
				continue
			}
		}
		to, ok := nodes[edge.TargetNodeUUID]
		if !ok {
			to, _ = a.api.GetNode(ctx, edge.TargetNodeUUID)
			if to == nil || !authorizedAttributes(principal, request.Request.RequestedScope, to.Attributes) {
				continue
			}
		}
		fromSubject, ok := nodeSubject(from)
		if !ok || isInternalBookkeepingSubject(fromSubject) {
			continue
		}
		toSubject, ok := nodeSubject(to)
		if !ok || fromSubject == toSubject || isInternalBookkeepingSubject(toSubject) {
			continue
		}
		evidence := edgeEvidence(edge)
		if len(evidence) == 0 {
			continue
		}
		for _, id := range evidence {
			evidenceSet[id] = struct{}{}
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
	evidence := make([]string, 0, len(evidenceSet))
	for id := range evidenceSet {
		evidence = append(evidence, id)
	}
	sort.Strings(evidence)
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
			DegradedReasons: []string{},
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
		if !ok || subject.Kind != kind {
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

// isInternalBookkeepingSubject reports whether subject is one of the
// adapter's own internal marker nodes (organizationRoot, markerSubject in
// identity.go) rather than a real canonical entity. These nodes exist only
// so projection has an anchor node for organization-scoped triples (the
// "HAS_SUBJECT" root edge, projection watermarks); a caller can never
// usefully mean one of them by name, and surfacing them as a subject
// candidate or a relationship endpoint would leak adapter-internal
// bookkeeping into a public result.
func isInternalBookkeepingSubject(subject contextfabric.SubjectRef) bool {
	if subject.Kind == contextfabric.SubjectOrganization && subject.CanonicalID == "organization-root" {
		return true
	}
	if subject.Kind == contextfabric.SubjectMetric && strings.HasPrefix(subject.CanonicalID, "projection-watermark:") {
		return true
	}
	return false
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
