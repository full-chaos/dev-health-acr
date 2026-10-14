package sourcerow_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The blocker facts and the graph projection read the same
// work_item_dependencies rows; every spelling of a blocker must reach both,
// with the same direction, once per pair.
func TestBlockerFactsAgreeWithProjectedBlocksEdges(t *testing.T) {
	ctx := context.Background()
	query, direct := startClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		exec(t, ctx, direct, statement)
	}
	org := integrationOrg
	id := func(name string) string { return "github:acme/api#" + name }
	itemRows := []string{}
	for _, name := range []string{"A1", "A2", "A3", "A4", "B1", "B2", "B3", "B4"} {
		itemRows = append(itemRows, fmt.Sprintf("('%s', '%s', 'github', 'item %s', 'open', %s, %s, '', %s, '%s')", grantedID, id(name), name, now7252, now7252, now7252, org))
	}
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, grantedID, grantedRep, now7252, now7252, org),
		`INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES ` + strings.Join(itemRows, ", "),
		fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES
			('%[2]s', '%[3]s', 'blocks', 'forward B1 blocks A1', %[1]s, '%[8]s'),
			('%[4]s', '%[5]s', 'blocked_by', 'inverse A2 blocked_by B2', %[1]s, '%[8]s'),
			('%[6]s', '%[7]s', ' Is_Blocked_By ', 'inverse A3 is_blocked_by B3', %[1]s, '%[8]s'),
			('%[9]s', '%[10]s', 'blocks', 'twin forward B4 blocks A4', %[1]s, '%[8]s'),
			('%[10]s', '%[9]s', 'BLOCKED_BY', 'twin inverse A4 blocked_by B4', %[1]s, '%[8]s'),
			('%[11]s', '%[4]s', 'requires', 'A1 requires A2', %[1]s, '%[8]s')`,
			now7252, id("B1"), id("A1"), id("A2"), id("B2"), id("A3"), id("B3"), org, id("B4"), id("A4"), id("A1")),
	} {
		exec(t, ctx, direct, statement)
	}

	projection, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	graph := map[string]bool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		batch, available, err := projection.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !available {
			break
		}
		for _, relationship := range batch.Relationships {
			if relationship.Type == contractsv1.ContextFabricRelationshipBlocks {
				graph[relationship.From.Label+" blocks "+relationship.To.Label] = true
			}
		}
		cursor = batch.NextCursor
	}
	if len(graph) != 4 {
		t.Fatalf("graph blocks edges = %v, want 4 (one per pair)", graph)
	}

	subject := func(name string) contextfabric.SubjectRef {
		canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{grantedID, id(name)}, nil)
		if err != nil || omitted {
			t.Fatalf("derive %s: %v omitted=%v", name, err, omitted)
		}
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonical, Label: id(name)}
	}
	subjects := []contextfabric.SubjectRef{subject("A1"), subject("A2"), subject("A3"), subject("A4")}
	read := func(kind contextfabric.FactKind) []contextfabric.CanonicalFact {
		for _, provider := range devhealthfacts.NewProviders(query) {
			if provider.Capability().Kind != kind {
				continue
			}
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind, Subjects: subjects,
			})
			if err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
			return result.Facts
		}
		t.Fatalf("no provider for %s", kind)
		return nil
	}

	facts := map[string]bool{}
	refByBlocked := map[string]string{}
	for _, fact := range read(contextfabric.FactBlockers) {
		blocker := *fact.Fields["blocked_by_work_item_id"].String
		facts[blocker+" blocks "+fact.Subject.Label] = true
		refByBlocked[fact.Subject.Label] = fact.EvidenceRefIDs[0]
	}
	if !equalSets(facts, graph) {
		t.Fatalf("blocker facts %v disagree with graph blocks edges %v", sorted(facts), sorted(graph))
	}

	// An inverse row is a blocker, not a required child; only the
	// 'requires' row of A1 is one.
	children := read(contextfabric.FactRequiredChildren)
	if len(children) != 1 || children[0].Subject.Label != id("A1") || *children[0].Fields["required_child_work_item_id"].String != id("A2") {
		t.Fatalf("required children = %+v, want only A1 requires A2", children)
	}

	resolve, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(query), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	resolve.WithRepoLessAdmitter(devhealthfacts.NewRepoLessWorkItemAdmitter(query))
	for blocked, want := range map[string]string{id("A2"): "inverse A2 blocked_by B2", id("A3"): "inverse A3 is_blocked_by B3"} {
		ref := refByBlocked[blocked]
		kind, refID := splitRef(ref)
		expanded, decision := resolve.ResolveSourceRow(ctx, storage.Principal{OrgID: org}, string(kind), refID)
		if decision.Reason != contextfabric.SourceRowServed || !strings.Contains(expanded.Evidence.Citation, want) {
			t.Errorf("blocker evidence of %s: decision %+v citation %q, want the stored row %q", blocked, decision, expanded.Evidence.Citation, want)
		}
	}
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
