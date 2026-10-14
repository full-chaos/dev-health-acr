package orgpurge

import (
	"os"
	"strings"
	"testing"
)

// Run takes the organization's session lock, then starts the transaction, then
// takes the write fence lock inside it as a transaction lock. This reads the
// source so that reordering them, or taking the fence as a session lock outside
// a transaction, fails here without a database.
func TestRunTakesTheOrgLockThenTheTransactionThenTheTransactionScopedFenceLock(t *testing.T) {
	raw, err := os.ReadFile("purge.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	run := source[strings.Index(source, "func (p *Purger) Run("):]
	remove := source[strings.Index(source, "func (p *Purger) removeOrganization("):]
	orgLock := strings.Index(run, "p.Locker.Lock(")
	beginTx := strings.Index(remove, "BeginTx(")
	fence := strings.Index(remove, "p.lockWriteFence(")
	if orgLock < 0 || beginTx < 0 || fence < 0 {
		t.Fatalf("lock order markers not found: org lock %d, BeginTx %d, fence %d", orgLock, beginTx, fence)
	}
	if !(strings.Index(source, "p.removeOrganization(ctx") > strings.Index(source, "p.Locker.Lock(")) {
		t.Fatal("the organization lock must be taken before removeOrganization runs")
	}
	if !(beginTx < fence) {
		t.Fatal("the write fence lock must be taken after BeginTx, inside the transaction")
	}
	fenceBody := source[strings.Index(source, "func (p *Purger) lockWriteFence("):]
	if !strings.Contains(fenceBody, "pg_advisory_xact_lock") || strings.Contains(fenceBody, "pg_advisory_lock(") || strings.Contains(fenceBody, "pg_try_advisory_lock(") {
		t.Fatal("the write fence lock must be a transaction-scoped advisory lock")
	}
}
