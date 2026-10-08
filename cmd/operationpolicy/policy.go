package main

import (
	"encoding/json"

	dr "github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The hand-authored policy declaration of slice S1a.
//
// Source of every disposition: the lane's step-0 result
// (POLICY-ARTIFACT-v0.md, BINDING), its evidence (resolver-matrix.md, ops
// 9dffd5f77, read-only), and the CHAOS-7036 design sections D.3 to D.7 and
// table K14-A. File:line citations below are ops paths under
// internal/queryapi at 9dffd5f77 ("[ops]"); "RM §n" is the resolver-matrix
// section that read them.
//
// Rule: this is an ALLOWLIST. A variable path not listed in Variables is
// refused; an operation not listed in Served is refused; a restricted
// caller is refused unless Restricted.Served is set with a forced
// repository variable and row id paths.

// windowDays is the D.6 window clamp for list and series operations.
const windowDays = 90

// analyticsWindowDays is the ops analytics limit (maxDays, [ops]
// analytics/cost.go:12,53-62): D.6 keeps analytics_batch "inside the ops
// limits".
const analyticsWindowDays = 3650

// pageSize is the D.6 page-size clamp for list operations.
const pageSize = 200

// idListMax bounds client id lists (ops binds 500 or 1000 ids silently).
const idListMax = 200

var (
	repository = &dr.SubjectConversion{Kind: dr.SubjectKindRepository, AcrPrefix: "repository:", OpsForm: "bare_uuid"}
	team       = &dr.SubjectConversion{Kind: dr.SubjectKindTeam, AcrPrefix: "team:", OpsForm: "bare_id"}

	principalOrg = variableDecl{Source: dr.SourcePrincipalOrg}
	forcedTrue   = variableDecl{Source: dr.SourceForced, ForcedValue: json.RawMessage("true")}
	forcedFalse  = variableDecl{Source: dr.SourceForced, ForcedValue: json.RawMessage("false")}
	client       = variableDecl{}

	repoIDs = variableDecl{MaxItems: idListMax, Subject: repository}
	teamIDs = variableDecl{MaxItems: idListMax, Subject: team}
	teamID  = variableDecl{Subject: team}
)

func between(lo, hi int64) variableDecl  { return variableDecl{Min: i64(lo), Max: i64(hi)} }
func text(maxLength int) variableDecl    { return variableDecl{MaxLength: maxLength} }
func enum(values ...string) variableDecl { return variableDecl{AllowedValues: values} }
func listOf(values ...string) variableDecl {
	return variableDecl{AllowedValues: values, MaxItems: len(values)}
}

func refuse(code dr.RefusalCode, reason string) dr.Refusal {
	return dr.Refusal{Code: code, Reason: reason}
}
func notApplied(reason string) dr.Refusal {
	return refuse(dr.RefusalVariableNotAllowed, "the ops resolver does not apply this variable: "+reason)
}

func window(start, end string, days int, allowOpenStart bool) dr.Constraint {
	return dr.Constraint{
		Kind: dr.ConstraintWindowMaxDays, Path: start, Other: end, MaxDays: days, AllowOpenStart: allowOpenStart,
		Code: dr.RefusalVariableOutOfRange, Reason: "window longer than the design D.6 clamp",
	}
}

func refusedFor(reason string) dr.CallerScope {
	return dr.CallerScope{
		Refusal: &dr.Refusal{Code: dr.RefusalOperationNotServedForCaller, Reason: reason},
		Basis:   reason,
	}
}

func served(basis string) dr.CallerScope { return dr.CallerScope{Basis: basis} }

func withVariables(base map[string]variableDecl, extra map[string]variableDecl) map[string]variableDecl {
	out := map[string]variableDecl{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func withRefusals(base, extra map[string]dr.Refusal) map[string]dr.Refusal {
	out := map[string]dr.Refusal{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

var securityFilterVariables = map[string]variableDecl{
	"orgId":              principalOrg,
	"filters.repoIds":    repoIDs,
	"filters.severities": listOf("LOW", "MEDIUM", "HIGH", "CRITICAL", "UNKNOWN"),
	"filters.sources":    listOf("DEPENDABOT", "CODE_SCANNING", "ADVISORY", "GITLAB_VULNERABILITY", "GITLAB_DEPENDENCY"),
	"filters.states":     listOf("OPEN", "FIXED", "DISMISSED", "DETECTED", "CONFIRMED", "RESOLVED"),
	"filters.since":      client,
	"filters.until":      client,
	"filters.openOnly":   client,
	"filters.search":     text(200),
}

var nodeTypes = []string{"ISSUE", "PR", "COMMIT", "FILE", "RELEASE", "FEATURE_FLAG", "AI_WORKFLOW_RUN", "DIFF", "REVIEW_OUTCOME", "DEPLOYMENT", "INCIDENT"}

var edgeTypes = []string{"BLOCKS", "RELATES", "DUPLICATES", "IS_BLOCKED_BY", "IS_RELATED_TO", "IS_DUPLICATE_OF", "PARENT_OF", "CHILD_OF", "REFERENCES", "IMPLEMENTS", "FIXES", "CONTAINS", "TOUCHES", "INTRODUCED_BY", "CONFIG_CHANGED_BY", "GUARDS", "IMPACTS", "HAS_AI_WORKFLOW", "GENERATES", "HAS_REVIEW_OUTCOME", "DEPLOYS", "LINKED_INCIDENT"}

var measures = []string{"COUNT", "CHURN_LOC", "PR_REWORK_RATIO", "CYCLE_TIME_HOURS", "THROUGHPUT", "PIPELINE_SUCCESS_RATE", "PIPELINE_FAILURE_RATE", "PIPELINE_DURATION_P95", "PIPELINE_QUEUE_TIME", "PIPELINE_RERUN_RATE", "TEST_PASS_RATE", "TEST_FAILURE_RATE", "TEST_FLAKE_RATE", "TEST_SUITE_DURATION_P95", "COVERAGE_LINE_PCT", "COVERAGE_BRANCH_PCT", "COVERAGE_DELTA_PCT", "FLAG_FRICTION_DELTA", "FLAG_ERROR_RATE_DELTA", "FLAG_COVERAGE_RATIO", "FLAG_ACTIVATION_RATE"}

// limitingFactorNotPerson: the person-name token "actor" matches inside the
// word "Factor" of home.limitingFactor; the field is a generated claim about
// the org, not a person (CHAOS-7202).
const limitingFactorNotPerson = "false positive: the token actor matches inside limitingFactor (Factor); the object is a generated org-level claim, not a person"

const workGraphWithheldEvidence = "free text evidence string; not all producers were read and it may carry person content (RM §11, §13; design D.3: evidence is not an allowed output path)"

func workGraphFilters(extra map[string]variableDecl) map[string]variableDecl {
	return withVariables(map[string]variableDecl{
		"orgId":                      principalOrg,
		"filters.repoIds":            repoIDs,
		"filters.theme":              text(128),
		"filters.subcategory":        text(128),
		"filters.allowScopedPartial": forcedFalse,
	}, extra)
}

var workGraphFilterNotApplied = map[string]dr.Refusal{
	"filters.sourceType": notApplied("[ops] workgraph/flow.go:70 and artifacts.go:103 build the WHERE with includeEdgeFilters=false (RM §12, §13)"),
	"filters.targetType": notApplied("[ops] workgraph/flow.go:70 and artifacts.go:103 (RM §12, §13)"),
	"filters.edgeType":   notApplied("[ops] workgraph/flow.go:70 and artifacts.go:103 (RM §12, §13)"),
	"filters.edgeTypes":  notApplied("[ops] workgraph/flow.go:70 and artifacts.go:103 (RM §12, §13)"),
	"filters.nodeId":     notApplied("[ops] workgraph/flow.go:70 and artifacts.go:103 (RM §12, §13)"),
}

const k14Reason = dr.BasisDependentShapeText + " (design table K14-A)"

var basisDependent = refuse(dr.RefusalBasisDependentShape, k14Reason)

// investmentDecl is the K14-A basis-free shape shared by investmentBreakdown
// and investmentFull: breakdown dimensions THEME, SUBCATEGORY, WORK_TYPE;
// scope absent or ORG with no ids; no what.repos; sankey null; the
// investment source forced on (useInvestment true, the path K14-A read).
func investmentDecl(documentName string, exceptions map[string]string, disclosure []dr.DisclosureField) operationDecl {
	return operationDecl{
		DocumentName: documentName,
		Cost:         dr.CostAnalyticsBatch,
		Variables: map[string]variableDecl{
			"orgId":            principalOrg,
			"batch.breakdowns": {MaxItems: 1},
			"batch.breakdowns[*].dimension": {
				AllowedValues: []string{"THEME", "SUBCATEGORY", "WORK_TYPE"},
				RefusedValues: []dr.ValueRefusal{
					{Value: "TEAM", Code: dr.RefusalBasisDependentShape, Reason: k14Reason + ": team vote join, [ops] analytics/investment.go:598-605"},
					{Value: "REPO", Code: dr.RefusalBasisDependentShape, Reason: k14Reason + ": repository allocation source, [ops] analytics/investment.go:574-578; refused until oracle O4"},
				},
			},
			"batch.breakdowns[*].measure":             enum(measures...),
			"batch.breakdowns[*].dateRange.startDate": client,
			"batch.breakdowns[*].dateRange.endDate":   client,
			"batch.breakdowns[*].topN":                between(1, 100),
			"batch.useInvestment":                     forcedTrue,
			"batch.filters.scope.level": {
				AllowedValues: []string{"ORG"},
				RefusedValues: []dr.ValueRefusal{
					{Value: "TEAM", Code: dr.RefusalBasisDependentShape, Reason: k14Reason + ": needsTeamJoin, [ops] analytics/investment.go:534-539, investmentquality.go:231-245"},
					{Value: "REPO", Code: dr.RefusalBasisDependentShape, Reason: k14Reason + ": repository filter translation, [ops] analytics/filtertranslation.go:49-51,213-214"},
					{Value: "SERVICE", Code: dr.RefusalVariableNotAllowed, Reason: "scope level SERVICE applies no filter at the data layer ([ops] analytics/filtertranslation.go:55-57)"},
				},
			},
			"batch.filters.scope.ids":        client,
			"batch.filters.why.workCategory": {MaxItems: 20, MaxLength: 128},
		},
		RefusedPaths: map[string]dr.Refusal{
			"batch.sankey":                basisDependent,
			"batch.filters.what.repos":    basisDependent,
			"batch.timeseries":            refuse(dr.RefusalVariableNotAllowed, "timeseries is not selected by the document; it would cost work that is never returned (RM §8-9)"),
			"batch.flowMatrix":            refuse(dr.RefusalVariableNotAllowed, "flowMatrix is not selected by the document and reads the team vote (RM §8-9, design D.3)"),
			"batch.filters.who.roles":     notApplied("[ops] analytics/filters.go:23 only (RM §8-9)"),
			"batch.filters.what.services": notApplied("[ops] analytics/repofilters.go:158 pass-through only (RM §8-9)"),
			"batch.filters.why.issueType": notApplied("RM §8-9"),
			"batch.filters.how":           notApplied("flowStage, [ops] analytics/filtertranslation.go:151-158 (RM §8-9)"),
		},
		Constraints: []dr.Constraint{
			{Kind: dr.ConstraintEmptyOrAbsent, Path: "batch.filters.scope.ids", Code: dr.RefusalBasisDependentShape, Reason: k14Reason + ": scope must be absent or ORG with no ids"},
			{Kind: dr.ConstraintRequiresValue, Path: "batch.filters.scope", Other: "batch.filters.scope.level", Values: []string{"ORG"}, OtherDefault: "ORG", Code: dr.RefusalBasisDependentShape, Reason: k14Reason + ": scope level must be ORG"},
			window("batch.breakdowns[*].dateRange.startDate", "batch.breakdowns[*].dateRange.endDate", analyticsWindowDays, false),
		},
		Unrestricted:     served("basis-free shape only: [ops] analytics/investment.go:573-582,598 reads the latest work unit investments with no team join and no repository allocation (K14-A, V)"),
		Restricted:       refusedFor("K14-A: forced scope inside the nested analytics batch is not proved; " + dr.BasisDependentShapeText),
		OutputExceptions: exceptions,
		Disclosure:       disclosure,
	}
}

func declaredPolicy() policyDeclaration {
	breakdown := investmentDecl("InvestmentBreakdown", map[string]string{
		"analytics.evidenceQualityDistribution":     "aggregate evidence-quality band counts, no person field ([ops] analytics/investmentquality.go; RM §8-9); computed only with useInvestment=true, which acr forces",
		"analytics.evidenceQualityStats.bandCounts": "aggregate evidence-quality band counts, no person field ([ops] analytics/investmentquality.go; RM §8-9)",
	}, nil)
	full := investmentDecl("InvestmentFull", nil, []dr.DisclosureField{
		{Path: "analytics.sankey.coverage.teamCoverage", Rule: dr.DisclosurePartialWhenBelowOne},
		{Path: "analytics.sankey.coverage.repoCoverage", Rule: dr.DisclosurePartialWhenBelowOne},
	})
	full.Notes = []string{"batch.sankey is refused in S1a (K14-A), so sankey is always null and the coverage disclosure never fires; completeness stays unknown"}

	return policyDeclaration{
		Served: map[string]operationDecl{
			"compoundingRisk": {
				DocumentName: "CompoundingRisk",
				Cost:         dr.CostSeries,
				Variables: map[string]variableDecl{
					"orgId":            principalOrg,
					"filter.day":       client,
					"filter.breakout":  enum("REPO", "TEAM"),
					"filter.repoIds":   repoIDs,
					"filter.teamIds":   teamIDs,
					"filter.trendDays": between(1, windowDays),
				},
				Constraints: []dr.Constraint{
					{Kind: dr.ConstraintRequiresValue, Path: "filter.repoIds", Other: "filter.breakout", Values: []string{"REPO"}, OtherDefault: "REPO", Code: dr.RefusalVariableNotAllowed, Reason: "repoIds applies only with breakout REPO; under TEAM it feeds only the fallback and the trend ([ops] compoundingrisk/resolve.go:242,300,341-356; RM §1)"},
					{Kind: dr.ConstraintRequiresValue, Path: "filter.teamIds", Other: "filter.breakout", Values: []string{"TEAM"}, OtherDefault: "REPO", Code: dr.RefusalVariableNotAllowed, Reason: "teamIds is ignored under breakout REPO ([ops] compoundingrisk/resolve.go:283-298,354; RM §1)"},
				},
				Unrestricted: served("[ops] compoundingrisk/resolve.go:273,284,354 apply repoIds under REPO; read.go:75-81 (RM §1, V)"),
				Restricted: dr.CallerScope{
					Served:             true,
					ForcedVariablePath: "filter.repoIds",
					SubjectKind:        dr.SubjectKindRepository,
					RowIDPaths:         []string{"compoundingRisk.rows[*].scopeId"},
					UncheckedPaths:     []string{"compoundingRisk.trend"},
					Constraints: []dr.Constraint{
						{Kind: dr.ConstraintRequiresValue, Other: "filter.breakout", Values: []string{"REPO"}, OtherDefault: "REPO", Code: dr.RefusalOperationNotServedForCaller, Reason: "breakout TEAM reads by team ids only and carries no repository id per row ([ops] compoundingrisk/resolve.go:300; read.go:117,217; RM §1; GWC A1.4 request 10: acr refuses)"},
						{Kind: dr.ConstraintEmptyOrAbsent, Path: "filter.teamIds", Code: dr.RefusalOperationNotServedForCaller, Reason: "team ids are not served to a repository-restricted caller (design D.3, K15)"},
					},
					Basis: "forced filter.repoIds (non-null; [] = nothing, read.go:64,86,150) + row check on rows[*].scopeId = toString(repos.id) (read.go:77,297); trend has no id and follows repoIds (resolve.go:341-356), so it is covered by the forced filter, not by a row check (RM §1)",
				},
			},
			"hotspots": {
				DocumentName: "Hotspots",
				Cost:         dr.CostList,
				Variables: map[string]variableDecl{
					"input.orgId":    principalOrg,
					"input.sinceUtc": client,
					"input.untilUtc": client,
					"input.repoIds":  repoIDs,
					"input.limit":    between(1, pageSize),
				},
				RefusedPaths: map[string]dr.Refusal{
					"input.teamIds": notApplied("[ops] graph/schema.resolvers.go:1063 passes only RepoIds (RM §2, design D.3)"),
				},
				Constraints:  []dr.Constraint{window("input.sinceUtc", "input.untilUtc", windowDays, false)},
				Unrestricted: served("[ops] hotspots/hotspots.go:245-254 apply repoIds; graph/schema.resolvers.go:1063 (RM §2, V)"),
				Restricted: dr.CallerScope{
					Served:             true,
					ForcedVariablePath: "input.repoIds",
					SubjectKind:        dr.SubjectKindRepository,
					RowIDPaths:         []string{"hotspots.rows[*].repoId", "hotspots.repos[*].repoId"},
					Basis:              "forced input.repoIds + row check on rows[*].repoId = toString(repo_id) ([ops] hotspots/hotspots.go:228,375) and on repos[*].repoId, the per-repository roll-up of the same rows. Empty or null repoIds means ALL repositories (:245), so an empty grant intersection must end before dispatch with no_granted_scope (RM §2)",
				},
			},
			"acrRepositoryScopes": {
				DocumentName: "ACRRepositoryScopes",
				Cost:         dr.CostCatalog,
				Variables:    map[string]variableDecl{"orgId": principalOrg},
				Unrestricted: served("[ops] analytics/catalog.go:110-133 lists every well-formed org repository slug, max 100 else an ops error (RM §4, V)"),
				Restricted:   refusedFor("no grant logic in ops and values are lowercased owner/name slugs, not uuids, so no row id check is possible ([ops] analytics/catalog.go:119,206-232; RM §4: design pointer 'keeps granted repositories' is STALE)"),
			},
			"catalogValues": {
				DocumentName: "CatalogValues",
				Cost:         dr.CostCatalog,
				Variables: map[string]variableDecl{
					"orgId":     principalOrg,
					"dimension": enum("TEAM", "REPO", "WORK_TYPE", "THEME", "SUBCATEGORY"),
				},
				Unrestricted: served("[ops] analytics/catalog.go:110-199: org-wide values, no filter variable in the document; THEME, SUBCATEGORY, WORK_TYPE are org-wide investment counts with no scope filter (K14-A, RM §5)"),
				Restricted:   refusedFor("the document has no filter variable, so every value is org-wide; REPO values are slugs and TEAM values are team ids of the whole org ([ops] analytics/catalog.go:110-162; RM §5)"),
			},
			"complexityTimeseries": {
				DocumentName: "ComplexityTimeseries",
				Cost:         dr.CostSeries,
				Variables: map[string]variableDecl{
					"input.orgId":       principalOrg,
					"input.sinceUtc":    client,
					"input.untilUtc":    client,
					"input.granularity": enum("DAY", "WEEK"),
					"input.scope":       enum("REPO", "FILE"),
					"input.repoIds":     repoIDs,
					"input.limit":       between(1, pageSize),
				},
				RefusedPaths: map[string]dr.Refusal{
					"input.teamIds": notApplied("[ops] graph/schema.resolvers.go:1033 does not pass TeamIds (RM §6, design D.3)"),
				},
				Constraints:  []dr.Constraint{window("input.sinceUtc", "input.untilUtc", windowDays, false)},
				Unrestricted: served("[ops] complexitytimeseries/complexitytimeseries.go:151-156,222-225 apply repoIds (RM §6)"),
				Restricted:   refusedFor("empty repoIds in REPO scope returns the top-N repositories, repoIds are truncated to limit, and the FILE scope id is composite ([ops] complexitytimeseries/complexitytimeseries.go:139-144,223-244,455; RM §6); kept refused in S1a (POLICY-ARTIFACT-v0)"),
				Notes:        []string{"totalScope counts the returned points only, not a true total (complexitytimeseries.go:501-508)"},
			},
			"cognitiveLoad": {
				DocumentName: "CognitiveLoad",
				Cost:         dr.CostSeries,
				Variables: map[string]variableDecl{
					"input.orgId":     principalOrg,
					"input.sinceDate": client,
					"input.untilDate": client,
					"input.teamId":    teamID,
				},
				Notes: []string{"input.teamId is required: without it the call is refused with scope_required"},
				RefusedPaths: map[string]dr.Refusal{
					"input.repoId": refuse(dr.RefusalVariableNotAllowed, "a repository-only read returns org-wide team ratios ([ops] cognitiveload/cognitiveload.go:178-195,343-356; RM §7, design D.3)"),
				},
				Constraints: []dr.Constraint{
					{Kind: dr.ConstraintRequired, Path: "input.teamId", Code: dr.RefusalScopeRequired, Reason: "cognitiveLoad is served only with a team id: send `input.teamId` ([ops] cognitiveload/cognitiveload.go:119-141; design D.3)"},
					window("input.sinceDate", "input.untilDate", windowDays, false),
				},
				Unrestricted: served("[ops] cognitiveload/cognitiveload.go:119-141,468-473 read team_cognitive_load_daily by team id (RM §7, V)"),
				Restricted:   refusedFor("rows carry no repository id; the repository-only path mixes scope ([ops] cognitiveload/cognitiveload.go:178-195; RM §7)"),
			},
			"home": {
				DocumentName: "Home",
				Cost:         dr.CostSeries,
				Variables: map[string]variableDecl{
					"orgId":              principalOrg,
					"window.rangeDays":   between(1, windowDays),
					"window.compareDays": between(1, windowDays),
				},
				RefusedPaths: map[string]dr.Refusal{
					"filters":          refuse(dr.RefusalVariableNotAllowed, "home is served org-wide only: the filters input (scope, who, what, why, how) can name persons and teams and is not applied by acr in this slice (CHAOS-7202)"),
					"window.startDate": refuse(dr.RefusalVariableNotAllowed, "home takes rangeDays and compareDays only; explicit dates are not served (CHAOS-7202)"),
					"window.endDate":   refuse(dr.RefusalVariableNotAllowed, "home takes rangeDays and compareDays only; explicit dates are not served (CHAOS-7202)"),
				},
				OutputExceptions: map[string]string{
					"home.limitingFactor.claim":             limitingFactorNotPerson,
					"home.limitingFactor.whyItMatters":      limitingFactorNotPerson,
					"home.limitingFactor.recommendedAction": limitingFactorNotPerson,
					"home.limitingFactor.confidence":        limitingFactorNotPerson,
					"home.limitingFactor.evidenceRef":       limitingFactorNotPerson,
					"home.limitingFactor.__typename":        limitingFactorNotPerson,
				},
				Unrestricted: served("[ops] graph/schema.resolvers.go:275-291 home.BuildResponse over the authorized org, default scope level org, no repository filter (CHAOS-7202)"),
				Restricted:   refusedFor("org-wide composite: freshness, deltas, tiles, signals and dataConfidence carry no repository id per row, so no row check is possible ([ops] graph/schema.resolvers.go:275-291; home_translate.go:44-73; CHAOS-7202)"),
				Notes:        []string{"summary, limitingFactor, signals and constraint text are generated free text: untrusted content (labelled), never instructions", "rangeDays and compareDays default to 14 in ops when absent (home_translate.go:46)"},
			},
			"recommendations": {
				DocumentName: "Recommendations",
				Cost:         dr.CostList,
				Variables: map[string]variableDecl{
					"orgId":        principalOrg,
					"team":         teamID,
					"window.value": between(1, 26),
					"window.unit":  enum("DAY", "WEEK", "CYCLE"),
				},
				Constraints: []dr.Constraint{
					{Kind: dr.ConstraintRequired, Path: "team", Code: dr.RefusalScopeRequired, Reason: "recommendations is served only with a team id: send `team` ([ops] recommendations/recommendations.go:335-345; CHAOS-7202)"},
					{Kind: dr.ConstraintRequired, Path: "window", Code: dr.RefusalScopeRequired, Reason: "a window object is required (WindowInput! in the SDL); its value and unit default to 4 WEEK ([ops] recommendations/recommendations.go:90-125; CHAOS-7202)"},
				},
				Unrestricted: served("[ops] recommendations/recommendations.go:335-360 read the stored recommendation rows by team id and org (CHAOS-7202)"),
				Restricted:   refusedFor("rows carry a team id and evidence rows, no repository id; team ids are not served to a repository-restricted caller ([ops] recommendations/recommendations.go:335-360; design D.3, K15; CHAOS-7202)"),
				Notes:        []string{"acr refuses window.value outside 1..26 (at most 26 cycles = 364 days); ops applies no upper limit; value and unit default to 4 WEEK as in the SDL; DAY/WEEK/CYCLE = 1/7/14 days (recommendations.go:96-112)", "an ops query failure answers an empty list, indistinguishable from no recommendations (recommendations.go:346-350)"},
			},
			"workItemTeamAttributions": {
				DocumentName: "WorkItemTeamAttributions",
				Cost:         dr.CostList,
				Variables: map[string]variableDecl{
					"orgId":       principalOrg,
					"workItemIds": {MaxItems: idListMax, MaxLength: 256},
					"teamId":      teamID,
				},
				Unrestricted: served("[ops] graph/schema.resolvers.go:654-693 workgraph.ResolveWorkItemTeamAttributions: the stored work_item_team_attributions rows as facts with source, confidence, isPrimary and evidence; acr adds no attribution logic (CHAOS-7202; AGENTS.md team-attribution contract)"),
				Restricted:   refusedFor("rows carry a work item id and a team id, no repository id, so a granted-repository row check is not possible ([ops] graph/schema.resolvers.go:654-693; design D.3, K15; CHAOS-7202)"),
				Notes:        []string{"team = project/repository ownership only; rows are raw attribution facts with provenance (source, confidence, evidence)", "workItemIds and teamId both absent reads every attribution row of the org: bounded by the response byte cap"},
			},
			"investmentBreakdown": breakdown,
			"investmentFull":      full,
			"securityOverview": {
				DocumentName: "SecurityOverview",
				Cost:         dr.CostSeries,
				Variables:    securityFilterVariables,
				Constraints:  []dr.Constraint{window("filters.since", "filters.until", windowDays, true)},
				Unrestricted: served("[ops] security/overview.go:49-179 apply the shared repoIds filter (filter.go:61-64; RM §10)"),
				Restricted:   refusedFor("kpis, severityBreakdown and trend are aggregates with no row id ([ops] security/overview.go:49-70,97-106,159-179; RM §10)"),
				Notes:        []string{"trend and the 30-day KPIs use fixed windows from now (overview.go:169,176); the filter window does not move them", "cost class not named by design D.6; series chosen (aggregate over fixed windows)"},
			},
			"workGraphEdges": {
				DocumentName: "WorkGraphEdges",
				Cost:         dr.CostList,
				Variables: workGraphFilters(map[string]variableDecl{
					"filters.sourceType": enum(nodeTypes...),
					"filters.targetType": enum(nodeTypes...),
					"filters.edgeType":   enum(edgeTypes...),
					"filters.edgeTypes":  {AllowedValues: edgeTypes, MaxItems: len(edgeTypes)},
					"filters.nodeId":     text(256),
					"filters.limit":      between(1, pageSize),
				}),
				Unrestricted:    served("[ops] workgraph/scope.go:66-136, edges.go:158-182 apply repoIds (RM §11)"),
				Restricted:      refusedFor("one repository id per edge (argMax winner) does not prove both ends; dependency edges carry none ([ops] workgraph/edges.go:182,195,252-254,304; RM §11, design D.3)"),
				WithheldOutputs: map[string]string{"workGraphEdges.edges[*].evidence": workGraphWithheldEvidence},
				Disclosure:      []dr.DisclosureField{{Path: "workGraphEdges.degradedReason", Rule: dr.DisclosurePartialWhenNonNull}},
				Notes:           []string{"allowScopedPartial is forced false: partial mode would be invisible, isPartial is not selected (RM §11)", "sourceDisplayName/targetDisplayName can be PR or incident titles (free text, displaynames.go:129,286)"},
			},
			"workGraphFlow": {
				DocumentName: "WorkGraphFlow",
				Cost:         dr.CostList,
				Variables:    workGraphFilters(nil),
				RefusedPaths: withRefusals(workGraphFilterNotApplied, map[string]dr.Refusal{"filters.limit": notApplied("[ops] workgraph/flow.go applies no limit (RM §12)")}),
				Unrestricted: served("[ops] workgraph/flow.go:47-58,70 apply repoIds (RM §12)"),
				Restricted:   refusedFor("rows carry no id of any kind ([ops] workgraph/flow.go:70; RM §12)"),
				Disclosure:   []dr.DisclosureField{{Path: "workGraphFlow.degradedReason", Rule: dr.DisclosurePartialWhenNonNull}},
				Notes:        []string{"cost class not named by design D.6; list chosen (same source as workGraphEdges)"},
			},
			"workGraphArtifacts": {
				DocumentName:    "WorkGraphArtifacts",
				Cost:            dr.CostList,
				Variables:       workGraphFilters(map[string]variableDecl{"filters.limit": between(1, pageSize)}),
				RefusedPaths:    workGraphFilterNotApplied,
				Unrestricted:    served("[ops] workgraph/artifacts.go:78-82,103 apply repoIds and limit (RM §13)"),
				Restricted:      refusedFor("rows carry no repository id; node ids are opaque except PR ids ([ops] workgraph/artifacts.go:103-105; RM §13)"),
				WithheldOutputs: map[string]string{"workGraphArtifacts.rows[*].evidence": workGraphWithheldEvidence},
				Disclosure:      []dr.DisclosureField{{Path: "workGraphArtifacts.degradedReason", Rule: dr.DisclosurePartialWhenNonNull}},
			},
			"throughputForecast": {
				DocumentName: "ThroughputForecast",
				Cost:         dr.CostSeries,
				Variables: map[string]variableDecl{
					"orgId":              principalOrg,
					"input.teamIds":      teamIDs,
					"input.backlogSize":  between(0, 100000),
					"input.historyWeeks": between(1, windowDays/7),
				},
				RefusedPaths: map[string]dr.Refusal{
					"input.workScopeId": refuse(dr.RefusalVariableNotAllowed, "work scope id form between acr and ops is UNCONFIRMED (POLICY-ARTIFACT-v0)"),
				},
				Unrestricted: served("[ops] throughputforecast clickhouse.go:44-63 apply teamIds (RM §14)"),
				Restricted:   refusedFor("no repository scope; reviewBottleneck and incidentLoad are org-wide ([ops] throughputforecast/resolve.go:145-152; clickhouse.go:395-411,492-510; RM §14); REFUSED until the K2 team path is read"),
				Disclosure:   []dr.DisclosureField{{Path: "throughputForecast.insufficientHistory", Rule: dr.DisclosurePartialWhenTrue}},
				Notes:        []string{"reviewBottleneck and incidentLoad stay org-wide even with teamIds (RM §14)"},
			},
			"capacityForecasts": {
				DocumentName: "CapacityForecasts",
				Cost:         dr.CostList,
				Variables: map[string]variableDecl{
					"orgId":            principalOrg,
					"filters.teamId":   teamID,
					"filters.fromDate": client,
					"filters.toDate":   client,
					"filters.limit":    between(1, pageSize),
				},
				RefusedPaths: map[string]dr.Refusal{
					"filters.workScopeId": refuse(dr.RefusalVariableNotAllowed, "work scope id form between acr and ops is UNCONFIRMED (POLICY-ARTIFACT-v0)"),
				},
				Constraints:       []dr.Constraint{window("filters.fromDate", "filters.toDate", windowDays, false)},
				AdditionalOutputs: []string{"capacityForecasts.edges[*].node.completionDistribution.days[*].value", "capacityForecasts.edges[*].node.completionDistribution.days[*].count", "capacityForecasts.edges[*].node.completionDistribution.items[*].value", "capacityForecasts.edges[*].node.completionDistribution.items[*].count"},
				Unrestricted:      served("[ops] capacityforecast.go:398-421 apply teamId and the dates; limit has no ops clamp (:423-454; A1.1) (RM §15)"),
				Restricted:        refusedFor("no repository scope; team aggregates ([ops] capacityforecast.go:398-421; RM §15); REFUSED until the K2 team path is read"),
			},
			"capacityCompletionDistribution": {
				DocumentName:      "CapacityCompletionDistribution",
				Cost:              dr.CostCompute,
				MaxInFlightPerOrg: 1,
				Variables: map[string]variableDecl{
					"orgId":             principalOrg,
					"input.teamId":      teamID,
					"input.targetItems": between(1, 100000),
					"input.targetDate":  client,
					"input.historyDays": between(1, 180),
					"input.simulations": between(1, 10000),
				},
				RefusedPaths: map[string]dr.Refusal{
					"input.workScopeId": refuse(dr.RefusalVariableNotAllowed, "work scope id form between acr and ops is UNCONFIRMED (POLICY-ARTIFACT-v0)"),
				},
				Constraints: []dr.Constraint{
					{Kind: dr.ConstraintMaxDaysAhead, Path: "input.targetDate", MaxDays: 365, Code: dr.RefusalVariableOutOfRange, Reason: "targetDate at most 365 days ahead (design D.6)"},
				},
				Unrestricted: served("[ops] capacityforecast/clickhouse.go:47-61 apply teamId (RM §16)"),
				Restricted:   refusedFor("no repository scope; team aggregate with compute cost ([ops] capacityforecast/clickhouse.go:47-61; capacityforecast.go:130; RM §16); REFUSED until the K2 team path is read"),
				Notes:        []string{"non-deterministic: a random seed per call, no seed argument (capacityforecast.go:139-148; A1.1)", "null result = no history, indistinguishable from no target (capacityforecast.go:229-260)"},
			},
			"capacityForecast": {
				DocumentName:      "CapacityForecast",
				Cost:              dr.CostCompute,
				MaxInFlightPerOrg: 1,
				Variables: map[string]variableDecl{
					"orgId":             principalOrg,
					"input.teamId":      teamID,
					"input.targetItems": between(1, 100000),
					"input.targetDate":  client,
					"input.historyDays": between(1, 180),
					"input.simulations": between(1, 10000),
				},
				RefusedPaths: map[string]dr.Refusal{
					"input.workScopeId": refuse(dr.RefusalVariableNotAllowed, "work scope id form between acr and ops is UNCONFIRMED (POLICY-ARTIFACT-v0)"),
				},
				Constraints: []dr.Constraint{
					{Kind: dr.ConstraintMaxDaysAhead, Path: "input.targetDate", MaxDays: 365, Code: dr.RefusalVariableOutOfRange, Reason: "targetDate at most 365 days ahead (design D.6)"},
				},
				Unrestricted: served("[ops] capacityforecast/clickhouse.go:47-61 apply teamId (RM §16)"),
				Restricted:   refusedFor("no repository scope; team aggregate with compute cost ([ops] capacityforecast/clickhouse.go:47-61; capacityforecast.go:130; RM §16); REFUSED until the K2 team path is read"),
				Notes:        []string{"non-deterministic: a random seed per call, no seed argument (capacityforecast.go:139-148; A1.1)", "null result = no history or no target, indistinguishable (capacityforecast.go:229-260)"},
			},
		},
		NotServed: notServed(),
	}
}

func notServed() map[string]notServedDecl {
	const (
		mutation   = "mutation: the MCP surface is read-only; never served (design D.3)"
		ai         = "AI analytics: later slice (design D.3)"
		flags      = "feature flag operation: later slice (design D.3)"
		operator   = "operator or superuser view (design D.3)"
		userOwned  = "user-owned product object (design D.3)"
		noScope    = "no scope rule or no honest status yet (design D.3)"
		personData = "reviewer and author data (design D.3)"
		rootDark   = "root field is not enabled on the ops query service run-operation path (root_field_not_enabled): dark on purpose"
	)
	return map[string]notServedDecl{
		"cloneSavedReport":                  {mutation},
		"createSavedReport":                 {mutation},
		"deleteSavedReport":                 {mutation},
		"triggerReport":                     {mutation},
		"updateSavedReport":                 {mutation},
		"aiAttributedPrs":                   {personData},
		"aiAttributionOverview":             {ai},
		"aiComparison":                      {ai},
		"aiGovernanceSummary":               {ai},
		"aiImpactSummary":                   {ai},
		"aiOpportunities":                   {ai},
		"aiReviewLoad":                      {ai},
		"aiRiskBreakdown":                   {ai},
		"aiWorkflowDrilldown":               {ai},
		"busFactor":                         {"ranked persons: topMaintainers { author sharePercent } ([ops] server/query_route.go:1029; design D.3, K18: a person-free document comes by the GWC batch)"},
		"connectorsDataHealth":              {operator},
		"dataHealthIdentity":                {operator},
		"mappingCoverageHealth":             {operator},
		"metricLineage":                     {operator},
		"productTelemetryDashboard":         {operator},
		"productTelemetryPlatformDashboard": {operator},
		"experiments":                       {operator},
		"featureFlagEvents":                 {flags},
		"featureFlagTimeseries":             {flags},
		"featureFlags":                      {flags},
		"flowMatrix":                        {"turns an execution error into an empty result with no error ([ops] analytics/resolve.go:549-556; design D.3)"},
		"improveOpportunities":              {"not in slice 1: no resolver path read in step 0 (default REFUSED, design D.4)"},
		"operatingReview":                   {"its investment part reads investment_metrics_daily, neither the acr basis nor the canonical mix ([ops] operatingreview/operatingreview.go:840; K14 ruling, table K14-A)"},
		"pr":                                {personData},
		"reviewEdges":                       {personData},
		"releaseImpact":                     {noScope},
		"coverageBaselines":                 {noScope},
		"coverageScopeBaseline":             {noScope},
		"investmentEvidenceQuality":         {noScope},
		"testOpsCoverage":                   {noScope},
		"testopsJobFailures":                {noScope},
		"sourceHealth":                      {noScope},
		"testOpsPipeline":                   {noScope},
		"testOpsTest":                       {noScope},
		"testopsRisk":                       {noScope},
		"securityAlerts":                    {rootDark},
		"reportRuns":                        {userOwned},
		"savedReport":                       {userOwned},
		"savedReports":                      {userOwned},
		"workUnitTeamAttributions":          {"source can be a membership source; memberCount; evidence is free JSON (design D.3)"},
	}
}
