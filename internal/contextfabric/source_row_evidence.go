package contextfabric

import (
	"context"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Source-row expansion of Context Fabric evidence refs (CHAOS-6180, option S).
//
// ExpandCitedEvidence (option R) serves an acr:v1:<type>:<id> ref from the
// stored investigation result that cited it: the persisted record, never the
// row the id names. ExpandEvidence first tries that ROW. A SourceRowResolver
// reads it from the Dev Health tables and authorizes it per kind (the
// caller's repository grant for every kind this file routes today). When it
// finds no row the caller may read, the persisted-record path runs exactly as
// before, so a row that is out of the caller's grant and a row that does not
// exist reach the same record path with the same result.
//
// The two expansions stay apart on existing fields only: a source row names
// source.system "dev_health" and the catalog's provenance, and its metadata
// says record "source_row" and row_state "current"; the persisted record keeps
// source.system "acr-investigation-record" and provenance "derived".

// SourceRowSystem is the source system a source-row expansion names: the same
// value the packet catalog's rows carry.
const SourceRowSystem = "dev_health"

// SourceRowRoute is how refs of one entity type reach their source row.
type SourceRowRoute string

const (
	// SourceRowRouteRecord: no source row is read; the persisted record
	// serves the ref.
	SourceRowRouteRecord SourceRowRoute = "record"
	// SourceRowRouteRepository: the id opens with the repository UUID
	// (<repo>:<rest>, or the UUID alone for a repository).
	SourceRowRouteRepository SourceRowRoute = "repository"
	// SourceRowRouteRowRepository: the id carries no repository; the row
	// names the repositories it maps to.
	SourceRowRouteRowRepository SourceRowRoute = "row_repository"
	// SourceRowRouteOwnership: the id names an organization-level row (a
	// team, a project) authorized by OWNERSHIP through the direct data
	// tools' subject gate (directread.SubjectGate), never by membership.
	SourceRowRouteOwnership SourceRowRoute = "ownership"
)

// SourceRowPlan is the route of one entity type and the statement that reads
// its row (a contextpacket catalog or source-row-only query id).
type SourceRowPlan struct {
	Route SourceRowRoute
	Query string
}

// sourceRowPlans is total over the closed evidence-entity vocabulary
// (TestSourceRowPlansAreTotal). Why each record kind stays on the record:
//
//   - organization: no canonical organization table exists.
//   - episode: an approved agent episode lives in ACR Postgres and is not
//     durable truth (AGENTS.md).
//   - project-team (CHAOS-7227 scope): <provider>:<project>:<team> joins two
//     colon-capable ids, and team_project_ownership is keyed by more than
//     the pair, so one ref is not one row (CHAOS-7252).
//   - work-item-dependency, work-item-hierarchy, work-item-team (CHAOS-7226
//     r2 P1 class): their producers join TWO colon-capable ids with ':'
//     (<src>:<tgt>:<key>, <repo>:<src>:<tgt>:<type>, <repo>:<child>:<parent>,
//     <repo>:<work item>:<team>), so one ref string can name two rows, and
//     a lookup by that string serves whichever one is left or admitted.
//     A source row is read only for a grammar that is injective: the
//     fixed-length repository UUID plus ONE opaque id, or the id alone
//     (TestSourceRowGrammarsAreInjective). They return once their producers
//     mint an injective grammar.
//   - deployment-incident (same class): its id is edge_id, a hash of
//     (deployment_id, incident_id) without the repository, and the table is
//     keyed (org_id, deployment_id, incident_id, source); deployment ids
//     collide across repositories, so one edge_id can be two rows in two
//     repositories and the grant filter would choose between them. A
//     row-anchored kind is read only when its id is its table's key within
//     the organization (incident: operational_incidents (org_id, id)).
//   - commit, commit-file, graph, hotspot, complexity, ai-run, ai-artifact,
//     review-outcome: no Context Fabric producer mints them. They occur only
//     as packet-catalog locators inside ev2 handles, so an acr:v1 ref of
//     these kinds reaches a caller only from a legacy stored result.
var sourceRowPlans = map[contractsv1.ContextFabricEvidenceEntityType]SourceRowPlan{
	contractsv1.ContextFabricEvidenceEntityAIArtifact:         {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityAIRun:              {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityCI:                 {Route: SourceRowRouteRepository, Query: "ci_pipeline_runs.v1"},
	contractsv1.ContextFabricEvidenceEntityCommit:             {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityCommitFile:         {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityComplexity:         {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityDeployment:         {Route: SourceRowRouteRepository, Query: "deployments.v1"},
	contractsv1.ContextFabricEvidenceEntityDeploymentIncident: {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityEpisode:            {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityGraph:              {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityHotspot:            {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityIncident:           {Route: SourceRowRouteRowRepository, Query: "incidents.v1"},
	contractsv1.ContextFabricEvidenceEntityOrganization:       {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityProject:            {Route: SourceRowRouteOwnership, Query: "projects.v1"},
	contractsv1.ContextFabricEvidenceEntityProjectTeam:        {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityPullRequest:        {Route: SourceRowRouteRepository, Query: "pull_requests.v1"},
	contractsv1.ContextFabricEvidenceEntityRepository:         {Route: SourceRowRouteRepository, Query: "repository_freshness.v1"},
	contractsv1.ContextFabricEvidenceEntityReview:             {Route: SourceRowRouteRepository, Query: "pull_request_reviews.v1"},
	contractsv1.ContextFabricEvidenceEntityReviewOutcome:      {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityTeam:               {Route: SourceRowRouteOwnership, Query: "teams.v1"},
	contractsv1.ContextFabricEvidenceEntityWorkItem:           {Route: SourceRowRouteRepository, Query: "work_items.v1"},
	contractsv1.ContextFabricEvidenceEntityWorkItemDependency: {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy:  {Route: SourceRowRouteRecord},
	contractsv1.ContextFabricEvidenceEntityWorkItemTeam:       {Route: SourceRowRouteRecord},
}

// SourceRowPlanFor returns the plan of an entity type segment. A segment
// outside the closed vocabulary stays on the record.
func SourceRowPlanFor(entityType string) SourceRowPlan {
	plan, ok := sourceRowPlans[contractsv1.ContextFabricEvidenceEntityType(entityType)]
	if !ok {
		return SourceRowPlan{Route: SourceRowRouteRecord}
	}
	return plan
}

// SourceRowPlans returns a copy of the plan table.
func SourceRowPlans() map[contractsv1.ContextFabricEvidenceEntityType]SourceRowPlan {
	plans := make(map[contractsv1.ContextFabricEvidenceEntityType]SourceRowPlan, len(sourceRowPlans))
	for kind, plan := range sourceRowPlans {
		plans[kind] = plan
	}
	return plans
}

// SourceRowQueryVocabulary is the closed set of statement ids a plan names,
// sorted by vocabulary order of the kinds.
func SourceRowQueryVocabulary() []string {
	seen := map[string]bool{}
	var queries []string
	for _, kind := range contractsv1.ContextFabricEvidenceEntityTypeVocabulary() {
		if query := sourceRowPlans[kind].Query; query != "" && !seen[query] {
			seen[query] = true
			queries = append(queries, query)
		}
	}
	return queries
}

// SourceRowReason is the closed outcome of one source-row resolution.
type SourceRowReason string

const (
	// SourceRowServed: one row the caller may read.
	SourceRowServed SourceRowReason = "served"
	// SourceRowKindOnRecord: the kind has no source row route.
	SourceRowKindOnRecord SourceRowReason = "kind_on_record"
	// SourceRowIDMalformed: the id does not fit the kind's grammar (decided
	// from the id text alone, before any read).
	SourceRowIDMalformed SourceRowReason = "id_malformed"
	// SourceRowNoRow: no row in a repository the caller may read. An
	// out-of-grant row and an absent row are this ONE reason on the wire;
	// the source_repositories/source_admitted counts tell them apart in the
	// trace.
	SourceRowNoRow SourceRowReason = "no_row"
	// SourceRowAmbiguous: more than one distinct row matched, or a row
	// mapped to more repositories than the bound.
	SourceRowAmbiguous SourceRowReason = "ambiguous"
	// SourceRowUnavailable: a read failed.
	SourceRowUnavailable SourceRowReason = "unavailable"
	// SourceRowInvalid: the one row found does not make a valid expansion
	// (a catalog value outside the contract, e.g. an unmapped provenance).
	// Retrying cannot help, so the persisted record answers instead and the
	// trace names the row's statement.
	SourceRowInvalid SourceRowReason = "row_invalid"
	// SourceRowBackendAbsent: this deployment composed no source-row
	// resolver.
	SourceRowBackendAbsent SourceRowReason = "backend_absent"
)

// SourceRowInvalidLogMessage is the one Warn line each row_invalid emits
// (beside the trace reason): a row the contract rejects is a catalog or
// contract defect, never an expected outcome. It carries the kind and the
// statement only, never an id.
const SourceRowInvalidLogMessage = "context fabric source row invalid"

// SourceRowReasonVocabulary is the closed set of reasons.
func SourceRowReasonVocabulary() [8]SourceRowReason {
	return [8]SourceRowReason{SourceRowServed, SourceRowKindOnRecord, SourceRowIDMalformed, SourceRowNoRow, SourceRowAmbiguous, SourceRowUnavailable, SourceRowInvalid, SourceRowBackendAbsent}
}

// Source-row id grammars (the source_grammar trace field).
const (
	// SourceRowGrammarRepoAnchored: <repo UUID>[:<rest>].
	SourceRowGrammarRepoAnchored = "repo_anchored"
	// SourceRowGrammarRowAnchored: the id alone; the row names its
	// repositories.
	SourceRowGrammarRowAnchored = "row_anchored"
	// SourceRowGrammarOrgKeyed: the id is an organization-level row key (a
	// team id; a project's <provider>:<id>).
	SourceRowGrammarOrgKeyed = "org_keyed"
)

// SourceRowGrammarVocabulary is the closed set of grammars.
func SourceRowGrammarVocabulary() [3]string {
	return [3]string{SourceRowGrammarRepoAnchored, SourceRowGrammarRowAnchored, SourceRowGrammarOrgKeyed}
}

// SourceRowDecision records what decided one source-row resolution. It
// carries counts and closed values only, never ids or labels.
type SourceRowDecision struct {
	Reason SourceRowReason
	// Query is the plan's statement id; empty when no read was planned.
	Query string
	// Grammar is the grammar of the served row, else of the last one tried.
	Grammar string
	// Repositories is how many repositories the lookups returned, before
	// the caller's grant; Admitted is how many of them the grant admits. On
	// the ownership route they count the subject the gate found (1 unless it
	// is absent) and admitted.
	Repositories int
	Admitted     int
	// Rows is how many rows the row reads returned.
	Rows int
	Err  error
}

// Read reports whether the resolver ran any read.
func (d SourceRowDecision) Read() bool {
	switch d.Reason {
	case SourceRowKindOnRecord, SourceRowIDMalformed, SourceRowBackendAbsent:
		return false
	}
	return d.Query != ""
}

// SourceRowResolver expands the source row of one Context Fabric evidence
// ref. entityType is a vocabulary member whose plan is not the record route;
// entityID is the ref's id segment, verbatim. The resolver authorizes the row
// for principal and must give an out-of-grant row and an absent row the same
// reads and the same SourceRowNoRow.
type SourceRowResolver interface {
	ResolveSourceRow(ctx context.Context, principal storage.Principal, entityType, entityID string) (contractsv1.ExpandedEvidence, SourceRowDecision)
}

// ExpandEvidence expands one Context Fabric evidence ref for principal: the
// source row when the resolver serves one, else the persisted record
// (ExpandCitedEvidence). source may be nil (no ClickHouse composed).
func ExpandEvidence(ctx context.Context, principal storage.Principal, ref string, source SourceRowResolver, lookup CitedEvidenceLookup, results InvestigationResultStore, gate StoredResultAuthorizer, now time.Time) (contractsv1.ExpandedEvidence, EvidenceExpansionDecision) {
	entityType, entityID, parsed := parseContextFabricEvidenceRef(ref)
	sourceDecision := SourceRowDecision{Reason: SourceRowIDMalformed}
	if parsed {
		plan := SourceRowPlanFor(entityType)
		switch {
		case plan.Route == SourceRowRouteRecord:
			sourceDecision = SourceRowDecision{Reason: SourceRowKindOnRecord}
		case storage.IsNil(source):
			sourceDecision = SourceRowDecision{Reason: SourceRowBackendAbsent, Query: plan.Query}
		default:
			var expanded contractsv1.ExpandedEvidence
			expanded, sourceDecision = source.ResolveSourceRow(ctx, principal, entityType, entityID)
			decision := EvidenceExpansionDecision{EntityType: registeredEvidenceEntityType(entityType), Source: sourceDecision}
			switch sourceDecision.Reason {
			case SourceRowServed:
				if err := expanded.Validate(); err != nil {
					decision.Reason, decision.Err = EvidenceExpansionInvalid, err
					return contractsv1.ExpandedEvidence{}, decision
				}
				if expanded.Evidence.EvidenceRefID != ref || expanded.Evidence.Source.System != SourceRowSystem {
					decision.Reason = EvidenceExpansionInvalid
					decision.Err = errSourceRowMislabeled
					return contractsv1.ExpandedEvidence{}, decision
				}
				decision.Reason = EvidenceExpansionSourceRowServed
				return expanded, decision
			case SourceRowUnavailable:
				// A failed read may hide the row this caller can read: a
				// retryable 503, never a fallback that would answer as if
				// the row were absent.
				decision.Reason, decision.Err = EvidenceExpansionSourceRowUnavailable, sourceDecision.Err
				return contractsv1.ExpandedEvidence{}, decision
			}
		}
	}
	expanded, decision := ExpandCitedEvidence(ctx, principal, ref, lookup, results, gate, now)
	decision.Source = sourceDecision
	return expanded, decision
}

type sourceRowError string

func (e sourceRowError) Error() string { return string(e) }

const errSourceRowMislabeled = sourceRowError("contextfabric: source-row expansion does not name the ref or the source system")
