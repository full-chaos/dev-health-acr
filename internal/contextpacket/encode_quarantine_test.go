package contextpacket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

type emptyLocatorEvidenceClient struct{}

func (emptyLocatorEvidenceClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if statement == contextpacket.RepositoryScopeQueryV1 {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service", "main"}}}, nil
	}
	if !strings.Contains(statement, "FROM ci_pipeline_runs AS c FINAL") || strings.Contains(statement, "max(") {
		return &rowScanner{}, nil
	}
	observed := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	return &rowScanner{rows: [][]any{
		{"acr:v1:ci:run-good", "dev_health", "ci_pipeline_run", "run-good", "CI run-good", "", "native", 1.0, "passed", observed},
		{"", "dev_health", "ci_pipeline_run", "run-no-locator", "CI run-no-locator", "", "native", 1.0, "passed", observed.Add(time.Minute)},
	}}, nil
}

func TestAssemblerServesTheOtherRowsWhenOneEvidenceRowHasNoLocator(t *testing.T) {
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(contextpacket.NewCatalogClickHouseRows(emptyLocatorEvidenceClient{}), contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	var output bytes.Buffer
	options := fixedOptions()
	options.Observer = observability.NewAssemblyObserver(observability.NewHooks(observability.NewSlogSink(slog.New(slog.NewTextHandler(&output, nil))), nil))

	packet, err := contextpacket.NewAssembler(store, options).Assemble(context.Background(), fixturePrincipal(), fixtureRequest("req-empty-locator", "main", ""))

	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	if strings.Contains(string(encoded), "run-no-locator") {
		t.Fatalf("the row with no locator was served: %s", encoded)
	}
	served := 0
	for _, item := range packet.Items {
		for _, handle := range item.EvidenceRefIDs {
			if strings.HasPrefix(handle, "ev2_") {
				served++
			}
		}
	}
	if served == 0 || !strings.Contains(string(encoded), "run-good") {
		t.Fatalf("packet status=%q items=%d reasons=%v: the row with a locator was not served", packet.Status, len(packet.Items), packet.Coverage.DegradedReasons)
	}
	if packet.Status != contractsv1.PacketPartial || !packet.Coverage.Partial {
		t.Fatalf("packet status = %q partial = %t, want partial: one row was dropped", packet.Status, packet.Coverage.Partial)
	}
	const reason = "evidence_data_invalid:ci_pipeline_runs.v1:invalid_shape"
	if !contains(packet.Coverage.DegradedReasons, reason) || !contains(packet.Warnings, reason) {
		t.Fatalf("degraded reasons = %v warnings = %v, want %q naming the dropped row", packet.Coverage.DegradedReasons, packet.Warnings, reason)
	}
	logged := output.String()
	if !strings.Contains(logged, "evidence rows quarantined") || !strings.Contains(logged, "source=ci_pipeline_runs.v1") || !strings.Contains(logged, "rule_code=invalid_shape") || !strings.Contains(logged, "dropped_rows=1") {
		t.Fatalf("quarantine log = %q, want one counted dropped row", logged)
	}
	if strings.Contains(logged, "run-no-locator") {
		t.Fatalf("quarantine log leaked row content: %q", logged)
	}
}

func TestClickHouseEvidenceStoreNeverServesARawLocatorItCouldNotEncode(t *testing.T) {
	observed := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	good := testEvidence("acr:v1:ci:run-good", "ci", observed)
	good.SourceVersion = "ci_pipeline_runs.v1"
	unknownSource := testEvidence("acr:v1:ci:raw-locator-of-an-unknown-source", "ci", observed)
	unknownSource.SourceVersion = "not_a_catalog_query.v1"
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(&bundleRows{evidence: []contractsv1.EvidenceRef{good, unknownSource}}, contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}

	bundle, err := store.ContextForTask(context.Background(), fixturePrincipal(), fixtureRequest("req-unknown-source", "main", ""))

	if err != nil {
		t.Fatalf("ContextForTask error = %v, want the encodable row kept", err)
	}
	if len(bundle.Evidence) != 2 || !strings.HasPrefix(bundle.Evidence[0].EvidenceRefID, "ev2_") {
		t.Fatalf("bundle evidence = %#v, want the encodable row with a handle", bundle.Evidence)
	}
	if bundle.Evidence[1].EvidenceRefID != "" {
		t.Fatalf("unencodable row kept locator %q, want none", bundle.Evidence[1].EvidenceRefID)
	}
}
