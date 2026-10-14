package dbacl

import (
	"os"
	"strings"
	"testing"
)

const composeScript = "../../deploy/compose/acr-db-init.sh"

// TestComposeScriptCarriesTheGeneratedGrants fails when the generated block of
// acr-db-init.sh differs from the declaration. DBACL_WRITE=1 rewrites it.
func TestComposeScriptCarriesTheGeneratedGrants(t *testing.T) {
	raw, err := os.ReadFile(composeScript)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	begin := strings.Index(script, ComposeBeginMarker)
	end := strings.Index(script, ComposeEndMarker)
	if begin < 0 || end < begin {
		t.Fatalf("%s has no generated runtime grants block between %q and %q", composeScript, ComposeBeginMarker, ComposeEndMarker)
	}
	end += len(ComposeEndMarker) + 1
	want := RenderCompose()
	if os.Getenv("DBACL_WRITE") == "1" {
		if err := os.WriteFile(composeScript, []byte(script[:begin]+want+script[end:]), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := script[begin:end]; got != want {
		t.Fatalf("the generated block of %s differs from internal/dbacl; run DBACL_WRITE=1 go test ./internal/dbacl\n--- script\n%s\n--- declaration\n%s", composeScript, got, want)
	}
}

func TestEveryDeclaredPrivilegeIsAKnownTablePrivilege(t *testing.T) {
	known := map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true}
	seen := map[string]bool{}
	for _, grant := range RuntimeGrants {
		if strings.TrimSpace(grant.Why) == "" {
			t.Errorf("%s: a row must cite the runtime code that needs it", grant.Table)
		}
		if len(grant.Privileges) == 0 {
			t.Errorf("%s: no privileges", grant.Table)
		}
		for _, privilege := range grant.Privileges {
			if !known[privilege] {
				t.Errorf("%s: unknown privilege %q", grant.Table, privilege)
			}
			key := grant.Table + "/" + privilege
			if seen[key] {
				t.Errorf("%s: privilege %s declared twice", grant.Table, privilege)
			}
			seen[key] = true
		}
	}
}
