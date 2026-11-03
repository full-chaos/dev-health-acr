package graphrank

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12706: under a requested repository scope, a work item hint is in the
// scope when it is linked to a pull request of a named repository (the entity
// tree), as the census tests its satisfiers: AuthorizedThroughLink and the
// keyed link read. The issue's own repository column never admits it. Before
// the change, the hint loop tested the issue's own repository against the
// scope, so a receipt for a linked item was dropped and the item committed only
// on the census's statistical basis, which client synthesis retracts.

const hintScopeSlug = "acme/svc"

// hintScopeWorld is one receipt turn: one work item of a provider, its own
// repository, the link read's answer for it, and the caller.
type hintScopeWorld struct {
	provider  string
	ownRepo   string   // "" for a provider whose items have no repository (linear, jira)
	linkAttr  []string // the read's link attribute (a native link into a granted repository)
	scope     []string
	principal storage.Principal
	source    string
	// link is the keyed link read's answer for the item: a tier, "" for no
	// link; readErr/cut/unwired make the read fail.
	link    string
	readErr error
	cut     bool
	unwired bool
}

type hintScopeTurn struct {
	committed bool
	basis     contextfabric.CommitBasis
	gate      string
	asked     [][]string
	scopes    [][]string
	linked    map[string]string
	scoped    bool
	// authzDropped is the hint loop's authorization-drop count: a node the
	// link read leaves out is outside the scope, not unauthorized.
	authzDropped int
}

func hintScopeItem(provider string) string {
	return fmt.Sprintf("work_item.v2:%s:CHAOS-91", provider)
}

func runHintScope(t *testing.T, w hintScopeWorld) hintScopeTurn {
	t.Helper()
	id := hintScopeItem(w.provider)
	repos := []string{contractsv1.ContextFabricNoRepositorySentinel}
	if w.ownRepo != "" {
		repos = []string{w.ownRepo}
	}
	node := candidateNode(contextfabric.SubjectWorkItem, id, "CHAOS-91", 0.5, repos)
	if len(w.linkAttr) > 0 {
		node.Attributes[AuthorizationLinkRepositoriesAttr] = w.linkAttr
	}
	backend := &fakeGraphBackend{
		exactHints:    map[string]CandidateNode{SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id}): node},
		searchResults: map[string][]CandidateNode{},
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	var turn hintScopeTurn
	if !w.unwired {
		deps.LinkScopedWorkItemsOf = func(_ context.Context, scope contextfabric.RequestedScope, ids []string) (map[string]string, bool, error) {
			turn.asked = append(turn.asked, slices.Clone(ids))
			turn.scopes = append(turn.scopes, slices.Clone(scope.RepositorySlugs))
			if w.readErr != nil {
				return nil, false, w.readErr
			}
			tiers := map[string]string{}
			if w.link != "" && slices.Contains(ids, id) {
				tiers[id] = w.link
			}
			return tiers, !w.cut, nil
		}
	}
	request := testRequest()
	request.Question = "What is its state now?"
	request.RequestedScope.RepositorySlugs = w.scope
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectWorkItem, ID: id, Source: w.source}}
	ctx := contextfabric.WithWorkItemCensusRepositoryScopeRecorder(context.Background())
	resolution, _, bases, digests, err := ResolveSubjectsWithCommitBasis(ctx, w.principal, request, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id}
	for _, s := range resolution.Committed {
		if s.Kind == contextfabric.SubjectWorkItem && s.CanonicalID == id {
			turn.committed = true
		}
	}
	turn.basis = bases[SubjectKey(subject)]
	turn.gate = digests[SubjectKey(subject)].CommitGate
	turn.linked = contextfabric.WorkItemCensusLinkedSatisfiers(ctx)
	turn.scoped = contextfabric.WorkItemCensusRepositoryScopeRecorded(ctx)
	for _, n := range backend.subjectCandidatesAuthzDropped {
		turn.authzDropped += n
	}
	return turn
}

// hintScopeProviders: Linear and Jira items have no repository; GitHub and
// GitLab issues have their own, here not the scoped one.
var hintScopeProviders = []struct{ provider, ownRepo string }{
	{"linear", ""}, {"jira", ""}, {"github", "acme/own"}, {"gitlab", "acme/own-gl"},
}

func TestAReceiptForALinkedWorkItemCommitsUnderARepositoryScope(t *testing.T) {
	t.Parallel()
	unrestricted := storage.Principal{OrgID: "org_1"}
	for _, p := range hintScopeProviders {
		for _, tier := range []string{
			contractsv1.ContextFabricWorkItemRepositoryTierNative,
			contractsv1.ContextFabricWorkItemRepositoryTierExplicitText,
			contractsv1.ContextFabricWorkItemRepositoryTierHeuristic,
		} {
			for _, source := range []string{string(hintsource.PriorSubjectReceipt), string(hintsource.ConfirmedCandidateReceipt), ""} {
				t.Run(p.provider+"/"+tier+"/"+source, func(t *testing.T) {
					t.Parallel()
					turn := runHintScope(t, hintScopeWorld{provider: p.provider, ownRepo: p.ownRepo, scope: []string{hintScopeSlug}, principal: unrestricted, source: source, link: tier})
					if !turn.committed || turn.basis != contextfabric.CommitBasisCallerCanonicalID {
						t.Fatalf("committed %t basis %q gate %q, want the hinted item committed on the caller's canonical id", turn.committed, turn.basis, turn.gate)
					}
					id := hintScopeItem(p.provider)
					if len(turn.asked) != 1 || !slices.Equal(turn.asked[0], []string{id}) || !slices.Equal(turn.scopes[0], []string{hintScopeSlug}) {
						t.Fatalf("link read asked %v under %v, want once, for the item under the request's scope", turn.asked, turn.scopes)
					}
					if turn.linked[id] != tier || !turn.scoped {
						t.Fatalf("recorded link %v scoped %t, want the item with tier %s and the scope recorded, as the census records its satisfiers", turn.linked, turn.scoped, tier)
					}
				})
			}
		}
	}
}

func TestAWorkItemHintThatTheLinkReadDoesNotAdmitIsNotCommitted(t *testing.T) {
	t.Parallel()
	unrestricted := storage.Principal{OrgID: "org_1"}
	for _, p := range hintScopeProviders {
		for name, w := range map[string]hintScopeWorld{
			"no link":         {},
			"a failed read":   {readErr: errors.New("graph down")},
			"a cut read":      {link: contractsv1.ContextFabricWorkItemRepositoryTierNative, cut: true},
			"no read wired":   {unwired: true},
			"a caller's hint": {source: ""},
		} {
			t.Run(p.provider+"/"+name, func(t *testing.T) {
				t.Parallel()
				w.provider, w.ownRepo, w.scope, w.principal = p.provider, p.ownRepo, []string{hintScopeSlug}, unrestricted
				if w.source == "" && name != "a caller's hint" {
					w.source = string(hintsource.PriorSubjectReceipt)
				}
				turn := runHintScope(t, w)
				if turn.committed {
					t.Fatalf("committed on basis %q gate %q: an item the link read does not admit is outside the scope", turn.basis, turn.gate)
				}
				if _, recorded := turn.linked[hintScopeItem(p.provider)]; recorded {
					t.Fatalf("recorded %v for an item the read did not admit", turn.linked)
				}
				if turn.authzDropped != 0 {
					t.Fatalf("authorization drops %d: an item outside the scope is not an authorization drop", turn.authzDropped)
				}
			})
		}
	}
}

// The behavior change the strict rule makes: a GitHub or GitLab issue tied to
// the scoped repository only by its own repository column is not admitted by a
// hint. Before the change it was, while the census refused it.
func TestAnIssueOfTheScopedRepositoryWithNoLinkIsNotAdmittedByAHint(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			turn := runHintScope(t, hintScopeWorld{provider: provider, ownRepo: hintScopeSlug, scope: []string{hintScopeSlug}, principal: storage.Principal{OrgID: "org_1"}, source: string(hintsource.PriorSubjectReceipt)})
			if turn.committed {
				t.Fatalf("committed on basis %q: the issue's own repository column admitted it", turn.basis)
			}
			if len(turn.asked) != 1 {
				t.Fatalf("link read asked %v, want once: the link decides", turn.asked)
			}
		})
	}
}

// A restricted caller's hint still meets its grants on the issue: a
// repository-less item is granted by a native link into a granted repository
// (the read adds the link attribute), and an issue with a repository of its own
// by that repository alone, which a link never widens (ruling 4 of
// CHAOS-12456). A subject outside the grant changes nothing: nothing is
// committed or recorded, and the link read is not asked.
func TestAScopedWorkItemHintStillMeetsTheCallersGrants(t *testing.T) {
	t.Parallel()
	for _, p := range hintScopeProviders {
		t.Run(p.provider, func(t *testing.T) {
			t.Parallel()
			inside := restrictedCaller(t, hintScopeSlug)
			native := contractsv1.ContextFabricWorkItemRepositoryTierNative
			grant := inside
			if p.ownRepo != "" {
				// The scoped repository alone does not grant an issue of
				// another repository, whatever its link.
				ownRepoUngranted := runHintScope(t, hintScopeWorld{provider: p.provider, ownRepo: p.ownRepo, scope: []string{hintScopeSlug}, principal: inside, source: string(hintsource.PriorSubjectReceipt), link: native})
				if ownRepoUngranted.committed || len(ownRepoUngranted.asked) != 0 {
					t.Fatalf("issue of an ungranted repository: committed %t, link reads %v; a link never widens the grant", ownRepoUngranted.committed, ownRepoUngranted.asked)
				}
				grant = restrictedCaller(t, p.ownRepo, hintScopeSlug)
			}
			granted := runHintScope(t, hintScopeWorld{provider: p.provider, ownRepo: p.ownRepo, linkAttr: []string{hintScopeSlug}, scope: []string{hintScopeSlug}, principal: grant, source: string(hintsource.PriorSubjectReceipt), link: native})
			if !granted.committed || granted.basis != contextfabric.CommitBasisCallerCanonicalID {
				t.Fatalf("restricted inside the grant: committed %t basis %q, want the item committed on the caller's canonical id", granted.committed, granted.basis)
			}
			text := runHintScope(t, hintScopeWorld{provider: p.provider, ownRepo: p.ownRepo, scope: []string{hintScopeSlug}, principal: inside, source: string(hintsource.PriorSubjectReceipt), link: contractsv1.ContextFabricWorkItemRepositoryTierExplicitText})
			if text.committed || len(text.asked) != 0 {
				t.Fatalf("restricted, text link only: committed %t, link reads %v; only a native link grants, so the read is not asked", text.committed, text.asked)
			}
			outside := restrictedCaller(t, "acme/elsewhere")
			denied := runHintScope(t, hintScopeWorld{provider: p.provider, ownRepo: p.ownRepo, linkAttr: []string{hintScopeSlug}, scope: []string{hintScopeSlug}, principal: outside, source: string(hintsource.PriorSubjectReceipt), link: native})
			if denied.committed || len(denied.asked) != 0 || len(denied.linked) != 0 || denied.scoped || denied.authzDropped != 1 {
				t.Fatalf("outside the grant: committed %t, link reads %v, recorded %v scoped %t, authorization drops %d; want nothing but the one drop", denied.committed, denied.asked, denied.linked, denied.scoped, denied.authzDropped)
			}
		})
	}
}

// Without a repository scope nothing changes: the hint commits on its own node,
// and the link read is not asked.
func TestAnUnscopedWorkItemHintIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, p := range hintScopeProviders {
		t.Run(p.provider, func(t *testing.T) {
			t.Parallel()
			turn := runHintScope(t, hintScopeWorld{provider: p.provider, ownRepo: p.ownRepo, principal: storage.Principal{OrgID: "org_1"}, source: string(hintsource.PriorSubjectReceipt)})
			if !turn.committed || turn.basis != contextfabric.CommitBasisCallerCanonicalID || turn.gate != contractsv1.CommitGatePreCommittedExactHint {
				t.Fatalf("committed %t basis %q gate %q, want the receipt committed as before", turn.committed, turn.basis, turn.gate)
			}
			if len(turn.asked) != 0 || len(turn.linked) != 0 || turn.scoped {
				t.Fatalf("link reads %v recorded %v scoped %t, want none without a scope", turn.asked, turn.linked, turn.scoped)
			}
		})
	}
}

// The answer reuse recheck keeps the issue's own node test (lead ruling 2): a
// link admission is re-derived by a fresh turn's census, never by the recheck.
func TestTheReuseRecheckIsNotAdmittedThroughTheLink(t *testing.T) {
	t.Parallel()
	turn := runHintScope(t, hintScopeWorld{provider: "linear", scope: []string{hintScopeSlug}, principal: storage.Principal{OrgID: "org_1"}, source: string(hintsource.AnswerReuseAuthorizationRecheck), link: contractsv1.ContextFabricWorkItemRepositoryTierNative})
	if turn.committed || len(turn.asked) != 0 {
		t.Fatalf("committed %t, link reads %v: the recheck must not admit a work item through the link", turn.committed, turn.asked)
	}
}

// Codex r1 on #1982 (executed): hints are independent, so a link read that
// fails for one hint does not refuse another. The first hint's read errors;
// the second hint's read admits it, and it commits.
func TestAFailedLinkReadForOneHintDoesNotRefuseAnother(t *testing.T) {
	t.Parallel()
	failing, linked := hintScopeItem("linear"), hintScopeItem("jira")
	hints := map[string]CandidateNode{}
	for _, id := range []string{failing, linked} {
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id})] = candidateNode(contextfabric.SubjectWorkItem, id, "CHAOS-91", 0.5, []string{contractsv1.ContextFabricNoRepositorySentinel})
	}
	deps := (&fakeGraphBackend{exactHints: hints, searchResults: map[string][]CandidateNode{}}).deps()
	var asked [][]string
	deps.LinkScopedWorkItemsOf = func(_ context.Context, _ contextfabric.RequestedScope, ids []string) (map[string]string, bool, error) {
		asked = append(asked, slices.Clone(ids))
		if slices.Contains(ids, failing) {
			return nil, false, errors.New("graph down")
		}
		return map[string]string{linked: contractsv1.ContextFabricWorkItemRepositoryTierNative}, true, nil
	}
	request := testRequest()
	request.Question = "What is their state now?"
	request.RequestedScope.RepositorySlugs = []string{hintScopeSlug}
	for _, id := range []string{failing, linked} {
		request.RequestedScope.SubjectHints = append(request.RequestedScope.SubjectHints, contextfabric.SubjectHint{Kind: contextfabric.SubjectWorkItem, ID: id, Source: string(hintsource.PriorSubjectReceipt)})
	}
	resolution, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var committed []string
	for _, s := range resolution.Committed {
		committed = append(committed, s.CanonicalID)
	}
	if !slices.Equal(committed, []string{linked}) || bases[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: linked})] != contextfabric.CommitBasisCallerCanonicalID {
		t.Fatalf("committed %v (reads %v), want only the hint whose read admitted it, on the caller's canonical id", committed, asked)
	}
	if len(asked) != 2 {
		t.Fatalf("link reads %v, want one per hint: a failed read is that hint's alone", asked)
	}
}
