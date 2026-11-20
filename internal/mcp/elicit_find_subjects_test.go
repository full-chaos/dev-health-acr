package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type elicitCall struct{ message, schema string }

func elicitFindSubjects(t *testing.T, opts *mcpsdk.ClientOptions) (structured string, text string) {
	t.Helper()
	st := newDTStack(t)
	cs, _ := st.sessionWith([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, opts)
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
	return string(b), text
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
	}{
		"decline":            {action: "decline"},
		"cancel":             {action: "cancel"},
		"client error":       {err: errors.New("boom")},
		"accept not offered": {action: "accept", content: map[string]any{"subject": "repository:" + dtRepoA}},
	} {
		t.Run(name+" is byte-equal to base", func(t *testing.T) {
			withSameLabelRepos(t)
			var calls []elicitCall
			gotStruct, gotText := elicitFindSubjects(t, handler(tc.action, tc.content, &calls, tc.err))
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
		gotStruct, _ := elicitFindSubjects(t, handler("accept", map[string]any{"subject": picked}, &calls, nil))
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
			Population struct {
				Returned, TotalKnown int `json:"total_known"`
			} `json:"population"`
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
