package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// isInternalSubject always reports false: falkorgraph has no anchor/marker
// nodes the way zepgraph did (organizationRoot, projection-watermark
// subject) -- those existed only because Zep's AddFactTriple forced every
// fact to have a source+target node. This adapter's watermark is its own
// reserved-label node (labelWatermark), never a :Subject node, so it can
// never surface as a subject candidate or relationship endpoint in the
// first place; there is nothing here to filter.
func isInternalSubject(contextfabric.SubjectRef) bool { return false }

// graphNotProjectedError translates ErrNotFound -- classifyFalkorError's own
// unambiguous "GRAPH.RO_QUERY against a graph key that never existed"
// classification (client.go's own doc comment) -- into the backend-neutral
// contextfabric.ErrGraphNotProjected sentinel (CHAOS-4077), so Engine can
// recognize a never-projected org without importing this package. Used
// ONLY at the two investigation-time read boundaries where a missing key
// genuinely means "this org has no projection yet" (ResolveSubjects,
// DiscoverContext below) -- never a blanket replacement for
// safeDependencyError, whose other call sites (constraint creation, list
// graphs, delete) have their own, different meaning for the same
// underlying FalkorDB error. Any error OTHER than ErrNotFound passes
// through unchanged, so a genuine rate limit, auth failure, or timeout is
// never misread as "no such graph".
func graphNotProjectedError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %w", err, contextfabric.ErrGraphNotProjected)
	}
	return err
}

// ResolveInvestigationBinding implements contextfabric.GraphReader (CHAOS-3898
// §2.1). It resolves the org's CURRENT ResolvedGraphBinding exactly the way
// resolveReadKey always has (KeyResolver, design brief §3.1; a nil
// Config.EpochResolver -- every production composition root today -- falls
// back to epoch 0's key, byte-identical to pre-CHAOS-3898 behavior) and
// stamps the SAME cf_resolved_graph_key/cf_graph_key_divergence telemetry
// resolveReadKey always has. The difference is WHO calls it: Engine now
// calls this once, itself, before either graph method below, and passes the
// result back in -- ResolveSubjects/DiscoverContext no longer resolve their
// own key.
func (a *Adapter) ResolveInvestigationBinding(ctx context.Context, principal storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.ResolvedGraphBinding{}, errors.New("authenticated organization is required")
	}
	epoch, err := a.resolveActiveEpoch(ctx, principal.OrgID)
	if err != nil {
		return contextfabric.ResolvedGraphBinding{}, err
	}
	key := graphKeyForEpoch(a.config.GraphPrefix, principal.OrgID, epoch)
	a.stampResolvedKey(ctx, principal.OrgID, epoch, contextfabric.GraphKeyRoleInvestigationRead, key)
	return contextfabric.ResolvedGraphBinding{GraphKey: key, Epoch: epoch}, nil
}

func (a *Adapter) ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, binding contextfabric.ResolvedGraphBinding, confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection, frame *contextfabric.QuestionFrame, scopeAnchorKind contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	// CHAOS-3898 §2.1: the binding was already resolved ONCE by Engine, via
	// ResolveInvestigationBinding above, before this call -- never
	// re-resolved here. See ResolvedGraphBinding's own doc comment for the
	// race that independent per-call resolution (this method's pre-S2
	// behavior) left open. effectiveKey's fallback exists only for a
	// direct/test caller that bypasses Engine and supplies a zero-value
	// binding -- see that method's own doc comment.
	key, err := a.effectiveKey(ctx, principal.OrgID, binding)
	if err != nil {
		return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, nil, nil, err
	}
	// One fence verification per resolution, not per term (codex round-2
	// R2-1). Scoped to this call and never shared across requests.
	fence := &resolutionFence{}
	// CHAOS-3781: the window comes from the INTERPRETED question, never
	// the wire request. A caller may send axis=current for a question
	// whose text is historical; the interpreter is what settles which
	// time this investigation is actually about, and the engine refuses
	// any interpreted historical axis it cannot bound (AC-3781-3: a
	// subject outside the window simply stops resolving here).
	temporal := newTemporalFilter(interpreted.TimeContext)
	deps := graphrank.ResolveDeps{
		ExactHint: func(ctx context.Context, subject contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
			cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) WHERE true%s RETURN n",
				labelSubject, propOrgID, propKind, propCanonicalID, temporal.predicate("n"))
			rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": principal.OrgID, "kind": string(subject.Kind), "id": subject.CanonicalID}), true)
			if err != nil {
				return graphrank.CandidateNode{}, false, safeDependencyError("resolve exact subject hint", err)
			}
			if len(rows) == 0 {
				return graphrank.CandidateNode{}, false, nil
			}
			n, ok := rows[0]["n"].(*node)
			if !ok || n == nil {
				return graphrank.CandidateNode{}, false, nil
			}
			return toCandidateNode(n), true, nil
		},
		Search: func(ctx context.Context, term string, limit int) ([]graphrank.CandidateNode, bool, bool, error) {
			return a.hybridSearchNodes(ctx, key, principal.OrgID, term, limit, fence, temporal)
		},
		// CHAOS-3838 (spec L11): the SAME fence and temporal filter this
		// resolution's per-term Search calls already share, so the
		// question-level pass costs no additional fence probe and obeys the
		// identical historical-axis skip.
		SearchQuestion: func(ctx context.Context, question string, limit int) ([]graphrank.CandidateNode, bool, bool, error) {
			return a.questionVectorSearchNodes(ctx, key, principal.OrgID, question, limit, fence, temporal)
		},
		// CHAOS-4038: the SAME temporal filter this resolution's per-term
		// Search/SearchQuestion calls already share -- no separate fence
		// probe needed, this pass is lexical-only.
		SearchKind: func(ctx context.Context, term string, kind contextfabric.SubjectKind, limit int) ([]graphrank.CandidateNode, bool, bool, error) {
			return a.kindScopedFulltextSearchNodes(ctx, key, principal.OrgID, term, kind, limit, temporal)
		},
		// CHAOS-4348: every repository/project/team node in scope, once per
		// resolution -- see ResolveDeps.ExactNameCandidates' own doc comment
		// (graphrank/resolve.go) for why this is a separate, unranked
		// retrieval path rather than another SearchKind call.
		ExactNameCandidates: func(ctx context.Context) ([]graphrank.CandidateNode, bool, error) {
			return a.chaos4348ExactNameCandidates(ctx, key, principal.OrgID, temporal)
		},
		// CHAOS-4154: whether THIS deployment has a live vector mechanism at
		// all -- see ResolveDeps.VectorMechanismConfigured's own doc comment
		// for why the confirmed-kind truncation-scoping mechanism needs
		// this rather than any per-call signal.
		VectorMechanismConfigured: a.embedder != nil,
		// CHAOS-4155 Phase 1: SHADOW-only kind-scoped vector completeness
		// census -- see graphrank.ResolveDeps.ConfirmedKindVectorCensus's
		// own doc comment. Bound to the SAME key/orgID every other closure
		// in this deps struct already closes over.
		ConfirmedKindVectorCensus: func(ctx context.Context, kind contextfabric.SubjectKind, terms []string) graphrank.ConfirmedKindVectorCensusOutcome {
			return a.confirmedKindVectorCensus(ctx, key, principal.OrgID, kind, terms)
		},
		Traverse: func(ctx context.Context, term string, observation graphrank.CandidateNode, allowExactMatch bool) (contextfabric.SubjectCandidate, graphrank.ObservationTraversal) {
			return graphrank.TraverseObservationToSubject(ctx, principal, request.RequestedScope, term, observation, isInternalSubject, allowExactMatch,
				func(ctx context.Context, uuid string) ([]graphrank.CandidateEdge, error) {
					return a.edgesOfNode(ctx, key, principal.OrgID, uuid, temporal)
				},
				func(ctx context.Context, uuid string) (graphrank.CandidateNode, bool) {
					n, err := a.nodeByUUID(ctx, key, principal.OrgID, uuid, temporal)
					if err != nil || n == nil {
						return graphrank.CandidateNode{}, false
					}
					return toCandidateNode(n), true
				},
			)
		},
		IsInternal: isInternalSubject,
		TraversalDegraded: func(ctx context.Context, orgID string, count int) {
			if a.config.Telemetry != nil {
				a.config.Telemetry.RecordObservationTraversalDegraded(ctx, orgID, count)
			}
		},
		// CHAOS-3888: same nil-safe, aggregate-report convention as
		// TraversalDegraded immediately above. Also reports through
		// contextfabric.RecordSubjectCandidatesAuthzDropped -- a no-op
		// unless the caller (Engine.Investigate) attached a recorder to
		// this SAME ctx -- so an authz-filtered-to-empty resolution is
		// classifiable at the terminal-result layer, not just visible in
		// this backend's own GraphTelemetry stream.
		SubjectCandidatesAuthzDropped: func(ctx context.Context, orgID string, count int) {
			if a.config.Telemetry != nil {
				a.config.Telemetry.RecordSubjectCandidatesAuthzDropped(ctx, orgID, count)
			}
			contextfabric.RecordSubjectCandidatesAuthzDropped(ctx, count)
		},
		// CHAOS-3829: the calibrated commit-path margin threshold captured
		// at attachEmbedder time (retrieval_policy.go). Zero (no calibrated
		// policy for this identity, or no embedder at all) disables the
		// carve-out entirely -- see ResolveDeps.VectorMarginCommitThreshold's
		// own doc comment.
		//
		// codex r2 G1 (REFUTED, proof recorded here so this premise cannot
		// re-cycle): claimed the carve-out is unsafe at a runtime
		// MaxSubjectCandidates (limit K, request.Options.MaxSubjectCandidates
		// in graphrank.ResolveSubjects) different from the report's
		// calibrated TopK=20. False for any K>=2 -- the production margin
		// is K-INVARIANT and EXACT, not merely approximately safe:
		//
		// Let s(x) = max over this resolution's terms of sim(term, x) (the
		// vectorArmSimilarity side map's own definition, mergeSearchResults
		// -- keeps the HIGHEST observed value across terms). top1 and the
		// TRUE #2 competitor rank by s. Let t* be the ONE term whose own
		// Search call attains s(true#2) for the true #2 (i.e. true#2's
		// best-across-terms similarity is realized in call t*). ANY
		// subject x that would outrank true#2 within call t* has
		// sim(t*, x) > sim(t*, true#2) = s(true#2) (by t*'s own
		// maximality) >= ... i.e. s(x) >= sim(t*,x) > s(true#2), so x's
		// own cross-term maximum EXCEEDS true#2's -- meaning x is top1,
		// not a rival to true#2's #2 standing. So AT MOST ONE subject
		// (top1 itself) can outrank true#2 within call t* -- true#2 is at
		// WORST rank 2 in that one call, and a k-NN call returns its own
		// top-K by construction, so true#2 IS RETURNED at any K>=2 in call
		// t*. F0's pre-NodeCandidate-rejection recording (mergeSearchResults)
		// then captures it into the side map regardless of downstream
		// eligibility, and by definition of "true #2" nothing else in the
		// side map can exceed s(true#2) -- so vectorMarginCommit's
		// COMPETITOR equals s(true#2) EXACTLY, at every K>=2, independent
		// of K's specific value. Corroboration at a smaller runtime lexical
		// limit is a SUBSET of what a larger limit would find (fewer
		// lexical proposals can only fail to corroborate a top-1 that a
		// wider search would have corroborated) -- so a narrower K can only
		// ever LOSE commits (fail closed further), never fabricate one.
		// K<2 is already refused independently (codex r1 F1, above this
		// call site in resolution.go).
		//
		// DISTINCT FROM MarginCalibrationOptions.TargetTopK (codex r1 F7):
		// that pin is REPORT-PROVENANCE discipline for the MEASUREMENT
		// chain -- it says the calibration report's own S+/S- harvest was
		// gathered at a stated K, so a caller cannot silently apply M
		// against a report measured under a DIFFERENT harvest depth. It is
		// not, and was never, a claim that the RUNTIME gate requires
		// matching K -- this proof is what establishes that the runtime
		// gate does not, for any K>=2.
		//
		// codex r4 J1 (REFUTED, SECOND raise of the K premise -- a NEW
		// mechanism angle, checked and refuted the same way): where G1
		// argued K-invariance for an IDEALIZED exact k-NN, J1 asked whether
		// the DEPLOYED index's own ANN APPROXIMATION reopens the question --
		// it does not. retrieval_policy.go's calibratedIdentityText3Large
		// pins EfRuntime=200 for this identity, and the pinned HNSW module
		// (CHAOS-3832, verified live) explores with ef = max(efRuntime, K)
		// -- so for every K the API allows (1-50), efRuntime=200 already
		// dominates: ef stays fixed at 200 regardless of K, meaning the
		// EXPLORED candidate set HNSW considers is IDENTICAL across every
		// allowed K. K changes only how much of that one fixed exploration
		// is RETURNED (the top-K prefix of it) -- never what was explored.
		// G1's argument above ("true#2 is at worst rank 2 in call t*, so it
		// is returned at any K>=2") therefore applies UNCHANGED over this
		// SAME fixed explored set: rank-2-of-explored is in every returned
		// prefix K>=2, independent of K. The index's own recall imperfection
		// (CHAOS-3832's measured 0.979 at efRuntime=200) is a property of ef
		// alone, not of K -- and it is not a NEW hazard M was calibrated
		// blind to: the oracle's own wrong-top1 population (calibratedIdentityText3Large's
		// doc comment) already includes an ann_loss case, meaning M was
		// measured against the ACTUAL deployed ANN's imperfect recall, not
		// an idealized exact k-NN that never misses. This premise has now
		// been raised and refuted TWICE under two different mechanism
		// framings (r2 G1: exact-KNN/runtime-K; r4 J1: ANN-approximation/ef)
		// -- both settled; a third raise is premise-cycling, not new
		// information.
		VectorMarginCommitThreshold: a.vectorMarginCommitThreshold,
		// codex r5 K1+K2 (both accepted -- NOT a third raise of the
		// settled G1/J1 K premise above, despite both mentioning "K":
		// G1/J1 asked whether the vector-arm MARGIN itself stays sound
		// across different runtime K values, and proved it does, for
		// any K>=2, via two independent mechanism arguments. K1/K2
		// attack entirely different preconditions -- K1 is about
		// CORROBORATION width (was the winning subject's lexical-arm
		// finding within the depth the oracle actually scored?), K2 is
		// about the LOWER bound itself being measured off the wrong
		// (nominal, uncapped) number. Settling G1/J1 said nothing about
		// either, and fixing K1/K2 does not reopen G1/J1 -- they are
		// four independent findings that happen to share a letter.
		CalibratedTopK:    a.calibratedTopK,
		MaxResultsCap:     a.config.MaxResults,
		CommitGatePolicy:  a.commitGatePolicy,
		RawSignalObserver: a.config.RawSignalObserver,
		ResolutionTracer:  a.config.ResolutionTracer,
		// CHAOS-3899 (SHADOW ONLY): nil unless the composition root sets
		// Config.CensusFunc -- see that field's own doc comment. Threaded
		// straight through, exactly like RawSignalObserver/ResolutionTracer
		// above; graphrank.ResolveSubjects itself is what gates the shadow
		// round on "stalled resolution only" and adds the 3s deadline +
		// panic recovery, so nothing extra is needed here.
		CensusFunc: a.config.CensusFunc,
		// CHAOS-3972 P3: nil unless the composition root sets
		// Config.HandleGrammarChecker -- see that field's own doc comment.
		HandleGrammarChecker: a.config.HandleGrammarChecker,
		// CHAOS-4042: false unless the composition root sets
		// Config.AnchorMembershipOffersEnabled -- see that field's own doc
		// comment (team-lead ruling: ships DARK until PR3).
		AnchorMembershipOffersEnabled: a.config.AnchorMembershipOffersEnabled,
	}
	// CHAOS-3884 (Option C): AliasLookup is left nil (deps' own zero value)
	// when this deployment has no identity-universe reader configured --
	// byte-identical to every pre-CHAOS-3884 backend, same convention
	// Config.IdentityUniverse's own doc comment documents. Assigned
	// conditionally, not via an always-present closure that checks nil
	// internally, so graphrank.ResolveSubjects' own "nil means
	// unsupported" contract (SearchQuestion's identical convention) holds
	// literally.
	//
	// Receipt notes (adjustment 5, team-lead amendment 2026-08-17): this
	// mechanism reads TWO sources (ClickHouse's identity universe, the live
	// graph's own nodes) that are not guaranteed to agree at every instant,
	// and that has two BENIGN consequences worth naming rather than
	// discovering later as surprises:
	//   - stale-label presentation: the table can be fresher than the graph
	//     (a rename landed in ClickHouse before the next projection cycle
	//     wrote it to FalkorDB) -- MatchIdentityRows matches against the
	//     table's CURRENT label/aliases, but toCandidateNode's own
	//     presentation (Name/Attributes) comes from the graph's still-OLD
	//     node. Cosmetic: the right subject still resolves, under its
	//     previous display text.
	//   - transient recall loss on rename: the reverse direction -- an old
	//     alias that no longer appears in the table (renamed away) will not
	//     be found via this mechanism even though the graph node might
	//     still carry it in its own attributes and would have matched via
	//     ORDINARY hybrid search alone. A resolution never regresses below
	//     what search already provided; it just does not gain the identity
	//     fast path for that one stale term until the next projection cycle
	//     catches up.
	// Both are transient, self-healing on the next projection cycle, and
	// strictly weaker than the ONE guarantee that matters most here:
	// authorization staleness FAILS CLOSED. The identity-universe table
	// NEVER supplies authorization data to this mechanism at all -- it only
	// ever answers "which canonical id/kind does this alias term identify."
	// AuthorizedAttributes then evaluates EXCLUSIVELY against the graph
	// node's OWN, CURRENT attributes (the same call every other candidate
	// path already goes through), so whatever staleness exists is the
	// graph's own pre-existing freshness property, identical to ordinary
	// search's, never a NEW window this mechanism opens: a table row can
	// never loosen, invent, or override an authorization scope the graph
	// itself has not (yet) recorded. The worst a stale graph node can do is
	// keep an OLD scope in effect a moment longer -- refusal or an
	// unchanged prior authorization -- never an admission grounded in
	// anything other than the graph's own state.
	if a.config.IdentityUniverse != nil {
		deps.AliasLookup = func(ctx context.Context, orgID string, terms []string) (map[string][]graphrank.CandidateNode, bool, error) {
			// HIGH-6: temporal authority stays with the graph -- a
			// historical-axis question never gets this mechanism at all,
			// mirroring vector.go's own "PLACEMENT IS THE ARGUMENT" choice
			// to skip a mechanism entirely on a historical axis rather
			// than thread a rewritten predicate through a new query path.
			if temporal.active {
				return nil, false, nil
			}
			rows, _, complete, err := a.config.IdentityUniverse(ctx, orgID)
			if err != nil {
				return nil, false, safeDependencyError("read identity universe", err)
			}
			// identity_universe trace event (chris ruling, 2026-08-17,
			// "turn the silent truncation into a counted, visible event"):
			// the RAW devhealthsource.IdentityUniverse completeness signal,
			// emitted HERE because this is the one place it exists as a
			// genuine local -- graphMissing (computed further below) has not
			// folded into it yet, and resolve.go/resolution.go never see
			// this raw value at all, only the folded aliasIdentityComplete.
			// complete==false means fetchIdentityKind hit
			// identityUniverseRowBudget on at least one kind for THIS call
			// -- previously silent (the fast path's own aliasIdentityComplete
			// gate absorbed it without ever surfacing which of "source
			// truncated" or "graph missing" was the actual cause). request
			// (the enclosing ResolveSubjects call's own parameter) is
			// captured by this closure, so RequestID correlates exactly like
			// every other stage's event.
			if a.config.ResolutionTracer != nil {
				a.config.ResolutionTracer.Trace(graphrank.ResolutionTraceEvent{
					RequestID: request.RequestID, Stage: "identity_universe",
					IdentityUniverseComplete: complete,
				})
			}
			matchesByTerm := graphrank.MatchIdentityRows(rows, terms)
			if len(matchesByTerm) == 0 {
				return nil, complete, nil
			}
			// Existence check (CHAOS-3884 Option C item 1): a source-table
			// match is NEVER trusted directly -- every claimant is
			// confirmed present in the graph via the SAME keyed,
			// temporal-filtered lookup ExactHint uses, and the resulting
			// CandidateNode comes from the GRAPH's own node (toCandidateNode),
			// never fabricated from raw ClickHouse row data. Authorization
			// re-application is a SEPARATE, unconditional guarantee, not a
			// special case handled here: a candidate this closure ever
			// returns is AUTHORIZED EXACTLY LIKE ANY OTHER --
			// AuthorizedAttributes runs on it downstream via the ordinary
			// NodeCandidate path, because it is never anything other than a
			// real graph node's own attributes. isReservedIdentityProjectID
			// below is a NARROWER, additional defense-in-depth check
			// specific to the reserved organization-scope namespace -- see
			// its own doc comment for why it is honestly framed as
			// non-load-bearing today rather than claimed as strictly
			// necessary. A claimant that exists ONLY in source tables and
			// NOT in the graph is excluded here, never granted a candidacy
			// on the strength of ClickHouse data alone.
			claimantsByTerm := make(map[string][]graphrank.CandidateNode, len(matchesByTerm))
			graphMissing := 0
			for term, matches := range matchesByTerm {
				for _, match := range matches {
					// isReservedIdentityProjectID (CHAOS-3884 step 5,
					// DEFENSE IN DEPTH, not the primary guard): since this
					// loop builds candidates from graph nodes, a
					// reserved-namespace project row was already rejected
					// by devhealthsource's own producer-side guard
					// (projectAuthorizationScope) and so never became a
					// real graph node -- the existence check just below
					// already enforces that rejection TRANSITIVELY
					// (nodeByKindID reports not-found for an id nothing
					// ever wrote). This filter's actual job is avoiding two
					// avoidable costs for a row that can never legitimately
					// commit anyway: a wasted graph round-trip, and a
					// spurious graphMissing increment that would degrade
					// aliasIdentityComplete for the WHOLE resolution over a
					// row that was poisoned, not merely projection-lagged.
					// It carries no load today (queryProjects already
					// aborts the whole read on such a row, so IdentityUniverse
					// can never even hand one to this loop) -- it becomes
					// load-bearing only if a future refined completeness
					// design counts over authorization-filtered TABLE rows
					// instead of the candidate set, at which point a
					// poisoned row surviving in that table-side count would
					// matter.
					if isReservedIdentityProjectID(match.Row) {
						continue
					}
					n, existsErr := a.nodeByKindID(ctx, key, orgID, string(match.Row.Kind), match.Row.CanonicalID, temporal)
					// ErrNotFound is the documented, EXPECTED signal for a
					// read-only lookup against a graph key that was never
					// created (or a purged organization) -- client.go's own
					// "Invalid graph operation on empty key" classification.
					// An organization whose identity-universe source tables
					// have rows but whose graph was never bootstrapped (no
					// write has landed yet) is precisely a graph-missing
					// claimant, not a backend fault -- treated identically
					// to nodeByKindID's own ordinary "0 rows" n==nil case,
					// never surfaced as an error that would abort the whole
					// resolution.
					if existsErr != nil && !errors.Is(existsErr, ErrNotFound) {
						return nil, false, safeDependencyError("identity-universe graph existence check", existsErr)
					}
					if n == nil {
						graphMissing++
						continue
					}
					node := toCandidateNode(n)
					node.Mechanism = match.Mechanism
					node.FromKeyedIdentityLookup = true
					claimantsByTerm[term] = append(claimantsByTerm[term], node)
				}
			}
			if graphMissing > 0 && a.config.Telemetry != nil {
				a.config.Telemetry.RecordIdentityGraphMissing(ctx, orgID, graphMissing)
			}
			// Decision 1 (team-lead amendment, 2026-08-17, settled): the
			// aliasIdentityComplete flag returned below only ever gated
			// resolution.go's OWN dedicated fast-path switch case --
			// identityCollision, the guard LoneFloor/TopFloor/the CHAOS-3829
			// rescue ALL use instead, counts the CANDIDATE set (claimants
			// that reached recordIdentityClaim), not the table set this
			// completeness flag is proven over. A claimant that fails the
			// existence check above (graphMissing) silently vanishes from
			// that count -- a surviving sibling then reads as uniquely
			// claimed and, since its confidence=1 identity-trust bump
			// (NodeCandidate's identityTrusted) is earned from
			// FromKeyedIdentityLookup alone, independent of
			// aliasIdentityComplete, it could still clear LoneFloor/TopFloor
			// on the strength of a claim this call never actually proved
			// unique. Stripping FromKeyedIdentityLookup from every survivor
			// of THIS call when graphMissing > 0 anywhere in it closes the
			// hole at its source: identityTrusted (and so the confidence=1
			// bump, and so eligibility for identityIndex/LoneFloor/TopFloor/
			// the rescue alike) requires it, so an incomplete call can never
			// manufacture the trust any of those sites relies on, without
			// touching resolution.go's ratified commit-gate logic at all.
			// The survivor is not discarded -- it still competes on its
			// ordinary (unboosted) confidence, exactly like any ordinary
			// Search()-sourced alias match.
			if graphMissing > 0 {
				for term, nodes := range claimantsByTerm {
					for i := range nodes {
						nodes[i].FromKeyedIdentityLookup = false
					}
					claimantsByTerm[term] = nodes
				}
			}
			// A graph-missing claimant folds into incompleteness for the
			// WHOLE call (not threaded as a separate flag): an identity
			// view that is missing even one confirmed-real claimant is not
			// one the fast path may trust as exhaustive, the identical
			// reasoning a truncated ordinary search already gets via
			// searchTruncated.
			return claimantsByTerm, complete && graphMissing == 0, nil
		}
	}
	// CHAOS-4085: the basis-carrying entry point. graphrank records, at
	// each commit site, which class of proof stood behind that commit; this
	// adapter is the one production GraphReader, so this is where that
	// record enters the engine. CHAOS-4087: digests is the SAME record's
	// wire-safe companion set, carried out identically.
	resolution, offers, bases, digests, err := graphrank.ResolveSubjectsWithCommitBasis(ctx, principal, request, interpreted, deps, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
	// CHAOS-4077: the single point every deps.* callback's own ErrNotFound
	// (a never-projected org's graph key) funnels through on its way back
	// to Engine -- translated here, once, rather than at each of the
	// several callbacks above, since graphrank.ResolveSubjectsWithCommitBasis
	// is this function's one delegated return.
	return resolution, offers, bases, digests, graphNotProjectedError(err)
}

func (a *Adapter) DiscoverContext(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.GraphContext{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.GraphContext{}, err
	}
	// CHAOS-3898 §2.1: see ResolveSubjects' identical comment above -- the
	// SAME binding Engine resolved once and threaded through
	// request.Binding, never re-resolved here (effectiveKey's fallback is
	// for a direct/test caller only).
	key, err := a.effectiveKey(ctx, principal.OrgID, request.Binding)
	if err != nil {
		return contextfabric.GraphContext{}, err
	}
	scope := request.Request.RequestedScope
	temporal := newTemporalFilter(request.Interpretation.TimeContext)

	// Codex P2a: collection is bounded by a.config.MaxResults, a generous
	// superset cap -- NEVER by request.Request.Options.MaxRelationshipPaths,
	// the final per-request admission budget. Truncating to the tight
	// per-request limit here, before graphrank.SortEdgesByRelevance and
	// graphrank.AdmitEdges ever see the full candidate set, could let a
	// low-value edge reached early consume the limit while a
	// higher-relevance edge discovered later never gets the chance to
	// compete for it. The one and only truncation to MaxRelationshipPaths
	// happens inside AdmitEdges, after ranking.
	collectLimit := a.config.MaxResults

	// falkorgraph resolves every edge endpoint from a single whole-path
	// query -- one graph per org means there is no second-hop concept the
	// way zepgraph needs one (see reader.go's package-level doc in
	// zepgraph and graphrank.ResolvedEdge's doc comment). Two sources feed
	// the candidate edge set: (1) a bounded hop-walk from the committed
	// origin subjects (native Cypher variable-length path, [*1..2]), and
	// (2) a lexical full-text search over the question text, for the
	// subjectless-cohort case (no committed origin) and for text-relevant
	// items outside the hop radius.
	var resolvedNodes []graphrank.CandidateNode
	var resolvedEdges []graphrank.ResolvedEdge
	seenEdge := make(map[string]bool)
	seenNode := make(map[string]bool)
	// failedLookups counts edges dropped because a genuine backend lookup
	// failed (not because authorization or a legitimate "endpoint no longer
	// exists" filtered them) -- Codex P2c: this is the signal that
	// distinguishes real degradation from ordinary, silent filtering, and it
	// alone drives Coverage.Partial.
	failedLookups := 0
	// exactNameTruncated (CHAOS-4395, codex round-1 P1) reports whether
	// chaos4348ExactNameCandidates' bounded org-wide kind census was cut
	// off before it finished -- only ever set for a discovered_cohort
	// request (see that call site below), and fed into Coverage.Partial/
	// DegradedReasons so a cohort built from an incomplete census never
	// silently claims completeness.
	exactNameTruncated := false
	// ranExhaustiveCensus (CHAOS-4577, codex round-2 P2) reports whether
	// chaos4348ExactNameCandidates' bounded org-wide kind census actually
	// ran for this call -- true only inside the same condition
	// exactNameTruncated is scoped to below. A "whole cohort denied by
	// authorization" claim is only honest when the candidate pool was an
	// attempt at an EXHAUSTIVE census: ShapeExplicitCohort and a
	// discovered_cohort request with an already-committed subject use only
	// the bounded fulltext/hopWalk candidates (this function's default
	// node source), which can easily contain one denied match while other,
	// never-retrieved members of the same cohort simply were not searched
	// for at all -- reporting authorization denial there would claim more
	// than a single incomplete, non-exhaustive result can support.
	ranExhaustiveCensus := false
	// edgeFilters (CHAOS-3888) aggregates every edge resolveEdge excluded as
	// edgeFiltered across BOTH sources this function reads from (hopWalk's
	// committed-origin traversal below, and the full-text-adjacent-edge loop
	// further down), by reason -- see edgeFilterCounts' own doc comment.
	var edgeFilters edgeFilterCounts

	for _, subject := range request.Resolution.Committed {
		nodes, edges, failed, filters, err := a.hopWalk(ctx, key, principal.OrgID, principal, scope, subject, 2, collectLimit, temporal)
		if err != nil {
			// CHAOS-4077: see graphNotProjectedError's own doc comment --
			// this is one of the two DiscoverContext sites that would
			// otherwise independently re-hit the identical never-projected
			// graph key ResolveSubjects already degraded gracefully from,
			// one call later, if this Adapter method is ever reached with
			// a resolution that came from a source other than Engine's own
			// short-circuit (e.g. a direct/test caller).
			return contextfabric.GraphContext{}, graphNotProjectedError(err)
		}
		failedLookups += failed
		edgeFilters.Authz += filters.Authz
		edgeFilters.TemporalWindow += filters.TemporalWindow
		for _, n := range nodes {
			nk := graphrank.SubjectKey(mustSubject(n))
			if !seenNode[nk] {
				seenNode[nk] = true
				resolvedNodes = append(resolvedNodes, n)
			}
		}
		for _, e := range edges {
			if !seenEdge[e.UUID] {
				seenEdge[e.UUID] = true
				resolvedEdges = append(resolvedEdges, e)
			}
		}
	}

	// The search-truncation signal (fulltextSearchNodes' 2nd return value)
	// is deliberately discarded here: it exists to gate SUBJECT-RESOLUTION
	// auto-commit (graphrank.ResolveFromMergedCandidates' searchTruncated,
	// via ResolveSubjects above) against an incomplete candidate set, per
	// Codex's round-3 ruling. DiscoverContext has no analogous auto-commit
	// decision to protect -- this call feeds cohort/edge DISCOVERY, already
	// bounded and already best-effort, not a committed-subject gate.
	textNodes, _, err := a.fulltextSearchNodes(ctx, key, principal.OrgID, request.Request.Question, collectLimit, temporal)
	if err != nil {
		// CHAOS-4077: see graphNotProjectedError's own doc comment and the
		// hopWalk error site's identical comment above -- this is the
		// UNCONDITIONAL query (runs even with zero committed subjects),
		// so it is the one that actually fires for a subjectless-cohort
		// never-projected org.
		return contextfabric.GraphContext{}, graphNotProjectedError(err)
	}
	// Codex P2a (round 2): the full-text-adjacent edge set is gathered from
	// EVERY matched node before any truncation decision, then ranked and
	// bounded the same way hopWalk's own per-hop collection is (Codex round
	// 2: "full-text node expansion also gathers adjacent edges with no
	// global cap") -- the previous version resolved every adjacent edge
	// from every matched node unconditionally as it was found, so an edge
	// discovered from the last matched node could never be dropped in favor
	// of a better one found earlier, but it also had no bound at all.
	var textCandidates []graphrank.CandidateEdge
	for _, n := range textNodes {
		subject, ok := graphrank.NodeSubject(n)
		if !ok {
			continue
		}
		nk := graphrank.SubjectKey(subject)
		if !seenNode[nk] {
			seenNode[nk] = true
			resolvedNodes = append(resolvedNodes, n)
		}
		textEdges, err := a.edgesOfNode(ctx, key, principal.OrgID, n.UUID, temporal)
		if err != nil {
			// CHAOS-4077: see graphNotProjectedError's own doc comment
			// above -- unreachable for a genuinely never-projected org in
			// practice (textNodes would already be empty), kept for the
			// same reason the hopWalk site is: consistent behavior if this
			// method is ever reached with a non-empty node set from a
			// source other than this adapter's own prior query.
			return contextfabric.GraphContext{}, graphNotProjectedError(err)
		}
		for _, ce := range textEdges {
			if seenEdge[ce.UUID] {
				continue
			}
			seenEdge[ce.UUID] = true
			textCandidates = append(textCandidates, ce)
		}
	}
	textAdmitted := 0
	for _, ce := range rankCandidateEdges(textCandidates) {
		if collectLimit > 0 && textAdmitted >= collectLimit {
			break
		}
		resolved, resolution, reason := a.resolveEdge(ctx, key, principal.OrgID, principal, scope, ce, temporal)
		edgeFilters.add(resolution, reason)
		switch resolution {
		case edgeLookupFailed:
			failedLookups++
			continue
		case edgeFiltered:
			continue
		}
		resolvedEdges = append(resolvedEdges, resolved)
		textAdmitted++
	}

	candidateEdges := make([]graphrank.CandidateEdge, 0, len(resolvedEdges))
	for _, r := range resolvedEdges {
		candidateEdges = append(candidateEdges, graphrank.CandidateEdge{
			UUID: r.UUID, Name: r.Name, Fact: r.Fact, Relevance: r.Relevance, Score: r.Score,
		})
	}
	order := graphrank.SortEdgesByRelevance(candidateEdges)
	orderedResolved := make([]graphrank.ResolvedEdge, 0, len(resolvedEdges))
	byUUID := make(map[string]graphrank.ResolvedEdge, len(resolvedEdges))
	for _, r := range resolvedEdges {
		byUUID[r.UUID] = r
	}
	for _, e := range order {
		orderedResolved = append(orderedResolved, byUUID[e.UUID])
	}

	admission := graphrank.AdmitEdges(principal.OrgID, orderedResolved, request.Request.Options, isInternalSubject)
	// CHAOS-4395: DiscoveredCohort's ONLY node source used to be
	// textNodes/resolvedNodes above -- fulltextSearchNodes over the raw
	// question TEXT. A cohort question that names no member by label/alias
	// ("which teams are struggling") cannot lexically match anything, so
	// the cohort starved even when Shape was correctly interpreted as
	// discovered_cohort and authorization would otherwise allow it.
	// CHAOS-4348's chaos4348ExactNameCandidates is the kind-exhaustive,
	// term-free fetch that already exists for exactly this problem on the
	// single-subject path (graphrank.applyExactNameArm) -- this wires the
	// SAME fetch into the cohort path (never touches resolvedNodes/
	// orderedResolved/Paths/unbounded-validity accounting above, which
	// stay fulltext+hop-walk only, unchanged for every non-cohort
	// investigation, proven by a dedicated test).
	//
	// Codex round-1 finding (P1): scoped to ShapeDiscoveredCohort ONLY,
	// deliberately excluding ShapeExplicitCohort. explicit_cohort means the
	// question NAMES specific members ("compare the frontend and backend
	// teams") -- chaos4348ExactNameCandidates returns the WHOLE org-wide
	// kind census with no term filtering at all, so admitting it for an
	// explicit_cohort request would widen a question that named two teams
	// into a cohort containing every team in the org. discovered_cohort is
	// the one shape that means "no term to match, give me the kind's whole
	// census" -- the only shape this broad a fetch is ever correct for.
	//
	// Codex round-1 finding (P2, authzDropped double-count): exactNameNodes
	// is merged through the SAME seenNode dedup map hopWalk/fulltext
	// already populate above, so a subject BOTH fulltext and exact-name
	// return (or that exact-name alone returns twice, though it cannot)
	// contributes to DiscoveredCohort's authzDropped/membership accounting
	// exactly once, never inflated by which arm(s) found it.
	cohortNodes := resolvedNodes
	// Codex round-2 finding (P1): gating on Shape alone is not enough. A
	// discovered_cohort request can still carry a resolved, committed
	// subject (an exact hint, a prior-turn carry-over) -- request.Resolution
	// is upstream of this method and not something DiscoverContext itself
	// produces or validates. Appending the org-wide census onto a request
	// that already has a committed anchor would widen a subject-anchored
	// investigation into an organization-wide cohort. The exact-name fetch
	// is therefore reserved for a GENUINELY subjectless eligible request:
	// Shape/anchor say "census" (see cohortExactNameCensusEligibility's own
	// doc comment -- CHAOS-4622 remainder widened this past Shape ==
	// ShapeDiscoveredCohort alone), AND nothing was already committed.
	shapeAnchorEligible, censusBasis := cohortExactNameCensusEligibility(request.Frame, request.ScopeAnchorResolved)
	censusAdmitted := shapeAnchorEligible && len(request.Resolution.Committed) == 0
	if censusBasis != "" && a.config.Telemetry != nil {
		reportedBasis := censusBasis
		if shapeAnchorEligible && !censusAdmitted {
			reportedBasis = CohortExactNameCensusBasisAlreadyCommitted
		}
		a.config.Telemetry.RecordCohortExactNameCensusGate(ctx, principal.OrgID, censusAdmitted, reportedBasis)
	}
	if censusAdmitted {
		ranExhaustiveCensus = true
		exactNameNodes, truncated, exactNameErr := a.chaos4348ExactNameCandidates(ctx, key, principal.OrgID, temporal)
		if exactNameErr != nil {
			// CHAOS-4077: same never-projected-graph degrade-gracefully
			// discipline as the hopWalk/fulltextSearchNodes error sites
			// above.
			return contextfabric.GraphContext{}, graphNotProjectedError(exactNameErr)
		}
		// Codex round-1 finding (P1, truncation disclosure): unlike the
		// fulltext call above (whose truncation signal genuinely has no
		// consumer here, per that call's own doc comment), a truncated
		// exact-name fetch means the kind's census itself was incomplete
		// -- a cohort built from it can silently under-count and still
		// report Complete=true. Disclosed below via Coverage.Partial/
		// DegradedReasons, the same mechanism this function already uses
		// for endpoint_lookup_failed/unknown_relationship_type, AND (codex
		// round-2 P2) forced onto Cohort.Complete directly below --
		// DiscoveredCohort has no truncation signal of its own to derive
		// that from.
		exactNameTruncated = truncated
		// Codex round-2 finding (P2, determinism): chaos4348ExactNameCandidates'
		// Cypher carries no ORDER BY, so FalkorDB's return order is
		// unspecified -- and DiscoveredCohort ranks members in INPUT
		// order and stops at MaxCohortMembers, so an unordered census
		// could select different members across otherwise-identical
		// calls (CHAOS-3782 answer-reuse needs reproducibility). Sorted
		// by subject key before merging, scoped to this cohort path only
		// -- never touches chaos4348ExactNameCandidates' own single-
		// subject callers.
		sortCandidateNodesBySubjectKey(exactNameNodes)
		cohortNodes = make([]graphrank.CandidateNode, 0, len(resolvedNodes)+len(exactNameNodes))
		cohortNodes = append(cohortNodes, resolvedNodes...)
		for _, n := range exactNameNodes {
			subject, ok := graphrank.NodeSubject(n)
			if !ok {
				continue
			}
			nk := graphrank.SubjectKey(subject)
			if seenNode[nk] {
				continue
			}
			seenNode[nk] = true
			cohortNodes = append(cohortNodes, n)
		}
	}
	cohort, cohortAuthzDropped, cohortKindScopedAuthzDropped, cohortKindBasis := graphrank.DiscoveredCohort(principal, request, cohortNodes, isInternalSubject)
	// SEAM 7 (CHAOS-4736): what decided the cohort kind, or what prevented
	// a cohort. This is the I/O boundary, so the telemetry call lives here
	// and DiscoveredCohort stays pure -- the same split the authzDropped
	// counters beside it already use. Emitted on EVERY call, including the
	// ones that discovered nothing: "the frame was absent" and "the graph
	// had no matching nodes" were indistinguishable before this, and the
	// first is the cost of having deleted the prose matcher.
	if a.config.Telemetry != nil {
		a.config.Telemetry.RecordCohortKindBasis(ctx, principal.OrgID, cohortKindBasis, cohort != nil)
	}
	if cohort != nil && exactNameTruncated {
		// Codex round-2 finding (P2): DiscoveredCohort computes Complete
		// purely from len(members) vs. MaxCohortMembers, with no way to
		// know its own node source was truncated upstream -- a truncated
		// census with fewer than MaxCohortMembers matching members would
		// otherwise report Complete=true despite genuinely missing some.
		cohort.Complete = false
	}
	factRequirements := admission.FactRequirements
	if cohort != nil {
		factRequirements = graphrank.MergeFactRequirements(factRequirements, contextfabric.FactHealth, contextfabric.FactWorkload)
	}
	// CHAOS-3888: telemetry-only, never affects Coverage/Partial/the
	// returned Cohort or Paths -- an authorization exclusion, a
	// self-loop exclusion, and a temporal-window exclusion are all
	// ordinary, expected outcomes of a correct read (see
	// GraphTelemetry.RecordEdgesFilteredByReason/
	// RecordCohortMembersAuthzDropped's own doc comments), not
	// degradation, so none of them touches partial/degradedReasons below.
	//
	// CHAOS-4577 is the one exception: when authorization denied EVERY
	// candidate cohort member OF THE REQUESTED KIND (cohort == nil AND
	// cohortKindScopedAuthzDropped > 0 -- as opposed to cohort == nil with
	// cohortKindScopedAuthzDropped == 0, which means the census genuinely
	// found no matching subject of that kind at all), the caller cannot
	// tell that apart from "there are no such teams" without a signal in
	// the answer itself. Deliberately keyed on cohortKindScopedAuthzDropped,
	// NOT the unscoped cohortAuthzDropped: the exact-name arm's pool mixes
	// repository/project/team nodes, so an unrelated repository node denied
	// for its own reasons must never manufacture a false
	// cohort_denied_by_authorization signal for a teams question that had
	// no denied team at all (codex round-1 P2). See
	// graphrank.DiscoveredCohort's own doc comment for the distinction.
	//
	// Also requires ranExhaustiveCensus && !exactNameTruncated (codex
	// round-2 P2): ShapeExplicitCohort and a discovered_cohort request with
	// an already-committed subject never run the org-wide census at all --
	// their bounded fulltext/hopWalk candidates can contain one denied
	// match while other cohort members the user actually asked about were
	// simply never searched for, which is a retrieval gap, not evidence
	// every member was denied. A truncated census has the same problem:
	// the row that would have survived authorization may be exactly the
	// one that got cut. Both cases already have their own, more accurate
	// disclosure (Cohort.Complete=false / exact_name_candidates_truncated);
	// this signal stays reserved for a genuinely exhaustive, untruncated
	// census that still came back with a kind-matching denial and nothing
	// else.
	cohortWhollyDeniedByAuthz := cohort == nil && cohortKindScopedAuthzDropped > 0 && ranExhaustiveCensus && !exactNameTruncated
	if a.config.Telemetry != nil {
		if edgeFilters.Authz > 0 || edgeFilters.TemporalWindow > 0 || admission.DroppedSelfLoopCount > 0 {
			a.config.Telemetry.RecordEdgesFilteredByReason(ctx, principal.OrgID, edgeFilters.Authz, edgeFilters.TemporalWindow, admission.DroppedSelfLoopCount)
		}
		if cohortWhollyDeniedByAuthz {
			a.config.Telemetry.RecordCohortDeniedByAuthorization(ctx, principal.OrgID, cohortKindScopedAuthzDropped)
		}
		if cohortAuthzDropped > 0 {
			a.config.Telemetry.RecordCohortMembersAuthzDropped(ctx, principal.OrgID, cohortAuthzDropped)
		}
	}

	// Codex P2c: a failed endpoint lookup is a real, silent loss of material
	// (an edge/path that legitimately exists in the graph but this
	// investigation could not confirm and admit) -- it must never present as
	// clean, complete coverage.
	//
	// CHAOS-3779 codex round-1 H1: an edge whose Type failed the closed
	// relationship-type vocabulary is the same shape of silent loss --
	// AdmitEdges (pure, no I/O) only counts and names what it dropped;
	// this is the one I/O boundary in the call chain, so it is the one
	// place that both marks Coverage.Partial and logs it. The type
	// strings themselves are safe to log (not evidence, not a credential,
	// not org-identifying).
	//
	// Codex round-2 ruling: this emits ONE AGGREGATE WARNING PER
	// DiscoverContext CALL -- bounded, request-scoped, naming every
	// distinct dropped type that call saw -- not a process-lifetime
	// dedup (no sync.Once, no cross-call suppression). A strict
	// once-ever log would HIDE recurring bad data on every call after
	// the first; per-call aggregation stays bounded (never one log line
	// per dropped edge) without ever going silent on a call that has
	// something to report.
	// CHAOS-3781: on a historical axis, count how much of what was
	// admitted carried NO validity bound at all. temporalFilter.predicate
	// admits such an element at every requested time (see its doc comment
	// for why excluding it would be worse), so the answer must disclose
	// how much of itself rests on elements that were never shown to have
	// been true then. Counted over what was ADMITTED, not over what was
	// scanned, so the number describes this answer rather than the graph.
	unbounded := 0
	if temporal.active {
		// Codex round-2 finding (P1): cohortNodes -- not resolvedNodes --
		// is what this call's answer actually rests on when Shape is
		// discovered_cohort: it is resolvedNodes PLUS the deduped
		// exact-name additions (identical to resolvedNodes for every
		// other Shape, since cohortNodes is only reassigned inside that
		// branch above). Counting resolvedNodes alone would silently
		// exclude an exact-name-sourced cohort member from this
		// historical-axis disclosure.
		unbounded = countUnboundedValidity(cohortNodes, orderedResolved)
	}

	partial := failedLookups > 0 || admission.DroppedUnknownRelationshipTypeCount > 0 || exactNameTruncated || cohortWhollyDeniedByAuthz
	var degradedReasons []string
	var coverageDetails []contextfabric.CoverageDetail
	// CHAOS-4690: every degraded reason this reader composes gets a paired
	// structured detail whose Raw is the exact composed string — the
	// dual-write derivation (degraded_reasons is derived from the degrading
	// details downstream) depends on the pairing being minted together.
	appendGraphDetail := func(code contractsv1.ContextFabricCoverageDetailCode, degrading bool, count *int, raw, source string) {
		detail := contextfabric.CoverageDetail{
			// Provisional per-group ordinal id; mergeCoverage re-mints the
			// final result-wide ids after normalization.
			DetailID:  fmt.Sprintf("cov-graph-%02d", len(coverageDetails)+1),
			Source:    source,
			Code:      code,
			Degrading: degrading,
			Count:     count,
			Raw:       raw,
		}
		detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
		coverageDetails = append(coverageDetails, detail)
	}
	if failedLookups > 0 {
		reason := fmt.Sprintf("endpoint_lookup_failed:%d", failedLookups)
		degradedReasons = append(degradedReasons, reason)
		count := failedLookups
		appendGraphDetail(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, true, &count, reason, "context-fabric:graph")
	}
	if exactNameTruncated {
		degradedReasons = append(degradedReasons, "exact_name_candidates_truncated")
		appendGraphDetail(contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated, true, nil, "exact_name_candidates_truncated", "context-fabric:graph")
	}
	if cohortWhollyDeniedByAuthz {
		// CHAOS-4577: the discovered_cohort request found candidate members,
		// but AuthorizedAttributes denied every one of them (the shape an
		// org's team_repo_ownership being empty produces via the CHAOS-4390
		// sentinel) -- the resulting empty Cohort must not read the same as
		// "no such teams exist". degradedReasons is the same free-text
		// vocabulary endpoint_lookup_failed/unknown_relationship_type
		// already use above; no new wire field. Count is the KIND-SCOPED
		// denial count (only candidates matching this cohort's requested
		// kind), not the unscoped cohortAuthzDropped -- see
		// graphrank.DiscoveredCohort's doc comment.
		cohortDeniedReason := fmt.Sprintf("cohort_denied_by_authorization:%d", cohortKindScopedAuthzDropped)
		degradedReasons = append(degradedReasons, cohortDeniedReason)
		deniedCount := cohortKindScopedAuthzDropped
		appendGraphDetail(contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, true, &deniedCount, cohortDeniedReason, "context-fabric:graph")
	}
	if admission.DroppedUnknownRelationshipTypeCount > 0 {
		unknownTypeReason := fmt.Sprintf("unknown_relationship_type:%d", admission.DroppedUnknownRelationshipTypeCount)
		degradedReasons = append(degradedReasons, unknownTypeReason)
		droppedCount := admission.DroppedUnknownRelationshipTypeCount
		appendGraphDetail(contractsv1.ContextFabricCoverageDetailGraphUnknownRelationshipType, true, &droppedCount, unknownTypeReason, "context-fabric:graph")
		slog.Default().Warn("context_fabric: dropped relationship edge(s) with a type outside the closed vocabulary",
			"count", admission.DroppedUnknownRelationshipTypeCount, "types", admission.DroppedUnknownRelationshipTypeNames)
	}
	sources := []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptrTime(a.now().UTC())}}
	if unbounded > 0 {
		// A distinct source row rather than a degraded reason: this is not
		// a failure and must not set Partial. The graph answered fully;
		// part of what it returned simply carries no validity bound, and a
		// reader deserves to see that separately from real degradation.
		validityReason := fmt.Sprintf("graph elements carrying no validity window were admitted at the requested time: %d", unbounded)
		sources = append(sources, contextfabric.SourceObservation{
			Source:     "context-fabric:graph-validity-windows",
			State:      contextfabric.SourceNotApplicable,
			ObservedAt: ptrTime(a.now().UTC()),
			Reason:     validityReason,
		})
		// CHAOS-4690: the matching NON-degrading detail (this row never sets
		// Partial — see the source comment above), so the structured surface
		// covers the sources[].reason shape too, not only degraded_reasons.
		unboundedCount := unbounded
		appendGraphDetail(contractsv1.ContextFabricCoverageDetailGraphValidityUnbounded, false, &unboundedCount, validityReason, "context-fabric:graph-validity-windows")
	}
	return contextfabric.GraphContext{
		Resolution: request.Resolution, Cohort: cohort, Paths: admission.Paths, DriverCandidates: admission.Drivers,
		EvidenceRefIDs: admission.EvidenceRefIDs, FactRequirements: factRequirements,
		Coverage: contextfabric.Coverage{
			Sources:         sources,
			Partial:         partial,
			DegradedReasons: degradedReasons,
			Details:         coverageDetails,
		},
	}, nil
}

// countUnboundedValidity counts the admitted nodes and edges that carry no
// validity bound on either side. See hasUnboundedValidity and
// temporalFilter.predicate for why those elements are admitted rather than
// excluded, and why the count has to reach the caller.
func countUnboundedValidity(nodes []graphrank.CandidateNode, edges []graphrank.ResolvedEdge) int {
	count := 0
	for _, n := range nodes {
		if hasUnboundedValidity(n.Attributes) {
			count++
		}
	}
	for _, e := range edges {
		if hasUnboundedValidity(e.Attributes) {
			count++
		}
	}
	return count
}

func mustSubject(n graphrank.CandidateNode) contextfabric.SubjectRef {
	subject, _ := graphrank.NodeSubject(n)
	return subject
}

// sortCandidateNodesBySubjectKey sorts nodes in place by graphrank.SubjectKey
// (kind + canonical id) -- CHAOS-4395's determinism fix for
// chaos4348ExactNameCandidates' cohort use: that fetch's own Cypher carries
// no ORDER BY, and DiscoveredCohort ranks members by INPUT order before
// stopping at MaxCohortMembers, so an unranked, unsorted census could admit
// different members across repeated identical calls. A node whose Subject
// cannot be read sorts last (stable, never causes a panic); this list is
// filtered for exactly that a few lines after the caller uses it.
func sortCandidateNodesBySubjectKey(nodes []graphrank.CandidateNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		si, iok := graphrank.NodeSubject(nodes[i])
		sj, jok := graphrank.NodeSubject(nodes[j])
		if !iok {
			return false
		}
		if !jok {
			return true
		}
		return graphrank.SubjectKey(si) < graphrank.SubjectKey(sj)
	})
}

func ptrTime[T any](v T) *T { return &v }

// isReservedIdentityProjectID (CHAOS-3884 step 5) reports whether row is a
// SubjectProject claimant whose canonical id falls inside the reserved
// organization-scope namespace (contractsv1.ContextFabricReservedOrganizationScopePrefix).
// Scoped to SubjectProject only: that reserved namespace collides with
// AuthorizationScope.ProjectIDs specifically (validateReservedOrganizationScope,
// internal/contracts/v1) -- repository claimants carry RepositorySlugs and
// team claimants carry TeamIDs, neither of which that check ever inspects,
// so the collision this guards against is structurally impossible for
// either kind. See the call site's own doc comment for why this is honest
// defense-in-depth rather than a claim of present-day necessity.
func isReservedIdentityProjectID(row graphrank.IdentityRow) bool {
	if row.Kind != contextfabric.SubjectProject {
		return false
	}
	return contractsv1.ContextFabricIsReservedOrganizationScopeID(strings.TrimPrefix(row.CanonicalID, "project:"))
}
