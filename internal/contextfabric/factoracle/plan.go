package factoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// pathRule says what the value pair of a root does with one ops output path,
// or with a block of them.
type pathRule struct {
	// Path is an output path, or the prefix of a block of output paths.
	Path string
	// Reason is why the path is not compared with an acr fact. A rule with
	// no reason is a path the value pair compares: a run that compares
	// nothing on it is not a measurement.
	Reason string
}

func (r pathRule) covers(path string) bool {
	return path == r.Path || strings.HasPrefix(path, r.Path+".") || strings.HasPrefix(path, r.Path+"[*]")
}

const (
	byDesign = "forecast_by_design: computed on demand; acr has no on-demand forecast"
	noField  = "no acr fact field"
)

// valuePaths is, for every value root, the plan of every output path of the
// root's operations: compared, or excluded with a reason. checkValuePaths
// holds it against the production policy, so an output path the policy gains
// stops the run until it is compared or excluded here. A __typename path
// needs no rule: the shape pass types it.
var valuePaths = map[string][]pathRule{
	"analytics": {
		{Path: "analytics.breakdowns[*].dimension"},
		{Path: "analytics.breakdowns[*].measure"},
		{Path: "analytics.breakdowns[*].items[*].key"},
		{Path: "analytics.breakdowns[*].items[*].value"},
		{Path: "analytics.evidenceQualityDistribution", Reason: noField},
		{Path: "analytics.evidenceQualityStats", Reason: noField},
		{Path: "analytics.sankey", Reason: "refused shape (design K14-A): sankey is null"},
	},
	"capacityForecasts": {
		{Path: "capacityForecasts.edges[*].node.forecastId"},
		{Path: "capacityForecasts.edges[*].node.computedAt"},
		{Path: "capacityForecasts.edges[*].node.workScopeId"},
		{Path: "capacityForecasts.edges[*].node.backlogSize"},
		{Path: "capacityForecasts.edges[*].node.p50Days"},
		{Path: "capacityForecasts.edges[*].node.throughputMean"},
		{Path: "capacityForecasts.edges[*].node.throughputStddev"},
		{Path: "capacityForecasts.edges[*].node.insufficientHistory"},
		{Path: "capacityForecasts.edges[*].node.highVariance"},
		{Path: "capacityForecasts.edges[*].node.teamId", Reason: "the team of the request filter; the list is read team by team"},
		{Path: "capacityForecasts.edges[*].node.targetItems", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.targetDate", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p50Date", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p85Date", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p95Date", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p85Days", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p95Days", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p50Items", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p85Items", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.p95Items", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].node.historyDays", Reason: "forecast_by_design: the acr workload fact carries the p50 day count only"},
		{Path: "capacityForecasts.edges[*].cursor", Reason: "list paging; the acr fact is not a list"},
		{Path: "capacityForecasts.pageInfo", Reason: "list paging; the acr fact is not a list"},
		{Path: "capacityForecasts.totalCount", Reason: "list paging; the acr fact is not a list"},
	},
	"catalog": {
		{Path: "catalog.values[*].value"},
		{Path: "catalog.values[*].count", Reason: noField},
	},
	"compoundingRisk": {
		{Path: "compoundingRisk.rows[*].day"},
		{Path: "compoundingRisk.rows[*].scopeId"},
		{Path: "compoundingRisk.rows[*].score"},
		{Path: "compoundingRisk.rows[*].severity"},
		{Path: "compoundingRisk.rows[*].computedAt"},
		{Path: "compoundingRisk.rows[*].components.churnNorm"},
		{Path: "compoundingRisk.rows[*].components.complexityNorm"},
		{Path: "compoundingRisk.rows[*].components.ownershipNorm"},
		{Path: "compoundingRisk.rows[*].components.reviewNorm"},
		{Path: "compoundingRisk.rows[*].weights.churn"},
		{Path: "compoundingRisk.rows[*].weights.complexity"},
		{Path: "compoundingRisk.rows[*].weights.ownership"},
		{Path: "compoundingRisk.rows[*].weights.review"},
		{Path: "compoundingRisk.orgId", Reason: "request echo"},
		{Path: "compoundingRisk.breakout", Reason: "request echo"},
		{Path: "compoundingRisk.generatedAt", Reason: "the time of the request"},
		{Path: "compoundingRisk.rows[*].scope", Reason: "the breakout of the request; the acr fact is read by subject kind"},
		{Path: "compoundingRisk.rows[*].scopeLabel", Reason: "a name; the acr fact carries the subject id"},
		{Path: "compoundingRisk.rows[*].components.reworkChurn", Reason: "raw component; " + noField},
		{Path: "compoundingRisk.rows[*].components.complexityDelta", Reason: "raw component; " + noField},
		{Path: "compoundingRisk.rows[*].components.ownershipGini", Reason: "raw component; " + noField},
		{Path: "compoundingRisk.rows[*].components.singleOwnerRatio", Reason: "raw component; " + noField},
		{Path: "compoundingRisk.rows[*].components.reviewLatencyP90h", Reason: "raw component; " + noField},
		{Path: "compoundingRisk.rows[*].thresholds", Reason: noField},
		{Path: "compoundingRisk.trend", Reason: "an organization average per day; no acr fact"},
	},
	"throughputForecast": {
		{Path: "throughputForecast.estimateCoverage.ratio"},
		{Path: "throughputForecast.estimateCoverage.estimatedCount"},
		{Path: "throughputForecast.estimateCoverage.unestimatedCount"},
		{Path: "throughputForecast.estimateCoverage.backlogSize"},
		{Path: "throughputForecast.forecastId", Reason: byDesign},
		{Path: "throughputForecast.computedAt", Reason: byDesign},
		{Path: "throughputForecast.teamId", Reason: "the team of the request"},
		{Path: "throughputForecast.workScopeId", Reason: "a variable acr refuses; always null"},
		{Path: "throughputForecast.backlogSize", Reason: byDesign},
		{Path: "throughputForecast.historyWeeks", Reason: "request echo"},
		{Path: "throughputForecast.p50Weeks", Reason: byDesign},
		{Path: "throughputForecast.p75Weeks", Reason: byDesign},
		{Path: "throughputForecast.p90Weeks", Reason: byDesign},
		{Path: "throughputForecast.insufficientHistory", Reason: byDesign},
		{Path: "throughputForecast.rollingWindows", Reason: byDesign},
		{Path: "throughputForecast.primaryRisk", Reason: "derived risk signal; no acr fact"},
		{Path: "throughputForecast.wipCongestion", Reason: "derived risk signal; no acr fact"},
		{Path: "throughputForecast.staleWip", Reason: "derived risk signal; no acr fact"},
		{Path: "throughputForecast.reviewBottleneck", Reason: "derived risk signal; no acr fact"},
		{Path: "throughputForecast.incidentLoad", Reason: "derived risk signal; no acr fact"},
	},
}

// valueNotes are statements about a value pair that are not an output path.
var valueNotes = map[string]map[string]string{
	"analytics": {
		"analytics.breakdowns[*] (dimension SUBCATEGORY, WORK_TYPE)": "acr investment facts carry the theme mix only: shape only",
		"breakdown dimension TEAM, REPO":                             "refused by acr (design K14-A, basis_dependent_shape): not reachable through graphql_query",
	},
	"catalog": {
		"catalog (dimension TEAM)":          "no acr fact kind has a team identity (team is a graph subject): shape only",
		"catalog (THEME, SUBCATEGORY, ...)": "value sets of the investment source with org-wide counts; no acr organization fact: shape only",
	},
}

// rootOutputs lists the output paths of every operation behind a root.
func rootOutputs(policy *directread.GraphQLPolicy, root *directread.GraphQLRootPolicy) []string {
	seen := map[string]bool{}
	for _, name := range root.Operations() {
		if op, refusal := policy.Catalogue().Lookup(name); refusal == nil && op != nil {
			for _, out := range op.Outputs {
				if out.BeyondDocument {
					continue
				}
				seen[out.Path] = true
			}
		}
	}
	return sortedKeys(seen)
}

// checkValuePaths holds valuePaths against the production policy: every
// output path of a value root has exactly one rule, and every rule covers an
// output path.
func checkValuePaths(policy *directread.GraphQLPolicy) error {
	for _, root := range policy.Roots() {
		pair, ok := rootPairs[root.Field]
		if !ok || pair.Mode != ModeValue {
			if _, planned := valuePaths[root.Field]; planned {
				return fmt.Errorf("root %s has an output path plan and is not a value root", root.Field)
			}
			continue
		}
		rules := valuePaths[root.Field]
		used := make([]bool, len(rules))
		for _, path := range rootOutputs(policy, root) {
			if strings.HasSuffix(path, ".__typename") {
				continue
			}
			covering := 0
			for i, rule := range rules {
				if rule.covers(path) {
					covering++
					used[i] = true
				}
			}
			if covering != 1 {
				return fmt.Errorf("output path %s of value root %s has %d rules in valuePaths: compare it or exclude it with a reason", path, root.Field, covering)
			}
		}
		for i, rule := range rules {
			if !used[i] {
				return fmt.Errorf("valuePaths rule %s of root %s covers no output path of the policy", rule.Path, root.Field)
			}
		}
	}
	return nil
}

// excludedPaths is the report form of a root's plan.
func excludedPaths(root string) map[string]string {
	out := map[string]string{}
	for _, rule := range valuePaths[root] {
		if rule.Reason != "" {
			out[rule.Path] = rule.Reason
		}
	}
	for note, reason := range valueNotes[root] {
		out[note] = reason
	}
	return out
}

// checkTouched holds what the value pair of a root compared against its
// plan: every compared path was compared at least once, and nothing outside
// the plan was compared. An acr-only check names its path "acr:...".
func checkTouched(rr *RootReport) {
	rules := valuePaths[rr.Root]
	for _, rule := range rules {
		if rule.Reason != "" {
			continue
		}
		if rr.touched[rule.Path] == 0 {
			rr.invalid("the output path %s is planned as compared and the run compared nothing on it", rule.Path)
		}
	}
	paths := make([]string, 0, len(rr.touched))
	for path := range rr.touched {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if strings.HasPrefix(path, "acr:") {
			continue
		}
		planned := false
		for _, rule := range rules {
			if rule.Reason == "" && rule.Path == path {
				planned = true
			}
		}
		if !planned {
			rr.invalid("the value pair compared %s, which is not a compared path of the plan", path)
		}
	}
}

// factPlan is, per fact kind, what the value pairs do with every field and
// every table column the real provider serves: "" for a compared one, else
// the reason it is not compared. checkFactPlan holds every fact the oracle
// reads against it, so a field a provider gains stops the run until it is
// compared or excluded here.
var factPlan = map[string]map[string]string{
	"investment": {
		"theme_breakdown.theme":           "",
		"theme_breakdown.weighted_effort": "",
		"theme_breakdown.share":           "the share of the compared weighted effort; ops serves no share",
		"theme_breakdown.source":          "a label of the mix source",
		"theme_breakdown.attribution":     "a label of the attribution basis",
		"attribution_basis":               "a label of the attribution basis",
		"mix_source":                      "a label of the mix source",
		"theme_feature_delivery":          "the share of the compared weighted effort; ops serves no share",
		"theme_maintenance":               "the share of the compared weighted effort; ops serves no share",
		"theme_operational":               "the share of the compared weighted effort; ops serves no share",
		"theme_quality":                   "the share of the compared weighted effort; ops serves no share",
		"theme_quality_bugfix":            "the bug fix share; the ops THEME breakdown has no such value",
		"theme_risk":                      "the share of the compared weighted effort; ops serves no share",
		"work_unit_count":                 "a count the ops THEME breakdown does not serve",
		"owned_repository_count":          "a count of the ownership rows; the roll-up compares the mix of the owned repositories",
		"prior_theme_feature_delivery":    "the mix of the period before the window; not read",
		"prior_theme_maintenance":         "the mix of the period before the window; not read",
		"prior_theme_operational":         "the mix of the period before the window; not read",
		"prior_theme_quality":             "the mix of the period before the window; not read",
		"prior_theme_risk":                "the mix of the period before the window; not read",
		"next_cursor":                     "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"page_share_total":                "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"repository_id":                   "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"scope_share_total":               "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"scope_unit_rows":                 "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"share_in_scope":                  "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_attribution_basis":          "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_effort_value":               "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_from":                       "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_kind":                       "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_mix_source":                 "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_pull_request_count":         "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_refs_unresolved":            "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_theme_feature_delivery":     "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_theme_maintenance":          "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_theme_operational":          "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_theme_quality":              "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_theme_risk":                 "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_to":                         "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_unresolved_refs":            "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"unit_weight":                     "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"units_refs_unresolved":           "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"units_returned":                  "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
		"work_unit_id":                    "the work-unit listing, served only when read_facts is asked for units; the oracle reads the mix, not the listing",
	},
	"identity": {
		"name":     "",
		"id":       "the subject id of the read",
		"provider": "the ops catalogue value carries no provider",
	},
	"health": {
		"compounding_risk":                 "",
		"computed_at":                      "",
		"severity":                         "",
		"severity_as_of":                   "",
		"risk_rules.signal":                "",
		"risk_rules.weight":                "",
		"risk_rules.norm_value":            "",
		"risk_rules.weighted_contribution": "the product of the two compared columns",
		"severity_freshness_window_days":   "a constant of the acr freshness rule",
		"severity_unavailable_reason":      "stated in the report for a fact with no day",
		"daily_health.day":                 "a series; the ops trend is an organization average",
		"daily_health.severity":            "a series; the ops trend is an organization average",
		"daily_health.compounding_risk":    "a series; the ops trend is an organization average",
		"daily_health_omitted_count":       "rows the provider row cap left out of a table; a windowed read that is cut is refused, and a cut table is not summed",
	},
	"workload": {
		"backlog_size":                     "",
		"computed_at":                      "",
		"forecast_p50_days":                "",
		"high_variance":                    "",
		"insufficient_history":             "",
		"throughput_mean":                  "",
		"throughput_stddev":                "",
		"work_scope_id":                    "",
		"daily_workload.day":               "",
		"daily_workload.backlog_size":      "",
		"daily_workload.throughput_mean":   "",
		"daily_workload.throughput_stddev": "",
		"basis":                            "a label of the fact's basis",
		"daily_workload_omitted_count":     "rows the provider row cap left out of a table; a windowed read that is cut is refused, and a cut table is not summed",
	},
	"readiness": {
		"day":                     "",
		"estimated_count":         "",
		"unestimated_count":       "",
		"backlog_size":            "",
		"work_scope_id":           "the partition key of the fact",
		"provider":                "the partition key of the fact",
		"estimate_coverage_ratio": "the ratio of one work scope; the team ratio is made from the summed counts, as ops makes it",
		"basis":                   "a label of the fact's basis",
		// The daily series. The current read omits tables; with no window the
		// series runs over all time and the row cap cuts it, which the count
		// states. The compared facts are held against the store rows.
		"daily_readiness.day":                     "the daily series; the current read omits tables",
		"daily_readiness.estimated_count":         "the daily series; the current read omits tables",
		"daily_readiness.unestimated_count":       "the daily series; the current read omits tables",
		"daily_readiness.backlog_size":            "the daily series; the current read omits tables",
		"daily_readiness.estimate_coverage_ratio": "the daily series; the current read omits tables",
		"daily_readiness_omitted_count":           "rows the provider row cap left out of the daily series, which is not read; the compared facts are held against the store",
	},
	"flow": {
		"items_started_window":                   "",
		"items_started_latest_day":               "",
		"items_completed_window":                 "",
		"items_completed_latest_day":             "",
		"daily_flow.items_started":               "",
		"daily_flow.items_completed":             "",
		"scope_breakdown.items_started":          "",
		"scope_breakdown.items_completed":        "",
		"scope_count":                            "a count of the scope table rows",
		"window_days":                            "the read window, stated by the fact",
		"window_days_with_data":                  "the read window, stated by the fact",
		"window_start":                           "the read window, stated by the fact",
		"window_end":                             "the read window, stated by the fact",
		"window_mode":                            "the read window, stated by the fact",
		"daily_flow.day":                         "the key of the summed series",
		"daily_flow.wip_count_end_of_day":        "not a count the fact names for the window",
		"daily_flow.bug_completed_ratio":         "not a count the fact names for the window",
		"daily_flow.story_points_completed":      "not a count the fact names for the window",
		"scope_breakdown.provider":               "the key of the scope table",
		"scope_breakdown.work_scope_id":          "the key of the scope table",
		"scope_breakdown.day":                    "the key of the scope table",
		"scope_breakdown.wip_count_end_of_day":   "not a count the fact names for the window",
		"scope_breakdown.bug_completed_ratio":    "not a count the fact names for the window",
		"scope_breakdown.story_points_completed": "not a count the fact names for the window",
		"scope_breakdown.wip_age_p50_hours":      "not a count the fact names for the window",
		"scope_breakdown.wip_age_p90_hours":      "not a count the fact names for the window",
		"scope_breakdown.cycle_time_p50_hours":   "not a count the fact names for the window",
		"scope_breakdown.cycle_time_p90_hours":   "not a count the fact names for the window",
		"scope_breakdown.lead_time_p50_hours":    "not a count the fact names for the window",
		"scope_breakdown.lead_time_p90_hours":    "not a count the fact names for the window",
		"daily_flow_omitted_count":               "rows the provider row cap left out of a table; a windowed read that is cut is refused, and a cut table is not summed",
		"daily_flow_omitted_reason":              "rows the provider row cap left out of a table; a windowed read that is cut is refused, and a cut table is not summed",
		"scope_breakdown_omitted_count":          "rows the provider row cap left out of a table; a windowed read that is cut is refused, and a cut table is not summed",
		// A flow fact of an acr build from before the counts were named.
		"items_started":   "the headline count of an older acr build; such a fact is stated as not joined",
		"items_completed": "the headline count of an older acr build; such a fact is stated as not joined",
	},
}

// factReads are the fact kinds the value pairs read, and the subject kinds
// they read them for.
var factReads = map[string][]contextfabric.SubjectKind{
	"investment": {contextfabric.SubjectRepository, contextfabric.SubjectTeam},
	"identity":   {contextfabric.SubjectRepository},
	"health":     {contextfabric.SubjectRepository, contextfabric.SubjectTeam},
	"workload":   {contextfabric.SubjectTeam},
	"readiness":  {contextfabric.SubjectTeam},
	"flow":       {contextfabric.SubjectTeam},
}

// declaredFactFields lists, from the providers' own field declaration
// (FactCapability.Fields), every field and every table column a fact of the
// kinds and subject kinds of factReads can carry.
func declaredFactFields(providers []contextfabric.FactProvider) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, provider := range providers {
		capability := provider.Capability()
		kinds, read := factReads[string(capability.Kind)]
		if !read {
			continue
		}
		names := map[string]bool{}
		for _, field := range capability.Fields {
			applies := false
			for _, kind := range kinds {
				applies = applies || field.AppliesTo(kind)
			}
			if !applies {
				continue
			}
			if field.Type != contextfabric.FactFieldTable {
				names[field.Name] = true
				continue
			}
			for _, column := range field.Columns {
				names[field.Name+"."+column.Name] = true
			}
		}
		out[string(capability.Kind)] = names
	}
	return out
}

// checkFactPlanDeclared holds factPlan against the providers' declaration:
// every declared field and table column of a kind the oracle reads is
// compared or excluded in the plan, and the plan names nothing a provider
// does not declare. The one exception is a field of an older acr build, which
// the plan names so that a venue that runs that build can still be read.
func checkFactPlanDeclared(providers []contextfabric.FactProvider) error {
	declared := declaredFactFields(providers)
	for kind := range factReads {
		names, ok := declared[kind]
		if !ok {
			return fmt.Errorf("no provider declares the fact kind %s the oracle reads", kind)
		}
		var missing, stale []string
		for name := range names {
			if _, planned := factPlan[kind][name]; !planned {
				missing = append(missing, name)
			}
		}
		for name := range factPlan[kind] {
			if !names[name] && !olderBuildFields[kind+"."+name] {
				stale = append(stale, name)
			}
		}
		sort.Strings(missing)
		sort.Strings(stale)
		if len(missing) > 0 {
			return fmt.Errorf("the %s provider declares %s, which factPlan neither compares nor excludes", kind, strings.Join(missing, ", "))
		}
		if len(stale) > 0 {
			return fmt.Errorf("factPlan names %s for kind %s, which the provider does not declare", strings.Join(stale, ", "), kind)
		}
	}
	for kind := range factPlan {
		if _, read := factReads[kind]; !read {
			return fmt.Errorf("factPlan has a plan for kind %s, which no value pair reads", kind)
		}
	}
	return nil
}

// olderBuildFields are fact fields this build no longer declares and an
// older acr build still serves.
var olderBuildFields = map[string]bool{
	"flow.items_started":   true,
	"flow.items_completed": true,
}

// checkFactPlan refuses a fact with a field or a table column that factPlan
// does not name.
func checkFactPlan(fact ServedFact) error {
	plan, ok := factPlan[fact.Kind]
	if !ok {
		return fmt.Errorf("fact kind %s has no plan in factPlan", fact.Kind)
	}
	var unknown []string
	for name := range fact.Fields {
		if _, planned := plan[name]; !planned {
			unknown = append(unknown, name)
		}
	}
	for name, table := range fact.Tables {
		for _, column := range table.Columns {
			if _, planned := plan[name+"."+column]; !planned {
				unknown = append(unknown, name+"."+column)
			}
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("a %s fact serves %s, which factPlan neither compares nor excludes", fact.Kind, strings.Join(unknown, ", "))
	}
	return nil
}

// temporaryAllowance is the allowance of the temporary class for one root.
type temporaryAllowance struct {
	// Operation serves the covered paths.
	Operation string
	// Paths are the covered output paths: a path, or the prefix of a block.
	Paths []string
	// Window is the client variable that sets how much history the operation
	// reads ("" when the operation takes no window), Narrow the second value
	// the probe asks for, and Echo the output path that states the history
	// the answer used.
	Window string
	Narrow int
	Echo   string
	// WideStated is the history the answer states for the request of the
	// case when the resolver rounds or clamps it (0: the answer states what was asked).
	// NarrowStated is the same for the second request.
	WideStated   int
	NarrowStated int
	// Contract is the digest of the operation's contract the allowance was
	// read against (contractDigest). A contract that changed is read again
	// before the allowance is renewed.
	Contract string
}

// temporaryAllowances are the ops output paths of the temporary class
// latest_day_vs_window: values of the latest day, or of all time, served
// beside a window with no label. Which paths they are is read from the code
// of the resolvers. What a run measures is that a covered value is the same
// for two different histories, while the answer states the two histories: the
// value does not follow the window the request names. A root with no window
// argument cannot be measured, and is stated as read from code, not counted.
// (The acr half of the class is gone: the flow fact names its window and its
// latest-day counts, and compareFlowWindow checks both.)
var temporaryAllowances = map[string]temporaryAllowance{
	"throughputForecast": {
		Operation: "throughputForecast", Window: "input.historyWeeks", Narrow: 4, Echo: "throughputForecast.historyWeeks",
		Paths:    []string{"throughputForecast.backlogSize", "throughputForecast.wipCongestion", "throughputForecast.staleWip", "throughputForecast.estimateCoverage"},
		Contract: "sha256:ad5d774fa899197d84842001d2f9fcee48155c5356a27238e1d726e1aff4db51",
	},
	"capacityForecast": {
		Operation: "capacityForecast", Window: "input.historyDays", Narrow: 30, Echo: "capacityForecast.historyDays", WideStated: 60, NarrowStated: 28,
		Paths:    []string{"capacityForecast.backlogSize"},
		Contract: "sha256:f93d3cf5c8b5ecd443fbacaeeb7a21aa84a06d7177f748732c2c06a13a8478dc",
	},
	"workGraphFlow": {
		Operation: "workGraphFlow",
		Paths:     []string{"workGraphFlow.rows[*].inflow", "workGraphFlow.rows[*].outflow"},
		Contract:  "sha256:d442757b8633c7f9542f48a92a34ef1dbcfacdd994e68c76ad7a31cce1428cf1",
	},
}

// contractDigest is the digest of what a client can send to an operation and
// what it can get back: the registered document, the variable rules and the
// output paths.
func contractDigest(op *directread.OperationPolicy) string {
	h := sha256.New()
	h.Write([]byte(op.DocumentText))
	for _, v := range op.Variables {
		fmt.Fprintf(h, "\nV %s %s %t %s", v.Path, v.Type, v.Allowed, v.Source)
	}
	for _, o := range op.Outputs {
		fmt.Fprintf(h, "\nO %s %s", o.Path, o.Type)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// temporaryPathsMissing lists the paths of spec that outputs no longer hold.
func temporaryPathsMissing(spec []string, outputs []string) []string {
	var missing []string
	for _, want := range spec {
		found := false
		for _, path := range outputs {
			if path == want || strings.HasPrefix(path, want+".") {
				found = true
			}
		}
		if !found {
			missing = append(missing, want)
		}
	}
	return missing
}
