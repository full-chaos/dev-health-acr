package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
)

// QueryVersion is this package's query_version-equivalent: every
// FactProviderResult and FactCapability this package produces carries it,
// so a consumer can tell exactly which SQL/column shape produced a fact.
//
// Exported (CHAOS-3810 codex round-1 P1) for the same reason
// devhealthsource.ClickHouseSourceVersion is: hosted composition stamps it
// into InvestigationResult.Versions.QueryVersion, and a result that reports
// "unwired" for a version whose authority exists in this repository cannot
// be attributed across a rebuild. There is deliberately ONE name for this
// value -- a second, unexported alias would be the anchor/alias drift this
// package's own comments warn about elsewhere.
//
// v1 -> v2 (CHAOS-4363, codex round-1 P1): QueryVersion is a CONJUNCTIVE
// answer-reuse key dimension (ports.go's ReuseKey/AnswerReuseGate). Every
// capability in this package shares the ONE constant, so bumping it on any
// SQL/capability shape change -- here, FactHealth/FactWorkload/
// FactInvestment/FactReadiness widening to answer for a project subject --
// invalidates every reuse candidate saved under the OLD shape, not only the
// four changed ones. That over-invalidation is the safe direction: leaving
// it unbumped would let an answer reuse gate serve a pre-deployment answer
// that never actually ran the new project readers, silently.
//
// v2 -> v3 (CHAOS-4418): FactMetrics' repository read widens from a flat
// scalar snapshot of the latest day to a real per-day series (Rows), and
// FactHealth's repo/team read widens to add the risk_rules per-component
// breakdown. Same over-invalidation rationale: a reuse candidate saved
// under the old flat/no-breakdown shape must not be served as if it
// carried the new Rows tables it never actually computed.
//
// v3 -> v4 (CHAOS-4521, codex round-1 P2): a read that reached ClickHouse
// and matched no rows now reports `no_data` with a reason instead of
// `available` with none. That changes what EVERY provider in this package
// reports, not what any one of them computes -- and the answer reuse gate
// runs BEFORE ReadFacts, so without this bump a candidate saved
// pre-deployment would keep being served with its old `available`-over-an-
// empty-bundle coverage, silently skipping both the fix and its ledger.
// Same over-invalidation rationale as v1->v2 and v2->v3 above: the safe
// direction is to invalidate more than strictly changed.
//
// v4 -> v5 (CHAOS-4521b, codex P1): the project reads changed SHAPE, not
// just their reported state. flow/readiness/workload now key on the
// project's own work_scope_id instead of its owning team's rows, and the
// ownership join underneath health/investment/landscape moved onto the
// project identity. A candidate saved under v4 was computed from a
// DIFFERENT row set -- empty, or another project's -- and answer reuse runs
// before any fact provider, so without this bump the gate would keep
// serving exactly the answers this change exists to replace.
const QueryVersion = "devhealthfacts.clickhouse.v5"

// defaultTimeout is the FactCapability.Timeout this package advertises for
// every provider. The registry (fact_registry.go's readProvider) wraps each
// ReadFacts call in a context with this deadline; providers here never add
// a second timeout of their own -- they only ever propagate the ctx they
// are given into ClickHouseQueryClient.Query.
const defaultTimeout = 5 * time.Second

// maxFactRowsPerQuery bounds how many rows a single provider query is
// allowed to read from ClickHouse (H6/H7 adversarial finding: "unbounded
// fanout" -- a single subject with a pathological number of matching rows,
// e.g. thousands of work_item_dependencies rows, otherwise returns them all
// before any truncation happens). 200 gives generous headroom for any one
// subject's rows (the providers in this package never return more than a
// handful of rows per subject in the ordinary case) while still sitting
// well under CanonicalFactBundle's bundle-level bounds, so one pathological
// subject can't blow the whole investigation's fact budget.
const maxFactRowsPerQuery = 200

// withRowLimit appends a LIMIT clause bounding a provider's SELECT to
// maxFactRowsPerQuery. maxFactRowsPerQuery is an internal Go constant, never
// a caller-supplied value, so -- mirroring this package's existing
// convention for such constants (see dependencies.go's
// blockerRelationshipType, inlined the same way) -- it is safe to inline
// directly into the statement rather than route it through
// clickhouseFacts.query's bindings, which only ever carry caller/subject
// scoped values (org_id, ids).
func withRowLimit(statement string) string {
	return statement + "\nLIMIT " + strconv.Itoa(maxFactRowsPerQuery)
}

// clickhouseFacts is the shared ClickHouse query boundary every provider in
// this package embeds. It reuses internal/contextpacket.ClickHouseQueryClient
// -- the same query boundary internal/contextfabric/devhealthsource uses --
// rather than opening a second database path.
type clickhouseFacts struct {
	client contextpacket.ClickHouseQueryClient
}

// query runs statement scoped to orgID and the given raw ids, invoking scan
// once per returned row. It never adds its own timeout; ctx is propagated
// straight through to the query client.
// extra carries any additional bindings the statement references -- today
// only the CHAOS-3781 time bounds (timebound.go). Like org_id and ids,
// every one is a bound PARAMETER, never interpolated text, so a requested
// instant can no more reach the statement than a subject id can.
func (f clickhouseFacts) query(ctx context.Context, statement, orgID string, ids []string, scan func(contextpacket.ClickHouseRowScanner) error, extra ...timeBinding) error {
	if f.client == nil {
		return errors.New("devhealthfacts: clickhouse query client is required")
	}
	bindings := []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "ids", Value: ids},
	}
	for _, binding := range extra {
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: binding.Name, Value: binding.Value})
	}
	rows, err := f.client.Query(ctx, statement, bindings)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// readFailure wraps a query/scan error into the *contextfabric.FactReadFailure
// shape the registry classifies (fact_registry.go's classifyFactReadError),
// rather than a bare error.
//
// Reason is a fixed, non-parameterized string built only from action (an
// internal Go string literal every caller controls -- never caller/request
// supplied) -- it deliberately never embeds err.Error() or "%v" of err (M6
// adversarial finding: fact_registry.go's classifyFactReadError copies this
// Reason straight into the PUBLIC context_fabric_investigation_result.v1
// response's coverage.sources[].reason, so a raw ClickHouse driver error --
// which can carry connection details, internal hostnames, or query
// fragments -- must never reach it). This mirrors
// internal/contextfabric/falkorgraph/client.go's safeDependencyError, which
// classifies to a fixed reason and never embeds the raw SDK error either.
// contextfabric.FactReadFailure carries no field for the original err, and
// this package has no server-side logging seam to hand it to (inventing one
// here is out of scope for this fix), so err is accepted for call sites'
// context but intentionally never reaches the returned error at all.
func readFailure(action string, err error) error {
	return &contextfabric.FactReadFailure{
		State:  contextfabric.SourceUnavailable,
		Reason: "devhealthfacts: " + action + " failed",
	}
}

// The pre-CHAOS-3781 refusal (checkCurrentTimeOnly, timeUnsupportedReason)
// lived here. It returned SourceUnconfigured for every non-current axis,
// in every provider, because none of them had a historical query path.
// timebound.go replaces it with a per-provider answer: Tier A and Tier B
// providers now bound their queries and answer honestly, while Tier C
// providers -- whose facts have no recorded history -- still refuse, but
// as not_applicable rather than unconfigured. See timebound.go's package
// comment for the tier split and for why the observed_time axis degrades
// everywhere.

// requireOrgID validates principal.OrgID the same way every provider in this
// package must: org-scoping comes from storage.Principal, never from the
// query or any other caller-supplied value.
func requireOrgID(orgID string) (string, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return "", &contextfabric.FactReadFailure{State: contextfabric.SourceUnavailable, Reason: "devhealthfacts: authenticated organization is required"}
	}
	return orgID, nil
}

// subjectsOfKind filters subjects down to the ones matching kind, preserving
// order.
func subjectsOfKind(subjects []contextfabric.SubjectRef, kind contextfabric.SubjectKind) []contextfabric.SubjectRef {
	filtered := make([]contextfabric.SubjectRef, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Kind == kind {
			filtered = append(filtered, subject)
		}
	}
	return filtered
}

// subjectIndex strips prefix from each subject's CanonicalID to recover the
// raw ClickHouse row key devhealthsource itself would have produced (e.g.
// "work_item:WIDGET-101" -> "WIDGET-101"), and returns both the list of raw
// ids (for the query's IN clause -- so the query only ever asks ClickHouse
// about subjects the caller actually requested, never the whole org) and a
// lookup back to the exact SubjectRef the caller supplied. Reusing that
// SubjectRef verbatim (rather than reconstructing one from the scanned row)
// guarantees the fact's Subject is byte-for-byte the same value the
// registry already validated into its allowed set, Label included.
//
// A subject whose CanonicalID doesn't carry prefix is skipped, not errored:
// callers only ever receive subjects contextfabric.buildFactQuery already
// filtered to this provider's SupportedSubjectKinds, so this only guards
// against a malformed id, and skipping it just means that one subject gets
// no fact entry -- the same "partial coverage is fine" contract a zero-row
// query result gets.
func subjectIndex(subjects []contextfabric.SubjectRef, prefix string) (ids []string, bySubject map[string]contextfabric.SubjectRef) {
	bySubject = make(map[string]contextfabric.SubjectRef, len(subjects))
	for _, subject := range subjects {
		raw := strings.TrimPrefix(subject.CanonicalID, prefix)
		if raw == "" || raw == subject.CanonicalID {
			continue
		}
		bySubject[raw] = subject
		ids = append(ids, raw)
	}
	return ids, bySubject
}

// v2Index recovers this package's ClickHouse lookup key for a CHAOS-3898
// changed kind (identity.KindWorkItem, identity.KindCIPipelineRun,
// identity.KindDeployment, identity.KindPullRequestReview -- identity.
// KindProject is unused here; this package has no project reader) out of a
// "<kind>.v2:<repo_id>:...:<raw_id>" canonical id, via identity.Segments
// (the exact decode inverse of the identity.Derive call that minted it).
//
// Before CHAOS-3898, subjectIndex's plain TrimPrefix recovered the bare
// source-row id (e.g. "work_item:WIDGET-101" -> "WIDGET-101") because that
// bare id WAS the whole natural key these providers' ClickHouse queries
// filtered on. Once repo_id becomes a real segment of the id, the bare row
// id alone is no longer collision-safe across repos in the same org -- the
// exact defect design brief D-1..D-5 close at the graph-projection
// producers (internal/contextfabric/devhealthsource). v2Index closes the
// same class here: it returns repo_id and the raw id JOINED with ':' (a
// plain, unescaped composite -- these are Go-side map keys this package's
// own SQL also builds with concat(toString(repo_id), ':', ...), never a
// value that leaves this process, so no codec is needed on the join
// itself, only on recovering each segment FROM the canonical id). Callers
// scope their WHERE clause on the same composite so a row only matches the
// subject that actually named it.
//
// A subject whose CanonicalID doesn't parse as kind's "<kind>.v2:" form is
// skipped, not errored, matching subjectIndex's "omit rather than guess"
// contract for a malformed id.
func v2Index(subjects []contextfabric.SubjectRef, kind string) (ids []string, bySubject map[string]contextfabric.SubjectRef) {
	bySubject = make(map[string]contextfabric.SubjectRef, len(subjects))
	for _, subject := range subjects {
		segments, ok := identity.Segments(kind, subject.CanonicalID)
		if !ok || len(segments) < 2 {
			continue
		}
		repoID := segments[0]
		rawID := segments[len(segments)-1]
		if repoID == "" || rawID == "" {
			continue
		}
		key := repoID + ":" + rawID
		bySubject[key] = subject
		ids = append(ids, key)
	}
	return ids, bySubject
}

// pullRequestKey builds the same "repoID:number" composite row key
// devhealthsource/tables.go's queryPullRequests uses as its rowSortKey, so a
// git_pull_requests row (which has no single-column primary key) can be
// matched back to the subject that asked for it.
func pullRequestKey(repoID string, number int64) string {
	return fmt.Sprintf("%s:%d", repoID, number)
}

// evidenceRefID mirrors devhealthsource/clickhouse.go's inline
// "acr:v1:<entity-type>:<id>" evidence ref convention (e.g.
// queryWorkItems' `EvidenceRefIDs: []string{"acr:v1:work-item:" + workItemID}`)
// so evidence refs minted here resolve through the same source_evidence
// path as the ones devhealthsource already produces.
//
// entityType is CHAOS-4698's closed ContextFabricEvidenceEntityType
// vocabulary, registry-asserted against contextFabricEvidenceEntityLabels
// (internal/contracts/v1) -- a new segment cannot compile here without
// joining that registry in the same change.
func evidenceRefID(entityType contractsv1.ContextFabricEvidenceEntityType, id string) string {
	return contractsv1.EvidenceRefID(entityType, id)
}

// projectOwnershipJoinSQL returns the SQL join fragment resolving every
// requested project subject (matched by "<provider>:<id>", the same key
// v2Index(subjects, identity.KindProject) and metrics.go's readProjectMetrics
// both use) to its owning teams via projects -> team_project_ownership.
//
// This mirrors metrics.go's readProjectMetrics CTE structure exactly
// (CHAOS-4108 id-space fix, see that function's own doc comment): the join
// is on project_key, never team_project_ownership.project_id, and a
// project_key that is empty or resolves to more than one project in the org
// is OMITTED, never guessed. CHAOS-4363's investment/workload/readiness/
// health project rollups all reuse this fragment rather than re-deriving
// it, so the id-space fix cannot drift between producers the way two
// independently hand-authored copies could.
//
// Selects p.provider, p.id (so a caller can rebuild the "<provider>:<id>"
// project subject key) and p.team_id (one row per currently- or
// as-of-owning team; a team owning the project through more than one
// `source` row still yields one row per source and must be deduped by the
// caller the same way readProjectMetrics dedupes by team_id).
// projectOwnershipJoinSQL DELEGATES to dev-health-go's
// readers.ProjectOwnershipJoinSQL rather than restating it (CHAOS-4521b).
//
// It used to be a hand-maintained copy of the same join, and that copy is
// exactly the drift this repo's own AGENTS.md warns about: the two versions
// had to be changed together to move the join off project_key and onto the
// project identity, and nothing would have failed if only one of them had
// been. Now there is one definition and acr reads it.
//
// The signature is unchanged, so every caller here (health, landscape, and
// flow's own inline copy) keeps passing acr's factTimeBound-derived
// predicate string.
func projectOwnershipJoinSQL(ownershipPredicate string) string {
	return readers.ProjectOwnershipJoinSQL(ownershipPredicate)
}

// projectIdentityJoinSQL and projectIdentityMatchSQL are the same
// delegation for the project-subject resolution the WORK-SCOPE-keyed reads
// use -- a project's own rows, with no ownership hop (CHAOS-4521b). See
// readers.ProjectIdentityJoinSQL for why one resolution serves both.
func projectIdentityJoinSQL() string {
	return readers.ProjectIdentityJoinSQL()
}

func projectIdentityMatchSQL(alias, column string) string {
	return readers.ProjectIdentityMatchSQL(alias, column)
}

func ownershipValidityPredicate(timeBound factTimeBound) string {
	if timeBound.active {
		return fmt.Sprintf(" AND valid_from <= {%s:DateTime64(6,'UTC')} AND (valid_to IS NULL OR valid_to > {%s:DateTime64(6,'UTC')})", boundEndParam, boundEndParam)
	}
	return " AND valid_from <= now64(3) AND valid_to IS NULL"
}

// dedupeTeamRow reports whether teamID has already been seen in seenTeams,
// recording it if not. team_project_ownership's own ORDER BY key includes
// `source`, so the SAME team can legitimately appear more than once for one
// project (e.g. a native AND a manual ownership edge both current at once);
// every project-rollup provider in this package must dedupe by team_id
// before aggregating, or a team owning a project through two sources would
// be double-counted -- mirrors metrics.go's readProjectMetrics inline
// seenTeams map.
func dedupeTeamRow(seenTeams map[string]bool, teamID string) bool {
	if seenTeams[teamID] {
		return true
	}
	seenTeams[teamID] = true
	return false
}

// newCapability builds a FactCapability declaring dimension (CHAOS-4468 --
// see factKindDimensions for the FactKind -> HealthDimension table every
// call site in this package draws from) and defaulting SubjectRoles to
// {FactRoleSubject}: every provider in this package answers strictly for
// the subject it was asked about, never for a cohort member or group in
// its own right (that layer sits above devhealthfacts, in
// fact_planner.go/cohort_ranking.go). A caller that also emits a declared
// CHAOS-4633 table sets .Tables on the returned value before returning it.
func newCapability(kind contextfabric.FactKind, name string, subjectKinds []contextfabric.SubjectKind) contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind:                  kind,
		Name:                  name,
		Version:               QueryVersion,
		SupportedSubjectKinds: subjectKinds,
		RequiresEvidence:      true,
		Timeout:               defaultTimeout,
		Dimension:             factKindDimension(kind),
		Obligations:           factKindObligations(kind),
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
	}
}

// factKindDimension is the dimension↔FactKind half of CHAOS-4468's mapping
// (design doc §5.3): every FactKind this package registers a provider for
// maps to exactly one of the nine canonical HealthDimension values, chosen
// off what that provider's own doc comment says it measures. Asserted, not
// hand-trusted: TestFactKindDimensionMappingCoversEveryRegisteredProvider
// (chaos4633_dimension_mapping_test.go) fails CI if a registered FactKind
// is missing here or maps to an invalid dimension, which is what makes the
// GENERATED dimension -> FactKind -> ranking-family table CHAOS-4468 asks
// for possible: it is derived from this map plus the ranking-family
// registry, never hand-maintained beside them.
//
// FactEvidence has no entry: it is registered nowhere (no provider exists
// for it -- see acr/docs' own producer table), so it never reaches
// newCapability and asserting a dimension for it here would be asserting a
// property of code that does not exist.
func factKindDimension(kind contextfabric.FactKind) contextfabric.HealthDimension {
	switch kind {
	// data_trust: identity/membership resolution and ingestion health are
	// what make every OTHER dimension's facts trustworthy in the first
	// place -- they answer "can this org's data be believed", not a
	// delivery signal of their own.
	case contextfabric.FactIdentity, contextfabric.FactMembership, contextfabric.FactSourceHealth:
		return contextfabric.HealthDimensionDataTrust
	// execution_completion: work-item state, actual-completion evidence,
	// raw work counts, and estimate coverage (readiness.go: "how much of
	// this team's backlog is estimated") are all about whether committed
	// work is actually landing.
	case contextfabric.FactStatus, contextfabric.FactActualCompletion, contextfabric.FactWork, contextfabric.FactReadiness:
		return contextfabric.HealthDimensionExecutionCompletion
	// dependencies_and_blockers: named for exactly this dimension.
	case contextfabric.FactBlockers, contextfabric.FactRequiredChildren:
		return contextfabric.HealthDimensionDependenciesBlocked
	// review_and_ci_pressure: PR/review load and the CI signal itself.
	case contextfabric.FactPullRequests, contextfabric.FactReviews, contextfabric.FactContinuousIntegration:
		return contextfabric.HealthDimensionReviewCIPressure
	// reliability_and_release: deployments and incidents are the release
	// and operational-reliability signals.
	case contextfabric.FactDeployments, contextfabric.FactIncidents:
		return contextfabric.HealthDimensionReliabilityRelease
	// delivery_flow: metrics.go's daily commit/PR/cycle series and
	// flow.go's own item-flow/WIP series are this platform's two direct
	// delivery-flow producers.
	case contextfabric.FactMetrics, contextfabric.FactFlow:
		return contextfabric.HealthDimensionDeliveryFlow
	// code_ownership_risk: health.go's compounding_risk formula is
	// literally w_churn/w_complexity/w_ownership/w_review-weighted risk --
	// its own doc comment names ownership as one of the weighted inputs.
	case contextfabric.FactHealth:
		return contextfabric.HealthDimensionCodeOwnershipRisk
	// cognitive_workload_pressure: capacity forecasts (workload.go),
	// IC-level churn/cycle/WIP throughput scatter (landscape.go), and
	// fired operational-deficiency rules (deficiencies.go, whose own doc
	// comment cites a CHAOS/saturation rule) are all workload/pressure
	// signals, not delivery or risk signals in themselves.
	case contextfabric.FactWorkload, contextfabric.FactLandscape, contextfabric.FactOperationalDeficiencies:
		return contextfabric.HealthDimensionCognitiveWorkload
	// investment_balance: named for exactly this dimension.
	case contextfabric.FactInvestment:
		return contextfabric.HealthDimensionInvestmentBalance
	default:
		return ""
	}
}

// factKindObligations is the obligation half of the same declaration:
// which AnswerObligations each registered producer's facts can SERVE, per
// subject kind.
//
// It sits beside factKindDimension deliberately and is maintained the same
// way -- one auditable table in the package that owns the producers, with
// a registry test that fails CI when a registered kind has no entry. A
// provider cannot reach the registry without someone deciding what its
// facts establish.
//
// WHY THIS EXISTS AT ALL, stated once. The frozen design carried the
// inverse: a hand-written obligation -> FactKind seed picked from
// obligation NAMES. Executed against this registry it derived 22 empty
// requirement cells across the four acceptance questions and the composed
// cases -- most of them because `state` was seeded to `status`, which this
// package serves for work_item and nothing else. The mapping is declared
// HERE, by the package that knows what each query returns, and the seed is
// GENERATED by inverting it.
//
// WHY IT IS KEYED BY SUBJECT KIND. Measured, not chosen: a flat per-kind
// list diverged from the CHAOS-4347 composition in three cells --
// metrics claiming state for team and project, flow claiming it for
// repository. metrics.go emits a daily series for a REPOSITORY and no
// table at all for a team; flow.go is the reverse. What a producer's facts
// establish is a joint property of the producer and the subject kind, so
// the declaration is keyed the way Tables already is. The full measurement
// is in FactCapability.Obligations' own doc comment.
//
// ONLY READ OBLIGATIONS APPEAR. `ranking` and `count` are COMPUTED (a
// named server step over already-read facts or over the resolved set) and
// `evidence`/`coverage` are answer-contract obligations that read nothing;
// FactCapability.Validate rejects a producer declaring any of them. That
// rejection is round 4's N3 finding made structural: the frozen rule
// modelled `ranking` and `count` as reads needing a table shape no
// producer declares, which is why BAR Q2's ranking derived the empty set.
//
// A NIL RETURN IS A DECISION, NOT A GAP. FactMembership and
// FactSourceHealth are substrate: they make other producers' facts
// believable rather than answering an obligation themselves.
// FactSourceHealth matters most because it is the ONLY producer serving
// SubjectOrganization -- declaring `state` on it would build an org-wide
// "how are we doing" answer out of ingestion health, which is the
// mis-answer the design's own B5 row DEFERS rather than ships. It stays
// empty on purpose, so an organization state requirement derives an
// explicit `unavailable` instead of a confident wrong answer.
func factKindObligations(kind contextfabric.FactKind) map[contextfabric.SubjectKind][]contextfabric.AnswerObligation {
	// The composition family: these producers answer "how is this subject
	// doing" directly (state), explain it (principal_drivers) and can be
	// compared across periods (period_delta). trend_series is declared by
	// capability; the SEED GENERATOR decides for which subject kinds by
	// reading Tables, so declaring it here is a statement about the
	// producer, not about every subject kind it supports.
	compositionMember := func(extra ...contextfabric.AnswerObligation) []contextfabric.AnswerObligation {
		return append([]contextfabric.AnswerObligation{
			contextfabric.ObligationState,
			contextfabric.ObligationPrincipalDrivers,
			contextfabric.ObligationTrendSeries,
			contextfabric.ObligationPeriodDelta,
		}, extra...)
	}

	switch kind {
	// work_item state: StatusProvider reads work_items.status directly,
	// the one subject kind that HAS a discrete status column. CHAOS-4347
	// deliberately leaves work_item out of its composition because this
	// 1:1 mapping already answers it correctly.
	case contextfabric.FactStatus:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectWorkItem: {contextfabric.ObligationState},
		}
	case contextfabric.FactActualCompletion:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectWorkItem: {contextfabric.ObligationCompletion},
		}
	// Raw work counts answer both how much landed and how much is left.
	case contextfabric.FactWork:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectWorkItem: {contextfabric.ObligationCompletion, contextfabric.ObligationRemainingWork},
		}
	// Blockers and unmet required children ARE the remaining work in the
	// dependencies_and_blockers sense (design table 3's own row).
	case contextfabric.FactBlockers, contextfabric.FactRequiredChildren:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectWorkItem: {contextfabric.ObligationRemainingWork},
		}
	// Identity is the CHAOS-4347 REPOSITORY composition's third member:
	// for a repository, "who owns and identifies this" is part of the
	// closest thing to a current state. It is NOT a driver -- it explains
	// nothing about how the subject is doing -- and it is NOT work_item
	// state, which FactStatus already answers exactly.
	case contextfabric.FactIdentity:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectRepository: {contextfabric.ObligationState},
		}
	// Substrate -- see the doc comment above.
	case contextfabric.FactMembership, contextfabric.FactSourceHealth:
		return nil
	// Review and CI pressure explain why delivery looks the way it does;
	// neither is a state reading of the subject in its own right.
	case contextfabric.FactPullRequests:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectPullRequest: {contextfabric.ObligationPrincipalDrivers},
		}
	case contextfabric.FactReviews:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contractsv1.ContextFabricSubjectPullRequestReview: {contextfabric.ObligationPrincipalDrivers},
		}
	case contextfabric.FactContinuousIntegration:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contractsv1.ContextFabricSubjectCIRun: {contextfabric.ObligationPrincipalDrivers},
			contextfabric.SubjectRepository:       {contextfabric.ObligationPrincipalDrivers},
		}
	// Deployment history is a REPOSITORY's release-readiness signal.
	//
	// It is deliberately NOT declared as readiness for a `deployment`
	// subject, and the distinction is the point of keying by subject kind:
	// "is this ready to release" is a question about the thing being
	// released, not about a release that already happened. A deployment
	// subject's own facts explain what happened (a driver); they do not
	// establish its readiness, and declaring that they do would put a
	// category error into the plan where it would read as coverage.
	case contextfabric.FactDeployments:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectDeployment: {contextfabric.ObligationPrincipalDrivers},
			contextfabric.SubjectRepository: {contextfabric.ObligationReadiness, contextfabric.ObligationPrincipalDrivers},
		}
	// Same reasoning, and here it leaves the producer with nothing to
	// serve `readiness` for at all: IncidentsProvider supports only the
	// `incident` subject kind, and an incident's readiness is not a
	// question. Incidents EXPLAIN reliability -- they are a driver. The
	// `readiness` obligation on a team or project is served by the
	// readiness producer, not by this one.
	case contextfabric.FactIncidents:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectIncident: {contextfabric.ObligationPrincipalDrivers},
		}
	// Fired operational-deficiency rules explain pressure: a driver, never
	// a state.
	case contextfabric.FactOperationalDeficiencies:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectTeam: {contextfabric.ObligationPrincipalDrivers},
		}
	// health.go serves all three of CHAOS-4347's rows -- it is the only
	// producer in both the repository set and the team/project set -- and
	// it is the named producer for the `health` obligation itself.
	case contextfabric.FactHealth:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectRepository: compositionMember(contextfabric.ObligationHealth),
			contextfabric.SubjectTeam:       compositionMember(contextfabric.ObligationHealth),
			contextfabric.SubjectProject:    compositionMember(contextfabric.ObligationHealth),
		}
	case contextfabric.FactWorkload:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectTeam:    compositionMember(),
			contextfabric.SubjectProject: compositionMember(),
		}
	// flow.go supports repository but is a team/project composition member
	// ONLY. It emits no repository table at all, and CHAOS-4347's
	// repository row excludes it: a repository's "state" is served by
	// metrics/health/identity, not by an item-flow series it never emits
	// for that kind.
	case contextfabric.FactFlow:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectTeam:    compositionMember(),
			contextfabric.SubjectProject: compositionMember(),
		}
	// readiness.go measures estimate coverage -- "how much of this team's
	// backlog is estimated" -- so it serves the readiness obligation by
	// name as well as being a composition member.
	case contextfabric.FactReadiness:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectTeam:    compositionMember(contextfabric.ObligationReadiness),
			contextfabric.SubjectProject: compositionMember(contextfabric.ObligationReadiness),
		}
	// investment.go emits Rows, not just a scalar, so it is the producer
	// for allocation_breakdown -- for whichever subject kinds it actually
	// declares a breakdown table, which the generator reads from Tables.
	// Declaring it for team as well as project is deliberate and is the
	// open producer question: the declaration says what investment MEANS
	// for a team, and Tables says whether it can currently emit it.
	case contextfabric.FactInvestment:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectTeam:    compositionMember(contextfabric.ObligationAllocationBreakdown),
			contextfabric.SubjectProject: compositionMember(contextfabric.ObligationAllocationBreakdown),
		}
	// landscape.go's IC-level throughput scatter is a team/project state
	// reading and a driver. It is deliberately NOT the producer for
	// `count`: the frozen seed mapped count -> landscape, but a count is a
	// CARDINALITY over the resolved or discovered set -- a server step,
	// not a fact read -- which is why that seed derived nothing at a
	// repository anchor or over the organization. It emits no time series,
	// so it declares no trend.
	case contextfabric.FactLandscape:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectTeam:    {contextfabric.ObligationState, contextfabric.ObligationPrincipalDrivers, contextfabric.ObligationPeriodDelta},
			contextfabric.SubjectProject: {contextfabric.ObligationState, contextfabric.ObligationPrincipalDrivers, contextfabric.ObligationPeriodDelta},
		}
	// metrics.go is the mirror image of flow.go: its daily commit/PR/cycle
	// series IS a repository state reading (CHAOS-4347's repository row
	// names it), while for a team it emits no table and the composition
	// excludes it. It stays a delivery-flow DRIVER for team and project,
	// which is a different claim from being their state.
	case contextfabric.FactMetrics:
		return map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
			contextfabric.SubjectRepository: compositionMember(),
			contextfabric.SubjectTeam: {
				contextfabric.ObligationPrincipalDrivers,
				contextfabric.ObligationTrendSeries,
				contextfabric.ObligationPeriodDelta,
			},
			contextfabric.SubjectProject: {
				contextfabric.ObligationPrincipalDrivers,
				contextfabric.ObligationTrendSeries,
				contextfabric.ObligationPeriodDelta,
			},
		}
	default:
		return nil
	}
}

// capFactValueRows truncates rows to contextfabric.MaxFactValueRows and
// reports how many were dropped (CHAOS-4364). contextfabric.FactValue.Validate
// rejects a Rows table over that bound OUTRIGHT -- it does not truncate --
// so a provider building a rollup whose row count depends on live data
// (e.g. team_count * map_count) must cap it here, before constructing the
// CanonicalFact, or a large-fanout subject fails its whole fact read
// instead of returning a bounded, disclosed result. Truncation keeps the
// FIRST rows in the caller's own deterministic order (never re-sorted),
// consistent with this package's "never re-derive order from a map" rule.
//
// CHAOS-4363 independently added an identically-named helper backed by its
// own unexported maxProjectRollupBreakdownRows=64 constant, for the same
// reason (its four project-rollup providers' team_breakdown/risk_breakdown
// Rows tables). Hand-merged on rebase onto this one, the earlier-exported
// version: investment.go/workload.go/readiness.go/health.go now call this
// (capped, omitted int) form rather than duplicate the constant.
func capFactValueRows(rows []contextfabric.FactValueRow) (capped []contextfabric.FactValueRow, omitted int) {
	if len(rows) <= contextfabric.MaxFactValueRows {
		return rows, 0
	}
	return rows[:contextfabric.MaxFactValueRows], len(rows) - contextfabric.MaxFactValueRows
}

// factValueRowContentBytes converts row to the wire row type and delegates
// to contracts/v1's ClaimedFactRowContentBytes -- the ONE json.Marshal-based
// measurement in the repository, per chris's ruling (via team-lead) on
// codex terra xhigh round 2's finding: an earlier version of this function
// carried its OWN copy of the measurement (first a raw-string-length sum,
// round-1-fixed only in contracts/v1's sibling; then briefly a second
// json.Marshal call here), and "two copies is the defect class, not the
// bug" -- a second implementation can drift out of sync with the first
// again regardless of how correct it is today. This function converts
// (never re-measures) so there is structurally only one place the
// arithmetic can be wrong.
func factValueRowContentBytes(row contextfabric.FactValueRow) int {
	return contractsv1.ClaimedFactRowContentBytes(claimedFactRowFromFactValueRow(row))
}

// claimedFactRowFromFactValueRow converts devhealthfacts' domain row type to
// the wire row type contracts/v1 measures -- a value-only conversion, never
// a re-implementation of any bound/measurement logic. contextfabric.
// FactValue and ContextFabricScalarValue share the identical leaf-variant
// shape (String/Integer/Number/Boolean/Null); Rows/Table are deliberately
// NOT copied here because a row's own Fields entries are always leaves
// (contextfabric.FactValueRow's own doc comment: "A row field must never
// itself carry Rows").
func claimedFactRowFromFactValueRow(row contextfabric.FactValueRow) contractsv1.ContextFabricClaimedFactRow {
	fields := make(map[string]contractsv1.ContextFabricScalarValue, len(row.Fields))
	for key, value := range row.Fields {
		fields[key] = contractsv1.ContextFabricScalarValue{
			String: value.String, Integer: value.Integer, Number: value.Number,
			Boolean: value.Boolean, Null: value.Null,
		}
	}
	return contractsv1.ContextFabricClaimedFactRow{Fields: fields}
}

// combinedRowsExceedBytesBound reports whether legacyRows (a producer's
// existing breakdown/ranking table, already destined for a fact's Fields
// map) COMBINED with timeSeriesRows (the CHAOS-4645/4682 additive time
// series about to be attached to the SAME fact) would violate CHAOS-4785's
// joint Rows+TimeSeriesRows bound -- the identical arithmetic
// internal/contracts/v1's validateClaimedFactRowsCombined enforces at the
// write path (contractsv1.ContextFabricClaimedFactCombinedCellsMax /
// ContextFabricClaimedFactCombinedContentBytesMax), checked here BEFORE
// construction so a producer can degrade (drop the additive time series,
// disclose why via recordFactBytesBoundExceeded) instead of ever handing
// the validator a fact it must reject outright.
//
// No producer has come close in measured real data (kiac/dh_0830 org
// 70d529e0, 2026-09-02: largest observed combined fact 16,246 bytes,
// against a 262,144-byte bound) -- this is deliberate defense in depth,
// not a response to an observed failure.
func combinedRowsExceedBytesBound(legacyRows, timeSeriesRows []contextfabric.FactValueRow) bool {
	if len(legacyRows) == 0 || len(timeSeriesRows) == 0 {
		// Mirrors internal/contracts/v1's validateClaimedFactRowsCombined
		// gate: the bound applies to the COMBINATION only. A single table
		// alone -- however large -- stays governed by capFactValueRows'
		// own pre-existing per-table cap, unchanged.
		return false
	}
	cells, contentBytes := 0, 0
	for _, row := range legacyRows {
		cells += len(row.Fields)
		contentBytes += factValueRowContentBytes(row)
	}
	for _, row := range timeSeriesRows {
		cells += len(row.Fields)
		contentBytes += factValueRowContentBytes(row)
	}
	return cells > contractsv1.ContextFabricClaimedFactCombinedCellsMax ||
		contentBytes > contractsv1.ContextFabricClaimedFactCombinedContentBytesMax
}

// recordFactBytesBoundExceeded emits the CHAOS-4785 decision-basis
// telemetry line for a producer that dropped an additive time series
// rather than hand the write-path validator an over-bound dual-table fact.
// producer and kind are both closed, low-cardinality vocabulary (this
// package's own domain name -- "flow", "health", "readiness", "workload",
// "metrics" -- and the fact's contextfabric.FactKind), matching this
// repository's telemetry convention (AGENTS.md: "Structured logging uses
// log/slog"); never a request id, subject label, or row content.
func recordFactBytesBoundExceeded(producer string, kind contextfabric.FactKind) {
	slog.Warn("context_fabric_fact_bytes_bound_exceeded",
		"producer", producer,
		"kind", string(kind),
		"combined_cells_max", contractsv1.ContextFabricClaimedFactCombinedCellsMax,
		"combined_bytes_max", contractsv1.ContextFabricClaimedFactCombinedContentBytesMax,
	)
}

// factBytesBoundExceededReason is the CLOSED-vocabulary token a caller
// discloses on a fact whose additive time series was dropped for
// CHAOS-4785 -- the SAME token recordFactBytesBoundExceeded's telemetry
// event uses, so a log line and a served answer name the identical cause.
const factBytesBoundExceededReason = "fact_bytes_bound_exceeded"

// disclosedDualTableDrop is CHAOS-4785's disclosure contract: a producer
// dropping timeSeriesRows because it combined with legacyRows past the
// joint bound must NEVER do so silently (chris's ruling: fail-closed means
// the fact is reported as unavailable-class coverage, never silently
// dropped -- a log line alone is not disclosure). drop=true means the
// caller must (1) fold droppedRows into ITS OWN omitted-rows accounting,
// which flows to FactProviderResult.Truncated/OmittedCount -- the SAME
// existing mechanism capFactValueRows' row-cap truncation already degrades
// coverage through -- and (2) set a closed-vocabulary reason field on the
// fact using reason, so a reader can tell THIS cause apart from an
// ordinary row-cap truncation. drop=false (the bound is not exceeded, or
// either table is empty -- see combinedRowsExceedBytesBound) means the
// caller proceeds exactly as before this ticket.
//
// preCapOmitted is the count of rows a caller's OWN earlier cap (e.g.
// capFactValueRows, inside flowDailyTable/healthDailyTable/etc.) already
// removed from timeSeriesRows BEFORE it was even passed in here -- those
// rows never appear in len(timeSeriesRows), so droppedRows must add them
// back in, not just count what survived the earlier cap (codex terra
// xhigh round 1, P2, EXECUTED: an earlier version of this function took
// no preCapOmitted parameter, and both flow.go call sites forgot to add
// their own dailyOmitted in separately -- a 70-row series capped to 64
// then fully dropped here reported OmittedCount contribution 64, not the
// true 70). Folding it into THIS function's own return value, rather than
// leaving it to every call site to remember, is deliberate: a caller
// cannot construct the reason string without also supplying the count it
// belongs with.
func disclosedDualTableDrop(producer string, kind contextfabric.FactKind, legacyRows, timeSeriesRows []contextfabric.FactValueRow, preCapOmitted int) (drop bool, droppedRows int, reason string) {
	if !combinedRowsExceedBytesBound(legacyRows, timeSeriesRows) {
		return false, 0, ""
	}
	recordFactBytesBoundExceeded(producer, kind)
	return true, len(timeSeriesRows) + preCapOmitted, factBytesBoundExceededReason
}

func stringOrNull(value string) contextfabric.FactValue {
	if value == "" {
		return contextfabric.NullFactValue()
	}
	return contextfabric.StringFactValue(value)
}

// teamScopedProjectReason explains a zero-row project read on a source that
// has no project dimension of its own (CHAOS-4521b).
//
// compounding_risk_daily (scope='repo'/'team'), investment_metrics_daily
// (repo_id/team_id) and ic_landscape_rolling_30d (repo_id/identity_id/
// team_id) carry no project column, so a project reaches them ONLY through
// team ownership. When that ownership resolves nothing the read is empty --
// and PR-A's generic "the source was reached and held no rows" is true but
// unhelpful, because it reads as "this project has no health", when what
// happened is that the question could not be routed to the project at all.
//
// It deliberately stops at the ROUTING and claims nothing about the
// outcome (codex P2). An earlier wording ended "...which resolved no owning
// team", which the read cannot know: SourceNoData is equally consistent
// with ownership resolving teams whose metric rows were simply absent.
// Asserting the stronger of two indistinguishable causes is the same
// failure class as CHAOS-4521 itself -- a coverage field claiming more than
// the read observed.
//
// A fixed literal, never interpolating a subject or a count.
const teamScopedProjectReason = "devhealthfacts: this source is team-scoped and carries no project dimension; a project reaches it only through team ownership"

// explainTeamScopedProjectAbsence narrows an empty read's reason when every
// requested subject was a project and the capability could only have
// answered through the ownership hop.
//
// Deliberately conditioned on ALL subjects being projects: a mixed read
// whose repository half legitimately held no rows must not be relabelled
// with an ownership explanation that does not apply to it.
func explainTeamScopedProjectAbsence(bound factTimeBound, state contextfabric.SourceState, reason string, subjects []contextfabric.SubjectRef) string {
	if state != contextfabric.SourceNoData || len(subjects) == 0 {
		return reason
	}
	// A HISTORICAL empty read already has a more specific reason than this
	// one: outOfRetentionReason says the window may predate the retained
	// corpus, which is a statement about TIME, not routing (codex P2).
	// Overwriting it would erase the distinction §19.8.3 exists to draw and
	// replace a true reason with a less true one.
	if bound.active {
		return reason
	}
	for _, subject := range subjects {
		if subject.Kind != contextfabric.SubjectProject {
			return reason
		}
	}
	return teamScopedProjectReason
}

// teamIDOrNull renders a project rollup row's team id, or NULL when the
// source row carried no team at all (CHAOS-4521b).
//
// The distinction matters downstream: a null team_id reads as "this
// measurement is not attributed to a team", while "" reads as a team whose
// id is the empty string -- a team that does not exist. The same row is
// also excluded from team_count and mints no team evidence ref, so the
// three surfaces agree.
func teamIDOrNull(hasTeam uint8, teamID string) contextfabric.FactValue {
	if hasTeam == 0 {
		return contextfabric.NullFactValue()
	}
	return contextfabric.StringFactValue(teamID)
}
