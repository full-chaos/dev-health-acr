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

func ProbeRowStages() map[string]string {
	where := "\nFROM work_unit_investments WHERE org_id = {org_id:String}"
	filtered := where + supersededWorkUnitIDsFilter() + subqueryMembershipScope.filter()
	return map[string]string{
		"r1_count":        "SELECT count()" + where,
		"r2_json_len":     "SELECT sum(length(structural_evidence_json))" + where,
		"r3_prs":          "SELECT sum(length(JSONExtract(structural_evidence_json, 'prs', 'Array(String)')))" + where,
		"r4_prs_issues":   "SELECT sum(length(JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))) + sum(length(JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))" + where,
		"r5_parse":        "SELECT sum(length(" + prRefsExpression + "))" + where,
		"r6_parse_scoped": "SELECT sum(length(" + prRefsExpression + "))" + filtered,
		"r7_json_scoped":  "SELECT sum(length(structural_evidence_json))" + filtered,
		"r8_maps":         "SELECT sum(length(mapKeys(theme_distribution_json))) + sum(length(mapKeys(subcategory_distribution_json)))" + where,
	}
}
