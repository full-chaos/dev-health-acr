package dbacl

import (
	"fmt"
	"strings"
)

// RetentionGrants are the privileges of the Ask Dev retention job's own role
// (CHAOS-11952, lead ruling: least privilege, not a widened runtime role).
// acr-api runs the job on a pool of its own from ACR_POSTGRES_RETENTION_DSN,
// opened only when ACR_ASK_DEV_RETENTION_ENABLED is on; grant-runtime-acl
// gives that role exactly this set (a role of its own: never the runtime
// role). Every SELECT is there because the statement's WHERE reads the
// table; the event log and the feedback rows are also removed by their
// foreign keys' cascades, which run as the table owner. The job takes no row
// lock on the thread rows (the organization's admission lock serializes it
// with admission and delete), so the threads need no UPDATE.
// cmd/acr-migrate TestRetentionRoleNeedsEveryGrantAndNothingMore runs the
// whole job as a role holding exactly this set, then with each privilege
// revoked in turn: every one is needed.
var RetentionGrants = []Grant{
	{Table: "context_fabric_threads", Privileges: []string{"SELECT", "DELETE"},
		Why: "SELECT the due threads and the unit's thread row, DELETE an expired thread: internal/contextfabric/pginvestigation/retention.go dueRetentionThreads, sweepRetentionThread"},
	{Table: "context_fabric_investigation_submissions", Privileges: []string{"SELECT", "UPDATE", "DELETE"},
		Why: "SELECT the unit's records (FOR UPDATE, which needs UPDATE) and the live-record rule, UPDATE the T1 erase, DELETE the expired thread's records and a record with no thread: pginvestigation/retention.go"},
	{Table: "context_fabric_investigation_results", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE the results no live record names (its WHERE reads the row): pginvestigation/retention.go removeRetainedResults; the feedback goes by cascade, as the owner"},
	{Table: "context_fabric_investigation_run_events", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE a deleted thread's event logs before the erase rewrites the reserved ids: pginvestigation/retention.go sweepRetentionThread; an expired record's log goes by cascade, as the owner"},
	{Table: "context_fabric_clarification_selections", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE the selections made on a removed result: pginvestigation/retention.go removeRetainedResults"},
	{Table: "context_fabric_structure_selections", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE the selections made on a removed result: pginvestigation/retention.go removeRetainedResults"},
	{Table: "context_fabric_structure_supersession_claims", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE the claims on a removed result: pginvestigation/retention.go removeRetainedResults"},
}

// RetentionGrantStatements are the bare idempotent GRANTs of RetentionGrants
// to quotedRole.
func RetentionGrantStatements(quotedRole string) []Statement {
	return grantStatements(RetentionGrants, quotedRole, nil)
}

// Markers bound the generated retention-role region of the compose script.
const (
	ComposeRetentionBeginMarker = "-- BEGIN GENERATED RETENTION GRANTS (internal/dbacl; regenerate with DBACL_WRITE=1 go test ./internal/dbacl)"
	ComposeRetentionEndMarker   = "-- END GENERATED RETENTION GRANTS"
)

// RenderComposeRetention returns the psql block that gives the retention role
// its declared privileges: inside a psql \if on retention_enabled, per table a
// REVOKE then the declared GRANT. The role exists only when the operator
// names one.
func RenderComposeRetention() string {
	var out strings.Builder
	out.WriteString(ComposeRetentionBeginMarker + "\n\\if :retention_enabled\n")
	for _, grant := range RetentionGrants {
		fmt.Fprintf(&out, "-- %s\n", grant.Why)
		fmt.Fprintf(&out, "REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.%s FROM :\"retention_user\";\n", grant.Table)
		fmt.Fprintf(&out, "GRANT %s ON TABLE acr.%s TO :\"retention_user\";\n", strings.Join(grant.Privileges, ", "), grant.Table)
	}
	out.WriteString("\\endif\n" + ComposeRetentionEndMarker + "\n")
	return out.String()
}
