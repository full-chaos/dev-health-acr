package factoracle

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Float tolerances. Each one is a declared rule of a pair, with its reason;
// a float pair with no rule here is compared exactly.
const (
	// relSum: a sum the two planes add in a different order.
	relSum = 1e-9
	// relOpsFloat32: ops casts the subcategory share to Float32 before it
	// multiplies by the effort (analytics investmentContextFor ARRAY JOIN).
	relOpsFloat32 = 1e-6
)

func scaled(rel float64, values ...float64) float64 {
	scale := 1.0
	for _, v := range values {
		scale = math.Max(scale, math.Abs(v))
	}
	return rel * scale
}

// themeTolerance is the tolerance of a compare with an ops theme value on
// one side: ops makes it from Float32 shares.
func themeTolerance(values ...float64) float64 { return scaled(relOpsFloat32, values...) }

// sumTolerance is the tolerance of a compare between the acr repository mix
// and what the store rows give for it. Both are Float64 sums of the same
// products, added in another order; the Float32 rule of ops has no part in
// either.
func sumTolerance(values ...float64) float64 { return scaled(relSum, values...) }

// themeEffort reads theme -> weighted_effort from an investment fact's
// theme_breakdown table.
func themeEffort(fact ServedFact) (map[string]float64, error) {
	table, ok := fact.Tables["theme_breakdown"]
	if !ok {
		return nil, fmt.Errorf("investment fact of %s has no theme_breakdown table", fact.Subject.Kind)
	}
	theme, effort := tableColumn(table, "theme"), tableColumn(table, "weighted_effort")
	if theme < 0 || effort < 0 {
		return nil, fmt.Errorf("theme_breakdown has no theme or weighted_effort column")
	}
	if table.TruncatedBy != "" || table.RowsOmitted != 0 {
		return nil, fmt.Errorf("theme_breakdown is truncated")
	}
	out := map[string]float64{}
	for _, row := range table.Rows {
		name, ok := row[theme].(string)
		if !ok {
			return nil, fmt.Errorf("theme_breakdown: theme is not a string")
		}
		leaf, err := typedLeaf(LeafFloat, row[effort])
		if err != nil {
			return nil, fmt.Errorf("theme_breakdown: weighted_effort: %w", err)
		}
		value, _ := leafFloat(leaf)
		out[name] = value
	}
	return out, nil
}

func themeKeys(maps ...map[string]float64) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	return sortedKeys(seen)
}

// investmentVerdict is the outcome of the organization investment compare.
type investmentVerdict struct {
	Matches     int
	Differences []Difference
	Findings    []string
	// Residual is ops minus the acr repository sum, per theme.
	Residual map[string]float64
}

// regressionClasses are the investment classes acr is aligned with ops on
// (ruling K16). They appear only when the acr reader loses the rule.
var regressionClasses = []Class{ClassSupersession, ClassMembershipScope, ClassNullableArgmax}

// classifyInvestment compares, per theme, the ops organization value, the
// sum of the acr repository mixes and the sum the store rows give for those
// mixes (expected).
//
// When acr equals expected, what is left between ops and acr is the effort
// that reaches no repository: the accepted class attribution_basis. When acr
// is off expected, the difference is named only if it EQUALS the witness of
// exactly one regression class in every theme; a difference a witness merely
// bounds is not named. Everything else is a finding.
func classifyInvestment(ops, acr, expected map[string]float64, witnesses Witnesses) investmentVerdict {
	verdict := investmentVerdict{Residual: map[string]float64{}}
	themes := themeKeys(ops, acr, expected)
	excess := map[string]float64{}
	off := false
	for _, theme := range themes {
		verdict.Residual[theme] = ops[theme] - acr[theme]
		d := acr[theme] - expected[theme]
		excess[theme] = d
		if math.Abs(d) > sumTolerance(acr[theme], expected[theme]) {
			off = true
		}
	}
	if !off {
		for _, theme := range themes {
			residual := verdict.Residual[theme]
			tol := themeTolerance(ops[theme], acr[theme])
			switch {
			case residual > tol:
				verdict.Differences = append(verdict.Differences, Difference{
					Pair: "investment_org", Key: theme, Class: ClassAttributionBasis, Exact: true,
					Detail: "ops organization value minus the sum of the acr repository mixes: effort that reaches no repository",
					Values: map[string]float64{"residual": residual, "ops": ops[theme], "acr_repository_sum": acr[theme]},
				})
			case residual < -tol:
				verdict.Findings = append(verdict.Findings, fmt.Sprintf("theme %s: the acr repository sum is above the ops organization value by %s", theme, formatFloat(-residual)))
			default:
				verdict.Matches++
			}
		}
		return verdict
	}
	var named []Class
	for _, class := range regressionClasses {
		witness := witnesses[class]
		equal := true
		for _, theme := range themes {
			if math.Abs(excess[theme]-witness[theme]) > sumTolerance(acr[theme], expected[theme], witness[theme]) {
				equal = false
			}
		}
		if equal {
			named = append(named, class)
		}
	}
	if len(named) != 1 {
		verdict.Findings = append(verdict.Findings, fmt.Sprintf(
			"the acr repository sum is off what the store rows give by %s and it equals the witness of %d classes %v", formatThemes(excess), len(named), named))
		return verdict
	}
	verdict.Differences = append(verdict.Differences, Difference{
		Pair: "investment_org", Key: "all themes", Class: named[0], Exact: true, Values: excess,
		Detail: "the acr repository mixes hold effort the ops reading leaves out; it equals the store witness of this class",
	})
	return verdict
}

func formatThemes(values map[string]float64) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatFloat(values[k])))
	}
	return strings.Join(parts, " ")
}

const investmentShape = "analytics/investmentBreakdown/all"

// compareInvestment is root analytics: the ops organization breakdown by
// theme under CHURN_LOC against the acr repository investment facts, and the
// acr team mix against the repositories the team owns.
func compareInvestment(ctx context.Context, o *Oracle, rr *RootReport) error {
	root, ok, err := o.served(ctx, rr, "investment_org", investmentShape, investmentVariables(o.Window, "THEME"))
	if err != nil || !ok {
		return err
	}
	ops := map[string]float64{}
	breakdowns := items(root["breakdowns"])
	if len(breakdowns) != 1 || breakdowns[0]["dimension"] != "THEME" || breakdowns[0]["measure"] != "CHURN_LOC" {
		rr.find(Finding{Pair: "investment_org", Detail: "echo: the answer is not one THEME breakdown under CHURN_LOC"})
		return nil
	}
	rr.touch("analytics.breakdowns[*].dimension", "analytics.breakdowns[*].measure")
	for _, item := range items(breakdowns[0]["items"]) {
		key, _ := item["key"].(string)
		rr.touch("analytics.breakdowns[*].items[*].key", "analytics.breakdowns[*].items[*].value")
		leaf, lerr := typedLeaf(LeafFloat, item["value"])
		if lerr != nil || leaf.T == LeafNull {
			rr.find(Finding{Pair: "investment_org", Key: key, Detail: "ops item value is not a number"})
			return nil
		}
		ops[key], _ = leafFloat(leaf)
	}

	// The reply and the store must describe the same rows before anything
	// is said about acr: the ops value is recomputed from the store rows.
	fromStore := o.Store.OrgThemeEffort(o.Window)
	for _, theme := range themeKeys(ops, fromStore) {
		rr.Compared++
		if math.Abs(ops[theme]-fromStore[theme]) <= themeTolerance(ops[theme], fromStore[theme]) {
			rr.Matches++
			continue
		}
		rr.find(Finding{Pair: "ops_reply_vs_store", Key: theme, Detail: fmt.Sprintf("ops answered %s, the store rows give %s", formatFloat(ops[theme]), formatFloat(fromStore[theme]))})
	}

	repoFacts, err := o.facts(ctx, "investment", "repository", o.Store.RepositoryIDs())
	if err != nil {
		return err
	}
	acr := map[string]float64{}
	byRepo := map[string]map[string]float64{}
	for _, fact := range repoFacts {
		effort, terr := themeEffort(fact)
		if terr != nil {
			return terr
		}
		byRepo[strings.ToLower(bareID(fact.Subject.CanonicalID))] = effort
		for theme, value := range effort {
			acr[theme] += value
		}
	}
	o.RepositoryEffort = byRepo
	expectedByRepo := o.Store.ExpectedRepositoryEffort(o.Window)
	expected := map[string]float64{}
	for _, effort := range expectedByRepo {
		for theme, value := range effort {
			expected[theme] += value
		}
	}
	verdict := classifyInvestment(ops, acr, expected, o.Store.Witnesses(o.Window))
	rr.Compared += len(themeKeys(ops, acr))
	rr.Matches += verdict.Matches
	for _, d := range verdict.Differences {
		rr.differ(d)
	}
	for _, detail := range verdict.Findings {
		rr.find(Finding{Pair: "investment_org", Detail: detail})
	}
	o.Residual, rr.Residual = verdict.Residual, verdict.Residual
	// Repository by repository, when the sums agree: effort that moved from
	// one repository to another is not seen in a sum.
	if len(verdict.Differences) == 0 || verdict.Differences[0].Class == ClassAttributionBasis {
		moved := 0
		for _, repo := range o.Store.RepositoryIDs() {
			for _, theme := range themeKeys(byRepo[repo], expectedByRepo[repo]) {
				rr.Compared++
				if math.Abs(byRepo[repo][theme]-expectedByRepo[repo][theme]) <= sumTolerance(byRepo[repo][theme], expectedByRepo[repo][theme]) {
					rr.Matches++
				} else {
					moved++
				}
			}
		}
		if moved > 0 {
			rr.find(Finding{Pair: "investment_repository", Detail: fmt.Sprintf("%d repository and theme values of the acr mix are off what the store rows give", moved)})
		}
	}
	return compareTeamRollup(ctx, o, rr, byRepo)
}

// compareTeamRollup is the acr team mix against the sum of the repository
// mixes of the repositories the team owns in the store. It has no ops side:
// acr refuses the TEAM shapes of the investment operations (design K14-A).
// Its named class is null_repo_id: a team that lacks exactly the mix of the
// repositories only an ownership row with no repo_id names.
func compareTeamRollup(ctx context.Context, o *Oracle, rr *RootReport, byRepo map[string]map[string]float64) error {
	owned, byNameOnly, err := o.Store.ownedRepositories(o.Window.End)
	if err != nil {
		return err
	}
	teams := make([]string, 0, len(owned))
	for team := range owned {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	if len(teams) == 0 {
		return nil
	}
	facts, err := o.facts(ctx, "investment", "team", teams)
	if err != nil {
		return err
	}
	got := map[string]map[string]float64{}
	for _, fact := range facts {
		effort, terr := themeEffort(fact)
		if terr != nil {
			return terr
		}
		got[bareID(fact.Subject.CanonicalID)] = effort
	}
	sum := func(repos map[string]bool) map[string]float64 {
		out := map[string]float64{}
		for repo := range repos {
			for theme, value := range byRepo[repo] {
				out[theme] += value
			}
		}
		return out
	}
	for _, team := range teams {
		want, nameOnly := sum(owned[team]), sum(byNameOnly[team])
		themes := themeKeys(want, got[team])
		deficit := map[string]float64{}
		equal, explained := true, true
		for _, theme := range themes {
			tol := relSum * math.Max(1, math.Max(math.Abs(want[theme]), math.Abs(got[team][theme])))
			d := want[theme] - got[team][theme]
			if math.Abs(d) > tol {
				equal = false
				deficit[theme] = d
			}
			if math.Abs(d-nameOnly[theme]) > tol {
				explained = false
			}
		}
		rr.Compared += len(themes)
		switch {
		case equal:
			rr.Matches += len(themes)
		case explained:
			rr.differ(Difference{Pair: "investment_team_rollup", Key: "team", Class: ClassNullRepoID, Exact: true, Values: deficit,
				Detail: "the team mix lacks exactly the mix of the repositories that only an ownership row with no repo_id names"})
		default:
			rr.find(Finding{Pair: "investment_team_rollup", Key: "team", Detail: "the team mix is not the sum of its owned repositories' mixes: " + formatThemes(deficit)})
		}
	}
	return nil
}

// compareCatalog is root catalog, dimension REPO: the ops repository slug
// list against the acr repository identity facts, as sets.
func compareCatalog(ctx context.Context, o *Oracle, rr *RootReport) error {
	root, ok, err := o.served(ctx, rr, "catalog_repositories", "catalog/acrRepositoryScopes/all", map[string]any{})
	if err != nil || !ok {
		return err
	}
	ops := map[string]bool{}
	for _, item := range items(root["values"]) {
		if value, isString := item["value"].(string); isString {
			ops[value] = true
		}
	}
	// The ops catalogue takes no window, so the acr side is the current read.
	facts, err := o.readFacts(ctx, "identity", "repository", o.Store.RepositoryIDs(), readCurrent)
	if err != nil {
		return err
	}
	acr := map[string]bool{}
	for _, fact := range facts {
		if name, isString := fact.Fields["name"].(string); isString {
			acr[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	all := map[string]bool{}
	for k := range ops {
		all[k] = true
	}
	for k := range acr {
		all[k] = true
	}
	for range sortedKeys(all) {
		rr.Compared++
		rr.touch("catalog.values[*].value")
	}
	missingInAcr, missingInOps := 0, 0
	for name := range all {
		switch {
		case ops[name] && acr[name]:
			rr.Matches++
		case ops[name]:
			missingInAcr++
		default:
			missingInOps++
		}
	}
	if missingInAcr > 0 || missingInOps > 0 {
		rr.find(Finding{Pair: "catalog_repositories", Detail: fmt.Sprintf("repository name sets differ: %d only in the ops catalogue, %d only in the acr identity facts", missingInAcr, missingInOps)})
	}
	return nil
}

var healthSignals = []string{"churn", "complexity", "ownership", "review"}

// compareHealth is root compoundingRisk: the ops row of a scope on the day
// the acr health fact states (severity_as_of) against that fact.
func compareHealth(ctx context.Context, o *Oracle, rr *RootReport) error {
	type scope struct{ kind, breakout, idsVariable, storeScope string }
	for _, sc := range []scope{{"repository", "REPO", "repoIds", "repo"}, {"team", "TEAM", "teamIds", "team"}} {
		ids := o.Store.RepositoryIDs()
		if sc.kind == "team" {
			ids = o.Store.TeamIDs()
		}
		if len(ids) == 0 {
			continue
		}
		facts, err := o.facts(ctx, "health", sc.kind, ids)
		if err != nil {
			return err
		}
		// Every subject with a risk row in the store must have a fact, and a
		// fact that states a day must have a row in the store: a subject that
		// is absent on one side is a finding, never skipped.
		inStore := o.Store.RiskSubjects(sc.storeScope)
		requested := map[string]bool{}
		for _, id := range ids {
			requested[strings.ToLower(id)] = true
		}
		hasFact := map[string]bool{}
		byDay := map[string][]ServedFact{}
		for _, fact := range facts {
			id := strings.ToLower(bareID(fact.Subject.CanonicalID))
			hasFact[id] = true
			day, ok := fact.Fields["severity_as_of"].(string)
			if !ok {
				reason, _ := fact.Fields["severity_unavailable_reason"].(string)
				rr.NotJoined = append(rr.NotJoined, sc.kind+": a health fact states no severity_as_of day ("+reason+")")
				continue
			}
			if !inStore[id] {
				rr.Compared++
				rr.find(Finding{Pair: "health_rows", Key: sc.kind, Detail: "an acr health fact states a day for a subject with no compounding risk row in the store"})
				continue
			}
			byDay[day] = append(byDay[day], fact)
		}
		for _, id := range sortedKeys(inStore) {
			switch {
			case !requested[id]:
				rr.NotJoined = append(rr.NotJoined, sc.kind+": a subject with compounding risk rows in the store is not a "+sc.kind+" of the organization tables")
			case !hasFact[id]:
				rr.Compared++
				rr.find(Finding{Pair: "health_rows", Key: sc.kind, Detail: "a subject with compounding risk rows in the store has no acr health fact"})
			}
		}
		days := make([]string, 0, len(byDay))
		for day := range byDay {
			days = append(days, day)
		}
		sort.Strings(days)
		for _, day := range days {
			subjects := make([]any, 0, len(byDay[day]))
			for _, fact := range byDay[day] {
				subjects = append(subjects, fact.Subject.CanonicalID)
			}
			variables := map[string]any{"filter": map[string]any{"breakout": sc.breakout, "day": day, sc.idsVariable: subjects, "trendDays": 1}}
			root, ok, serr := o.served(ctx, rr, "health_rows", "compoundingRisk/compoundingRisk/all", variables)
			if serr != nil {
				return serr
			}
			if !ok {
				continue
			}
			rows := map[string]map[string]any{}
			for _, row := range items(root["rows"]) {
				if id, isString := row["scopeId"].(string); isString {
					rows[strings.ToLower(id)] = row
					rr.touch("compoundingRisk.rows[*].scopeId")
				}
			}
			for _, fact := range byDay[day] {
				key := sc.kind + " on " + day
				row, found := rows[strings.ToLower(bareID(fact.Subject.CanonicalID))]
				if !found {
					rr.Compared++
					rr.find(Finding{Pair: "health_rows", Key: key, Detail: "the acr health fact has no ops row for its own day"})
					continue
				}
				compareHealthRow(rr, key, row, fact)
			}
		}
	}
	return nil
}

func compareHealthRow(rr *RootReport, key string, row map[string]any, fact ServedFact) {
	pair := "health_rows"
	both := func(path, opsKind string, opsValue any, acrKind string, acrValue any) {
		ops, opsErr := typedLeaf(opsKind, opsValue)
		acr, acrErr := typedLeaf(acrKind, acrValue)
		rr.leafPair(pair, key, path, ops, acr, opsErr, acrErr, 0)
	}
	const rows = "compoundingRisk.rows[*]."
	both(rows+"score", LeafFloat, row["score"], LeafFloat, fact.Fields["compounding_risk"])
	// ops prints the severity as its GraphQL enum name (upper case); the acr
	// fact carries the stored value (lower case). The enum name is lowered:
	// the two are one vocabulary in two spellings.
	opsSeverity := row["severity"]
	if name, ok := opsSeverity.(string); ok {
		opsSeverity = strings.ToLower(name)
	}
	both(rows+"severity", LeafString, opsSeverity, LeafString, fact.Fields["severity"])
	both(rows+"day", LeafDate, row["day"], LeafDate, fact.Fields["severity_as_of"])
	both(rows+"computedAt", LeafTime, row["computedAt"], LeafTime, fact.Fields["computed_at"])
	rules := fact.Tables["risk_rules"]
	signal, weight, norm := tableColumn(rules, "signal"), tableColumn(rules, "weight"), tableColumn(rules, "norm_value")
	components, _ := row["components"].(map[string]any)
	weights, _ := row["weights"].(map[string]any)
	for _, name := range healthSignals {
		var acrWeight, acrNorm any
		found := false
		if signal >= 0 && weight >= 0 && norm >= 0 {
			for _, r := range rules.Rows {
				if r[signal] == name {
					acrWeight, acrNorm, found = r[weight], r[norm], true
				}
			}
		}
		if !found {
			rr.Compared++
			rr.find(Finding{Pair: pair, Key: key, Path: "acr:health.risk_rules." + name, Detail: "the acr fact has no risk_rules row for the signal"})
			continue
		}
		both(rows+"components."+name+"Norm", LeafFloat, components[name+"Norm"], LeafFloat, acrNorm)
		both(rows+"weights."+name, LeafFloat, weights[name], LeafFloat, acrWeight)
	}
}

const nodePath = "capacityForecasts.edges[*].node."

type forecastNode struct {
	scope, id string
	computed  Leaf
	day       string
	row       map[string]any
}

// compareWorkload is root capacityForecasts: the stored forecast list of a
// team, reduced by acr's own row shape (one row per team, work scope and
// day: the latest by computed_at, then forecast id; design A1.1), against
// the acr workload facts.
func compareWorkload(ctx context.Context, o *Oracle, rr *RootReport) error {
	teams := map[string]bool{}
	for _, row := range o.Store.extract.Tables[tableCapacityForecasts] {
		if team, ok := rowString(row, "team_id"); ok && team != "" {
			teams[team] = true
		}
	}
	ids := sortedKeys(teams)
	if len(ids) == 0 {
		rr.NotJoined = append(rr.NotJoined, "no stored forecast with a team in the window")
		return nil
	}
	facts, err := o.facts(ctx, "workload", "team", ids)
	if err != nil {
		return err
	}
	byTeam := map[string][]ServedFact{}
	for _, fact := range facts {
		team := bareID(fact.Subject.CanonicalID)
		byTeam[team] = append(byTeam[team], fact)
	}
	const limit = 200
	collapsed := 0
	for _, team := range ids {
		variables := map[string]any{"filters": map[string]any{"teamId": "team:" + team, "fromDate": o.Window.startDate(), "toDate": o.Window.lastDay(), "limit": limit}}
		root, ok, serr := o.served(ctx, rr, "workload", "capacityForecasts/capacityForecasts/all", variables)
		if serr != nil {
			return serr
		}
		if !ok {
			continue
		}
		edges := items(root["edges"])
		if len(edges) == 0 && len(byTeam[team]) == 0 {
			// The store has forecast rows of the team in the window.
			rr.Compared++
			rr.find(Finding{Pair: "workload", Key: "team", Detail: "a team with forecast rows in the store has an ops list with no row and no acr workload fact"})
			continue
		}
		if len(edges) >= limit {
			rr.find(Finding{Pair: "workload", Key: "team", Detail: "the ops list is at its limit; the compare would be over a cut list"})
			continue
		}
		latest := map[string]forecastNode{}
		for _, edge := range edges {
			node, _ := edge["node"].(map[string]any)
			computed, cerr := typedLeaf(LeafTime, node["computedAt"])
			id, _ := node["forecastId"].(string)
			if cerr != nil || computed.T == LeafNull || id == "" {
				rr.find(Finding{Pair: "workload", Key: "team", Detail: "an ops forecast row has no computedAt or forecastId"})
				continue
			}
			scopeID, _ := node["workScopeId"].(string)
			rr.touch(nodePath+"forecastId", nodePath+"workScopeId")
			n := forecastNode{scope: scopeID, id: id, computed: computed, day: computed.V[:10], row: node}
			key := n.scope + "\x00" + n.day
			if prior, seen := latest[key]; !seen || n.computed.V > prior.computed.V || (n.computed.V == prior.computed.V && n.id > prior.id) {
				latest[key] = n
			}
		}
		collapsed += len(edges) - len(latest)
		compareWorkloadTeam(rr, latest, byTeam[team])
	}
	if collapsed > 0 {
		rr.differ(Difference{Pair: "workload", Key: "all teams", Class: ClassForecastByDesign, Exact: true,
			Values: map[string]float64{"ops_rows_collapsed": float64(collapsed)},
			Detail: "the ops list is the raw forecast log; acr keeps one row per team, work scope and day (dedup_shape)"})
	}
	return nil
}

func compareWorkloadTeam(rr *RootReport, latest map[string]forecastNode, facts []ServedFact) {
	pair := "workload"
	type dayTotal struct {
		backlog  int64
		mean, sq float64
	}
	days := map[string]*dayTotal{}
	scopeLatest := map[string]forecastNode{}
	for _, n := range latest {
		total := days[n.day]
		if total == nil {
			total = &dayTotal{}
			days[n.day] = total
		}
		if leaf, err := typedLeaf(LeafInt, n.row["backlogSize"]); err == nil && leaf.T == LeafInt {
			var v int64
			_, _ = fmt.Sscan(leaf.V, &v)
			total.backlog += v
		}
		if leaf, err := typedLeaf(LeafFloat, n.row["throughputMean"]); err == nil {
			v, _ := leafFloat(leaf)
			total.mean += v
		}
		if leaf, err := typedLeaf(LeafFloat, n.row["throughputStddev"]); err == nil {
			v, _ := leafFloat(leaf)
			total.sq += v * v
		}
		if prior, seen := scopeLatest[n.scope]; !seen || n.computed.V > prior.computed.V || (n.computed.V == prior.computed.V && n.id > prior.id) {
			scopeLatest[n.scope] = n
		}
	}

	// The daily series: every fact of a team carries the same table.
	var daily *ServedTable
	for i := range facts {
		if table, ok := facts[i].Tables["daily_workload"]; ok {
			daily = &table
			break
		}
	}
	acrDays := map[string][]any{}
	truncated := false
	if daily != nil {
		truncated = daily.TruncatedBy != "" || daily.RowsOmitted != 0
		dayColumn := tableColumn(*daily, "day")
		for _, row := range daily.Rows {
			if dayColumn >= 0 {
				if day, ok := row[dayColumn].(string); ok {
					acrDays[day] = row
				}
			}
		}
	}
	allDays := map[string]bool{}
	for day := range days {
		if !truncated || acrDays[day] != nil {
			allDays[day] = true
		}
	}
	for day := range acrDays {
		allDays[day] = true
	}
	for _, day := range sortedKeys(allDays) {
		total, row := days[day], acrDays[day]
		key := "team on " + day
		if total == nil || row == nil {
			rr.Compared++
			rr.find(Finding{Pair: pair, Key: key, Detail: fmt.Sprintf("daily series: day present in ops %t, in acr %t", total != nil, row != nil)})
			continue
		}
		backlog, backlogErr := factInteger(cell(*daily, row, "backlog_size"))
		rr.leafPair(pair, key, nodePath+"backlogSize", Leaf{T: LeafInt, V: fmt.Sprint(total.backlog)}, backlog, nil, backlogErr, 0)
		mean, meanErr := typedLeaf(LeafFloat, cell(*daily, row, "throughput_mean"))
		rr.leafPair(pair, key, nodePath+"throughputMean", Leaf{T: LeafFloat, V: formatFloat(total.mean)}, mean, nil, meanErr, relSum)
		stddev, stddevErr := typedLeaf(LeafFloat, cell(*daily, row, "throughput_stddev"))
		rr.leafPair(pair, key, nodePath+"throughputStddev", Leaf{T: LeafFloat, V: formatFloat(math.Sqrt(total.sq))}, stddev, nil, stddevErr, relSum)
	}
	if truncated {
		rr.NotJoined = append(rr.NotJoined, "workload: a daily_workload table is cut by the provider row cap; only its days were compared")
	}

	// The scalars: one acr fact per work scope against the latest ops row of
	// that scope.
	seen := map[string]bool{}
	for _, fact := range facts {
		scopeID, _ := fact.Fields["work_scope_id"].(string)
		seen[scopeID] = true
		n, ok := scopeLatest[scopeID]
		if !ok {
			rr.Compared++
			rr.find(Finding{Pair: pair, Key: "team work scope", Detail: "an acr workload fact has no ops forecast row for its work scope in the window"})
			continue
		}
		key := "team work scope"
		both := func(path, kind string, opsValue, acrValue any) {
			ops, opsErr := typedLeaf(kind, opsValue)
			var acr Leaf
			var acrErr error
			if kind == LeafInt {
				acr, acrErr = factInteger(acrValue)
			} else {
				acr, acrErr = typedLeaf(kind, acrValue)
			}
			rr.leafPair(pair, key, nodePath+path, ops, acr, opsErr, acrErr, 0)
		}
		both("backlogSize", LeafInt, n.row["backlogSize"], fact.Fields["backlog_size"])
		both("throughputMean", LeafFloat, n.row["throughputMean"], fact.Fields["throughput_mean"])
		both("throughputStddev", LeafFloat, n.row["throughputStddev"], fact.Fields["throughput_stddev"])
		both("insufficientHistory", LeafBool, n.row["insufficientHistory"], fact.Fields["insufficient_history"])
		both("highVariance", LeafBool, n.row["highVariance"], fact.Fields["high_variance"])
		both("p50Days", LeafInt, n.row["p50Days"], fact.Fields["forecast_p50_days"])
		both("computedAt", LeafTime, n.row["computedAt"], fact.Fields["computed_at"])
	}
	for scopeID := range scopeLatest {
		if !seen[scopeID] {
			rr.Compared++
			rr.find(Finding{Pair: pair, Key: "team work scope", Detail: "an ops forecast work scope has no acr workload fact"})
		}
	}
}

const coveragePath = "throughputForecast.estimateCoverage."

// compareReadiness is root throughputForecast: the estimate coverage block
// of the forecast against the acr readiness facts of the team, on the
// latest day. The store rows of that day are the third reading: the acr
// facts are held against them first, so a current read the provider marks
// truncated is compared only when its facts are whole.
func compareReadiness(ctx context.Context, o *Oracle, rr *RootReport) error {
	coverageRows, err := o.Store.LatestEstimateCoverage()
	if err != nil {
		return err
	}
	teams := map[string]bool{}
	for team := range coverageRows {
		teams[team] = true
	}
	ids := sortedKeys(teams)
	if len(ids) == 0 {
		rr.NotJoined = append(rr.NotJoined, "no estimate coverage row with a team in the window")
		return nil
	}
	// ops reads the team's latest day with no window, so the acr side is the
	// current read: one fact per work scope, each with its own latest day.
	facts, err := o.readFacts(ctx, "readiness", "team", ids, readCurrentHeldToStore)
	if err != nil {
		return err
	}
	byTeam := map[string][]ServedFact{}
	for _, fact := range facts {
		team := bareID(fact.Subject.CanonicalID)
		byTeam[team] = append(byTeam[team], fact)
	}
	counts := []struct{ ops, acr string }{{"estimatedCount", "estimated_count"}, {"unestimatedCount", "unestimated_count"}, {"backlogSize", "backlog_size"}}
	for _, team := range ids {
		variables := map[string]any{"input": map[string]any{"teamIds": []any{"team:" + team}, "historyWeeks": 12}}
		root, ok, serr := o.served(ctx, rr, "readiness", "throughputForecast/throughputForecast/all", variables)
		if serr != nil {
			return serr
		}
		if !ok {
			continue
		}
		coverage, hasOps := root["estimateCoverage"].(map[string]any)
		// ops sums the work scopes that have a row on the team's latest day.
		latestDay := ""
		for _, fact := range byTeam[team] {
			if day, isString := fact.Fields["day"].(string); isString && day > latestDay {
				latestDay = day
			}
		}
		sums := map[string]int64{}
		scopes := 0
		for _, fact := range byTeam[team] {
			if day, _ := fact.Fields["day"].(string); day != latestDay {
				continue
			}
			scopes++
			for _, f := range counts {
				leaf, lerr := factInteger(fact.Fields[f.acr])
				if lerr != nil || leaf.T != LeafInt {
					rr.Compared++
					rr.find(Finding{Pair: "readiness", Key: "team", Path: "acr:readiness." + f.acr, Detail: "an acr readiness fact has no integer " + f.acr})
					continue
				}
				var v int64
				_, _ = fmt.Sscan(leaf.V, &v)
				sums[f.acr] += v
			}
		}
		key := "team"
		// The acr facts against the store rows of the team's latest day.
		stored := coverageRows[team]
		if latestDay == "" {
			rr.Compared++
			rr.find(Finding{Pair: "readiness_store", Key: key, Detail: "a team with estimate coverage rows in the store has no acr readiness fact with a day"})
		} else {
			rr.leafPair("readiness_store", key, "acr:readiness.day", Leaf{T: LeafDate, V: stored.Day}, Leaf{T: LeafDate, V: latestDay}, nil, nil, 0)
			rr.leafPair("readiness_store", key, "acr:readiness.scopes", Leaf{T: LeafInt, V: fmt.Sprint(stored.Scopes)}, Leaf{T: LeafInt, V: fmt.Sprint(scopes)}, nil, nil, 0)
			for _, f := range counts {
				rr.leafPair("readiness_store", key, "acr:readiness."+f.acr, Leaf{T: LeafInt, V: fmt.Sprint(stored.Counts[f.acr])}, Leaf{T: LeafInt, V: fmt.Sprint(sums[f.acr])}, nil, nil, 0)
			}
		}
		switch {
		case !hasOps && latestDay == "":
			// The store has rows for the team and neither plane answers: a
			// measurement that did not happen, not an agreement.
			rr.Compared++
			rr.find(Finding{Pair: "readiness", Key: key, Detail: "a team with estimate coverage rows in the store has an estimate coverage on neither side"})
		case !hasOps || latestDay == "":
			rr.Compared++
			rr.find(Finding{Pair: "readiness", Key: key, Detail: fmt.Sprintf("estimate coverage present in ops %t, in acr %t", hasOps, latestDay != "")})
		default:
			for _, f := range counts {
				opsLeaf, opsErr := typedLeaf(LeafInt, coverage[f.ops])
				rr.leafPair("readiness", key, coveragePath+f.ops, opsLeaf, Leaf{T: LeafInt, V: fmt.Sprint(sums[f.acr])}, opsErr, nil, 0)
			}
			// ops: ratio = estimated / backlog, null when the backlog is 0.
			opsRatio, opsErr := typedLeaf(LeafFloat, coverage["ratio"])
			acrRatio := Leaf{T: LeafNull}
			if sums["backlog_size"] != 0 {
				acrRatio = Leaf{T: LeafFloat, V: formatFloat(float64(sums["estimated_count"]) / float64(sums["backlog_size"]))}
			}
			rr.leafPair("readiness", key, coveragePath+"ratio", opsRatio, acrRatio, opsErr, nil, relSum)
		}
	}
	rr.differ(Difference{Pair: "readiness", Key: "all teams", Class: ClassForecastByDesign, Exact: true,
		Detail: "the forecast percentiles are computed on demand and have no acr fact; only the estimate coverage block is compared"})
	return compareFlowWindow(ctx, o, rr)
}

// flowCounts are the two counts of the flow fact the oracle checks.
var flowCounts = []string{"items_started", "items_completed"}

// flowWindowCheck holds one team's flow fact, read over the window, against
// its own tables: the window-named count is the sum of the daily series, and
// the latest-day count is the sum of each work scope's latest day. joined is
// false for a fact with no window-named count: an acr build from before the
// counts were named has nothing to check.
func flowWindowCheck(fact ServedFact) (compared, matches int, findings []Finding, joined bool, err error) {
	if _, has := fact.Fields[flowCounts[0]+"_window"]; !has {
		return 0, 0, nil, false, nil
	}
	daily, dok := fact.Tables["daily_flow"]
	scopes, sok := fact.Tables["scope_breakdown"]
	if !dok || !sok {
		return 0, 0, nil, false, fmt.Errorf("a flow fact with window counts has no daily series or no scope table")
	}
	for _, count := range flowCounts {
		for _, part := range []struct {
			field string
			table ServedTable
			what  string
		}{{count + "_window", daily, "the sum of the daily series over the window"}, {count + "_latest_day", scopes, "the sum of each work scope's latest day"}} {
			compared++
			stated, serr := factInteger(fact.Fields[part.field])
			sum, terr := tableSum(part.table, count)
			switch {
			case serr != nil || stated.T != LeafInt:
				findings = append(findings, Finding{Pair: "flow_window", Key: "team", Path: "acr:flow." + part.field, Detail: "the flow fact states no integer " + part.field})
			case terr != nil:
				findings = append(findings, Finding{Pair: "flow_window", Key: "team", Path: "acr:flow." + part.field, Detail: "the table to sum is not whole: " + terr.Error()})
			case stated.V != fmt.Sprint(sum):
				findings = append(findings, Finding{Pair: "flow_window", Key: "team", Path: "acr:flow." + part.field, Detail: fmt.Sprintf("the fact states %s; %s is %d", stated.V, part.what, sum)})
			default:
				matches++
			}
		}
	}
	return compared, matches, findings, true, nil
}

func tableSum(table ServedTable, column string) (int64, error) {
	if tableColumn(table, column) < 0 {
		return 0, fmt.Errorf("no %s column", column)
	}
	if table.TruncatedBy != "" || table.RowsOmitted != 0 {
		return 0, fmt.Errorf("table is cut by the provider row cap")
	}
	total := int64(0)
	for _, row := range table.Rows {
		leaf, err := factInteger(cell(table, row, column))
		if err != nil || leaf.T != LeafInt {
			return 0, fmt.Errorf("%s is not an integer", column)
		}
		var v int64
		_, _ = fmt.Sscan(leaf.V, &v)
		total += v
	}
	return total, nil
}

// compareFlowWindow checks the acr flow facts of the teams with work item
// metrics in the window. It has no ops side: it is the acr half of the
// latest-day-beside-a-window difference, which the flow fact now names in
// its own fields.
func compareFlowWindow(ctx context.Context, o *Oracle, rr *RootReport) error {
	teams := map[string]bool{}
	for _, row := range o.Store.extract.Tables[tableWorkItemMetricsDaily] {
		if team, ok := rowString(row, "team_id"); ok && team != "" {
			teams[team] = true
		}
	}
	ids := sortedKeys(teams)
	if len(ids) == 0 {
		rr.NotJoined = append(rr.NotJoined, "flow: no work item metrics row with a team in the window")
		return nil
	}
	facts, err := o.facts(ctx, "flow", "team", ids)
	if err != nil {
		return err
	}
	hasFact := map[string]bool{}
	for _, fact := range facts {
		hasFact[bareID(fact.Subject.CanonicalID)] = true
	}
	for _, team := range ids {
		if !hasFact[team] {
			rr.Compared++
			rr.find(Finding{Pair: "flow_window", Key: "team", Detail: "a team with work item metrics rows in the store has no acr flow fact"})
		}
	}
	for _, fact := range facts {
		compared, matches, findings, joined, cerr := flowWindowCheck(fact)
		if cerr != nil {
			return cerr
		}
		if !joined {
			rr.NotJoined = append(rr.NotJoined, "flow: a team's flow fact has no window-named counts (an acr build from before they were named)")
			continue
		}
		rr.Compared += compared
		rr.Matches += matches
		for _, f := range findings {
			rr.find(f)
		}
	}
	return nil
}
