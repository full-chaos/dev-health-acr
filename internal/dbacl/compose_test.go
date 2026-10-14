package dbacl

import (
	"os"
	"os/exec"
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
