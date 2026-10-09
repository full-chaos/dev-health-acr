package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Field declarations for direct fact reads (CHAOS-7073). factKindFields
// returns what a fact of one kind may carry: every scalar field and table
// column its provider emits, with type, nullability, unit, and which values
// name ANOTHER subject (those go through the subject gate before a direct
// read leaves acr-api).
//
// Every registered kind has an entry here (CHAOS-7120 declared the twelve
// entity kinds); a kind with none is not served by the direct read tools,
// and chaos7073_field_declarations_test.go fails on it.
// The catalogue-truth test drives every provider and fails when a provider
// emits a field or column that is not declared here, or when a declaration
// is never emitted.
//
// Conventions:
//   - Aggregate marks a scalar computed over every repository or team a team
//     or project subject reaches (decision K2).
//   - Score marks a judged value (decision K7); its DriversTable names the
//     declared table that explains it.
//   - A subject reference on a column applies to every row of the table; a
//     label column (team_name, scope_name) travels with its reference column
//     in the same row and declares none.

var (
	declTeamOnly         = []contextfabric.SubjectKind{contextfabric.SubjectTeam}
	declProjectOnly      = []contextfabric.SubjectKind{contextfabric.SubjectProject}
	declRepositoryOnly   = []contextfabric.SubjectKind{contextfabric.SubjectRepository}
	declTeamProject      = []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject}
	declOrganizationOnly = []contextfabric.SubjectKind{contextfabric.SubjectOrganization}
	declRepoTeamOrg      = []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectOrganization}
	declRepoTeam         = []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam}
	declWorkItemOnly     = []contextfabric.SubjectKind{contextfabric.SubjectWorkItem}
	declCIRunOnly        = []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectCIRun}
	declDeploymentOnly   = []contextfabric.SubjectKind{contextfabric.SubjectDeployment}
	declIncidentOnly     = []contextfabric.SubjectKind{contextfabric.SubjectIncident}
	declPullRequestOnly  = []contextfabric.SubjectKind{contextfabric.SubjectPullRequest}
)

type fieldDecl = contextfabric.FactFieldDeclaration
type columnDecl = contextfabric.FactColumnDeclaration

func fStr(name string) fieldDecl { return fieldDecl{Name: name, Type: contextfabric.FactFieldString} }
func fInt(name, unit string) fieldDecl {
	return fieldDecl{Name: name, Type: contextfabric.FactFieldInteger, Unit: unit}
}
func fNum(name, unit string) fieldDecl {
	return fieldDecl{Name: name, Type: contextfabric.FactFieldNumber, Unit: unit}
}
func fBool(name string) fieldDecl { return fieldDecl{Name: name, Type: contextfabric.FactFieldBoolean} }

func fTable(name string, columns ...columnDecl) fieldDecl {
	return fieldDecl{Name: name, Type: contextfabric.FactFieldTable, Columns: columns}
}

func declNullable(d fieldDecl) fieldDecl { d.Nullable = true; return d }
func declFresh(d fieldDecl) fieldDecl    { d.Freshness = true; return d }

// declCallerScoped marks a count computed over the caller's authorized items
// only; it is served with a population-scope label.
func declCallerScoped(d fieldDecl) fieldDecl { d.CallerScoped = true; return d }

func declAggregate(d fieldDecl) fieldDecl {
	d.Aggregate = true
	return d
}
func declScore(drivers string, d fieldDecl) fieldDecl {
	d.Score = true
	d.DriversTable = drivers
	return d
}
func declRef(ref *contextfabric.FactSubjectRefDeclaration, d fieldDecl) fieldDecl {
	d.SubjectRef = ref
	return d
}

// on limits every declaration to the given subject kinds.
func declOn(kinds []contextfabric.SubjectKind, fields ...fieldDecl) []fieldDecl {
	out := make([]fieldDecl, len(fields))
	for i, field := range fields {
		field.SubjectKinds = kinds
		out[i] = field
	}
	return out
}

// fDaily declares a table field that is one row per day of one subject.
func fDaily(name string, columns ...columnDecl) fieldDecl {
	field := fTable(name, columns...)
	field.DailySeries = true
	return field
}

// cAdd classifies a daily count column additive: its sum over days is the
// count over the period. cNon classifies one that is not (a ratio, an
// average, a percentile, a gauge or a state).
func cAdd(c columnDecl) columnDecl { c.Additivity = contextfabric.FactAdditive; return c }
func cNon(c columnDecl) columnDecl { c.Additivity = contextfabric.FactNonAdditive; return c }

func cStr(name string) columnDecl { return columnDecl{Name: name, Type: contextfabric.FactFieldString} }
func cInt(name, unit string) columnDecl {
	return columnDecl{Name: name, Type: contextfabric.FactFieldInteger, Unit: unit}
}
func cNum(name, unit string) columnDecl {
	return columnDecl{Name: name, Type: contextfabric.FactFieldNumber, Unit: unit}
}
func cBool(name string) columnDecl {
	return columnDecl{Name: name, Type: contextfabric.FactFieldBoolean}
}
func cNullable(c columnDecl) columnDecl { c.Nullable = true; return c }
func cRef(ref *contextfabric.FactSubjectRefDeclaration, c columnDecl) columnDecl {
	c.SubjectRef = ref
	return c
}

var (
	// declTeamRef: a team id column ("team:"+id is the stored canonical id).
	declTeamRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectTeam, IDForm: contextfabric.FactSubjectIDTeamID}
	// declWorkScopeRef: a provider work scope (work_items.project_id), not a graph
	// id; the gate cannot resolve it, so it is opaque.
	declWorkScopeRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectProject, IDForm: contextfabric.FactSubjectIDOpaque}
	// declRepositoryRef: a repos.id uuid ("repository:"+uuid is the stored
	// canonical id).
	declRepositoryRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectRepository, IDForm: contextfabric.FactSubjectIDRepositoryUUID}
	// declUnresolvedHandleRef: a reference no repository of the organization
	// resolves; the gate cannot decide it, so it is opaque.
	declUnresolvedHandleRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectRepository, IDForm: contextfabric.FactSubjectIDOpaque}
	// declOrganizationRef: an organization id; the gate admits only the
	// caller's own organization.
	declOrganizationRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectOrganization, IDForm: contextfabric.FactSubjectIDCanonical}
	// declWorkItemRef: a stored work item canonical id
	// ("work_item.v2:<repo_id>:<work_item_id>"); the gate decides it exactly
	// as it decides a work item root.
	declWorkItemRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectWorkItem, IDForm: contextfabric.FactSubjectIDCanonical}
	// declBareWorkItemRef: a bare work_items.work_item_id. It carries no
	// repository and is not unique across repositories, so no canonical id
	// can be derived from the value alone: opaque (withheld for a
	// repository-restricted caller). The same row carries the canonical id
	// in a declWorkItemRef field when the provider could resolve it.
	declBareWorkItemRef = &contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectWorkItem, IDForm: contextfabric.FactSubjectIDOpaque}
	// declHealthScopeRef: risk_breakdown.scope_id names a team or a repository
	// depending on the row's scope column.
	declHealthScopeRef = &contextfabric.FactSubjectRefDeclaration{
		KindColumn: "scope",
		KindByValue: map[string]contextfabric.SubjectKind{
			"repo": contextfabric.SubjectRepository,
			"team": contextfabric.SubjectTeam,
		},
		IDForm:     contextfabric.FactSubjectIDTeamID,
		FormByKind: map[contextfabric.SubjectKind]contextfabric.FactSubjectIDForm{contextfabric.SubjectRepository: contextfabric.FactSubjectIDRepositoryUUID},
	}
)

// factKindFields returns the field declarations of kind, or nil when the kind
// is not served by the direct read tools.
func factKindFields(kind contextfabric.FactKind) []contextfabric.FactFieldDeclaration {
	switch kind {
	case contextfabric.FactInvestment:
		return investmentFields()
	case contextfabric.FactHealth:
		return healthFields()
	case contextfabric.FactMetrics:
		return metricsFields()
	case contextfabric.FactFlow:
		return flowFields()
	case contextfabric.FactReadiness:
		return readinessFields()
	case contextfabric.FactWorkload:
		return workloadFields()
	case contextfabric.FactLandscape:
		return landscapeFields()
	case contextfabric.FactOperationalDeficiencies:
		return deficiencyFields()
	case contextfabric.FactSourceHealth:
		return sourceHealthFields()
	case contextfabric.FactIdentity:
		return identityFields()
	case contextfabric.FactMembership:
		return membershipFields()
	case contextfabric.FactStatus:
		return statusFields()
	case contextfabric.FactWork:
		return workFields()
	case contextfabric.FactActualCompletion:
		return actualCompletionFields()
	case contextfabric.FactBlockers:
		return blockersFields()
	case contextfabric.FactRequiredChildren:
		return requiredChildrenFields()
	case contextfabric.FactPullRequests:
		return pullRequestFields()
	case contextfabric.FactReviews:
		return reviewFields()
	case contextfabric.FactContinuousIntegration:
		return continuousIntegrationFields()
	case contextfabric.FactDeployments:
		return deploymentFields()
	case contextfabric.FactIncidents:
		return incidentFields()
	default:
		return nil
	}
}

func declJoin(groups ...[]fieldDecl) []fieldDecl {
	var out []fieldDecl
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// ---- entity kinds (CHAOS-7120). Each fact is about one admitted root; its
// evidence reference is mapped to the root's canonical id by the embedded
// gate (directread/embedded_subject_gate.go resolveEvidence).

func identityFields() []fieldDecl {
	return declJoin(
		// id is the subject's own repos.id; declared as a reference so the
		// gate checks it against the admitted root rather than trusting it.
		declOn(declRepositoryOnly, declRef(declRepositoryRef, fStr("id")), declNullable(fStr("name")), fStr("provider")),
		// id is the subject's own bare work_item_id (the provider matches the
		// row to the subject on repo_id + work_item_id), not another subject.
		declOn(declWorkItemOnly, fStr("id"), declNullable(fStr("title"))),
	)
}

func membershipFields() []fieldDecl {
	return declJoin(
		declOn(declRepositoryOnly, declRef(declOrganizationRef, fStr("organization_id"))),
		// repository_id is the work item's own repository. A repo-less item
		// (the zero uuid) or an orphaned one has no repository node, so the
		// gate withholds the id for every caller; repository_name is then
		// null or the orphan's last slug.
		declOn(declWorkItemOnly, declRef(declRepositoryRef, fStr("repository_id")), declNullable(fStr("repository_name"))),
	)
}

func statusFields() []fieldDecl {
	return []fieldDecl{
		declNullable(fStr("status")),
		fStr("status_basis"),
		declNullable(fBool("status_in_vocabulary")),
		fStr("status_provenance"),
	}
}

func workFields() []fieldDecl { return []fieldDecl{declNullable(fStr("title"))} }

func actualCompletionFields() []fieldDecl {
	return declJoin(
		// completed_at is the event time of the completion, not freshness.
		declOn(declWorkItemOnly, fBool("completed"), fStr("completed_at")),
		// The project counts are computed over the work items the CALLER is
		// authorized for (the provider applies the work-item authorization
		// rule in SQL), not over every item the project reaches: they are
		// not Aggregate, and a restricted caller gets its own population.
		// They are CallerScoped: served with population_scope
		// "caller_authorized_items" so a client never reads a restricted
		// caller's subset as the project-wide ratio (codex r1 P1).
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			fStr("member_kind"),
			declCallerScoped(fInt("work_item_count", "count")),
			declCallerScoped(fInt("cancelled_count", "count")),
			declCallerScoped(fInt("unknown_status_count", "count")),
			declCallerScoped(fInt("counted_work_items", "count")),
			declCallerScoped(fInt("completed_count", "count")),
			declCallerScoped(fNum("completion_ratio", "ratio")),
			fStr("archived_items"),
		),
	)
}

func blockersFields() []fieldDecl {
	return declJoin(
		declOn(declWorkItemOnly,
			declRef(declBareWorkItemRef, fStr("blocked_by_work_item_id")),
			// Emitted only when the blocking item resolves to exactly one
			// repository; gated like a work item root.
			declRef(declWorkItemRef, fStr("blocked_by_work_item_ref")),
		),
		teamRollupCommonFields(false),
		// Current axis only: a dependency row carries no event time, so the
		// counts name no window. Both ends pass the work-item authorization
		// rule, so they are the caller's authorized population.
		declOn(declTeamOnly,
			declCallerScoped(fInt("blocked_work_items_current", "count")),
			declCallerScoped(fInt("blocker_dependencies_current", "count")),
			fTable("repository_breakdown",
				cRef(declRepositoryRef, cStr("repository_id")),
				cNullable(cStr("repository_name")),
				cInt("blocked_work_items_current", "count"),
				cInt("blocker_dependencies_current", "count"),
			),
		),
	)
}

// teamRollupCommonFields are the fields every team rollup of deployments,
// incidents, pull requests and blockers carries: the basis, the owned
// repository pointer and counts, and (windowed kinds) the echoed window.
func teamRollupCommonFields(windowed bool) []fieldDecl {
	fields := []fieldDecl{
		fStr("rollup_basis"),
		declAggregate(fInt("owned_repository_count", "count")),
		declAggregate(fInt("repositories_with_data_count", "count")),
		declAggregate(fInt("repositories_without_data_count", "count")),
		// The pointer: each row is a repository subject reference the gate
		// checks, so a client can continue per repository.
		fTable("owned_repositories",
			cRef(declRepositoryRef, cStr("repository_id")),
			cNullable(cStr("repository_name")),
		),
		fInt("owned_repositories_omitted_count", "count"),
		fInt("repository_breakdown_omitted_count", "count"),
	}
	if windowed {
		fields = append(fields, fStr("window_basis"), fStr("window_start"), fStr("window_end"))
	}
	return declOn(declTeamOnly, fields...)
}

func requiredChildrenFields() []fieldDecl {
	return []fieldDecl{
		declRef(declBareWorkItemRef, fStr("required_child_work_item_id")),
		// Emitted only when the child resolves to exactly one repository;
		// gated like a work item root.
		declRef(declWorkItemRef, fStr("required_child_work_item_ref")),
		fStr("relationship_type"),
	}
}

func pullRequestFields() []fieldDecl {
	return declJoin(
		declOn(declPullRequestOnly, declNullable(fStr("state"))),
		teamRollupCommonFields(true),
		// Counted by each pull request's own event time inside the window.
		declOn(declTeamOnly,
			declAggregate(fInt("pull_requests_opened_window", "count")),
			declAggregate(fInt("pull_requests_merged_window", "count")),
			declAggregate(fInt("pull_requests_closed_unmerged_window", "count")),
			fTable("repository_breakdown",
				cRef(declRepositoryRef, cStr("repository_id")),
				cNullable(cStr("repository_name")),
				cInt("pull_requests_opened_window", "count"),
				cInt("pull_requests_merged_window", "count"),
				cInt("pull_requests_closed_unmerged_window", "count"),
			),
		),
	)
}

func reviewFields() []fieldDecl { return []fieldDecl{declNullable(fStr("state"))} }

func continuousIntegrationFields() []fieldDecl {
	return declJoin(
		declOn(declCIRunOnly, declNullable(fStr("status"))),
		// success_rate is a raw ratio, not a judged score.
		declOn(declRepositoryOnly,
			declFresh(fStr("day")),
			fInt("pipelines_count", "count"),
			fNum("success_rate", "ratio"),
			fNum("avg_duration_minutes", "minutes"),
			fNum("p90_duration_minutes", "minutes"),
			fNum("avg_queue_minutes", "minutes"),
		),
	)
}

func deploymentFields() []fieldDecl {
	return declJoin(
		// environment is a provider label, not a subject reference.
		declOn(declDeploymentOnly, declNullable(fStr("status")), fStr("environment")),
		declOn(declRepositoryOnly,
			declFresh(fStr("day")),
			fInt("deployments_count", "count"),
			fInt("failed_deployments_count", "count"),
			fNum("deploy_time_p50_hours", "hours"),
			fNum("lead_time_p50_hours", "hours"),
		),
		teamRollupCommonFields(true),
		// Totals over the window's daily rows, summed across the owned
		// repositories; percentiles are per repository at its latest day in
		// the window and are never summed or averaged.
		declOn(declTeamOnly,
			declAggregate(fInt("deployments_count_window", "count")),
			declAggregate(fInt("failed_deployments_count_window", "count")),
			fTable("repository_breakdown",
				cRef(declRepositoryRef, cStr("repository_id")),
				cNullable(cStr("repository_name")),
				cInt("deployments_count_window", "count"),
				cInt("failed_deployments_count_window", "count"),
				cInt("days_with_data_window", "count"),
				cStr("latest_day"),
				cNullable(cNum("deploy_time_p50_hours_latest_day", "hours")),
				cNullable(cNum("lead_time_p50_hours_latest_day", "hours")),
			),
		),
	)
}

func incidentFields() []fieldDecl {
	// severity is the source's own label, not a computed score (the same
	// ruling as operational_deficiencies.severity); it is omitted on a
	// historical read.
	return declJoin(
		declOn(declIncidentOnly, declNullable(fStr("status")), fStr("severity")),
		teamRollupCommonFields(true),
		// operational_incidents carries no repository: an incident reaches a
		// repository only through a deployment-incident edge, so the team
		// counts are deployment_linked and the unlinked remainder is an
		// org-wide figure, never a team's.
		declOn(declTeamOnly,
			fStr("incident_attribution_basis"),
			declAggregate(fInt("incidents_count_window", "count")),
			declAggregate(fInt("resolved_incidents_count_window", "count")),
			// Organization-wide, served only to a caller with no repository
			// restriction.
			declAggregate(fInt("org_incidents_not_attributable_count_window", "count")),
			fTable("repository_breakdown",
				cRef(declRepositoryRef, cStr("repository_id")),
				cNullable(cStr("repository_name")),
				cInt("incidents_count_window", "count"),
				cInt("resolved_incidents_count_window", "count"),
			),
		),
	)
}

func sourceHealthFields() []fieldDecl {
	return []fieldDecl{
		fStr("provider"),
		fStr("scope"),
		declNullable(declFresh(fStr("last_sync_at"))),
		declNullable(declFresh(fStr("last_failure_occurred_at"))),
		declNullable(fStr("last_failure_stage")),
	}
}

func deficiencyFields() []fieldDecl {
	// severity is the firing rule's own label, not a computed score: the rule
	// text (rationale, success_criterion) is its explanation.
	return []fieldDecl{
		declNullable(fStr("rule_id")),
		declNullable(fStr("rule_version")),
		declNullable(fStr("severity")),
		declNullable(fStr("title")),
		declNullable(fStr("rationale")),
		declNullable(fStr("success_criterion")),
		declFresh(fStr("window_start")),
		declFresh(fStr("window_end")),
	}
}

func landscapeAreaColumns() []columnDecl {
	return []columnDecl{
		cInt("identity_count", "count"),
		cInt("churn_loc_30d", "loc"),
		cInt("delivery_units_30d", "count"),
		cNum("cycle_p50_30d_hours_avg", "hours"),
		cInt("wip_max_30d", "count"),
	}
}

func landscapeFields() []fieldDecl {
	areaCols := append([]columnDecl{cStr("map_name"), cStr("as_of_day")}, landscapeAreaColumns()...)
	teamCols := append([]columnDecl{cRef(declTeamRef, cStr("team_id")), cStr("map_name"), cStr("as_of_day")}, landscapeAreaColumns()...)
	return declJoin(
		declOn(declTeamOnly,
			fInt("area_count", "count"),
			fTable("area_breakdown", areaCols...),
			fInt("area_breakdown_omitted_count", "count"),
		),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			fTable("team_breakdown", teamCols...),
			fInt("team_breakdown_omitted_count", "count"),
		),
	)
}

func readinessCoverageColumns() []columnDecl {
	return []columnDecl{
		cInt("estimated_count", "count"),
		cInt("unestimated_count", "count"),
		cInt("backlog_size", "count"),
		cNum("estimate_coverage_ratio", "ratio"),
	}
}

// readinessDailyColumns classifies the readiness coverage columns for the
// daily series: each is a snapshot of the backlog on that day, never a flow.
func readinessDailyColumns() []columnDecl {
	cols := readinessCoverageColumns()
	out := make([]columnDecl, len(cols))
	for i, c := range cols {
		out[i] = cNon(c)
	}
	return out
}

func readinessFields() []fieldDecl {
	daily := fDaily("daily_readiness", append([]columnDecl{cStr("day")}, readinessDailyColumns()...)...)
	teamBreakdown := fTable("team_breakdown", append([]columnDecl{
		cNullable(cRef(declTeamRef, cStr("team_id"))),
		cNullable(cStr("team_name")),
		cNullable(cRef(declWorkScopeRef, cStr("work_scope_id"))),
		cNullable(cStr("provider")),
		cStr("day"),
	}, readinessCoverageColumns()...)...)
	return declJoin(
		[]fieldDecl{fStr("basis"), declFresh(fStr("day")), daily, fInt("daily_readiness_omitted_count", "count")},
		declOn(declTeamOnly,
			declNullable(declRef(declWorkScopeRef, fStr("work_scope_id"))),
			declNullable(fStr("provider")),
			fInt("estimated_count", "count"),
			fInt("unestimated_count", "count"),
			fInt("backlog_size", "count"),
			fNum("estimate_coverage_ratio", "ratio"),
		),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			declAggregate(fInt("estimated_count", "count")),
			declAggregate(fInt("unestimated_count", "count")),
			declAggregate(fInt("backlog_size", "count")),
			declAggregate(fNum("estimate_coverage_ratio", "ratio")),
			teamBreakdown,
		),
	)
}

func workloadFields() []fieldDecl {
	daily := fDaily("daily_workload",
		cStr("day"), cNon(cInt("backlog_size", "count")), cNon(cNum("throughput_mean", "")), cNon(cNum("throughput_stddev", "")))
	teamBreakdown := fTable("team_breakdown",
		cNullable(cRef(declTeamRef, cStr("team_id"))),
		cNullable(cStr("team_name")),
		cNum("throughput_mean", ""),
		cNum("throughput_stddev", ""),
		cBool("insufficient_history"),
		cBool("high_variance"),
		cInt("backlog_size", "count"),
		cStr("computed_at"),
		cNullable(cRef(declWorkScopeRef, cStr("work_scope_id"))),
		cInt("forecast_p50_days", "days"),
	)
	return declJoin(
		[]fieldDecl{fStr("basis"), daily, fInt("daily_workload_omitted_count", "count")},
		declOn(declTeamOnly,
			fNum("throughput_mean", ""),
			fNum("throughput_stddev", ""),
			fBool("insufficient_history"),
			fBool("high_variance"),
			fInt("backlog_size", "count"),
			declFresh(fStr("computed_at")),
			declRef(declWorkScopeRef, fStr("work_scope_id")),
			fInt("forecast_p50_days", "days"),
		),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			fInt("team_breakdown_rows_shown", "count"),
			declAggregate(fInt("team_breakdown_rows_total", "count")),
			declAggregate(fInt("p50_known_count", "count")),
			declAggregate(fInt("p50_excluded_unattributed_count", "count")),
			declAggregate(fInt("p50_excluded_null_p50_count", "count")),
			declAggregate(fInt("forecast_p50_days", "days")),
			fStr("p50_basis"),
			declAggregate(fBool("insufficient_history")),
			declAggregate(fBool("high_variance")),
			fStr("p50_unavailable_reason"),
			declFresh(fStr("day")),
			declAggregate(fInt("backlog_size", "count")),
			declAggregate(fNum("throughput_mean", "")),
			declAggregate(fNum("throughput_stddev", "")),
			teamBreakdown,
		),
	)
}

func flowMeasureColumns() []columnDecl {
	return []columnDecl{
		cInt("items_started", "count"),
		cInt("items_completed", "count"),
		cInt("wip_count_end_of_day", "count"),
		cNum("bug_completed_ratio", "ratio"),
		cNum("story_points_completed", "points"),
		cNum("wip_age_p50_hours", "hours"),
		cNum("wip_age_p90_hours", "hours"),
		cNum("cycle_time_p50_hours", "hours"),
		cNum("cycle_time_p90_hours", "hours"),
		cNum("lead_time_p50_hours", "hours"),
		cNum("lead_time_p90_hours", "hours"),
	}
}

func flowTeamBreakdownColumns() []columnDecl {
	cols := flowMeasureColumns()
	out := make([]columnDecl, 0, len(cols))
	for _, c := range cols {
		if c.Name == "items_started" || c.Name == "items_completed" {
			c.Name += "_latest_day"
		}
		out = append(out, c)
	}
	return out
}

func windowEchoFields() []fieldDecl {
	return []fieldDecl{
		fStr("window_mode"),
		fStr("window_start"),
		fStr("window_end"),
		fInt("window_days", "days"),
		fInt("window_days_with_data", "days"),
		fInt("items_started_window", "count"),
		fInt("items_completed_window", "count"),
	}
}

func flowFields() []fieldDecl {
	daily := fDaily("daily_flow",
		cStr("day"),
		cAdd(cInt("items_started", "count")),
		cAdd(cInt("items_completed", "count")),
		cNon(cInt("wip_count_end_of_day", "count")),
		cNon(cNum("bug_completed_ratio", "ratio")),
		cNon(cNum("story_points_completed", "points")),
	)
	scopeBreakdown := fTable("scope_breakdown", append([]columnDecl{
		cStr("provider"),
		cRef(declWorkScopeRef, cStr("work_scope_id")),
		cStr("day"),
	}, flowMeasureColumns()...)...)
	teamBreakdown := fTable("team_breakdown", append([]columnDecl{cRef(declTeamRef, cStr("team_id"))}, flowTeamBreakdownColumns()...)...)
	return declJoin(
		declOn(declTeamProject, daily, fInt("daily_flow_omitted_count", "count"), fStr("daily_flow_omitted_reason")),
		declOn(declTeamProject, windowEchoFields()...),
		declOn(declTeamOnly,
			fInt("scope_count", "count"),
			fInt("items_started_latest_day", "count"),
			fInt("items_completed_latest_day", "count"),
			scopeBreakdown,
			fInt("scope_breakdown_omitted_count", "count"),
		),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			declAggregate(fInt("items_started_latest_day", "count")),
			declAggregate(fInt("items_completed_latest_day", "count")),
			teamBreakdown,
			fInt("team_breakdown_omitted_count", "count"),
		),
		declOn(declRepositoryOnly,
			declFresh(fStr("day")),
			fInt("prs_merged", "count"),
			fInt("prs_with_first_review", "count"),
			fNum("pr_pickup_time_p50_hours", "hours"),
			fNum("pr_review_time_p50_hours", "hours"),
			fNum("pr_first_review_p50_hours", "hours"),
			fNum("pr_first_review_p90_hours", "hours"),
		),
	)
}

func metricsFields() []fieldDecl {
	dailyMetrics := fDaily("daily_metrics",
		cStr("day"),
		cAdd(cInt("commits_count", "count")),
		cAdd(cInt("prs_merged", "count")),
		cNon(cNum("median_pr_cycle_hours", "hours")),
		cNon(cNum("change_failure_rate", "ratio")),
		cNon(cInt("bus_factor", "count")),
		cNon(cNum("code_ownership_gini", "ratio")),
		cNon(cNum("mttr_hours", "hours")),
	)
	teamBreakdown := fTable("team_breakdown",
		cRef(declTeamRef, cStr("team_id")),
		cStr("team_name"),
		cStr("day"),
		cInt("commits_count", "count"),
		cInt("after_hours_commits_count", "count"),
		cInt("weekend_commits_count", "count"),
		cNum("after_hours_commit_ratio", "ratio"),
		cNum("weekend_commit_ratio", "ratio"),
	)
	return declJoin(
		declOn(declRepoTeam, declFresh(fStr("day")), fInt("commits_count", "count")),
		declOn(declRepositoryOnly,
			fInt("day_count", "days"),
			fInt("prs_merged", "count"),
			fNum("median_pr_cycle_hours", "hours"),
			fNum("change_failure_rate", "ratio"),
			fInt("bus_factor", "count"),
			fNum("code_ownership_gini", "ratio"),
			fNum("mttr_hours", "hours"),
			dailyMetrics,
		),
		declOn(declTeamOnly,
			fInt("after_hours_commits_count", "count"),
			fInt("weekend_commits_count", "count"),
			fNum("after_hours_commit_ratio", "ratio"),
			fNum("weekend_commit_ratio", "ratio"),
		),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			declAggregate(fInt("commits_count", "count")),
			declAggregate(fInt("after_hours_commits_count", "count")),
			declAggregate(fInt("weekend_commits_count", "count")),
			teamBreakdown,
		),
	)
}

func healthFields() []fieldDecl {
	rules := fTable("risk_rules",
		cStr("signal"),
		cNum("weight", ""),
		cNullable(cNum("norm_value", "")),
		cNullable(cNum("weighted_contribution", "")),
	)
	riskBreakdown := fTable("risk_breakdown",
		cStr("scope"),
		cRef(declHealthScopeRef, cStr("scope_id")),
		cNullable(cStr("scope_name")),
		cStr("computed_at"),
		cNum("compounding_risk", ""),
		cStr("severity"),
		cStr("severity_as_of"),
		cStr("severity_unavailable_reason"),
	)
	daily := fDaily("daily_health",
		cStr("day"),
		cNullable(cStr("severity")),
		cNon(cNum("compounding_risk", "")),
	)
	return declJoin(
		declOn(declTeamProject, daily, fInt("daily_health_omitted_count", "count")),
		[]fieldDecl{declFresh(fStr("severity_as_of")), fStr("severity_unavailable_reason"), fInt("severity_freshness_window_days", "days")},
		// Repository facts: a scope's own severity and risk; risk_rules explains them.
		declOn(declRepositoryOnly,
			declScore("risk_rules", fNum("compounding_risk", "")),
			declScore("risk_rules", fStr("severity")),
		),
		declOn(declRepoTeam, declFresh(fStr("computed_at")), rules),
		// Team facts: the team scope's own row, computed over every repository
		// the team owns.
		declOn(declTeamOnly,
			declAggregate(declScore("risk_rules", fNum("compounding_risk", ""))),
			declAggregate(declScore("risk_rules", fStr("severity"))),
		),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			declAggregate(fInt("repo_count", "count")),
			fInt("risk_breakdown_rows_shown", "count"),
			declAggregate(fInt("risk_breakdown_rows_total", "count")),
			declAggregate(declScore("risk_breakdown", fStr("severity"))),
			fStr("severity_basis"),
			declAggregate(declScore("risk_breakdown", fNum("compounding_risk", ""))),
			riskBreakdown,
		),
	)
}

func investmentThemeFields(aggregateShares bool) []fieldDecl {
	names := []string{"theme_feature_delivery", "theme_operational", "theme_maintenance", "theme_quality", "theme_risk", "theme_quality_bugfix"}
	out := make([]fieldDecl, 0, len(names))
	for _, name := range names {
		field := fNum(name, "ratio")
		if aggregateShares {
			field = declAggregate(field)
		}
		out = append(out, field)
	}
	return out
}

func investmentFields() []fieldDecl {
	themeBreakdown := fTable("theme_breakdown",
		cStr("theme"), cNum("share", "ratio"), cNum("weighted_effort", ""), cStr("source"), cStr("attribution"))
	teamBreakdown := fTable("team_breakdown",
		cRef(declTeamRef, cStr("team_id")),
		cNullable(cStr("team_name")),
		cStr("day"),
		cInt("delivery_units", "count"),
		cInt("work_items_completed", "count"),
		cInt("prs_merged", "count"),
		cInt("churn_loc", "loc"),
		// cycle_p50_hours is present only when one repository stands behind
		// the row (an exact median). cycle_p50_hours_weighted_mean is an
		// APPROXIMATION, not a median: the work-item-weighted mean of the
		// per-repository medians.
		cNullable(cNum("cycle_p50_hours", "hours")),
		cNullable(cNum("cycle_p50_hours_weighted_mean", "hours")),
		cNullable(cStr("investment_area")),
		cNullable(cStr("project_stream")),
	)
	var prior []fieldDecl
	for _, name := range []string{"prior_theme_feature_delivery", "prior_theme_operational", "prior_theme_maintenance", "prior_theme_quality", "prior_theme_risk"} {
		prior = append(prior, declAggregate(fNum(name, "ratio")))
	}
	return declJoin(
		declOn(declRepositoryOnly,
			investmentThemeFields(false)...,
		),
		declOn(declTeamProject, investmentThemeFields(true)...),
		declOn(declOrganizationOnly, investmentThemeFields(false)...),
		declOn(declRepoTeamOrg, themeBreakdown, fStr("mix_source"), fStr("attribution_basis")),
		declOn(declRepoTeam, investmentUnitFields()...),
		declOn(declOrganizationOnly, fStr("scope"), fInt("repositories_in_scope", "count"), fNum("unresolved_effort_share", "ratio")),
		declOn([]contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectOrganization}, fInt("work_unit_count", "count")),
		declOn(declTeamOnly, declAggregate(fInt("owned_repository_count", "count"))),
		declOn(declTeamOnly, prior...),
		declOn(declProjectOnly,
			fStr("rollup_basis"),
			declAggregate(fInt("team_count", "count")),
			teamBreakdown,
			declAggregate(fInt("repo_count", "count")),
			declAggregate(fInt("work_unit_count", "count")),
			declAggregate(fInt("work_units_without_repo_link", "count")),
			fStr("population_window"),
			fStr("investment_mix_source"),
			declAggregate(fInt("effort_unit_count", "count")),
			declAggregate(fInt("spanning_unit_count", "count")),
			declAggregate(fInt("native_multi_placed_unit_count", "count")),
			declAggregate(fInt("owning_team_rollup_work_unit_count", "count")),
		),
	)
}

// investmentUnitFields declares the work-unit listing facts (unit_kind
// work_unit_share and work_unit_page), served only when read_facts is asked
// for units. repository_id is a repository reference, so a unit whose
// repository the caller may not read has the field withheld and the fact is
// dropped by the reader.
func investmentUnitFields() []fieldDecl {
	fields := []fieldDecl{
		fStr("unit_kind"), fStr("unit_weight"), fStr("work_unit_id"),
		declRef(declRepositoryRef, fStr("repository_id")),
		fStr("unit_from"), fStr("unit_to"),
		fNum("share_in_scope", ""), fNum("unit_effort_value", ""),
		fInt("unit_pull_request_count", "count"), fInt("unit_refs_unresolved", "count"),
		// The handles name issue keys that matched no repository of the
		// organization, so they can name a repository the caller has no grant
		// for: an opaque reference, withheld for a repository-restricted caller.
		declRef(declUnresolvedHandleRef, fStr("unit_unresolved_refs")), fStr("unit_mix_source"), fStr("unit_attribution_basis"),
		fInt("units_returned", "count"), fNum("page_share_total", ""), fInt("units_refs_unresolved", "count"),
		fNum("scope_share_total", ""), fInt("scope_unit_rows", "count"), fStr("next_cursor"), fStr("units_limitation"),
	}
	for _, theme := range canonicalInvestmentThemes {
		fields = append(fields, fNum("unit_"+contextfabric.FactFieldTheme(theme), "ratio"))
	}
	// The page fact's counts and totals are taken over every row of the page,
	// including rows the reader drops for a caller who may not read their
	// repository, so they are aggregates: served with the all-owned-
	// repositories label like the mix they sum to.
	pageAggregates := map[string]bool{
		"units_returned": true, "page_share_total": true, "units_refs_unresolved": true,
		"scope_share_total": true, "scope_unit_rows": true,
	}
	for i := range fields {
		fields[i].Nullable = true
		fields[i].Aggregate = pageAggregates[fields[i].Name]
	}
	return fields
}
