package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// opsLatestRowOrderKey is ops' latestrow.OrderKey at dev-health-ops 1e44cc12de64
// (internal/storage/clickhouse/latestrow/latestrow.go:23), the order every ops
// served reader of work_unit_investments picks its latest row by.
const opsLatestRowOrderKey = "(work_unit_investments.computed_at, toUInt64OrZero(splitByChar('_', work_unit_investments._part)[3]), work_unit_investments._part_offset)"

// tieUnits is how many units carry a same-millisecond tie in one case: an
// arbitrary pick can match the expected row on one unit by chance, on all of
// them only rarely.
const tieUnits = 8

func tieUnitIDs(prefix string) []string {
	ids := make([]string, tieUnits)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return ids
}

type tieVersion struct{ label, theme string }

// tieOrders are the insert orders of two same-millisecond versions of each tie
// unit; the LAST one is the unit's version (the row a merge keeps).
var tieOrders = map[string][]tieVersion{
	"risk written last":    {{"run-q", "quality"}, {"run-r", "risk"}},
	"quality written last": {{"run-r", "risk"}, {"run-q", "quality"}},
}

func mergeWorkUnitInvestments(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range []string{`SYSTEM START MERGES work_unit_investments`, `OPTIMIZE TABLE work_unit_investments FINAL`} {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// Two versions of one work unit that share a computed_at millisecond: the one
// inserted later is the unit's version, in every phase, in the single
// statements, and after a merge. The expected answer is the same read on a
// second database that holds only the later rows, so each unit there has one
// version and no tie.
func TestProjectMixSameMillisecondVersionsPickTheLaterInsertAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	const rowLimit = 201
	for name, order := range tieOrders {
		last := order[len(order)-1]
		t.Run(name+"/roll-up", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			const orgID = "org-7323-tie-rollup"
			at := seedCHAOS7257Parity(t, ctx, direct, orgID)
			ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
			baseline := runRollupPhased(t, ctx, query, orgID, ids, window)
			for _, id := range tieUnitIDs("wu-tie") {
				insertMembership(t, ctx, direct, orgID, id, at)
				for _, v := range order {
					insertRunVersionAtMilli(t, ctx, direct, orgID, id, repoUUID("r1a"), `{"issues":[],"prs":[]}`, v.theme, v.label, at.Add(-time.Hour), at.Add(-73*time.Hour))
				}
			}
			arms := map[string][]rollupRow{
				"phased":                  runRollupPhased(t, ctx, query, orgID, ids, window),
				"phased, chunks of 1 row": runRollupPhased(t, devhealthfacts.WithProjectMixChunking(ctx, 0, 1), query, orgID, ids, window),
				"single statement":        runRollupStatement(t, ctx, query, "SingleRollup", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window),
			}
			var want []rollupRow
			t.Run("want", func(t *testing.T) {
				wantQuery, wantDirect := newScopedCHAOS7257Client(t, nil)
				createCHAOS7257Tables(t, ctx, wantDirect)
				seedCHAOS7257Parity(t, ctx, wantDirect, orgID)
				for _, id := range tieUnitIDs("wu-tie") {
					insertMembership(t, ctx, wantDirect, orgID, id, at)
					insertRunVersionAtMilli(t, ctx, wantDirect, orgID, id, repoUUID("r1a"), `{"issues":[],"prs":[]}`, last.theme, last.label, at.Add(-time.Hour), at.Add(-73*time.Hour))
				}
				want = runRollupPhased(t, ctx, wantQuery, orgID, ids, window)
			})
			if err := rollupRowsDiffer(baseline, want); err == nil {
				t.Fatal("control: the later versions left the answer unchanged, so the cases below prove nothing")
			}
			mergeWorkUnitInvestments(t, ctx, direct)
			arms["phased, after OPTIMIZE FINAL"] = runRollupPhased(t, ctx, query, orgID, ids, window)
			arms["single statement, after OPTIMIZE FINAL"] = runRollupStatement(t, ctx, query, "SingleRollup", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
			for arm, got := range arms {
				if err := rollupRowsDiffer(want, got); err != nil {
					t.Errorf("%s: the tie did not resolve to the later insert (%s): %v", arm, last.label, err)
				}
			}
		})
		t.Run(name+"/native", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			const orgID = "org-7323-tie-native"
			at := seedCHAOS7257Native(t, ctx, direct, orgID)
			ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
			baseline := runNativePhased(t, ctx, query, orgID, ids, window, rowLimit)
			for _, id := range tieUnitIDs("nu-tie") {
				insertMembership(t, ctx, direct, orgID, id, at)
				for _, v := range order {
					insertRunVersionAtMilli(t, ctx, direct, orgID, id, "", `{"issues":["linear:A-1"],"prs":[]}`, v.theme, v.label, at.Add(-time.Hour), at.Add(-73*time.Hour))
				}
			}
			arms := map[string][]nativeRow{
				"phased":                  runNativePhased(t, ctx, query, orgID, ids, window, rowLimit),
				"phased, chunks of 1 row": runNativePhased(t, devhealthfacts.WithProjectMixChunking(ctx, 0, 1), query, orgID, ids, window, rowLimit),
				"single statement":        runNativeStatement(t, ctx, query, "SingleNative", devhealthfacts.ProjectNativeStatement(window, rowLimit), orgID, ids, window),
			}
			var want []nativeRow
			t.Run("want", func(t *testing.T) {
				wantQuery, wantDirect := newScopedCHAOS7257Client(t, nil)
				createCHAOS7257Tables(t, ctx, wantDirect)
				seedCHAOS7257Native(t, ctx, wantDirect, orgID)
				for _, id := range tieUnitIDs("nu-tie") {
					insertMembership(t, ctx, wantDirect, orgID, id, at)
					insertRunVersionAtMilli(t, ctx, wantDirect, orgID, id, "", `{"issues":["linear:A-1"],"prs":[]}`, last.theme, last.label, at.Add(-time.Hour), at.Add(-73*time.Hour))
				}
				want = runNativePhased(t, ctx, wantQuery, orgID, ids, window, rowLimit)
			})
			if err := nativeRowsDiffer(baseline, want); err == nil {
				t.Fatal("control: the later versions left the answer unchanged, so the cases below prove nothing")
			}
			mergeWorkUnitInvestments(t, ctx, direct)
			arms["phased, after OPTIMIZE FINAL"] = runNativePhased(t, ctx, query, orgID, ids, window, rowLimit)
			arms["single statement, after OPTIMIZE FINAL"] = runNativeStatement(t, ctx, query, "SingleNative", devhealthfacts.ProjectNativeStatement(window, rowLimit), orgID, ids, window)
			for arm, got := range arms {
				if err := nativeRowsDiffer(want, got); err != nil {
					t.Errorf("%s: the tie did not resolve to the later insert (%s): %v", arm, last.label, err)
				}
			}
		})
	}
}

// A row written between phase 0 and the later phases into a pinned unit's
// millisecond is that unit's latest row for the later phases only. Its to_ts is
// outside the window, so a blended attempt (phase 0's window, the later phases'
// values) counts the unit, where a consistent read leaves it out. The attempt
// must retry and answer the read after the write.
func TestProjectMixRetriesWhenAPinnedUnitGainsASameMillisecondRowBetweenThePhasesAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7323-grown-rollup"
	at := seedCHAOS7257Parity(t, ctx, direct, orgID)
	window := devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-10 * 24 * time.Hour), End: at.Add(24 * time.Hour)}
	ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
	insertMembership(t, ctx, direct, orgID, "wu-grow", at)
	insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-grow", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "quality", "run-1", at.Add(-time.Hour), at.Add(-73*time.Hour))
	baseline := runRollupPhased(t, ctx, query, orgID, ids, window)
	landed := 0
	hooked, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
		if landed == 0 {
			insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-grow", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "risk", "run-2", at.Add(-time.Hour), at.Add(-60*24*time.Hour))
		}
		landed++
	}), query, orgID, ids, window)
	if err != nil || landed < 2 {
		t.Fatalf("hooked read: err %v, phases ran %d times (want a retry)", err, landed)
	}
	after := runRollupPhased(t, ctx, query, orgID, ids, window)
	if err := rollupRowsDiffer(baseline, after); err == nil {
		t.Fatal("control: the row written between the phases left the answer unchanged, so the case proves nothing")
	}
	if err := rollupRowsDiffer(after, rollupRowsOf(hooked)); err != nil {
		t.Fatalf("an attempt that saw a pinned unit gain a row was answered: %v", err)
	}
}

// The growth guard compares each pinned unit on its own: a merge that collapses
// one unit's same-millisecond rows (2 -> 1) in the same attempt in which another
// unit gains one (1 -> 2) leaves the pinned set's total unchanged, and the
// attempt must still retry.
func TestProjectMixGrowthGuardIsPerUnitNotASetTotalAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7323-grown-per-unit"
	at := seedCHAOS7257Parity(t, ctx, direct, orgID)
	window := devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-10 * 24 * time.Hour), End: at.Add(24 * time.Hour)}
	ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
	for _, id := range []string{"wu-collapse", "wu-grow"} {
		insertMembership(t, ctx, direct, orgID, id, at)
	}
	insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-collapse", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "quality", "collapse-1", at.Add(-time.Hour), at.Add(-73*time.Hour))
	insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-collapse", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "quality", "collapse-2", at.Add(-time.Hour), at.Add(-73*time.Hour))
	insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-grow", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "quality", "grow-1", at.Add(-time.Hour), at.Add(-73*time.Hour))
	baseline := runRollupPhased(t, ctx, query, orgID, ids, window)
	landed := 0
	hooked, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
		if landed == 0 {
			mergeWorkUnitInvestments(t, ctx, direct)
			if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_unit_investments`); err != nil {
				t.Errorf("stop merges: %v", err)
			}
			insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-grow", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "risk", "grow-2", at.Add(-time.Hour), at.Add(-60*24*time.Hour))
		}
		landed++
	}), query, orgID, ids, window)
	if err != nil || landed < 2 {
		t.Fatalf("hooked read: err %v, phases ran %d times (want a retry)", err, landed)
	}
	after := runRollupPhased(t, ctx, query, orgID, ids, window)
	if err := rollupRowsDiffer(baseline, after); err == nil {
		t.Fatal("control: the row written between the phases left the answer unchanged, so the case proves nothing")
	}
	if err := rollupRowsDiffer(after, rollupRowsOf(hooked)); err != nil {
		t.Fatalf("an attempt in which one unit collapsed and another grew was answered: %v", err)
	}
}

// A pinned unit that gains a same-millisecond row between the phases on EVERY
// attempt exhausts the attempts: the read fails as contended, it never answers.
func TestProjectMixFailsContendedWhenAPinnedUnitGrowsOnEveryAttemptAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	for name, run := range map[string]func(ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, ids []string) error{
		"roll-up": func(ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, ids []string) error {
			_, err := devhealthfacts.RunProjectRollupMix(ctx, query, orgID, ids, window)
			return err
		},
		"native": func(ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, ids []string) error {
			_, err := devhealthfacts.RunProjectNativeMix(ctx, query, orgID, ids, window, 201)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			orgID := "org-7323-grows-" + name
			var at time.Time
			var ids []string
			repo, evidence := repoUUID("r1a"), `{"issues":[],"prs":[]}`
			if name == "native" {
				at = seedCHAOS7257Native(t, ctx, direct, orgID)
				ids, repo, evidence = []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}, "", `{"issues":["linear:A-1"],"prs":[]}`
			} else {
				at = seedCHAOS7257Parity(t, ctx, direct, orgID)
				ids = []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
			}
			insertMembership(t, ctx, direct, orgID, "wu-grow", at)
			insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-grow", repo, evidence, "quality", "run-0", at.Add(-time.Hour), at.Add(-73*time.Hour))
			attempts := 0
			err := run(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
				attempts++
				insertRunVersionAtMilli(t, ctx, direct, orgID, "wu-grow", repo, evidence, "risk", fmt.Sprintf("run-%d", attempts), at.Add(-time.Hour), at.Add(-73*time.Hour))
			}), query, orgID, ids)
			var contended *devhealthfacts.ProjectMixContendedError
			if !errors.As(err, &contended) {
				t.Fatalf("a read whose pinned unit grew on all %d attempts returned err %v, want ProjectMixContendedError", attempts, err)
			}
			if attempts < 2 {
				t.Fatalf("phases ran %d times, want every attempt", attempts)
			}
		})
	}
}

// The repository mix (the requested-repository split: its argMaxIf and HAVING
// over a subquery that projects the key as unit_version) and the organization
// mix (the whole-organization split) pick the later insert too, theme,
// repository and evidence gate alike, before and after a merge. The two rows
// differ in all three: one is in repo-a and categorized, the other in repo-b
// and gated. The same tie, read by ops' order key, picks the same row (the
// acr-vs-ops pair).
func TestRepositoryAndOrganizationMixSameMillisecondVersionsPickTheLaterInsertAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	type tieRow struct{ theme, status, repo string }
	categorized := tieRow{"risk", "ok", "repo-a"}
	gated := tieRow{"quality", "insufficient_evidence", "repo-b"}
	// gatedInA is gated in the SAME repository as categorized, so both rows
	// of a wu-both unit reach repo-a and the split's argMaxIf and gate pick
	// between them.
	gatedInA := tieRow{"quality", "insufficient_evidence", "repo-a"}
	type mix struct {
		weighted             map[string]float64
		units, uncategorized int64
	}
	read := func(t *testing.T, ctx context.Context, providers []contextfabric.FactProvider, orgID string, subject contextfabric.SubjectRef) mix {
		t.Helper()
		result, err := findProvider(t, providers, contextfabric.FactInvestment).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: investmentAllHistory(), Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil || len(result.Facts) > 1 {
			t.Fatalf("ReadFacts %s: err %v, %d facts", subject.CanonicalID, err, len(result.Facts))
		}
		out := mix{weighted: map[string]float64{}}
		if len(result.Facts) == 0 {
			return out
		}
		fact := result.Facts[0]
		if v, ok := fact.Fields["theme_breakdown"]; ok && v.Table != nil {
			for _, row := range v.Table.Rows {
				out.weighted[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
			}
		}
		if v, ok := fact.Fields["work_unit_count"]; ok && v.Integer != nil {
			out.units = *v.Integer
		}
		if v, ok := fact.Fields["uncategorized_unit_count"]; ok && v.Integer != nil {
			out.uncategorized = *v.Integer
		}
		return out
	}
	const orgID = "org-7323-tie-repo"
	at := ts(2026, 9, 18, 0, 0, 0)
	seed := func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, tieRows, bothRows []tieRow) {
		t.Helper()
		if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_unit_investments`); err != nil {
			t.Fatalf("stop merges: %v", err)
		}
		for _, label := range []string{"repo-a", "repo-b"} {
			if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
				t.Fatalf("seed repo: %v", err)
			}
		}
		for _, set := range []struct {
			prefix string
			rows   []tieRow
		}{{"wu-tie", tieRows}, {"wu-both", bothRows}} {
			for _, id := range tieUnitIDs(set.prefix) {
				for _, r := range set.rows {
					if err := direct.Exec(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, categorization_status, computed_at, org_id)
SELECT ?, ?, ?, toUUID(?), 1, map(?, 1.0), map(), ?, ?, fromUnixTimestamp64Milli(?, 'UTC'), ?`,
						id, at, at, repoUUID(r.repo), r.theme, `{"issues":[],"prs":[]}`, r.status, at.UnixMilli(), orgID); err != nil {
						t.Fatalf("seed %s %s: %v", id, r.theme, err)
					}
				}
			}
		}
	}
	subjects := map[string]contextfabric.SubjectRef{
		"repository repo-a": {Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID("repo-a"), Label: "repo-a"},
		"repository repo-b": {Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID("repo-b"), Label: "repo-b"},
		"organization":      {Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:" + orgID},
	}
	readAll := func(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient) map[string]mix {
		t.Helper()
		out := map[string]mix{}
		for s, subject := range subjects {
			out[s] = read(t, ctx, devhealthfacts.NewProviders(query), orgID, subject)
		}
		return out
	}
	for name, c := range map[string]struct{ tie, both []tieRow }{
		"categorized written last": {[]tieRow{gated, categorized}, []tieRow{gatedInA, categorized}},
		"gated written last":       {[]tieRow{categorized, gated}, []tieRow{categorized, gatedInA}},
	} {
		order := c.tie
		last, first := order[len(order)-1], order[0]
		lastBoth, firstBoth := c.both[len(c.both)-1], c.both[0]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			seed(t, ctx, direct, c.tie, c.both)
			got := readAll(t, ctx, query)
			var opsPick map[string]string
			if err := func() error {
				rows, err := direct.Query(ctx, `SELECT work_unit_id, arrayElement(mapKeys(argMax(theme_distribution_json, `+opsLatestRowOrderKey+`)), 1) FROM work_unit_investments WHERE org_id = ? GROUP BY work_unit_id`, orgID)
				if err != nil {
					return err
				}
				defer rows.Close()
				opsPick = map[string]string{}
				for rows.Next() {
					var id, theme string
					if err := rows.Scan(&id, &theme); err != nil {
						return err
					}
					opsPick[id] = theme
				}
				return rows.Err()
			}(); err != nil {
				t.Fatalf("ops order key read: %v", err)
			}
			for prefix, later := range map[string]tieRow{"wu-tie": last, "wu-both": lastBoth} {
				for _, id := range tieUnitIDs(prefix) {
					if opsPick[id] != later.theme {
						t.Errorf("ops order key picks %q for %s, want the later insert %q", opsPick[id], id, later.theme)
					}
				}
			}
			var want, wrong map[string]mix
			t.Run("want", func(t *testing.T) {
				wantQuery, wantDirect := newScopedCHAOS7257Client(t, nil)
				createCHAOS7257Tables(t, ctx, wantDirect)
				seed(t, ctx, wantDirect, []tieRow{last}, []tieRow{lastBoth})
				want = readAll(t, ctx, wantQuery)
			})
			t.Run("earlier only", func(t *testing.T) {
				wrongQuery, wrongDirect := newScopedCHAOS7257Client(t, nil)
				createCHAOS7257Tables(t, ctx, wrongDirect)
				seed(t, ctx, wrongDirect, []tieRow{first}, []tieRow{firstBoth})
				wrong = readAll(t, ctx, wrongQuery)
			})
			if s := "repository repo-a"; reflect.DeepEqual(want[s], wrong[s]) {
				t.Fatalf("control, %s: the later and the earlier version read the same (%+v), so the case proves nothing", s, want[s])
			}
			mergeWorkUnitInvestments(t, ctx, direct)
			merged := readAll(t, ctx, query)
			for s := range subjects {
				t.Logf("%s: before merge %+v, after merge %+v, want %+v", s, got[s], merged[s], want[s])
				if !reflect.DeepEqual(want[s], got[s]) {
					t.Errorf("%s: the tie did not resolve to the later insert (%s): got %+v, want %+v", s, last.theme, got[s], want[s])
				}
				if !reflect.DeepEqual(want[s], merged[s]) {
					t.Errorf("%s, after OPTIMIZE FINAL: got %+v, want %+v", s, merged[s], want[s])
				}
			}
		})
	}
}

// acr's ordinal is built from the three components of ops' latestrow.OrderKey,
// each in its own 64 bits. This text pin catches a change of a component on the
// acr side only: a later change of the ops key does not fail here.
func TestWorkUnitVersionKeyIsBuiltFromTheOpsLatestRowOrderKeyComponents(t *testing.T) {
	t.Parallel()
	for _, component := range []struct{ ops, acr string }{
		{"work_unit_investments.computed_at", "toUInt256(toUnixTimestamp64Milli(work_unit_investments.computed_at)), 128)"},
		{"toUInt64OrZero(splitByChar('_', work_unit_investments._part)[3])", "toUInt256(toUInt64OrZero(splitByChar('_', work_unit_investments._part)[3])), 64)"},
		{"work_unit_investments._part_offset", "toUInt256(work_unit_investments._part_offset))"},
	} {
		if !strings.Contains(opsLatestRowOrderKey, component.ops) {
			t.Errorf("ops latestrow.OrderKey %q lacks %q", opsLatestRowOrderKey, component.ops)
		}
		if !strings.Contains(devhealthfacts.WorkUnitVersionKey, component.acr) {
			t.Errorf("workUnitVersionKey %q lacks %q", devhealthfacts.WorkUnitVersionKey, component.acr)
		}
	}
}

// acr's ordinal and ops' latestrow.OrderKey pick the same row of every unit, on
// a table of same-millisecond ties in two parts each (both insert orders) and a
// unit whose newer computed_at was inserted first, before and after OPTIMIZE
// FINAL; after the merge both pick the row the merge kept.
func TestWorkUnitVersionKeyOrdersLikeTheOpsLatestRowOrderKeyAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_unit_investments`); err != nil {
		t.Fatalf("stop merges: %v", err)
	}
	const orgID = "org-7323-order-parity"
	at := ts(2026, 9, 18, 0, 0, 0)
	for _, w := range []struct {
		id, theme string
		at        time.Time
	}{
		{"tie-risk-last", "quality", at}, {"tie-risk-last", "risk", at},
		{"tie-quality-last", "risk", at}, {"tie-quality-last", "quality", at},
		{"newer-first", "risk", at.Add(time.Millisecond)}, {"newer-first", "quality", at},
	} {
		insertRunVersionAtMilli(t, ctx, direct, orgID, w.id, "", `{"issues":[],"prs":[]}`, w.theme, w.theme, w.at, at.Add(-time.Hour))
	}
	want := map[string]string{"tie-risk-last": "risk", "tie-quality-last": "quality", "newer-first": "risk"}
	pick := func(final string) map[string][2]string {
		t.Helper()
		rows, err := direct.Query(ctx, `SELECT work_unit_id,
	arrayElement(mapKeys(argMax(theme_distribution_json, `+devhealthfacts.WorkUnitVersionKey+`)), 1),
	arrayElement(mapKeys(argMax(theme_distribution_json, `+opsLatestRowOrderKey+`)), 1)
FROM work_unit_investments`+final+` WHERE org_id = ? GROUP BY work_unit_id`, orgID)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		defer rows.Close()
		out := map[string][2]string{}
		for rows.Next() {
			var id, acr, ops string
			if err := rows.Scan(&id, &acr, &ops); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out[id] = [2]string{acr, ops}
		}
		return out
	}
	check := func(stage string, got map[string][2]string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d units, want %d", stage, len(got), len(want))
		}
		for id, theme := range want {
			if got[id][0] != theme || got[id][1] != theme {
				t.Errorf("%s, %s: acr ordinal picks %q, ops OrderKey picks %q, want %q", stage, id, got[id][0], got[id][1], theme)
			}
		}
	}
	check("before the merge", pick(""))
	mergeWorkUnitInvestments(t, ctx, direct)
	check("after OPTIMIZE FINAL", pick(""))
	check("FINAL read after OPTIMIZE FINAL", pick(" FINAL"))
}

// insertRunVersionAtMilli writes one version of a unit (label names it in a
// failure) with a computed_at at the column's own precision (see
// insertVersionAtMilli); from_ts and to_ts are both span.
func insertRunVersionAtMilli(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, id, repoID, evidence, theme, label string, computedAt, span time.Time) {
	t.Helper()
	span = span.Truncate(time.Second)
	if err := direct.Exec(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
SELECT ?, ?, ?, toUUIDOrNull(?), 0, map(?, 1.0), map(), ?, fromUnixTimestamp64Milli(?, 'UTC'), ?`,
		id, span, span, repoID, theme, evidence, computedAt.UnixMilli(), orgID); err != nil {
		t.Fatalf("insert version %s of %s: %v", label, id, err)
	}
}
