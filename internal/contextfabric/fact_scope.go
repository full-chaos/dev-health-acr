package contextfabric

// CHAOS-4099 stage 1: the fact-read scope resolver, its outcome vocabulary,
// and the honest disclosure that replaces a false prune.
//
// THE DEFECT (ext65 index 60). planFactReads prunes a capability strictly by
// resolved subject KIND. No canonical-fact capability declares
// SupportedSubjectKinds containing `project`, so a committed project subject
// prunes EVERY capability -- pull_requests, reviews and metrics included --
// and the fact read returns zero facts. The model then honestly reports
// no_match. Before CHAOS-4098 that surfaced as a 500; after it, as a
// false_no_match. Either way the answer was wrong for a structural reason
// the answer never disclosed.
//
// WHY THE PRUNE ITSELF IS THE LIE. FactPruneReasonSubjectKindUnsupported is
// documented as a PROOF: "the capability could not have produced a single
// admissible fact". SourcePruned is deliberately excluded from
// factStateDegrades on exactly that ground -- nothing is missing, so the
// answer is not partial. That reasoning is sound for a subject kind nothing
// could ever reach from. It is FALSE for a project: the typed chain
// `project <-BELONGS_TO_PROJECT- work_item -BELONGS_TO_REPOSITORY-> repository`
// exists in prod projection code (devhealthsource/tables.go), and
// `pull_request -BELONGS_TO_REPOSITORY-> repository` plus
// `pull_request_review -BELONGS_TO_PULL_REQUEST-> pull_request` complete it.
// The facts are reachable; the planner simply has no step that reaches them.
// So the investigation recorded "proven nothing missing" over a gap it had
// never looked into.
//
// WHAT THIS FILE DOES, AND DELIBERATELY DOES NOT DO (stage 1). It introduces
// the resolver that OWNS that question, the closed vocabulary that describes
// its answer, and the disclosure that fires when the answer is "this could
// not be reached". Every policy ships DISABLED, so stage 1 changes no fact
// that reaches synthesis: it changes what the answer SAYS about the facts it
// did not get. The traversal that closes the gap is stage 2, gated on the
// preconditions in the ratified ruling (oracle comparison, canonical-ID
// alignment, per-hop authorization proof).
//
// THE TWO SUBJECT SETS (ruling, architecture). RootSubjects -- the committed
// resolution, the cohort, or a requirement's own override -- are untouched
// by anything here. ReadSubjects are what a provider is actually asked
// about: the directly-supported roots, plus (stage 2) authorized derived
// targets. Expansion grants fact-READ permission and nothing else. It never
// mutates SubjectResolution.Committed, never mints a second commit, and
// never becomes a new investigation subject -- DP9's commit bases are
// decided before this runs and are not readable from here.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ---------------------------------------------------------------------------
// Closed vocabulary
// ---------------------------------------------------------------------------

// FactScopePolicy names ONE expansion rule: a specific typed path from one
// origin subject kind to one target subject kind, for one requirement kind.
//
// Named rather than derived, and versioned in the name, because a policy is
// a PRODUCT commitment about what a fact family means for a subject that
// does not directly own those facts -- "the PRs of a project" is a claim,
// not a join. Changing what the chain traverses changes that claim, and a
// new claim gets a new name (`_v2`) rather than a silently different `_v1`.
type FactScopePolicy string

const (
	// FactScopePolicyNone marks a (requirement, origin) pair for which NO
	// expansion policy is defined at all.
	//
	// It is not a placeholder for a future policy name. Team-origin
	// expansion is exactly this case today: CHAOS-4101 confirmed the
	// identical structural gap for `team`, but the team-attribution edge is
	// rule_inferred/source_asserted rather than a typed source-asserted
	// chain, so expanding through it would launder an inference into fact
	// scope. Naming a team policy here would pre-empt the product ruling
	// CHAOS-4101 exists to obtain. The gap is still DISCLOSED -- that is the
	// whole point of this value existing rather than the pair simply being
	// absent from the table.
	FactScopePolicyNone FactScopePolicy = "none"

	// FactScopePolicyProjectWorkItemRepository reaches a project's
	// repositories via its linked work items, for repository-scoped metrics.
	FactScopePolicyProjectWorkItemRepository FactScopePolicy = "project_work_item_repository_v1"
	// FactScopePolicyProjectWorkItemPullRequest continues that chain to the
	// pull requests of those repositories.
	FactScopePolicyProjectWorkItemPullRequest FactScopePolicy = "project_work_item_pull_request_v1"
	// FactScopePolicyProjectWorkItemPullRequestReview continues one hop
	// further, to the reviews on those pull requests.
	FactScopePolicyProjectWorkItemPullRequestReview FactScopePolicy = "project_work_item_pull_request_review_v1"
)

// FactScopeBasis names the EPISTEMIC standing of an expansion path -- what
// kind of claim admitting a derived target makes.
type FactScopeBasis string

const (
	// FactScopeBasisDirect is an edge that asserts the relationship it is
	// used for. Nothing uses it yet; it exists so that a future ownership
	// edge (a real project->repository link, were one ever projected) is
	// distinguishable in telemetry from the proxy below, rather than both
	// arriving as an unlabelled "expanded".
	FactScopeBasisDirect FactScopeBasis = "direct"

	// FactScopeBasisActivityProxy is an edge chain that asserts ACTIVITY,
	// not ownership.
	//
	// This is what every project policy here actually is, and saying so is
	// binding (ruling invariant 6). A "project" in this system is a
	// work-tracking project (Linear-shaped); there is NO project->repository
	// edge. What the chain proves is "this repository has at least one work
	// item linked to this project" -- which is a good enough scope for
	// "how is this project's code doing" and is NOT a statement that the
	// project owns the repository. A reader who is not told the difference
	// will read the second meaning into the first, so any result derived on
	// this basis is disclosed as a proxy.
	FactScopeBasisActivityProxy FactScopeBasis = "activity_proxy"
)

// FactScopeExpansionOutcome is what the resolver decided for one
// (requirement, origin kind, policy) triple.
//
// THIS VOCABULARY IS DELIBERATELY NOT SourcePruned, AND MUST NEVER BECOME IT
// (ruling invariant 7). SourcePruned asserts a proof -- "nothing is missing".
// Every value below except NotNeeded describes a case where the system does
// NOT hold that proof: it did not look (PolicyUnavailable), it looked and the
// chain ended (AttemptedEmpty), it looked and could not finish (Truncated /
// Failed). Collapsing those into "pruned" is precisely the defect this
// ticket names, and the reason the two vocabularies are separate types
// rather than a widened reason string.
type FactScopeExpansionOutcome string

const (
	// FactScopeNotNeeded: at least one root subject is directly supported by
	// the capability, so this requirement is answerable without expanding.
	// No disclosure, no degradation -- the ordinary path.
	FactScopeNotNeeded FactScopeExpansionOutcome = "not_needed"

	// FactScopePolicyUnavailable: expansion WOULD have been needed, and no
	// usable policy ran. Stage 1's universal outcome (every policy ships
	// disabled), and permanently the outcome for a non-current time axis and
	// for team origins pending CHAOS-4101. The honest statement is "this was
	// not attempted", which is why it degrades the answer.
	FactScopePolicyUnavailable FactScopeExpansionOutcome = "policy_unavailable"

	// FactScopeAttemptedEmpty: the policy ran and the chain yielded no
	// target. This is the one non-not_needed outcome that is genuinely a
	// proof of absence for THIS question -- the project really has no linked
	// work item that reaches a repository -- so it does not degrade.
	FactScopeAttemptedEmpty FactScopeExpansionOutcome = "attempted_empty"

	// FactScopeTargetKindMismatch: the policy produced targets, but none of
	// the kind the capability supports. A policy/capability wiring error
	// rather than a data statement; reported distinctly so it cannot hide
	// inside attempted_empty.
	FactScopeTargetKindMismatch FactScopeExpansionOutcome = "target_kind_mismatch"

	// FactScopeExpanded: the policy ran, targets were admitted, and the
	// traversal was complete.
	FactScopeExpanded FactScopeExpansionOutcome = "expanded"

	// FactScopeExpandedPartial: targets were admitted but the traversal hit
	// a cap. Detected by limit+1, never by a full page (ruling invariant 8):
	// a result that exactly fills the limit is indistinguishable from a
	// truncated one without the extra row, and guessing there would be
	// silent truncation -- the failure this value exists to make loud.
	FactScopeExpandedPartial FactScopeExpansionOutcome = "expanded_partial"

	// FactScopeFailed: the traversal backend errored. Fails CLOSED: no
	// derived target is admitted from a partial failure, because a subject
	// set assembled from a half-completed authorization pass is not a
	// subject set anyone may read facts for.
	FactScopeFailed FactScopeExpansionOutcome = "failed"

	// FactScopeMatchedUnauthorized: the chain ran to completion, candidates
	// existed, and every one of them was dropped by authorization -- a
	// caller unauthorized for a project's repositories traversing that same
	// project.
	//
	// RULED 2026-08-22 (design doc §6b), SUPERSEDING an earlier same-day
	// ruling that collapsed this case into attempted_empty. Reporting an
	// all-auth-dropped traversal as attempted_empty is a FALSE proof of
	// absence (invariant 7): targets existed, the caller simply could not
	// see them. The fix is not to make the two cases indistinguishable --
	// it is to give this one its own honest name. Invariant 9 is amended to
	// permit it: within an org, repository EXISTENCE is not confidential
	// (org access already implies knowing a repository exists; what is
	// actually restricted is its CONTENT), so disclosing that unauthorized
	// repositories exist protects nothing invariant 9 was written to guard.
	// Fact content from an unauthorized repository never reaches the
	// caller under ANY outcome, including this one.
	//
	// SCOPED TO THE ALL-DROPPED CASE ONLY (team-lead ruling 2026-08-22): a
	// MIXED traversal -- some candidates admitted, some auth-dropped --
	// stays FactScopeExpanded/FactScopeExpandedPartial and does not raise
	// this outcome or its warning. No false proof of absence is made in the
	// mixed case (real facts DO reach the answer), so invariant 7 is not in
	// play there; extending the warning to that case is a deliberate v1
	// limitation, left to a possible fast-follow (see design doc §6b).
	FactScopeMatchedUnauthorized FactScopeExpansionOutcome = "matched_unauthorized"
)

// FactScopeFailureClass is the closed sub-vocabulary for FactScopeFailed.
// Empty for every other outcome.
type FactScopeFailureClass string

const (
	// FactScopeFailureNone is the value carried by any outcome that is not a
	// failure. Explicit rather than "the zero value happens to be empty", so
	// a telemetry consumer can assert on it.
	FactScopeFailureNone FactScopeFailureClass = ""
	// FactScopeFailureBackendUnavailable: the graph backend could not be
	// reached or returned an error.
	FactScopeFailureBackendUnavailable FactScopeFailureClass = "backend_unavailable"
	// FactScopeFailureTimeout: the traversal exceeded its own deadline.
	FactScopeFailureTimeout FactScopeFailureClass = "timeout"
	// FactScopeFailureAuthorization: the authorization check itself failed
	// (distinct from a subject being authorization-DROPPED, which is a
	// normal, telemetry-only outcome -- see AuthorizationDroppedCount).
	FactScopeFailureAuthorization FactScopeFailureClass = "authorization_error"
)

// ---------------------------------------------------------------------------
// The telemetry event (CHAOS-4089 standing order)
// ---------------------------------------------------------------------------

// FactScopeExpansionEvent is ONE resolver decision, reported to
// EngineTelemetry exactly once per (requirement, origin kind, policy).
//
// CONTENT-SAFE BY CONSTRUCTION, the same discipline every event beside it
// holds: closed enums and counts only. No canonical id, no label, no
// question text, no repository slug, no error string ever reaches a field
// here. A reader who needs to know WHICH project correlates by request_id
// with the resolution trace, where subject identity legitimately lives.
//
// WHY THE COUNTS ARE SPLIT THE WAY THEY ARE. A single "we got N targets"
// number cannot distinguish the four ways expansion under-delivers, and each
// one demands a different response from an operator:
//
//   - AuthorizationDroppedCount: the traversal found targets the caller may
//     not read. NORMAL and expected on a shared graph. It is telemetry-ONLY
//     and never reaches the answer or public provenance (ruling invariant 9)
//     -- disclosing "there were 3 repositories you cannot see" is an
//     existence side-channel, which is the same class of leak
//     RecordSubjectlessTerminal's authz_filtered_to_empty reason is kept out
//     of the response contract for.
//   - TemporalDroppedCount: targets excluded by the validity window. A
//     nonzero count on a current-axis question means the projection's
//     validity bounds disagree with "now", which is a projection bug, not a
//     scope bug.
//   - MissingNextHopCount: an intermediate entity had no next edge -- e.g. a
//     work item whose repo_id is the zero-UUID sentinel (a Linear issue with
//     no repository). This one is load-bearing for correctness, not just
//     diagnosis: the sentinel must never expand to a fake repository, and a
//     count that moves from N to 0 is how a regression in that filter
//     announces itself.
//   - TargetKindMismatchCount: targets of the wrong kind for the capability.
//     Always a wiring error.
type FactScopeExpansionEvent struct {
	// RequirementKind is the fact family that needed expanding.
	RequirementKind FactKind
	// OriginKind is the kind of the root subjects expansion started from.
	OriginKind SubjectKind
	// TargetKind is the kind the policy aims at -- the kind the capability
	// declares it supports. Set even when nothing was produced, so a reader
	// can see what the attempt was FOR.
	TargetKind SubjectKind
	// Policy is the named rule, or FactScopePolicyNone when the pair has no
	// policy defined.
	Policy FactScopePolicy
	// Basis is the epistemic standing of the path. See FactScopeBasis.
	Basis FactScopeBasis
	// Outcome is the closed verdict.
	Outcome FactScopeExpansionOutcome
	// OriginCount is how many root subjects of OriginKind expansion was
	// asked to start from.
	OriginCount int
	// CandidateCount is how many targets the traversal produced BEFORE any
	// filtering. Zero whenever no traversal ran.
	CandidateCount int
	// AdmittedCount is how many survived every filter and became read
	// subjects. It is the number that actually changed the fact read.
	AdmittedCount int
	// AuthorizationDroppedCount, TemporalDroppedCount, MissingNextHopCount
	// and TargetKindMismatchCount split the difference between
	// CandidateCount and AdmittedCount. See the type comment for why each is
	// separate.
	AuthorizationDroppedCount int
	TemporalDroppedCount      int
	MissingNextHopCount       int
	TargetKindMismatchCount   int
	// TargetKindMismatchCount is how many targets the expander DROPPED
	// itself for being the wrong kind. An expander must not both count a
	// target here and return it: the resolver runs its own defensive filter
	// and adds what IT drops, so returning a target already counted would
	// double-count it. The two are disjoint by contract.
	//
	// Truncated is the EXPANDER's own report that it hit a cap of its own
	// (a per-hop bound, a backend page limit). The resolver ORs its own
	// overflow detection into this rather than replacing it: both are real
	// ways a traversal can be incomplete, and either one alone is enough to
	// make the answer partial.
	Truncated bool
	// FailureClass is set only for FactScopeFailed.
	FailureClass FactScopeFailureClass
}

// ---------------------------------------------------------------------------
// The policy table
// ---------------------------------------------------------------------------

// factScopePolicyRule is one row of the table: which chain, aimed at what,
// on what basis, and whether it is live.
type factScopePolicyRule struct {
	Policy     FactScopePolicy
	TargetKind SubjectKind
	Basis      FactScopeBasis
	// Enabled is stage 2's switch. Every rule ships false (stage 1), and a
	// rule that is enabled but has no expander wired still fails CLOSED to
	// FactScopePolicyUnavailable -- see FactReadScopeResolver.Resolve.
	Enabled bool
	// Limit overrides maxFactScopeTargets for this policy. Zero means the
	// default. A per-policy bound exists because the chains have very
	// different fan-out: one project reaches few repositories but those
	// repositories reach many pull requests, and many more reviews.
	Limit int
	// Chain cites the typed edges this row's reachability claim rests on,
	// naming the prod producer that writes them.
	//
	// REQUIRED ON EVERY ROW, and load-bearing rather than documentation
	// (ruling criterion 1: "no chain citation, no row"). A row in this table
	// asserts that the facts ARE reachable and that the pruner's proof of
	// absence is therefore false. That assertion is only as good as the
	// edges behind it, and an uncited row is someone's guess about
	// reachability wearing the same clothes as a verified one -- which is
	// how this table would drift back into the global widen option D was
	// ratified instead of. TestChaos4099_EveryEligibilityRowCitesItsChain
	// enforces it.
	Chain string
}

// limitOrDefault is the cap this rule's traversal is bounded by.
func (r factScopePolicyRule) limitOrDefault() int {
	if r.Limit > 0 {
		return r.Limit
	}
	return maxFactScopeTargets
}

// The three typed chains every row below rests on, verified against prod
// projection code at 0e662ceb. Named once so a row cites a chain rather than
// restating edges, and so a chain that moves is corrected in one place.
const (
	// factScopeChainWorkItem is the ONE-HOP chain, and the shortest thing in
	// this table: work_items.project_id / the primary team attribution.
	//
	// The team half is epistemically weaker than the project half and that
	// difference is why CHAOS-4101 exists -- OWNED_BY_TEAM comes from
	// work_item_team_attributions, an Ops-COMPUTED attribution whose source
	// enum spans native_team/assignee_membership/issue_project/linked_issue/
	// manual_fallback. Weak enough that no policy may traverse it without a
	// product ruling; NOT weak enough to justify telling a reader "nothing is
	// missing" when the work items are right there.
	factScopeChainWorkItem = "work_item -BELONGS_TO_PROJECT-> project / work_item -OWNED_BY_TEAM-> team (devhealthsource/teams_projects_edges.go: querySubjectProjectMemberships, queryWorkItemTeams)"
	// factScopeChainRepository continues one hop through the work item's own
	// repository. The zero-UUID sentinel lives on this hop: a Linear-sourced
	// work item carries repo_id = the zero UUID and is repo-LESS by design,
	// never an orphan, and must never expand to a fake repository.
	factScopeChainRepository = factScopeChainWorkItem + "; work_item -BELONGS_TO_REPOSITORY-> repository (devhealthsource/tables.go: queryWorkItems)"
	// factScopeChainPullRequest reaches the pull requests of those
	// repositories.
	factScopeChainPullRequest = factScopeChainRepository + "; pull_request -BELONGS_TO_REPOSITORY-> repository (devhealthsource/tables.go: queryPullRequests)"
	// factScopeChainReview reaches the reviews on those pull requests.
	factScopeChainReview = factScopeChainPullRequest + "; pull_request_review -BELONGS_TO_PULL_REQUEST-> pull_request (devhealthsource/tables.go: queryPullRequestReviews)"
)

// factScopeEligibilityRow is one declared row of the table below. Declared as
// a list and folded into the map by factScopePoliciesFrom, so the whole
// eligibility set reads as one auditable block rather than a nested literal
// nobody checks for gaps.
type factScopeEligibilityRow struct {
	Requirement FactKind
	Origins     []SubjectKind
	Rule        factScopePolicyRule
}

// factScopeEligibility is the CLOSED eligibility set: every (requirement
// kind, origin kind) pair for which a missing capability is a REACHABLE gap
// rather than a proof of irrelevance. A pair absent from it keeps CHAOS-3783's
// honest prune.
//
// THE ADMISSION CRITERION IS A VERIFIED TYPED CHAIN (ruling, 2026-08-22). A
// row exists only where the traversal chain exists in prod code AND the row
// cites it. That is what keeps this a closed table rather than the rule
// "anything a project cannot answer directly is a gap" -- which would
// over-claim reachability nobody has shown.
//
// WHY THIS IS WIDER THAN THE THREE ACTIVATION POLICIES. Invariant 7 is the
// controlling rule: SourcePruned asserts "proven nothing missing", and on a
// reachable chain that assertion is FALSE. CHAOS-3783's argument for a
// non-degrading prune is about not degrading where pruning is genuinely
// sound; it cannot justify keeping a false proof of absence. Where the two
// conflict, honesty wins. Concretely: work-item status is ONE typed hop from
// a project -- shorter than the chain the three named policies use -- so
// pruning it was the same defect this ticket exists to fix, on a more
// obviously reachable path.
//
// MEASUREMENT EFFECT, PRE-ADJUDICATED by the ruling: Coverage.Partial fires
// more often on project- and team-scoped questions than it did. That is
// disclosure reflecting reality, not a regression.
//
// WHAT IS DELIBERATELY ABSENT:
//
//   - team-target families from a PROJECT origin (workload, investment,
//     readiness, operational_deficiencies). The chain would run
//     project <- work_item -OWNED_BY_TEAM-> team, which laundle-launders the
//     same computed attribution CHAOS-4101 is holding back -- reaching it
//     from the other direction does not make it stronger.
//   - incidents, deployments, continuous_integration. Chains plausibly
//     exist; none was traced end to end for this ticket, and an uncited row
//     is exactly what criterion 1 forbids.
//   - source_health (organization-scoped). No chain.
//   - health from a TEAM origin: HealthProvider already supports team
//     directly, so it never prunes and never reaches this table.
var factScopeEligibility = []factScopeEligibilityRow{
	// --- The three ACTIVATABLE policies (stage 2, project origin only) ---
	//
	// ENABLED (CHAOS-4099 stage 2, 2026-08-22): every stage-2 activation
	// precondition is satisfied -- see design doc §9 and this change's own
	// PR body for the evidence (oracle comparison, canonical-ID alignment
	// via CHAOS-4108, per-hop authorization proof, activity_proxy and
	// current-axis-only sign-offs). No control-flow change from stage 1:
	// exactly the Enabled flip and a wired FactScopeExpander implementation
	// the design doc's own §8 promised.
	{
		Requirement: FactMetrics, Origins: []SubjectKind{SubjectProject},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainRepository, Enabled: true,
		},
	},
	{
		Requirement: FactPullRequests, Origins: []SubjectKind{SubjectProject},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyProjectWorkItemPullRequest, TargetKind: SubjectPullRequest,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainPullRequest, Enabled: true,
		},
	},
	{
		Requirement: FactReviews, Origins: []SubjectKind{SubjectProject},
		Rule: factScopePolicyRule{
			Policy:     FactScopePolicyProjectWorkItemPullRequestReview,
			TargetKind: contractsv1.ContextFabricSubjectPullRequestReview,
			Enabled:    true,
			Basis:      FactScopeBasisActivityProxy, Chain: factScopeChainReview,
		},
	},

	// --- Disclosure-only rows: reachable, cited, and never traversed ---
	//
	// policy `none` (ruling point 4). These disclose the gap and emit
	// policy_unavailable telemetry; activating any of them is a follow-on
	// ticket with its own preconditions, exactly as CHAOS-4101 is for team.
	{
		Requirement: FactMetrics, Origins: []SubjectKind{SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainRepository,
		},
	},
	{
		Requirement: FactPullRequests, Origins: []SubjectKind{SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectPullRequest,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainPullRequest,
		},
	},
	{
		Requirement: FactReviews, Origins: []SubjectKind{SubjectTeam},
		Rule: factScopePolicyRule{
			Policy:     FactScopePolicyNone,
			TargetKind: contractsv1.ContextFabricSubjectPullRequestReview,
			Basis:      FactScopeBasisActivityProxy, Chain: factScopeChainReview,
		},
	},
	// health is repository-scoped, reached by the SAME chain as metrics.
	// Project origin only -- see WHAT IS DELIBERATELY ABSENT above.
	{
		Requirement: FactHealth, Origins: []SubjectKind{SubjectProject},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainRepository,
		},
	},
	// The work-item families: ONE typed hop, the shortest chain in this
	// table and the clearest case that the old prune asserted a false proof.
	{
		Requirement: FactStatus, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
	{
		Requirement: FactWork, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
	{
		Requirement: FactActualCompletion, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
	{
		Requirement: FactBlockers, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
	{
		Requirement: FactRequiredChildren, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
	{
		Requirement: FactIdentity, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
	{
		Requirement: FactMembership, Origins: []SubjectKind{SubjectProject, SubjectTeam},
		Rule: factScopePolicyRule{
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisActivityProxy, Chain: factScopeChainWorkItem,
		},
	},
}

// factScopePolicies is factScopeEligibility folded into the lookup shape.
// Package-level var rather than a function call per lookup, and reassignable
// so a test can install a narrow table for one case.
var factScopePolicies = factScopePoliciesFrom(factScopeEligibility)

func factScopePoliciesFrom(rows []factScopeEligibilityRow) map[FactKind]map[SubjectKind]factScopePolicyRule {
	table := make(map[FactKind]map[SubjectKind]factScopePolicyRule, len(rows))
	for _, row := range rows {
		byOrigin, exists := table[row.Requirement]
		if !exists {
			byOrigin = map[SubjectKind]factScopePolicyRule{}
			table[row.Requirement] = byOrigin
		}
		for _, origin := range row.Origins {
			byOrigin[origin] = row.Rule
		}
	}
	return table
}

// lookupFactScopePolicy returns the rule for a pair and whether the pair is
// expansion-eligible at all.
//
// An ineligible pair keeps CHAOS-3783's honest prune: no path from that
// subject kind to that fact family has been established, so "nothing is
// missing" remains a statement the system can make. An eligible pair ALWAYS
// discloses -- including one whose policy is FactScopePolicyNone, which is
// the team case in full.
func lookupFactScopePolicy(kind FactKind, origin SubjectKind) (factScopePolicyRule, bool) {
	rule, ok := factScopePolicies[kind][origin]
	return rule, ok
}

// ---------------------------------------------------------------------------
// The resolved scope
// ---------------------------------------------------------------------------

// FactScopeGap is what the fact registry needs to know to report one
// requirement honestly: the outcome, and the policy that produced it.
//
// It carries no subjects. A gap is by definition the case where no derived
// read subject was admitted.
type FactScopeGap struct {
	Outcome FactScopeExpansionOutcome
	Policy  FactScopePolicy
	Basis   FactScopeBasis
	// OriginKind is the kind that could not be expanded from. Used only to
	// compose the coverage reason, which reports KINDS and never identities.
	OriginKind SubjectKind
}

// FactScopeDerivation binds one derived read subject back to the root it was
// derived from and the policy that derived it.
//
// IT IS A PARALLEL STRUCTURE, NOT A FIELD ON SubjectRef (ruling invariant
// 5). CanonicalFact.Subject must stay the exact repository/PR/review the
// fact is about -- that is what makes the fact re-verifiable against its
// source. Hanging a DerivedFrom on SubjectRef would put engine bookkeeping
// on a contract type that is serialized into every stored result, compared
// for reuse, and validated; the derivation is the ENGINE's knowledge about
// how a subject entered scope, not a property of the subject.
type FactScopeDerivation struct {
	Root   SubjectRef
	Target SubjectRef
	Policy FactScopePolicy
	Basis  FactScopeBasis
}

// FactReadScope is the resolver's whole output: what may be read, why, and
// what could not be.
type FactReadScope struct {
	// DerivedSubjects maps a requirement kind to the authorized derived
	// targets admitted for it. Empty in stage 1.
	DerivedSubjects map[FactKind][]SubjectRef
	// Derivations is the flat provenance list. Deterministically ordered.
	Derivations []FactScopeDerivation
	// Gaps maps a requirement kind to its disclosure, when expansion was
	// needed and did not deliver.
	Gaps map[FactKind]FactScopeGap
	// Events is every decision, in requirement order, for telemetry.
	Events []FactScopeExpansionEvent
}

// derivedSubjectsFor returns the derived read subjects for one requirement.
// nil-safe so every caller can treat an absent scope as "no expansion",
// which is exactly what an un-resolved request means.
func (s *FactReadScope) derivedSubjectsFor(kind FactKind) []SubjectRef {
	if s == nil {
		return nil
	}
	return s.DerivedSubjects[kind]
}

// gapFor returns the disclosure for one requirement, if any.
func (s *FactReadScope) gapFor(kind FactKind) (FactScopeGap, bool) {
	if s == nil {
		return FactScopeGap{}, false
	}
	gap, ok := s.Gaps[kind]
	return gap, ok
}

// HasActivityProxyDerivation reports whether any admitted read subject
// entered scope on an activity-proxy basis. It is the trigger for the
// proxy disclosure (ruling invariant 6); false throughout stage 1, because
// nothing is admitted.
func (s *FactReadScope) HasActivityProxyDerivation() bool {
	if s == nil {
		return false
	}
	for _, derivation := range s.Derivations {
		if derivation.Basis == FactScopeBasisActivityProxy {
			return true
		}
	}
	return false
}

// HasDisclosableGap reports whether any requirement needs the unexpanded
// disclosure. Outcomes that are themselves a proof of absence
// (attempted_empty) do not qualify -- nothing is missing in that case, and
// disclosing one would train readers to ignore the disclosure.
func (s *FactReadScope) HasDisclosableGap() bool {
	if s == nil {
		return false
	}
	for _, gap := range s.Gaps {
		if factScopeGapDegrades(gap.Outcome) {
			return true
		}
	}
	return false
}

// MatchedUnauthorizedCount sums AuthorizationDroppedCount across every event
// this resolution recorded a matched_unauthorized outcome for -- the trigger
// AND the count for the existence-disclosure warning (ruled 2026-08-22,
// design doc §6b). Zero whenever no requirement hit that outcome, which
// HasMatchedUnauthorizedGap treats identically to "nothing to disclose."
//
// Summed from Events, not Gaps: Gaps holds one entry PER REQUIREMENT (the
// worst-wins slot), so reading it back would under-count a requirement
// whose matched_unauthorized origin lost the slot to a worse-still gap from
// a different origin kind -- unreachable for the three ruled policies today
// (each has exactly one eligible origin kind, project), but Events is the
// complete, ungated record and costs nothing extra to sum over.
func (s *FactReadScope) MatchedUnauthorizedCount() int {
	if s == nil {
		return 0
	}
	count := 0
	for _, event := range s.Events {
		if event.Outcome == FactScopeMatchedUnauthorized {
			count += event.AuthorizationDroppedCount
		}
	}
	return count
}

// HasMatchedUnauthorizedGap reports whether any requirement's expansion hit
// the matched_unauthorized outcome -- the trigger for the existence-drop
// warning below.
func (s *FactReadScope) HasMatchedUnauthorizedGap() bool {
	return s.MatchedUnauthorizedCount() > 0
}

// factScopeGapDegrades says whether an outcome means something the answer
// wanted is genuinely missing.
//
// attempted_empty and target_kind_mismatch are excluded for opposite
// reasons: the first is a proof of absence (the chain ran and ended), and
// the second is a wiring error that produces no user-visible loss the user
// could act on -- it is loud in telemetry, where the operator who can fix it
// is looking. Everything else means the system did not, or could not, look.
func factScopeGapDegrades(outcome FactScopeExpansionOutcome) bool {
	switch outcome {
	// The system did not look, could not finish, or produced targets it
	// could not use. In every one of these the answer is missing evidence it
	// could have had.
	//
	// target_kind_mismatch is HERE, not below, on the second pass over this
	// decision (codex review). It was originally excluded as "a wiring error
	// with no user-visible loss the user could act on" -- but that reasons
	// about the CAUSE, and Coverage.Partial describes the ANSWER. A
	// mismatched traversal yields no facts, so the answer is exactly as
	// incomplete as if the policy had been disabled; the reader is owed the
	// same disclosure either way. That the operator ALSO gets a loud
	// telemetry event is not a substitute for telling the reader.
	// FactScopeMatchedUnauthorized degrades (ruled 2026-08-22, design doc
	// §6b): candidates existed and coverage is genuinely partial, even
	// though the cause is authorization rather than a wiring error or an
	// incomplete traversal.
	case FactScopePolicyUnavailable, FactScopeExpandedPartial, FactScopeFailed, FactScopeTargetKindMismatch, FactScopeMatchedUnauthorized:
		return true
	// A complete expansion, and a chain that ran and genuinely ended, are
	// the only two outcomes where nothing is missing.
	case FactScopeNotNeeded, FactScopeExpanded, FactScopeAttemptedEmpty:
		return false
	default:
		// FAIL CLOSED on an outcome this function has never heard of. A new
		// value defaulting to "nothing is missing" would reintroduce this
		// ticket's entire defect silently, one enum addition at a time.
		return true
	}
}

// factScopeGapSourceState maps an expansion outcome onto the contract's
// closed SourceState enum.
//
// THE ENUM IS NOT WIDENED, ON PURPOSE. ContextFabricSourceState is a wire
// contract, and CONVENTIONS makes an enum change a new major contract. The
// expansion vocabulary is carried in the structured REASON prefix
// (FactScopeReason*) instead, which is what tests, operators and downstream
// surfaces match on -- the same discipline CHAOS-3783's own prune/narrow
// prefixes established. What matters for the contract is that the state is
// never SourcePruned for a gap, so the answer never claims a proof it does
// not have.
func factScopeGapSourceState(outcome FactScopeExpansionOutcome) SourceState {
	switch outcome {
	case FactScopePolicyUnavailable:
		// Nothing is configured to reach these facts from this subject.
		return SourceUnconfigured
	case FactScopeExpandedPartial:
		return SourceTruncated
	case FactScopeFailed:
		return SourceUnavailable
	case FactScopeTargetKindMismatch:
		// NOT SourceNotApplicable: that state does not degrade, and this
		// outcome does (see factScopeGapDegrades). The targets existed and
		// could not be used, which is what "unavailable" says.
		return SourceUnavailable
	case FactScopeAttemptedEmpty:
		// The chain ran and there was genuinely nothing there.
		return SourceNoData
	case FactScopeMatchedUnauthorized:
		// Candidates existed and some of what was asked for is withheld --
		// the same "some of what you asked for, honestly labelled" shape
		// expanded_partial already carries, not SourceUnavailable (nothing
		// ran) or SourceNoData (nothing was there).
		return SourceTruncated
	default:
		// FAIL CLOSED, same reasoning as factScopeGapDegrades' own default:
		// an unrecognised outcome must not be able to present itself as a
		// clean no_data.
		return SourceUnavailable
	}
}

// ---------------------------------------------------------------------------
// The resolver
// ---------------------------------------------------------------------------

// FactScopeExpander is the port stage 2 implements: one typed traversal,
// authorized at every hop.
//
// Stage 1 wires NO implementation, and a nil expander is not an error -- it
// is the reason every policy resolves to FactScopePolicyUnavailable. That is
// the fail-closed default the ruling asks for: the resolver never assumes an
// expansion succeeded because it could not check.
type FactScopeExpander interface {
	// ExpandFactScope traverses from origins to the policy's target kind.
	// The returned event carries every count the traversal observed; the
	// returned subjects are ONLY those that survived authorization at every
	// hop. An implementation that cannot authorize a hop returns no subject
	// from it rather than returning it unchecked.
	ExpandFactScope(ctx context.Context, request FactScopeExpansionRequest) (FactScopeExpansionResult, error)
}

// FactScopeExpansionRequest is one traversal ask.
type FactScopeExpansionRequest struct {
	// Principal is the AUTHENTICATED caller, threaded through unmodified
	// from ReadFacts (stage 2; unused while every policy stays disabled).
	// An implementation authorizes every hop against THIS value -- never a
	// value it re-derives from request.Origins or from anything read
	// mid-traversal, and never a Principal it constructs itself (see
	// storage.Principal's own doc comment: derived from validated
	// authentication only).
	Principal       storage.Principal
	RequirementKind FactKind
	Origins         []SubjectRef
	Policy          FactScopePolicy
	TargetKind      SubjectKind
	// Limit bounds admitted targets. The implementation must read up to
	// Limit+1 rows and return all of them: the resolver detects truncation
	// from the OVERFLOW row and enforces the cap itself (ruling invariant 8).
	//
	// The resolver does not trust Counts.Truncated alone. An expander that
	// issued `LIMIT 200` instead of `LIMIT 201` would return exactly 200
	// rows with Truncated=false, and a full page is indistinguishable from a
	// truncated one -- which is precisely the silent truncation invariant 8
	// forbids. The overflow row is the only evidence that distinguishes
	// them, so the party that owns the invariant is the party that checks
	// for it.
	Limit int
}

// FactScopeExpansionResult is one traversal answer.
type FactScopeExpansionResult struct {
	// Targets are authorized, deduplicated and deterministically ordered.
	Targets []SubjectRef
	// Counts carries every diagnostic count; the resolver copies them onto
	// the emitted event rather than recomputing them.
	Counts FactScopeExpansionCounts
}

// FactScopeExpansionCounts is the diagnostic split. See
// FactScopeExpansionEvent for what each one is for.
type FactScopeExpansionCounts struct {
	CandidateCount            int
	AuthorizationDroppedCount int
	TemporalDroppedCount      int
	MissingNextHopCount       int
	TargetKindMismatchCount   int
	Truncated                 bool
}

// maxFactScopeTargets bounds how many derived subjects one requirement may
// contribute. It sits well below maxCanonicalFactsPerBundle: a derived
// target is a subject a provider is then queried about, so the cost is a
// query per target, not a fact per target.
const maxFactScopeTargets = 200

// FactReadScopeResolver decides, after resolution and before planFactReads,
// which subjects each fact requirement may be READ for.
//
// It sits between the two deliberately. Later than resolution, so it can
// never feed back into a commit decision -- DP9's committed set and commit
// bases are already final and are not inputs here (ruling invariant 1).
// Earlier than planFactReads, so the planner keeps its single, auditable
// rule (subject kind vs. SupportedSubjectKinds) and providers keep their
// declared SupportedSubjectKinds UNCHANGED (ruling invariant 3, acceptance
// point 3). Nothing about a provider's contract moves; what moves is the
// subject list the planner is handed.
type FactReadScopeResolver struct {
	// expander performs the traversal. nil in stage 1.
	expander FactScopeExpander
}

// NewFactReadScopeResolver builds the resolver. A nil expander yields the
// stage-1 resolver: every policy resolves to policy_unavailable, disclosed.
func NewFactReadScopeResolver(expander FactScopeExpander) *FactReadScopeResolver {
	return &FactReadScopeResolver{expander: expander}
}

// factScopeResolveInput is everything Resolve may read. Narrow by
// construction, exactly like factPlanInput: no question prose reaches here,
// so no model output can influence which subjects become readable.
type factScopeResolveInput struct {
	// Roots is the investigation-wide subject set (RootSubjects). Never
	// mutated.
	Roots []SubjectRef
	// Requirements is the requested fact families, with their own subject
	// overrides.
	Requirements []factPlanRequirement
	// Axis is the interpreted temporal axis. v1 expands on `current` ONLY.
	Axis TemporalAxis
}

// newFactScopeResolveInput narrows a fact request down to what scope
// resolution may see -- the single place that narrowing happens, so the
// reachable field set is auditable in one read.
func newFactScopeResolveInput(request CanonicalFactRequest) factScopeResolveInput {
	requirements := make([]factPlanRequirement, 0, len(request.Requirements))
	for _, requirement := range request.Requirements {
		requirements = append(requirements, factPlanRequirement{
			Kind: requirement.Kind, Subjects: requirement.Subjects,
		})
	}
	return factScopeResolveInput{
		Roots:        investigationScopeSubjects(request),
		Requirements: requirements,
		Axis:         request.Question.TimeContext.Axis,
	}
}

// Resolve produces the read scope for one investigation.
//
// It emits exactly one event per (requirement, origin kind, policy) triple
// that needed a decision, and NONE for a requirement already answerable from
// its roots -- a not_needed event per ordinary requirement would drown the
// stream this exists to make readable. Requirements are walked in input
// order and origin kinds in a sorted order, so the event sequence and the
// derived subject sets are deterministic (ruling invariant 8).
func (r *FactReadScopeResolver) Resolve(
	ctx context.Context,
	principal storage.Principal,
	input factScopeResolveInput,
	capabilities map[FactKind]FactCapability,
) FactReadScope {
	scope := FactReadScope{
		DerivedSubjects: map[FactKind][]SubjectRef{},
		Gaps:            map[FactKind]FactScopeGap{},
	}
	for _, requirement := range input.Requirements {
		capability, registered := capabilities[requirement.Kind]
		if !registered {
			// An unregistered kind is SourceUnconfigured through ReadFacts'
			// own path and has no capability to declare a target kind
			// against. Expanding scope for a provider that does not exist
			// would be inventing a gap.
			continue
		}
		roots := requirement.Subjects
		if len(roots) == 0 {
			roots = input.Roots
		}
		supported, _ := partitionBySupportedSubjectKind(roots, capability.SupportedSubjectKinds)
		if len(supported) > 0 {
			// At least one root is directly readable. Expansion is not
			// needed, and adding derived subjects here would silently widen
			// a requirement the caller scoped -- mixed direct+derived roots
			// are stage 2's explicit case, gated on the same policy switch.
			continue
		}
		r.resolveRequirement(ctx, principal, &scope, requirement.Kind, roots, input.Axis)
	}
	sortFactScopeDerivations(scope.Derivations)
	return scope
}

// resolveRequirement handles ONE requirement that no root can answer
// directly, walking its origin kinds in deterministic order.
func (r *FactReadScopeResolver) resolveRequirement(
	ctx context.Context,
	principal storage.Principal,
	scope *FactReadScope,
	kind FactKind,
	roots []SubjectRef,
	axis TemporalAxis,
) {
	byOriginKind := map[SubjectKind][]SubjectRef{}
	for _, root := range roots {
		byOriginKind[root.Kind] = append(byOriginKind[root.Kind], root)
	}
	originKinds := make([]SubjectKind, 0, len(byOriginKind))
	for originKind := range byOriginKind {
		originKinds = append(originKinds, originKind)
	}
	sort.Slice(originKinds, func(i, j int) bool { return originKinds[i] < originKinds[j] })

	for _, originKind := range originKinds {
		rule, eligible := lookupFactScopePolicy(kind, originKind)
		if !eligible {
			// CHAOS-3783's prune is correct and stays correct for this kind:
			// there is no known path from it, so "nothing is missing" is a
			// statement the system can still make honestly.
			continue
		}
		origins := byOriginKind[originKind]
		event := FactScopeExpansionEvent{
			RequirementKind: kind,
			OriginKind:      originKind,
			TargetKind:      rule.TargetKind,
			Policy:          rule.Policy,
			Basis:           rule.Basis,
			OriginCount:     len(origins),
			FailureClass:    FactScopeFailureNone,
		}

		// FAIL-CLOSED LADDER. Each rung is a reason the resolver may not
		// expand, and every one lands on the SAME outcome -- the system did
		// not look -- rather than on a quietly different story per cause.
		// The cause is recoverable from the event's Policy field and the
		// axis, which telemetry already carries.
		switch {
		case rule.Policy == FactScopePolicyNone:
			// No policy defined for this pair. Team origins, today.
			event.Outcome = FactScopePolicyUnavailable
		case !rule.Enabled:
			// Stage 1, and any future policy shipped dark.
			event.Outcome = FactScopePolicyUnavailable
		case axis != contractsv1.ContextFabricTemporalCurrent:
			// v1 IS CURRENT-AXIS ONLY, and this is a hard gate rather than a
			// best-effort. The work_item->project edge carries ObservedAt
			// but no ValidFrom/ValidTo, so there is no sound way to ask
			// "which repositories did this project touch as of last March" --
			// the traversal would silently answer with TODAY's membership
			// and label it historical. Refusing and disclosing is the only
			// honest option until projection validity lands for that edge.
			event.Outcome = FactScopePolicyUnavailable
		case r.expander == nil:
			// An enabled policy with nothing to execute it. Fails closed for
			// the same reason as every rung above.
			event.Outcome = FactScopePolicyUnavailable
		default:
			r.expand(ctx, principal, scope, &event, origins, rule)
		}

		scope.Events = append(scope.Events, event)
		// A COMPLETE expansion that actually admitted something is the only
		// outcome with no gap to record. expanded_partial deliberately does
		// record one even though it admitted targets: it admitted SOME, and
		// an answer built on a truncated scope is incomplete however many
		// rows it got.
		if event.Outcome == FactScopeExpanded && event.AdmittedCount > 0 {
			continue
		}
		// A gap is recorded per requirement. The coverage contract allows one
		// observation per source, so several origin kinds compete for one
		// disclosure slot; the per-origin detail lives in the events, which
		// are emitted for every one of them.
		//
		// THE WORST OUTCOME WINS, not the first.
		//
		// Codex round 3 (Medium): first-in-sorted-order was a THIRD instance
		// of the round-2 masking class, one level up from the two fixed
		// inside expand(). attempted_empty is non-degrading and maps to
		// SourceNoData; policy_unavailable, failed, target_kind_mismatch and
		// expanded_partial all degrade. Origin kinds are walked sorted, so
		// `project` is decided before `team`: a project chain that ran and
		// genuinely found nothing took the slot and DISCARDED a team gap that
		// was still owed a disclosure. The bundle then reported a clean
		// SourceNoData with no Coverage.Partial, and HasDisclosableGap --
		// which reads the same map -- returned false, so the answer's
		// sentence vanished too. A clean outcome must never evict a degrading
		// one.
		//
		// Among gaps that agree on whether they degrade, the first in sorted
		// order still wins, so the choice stays a deterministic function of
		// the input (ruling invariant 8).
		candidate := FactScopeGap{
			Outcome:    event.Outcome,
			Policy:     event.Policy,
			Basis:      event.Basis,
			OriginKind: originKind,
		}
		existing, exists := scope.Gaps[kind]
		if !exists || (!factScopeGapDegrades(existing.Outcome) && factScopeGapDegrades(candidate.Outcome)) {
			scope.Gaps[kind] = candidate
		}
	}
}

// expand runs the wired expander for one enabled policy and folds the result
// into the scope. Stage 1 never reaches it (expander is nil); it is written
// here so stage 2 activates a policy by flipping Enabled and wiring an
// implementation, with no control-flow change to review a second time.
func (r *FactReadScopeResolver) expand(
	ctx context.Context,
	principal storage.Principal,
	scope *FactReadScope,
	event *FactScopeExpansionEvent,
	origins []SubjectRef,
	rule factScopePolicyRule,
) {
	result, err := r.expander.ExpandFactScope(ctx, FactScopeExpansionRequest{
		Principal:       principal,
		RequirementKind: event.RequirementKind,
		Origins:         origins,
		Policy:          rule.Policy,
		TargetKind:      rule.TargetKind,
		Limit:           rule.limitOrDefault(),
	})
	event.CandidateCount = result.Counts.CandidateCount
	event.AuthorizationDroppedCount = result.Counts.AuthorizationDroppedCount
	event.TemporalDroppedCount = result.Counts.TemporalDroppedCount
	event.MissingNextHopCount = result.Counts.MissingNextHopCount
	event.TargetKindMismatchCount = result.Counts.TargetKindMismatchCount
	event.Truncated = result.Counts.Truncated
	if err != nil {
		// FAILS CLOSED: not one target from a failed traversal is admitted,
		// even if some came back before the error. A subject set assembled
		// from a half-finished authorization pass is not one anybody may
		// read facts for.
		event.Outcome = FactScopeFailed
		event.FailureClass = classifyFactScopeFailure(err)
		event.AdmittedCount = 0
		return
	}
	// The target-kind check is re-run HERE rather than trusted from the
	// expander: this is the last point before a subject becomes readable,
	// and a provider must never be handed a subject kind its capability says
	// it cannot answer. buildFactQuery rejects an unsupported kind by
	// failing the WHOLE bundle, so a miswired policy that slipped through
	// would cost the entire investigation, not just this requirement.
	admitted := make([]SubjectRef, 0, len(result.Targets))
	mismatched := 0
	for _, target := range result.Targets {
		if target.Kind != rule.TargetKind {
			mismatched++
			continue
		}
		admitted = append(admitted, target)
	}
	// The expander's own mismatch count and the resolver's are DISJOINT by
	// contract: an expander reports only targets it dropped ITSELF and never
	// returns them, so summing cannot double-count (codex review raised the
	// ambiguity; this is the contract that resolves it, stated on
	// FactScopeExpansionCounts.TargetKindMismatchCount too).
	event.TargetKindMismatchCount += mismatched
	// CAP ENFORCED HERE, from the OVERFLOW row (ruling invariant 8; codex
	// review finding). The expander is asked for up to Limit+1 targets
	// precisely so that "we got a full page" and "there was more" are
	// distinguishable, and the distinction is worthless if the party that
	// owns the invariant delegates it. An expander that under-reports
	// Truncated, or ignores the limit entirely and returns thousands, is
	// contained by this rather than believed.
	overflowed := false
	if len(admitted) > rule.limitOrDefault() {
		admitted = admitted[:rule.limitOrDefault()]
		overflowed = true
	}
	event.Truncated = result.Counts.Truncated || overflowed
	event.AdmittedCount = len(admitted)
	// OUTCOME LADDER, ORDERED SO THAT NOTHING DEGRADING CAN BE MASKED BY
	// SOMETHING CLEAN. Both orderings below were wrong on the first pass
	// (codex review), and both failed in the same direction: an outcome that
	// meant "evidence is missing" was reported as one that meant "nothing is
	// missing" -- this ticket's own defect, reintroduced inside its fix.
	switch {
	case event.TargetKindMismatchCount > 0:
		// A wiring error, reported EVEN WHEN some valid targets survived.
		// Checking len(admitted)==0 first meant a chain returning one good
		// repository and one wrong-kind work item was reported as a clean
		// `expanded`: the bad candidate vanished with no gap, no disclosure
		// and no degradation, and the only trace was a count nobody was
		// alerted to. Whatever else the traversal got right, it produced
		// something it could not use, and the answer is short of evidence.
		event.Outcome = FactScopeTargetKindMismatch
		if len(admitted) == 0 {
			return
		}
	case event.Truncated:
		// BEFORE the empty check, not after. A traversal that hit a cap and
		// admitted nothing (every candidate on the first page dropped by
		// authorization, with more pages behind it) is NOT a proof that
		// there is nothing there -- and attempted_empty says exactly that,
		// non-degrading, logged at INFO. It is the least-evidence case of
		// all, so it must be the loudest, not the quietest.
		event.Outcome = FactScopeExpandedPartial
		if len(admitted) == 0 {
			return
		}
	case len(admitted) == 0 && event.AuthorizationDroppedCount > 0:
		// The chain ran to completion, candidates existed, and every one
		// was dropped by authorization -- NOT a proof of absence (ruled
		// 2026-08-22, design doc §6b, superseding an earlier same-day
		// ruling that collapsed this into attempted_empty). Distinct from
		// the Truncated branch above: this traversal saw every candidate
		// there was (no more pages behind it) and none of them survived
		// authorization, which is a different, and more complete, picture
		// than "we stopped partway through and everything so far was
		// dropped."
		event.Outcome = FactScopeMatchedUnauthorized
		return
	case len(admitted) == 0:
		// The chain genuinely ran to completion and there was nothing
		// there -- no candidates at all, or candidates that were dropped
		// for a reason OTHER than authorization (temporal, missing next
		// hop). The ONE outcome here that is a real proof of absence.
		event.Outcome = FactScopeAttemptedEmpty
		return
	default:
		event.Outcome = FactScopeExpanded
	}
	// DEDUP AND CAP AT THE REQUIREMENT LEVEL, not merely per event.
	//
	// Both bounds above are per-(origin kind) because that is the unit an
	// expander is called for, and a requirement can have several origin
	// kinds. Without this second pass, two origin groups could each admit
	// `limit` targets for ONE requirement -- twice the cap the policy
	// declares -- and a target reachable from both would be admitted twice,
	// which buildFactQuery rejects outright ("fact query subjects must be
	// unique") and thereby fails the WHOLE bundle rather than this one
	// requirement.
	//
	// AdmittedCount is recomputed from what actually survives, so the
	// telemetry reports the subjects the provider is really asked about
	// rather than the subjects this origin group happened to produce.
	existing := scope.DerivedSubjects[event.RequirementKind]
	seen := make(map[string]struct{}, len(existing)+len(admitted))
	for _, target := range existing {
		seen[canonicalFactSubjectKey(target)] = struct{}{}
	}
	kept := make([]SubjectRef, 0, len(admitted))
	for _, target := range admitted {
		if len(existing)+len(kept) >= rule.limitOrDefault() {
			event.Truncated = true
			// Never downgrade a mismatch verdict: both are degrading, but
			// target_kind_mismatch names a wiring error an operator must fix
			// and expanded_partial names a bound working as designed.
			if event.Outcome != FactScopeTargetKindMismatch {
				event.Outcome = FactScopeExpandedPartial
			}
			break
		}
		key := canonicalFactSubjectKey(target)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, target)
	}
	event.AdmittedCount = len(kept)
	if len(kept) == 0 {
		// Everything this group produced was already in scope from another
		// origin. Nothing NEW was reached, and nothing is claimed -- but a
		// degrading verdict already reached above stands, for the same
		// reason the ladder is ordered the way it is.
		if !factScopeGapDegrades(event.Outcome) {
			event.Outcome = FactScopeAttemptedEmpty
		}
		return
	}
	scope.DerivedSubjects[event.RequirementKind] = append(existing, kept...)
	for _, target := range kept {
		// Provenance binds every admitted target to the policy that admitted
		// it. Root is recorded per ORIGIN GROUP rather than per edge -- the
		// expander's own traversal knows which specific work item linked
		// which repository, but that detail is neither needed here nor safe
		// to carry into a structure the answer is composed from.
		scope.Derivations = append(scope.Derivations, FactScopeDerivation{
			Root:   origins[0],
			Target: target,
			Policy: rule.Policy,
			Basis:  rule.Basis,
		})
	}
}

// classifyFactScopeFailure maps a traversal error onto the closed failure
// vocabulary.
//
// A DEADLINE IS SPLIT OUT FROM EVERY OTHER ERROR because the two demand
// opposite operator responses: a timeout usually means the traversal is too
// wide or the cap too high (tune the policy), while an unreachable backend
// means the graph is sick (page someone). Collapsing them would leave the
// most common tuning signal indistinguishable from an outage.
//
// Everything else defaults to backend_unavailable, which is the conservative
// reading -- it says "the traversal did not complete", true of every error.
// Stage 2 adds cases here alongside the implementation that produces the
// errors they classify; a class is never inferred from an error STRING,
// which is why authorization_error has no case yet: nothing returns a typed
// error for it, and matching on message text is how a vocabulary silently
// stops being closed.
func classifyFactScopeFailure(err error) FactScopeFailureClass {
	switch {
	case err == nil:
		return FactScopeFailureNone
	case errors.Is(err, context.DeadlineExceeded):
		return FactScopeFailureTimeout
	default:
		return FactScopeFailureBackendUnavailable
	}
}

// sortFactScopeDerivations gives provenance a total, content-independent
// order so two runs over the same graph produce byte-identical scope
// (ruling invariant 8). Keyed on policy then canonical id: the policy groups
// related derivations together for a reader, and the canonical id is the
// only per-subject value guaranteed unique.
func sortFactScopeDerivations(derivations []FactScopeDerivation) {
	sort.SliceStable(derivations, func(i, j int) bool {
		if derivations[i].Policy != derivations[j].Policy {
			return derivations[i].Policy < derivations[j].Policy
		}
		return derivations[i].Target.CanonicalID < derivations[j].Target.CanonicalID
	})
}

// ---------------------------------------------------------------------------
// Engine-side: telemetry emission and answer disclosure
// ---------------------------------------------------------------------------

// factScopeUnexpandedLimitation is the answer-facing disclosure for a
// requirement scope expansion could not reach.
//
// FIXED and non-interpolated, the same discipline every service disclosure
// holds. See the contract constant's own doc comment for why it names no
// fact family, policy or subject kind.
const factScopeUnexpandedLimitation = contractsv1.ContextFabricFactScopeUnexpandedLimitation

// factScopeActivityProxyLimitation is the answer-facing disclosure that some
// evidence was gathered by activity association rather than ownership
// (ruling invariant 6). See the contract constant's own doc comment.
const factScopeActivityProxyLimitation = contractsv1.ContextFabricFactScopeActivityProxyLimitation

// recordFactScopeExpansion emits every decision the resolver made.
//
// Needs no type assertion: RecordFactScopeExpansion is a method on
// EngineTelemetry itself, so a sink that drops it fails to compile. See that
// method's own doc comment for why this ticket refused the optional-interface
// shape CHAOS-4085 lost a whole signal to.
func (e *Engine) recordFactScopeExpansion(ctx context.Context, principal storage.Principal, scope *FactReadScope) {
	if scope == nil || e.telemetry == nil {
		return
	}
	for _, event := range scope.Events {
		e.telemetry.RecordFactScopeExpansion(ctx, principal, event)
	}
}

// applyFactScopeDisclosure states, in the answer itself, that some requested
// evidence was never reachable from this question's subject.
//
// INVARIANTS, structural rather than incidental:
//
//   - NARROW TRIGGER: fires only when the resolver holds a DEGRADING gap.
//     attempted_empty is excluded on purpose -- the chain ran and ended, so
//     nothing is missing and a disclosure there would train readers to
//     ignore the disclosure everywhere else.
//   - SUBTRACTIVE ONLY: it adds a limitation and sets Coverage.Partial. It
//     never changes the status, never adds or removes a driver, a finding or
//     a claim, and never touches SubjectResolution. A gap does not make an
//     answer wrong, it makes it incomplete, and the contract already has the
//     word for that.
//   - IDEMPOTENT: appendBoundedLimitations skips an addition already stated,
//     and Coverage.Partial is already true on a second call.
//   - LEAKS NOTHING: the string is a constant. Not one field of the gap --
//     not the origin kind, not the policy, not a count -- reaches the
//     answer. All of it is on the telemetry stream instead.
func applyFactScopeDisclosure(result *InvestigationResult, scope *FactReadScope) {
	if result == nil {
		return
	}
	// TWO INDEPENDENT LIMITATION DISCLOSURES, EITHER OR BOTH.
	//
	// They say opposite things and neither implies the other. The gap
	// disclosure says "we could not reach some evidence"; the proxy
	// disclosure says "we DID reach evidence, by a route weaker than it
	// looks". One investigation can easily be both -- metrics expanded
	// through the activity proxy while reviews hit a disabled policy -- and
	// a reader told only the first would take everything PRESENT in the
	// answer at face value.
	//
	// The proxy half was originally deferred to stage 2 on the reasoning
	// that nothing is admitted while every policy is disabled. Codex review
	// caught that for what it was: HasActivityProxyDerivation existed,
	// documented as invariant 6's trigger, and nothing called it. Wiring it
	// now means stage 2 flips a policy flag rather than also having to
	// introduce an invariant -- and the mechanism is tested before it is
	// load-bearing rather than after.
	disclosures := make([]string, 0, 2)
	if scope.HasDisclosableGap() {
		disclosures = append(disclosures, factScopeUnexpandedLimitation)
	}
	if scope.HasActivityProxyDerivation() {
		disclosures = append(disclosures, factScopeActivityProxyLimitation)
	}
	if len(disclosures) > 0 {
		// appendBoundedLimitations is the ONE path by which anything is
		// added to a composed result's limitations. Both disclosures are
		// registered service-authored in the contract's own list, so each
		// can displace a model caveat but can never itself be the caveat
		// displaced.
		composed, displaced := appendBoundedLimitations(result.Limitations, disclosures)
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
		// The answer does not cover what it set out to cover. This is the
		// same field, set for the same reason, as the retrieval-degradation
		// path (engine.go), the commit-affirmation gate and CHAOS-4098's
		// override.
		//
		// It is set here as well as by appendFactCoverage on the fact
		// BUNDLE (fact_registry.go) rather than instead of it: the
		// bundle's coverage is merged by the synthesizer, and a
		// Synthesizer implementation that composes its own Coverage would
		// otherwise drop the flag on the floor between the two. The
		// result is what is validated, returned and persisted, so the
		// result is where the guarantee has to hold.
		result.Coverage.Partial = true
	}
	// A THIRD, SEPARATE disclosure -- a WARNING, not a limitation (ruled
	// 2026-08-22, design doc §6b, superseding the earlier same-day ruling
	// that kept every authorization drop telemetry-only). Existence-level
	// visibility of an authorization drop is allowed by design within org
	// scope: org access already implies knowing a repository exists, and
	// what disclosing it protects is nothing invariant 9 was written to
	// guard once existence itself is not the secret. Fact CONTENT from an
	// unauthorized repository still never reaches the caller, under any
	// outcome, through any channel -- this warning names a COUNT only.
	//
	// Deliberately a Warning rather than a Limitation: every existing
	// Limitations entry is FIXED and non-interpolated
	// (ContextFabricServiceAuthoredLimitations' own exact-match contract),
	// and this disclosure is "count-level" by ruling -- it cannot be both a
	// fixed string and carry a count. Warnings have no such contract
	// (appendUniqueWarning's own bound-and-dedupe discipline is sufficient).
	if count := scope.MatchedUnauthorizedCount(); count > 0 {
		result.Warnings = appendUniqueWarning(result.Warnings, factScopeMatchedUnauthorizedWarning(count))
	}
}

// factScopeMatchedUnauthorizedWarning renders the count-level existence
// disclosure for an authorization-dropped expansion. COUNT ONLY: no
// repository identity, no policy, no requirement kind, no subject kind --
// "identity naming is implementation freedom within same-org scope" per the
// ruling, and this implementation takes the more conservative option rather
// than assuming the freedom must be used. Full operator detail reaches
// telemetry regardless (RecordFactScopeExpansion's own AuthorizationDroppedCount).
//
// "match(es)", not "repositories" (codex xhigh review round 1, confirmed
// real, LOW): MatchedUnauthorizedCount sums AuthorizationDroppedCount
// across every requirement's own event, and the SAME physical repository
// dropped for metrics, pull_requests, AND reviews in one investigation
// counts three times -- once per requirement, since each runs its own
// traversal and authorization check independently. Deduplicating to a
// true distinct-repository count would mean carrying repository identity
// up into FactReadScope for this purpose alone, which the rest of this
// disclosure path deliberately never does (identity stays out of every
// caller-visible count by design). "Match" is the honest unit for what is
// actually counted: one authorization drop the traversal encountered,
// not one repository guaranteed unique.
func factScopeMatchedUnauthorizedWarning(count int) string {
	noun := "match"
	if count != 1 {
		noun = "matches"
	}
	return fmt.Sprintf("%d repository %s within this organization could not be read due to authorization while gathering evidence for this question, so that content is not included in this answer.", count, noun)
}
