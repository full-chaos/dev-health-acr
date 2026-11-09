package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// chunkedRollupClient answers the roll-up mix with 600 pinned units cut into two
// chunks at u300. The repo arm counts two projects, linear:a000 and linear:z.
// Per chunk, the evidence arm and the bugfix phase answer:
//
//	chunk u000..u299: linear:a000 .. linear:a250 (251 keys, each 1)
//	chunk u300..u599: linear:a000 = 2, linear:z = 5
//
// One unchunked statement would see 252 keys and keep the first 251 (its
// population LIMIT): linear:a000 = 3, linear:z cut.
type chunkedRollupClient struct{ evidence, bugfix []string }

func (c *chunkedRollupClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	bound := map[string]any{}
	for _, b := range bindings {
		bound[b.Name] = b.Value
	}
	perChunk := func(first, second func() [][]any) [][]any {
		if bound["chunk_lo"] == "u300" {
			return second()
		}
		return first()
	}
	firstChunkKeys := func(value any) [][]any {
		rows := make([][]any, 0, 251)
		for i := 0; i < 251; i++ {
			rows = append(rows, []any{fmt.Sprintf("linear:a%03d", i), value})
		}
		return rows
	}
	switch {
	case strings.Contains(statement, "sum(cityHash64("):
		row := make([]any, 64)
		for i := range row {
			row[i] = uint64(1)
		}
		return &fakeScanner{rows: [][]any{row}}, nil
	case strings.Contains(statement, "groupArrayIf(work_unit_id, in_window)"):
		ids, versions := make([]string, 0, 600), make([]int64, 0, 600)
		for i := 0; i < 600; i++ {
			ids, versions = append(ids, fmt.Sprintf("u%03d", i)), append(versions, int64(1000+i))
		}
		return &fakeScanner{rows: [][]any{{ids, versions, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}}, nil
	case strings.Contains(statement, "groupArray(link_teams)"):
		return &fakeScanner{rows: [][]any{{[]string{}, []string{}, []string{}, []string{}, [][]string{}}}}, nil
	case strings.Contains(statement, "AS chunk_bounds"):
		return &fakeScanner{rows: [][]any{{[]string{"u300"}}}}, nil
	case strings.Contains(statement, "AS pinned_present"):
		return &fakeScanner{rows: [][]any{{uint64(600)}}}, nil
	case strings.Contains(statement, "AS feature_delivery"):
		at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		return &fakeScanner{rows: [][]any{
			{"linear:a000", 1.0, 0.0, 0.0, 0.0, 0.0, uint64(10), uint64(1), uint64(1), at, uint64(0), uint64(0)},
			{"linear:z", 1.0, 0.0, 0.0, 0.0, 0.0, uint64(10), uint64(1), uint64(1), at, uint64(0), uint64(0)},
		}}, nil
	case strings.Contains(statement, "AS bugfix_weighted"):
		c.bugfix = append(c.bugfix, fmt.Sprint(bound["chunk_lo"]))
		return &fakeScanner{rows: perChunk(func() [][]any { return firstChunkKeys(1.0) }, func() [][]any {
			return [][]any{{"linear:a000", 2.0}, {"linear:z", 2.5}}
		})}, nil
	case strings.Contains(statement, "AS excluded_no_repo_link"):
		c.evidence = append(c.evidence, fmt.Sprint(bound["chunk_lo"]))
		return &fakeScanner{rows: perChunk(func() [][]any { return firstChunkKeys(uint64(1)) }, func() [][]any {
			return [][]any{{"linear:a000", uint64(2)}, {"linear:z", uint64(5)}}
		})}, nil
	}
	return nil, fmt.Errorf("chunkedRollupClient: unexpected statement %.80q", statement)
}

// The roll-up adds each project's per-chunk counts and bugfix sums, and keeps
// exactly the project keys one unchunked statement's population LIMIT kept: a
// key only a later chunk ranks within its own first 251 is cut, as before.
func TestProjectRollupChunksAddPerProjectAndKeepTheUnchunkedPopulationCut(t *testing.T) {
	t.Parallel()
	client := &chunkedRollupClient{}
	rows, err := devhealthfacts.RunProjectRollupMix(context.Background(), client, "org-1", []string{"linear:a000", "linear:z"}, devhealthfacts.ProjectMixWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(client.evidence, ","), "u000,u300"; got != want {
		t.Fatalf("evidence arm ran for chunks %s, want %s", got, want)
	}
	if got, want := strings.Join(client.bugfix, ","), "u000,u300"; got != want {
		t.Fatalf("bugfix phase ran for chunks %s, want %s", got, want)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.ProjectKey] = fmt.Sprintf("excluded=%d bugfix=%g", r.ExcludedNoRepoLink, r.BugfixWeighted)
	}
	want := map[string]string{"linear:a000": "excluded=3 bugfix=3", "linear:z": "excluded=0 bugfix=0"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
}
