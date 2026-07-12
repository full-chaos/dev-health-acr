package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// These tests prove the CRITICAL boundary decodeExact alone cannot cover:
// a response that decodes as structurally valid JSON (no unknown fields,
// no trailing content) but is semantically incomplete or invalid (a
// missing required field, an unrecognized enum value, an out-of-range
// number) must still be rejected by validateCapabilities/
// validateContextPacket/validateExpandedEvidence, not silently accepted.

func serveCapabilities(t *testing.T, fixture contractsv1.Capabilities) (contractsv1.Capabilities, error) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, fixture)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client.Capabilities(context.Background())
}

func serveContextPacket(t *testing.T, fixture contractsv1.ContextPacket) (contractsv1.ContextPacket, error) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, fixture)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client.ContextPacket(context.Background(), validContextPacketRequest())
}

func serveEvidence(t *testing.T, fixture contractsv1.ExpandedEvidence) (contractsv1.ExpandedEvidence, error) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, fixture)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	return client.Evidence(context.Background(), "ev_abc123")
}

func TestClientRejectsCapabilitiesMissingService(t *testing.T) {
	fixture := validCapabilitiesFixture()
	fixture.Service = ""
	if _, err := serveCapabilities(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a capabilities response missing service was accepted: %v", err)
	}
}

func TestClientRejectsCapabilitiesLimitsOutOfRange(t *testing.T) {
	fixture := validCapabilitiesFixture()
	fixture.Limits.MaxItems = 0
	if _, err := serveCapabilities(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a capabilities response with an out-of-range limit was accepted: %v", err)
	}
}

func TestClientRejectsCapabilitiesWrongSchemaVersion(t *testing.T) {
	fixture := validCapabilitiesFixture()
	fixture.SchemaVersion = "capabilities.v0"
	if _, err := serveCapabilities(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a capabilities response with the wrong schema_version was accepted: %v", err)
	}
}

func TestClientAcceptsFullyValidCapabilities(t *testing.T) {
	if _, err := serveCapabilities(t, validCapabilitiesFixture()); err != nil {
		t.Fatalf("a fully valid capabilities response was rejected: %v", err)
	}
}

func TestClientRejectsContextPacketMissingStatus(t *testing.T) {
	fixture := validContextPacketFixture("req_12345678")
	fixture.Status = ""
	if _, err := serveContextPacket(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a context packet missing status was accepted: %v", err)
	}
}

func TestClientRejectsContextPacketMissingResolvedScope(t *testing.T) {
	fixture := validContextPacketFixture("req_12345678")
	fixture.ResolvedScope.RepoSlug = ""
	if _, err := serveContextPacket(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a context packet missing resolved_scope.repo_slug was accepted: %v", err)
	}
}

func TestClientRejectsContextPacketWithInvalidItem(t *testing.T) {
	fixture := validContextPacketFixture("req_12345678")
	fixture.Items = []contractsv1.ContextPacketItem{{
		SchemaVersion: contractsv1.ContextPacketItemSchema, PacketItemID: "item_00000001",
		Category: contractsv1.CategoryCause, ClaimKind: contractsv1.ClaimInferred,
		Title: "t", Summary: "s", WhyIncluded: "w", RuleID: "r",
		Confidence:      5.0, // out of [0,1]; ContextPacketItem.Validate() must reject this
		Severity:        contractsv1.SeverityInfo,
		RelatedEntities: []contractsv1.RelatedEntity{},
		EvidenceRefIDs:  []string{},
	}}
	if _, err := serveContextPacket(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a context packet with an out-of-bounds item confidence was accepted: %v", err)
	}
}

func TestClientRejectsContextPacketMissingItemsList(t *testing.T) {
	fixture := validContextPacketFixture("req_12345678")
	fixture.Items = nil
	if _, err := serveContextPacket(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("a context packet with a missing items list was accepted: %v", err)
	}
}

func TestClientAcceptsFullyValidContextPacket(t *testing.T) {
	if _, err := serveContextPacket(t, validContextPacketFixture("req_12345678")); err != nil {
		t.Fatalf("a fully valid context packet was rejected: %v", err)
	}
}

func TestClientRejectsEvidenceInvalidAvailability(t *testing.T) {
	fixture := validExpandedEvidenceFixture("ev_abc123")
	fixture.Availability = "bogus_state"
	if _, err := serveEvidence(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("evidence with an unrecognized availability value was accepted: %v", err)
	}
}

func TestClientRejectsEvidenceConfidenceOutOfRange(t *testing.T) {
	fixture := validExpandedEvidenceFixture("ev_abc123")
	fixture.Evidence.Confidence = 1.5 // valid JSON number, but out of the [0,1] contract bound
	if _, err := serveEvidence(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("evidence with an out-of-range confidence was accepted: %v", err)
	}
}

func TestClientRejectsEvidenceMissingStructuredFields(t *testing.T) {
	fixture := validExpandedEvidenceFixture("ev_abc123")
	fixture.Structured = nil
	if _, err := serveEvidence(t, fixture); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("evidence with a missing structured_fields map was accepted: %v", err)
	}
}

func TestClientAcceptsFullyValidEvidence(t *testing.T) {
	if _, err := serveEvidence(t, validExpandedEvidenceFixture("ev_abc123")); err != nil {
		t.Fatalf("fully valid evidence was rejected: %v", err)
	}
}
