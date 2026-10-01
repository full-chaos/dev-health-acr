package factoracle

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Store is the reference reading of the extract rows: what the two planes
// read, reduced only as far as the witnesses of the named classes need. It
// does not attribute effort to repositories; that is the acr producer's
// work and it is never repeated here.
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
}

type unitGeneration struct {
	computedAt time.Time
	fromTS     time.Time
	toTS       time.Time
	repoID     *string
	effort     float64
	themes     map[string]float64
	subcats    map[string]float64
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
	s := &Store{extract: extract, units: map[string]*unitGenerations{}, superseded: map[string]bool{}, inRun: map[string]bool{}}
	generations := map[string][]unitGeneration{}
	for _, row := range extract.Tables["work_unit_investments"] {
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
		if gen.effort, err = rowFloat(row, "effort_value"); err != nil {
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
	for _, row := range extract.Tables["work_unit_supersessions"] {
		if id, ok := rowString(row, "superseded_work_unit_id"); ok {
			s.superseded[id] = true
		}
	}
	var latestMarker time.Time
	for _, row := range extract.Tables["work_unit_membership_runs"] {
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
	for _, row := range extract.Tables["work_unit_membership"] {
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

// OrgThemeEffort is the ops ORG reading under CHURN_LOC by theme: the sum,
// over the latest generation of every work unit in scope and in the window,
// of effort_value x subcategory share, grouped by the theme prefix of the
// subcategory key, with the share as the Float32 ops casts it to
// (dev-health-ops internal/queryapi/analytics investmentContextFor).
func (s *Store) OrgThemeEffort(w Window) map[string]float64 {
	out := map[string]float64{}
	for _, id := range s.unitIDs() {
		unit := s.units[id]
		if !s.inScope(id) || !w.holds(unit.latest) {
			continue
		}
		addSubcategoryEffort(out, unit.latest)
	}
	return out
}

func addSubcategoryEffort(out map[string]float64, gen unitGeneration) {
	keys := make([]string, 0, len(gen.subcats))
	for k := range gen.subcats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		theme, _, _ := strings.Cut(key, ".")
		out[theme] += float64(float32(gen.subcats[key])) * gen.effort
	}
}

func addThemeEffort(out map[string]float64, gen unitGeneration) {
	for theme, share := range gen.themes {
		out[theme] += share * gen.effort
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

// Witnesses are, per class, the theme effort the store holds that an acr
// reader without that class's rule would add to a repository mix. It is an
// upper bound per theme (a unit may reach no repository); it is exact for a
// unit that names its repository itself.
type Witnesses map[Class]map[string]float64

// Witnesses computes the witness of every investment regression class.
func (s *Store) Witnesses(w Window) Witnesses {
	out := Witnesses{ClassSupersession: {}, ClassMembershipScope: {}, ClassNullableArgmax: {}}
	for _, id := range s.unitIDs() {
		unit := s.units[id]
		if !w.holds(unit.latest) {
			continue
		}
		switch {
		case s.superseded[id]:
			addThemeEffort(out[ClassSupersession], unit.latest)
		case s.scopeRun != "" && !s.inRun[id]:
			addThemeEffort(out[ClassMembershipScope], unit.latest)
		case unit.revivedRepo != "":
			addThemeEffort(out[ClassNullableArgmax], unit.latest)
		}
	}
	return out
}

// RepositoryIDs lists the organization's repository ids, sorted.
func (s *Store) RepositoryIDs() []string {
	seen := map[string]bool{}
	for _, row := range s.extract.Tables["repos"] {
		if id, ok := rowString(row, "id"); ok {
			seen[strings.ToLower(id)] = true
		}
	}
	return sortedKeys(seen)
}

// TeamIDs lists the active teams, sorted.
func (s *Store) TeamIDs() []string {
	seen := map[string]bool{}
	for _, row := range s.extract.Tables["teams"] {
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

// ownedRepositories is the reference reading of team ownership at instant at:
// the rows valid then (valid_from <= at, valid_to NULL or after at), a row's
// own repo_id first, else the repository whose provider and lower-cased name
// match (design K11). byNameOnly lists, per team, the repositories that only
// a row with no repo_id names.
func (s *Store) ownedRepositories(at time.Time) (owned map[string]map[string]bool, byNameOnly map[string]map[string]bool, err error) {
	type repoKey struct{ provider, name string }
	byName := map[repoKey]string{}
	known := map[string]bool{}
	for _, row := range s.extract.Tables["repos"] {
		id, _ := rowString(row, "id")
		provider, _ := rowString(row, "provider")
		name, _ := rowString(row, "repo")
		known[strings.ToLower(id)] = true
		byName[repoKey{provider, strings.ToLower(strings.TrimSpace(name))}] = strings.ToLower(id)
	}
	owned, byNameOnly = map[string]map[string]bool{}, map[string]map[string]bool{}
	byID := map[string]map[string]bool{}
	for _, row := range s.extract.Tables["team_repo_ownership"] {
		from, ferr := rowTime(row, "valid_from")
		if ferr != nil {
			return nil, nil, ferr
		}
		if from.After(at) {
			continue
		}
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

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
