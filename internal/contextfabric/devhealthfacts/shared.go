package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
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
func evidenceRefID(entityType, id string) string {
	return "acr:v1:" + entityType + ":" + id
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

func newCapability(kind contextfabric.FactKind, name string, subjectKinds []contextfabric.SubjectKind) contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind:                  kind,
		Name:                  name,
		Version:               QueryVersion,
		SupportedSubjectKinds: subjectKinds,
		RequiresEvidence:      true,
		Timeout:               defaultTimeout,
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
