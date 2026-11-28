package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const (
	subjectAskQuestion   = "What is the status of the alpha project?"
	subjectClarifyResult = "result_subject_ask01"
	subjectOutcomeTool   = "investigate_question_subject"
)

// subjectClarification is a clarification_required answer offering n project
// candidates with subr_ receipts.
func subjectClarification(n int) contractsv1.ContextFabricInvestigationResult {
	result := parityResult()
	result.ResultID = subjectClarifyResult
	result.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	candidates := make([]contractsv1.ContextFabricSubjectCandidate, 0, n)
	for i := 1; i <= n; i++ {
		candidates = append(candidates, contractsv1.ContextFabricSubjectCandidate{
			ReceiptID: fmt.Sprintf("subr_subject_ask_%02d", i),
			Subject:   contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: fmt.Sprintf("project_alpha_%02d", i), Label: fmt.Sprintf("Alpha %02d", i)},
			State:     contractsv1.ContextFabricResolutionAmbiguous, MatchReasons: []string{"label match"}, Confidence: 0.8, EvidenceRefIDs: []string{},
		})
	}
	result.SubjectResolution = contractsv1.ContextFabricSubjectResolution{
		Candidates: candidates, Committed: []contractsv1.ContextFabricSubjectRef{},
		ClarificationPrompt: "Which project did you mean?",
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

type subjectHosted struct {
	mu       sync.Mutex
	requests [][]byte
}

func (h *subjectHosted) record(raw []byte) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, raw)
	return len(h.requests)
}

func (h *subjectHosted) all() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.requests...)
}

// subjectFixtureBootstrap answers the first investigation with first (stating
// the whole-candidate header when whole is true) and every later one with the
// committed parity answer.
func subjectFixtureBootstrap(t *testing.T, first contractsv1.ContextFabricInvestigationResult, whole, failSecond bool, hosted *subjectHosted) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/context-fabric/investigations" {
			writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if hosted.record(raw) == 1 {
			if whole {
				w.Header().Set(contractsv1.ContextFabricSubjectCandidatesCompleteHeader, contractsv1.ContextFabricSubjectCandidatesCompleteValue)
			}
			writeJSONFixture(t, w, http.StatusOK, first)
			return
		}
		if failSecond {
			writeErrorFixture(t, w, http.StatusServiceUnavailable, "upstream_unavailable", true)
			return
		}
		writeJSONFixture(t, w, http.StatusOK, parityResult())
	}))
	t.Cleanup(server.Close)
	cfg := fixtureConfig(t, server)
	cfg.MaxResponseBytes = 8 << 20
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	require.NoError(t, err)
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

type subjectRun struct {
	hosted  [][]byte
	text    string
	struct_ string
	meta    string
	asks    []elicitCall
	diag    string
	cells   map[string]int64
}

type subjectRunOptions struct {
	first      contractsv1.ContextFabricInvestigationResult
	whole      bool
	args       map[string]any
	handler    func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error)
	malformed  bool
	failWrite  bool
	failSecond bool
}

func investigateSubjectRun(t *testing.T, o subjectRunOptions) subjectRun {
	t.Helper()
	hosted := &subjectHosted{}
	boot := subjectFixtureBootstrap(t, o.first, o.whole, o.failSecond, hosted)
	var buf syncBufferLocal
	cfg, caller := boot.split(&buf)
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{Tools: HTTPToolVocabulary()})
	cfg.metrics = instruments
	server := newStdioServer(cfg, caller, "test")
	t1, t2 := mcpsdk.NewInMemoryTransports()
	var serverTransport mcpsdk.Transport = t1
	if o.failWrite {
		serverTransport = failElicitTransport{t1}
	}
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	var run subjectRun
	var opts *mcpsdk.ClientOptions
	if o.handler != nil {
		opts = &mcpsdk.ClientOptions{ElicitationHandler: func(_ context.Context, r *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			schema, _ := json.Marshal(r.Params.RequestedSchema)
			run.asks = append(run.asks, elicitCall{r.Params.Message, string(schema)})
			return o.handler(r)
		}}
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, opts).Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	args := o.args
	if args == nil {
		args = map[string]any{"question": subjectAskQuestion}
	}
	params := &mcpsdk.CallToolParams{Name: toolInvestigateQuestion, Arguments: args}
	if o.malformed {
		params.SetMeta(map[string]any{mcpsdk.MetaKeyClientCapabilities: "not an object"})
	}
	result, err := cs.CallTool(context.Background(), params)
	if o.failWrite {
		for i := 0; i < 300 && !strings.Contains(buf.String(), "mcp elicitation outcome"); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		run.diag = buf.String()
		run.hosted = hosted.all()
		return run
	}
	require.NoError(t, err)
	require.False(t, result.IsError, "%+v", result.Content)
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			run.text += tc.Text
		}
	}
	structured, _ := json.Marshal(result.StructuredContent)
	run.struct_ = string(structured)
	metaJSON, _ := json.Marshal(result.Meta)
	run.meta = string(metaJSON)
	run.diag = buf.String()
	run.hosted = hosted.all()
	run.cells = read()
	return run
}

func subjectOutcomeLines(diag string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(diag, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil || m["msg"] != eventspec.MCPElicitationOutcome.Msg || m["tool"] != subjectOutcomeTool {
			continue
		}
		out = append(out, m)
	}
	return out
}

func subjectOutcomeCertified(t *testing.T, diag, outcome string, asked bool, offered int) {
	t.Helper()
	require.Len(t, subjectOutcomeLines(diag), 1, "exactly one subject outcome line: %s", diag)
	log, err := certify.Parse([]byte(diag))
	require.NoError(t, err)
	_, err = certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{
		"tool": subjectOutcomeTool, "outcome": outcome, "asked": asked, "offered": float64(offered),
	}})
	require.NoError(t, err, diag)
}

type subjectFollowUp struct {
	Question             string                                         `json:"question"`
	ParentResultID       string                                         `json:"parent_result_id"`
	PriorSubjectReceipts []contractsv1.ContextFabricBoundSubjectReceipt `json:"prior_subject_receipts"`
}

func answerCells(cells map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for name, value := range cells {
		if strings.HasPrefix(name, "acr_answers_total") {
			out[name] = value
		}
	}
	return out
}

func TestInvestigateQuestionSubjectElicitationAcceptAsksAgainWithTheChosenReceipt(t *testing.T) {
	first := subjectClarification(2)
	run := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true,
		handler: answerOf("accept", map[string]any{"subject": "project_alpha_02"}, nil)})
	require.Len(t, run.asks, 1)
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Subject struct {
				OneOf []struct {
					Const string `json:"const"`
					Title string `json:"title"`
				} `json:"oneOf"`
			} `json:"subject"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(run.asks[0].schema), &schema))
	require.Equal(t, []string{"subject"}, schema.Required)
	require.Len(t, schema.Properties.Subject.OneOf, 2)
	require.Equal(t, "project_alpha_01", schema.Properties.Subject.OneOf[0].Const, "the engine's own order")
	require.Equal(t, "project_alpha_02", schema.Properties.Subject.OneOf[1].Const)
	require.Equal(t, "Alpha 02 (project, project_alpha_02)", schema.Properties.Subject.OneOf[1].Title)

	require.Len(t, run.hosted, 2, "the pick is a second hosted call")
	var followUp subjectFollowUp
	require.NoError(t, json.Unmarshal(run.hosted[1], &followUp))
	require.Equal(t, subjectAskQuestion, followUp.Question)
	require.Equal(t, subjectClarifyResult, followUp.ParentResultID)
	require.Equal(t, []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: subjectClarifyResult, ReceiptID: "subr_subject_ask_02"}}, followUp.PriorSubjectReceipts)
	require.Equal(t, hostedMembers(t, run.hosted[0]), hostedMembers(t, run.hosted[1], "parent_result_id", "prior_subject_receipts"), "the second request is the first plus the pick only")
	require.NotContains(t, string(run.hosted[0]), "parent_result_id")

	require.Contains(t, run.struct_, `"status":"complete"`, "the answer rendered is the picked one")
	require.Equal(t, map[string]int64{"acr_answers_total{status=complete,tool=investigate_question}": 1}, answerCells(run.cells), "one Answer metric, for the answer delivered")
	subjectOutcomeCertified(t, run.diag, "accepted", true, 2)
}

// hostedMembers is a hosted request as its members, with the generated
// request id and the named members removed.
func hostedMembers(t *testing.T, raw []byte, drop ...string) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &doc))
	delete(doc, "request_id")
	for _, name := range drop {
		delete(doc, name)
	}
	return doc
}

func TestInvestigateQuestionSubjectElicitationKeepsTodaysAnswerOtherwise(t *testing.T) {
	first := subjectClarification(2)
	base := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true})
	require.Len(t, base.hosted, 1)
	require.Empty(t, base.asks)
	require.Contains(t, base.struct_, `"status":"clarification_required"`)
	subjectOutcomeCertified(t, base.diag, "unavailable", false, 2)

	for name, tc := range map[string]struct {
		handler func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error)
		outcome string
		asked   bool
	}{
		"decline":            {answerOf("decline", nil, nil), "declined", true},
		"cancel":             {answerOf("cancel", nil, nil), "declined", true},
		"client error":       {answerOf("", nil, errors.New("boom")), "unavailable", false},
		"accept not offered": {answerOf("accept", map[string]any{"subject": "project_other"}, nil), "unavailable", false},
	} {
		t.Run(name, func(t *testing.T) {
			run := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true, handler: tc.handler})
			require.Len(t, run.asks, 1)
			require.Len(t, run.hosted, 1, "no second hosted call")
			require.Equal(t, rawHosted(base.hosted[0]), rawHosted(run.hosted[0]))
			require.Equal(t, base.text, run.text)
			require.Equal(t, base.struct_, run.struct_)
			require.Equal(t, base.meta, run.meta)
			require.Equal(t, answerCells(base.cells), answerCells(run.cells))
			subjectOutcomeCertified(t, run.diag, tc.outcome, tc.asked, 2)
		})
	}
	t.Run("malformed per-request capability falls back to the session capability", func(t *testing.T) {
		without := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true, malformed: true})
		require.Empty(t, without.asks)
		require.Equal(t, base.struct_, without.struct_)
		subjectOutcomeCertified(t, without.diag, "unavailable", false, 2)
		with := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true, malformed: true, handler: answerOf("decline", nil, nil)})
		require.Len(t, with.asks, 1)
		require.Equal(t, base.struct_, with.struct_)
		subjectOutcomeCertified(t, with.diag, "declined", true, 2)
	})
	t.Run("a failed second call returns today's answer", func(t *testing.T) {
		run := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true, failSecond: true, handler: answerOf("accept", map[string]any{"subject": "project_alpha_02"}, nil)})
		require.Len(t, run.asks, 1)
		require.Len(t, run.hosted, 2)
		require.Equal(t, base.text, run.text)
		require.Equal(t, base.struct_, run.struct_)
		require.Equal(t, answerCells(base.cells), answerCells(run.cells))
		subjectOutcomeCertified(t, run.diag, "accepted", true, 2)
	})
	t.Run("a failed elicitation write is no client reply", func(t *testing.T) {
		run := investigateSubjectRun(t, subjectRunOptions{first: first, whole: true, failWrite: true, handler: answerOf("accept", map[string]any{"subject": "project_alpha_02"}, nil)})
		require.Empty(t, run.asks)
		require.Len(t, run.hosted, 1)
		subjectOutcomeCertified(t, run.diag, "unavailable", false, 2)
	})
}

func TestInvestigateQuestionSubjectElicitationNeverAsksOutsideTheRule(t *testing.T) {
	accept := answerOf("accept", map[string]any{"subject": "project_alpha_01"}, nil)
	for name, tc := range map[string]struct {
		first contractsv1.ContextFabricInvestigationResult
		whole bool
		args  map[string]any
	}{
		"candidate set not proven whole": {subjectClarification(2), false, nil},
		"one candidate":                  {subjectClarification(1), true, nil},
		"26 candidates":                  {subjectClarification(26), true, nil},
		"caller sent a parent result": {subjectClarification(2), true, map[string]any{
			"question": subjectAskQuestion, "parent_result_id": "result_caller_parent"}},
	} {
		t.Run(name, func(t *testing.T) {
			run := investigateSubjectRun(t, subjectRunOptions{first: tc.first, whole: tc.whole, args: tc.args, handler: accept})
			require.Empty(t, run.asks, "must not ask")
			require.Len(t, run.hosted, 1)
			subjectOutcomeCertified(t, run.diag, "not_eligible", false, 0)
		})
	}
	t.Run("25 candidates are asked", func(t *testing.T) {
		run := investigateSubjectRun(t, subjectRunOptions{first: subjectClarification(25), whole: true, handler: accept})
		require.Len(t, run.asks, 1)
		require.Len(t, run.hosted, 2)
		subjectOutcomeCertified(t, run.diag, "accepted", true, 25)
	})
	t.Run("an answer that is not a clarification logs no line", func(t *testing.T) {
		run := investigateSubjectRun(t, subjectRunOptions{first: parityResult(), whole: true, handler: accept})
		require.Empty(t, run.asks)
		require.Len(t, run.hosted, 1)
		require.Empty(t, subjectOutcomeLines(run.diag))
	})
}
