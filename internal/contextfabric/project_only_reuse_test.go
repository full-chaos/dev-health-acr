package contextfabric

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12298 (c2) lifts c3's fail-closed reuse and stored-result refusals
// (CHAOS-12299 r1 F4): the class-tagged reuse key and the p: digest now tell
// two project grants apart. The codex r1 F4 case: {P-1, P-2} and {P-1}.

var (
	wideProjectGrant   = storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-1", "project.v2:github:P-2"}}
	narrowProjectGrant = storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-1"}}
)

// The stored-result gate: a project-only reader is admitted only to a result
// whose p: digest equals its own; a restricted reader never matches a p:
// digest; unrestricted and universal readers keep the existing gate (R2-4).
func TestTheStoredResultGateSeparatesProjectGrants(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project.v2:github:P-1", Label: "P-1"}
	wideResult := StoredInvestigationResult{Result: gateResult(project), GrantDigest: StoredResultGrantDigest(wideProjectGrant)}
	reordered := storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-2", "project.v2:github:P-1"}}
	for _, tc := range []struct {
		name   string
		reader storage.Principal
		stored StoredInvestigationResult
		want   StoredResultDecision
		reason StoredResultAuthorizationReason
	}{
		{"identical project grant", wideProjectGrant, wideResult, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted},
		{"identical grant, other order", reordered, wideResult, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted},
		{"narrower project grant", narrowProjectGrant, wideResult, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"wider project grant", wideProjectGrant, StoredInvestigationResult{Result: gateResult(project), GrantDigest: StoredResultGrantDigest(narrowProjectGrant)}, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"project reader, open result", wideProjectGrant, StoredInvestigationResult{Result: gateResult(project), GrantDigest: "open"}, StoredResultDenied, StoredResultReasonGrantMismatch},
		{"project reader, unrecorded grant", wideProjectGrant, StoredInvestigationResult{Result: gateResult(project)}, StoredResultDenied, StoredResultReasonGrantUnrecorded},
		{"repository reader, p: result", storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"acme/api"}}, wideResult, StoredResultDenied, StoredResultReasonGrantMismatch},
		// R2-4: the existing gate admits an unrestricted or universal reader to
		// any recorded digest, p: included.
		{"universal reader, p: result", storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"*"}}, wideResult, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted},
		{"unrestricted reader, p: result", storage.Principal{OrgID: "org_reuse"}, wideResult, StoredResultAdmitted, StoredResultReasonUnrestrictedPrincipal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}
			decision := NewStoredResultGate(graph).Authorize(context.Background(), tc.reader, tc.stored, StoredResultSurfacePriorResult)
			if decision.Decision != tc.want || decision.Reason != tc.reason {
				t.Fatalf("decision %s/%s, want %s/%s", decision.Decision, decision.Reason, tc.want, tc.reason)
			}
			if tc.want == StoredResultDenied && graph.calls != 0 {
				t.Error("the gate read the graph for a result refused on its grant")
			}
		})
	}
}

// singleResultStore returns one stored row to every Get.
type singleResultStore struct {
	resultStoreStub
	stored StoredInvestigationResult
}

func (s *singleResultStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return s.stored, nil
}

// R2-4: a p: result served to an unrestricted reader keeps its project-only
// limitations and coverage exactly as stored.
func TestAProjectResultServedToAnUnrestrictedReaderKeepsItsLimitationsAndCoverage(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project.v2:github:P-1", Label: "P-1"}
	result := gateResult(project)
	result.ResultID = "result_project_only"
	result.Limitations = append(result.Limitations, "project-only scope: teams and pull requests are not visible")
	result.Coverage.Partial = true
	result.Coverage.DegradedReasons = append(result.Coverage.DegradedReasons, "project_only_withheld")
	store := authorizedResultStore{
		InvestigationResultStore: &singleResultStore{stored: StoredInvestigationResult{Result: result, GrantDigest: StoredResultGrantDigest(wideProjectGrant)}},
		gate:                     NewStoredResultGate(&gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}),
	}
	got, err := store.Get(context.Background(), storage.Principal{OrgID: "org_reuse"}, result.ResultID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.Result.Limitations, result.Limitations) || !reflect.DeepEqual(got.Result.Coverage, result.Coverage) {
		t.Fatalf("served limitations/coverage changed:\n got %v / %+v\nwant %v / %+v", got.Result.Limitations, got.Result.Coverage, result.Limitations, result.Coverage)
	}
}

// keyedReuseGate holds one saved answer under the exact time-axis key its
// saver's Save wrote, and returns it only to a lookup with that key.
type keyedReuseGate struct {
	timeAxisKey string
	stored      StoredInvestigationResult
	lookups     []string
}

func (g *keyedReuseGate) FindReusable(_ context.Context, _ storage.Principal, key ReuseKey) (StoredInvestigationResult, bool, ReuseMissReason, error) {
	g.lookups = append(g.lookups, key.TimeAxisKey)
	if key.TimeAxisKey != g.timeAxisKey {
		return StoredInvestigationResult{}, false, ReuseMissNoCandidate, nil
	}
	return g.stored, true, "", nil
}

// The reuse side: an answer saved under one project grant is reused by the
// same grant only. No unrestricted, universal or repository investigation
// reuses it (R2-4), and a narrower or wider project grant does not.
func TestAProjectAnswerIsReusedByItsOwnGrantOnly(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	request := validInvestigationRequest()
	base := RequestScopeTimeAxisKey(request, composeTimeAxisKey(TimeAxisKeyFor(TimeContext{Axis: TemporalCurrent}), ""))
	for _, tc := range []struct {
		name   string
		saver  storage.Principal
		reader storage.Principal
		want   bool
	}{
		{"same project grant", wideProjectGrant, wideProjectGrant, true},
		{"same grant, other order", wideProjectGrant, storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-2", "project.v2:github:P-1"}}, true},
		{"narrower grant", wideProjectGrant, narrowProjectGrant, false},
		{"wider grant", narrowProjectGrant, wideProjectGrant, false},
		{"unrestricted reader", wideProjectGrant, storage.Principal{OrgID: "org_reuse"}, false},
		{"universal reader", wideProjectGrant, storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"*"}}, false},
		{"repository reader", wideProjectGrant, storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"acme/api"}}, false},
		{"project reader of an unrestricted answer", storage.Principal{OrgID: "org_reuse"}, wideProjectGrant, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &keyedReuseGate{
				timeAxisKey: GrantScopedTimeAxisKey(tc.saver, base),
				stored:      StoredInvestigationResult{Result: candidate, GrantDigest: StoredResultGrantDigest(tc.saver), SemanticStateRead: SemanticStateReadAbsent},
			}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}, bases: provenCommitBases(project)},
				Results:   &resultStoreStub{},
				ReuseGate: gate,
			})
			_, hit, _, _ := engine.tryReuse(context.Background(), tc.reader, request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
			if hit != tc.want {
				t.Fatalf("hit = %v, want %v (lookups %v, saved under %q)", hit, tc.want, gate.lookups, gate.timeAxisKey)
			}
			if len(gate.lookups) == 0 {
				t.Fatal("no reuse lookup was made")
			}
		})
	}
}

// capturingSaveStore records what the engine's only Save persisted.
type capturingSaveStore struct {
	resultStoreStub
	timeAxisKey string
}

func (s *capturingSaveStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, snapshot SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, identity ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, parent string, semantic SemanticStateWrite) error {
	s.timeAxisKey = timeAxisKey
	return s.resultStoreStub.Save(ctx, principal, result, snapshot, epoch, timeAxisKey, identity, prompts, authorities, graphEpoch, parent, semantic)
}

// The save side: a project-only result is stored as reusable, under its
// class-tagged key; other classes are unchanged.
func TestAProjectResultIsStoredAsReusableUnderItsClassTaggedKey(t *testing.T) {
	t.Parallel()
	watermark := SourceWatermarkSnapshot{"source": "w1"}
	one := int64(1)
	var epoch RebuildEpoch = &one
	for _, tc := range []struct {
		principal storage.Principal
		tag       string
	}{
		{principal: wideProjectGrant, tag: "+p:"},
		{principal: storage.Principal{OrgID: "org_reuse", RepositoryScopes: []string{"acme/api"}}, tag: "+g:"},
		{principal: reusePrincipal(), tag: ""},
	} {
		store := &capturingSaveStore{}
		engine := mustReuseTestEngine(t, EngineDependencies{Graph: graphReaderStub{}, Results: store})
		if err := engine.saveResult(context.Background(), tc.principal, BudgetAssertDecisive, InvestigationResult{ResultID: "result_reuse"}, watermark, epoch, "current", 0, "", absentSemanticState(SemanticStateAbsenceContinuationRefused)); err != nil {
			t.Fatalf("saveResult: %v", err)
		}
		if store.savedSnapshot == nil || store.savedEpoch == nil {
			t.Errorf("%v/%v: not stored as reusable", tc.principal.RepositoryScopes, tc.principal.ProjectScopes)
		}
		if tc.tag == "" && store.timeAxisKey != "current" || tc.tag != "" && !strings.HasPrefix(store.timeAxisKey, "current"+tc.tag) {
			t.Errorf("%v/%v: saved key %q, want tag %q", tc.principal.RepositoryScopes, tc.principal.ProjectScopes, store.timeAxisKey, tc.tag)
		}
	}
}
