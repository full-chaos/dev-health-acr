package graphrank

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12505, ruling 3 (from the CHAOS-12523 rows): a work item committed beside
// one named repository says whether a pull request link joins the two, from the
// keyed link read with the repository as the scope, or that the link could not be
// read. It never reads a failed read as "no link", and a lower tier names itself.
func TestAWorkItemBesideARepositorySaysWhetherALinkJoinsThem(t *testing.T) {
	t.Parallel()
	id := besideWorkItem(t, "linear", "CHAOS-71")
	row := besideRow{contextfabric.SubjectWorkItem, "CHAOS-71", id, "", scopeAnchorRepo}
	nodes := []CandidateNode{repoLessItem(id, "Split the job", scopeAnchorRepo)}
	type read struct {
		scopes [][]string
		ids    [][]string
	}
	keyed := func(seen *read, tiers map[string]string, complete bool, err error) func(*ResolveDeps) {
		return func(deps *ResolveDeps) {
			repositorySlugs(deps, scopeAnchorRepo)
			deps.LinkScopedWorkItemsOf = func(_ context.Context, scope contextfabric.RequestedScope, ids []string) (map[string]string, bool, error) {
				seen.scopes = append(seen.scopes, scope.RepositorySlugs)
				seen.ids = append(seen.ids, ids)
				return tiers, complete, err
			}
		}
	}
	for _, c := range []struct {
		name     string
		tiers    map[string]string
		complete bool
		err      error
		none     bool
		want     string
	}{
		{"a native link", map[string]string{id: "native"}, true, nil, false, contractsv1.ContextFabricNamedRepositoryNativeLinkLimitation},
		{"an explicit text link only", map[string]string{id: "explicit_text"}, true, nil, false, contractsv1.ContextFabricNamedRepositoryTextLinkLimitation},
		{"a heuristic link only", map[string]string{id: "heuristic"}, true, nil, false, contractsv1.ContextFabricNamedRepositoryHeuristicLinkLimitation},
		{"no link (CHAOS-73 beside acme/r00)", map[string]string{}, true, nil, false, contractsv1.ContextFabricNamedRepositoryNoLinkLimitation},
		{"a failed read", nil, false, errors.New("graph down"), false, contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation},
		{"a cut read", map[string]string{id: "native"}, false, nil, false, contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation},
		{"no read wired", nil, false, nil, true, contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation},
	} {
		for caller, principal := range besideCallers(t) {
			t.Run(c.name+", "+caller, func(t *testing.T) {
				t.Parallel()
				var seen read
				world := besideWorld{principal: principal, question: "Why is CHAOS-71 blocked in " + scopeAnchorRepo + "?", frame: statedFrame(contextfabric.SubjectWorkItem), census: &besideCensus{rows: []besideRow{row}}, nodes: nodes}
				if !c.none {
					world.link = keyed(&seen, c.tiers, c.complete, c.err)
				}
				turn := runBeside(t, world)
				if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, id) || !slices.Contains(got, scopeAnchorRepoID) {
					t.Fatalf("committed %v, want the item beside the repository", got)
				}
				if turn.resolution.NamedRepositoryLinkLimitation != c.want {
					t.Fatalf("link sentence %q, want %q", turn.resolution.NamedRepositoryLinkLimitation, c.want)
				}
				if c.none {
					return
				}
				if len(seen.ids) != 1 || !slices.Equal(seen.ids[0], []string{id}) || !slices.Equal(seen.scopes[0], []string{scopeAnchorRepo}) {
					t.Fatalf("keyed read asked %v under %v, want once, for the item under the named repository", seen.ids, seen.scopes)
				}
			})
		}
	}
}

// repositorySlugs makes the named repository's node list slugs, as a real
// repository node lists its own one; the shared fixture lists two.
func repositorySlugs(deps *ResolveDeps, slugs ...string) {
	inner := deps.ExactHint
	deps.ExactHint = func(ctx context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
		node, ok, err := inner(ctx, subject)
		if err == nil && ok && subject.Kind == contextfabric.SubjectRepository {
			attributes := maps.Clone(node.Attributes)
			attributes[authorizationRepositoriesAttr] = slices.Clone(slugs)
			node.Attributes = attributes
		}
		return node, ok, err
	}
}

// A repository node that lists another slug could admit a link into that one and
// say it of the named repository (codex r1 on #1929): it is not read, and the
// answer says the link could not be read.
func TestARepositoryThatListsTwoSlugsIsNotRead(t *testing.T) {
	t.Parallel()
	id := besideWorkItem(t, "linear", "CHAOS-71")
	var asked int
	turn := runBeside(t, besideWorld{
		principal: besideCallers(t)["unrestricted"], question: "Why is CHAOS-71 blocked in " + scopeAnchorRepo + "?", frame: statedFrame(contextfabric.SubjectWorkItem),
		census: &besideCensus{rows: []besideRow{{contextfabric.SubjectWorkItem, "CHAOS-71", id, "", scopeAnchorRepo}}},
		nodes:  []CandidateNode{repoLessItem(id, "Split the job", scopeAnchorRepo)},
		link: func(deps *ResolveDeps) {
			repositorySlugs(deps, scopeAnchorRepo, besideOtherRepo)
			deps.LinkScopedWorkItemsOf = func(context.Context, contextfabric.RequestedScope, []string) (map[string]string, bool, error) {
				asked++
				return map[string]string{id: "native"}, true, nil
			}
		},
	})
	if turn.resolution.NamedRepositoryLinkLimitation != contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation || asked != 0 {
		t.Fatalf("link sentence %q, reads %d, want the unread sentence and no read", turn.resolution.NamedRepositoryLinkLimitation, asked)
	}
}

// A handle with a repository key is answered inside the repository by its own
// census: it owes no link sentence, and the read is not asked.
func TestAHandleInsideTheRepositoryOwesNoLinkSentence(t *testing.T) {
	t.Parallel()
	var asked int
	turn := runBeside(t, besideWorld{
		principal: besideCallers(t)["unrestricted"], question: besideQuestion("pull request 747"), frame: statedFrame(contextfabric.SubjectPullRequest),
		census: &besideCensus{rows: besideRows()},
		link: func(deps *ResolveDeps) {
			deps.LinkScopedWorkItemsOf = func(context.Context, contextfabric.RequestedScope, []string) (map[string]string, bool, error) {
				asked++
				return nil, true, nil
			}
		},
	})
	if got := besideCommittedIDs(turn.resolution); !slices.Contains(got, besidePRInRepo) {
		t.Fatalf("committed %v, want the pull request", got)
	}
	if turn.resolution.NamedRepositoryLinkLimitation != "" || asked != 0 {
		t.Fatalf("link sentence %q, reads %d: a pull request inside the repository owes none", turn.resolution.NamedRepositoryLinkLimitation, asked)
	}
}
