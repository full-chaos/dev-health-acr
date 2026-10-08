package hosted

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type recordingQueryClient struct {
	calls  []directread.QueryCall
	result directread.QueryResult
	err    error
}

func (c *recordingQueryClient) Execute(_ context.Context, call directread.QueryCall) (directread.QueryResult, error) {
	c.calls = append(c.calls, call)
	return c.result, c.err
}

func sourceHealthRunner(t *testing.T, client directread.QueryClient) (*directread.OperationRunner, *directread.Catalogue) {
	t.Helper()
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: catalogue, Gate: directread.NewSubjectGate(nil, directread.NewSlogRecorder(slog.Default())), Client: client,
		Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, catalogue
}

const registeredSourceHealthDigest = "f9196883f03de7e9689294590f40c0216374a8301ed8712830ec4b9836034fc0"

// The fact reads the root through the real runner: the document sent is the
// vendored registered one (digest pinned), the org is the caller's, and the
// rows come back as a served outcome.
func TestFactOperationCallerSendsTheRegisteredSourceHealthDocument(t *testing.T) {
	client := &recordingQueryClient{result: directread.QueryResult{StatusCode: 200, Body: []byte(`{"data":{"sourceHealth":[{"provider":"github","scope":"all","lastSyncAt":"2026-10-08T01:00:00Z","lastFailure":null,"__typename":"SourceHealth"}]}}`)}}
	runner, catalogue := sourceHealthRunner(t, client)
	policy, refusal := catalogue.Lookup(devhealthfacts.SourceHealthOperationName)
	if refusal != nil || policy == nil {
		t.Fatalf("sourceHealth is not a served operation: %+v", refusal)
	}
	if policy.Digest != registeredSourceHealthDigest || directread.DocumentDigest(policy.DocumentText) != registeredSourceHealthDigest {
		t.Fatalf("policy digest = %s, want the registered %s", policy.Digest, registeredSourceHealthDigest)
	}
	outcome, err := newFactOperationCaller(runner).CallOperation(context.Background(), storage.Principal{OrgID: "11111111-1111-4111-8111-111111111111"}, devhealthfacts.SourceHealthOperationName)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Served || !outcome.Complete {
		t.Fatalf("outcome = %+v, want served and complete", outcome)
	}
	if len(client.calls) != 1 {
		t.Fatalf("query calls = %d, want 1", len(client.calls))
	}
	call := client.calls[0]
	if directread.DocumentDigest(call.Document) != registeredSourceHealthDigest {
		t.Fatalf("document sent has digest %s, want the registered %s", directread.DocumentDigest(call.Document), registeredSourceHealthDigest)
	}
	if call.OrgID != "11111111-1111-4111-8111-111111111111" || call.Variables["orgId"] != call.OrgID {
		t.Fatalf("call = %+v, want the principal org as orgId", call)
	}
	if !strings.Contains(string(outcome.Data), `"sourceHealth"`) {
		t.Fatalf("data = %s", outcome.Data)
	}
}

func TestFactOperationCallerReportsRootNotEnabledAsAClosedReason(t *testing.T) {
	runner, _ := sourceHealthRunner(t, &recordingQueryClient{err: &directread.QueryError{Class: directread.QueryErrorNotFound, StatusCode: 404, ListenerReason: directread.ListenerNotFoundRootNotEnabled}})
	outcome, err := newFactOperationCaller(runner).CallOperation(context.Background(), storage.Principal{OrgID: "11111111-1111-4111-8111-111111111111"}, devhealthfacts.SourceHealthOperationName)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Served || outcome.Reason != string(directread.CallOperationUnavailable) {
		t.Fatalf("outcome = %+v, want not served with a closed reason", outcome)
	}
}

func TestBindSourceHealthOperations(t *testing.T) {
	runner, _ := sourceHealthRunner(t, &recordingQueryClient{})
	cases := []struct {
		name                 string
		composed, configured bool
		runner               *directread.OperationRunner
		holder               *devhealthfacts.OperationHolder
		wantErr, wantSet     bool
	}{
		{"configured and composed, runner present", true, true, runner, devhealthfacts.NewOperationHolder(), false, true},
		{"configured and composed, runner missing", true, true, nil, devhealthfacts.NewOperationHolder(), true, false},
		{"configured and composed, holder missing", true, true, runner, nil, true, false},
		{"data query not configured", true, false, nil, devhealthfacts.NewOperationHolder(), false, false},
		{"no investigator", false, true, nil, devhealthfacts.NewOperationHolder(), false, false},
	}
	for _, tc := range cases {
		err := bindSourceHealthOperations(tc.holder, tc.composed, tc.configured, tc.runner)
		if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, errSourceHealthOperationsUnbound)) {
			t.Errorf("%s: err = %v, wantErr = %v", tc.name, err, tc.wantErr)
		}
		if tc.holder != nil && tc.holder.IsSet() != tc.wantSet {
			t.Errorf("%s: holder set = %v, want %v", tc.name, tc.holder.IsSet(), tc.wantSet)
		}
	}
}
