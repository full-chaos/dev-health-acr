package factoracle

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

// The acceptance gate: defects the oracle must keep finding and naming.
//
// Each case plants one known defect at the data layer. The store the acr
// plane reads loses the input the fix of that defect depends on, so the
// real, current producer answers as the defective one did; the ops plane is
// the recorded reply of the real listener for the reference store, and the
// witnesses are read from the reference store. A case is three runs:
//
//   - control: the producer on the reference store. No difference of the
//     class and no finding: with the input present the fix holds.
//   - planted: the producer on the planted store. The oracle must report
//     exactly the one class, with the values the case expects, and no
//     finding. The expected values are not read from the oracle's own
//     witness: they are the effort of the planted work unit, or what the
//     real producer answers on the two stores.
//   - in the planted run the shape pass (the wire-shape check that existed
//     before this oracle) still reports nothing: it cannot see the defect.
//
// Re-run this gate after every change to the comparator.

// gateRun runs root analytics with reference as the store both the ops reply
// and the witnesses describe, and acr as the store the acr plane reads.
func gateRun(t *testing.T, manifest Manifest, recording Recording, reference, acr *Extract) (*Oracle, *RootReport) {
	t.Helper()
	planes := localPlanes(t, seedStore(t, acr), &recording)
	oracle := oracleFor(t, manifest, planes, reference)
	oracle.OnlyRoots = []string{"analytics"}
	report := runOracle(t, oracle)
	if failures := planes.Listener.Failures(); len(failures) > 0 {
		t.Fatalf("replay listener failures: %v", failures)
	}
	rr := report.Root("analytics")
	if rr == nil || rr.ShapesRun == 0 || rr.Compared == 0 {
		t.Fatalf("root analytics was not run: %+v", rr)
	}
	// A run that is not a valid measurement proves nothing about the class.
	if len(rr.Invalid) > 0 {
		t.Fatalf("root analytics is not a valid measurement: %v", rr.Invalid)
	}
	if err := report.Err(); err != nil {
		t.Fatalf("the gate run is an error: %v", err)
	}
	return oracle, rr
}

func classesOf(rr *RootReport, pair string) []Class {
	seen := map[Class]bool{}
	for _, d := range rr.Differences {
		if d.Pair == pair {
			seen[d.Class] = true
		}
	}
	out := make([]Class, 0, len(seen))
	for class := range seen {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func wantClean(t *testing.T, rr *RootReport, allowed ...Class) {
	t.Helper()
	ok := map[Class]bool{}
	for _, class := range allowed {
		ok[class] = true
	}
	for _, d := range rr.Differences {
		if !ok[d.Class] {
			t.Errorf("control run reports class %s (%s): the fix does not hold on the reference store", d.Class, d.Detail)
		}
	}
	if len(rr.Findings) > 0 {
		t.Errorf("control run has findings: %+v", rr.Findings)
	}
}

// sameThemes reports whether two theme maps hold the same values; a theme
// that one map lacks is zero there.
func sameThemes(got, want map[string]float64) bool {
	// A theme whose true value is zero comes out as the rounding error of
	// the sums of the other themes, so the tolerance is the one of the
	// largest theme of the two maps, not of the theme itself.
	scale := 0.0
	for _, theme := range themeKeys(got, want) {
		scale = math.Max(scale, math.Max(math.Abs(got[theme]), math.Abs(want[theme])))
	}
	for _, theme := range themeKeys(got, want) {
		if math.Abs(got[theme]-want[theme]) > sumTolerance(scale) {
			return false
		}
	}
	return true
}

// repositorySum is the acr repository mix of a run, summed per theme.
func repositorySum(o *Oracle) map[string]float64 {
	out := map[string]float64{}
	for _, effort := range o.RepositoryEffort {
		for theme, value := range effort {
			out[theme] += value
		}
	}
	return out
}

func minus(a, b map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for _, theme := range themeKeys(a, b) {
		out[theme] = a[theme] - b[theme]
	}
	return out
}

// wantPlanted requires the planted run to name exactly one class in pair,
// once, by a witness that equals the difference, with the values the case
// expects, and to have no finding at all: in particular the shape pass, the
// wire-shape check that existed before the oracle, ran and saw nothing.
func wantPlanted(t *testing.T, rr *RootReport, pair string, want Class, values map[string]float64) {
	t.Helper()
	got := classesOf(rr, pair)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("planted run names %v in pair %s, want exactly [%s]; findings: %+v", got, pair, want, rr.Findings)
	}
	nonZero := 0.0
	for _, v := range values {
		nonZero += math.Abs(v)
	}
	if nonZero == 0 {
		t.Fatalf("the case expects a difference of zero: it would pin nothing")
	}
	// Only that class: any other accepted difference in the whole run (the
	// residual of attribution_basis is by design and always present) is a
	// second defect the plant did not make.
	for _, d := range rr.Differences {
		if d.Class != want && d.Class != ClassAttributionBasis {
			t.Errorf("planted run carries class %s in pair %s beside %s: %s", d.Class, d.Pair, want, d.Detail)
		}
	}
	named := 0
	for _, d := range rr.Differences {
		if d.Pair != pair || d.Class != want {
			continue
		}
		named++
		if !d.Exact {
			t.Errorf("class %s is named by a witness that does not equal the difference", want)
		}
		if !sameThemes(d.Values, values) {
			t.Errorf("class %s is named with values %s, the case expects %s", want, formatThemes(d.Values), formatThemes(values))
		}
	}
	if named != 1 {
		t.Errorf("class %s is named %d times in pair %s, want once", want, named, pair)
	}
	if len(rr.Findings) > 0 {
		t.Errorf("planted run has findings beside the named class: %+v", rr.Findings)
	}
	if rr.ShapesRun == 0 || rr.Leaves == 0 {
		t.Errorf("the shape pass did not run on the planted store")
	}
}

// directUnit picks the work unit of the extract with the largest effort
// whose whole effort reaches organization repositories: one generation, a
// repo_id that is an organization repository, every pull request reference
// in the "<repository uuid>#pr<n>" form with an organization repository, no
// issue-side reference, in the window and in scope. The pull request share
// partition of such a unit sums to its whole effort.
func directUnit(t *testing.T, extract *Extract, window Window) (id string, row Row) {
	t.Helper()
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	repos := map[string]bool{}
	for _, repo := range store.RepositoryIDs() {
		repos[repo] = true
	}
	best := -1.0
	for _, candidate := range extract.Tables[tableWorkUnitInvestments] {
		unitID, _ := rowString(candidate, "work_unit_id")
		unit := store.units[unitID]
		repo, hasRepo := rowString(candidate, "repo_id")
		evidence, _ := rowString(candidate, "structural_evidence_json")
		var refs struct {
			PRs    []string `json:"prs"`
			Issues []string `json:"issues"`
		}
		if json.Unmarshal([]byte(evidence), &refs) != nil || len(refs.Issues) > 0 {
			continue
		}
		resolved := true
		for _, ref := range refs.PRs {
			m := evidenceUUIDRef.FindStringSubmatch(ref)
			if m == nil || !repos[strings.ToLower(m[1])] {
				resolved = false
			}
		}
		if !resolved {
			continue
		}
		if unit == nil || unit.generations != 1 || !hasRepo || !repos[strings.ToLower(repo)] || !store.inScope(unitID) || !window.holds(unit.latest) {
			continue
		}
		total := 0.0
		for _, share := range unit.latest.themes {
			total += share * unit.latest.effort
		}
		if total > best || (total == best && unitID < id) {
			best, id, row = total, unitID, candidate
		}
	}
	if id == "" || best <= 0 {
		t.Fatal("the extract has no work unit whose whole effort reaches organization repositories: the gate case cannot be built")
	}
	return id, row
}

func cloneRow(row Row) Row {
	out := Row{}
	for k, v := range row {
		out[k] = v
	}
	return out
}

func unitThemeEffort(t *testing.T, row Row) map[string]float64 {
	t.Helper()
	effort, err := rowFloat(row, "effort_value")
	if err != nil {
		t.Fatal(err)
	}
	themes, err := rowMap(row, "theme_distribution_json")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for theme, share := range themes {
		out[theme] = share * effort
	}
	return out
}

func acceptanceGateMembershipScope(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	outside := 0.0
	for _, v := range store.Witnesses(manifest.Window)[ClassMembershipScope] {
		outside += v
	}
	if store.scopeRun == "" || outside <= 0 {
		t.Fatal("the extract has no complete membership run with work units outside it: the gate case cannot be built")
	}
	controlOracle, control := gateRun(t, manifest, recording, extract, extract)
	wantClean(t, control, ClassAttributionBasis)

	// Plant: the membership run markers are gone, so the scope filter of the
	// current reader keeps every work unit, as the reader did before it had
	// the filter. The expected values are what the real producer gains from
	// the control store to the planted one.
	planted := extract.Clone()
	planted.Tables[tableWorkUnitMembershipRuns] = nil
	plantedOracle, rr := gateRun(t, manifest, recording, extract, planted)
	wantPlanted(t, rr, "investment_org", ClassMembershipScope, minus(repositorySum(plantedOracle), repositorySum(controlOracle)))
}

func acceptanceGateSupersession(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	_, source := directUnit(t, extract, manifest.Window)
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}

	// Reference: a superseded work unit. It is a copy of a real work unit
	// under a new id, in the membership run, with a supersession row; ops
	// leaves a superseded unit out, so the recorded reply stands.
	const cloneID = "wu-gate-superseded"
	reference := extract.Clone()
	unit := cloneRow(source)
	unit["work_unit_id"] = cloneID
	reference.Tables[tableWorkUnitInvestments] = append(reference.Tables[tableWorkUnitInvestments], unit)
	computedAt, _ := rowString(source, "computed_at")
	reference.Tables[tableWorkUnitMembership] = append(reference.Tables[tableWorkUnitMembership], Row{
		"org_id": FixtureOrgID, "node_type": "issue", "node_id": "node-gate-superseded", "work_unit_id": cloneID,
		"category_kind": "theme", "category": "gate", "computed_at": computedAt, "run_id": store.scopeRun,
	})
	reference.Tables[tableWorkUnitSupersessions] = append(reference.Tables[tableWorkUnitSupersessions], Row{
		"org_id": FixtureOrgID, "superseded_work_unit_id": cloneID, "superseded_at": "2026-09-21 00:00:00.000000000",
	})
	_, control := gateRun(t, manifest, recording, reference, reference)
	wantClean(t, control, ClassAttributionBasis)

	// Plant: the supersession row is gone, so the current reader reads the
	// superseded unit as live.
	planted := reference.Clone()
	planted.Tables[tableWorkUnitSupersessions] = nil
	_, rr := gateRun(t, manifest, recording, reference, planted)
	// The superseded unit is a copy of a unit whose whole effort reaches
	// organization repositories: read as live, it adds exactly its effort.
	wantPlanted(t, rr, "investment_org", ClassSupersession, unitThemeEffort(t, source))
}

func acceptanceGateNullableArgmax(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	id, source := directUnit(t, extract, manifest.Window)
	at, err := rowTime(source, "computed_at")
	if err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Second).Format("2006-01-02 15:04:05.000")

	// Reference: the unit gets a newer generation with no pull request
	// reference and a NULL repo_id. The ops organization value reads neither,
	// so the recorded reply stands; the current reader keeps the NULL and the
	// unit reaches no repository. This control run is the one that puts the
	// NULL in front of the reader's own SQL (argMax over a tuple): the seeded
	// store holds both generations of the unit (Seed keeps every row), so a
	// reader whose argMax skips the NULL revives the older repository here,
	// and the control then names the class and fails. The hand-run kill proof
	// of that SQL clause is in the pull request's test evidence.
	reference := extract.Clone()
	newer := cloneRow(source)
	newer["computed_at"], newer["repo_id"], newer["structural_evidence_json"] = later, nil, `{"issues":[],"prs":[]}`
	reference.Tables[tableWorkUnitInvestments] = append(reference.Tables[tableWorkUnitInvestments], newer)
	controlOracle, control := gateRun(t, manifest, recording, reference, reference)
	wantClean(t, control, ClassAttributionBasis)
	// The control is not vacuous: the unit left the repository mix, so the
	// residual grew by exactly its effort.
	for theme, effort := range unitThemeEffort(t, source) {
		got := controlOracle.Residual[theme] - manifest.Residual[theme]
		if math.Abs(got-effort) > themeTolerance(effort) {
			t.Fatalf("control: the unit %s must leave the repository mix; residual of %s grew by %v, want %v", id, theme, got, effort)
		}
	}

	// Plant: the newest generation names the repository of the older one,
	// which is what an argMax that skips NULL hands the reader. The data
	// layer cannot make the current SQL skip a NULL, so this run shows that
	// the comparator names the class for that answer, and with which values;
	// the SQL itself is exercised by the control run above.
	planted := reference.Clone()
	rows := planted.Tables[tableWorkUnitInvestments]
	rows[len(rows)-1]["repo_id"] = source["repo_id"]
	_, rr := gateRun(t, manifest, recording, reference, planted)
	wantPlanted(t, rr, "investment_org", ClassNullableArgmax, unitThemeEffort(t, source))
}

func acceptanceGateNullRepoID(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	cleanOracle, clean := gateRun(t, manifest, recording, extract, extract)
	wantClean(t, clean, ClassAttributionBasis)
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	owned, _, err := store.ownedRepositories(manifest.Window.Start)
	if err != nil {
		t.Fatal(err)
	}
	team, repo, best := "", "", 0.0
	for _, candidate := range sortedKeys(func() map[string]bool {
		out := map[string]bool{}
		for name := range owned {
			out[name] = true
		}
		return out
	}()) {
		for _, r := range sortedKeys(owned[candidate]) {
			total := 0.0
			for _, v := range cleanOracle.RepositoryEffort[r] {
				total += v
			}
			if total > best {
				team, repo, best = candidate, r, total
			}
		}
	}
	if team == "" {
		t.Fatal("no team owns a repository with effort in the window: the gate case cannot be built")
	}

	// Reference: every valid ownership row of the team for that repository
	// loses its repo_id and keeps its name. ops resolves such a row by name
	// (ruling K11), and so does the current reader.
	valid := func(row Row) bool {
		rowTeam, _ := rowString(row, "team_id")
		rowRepo, hasRepo := rowString(row, "repo_id")
		if rowTeam != team || !hasRepo || strings.ToLower(rowRepo) != repo {
			return false
		}
		if to, ok := rowString(row, "valid_to"); ok {
			until, terr := parseInstant(to)
			return terr == nil && until.After(manifest.Window.Start)
		}
		return true
	}
	reference := extract.Clone()
	var changed []int
	for i, row := range reference.Tables[tableTeamRepoOwnership] {
		if valid(row) {
			row["repo_id"] = nil
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		t.Fatal("no valid ownership row for the chosen team and repository")
	}
	_, control := gateRun(t, manifest, recording, reference, reference)
	wantClean(t, control, ClassAttributionBasis)

	// Plant: the name no longer matches a repository, so the row resolves to
	// nothing and the team loses the repository, as it did when a row with
	// no repo_id was dropped.
	planted := reference.Clone()
	for _, i := range changed {
		name, _ := rowString(planted.Tables[tableTeamRepoOwnership][i], "repo_full_name")
		planted.Tables[tableTeamRepoOwnership][i]["repo_full_name"] = name + "-moved"
	}
	_, rr := gateRun(t, manifest, recording, reference, planted)
	// The team loses exactly the mix of the repository, as the real producer
	// gave it on the clean store.
	wantPlanted(t, rr, "investment_team_rollup", ClassNullRepoID, cleanOracle.RepositoryEffort[repo])
	if got := classesOf(rr, "investment_org"); len(got) != 0 && !(len(got) == 1 && got[0] == ClassAttributionBasis) {
		t.Errorf("the organization pair must not change when only ownership changes, got %v", got)
	}
}

// A team that loses a repository it owns by id is not a null_repo_id
// difference: the class is named only when the loss is exactly the
// repositories a row with no repo_id names.
func teamRollupLossWithNoNullRowIsAFinding(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	cleanOracle, _ := gateRun(t, manifest, recording, extract, extract)
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	owned, _, err := store.ownedRepositories(manifest.Window.Start)
	if err != nil {
		t.Fatal(err)
	}
	team, repo, best := "", "", 0.0
	for name, repos := range owned {
		for r := range repos {
			total := 0.0
			for _, v := range cleanOracle.RepositoryEffort[r] {
				total += v
			}
			if total > best || (total == best && total > 0 && name+r < team+repo) {
				team, repo, best = name, r, total
			}
		}
	}
	if team == "" {
		t.Fatal("no team owns a repository with effort in the window")
	}
	planted := extract.Clone()
	var kept []Row
	for _, row := range planted.Tables[tableTeamRepoOwnership] {
		rowTeam, _ := rowString(row, "team_id")
		rowRepo, _ := rowString(row, "repo_id")
		if rowTeam == team && strings.ToLower(rowRepo) == repo {
			continue
		}
		kept = append(kept, row)
	}
	planted.Tables[tableTeamRepoOwnership] = kept
	_, rr := gateRun(t, manifest, recording, extract, planted)
	if got := classesOf(rr, "investment_team_rollup"); len(got) != 0 {
		t.Fatalf("a loss with no null row was named %v", got)
	}
	found := false
	for _, f := range rr.Findings {
		if f.Pair == "investment_team_rollup" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a team that lost an owned repository is not a finding: %+v", rr)
	}
}

// Every value pair sees a store that moved: one changed value per pair on
// the acr side, against the recorded ops replies of the unchanged store, is
// a finding on that root.
func everyValuePairFindsAChangedValue(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	bump := func(row Row, column string, by float64) {
		t.Helper()
		value, err := rowFloat(row, column)
		if err != nil {
			t.Fatalf("%s: %v", column, err)
		}
		row[column] = json.Number(formatFloat(value + by))
	}
	planted := extract.Clone()
	for _, row := range planted.Tables[tableCompoundingRiskDaily] {
		if row["compounding_risk"] != nil {
			bump(row, "compounding_risk", 0.125)
		}
	}
	for _, row := range planted.Tables[tableCapacityForecasts] {
		bump(row, "backlog_size", 1)
	}
	for _, row := range planted.Tables[tableEstimateCoverageMetricsDaily] {
		bump(row, "unestimated_count", 1)
	}
	for _, row := range planted.Tables[tableWorkUnitInvestments] {
		bump(row, "effort_value", 1)
	}
	name, _ := rowString(planted.Tables[tableRepos][0], "repo")
	planted.Tables[tableRepos][0]["repo"] = name + "x"

	manifest, skipped := withoutNotRecordedRoots(t, manifest)
	planes := localPlanes(t, seedStore(t, planted), &recording)
	oracle := oracleFor(t, manifest, planes, extract)
	oracle.OnlyRoots = rootsExcept(t, skipped)
	report := runOracle(t, oracle)
	for _, root := range []string{"analytics", "capacityForecasts", "catalog", "compoundingRisk", "throughputForecast"} {
		rr := report.Root(root)
		want := manifest.Expect[root]
		if rr == nil || len(rr.Findings) <= len(want.Findings) {
			t.Errorf("root %s reports no new finding for a changed acr store: %+v", root, rr)
		}
	}
	// The shape-only roots read no fact: they are as on the venue.
	for _, rr := range report.Roots {
		if rr.Mode == ModeShape && len(rr.Findings) != len(manifest.Expect[rr.Root].Findings) {
			t.Errorf("shape root %s changed with the acr store: %+v", rr.Root, rr.Findings)
		}
	}
}

// Effort that moves from one repository to another leaves every sum as it
// was. The repository-by-repository compare sees it.
func effortMovedBetweenRepositoriesIsAFinding(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	id, source := directUnit(t, extract, manifest.Window)
	from, _ := rowString(source, "repo_id")
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	to := ""
	for _, repo := range store.RepositoryIDs() {
		if repo != strings.ToLower(from) {
			to = repo
			break
		}
	}
	if to == "" {
		t.Fatal("the extract has one repository only")
	}
	planted := extract.Clone()
	for _, row := range planted.Tables[tableWorkUnitInvestments] {
		if unit, _ := rowString(row, "work_unit_id"); unit == id {
			evidence, _ := rowString(row, "structural_evidence_json")
			row["structural_evidence_json"] = strings.ReplaceAll(evidence, from, to)
			row["repo_id"] = to
		}
	}
	_, rr := gateRun(t, manifest, recording, extract, planted)
	// The residual of attribution_basis is by design and present whenever
	// effort reaches no repository (the venue at ops 5c9a3d32 has some);
	// moved effort must not be named by any other class.
	for _, d := range rr.Differences {
		if d.Class != ClassAttributionBasis {
			t.Fatalf("moved effort was named a class: %+v", d)
		}
	}
	found := false
	for _, f := range rr.Findings {
		if f.Pair == "investment_repository" {
			found = true
		}
	}
	if !found {
		t.Fatalf("effort moved between two repositories is not a finding: %+v", rr.Findings)
	}
}

// A recorded reply that is not the reply of the reference store is a
// finding before anything is said about acr.
func aReplyThatIsNotOfTheStoreIsAFinding(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	other := extract.Clone()
	for _, row := range other.Tables[tableWorkUnitInvestments] {
		value, err := rowFloat(row, "effort_value")
		if err != nil {
			t.Fatal(err)
		}
		row["effort_value"] = json.Number(formatFloat(value * 2))
	}
	_, rr := gateRun(t, manifest, recording, other, extract)
	found := 0
	for _, f := range rr.Findings {
		if f.Pair == "ops_reply_vs_store" {
			found++
		}
	}
	if found == 0 {
		t.Fatalf("a reply of another store is not a finding: %+v", rr.Findings)
	}
}
