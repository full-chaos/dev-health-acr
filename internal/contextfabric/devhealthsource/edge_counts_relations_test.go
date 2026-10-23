package devhealthsource

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestClickHouseEdgePopulationsMeasureEveryArmAndZeroFillTheMissingOne(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{
		"'pull_request' AS arm": {{"pull_request", uint64(5)}, {"incident", uint64(2)}},
		"GROUP BY arm":          {{"native", uint64(3)}},
	}}
	source, err := NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	repository := func(kind contractsv1.ContextFabricSubjectKind) contextfabric.EdgeCountKey {
		return contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: string(kind)}
	}
	link := func(tier string) contextfabric.EdgeCountKey {
		return contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipLinksPullRequest, Arm: tier}
	}
	want := map[contextfabric.EdgeCountKey]int64{
		repository(contractsv1.ContextFabricSubjectPullRequest):                 5,
		repository(contractsv1.ContextFabricSubjectIncident):                    2,
		repository(contractsv1.ContextFabricSubjectWorkItem):                    0,
		repository(contractsv1.ContextFabricSubjectDeployment):                  0,
		repository(contractsv1.ContextFabricSubjectCIRun):                       0,
		link(contextfabric.EdgeArmLinkNative):                                   3,
		link(contextfabric.EdgeArmLinkExplicitText):                             0,
		link(contextfabric.EdgeArmLinkHeuristic):                                0,
		{Relation: contractsv1.ContextFabricRelationshipCorrelatedWithIncident}: 0,
		{Relation: contractsv1.ContextFabricRelationshipPartOf}:                 0,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}:   0,
	}
	for key, n := range want {
		if got, ok := counts[key]; !ok || got != n {
			t.Errorf("%+v = %d (present %v), want %d", key, got, ok, n)
		}
	}
	if len(counts) != len(want) {
		t.Errorf("returned %d keys, want %d: %+v", len(counts), len(want), counts)
	}
	declared := source.EdgeCountKeys()
	if len(declared) != len(want) {
		t.Errorf("declared %d keys, want %d", len(declared), len(want))
	}
}

func TestClickHouseEdgePopulationsRefuseAnUnknownArm(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{"GROUP BY arm": {{"mystery", uint64(1)}}}}
	source, _ := NewClickHouseProjectionSource(client)
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err == nil || !strings.Contains(err.Error(), `unknown arm "mystery"`) || counts != nil {
		t.Fatalf("want an unknown-arm error and no counts, got %+v, %v", counts, err)
	}
}

func TestDeferredEdgeRelationsAreTheDependencyTypesOnly(t *testing.T) {
	t.Parallel()
	source, _ := NewClickHouseProjectionSource(&edgeCountClient{})
	var got []string
	for _, relation := range source.DeferredEdgeRelations() {
		got = append(got, string(relation))
	}
	sort.Strings(got)
	var want []string
	for _, relation := range []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBlocks, contractsv1.ContextFabricRelationshipDuplicates,
		contractsv1.ContextFabricRelationshipRelatedTo, contractsv1.ContextFabricRelationshipRelatesTo,
	} {
		want = append(want, string(relation))
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deferred = %v, want %v", got, want)
	}
}

func TestBelongsToRepositoryEdgeCountKeepsTheOmissionsOfEachProducer(t *testing.T) {
	t.Parallel()
	statement := belongsToRepositoryEdgeCountSQL()
	if strings.Count(statement, "UNION ALL") != len(repositoryEdgeArms)-1 {
		t.Fatalf("one SELECT per arm:\n%s", statement)
	}
	for _, want := range []string{
		derivedIDFitsSQL(identity.KindWorkItem, "toString(w.repo_id)", "w.work_item_id"),
		derivedIDFitsSQL(identity.KindDeployment, "toString(d.repo_id)", "d.deployment_id"),
		derivedIDFitsSQL(identity.KindCIPipelineRun, "toString(c.repo_id)", "c.run_id"),
		"ifNull(r.repo, '') != ''",
		"i.is_deleted = 0",
		"uniqExact(i.id, m.repo_id)",
		"m.is_active = 1",
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, statement)
		}
	}
}

func TestPartOfEdgeCountReadsTheProducersOwnRowSource(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{}
	if _, _, err := queryWorkItemHierarchy(context.Background(), client, "org-a", cursorState{}, 10); err != nil {
		t.Fatal(err)
	}
	from := hierarchyFromSQL(hierarchyParentRef)
	if len(client.statements) == 0 || !strings.Contains(client.statements[len(client.statements)-1], from) {
		t.Fatal("the producer must read hierarchyFromSQL")
	}
	statement := partOfEdgeCountSQL()
	if !strings.Contains(statement, from) {
		t.Fatalf("the count must read the same row source:\n%s", statement)
	}
	qualified := qualifiedParentSQL(hierarchyParentRef)
	for _, want := range []string{
		"c.parent_ref != c.work_item_id",
		"NOT (" + emptyKeyParentSQL(hierarchyParentRef) + ")",
		"ifNull(pa.candidates, 0) > 0",
		"NOT (" + qualified + " AND ifNull(pa.candidates, 0) > 1)",
		"(" + qualified + " OR ifNull(p.work_item_id, '') != '')",
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks the producer's retraction clause %q", want)
		}
	}
}

func TestLinksPullRequestEdgeCountDropsALowerTierBesideANativeLink(t *testing.T) {
	t.Parallel()
	statement := linksPullRequestEdgeCountSQL()
	for _, want := range []string{
		issuePullRequestLinkOutranked + " = 0",
		"GROUP BY arm",
		"lower(trimBoth(ifNull(w.type, ''))) NOT IN ('merge_request', 'pr')",
		"ifNull(p.number, 0) > 0 AND trimBoth(ifNull(r.repo, '')) != ''",
		"ifNull(w.work_item_id, '') != ''",
		derivedIDFitsSQL(identity.KindWorkItem, "toString(w.repo_id)", "w.work_item_id"),
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, statement)
		}
	}
	if len(issuePullRequestLinkTierRank) != len(linkEdgeArms) {
		t.Fatalf("the arms are the producer's tiers: %v vs %v", issuePullRequestLinkTierRank, linkEdgeArms)
	}
	for _, arm := range linkEdgeArms {
		if _, ok := issuePullRequestLinkTierRank[arm]; !ok {
			t.Errorf("arm %q is not a producer tier", arm)
		}
	}
}
