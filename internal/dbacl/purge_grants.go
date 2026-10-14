package dbacl

// PurgeGrants is the privileges the org purge's own database role (acr_purge)
// holds. It is the purge's statements and nothing else: orgpurge.Privileges
// derives that set from the SQL Run executes, and a test requires this
// declaration to equal it plus the two rows in PurgeExtras. The runtime role is
// not widened for the purge.
var PurgeGrants = []Grant{
	{Table: "schema_migrations", Privileges: []string{"SELECT"},
		Why: "the purge command verifies the schema when it opens its runtime: cmd/acr-projector/runtime.go:261"},
	{Table: "client_credentials", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE of the organization's credentials (purge table list), SELECT in the device authorization predicate: internal/contextfabric/orgpurge/purge.go purgeTables"},
	{Table: "workload_bindings", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_packet_snapshots", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "audit_events", Privileges: []string{"SELECT", "INSERT"},
		Why: "the kept-events count (auditCountSQL) and the org_purged and org_purge_incomplete events (purgedAuditInsertSQL, incompleteAuditInsertSQL); never deleted"},
	{Table: "agent_episodes", Privileges: []string{"SELECT", "DELETE"},
		Why: "DELETE of non-legal-hold episodes, SELECT for the kept legal-hold count (legalHoldCountSQL)"},
	{Table: "device_authorizations", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables; cascades to its requests and grants as the owner"},
	{Table: "oauth_authorization_requests", Privileges: []string{"SELECT"}, Why: "read by the device authorization predicate and the cascade count; removed by cascade"},
	{Table: "oauth_device_grants", Privileges: []string{"SELECT"}, Why: "cascade count; removed by cascade"},
	{Table: "context_fabric_projection_checkpoints", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_projection_rebuild_markers", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_investigation_results", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_model_execution_receipts", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_org_model_config", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_reuse_invalidations", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_clarification_selections", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_structure_selections", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_structure_supersession_claims", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_structure_prior_pointer_history", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_structure_prior_pointer", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_structure_prior_revocations", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_structure_priors", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_graph_build_source_progress", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_graph_epoch_retirements", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_graph_lifecycle", Privileges: []string{"SELECT", "DELETE"}, Why: "purge table list: orgpurge purgeTables"},
	{Table: "context_fabric_org_purge_markers", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Why: "the purge's marker upsert (markerUpsertSQL: INSERT, UPDATE, SELECT); DELETE because the discovery the purge command runs first clears markers of organizations that returned (internal/contextfabric/pgprojection/org_purge_marker.go)"},
}

// PurgeExtras names the two privileges PurgeGrants holds that no purge
// statement needs, with the reason; the class test allows exactly these.
var PurgeExtras = map[string]string{
	"context_fabric_org_purge_markers/DELETE": "discovery's clear runs in the purge command's own process",
	"schema_migrations/SELECT":                "the purge command verifies the schema when it opens its runtime",
}

// PurgeGrantStatements returns the idempotent GRANTs for the purge role.
func PurgeGrantStatements(quotedRole string) []Statement {
	return grantStatements(PurgeGrants, quotedRole, nil)
}
