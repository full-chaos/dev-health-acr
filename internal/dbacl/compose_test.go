package dbacl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const composeScript = "../../deploy/compose/acr-db-init.sh"

// TestComposeScriptCarriesTheGeneratedGrants fails when a generated block of
// acr-db-init.sh differs from the declaration. DBACL_WRITE=1 rewrites them.
func TestComposeScriptCarriesTheGeneratedGrants(t *testing.T) {
	raw, err := os.ReadFile(composeScript)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, block := range []struct{ begin, end, want string }{
		{ComposeBeginMarker, ComposeEndMarker, RenderCompose()},
		{ComposePurgeBeginMarker, ComposePurgeEndMarker, RenderComposePurge()},
	} {
		begin := strings.Index(script, block.begin)
		end := strings.Index(script, block.end)
		if begin < 0 || end < begin {
			t.Fatalf("%s has no generated block between %q and %q", composeScript, block.begin, block.end)
		}
		end += len(block.end) + 1
		if os.Getenv("DBACL_WRITE") == "1" {
			script = script[:begin] + block.want + script[end:]
			continue
		}
		if got := script[begin:end]; got != block.want {
			t.Fatalf("a generated block of %s differs from internal/dbacl; run DBACL_WRITE=1 go test ./internal/dbacl\n--- script\n%s\n--- declaration\n%s", composeScript, got, block.want)
		}
	}
	if os.Getenv("DBACL_WRITE") == "1" {
		if err := os.WriteFile(composeScript, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
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

// TestComposeWorkloadGateFollowsTheRuntimeRule runs the script's gate for the
// workload feature with real shell and compares it with the runtime's rule: both
// settings set to something other than whitespace.
func TestComposeWorkloadGateFollowsTheRuntimeRule(t *testing.T) {
	raw, err := os.ReadFile(composeScript)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	begin := strings.Index(script, "trim() {")
	end := strings.Index(script, "export PGPASSWORD=")
	if begin < 0 || end < begin {
		t.Fatal("the workload gate was not found in the compose script")
	}
	gate := script[begin:end]
	for _, tc := range []struct {
		audience, trust string
		want            string
	}{
		{"https://acr.test", "cluster.test", "true"},
		{"", "cluster.test", "false"},
		{"https://acr.test", "", "false"},
		{" ", " ", "false"},
		{"https://acr.test", " \t", "false"},
		{"  https://acr.test ", " cluster.test ", "true"},
	} {
		command := exec.Command("sh", "-c", gate+"\nprintf %s \"$workload_token_exchange\"")
		command.Env = []string{"ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE=" + tc.audience, "ACR_WORKLOAD_TRUST_DOMAIN=" + tc.trust, "PATH=" + os.Getenv("PATH")}
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%q/%q: %v: %s", tc.audience, tc.trust, err, out)
		}
		if string(out) != tc.want {
			t.Fatalf("audience %q trust %q: gate = %q, want %q", tc.audience, tc.trust, out, tc.want)
		}
	}
}

// TestComposeBootstrapHandlesThePurgeRoleSettings runs the real script with
// stand-ins for pg_isready and psql that only record what they are given, so no
// database is involved: with a purge user and a password (direct or from a
// file) the roles stage passes them on, without a purge user the purge role is
// off and no password is demanded, and a missing or doubly given password is
// refused.
func TestComposeBootstrapHandlesThePurgeRoleSettings(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(bin, "psql.args")
	for name, body := range map[string]string{
		"pg_isready": "#!/bin/sh\nexit 0\n",
		"psql":       "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + record + "\ncat >/dev/null\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secrets := t.TempDir()
	passwordFile := filepath.Join(secrets, "purge-password")
	if err := os.WriteFile(passwordFile, []byte("file-purge-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "POSTGRES_USER=postgres", "POSTGRES_PASSWORD=admin-secret",
		"ACR_RUNTIME_DB_USER=acr_runtime", "ACR_RUNTIME_DB_PASSWORD=runtime-secret",
		"ACR_MIGRATION_DB_USER=acr_migrate", "ACR_MIGRATION_DB_PASSWORD=migrate-secret",
	}
	for _, tc := range []struct {
		name    string
		env     []string
		wantErr string
		want    []string
	}{
		{"no purge user", nil, "", []string{"purge_enabled=false"}},
		{"direct password", []string{"ACR_PURGE_DB_USER=acr_purge", "ACR_PURGE_DB_PASSWORD=direct-purge-secret"}, "", []string{"purge_enabled=true", "purge_user=acr_purge", "purge_password=direct-purge-secret"}},
		{"password file", []string{"ACR_PURGE_DB_USER=acr_purge", "ACR_PURGE_DB_PASSWORD_FILE=" + passwordFile}, "", []string{"purge_enabled=true", "purge_user=acr_purge", "purge_password=file-purge-secret"}},
		{"no password", []string{"ACR_PURGE_DB_USER=acr_purge"}, "ACR_PURGE_DB_PASSWORD or ACR_PURGE_DB_PASSWORD_FILE is required", nil},
		{"both sources", []string{"ACR_PURGE_DB_USER=acr_purge", "ACR_PURGE_DB_PASSWORD=x", "ACR_PURGE_DB_PASSWORD_FILE=" + passwordFile}, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(record)
			command := exec.Command("sh", composeScript, "roles")
			command.Env = append(append([]string{}, base...), tc.env...)
			out, err := command.CombinedOutput()
			if tc.name == "both sources" {
				if err == nil {
					t.Fatalf("a password given both directly and by file must be refused: %s", out)
				}
				return
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(string(out), tc.wantErr) {
					t.Fatalf("want refusal %q, got err=%v out=%s", tc.wantErr, err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("script failed: %v: %s", err, out)
			}
			args, _ := os.ReadFile(record)
			for _, want := range tc.want {
				if !strings.Contains(string(args), want) {
					t.Fatalf("psql was not given %q: %s", want, args)
				}
			}
		})
	}
}
