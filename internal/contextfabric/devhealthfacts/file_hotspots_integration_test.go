package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type hotspotSeedExec interface {
	Exec(context.Context, string, ...any) error
}

func seedHotspotRow(t *testing.T, ctx context.Context, direct hotspotSeedExec, orgID, repoID string, day time.Time, path string, churn uint64, commits, cyclomatic uint32, risk float64, computedAt time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO file_hotspot_daily (org_id, repo_id, day, file_path, churn_loc_30d, churn_commits_30d, cyclomatic_total, cyclomatic_avg, blame_concentration, risk_score, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, repoID, day, path, churn, commits, cyclomatic, float64(cyclomatic)/2, 0.25, risk, computedAt); err != nil {
		t.Fatalf("seed file_hotspot_daily(%s): %v", path, err)
	}
}

type slugGrantGraph struct {
	admitAllGraph
	slugByRepo map[string]string
}

func (g slugGrantGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index, subject := range subjects {
		out[index] = contextfabric.StoredSubjectDenied
		slug := g.slugByRepo[strings.TrimPrefix(subject.CanonicalID, "repository:")]
		for _, scope := range principal.RepositoryScopes {
			if scope == "*" || scope == slug {
				out[index] = contextfabric.StoredSubjectAdmitted
			}
		}
	}
	return out, nil
}

// The served read_facts output carries the files of the latest stored run in
// the window, at most ten per repository ordered by the stored score, the run
// size, an own basis for a repository with no row, null cyclomatic fields
// with a churn-only note where no snapshot is stored, and nothing of another
// organization or of a repository outside the caller's grant.
func TestFileHotspotsServedByReadFactsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	const orgID = "org-file-hotspots"
	const otherOrg = "org-file-hotspots-other"
	at := ts(2026, 10, 1, 6, 0, 0)
	day1 := ts(2026, 10, 1, 0, 0, 0)
	day0 := ts(2026, 9, 30, 0, 0, 0)
	dayLate := ts(2026, 10, 10, 0, 0, 0)
	busy, empty, noSnapshot, secret := repoUUID("hot-busy"), repoUUID("hot-empty"), repoUUID("hot-nosnap"), repoUUID("hot-secret")
	slugs := map[string]string{busy: "acme/hot-busy", empty: "acme/hot-empty", noSnapshot: "acme/hot-nosnap", secret: "acme/hot-secret"}
	for id, slug := range slugs {
		seedSecurityRepo(t, ctx, direct, orgID, id, slug, "github", at)
	}

	// busy: 12 files in the latest run of 2026-10-01 (two versions of one
	// day: only the later computed_at is the latest run), one file only in
	// the older run, and a later run outside the range window.
	for i := 0; i < 12; i++ {
		seedHotspotRow(t, ctx, direct, orgID, busy, day1, fmt.Sprintf("src/file_%02d.go", i), uint64(100+i), 3, uint32(5+i), float64(i), at.Add(time.Hour))
	}
	seedHotspotRow(t, ctx, direct, orgID, busy, day1, "src/superseded.go", 9999, 99, 99, 999, at)
	seedHotspotRow(t, ctx, direct, orgID, busy, day0, "src/old_only.go", 8888, 88, 88, 888, at.Add(-24*time.Hour))
	seedHotspotRow(t, ctx, direct, orgID, busy, dayLate, "src/late.go", 7, 1, 1, 0.5, ts(2026, 10, 10, 6, 0, 0))
	seedHotspotRow(t, ctx, direct, orgID, noSnapshot, day1, "lib/x.go", 10, 1, 0, 2.5, at)
	seedHotspotRow(t, ctx, direct, orgID, secret, day1, "secret/plan.go", 10, 1, 3, 4.5, at)
	seedHotspotRow(t, ctx, direct, otherOrg, busy, day1, "other/org.go", 5000, 50, 50, 500, at)
	if err := direct.Exec(ctx, `INSERT INTO file_complexity_snapshots (org_id, repo_id, as_of_day, file_path, computed_at) VALUES (?,?,?,?,?)`, orgID, busy, day1, "src/file_00.go", at); err != nil {
		t.Fatalf("seed file_complexity_snapshots: %v", err)
	}

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(slugGrantGraph{slugByRepo: slugs}, nil), directread.NewFactReader(seam), nil)
	read := func(scopes []string, window *directread.RequestWindow, repos ...string) (directread.FactsResponse, map[string]map[string]any, string) {
		t.Helper()
		subjects := []directread.RequestSubject{}
		for _, id := range repos {
			subjects = append(subjects, directread.RequestSubject{Kind: "repository", CanonicalID: "repository:" + id})
		}
		response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009598"),
			storage.Principal{OrgID: orgID, Subject: "t9598", CredentialID: "t9598", RepositoryScopes: scopes},
			directread.FactsRequest{Kinds: []string{"file_hotspots"}, Subjects: subjects, Window: window})
		if err != nil {
			t.Fatalf("read_facts: %v", err)
		}
		raw, _ := json.Marshal(response)
		byRepo := map[string]map[string]any{}
		for _, fact := range response.Facts {
			fields := map[string]any{}
			for name, value := range fact.Fields {
				fields[name] = value
			}
			if table, ok := fact.Tables["hotspot_files"]; ok {
				fields["hotspot_files"] = table
			}
			byRepo[fact.Subject.CanonicalID] = fields
		}
		return response, byRepo, string(raw)
	}
	field := func(fields map[string]any, name string) string {
		value, ok := fields[name]
		if !ok {
			return "<absent>"
		}
		out, _ := json.Marshal(value)
		return string(out)
	}

	rangeWindow := &directread.RequestWindow{Mode: directread.WindowRange, Start: ptrTime(ts(2026, 9, 25, 0, 0, 0)), End: ptrTime(ts(2026, 10, 2, 0, 0, 0))}
	_, byRepo, raw := read([]string{"*"}, rangeWindow, busy, empty, noSnapshot)
	if len(byRepo) != 3 {
		t.Fatalf("facts for %d repositories, want 3: %s", len(byRepo), raw)
	}
	b := byRepo["repository:"+busy]
	if got := field(b, "hotspot_basis"); got != `"latest_run_in_window"` {
		t.Errorf("busy hotspot_basis = %s: %s", got, raw)
	}
	if got := field(b, "hotspot_file_count"); got != `"12"` {
		t.Errorf("busy hotspot_file_count = %s, want the 12 files of the latest run (the earlier run of the same day and the older day are not in it): %s", got, raw)
	}
	if got := field(b, "latest_run_day"); got != `"2026-10-01"` {
		t.Errorf("busy latest_run_day = %s: %s", got, raw)
	}
	files := field(b, "hotspot_files")
	if strings.Contains(files, "superseded.go") || strings.Contains(files, "old_only.go") || strings.Contains(files, "late.go") || strings.Contains(files, "other/org.go") {
		t.Errorf("busy hotspot_files carries a file outside the latest run in the window or of another organization: %s", files)
	}
	if strings.Count(files, `"file_path"`) > 10 {
		t.Errorf("busy hotspot_files lists more than 10 files: %s", files)
	}
	if first := strings.Index(files, "src/file_11.go"); first < 0 || first > strings.Index(files, "src/file_10.go") {
		t.Errorf("busy hotspot_files is not ordered by the stored score, highest first: %s", files)
	}
	if got := field(b, "complexity_basis"); got != `"complexity_snapshot_stored"` {
		t.Errorf("busy complexity_basis = %s", got)
	}
	// The same repository through the ops operation's argMax-over-window
	// statement shape (hotspots.go:241-262): every file of the latest run
	// carries the same stored values there.
	opsRows, opsErr := direct.Query(ctx, `SELECT file_path, tupleElement(agg, 1), tupleElement(agg, 2), tupleElement(agg, 3), tupleElement(agg, 6)
FROM (
	SELECT file_path, argMax(tuple(churn_loc_30d, churn_commits_30d, cyclomatic_total, cyclomatic_avg, blame_concentration, risk_score), (day, computed_at)) AS agg
	FROM file_hotspot_daily
	WHERE org_id = ? AND toString(repo_id) = ? AND day >= toDate('2026-09-25') AND day <= toDate('2026-10-02')
	GROUP BY file_path
)`, orgID, busy)
	if opsErr != nil {
		t.Fatalf("ops statement shape: %v", opsErr)
	}
	opsByFile := map[string]string{}
	for opsRows.Next() {
		var path string
		var churn uint64
		var commits, cyclomatic uint32
		var risk float64
		if err := opsRows.Scan(&path, &churn, &commits, &cyclomatic, &risk); err != nil {
			t.Fatalf("ops statement scan: %v", err)
		}
		opsByFile[path] = fmt.Sprintf("%d/%d/%d/%g", churn, commits, cyclomatic, risk)
	}
	opsRows.Close()
	for i := 11; i >= 2; i-- {
		path := fmt.Sprintf("src/file_%02d.go", i)
		want := fmt.Sprintf("%d/%d/%d/%g", 100+i, 3, 5+i, float64(i))
		if opsByFile[path] != want {
			t.Errorf("ops argMax shape for %s = %q, want %q", path, opsByFile[path], want)
		}
		if !strings.Contains(files, path) {
			t.Errorf("served hotspot_files lacks %s: %s", path, files)
		}
	}
	e := byRepo["repository:"+empty]
	if got := field(e, "hotspot_basis"); got != `"no_hotspot_rows_in_window"` {
		t.Errorf("empty repository hotspot_basis = %s, want no_hotspot_rows_in_window: %s", got, raw)
	}
	for _, name := range []string{"hotspot_files", "hotspot_file_count", "latest_run_day"} {
		if got := field(e, name); got != "<absent>" {
			t.Errorf("empty repository %s = %s, want absent, never a zero or an empty list", name, got)
		}
	}
	n := byRepo["repository:"+noSnapshot]
	if got := field(n, "complexity_basis"); got != `"no_complexity_snapshot_stored"` {
		t.Errorf("no-snapshot complexity_basis = %s", got)
	}
	if !strings.Contains(field(n, "rank_basis"), "reflects churn only") {
		t.Errorf("no-snapshot rank_basis does not say the stored score reflects churn only: %s", field(n, "rank_basis"))
	}
	if table, ok := n["hotspot_files"].(directread.ServedTable); ok {
		column := -1
		for index, name := range table.Columns {
			if name == "cyclomatic_total" {
				column = index
			}
		}
		for _, row := range table.Rows {
			if column < 0 || row[column] != nil {
				t.Errorf("no-snapshot cyclomatic_total is served as %v, want null: %v", row[column], row)
			}
		}
	} else {
		t.Errorf("no-snapshot hotspot_files table is absent: %v", n)
	}

	// A repository outside the caller's grant yields no fact, and the
	// provider is never asked for it.
	before := len(seam.requests)
	_, restricted, rawRestricted := read([]string{slugs[busy]}, rangeWindow, busy, secret)
	if _, leaked := restricted["repository:"+secret]; leaked || strings.Contains(rawRestricted, "secret/plan.go") {
		t.Fatalf("a repository outside the grant was served: %s", rawRestricted)
	}
	if _, ok := restricted["repository:"+busy]; !ok {
		t.Fatalf("the granted repository was not served: %s", rawRestricted)
	}
	for _, request := range seam.requests[before:] {
		for _, subject := range request.Subjects {
			if strings.HasSuffix(subject.CanonicalID, secret) {
				t.Fatalf("the provider seam was asked for a repository outside the grant: %v", subject)
			}
		}
	}

	// The current axis serves the newest run: the late one.
	_, current, rawCurrent := read([]string{"*"}, nil, busy)
	if got := field(current["repository:"+busy], "latest_run_day"); got != `"2026-10-10"` {
		t.Errorf("current latest_run_day = %s, want the newest run: %s", got, rawCurrent)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
