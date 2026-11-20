package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type elicitCall struct{ message, schema string }

func elicitFindSubjects(t *testing.T, opts *mcpsdk.ClientOptions) (structured string, text string) {
	t.Helper()
	structured, text, _ = elicitFindSubjectsLogged(t, opts)
	return structured, text
}

func elicitFindSubjectsLogged(t *testing.T, opts *mcpsdk.ClientOptions) (structured string, text string, diagnostics string) {
	t.Helper()
	st := newDTStack(t)
	var buf bytes.Buffer
	cs, _ := st.sessionWithDiagnostics([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, opts, &buf)
	result, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "find_subjects", Arguments: map[string]any{"query": dtSlugSame, "kind": "repository"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			text += tc.Text
		}
	}
	b, _ := json.Marshal(result.StructuredContent)
	return string(b), text, buf.String()
}

func withSameLabelRepos(t *testing.T) {
	dtSameLabelRepos = true
	t.Cleanup(func() { dtSameLabelRepos = false })
}

func TestFindSubjectsAmbiguousElicitation(t *testing.T) {
	withSameLabelRepos(t)
	baseStruct, baseText := elicitFindSubjects(t, nil)
	var base map[string]any
	if err := json.Unmarshal([]byte(baseStruct), &base); err != nil {
		t.Fatal(err)
	}
	if base["status"] != "ambiguous" {
		t.Fatalf("base must be ambiguous: %s", baseStruct)
	}
	t.Logf("BASE (no elicitation capability): %s", baseStruct)

	handler := func(action string, content map[string]any, calls *[]elicitCall, err error) *mcpsdk.ClientOptions {
		return &mcpsdk.ClientOptions{ElicitationHandler: func(_ context.Context, r *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			schema, _ := json.Marshal(r.Params.RequestedSchema)
			*calls = append(*calls, elicitCall{r.Params.Message, string(schema)})
			if err != nil {
				return nil, err
			}
			return &mcpsdk.ElicitResult{Action: action, Content: content}, nil
		}}
	}

	for name, tc := range map[string]struct {
		action  string
		content map[string]any
		err     error
		outcome string
	}{
		"decline":            {action: "decline", outcome: "declined"},
		"cancel":             {action: "cancel", outcome: "declined"},
		"client error":       {err: errors.New("boom"), outcome: "unavailable"},
		"accept not offered": {action: "accept", content: map[string]any{"subject": "repository:" + dtRepoA}, outcome: "unavailable"},
	} {
		t.Run(name+" is byte-equal to base", func(t *testing.T) {
			withSameLabelRepos(t)
			var calls []elicitCall
			gotStruct, gotText, diag := elicitFindSubjectsLogged(t, handler(tc.action, tc.content, &calls, tc.err))
			log, err := certify.Parse([]byte(diag))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{
				"tool": "find_subjects", "outcome": tc.outcome, "asked": true, "offered": float64(2),
			}}); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 {
				t.Fatalf("want 1 elicitation, got %d", len(calls))
			}
			if gotStruct != baseStruct || gotText != baseText {
				t.Fatalf("not byte-equal:\n%s\nvs\n%s", gotStruct, baseStruct)
			}
		})
	}

	t.Run("accept returns the picked subject", func(t *testing.T) {
		withSameLabelRepos(t)
		var calls []elicitCall
		picked := "repository:" + dtRepoD
		gotStruct, _, diag := elicitFindSubjectsLogged(t, handler("accept", map[string]any{"subject": picked}, &calls, nil))
		log, err := certify.Parse([]byte(diag))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPElicitationOutcome, Want: map[string]any{
			"tool": "find_subjects", "outcome": "accepted", "asked": true, "offered": float64(2),
		}}); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 {
			t.Fatalf("want 1 elicitation, got %d", len(calls))
		}
		t.Logf("ELICIT message=%q schema=%s", calls[0].message, calls[0].schema)
		t.Logf("ACCEPT: %s", gotStruct)
		var got struct {
			Status   string `json:"status"`
			Subjects []struct {
				CanonicalID string `json:"canonical_id"`
			} `json:"subjects"`
		}
		if err := json.Unmarshal([]byte(gotStruct), &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != "complete" || len(got.Subjects) != 1 || got.Subjects[0].CanonicalID != picked {
			t.Fatalf("bad narrowed answer: %s", gotStruct)
		}
	})

	t.Run("unambiguous query never elicits", func(t *testing.T) {
		withSameLabelRepos(t)
		var calls []elicitCall
		st := newDTStack(t)
		cs, _ := st.sessionWith([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, handler("accept", nil, &calls, nil))
		if _, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "find_subjects", Arguments: map[string]any{"query": dtSlugA, "kind": "repository"}}); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 0 {
			t.Fatalf("elicited on an unambiguous answer: %d", len(calls))
		}
	})
}

func TestFindSubjectsAmbiguousElicitationOverRealPaging(t *testing.T) {
	for name, tc := range map[string]struct {
		extra int
		limit int
		asks  int
	}{
		"26 matches default page": {extra: 24, limit: 0, asks: 0},
		"26 matches limit 2":      {extra: 24, limit: 2, asks: 0},
		"3 matches whole page":    {extra: 1, limit: 0, asks: 1},
	} {
		t.Run(name, func(t *testing.T) {
			withSameLabelRepos(t)
			dtExtraSameLabelRepos = tc.extra
			t.Cleanup(func() { dtExtraSameLabelRepos = 0 })
			args := map[string]any{"query": dtSlugSame, "kind": "repository"}
			if tc.limit > 0 {
				args["limit"] = tc.limit
			}
			call := func(opts *mcpsdk.ClientOptions) (string, int) {
				st := newDTStack(t)
				cs, _ := st.sessionWith([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, opts)
				result, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "find_subjects", Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(result.StructuredContent)
				return string(b), 0
			}
			base, _ := call(nil)
			var asks int
			got, _ := call(&mcpsdk.ClientOptions{ElicitationHandler: func(_ context.Context, r *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
				asks++
				props := r.Params.RequestedSchema.(map[string]any)["properties"].(map[string]any)["subject"].(map[string]any)["oneOf"].([]any)
				return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"subject": props[0].(map[string]any)["const"]}}, nil
			}})
			if asks != tc.asks {
				t.Fatalf("asks=%d want %d; base=%s", asks, tc.asks, base)
			}
			if tc.asks == 0 && got != base {
				t.Fatalf("bytes differ from base:\n%s\n%s", got, base)
			}
			t.Logf("%s: asks=%d base_status=%s", name, asks, base[:min(len(base), 160)])
		})
	}
}

func TestFindSubjectsMalformedRequestCapabilitiesFallBackToSession(t *testing.T) {
	for name, tc := range map[string]struct {
		withHandler bool
		wantAsks    int
	}{
		"session with elicitation is offered":     {true, 1},
		"session without elicitation not offered": {false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			withSameLabelRepos(t)
			asks := 0
			var opts *mcpsdk.ClientOptions
			if tc.withHandler {
				opts = &mcpsdk.ClientOptions{ElicitationHandler: func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
					asks++
					return &mcpsdk.ElicitResult{Action: "decline"}, nil
				}}
			}
			st := newDTStack(t)
			cs, _ := st.sessionWith([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, opts)
			params := &mcpsdk.CallToolParams{Name: "find_subjects", Arguments: map[string]any{"query": dtSlugSame, "kind": "repository"}}
			params.SetMeta(map[string]any{mcpsdk.MetaKeyClientCapabilities: "not an object"})
			if _, err := cs.CallTool(context.Background(), params); err != nil {
				t.Fatal(err)
			}
			if asks != tc.wantAsks {
				t.Fatalf("want %d elicitations, got %d", tc.wantAsks, asks)
			}
		})
	}
}
