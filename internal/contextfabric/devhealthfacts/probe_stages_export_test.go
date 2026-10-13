package devhealthfacts

import "strings"

func probeCut(s, from, to string, keepTo bool) string {
	i := strings.Index(s, from)
	if i < 0 {
		panic("probe: start not found: " + from)
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		panic("probe: end not found: " + to)
	}
	if keepTo {
		j += len(to)
	}
	return s[i : i+j]
}

func ProbeRequestedStages() map[string]string {
	full := repoMixStatementScoped([]factTimeBound{{}}, subqueryMembershipScope, 100000)
	rowLevel := probeCut(full, "SELECT work_unit_id, computed_at,", "\n\t\t\t\t\t)\n\t\t\t\t\tGROUP BY work_unit_id", false)
	agg := probeCut(full, "SELECT work_unit_id,\n\t\t\t\t\t\targMaxIf", "HAVING argMax(reaches, computed_at)", true)
	fold := probeCut(full, "SELECT work_unit_id,\n\t\tany(effort_value)", "\n\tGROUP BY work_unit_id", true)
	return map[string]string{
		"a0_rows":         "SELECT count(), sum(reaches), sum(length(pr_refs)) FROM (" + rowLevel + ")",
		"a0_rows_noreach": "SELECT count(), sum(length(pr_refs)) FROM (" + strings.Replace(rowLevel, "arrayExists(r -> "+requestedRepositoryReach+", "+repoRefsExpression+") AS reaches", "1 AS reaches", 1) + ")",
		"a1_agg":          "SELECT count(), sum(length(latest.7)), sum(latest.4) FROM (" + agg + ")",
		"a1_agg_nohaving": "SELECT count(), sum(length(latest.7)), sum(latest.4) FROM (" + strings.Replace(agg, "HAVING argMax(reaches, computed_at)", "", 1) + ")",
		"c_fold":          "SELECT count(), sum(length(pairs)), sum(length(mapKeys(theme_distribution_json))) FROM (" + fold + ")",
		"z_full":          full,
	}
}
