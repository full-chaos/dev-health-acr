package devhealthsource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Lane 9556x: does the ownership-history compaction change anything acr's
// ownership readers emit? Seed a pre-compaction history, read, apply the
// compaction (phase A insert, phase B delete) in the table, read again.

type cmpRow struct {
	label  string
	repo   bool
	team   string
	name   string // repo_full_name | project_id
	key    string // project_key
	repoID string
	prio   int32
	from   time.Time
	to     *time.Time
	upd    time.Time
	synced time.Time
	zero   bool // true only for rows inserted by the compaction
}

func (r cmpRow) isTomb() bool  { return r.to != nil && r.to.Equal(r.from) }
func (r cmpRow) isOpen() bool  { return r.to == nil }
func (r cmpRow) shape() string { return fmt.Sprint(r.repoID, r.prio, r.key) }
func (r cmpRow) groupKey() string {
	return fmt.Sprint(r.repo, "|", r.team, "|", r.name)
}

func cmpDay(month, day int) time.Time {
	return time.Date(2026, time.Month(month+6), day, 0, 0, 0, 0, time.UTC)
}
func cmpPtr(t time.Time) *time.Time { return &t }

var cmpCompactedAt = time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC)

func cmpRepoID(n int) string { return fmt.Sprintf("7a000000-0000-4000-8000-%012d", n) }

// cmpSeed is the pre-compaction history. Each label names a case of the PLAN.
func cmpSeed() []cmpRow {
	var rows []cmpRow
	add := func(repo bool, label, name string, n int, team string, spans ...[3]any) {
		for _, s := range spans {
			from := s[0].(time.Time)
			var to *time.Time
			if s[1] != nil {
				to = cmpPtr(s[1].(time.Time))
			}
			r := cmpRow{label: label, repo: repo, team: team, name: name, from: from, to: to, upd: s[2].(time.Time), synced: s[2].(time.Time)}
			if repo {
				r.repoID = cmpRepoID(n)
				r.prio = 0
			} else {
				r.key = "KEY-" + name
			}
			rows = append(rows, r)
		}
	}
	nm := func(repo bool, s string) string {
		if repo {
			return "acme/" + s
		}
		return "PROJ-" + s
	}
	for i, repo := range []bool{true, false} {
		n := i * 100
		add(repo, "mono", nm(repo, "mono"), n+1, "team-a",
			[3]any{cmpDay(1, 1), cmpDay(1, 5), cmpDay(1, 5)}, [3]any{cmpDay(1, 5), cmpDay(1, 10), cmpDay(1, 10)},
			[3]any{cmpDay(1, 10), cmpDay(1, 15), cmpDay(1, 15)}, [3]any{cmpDay(1, 15), cmpDay(1, 20), cmpDay(1, 20)})
		add(repo, "nonmono", nm(repo, "nonmono"), n+2, "team-a",
			[3]any{cmpDay(1, 1), cmpDay(1, 5), cmpDay(2, 20)}, [3]any{cmpDay(1, 5), cmpDay(1, 10), cmpDay(1, 3)})
		add(repo, "lasttomb", nm(repo, "lasttomb"), n+3, "team-a",
			[3]any{cmpDay(1, 1), cmpDay(1, 5), cmpDay(1, 5)}, [3]any{cmpDay(1, 5), cmpDay(1, 10), cmpDay(1, 10)},
			[3]any{cmpDay(1, 12), cmpDay(1, 12), cmpDay(1, 12)})
		add(repo, "midtomb-maxfalls", nm(repo, "midtomb"), n+4, "team-a",
			[3]any{cmpDay(1, 1), cmpDay(1, 3), cmpDay(1, 3)}, [3]any{cmpDay(1, 3), cmpDay(1, 5), cmpDay(1, 5)},
			[3]any{cmpDay(1, 4), cmpDay(1, 4), cmpDay(3, 1)}, [3]any{cmpDay(1, 5), cmpDay(1, 8), cmpDay(1, 8)})
		add(repo, "openafter", nm(repo, "openafter"), n+5, "team-a",
			[3]any{cmpDay(1, 1), cmpDay(1, 5), cmpDay(1, 5)}, [3]any{cmpDay(1, 5), cmpDay(1, 10), cmpDay(1, 10)},
			[3]any{cmpDay(1, 10), nil, cmpDay(1, 10)})
		add(repo, "gap", nm(repo, "gap"), n+6, "team-a",
			[3]any{cmpDay(1, 1), cmpDay(1, 5), cmpDay(1, 5)}, [3]any{cmpDay(1, 7), cmpDay(1, 10), cmpDay(1, 10)})
		add(repo, "firsttomb", nm(repo, "firsttomb"), n+8, "team-a",
			[3]any{cmpDay(1, 4), cmpDay(1, 4), cmpDay(1, 4)}, [3]any{cmpDay(1, 5), cmpDay(1, 8), cmpDay(1, 8)},
			[3]any{cmpDay(1, 8), cmpDay(1, 12), cmpDay(1, 12)})
		// a second team on the same repository/project as "mono"
		add(repo, "mono-b", nm(repo, "mono"), n+1, "team-b",
			[3]any{cmpDay(1, 1), cmpDay(1, 5), cmpDay(1, 5)}, [3]any{cmpDay(1, 5), cmpDay(1, 9), cmpDay(1, 9)})
	}
	// shape change (repository table only; the project table has no shape column besides the key)
	rows = append(rows,
		cmpRow{label: "shape", repo: true, team: "team-a", name: "acme/shape", repoID: cmpRepoID(7), prio: 0, from: cmpDay(1, 1), to: cmpPtr(cmpDay(1, 5)), upd: cmpDay(1, 5), synced: cmpDay(1, 5)},
		cmpRow{label: "shape", repo: true, team: "team-a", name: "acme/shape", repoID: cmpRepoID(7), prio: 1, from: cmpDay(1, 5), to: cmpPtr(cmpDay(1, 10)), upd: cmpDay(1, 10), synced: cmpDay(1, 10)})
	return rows
}

// cmpCompact is the PLAN's compaction: phase A rows (merged first rows and
// tombstoned inner rows), and the keys phase B deletes.
func cmpCompact(rows []cmpRow, keepStamp bool) (insert []cmpRow, deletes []cmpRow) {
	groups := map[string][]cmpRow{}
	var order []string
	for _, r := range rows {
		if _, ok := groups[r.groupKey()]; !ok {
			order = append(order, r.groupKey())
		}
		groups[r.groupKey()] = append(groups[r.groupKey()], r)
	}
	for _, gk := range order {
		g := groups[gk]
		sort.Slice(g, func(i, j int) bool { return g[i].from.Before(g[j].from) })
		var live []cmpRow
		var tombs []cmpRow
		earliest := g[0].from
		for _, r := range g {
			if r.isTomb() {
				tombs = append(tombs, r)
			} else {
				live = append(live, r)
			}
		}
		hasOpen := false
		var maxLiveStamp time.Time
		for _, r := range live {
			if r.upd.After(maxLiveStamp) {
				maxLiveStamp = r.upd
			}
		}
		var maxTo time.Time
		for _, r := range g {
			if r.isOpen() {
				hasOpen = true
			} else if r.to.After(maxTo) {
				maxTo = *r.to
			}
		}
		// pre-existing tombstones: keep the earliest row of the key and the latest by fact order
		for _, t := range tombs {
			keepEarliest := !t.from.After(earliest)
			keepLatest := !hasOpen && !maxTo.After(t.from)
			keepNewest := keepStamp && t.upd.After(maxLiveStamp.Add(time.Millisecond))
			if !keepEarliest && !keepLatest && !keepNewest {
				deletes = append(deletes, t)
			}
		}
		var run []cmpRow
		flush := func() {
			if len(run) >= 2 {
				merged := run[0]
				merged.to = cmpPtr(*run[len(run)-1].to)
				newest := run[0].upd
				for _, r := range run {
					if r.upd.After(newest) {
						newest = r.upd
					}
				}
				if !newest.After(run[0].upd) {
					newest = run[0].upd.Add(time.Millisecond)
				}
				merged.upd = newest
				merged.synced = cmpCompactedAt
				merged.zero = true
				insert = append(insert, merged)
				for _, inner := range run[1:] {
					tomb := inner
					tomb.to = cmpPtr(inner.from)
					tomb.upd = inner.upd.Add(time.Millisecond)
					tomb.synced = cmpCompactedAt
					tomb.zero = true
					insert = append(insert, tomb)
					deletes = append(deletes, tomb)
				}
			}
			run = nil
		}
		for _, r := range live {
			if r.isOpen() {
				flush()
				continue
			}
			if len(run) > 0 {
				last := run[len(run)-1]
				if last.to.Equal(r.from) && last.shape() == r.shape() {
					run = append(run, r)
					continue
				}
				flush()
			}
			run = append(run, r)
		}
		flush()
	}
	return insert, deletes
}

// cmpInsert writes through a native batch, as the producer does: a client-side
// bound `?` parameter drops the milliseconds of a DateTime64 value.
func cmpInsert(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, org string, legacy bool, r cmpRow) {
	t.Helper()
	var to *time.Time
	if r.to != nil {
		v := *r.to
		to = &v
	}
	var batch interface {
		Append(v ...any) error
		Send() error
	}
	var err error
	switch {
	case r.repo:
		batch, err = direct.PrepareBatch(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)`)
		if err == nil {
			err = batch.Append(org, "github", r.team, &r.repoID, r.name, "exact", "inferred", uint8(1), uint16(100), r.prio, r.from, to, r.upd)
		}
	case legacy:
		batch, err = direct.PrepareBatch(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)`)
		if err == nil {
			err = batch.Append(org, "linear", "p"+r.team, r.name, &r.key, "native", r.from, to, r.upd)
		}
	default:
		batch, err = direct.PrepareBatch(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)`)
		if err == nil {
			err = batch.Append(org, "linear", "p"+r.team, r.name, &r.key, "native", r.from, to, r.upd, r.synced)
		}
	}
	if err == nil {
		err = batch.Send()
	}
	if err != nil {
		t.Fatalf("insert %s %s: %v", r.label, r.name, err)
	}
}

func cmpDelete(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, org string, r cmpRow) {
	t.Helper()
	table, col, src, prov, team := "team_project_ownership", "project_id", "native", "linear", "p"+r.team
	if r.repo {
		table, col, src, prov, team = "team_repo_ownership", "repo_full_name", "inferred", "github", r.team
	}
	ctx = clickhousedriver.Context(ctx, clickhousedriver.WithSettings(clickhousedriver.Settings{"mutations_sync": "2"}))
	mustExec(t, ctx, direct, fmt.Sprintf(`ALTER TABLE %s DELETE WHERE org_id = ? AND provider = '%s' AND %s = ? AND team_id = ? AND source = ? AND valid_from = ?`, table, prov, col),
		org, r.name, team, src, r.from)
}

type cmpSnap struct {
	values   map[string]string    // class:id -> JSON without observed_at
	observed map[string]time.Time // class:id -> observed_at
	tombs    map[string]bool
	raw      map[string]string // grouped SQL rows without the stamps
	rawStamp map[string]string // the stamps of the grouped SQL rows
	cursor   string
}

func cmpDrain(t *testing.T, ctx context.Context, f *ownershipFixture, cursor string) cmpSnap {
	t.Helper()
	s := cmpSnap{values: map[string]string{}, observed: map[string]time.Time{}, tombs: map[string]bool{}, raw: map[string]string{}, rawStamp: map[string]string{}}
	for page := 0; ; page++ {
		if page > 300 {
			t.Fatalf("no convergence")
		}
		batch, ok, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if !ok {
			break
		}
		cursor = batch.NextCursor
		for _, e := range batch.Entities {
			id := "entity:" + string(e.Subject.Kind) + ":" + e.Subject.CanonicalID
			at := e.ObservedAt
			e.ObservedAt = time.Time{}
			b, _ := json.Marshal(e)
			s.values[id] = string(b)
			s.observed[id] = at
		}
		for _, r := range batch.Relationships {
			id := "rel:" + string(r.Type) + ":" + r.RelationshipID
			at := r.ObservedAt
			r.ObservedAt = time.Time{}
			b, _ := json.Marshal(r)
			s.values[id] = string(b)
			s.observed[id] = at
		}
		for _, tb := range batch.Tombstones {
			s.tombs[tb.Kind+":"+tb.CanonicalID] = true
		}
	}
	s.cursor = cursor
	return s
}

func cmpRaw(t *testing.T, ctx context.Context, f *ownershipFixture, s *cmpSnap, label, sql, keyExpr, exceptStamps, stampExpr string) {
	t.Helper()
	q := fmt.Sprintf("SELECT %s, toString(tuple(* EXCEPT (%s))), toString(%s) FROM (%s) ORDER BY 1", keyExpr, exceptStamps, stampExpr, sql)
	rows, err := f.direct.Query(ctx, q, clickhousedriver.Named("org_id", f.orgID))
	if err != nil {
		t.Fatalf("raw %s: %v", label, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v, st string
		if err := rows.Scan(&k, &v, &st); err != nil {
			t.Fatalf("scan: %v", err)
		}
		s.raw[label+":"+k] = v
		s.rawStamp[label+":"+k] = st
	}
}

func cmpDiff(a, b cmpSnap) (valueDiffs, stampDiffs []string) {
	for k, v := range a.values {
		w, ok := b.values[k]
		if !ok {
			valueDiffs = append(valueDiffs, "missing after: "+k)
		} else if v != w {
			valueDiffs = append(valueDiffs, "value differs: "+k+"\n   before "+v+"\n   after  "+w)
		}
	}
	for k := range b.values {
		if _, ok := a.values[k]; !ok {
			valueDiffs = append(valueDiffs, "new after: "+k)
		}
	}
	for k := range a.tombs {
		if !b.tombs[k] {
			valueDiffs = append(valueDiffs, "tombstone missing after: "+k)
		}
	}
	for k := range b.tombs {
		if !a.tombs[k] {
			valueDiffs = append(valueDiffs, "tombstone new after: "+k)
		}
	}
	for k, v := range a.raw {
		if w, ok := b.raw[k]; !ok || v != w {
			valueDiffs = append(valueDiffs, "raw group differs: "+k+"\n   before "+v+"\n   after  "+b.raw[k])
		}
	}
	for k := range b.raw {
		if _, ok := a.raw[k]; !ok {
			valueDiffs = append(valueDiffs, "raw group new: "+k)
		}
	}
	for k, v := range a.observed {
		if w, ok := b.observed[k]; ok && !v.Equal(w) {
			stampDiffs = append(stampDiffs, fmt.Sprintf("observed_at %s: %s -> %s", k, v.Format(time.RFC3339Nano), w.Format(time.RFC3339Nano)))
		}
	}
	for k, v := range a.rawStamp {
		if w := b.rawStamp[k]; v != w {
			stampDiffs = append(stampDiffs, fmt.Sprintf("raw stamp %s: %s -> %s", k, v, w))
		}
	}
	sort.Strings(valueDiffs)
	sort.Strings(stampDiffs)
	return
}

// cmpLabels names the edges by case for the report.
func cmpLabels(t *testing.T, orgRows []cmpRow) map[string]string {
	out := map[string]string{}
	for _, r := range orgRows {
		if r.repo {
			out[devhealthsource.RepositoryTeamRelationshipIDForTest(r.repoID, r.team, "github", "inferred")] = "repo/" + r.label + "/" + r.team
		} else {
			out[devhealthsource.ProjectTeamRelationshipIDForTest(t, "linear", r.name, "p"+r.team, "native")] = "project/" + r.label + "/" + r.team
		}
	}
	return out
}

func TestOwnershipCompactionEquivalenceIngestCursor(t *testing.T) { cmpEquivalence(t, false) }
func TestOwnershipCompactionEquivalenceLegacyCursor(t *testing.T) { cmpEquivalence(t, true) }

func cmpEquivalence(t *testing.T, legacy bool) {
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
	var cols uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.columns WHERE database = currentDatabase() AND ((table = 'team_project_ownership' AND name = 'last_synced') OR (table = 'project_membership_transitions' AND name = 'ingested_at') OR (table = 'project_membership_presence' AND name = 'last_synced') OR (table = 'projects' AND name = 'last_synced'))`).Scan(&cols); err != nil {
		t.Fatalf("probe: %v", err)
	}
	ingest := cols == 4
	t.Logf("ingest columns found %d of 4 -> the project edge pages on the %s cursor", cols, map[bool]string{true: "ingest (last_synced)", false: "legacy (updated_at)"}[ingest])
	if ingest == legacy {
		t.Fatalf("scenario wanted legacy=%v but the probe says ingest=%v", legacy, ingest)
	}
	for i, plant := range []string{"", "max-falls", "drop-latest-tombstone", "valid-to-early"} {
		name := "correct"
		if plant == "max-falls" {
			name = "old-rule-max-falls"
		} else if plant != "" {
			name = "plant-" + plant
		}
		org := fmt.Sprintf("95560000-0000-4000-8000-%012d", i+1)
		if legacy {
			org = fmt.Sprintf("95560000-0000-4000-8000-%012d", i+11)
		}
		t.Run(name, func(t *testing.T) {
			f := newOwnershipFixture(t, ctx, query, direct, org)
			runCompactionScenario(t, ctx, f, legacy, plant)
		})
	}
}

func runCompactionScenario(t *testing.T, ctx context.Context, f *ownershipFixture, legacy bool, plant string) {
	t.Helper()
	org := f.orgID
	rows := cmpSeed()
	mustExec(t, ctx, f.direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES ('team-a','a','',?,?,'github','team-a',[],1),('team-b','b','',?,?,'github','team-b',[],1),('pteam-a','pa','',?,?,'linear','pteam-a',[],1),('pteam-b','pb','',?,?,'linear','pteam-b',[],1)`,
		cmpDay(1, 1), org, cmpDay(1, 1), org, cmpDay(1, 1), org, cmpDay(1, 1), org)
	seenRepo, seenProj := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		if r.repo && !seenRepo[r.name] {
			seenRepo[r.name] = true
			mustExec(t, ctx, f.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
				r.repoID, r.name, nil, cmpDay(1, 1), nil, cmpDay(1, 1), org, "github")
		}
		if !r.repo && !seenProj[r.name] {
			seenProj[r.name] = true
			mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,1,'started','',?)`,
				r.name, org, "linear", r.key, r.name, cmpDay(1, 1))
		}
		cmpInsert(t, ctx, f.direct, org, legacy, r)
	}
	f.source.WithOverlap(time.Millisecond) //nolint:errcheck
	snapshot := func(cursor string) cmpSnap {
		s := cmpDrain(t, ctx, f, cursor)
		cmpRaw(t, ctx, f, &s, "repo", devhealthsource.RepositoryTeamsGroupedSQLForTest(),
			"concat(repo_key,'|',owning_team_id,'|',source_name,'|',provider)", "observed_at", "observed_at")
		except, stamp := "observed_at, ingest_at", "ingest_at"
		if legacy {
			except, stamp = "observed_at", "observed_at"
		}
		cmpRaw(t, ctx, f, &s, "project", devhealthsource.ProjectTeamsGroupSQLCompactionForTest(!legacy),
			"concat(project_id,'|',team_id,'|',source_name,'|',provider)", except, stamp)
		return s
	}
	pre := snapshot("")
	if len(pre.values) == 0 || len(pre.raw) == 0 {
		t.Fatalf("pre snapshot empty: values=%d raw=%d (measurement did not happen)", len(pre.values), len(pre.raw))
	}
	count := func() (n uint64) {
		var a, b uint64
		_ = f.direct.QueryRow(ctx, `SELECT count() FROM team_repo_ownership WHERE org_id = ?`, org).Scan(&a)
		_ = f.direct.QueryRow(ctx, `SELECT count() FROM team_project_ownership WHERE org_id = ?`, org).Scan(&b)
		return a*1000 + b
	}
	digest := func(stage string) string {
		var c1, h1, c2, h2 uint64
		if err := f.direct.QueryRow(ctx, `SELECT (SELECT count() FROM team_project_ownership WHERE org_id = {org_id:String}), (SELECT sum(cityHash64(provider, team_id, project_id, source, valid_from, valid_to, updated_at)) FROM team_project_ownership WHERE org_id = {org_id:String}), (SELECT count() FROM team_repo_ownership WHERE org_id = {org_id:String}), (SELECT sum(cityHash64(provider, team_id, repo_id, match_type, source, is_primary, valid_from, valid_to, updated_at)) FROM team_repo_ownership WHERE org_id = {org_id:String})`, clickhousedriver.Named("org_id", org)).Scan(&c1, &h1, &c2, &h2); err != nil {
			t.Fatalf("digest: %v", err)
		}
		d := fmt.Sprintf("project(count=%d hash=%d) repo(count=%d hash=%d)", c1, h1, c2, h2)
		t.Logf("DIGEST %s: %s", stage, d)
		return d
	}
	dPre := digest("pre")
	physPre := count()
	insert, deletes := cmpCompact(rows, plant != "max-falls")
	if len(insert) == 0 || len(deletes) == 0 {
		t.Fatalf("compaction planned nothing")
	}
	for _, r := range insert {
		t.Logf("PHASE-A insert %s %s %s %s->%v upd %s", map[bool]string{true: "repo", false: "project"}[r.repo], r.label, r.team, r.from.Format("01-02"), r.to, r.upd.Format("01-02T15:04:05.000"))
		cmpInsert(t, ctx, f.direct, org, legacy, r)
	}
	for _, r := range deletes {
		t.Logf("PHASE-B delete %s %s %s from %s", map[bool]string{true: "repo", false: "project"}[r.repo], r.label, r.team, r.from.Format("01-02"))
	}
	dA := digest("after phase A")
	afterA := snapshot("")
	for _, r := range deletes {
		cmpDelete(t, ctx, f.direct, org, r)
	}
	switch plant {
	case "drop-latest-tombstone":
		for _, r := range rows {
			if r.label == "lasttomb" && r.isTomb() {
				cmpDelete(t, ctx, f.direct, org, r)
			}
		}
	case "valid-to-early":
		for _, r := range insert {
			if r.label == "mono" && !r.isTomb() {
				w := r
				w.to = cmpPtr(r.to.Add(-24 * time.Hour))
				w.upd = r.upd.Add(time.Millisecond)
				cmpInsert(t, ctx, f.direct, org, legacy, w)
			}
		}
	}
	physPost := count()
	afterB := snapshot("")
	dB := digest("after phase B")
	dB2 := digest("after phase B, read again")
	if plant == "" {
		_ = f.direct.Exec(ctx, `OPTIMIZE TABLE team_repo_ownership FINAL`)
		_ = f.direct.Exec(ctx, `OPTIMIZE TABLE team_project_ownership FINAL`)
		dM := digest("after a background-style merge (OPTIMIZE FINAL)")
		t.Logf("DIGEST moved A:%v B:%v stable-reread:%v merge-moved:%v", dA != dPre, dB != dA, dB == dB2, dM != dB)
	}
	t.Logf("physical rows (repo*1000+project): before %d after %d", physPre, physPost)
	if physPost >= physPre {
		t.Fatalf("compaction removed no rows: %d -> %d", physPre, physPost)
	}
	labels := cmpLabels(t, rows)
	label := func(k string) string {
		for id, l := range labels {
			if strings.Contains(k, id) {
				return strings.Replace(k, id, l, 1)
			}
		}
		return k
	}
	report := func(stage string, post cmpSnap) (nValue int) {
		vd, sd := cmpDiff(pre, post)
		t.Logf("%s: value differences=%d, stamp differences=%d", stage, len(vd), len(sd))
		for _, d := range vd {
			t.Logf("  VALUE %s", label(d))
		}
		for _, d := range sd {
			t.Logf("  STAMP %s", label(d))
		}
		return len(vd)
	}
	nA := report("after phase A", afterA)
	nB := report("after phase B", afterB)
	if plant == "" || plant == "max-falls" {
		if nA != 0 || nB != 0 {
			t.Errorf("compaction changed a served value (A=%d, B=%d)", nA, nB)
		}
	} else if nB == 0 {
		t.Errorf("planted defect %q was NOT detected: comparison cannot fail", plant)
	}
	// watermark readers: re-read from the cursor taken before the compaction.
	replay := cmpDrain(t, ctx, f, pre.cursor)
	emitted := []string{}
	for k := range replay.values {
		emitted = append(emitted, label(k))
	}
	sort.Strings(emitted)
	t.Logf("replay from the pre-compaction cursor re-emitted %d ids:", len(emitted))
	for _, e := range emitted {
		t.Logf("  REEMIT %s", e)
	}
	_ = contractsv1.ContextFabricRelationshipOwnedByTeam
}
