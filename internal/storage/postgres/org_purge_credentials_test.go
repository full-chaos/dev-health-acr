package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

const purgeOrg = "aaaaaaaa-0000-4000-8000-000000000001"

func purgeCredential(id string, mutate func(*contractsv1.ClientCredential)) contractsv1.ClientCredential {
	c := contractsv1.ClientCredential{
		CredentialID: id, OrgID: purgeOrg, Name: "n", TokenPrefix: "fcacr_secretsecret", Scopes: []string{"context:read"},
		RepositoryScopes: []string{"acme/widgets"}, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func TestCredentialLiveAtIsOnePredicate(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	for name, tc := range map[string]struct {
		mutate func(*contractsv1.ClientCredential)
		want   bool
	}{
		"no expiry":                 {nil, true},
		"expires in the future":     {func(c *contractsv1.ClientCredential) { c.ExpiresAt = &future }, true},
		"expires exactly now":       {func(c *contractsv1.ClientCredential) { c.ExpiresAt = &now }, false},
		"expired":                   {func(c *contractsv1.ClientCredential) { c.ExpiresAt = &past }, false},
		"revoked":                   {func(c *contractsv1.ClientCredential) { c.RevokedAt = &past }, false},
		"revoked, not yet expired":  {func(c *contractsv1.ClientCredential) { c.RevokedAt = &past; c.ExpiresAt = &future }, false},
		"revoked in the future too": {func(c *contractsv1.ClientCredential) { c.RevokedAt = &future }, false},
	} {
		if got := CredentialLiveAt(purgeCredential("cred_a", tc.mutate), now); got != tc.want {
			t.Errorf("%s: live = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPurgeRevocationsWriteOneRowPerLiveCredentialWithFirstTouchFirst(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	rows := []contractsv1.ClientCredential{
		purgeCredential("cred_c_live", nil),
		purgeCredential("cred_a_live", func(c *contractsv1.ClientCredential) { c.ExpiresAt = &future }),
		purgeCredential("cred_b_revoked", func(c *contractsv1.ClientCredential) { c.RevokedAt = &past }),
		purgeCredential("cred_d_expired", func(c *contractsv1.ClientCredential) { c.ExpiresAt = &past }),
	}
	ledger := credentiallifecycle.NewLedger()
	n, err := appendLiveRevocations(context.Background(), ledger, rows, now)
	if err != nil || n != 2 {
		t.Fatalf("appended %d, %v; want 2", n, err)
	}
	got := ledger.Rows()
	if len(got) != 4 {
		t.Fatalf("rows = %d, want a first touch and a revoked row for each of the two live credentials", len(got))
	}
	type shape struct {
		id  string
		rev int
		ev  string
	}
	want := []shape{{"cred_a_live", 1, "acr_credential.issued"}, {"cred_a_live", 2, "acr_credential.revoked"}, {"cred_c_live", 1, "acr_credential.issued"}, {"cred_c_live", 2, "acr_credential.revoked"}}
	for i, row := range got {
		if (shape{row.AggregateID, row.Revision, row.EventType}) != want[i] {
			t.Errorf("row %d = %+v, want %+v (credential id order, first touch before the event)", i, shape{row.AggregateID, row.Revision, row.EventType}, want[i])
		}
	}
	var event struct {
		CreatedAt string `json:"created_at"`
		Payload   struct {
			ActorType string `json:"actor_type"`
			ActorRef  string `json:"actor_ref"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(got[1].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Payload.ActorType != "system" || event.Payload.ActorRef != "" || event.CreatedAt != "2026-11-01T12:00:00Z" {
		t.Errorf("revoked event = %s, want system, no ref, the purge time", got[1].Payload)
	}
	for _, row := range got {
		if strings.Contains(string(row.Payload), "secretsecret") {
			t.Errorf("a token prefix reached a row: %s", row.Payload)
		}
	}
}

func TestPurgeRevocationsFailLoudlyAndWriteNothingPastTheFailure(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	rows := []contractsv1.ClientCredential{purgeCredential("cred_a", func(c *contractsv1.ClientCredential) { c.OrgID = "a b" })}
	if n, err := appendLiveRevocations(context.Background(), credentiallifecycle.NewLedger(), rows, now); err == nil || n != 0 {
		t.Fatalf("n=%d err=%v: a credential the mapper refuses must fail the purge", n, err)
	}
}

func TestPurgeSQLConstantsShareOneColumnList(t *testing.T) {
	for name, statement := range map[string]string{"delete": OrgPurgeCredentialsDeleteSQL, "select": OrgPurgeCredentialsSelectSQL} {
		if !strings.Contains(statement, credentialColumnList) {
			t.Errorf("%s statement does not carry the scanCredential column list", name)
		}
		if !strings.Contains(statement, "org_id = $1::uuid") {
			t.Errorf("%s statement is not scoped to the organization", name)
		}
	}
	if !strings.HasPrefix(OrgPurgeCredentialsDeleteSQL, "DELETE FROM acr.client_credentials") || !strings.Contains(OrgPurgeCredentialsDeleteSQL, "RETURNING "+credentialColumnList) {
		t.Errorf("the purge takes the rows with DELETE ... RETURNING: %s", OrgPurgeCredentialsDeleteSQL)
	}
	if !strings.HasPrefix(OrgPurgeCredentialsSelectSQL, "SELECT ") || strings.Contains(OrgPurgeCredentialsSelectSQL, "FOR UPDATE") {
		t.Errorf("the dry run is a plain SELECT: %s", OrgPurgeCredentialsSelectSQL)
	}
	if !strings.Contains(LifecycleOutboxMaxRevisionSQL, "acr.credential_lifecycle_outbox") || !strings.Contains(LifecycleOutboxInsertSQL, "INSERT INTO acr.credential_lifecycle_outbox") {
		t.Errorf("the outbox statements must be exported for the purge's privilege derivation")
	}
}
