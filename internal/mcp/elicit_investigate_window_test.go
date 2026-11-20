package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

const windowAskQuestion = "Which repository carried the most operational work for last month?"

type windowRun struct {
	hosted []byte
	calls  int
	text   string
	asks   []elicitCall
	diag   string
}

type elicitCall struct{ message, schema string }

func investigateWindowRun(t *testing.T, args map[string]any, handler func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error), malformedCaps bool) windowRun {
	t.Helper()
	var run windowRun
	boot := synthesisFixtureBootstrap(t, envelopeBody(nil), &run.hosted, &run.calls)
	var buf bytes.Buffer
	server := NewServerWithDiagnostics(boot, "test", &buf)
	t1, t2 := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)
	var opts *mcpsdk.ClientOptions
	if handler != nil {
		opts = &mcpsdk.ClientOptions{ElicitationHandler: func(_ context.Context, r *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			schema, _ := json.Marshal(r.Params.RequestedSchema)
			run.asks = append(run.asks, elicitCall{r.Params.Message, string(schema)})
			return handler(r)
		}}
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, opts).Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	params := &mcpsdk.CallToolParams{Name: toolInvestigateQuestion, Arguments: args}
	if malformedCaps {
		params.SetMeta(map[string]any{mcpsdk.MetaKeyClientCapabilities: "not an object"})
	}
	result, err := cs.CallTool(context.Background(), params)
	require.NoError(t, err)
	require.False(t, result.IsError, "%+v", result.Content)
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			run.text += tc.Text
		}
	}
	run.diag = buf.String()
	return run
}

func answerOf(action string, content map[string]any, err error) func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
	return func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: action, Content: content}, err
	}
}

func windowOutcomeCertified(t *testing.T, diag, outcome string, asked bool, offered int) {
	t.Helper()
	log, err := certify.Parse([]byte(diag))
	require.NoError(t, err)
	_, err = certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{
		"tool": "investigate_question_window", "outcome": outcome, "asked": asked, "offered": float64(offered),
	}})
	require.NoError(t, err, diag)
}

func noWindowOutcomeLine(t *testing.T, diag string) {
	t.Helper()
	log, err := certify.Parse([]byte(diag))
	if err != nil {
		return
	}
	_, err = certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{"tool": "investigate_question_window"}})
	require.Error(t, err, "no window outcome line expected: %s", diag)
}

func hostedEvidenceWindow(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var req struct {
		TimeContext struct {
			EvidenceWindow json.RawMessage `json:"evidence_window"`
		} `json:"time_context"`
	}
	require.NoError(t, json.Unmarshal(raw, &req))
	return req.TimeContext.EvidenceWindow
}

func normalizedHosted(t *testing.T, raw []byte) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	delete(m, "request_id")
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

func withoutEvidenceWindow(t *testing.T, raw []byte) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	delete(m, "request_id")
	if tc, ok := m["time_context"].(map[string]any); ok {
		delete(tc, "evidence_window")
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

func TestInvestigateQuestionWindowElicitation(t *testing.T) {
	args := map[string]any{"question": windowAskQuestion}
	base := investigateWindowRun(t, args, nil, false)
	require.Equal(t, 1, base.calls)
	require.Empty(t, base.asks)
	require.Empty(t, hostedEvidenceWindow(t, base.hosted), "today the request carries no evidence_window")
	windowOutcomeCertified(t, base.diag, "unavailable", false, 2)

	for name, tc := range map[string]struct {
		handler func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error)
		outcome string
	}{
		"decline":            {answerOf("decline", nil, nil), "declined"},
		"cancel":             {answerOf("cancel", nil, nil), "declined"},
		"client error":       {answerOf("", nil, errors.New("boom")), "unavailable"},
		"accept not offered": {answerOf("accept", map[string]any{"window": "trailing_90d"}, nil), "unavailable"},
		"accept calendar":    {answerOf("accept", map[string]any{"window": "previous_calendar_month"}, nil), "accepted"},
	} {
		t.Run(name+" sends today's request and answer", func(t *testing.T) {
			run := investigateWindowRun(t, args, tc.handler, false)
			require.Len(t, run.asks, 1)
			require.Equal(t, 1, run.calls)
			require.Equal(t, normalizedHosted(t, base.hosted), normalizedHosted(t, run.hosted), "hosted request must be byte-equal to base")
			require.Equal(t, base.text, run.text)
			windowOutcomeCertified(t, run.diag, tc.outcome, true, 2)
		})
	}

	t.Run("accept trailing_30d names that window and changes nothing else", func(t *testing.T) {
		run := investigateWindowRun(t, args, answerOf("accept", map[string]any{"window": "trailing_30d"}, nil), false)
		require.Len(t, run.asks, 1)
		t.Logf("ELICIT message=%q schema=%s", run.asks[0].message, run.asks[0].schema)
		require.Equal(t, 1, run.calls)
		require.JSONEq(t, `{"relative_id":"trailing_30d"}`, string(hostedEvidenceWindow(t, run.hosted)))
		require.Equal(t, withoutEvidenceWindow(t, base.hosted), withoutEvidenceWindow(t, run.hosted))
		require.Equal(t, base.text, run.text, "the fixture answer is the same; only the request differs")
		windowOutcomeCertified(t, run.diag, "accepted", true, 2)
	})

	t.Run("malformed per-request capability falls back to the session capability", func(t *testing.T) {
		with := investigateWindowRun(t, args, answerOf("decline", nil, nil), true)
		require.Len(t, with.asks, 1, "session declared form elicitation")
		require.Equal(t, normalizedHosted(t, base.hosted), normalizedHosted(t, with.hosted))
		without := investigateWindowRun(t, args, nil, true)
		require.Empty(t, without.asks)
		require.Equal(t, normalizedHosted(t, base.hosted), normalizedHosted(t, without.hosted))
		windowOutcomeCertified(t, without.diag, "unavailable", false, 2)
	})
}

func TestInvestigateQuestionWindowNeverAsksOutsideTheRule(t *testing.T) {
	accept := answerOf("accept", map[string]any{"window": "trailing_30d"}, nil)
	falseVal := false
	for name, tc := range map[string]struct {
		args    map[string]any
		outcome string
		line    bool
	}{
		"bare last month, no preposition": {map[string]any{"question": "Which repository carried the most operational work last month?"}, "not_eligible", true},
		"in last month, no article":       {map[string]any{"question": "Which repository carried the most operational work in last month?"}, "not_eligible", true},
		"trailing form with article":      {map[string]any{"question": "Which repository carried the most operational work for the last month?"}, "", false},
		"trailing form in the last month": {map[string]any{"question": "Which repository carried the most operational work in the last month?"}, "", false},
		"no period":                       {map[string]any{"question": "Which repository carried the most operational work?"}, "", false},
		"last quarter":                    {map[string]any{"question": "Which repository carried the most operational work for last quarter?"}, "", false},
		"point in time":                   {map[string]any{"question": "What was the state as of the end of last month?"}, "", false},
		"caller sent an evidence window": {map[string]any{"question": windowAskQuestion,
			"evidence_window": map[string]any{"relative_id": "trailing_90d"}}, "not_eligible", true},
		"allow_clarification false": {map[string]any{"question": windowAskQuestion, "allow_clarification": falseVal}, "not_eligible", true},
	} {
		t.Run(name, func(t *testing.T) {
			run := investigateWindowRun(t, tc.args, accept, false)
			require.Empty(t, run.asks, "must not ask")
			require.Equal(t, 1, run.calls)
			if tc.line {
				windowOutcomeCertified(t, run.diag, tc.outcome, false, 0)
			} else {
				noWindowOutcomeLine(t, run.diag)
			}
			if name == "caller sent an evidence window" {
				require.JSONEq(t, `{"relative_id":"trailing_90d"}`, string(hostedEvidenceWindow(t, run.hosted)))
			}
		})
	}
}

func TestWindowChoiceIsAskedOnlyFromInvestigateQuestion(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		return string(raw)
	}
	require.Equal(t, 1, strings.Count(read("investigate_question.go"), "chooseWindow("))
	for _, name := range []string{"investigate_with_interpretation.go", "tasks.go"} {
		require.NotContains(t, read(name), "chooseWindow(", name)
	}
}
