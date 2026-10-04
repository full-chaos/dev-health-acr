package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// followUpReuse counts every reuse read of one engine: the lookup and the two
// snapshots a saved row's reuse key is built from.
type followUpReuse struct {
	store                       *pginvestigation.Store
	lookups, watermarks, epochs int
}

func (r *followUpReuse) FindReusable(ctx context.Context, p storage.Principal, key contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	r.lookups++
	return r.store.FindReusable(ctx, p, key)
}

func (r *followUpReuse) SnapshotSourceWatermarks(ctx context.Context, orgID string) (contextfabric.SourceWatermarkSnapshot, error) {
	r.watermarks++
	return r.store.SnapshotSourceWatermarks(ctx, orgID)
}

func (r *followUpReuse) SnapshotRebuildEpoch(ctx context.Context, orgID string) (int64, error) {
	r.epochs++
	return r.store.SnapshotRebuildEpoch(ctx, orgID)
}

// newFollowUpEngine is a fresh engine over a shared result store. With a
// reuse recorder it also turns answer reuse on over the PostgreSQL store.
func newFollowUpEngine(t *testing.T, interpreter contextfabric.RuntimeQuestionInterpreter, outcome contextfabric.StoredSubjectOutcome, synthesized *[]contextfabric.InterpretedQuestion, store contextfabric.InvestigationResultStore, reuse *followUpReuse) *contextfabric.Engine {
	t.Helper()
	project := suppliedRouteProject()
	results := 0
	deps := contextfabric.EngineDependencies{
		Interpreter: interpreter,
		Graph:       authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: outcome},
		Facts:       liveFactReader{bundle: liveCanonicalFacts(project)},
		Synthesizer: fixedAnswerSynthesizer{interpretations: synthesized},
		Results:     store,
	}
	options := contextfabric.EngineOptions{
		ServiceVersion: "supplied-interpretation-follow-up",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			results++
			return fmt.Sprintf("result_follow_up_%d_%02d", time.Now().UnixNano(), results)
		},
	}
	if reuse != nil {
		deps.ReuseGate, deps.ReuseSnapshotter, deps.ReuseEpochSnapshotter = reuse, reuse, reuse
		options.ReuseProjectionVersion = "projection-v1"
		options.ReuseModelIdentities = []string{"test-provider/test-model"}
		options.ReuseRetrievalIdentity = contextfabric.ReuseRetrievalIdentity{EmbedRetrievalIdentity: "none", RetrievalPolicyVersion: "follow-up-v1"}
		options.ReusePromptVersions = contextfabric.ReusePromptVersions{InterpretationPromptVersion: "follow-up-interpret-v1", SynthesisPromptVersion: "follow-up-synthesis-v1"}
		options.ReuseVersionAuthorities = contextfabric.ReuseVersionAuthorities{QueryVersion: "query-v1", CanonicalServiceVersion: "follow-up-facts-v1", ModelOutputSchemaVersion: "follow-up-schema-v1", IdentityNormalizationVersion: "follow-up-identity-v1", WindowInferenceVersion: contextfabric.WindowInferenceVersion, CommitGateVersion: contextfabric.CommitGateVersion, RankingFormulaVersion: contextfabric.RankingFormulaVersion, QuestionFamilyVersion: contextfabric.QuestionFamilyTableVersion, OwnershipRoutingVersion: contextfabric.OwnershipRoutingVersion}
	}
	engine, err := contextfabric.NewEngine(deps, options)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

type followUpTurn struct {
	status    contractsv1.ContextFabricInvestigationStatus
	refusal   contractsv1.ContextFabricRefusalBasis
	window    *contractsv1.ContextFabricEffectiveEvidenceWindow
	limits    []string
	committed []contractsv1.ContextFabricSubjectRef
	answer    string
	resultID  string
}

// suppliedFollowUpLegs serves a window-asking first turn and its follow-up
// (parent_result_id plus the receipt the first answer offers), each turn by
// its own fresh engine over one shared result store. The legs are both turns
// supplied, both turns by the model, and the two mixed orders. Every leg must
// serve the follow-up the model path serves, each turn names its interpreter,
// a supplied turn makes no interpret call, and the second engine re-checks
// the committed root against the live authorizer: a root the caller can no
// longer read ends the follow-up the same way on every leg.
func suppliedFollowUpLegs(t *testing.T, store func(t *testing.T) contextfabric.InvestigationResultStore, reusing func(contextfabric.InvestigationResultStore) *followUpReuse, db *sql.DB) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interpreted, err := genkitruntime.ParseInterpretationOutput([]byte(suppliedRouteOutput), contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	modelReceipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: contract.PromptVersion, SchemaVersion: contract.ModelOutputVersion, EvaluatorVersion: "eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1, InputDigest: strings.Repeat("a", 64), Outcome: "success",
	}
	suppliedField := &contractsv1.ContextFabricSuppliedInterpretation{
		Output: json.RawMessage(suppliedRouteOutput), ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
		ClientModel: "claude-test",
	}
	const clientIdentity, serverIdentity = "client-supplied/claude-test", "test-provider/test-model"

	run := func(t *testing.T, firstSupplied, secondSupplied bool, secondOutcome contextfabric.StoredSubjectOutcome) followUpTurn {
		t.Helper()
		shared := store(t)
		interprets := 0
		var synthesized []contextfabric.InterpretedQuestion
		interpreter := contextfabric.RuntimeQuestionInterpreter{
			Runtime:  countingScriptedRuntime{scriptedInterpretRuntime: scriptedInterpretRuntime{interpreted: interpreted, receipt: modelReceipt}, interprets: &interprets},
			Supplied: supplied,
		}
		send := func(engine *contextfabric.Engine, body contractsv1.ContextFabricInvestigationRequest, suppliedTurn bool) contractsv1.ContextFabricInvestigationResult {
			t.Helper()
			app, token := newLiveContextFabricTestApp(t, engine)
			if suppliedTurn {
				body.SuppliedInterpretation = suppliedField
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
			}
			var result contractsv1.ContextFabricInvestigationResult
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("served result fails its own contract: %v", err)
			}
			wantSource, wantIdentity := contractsv1.ContextFabricInterpretationSourceServer, serverIdentity
			if suppliedTurn {
				wantSource, wantIdentity = contractsv1.ContextFabricInterpretationSourceClient, clientIdentity
			}
			if got := result.Versions; got.InterpretationSource != wantSource || got.InterpretationModelIdentity != wantIdentity {
				t.Fatalf("interpretation_source = %q interpretation_model_identity = %q, want %q and %q", got.InterpretationSource, got.InterpretationModelIdentity, wantSource, wantIdentity)
			}
			return result
		}

		firstReuse, secondReuse := reusing(shared), reusing(shared)
		first := newFollowUpEngine(t, interpreter, contextfabric.StoredSubjectAdmitted, &synthesized, shared, firstReuse)
		asked := send(first, investigationRequestBody(), firstSupplied)
		if asked.Status != contractsv1.ContextFabricInvestigationClarificationRequired || asked.WindowClarification == nil || len(asked.WindowClarification.Options) == 0 || len(synthesized) != 0 {
			t.Fatalf("first turn: status = %q syntheses = %d, want a window clarification with an option and no synthesis", asked.Status, len(synthesized))
		}
		interpretsAfterFirst := interprets

		second := newFollowUpEngine(t, interpreter, secondOutcome, &synthesized, shared, secondReuse)
		followUp := investigationRequestBody()
		followUp.ParentResultID = asked.ResultID
		followUp.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: asked.ResultID, ReceiptID: asked.WindowClarification.Options[0].ReceiptID}}
		served := send(second, followUp, secondSupplied)

		for name, turn := range map[string]struct {
			supplied bool
			reuse    *followUpReuse
		}{"first": {firstSupplied, firstReuse}, "second": {secondSupplied, secondReuse}} {
			if turn.reuse == nil || !turn.supplied {
				continue
			}
			if turn.reuse.lookups != 0 {
				t.Fatalf("%s turn is supplied: reuse lookups = %d, want 0", name, turn.reuse.lookups)
			}
		}
		wantInterprets := 0
		for _, suppliedTurn := range []bool{firstSupplied, secondSupplied} {
			if !suppliedTurn {
				wantInterprets++
			}
		}
		if interprets != wantInterprets || (firstSupplied && interpretsAfterFirst != 0) {
			t.Fatalf("model interpret calls = %d (after the first turn %d), want %d: one for each turn the service interprets and none for a supplied turn", interprets, interpretsAfterFirst, wantInterprets)
		}
		if db != nil {
			for _, saved := range []struct {
				id       string
				supplied bool
			}{{asked.ResultID, firstSupplied}, {served.ResultID, secondSupplied}} {
				if !saved.supplied {
					continue
				}
				var reuseKeyed bool
				if err := db.QueryRowContext(context.Background(), `SELECT question_hash IS NOT NULL OR source_watermarks IS NOT NULL OR invalidation_epoch IS NOT NULL FROM acr.context_fabric_investigation_results WHERE org_id=$1 AND result_id=$2`, "org_1", saved.id).Scan(&reuseKeyed); err != nil {
					t.Fatalf("read the stored row of %s: %v", saved.id, err)
				}
				if reuseKeyed {
					t.Fatalf("stored row %s of a supplied turn carries reuse-key columns, want none: a supplied turn is not a reuse source", saved.id)
				}
			}
		}
		return followUpTurn{
			status: served.Status, refusal: served.RefusalBasis, window: served.EffectiveEvidenceWindow, limits: served.Limitations,
			committed: served.SubjectResolution.Committed, answer: served.DeterministicAnswer, resultID: served.ResultID,
		}
	}

	legs := map[string][2]bool{"client then client": {true, true}, "client then server": {true, false}, "server then client": {false, true}}
	t.Run("served", func(t *testing.T) {
		model := run(t, false, false, contextfabric.StoredSubjectAdmitted)
		if model.status != contractsv1.ContextFabricInvestigationComplete || model.window == nil || model.window.Provenance != contractsv1.ContextFabricWindowClarificationConfirmed || len(model.committed) != 1 {
			t.Fatalf("model path follow-up = %#v, want the answer served under the confirmed window with the project committed", model)
		}
		for name, leg := range legs {
			t.Run(name, func(t *testing.T) {
				got := run(t, leg[0], leg[1], contextfabric.StoredSubjectAdmitted)
				got.resultID, model.resultID = "", ""
				if !reflect.DeepEqual(got, model) {
					t.Fatalf("the served follow-up differs from the model path\ngot:   %#v\nmodel: %#v", got, model)
				}
			})
		}
	})
	t.Run("root_no_longer_readable", func(t *testing.T) {
		model := run(t, false, false, contextfabric.StoredSubjectDenied)
		if len(model.committed) != 0 || model.status == contractsv1.ContextFabricInvestigationComplete {
			t.Fatalf("model path with a denied root = %#v, want no committed subject and no answer", model)
		}
		for name, leg := range legs {
			t.Run(name, func(t *testing.T) {
				got := run(t, leg[0], leg[1], contextfabric.StoredSubjectDenied)
				got.resultID, model.resultID = "", ""
				if !reflect.DeepEqual(got, model) {
					t.Fatalf("the refused follow-up differs from the model path\ngot:   %#v\nmodel: %#v", got, model)
				}
			})
		}
	})
}

// TestSuppliedInterpretationFollowUpIsServedByAFreshEngineOverMemory runs the
// follow-up legs with the in-memory store so the assertions are exercised
// without a container.
func TestSuppliedInterpretationFollowUpIsServedByAFreshEngineOverMemory(t *testing.T) {
	suppliedFollowUpLegs(t,
		func(*testing.T) contextfabric.InvestigationResultStore {
			return rankingSurfaceStore{memoryinvestigation.NewStore()}
		},
		func(contextfabric.InvestigationResultStore) *followUpReuse { return nil }, nil)
}

// TestSuppliedInterpretationFollowUpIsServedByAFreshEngineOverPostgres is the
// same over the production store: the second turn is a new engine that holds
// nothing of the first but the PostgreSQL rows, with answer reuse on, so a
// supplied turn's reuse reads and reuse-key columns are observed too.
func TestSuppliedInterpretationFollowUpIsServedByAFreshEngineOverPostgres(t *testing.T) {
	db := freshPGReuseDatabase(t)
	suppliedFollowUpLegs(t,
		func(t *testing.T) contextfabric.InvestigationResultStore {
			store, err := pginvestigation.NewStore(db, pginvestigation.WithAnswerReuse(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			return store
		},
		func(shared contextfabric.InvestigationResultStore) *followUpReuse {
			return &followUpReuse{store: shared.(*pginvestigation.Store)}
		}, db)
}
