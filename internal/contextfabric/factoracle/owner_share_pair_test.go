package factoracle

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// The owner-share pair (CHAOS-12691) on hand-seeded rows. No test here starts
// a container: the store reads the extract rows in Go and the comparator is
// pure.

const (
	osRepoA = "aaaaaaaa-0000-4000-8000-00000000000a"
	osRepoB = "bbbbbbbb-0000-4000-8000-00000000000b"
	osRepoC = "cccccccc-0000-4000-8000-00000000000c"
	osTeamX = "team-x"
	osTeamY = "team-y"
	osTeamZ = "team-z"
)

var osAsOf = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// osOwnership is one ownership row. share "" leaves the column out (a capture
// taken before it was captured), "null" stores NULL.
func osOwnership(team, repo, share string, primary int) Row {
	row := Row{"org_id": FixtureOrgID, "provider": "github", "team_id": team, "repo_id": repo, "repo_full_name": "o/" + repo[:1],
		"match_type": "exact", "source": "inferred", "is_primary": json.Number(fmt.Sprint(primary)), "specificity": json.Number("10"),
		"priority": json.Number("0"), "valid_from": "2026-09-01 00:00:00.000", "valid_to": nil, "updated_at": "2026-09-01 00:00:00.000"}
	switch share {
	case "":
	case "null":
		row["share"] = nil
	default:
		row["share"] = json.Number(share)
	}
	return row
}

func osTeam(id, updated string, active int) Row {
	return Row{"id": id, "org_id": FixtureOrgID, "provider": "linear", "updated_at": updated, "last_synced": updated, "is_active": json.Number(fmt.Sprint(active))}
}

// osExtract is three repositories, three active teams and the ownership rows.
func osExtract(ownership ...Row) *Extract {
	repo := func(id, name string) Row {
		return Row{"id": id, "repo": name, "provider": "github", "org_id": FixtureOrgID, "last_synced": "2026-09-01 00:00:00.000"}
	}
	return &Extract{Tables: map[string][]Row{
		tableRepos:             {repo(osRepoA, "o/a"), repo(osRepoB, "o/b"), repo(osRepoC, "o/c")},
		tableTeams:             {osTeam(osTeamX, "2026-08-01 00:00:00.000", 1), osTeam(osTeamY, "2026-08-01 00:00:00.000", 1), osTeam(osTeamZ, "2026-08-01 00:00:00.000", 1)},
		tableTeamRepoOwnership: ownership,
	}}
}

// osReference: repository A owned by X (main, 0.7) and Y (0.3), B by X alone,
// C by nobody.
func osReference() []Row {
	return []Row{osOwnership(osTeamX, osRepoA, "0.7", 1), osOwnership(osTeamY, osRepoA, "0.3", 2), osOwnership(osTeamX, osRepoB, "1", 1)}
}

func osReading(t *testing.T, extract *Extract) *OwnerShareReading {
	t.Helper()
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	reading, err := store.OwnerShares(osAsOf)
	if err != nil {
		t.Fatal(err)
	}
	return reading
}

// osRepoAnswers are the ops REPO answers, by theme.
var osRepoAnswers = map[string]map[string]float64{
	osRepoA: {"feature_delivery": 10, "maintenance": 4},
	osRepoB: {"feature_delivery": 6, "quality": 2},
	osRepoC: {"feature_delivery": 3, "risk_security": 1},
}

// osWeighted builds an ops TEAM answer from the REPO answers and the weights
// the test states, never from the store reading under test.
func osWeighted(weights map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for repo, weight := range weights {
		for key, value := range osRepoAnswers[repo] {
			out[key] += weight * value
		}
	}
	return out
}

func osCompare(t *testing.T, reading *OwnerShareReading, team map[string]map[string]float64) OwnerShareVerdict {
	t.Helper()
	verdict, err := CompareOwnerShares(OwnerShareInput{Measure: "COUNT", Team: team, Repo: osRepoAnswers, Shares: reading,
		Repositories: []string{osRepoA, osRepoB, osRepoC}})
	if err != nil {
		t.Fatal(err)
	}
	return verdict
}

func osRules(v OwnerShareVerdict) []string {
	seen := map[string]bool{}
	for _, f := range v.Findings {
		seen[string(f.Rule)] = true
	}
	return sortedKeys(seen)
}

func osOwners(reading *OwnerShareReading, repo string) map[string]float64 {
	out := map[string]float64{}
	for _, owner := range reading.Owners[repo] {
		out[owner.Team] = owner.Share
	}
	return out
}

func osSameShares(got, want map[string]float64) bool {
	if len(got) != len(want) {
		return false
	}
	for team, share := range want {
		if g, ok := got[team]; !ok || math.Abs(g-share) > 1e-12 {
			return false
		}
	}
	return true
}

func TestOwnerShareStoreReadsTheStoredShares(t *testing.T) {
	reading := osReading(t, osExtract(osReference()...))
	if got := osOwners(reading, osRepoA); !osSameShares(got, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}) {
		t.Fatalf("owners of A %v, want X 0.7 and Y 0.3", got)
	}
	if got := osOwners(reading, osRepoB); !osSameShares(got, map[string]float64{osTeamX: 1}) {
		t.Fatalf("owners of B %v, want X 1", got)
	}
	if _, owned := reading.Owners[osRepoC]; owned {
		t.Fatalf("C has owners: %v", reading.Owners[osRepoC])
	}
	if len(reading.Breaches) != 0 || len(reading.EqualSplit) != 0 || len(reading.Clamped) != 0 {
		t.Fatalf("breaches %v, equal split %v, clamped %v: want none", reading.Breaches, reading.EqualSplit, reading.Clamped)
	}
	for _, owner := range reading.Owners[osRepoA] {
		if owner.Main != (owner.Team == osTeamX) || owner.Stored == nil {
			t.Fatalf("owner %+v: the main owner is X, each has a stored share", owner)
		}
	}
	// Narrowing comes after the shares: Y in view alone keeps 0.3 of A.
	if got := reading.ForTeams([]string{osTeamY}); len(got) != 1 || len(got[osTeamY]) != 1 || got[osTeamY][osRepoA] != 0.3 {
		t.Fatalf("Y in view alone holds %v, want A 0.3 (never the whole repository)", got)
	}
}

func TestOwnerSharePairPassesOnAnswersBuiltFromTheShares(t *testing.T) {
	reading := osReading(t, osExtract(osReference()...))
	verdict := osCompare(t, reading, map[string]map[string]float64{
		osTeamX: osWeighted(map[string]float64{osRepoA: 0.7, osRepoB: 1}),
		osTeamY: osWeighted(map[string]float64{osRepoA: 0.3}),
	})
	if len(verdict.Findings) != 0 || verdict.Compared == 0 || verdict.Matches != verdict.Compared {
		t.Fatalf("control: %+v", verdict)
	}
	// 3 keys of X, 2 of Y, 4 keys of conservation.
	if verdict.Compared != 9 {
		t.Fatalf("compared %d values, want 9", verdict.Compared)
	}
}

// Each planted defect must fail with its own finding; the control above is
// the same pair on the reference rows and answers.
func TestOwnerSharePairFindsEachPlantedDefect(t *testing.T) {
	planted := append(osReference(), osOwnership(osTeamZ, osRepoA, "0.005", 2))
	cases := []struct {
		name     string
		rows     []Row
		team     map[string]map[string]float64
		rules    []string
		witness  OwnerShareFinding
		breachOf string
	}{
		{
			name: "ops TEAM is the unweighted sum of the owned repositories",
			rows: osReference(),
			team: map[string]map[string]float64{
				osTeamX: osWeighted(map[string]float64{osRepoA: 1, osRepoB: 1}),
				osTeamY: osWeighted(map[string]float64{osRepoA: 1}),
			},
			rules:   []string{string(RuleConservation), string(RuleTeamIsShareWeightedSum)},
			witness: OwnerShareFinding{Rule: RuleTeamIsShareWeightedSum, Team: osTeamX, Key: "feature_delivery", Got: 16, Want: 13},
		},
		{
			name: "ops TEAM splits a co-owned repository equally",
			rows: osReference(),
			team: map[string]map[string]float64{
				osTeamX: osWeighted(map[string]float64{osRepoA: 0.5, osRepoB: 1}),
				osTeamY: osWeighted(map[string]float64{osRepoA: 0.5}),
			},
			rules:   []string{string(RuleTeamIsShareWeightedSum)},
			witness: OwnerShareFinding{Rule: RuleTeamIsShareWeightedSum, Team: osTeamX, Key: "feature_delivery", Got: 11, Want: 13},
		},
		{
			name: "ops TEAM narrows to the team in view before the shares",
			rows: osReference(),
			team: map[string]map[string]float64{
				osTeamX: osWeighted(map[string]float64{osRepoA: 0.7, osRepoB: 1}),
				osTeamY: osWeighted(map[string]float64{osRepoA: 1}),
			},
			rules:   []string{string(RuleConservation), string(RuleTeamIsShareWeightedSum)},
			witness: OwnerShareFinding{Rule: RuleTeamIsShareWeightedSum, Team: osTeamY, Key: "feature_delivery", Got: 10, Want: 3},
		},
		{
			// The answers are right for the planted rows: only the contract
			// of the rows fails.
			name: "a co-owner row with a stored share below 1%",
			rows: planted,
			team: map[string]map[string]float64{
				osTeamX: osWeighted(map[string]float64{osRepoA: 0.7 / 1.005, osRepoB: 1}),
				osTeamY: osWeighted(map[string]float64{osRepoA: 0.3 / 1.005}),
				osTeamZ: osWeighted(map[string]float64{osRepoA: 0.005 / 1.005}),
			},
			rules:    []string{string(RuleShareContract)},
			witness:  OwnerShareFinding{Rule: RuleShareContract, Repo: osRepoA, Breach: BreachCoOwnerBelowFloor},
			breachOf: osTeamZ,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reading := osReading(t, osExtract(tc.rows...))
			verdict := osCompare(t, reading, tc.team)
			if got := osRules(verdict); strings.Join(got, ",") != strings.Join(tc.rules, ",") {
				t.Fatalf("rules %v, want %v: %+v", got, tc.rules, verdict.Findings)
			}
			found := false
			for _, f := range verdict.Findings {
				if f.Rule == tc.witness.Rule && f.Team == tc.witness.Team && f.Key == tc.witness.Key && f.Repo == tc.witness.Repo &&
					f.Breach == tc.witness.Breach && math.Abs(f.Got-tc.witness.Got) < 1e-9 && math.Abs(f.Want-tc.witness.Want) < 1e-9 {
					found = true
				}
				if pf := f.Finding(); pf.Pair != pairOwnerShare || pf.Path != string(f.Rule) || pf.Detail == "" {
					t.Fatalf("report form %+v of %+v", pf, f)
				}
			}
			if !found {
				t.Fatalf("no finding %+v among %+v", tc.witness, verdict.Findings)
			}
			if tc.breachOf != "" {
				if len(reading.Breaches) != 1 || len(reading.Breaches[0].Teams) != 1 || reading.Breaches[0].Teams[0] != tc.breachOf ||
					reading.Breaches[0].Values[tc.breachOf] != 0.005 {
					t.Fatalf("breaches %+v, want one naming %s at its stored 0.005", reading.Breaches, tc.breachOf)
				}
			}
		})
	}
}

func TestOwnerShareANullShareSplitsTheRepositoryEqually(t *testing.T) {
	for _, tc := range []struct {
		name   string
		xShare string
		yShare string
	}{
		{"no share column (the recorded capture)", "", ""},
		{"NULL beside a stored share", "0.7", "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reading := osReading(t, osExtract(osOwnership(osTeamX, osRepoA, tc.xShare, 1), osOwnership(osTeamY, osRepoA, tc.yShare, 2),
				osOwnership(osTeamX, osRepoB, "1", 1)))
			if got := osOwners(reading, osRepoA); !osSameShares(got, map[string]float64{osTeamX: 0.5, osTeamY: 0.5}) {
				t.Fatalf("owners of A %v, want 1/2 each", got)
			}
			if strings.Join(reading.EqualSplit, ",") != osRepoA || len(reading.Breaches) != 0 {
				t.Fatalf("equal split %v, breaches %+v: want A split, no breach", reading.EqualSplit, reading.Breaches)
			}
			verdict := osCompare(t, reading, map[string]map[string]float64{
				osTeamX: osWeighted(map[string]float64{osRepoA: 0.5, osRepoB: 1}),
				osTeamY: osWeighted(map[string]float64{osRepoA: 0.5}),
			})
			if len(verdict.Findings) != 0 || len(verdict.Notes) != 1 || !strings.Contains(verdict.Notes[0], osRepoA) {
				t.Fatalf("verdict %+v: want a pass that names A as split equally", verdict)
			}
		})
	}
}

// A full tie of the largest share is valid ownership: every tied team is a
// main owner (D5990; team-attribution.md section 0.4d). It is no breach, and
// a tied owner reads its equal half.
func TestOwnerShareAFullTieIsValidOwnership(t *testing.T) {
	reading := osReading(t, osExtract(osOwnership(osTeamX, osRepoA, "0.5", 1), osOwnership(osTeamY, osRepoA, "0.5", 1)))
	if len(reading.Breaches) != 0 {
		t.Fatalf("breaches %+v, want none: a full tie is valid (D5990)", reading.Breaches)
	}
	verdict := osCompare(t, reading, map[string]map[string]float64{
		osTeamX: osWeighted(map[string]float64{osRepoA: 0.5}),
		osTeamY: osWeighted(map[string]float64{osRepoA: 0.5}),
	})
	if len(verdict.Findings) != 0 {
		t.Fatalf("verdict %+v: want a pass", verdict)
	}
}

func TestOwnerShareAMissingAnswerIsAFindingAndStopsConservation(t *testing.T) {
	reading := osReading(t, osExtract(osReference()...))
	repos := map[string]map[string]float64{osRepoA: osRepoAnswers[osRepoA], osRepoB: osRepoAnswers[osRepoB]}
	verdict, err := CompareOwnerShares(OwnerShareInput{Measure: "ISSUE_SIZE", Repo: repos, Shares: reading,
		Team:         map[string]map[string]float64{osTeamX: osWeighted(map[string]float64{osRepoA: 0.7, osRepoB: 1})},
		Repositories: []string{osRepoA, osRepoB, osRepoC}})
	if err != nil {
		t.Fatal(err)
	}
	if got := osRules(verdict); strings.Join(got, ",") != string(RuleRepoAnswerMissing)+","+string(RuleTeamAnswerMissing) {
		t.Fatalf("rules %v: %+v", got, verdict.Findings)
	}
	for _, f := range verdict.Findings {
		if (f.Rule == RuleTeamAnswerMissing && f.Team != osTeamY) || (f.Rule == RuleRepoAnswerMissing && f.Repo != osRepoC) {
			t.Fatalf("finding %+v names the wrong subject", f)
		}
	}
	if verdict.Matches != 3 || !strings.Contains(strings.Join(verdict.Notes, ";"), "conservation was not compared") {
		t.Fatalf("verdict %+v: X is compared, conservation is not", verdict)
	}
}

func TestOwnerSharePairRefusesAMeasureThatIsNotAdditive(t *testing.T) {
	reading := osReading(t, osExtract(osReference()...))
	for _, measure := range []string{"CYCLE_TIME_HOURS", "PR_REWORK_RATIO", "TEST_PASS_RATE", "THROUGHPUT", ""} {
		_, err := CompareOwnerShares(OwnerShareInput{Measure: measure, Shares: reading})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", measure)) {
			t.Errorf("measure %q: %v, want a refusal that names it", measure, err)
		}
	}
	for _, measure := range []string{"COUNT", "ISSUE_SIZE"} {
		if _, err := CompareOwnerShares(OwnerShareInput{Measure: measure, Shares: reading}); err != nil {
			t.Errorf("measure %s refused: %v", measure, err)
		}
	}
}

// The store reads the rows as the ops statement does.
func TestOwnerShareStoreReadsTheRowsAsOpsDoes(t *testing.T) {
	type change func(rows []Row, extract *Extract) []Row
	cases := []struct {
		name    string
		change  change
		want    map[string]float64
		clamped bool
	}{
		{"a row that ended before the instant owns nothing", func(rows []Row, _ *Extract) []Row {
			rows[1]["valid_to"] = "2026-09-15 00:00:00.000"
			return rows
		}, map[string]float64{osTeamX: 1}, false},
		{"an inactive team owns nothing (its newest teams row decides)", func(rows []Row, e *Extract) []Row {
			e.Tables[tableTeams] = append(e.Tables[tableTeams], osTeam(osTeamY, "2026-09-01 00:00:00.000", 0))
			return rows
		}, map[string]float64{osTeamX: 1}, false},
		{"a team set inactive and then active again owns", func(rows []Row, e *Extract) []Row {
			e.Tables[tableTeams] = append(e.Tables[tableTeams], osTeam(osTeamY, "2026-07-01 00:00:00.000", 0))
			return rows
		}, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}, false},
		{"a newer version of a row under the same key replaces its share", func(rows []Row, _ *Extract) []Row {
			newer := cloneRow(rows[1])
			newer["share"], newer["updated_at"] = json.Number("0.2"), "2026-09-20 00:00:00.000"
			return append(rows, newer)
		}, map[string]float64{osTeamX: 0.7 / 0.9, osTeamY: 0.2 / 0.9}, false},
		{"a team's stored share is the largest of its valid rows", func(rows []Row, _ *Extract) []Row {
			other := cloneRow(rows[1])
			other["source"], other["share"] = "provider_access", nil
			another := cloneRow(rows[1])
			another["valid_from"], another["share"] = "2026-09-10 00:00:00.000", json.Number("0.1")
			return append(rows, other, another)
		}, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}, false},
		{"a row with no repo_id resolves by provider and lower-cased name", func(rows []Row, _ *Extract) []Row {
			rows[1]["repo_id"], rows[1]["repo_full_name"] = nil, "O/A"
			return rows
		}, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}, false},
		{"a row of another provider's name resolves to nothing", func(rows []Row, _ *Extract) []Row {
			rows[1]["repo_id"], rows[1]["provider"] = nil, "gitlab"
			return rows
		}, map[string]float64{osTeamX: 1}, false},
		{"a row of a repository the catalog does not hold owns nothing", func(rows []Row, _ *Extract) []Row {
			rows[1]["repo_id"] = "dddddddd-0000-4000-8000-00000000000d"
			return rows
		}, map[string]float64{osTeamX: 1}, false},
		{"a row with an empty team id owns nothing", func(rows []Row, _ *Extract) []Row {
			return append(rows, osOwnership("", osRepoA, "0.5", 2))
		}, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}, false},
		{"a row that starts after the instant does not own when the repository has an earlier record", func(rows []Row, _ *Extract) []Row {
			rows[1]["valid_from"] = "2026-10-15 00:00:00.000"
			return rows
		}, map[string]float64{osTeamX: 1}, false},
		{"before its first record a repository reads its first owners", func(rows []Row, _ *Extract) []Row {
			for _, row := range rows {
				row["valid_from"] = "2026-10-15 00:00:00.000"
			}
			return rows
		}, map[string]float64{osTeamX: 0.7, osTeamY: 0.3}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extract := osExtract()
			extract.Tables[tableTeamRepoOwnership] = tc.change(osReference(), extract)
			reading := osReading(t, extract)
			if got := osOwners(reading, osRepoA); !osSameShares(got, tc.want) {
				t.Fatalf("owners of A %v, want %v", got, tc.want)
			}
			clamped := false
			for _, repo := range reading.Clamped {
				clamped = clamped || repo == osRepoA
			}
			if clamped != tc.clamped {
				t.Fatalf("clamped %v, want A clamped %v", reading.Clamped, tc.clamped)
			}
		})
	}
}

// The extract carries the share and keeps of one row key the version FINAL
// keeps, so a capture holds what the store reads.
func TestOwnerShareTheExtractCarriesTheShare(t *testing.T) {
	for _, spec := range extractTables {
		if spec.Table != tableTeamRepoOwnership {
			continue
		}
		grouped := false
		for _, column := range strings.Split(spec.LimitBy, ",") {
			grouped = grouped || strings.TrimSpace(column) == "share"
		}
		if spec.Rules["share"] != ruleKeep || !grouped || !strings.Contains(spec.OrderBy, "updated_at DESC") {
			t.Fatalf("ownership spec %+v: share must be kept and grouped, the newest version of a key kept", spec)
		}
		return
	}
	t.Fatal("the extract has no ownership table")
}

// The capture of CHAOS-12808 carries the share (the extract keeps the column
// since CHAOS-12691): at the window's end every owner reads its stored share,
// no owned repository splits equally, and nothing is a breach. On askdev-1
// each owned repository has one owner.
func TestOwnerShareTheRecordedCaptureReadsTheStoredShare(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	reading, err := store.OwnerShares(manifest.Window.End)
	if err != nil {
		t.Fatal(err)
	}
	if len(reading.Owners) == 0 {
		t.Fatal("the recorded capture has no owned repository; this test checks nothing")
	}
	for repo, owners := range reading.Owners {
		for _, owner := range owners {
			if owner.Stored == nil {
				t.Fatalf("repository %s: owner %s reads no stored share in a capture that carries the column", repo, owner.Team)
			}
		}
	}
	if len(reading.EqualSplit) != 0 || len(reading.Breaches) != 0 {
		t.Fatalf("%d of %d owned repositories split equally, breaches %+v", len(reading.EqualSplit), len(reading.Owners), reading.Breaches)
	}
}
