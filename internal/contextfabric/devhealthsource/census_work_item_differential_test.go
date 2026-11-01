package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12456: the differential of the work-item census. The real resolver, the
// real adapter and a real principal, over a census source and a fake graph. The
// body runs on a statement model that evaluates what a statement carries, and on
// real ClickHouse (ACR_CONTAINER_TESTS=1).

const (
	linkKey        = "CHAOS-204"
	repoLessRepoID = "00000000-0000-0000-0000-000000000000"
	linkQuestion   = "why is " + linkKey + " blocked?"
)

// linkItem is one work_items row. An empty repository is repo-less: Linear and
// Jira write the zero UUID.
type linkItem struct {
	id, provider string
	repository   grantRepository
}

// issueLink is one work_graph_issue_pr row.
type issueLink struct {
	item, tier string
	repository grantRepository
}

// grantedItemWorld is one organization. addItem and addLink write a row; client is what
// the census reads.
type grantedItemWorld struct {
	grantWorld
	addItem func(t *testing.T, item linkItem)
	addLink func(t *testing.T, link issueLink)
	links   *[]issueLink
	items   *[]linkItem
}

func (w grantedItemWorld) naturalKey(item linkItem) string {
	repo := repoLessRepoID
	if item.repository.id != "" {
		repo = item.repository.id
	}
	return fmt.Sprintf("%s:%s:%s", w.org, repo, item.id)
}

func (w grantedItemWorld) canonicalID(t *testing.T, item linkItem) string {
	t.Helper()
	id, omitted, err := devhealthsource.BridgeSatisfierToCanonicalID(contextfabric.SubjectWorkItem, w.naturalKey(item))
	if err != nil || omitted {
		t.Fatalf("bridge %s: omitted %v err %v", item.id, omitted, err)
	}
	return id
}

func (w grantedItemWorld) granted() []grantRepository {
	return []grantRepository{w.insideGitHub, w.insideGitLab}
}

// node is what a read returns for a restricted caller: the sentinel, and the
// attribute the link reach adds for a native link to a granted repository.
func (w grantedItemWorld) node(t *testing.T, item linkItem) graphrank.CandidateNode {
	t.Helper()
	id := w.canonicalID(t, item)
	repositories := []string{contractsv1.ContextFabricNoRepositorySentinel}
	if item.repository.slug != "" {
		repositories = []string{item.repository.slug}
	}
	attributes := map[string]interface{}{
		"canonical_id": id, "subject_kind": string(contextfabric.SubjectWorkItem), "label": linkKey + " login bug",
		"evidence_refs": []string{"evidence_identity_1234"}, "authorization_repositories": repositories,
	}
	var linked []string
	for _, link := range *w.links {
		if link.item == item.id && link.tier == "native" && slices.Contains(w.granted(), link.repository) {
			linked = append(linked, link.repository.slug)
		}
	}
	if len(linked) > 0 && item.repository.slug == "" {
		sort.Strings(linked)
		attributes[graphrank.AuthorizationLinkRepositoriesAttr] = linked
	}
	return graphrank.CandidateNode{UUID: "node-" + id, Name: linkKey, Relevance: graphrank.Normalized(0.5), Attributes: attributes}
}

// observeLinkTurn runs one turn. The graph holds a node for every row, so a
// difference between two turns can only come from what the census read.
func observeLinkTurn(t *testing.T, world grantedItemWorld) grantObservation {
	t.Helper()
	nodes := map[string]graphrank.CandidateNode{}
	for _, item := range *world.items {
		nodes[world.canonicalID(t, item)] = world.node(t, item)
	}
	log := &grantStatementLog{inner: world.client}
	tracer := &grantTracer{}
	var graphReads []string
	deps := graphrank.ResolveDeps{
		ExactHint: func(_ context.Context, subject contextfabric.SubjectRef) (graphrank.CandidateNode, bool, error) {
			graphReads = append(graphReads, "exact "+subject.CanonicalID)
			node, ok := nodes[subject.CanonicalID]
			return node, ok, nil
		},
		Search: func(_ context.Context, term string, _ int) ([]graphrank.CandidateNode, bool, bool, error) {
			graphReads = append(graphReads, "search "+term)
			return nil, false, false, nil
		},
		Traverse: func(context.Context, string, graphrank.CandidateNode, bool) (contextfabric.SubjectCandidate, graphrank.ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, graphrank.ObservationNoParent
		},
		IsInternal:       func(contextfabric.SubjectRef) bool { return false },
		ResolutionTracer: tracer,
		CensusFunc:       devhealthsource.NewCensusFunc(log),
		HandleGrammarChecker: func(kind contextfabric.SubjectKind, patternID, value string) (string, bool) {
			return "work_items.work_item_id", graphrank.ValidateHandleGrammar(kind, patternID, value)
		},
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: linkQuestion,
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{linkKey},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	principal := userprincipal.Narrowed(t, world.org, []string{"acme/*", "group/*"}, []string{world.insideGitHub.slug, world.insideGitLab.slug})
	resolution, offers, bases, digests, err := graphrank.ResolveSubjectsWithCommitBasis(context.Background(), principal, request, interpreted, deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis: %v", err)
	}
	return grantObservation{resolution, offers, bases, digests, tracer.events, log.lines, graphReads}
}

func roundOutcome(t *testing.T, turn grantObservation) string {
	t.Helper()
	rounds := (&grantTracer{events: turn.trace}).stage("evidence_round")
	if len(rounds) != 1 {
		t.Fatalf("evidence_round = %+v, want one", rounds)
	}
	return rounds[0].ShadowOutcome
}

type grantedItemWorldBuilder func(t *testing.T, name string) grantedItemWorld

// outsideLinksChangeNothing reads the same turn before and after rows that lie
// outside the grant: an item linked only outside, a native link to an outside
// pull request on an item inside, and a lower-tier link into the grant.
func outsideLinksChangeNothing(t *testing.T, build grantedItemWorldBuilder) {
	type setup func(t *testing.T, w grantedItemWorld) []linkItem
	item := func(w grantedItemWorld, provider string) linkItem {
		return linkItem{id: provider + ":" + linkKey, provider: provider}
	}
	cases := []struct {
		name    string
		seed    setup
		outcome string
		commits int
	}{
		{"no linked item", func(*testing.T, grantedItemWorld) []linkItem { return nil }, string(graphrank.ShadowWouldNoMatch), 0},
		{"linear, linked to github", func(t *testing.T, w grantedItemWorld) []linkItem {
			i := item(w, "linear")
			w.addItem(t, i)
			w.addLink(t, issueLink{i.id, "native", w.insideGitHub})
			return []linkItem{i}
		}, string(graphrank.ShadowWouldCommit), 1},
		{"jira, linked to gitlab", func(t *testing.T, w grantedItemWorld) []linkItem {
			i := item(w, "jira")
			w.addItem(t, i)
			w.addLink(t, issueLink{i.id, "native", w.insideGitLab})
			return []linkItem{i}
		}, string(graphrank.ShadowWouldCommit), 1},
		{"two linked items", func(t *testing.T, w grantedItemWorld) []linkItem {
			a, b := item(w, "linear"), item(w, "jira")
			w.addItem(t, a)
			w.addItem(t, b)
			w.addLink(t, issueLink{a.id, "native", w.insideGitHub})
			w.addLink(t, issueLink{b.id, "native", w.insideGitLab})
			return []linkItem{a, b}
		}, string(graphrank.ShadowWouldClarify), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := build(t, tc.name)
			inside := tc.seed(t, world)
			before := observeLinkTurn(t, world)
			if got := roundOutcome(t, before); got != tc.outcome {
				t.Fatalf("round = %q, want %q", got, tc.outcome)
			}
			if len(before.resolution.Committed) != tc.commits {
				t.Fatalf("committed = %+v, want %d", before.resolution.Committed, tc.commits)
			}
			hidden := linkItem{id: "shortcut:" + linkKey, provider: "jira"}
			world.addItem(t, hidden)
			world.addLink(t, issueLink{hidden.id, "native", world.outsideGitHub})
			lower := linkItem{id: "other:" + linkKey, provider: "linear"}
			world.addItem(t, lower)
			world.addLink(t, issueLink{lower.id, "explicit_text", world.insideGitHub})
			for _, i := range inside {
				world.addLink(t, issueLink{i.id, "native", world.outsideGitLab})
			}
			after := observeLinkTurn(t, world)
			if !reflect.DeepEqual(before.resolution, after.resolution) {
				t.Fatalf("resolution changed:\nbefore %+v\nafter  %+v", before.resolution, after.resolution)
			}
			if !reflect.DeepEqual(before.offers, after.offers) {
				t.Fatalf("offers changed:\nbefore %+v\nafter  %+v", before.offers, after.offers)
			}
			if !reflect.DeepEqual(before.trace, after.trace) {
				t.Fatalf("trace changed:\nbefore %+v\nafter  %+v", before.trace, after.trace)
			}
			if !slices.Equal(before.statements, after.statements) || !slices.Equal(before.graphReads, after.graphReads) {
				t.Fatalf("reads changed:\nbefore %q %q\nafter  %q %q", before.statements, before.graphReads, after.statements, after.graphReads)
			}
		})
	}
}

// An id carried by a row the arm refuses is out, whole: the rule is every row.
func anIdWithARowOutsideTheArmIsNotCounted(t *testing.T, build grantedItemWorldBuilder) {
	world := build(t, "id in two rows")
	repoLess := linkItem{id: "jira:" + linkKey, provider: "jira"}
	world.addItem(t, repoLess)
	world.addLink(t, issueLink{repoLess.id, "native", world.insideGitHub})
	world.addItem(t, linkItem{id: repoLess.id, provider: "jira", repository: world.outsideGitHub})
	turn := observeLinkTurn(t, world)
	probes := (&grantTracer{events: turn.trace}).stage("evidence_probe")
	if len(probes) != 1 || probes[0].CensusCount != 0 {
		t.Fatalf("evidence_probe = %+v, want count 0", probes)
	}
}

func aLowerTierLinkNeverGrants(t *testing.T, build grantedItemWorldBuilder) {
	for _, tier := range []string{"explicit_text", "heuristic"} {
		t.Run(tier, func(t *testing.T) {
			world := build(t, tier)
			i := linkItem{id: "linear:" + linkKey, provider: "linear"}
			world.addItem(t, i)
			world.addLink(t, issueLink{i.id, tier, world.insideGitHub})
			turn := observeLinkTurn(t, world)
			if got := roundOutcome(t, turn); got != string(graphrank.ShadowWouldNoMatch) {
				t.Fatalf("round = %q, want would_no_match", got)
			}
			if len(turn.resolution.Committed) != 0 || turn.resolution.WorkItemLinkLimitation != contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation {
				t.Fatalf("resolution = %+v", turn.resolution)
			}
		})
	}
}

// A project a granted repository's team owns is never the relation.
func aProjectOwnedItemWithNoNativeLinkStaysDenied(t *testing.T, build grantedItemWorldBuilder) {
	world := build(t, "project owned")
	i := linkItem{id: "linear:" + linkKey, provider: "linear"}
	world.addItem(t, i)
	turn := observeLinkTurn(t, world)
	if got := roundOutcome(t, turn); got != string(graphrank.ShadowWouldNoMatch) {
		t.Fatalf("round = %q, want would_no_match", got)
	}
}

func aNativeLinkBesideAnOutsideOneAdmits(t *testing.T, build grantedItemWorldBuilder) {
	world := build(t, "beside outside")
	i := linkItem{id: "linear:" + linkKey, provider: "linear"}
	world.addItem(t, i)
	world.addLink(t, issueLink{i.id, "native", world.outsideGitHub})
	world.addLink(t, issueLink{i.id, "native", world.insideGitHub})
	turn := observeLinkTurn(t, world)
	want := world.canonicalID(t, i)
	if len(turn.resolution.Committed) != 1 || turn.resolution.Committed[0].CanonicalID != want {
		t.Fatalf("committed = %+v, want %s", turn.resolution.Committed, want)
	}
	rendered := fmt.Sprintf("%+v %+v %+v %q", turn.resolution, turn.offers, turn.trace, turn.graphReads)
	if strings.Contains(rendered, world.outsideGitHub.slug) || strings.Contains(rendered, world.outsideGitHub.id) {
		t.Fatalf("the outside pull request's repository is in what the turn exposes: %s", rendered)
	}
}

// linkStatementModel answers a work-item census from rows, evaluating what the
// statement and its bindings carry: the arm in its HAVING, the aggregate over an
// id's rows, and the grant arrays. A statement with no HAVING reads every row.
type linkStatementModel struct {
	world  *grantedItemWorld
	readAt time.Time
}

func (m *linkStatementModel) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	values := bindingValues(bindings)
	slugs, _ := values["authorized_repo_slugs"].([]string)
	owners, _ := values["authorized_repo_owners"].([]string)
	granted := func(repository grantRepository) bool {
		owner, _, _ := strings.Cut(repository.slug, "/")
		return values["authorized_repo_all"] == uint8(1) || slices.Contains(slugs, repository.slug) || slices.Contains(owners, owner)
	}
	having := ""
	if index := strings.Index(statement, "HAVING"); index >= 0 {
		having = statement[index:]
	}
	rowPasses := func(item linkItem) bool {
		if strings.Contains(having, "wia_link_auth.wia_link_repositories") && item.repository.id == "" {
			for _, link := range *m.world.links {
				if link.item == item.id && link.tier == "native" && granted(link.repository) {
					return true
				}
			}
		}
		// The direct arm reads the item's own repository.
		if strings.Contains(having, "authorized_repo_slugs") && item.repository.id != "" {
			return granted(item.repository)
		}
		return false
	}
	keep := map[string]bool{}
	byID := map[string][]linkItem{}
	for _, item := range *m.world.items {
		_, key, _ := strings.Cut(item.id, ":")
		if key == values["census_handle_ticket_key"] {
			byID[item.id] = append(byID[item.id], item)
		}
	}
	for id, rows := range byID {
		passed := 0
		for _, row := range rows {
			if rowPasses(row) {
				passed++
			}
		}
		switch {
		case having == "":
			keep[id] = true
		case strings.Contains(having, "HAVING max("):
			keep[id] = passed > 0
		default:
			keep[id] = passed == len(rows)
		}
	}
	var keys []string
	for id, rows := range byID {
		if keep[id] {
			for _, row := range rows {
				keys = append(keys, m.world.naturalKey(row))
			}
		}
	}
	sort.Strings(keys)
	fake := &censusFakeClient{aggregateCount: uint64(len(keys)), aggregateReadAt: m.readAt, rowKeys: keys}
	return fake.Query(ctx, statement, bindings)
}

func grantedItemModelWorld(t *testing.T, name string) grantedItemWorld {
	base := grantModelWorld(t, name)
	links, items := &[]issueLink{}, &[]linkItem{}
	world := grantedItemWorld{grantWorld: base, links: links, items: items}
	world.client = &linkStatementModel{world: &world, readAt: time.Unix(1_800_000_000, 0).UTC()}
	world.addItem = func(_ *testing.T, item linkItem) { *items = append(*items, item) }
	world.addLink = func(_ *testing.T, link issueLink) { *links = append(*links, link) }
	return world
}

func TestOutsideLinksChangeNothingOnTheStatementModel(t *testing.T) {
	t.Parallel()
	outsideLinksChangeNothing(t, grantedItemModelWorld)
}

func TestAnIdWithARowOutsideTheArmIsNotCounted(t *testing.T) {
	t.Parallel()
	anIdWithARowOutsideTheArmIsNotCounted(t, grantedItemModelWorld)
}

func TestALowerTierLinkNeverGrants(t *testing.T) {
	t.Parallel()
	aLowerTierLinkNeverGrants(t, grantedItemModelWorld)
}

func TestAProjectOwnedItemWithNoNativeLinkStaysDenied(t *testing.T) {
	t.Parallel()
	aProjectOwnedItemWithNoNativeLinkStaysDenied(t, grantedItemModelWorld)
}

func TestANativeLinkBesideAnOutsideOneAdmits(t *testing.T) {
	t.Parallel()
	aNativeLinkBesideAnOutsideOneAdmits(t, grantedItemModelWorld)
}
