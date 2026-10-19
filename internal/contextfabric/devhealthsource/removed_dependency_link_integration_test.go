package devhealthsource_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// A dependency link of any type the provider removed leaves its insert-only
// row behind. When the items that write the link sync again without it, the
// link is gone and its edge must be retracted in an incremental pass and left
// out of a rebuild. A link whose writer is not known from the row (only one
// item synced again, or a raw kind read by a pass that can skip it) must keep
// its edge: a live link is never dropped.
func TestRemovedDependencyLinkOfAnyTypeRetractsOrKeepsItsEdge(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	linked := now.Add(-5 * time.Hour)
	resynced := now.Add(-time.Hour)
	type item struct{ id, provider string }
	cases := []struct {
		name           string
		source, target item
		relType, raw   string
		writer         string
		resync         []string
		stillReported  bool
		twin           *struct{ relType, raw string }
		wantRetracted  bool
		// wantDisclosed is the log attribute the kept edge is counted under:
		// kept_links (the writer is not known) or left_out_links (a twin
		// still states the edge); "" for no disclosure line.
		wantDisclosed string
	}{
		{name: "jira relates_to removed", source: item{"jira:RDL-1", "jira"}, target: item{"jira:RDL-2", "jira"}, relType: "relates_to", raw: "relates to",
			resync: []string{"jira:RDL-1", "jira:RDL-2"}, wantRetracted: true},
		{name: "jira duplicates removed", source: item{"jira:RDL-3", "jira"}, target: item{"jira:RDL-4", "jira"}, relType: "duplicates", raw: "duplicates",
			resync: []string{"jira:RDL-3", "jira:RDL-4"}, wantRetracted: true},
		{name: "linear relates_to removed", source: item{"linear:RDL-5", "linear"}, target: item{"linear:RDL-6", "linear"}, relType: "relates_to", raw: "linear_relation:related",
			resync: []string{"linear:RDL-5", "linear:RDL-6"}, wantRetracted: true},
		{name: "gitlab relates_to with a stored writer, one writer synced", source: item{"gitlab:acme/rdl#7", "gitlab"}, target: item{"gitlab:acme/rdl#8", "gitlab"}, relType: "relates_to", raw: "relates_to",
			writer: "both", resync: []string{"gitlab:acme/rdl#8"}, wantRetracted: true},
		{name: "jira relates_to, only one item synced again", source: item{"jira:RDL-9", "jira"}, target: item{"jira:RDL-10", "jira"}, relType: "relates_to", raw: "relates to",
			resync: []string{"jira:RDL-9"}, wantRetracted: false},
		{name: "jira relates_to still reported", source: item{"jira:RDL-11", "jira"}, target: item{"jira:RDL-12", "jira"}, relType: "relates_to", raw: "relates to",
			resync: []string{"jira:RDL-11", "jira:RDL-12"}, stillReported: true, wantRetracted: false},
		{name: "relates_to removed beside a live RELATES twin", source: item{"jira:RDL-13", "jira"}, target: item{"jira:RDL-14", "jira"}, relType: "relates_to", raw: "relates to",
			resync: []string{"jira:RDL-13", "jira:RDL-14"}, twin: &struct{ relType, raw string }{"RELATES", "relates to"}, wantRetracted: false, wantDisclosed: "left_out_links=1"},
		{name: "jira dev status link, writer pass can skip", source: item{"ghpr:acme/rdl#15", "github"}, target: item{"jira:RDL-16", "jira"}, relType: "relates_to", raw: "jira_dev_status",
			resync: []string{"ghpr:acme/rdl#15", "jira:RDL-16"}, wantRetracted: false, wantDisclosed: "kept_links=1"},
		{name: "linear attachment link, writer not stored", source: item{"ghpr:acme/rdl#17", "github"}, target: item{"linear:RDL-18", "linear"}, relType: "relates_to", raw: "linear_attachment",
			resync: []string{"ghpr:acme/rdl#17", "linear:RDL-18"}, wantRetracted: false, wantDisclosed: "kept_links=1"},
		{name: "github closing reference", source: item{"ghpr:acme/rdl#19", "github"}, target: item{"gh:acme/rdl#20", "github"}, relType: "relates_to", raw: "github_closing_reference",
			resync: []string{"ghpr:acme/rdl#19", "gh:acme/rdl#20"}, wantRetracted: false, wantDisclosed: "kept_links=1"},
		{name: "gitlab closing reference", source: item{"gitlab:acme/rdl!21", "gitlab"}, target: item{"gitlab:acme/rdl#22", "gitlab"}, relType: "relates_to", raw: "gitlab_closing_reference",
			resync: []string{"gitlab:acme/rdl!21", "gitlab:acme/rdl#22"}, wantRetracted: false, wantDisclosed: "kept_links=1"},
		{name: "github comment linear url", source: item{"gh:acme/rdl#23", "github"}, target: item{"gh:acme/rdl#24", "github"}, relType: "relates_to", raw: "github_comment_linear_url",
			resync: []string{"gh:acme/rdl#23", "gh:acme/rdl#24"}, wantRetracted: false, wantDisclosed: "kept_links=1"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			org := fmt.Sprintf("95290000-0000-4000-8000-0000000001%02d", index)
			repo := fmt.Sprintf("95290000-0000-4000-8000-0000000002%02d", index)
			mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo, fmt.Sprintf("acme/removed-link-%d", index), nil, now, nil, linked, org, tc.source.provider)
			insertItem := func(it item, at time.Time) {
				mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					it.id, repo, org, "item "+it.id, "open", "", "", it.provider, "", at, at)
			}
			insertRow := func(relType, raw string, at time.Time) {
				var writer *string
				if tc.writer != "" {
					writer = &tc.writer
				}
				mustExec(t, ctx, direct, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced, relation_writer) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					tc.source.id, tc.target.id, relType, raw, org, at, writer)
			}
			insertItem(tc.source, linked)
			insertItem(tc.target, linked)
			insertRow(tc.relType, tc.raw, linked)
			if tc.twin != nil {
				insertRow(tc.twin.relType, tc.twin.raw, linked)
			}

			source, err := devhealthsource.NewClickHouseProjectionSource(query)
			if err != nil {
				t.Fatal(err)
			}
			logged := &lockedBuffer{}
			source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
			drain := func(cursor string) (string, map[string]bool, map[string]bool) {
				edges, tombstones := map[string]bool{}, map[string]bool{}
				for i := 0; i < 60; i++ {
					batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
					if err != nil {
						t.Fatalf("NextProjectionBatch: %v%s", err, rawCauses(err))
					}
					if !ok {
						return cursor, edges, tombstones
					}
					cursor = batch.NextCursor
					for _, r := range batch.Relationships {
						edges[r.RelationshipID] = true
					}
					for _, tomb := range batch.Tombstones {
						if tomb.Kind == "relationship" {
							tombstones[tomb.CanonicalID] = true
						}
					}
				}
				t.Fatal("did not converge")
				return "", nil, nil
			}
			edgeID := workItemDependencyRelationshipID(t, repo, tc.source.id, repo, tc.target.id, tc.relType)

			cursor, edges, _ := drain("")
			if !edges[edgeID] {
				t.Fatalf("the link was not projected: %v", edges)
			}
			for _, id := range tc.resync {
				it := tc.source
				if id == tc.target.id {
					it = tc.target
				}
				insertItem(it, resynced)
			}
			if tc.stillReported {
				insertRow(tc.relType, tc.raw, resynced)
			}
			if tc.twin != nil {
				insertRow(tc.twin.relType, tc.twin.raw, resynced)
			}
			logged.Reset()
			_, _, retracted := drain(cursor)
			lines := logged.String()
			for _, attribute := range []string{"kept_links=", "left_out_links="} {
				counted := strings.Contains(lines, attribute)
				want := tc.wantDisclosed != "" && strings.HasPrefix(tc.wantDisclosed, attribute)
				if counted != want {
					t.Fatalf("%s disclosed=%v, want %v: %q", attribute, counted, want, lines)
				}
			}
			if tc.wantDisclosed != "" && !strings.Contains(lines, tc.wantDisclosed) {
				t.Fatalf("the kept edge was not counted as %s: %q", tc.wantDisclosed, lines)
			}
			if retracted[edgeID] != tc.wantRetracted {
				t.Fatalf("incremental pass: edge retracted=%v, want %v", retracted[edgeID], tc.wantRetracted)
			}
			_, rebuilt, _ := drain("")
			if rebuilt[edgeID] == tc.wantRetracted {
				t.Fatalf("rebuild: edge asserted=%v, want %v", rebuilt[edgeID], !tc.wantRetracted)
			}
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}
