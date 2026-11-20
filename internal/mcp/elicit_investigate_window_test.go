package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

const windowAskQuestion = "Which repository carried the most operational work for last month?"

type windowRun struct {
	hosted  []byte
	calls   int
	text    string
	struct_ string
	asks    []elicitCall
	diag    string
}

type elicitCall struct{ message, schema string }

func investigateWindowRun(t *testing.T, args map[string]any, handler func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error), malformedCaps bool) windowRun {
	t.Helper()
	return investigateWindowRunWith(t, toolInvestigateQuestion, args, handler, windowRunMeta{malformed: malformedCaps}, nil)
}

type windowRunMeta struct {
	malformed bool
	perCall   map[string]any
}

func investigateWindowRunWith(t *testing.T, tool string, args map[string]any, handler func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error), meta windowRunMeta, prepare func(*Bootstrap) *Bootstrap) windowRun {
	t.Helper()
	var run windowRun
	boot := synthesisFixtureBootstrap(t, envelopeBody(nil), &run.hosted, &run.calls)
	if prepare != nil {
		boot = prepare(boot)
	}
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
	params := &mcpsdk.CallToolParams{Name: tool, Arguments: args}
	if meta.malformed {
		params.SetMeta(map[string]any{mcpsdk.MetaKeyClientCapabilities: "not an object"})
	}
	if meta.perCall != nil {
		params.SetMeta(map[string]any{mcpsdk.MetaKeyClientCapabilities: meta.perCall})
	}
	result, err := cs.CallTool(context.Background(), params)
	require.NoError(t, err)
	require.False(t, result.IsError, "%+v", result.Content)
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			run.text += tc.Text
		}
	}
	structured, _ := json.Marshal(result.StructuredContent)
	run.struct_ = string(structured)
	run.diag = buf.String()
	return run
}

func answerOf(action string, content map[string]any, err error) func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
	return func(*mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: action, Content: content}, err
	}
}

func windowOutcomeLines(t *testing.T, diag string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(diag, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil || m["msg"] != eventspec.MCPElicitationOutcome.Msg || m["tool"] != "investigate_question_window" {
			continue
		}
		out = append(out, m)
	}
	return out
}

func windowOutcomeCertified(t *testing.T, diag, outcome string, asked bool, offered int) {
	t.Helper()
	lines := windowOutcomeLines(t, diag)
	require.Len(t, lines, 1, "exactly one window outcome line: %s", diag)
	log, err := certify.Parse([]byte(diag))
	require.NoError(t, err)
	_, err = certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{
		"tool": "investigate_question_window", "outcome": outcome, "asked": asked, "offered": float64(offered),
	}})
	require.NoError(t, err, diag)
}

func noWindowOutcomeLine(t *testing.T, diag string) {
	t.Helper()
	require.Empty(t, windowOutcomeLines(t, diag), "no window outcome line expected: %s", diag)
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

var hostedRequestID = regexp.MustCompile(`"request_id":"req_[0-9a-f]+"`)

// rawHosted is the hosted request bytes with only the generated request id masked.
func rawHosted(raw []byte) string {
	return hostedRequestID.ReplaceAllString(string(raw), `"request_id":"req_X"`)
}

// rawHostedWithoutWindow is rawHosted with the one evidence_window member of an accepted choice removed from the bytes.
func rawHostedWithoutWindow(raw []byte) string {
	return strings.Replace(rawHosted(raw), `,"evidence_window":{"relative_id":"trailing_30d"}`, "", 1)
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
			require.Equal(t, rawHosted(base.hosted), rawHosted(run.hosted), "hosted request must be byte-equal to base")
			require.Equal(t, base.text, run.text)
			require.Equal(t, base.struct_, run.struct_)
			windowOutcomeCertified(t, run.diag, tc.outcome, true, 2)
		})
	}

	t.Run("accept trailing_30d names that window and changes nothing else", func(t *testing.T) {
		run := investigateWindowRun(t, args, answerOf("accept", map[string]any{"window": "trailing_30d"}, nil), false)
		require.Len(t, run.asks, 1)
		t.Logf("ELICIT message=%q schema=%s", run.asks[0].message, run.asks[0].schema)
		require.Equal(t, 1, run.calls)
		require.JSONEq(t, `{"relative_id":"trailing_30d"}`, string(hostedEvidenceWindow(t, run.hosted)))
		require.Equal(t, rawHosted(base.hosted), rawHostedWithoutWindow(run.hosted))
		require.Equal(t, base.text, run.text, "the fixture answer is the same; only the request differs")
		windowOutcomeCertified(t, run.diag, "accepted", true, 2)
	})

	t.Run("malformed per-request capability falls back to the session capability", func(t *testing.T) {
		with := investigateWindowRun(t, args, answerOf("decline", nil, nil), true)
		require.Len(t, with.asks, 1, "session declared form elicitation")
		require.Equal(t, rawHosted(base.hosted), rawHosted(with.hosted))
		without := investigateWindowRun(t, args, nil, true)
		require.Empty(t, without.asks)
		require.Equal(t, rawHosted(base.hosted), rawHosted(without.hosted))
		windowOutcomeCertified(t, without.diag, "unavailable", false, 2)
	})

	t.Run("malformed per-request capability with session form capability and an accepting client asks and sends the choice", func(t *testing.T) {
		run := investigateWindowRun(t, args, answerOf("accept", map[string]any{"window": "trailing_30d"}, nil), true)
		require.Len(t, run.asks, 1)
		require.JSONEq(t, `{"relative_id":"trailing_30d"}`, string(hostedEvidenceWindow(t, run.hosted)))
		windowOutcomeCertified(t, run.diag, "accepted", true, 2)
	})

	t.Run("per-call form capability with no session capability sends no prompt and logs asked false", func(t *testing.T) {
		run := investigateWindowRunWith(t, toolInvestigateQuestion, args, nil, windowRunMeta{perCall: map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}}, nil)
		require.Empty(t, run.asks)
		require.Equal(t, rawHosted(base.hosted), rawHosted(run.hosted))
		windowOutcomeCertified(t, run.diag, "unavailable", false, 2)
	})
}

func TestInvestigateQuestionWindowAsksOnlyWhenThePhraseEndsItsClause(t *testing.T) {
	accept := answerOf("accept", map[string]any{"window": "trailing_30d"}, nil)
	for question, asks := range map[string]bool{
		"What changed for last month's release?":                           false,
		"How many incidents did we have during last month of the quarter?": false,
		"Show deployments for last month through November 10.":             false,
		"Which repository carried the most work for last month?":           true,
		"Which repository carried the most work for last month, and why?":  true,
		"Which repository carried the most work for last month and why?":   true,
		"Which repository carried the most work for last month: top three": true,
	} {
		t.Run(question, func(t *testing.T) {
			run := investigateWindowRun(t, map[string]any{"question": question}, accept, false)
			if !asks {
				require.Empty(t, run.asks, "must not ask")
				require.Empty(t, hostedEvidenceWindow(t, run.hosted))
				return
			}
			require.Len(t, run.asks, 1)
			require.JSONEq(t, `{"relative_id":"trailing_30d"}`, string(hostedEvidenceWindow(t, run.hosted)))
		})
	}
}

func TestSuppliedInterpretationNeverAsksTheWindowQuestion(t *testing.T) {
	accept := answerOf("accept", map[string]any{"window": "trailing_30d"}, nil)
	t.Run("investigate_with_interpretation", func(t *testing.T) {
		var args map[string]any
		require.NoError(t, json.Unmarshal(interpretationArgs(t, func(m map[string]any) { m["question"] = windowAskQuestion }), &args))
		run := investigateWindowRunWith(t, toolInvestigateWithInterpretation, args, accept, windowRunMeta{}, withInterpretationTool)
		require.Empty(t, run.asks)
		require.Equal(t, 1, run.calls)
		require.Empty(t, hostedEvidenceWindow(t, run.hosted))
		noWindowOutcomeLine(t, run.diag)
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
