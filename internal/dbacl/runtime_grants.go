// Package dbacl is the one declaration of the table privileges the runtime
// database role holds. The Kubernetes grant path (acr-migrate grant-runtime-acl)
// applies it as idempotent GRANTs, and the compose bootstrap script
// (deploy/compose/acr-db-init.sh) carries a block generated from it, so the two
// paths cannot drift.
//
// Each row is derived from what runtime code does with the table, not from
// either earlier grant list. Why cites the first statement for each privilege.
// A table with no row either has no runtime user (see NoRuntimeAccess) or is a
// bug the class test in cmd/acr-migrate reports.
package dbacl

import (
	"fmt"
	"sort"
	"strings"
)

// Feature gates a row on an optional runtime feature, so a deployment that
// does not use the feature does not hold the privilege.
type Feature string

const (
	// AlwaysOn rows apply to every deployment.
	AlwaysOn Feature = ""
	// EpisodeWriteback is ACR_ENABLE_EPISODE_WRITEBACK=true.
	EpisodeWriteback Feature = "episode_writeback"
	// WorkloadTokenExchange is a configured workload token exchange: both
	// ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE and ACR_WORKLOAD_TRUST_DOMAIN set.
	WorkloadTokenExchange Feature = "workload_token_exchange"
)

// Grant is the privileges the runtime role holds on one table.
type Grant struct {
	Table      string
	Privileges []string
	Feature    Feature
	// Sequences are sequences the table's inserts draw from; the role needs USAGE.
	Sequences []string
	// Why cites the runtime code that needs each privilege.
	Why string
}

// RuntimeGrants lists every table the runtime role uses, in the order the
// compose script applies them.
var RuntimeGrants = []Grant{
	{Table: "schema_migrations", Privileges: []string{"SELECT"},
		Why: "readiness check and migration verification read it: internal/runtime/hosted/postgres_readiness.go:20"},
	{Table: "client_credentials", Privileges: []string{"SELECT", "INSERT", "UPDATE"},
		Why: "SELECT internal/storage/postgres/credentials.go:61, INSERT credentials.go:167, UPDATE credentials.go:115 and credentials_audit.go:72"},
	{Table: "client_credentials", Privileges: []string{"DELETE"}, Feature: WorkloadTokenExchange,
		Why: "DELETE of expired workload credentials: internal/storage/postgres/workload_bindings.go:65, started only when workload token exchange is configured (internal/runtime/hosted/postgres.go:185)"},
	{Table: "workload_bindings", Privileges: []string{"SELECT"}, Feature: WorkloadTokenExchange,
		Why: "read only, no write path; the resolver is built only when workload token exchange is configured: internal/storage/postgres/workload_bindings.go:36, internal/runtime/hosted/workload_token_exchange.go:57"},
	{Table: "context_packet_snapshots", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Why: "SELECT internal/storage/postgres/packet.go:69, INSERT packet.go:49, DELETE packet_retention.go:28; UPDATE because the purge locks rows with FOR UPDATE SKIP LOCKED (packet_retention.go:20), which Postgres checks as UPDATE; the readiness check asserts it"},
	{Table: "audit_events", Privileges: []string{"SELECT", "INSERT"},
		Why: "INSERT internal/storage/postgres/audit.go:69; SELECT credentials_audit.go:50 and :61 (credential rotation conflict checks)"},
	{Table: "agent_episodes", Privileges: []string{"SELECT", "INSERT", "UPDATE"}, Feature: EpisodeWriteback,
		Why: "SELECT internal/storage/postgres/episodes.go:92, INSERT episodes.go:63, UPDATE episodes.go:126"},
	{Table: "device_authorizations", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Why: "SELECT internal/storage/postgres/device_authorization.go:88, INSERT device_authorization.go:61, UPDATE device_authorization.go:132, DELETE oauth_purge.go:167"},
	{Table: "oauth_clients", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Why: "SELECT internal/storage/postgres/oauth.go:66, INSERT oauth.go:104, DELETE oauth_purge.go:109; UPDATE because the idle-client purge locks rows with FOR UPDATE OF c SKIP LOCKED (oauth_purge.go:103), which Postgres checks as UPDATE"},
	{Table: "oauth_authorization_requests", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Why: "SELECT internal/storage/postgres/oauth.go:165, INSERT oauth.go:68, UPDATE oauth.go:185, DELETE oauth_purge.go:59"},
	{Table: "oauth_device_grants", Privileges: []string{"SELECT", "INSERT"},
		Why: "SELECT internal/storage/postgres/oauth.go:308, INSERT oauth.go:79; removed with its device_authorizations parent by cascade"},
	{Table: "web_assertion_replays", Privileges: []string{"SELECT", "INSERT", "DELETE"},
		Why: "INSERT internal/storage/postgres/web_assertion_replay.go:24, SELECT and DELETE web_assertion_replay.go:33"},
	{Table: "context_fabric_clarification_selections", Privileges: []string{"INSERT"},
		Why: "insert-only capture sink: internal/contextfabric/pgclarification/sink.go:387"},
	{Table: "context_fabric_projection_checkpoints", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Why: "SELECT internal/contextfabric/pgprojection/checkpoints.go:38, INSERT checkpoints.go:99, UPDATE checkpoints.go:80, DELETE checkpoints.go:258"},
	{Table: "context_fabric_projection_rebuild_markers", Privileges: []string{"SELECT", "INSERT", "DELETE"},
		Why: "SELECT internal/contextfabric/pgprojection/rebuild_marker.go:63, INSERT rebuild_marker.go:43, DELETE rebuild_marker.go:79"},
	{Table: "context_fabric_org_purge_markers", Privileges: []string{"SELECT", "DELETE"},
		Why: "SELECT internal/contextfabric/pgprojection/org_purge_marker.go:46 (the tick's check), DELETE org_purge_marker.go:86 (discovery's clear); only the purge writes it"},
	{Table: "context_fabric_investigation_results", Privileges: []string{"SELECT", "INSERT"},
		Why: "SELECT internal/contextfabric/pginvestigation/store.go:395, INSERT store.go:284"},
	{Table: "context_fabric_structure_supersession_claims", Privileges: []string{"SELECT", "INSERT"},
		Why: "SELECT internal/contextfabric/pginvestigation/store.go:491, INSERT store.go:345"},
	{Table: "context_fabric_reuse_invalidations", Privileges: []string{"SELECT", "INSERT", "UPDATE"},
		Why: "SELECT internal/contextfabric/pginvestigation/store.go:748, INSERT and upsert store.go:1290"},
	{Table: "context_fabric_org_model_config", Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE"},
		Sequences: []string{"context_fabric_org_model_config_generation_seq"},
		Why:       "SELECT internal/contextfabric/pgmodelconfig/store.go:138, INSERT and upsert store.go:95, DELETE store.go:195; generation draws from an explicit sequence"},
	{Table: "context_fabric_model_execution_receipts", Privileges: []string{"INSERT"},
		Why: "insert-only: internal/contextfabric/pgmodelreceipts/store.go:80"},
	{Table: "context_fabric_graph_lifecycle", Privileges: []string{"SELECT", "INSERT", "UPDATE"},
		Why: "SELECT internal/contextfabric/pglifecycle/store.go:83, INSERT store.go:114, UPDATE store.go:240"},
	{Table: "context_fabric_graph_epoch_retirements", Privileges: []string{"SELECT", "INSERT", "UPDATE"},
		Why: "SELECT internal/contextfabric/pglifecycle/store.go:469, INSERT store.go:328, UPDATE store.go:496"},
	{Table: "context_fabric_graph_build_source_progress", Privileges: []string{"SELECT", "INSERT", "UPDATE"},
		Why: "SELECT internal/contextfabric/pglifecycle/store.go:172, INSERT and upsert store.go:153"},
	{Table: "context_fabric_structure_selections", Privileges: []string{"SELECT", "INSERT"},
		Why: "INSERT internal/contextfabric/pgstructureselection/sink.go:423, SELECT structurepriorcuration/curate.go:104"},
	{Table: "context_fabric_structure_priors", Privileges: []string{"SELECT", "INSERT"},
		Why: "SELECT internal/contextfabric/pgstructurepriors/store.go:130, INSERT store.go:221"},
	{Table: "context_fabric_structure_prior_pointer", Privileges: []string{"SELECT", "INSERT", "UPDATE"},
		Why: "SELECT internal/contextfabric/pgstructurepriors/store.go:113, INSERT store.go:463, UPDATE store.go:366"},
	{Table: "context_fabric_structure_prior_pointer_history", Privileges: []string{"INSERT"},
		Sequences: []string{"context_fabric_structure_prior_pointer_history_id_seq"},
		Why:       "insert-only history: internal/contextfabric/pgstructurepriors/store.go:374; id is BIGSERIAL"},
	{Table: "context_fabric_structure_prior_revocations", Privileges: []string{"SELECT", "INSERT"},
		Why: "SELECT internal/contextfabric/pgstructurepriors/store.go:169, INSERT store.go:499"},
}

// NoRuntimeAccess names the tables a migration creates that the runtime role
// deliberately has no privilege on, with the reason. The class test requires
// every migration table to be in RuntimeGrants or here.
var NoRuntimeAccess = map[string]string{}

// Features is the set of enabled optional features.
type Features map[Feature]bool

func (f Features) enabled(feature Feature) bool {
	return feature == AlwaysOn || f[feature]
}

// Tables returns the distinct table names declared in RuntimeGrants.
func Tables() []string {
	seen := map[string]bool{}
	var tables []string
	for _, grant := range RuntimeGrants {
		if !seen[grant.Table] {
			seen[grant.Table] = true
			tables = append(tables, grant.Table)
		}
	}
	sort.Strings(tables)
	return tables
}

// QuoteIdent quotes a role name as a PostgreSQL identifier.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Statement is one GRANT with the table and privileges it covers.
type Statement struct {
	Table      string
	Privileges []string
	SQL        string
}

// GrantStatements returns the idempotent GRANTs for the runtime role under
// features: bare grants that never revoke, safe on a role serving traffic.
func GrantStatements(quotedRole string, features Features) []Statement {
	return grantStatements(RuntimeGrants, quotedRole, features)
}

func grantStatements(grants []Grant, quotedRole string, features Features) []Statement {
	var statements []Statement
	for _, grant := range grants {
		if !features.enabled(grant.Feature) {
			continue
		}
		statements = append(statements, Statement{
			Table:      grant.Table,
			Privileges: grant.Privileges,
			SQL:        fmt.Sprintf("GRANT %s ON TABLE acr.%s TO %s", strings.Join(grant.Privileges, ", "), grant.Table, quotedRole),
		})
		for _, sequence := range grant.Sequences {
			statements = append(statements, Statement{
				Table: grant.Table,
				SQL:   fmt.Sprintf("GRANT USAGE ON SEQUENCE acr.%s TO %s", sequence, quotedRole),
			})
		}
	}
	return statements
}
