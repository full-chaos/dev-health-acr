package modelruntimeresolver_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A per-organization runtime built by the resolver names its refused dial with
// that organization's eight-character prefix, through the logger the
// deployment default carries, and the line names no endpoint text.
func TestAResolvedOrgRuntimeNamesARefusedBYODialWithItsOrgPrefix(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	host, port, _ := net.SplitHostPort(address)

	logs := &syncBuffer{}
	defaults := modelprovider.Config{
		Timeout: 3 * time.Second, MaxAttempts: 1,
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	}
	configs := newOrgConfigStub(nil)
	const org = "0123456789abcdef"
	request := writeRequest("org-model")
	request.BaseURL = "https://" + address + "/v1"
	if _, err := configs.UpsertOrgModelConfig(context.Background(), storage.Principal{OrgID: org}, request); err != nil {
		t.Fatal(err)
	}
	resolver := modelruntimeresolver.New(nil, configs, modelruntimeresolver.NewModelProviderBuild(defaults))

	_, _, err = resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: org}, validInterpretRequest())
	if err == nil {
		t.Fatal("a call to a loopback base_url with the opt-out unset succeeded")
	}
	text := logs.String()
	if !strings.Contains(text, "reason=non_public_address") || !strings.Contains(text, "org_id=01234567") {
		t.Fatalf("no refusal line with the organization prefix: %q", text)
	}
	for _, forbidden := range []string{host, port, address, "/v1", "89abcdef", "sk-acme"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the logs hold %q: %s", forbidden, text)
		}
	}
}

func validInterpretRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_12345678", Question: "What is driving Ask Dev?",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
}
