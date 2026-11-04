package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func revokedAuditEvents(h *oauthHarness, credentialID string) []storage.AuditEvent {
	var out []storage.AuditEvent
	for _, e := range h.audit.Events() {
		if e.Action == storage.AuditActionCredentialRevoked && e.ResourceID == credentialID {
			out = append(out, e)
		}
	}
	return out
}

// CHAOS-12825: the audit trail of a revoke by presented token.
func TestRevokePresentedAccessTokenRecordsOneAuditEventAndNoMore(t *testing.T) {
	h := newOAuthHarness(t)
	issued, clientID := oauthIssuedCredential(t, h)
	service, err := NewService(h.creds, ServiceOptions{Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	id := issued.Credential.CredentialID
	ctx := context.Background()

	got, err := service.RevokePresentedAccessToken(ctx, issued.Token, "acrc_other")
	if err != nil || got.Outcome != oauthvocab.OutcomeClientMismatch || len(revokedAuditEvents(h, id)) != 0 {
		t.Fatalf("other client: %+v err=%v audit=%d, want client_mismatch and no audit", got, err, len(revokedAuditEvents(h, id)))
	}
	got, err = service.RevokePresentedAccessToken(ctx, issued.Token, clientID)
	events := revokedAuditEvents(h, id)
	if actor := lastLifecycleActor(t, h, id); actor != "credential" {
		t.Fatalf("lifecycle outbox actor_type = %q, want credential (as for self/revoke)", actor)
	}
	if err != nil || got.Outcome != oauthvocab.OutcomeOK || len(events) != 1 || events[0].ActorID != id {
		t.Fatalf("owner: %+v err=%v audit=%+v, want ok and one event with the credential as actor", got, err, events)
	}
	got, err = service.RevokePresentedAccessToken(ctx, issued.Token, clientID)
	if err != nil || got.Outcome != oauthvocab.OutcomeInvalidGrant || len(revokedAuditEvents(h, id)) != 1 {
		t.Fatalf("second revoke: %+v err=%v audit=%d, want invalid_grant and still one event", got, err, len(revokedAuditEvents(h, id)))
	}
}

func lastLifecycleActor(t *testing.T, h *oauthHarness, id string) string {
	t.Helper()
	actor := ""
	for _, row := range h.audit.LifecycleOutbox() {
		if row.AggregateID != id {
			continue
		}
		var payload struct {
			Payload struct {
				ActorType string `json:"actor_type"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		actor = payload.Payload.ActorType
	}
	return actor
}
