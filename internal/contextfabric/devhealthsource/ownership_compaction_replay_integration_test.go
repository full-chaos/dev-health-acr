package devhealthsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

const replayDir = "testdata/ownership_replay_c7bee4ef/"

type tsvRow map[string]string

func replayRead(t *testing.T, name string) []tsvRow {
	t.Helper()
	b, err := os.ReadFile(replayDir + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	head := strings.Split(lines[0], "\t")
	var out []tsvRow
	for _, l := range lines[1:] {
		f := strings.Split(l, "\t")
		r := tsvRow{}
		for i, h := range head {
			r[h] = f[i]
		}
		out = append(out, r)
	}
	return out
}

func replayTime(s string) *time.Time {
	if s == `\N` {
		return nil
	}
	v, err := time.ParseInLocation("2006-01-02 15:04:05.000", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return &v
}

func replayStr(s string) *string {
	if s == `\N` {
		return nil
	}
	return &s
}

func replayInsert(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, repo, ingest bool, rows []tsvRow) {
	t.Helper()
	if len(rows) == 0 {
		return
	}
	var sql string
	switch {
	case repo:
		sql = `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)`
	case ingest:
		sql = `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)`
	default:
		sql = `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)`
	}
	b, err := direct.PrepareBatch(ctx, sql)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, r := range rows {
		var u8 uint8
		var u16 uint16
		var i32 int32
		fmt.Sscan(r["is_primary"], &u8)
		fmt.Sscan(r["specificity"], &u16)
		fmt.Sscan(r["priority"], &i32)
		switch {
		case repo:
			err = b.Append(r["org_id"], r["provider"], r["team_id"], replayStr(r["repo_id"]), r["repo_full_name"], r["match_type"], r["source"], u8, u16, i32, *replayTime(r["valid_from"]), replayTime(r["valid_to"]), *replayTime(r["updated_at"]))
		case ingest:
			err = b.Append(r["org_id"], r["provider"], r["team_id"], r["project_id"], replayStr(r["project_key"]), r["source"], *replayTime(r["valid_from"]), replayTime(r["valid_to"]), *replayTime(r["updated_at"]), *replayTime(r["last_synced"]))
		default:
			err = b.Append(r["org_id"], r["provider"], r["team_id"], r["project_id"], replayStr(r["project_key"]), r["source"], *replayTime(r["valid_from"]), replayTime(r["valid_to"]), *replayTime(r["updated_at"]))
		}
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := b.Send(); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// replayDropLatestTombstone is the red control: remove, per key, the row that
// is the key's latest by valid_to when it is a tombstone.
func replayDropLatestTombstone(rows []tsvRow, nameCol string) (kept []tsvRow, dropped int) {
	best := map[string]tsvRow{}
	for _, r := range rows {
		k := r["org_id"] + "|" + r["provider"] + "|" + r[nameCol] + "|" + r["team_id"] + "|" + r["source"]
		if r["valid_to"] == `\N` {
			best[k] = tsvRow{"open": "1"}
			continue
		}
		if cur, ok := best[k]; ok && (cur["open"] == "1" || cur["valid_to"] >= r["valid_to"]) {
			continue
		}
		best[k] = r
	}
	for _, r := range rows {
		k := r["org_id"] + "|" + r["provider"] + "|" + r[nameCol] + "|" + r["team_id"] + "|" + r["source"]
		b := best[k]
		if b["open"] != "1" && r["valid_to"] == r["valid_from"] && r["valid_to"] == b["valid_to"] && r["valid_from"] == b["valid_from"] {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	return
}

func TestOwnershipCompactionRealVerbReplayIngestCursor(t *testing.T) { replayRun(t, false) }
func TestOwnershipCompactionRealVerbReplayLegacyCursor(t *testing.T) { replayRun(t, true) }

func replayRun(t *testing.T, legacy bool) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	if legacy {
		mustExec(t, ctx, direct, `ALTER TABLE team_project_ownership DROP COLUMN last_synced`)
	}
	beforeRepo, beforeProj := replayRead(t, "fixture-before-team_repo_ownership.tsv"), replayRead(t, "fixture-before-team_project_ownership.tsv")
	afterRepo, afterProj := replayRead(t, "fixture-after-team_repo_ownership.tsv"), replayRead(t, "fixture-after-team_project_ownership.tsv")
	orgs := map[string]bool{}
	teamSeen, repoSeen, projSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	labels := map[string]string{}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, r := range append(append([]tsvRow{}, beforeRepo...), afterRepo...) {
		orgs[r["org_id"]] = true
		if k := r["org_id"] + r["team_id"]; !teamSeen[k] {
			teamSeen[k] = true
			mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES (?,?,?,?,?,?,?,?,?)`, r["team_id"], r["team_id"], "", stamp, r["org_id"], r["provider"], r["team_id"], []string{}, uint8(1))
		}
		if k := r["org_id"] + r["repo_id"]; r["repo_id"] != `\N` && !repoSeen[k] {
			repoSeen[k] = true
			mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, r["repo_id"], r["repo_full_name"], nil, stamp, nil, stamp, r["org_id"], r["provider"])
		}
		if r["repo_id"] != `\N` {
			labels[devhealthsource.RepositoryTeamRelationshipIDForTest(r["repo_id"], r["team_id"], r["provider"], r["source"])] = "repo " + r["repo_full_name"] + " " + r["team_id"] + " " + r["source"]
		}
	}
	for _, r := range append(append([]tsvRow{}, beforeProj...), afterProj...) {
		orgs[r["org_id"]] = true
		if k := r["org_id"] + r["team_id"]; !teamSeen[k] {
			teamSeen[k] = true
			mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES (?,?,?,?,?,?,?,?,?)`, r["team_id"], r["team_id"], "", stamp, r["org_id"], r["provider"], r["team_id"], []string{}, uint8(1))
		}
		if k := r["org_id"] + r["project_id"]; !projSeen[k] {
			projSeen[k] = true
			mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,1,'started','',?)`, r["project_id"], r["org_id"], r["provider"], strings.TrimPrefix(r["project_key"], `\N`), r["project_id"], stamp)
		}
		labels[devhealthsource.ProjectTeamRelationshipIDForTest(t, r["provider"], r["project_id"], r["team_id"], r["source"])] = "project " + r["project_id"] + " " + r["team_id"] + " " + r["source"]
	}
	var orgList []string
	for o := range orgs {
		orgList = append(orgList, o)
	}
	sort.Strings(orgList)
	label := func(s string) string {
		for id, l := range labels {
			if strings.Contains(s, id) {
				return strings.Replace(s, id, "["+l+"]", 1)
			}
		}
		return s
	}
	wipe := func() {
		wctx := clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"mutations_sync": "2"}))
		for _, tb := range []string{"team_repo_ownership", "team_project_ownership"} {
			mustExec(t, wctx, direct, `ALTER TABLE `+tb+` DELETE WHERE org_id IN (?)`, orgList)
		}
	}
	snap := func(org string) cmpSnap {
		f := &ownershipFixture{orgID: org, direct: direct, query: query}
		src, err := devhealthsource.NewTeamsProjectsSource(query, true)
		if err != nil {
			t.Fatal(err)
		}
		src.WithOverlap(time.Millisecond) //nolint:errcheck
		f.source = src
		s := cmpDrain(t, ctx, f, "")
		cmpRaw(t, ctx, f, &s, "repo", devhealthsource.RepositoryTeamsGroupedSQLForTest(), "concat(repo_key,'|',owning_team_id,'|',source_name,'|',provider)", "observed_at", "observed_at")
		except, st := "observed_at, ingest_at", "ingest_at"
		if legacy {
			except, st = "observed_at", "observed_at"
		}
		cmpRaw(t, ctx, f, &s, "project", devhealthsource.ProjectTeamsGroupSQLCompactionForTest(!legacy), "concat(project_id,'|',team_id,'|',source_name,'|',provider)", except, st)
		return s
	}
	oddRowKey := ""
	for _, r := range beforeRepo {
		if r["repo_full_name"] == "acme/odd" {
			oddRowKey = identity.JoinSegments(r["provider"], r["repo_id"], r["team_id"], r["source"])
			break
		}
	}
	if oddRowKey == "" {
		t.Fatalf("no acme/odd row in the before file")
	}
	// the cursor of a real drain (right position space for this mode), moved
	// to acme/odd's own stamp and row key
	mkCursor := func(real string) string {
		raw, err := base64.RawURLEncoding.DecodeString(real)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("cursor json: %v", err)
		}
		m["since"] = "2026-02-20T00:00:00Z"
		m["after"] = oddRowKey
		delete(m, "ack")
		out, _ := json.Marshal(m)
		return base64.RawURLEncoding.EncodeToString(out)
	}
	oddCursor := ""
	replayAt := func(o, cursor string) map[string]string {
		f := &ownershipFixture{orgID: o, direct: direct, query: query}
		src, _ := devhealthsource.NewTeamsProjectsSource(query, true)
		src.WithOverlap(time.Millisecond) //nolint:errcheck
		f.source = src
		return cmpDrain(t, ctx, f, cursor).values
	}
	for _, scenario := range []string{"real-verb-after", "red-control-drop-latest-tombstone"} {
		t.Run(scenario, func(t *testing.T) {
			wipe()
			replayInsert(t, ctx, direct, true, !legacy, beforeRepo)
			replayInsert(t, ctx, direct, false, !legacy, beforeProj)
			pre := map[string]cmpSnap{}
			for _, o := range orgList {
				pre[o] = snap(o)
				if len(pre[o].values) == 0 || len(pre[o].raw) == 0 {
					t.Fatalf("empty pre snapshot (measurement did not happen)")
				}
			}
			oddPre := map[string]map[string]string{}
			oddCursor = mkCursor(pre[orgList[0]].cursor)
			t.Logf("cursor used for the acme/odd test (decoded): %s", func() string { r, _ := base64.RawURLEncoding.DecodeString(oddCursor); return string(r) }())
			for _, o := range orgList {
				oddPre[o] = replayAt(o, oddCursor)
			}
			wipe()
			ar, ap := afterRepo, afterProj
			if scenario != "real-verb-after" {
				var d1, d2 int
				ar, d1 = replayDropLatestTombstone(afterRepo, "repo_full_name")
				ap, d2 = replayDropLatestTombstone(afterProj, "project_id")
				t.Logf("red control dropped %d repo rows and %d project rows", d1, d2)
				if d1+d2 == 0 {
					t.Fatalf("red control dropped nothing")
				}
			}
			replayInsert(t, ctx, direct, true, !legacy, ar)
			replayInsert(t, ctx, direct, false, !legacy, ap)
			total := 0
			for i, o := range orgList {
				post := snap(o)
				vd, sd := cmpDiff(pre[o], post)
				var edges int
				for k := range pre[o].values {
					if strings.HasPrefix(k, "rel:OWNED_BY_TEAM") {
						edges++
					}
				}
				t.Logf("org#%d: edges=%d groups=%d value differences=%d stamp differences=%d", i+1, edges, len(pre[o].raw), len(vd), len(sd))
				for _, d := range vd {
					t.Logf("  VALUE %s", label(d))
				}
				for _, d := range sd {
					t.Logf("  STAMP %s", label(d))
				}
				total += len(vd)
				// replay from the pre-compaction cursor
				f := &ownershipFixture{orgID: o, direct: direct, query: query}
				src, _ := devhealthsource.NewTeamsProjectsSource(query, true)
				src.WithOverlap(time.Millisecond) //nolint:errcheck
				f.source = src
				re := cmpDrain(t, ctx, f, pre[o].cursor)
				var ids []string
				for id := range re.values {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				t.Logf("org#%d replay from the pre cursor re-emitted %d ids", i+1, len(ids))
				for _, id := range ids {
					same := "values equal to before"
					if re.values[id] != pre[o].values[id] {
						same = "VALUES DIFFER from before"
					}
					t.Logf("  REEMIT %s: %s; observed_at %v -> %v", label(id), same, pre[o].observed[id].Format(time.RFC3339Nano), re.observed[id].Format(time.RFC3339Nano))
				}
			}
			for i, o := range orgList {
				at := replayAt(o, oddCursor)
				var ids []string
				for id := range at {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				t.Logf("org#%d CURSOR AT acme/odd (2026-02-20 00:00:00.000, its own row key): before-state re-emitted %d ids, after-state %d ids", i+1, len(oddPre[o]), len(ids))
				for _, id := range ids {
					state := "NEW after (not emitted before): re-emitted value EQUAL to the full read before compaction"
					if pre[o].values[id] != at[id] {
						state = "NEW after (not emitted before): VALUE DIFFERS from the full read before compaction"
					}
					if pv, ok := oddPre[o][id]; ok {
						state = "emitted before and after"
						if pv != at[id] {
							state = "emitted before and after, VALUES DIFFER"
						}
					}
					t.Logf("  AT-CURSOR %s: %s", label(id), state)
				}
				for id := range oddPre[o] {
					if _, ok := at[id]; !ok {
						t.Logf("  AT-CURSOR %s: emitted before, NOT after", label(id))
					}
				}
			}
			if scenario == "real-verb-after" && total != 0 {
				t.Errorf("real verb output changed a served value: %d differences", total)
			}
			if scenario != "real-verb-after" && total == 0 {
				t.Errorf("red control not detected")
			}
		})
	}
}
