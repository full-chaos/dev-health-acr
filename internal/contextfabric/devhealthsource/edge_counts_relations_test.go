package devhealthsource

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestClickHouseEdgePopulationsMeasureEveryArmAndZeroFillTheMissingOne(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{
		"'pull_request' AS arm":    {{"pull_request", uint64(5)}, {"incident", uint64(2)}},
		"FROM work_graph_issue_pr": {{"native", uint64(3)}},
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
		{Relation: contractsv1.ContextFabricRelationshipBlocks}:                 0,
		{Relation: contractsv1.ContextFabricRelationshipRelatesTo}:              0,
		{Relation: contractsv1.ContextFabricRelationshipDuplicates}:             0,
		{Relation: contractsv1.ContextFabricRelationshipRelatedTo}:              0,
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
	client := &edgeCountClient{rows: map[string][][]any{"FROM work_graph_issue_pr": {{"mystery", uint64(1)}}}}
	source, _ := NewClickHouseProjectionSource(client)
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err == nil || !strings.Contains(err.Error(), `unknown arm "mystery"`) || counts != nil {
		t.Fatalf("want an unknown-arm error and no counts, got %+v, %v", counts, err)
	}
}

func TestClickHouseSourceDefersNoRelationType(t *testing.T) {
	t.Parallel()
	source, _ := NewClickHouseProjectionSource(&edgeCountClient{})
	if deferred := source.DeferredEdgeRelations(); len(deferred) != 0 {
		t.Fatalf("every relation type the source produces is measured, deferred = %v", deferred)
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
		"lower(" + trimSpaceSQL("ifNull(w.type, '')") + ") NOT IN ('merge_request', 'pr')",
		"ifNull(p.number, 0) > 0 AND " + trimSpaceSQL("ifNull(r.repo, '')") + " != ''",
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

func TestDependencyEdgeCountReadsTheProducersOwnRowSourceAndKeepsItsOmissions(t *testing.T) {
	t.Parallel()
	statement := dependencyEdgeCountSQL()
	for _, want := range []string{
		dependencyFromSQL(),
		dependencyrelation.KeySQL(dependencyRelationshipTypeExpr),
		"ifNull(rr.ended, 0) = 0",
		"NOT IN ('external_issue_key')",
		derivedIDFitsSQL(identity.KindWorkItem, "toString(w.repo_id)", "d.source_work_item_id"),
		derivedIDFitsSQL(identity.KindWorkItem, "toString(t.repo_id)", "d.target_work_item_id"),
		"isNotNull(t.created_at)",
		"src != tgt",
		"if(endsWith(k, ':inv'), tgt, src) AS edge_from",
		"uniqExact(edge_from, edge_to)",
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, statement)
		}
	}
}

func TestEveryDependencySpellingResolvesToAMeasuredRelation(t *testing.T) {
	t.Parallel()
	for _, alias := range dependencyrelation.Aliases() {
		typ, _, _ := dependencyrelation.Resolve(alias)
		if _, ok := dependencyEdgeRelations[strings.ToLower(string(typ))]; !ok {
			t.Errorf("dependencyrelation maps %q onto %s, which no dependency edge count measures: add it to dependencyEdgeRelations and to ProducedRelationshipTypes", alias, typ)
		}
	}
	produced := map[contractsv1.ContextFabricRelationshipType]bool{}
	for _, relation := range ProducedRelationshipTypes() {
		produced[relation] = true
	}
	for arm, relation := range dependencyEdgeRelations {
		if !produced[relation] || arm != strings.ToLower(string(relation)) {
			t.Errorf("dependency arm %q relation %s is not a produced relation type spelled as its arm", arm, relation)
		}
	}
}

func TestDependencyEdgeCountBindsTheRemovedLinkSetAndReportsEachRelation(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{
		"AS ended_links": {{[]string{"6162.6364.626c6f636b73.1"}, uint64(1), uint64(0), uint64(0)}},
		"AS edge_count FROM (\n  SELECT substring": {{"blocks", uint64(4)}, {"relates_to", uint64(2)}},
	}}
	source, _ := NewClickHouseProjectionSource(client)
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	want := map[contractsv1.ContextFabricRelationshipType]int64{
		contractsv1.ContextFabricRelationshipBlocks: 4, contractsv1.ContextFabricRelationshipRelatesTo: 2,
		contractsv1.ContextFabricRelationshipDuplicates: 0, contractsv1.ContextFabricRelationshipRelatedTo: 0,
	}
	for relation, n := range want {
		if got, ok := counts[contextfabric.EdgeCountKey{Relation: relation}]; !ok || got != n {
			t.Errorf("%s = %d (present %v), want %d", relation, got, ok, n)
		}
	}
	var bound bool
	for i, statement := range client.statements {
		if strings.Contains(statement, "AS edge_count FROM (\n  SELECT substring") {
			for _, b := range client.bindings[i] {
				bound = bound || b.Name == "ended_links"
			}
		}
	}
	if !bound {
		t.Fatal("the dependency count must bind the removed-link set the producer binds")
	}
}

func TestDependencyEdgeCountsAreUnmeasuredWhenTheRemovedLinkSetIsCut(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{
		"AS ended_links": {{[]string{"6162.6364.626c6f636b73.1"}, uint64(5), uint64(0), uint64(0)}},
	}}
	source, _ := NewClickHouseProjectionSource(client)
	source.SetEndedLinksBoundForTest(1)
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	var unmeasured *contextfabric.EdgeCountsUnmeasured
	if !errors.As(err, &unmeasured) || unmeasured.Reason != "removed_link_set_cut" || len(unmeasured.Keys) != len(dependencyEdgeRelations) {
		t.Fatalf("a cut removed-link set is reported unmeasured with its reason, got %v", err)
	}
	for _, key := range source.EdgeCountKeys() {
		_, present := counts[key]
		isDependency := key.Relation == contractsv1.ContextFabricRelationshipBlocks || key.Relation == contractsv1.ContextFabricRelationshipRelatesTo ||
			key.Relation == contractsv1.ContextFabricRelationshipDuplicates || key.Relation == contractsv1.ContextFabricRelationshipRelatedTo
		if isDependency == present {
			t.Errorf("%+v present=%v: a cut removed-link set leaves the dependency keys out and keeps the others", key, present)
		}
	}
	for _, statement := range client.statements {
		if strings.Contains(statement, "AS edge_count FROM (\n  SELECT substring") {
			t.Fatal("no count statement runs over a truncated removed-link set")
		}
	}
}

func TestDependencyEdgeCountDropsARelationWhoseEvidenceReferenceBreaksTheBound(t *testing.T) {
	t.Parallel()
	statement := dependencyEdgeCountSQL()
	ref := "concat('acr:v1:work-item-dependency.v2:', " + encodeSegmentSQL("d.source_work_item_id") + ", ':', " + encodeSegmentSQL("d.target_work_item_id") + ", ':', " + encodeSegmentSQL("k") + ")"
	for _, want := range []string{"lengthUTF8(" + ref + ") <= 256", "position(" + ref + ", '|') = 0", "AND ref_ok"} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks the evidence-reference bound %q:\n%s", want, statement)
		}
	}
	got, fits := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "s", "t", dependencyrelation.Key("blocks"))
	if want := "acr:v1:work-item-dependency.v2:s:t:blocks%3Afwd"; got != want || !fits {
		t.Fatalf("the SQL mirrors the producer's reference %q, got %q fits=%v", want, got, fits)
	}
}

func TestDependencyProducerStatementIsByteIdenticalToTheGolden(t *testing.T) {
	t.Parallel()
	for name, cursor := range map[string]cursorState{"empty": {}, "after": {After: "k"}} {
		client := &edgeCountClient{}
		if _, _, err := queryWorkItemDependencies(context.Background(), client, "org-a", cursor, 10); err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile("testdata/work_item_dependencies_statement_" + name + ".golden.sql")
		if err != nil {
			t.Fatal(err)
		}
		if got := client.statements[len(client.statements)-1]; got != string(want) {
			t.Errorf("cursor %s: the producer statement drifted from the golden captured before dependencyFromSQL was extracted", name)
		}
	}
}
