//go:build sizeproof

package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12523, on real stores: seeded ClickHouse rows, projected by the real
// producers into a real FalkorDB, read by the real Engine, resolver and census, with
// the identity universe production wires, so the census is anchored on the repository.
// The stub synthesizer is the proof of what reaches synthesis: no model is called.

// besideInterpreter hands the turn its frame, as a validated interpretation does.
type besideInterpreter struct {
	interpretation contextfabric.InterpretedQuestion
	frame          *contextfabric.QuestionFrame
}

func (b besideInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return b.interpretation, contextfabric.QuestionFamilyOutcome{Frame: b.frame}, nil
}

// besideFacts records the subjects facts were read for.
type besideFacts struct {
	emptyFactReader
	mu       sync.Mutex
	subjects []string
}

func (f *besideFacts) ReadFacts(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	f.mu.Lock()
	for _, subject := range request.Subjects {
		f.subjects = append(f.subjects, string(subject.Kind))
	}
	f.mu.Unlock()
	return f.emptyFactReader.ReadFacts(ctx, principal, request)
}

type besideLiveTurn struct {
	result      contextfabric.InvestigationResult
	synthesized bool
	committed   []string
	factKinds   []string
	// link is the named-repository link sentence the resolution handed to
	// synthesis (CHAOS-12505 ruling 3).
	link string
}

func (b besideLiveTurn) hasCommitted(kind contextfabric.SubjectKind) bool {
	return slices.Contains(b.committed, string(kind))
}

func TestANamedSubjectBesideARepositoryOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := scopedClickHouse(t, ctx)
	adapter := scopedFalkor(t, ctx, query, &listingStageTracer{})
	adapter.config.HandleGrammarChecker = func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
		if !graphrank.ValidateHandleGrammar(kind, patternID, value) {
			return "", false
		}
		return graphrank.HandleSourceColumn(kind, patternID)
	}
	adapter.config.IdentityUniverse = func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return devhealthsource.IdentityUniverse(ctx, query, orgID)
	}
	org := scopedUUID("org-12523")
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-20 * 24 * time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	repo := map[string]string{}
	addRepo := func(slug, provider string) {
		repo[slug] = scopedUUID("beside repo " + slug)
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo[slug], org, slug, provider, now)
	}
	addPR := func(slug string, number uint32, title string) {
		exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`, repo[slug], org, number, title, "merged", created, now)
	}
	addRun := func(slug, id string) {
		exec("ci run", `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`, repo[slug], id, "success", created, now, org)
	}
	const github, gitlab = "acme/r00", "acme/r02"
	addRepo(github, "github")
	addRepo("acme/r01", "github")
	addRepo(gitlab, "gitlab")
	addRepo("other/secret", "github")
	addRepo("other/hidden", "gitlab")
	inside := []string{github, "acme/r01", gitlab}
	for _, slug := range inside {
		addPR(slug, 12, "Refactor the widget cache")
		addRun(slug, "4242")
	}
	addPR(github, 77, "Lone change here")
	addPR(gitlab, 75, "Lone change on the other provider")
	addRun(github, "4343")
	addRun(gitlab, "4345")
	addPR("acme/r01", 78, "Lone change elsewhere")
	addPR("other/secret", 555, "Hidden change")
	addPR("other/hidden", 556, "Hidden change on the other provider")
	addPR(github, 80, "Change for the linked items")
	addPR("acme/r01", 81, "Change elsewhere for a linked item")
	addPR("other/secret", 82, "Outside change for a linked item")
	for i, item := range []struct {
		id, provider, itemType, slug string
		number                       uint32
	}{
		{"linear:CHAOS-70", "linear", "issue", github, 80},
		{"jira:CHAOS-70", "jira", "story", github, 80},
		{"linear:CHAOS-71", "linear", "issue", github, 80},
		{"jira:CHAOS-74", "jira", "story", github, 80},
		{"jira:CHAOS-73", "jira", "story", "acme/r01", 81},
		{"linear:CHAOS-72", "linear", "issue", "other/secret", 82},
	} {
		exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, scopedZeroRepo, org, "Split the job", item.itemType, "open", item.provider, "", created, now, now)
		exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repo[item.slug], item.id, item.number, float32(0.9), "native", "", now.Add(time.Duration(i)*time.Millisecond), org)
	}
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	scopedDrain(t, ctx, main, devhealthsource.SourceName, org, adapter)

	restricted := userprincipal.Narrowed(t, org, []string{"acme/*"}, inside)
	unrestricted := storage.Principal{OrgID: org, Subject: "u", CredentialID: "c"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, unrestricted)
	if err != nil {
		t.Fatal(err)
	}
	store := listingStore{Store: memoryinvestigation.NewStore(), epoch: binding.Epoch}
	results := 0
	turn := func(t *testing.T, principal storage.Principal, question, slug string, frame *contextfabric.QuestionFrame, edit func(*contextfabric.InvestigationRequest)) besideLiveTurn {
		t.Helper()
		var got besideLiveTurn
		facts := &besideFacts{}
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: besideInterpreter{frame: frame, interpretation: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{slug},
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
			}},
			Graph: adapter, Facts: facts,
			Synthesizer: synthesizeFunc(func(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
				got.synthesized = true
				got.link = input.Graph.Resolution.NamedRepositoryLinkLimitation
				for _, subject := range input.Graph.Resolution.Committed {
					got.committed = append(got.committed, string(subject.Kind))
				}
				return contextfabric.InvestigationResult{}, errors.New("no model in this test")
			}),
			Results: store, Requirements: productionRequirementDeriver{},
		}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string {
			results++
			return fmt.Sprintf("result_12523%04d", results)
		}})
		if err != nil {
			t.Fatal(err)
		}
		request := contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12523001", Question: question,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		}
		if edit != nil {
			edit(&request)
		}
		runCtx, _ := contextfabric.WithSynthesisInputCollector(ctx)
		result, err := engine.Investigate(runCtx, principal, request)
		if err != nil && !got.synthesized {
			t.Fatalf("Investigate: %v", err)
		}
		got.result = result
		facts.mu.Lock()
		got.factKinds = slices.Clone(facts.subjects)
		facts.mu.Unlock()
		return got
	}
	stated := func(kind contextfabric.SubjectKind, slug string) *contextfabric.QuestionFrame {
		return &contextfabric.QuestionFrame{
			Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
			SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{slug}, ExpectedKind: &kind}},
			Temporal:          contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
		}
	}
	unstated := func(slug string) *contextfabric.QuestionFrame {
		frame := stated(contextfabric.SubjectPullRequest, slug)
		frame.SubjectExpression.Named.ExpectedKind = nil
		return frame
	}
	callers := map[string]storage.Principal{"restricted": restricted, "unrestricted": unrestricted}
	hint := func(slug string) func(*contextfabric.InvestigationRequest) {
		return func(r *contextfabric.InvestigationRequest) {
			r.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectRepository, ID: "repository:" + repo[slug], Label: slug, Source: "workbench"}}
		}
	}
	// absent is the terminal of a stated 0: nothing reaches synthesis, the repository
	// is offered, and no sentence reads wider than the census did.
	absent := func(t *testing.T, got besideLiveTurn, sentence string) {
		t.Helper()
		if got.synthesized {
			t.Fatalf("synthesis ran on %v", got.committed)
		}
		if got.result.Status != contextfabric.InvestigationClarificationRequired {
			t.Fatalf("status = %q", got.result.Status)
		}
		if !slices.Contains(got.result.Limitations, sentence) {
			t.Fatalf("limitations %q lack %q", got.result.Limitations, sentence)
		}
		for _, limitation := range got.result.Limitations {
			if strings.HasPrefix(limitation, "One authorized subject") || strings.Contains(limitation, "more than one authorized subject") {
				t.Fatalf("limitations %q say something matched", got.result.Limitations)
			}
		}
		if !slices.ContainsFunc(got.result.SubjectResolution.Candidates, func(c contextfabric.SubjectCandidate) bool {
			return c.Subject.Kind == contextfabric.SubjectRepository
		}) {
			t.Fatalf("candidates %+v, want the repository offered", got.result.SubjectResolution.Candidates)
		}
	}
	both := func(t *testing.T, got besideLiveTurn, kind contextfabric.SubjectKind) {
		t.Helper()
		if !got.synthesized || !got.hasCommitted(contextfabric.SubjectRepository) || !got.hasCommitted(kind) {
			t.Fatalf("synthesis %t committed %v, want the repository and %s", got.synthesized, got.committed, kind)
		}
		if !slices.Contains(got.factKinds, string(kind)) {
			t.Fatalf("facts were read for %v, want %s", got.factKinds, kind)
		}
	}
	const scope = contractsv1.ContextFabricCensusAbsentInScopeLimitation
	pr := func(number, slug string) string {
		return "What is the state of pull request " + number + " in " + slug + "?"
	}
	run := func(id, slug string) string { return "What is the state of CI run " + id + " in " + slug + "?" }
	item := func(key, slug string) string { return "Why is " + key + " blocked in " + slug + "?" }
	for name, principal := range callers {
		t.Run(name, func(t *testing.T) {
			kindPR := stated(contextfabric.SubjectPullRequest, github)
			t.Run("a count of 1 commits beside the repository, on each provider", func(t *testing.T) {
				both(t, turn(t, principal, pr("77", github), github, kindPR, nil), contextfabric.SubjectPullRequest)
				both(t, turn(t, principal, pr("75", gitlab), gitlab, stated(contextfabric.SubjectPullRequest, gitlab), nil), contextfabric.SubjectPullRequest)
				both(t, turn(t, principal, run("4343", github), github, stated(contractsv1.ContextFabricSubjectCIRun, github), nil), contractsv1.ContextFabricSubjectCIRun)
				both(t, turn(t, principal, run("4345", gitlab), gitlab, stated(contractsv1.ContextFabricSubjectCIRun, gitlab), nil), contractsv1.ContextFabricSubjectCIRun)
				both(t, turn(t, principal, pr("12", github), github, kindPR, nil), contextfabric.SubjectPullRequest)
				both(t, turn(t, principal, pr("77", github), github, unstated(github), nil), contextfabric.SubjectPullRequest)
			})
			t.Run("a stated 0 takes the repository out of the answer", func(t *testing.T) {
				absent(t, turn(t, principal, pr("999", github), github, kindPR, nil), scope)
				absent(t, turn(t, principal, run("9999", github), github, stated(contractsv1.ContextFabricSubjectCIRun, github), nil), scope)
				// PR 78 is in acme/r01, and the caller can read it: the row that pins the
				// sentence to the repository.
				absent(t, turn(t, principal, pr("78", github), github, kindPR, nil), scope)
			})
			t.Run("the exact hint takes the same rule", func(t *testing.T) {
				both(t, turn(t, principal, pr("77", github), github, kindPR, hint(github)), contextfabric.SubjectPullRequest)
				both(t, turn(t, principal, pr("77", github), github, unstated(github), hint(github)), contextfabric.SubjectPullRequest)
				// The caller named this repository, and the exit offers nothing to redeem,
				// so there is nothing to ask: no_match, with the same sentence.
				gone := turn(t, principal, pr("999", github), github, kindPR, hint(github))
				if gone.synthesized || gone.result.Status != contextfabric.InvestigationNoMatch || !slices.Contains(gone.result.Limitations, scope) {
					t.Fatalf("stated: synthesis %t status %q limitations %q", gone.synthesized, gone.result.Status, gone.result.Limitations)
				}
				kept := turn(t, principal, pr("999", github), github, unstated(github), hint(github))
				if !kept.synthesized || !slices.Equal(kept.committed, []string{string(contextfabric.SubjectRepository)}) {
					t.Fatalf("unstated: synthesis %t committed %v", kept.synthesized, kept.committed)
				}
			})
			t.Run("a mention keeps the repository's answer", func(t *testing.T) {
				for _, frame := range []*contextfabric.QuestionFrame{unstated(github), nil, stated(contextfabric.SubjectRepository, github)} {
					got := turn(t, principal, "How is acme/r00 doing since pull request 999 merged?", github, frame, nil)
					if !got.synthesized || !slices.Equal(got.committed, []string{string(contextfabric.SubjectRepository)}) {
						t.Fatalf("synthesis %t committed %v", got.synthesized, got.committed)
					}
				}
				got := turn(t, principal, "What is the run 2026 plan for acme/r00?", github, stated(contextfabric.SubjectRepository, github), nil)
				if !got.synthesized || !slices.Equal(got.committed, []string{string(contextfabric.SubjectRepository)}) {
					t.Fatalf("a false bind: synthesis %t committed %v", got.synthesized, got.committed)
				}
			})
			t.Run("a work item, on each tracker", func(t *testing.T) {
				frame := stated(contextfabric.SubjectWorkItem, github)
				// CHAOS-12505 ruling 3: each commit says whether a pull request link
				// joins the item to the named repository, read keyed on the item.
				linked := func(t *testing.T, got besideLiveTurn, want string) {
					t.Helper()
					both(t, got, contextfabric.SubjectWorkItem)
					if got.link != want {
						t.Fatalf("link sentence %q, want %q", got.link, want)
					}
				}
				linked(t, turn(t, principal, item("CHAOS-71", github), github, frame, nil), contractsv1.ContextFabricNamedRepositoryNativeLinkLimitation)
				linked(t, turn(t, principal, item("CHAOS-74", github), github, frame, nil), contractsv1.ContextFabricNamedRepositoryNativeLinkLimitation)
				// CHAOS-73 is linked into acme/r01, not acme/r00: both commit, and the
				// answer says no link joins them.
				linked(t, turn(t, principal, item("CHAOS-73", github), github, frame, nil), contractsv1.ContextFabricNamedRepositoryNoLinkLimitation)
				// The GitLab repository holds no pull request either item links to.
				linked(t, turn(t, principal, item("CHAOS-71", gitlab), gitlab, stated(contextfabric.SubjectWorkItem, gitlab), nil), contractsv1.ContextFabricNamedRepositoryNoLinkLimitation)
				twice := turn(t, principal, item("CHAOS-70", github), github, frame, nil)
				if twice.synthesized || twice.result.Status != contextfabric.InvestigationClarificationRequired {
					t.Fatalf("count 2: synthesis %t status %q", twice.synthesized, twice.result.Status)
				}
				if prompt := twice.result.SubjectResolution.ClarificationPrompt; strings.Contains(prompt, "could not be matched") {
					t.Fatalf("count 2: prompt = %q, want the listing's own", prompt)
				}
				gone := turn(t, principal, item("CHAOS-999", github), github, frame, nil)
				if gone.synthesized || gone.result.Status == contextfabric.InvestigationComplete {
					t.Fatalf("count 0: synthesis %t status %q", gone.synthesized, gone.result.Status)
				}
			})
		})
	}
	t.Run("no row outside the grant shows, on either provider", func(t *testing.T) {
		kindPR := stated(contextfabric.SubjectPullRequest, github)
		absent(t, turn(t, restricted, pr("555", github), github, kindPR, nil), scope)
		absent(t, turn(t, restricted, pr("556", github), github, kindPR, nil), scope)
		for _, key := range []string{"CHAOS-72", "CHAOS-999"} {
			got := turn(t, restricted, item(key, github), github, stated(contextfabric.SubjectWorkItem, github), nil)
			if got.synthesized || !slices.Contains(got.result.Limitations, contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation) {
				t.Fatalf("%s: synthesis %t limitations %q", key, got.synthesized, got.result.Limitations)
			}
		}
	})
}
