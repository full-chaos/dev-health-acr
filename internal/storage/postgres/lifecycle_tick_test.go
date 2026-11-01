package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

func TestTheLifecycleTickStatementsHaveTheDesignedShape(t *testing.T) {
	for name, statement := range map[string]string{"expired": LifecycleTickExpiredSQL, "first touch": LifecycleTickFirstTouchSQL} {
		for _, want := range []string{"cc.revoked_at IS NULL", "FOR UPDATE OF cc SKIP LOCKED", "LIMIT $2", "NOT EXISTS", "acr.credential_lifecycle_outbox", "ORDER BY"} {
			if !strings.Contains(statement, want) {
				t.Errorf("%s statement lacks %q:\n%s", name, want, statement)
			}
		}
	}
	for _, want := range []string{"cc.expires_at IS NOT NULL", "cc.expires_at <= $1", "'acr_credential.expired'", "'acr_credential.revoked'"} {
		if !strings.Contains(LifecycleTickExpiredSQL, want) {
			t.Errorf("expired statement lacks %q", want)
		}
	}
	for _, want := range []string{"cc.expires_at IS NULL OR cc.expires_at > $1"} {
		if !strings.Contains(LifecycleTickFirstTouchSQL, want) {
			t.Errorf("first-touch statement lacks %q", want)
		}
	}
	if strings.Contains(LifecycleTickFirstTouchSQL, "'acr_credential.expired'") {
		t.Error("the first-touch statement asks whether the aggregate has ANY row, not a terminal one")
	}
	if !strings.Contains(LifecycleOutboxLatestEventSQL, "ORDER BY revision DESC LIMIT 1") || !strings.Contains(LifecycleOutboxLatestEventSQL, "aggregate_id = $1") {
		t.Errorf("the latest-event read is the aggregate's highest revision: %s", LifecycleOutboxLatestEventSQL)
	}
}

// The retention deletes never take an unrevoked credential before its expired
// event exists: a revoked row (legacy or not) goes as before, an unrevoked one
// waits for the tick.
func TestTheRetentionDeletesAreGatedOnTheExpiredEvent(t *testing.T) {
	for _, want := range []string{"revoked_at IS NOT NULL", "o.aggregate_id = cc.credential_id", "o.event_type = 'acr_credential.expired'"} {
		if !strings.Contains(RetentionDeleteGateSQL, want) {
			t.Errorf("the gate lacks %q: %s", want, RetentionDeleteGateSQL)
		}
	}
	if !strings.Contains(workloadCredentialPurgeSQL, RetentionDeleteGateSQL) {
		t.Errorf("the workload credential purge does not carry the gate:\n%s", workloadCredentialPurgeSQL)
	}
	for name, statement := range map[string]string{"delete": purgeEndedBrowserCredentialsSQL, "count": countEndedBrowserCredentialsSQL} {
		if !strings.Contains(statement, RetentionDeleteGateSQL) {
			t.Errorf("the ended browser credential %s statement does not carry the gate", name)
		}
	}
	if strings.Contains(purgeEndedBrowserFamiliesSQL, RetentionDeleteGateSQL) {
		t.Error("the family delete removes no credential and takes no gate")
	}
}

func TestTheTickSkipsWhatTheMapperRefusesAndNothingElse(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	expires := now.Add(-time.Hour)
	bad := purgeCredentialForTick("cred_tk_unit_bad", &expires)
	bad.Scopes = nil
	good := purgeCredentialForTick("cred_tk_unit_good", &expires)

	ledger := credentiallifecycle.NewLedger()
	skipped, err := appendTickExpired(context.Background(), ledger, bad, now)
	if err != nil || !skipped {
		t.Fatalf("a credential with no scopes: skipped=%v err=%v, want skipped and no error", skipped, err)
	}
	if rows := ledger.Rows(); len(rows) != 0 {
		t.Fatalf("a skipped credential writes no row, got %d", len(rows))
	}
	skipped, err = appendTickExpired(context.Background(), ledger, good, now)
	if err != nil || skipped || len(ledger.Rows()) != 2 {
		t.Fatalf("a normal credential: skipped=%v err=%v rows=%d, want its first touch and its expired row", skipped, err, len(ledger.Rows()))
	}

	live := purgeCredentialForTick("cred_tk_unit_live", nil)
	live.Scopes = nil
	skipped, err = ensureTickFirstTouch(context.Background(), ledger, live)
	if err != nil || !skipped {
		t.Fatalf("first touch of a credential with no scopes: skipped=%v err=%v", skipped, err)
	}

	boom := errors.New("insert failed")
	for name, sink := range map[string]credentiallifecycle.Sink{
		"a sink error":        failingTickSink{err: boom},
		"a revision conflict": failingTickSink{err: credentiallifecycle.ErrRevisionConflict},
	} {
		skipped, err = appendTickExpired(context.Background(), sink, good, now)
		if err == nil || skipped {
			t.Errorf("%s: skipped=%v err=%v: only a mapper refusal is skipped, a database error stays loud", name, skipped, err)
		}
	}
}

func purgeCredentialForTick(id string, expires *time.Time) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{
		CredentialID: id, OrgID: "aaaaaaaa-0000-4000-8000-000000000001", Name: "n", Scopes: []string{"context:read"},
		RepositoryScopes: []string{"acme/widgets"}, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: expires,
	}
}

// failingTickSink fails every insert with its error; reads see no row.
type failingTickSink struct{ err error }

func (failingTickSink) MaxRevision(context.Context, string) (int, error)        { return 0, nil }
func (failingTickSink) LatestEventType(context.Context, string) (string, error) { return "", nil }
func (s failingTickSink) Insert(context.Context, credentiallifecycle.Row) error { return s.err }
