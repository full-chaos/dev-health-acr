package v1

import (
	"errors"
	"strings"
	"time"
)

// ErrContextFabricUnknownRelationshipType is returned, wrapped with the
// offending value, whenever a relationship projection or edge names a type
// outside ContextFabricRelationshipType's closed vocabulary. Named and
// exported -- rather than folded into a generic bounds-violation string --
// so a caller can assert on it directly with errors.Is. The H4 lesson
// (TRD §19.5.5) is that an unrecognized value must fail loudly, never be
// silently admitted or silently dropped.
var ErrContextFabricUnknownRelationshipType = errors.New("context fabric: unknown relationship type")

const (
	ContextFabricInvestigationRequestSchema = "context_fabric_investigation_request.v1"
	ContextFabricInvestigationResultSchema  = "context_fabric_investigation_result.v1"
	ContextFabricProjectionBatchSchema      = "context_fabric_projection_batch.v1"
)

// ContextFabricProjectionBatch{Max...} are the v1 semantic (Go-level)
// per-batch item bounds ContextFabricProjectionBatch.Validate() enforces
// -- stricter than the JSON Schema's wire-format maxItems ceiling, which
// is a looser bound this application-level policy sits inside of.
// Exported (CHAOS-3753 codex round-2 finding K4) so a producer -- e.g.
// devhealthsource's fullSnapshot -- can detect an aggregate-oversized
// batch itself, before ever calling Validate(), rather than duplicating
// these numbers and risking drift.
const (
	ContextFabricProjectionBatchMaxEntities      = 1000
	ContextFabricProjectionBatchMaxRelationships = 5000
	ContextFabricProjectionBatchMaxContents      = 1000
	ContextFabricProjectionBatchMaxEpisodes      = 1000
	ContextFabricProjectionBatchMaxTombstones    = 5000
)

// ContextFabricReservedOrganizationScopePrefix is the reserved namespace a
// synthesized Organization entity uses to encode organization-wide
// authorization inside AuthorizationScope.ProjectIDs -- ContextFabric has
// no dedicated organization-scope field yet (see
// docs/design/context-fabric-projection-worker.md, "Flagged for 1B/1C
// org-level authorization review"). Only an entity/content/episode
// projection whose Subject.Kind is ContextFabricSubjectOrganization may
// ever carry a ProjectIDs value inside this namespace -- Validate() below
// rejects any other producer that does, at the contract boundary, rather
// than leaving it to convention (CHAOS-3753 codex finding W2): a real
// project ID that happened to collide with this prefix would otherwise
// incorrectly inherit organization-wide authorization once downstream
// scope filtering exists.
const ContextFabricReservedOrganizationScopePrefix = "acr-context-fabric:org-scope:"

// ContextFabricIsReservedOrganizationScopeID reports whether id falls
// inside the reserved organization-scope namespace -- see
// ContextFabricReservedOrganizationScopePrefix.
func ContextFabricIsReservedOrganizationScopeID(id string) bool {
	return strings.HasPrefix(id, ContextFabricReservedOrganizationScopePrefix)
}

type ContextFabricInvestigationStatus string

const (
	ContextFabricInvestigationComplete              ContextFabricInvestigationStatus = "complete"
	ContextFabricInvestigationPartial               ContextFabricInvestigationStatus = "partial"
	ContextFabricInvestigationDegraded              ContextFabricInvestigationStatus = "degraded"
	ContextFabricInvestigationClarificationRequired ContextFabricInvestigationStatus = "clarification_required"
	ContextFabricInvestigationNoMatch               ContextFabricInvestigationStatus = "no_match"
)

type ContextFabricInvestigationShape string

const (
	ContextFabricShapeSingleSubject    ContextFabricInvestigationShape = "single_subject"
	ContextFabricShapeExplicitCohort   ContextFabricInvestigationShape = "explicit_cohort"
	ContextFabricShapeDiscoveredCohort ContextFabricInvestigationShape = "discovered_cohort"
	ContextFabricShapeOpen             ContextFabricInvestigationShape = "open"
)

type ContextFabricSubjectKind string

const (
	ContextFabricSubjectOrganization ContextFabricSubjectKind = "organization"
	ContextFabricSubjectTeam         ContextFabricSubjectKind = "team"
	ContextFabricSubjectProject      ContextFabricSubjectKind = "project"
	ContextFabricSubjectRepository   ContextFabricSubjectKind = "repository"
	ContextFabricSubjectWorkItem     ContextFabricSubjectKind = "work_item"
	ContextFabricSubjectPullRequest  ContextFabricSubjectKind = "pull_request"
	ContextFabricSubjectDeployment   ContextFabricSubjectKind = "deployment"
	ContextFabricSubjectIncident     ContextFabricSubjectKind = "incident"
	ContextFabricSubjectDocument     ContextFabricSubjectKind = "document"
	ContextFabricSubjectDecision     ContextFabricSubjectKind = "decision"
	ContextFabricSubjectEpisode      ContextFabricSubjectKind = "episode"
	ContextFabricSubjectMetric       ContextFabricSubjectKind = "metric"
	// ContextFabricSubjectPullRequestReview and ContextFabricSubjectCIRun
	// (CHAOS-3753 codex finding C7) are additive v1 enum values -- new
	// entity kinds a producer may emit, not a change in meaning for any
	// existing one, so this stays v1 per contracts/AGENTS.md ("narrowed
	// values...require a new major version"; this is a widening).
	ContextFabricSubjectPullRequestReview ContextFabricSubjectKind = "pull_request_review"
	ContextFabricSubjectCIRun             ContextFabricSubjectKind = "ci_pipeline_run"
	// ContextFabricSubjectWorkItemRef (CHAOS-3898 §1.5, P5) is a NON-
	// AUTHORITATIVE stub subject kind for a work_item_dependency/
	// work_item_hierarchy edge's target when the row's raw target id does
	// not resolve to a real work_items row at projection time. It carries
	// no authorization scope of its own, is never internal-node-excluded
	// the way a document/episode observation is (it IS user-visible, as
	// the honest "we don't know what this is yet" placeholder), and is
	// never a fact-eligible or census-eligible subject -- see
	// devhealthfacts' and the census registry's own kind allowlists,
	// neither of which lists it. It heals deterministically: once the
	// same raw target id resolves on a later sync, the producer emits the
	// real edge AND tombstones the ref-form edge/node in the SAME batch
	// (design brief §1.5, ProjectionTombstone). This is an additive v1
	// enum value, same widening discipline as
	// ContextFabricSubjectPullRequestReview/ContextFabricSubjectCIRun
	// above.
	ContextFabricSubjectWorkItemRef ContextFabricSubjectKind = "work_item_ref"
)

type ContextFabricResolutionState string

const (
	ContextFabricResolutionCommitted  ContextFabricResolutionState = "committed"
	ContextFabricResolutionProposed   ContextFabricResolutionState = "proposed"
	ContextFabricResolutionAmbiguous  ContextFabricResolutionState = "ambiguous"
	ContextFabricResolutionUnresolved ContextFabricResolutionState = "unresolved"
)

// ContextFabricSubjectMatchMechanism names HOW a subject candidate was
// proposed (CHAOS-3778 / AC-3778-6): a reader can tell a vector match from an
// exact match, an alias match, and a graph match. This is a CLOSED enum --
// adding a member is a contract change, not an implementation detail, because
// the corroboration band in graphrank counts DISTINCT members (see
// ValidContextFabricSubjectMatchMechanism and graphrank.CorroboratedConfidence).
type ContextFabricSubjectMatchMechanism string

const (
	// ContextFabricMatchExact is an exact canonical identity or label match.
	ContextFabricMatchExact ContextFabricSubjectMatchMechanism = "exact"
	// ContextFabricMatchAlias is a match on an approved alias or previous name.
	ContextFabricMatchAlias ContextFabricSubjectMatchMechanism = "alias"
	// ContextFabricMatchProviderKey is a match on an upstream provider key.
	ContextFabricMatchProviderKey ContextFabricSubjectMatchMechanism = "provider_key"
	// ContextFabricMatchLexical is a full-text/keyword retrieval match.
	ContextFabricMatchLexical ContextFabricSubjectMatchMechanism = "lexical"
	// ContextFabricMatchVector is an embedding-similarity retrieval match.
	ContextFabricMatchVector ContextFabricSubjectMatchMechanism = "vector"
	// ContextFabricMatchTraversalParent is a match reached by walking from a
	// matched observation (document/episode) to its canonical parent entity.
	ContextFabricMatchTraversalParent ContextFabricSubjectMatchMechanism = "traversal_parent"
)

type ContextFabricTemporalAxis string

const (
	ContextFabricTemporalCurrent      ContextFabricTemporalAxis = "current"
	ContextFabricTemporalValidTime    ContextFabricTemporalAxis = "valid_time"
	ContextFabricTemporalObservedTime ContextFabricTemporalAxis = "observed_time"
	ContextFabricTemporalRange        ContextFabricTemporalAxis = "range"
)

type ContextFabricDriverStanding string

const (
	ContextFabricDriverPrincipal    ContextFabricDriverStanding = "principal"
	ContextFabricDriverContributing ContextFabricDriverStanding = "contributing"
	ContextFabricDriverSymptom      ContextFabricDriverStanding = "symptom"
	ContextFabricDriverContext      ContextFabricDriverStanding = "context"
	ContextFabricDriverWithheld     ContextFabricDriverStanding = "withheld"
)

type ContextFabricDerivationMethod string

const (
	ContextFabricDerivationCanonicalStructured     ContextFabricDerivationMethod = "canonical_structured"
	ContextFabricDerivationDeterministicProjection ContextFabricDerivationMethod = "deterministic_projection"
	ContextFabricDerivationGraphAssociated         ContextFabricDerivationMethod = "graph_associated"
	ContextFabricDerivationModelExtracted          ContextFabricDerivationMethod = "model_extracted"
	ContextFabricDerivationRuleInferred            ContextFabricDerivationMethod = "rule_inferred"
)

type ContextFabricEpistemicStatus string

const (
	ContextFabricEpistemicObserved       ContextFabricEpistemicStatus = "observed"
	ContextFabricEpistemicSourceAsserted ContextFabricEpistemicStatus = "source_asserted"
	ContextFabricEpistemicInferred       ContextFabricEpistemicStatus = "inferred"
	ContextFabricEpistemicDisputed       ContextFabricEpistemicStatus = "disputed"
	ContextFabricEpistemicSuperseded     ContextFabricEpistemicStatus = "superseded"
	ContextFabricEpistemicUnknown        ContextFabricEpistemicStatus = "unknown"
)

// ContextFabricRelationshipType is the closed vocabulary for
// ContextFabricRelationshipProjection.Type and
// ContextFabricRelationshipEdge.Type (CHAOS-3779, closing drift item D9 /
// the H4 lesson recorded in the TRD's §19.5.5). Before this type existed,
// both fields were free strings of 1 to 128 bytes: a typo or a novel
// spelling from a projection source would enter the graph silently and
// degrade driver discovery the same way the H4 free-form category strings
// did. Every producer and every reader now shares exactly one closed list.
//
// Membership is additive-only after this point (removing or renaming a
// member is a new major contract version -- see contracts/AGENTS.md and
// §19.5.4's "do not rename an existing projected type without a
// migration"). The six pre-existing types
// (BelongsToRepository/BelongsToPullRequest/CorrelatedWithIncident/
// RelatedTo/DocumentedBy/HasEpisode) keep their exact wire spelling.
//
// RelatesTo and Duplicates are admitted, not mapped onto RelatedTo:
// live ClickHouse work_item_dependencies.relationship_type carries
// 'relates_to' and 'duplicates' today (verified against the running
// database, TRD §19.13 Correction 1), and both are semantically distinct
// from the generic RelatedTo association DOCUMENTED_BY/HAS_EPISODE-style
// producers use, and from each other -- folding 'duplicates' into
// RelatedTo would silently discard the "this is a duplicate" signal a
// reader needs.
type ContextFabricRelationshipType string

const (
	// Pre-existing six -- see this type's doc comment. Spelling is frozen.
	ContextFabricRelationshipBelongsToRepository    ContextFabricRelationshipType = "BELONGS_TO_REPOSITORY"
	ContextFabricRelationshipBelongsToPullRequest   ContextFabricRelationshipType = "BELONGS_TO_PULL_REQUEST"
	ContextFabricRelationshipCorrelatedWithIncident ContextFabricRelationshipType = "CORRELATED_WITH_INCIDENT"
	ContextFabricRelationshipRelatedTo              ContextFabricRelationshipType = "RELATED_TO"
	ContextFabricRelationshipDocumentedBy           ContextFabricRelationshipType = "DOCUMENTED_BY"
	ContextFabricRelationshipHasEpisode             ContextFabricRelationshipType = "HAS_EPISODE"
	// ContextFabricRelationshipBlocks (CHAOS-3779): work_item_dependencies
	// rows with relationship_type='blocks'. Already flowed into the graph
	// before this type closed the vocabulary (TRD §19.13 Correction 1);
	// this makes it a deliberate, tested, and recognized member.
	ContextFabricRelationshipBlocks ContextFabricRelationshipType = "BLOCKS"
	// ContextFabricRelationshipPartOf (CHAOS-3779): work_items.parent_id
	// hierarchy. New producer; see devhealthsource's queryWorkItemHierarchy.
	ContextFabricRelationshipPartOf ContextFabricRelationshipType = "PART_OF"
	// ContextFabricRelationshipRelatesTo and ContextFabricRelationshipDuplicates
	// (CHAOS-3779): the other two live work_item_dependencies.relationship_type
	// values -- see this type's doc comment for why they are admitted
	// rather than mapped onto ContextFabricRelationshipRelatedTo.
	ContextFabricRelationshipRelatesTo  ContextFabricRelationshipType = "RELATES_TO"
	ContextFabricRelationshipDuplicates ContextFabricRelationshipType = "DUPLICATES"
	// ContextFabricRelationshipBelongsToProject and
	// ContextFabricRelationshipOwnedByTeam (CHAOS-3802): the two containment
	// edges the newly-projected team and project subject kinds need --
	// work_item -> project (work_items.project_id) and both work_item ->
	// team (work_item_team_attributions) and project -> team
	// (team_project_ownership). Additive v1 members, following the same
	// precedent as CHAOS-3779's four above.
	//
	// Overloading the existing PART_OF for containment was considered and
	// rejected: the closed vocabulary exists so semantics stay distinct, and
	// graphrank's traversal corroboration must be able to tell a work-item
	// HIERARCHY (PART_OF) from ownership/containment. Like PART_OF, neither
	// member belongs in graphrank's relationMeaningTable -- both are
	// structural facts, not driver signals (see that table's doc comment for
	// why PART_OF, BELONGS_TO_REPOSITORY, CORRELATED_WITH_INCIDENT,
	// RELATED_TO, RELATES_TO and DUPLICATES are all absent from it too).
	ContextFabricRelationshipBelongsToProject ContextFabricRelationshipType = "BELONGS_TO_PROJECT"
	ContextFabricRelationshipOwnedByTeam      ContextFabricRelationshipType = "OWNED_BY_TEAM"
)

type ContextFabricSourceState string

const (
	ContextFabricSourceAvailable     ContextFabricSourceState = "available"
	ContextFabricSourceStale         ContextFabricSourceState = "stale"
	ContextFabricSourceUnavailable   ContextFabricSourceState = "unavailable"
	ContextFabricSourceUnconfigured  ContextFabricSourceState = "unconfigured"
	ContextFabricSourceUnauthorized  ContextFabricSourceState = "unauthorized"
	ContextFabricSourceNoData        ContextFabricSourceState = "no_data"
	ContextFabricSourceTruncated     ContextFabricSourceState = "truncated"
	ContextFabricSourceConflicted    ContextFabricSourceState = "conflicted"
	ContextFabricSourceNotApplicable ContextFabricSourceState = "not_applicable"
	// ContextFabricSourcePruned (CHAOS-3783) means the fact-read planner
	// decided, before any fan-out, that this source could not contribute to
	// this investigation, and therefore never ran it. It is an additive v1
	// enum value -- a new value a producer may emit, not a change in
	// meaning for any existing one -- exactly like
	// ContextFabricSubjectPullRequestReview/ContextFabricSubjectCIRun
	// (CHAOS-3753 codex finding C7); a widening stays v1 per
	// contracts/AGENTS.md.
	//
	// It is deliberately DISTINCT from not_applicable, which consumers
	// already switch on. not_applicable is a statement the provider itself
	// made after running ("I do not apply here"); pruned is a statement the
	// PLANNER made instead of running it ("you could not have applied
	// here"). The two carry different diagnostic meaning: a wrong
	// not_applicable is a provider bug, a wrong pruned is a planner bug,
	// and only the latter is findable by auditing pruning decisions. A
	// consumer that wants the count of skipped-without-running sources --
	// CHAOS-3746's in-flight surface among them -- reads this state rather
	// than parsing the free-text Reason.
	//
	// A pruned source carries NO facts and is NOT a degradation: the
	// absence is fully explained by the accompanying Reason, so it must
	// never set Coverage.Partial or add a degraded reason. See
	// contextfabric.factStateDegrades/stateRejectsFacts and
	// docs/design/context-fabric-fact-planning.md.
	ContextFabricSourcePruned ContextFabricSourceState = "pruned"
)

type ContextFabricConversationRole string

const (
	ContextFabricConversationUser      ContextFabricConversationRole = "user"
	ContextFabricConversationAssistant ContextFabricConversationRole = "assistant"
)

type ContextFabricFactKind string

const (
	ContextFabricFactIdentity                ContextFabricFactKind = "identity"
	ContextFabricFactMembership              ContextFabricFactKind = "membership"
	ContextFabricFactStatus                  ContextFabricFactKind = "status"
	ContextFabricFactActualCompletion        ContextFabricFactKind = "actual_completion"
	ContextFabricFactWork                    ContextFabricFactKind = "work"
	ContextFabricFactBlockers                ContextFabricFactKind = "blockers"
	ContextFabricFactRequiredChildren        ContextFabricFactKind = "required_children"
	ContextFabricFactPullRequests            ContextFabricFactKind = "pull_requests"
	ContextFabricFactReviews                 ContextFabricFactKind = "reviews"
	ContextFabricFactContinuousIntegration   ContextFabricFactKind = "continuous_integration"
	ContextFabricFactDeployments             ContextFabricFactKind = "deployments"
	ContextFabricFactIncidents               ContextFabricFactKind = "incidents"
	ContextFabricFactMetrics                 ContextFabricFactKind = "metrics"
	ContextFabricFactHealth                  ContextFabricFactKind = "health"
	ContextFabricFactWorkload                ContextFabricFactKind = "workload"
	ContextFabricFactInvestment              ContextFabricFactKind = "investment"
	ContextFabricFactReadiness               ContextFabricFactKind = "readiness"
	ContextFabricFactOperationalDeficiencies ContextFabricFactKind = "operational_deficiencies"
	ContextFabricFactSourceHealth            ContextFabricFactKind = "source_health"
	ContextFabricFactEvidence                ContextFabricFactKind = "evidence"
	// ContextFabricFactFlow and ContextFabricFactLandscape (CHAOS-4364) are
	// v1 ADDITIVE fact kinds: delivery-flow/bottleneck signals
	// (work_item_cycle_times, work_item_metrics_daily, repo_metrics_daily's
	// PR pickup/review columns) and IC landscape/area ownership
	// (ic_landscape_rolling_30d ⋈ team_project_ownership), both team- and
	// project-keyed via real table joins/rollups the same way CHAOS-4347
	// widened FactMetrics -- see
	// internal/contextfabric/devhealthfacts/flow.go and landscape.go.
	ContextFabricFactFlow      ContextFabricFactKind = "flow"
	ContextFabricFactLandscape ContextFabricFactKind = "landscape"
)

// contextFabricFactKinds is the closed fact-kind vocabulary in published
// order -- the SINGLE declaration every other check derives from:
// validFactKind, the fact_requirements count bound, the interpretation
// prompt's closed-set sentence, and the schema-parity proof.
//
// It is an ARRAY, not a slice, so len() is a compile-time constant and
// ContextFabricFactRequirementsMaxCount can be derived from it rather than
// transcribed. Adding or removing a kind therefore moves the bound, the
// prompt, and the parity assertions together; the only thing left to update
// by hand is the published schema, and a test fails until that happens.
//
// It is UNEXPORTED, and that is load-bearing (codex round-10 F2). As an
// exported array var its elements were assignable by any importing package:
// validation reads the vocabulary live, while the interpretation prompt
// snapshots it at init, so a single in-process write desynchronized them --
// the validator would accept a kind the prompt never advertised and the
// schema does not publish. Callers get ContextFabricFactKindVocabulary()
// instead, which returns a COPY, so there is no writable path to the
// declaration the validator actually consults.
var contextFabricFactKinds = [...]ContextFabricFactKind{
	ContextFabricFactIdentity,
	ContextFabricFactMembership,
	ContextFabricFactStatus,
	ContextFabricFactActualCompletion,
	ContextFabricFactWork,
	ContextFabricFactBlockers,
	ContextFabricFactRequiredChildren,
	ContextFabricFactPullRequests,
	ContextFabricFactReviews,
	ContextFabricFactContinuousIntegration,
	ContextFabricFactDeployments,
	ContextFabricFactIncidents,
	ContextFabricFactMetrics,
	ContextFabricFactHealth,
	ContextFabricFactWorkload,
	ContextFabricFactInvestment,
	ContextFabricFactReadiness,
	ContextFabricFactOperationalDeficiencies,
	ContextFabricFactSourceHealth,
	ContextFabricFactEvidence,
	ContextFabricFactFlow,
	ContextFabricFactLandscape,
}

// ContextFabricFactKindCount is the size of the closed fact-kind vocabulary,
// as a compile-time constant.
const ContextFabricFactKindCount = len(contextFabricFactKinds)

// ContextFabricFactKindVocabulary returns the closed fact-kind vocabulary in
// published order.
//
// The return type is an ARRAY, so the value is copied to the caller: writing
// to it cannot reach the declaration validFactKind consults. A slice return
// would hand back an alias and reintroduce exactly the mutable-vocabulary
// defect this shape exists to prevent (codex round-10 F2), so it must stay
// an array even though a slice would be marginally more convenient to range
// over.
func ContextFabricFactKindVocabulary() [ContextFabricFactKindCount]ContextFabricFactKind {
	return contextFabricFactKinds
}

// ContextFabricDriverCategory is a closed vocabulary for
// ContextFabricDriverJudgment.Category / ContextFabricFinding.Kind values
// that assert something canonical-fact-shaped (as opposed to a
// graph-associated or purely narrative category, e.g. "relationship" or
// "narrative", which are additive free-text values outside this table by
// design). ContextFabricDriverCategoryFactKind is the ONLY mechanism that
// decides whether a driver/finding must cite a ClaimedFact -- it is an
// exact-match lookup against this closed map, never a substring/keyword
// match over Category, Title, or Summary text (CHAOS-3755 must-do: value
// closure has to be enum-driven, not string-matched, or wording alone could
// dodge it in either direction).
type ContextFabricDriverCategory string

const (
	ContextFabricDriverCategoryStatus       ContextFabricDriverCategory = "status"
	ContextFabricDriverCategoryCompletion   ContextFabricDriverCategory = "actual_completion"
	ContextFabricDriverCategoryWork         ContextFabricDriverCategory = "work"
	ContextFabricDriverCategoryBlockers     ContextFabricDriverCategory = "blockers"
	ContextFabricDriverCategoryReviews      ContextFabricDriverCategory = "reviews"
	ContextFabricDriverCategoryCI           ContextFabricDriverCategory = "continuous_integration"
	ContextFabricDriverCategoryDeployments  ContextFabricDriverCategory = "deployments"
	ContextFabricDriverCategoryIncidents    ContextFabricDriverCategory = "incidents"
	ContextFabricDriverCategoryHealth       ContextFabricDriverCategory = "health"
	ContextFabricDriverCategoryWorkload     ContextFabricDriverCategory = "workload"
	ContextFabricDriverCategoryInvestment   ContextFabricDriverCategory = "investment"
	ContextFabricDriverCategoryReadiness    ContextFabricDriverCategory = "readiness"
	ContextFabricDriverCategoryDeficiency   ContextFabricDriverCategory = "operational_deficiency"
	ContextFabricDriverCategorySourceHealth ContextFabricDriverCategory = "source_health"
	// ContextFabricDriverCategoryRelationship and
	// ContextFabricDriverCategoryNarrative are graph-associated /
	// inference-only categories. They are intentionally absent from
	// contextFabricDriverCategoryFactKind: no canonical fact backs a
	// relationship-derived or purely narrative judgment, so no
	// ClaimedFactID requirement applies to them.
	ContextFabricDriverCategoryRelationship ContextFabricDriverCategory = "relationship"
	ContextFabricDriverCategoryNarrative    ContextFabricDriverCategory = "narrative"
)

// contextFabricDriverCategories is the closed driver-category vocabulary in
// published order -- the SINGLE declaration validDriverCategory derives from,
// and now also the vocabulary ContextFabricFinding.Kind is checked against.
//
// Finding.Kind is the category-equivalent field for findings and has always
// been governed by the same closed set in the synthesis prompt and in
// ContextFabricDriverCategoryRequiresClaimedFact. It was simply never
// ENFORCED (codex round-12): Finding.validate checked only non-emptiness and
// length, and the published schema left the field an unrestricted string
// while DriverJudgment.category carried this exact enum. A model could emit
// kind "source_disagreement" with valid evidence and produce a canonical
// result that validated -- the prompt advertising a closed set the contract
// did not keep.
//
// Unexported behind an accessor for the same reason as the fact-kind
// vocabulary: an exported array var's elements are assignable, and a caller
// mutating the vocabulary would desynchronize validation from the schema and
// the prompt.
var contextFabricDriverCategories = [...]ContextFabricDriverCategory{
	ContextFabricDriverCategoryStatus,
	ContextFabricDriverCategoryCompletion,
	ContextFabricDriverCategoryWork,
	ContextFabricDriverCategoryBlockers,
	ContextFabricDriverCategoryReviews,
	ContextFabricDriverCategoryCI,
	ContextFabricDriverCategoryDeployments,
	ContextFabricDriverCategoryIncidents,
	ContextFabricDriverCategoryHealth,
	ContextFabricDriverCategoryWorkload,
	ContextFabricDriverCategoryInvestment,
	ContextFabricDriverCategoryReadiness,
	ContextFabricDriverCategoryDeficiency,
	ContextFabricDriverCategorySourceHealth,
	ContextFabricDriverCategoryRelationship,
	ContextFabricDriverCategoryNarrative,
}

// ContextFabricDriverCategoryCount is the size of the closed driver-category
// vocabulary, as a compile-time constant.
const ContextFabricDriverCategoryCount = len(contextFabricDriverCategories)

// ContextFabricDriverCategoryVocabulary returns the closed driver-category
// vocabulary in published order. The return type is an ARRAY, so the value is
// copied to the caller -- see ContextFabricFactKindVocabulary for why that
// matters.
func ContextFabricDriverCategoryVocabulary() [ContextFabricDriverCategoryCount]ContextFabricDriverCategory {
	return contextFabricDriverCategories
}

// contextFabricDriverCategoryFactKind is the closed Category->FactKind
// table. See ContextFabricDriverCategoryRequiresClaimedFact.
var contextFabricDriverCategoryFactKind = map[ContextFabricDriverCategory]ContextFabricFactKind{
	ContextFabricDriverCategoryStatus:       ContextFabricFactStatus,
	ContextFabricDriverCategoryCompletion:   ContextFabricFactActualCompletion,
	ContextFabricDriverCategoryWork:         ContextFabricFactWork,
	ContextFabricDriverCategoryBlockers:     ContextFabricFactBlockers,
	ContextFabricDriverCategoryReviews:      ContextFabricFactReviews,
	ContextFabricDriverCategoryCI:           ContextFabricFactContinuousIntegration,
	ContextFabricDriverCategoryDeployments:  ContextFabricFactDeployments,
	ContextFabricDriverCategoryIncidents:    ContextFabricFactIncidents,
	ContextFabricDriverCategoryHealth:       ContextFabricFactHealth,
	ContextFabricDriverCategoryWorkload:     ContextFabricFactWorkload,
	ContextFabricDriverCategoryInvestment:   ContextFabricFactInvestment,
	ContextFabricDriverCategoryReadiness:    ContextFabricFactReadiness,
	ContextFabricDriverCategoryDeficiency:   ContextFabricFactOperationalDeficiencies,
	ContextFabricDriverCategorySourceHealth: ContextFabricFactSourceHealth,
}

// ContextFabricDriverCategoryRequiresClaimedFact reports whether category
// asserts a canonical-fact-shaped judgment and, if so, which FactKind it is
// shaped after. A driver or finding in a category this returns true for
// must cite at least one ClaimedFactID resolving to a ContextFabricClaimedFact
// of the matching Kind (see ContextFabricDriverJudgment.Validate /
// ContextFabricFinding.Validate) -- ordinary EvidenceRefIDs/PathIDs closure
// is not sufficient for these categories, because it proves something was
// cited, not that the cited value agrees with the canonical fact.
func ContextFabricDriverCategoryRequiresClaimedFact(category ContextFabricDriverCategory) (ContextFabricFactKind, bool) {
	kind, ok := contextFabricDriverCategoryFactKind[category]
	return kind, ok
}

type ContextFabricInvestigationRequest struct {
	SchemaVersion        string                             `json:"schema_version"`
	RequestID            string                             `json:"request_id"`
	Question             string                             `json:"question"`
	Conversation         []ContextFabricConversationTurn    `json:"conversation,omitempty"`
	PriorSubjectReceipts []ContextFabricBoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
	// PriorWindowReceipts (CHAOS-3900 W1) names winr_-prefixed receipts
	// from an earlier stored result's own ContextFabricWindowClarification
	// -- a NEW, PARALLEL field to PriorSubjectReceipts rather than an
	// overload of it: a window receipt's match target (a stored
	// WindowOption, not a SubjectCandidate) and its effect (confirms an
	// evidence window, never binds a subject) are both different, so
	// conflating the two fields would let a namespace mismatch silently
	// fall through to the wrong matcher instead of failing loudly at
	// validation (see Validate's winr_-prefix check).
	PriorWindowReceipts []ContextFabricBoundSubjectReceipt `json:"prior_window_receipts,omitempty"`
	// PriorKindReceipts, PriorAnchorReceipts, and PriorHandleReceipts
	// (CHAOS-3900 P1, pivot-intent design brief §2.1) name kindr_/ancr_/
	// handr_-prefixed receipts from an earlier stored result's own
	// StructureNeeds offer sets -- three MORE new, PARALLEL fields
	// following PriorWindowReceipts' own precedent exactly: each match
	// target and effect differs (kind narrows the census scope, anchor
	// binds the 3896 discriminator, handle binds a keyed source row), so
	// none of the four (nor PriorSubjectReceipts) may ever accept another's
	// namespace -- see Validate's closed structure-receipt-prefix check.
	PriorKindReceipts   []ContextFabricBoundSubjectReceipt `json:"prior_kind_receipts,omitempty"`
	PriorAnchorReceipts []ContextFabricBoundSubjectReceipt `json:"prior_anchor_receipts,omitempty"`
	PriorHandleReceipts []ContextFabricBoundSubjectReceipt `json:"prior_handle_receipts,omitempty"`
	// PriorCandidateReceipts (CHAOS-4012) names candr_-prefixed receipts
	// from an earlier stored result's own StructureNeeds.CandidateOptions --
	// a FIFTH, PARALLEL field following the same precedent: a candidate
	// receipt's match target (a stored ContextFabricCandidateOption) and
	// effect (binds a specific ranked-candidate subject, never narrows a
	// census scope) differ from all four other namespaces, so it may never
	// accept another's prefix either -- see Validate's closed
	// structure-receipt-prefix check.
	PriorCandidateReceipts []ContextFabricBoundSubjectReceipt `json:"prior_candidate_receipts,omitempty"`
	// ExpectedKinds and SubjectHandles (CHAOS-3972 P3, pivot-intent design
	// brief §2.3/§2.0) are the caller's own EXPLICIT structure fields --
	// the wire concept structure.go's own P1.B scope note deferred to this
	// ticket. Per the DP12(b) uniform surface split, a value here NEVER
	// mints question_stated by itself: on the MCP surface it enters at
	// inferred_default/explicit_unattributed (drives census-narrowing and
	// offer-shaping only; a decisive outcome still requires the matching
	// kindr_/handr_ receipt, or -- for kind -- the §2.0 kind-insensitivity
	// proof); on every other surface (panel/web_assertion) an explicit
	// value keeps 3900 v5.2's ordinary question_stated rule, mirroring
	// EvidenceWindow's own per-surface split exactly (windowExplicitProvenance,
	// window.go). See canonicalizeStructure (structure.go) for the
	// resolution mechanics and the explicit-vs-receipt conflict rule.
	ExpectedKinds []ContextFabricSubjectKind `json:"expected_kinds,omitempty"`
	// SubjectHandles names grammar-typed handle values the caller already
	// knows (design brief §2.3: "a grammar-VALID handle that keys a unique
	// source row is still redeemed via its offered receipt, one turn").
	// PatternID must name one of the closed handle-grammar registry
	// entries this deployment discloses via StructureNeeds.AcceptedGrammars
	// -- never free text or a caller-supplied regex.
	SubjectHandles []ContextFabricRequestedHandle    `json:"subject_handles,omitempty"`
	RequestedScope ContextFabricRequestedScope       `json:"requested_scope,omitempty"`
	TimeContext    ContextFabricTimeContext          `json:"time_context"`
	Options        ContextFabricInvestigationOptions `json:"options"`
	Consumer       ContextFabricConsumerInfo         `json:"consumer"`
}

// ContextFabricExpectedKindsMaxCount bounds ExpectedKinds -- the closed
// ContextFabricSubjectKind vocabulary's own size (15 members), so this
// bound can never reject a caller naming every kind at once while still
// rejecting a malformed, unbounded list.
const ContextFabricExpectedKindsMaxCount = 15

// ContextFabricRequestedHandle is one caller-supplied explicit subject_handle
// value (CHAOS-3972 P3, design brief §2.3): a typed (kind, pattern_id, value)
// triple the engine grammar-validates before it may become a receipt-bound
// HandleOption offer. Mirrors ContextFabricHandleOption's own Kind/PatternID/
// Value fields exactly -- deliberately NOT SourceColumn or the offer
// identity fields, which are things the SERVER computes, never something a
// caller sends (the same asymmetry ContextFabricRequestedEvidenceWindow
// draws against ContextFabricEffectiveEvidenceWindow).
type ContextFabricRequestedHandle struct {
	Kind      ContextFabricSubjectKind `json:"kind"`
	PatternID string                   `json:"pattern_id"`
	Value     string                   `json:"value"`
}

type ContextFabricConversationTurn struct {
	TurnID    string                        `json:"turn_id"`
	Role      ContextFabricConversationRole `json:"role"`
	Content   string                        `json:"content"`
	CreatedAt time.Time                     `json:"created_at"`
}

type ContextFabricBoundSubjectReceipt struct {
	ResultID  string `json:"result_id"`
	ReceiptID string `json:"receipt_id"`
}

// ContextFabricPriorSubjectReceiptDisposition is the closed vocabulary for
// what happened to one PriorSubjectReceipts entry (CHAOS-3478/CHAOS-3813:
// "a veto the caller cannot see is the silent drop reborn" -- the SAME
// design brief §2.1 rule ContextFabricStructureDisposition already applies
// to structure receipts, extended to the plural, best-effort prior-subject
// list). Unlike structure receipts, a skipped prior-subject receipt does
// NOT veto the investigation (resolvePriorSubjectHints' own doc comment:
// "skipped, never trusted outright, and never treated as an error" is
// deliberate conversational degradation for a plural hint list, not an
// oversight) -- this vocabulary discloses that outcome instead of hiding
// it, mirroring the reasons EngineTelemetry.RecordPriorSubjectReceiptSkipReason
// already reports server-side.
type ContextFabricPriorSubjectReceiptDisposition string

const (
	ContextFabricPriorSubjectReceiptApplied                ContextFabricPriorSubjectReceiptDisposition = "applied"
	ContextFabricPriorSubjectReceiptSkippedUnloadable      ContextFabricPriorSubjectReceiptDisposition = "skipped_unloadable"
	ContextFabricPriorSubjectReceiptSkippedNoMatch         ContextFabricPriorSubjectReceiptDisposition = "skipped_no_match"
	ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch ContextFabricPriorSubjectReceiptDisposition = "skipped_stale_graph_epoch"
	ContextFabricPriorSubjectReceiptSkippedFailedReauth    ContextFabricPriorSubjectReceiptDisposition = "skipped_failed_reauth"
)

func ValidContextFabricPriorSubjectReceiptDisposition(value ContextFabricPriorSubjectReceiptDisposition) bool {
	switch value {
	case ContextFabricPriorSubjectReceiptApplied, ContextFabricPriorSubjectReceiptSkippedUnloadable,
		ContextFabricPriorSubjectReceiptSkippedNoMatch, ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch,
		ContextFabricPriorSubjectReceiptSkippedFailedReauth:
		return true
	default:
		return false
	}
}

// ContextFabricPriorSubjectReceiptEntry is the wire-visible disposition for
// ONE PriorSubjectReceipts entry the caller sent -- one entry per carried
// receipt, INCLUDING skipped ones (CHAOS-3478/CHAOS-3813), same
// "echo every carried item" rule ContextFabricConfirmedStructureEntry
// already follows for structure receipts. PriorResultID/ReceiptID echo the
// caller's own ContextFabricBoundSubjectReceipt so a reader can match this
// entry back to the request without re-sending it.
type ContextFabricPriorSubjectReceiptEntry struct {
	PriorResultID string                                      `json:"prior_result_id"`
	ReceiptID     string                                      `json:"receipt_id"`
	Disposition   ContextFabricPriorSubjectReceiptDisposition `json:"disposition"`
}

type ContextFabricRequestedScope struct {
	RepositorySlugs []string                   `json:"repository_slugs,omitempty"`
	ProjectIDs      []string                   `json:"project_ids,omitempty"`
	TeamIDs         []string                   `json:"team_ids,omitempty"`
	SubjectHints    []ContextFabricSubjectHint `json:"subject_hints,omitempty"`
}

type ContextFabricSubjectHint struct {
	Kind   ContextFabricSubjectKind `json:"kind"`
	ID     string                   `json:"id,omitempty"`
	Label  string                   `json:"label,omitempty"`
	Source string                   `json:"source"`
}

type ContextFabricTimeContext struct {
	Axis  ContextFabricTemporalAxis `json:"axis"`
	AsOf  *time.Time                `json:"as_of,omitempty"`
	Start *time.Time                `json:"start,omitempty"`
	End   *time.Time                `json:"end,omitempty"`
	// EvidenceWindow (CHAOS-3900 W1) is the caller's requested evidence
	// window -- legal ONLY on the current axis (see Validate); a window on
	// any other axis is a named invariant violation, never a silent drop,
	// because a non-current axis's own Start/End/AsOf already IS the
	// window that axis answers for.
	EvidenceWindow *ContextFabricRequestedEvidenceWindow `json:"evidence_window,omitempty"`
}

// ContextFabricTemporalGrain names the coarsest source grain that
// contributed to a historical answer (CHAOS-3781, AC-3781-2). A closed
// vocabulary, not a free string: drift item D9 records free-string
// vocabularies in this contract as a governance gap, and a caller must be
// able to branch on this value rather than parse prose.
type ContextFabricTemporalGrain string

const (
	// ContextFabricGrainInstant: every contributing source answered at the
	// exact requested instant. Only reachable when every source that
	// contributed derives its answer from an immutable event timestamp.
	ContextFabricGrainInstant ContextFabricTemporalGrain = "instant"
	// ContextFabricGrainDay: at least one contributing source answers at
	// day grain (the daily rollup tables), so the answer speaks for the
	// most recent day at or before the requested instant, NOT for the
	// instant itself. A caller must not read instant precision from it.
	ContextFabricGrainDay ContextFabricTemporalGrain = "day"
	// ContextFabricGrainNone: no source could speak for the requested
	// time at all, so the answer carries no fact coverage on this axis.
	// This is the honest steady state for the observed_time axis, where
	// no canonical source retains observation history -- see
	// ContextFabricTemporalLabel's doc comment.
	ContextFabricGrainNone ContextFabricTemporalGrain = "none"
)

// ContextFabricTemporalLabel states the time an answer actually speaks
// for, which is not always the time that was requested (CHAOS-3781, TRD
// §19.8, AC-3781-2).
//
// Present only on a non-current time axis; nil means the answer is about
// current state, which is what every result meant before CHAOS-3781.
// Interpretation.TimeContext already round-trips what the caller ASKED
// for, so this type exists for what that field cannot express: the
// EFFECTIVE time after each source's own grain is applied, and whether
// every source could speak for it at all.
//
// Why Requested and Effective are both full ContextFabricTimeContext
// values rather than loose timestamp fields: the axis-shape rules
// (as_of for a point-in-time axis, ordered start/end for a range, and
// neither for current) are already defined and validated exactly once, on
// that type. Restating them here as parallel pointers would let the two
// definitions drift.
//
// Effective is always NARROWER than or equal to Requested, never wider:
// a point-in-time Effective.AsOf is at or before Requested.AsOf, and a
// range's effective window sits inside the requested one. An answer may
// only ever speak for less time than was asked about, never more --
// widening it would be the false historical answer the H6 refusal existed
// to prevent.
type ContextFabricTemporalLabel struct {
	// Requested is the time context the investigation ran on, after
	// interpretation. It equals Interpretation.TimeContext.
	Requested ContextFabricTimeContext `json:"requested"`
	// Effective is the time this answer speaks for once every
	// contributing source's grain is applied. Same axis as Requested.
	Effective ContextFabricTimeContext `json:"effective"`
	// Grain is the coarsest contributing source grain. See
	// ContextFabricTemporalGrain.
	Grain ContextFabricTemporalGrain `json:"grain"`
	// CoverageComplete is true only when every source consulted could
	// speak for the requested time. False means at least one source
	// reported that it cannot answer for that time, and its limitation is
	// recorded in Coverage.Sources and Limitations (AC-3781-5). It is
	// deliberately NOT a restatement of Coverage.Partial: a source can be
	// partial for reasons that have nothing to do with time.
	CoverageComplete bool `json:"coverage_complete"`
}

// ContextFabricSourceObservationReasonMaxLength and
// ContextFabricCoverageDegradedReasonMaxLength are the contract's own
// bounds on the two coverage explanation strings.
//
// Named because internal/contextfabric clamps every reason it emits to
// them before composing a result (fact_registry.go's appendFactCoverage),
// and until CHAOS-3746 that clamp was a hand-copied 2000 whose comment
// said it "mirrors" this bound. A mirror is not a derivation: widening
// the contract would leave the clamp shortening explanations for no
// reason, and narrowing it would let the clamp emit a result the
// validator then rejects in full -- losing the answer, not just the
// explanation.
//
// They are equal today and still separate constants, because they bound
// different strings: a degraded entry is BUILT from a reason and is
// strictly longer (it carries a "<kind>: " prefix), so sharing one
// constant would be a coincidence dressed as a relation.
const (
	ContextFabricSourceObservationReasonMaxLength = 2000
	ContextFabricCoverageDegradedReasonMaxLength  = 2000
)

// ContextFabricSerializedBytesMin and ContextFabricSerializedBytesMax bound
// ContextFabricInvestigationOptions.MaxSerializedBytes: the largest single
// investigation response this service will serve.
//
// Named rather than inline (CHAOS-3795) because the number is load-bearing
// somewhere else entirely. The MCP sidecar caps every hosted response body
// it will read at its own ceiling, and the claim that the ceiling can never
// truncate a legitimate answer is an arithmetic relation between the two
// values -- see TestSidecarCeilingClearsTheServingBudget in
// internal/sidecar. A relation between two literals is not checkable; a
// relation between two constants is.
//
// This is the SERVING budget for one response, deliberately not the
// contract's aggregate maximum across a full expansion (hundreds of MiB).
// The sidecar reads one response at a time, so one response is the quantity
// its ceiling has to clear.
const (
	ContextFabricSerializedBytesMin = 8192
	ContextFabricSerializedBytesMax = 1 << 20
)

type ContextFabricInvestigationOptions struct {
	MaxSubjectCandidates int  `json:"max_subject_candidates"`
	MaxCohortMembers     int  `json:"max_cohort_members"`
	MaxRelationshipPaths int  `json:"max_relationship_paths"`
	MaxDrivers           int  `json:"max_drivers"`
	MaxEvidenceRefs      int  `json:"max_evidence_refs"`
	MaxSerializedBytes   int  `json:"max_serialized_bytes"`
	AllowClarification   bool `json:"allow_clarification"`
	IncludeDebug         bool `json:"include_debug"`
	// WindowConfirmationMode (CHAOS-3900 W2, design brief §4) selects
	// whether an INFERRED (non-confirmed) evidence window additionally
	// nudges the caller via a disclosed Warnings sentence, beside the
	// WindowClarification data every inferred window already carries
	// regardless of mode. Empty means the DW3-ruled default: headless, no
	// additional Warnings sentence. CHAOS-4040 (sol-max ruling
	// 2026-08-21): the mode itself never determines whether the request
	// blocks -- every inferred window is now gated to a
	// confirmation-required terminal regardless of this field; see
	// ContextFabricWindowConfirmationMode's own doc comment.
	WindowConfirmationMode ContextFabricWindowConfirmationMode `json:"window_confirmation_mode,omitempty"`
}

type ContextFabricConsumerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Surface string `json:"surface"`
}

type ContextFabricSubjectRef struct {
	Kind        ContextFabricSubjectKind `json:"kind"`
	CanonicalID string                   `json:"canonical_id"`
	Label       string                   `json:"label"`
}

type ContextFabricSubjectCandidate struct {
	ReceiptID      string                       `json:"receipt_id"`
	Subject        ContextFabricSubjectRef      `json:"subject"`
	State          ContextFabricResolutionState `json:"state"`
	MatchedTerms   []string                     `json:"matched_terms,omitempty"`
	MatchReasons   []string                     `json:"match_reasons"`
	Confidence     float64                      `json:"confidence"`
	EvidenceRefIDs []string                     `json:"evidence_ref_ids,omitempty"`
	// MatchMechanisms records WHICH retrieval mechanisms proposed this
	// candidate (CHAOS-3778 / AC-3778-6). Additive and optional in v1: every
	// InvestigationResult persisted before CHAOS-3778 was serialized without
	// it, and those snapshots must still validate on replay, so this field is
	// never required and an empty value is never an error. A reader that finds
	// it empty learns only "this result predates mechanism recording", never
	// "no mechanism matched".
	MatchMechanisms []ContextFabricSubjectMatchMechanism `json:"match_mechanisms,omitempty"`
}

type ContextFabricSubjectResolution struct {
	Candidates          []ContextFabricSubjectCandidate `json:"candidates"`
	Committed           []ContextFabricSubjectRef       `json:"committed"`
	ClarificationPrompt string                          `json:"clarification_prompt,omitempty"`
	// PriorSubjectReceiptDispositions (CHAOS-3478/CHAOS-3813) is the
	// wire-visible fate of every PriorSubjectReceipts entry THIS request
	// carried -- additive-optional: nil means "the request carried none, or
	// this call returned before receipt resolution could run" (an earlier
	// window/structure veto short-circuits before resolvePriorSubjectHints
	// -- see that function's own doc comment), never "every receipt
	// applied". Once present it carries exactly one entry per carried
	// receipt, in the caller's own order, including skipped ones -- closing
	// the gap where a well-formed but unresolvable receipt was silently
	// dropped with no wire signal (CHAOS-3813).
	PriorSubjectReceiptDispositions []ContextFabricPriorSubjectReceiptEntry `json:"prior_subject_receipt_dispositions,omitempty"`
	// RetrievalDegraded reports that a retrieval MECHANISM was unavailable
	// for this resolution -- CHAOS-3778's vector step timed out, errored, or
	// was fenced off -- so the candidate set may be narrower than a healthy
	// run would produce (codex round-1 F4).
	//
	// It is a single boolean, not a taxonomy, on purpose: every cause has the
	// same consequence for a reader ("retrieval saw less than it should"),
	// and naming causes here would leak retrieval internals into an
	// answer-facing contract. The operator-facing detail lives in telemetry.
	//
	// REQUEST-SCOPED. It describes THIS resolution, not the organization's
	// recent health, which is what makes it usable as an input to the
	// engine's own coverage and limitation handling.
	//
	// IT REPORTS MECHANISM AVAILABILITY, NOT CORPUS COMPLETENESS (codex
	// round-2 R2-4). A node that carries no vector -- because a projection
	// batch's embedding step failed and cleared it, or because projection has
	// not reached it yet -- is a DATA GAP, and a later query that runs the
	// vector mechanism successfully over the rest of the corpus reports no
	// degradation. That is intended: such a node is still fully reachable
	// lexically, since both retrieval paths index the same text, so it is
	// findable one way instead of two rather than lost. Degradation means the
	// query could not run one of its retrieval strategies AT ALL, so every
	// subject was searched one way short. Reporting data gaps here would make
	// the field fire on nearly every answer during any re-embedding backlog,
	// and a partial-coverage signal that is always on carries no information.
	//
	// Additive-optional in v1: absent means "not reported", never "healthy".
	// Every result persisted before CHAOS-3778 lacks it and must still
	// validate on replay.
	RetrievalDegraded bool `json:"retrieval_degraded,omitempty"`
	// GraphNotProjected reports that this resolution is empty because the
	// organization's graph has never been projected at all (CHAOS-4077,
	// contextfabric.ErrGraphNotProjected), never because a search ran and
	// genuinely found nothing. Distinct from an ordinary empty pool for
	// the same reason RetrievalDegraded is distinct from an ordinary
	// narrow one: the two situations look identical in Candidates/
	// Committed (both empty) but call for a different operator response
	// -- run the projector for this org, not "investigate why the search
	// missed a real subject".
	//
	// Additive-optional in v1, same convention as RetrievalDegraded:
	// absent means "not reported", never "the graph is projected".
	GraphNotProjected bool `json:"graph_not_projected,omitempty"`
	// CommitDecisionDigests (CHAOS-4087) is a small, closed-vocabulary
	// summary of WHICH MECHANISM committed each subject in Committed --
	// persisted so a wrong commit discovered later from a STORED result
	// has a durable link to the decision that produced it, closing the gap
	// a live-sink-only trace stream and a tryReuse hit (which serves an
	// old result without re-emitting any correlating trace) both leave
	// open. See ContextFabricCommitDecisionDigest's own doc comment for
	// the exact fields and their fail-closed zero value.
	//
	// One entry PER SUBJECT in Committed whenever this resolution runs
	// through a commit path that records at all -- len(CommitDecisionDigests)
	// == len(Committed), or the field is entirely absent (a resolution
	// engine that predates CHAOS-4087, or a test double that returns a
	// bare SubjectResolution). Each entry self-identifies via its own
	// Subject field, so a consumer never has to trust positional
	// correspondence with Committed. A committed subject with no digest
	// actually recorded for it STILL gets an entry here, with
	// CommitGate=="" -- the fail-closed "nothing recorded" reading, never
	// silently omitted.
	//
	// Additive-optional in v1, same convention as GraphNotProjected/
	// RetrievalDegraded above: absent means "not reported", never "no
	// digest exists".
	CommitDecisionDigests []ContextFabricCommitDecisionDigest `json:"commit_decision_digests,omitempty"`
}

// ContextFabricCommitDecisionDigest (CHAOS-4087) is the wire-safe,
// persisted counterpart to contextfabric.CommitBasis -- see that
// (internal-only, never-wire) type's own doc comment for the exact
// boundary this type sits on the allowed side of: IdentityProven is a
// DERIVED boolean projection of CommitBasis.IdentityProven(), never the
// raw internal enum itself.
type ContextFabricCommitDecisionDigest struct {
	// Subject is which committed subject (kind + canonical id) this digest
	// describes -- ContextFabricSubjectResolution.CommitDecisionDigests'
	// own doc comment for why this makes each entry self-identifying
	// rather than positionally correlated with Committed.
	Subject ContextFabricSubjectRef `json:"subject"`
	// CommitGate is the closed-vocabulary name of the commit path that
	// fired for this subject -- the SAME vocabulary
	// graphrank.ResolutionTraceEvent.CommitGate already uses for the
	// identical concept, live-only ("caller_hint_short_circuit" |
	// "pre_committed_exact_hint" | "exact_index" | "identity_fast_path" |
	// "lone_floor" | "top_of_two" | "vector_margin_rescue" |
	// "evidence_census").
	//
	// Empty is the FAIL-CLOSED zero value: it means NOTHING recorded a
	// digest for this subject, never "recorded and clean." A consumer
	// must check CommitGate != "" before trusting the three fields below
	// at all -- an unrecorded digest and a recorded-but-clean one are
	// deliberately indistinguishable in every OTHER field, which is
	// exactly the point: there is nothing safe to infer from an
	// unrecorded digest.
	CommitGate string `json:"commit_gate,omitempty"`
	// IdentityProven reports whether this subject's commit stood on a
	// PROVEN identity (a caller-supplied canonical id, or an
	// authoritative keyed identity under a completely-enumerated identity
	// universe) rather than a score comparison among candidates. Only
	// meaningful when CommitGate != "".
	IdentityProven bool `json:"identity_proven,omitempty"`
	// SearchTruncated/AliasLookupComplete mirror the SAME resolution-wide
	// signals ResolutionTraceEvent's decision-stage event already carries,
	// live-only, persisted here so a stored result answers "was the
	// search truncated / was alias lookup complete" without a live trace
	// consumer having been attached at request time. Only meaningful when
	// CommitGate != "".
	SearchTruncated     bool `json:"search_truncated,omitempty"`
	AliasLookupComplete bool `json:"alias_lookup_complete,omitempty"`
}

type ContextFabricCohort struct {
	Kind       ContextFabricSubjectKind       `json:"kind"`
	Members    []ContextFabricCohortMember    `json:"members"`
	Exclusions []ContextFabricCohortExclusion `json:"exclusions,omitempty"`
	Rationale  string                         `json:"rationale"`
	Complete   bool                           `json:"complete"`
	Truncated  bool                           `json:"truncated"`
}

type ContextFabricCohortMember struct {
	Subject          ContextFabricSubjectRef `json:"subject"`
	Rank             int                     `json:"rank"`
	InclusionReasons []string                `json:"inclusion_reasons"`
	EvidenceRefIDs   []string                `json:"evidence_ref_ids,omitempty"`
}

type ContextFabricCohortExclusion struct {
	Subject ContextFabricSubjectRef `json:"subject"`
	Reason  string                  `json:"reason"`
}

type ContextFabricRelationshipPath struct {
	PathID         string                          `json:"path_id"`
	Nodes          []ContextFabricSubjectRef       `json:"nodes"`
	Edges          []ContextFabricRelationshipEdge `json:"edges"`
	WhyRelevant    string                          `json:"why_relevant"`
	EvidenceRefIDs []string                        `json:"evidence_ref_ids"`
	Truncated      bool                            `json:"truncated"`
}

type ContextFabricRelationshipEdge struct {
	Type            ContextFabricRelationshipType `json:"type"`
	From            ContextFabricSubjectRef       `json:"from"`
	To              ContextFabricSubjectRef       `json:"to"`
	Derivation      ContextFabricDerivationMethod `json:"derivation"`
	EpistemicStatus ContextFabricEpistemicStatus  `json:"epistemic_status"`
	ObservedAt      *time.Time                    `json:"observed_at,omitempty"`
	ValidFrom       *time.Time                    `json:"valid_from,omitempty"`
	ValidTo         *time.Time                    `json:"valid_to,omitempty"`
	EvidenceRefIDs  []string                      `json:"evidence_ref_ids"`
}

type ContextFabricDriverJudgment struct {
	DriverID         string                      `json:"driver_id"`
	Standing         ContextFabricDriverStanding `json:"standing"`
	Category         string                      `json:"category"`
	Title            string                      `json:"title"`
	Summary          string                      `json:"summary"`
	AffectedSubjects []ContextFabricSubjectRef   `json:"affected_subjects"`
	PathIDs          []string                    `json:"path_ids,omitempty"`
	EvidenceRefIDs   []string                    `json:"evidence_ref_ids"`
	// ClaimedFactIDs references ContextFabricInvestigationResult.ClaimedFacts
	// entries this driver's judgment rests on -- the canonical-observation
	// leg of the graph-association/source-assertion/canonical-observation/
	// inference distinction. Required (non-empty) whenever Category matches
	// ContextFabricDriverCategoryRequiresClaimedFact; optional otherwise.
	ClaimedFactIDs  []string                      `json:"claimed_fact_ids,omitempty"`
	Derivation      ContextFabricDerivationMethod `json:"derivation"`
	EpistemicStatus ContextFabricEpistemicStatus  `json:"epistemic_status"`
	Confidence      float64                       `json:"confidence"`
	Qualification   string                        `json:"qualification,omitempty"`
	Current         bool                          `json:"current"`
}

type ContextFabricFinding struct {
	FindingID      string                    `json:"finding_id"`
	Kind           string                    `json:"kind"`
	Summary        string                    `json:"summary"`
	Subjects       []ContextFabricSubjectRef `json:"subjects,omitempty"`
	EvidenceRefIDs []string                  `json:"evidence_ref_ids"`
	// ClaimedFactIDs mirrors ContextFabricDriverJudgment.ClaimedFactIDs --
	// see that field's doc comment.
	ClaimedFactIDs []string `json:"claimed_fact_ids,omitempty"`
}

// ContextFabricClaimedFact is a synthesis-time restatement of a single
// canonical fact field the answer relies on. It exists so evidence closure
// can be checked at VALUE level by exact code (deep equality), not by
// trusting free-text prose: ContextFabricInvestigationResult.Validate does
// not itself compare ClaimedFacts against canonical fact values (Validate
// only checks wire-shape bounds and internal referential integrity -- it has
// no canonical fact bundle to compare against), but
// SynthesisDraft.ValidateAgainst in internal/contextfabric does, before a
// result is ever built. See docs/design/context-fabric-result-semantics.md
// for the full four-way evidence distinction this type is part of.
type ContextFabricClaimedFact struct {
	ClaimID string                   `json:"claim_id"`
	Kind    ContextFabricFactKind    `json:"kind"`
	Subject ContextFabricSubjectRef  `json:"subject"`
	Field   string                   `json:"field"`
	Value   ContextFabricScalarValue `json:"value"`
	// Rows is an OPTIONAL, additive renderable table (CHAOS-4347): a fact
	// whose evidence is genuinely a set of rows -- e.g. a project's
	// per-team metrics rollup, where summing counts across teams is sound
	// but averaging their rates is not -- carries them here instead of
	// forcing one lossy scalar into Value. Field/Value stay required and
	// carry the claim's primary scalar either way, so every pre-4347
	// consumer that only reads Field/Value is unaffected.
	//
	// This intentionally does NOT reuse ContextFabricScalarValue as the
	// carrier: that type is also the projection-write contract's only
	// admitted property value (ContextFabricEntityProjection.Properties),
	// which documents "nested objects and arrays remain disallowed" as a
	// deliberate invariant for that surface. ContextFabricClaimedFactRow
	// is a new, answer-surface-only shape so widening a claim can never
	// widen what a projection batch is allowed to write.
	Rows []ContextFabricClaimedFactRow `json:"rows,omitempty"`
}

// ContextFabricClaimedFactRow is one row of a claimed fact's OPTIONAL
// renderable table (CHAOS-4347). Fields are LEAF ContextFabricScalarValue
// entries -- a row never nests another table, so a renderable claim's shape
// is bounded by construction. See ContextFabricClaimedFact.Rows.
type ContextFabricClaimedFactRow struct {
	Fields map[string]ContextFabricScalarValue `json:"fields"`
}

type ContextFabricSourceObservation struct {
	Source     string                   `json:"source"`
	State      ContextFabricSourceState `json:"state"`
	ObservedAt *time.Time               `json:"observed_at,omitempty"`
	Watermark  string                   `json:"watermark,omitempty"`
	Reason     string                   `json:"reason,omitempty"`
}

type ContextFabricCoverage struct {
	Sources         []ContextFabricSourceObservation `json:"sources"`
	Partial         bool                             `json:"partial"`
	DegradedReasons []string                         `json:"degraded_reasons,omitempty"`
}

type ContextFabricVersionSet struct {
	ServiceVersion  string `json:"service_version"`
	ContractVersion string `json:"contract_version"`
	// Backend names the graph retrieval capability class (e.g. "graph"),
	// not the vendor or product backing it. The public answer contract
	// must not name a specific vendor: a vendor swap is an operational
	// change, not a client-visible contract change.
	Backend string `json:"backend"`
	// BackendVersion is an opaque version token for the backend
	// implementation in force. Callers must treat it as opaque -- it is
	// not guaranteed to follow any particular scheme and must not be
	// parsed for vendor identity or capability.
	BackendVersion          string `json:"backend_version,omitempty"`
	ProjectionVersion       string `json:"projection_version"`
	QueryVersion            string `json:"query_version"`
	InterpretationVersion   string `json:"interpretation_version"`
	SynthesisVersion        string `json:"synthesis_version"`
	CanonicalServiceVersion string `json:"canonical_service_version"`
	// ModelIdentity names the provider and model that produced this
	// result's synthesis (e.g. "openai-compatible/gpt-5-nano"), never a
	// bare vendor name on its own -- the same provider-shaped rule
	// §19.3.6 applies to configuration applies here. It is one of the
	// dimensions CHAOS-3782 answer reuse binds to (TRD §19.7.2, AC-3782-7):
	// a result generated under one model identity is never reused once the
	// organization's configured model changes, even if the prompt/model
	// version strings captured in SynthesisVersion happen to collide.
	//
	// omitempty (Codex round-3 finding 2): the field is genuinely
	// OPTIONAL -- a pre-0011 payload, or one written with answer reuse
	// disabled, never carried it (see the schema's own minLength:1
	// description). Without omitempty, decoding such a legacy payload
	// yields the Go zero value "", and re-marshaling emits
	// "model_identity":"" -- a PRESENT empty string, which violates the
	// schema's minLength:1 even though absence itself is allowed. A
	// result read then written back unchanged (Get -> re-serve, or any
	// round trip) would fail schema validation purely from this
	// asymmetry.
	ModelIdentity string `json:"model_identity,omitempty"`
}

type ContextFabricInterpretedQuestion struct {
	Shape               ContextFabricInvestigationShape `json:"shape"`
	RequestedJudgment   string                          `json:"requested_judgment"`
	SubjectTerms        []string                        `json:"subject_terms,omitempty"`
	ComparisonTerms     []string                        `json:"comparison_terms,omitempty"`
	TimeContext         ContextFabricTimeContext        `json:"time_context"`
	FactRequirements    []ContextFabricFactRequirement  `json:"fact_requirements"`
	ClarificationNeeded bool                            `json:"clarification_needed"`
	ClarificationReason string                          `json:"clarification_reason,omitempty"`
	// WindowClass/WindowConfidence (CHAOS-3900 W1, promoted from the W0
	// shadow-only receipt-only capture -- see chaos3900_window_vocab.go)
	// is the model's own sanitized, closed-vocabulary evidence-window
	// classification pick. This is the ONLY latitude a model has over an
	// evidence window: never a timestamp, never a duration. Additive
	// optional in v1: every interpretation produced before CHAOS-3900 W1
	// lacks these, and an absent value is a legitimate "the model made no
	// pick" state the engine's post-pass (graphrank.ClassifyWindow) falls
	// back from, never an error.
	WindowClass      ContextFabricWindowClass      `json:"window_class,omitempty"`
	WindowConfidence ContextFabricWindowConfidence `json:"window_confidence,omitempty"`
}

type ContextFabricFactRequirement struct {
	Kind       ContextFabricFactKind     `json:"kind"`
	Subjects   []ContextFabricSubjectRef `json:"subjects,omitempty"`
	Parameters map[string]string         `json:"parameters,omitempty"`
}

type ContextFabricInvestigationResult struct {
	SchemaVersion      string                           `json:"schema_version"`
	ResultID           string                           `json:"result_id"`
	RequestID          string                           `json:"request_id"`
	GeneratedAt        time.Time                        `json:"generated_at"`
	Status             ContextFabricInvestigationStatus `json:"status"`
	Question           string                           `json:"question"`
	Interpretation     ContextFabricInterpretedQuestion `json:"interpretation"`
	SubjectResolution  ContextFabricSubjectResolution   `json:"subject_resolution"`
	Cohort             *ContextFabricCohort             `json:"cohort,omitempty"`
	DirectJudgment     string                           `json:"direct_judgment"`
	CurrentState       string                           `json:"current_state"`
	StrongestPressures []string                         `json:"strongest_pressures"`
	Drivers            []ContextFabricDriverJudgment    `json:"drivers"`
	RemainingWork      []ContextFabricFinding           `json:"remaining_work"`
	ReadinessGaps      []ContextFabricFinding           `json:"readiness_gaps"`
	Paths              []ContextFabricRelationshipPath  `json:"paths"`
	Conflicts          []ContextFabricFinding           `json:"conflicts"`
	Limitations        []string                         `json:"limitations"`
	// LimitationsDisplaced counts model-authored limitations the engine
	// dropped to fit the retrieval-degradation disclosure inside
	// ContextFabricLimitationsMaxCount (CHAOS-3746).
	//
	// It exists because the loss cannot be inferred from Limitations
	// itself: a displaced list and a list that simply had room are the
	// same length and the same shape, both ending with the disclosure. A
	// consumer told nothing would read a list with content silently
	// removed as a complete one, which is the exact class the projection
	// budget exists to prevent -- so the canonical result declares it too,
	// rather than leaving it to be re-derived by something that cannot.
	//
	// Zero for every result written before this field existed, and for
	// every result where nothing was displaced, so it is omitempty.
	LimitationsDisplaced int      `json:"limitations_displaced,omitempty"`
	EvidenceRefIDs       []string `json:"evidence_ref_ids"`
	// ClaimedFacts is the closed, checkable set of canonical fact
	// restatements every fact-shaped driver/finding in this result cites
	// by ClaimID. See ContextFabricClaimedFact's doc comment for why this
	// exists and docs/design/context-fabric-result-semantics.md for the
	// full evidence-kind distinction (canonical observation vs graph
	// association vs source assertion vs inference).
	ClaimedFacts        []ContextFabricClaimedFact `json:"claimed_facts"`
	Coverage            ContextFabricCoverage      `json:"coverage"`
	Versions            ContextFabricVersionSet    `json:"versions"`
	DeterministicAnswer string                     `json:"deterministic_answer"`
	Warnings            []string                   `json:"warnings"`
	// Reused marks whether this result was served from the immutable
	// result store instead of a fresh investigation (CHAOS-3782, TRD
	// §19.7, AC-3782-2). When true, ResultID and GeneratedAt above are NOT
	// this request's own identifier and timestamp -- they are the
	// identifier and generation time of the reused result, unchanged from
	// when it was first produced. A caller can always tell a reused answer
	// from a fresh one by this field alone.
	Reused bool `json:"reused"`
	// Temporal states the time this answer speaks for on a historical
	// time axis (CHAOS-3781, AC-3781-2). nil on the current axis, which
	// keeps every pre-CHAOS-3781 result byte-identical: an additive
	// optional field stays inside v1 per the contract-first rule.
	Temporal *ContextFabricTemporalLabel `json:"temporal,omitempty"`
	// EffectiveEvidenceWindow (CHAOS-3900 W1) is the evidence window this
	// answer actually speaks for, once canonicalization has run -- nil
	// when no window is in play for this investigation (a non-current
	// axis, or a resolved class that carries no window at all, e.g.
	// state_snapshot). Additive optional field, same contract-first
	// discipline as Temporal: absent on every result generated before
	// this field existed.
	EffectiveEvidenceWindow *ContextFabricEffectiveEvidenceWindow `json:"effective_evidence_window,omitempty"`
	// WindowClarification (CHAOS-3900 W1) carries every window option this
	// result offered, when it offered any -- see
	// ContextFabricWindowClarification's own doc comment for why it lives
	// on the canonical result rather than projection-only.
	WindowClarification *ContextFabricWindowClarification `json:"window_clarification,omitempty"`
	// StructureNeeds (CHAOS-3900 P1) is the disclosure block naming which
	// intent-frame members (kind/anchor/handle/window) are missing or
	// ambiguous, with typed receipt-bound offers per member -- present
	// whenever this round ends short of decisive. nil on a decisive
	// result that needed no clarification at all.
	StructureNeeds *ContextFabricStructureNeeds `json:"structure_needs,omitempty"`
	// ConfirmedStructure (CHAOS-3900 P1) is the wire-visible disposition
	// for every structure member THIS request carried (receipt or
	// explicit field), including vetoed ones -- present whenever the
	// request carried any structure receipt or explicit structure field,
	// one entry per carried member (design brief §2.1's silent-drop
	// closure).
	ConfirmedStructure []ContextFabricConfirmedStructureEntry `json:"confirmed_structure,omitempty"`
	// StructureOfferSnapshot (CHAOS-3900 P1) echoes the SOURCE offer set
	// for every redeemed member on a DECISIVE result only (design brief
	// §2.1's B5 gap: StructureNeeds itself appears only on non-decisive
	// terminals, so a decisive result reached via confirmation would
	// otherwise lose the (offered, selected) pair the Bridge needs).
	// Construction-bounded (never post-hoc truncated) and
	// CANONICAL-STORAGE-ONLY -- it is deliberately absent from
	// ContextFabricAnswerProjection; ConfirmedStructure above is what
	// projects.
	StructureOfferSnapshot []ContextFabricStructureOfferSnapshotEntry `json:"structure_offer_snapshot,omitempty"`
}

// ContextFabricScalarValue is the only free-form value admitted by the public
// projection-control contract. Nested objects and arrays remain disallowed.
type ContextFabricScalarValue struct {
	String  *string  `json:"string,omitempty"`
	Integer *int64   `json:"integer,omitempty"`
	Number  *float64 `json:"number,omitempty"`
	Boolean *bool    `json:"boolean,omitempty"`
	Null    bool     `json:"null,omitempty"`
}

type ContextFabricProjectionBatch struct {
	SchemaVersion       string                                `json:"schema_version"`
	BatchID             string                                `json:"batch_id"`
	OrgID               string                                `json:"org_id"`
	Source              string                                `json:"source"`
	SourceVersion       string                                `json:"source_version"`
	Cursor              string                                `json:"cursor"`
	NextCursor          string                                `json:"next_cursor"`
	GeneratedAt         time.Time                             `json:"generated_at"`
	FullSnapshot        bool                                  `json:"full_snapshot"`
	CompleteEnumeration bool                                  `json:"complete_enumeration"`
	Entities            []ContextFabricEntityProjection       `json:"entities"`
	Relationships       []ContextFabricRelationshipProjection `json:"relationships"`
	Contents            []ContextFabricContentProjection      `json:"contents"`
	Episodes            []ContextFabricEpisodeProjection      `json:"episodes"`
	Tombstones          []ContextFabricProjectionTombstone    `json:"tombstones"`
}

type ContextFabricAuthorizationScope struct {
	RepositorySlugs []string `json:"repository_slugs,omitempty"`
	ProjectIDs      []string `json:"project_ids,omitempty"`
	TeamIDs         []string `json:"team_ids,omitempty"`
}

type ContextFabricEntityProjection struct {
	Subject ContextFabricSubjectRef `json:"subject"`
	Aliases []string                `json:"aliases,omitempty"`
	// ProviderAliases (CHAOS-3884, additive) are provider-qualified
	// identity variants (e.g. "github:full-chaos/dev-health-acr"),
	// distinct from Aliases' bare-name-shaped handles so a resolver can
	// tell MatchAlias (bare name) apart from MatchProviderKey (provider
	// variant) even though both flow through the identical
	// projection/write/read path (falkorgraph: propAliases vs
	// propProviderAliases). Same bounds/uniqueness/no-"|" discipline as
	// Aliases -- see Validate().
	ProviderAliases []string                            `json:"provider_aliases,omitempty"`
	PreviousNames   []string                            `json:"previous_names,omitempty"`
	ProviderIDs     map[string]string                   `json:"provider_ids,omitempty"`
	Properties      map[string]ContextFabricScalarValue `json:"properties,omitempty"`
	Authorization   ContextFabricAuthorizationScope     `json:"authorization"`
	EvidenceRefIDs  []string                            `json:"evidence_ref_ids"`
	ObservedAt      time.Time                           `json:"observed_at"`
	ValidFrom       *time.Time                          `json:"valid_from,omitempty"`
	ValidTo         *time.Time                          `json:"valid_to,omitempty"`
	SourceVersion   string                              `json:"source_version"`
}

type ContextFabricRelationshipProjection struct {
	RelationshipID  string                              `json:"relationship_id"`
	Type            ContextFabricRelationshipType       `json:"type"`
	From            ContextFabricSubjectRef             `json:"from"`
	To              ContextFabricSubjectRef             `json:"to"`
	Properties      map[string]ContextFabricScalarValue `json:"properties,omitempty"`
	Derivation      ContextFabricDerivationMethod       `json:"derivation"`
	EpistemicStatus ContextFabricEpistemicStatus        `json:"epistemic_status"`
	Authorization   ContextFabricAuthorizationScope     `json:"authorization"`
	EvidenceRefIDs  []string                            `json:"evidence_ref_ids"`
	ObservedAt      time.Time                           `json:"observed_at"`
	ValidFrom       *time.Time                          `json:"valid_from,omitempty"`
	ValidTo         *time.Time                          `json:"valid_to,omitempty"`
	SourceVersion   string                              `json:"source_version"`
}

type ContextFabricContentProjection struct {
	ContentID      string                          `json:"content_id"`
	Subject        ContextFabricSubjectRef         `json:"subject"`
	Title          string                          `json:"title"`
	Body           string                          `json:"body"`
	ContentDigest  string                          `json:"content_digest"`
	Authorization  ContextFabricAuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string                        `json:"evidence_ref_ids"`
	ObservedAt     time.Time                       `json:"observed_at"`
	SourceVersion  string                          `json:"source_version"`
	Untrusted      bool                            `json:"untrusted"`
}

type ContextFabricEpisodeProjection struct {
	EpisodeID      string                          `json:"episode_id"`
	Subject        ContextFabricSubjectRef         `json:"subject"`
	Goal           string                          `json:"goal"`
	Outcome        string                          `json:"outcome"`
	Summary        string                          `json:"summary"`
	Authorization  ContextFabricAuthorizationScope `json:"authorization"`
	EvidenceRefIDs []string                        `json:"evidence_ref_ids"`
	StartedAt      time.Time                       `json:"started_at"`
	EndedAt        time.Time                       `json:"ended_at"`
	SourceVersion  string                          `json:"source_version"`
}

type ContextFabricProjectionTombstone struct {
	Kind          string    `json:"kind"`
	CanonicalID   string    `json:"canonical_id"`
	Reason        string    `json:"reason"`
	EffectiveAt   time.Time `json:"effective_at"`
	SourceVersion string    `json:"source_version"`
}

type ContextFabricProjectionReceipt struct {
	BatchID           string    `json:"batch_id"`
	AppliedAt         time.Time `json:"applied_at"`
	BackendWatermark  string    `json:"backend_watermark"`
	EntitiesApplied   int       `json:"entities_applied"`
	EdgesApplied      int       `json:"edges_applied"`
	ContentsApplied   int       `json:"contents_applied"`
	EpisodesApplied   int       `json:"episodes_applied"`
	TombstonesApplied int       `json:"tombstones_applied"`
}

type ContextFabricProjectionWatermark struct {
	OrgID            string    `json:"org_id"`
	Source           string    `json:"source"`
	Cursor           string    `json:"cursor"`
	SourceVersion    string    `json:"source_version"`
	ProjectedAt      time.Time `json:"projected_at"`
	BackendWatermark string    `json:"backend_watermark"`
}

type ContextFabricProjectionCheckpoint struct {
	OrgID  string `json:"org_id"`
	Source string `json:"source"`
	// Epoch is the CHAOS-3898 S2a per-epoch checkpoint dimension (design
	// brief §3.4): which graph epoch this cursor describes. The zero value
	// (0) is the legacy/pre-migration epoch -- every checkpoint written
	// before this field existed, and every checkpoint an unmigrated caller
	// writes today, is an epoch-0 checkpoint by construction, so this field
	// requires no behavior change from any existing caller. Not part of any
	// external wire contract (this type has no JSON Schema/OpenAPI/MCP
	// surface); the json tag exists only for parity with its sibling
	// fields.
	Epoch            int64     `json:"epoch"`
	Cursor           string    `json:"cursor"`
	SourceVersion    string    `json:"source_version"`
	BackendWatermark string    `json:"backend_watermark"`
	UpdatedAt        time.Time `json:"updated_at"`
	// RowsApplied (CHAOS-4305) is a durable, monotonic count of rows this
	// (org, epoch, source) checkpoint has applied to the backend, advanced
	// in the SAME CAS statement that moves Cursor
	// (pgprojection.CheckpointStore.CompareAndSwapProjectionCheckpoint/
	// ...ForEpoch) -- it can never diverge from the cursor it travels with.
	// This closes the checkpoint<->cf_build_source_progress non-atomicity
	// gap: projectionrun.Coordinator's runBuildPair now seeds each tick's
	// row-count accumulator from this field (via ProjectionRun.RowsApplied)
	// instead of from cf_build_source_progress.rows_projected, a
	// separately-written value that could go durably stale if
	// RecordSourceProgress failed for a whole drain. Same "not part of any
	// external wire contract" status as Epoch above -- the json tag exists
	// only for parity with its sibling fields.
	RowsApplied int64 `json:"rows_applied"`
}

type ContextFabricCapabilities struct {
	Enabled                        bool                          `json:"enabled"`
	SupportsOpenQuestions          bool                          `json:"supports_open_questions"`
	SupportsPriorSubjectReceipts   bool                          `json:"supports_prior_subject_receipts"`
	SupportedRequestSchemaVersions []string                      `json:"supported_request_schema_versions"`
	SupportedResultSchemaVersions  []string                      `json:"supported_result_schema_versions"`
	Limits                         ContextFabricCapabilityLimits `json:"limits"`
}

type ContextFabricCapabilityLimits struct {
	MaxSubjectCandidates int `json:"max_subject_candidates"`
	MaxCohortMembers     int `json:"max_cohort_members"`
	MaxRelationshipPaths int `json:"max_relationship_paths"`
	MaxDrivers           int `json:"max_drivers"`
	MaxEvidenceRefs      int `json:"max_evidence_refs"`
	MaxSerializedBytes   int `json:"max_serialized_bytes"`
}
