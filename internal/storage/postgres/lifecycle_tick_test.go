package postgres

import (
	"strings"
	"testing"
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
