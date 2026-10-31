package dbacl

// RetentionPrivileges are what the Ask Dev retention job's statements need
// (CHAOS-11952). The job runs in acr-api as the runtime role (CHAOS-12244:
// no role of its own), so RuntimeGrants holds every one of them
// (TestRuntimeGrantsHoldTheRetentionPrivileges); this list is not granted to
// any role by itself. Every SELECT is there because the statement's WHERE
// reads the table; the event log and the feedback rows are also removed by
// their foreign keys' cascades, which run as the table owner. The job takes
// no row lock on the thread rows (the organization's admission lock
// serializes it with admission and delete), so the threads need no UPDATE.
// cmd/acr-migrate TestRuntimeRoleRunsTheAskDevRetention runs the whole job as
// the runtime role, then with each of these privileges revoked in turn:
// every one is needed.
var RetentionPrivileges = []Grant{
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
