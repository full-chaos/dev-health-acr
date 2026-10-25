package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/llm"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const servedCapOrg = "0123456789abcdef"

// servedCapWorkClient is the canned ClickHouse answer to the work item read: one
// row per work item in the columns the production work provider scans.
type servedCapWorkClient struct{ rows [][]string }

func (c servedCapWorkClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return &servedCapScanner{rows: c.rows}, nil
}

type servedCapScanner struct {
	rows [][]string
	next int
}

func (s *servedCapScanner) Next() bool { return s.next < len(s.rows) }

func (s *servedCapScanner) Scan(dest ...any) error {
	row := s.rows[s.next]
	s.next++
	for index, target := range dest {
		value, ok := target.(*string)
		if !ok || index >= len(row) {
			return fmt.Errorf("servedCapScanner: column %d is %T, want a string column the row holds", index, target)
		}
		*value = row[index]
	}
	return nil
}

func (*servedCapScanner) Err() error   { return nil }
func (*servedCapScanner) Close() error { return nil }

func servedCapWorkItem(id string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", id}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("servedCapWorkItem(%q): omitted=%v err=%v", id, omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonicalID, Label: id}
}

// servedCapModel is an OpenAI-compatible endpoint that counts the synthesize
// calls it is given, keeps the user message of each and answers with a draft.
type servedCapModel struct {
	mu      sync.Mutex
	prompts []string
	url     string
}

func newServedCapModel(t *testing.T) *servedCapModel {
	t.Helper()
	model := &servedCapModel{}
	const draft = `{"status":"complete","direct_judgment":"The work appears open.","current_state":"Open work.","strongest_pressures":[],"drivers":[],"remaining_work":[],"readiness_gaps":[],"conflicts":[],"limitations":[],"evidence_ref_ids":[],"claimed_facts":[],"deterministic_answer":"The work appears open.","warnings":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read model request: %v", err)
		}
		var decoded struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		for _, message := range decoded.Messages {
			if message.Role != "user" {
				continue
			}
			var text string
			if err := json.Unmarshal(message.Content, &text); err != nil {
				var parts []struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(message.Content, &parts); err != nil {
					t.Errorf("decode the user message: %v", err)
				}
				for _, part := range parts {
					text += part.Text
				}
			}
			model.mu.Lock()
			model.prompts = append(model.prompts, text)
			model.mu.Unlock()
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"id":"chatcmpl-test","object":"chat.completion","created":1760000000,"model":"gpt-5-nano","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":41,"completion_tokens":17,"total_tokens":58}}`, draft)
	}))
	t.Cleanup(server.Close)
	model.url = server.URL + "/v1/"
	return model
}

func (m *servedCapModel) given() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.prompts)
}

// servedCapOrgConfig is an organization with its own model configuration.
type servedCapOrgConfig struct{ found bool }

func (c servedCapOrgConfig) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	if !c.found {
		return contextfabric.ResolvedOrgModelConfig{}, false, nil
	}
	return contextfabric.ResolvedOrgModelConfig{
		Provider: "acme-gateway", BaseURL: "https://127.0.0.1:9/v1", Model: "org-model",
		Credential: llm.NewSecret("sk-acme-live-a1b2c3d4e5f6wxyz"), Generation: 1,
	}, true, nil
}

// servedCapGraph commits one subject and discovers no relationship beside it.
type servedCapGraph struct {
	authorizingGraph
	committed []contextfabric.SubjectRef
}

func (g servedCapGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	bases := contextfabric.CommitBasisSet{}
	for _, subject := range g.committed {
		bases.Record(subject, contextfabric.CommitBasisCallerCanonicalID)
	}
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: slices.Clone(g.committed)}, contextfabric.StructureOfferMaterial{}, bases, nil, nil
}

func (g servedCapGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: slices.Clone(g.committed)},
		Paths:      []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type servedCapRig struct {
	engine *contextfabric.Engine
	model  *servedCapModel
}

// newServedCapRig is the real engine over the real work item fact provider
// (canned ClickHouse rows in the production column shape) and the production
// resolver and provider build, with MaxSynthesisInputTokens set to tokenCap.
// The deployment runtime dials the recording model; an organization runtime
// would dial its https endpoint, which the build never lets reach loopback.
func newServedCapRig(t *testing.T, tokenCap int, committed, others []contextfabric.SubjectRef, rows [][]string, org servedCapOrgConfig) *servedCapRig {
	t.Helper()
	model := newServedCapModel(t)
	defaults := modelprovider.Config{
		Provider: modelprovider.DefaultProvider, BaseURL: model.url, Model: modelprovider.DefaultModel,
		APIKey: llm.NewSecret("sk-configured"), Timeout: 30 * time.Second, MaxAttempts: 1,
		AllowInsecureBaseURL: true, MaxSynthesisInputTokens: tokenCap,
	}
	deployment, err := modelprovider.New(context.Background(), defaults)
	if err != nil {
		t.Fatal(err)
	}
	resolver := modelruntimeresolver.New(deployment, org, modelruntimeresolver.NewModelProviderBuild(defaults))
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(servedCapWorkClient{rows: rows}), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "open work", SubjectTerms: []string{committed[0].Label},
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactWork, Subjects: committed}},
	}
	graph := servedCapGraph{authorizingGraph: authorizingGraph{liveGraphReader: liveGraphReader{project: committed[0]}, outcome: contextfabric.StoredSubjectAdmitted}, committed: committed}
	store := memoryinvestigation.NewStore()
	next := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: scriptedInterpretRuntime{interpreted: interpreted, receipt: clientRouteReceipt(contextfabric.ModelOperationInterpret)}},
		Graph:       graph,
		Facts:       servedCapFactReader{registry: registry, others: others},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: resolver, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "served-cap", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"}},
		Results:     store,
	}, contextfabric.EngineOptions{
		ServiceVersion: "served-cap",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			next++
			return fmt.Sprintf("result_served_cap%02d", next)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &servedCapRig{engine: engine, model: model}
}

// servedCapFactReader reads the work item facts of the committed subject and of
// others beside it through the production registry and work provider. The
// others are not committed: a fact about a committed subject is never cut.
type servedCapFactReader struct {
	registry *contextfabric.FactCapabilityRegistry
	others   []contextfabric.SubjectRef
}

func (r servedCapFactReader) ReadFacts(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	request.Subjects = append(slices.Clone(request.Subjects), r.others...)
	request.Requirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactWork, Subjects: request.Subjects}}
	return r.registry.ReadFacts(ctx, principal, request)
}

func (r *servedCapRig) investigate(t *testing.T, org string) (contextfabric.InvestigationResult, error) {
	t.Helper()
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_served_cap_0001",
		Question:    "What open work is there?",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 50, MaxCohortMembers: 250, MaxRelationshipPaths: 250,
			MaxDrivers: 50, MaxEvidenceRefs: 500, MaxSerializedBytes: 1 << 20,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	return r.engine.Investigate(context.Background(), storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, request)
}

// servedCapRows is n work items with a title of about titleBytes bytes each.
func servedCapRows(n, titleBytes int) ([][]string, []contextfabric.SubjectRef) {
	rows := make([][]string, 0, n)
	subjects := make([]contextfabric.SubjectRef, 0, n)
	for index := 0; index < n; index++ {
		id := fmt.Sprintf("WORK-%04d", index)
		rows = append(rows, []string{id, id + " " + strings.Repeat("t", titleBytes), "repo-1"})
		subjects = append(subjects, servedCapWorkItem(id))
	}
	return rows, subjects
}

func servedCapFactsInPrompt(t *testing.T, prompt string) int {
	t.Helper()
	var given struct {
		Facts []struct {
			Kind contextfabric.FactKind `json:"kind"`
		} `json:"canonical_facts"`
	}
	if err := json.Unmarshal([]byte(prompt), &given); err != nil {
		t.Fatalf("the model input is not the synthesis input: %v", err)
	}
	count := 0
	for _, fact := range given.Facts {
		if fact.Kind == contextfabric.FactWork {
			count++
		}
	}
	return count
}

func servedCapOverheadBytes() int {
	return len(synthesisprompt.System()) + len(synthesisprompt.OutputSchema())
}

func servedCapDetail(result contextfabric.InvestigationResult) *contextfabric.CoverageDetail {
	for index, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisInputBounded && detail.FactKind == contextfabric.FactWork {
			return &result.Coverage.Details[index]
		}
	}
	return nil
}

const (
	servedCapItems      = 150
	servedCapTitleBytes = 300
)

// servedCapCutFixture is the cut fixture and the cap in tokens it is run at: twice the minimum, so the bound is well above the
// 8 KiB floor and far below the fixture.
func servedCapCutFixture(t *testing.T) (rows [][]string, subjects []contextfabric.SubjectRef, tokenCap int) {
	t.Helper()
	rows, subjects = servedCapRows(servedCapItems, servedCapTitleBytes)
	return rows, subjects, 2 * genkitruntime.MinSynthesisInputTokens
}

// With the cap on, the prompt the model is given through Engine.Investigate is
// cut to fit, and the served answer says so with the numbers.
func TestServedSynthesisPromptIsCutToTheTokenCapOnTheDeploymentRuntime(t *testing.T) {
	rows, subjects, tokenCap := servedCapCutFixture(t)
	bound := 3*tokenCap - servedCapOverheadBytes()

	control := newServedCapRig(t, 0, subjects[:1], subjects[1:], rows, servedCapOrgConfig{})
	controlResult, err := control.investigate(t, servedCapOrg)
	if err != nil {
		t.Fatalf("control: Investigate error = %v", err)
	}
	controlPrompts := control.model.given()
	if len(controlPrompts) == 0 {
		t.Fatal("control: the model was not called")
	}
	if len(controlPrompts[0]) <= bound {
		t.Fatalf("control prompt is %d bytes, within the bound of %d: the fixture no longer exceeds the cap", len(controlPrompts[0]), bound)
	}
	if read := servedCapFactsInPrompt(t, controlPrompts[0]); read != servedCapItems {
		t.Fatalf("control prompt carries %d work facts, want all %d read", read, servedCapItems)
	}
	if servedCapDetail(controlResult) != nil || slices.Contains(controlResult.Limitations, contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatalf("control: limitations = %q details = %+v, want no bounded-input disclosure with the cap off", controlResult.Limitations, controlResult.Coverage.Details)
	}
	if controlResult.Status != contextfabric.InvestigationComplete {
		t.Fatalf("control status = %q, want %q", controlResult.Status, contextfabric.InvestigationComplete)
	}

	capped := newServedCapRig(t, tokenCap, subjects[:1], subjects[1:], rows, servedCapOrgConfig{})
	result, err := capped.investigate(t, servedCapOrg)
	if err != nil {
		t.Fatalf("Investigate error = %v, want the cut answer served", err)
	}
	prompts := capped.model.given()
	if len(prompts) == 0 {
		t.Fatal("the model was not called")
	}
	given := -1
	for index, prompt := range prompts {
		if len(prompt) > bound {
			t.Fatalf("prompt %d is %d bytes, over the cap bound of %d", index, len(prompt), bound)
		}
		count := servedCapFactsInPrompt(t, prompt)
		if given >= 0 && count != given {
			t.Fatalf("prompt %d carries %d work facts, prompt 0 carried %d", index, count, given)
		}
		given = count
	}
	if given < 1 || given >= servedCapItems {
		t.Fatalf("prompt carries %d work facts, want a part of the %d read", given, servedCapItems)
	}
	detail := servedCapDetail(result)
	if detail == nil || detail.Declared == nil || detail.Served == nil || *detail.Declared != servedCapItems || *detail.Served != given {
		t.Fatalf("coverage details = %+v, want a work synthesis_input_bounded detail of %d given of %d read", result.Coverage.Details, given, servedCapItems)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatalf("limitations = %q, want the bounded-input disclosure", result.Limitations)
	}
	if result.Status != contextfabric.InvestigationPartial {
		t.Fatalf("status = %q, want %q: the model said complete about part of the facts", result.Status, contextfabric.InvestigationPartial)
	}
	if terminal := contextfabric.ComputeAnswerCompleteness(result).TerminalStatus; terminal != contextfabric.InvestigationPartial {
		t.Fatalf("served completeness terminal status = %q, want %q", terminal, contextfabric.InvestigationPartial)
	}
}

// When the committed subjects alone exceed the cap bound no cut can fit: the
// investigation ends with the input overflow at the bound the cap leaves, and
// the model is never called. With the cap off the same fixture is sent: the
// deployment runtime answers it, and the organization runtime attempts its own
// endpoint and is refused there.
func TestServedSynthesisThatCannotFitTheTokenCapIsNeverSent(t *testing.T) {
	rows, subjects := servedCapRows(30, 500)
	bound := 3*genkitruntime.MinSynthesisInputTokens - servedCapOverheadBytes()
	for _, tc := range []struct {
		name string
		org  servedCapOrgConfig
	}{
		{name: "deployment runtime", org: servedCapOrgConfig{}},
		{name: "organization runtime", org: servedCapOrgConfig{found: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capped := newServedCapRig(t, genkitruntime.MinSynthesisInputTokens, subjects, nil, rows, tc.org)
			_, err := capped.investigate(t, servedCapOrg)
			var overflow *contextfabric.ModelInputOverflow
			if !errors.As(err, &overflow) || !errors.Is(err, contextfabric.ErrModelInputTooLarge) {
				t.Fatalf("Investigate error = %v, want the model input overflow", err)
			}
			if overflow.MaxBytes != bound || overflow.Bytes <= bound {
				t.Fatalf("overflow = %d bytes over a bound of %d, want a bound of %d the cap leaves", overflow.Bytes, overflow.MaxBytes, bound)
			}
			if calls := len(capped.model.given()); calls != 0 {
				t.Fatalf("model calls = %d, want 0", calls)
			}

			off := newServedCapRig(t, 0, subjects, nil, rows, tc.org)
			result, err := off.investigate(t, servedCapOrg)
			if err != nil {
				t.Fatalf("cap off: Investigate error = %v, want the call placed and an answer served", err)
			}
			if tc.org.found {
				refused := false
				for _, detail := range result.Coverage.Details {
					refused = refused || (detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed && string(detail.FailureClass) == "model_call_not_completed")
				}
				if result.Status != contextfabric.InvestigationDegraded || !refused || len(off.model.given()) != 0 {
					t.Fatalf("cap off, organization runtime: status = %q refused = %v calls = %d, want the call attempted at the organization endpoint and refused there (synthesis_model_failed, model_call_not_completed)", result.Status, refused, len(off.model.given()))
				}
				return
			}
			if len(off.model.given()) == 0 {
				t.Fatal("cap off: the model was not called")
			}
		})
	}
}
