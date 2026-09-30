package guidegen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type guideGraphQLClient struct{ calls int }

func (c *guideGraphQLClient) Execute(_ context.Context, call directread.QueryCall) (directread.QueryResult, error) {
	c.calls++
	return directread.QueryResult{Body: []byte(`{"data":{"hotspots":{"rows":[]}}}`), StatusCode: 200}, nil
}

// The guide's graphql_query example runs through the real graphql_query
// runner (the policy, the validator, the rebuild and the SDL checks) and
// is served: an example the runner would refuse cannot ship. The root table
// lists every allowed root.
func TestGraphQLGuideExampleIsServedByTheRealRunner(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	client := &guideGraphQLClient{}
	runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: directread.NewSubjectGate(exGraph{}, nil), Client: client})
	if err != nil {
		t.Fatal(err)
	}
	var args struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(GraphQLExample.Args), &args); err != nil {
		t.Fatal(err)
	}
	vars, _ := json.Marshal(args.Variables)
	resp, err := runner.Run(context.Background(), storage.Principal{OrgID: exOrg, Subject: "user-x", CredentialID: "cred-x"}, directread.GraphQLRequest{Query: args.Query, Variables: vars})
	if err != nil || resp.Call != directread.CallServed || client.calls != 1 {
		raw, _ := json.Marshal(resp)
		t.Fatalf("the guide example is not served: %v %s", err, raw)
	}
	text, err := buildData(FromRegistries())
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range policy.Roots() {
		if !strings.Contains(text, "| `"+root.Field+"` |") {
			t.Errorf("root %s is not in the guide table", root.Field)
		}
	}
}
