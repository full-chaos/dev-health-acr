package devhealthfacts_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

//go:embed testdata/relationoracle/relation_current_golden.json
var relationCurrentGolden []byte

type relationOracleCase struct {
	Name           string  `json:"name"`
	Source         string  `json:"source"`
	Target         string  `json:"target"`
	Raw            string  `json:"raw"`
	Writer         *string `json:"writer"`
	RelationSynced int64   `json:"relation_synced"`
	SourceSynced   int64   `json:"source_synced"`
	TargetSynced   int64   `json:"target_synced"`
	SourceWrites   bool    `json:"source_writes"`
	TargetWrites   bool    `json:"target_writes"`
	Current        bool    `json:"current"`
}

func relationOracleTextProvider(id string) string {
	prefix, _, _ := strings.Cut(id, ":")
	switch prefix {
	case "gh", "ghpr":
		return "github"
	case "gitlab":
		return "gitlab"
	}
	return ""
}

// The SQL end rule agrees with the ops Go rule (RelationWriters,
// RelationEndedBy, BlockingRelation.RelationWritersOf), executed by
// scripts/oracle/relation-current-golden.sh over every provider x link kind x
// stored writer x sync order, external-key targets included. One named
// difference: a row whose writer cannot be derived (a text-reading provider,
// no stored writer, an unrecognized raw value) is taken by ops as written by
// both items, which ends a live relation when either syncs later; here it
// ends only when both have, and the row is flagged.
func TestRelationCurrentAgreesWithTheOpsRuleOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("work_items", "work_item_dependencies", "work_item_relations_read") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	var cases []relationOracleCase
	if err := json.Unmarshal(relationCurrentGolden, &cases); err != nil {
		t.Fatalf("golden: %v", err)
	}
	if len(cases) < 1000 {
		t.Fatalf("golden holds %d cases, want the whole matrix", len(cases))
	}
	const org = "relation-oracle-org"
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(seconds int64) time.Time { return base.Add(time.Duration(seconds) * time.Second) }
	suffix := func(index int) string { return fmt.Sprintf("~%d", index) }

	items, err := direct.PrepareBatch(ctx, "INSERT INTO work_items (work_item_id, repo_id, org_id, last_synced)")
	if err != nil {
		t.Fatal(err)
	}
	reads, err := direct.PrepareBatch(ctx, "INSERT INTO work_item_relations_read (org_id, provider, work_item_id, relations_read_at)")
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := direct.PrepareBatch(ctx, "INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_writer)")
	if err != nil {
		t.Fatal(err)
	}
	byPair := map[string]relationOracleCase{}
	for index, c := range cases {
		source, target := c.Source+suffix(index), c.Target+suffix(index)
		byPair[source+"\x00"+target] = c
		if index%2 == 0 {
			// A stored read time wins over the item's own latest sync.
			far := at(1_000_000)
			for _, row := range []struct {
				id     string
				synced int64
			}{{source, c.SourceSynced}, {target, c.TargetSynced}} {
				if err := items.Append(row.id, repoUUID("oracle"), org, far); err != nil {
					t.Fatal(err)
				}
				if err := reads.Append(org, "jira", row.id, at(row.synced)); err != nil {
					t.Fatal(err)
				}
			}
		} else {
			if err := items.Append(source, repoUUID("oracle"), org, at(c.SourceSynced)); err != nil {
				t.Fatal(err)
			}
			if err := items.Append(target, repoUUID("oracle"), org, at(c.TargetSynced)); err != nil {
				t.Fatal(err)
			}
		}
		if err := dependencies.Append(org, source, target, "blocks", c.Raw, at(c.RelationSynced), c.Writer); err != nil {
			t.Fatal(err)
		}
	}
	for _, batch := range []interface{ Send() error }{items, reads, dependencies} {
		if err := batch.Send(); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := direct.Query(ctx, devhealthfacts.RelationCurrentProbeForTest(org))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer rows.Close()
	seen, undecided, differing := 0, 0, 0
	for rows.Next() {
		var source, target string
		var current, flagged uint8
		if err := rows.Scan(&source, &target, &current, &flagged); err != nil {
			t.Fatal(err)
		}
		c, ok := byPair[source+"\x00"+target]
		if !ok {
			t.Fatalf("probe row %q > %q is no golden case", source, target)
		}
		seen++
		sourceLater, targetLater := c.SourceSynced > c.RelationSynced, c.TargetSynced > c.RelationSynced
		storedWriter := c.Writer != nil && (*c.Writer == "source" || *c.Writer == "target" || *c.Writer == "both")
		_, external := strings.CutPrefix(c.Target, "extkey:")
		textProvider := relationOracleTextProvider(c.Source)
		underivable := !storedWriter && !external && textProvider != "" && textProvider == relationOracleTextProvider(c.Target) && c.SourceWrites && c.TargetWrites
		if (flagged == 1) != underivable {
			t.Errorf("%s: writer flagged underivable = %v, want %v", c.Name, flagged == 1, underivable)
		}
		if !underivable {
			if (current == 1) != c.Current {
				t.Errorf("%s: current = %v, ops rule says %v", c.Name, current == 1, c.Current)
			}
			continue
		}
		undecided++
		want := !(sourceLater && targetLater)
		if (current == 1) != want {
			t.Errorf("%s: underivable writer: current = %v, want %v (ended only when both items synced later)", c.Name, current == 1, want)
		}
		if c.Current && current != 1 {
			t.Errorf("%s: ended a relation the ops rule keeps", c.Name)
		}
		if (current == 1) != c.Current {
			differing++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(cases) {
		t.Fatalf("probe returned %d rows for %d cases", seen, len(cases))
	}
	if undecided == 0 || differing == 0 {
		t.Fatalf("the matrix holds %d underivable cases and %d named differences, want both present", undecided, differing)
	}
}

// Known defects the rule must keep telling apart, each pinned by value so a
// regeneration of the golden cannot move them.
func TestRelationCurrentKnownDefectCases(t *testing.T) {
	var cases []relationOracleCase
	if err := json.Unmarshal(relationCurrentGolden, &cases); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		// A native link removed at the provider: either item's later sync ends it.
		`jira:OPS-1>jira:OPS-2|"blocks"|nil|2000/1000`: false,
		// A text relation written by the blocked item: the blocker's later sync says nothing.
		`gh:acme/api#7>gh:acme/api#12|"blocked by #12"|nil|2000/1000`: true,
		// The blocked item's later sync without the text ends it.
		`gh:acme/api#7>gh:acme/api#12|"blocked by #12"|nil|1000/2000`: false,
		// A stored writer overrides the raw value.
		`gitlab:acme/api#7>gitlab:acme/api#12|"blocks"|target|2000/1000`: true,
		`gitlab:acme/api#7>gitlab:acme/api#12|"blocks"|source|2000/1000`: false,
		// An external-key target is written by the source alone.
		`jira:OPS-1>extkey:OPS-2|"external_issue_key"|nil|1000/2000`: true,
		`jira:OPS-1>extkey:OPS-2|"external_issue_key"|nil|2000/1000`: false,
	}
	found := map[string]bool{}
	for _, c := range cases {
		if expected, ok := want[c.Name]; ok {
			found[c.Name] = true
			if c.Current != expected {
				t.Errorf("%s: ops rule says current = %v, pinned %v", c.Name, c.Current, expected)
			}
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("known case %s is not in the golden", name)
		}
	}
}
