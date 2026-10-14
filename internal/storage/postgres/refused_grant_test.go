package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyDatabaseErrorNamesTheTableOfARefusedGrantAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name, message, want string
	}{
		{"table", "permission denied for table client_credentials", "client_credentials"},
		{"sequence", "permission denied for sequence context_fabric_org_model_config_generation_seq", "context_fabric_org_model_config_generation_seq"},
		{"with a query after it", "permission denied for table client_credentials; DROP TABLE x", ""},
		{"with a value", "permission denied for table client_credentials (password=hunter2)", ""},
		{"schema qualified", "permission denied for table acr.client_credentials", ""},
		{"upper case", "permission denied for table Client_Credentials", ""},
		{"another message", `relation "x" does not exist`, ""},
		{"too long", "permission denied for table " + string(make([]byte, 80)), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			class := classifyDatabaseError(fmt.Errorf("find: %w", &pgconn.PgError{Code: "42501", Message: tc.message}))
			if class == nil || class.SQLState != "42501" || class.Class != "insufficient_privilege" {
				t.Fatalf("class = %+v", class)
			}
			if class.Table != tc.want {
				t.Fatalf("Table = %q, want %q", class.Table, tc.want)
			}
		})
	}
	var class *storage.DependencyErrorClass
	if !errors.As(sanitizeDatabaseError(&pgconn.PgError{Code: "42501", Message: "permission denied for table audit_events"}), &class) || class.Table != "audit_events" {
		t.Fatalf("the sanitized error keeps the table: %+v", class)
	}
	other := classifyDatabaseError(&pgconn.PgError{Code: "23505", Message: "permission denied for table audit_events"})
	if other.Table != "" {
		t.Fatalf("only a refused grant names a table from its message, got %q", other.Table)
	}
}
