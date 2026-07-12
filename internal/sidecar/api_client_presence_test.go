package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// These tests prove the parity gap requiredFieldsPresent
// (api_client_presence.go) closes: a response field whose zero value is
// itself a legitimate value (a false permission, an empty summary, a 0.0
// confidence) must still be rejected when the field is entirely absent
// or explicit JSON null, even though decodeExact accepts the shape and
// canonical Validate() accepts the resulting Go zero value. Every case
// below starts from a golden, schema-valid fixture and prunes exactly
// one field at the wire level (never through Go struct assignment, since
// that could never produce "absent" for a non-omitempty field), so a
// regression that widens requiredFieldsPresent's blind spot is caught
// here first.

// pruneJSONField deletes the JSON key or array index named by the last
// path segment, navigating object keys and array indices to get there.
// It operates on raw bytes (never round-tripping through a Go struct)
// so it can produce a payload no ordinary struct marshal could: a
// non-omitempty field's key genuinely missing from the wire.
func pruneJSONField(t *testing.T, raw []byte, path ...string) []byte {
	t.Helper()
	if len(path) == 0 {
		t.Fatal("pruneJSONField requires at least one path segment")
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return pruneJSONArrayField(t, raw, path...)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode JSON object at %q: %v", path[0], err)
	}
	if _, ok := obj[path[0]]; !ok {
		t.Fatalf("path segment %q not found in object", path[0])
	}
	if len(path) == 1 {
		delete(obj, path[0])
	} else {
		obj[path[0]] = pruneJSONField(t, obj[path[0]], path[1:]...)
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("encode JSON object: %v", err)
	}
	return encoded
}

func pruneJSONArrayField(t *testing.T, raw []byte, path ...string) []byte {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("decode JSON array at %q: %v", path[0], err)
	}
	index, err := strconv.Atoi(path[0])
	if err != nil || index < 0 || index >= len(arr) {
		t.Fatalf("invalid array index %q for array of length %d", path[0], len(arr))
	}
	if len(path) == 1 {
		t.Fatal("pruneJSONField targets an object key inside an array element, not the element itself")
	}
	arr[index] = pruneJSONField(t, arr[index], path[1:]...)
	encoded, err := json.Marshal(arr)
	if err != nil {
		t.Fatalf("encode JSON array: %v", err)
	}
	return encoded
}

func readGoldenFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(contractFixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func serveRawCapabilities(t *testing.T, raw []byte) (contractsv1.Capabilities, error) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client.Capabilities(context.Background())
}

func serveRawContextPacket(t *testing.T, raw []byte) (contractsv1.ContextPacket, error) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client.ContextPacket(context.Background(), validContextPacketRequest())
}

func serveRawEvidence(t *testing.T, raw []byte) (contractsv1.ExpandedEvidence, error) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client.Evidence(context.Background(), "ev_abc123")
}

func TestClientRejectsCapabilitiesOmittedZeroValidRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		path []string
	}{
		{name: "top_level_permissions_absent", path: []string{"permissions"}},
		{name: "top_level_entitlements_absent", path: []string{"entitlements"}},
		{name: "nested_permissions_episode_write_absent", path: []string{"permissions", "episode_write"}},
		{name: "nested_entitlements_agent_context_runtime_absent", path: []string{"entitlements", "agent_context_runtime"}},
	}
	golden := readGoldenFixture(t, "capabilities.v1.json")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pruned := pruneJSONField(t, golden, tc.path...)
			if _, err := serveRawCapabilities(t, pruned); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("a capabilities response missing %v (a zero-valid field) was accepted: %v", tc.path, err)
			}
		})
	}
}

func TestClientRejectsContextPacketOmittedZeroValidRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		path []string
	}{
		{name: "top_level_summary_absent", path: []string{"summary"}},
		{name: "top_level_requested_scope_absent", path: []string{"requested_scope"}},
		{name: "nested_item_flags_absent", path: []string{"items", "0", "flags"}},
		{name: "nested_item_confidence_absent", path: []string{"items", "0", "confidence"}},
	}
	golden := readGoldenFixture(t, "context_packet.v1.json")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pruned := pruneJSONField(t, golden, tc.path...)
			if _, err := serveRawContextPacket(t, pruned); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("a context packet response missing %v (a zero-valid field) was accepted: %v", tc.path, err)
			}
		})
	}
}

func TestClientRejectsEvidenceOmittedZeroValidRequiredFields(t *testing.T) {
	golden := readGoldenFixture(t, "expanded_evidence.v1.json")
	pruned := pruneJSONField(t, golden, "evidence", "confidence")
	if _, err := serveRawEvidence(t, pruned); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("an evidence response missing evidence.confidence (a zero-valid field) was accepted: %v", err)
	}
}

// TestClientAcceptsResponsesWithLegitimateZeroValuesPresent is
// requiredFieldsPresent's converse proof: none of the fields above are
// rejected because they are zero, only because they are absent. Every
// field pruned above is set to its legitimate zero value here instead
// (still present on the wire, via ordinary struct marshaling) and must
// be accepted exactly as before this change.
func TestClientAcceptsResponsesWithLegitimateZeroValuesPresent(t *testing.T) {
	capabilities := validCapabilitiesFixture()
	capabilities.Permissions = contractsv1.CapabilityPermissions{}
	capabilities.Entitlements = contractsv1.CapabilityEntitlements{}
	if _, err := serveCapabilities(t, capabilities); err != nil {
		t.Fatalf("capabilities with present-but-false permissions/entitlements were rejected: %v", err)
	}

	packet := validContextPacketFixture("req_12345678")
	packet.Summary = ""
	packet.RequestedScope = contractsv1.RequestedScope{}
	if _, err := serveContextPacket(t, packet); err != nil {
		t.Fatalf("a context packet with a present-but-empty summary/requested_scope was rejected: %v", err)
	}

	evidence := validExpandedEvidenceFixture("ev_abc123")
	evidence.Evidence.Confidence = 0
	if _, err := serveEvidence(t, evidence); err != nil {
		t.Fatalf("evidence with a present-but-zero confidence was rejected: %v", err)
	}
}
