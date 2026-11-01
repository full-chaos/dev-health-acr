package orgpurge

import (
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"reflect"
	"strings"
	"testing"
)

func TestPrivilegesAreDerivedFromTheStatementsRunIssues(t *testing.T) {
	got := Privileges()
	for table, want := range map[string][]string{
		"client_credentials":               {"DELETE", "SELECT"},
		"audit_events":                     {"INSERT", "SELECT"},
		"context_fabric_org_purge_markers": {"INSERT", "SELECT", "UPDATE"},
		"agent_episodes":                   {"DELETE", "SELECT"},
		"oauth_device_grants":              {"SELECT"}, // removed by cascade, counted by the dry run
	} {
		if !reflect.DeepEqual(got[table], want) {
			t.Errorf("%s: privileges = %v, want %v", table, got[table], want)
		}
	}
	if _, ok := got["context_fabric_org_purge_markers"]; !ok {
		t.Fatal("the marker upsert is not in the derived set")
	}
	for _, table := range PurgedTables() {
		var delete bool
		for _, privilege := range got[table] {
			delete = delete || privilege == "DELETE"
		}
		if !delete {
			t.Errorf("%s is purged but the derived set has no DELETE on it", table)
		}
	}
	for _, table := range CascadedTables() {
		if got[table] == nil {
			t.Errorf("%s is read by the cascade counts but is not in the derived set", table)
		}
	}
}

func TestTheCredentialsAreTakenByDeleteReturningAndNeverByASeparateSelect(t *testing.T) {
	var deletes int
	for _, statement := range Statements() {
		if strings.Contains(statement, "DELETE FROM acr.client_credentials") {
			deletes++
			if !strings.Contains(statement, "RETURNING") {
				t.Errorf("a client_credentials delete without RETURNING: %s", statement)
			}
		}
	}
	if deletes != 1 {
		t.Fatalf("%d client_credentials deletes in the statement set, want exactly the one DELETE ... RETURNING", deletes)
	}
}

func TestThePurgeRoleGainsOnlySelectAndInsertOnTheOutbox(t *testing.T) {
	got := Privileges()["credential_lifecycle_outbox"]
	if !reflect.DeepEqual(got, []string{"INSERT", "SELECT"}) {
		t.Fatalf("outbox privileges = %v, want exactly INSERT and SELECT: the purge never updates or deletes an outbox row", got)
	}
	for table, privileges := range Privileges() {
		for _, privilege := range privileges {
			if table == "credential_lifecycle_outbox" && (privilege == "UPDATE" || privilege == "DELETE") {
				t.Errorf("%s holds %s", table, privilege)
			}
		}
	}
}

func TestTheStatementSetListsEveryOutboxStatementTheWriterIssues(t *testing.T) {
	listed := map[string]bool{}
	for _, statement := range Statements() {
		listed[statement] = true
	}
	for name, statement := range map[string]string{
		"max revision": storagepostgres.LifecycleOutboxMaxRevisionSQL, "latest event": storagepostgres.LifecycleOutboxLatestEventSQL, "insert": storagepostgres.LifecycleOutboxInsertSQL,
	} {
		if !listed[statement] {
			t.Errorf("the %s statement the lifecycle writer issues is not in Statements(): the purge role's privileges would not be derived from it", name)
		}
	}
}
