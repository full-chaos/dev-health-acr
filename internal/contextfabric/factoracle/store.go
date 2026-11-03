package factoracle

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Store is the reference reading of the extract rows: what the two planes
// read. It restates, in Go and over the rows, the two readings the oracle
// compares: the ops organization value (OrgThemeEffort) and the acr
// repository mix (ExpectedRepositoryEffort, the pull request reference share
// of devhealthfacts repoMixStatement). It is a test model, not a producer:
// the live mode and the recorded mode both hold it against the real planes on
// real rows, so a wrong model fails there before it can excuse a difference.
type Store struct {
	extract *Extract
	units   map[string]*unitGenerations
	// superseded is the work units a later run retired.
	superseded map[string]bool
	// scopeRun is the run id of the latest complete membership marker, ""
	// when the organization has none.
	scopeRun string
	// inRun is the work units of scopeRun.
	inRun map[string]bool
	// repoIDs is the organization's repository ids; reposByName maps
	// provider + NUL + name to the ids that carry that name.
	repoIDs     map[string]bool
	reposByName map[string][]string
}

type unitGeneration struct {
	computedAt time.Time
	fromTS     time.Time
	toTS       time.Time
	repoID     *string
	themes     map[string]float64
	subcats    map[string]float64
	prs        []string
	issues     []string
	// status is the row's categorization_status.
	status string
}

// gated is the evidence gate of both producers: a work unit whose latest
// generation has too little text (insufficient_evidence) or none
// (no_text_sources) carries a fixed uniform prior, not a measured split, and
// stays out of the theme mix. ops: analytics.CategorizedOnlySQL
// (internal/queryapi/analytics/investment.go:677 at 0b763a85), counted in
// evidenceQualityStats.uncategorizedCount; acr: evidenceGatedStatuses
// (devhealthfacts/investment_uncategorized.go, #1666).
func (g unitGeneration) gated() bool {
	return g.status == "insufficient_evidence" || g.status == "no_text_sources"
}

type unitGenerations struct {
	latest unitGeneration
	// revivedRepo is the repository an earlier generation named when the
	// latest generation names none.
	revivedRepo string
	generations int
}

func rowString(row Row, column string) (string, bool) {
	v, ok := row[column]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func rowFloat(row Row, column string) (float64, error) {
	switch v := row[column].(type) {
	case json.Number:
		return strconv.ParseFloat(v.String(), 64)
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("column %s is not a number", column)
	}
}

func rowTime(row Row, column string) (time.Time, error) {
	s, ok := rowString(row, column)
	if !ok {
		return time.Time{}, fmt.Errorf("column %s is not a time", column)
	}
	return parseInstant(s)
}

func rowMap(row Row, column string) (map[string]float64, error) {
	raw, ok := row[column].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("column %s is not a map", column)
	}
	out := make(map[string]float64, len(raw))
	for k, v := range raw {
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("column %s: value of %s is not a number", column, k)
		}
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil {
			return nil, err
		}
		out[k] = f
	}
	return out, nil
}

// NewStore reads the extract rows.
func NewStore(extract *Extract) (*Store, error) {
	s := &Store{extract: extract, units: map[string]*unitGenerations{}, superseded: map[string]bool{}, inRun: map[string]bool{},
		repoIDs: map[string]bool{}, reposByName: map[string][]string{}}
	generations := map[string][]unitGeneration{}
	for _, row := range extract.Tables[tableWorkUnitInvestments] {
		id, ok := rowString(row, "work_unit_id")
		if !ok {
			return nil, fmt.Errorf("work_unit_investments: a row has no work_unit_id")
		}
		var gen unitGeneration
		var err error
		if gen.computedAt, err = rowTime(row, "computed_at"); err != nil {
			return nil, err
		}
		if gen.fromTS, err = rowTime(row, "from_ts"); err != nil {
			return nil, err
		}
		if gen.toTS, err = rowTime(row, "to_ts"); err != nil {
			return nil, err
		}
		if gen.themes, err = rowMap(row, "theme_distribution_json"); err != nil {
			return nil, err
		}
		if gen.subcats, err = rowMap(row, "subcategory_distribution_json"); err != nil {
			return nil, err
		}
		if repo, ok := rowString(row, "repo_id"); ok {
			gen.repoID = &repo
		}
		gen.status, _ = rowString(row, "categorization_status")
		if evidence, ok := rowString(row, "structural_evidence_json"); ok {
			var refs struct {
				PRs    []string `json:"prs"`
				Issues []string `json:"issues"`
			}
			// JSONExtract over text that is not JSON yields no reference.
			if json.Unmarshal([]byte(evidence), &refs) == nil {
				gen.prs, gen.issues = refs.PRs, refs.Issues
			}
		}
		generations[id] = append(generations[id], gen)
	}
	for id, gens := range generations {
		sort.SliceStable(gens, func(i, j int) bool { return gens[i].computedAt.Before(gens[j].computedAt) })
		unit := &unitGenerations{latest: gens[len(gens)-1], generations: len(gens)}
		if unit.latest.repoID == nil {
			for i := len(gens) - 2; i >= 0; i-- {
				if gens[i].repoID != nil {
					unit.revivedRepo = *gens[i].repoID
					break
				}
			}
		}
		s.units[id] = unit
	}
	s.readRepositories()
	for _, row := range extract.Tables[tableWorkUnitSupersessions] {
		if id, ok := rowString(row, "superseded_work_unit_id"); ok {
			s.superseded[id] = true
		}
	}
	var latestMarker time.Time
	for _, row := range extract.Tables[tableWorkUnitMembershipRuns] {
		at, err := rowTime(row, "completed_at")
		if err != nil {
			return nil, err
		}
		run, _ := rowString(row, "run_id")
		if s.scopeRun == "" || at.After(latestMarker) {
			latestMarker, s.scopeRun = at, run
		}
	}
	if s.scopeRun == legacyRunID {
		return nil, fmt.Errorf("the latest membership marker is the legacy one; the reference reading does not cover that branch")
	}
	for _, row := range extract.Tables[tableWorkUnitMembership] {
		run, _ := rowString(row, "run_id")
		id, _ := rowString(row, "work_unit_id")
		if run == s.scopeRun && s.scopeRun != "" {
			s.inRun[id] = true
		}
	}
	return s, nil
}

func (w Window) holds(gen unitGeneration) bool {
	return gen.fromTS.Before(w.End) && !gen.toTS.Before(w.Start)
}

func (s *Store) inScope(id string) bool {
	if s.superseded[id] {
		return false
	}
	return s.scopeRun == "" || s.inRun[id]
}

// OrgThemeEffort is the ops ORG reading under COUNT by theme: the sum,
// over the latest generation of every work unit in scope, in the window and
// not evidence-gated (gated),
// of one per work unit x subcategory share, grouped by the theme prefix of the
// subcategory key, with the share as the Float32 ops casts it to
// (dev-health-ops internal/queryapi/analytics investmentContextFor).
func (s *Store) OrgThemeEffort(w Window) map[string]float64 {
	out := map[string]float64{}
	for _, id := range s.unitIDs() {
		unit := s.units[id]
		if !s.inScope(id) || !w.holds(unit.latest) || unit.latest.gated() {
			continue
		}
		addSubcategoryEffort(out, unit.latest)
	}
	return out
}

// GatedUnits is the number of work units in scope and in the window that the
// evidence gate leaves out of the mix: what ops counts as uncategorized.
func (s *Store) GatedUnits(w Window) int64 {
	var n int64
	for _, id := range s.unitIDs() {
		unit := s.units[id]
		if s.inScope(id) && w.holds(unit.latest) && unit.latest.gated() {
			n++
		}
	}
	return n
}

func addSubcategoryEffort(out map[string]float64, gen unitGeneration) {
	keys := make([]string, 0, len(gen.subcats))
	for k := range gen.subcats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		theme, _, _ := strings.Cut(key, ".")
		out[theme] += float64(float32(gen.subcats[key]))
	}
}

func (s *Store) unitIDs() []string {
	ids := make([]string, 0, len(s.units))
	for id := range s.units {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// readRepositories builds the name lookup of the repository mix: per
// repository id the latest name, and its provider when the id has one
// provider only (repoMixStatement's `rl` subquery).
func (s *Store) readRepositories() {
	type latest struct {
		at        time.Time
		name      string
		provider  string
		providers map[string]bool
	}
	byID := map[string]*latest{}
	for _, row := range s.extract.Tables[tableRepos] {
		id, ok := rowString(row, "id")
		if !ok {
			continue
		}
		id = strings.ToLower(id)
		name, _ := rowString(row, "repo")
		provider, _ := rowString(row, "provider")
		at, _ := rowTime(row, "last_synced")
		entry := byID[id]
		if entry == nil {
			entry = &latest{at: at, name: name, provider: provider, providers: map[string]bool{}}
			byID[id] = entry
		} else if at.After(entry.at) {
			entry.at, entry.name, entry.provider = at, name, provider
		}
		entry.providers[provider] = true
	}
	for id, entry := range byID {
		s.repoIDs[id] = true
		provider := entry.provider
		if len(entry.providers) != 1 {
			provider = ""
		}
		key := provider + "\x00" + entry.name
		s.reposByName[key] = append(s.reposByName[key], id)
	}
}

// reach is the share of a work unit's effort each organization repository
// gets: the unit's pull request references, each resolved to a repository,
// counted distinct per repository, over the count of all its references. A
// reference that resolves to no repository keeps its share and gives it to
// nobody. A unit with no reference reaches its own repo_id.
func (s *Store) reach(gen unitGeneration, repoID *string) map[string]float64 {
	distinct := map[string]map[string]bool{}
	add := func(repo, item string) {
		if distinct[repo] == nil {
			distinct[repo] = map[string]bool{}
		}
		distinct[repo][item] = true
	}
	refs := 0
	for _, ref := range gen.prs {
		if m := evidenceUUIDRef.FindStringSubmatch(ref); m != nil {
			refs++
			add(m[1], m[1]+"#"+m[2])
		}
	}
	for _, ref := range gen.issues {
		provider, m := "github", evidenceGithubRef.FindStringSubmatch(ref)
		if m == nil {
			provider, m = "gitlab", evidenceGitlabRef.FindStringSubmatch(ref)
		}
		if m == nil {
			continue
		}
		refs++
		matched := s.reposByName[provider+"\x00"+m[1]]
		if len(matched) == 0 {
			add("", ref)
		}
		for _, repo := range matched {
			add(repo, repo+"#"+m[2])
		}
	}
	if refs == 0 && repoID != nil {
		add(strings.ToLower(*repoID), strings.ToLower(*repoID)+"#")
	}
	total := 0
	for _, items := range distinct {
		total += len(items)
	}
	out := map[string]float64{}
	for repo, items := range distinct {
		if repo != "" && s.repoIDs[repo] {
			out[repo] = float64(len(items)) / float64(total)
		}
	}
	return out
}

func reachTotal(reach map[string]float64) float64 {
	total := 0.0
	for _, share := range reach {
		total += share
	}
	return total
}

// ExpectedRepositoryEffort is the acr repository mix the store rows give:
// per repository and theme, one per work unit x theme share x the repository's
// share of the unit, over the latest generation of every work unit in scope,
// in the window and not evidence-gated (gated).
func (s *Store) ExpectedRepositoryEffort(w Window) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	for _, id := range s.unitIDs() {
		unit := s.units[id]
		if !s.inScope(id) || !w.holds(unit.latest) || unit.latest.gated() {
			continue
		}
		for repo, share := range s.reach(unit.latest, unit.latest.repoID) {
			if out[repo] == nil {
				out[repo] = map[string]float64{}
			}
			for theme, themeShare := range unit.latest.themes {
				out[repo][theme] += themeShare * share
			}
		}
	}
	return out
}

// Witnesses are, per regression class, the theme effort the repository mixes
// gain when the reader loses that class's rule: the effort, as far as it
// reaches a repository, of the superseded work units, of the work units
// outside the membership run, and of the work units whose latest NULL
// repo_id an older generation would replace. An evidence-gated work unit
// (gated) is out of every mix whatever the rule, so it witnesses nothing.
type Witnesses map[Class]map[string]float64

// Witnesses computes the witness of every investment regression class.
func (s *Store) Witnesses(w Window) Witnesses {
	out := Witnesses{ClassSupersession: {}, ClassMembershipScope: {}, ClassNullableArgmax: {}}
	add := func(class Class, gen unitGeneration, share float64) {
		for theme, themeShare := range gen.themes {
			out[class][theme] += themeShare * share
		}
	}
	for _, id := range s.unitIDs() {
		unit := s.units[id]
		if !w.holds(unit.latest) || unit.latest.gated() {
			continue
		}
		switch {
		case s.superseded[id]:
			add(ClassSupersession, unit.latest, reachTotal(s.reach(unit.latest, unit.latest.repoID)))
		case s.scopeRun != "" && !s.inRun[id]:
			add(ClassMembershipScope, unit.latest, reachTotal(s.reach(unit.latest, unit.latest.repoID)))
		case unit.revivedRepo != "":
			revived := unit.revivedRepo
			add(ClassNullableArgmax, unit.latest, reachTotal(s.reach(unit.latest, &revived))-reachTotal(s.reach(unit.latest, nil)))
		}
	}
	return out
}

// RiskSubjects is the subjects of one scope ("repo", "team") with a
// compounding risk row in the extract, by lower-cased id.
func (s *Store) RiskSubjects(scope string) map[string]bool {
	out := map[string]bool{}
	for _, row := range s.extract.Tables[tableCompoundingRiskDaily] {
		rowScope, _ := rowString(row, "scope")
		id, ok := rowString(row, "scope_id")
		if ok && id != "" && strings.EqualFold(rowScope, scope) {
			out[strings.ToLower(id)] = true
		}
	}
	return out
}

// EstimateCoverage is the store reading of one team's estimate coverage on
// its latest day.
type EstimateCoverage struct {
	Day string
	// Scopes counts the (provider, work scope) rows of that day.
	Scopes int
	// Counts sums estimated_count, unestimated_count and backlog_size over
	// those rows.
	Counts map[string]int64
}

// LatestEstimateCoverage reads, per team, the estimate coverage rows of the
// team's latest day in the extract: one row per provider and work scope (the
// latest by computed_at), summed. It is what both planes state for the team:
// ops as the coverage block of the forecast, acr as the readiness facts of
// that day.
func (s *Store) LatestEstimateCoverage() (map[string]EstimateCoverage, error) {
	type scopeRow struct {
		at     time.Time
		counts map[string]int64
	}
	latestDay := map[string]string{}
	rows := map[string]map[string]scopeRow{}
	for _, row := range s.extract.Tables[tableEstimateCoverageMetricsDaily] {
		team, ok := rowString(row, "team_id")
		if !ok || team == "" {
			continue
		}
		day, ok := rowString(row, "day")
		if !ok {
			return nil, fmt.Errorf("estimate_coverage_metrics_daily: a row has no day")
		}
		if day < latestDay[team] {
			continue
		}
		if day > latestDay[team] {
			latestDay[team], rows[team] = day, map[string]scopeRow{}
		}
		at, err := rowTime(row, "computed_at")
		if err != nil {
			return nil, err
		}
		provider, _ := rowString(row, "provider")
		scope, _ := rowString(row, "work_scope_id")
		key := provider + "\x00" + scope
		if prior, seen := rows[team][key]; seen && !at.After(prior.at) {
			continue
		}
		counts := map[string]int64{}
		for _, column := range []string{"estimated_count", "unestimated_count", "backlog_size"} {
			value, err := rowFloat(row, column)
			if err != nil {
				return nil, fmt.Errorf("estimate_coverage_metrics_daily: %w", err)
			}
			counts[column] = int64(value)
		}
		rows[team][key] = scopeRow{at: at, counts: counts}
	}
	out := map[string]EstimateCoverage{}
	for team, day := range latestDay {
		coverage := EstimateCoverage{Day: day, Scopes: len(rows[team]), Counts: map[string]int64{}}
		for _, row := range rows[team] {
			for column, value := range row.counts {
				coverage.Counts[column] += value
			}
		}
		out[team] = coverage
	}
	return out, nil
}

// RepositoryIDs lists the organization's repository ids, sorted.
func (s *Store) RepositoryIDs() []string { return sortedKeys(s.repoIDs) }

// TeamIDs lists the active teams, sorted.
func (s *Store) TeamIDs() []string {
	seen := map[string]bool{}
	for _, row := range s.extract.Tables[tableTeams] {
		id, ok := rowString(row, "id")
		if !ok || id == "" {
			continue
		}
		if active, isNum := row["is_active"].(json.Number); isNum && active.String() == "0" {
			continue
		}
		seen[id] = true
	}
	return sortedKeys(seen)
}

type ownershipRow struct {
	team, provider, name string
	repoID               *string
}

// ownedRepositories is the reference reading of team ownership for a window
// starting at instant at: the rows that have not ended by then (valid_to NULL
// or after at; valid_from is a sync stamp and never filters), a row's
// own repo_id first, else the repository whose provider and lower-cased name
// match (design K11). byNameOnly lists, per team, the repositories that only
// a row with no repo_id names.
func (s *Store) ownedRepositories(at time.Time) (owned map[string]map[string]bool, byNameOnly map[string]map[string]bool, err error) {
	type repoKey struct{ provider, name string }
	byName := map[repoKey]string{}
	known := map[string]bool{}
	for _, row := range s.extract.Tables[tableRepos] {
		id, _ := rowString(row, "id")
		provider, _ := rowString(row, "provider")
		name, _ := rowString(row, "repo")
		known[strings.ToLower(id)] = true
		byName[repoKey{provider, strings.ToLower(strings.TrimSpace(name))}] = strings.ToLower(id)
	}
	owned, byNameOnly = map[string]map[string]bool{}, map[string]map[string]bool{}
	byID := map[string]map[string]bool{}
	for _, row := range s.extract.Tables[tableTeamRepoOwnership] {
		if to, ok := rowString(row, "valid_to"); ok {
			until, terr := parseInstant(to)
			if terr != nil {
				return nil, nil, terr
			}
			if !until.After(at) {
				continue
			}
		}
		team, _ := rowString(row, "team_id")
		provider, _ := rowString(row, "provider")
		name, _ := rowString(row, "repo_full_name")
		repo, hasID := rowString(row, "repo_id")
		repo = strings.ToLower(repo)
		if !hasID {
			resolved, named := byName[repoKey{provider, strings.ToLower(strings.TrimSpace(name))}]
			if !named {
				continue
			}
			repo = resolved
		}
		if !known[repo] {
			continue
		}
		if owned[team] == nil {
			owned[team], byNameOnly[team], byID[team] = map[string]bool{}, map[string]bool{}, map[string]bool{}
		}
		owned[team][repo] = true
		if hasID {
			byID[team][repo] = true
		} else {
			byNameOnly[team][repo] = true
		}
	}
	for team, repos := range byNameOnly {
		for repo := range repos {
			if byID[team][repo] {
				delete(repos, repo)
			}
		}
	}
	return owned, byNameOnly, nil
}

// coOwnerShareFloor is the smallest stored share a co-owner row may carry: the
// ops writer gives a co-owner below it no row (chris 2026-10-15, CHAOS-9822;
// ops internal/providersync/team_repo_ownership_derivation.go
// teamRepoOwnershipCoOwnerShareFloor). A co-owner at exactly the floor keeps
// its row.
const coOwnerShareFloor = 0.01

// ownerShareSumTolerance: the owner shares of one repository sum to 1 within
// it.
const ownerShareSumTolerance = 1e-9

// OwnerShareBreachKind names a breach of the ownership share contract (D5988)
// in the stored rows.
type OwnerShareBreachKind string

const (
	// BreachShareSum: the owner shares of a repository do not sum to 1.
	BreachShareSum OwnerShareBreachKind = "share_sum"
	// BreachCoOwnerBelowFloor: a co-owner of a repository with more than one
	// owner has a stored share below coOwnerShareFloor.
	BreachCoOwnerBelowFloor OwnerShareBreachKind = "co_owner_below_floor"
)

// OwnerShareBreach is one breach of the share contract, by repository.
type OwnerShareBreach struct {
	Kind  OwnerShareBreachKind
	Repo  string
	Teams []string
	// Values holds, per team, the share the breach is about: the owner
	// share for share_sum, the stored share for
	// co_owner_below_floor.
	Values map[string]float64
	Detail string
}

// RepositoryOwner is one owning team of a repository as of an instant.
type RepositoryOwner struct {
	Team string
	// Stored is the team's stored share: the largest non-NULL share of its
	// valid rows; nil when every one of them is NULL (not measured).
	Stored *float64
	// Share is the owner share: Stored over the sum of Stored of every owner
	// of the repository, or 1/n when an owner has no stored share or the
	// stored shares sum to nothing.
	Share float64
	// Main is true when a valid row of the team has is_primary = 1, the ops
	// main owner (link rank).
	Main bool
}

// OwnerShareReading is the owners of every repository as of one instant, by
// the ops rule, with the breaches of the share contract in the rows.
type OwnerShareReading struct {
	AsOf time.Time
	// Owners holds, per repository, its owners sorted by team; the shares
	// are computed over every owner, never over the teams in view.
	Owners map[string][]RepositoryOwner
	// EqualSplit lists the repositories whose owners split equally: an
	// owner has no stored share, or the stored shares sum to nothing (the
	// known limit of D5988, not a breach).
	EqualSplit []string
	// Clamped lists the repositories whose first record is after AsOf: the
	// owners valid at that first record were read (ops ownershipwindow). The
	// extract holds no row that ended before its window, so for such a
	// repository it cannot tell an owner of the first record from a gap
	// between two owners of the full table; a reader must not take a clamped
	// repository as measured.
	Clamped []string
	// Breaches are the breaches of the share contract, by repository and
	// kind. They are findings of the owner-share pair, never dropped.
	Breaches []OwnerShareBreach
}

// Share is the owner share of team in repo, 0 when the team does not own it.
func (r *OwnerShareReading) Share(repo, team string) float64 {
	for _, owner := range r.Owners[repo] {
		if owner.Team == team {
			return owner.Share
		}
	}
	return 0
}

// ForTeams narrows the owners to the teams in view AFTER the shares were
// computed over every owner (ops teamscope.OwnerShares, CHAOS-9413): a
// co-owner in view keeps its own share of a repository, never the whole of
// it. It returns team -> repository -> owner share.
func (r *OwnerShareReading) ForTeams(teams []string) map[string]map[string]float64 {
	inView := map[string]bool{}
	for _, team := range teams {
		inView[team] = true
	}
	out := map[string]map[string]float64{}
	for repo, owners := range r.Owners {
		for _, owner := range owners {
			if !inView[owner.Team] {
				continue
			}
			if out[owner.Team] == nil {
				out[owner.Team] = map[string]float64{}
			}
			out[owner.Team][repo] = owner.Share
		}
	}
	return out
}

// OwningTeams lists the teams that own a repository, sorted.
func (r *OwnerShareReading) OwningTeams() []string {
	teams := map[string]bool{}
	for _, owners := range r.Owners {
		for _, owner := range owners {
			teams[owner.Team] = true
		}
	}
	return sortedKeys(teams)
}

// asciiLower is ClickHouse lower(): ASCII letters only.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// trimSpaces is ClickHouse trimBoth(): spaces (ASCII 32) only.
func trimSpaces(s string) string { return strings.Trim(s, " ") }

func rowNullableFloat(row Row, column string) (*float64, error) {
	if v, ok := row[column]; !ok || v == nil {
		return nil, nil
	}
	f, err := rowFloat(row, column)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// inactiveTeams is ops ownershipwindow.InactiveTeams over the teams rows: per
// raw id, the newest row by (updated_at, last_synced, is_active) decides
// (teamactive.NewestRowInactive); the ids are compared trimmed.
func (s *Store) inactiveTeams() (map[string]bool, error) {
	type newest struct {
		updated, synced time.Time
		active          float64
	}
	byID := map[string]newest{}
	for _, row := range s.extract.Tables[tableTeams] {
		id, ok := rowString(row, "id")
		if !ok {
			continue
		}
		updated, err := rowTime(row, "updated_at")
		if err != nil {
			return nil, fmt.Errorf("teams: %w", err)
		}
		synced, err := rowTime(row, "last_synced")
		if err != nil {
			return nil, fmt.Errorf("teams: %w", err)
		}
		active, err := rowFloat(row, "is_active")
		if err != nil {
			return nil, fmt.Errorf("teams: %w", err)
		}
		next := newest{updated, synced, active}
		if prior, seen := byID[id]; seen {
			later := next.updated.After(prior.updated) ||
				(next.updated.Equal(prior.updated) && (next.synced.After(prior.synced) || (next.synced.Equal(prior.synced) && next.active > prior.active)))
			if !later {
				continue
			}
		}
		byID[id] = next
	}
	out := map[string]bool{}
	for id, row := range byID {
		if row.active == 0 {
			out[trimSpaces(id)] = true
		}
	}
	return out, nil
}

// OwnerShares is the reference reading of ops teamscope.OwnerShares (D5988,
// CHAOS-9408) over the extract rows, as of asOf:
//
//   - team_repo_ownership as FINAL reads it: per sorting key (provider,
//     repo_full_name, team_id, source, valid_from) the row of the newest
//     updated_at, the last one read on a tie;
//   - a row owns when its team_id is not empty, its team is not inactive
//     (inactiveTeams), and it resolves to a repository of the catalog: its own
//     repo_id, else every repos id (repos as FINAL reads it) of the same
//     provider whose lower-cased name equals its lower-cased repo_full_name;
//   - a row holds at the later of asOf and the first record of its
//     repository (the earliest valid_from of the owning rows of active teams,
//     ops ownershipwindow): valid_from <= that instant < valid_to (or open);
//   - a team's stored share of a repository is the largest non-NULL share of
//     its valid rows, NULL when all are NULL; the owner share is stored over
//     the sum of stored over ALL owners when no owner is NULL and the sum is
//     above 0, else 1/(number of owners).
//
// The breaches of the share contract are returned in the reading, never
// dropped: the owner shares of a repository sum to 1; no co-owner of a
// repository with more than one owner has a stored share below the 1% floor.
// The ops main owner (is_primary = 1) and every owner at the largest share are
// not co-owners: ops keeps a main owner's row whatever its share, and a full
// tie makes every tied team a main owner (D5990; team-attribution.md section
// 0.4d), so a tie is no breach.
//
// This reading differs from ownedRepositories, acr's own reading of team
// ownership (design K11), which never filters by valid_from.
func (s *Store) OwnerShares(asOf time.Time) (*OwnerShareReading, error) {
	asOf = asOf.UTC()
	inactive, err := s.inactiveTeams()
	if err != nil {
		return nil, err
	}

	// repos as FINAL reads it: ReplacingMergeTree(last_synced) ORDER BY
	// (org_id, id).
	type repoRow struct {
		at             time.Time
		provider, name string
	}
	catalog := map[string]repoRow{}
	for _, row := range s.extract.Tables[tableRepos] {
		id, ok := rowString(row, "id")
		if !ok {
			continue
		}
		at, terr := rowTime(row, "last_synced")
		if terr != nil {
			return nil, fmt.Errorf("repos: %w", terr)
		}
		id = strings.ToLower(id)
		if prior, seen := catalog[id]; seen && at.Before(prior.at) {
			continue
		}
		provider, _ := rowString(row, "provider")
		name, _ := rowString(row, "repo")
		catalog[id] = repoRow{at: at, provider: provider, name: name}
	}
	byName := map[string][]string{}
	for _, id := range mapKeys(catalog) {
		entry := catalog[id]
		key := entry.provider + "\x00" + asciiLower(entry.name)
		byName[key] = append(byName[key], id)
	}

	// team_repo_ownership as FINAL reads it: ReplacingMergeTree(updated_at)
	// ORDER BY (org_id, provider, repo_full_name, team_id, source,
	// valid_from).
	type ownKey struct {
		provider, name, team, source string
		from                         int64
	}
	type ownRow struct {
		updated  time.Time
		from     time.Time
		to       *time.Time
		team     string
		repos    []string
		share    *float64
		primary  bool
		resolves bool
	}
	final := map[ownKey]*ownRow{}
	var keys []ownKey
	for _, row := range s.extract.Tables[tableTeamRepoOwnership] {
		from, ferr := rowTime(row, "valid_from")
		if ferr != nil {
			return nil, fmt.Errorf("team_repo_ownership: %w", ferr)
		}
		updated, uerr := rowTime(row, "updated_at")
		if uerr != nil {
			return nil, fmt.Errorf("team_repo_ownership: %w", uerr)
		}
		provider, _ := rowString(row, "provider")
		name, _ := rowString(row, "repo_full_name")
		team, _ := rowString(row, "team_id")
		source, _ := rowString(row, "source")
		key := ownKey{provider: provider, name: name, team: team, source: source, from: from.UnixNano()}
		prior, seen := final[key]
		if seen && updated.Before(prior.updated) {
			continue
		}
		if !seen {
			keys = append(keys, key)
		}
		next := &ownRow{updated: updated, from: from, team: team}
		if to, ok := rowString(row, "valid_to"); ok {
			until, terr := parseInstant(to)
			if terr != nil {
				return nil, fmt.Errorf("team_repo_ownership: %w", terr)
			}
			next.to = &until
		}
		if next.share, err = rowNullableFloat(row, "share"); err != nil {
			return nil, fmt.Errorf("team_repo_ownership: %w", err)
		}
		if primary, perr := rowFloat(row, "is_primary"); perr == nil && primary == 1 {
			next.primary = true
		}
		if id, ok := rowString(row, "repo_id"); ok {
			next.repos, next.resolves = []string{strings.ToLower(id)}, true
		} else {
			next.repos = byName[provider+"\x00"+asciiLower(name)]
			next.resolves = len(next.repos) > 0
		}
		final[key] = next
	}

	owning := func(r *ownRow) bool { return r.team != "" && !inactive[trimSpaces(r.team)] && r.resolves }
	first := map[string]time.Time{}
	for _, key := range keys {
		r := final[key]
		if !owning(r) {
			continue
		}
		for _, repo := range r.repos {
			if at, seen := first[repo]; !seen || r.from.Before(at) {
				first[repo] = r.from
			}
		}
	}

	type pairKey struct{ repo, team string }
	owners := map[pairKey]*RepositoryOwner{}
	clamped := map[string]bool{}
	for _, key := range keys {
		r := final[key]
		if !owning(r) {
			continue
		}
		for _, repo := range r.repos {
			if _, inCatalog := catalog[repo]; !inCatalog {
				continue
			}
			at := asOf
			if f := first[repo]; f.After(asOf) {
				at = f
			}
			if r.from.After(at) || (r.to != nil && !r.to.After(at)) {
				continue
			}
			if at.After(asOf) {
				clamped[repo] = true
			}
			pk := pairKey{repo, r.team}
			owner := owners[pk]
			if owner == nil {
				owner = &RepositoryOwner{Team: r.team}
				owners[pk] = owner
			}
			if r.share != nil && (owner.Stored == nil || *r.share > *owner.Stored) {
				share := *r.share
				owner.Stored = &share
			}
			owner.Main = owner.Main || r.primary
		}
	}

	out := &OwnerShareReading{AsOf: asOf, Owners: map[string][]RepositoryOwner{}, Clamped: sortedKeys(clamped)}
	for pk, owner := range owners {
		out.Owners[pk.repo] = append(out.Owners[pk.repo], *owner)
	}
	repos := make([]string, 0, len(out.Owners))
	for repo := range out.Owners {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		list := out.Owners[repo]
		sort.Slice(list, func(i, j int) bool { return list[i].Team < list[j].Team })
		measured, sum := true, 0.0
		for _, owner := range list {
			if owner.Stored == nil {
				measured = false
				continue
			}
			sum += *owner.Stored
		}
		measured = measured && sum > 0
		for i := range list {
			if measured {
				list[i].Share = *list[i].Stored / sum
			} else {
				list[i].Share = 1 / float64(len(list))
			}
		}
		if !measured {
			out.EqualSplit = append(out.EqualSplit, repo)
		}
		out.Breaches = append(out.Breaches, shareContractBreaches(repo, list)...)
	}
	return out, nil
}

// shareContractBreaches checks the owners of one repository against the share
// contract.
func shareContractBreaches(repo string, owners []RepositoryOwner) []OwnerShareBreach {
	var out []OwnerShareBreach
	shares := map[string]float64{}
	total := 0.0
	for _, owner := range owners {
		shares[owner.Team] = owner.Share
		total += owner.Share
	}
	if math.IsNaN(total) || math.Abs(total-1) > ownerShareSumTolerance {
		out = append(out, OwnerShareBreach{Kind: BreachShareSum, Repo: repo, Teams: teamsOf(owners), Values: shares,
			Detail: fmt.Sprintf("the owner shares of the repository sum to %s, not 1", formatFloat(total))})
	}
	if len(owners) < 2 {
		return out
	}
	largest := math.Inf(-1)
	for _, owner := range owners {
		largest = math.Max(largest, owner.Share)
	}
	// A full tie of the largest share is valid: every tied team is a main
	// owner and keeps its row whatever its share (D5990; team-attribution.md
	// section 0.4d). The floor binds lesser owners only, so the main owner
	// (is_primary = 1) and every owner at the largest share are exempt.
	top := map[string]bool{}
	for _, owner := range owners {
		if largest-owner.Share <= ownerShareSumTolerance {
			top[owner.Team] = true
		}
	}
	var below []string
	stored := map[string]float64{}
	for _, owner := range owners {
		if owner.Main || top[owner.Team] || owner.Stored == nil || *owner.Stored >= coOwnerShareFloor {
			continue
		}
		below = append(below, owner.Team)
		stored[owner.Team] = *owner.Stored
	}
	if len(below) > 0 {
		out = append(out, OwnerShareBreach{Kind: BreachCoOwnerBelowFloor, Repo: repo, Teams: below, Values: stored,
			Detail: fmt.Sprintf("%d co-owners of the repository have a stored share below the %s floor; the ops writer gives them no row", len(below), formatFloat(coOwnerShareFloor))})
	}
	return out
}

func teamsOf(owners []RepositoryOwner) []string {
	out := make([]string, 0, len(owners))
	for _, owner := range owners {
		out = append(out, owner.Team)
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
