// CHAOS-3273 L3: acr endpoint-profile inventory contract + CI enforcement.
//
// Proves two things, same standard as dev-health-ops's
// tests/test_endpoint_profiles_contract.py (itself modelled on
// ci/check_transitional_inventory.py's proving test):
//
//  1. The real, checked-in inventory
//     (contracts/auth/v1/endpoint-profiles.acr.json) is currently
//     consistent with independent code discovery on this tree -- i.e. the
//     CI gate passes today. This test SKIPS (does not silently pass, does
//     not fail) when the ops-owned schema/credential-classes files it needs
//     are not reachable -- see main.go's doc comment for the cross-repo
//     distribution gap this lane flagged rather than solved.
//  2. The gate actually *works*: for every failure class it is supposed to
//     catch, a synthetic violation is seeded in a t.TempDir fixture tree and
//     the gate is asserted to reject it. No real violation is ever
//     committed. Fixture tests use SELF-CONTAINED schema/credential-classes
//     fixtures (not the real ops files) so they always run in CI regardless
//     of the cross-repo gap.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// ci/checkendpointprofiles -> repo root is two levels up.
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func realDiscovererPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot(t), "ci", "discover_acr_routes.go")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("ci/discover_acr_routes.go not found at %s: %v", p, err)
	}
	return p
}

// opsOwnedFixturePaths locates the real ops-owned schema/credential-classes
// files for the "real tree passes today" proof. Tries an env override first
// (ACR_ENDPOINT_PROFILE_SCHEMA / ACR_CREDENTIAL_CLASSES), then the
// conventional sibling-worktree layout this lane was assigned
// (dev-health/ops-worktrees/chaos-3273-wave0). Returns ok=false rather than
// guessing when neither is reachable.
// opsOwnedFixturePaths locates the real ops-owned schema/credential-classes
// files. In CI (the CI env var, the near-universal convention GitHub
// Actions and most other CI systems set), ONLY the env-var override
// (ACR_ENDPOINT_PROFILE_SCHEMA / ACR_CREDENTIAL_CLASSES, set by the pinned
// sparse-checkout workflow step) counts -- a real CI job never has an
// incidental sibling ops worktree lying around, so falling back to one
// there would mask exactly the failure this function exists to surface.
// Locally, the env override is tried first, then the sibling-worktree
// layout this lane was assigned (dev-health/ops-worktrees/chaos-3273-wave0)
// as a developer convenience. Returns ok=false rather than guessing when
// nothing is reachable.
func opsOwnedFixturePaths(t *testing.T) (schemaPath, credentialClassesPath string, ok bool) {
	t.Helper()
	if s := os.Getenv("ACR_ENDPOINT_PROFILE_SCHEMA"); s != "" {
		if c := os.Getenv("ACR_CREDENTIAL_CLASSES"); c != "" {
			if fileExists(s) && fileExists(c) {
				return s, c, true
			}
		}
	}
	if os.Getenv("CI") != "" {
		return "", "", false
	}
	// dev-health/acr-worktrees/<lane> -> dev-health/ops-worktrees/chaos-3273-wave0
	devHealthRoot := filepath.Clean(filepath.Join(repoRoot(t), "..", ".."))
	candidateOpsRoot := filepath.Join(devHealthRoot, "ops-worktrees", "chaos-3273-wave0")
	s := filepath.Join(candidateOpsRoot, "contracts", "auth", "v1", "endpoint-profile.schema.json")
	c := filepath.Join(candidateOpsRoot, "contracts", "auth", "v1", "credential-classes.json")
	if fileExists(s) && fileExists(c) {
		return s, c, true
	}
	return "", "", false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestRealTreePassesTheGateToday(t *testing.T) {
	schemaPath, credentialClassesPath, ok := opsOwnedFixturePaths(t)
	if !ok {
		// Codex/coordinator-verified gap (round 1): "skip cleanly rather
		// than silently pass" was the right call LOCALLY, but codex is
		// right about the consequence in CI -- a probe that cannot fail
		// loudly reads as a result (the same skip-reads-as-ok trap this
		// codebase already names for acr_db_init_integration_test.go, see
		// ci.yml). A bad sparse-checkout path or a mistyped env var would
		// SKIP, and a skip in `go test` reads as part of a passing run:
		// the CI job goes green with the contract gate never having
		// executed. In CI, this must FAIL, and name exactly which input
		// was missing so nobody mistakes it for a broken mechanism (the
		// pin being unresolved because ops hasn't merged yet is a KNOWN,
		// separate, accepted state -- see ci/ops-contract.pin's own
		// commit message -- but that failure mode is legible on its own
		// via the sparse-checkout step itself failing before this test
		// even runs; this check is the belt-and-braces layer for every
		// OTHER way the inputs could go missing).
		if os.Getenv("CI") != "" {
			t.Fatalf(
				"ops-owned schema/credential-classes files not reachable in CI "+
					"(ACR_ENDPOINT_PROFILE_SCHEMA=%q ACR_CREDENTIAL_CLASSES=%q) -- "+
					"the pinned sparse checkout (ci/ops-contract.pin) did not deliver "+
					"usable inputs; this must fail, not skip, or the contract gate silently never runs",
				os.Getenv("ACR_ENDPOINT_PROFILE_SCHEMA"), os.Getenv("ACR_CREDENTIAL_CLASSES"),
			)
		}
		t.Skip("ops-owned endpoint-profile.schema.json / credential-classes.json not reachable " +
			"(cross-repo distribution gap -- see main.go doc comment); set " +
			"ACR_ENDPOINT_PROFILE_SCHEMA / ACR_CREDENTIAL_CLASSES to run this proof")
	}
	root := repoRoot(t)
	inventoryPath := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("gate found %d violation(s) on the real tree:\n%s", len(errs), strings.Join(errs, "\n"))
	}
}

func TestRealTreeProofFailsLoudlyInCIWhenInputsAreMissing(t *testing.T) {
	// EXECUTED repro for the fix above: actually run this package's own
	// TestRealTreePassesTheGateToday in a subprocess with CI=1 and the
	// override env vars UNSET, and assert it FAILS (not skips, not
	// passes) -- proving the CI branch fires, not just that the code
	// compiles. The subprocess's own sibling-worktree fallback is
	// irrelevant here regardless of what exists on this host, since the
	// CI branch in opsOwnedFixturePaths skips that fallback entirely once
	// CI is set.
	pkgDir := filepath.Join(repoRoot(t), "ci", "checkendpointprofiles")
	cmd := exec.Command("go", "test", "-run", "^TestRealTreePassesTheGateToday$", "-v", ".")
	cmd.Dir = pkgDir
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "ACR_ENDPOINT_PROFILE_SCHEMA=") || strings.HasPrefix(kv, "ACR_CREDENTIAL_CLASSES=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	cmd.Env = append(filtered, "CI=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the subprocess to fail (CI=1, no ops inputs configured), but it exited 0:\n%s", output)
	}
	if strings.Contains(string(output), "--- SKIP") {
		t.Fatalf("expected a FAIL, not a SKIP, when CI=1 and ops inputs are missing:\n%s", output)
	}
	if !strings.Contains(string(output), "--- FAIL") {
		t.Fatalf("expected an explicit --- FAIL in subprocess output:\n%s", output)
	}
}

func TestRealInventoryRowCountMatchesTheWave0Baseline(t *testing.T) {
	// 16 rows = 12 protected / 4 public (lane brief section 6 baseline). A
	// different number is a finding to reconcile, not an adjustment.
	root := repoRoot(t)
	inventoryPath := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
	inventory, err := loadJSON(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	rows := asArray(inventory["rows"])
	if len(rows) != 16 {
		t.Fatalf("expected 16 rows, got %d", len(rows))
	}
	var protected, public int
	for _, r := range rows {
		row := asObject(r)
		switch c, _ := asString(row["classification"]); c {
		case "protected":
			protected++
		case "public":
			public++
		}
	}
	if protected != 12 {
		t.Errorf("expected 12 protected rows, got %d", protected)
	}
	if public != 4 {
		t.Errorf("expected 4 public rows, got %d", public)
	}
}

// ---------------------------------------------------------------------------
// Fixture-based proofs that the gate actually catches violations. Fixture
// trees use self-contained schema/credential-classes fixtures (not the real
// ops files) and the REAL discover_acr_routes.go (pointed at a synthetic
// -root), so these always run regardless of the cross-repo gap.
// ---------------------------------------------------------------------------

const fixtureAppFile = "internal/api/app.go"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureSchemaJSON is a self-contained fixture schema -- NOT the real
// endpoint-profile.schema.json (which is ops-owned and not vendored into
// this repo; see main.go's doc comment) -- but faithful enough to the real
// one's load-bearing structure (top-level additionalProperties:false and
// required, rows.items -> $defs.endpointProfile, nested $defs for anchor/
// issuedCredential/exposure) that real Draft 2020-12 validation exercises
// the same rule shapes fixture tests probe: missing required fields, wrong
// field types, an unexpected top-level key, enum membership. Kept
// self-contained (not copied from the real ops file, unlike
// TestRealTreePassesTheGateToday's real-tree proof) so fixture tests always
// run in CI regardless of the cross-repo distribution gap.
const fixtureSchemaJSON = `{
  "type": "object",
  "required": ["schema_version", "generated_at", "source_commit", "rows"],
  "additionalProperties": false,
  "properties": {
    "schema_version": {"const": "endpoint-profile.v1"},
    "generated_at": {"type": "string"},
    "source_commit": {"type": "string"},
    "credential_class_source": {"type": "string"},
    "rows": {"type": "array", "items": {"$ref": "#/$defs/endpointProfile"}}
  },
  "$defs": {
    "anchor": {
      "oneOf": [
        {
          "type": "object",
          "required": ["path", "line"],
          "additionalProperties": false,
          "properties": {
            "path": {"type": "string"},
            "line": {"type": "integer"},
            "line_end": {"type": "integer"},
            "note": {"type": "string"}
          }
        },
        {"type": "null"}
      ]
    },
    "issuedCredential": {
      "type": "object",
      "required": ["class_id", "direction", "anchor"],
      "additionalProperties": false,
      "properties": {
        "class_id": {"type": "string"},
        "direction": {"enum": ["outbound_to_dependency", "returned_to_caller"]},
        "anchor": {"$ref": "#/$defs/anchor"},
        "issuer": {"type": ["string", "null"]},
        "audience": {"type": ["string", "null"]},
        "algorithm": {"type": ["string", "null"]},
        "lifetime_seconds": {"type": ["integer", "null"]},
        "key_source": {"type": ["string", "null"]},
        "verified_by": {"type": ["string", "null"]},
        "note": {"type": "string"}
      }
    },
    "endpointProfile": {
      "type": "object",
      "required": ["id", "surface_kind", "method", "route", "service", "source", "classification", "gaps"],
      "additionalProperties": false,
      "properties": {
        "id": {"type": "string"},
        "surface_kind": {"enum": ["rest", "graphql_field", "graphql_mutation", "server_action"]},
        "method": {"type": ["string", "null"]},
        "route": {"type": ["string", "null"]},
        "graphql_field_name": {"type": ["string", "null"]},
        "service": {"enum": ["dev-health-ops-api", "dev-health-ops-billing-edge", "dev-health-web", "dev-health-acr-api", "dev-health-acr-mcp"]},
        "source": {
          "type": "object",
          "required": ["file", "line"],
          "properties": {
            "file": {"type": "string"},
            "line": {"type": "integer"},
            "router_local_path": {"type": "string"}
          }
        },
        "classification": {"enum": ["protected", "public"]},
        "public_rationale": {"type": ["string", "null"]},
        "accepted_credential_classes": {"type": "array", "items": {"type": "string"}},
        "issued_credential": {"type": ["array", "null"], "items": {"$ref": "#/$defs/issuedCredential"}},
        "primary_validator": {"type": ["object", "null"]},
        "token_shape": {"type": ["object", "null"]},
        "reachable_validators": {"type": "array"},
        "action": {"type": ["string", "null"]},
        "resource_resolver": {"type": ["string", "null"]},
        "tenant_requirement": {"enum": ["org_scoped", "cross_org_superuser_only", "none", "unknown"]},
        "current_state_cache_behavior": {"type": ["string", "null"]},
        "impersonation_policy": {"enum": ["subject_to_impersonation_override", "exempt_no_org_context", "not_applicable_non_main_app", "unknown"]},
        "entitlement_requirement": {"type": ["string", "null"]},
        "disclosure_behavior": {"type": ["string", "null"]},
        "exposure": {
          "type": ["object", "null"],
          "required": ["reachability", "source"],
          "additionalProperties": false,
          "properties": {
            "reachability": {"enum": ["public_via_edge", "private_network_only", "unknown"]},
            "source": {"type": "string"},
            "observed_at": {"type": ["string", "null"]},
            "note": {"type": "string"}
          }
        },
        "gaps": {"type": "array", "items": {"type": "string"}}
      }
    }
  }
}`

func seedFixtureSchemaAndCredentialClasses(t *testing.T, root string) (schemaPath, credentialClassesPath string) {
	t.Helper()
	credentialClasses := map[string]any{
		"classes": []map[string]any{
			{"class_id": "acr_client_credential"},
			{"class_id": "acr_device_flow_code"},
			{"class_id": "acr_web_assertion"},
			{"class_id": "internal_svc_acr_token"},
		},
	}
	schemaPath = filepath.Join(root, "fixture-schema.json")
	credentialClassesPath = filepath.Join(root, "fixture-credential-classes.json")
	writeFile(t, schemaPath, fixtureSchemaJSON)
	writeJSON(t, credentialClassesPath, credentialClasses)
	return schemaPath, credentialClassesPath
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(raw)+"\n")
}

func seedFixtureAppGo(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+
			"\n"+
			"import \"net/http\"\n"+
			"\n"+
			"func Handler() http.Handler {\n"+
			"	mux := http.NewServeMux()\n"+
			"	mux.HandleFunc(\"GET /healthz\", healthzHandler)\n"+
			"	return mux\n"+
			"}\n"+
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n")
}

func minimalValidRow(overrides map[string]any) map[string]any {
	row := map[string]any{
		"id":                          "GET /healthz [dev-health-acr-api]",
		"surface_kind":                "rest",
		"method":                      "GET",
		"route":                       "/healthz",
		"graphql_field_name":          nil,
		"service":                     "dev-health-acr-api",
		"source":                      map[string]any{"file": fixtureAppFile, "line": float64(7)},
		"classification":              "public",
		"public_rationale":            "test fixture, no credential required",
		"accepted_credential_classes": []any{},
		"gaps":                        []any{},
	}
	for k, v := range overrides {
		row[k] = v
	}
	return row
}

func writeInventory(t *testing.T, root string, rows []map[string]any) string {
	t.Helper()
	rowsAny := make([]any, len(rows))
	for i, r := range rows {
		rowsAny[i] = r
	}
	inventory := map[string]any{
		"schema_version":          "endpoint-profile.v1",
		"generated_at":            "2026-09-01T00:00:00Z",
		"source_commit":           "0000000000000000000000000000000000000",
		"credential_class_source": "contracts/auth/v1/credential-classes.json",
		"rows":                    rowsAny,
	}
	path := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
	writeJSON(t, path, inventory)
	return path
}

type fixture struct {
	root                              string
	inventoryPath                     string
	schemaPath, credentialClassesPath string
	discovererPath                    string
}

func minimalValidFixture(t *testing.T, rows []map[string]any) fixture {
	t.Helper()
	root := t.TempDir()
	seedFixtureAppGo(t, root)
	schemaPath, credentialClassesPath := seedFixtureSchemaAndCredentialClasses(t, root)
	inventoryPath := writeInventory(t, root, rows)
	return fixture{
		root:                  root,
		inventoryPath:         inventoryPath,
		schemaPath:            schemaPath,
		credentialClassesPath: credentialClassesPath,
		discovererPath:        realDiscovererPath(t),
	}
}

func (f fixture) check(t *testing.T) []string {
	t.Helper()
	errs, err := check(f.root, f.inventoryPath, f.schemaPath, f.credentialClassesPath, f.discovererPath)
	if err != nil {
		t.Fatal(err)
	}
	return errs
}

func mustContain(t *testing.T, errs []string, substrs ...string) {
	t.Helper()
	for _, e := range errs {
		ok := true
		for _, s := range substrs {
			if !strings.Contains(e, s) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("expected an error containing %v, got:\n%s", substrs, strings.Join(errs, "\n"))
}

func TestGatePassesOnAMinimalFullyOwnedFixtureTree(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesASyntheticUnownedRoute(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	writeFile(t, filepath.Join(f.root, fixtureAppFile),
		"package api\n"+
			"\n"+
			"import \"net/http\"\n"+
			"\n"+
			"func Handler() http.Handler {\n"+
			"	mux := http.NewServeMux()\n"+
			"	mux.HandleFunc(\"GET /healthz\", healthzHandler)\n"+
			"	mux.HandleFunc(\"POST /rogue\", rogueHandler)\n"+
			"	return mux\n"+
			"}\n"+
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n"+
			"func rogueHandler(w http.ResponseWriter, r *http.Request)   {}\n")
	errs := f.check(t)
	mustContain(t, errs, "UNOWNED SURFACE", fixtureAppFile+":8")
}

func TestGateCatchesAPhantomStaleRow(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{
		minimalValidRow(map[string]any{"source": map[string]any{"file": fixtureAppFile, "line": float64(3)}}),
	})
	errs := f.check(t)
	mustContain(t, errs, "PHANTOM ROW")
	mustContain(t, errs, "UNOWNED SURFACE")
}

func TestGateCatchesADuplicateID(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil), minimalValidRow(nil)})
	errs := f.check(t)
	mustContain(t, errs, "DUPLICATE ID")
}

func TestGateCatchesAnUnknownAcceptedCredentialClass(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"not_a_real_credential_class"},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNKNOWN accepted_credential_class")
}

func TestGateCatchesAnUnknownService(t *testing.T) {
	// Guardrail G-26: enforced by the real Draft 2020-12 validator now
	// (service is a schema enum), not a hand-rolled check -- so the
	// message is the schema's own.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"service": "totally-unregistered-app"})})
	errs := f.check(t)
	mustContain(t, errs, "JSON SCHEMA VIOLATION", "totally-unregistered-app")
}

func TestGateCatchesAProtectedRowWithNoAcceptedCredentialClasses(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "EMPTY accepted_credential_classes")
}

func TestGateCatchesAPublicRowWithNoRationale(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"public_rationale": nil})})
	errs := f.check(t)
	mustContain(t, errs, "MISSING public_rationale")
}

func TestGateCatchesContentDriftWhenTheMethodIsSwapped(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"method": "POST"})})
	errs := f.check(t)
	mustContain(t, errs, "STALE ANCHOR", "content drift")
}

func TestGateCatchesContentDriftWhenTheRoutePathIsSwapped(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"route": "/a-different-path"})})
	errs := f.check(t)
	mustContain(t, errs, "STALE ANCHOR", "content drift")
}

func TestGateCatchesAStrayTopLevelKey(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	inventory, err := loadJSON(f.inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	inventory["schema_deviation_note"] = "this key should not exist"
	writeJSON(t, f.inventoryPath, inventory)
	errs := f.check(t)
	mustContain(t, errs, "SCHEMA VIOLATION", "schema_deviation_note")
}

func TestGateCatchesARowMissingARequiredField(t *testing.T) {
	row := minimalValidRow(nil)
	delete(row, "classification")
	f := minimalValidFixture(t, []map[string]any{row})
	errs := f.check(t)
	mustContain(t, errs, "SCHEMA VIOLATION", "classification")
}

func TestGateCatchesIssuedCredentialNullWithNoGapsEntry(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"issued_credential": nil,
		"gaps":              []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNSTATED NULL", "issued_credential")
}

func TestGateAcceptsIssuedCredentialNullWithAGapsEntry(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"issued_credential": nil,
		"gaps":              []any{"issued_credential not determined this pass"},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateAcceptsIssuedCredentialEmptyArrayWithNoGapsEntry(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"issued_credential": []any{},
		"gaps":              []any{},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesAnUnknownIssuedCredentialClassID(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"issued_credential": []any{
			map[string]any{
				"class_id":  "not_a_real_class",
				"direction": "returned_to_caller",
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(7)},
			},
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNKNOWN issued_credential class_id")
}

func TestGateCatchesAnIssuedCredentialAnchorPointingAtAMissingFile(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				"anchor":    map[string]any{"path": "internal/does/not/exist.go", "line": float64(1)},
			},
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "STALE ANCHOR", "issued_credential")
}

func TestGateAcceptsAFullyPopulatedIssuedCredentialRow(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				// line 7 falls inside func Handler() (declared at line 5) --
				// anchor.note names it so the content-identity check passes.
				"anchor":           map[string]any{"path": fixtureAppFile, "line": float64(7), "note": "Handler wires up the route"},
				"issuer":           "acr",
				"audience":         nil,
				"algorithm":        nil,
				"lifetime_seconds": nil,
				"key_source":       nil,
				"verified_by":      nil,
			},
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesExposureNullWithNoGapsEntry(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"exposure": nil,
		"gaps":     []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNSTATED NULL", "exposure")
}

func TestGateAcceptsExposureNullWithAGapsEntry(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"exposure": nil,
		"gaps":     []any{"exposure/edge reachability not determined this pass"},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesExposureUnknownReachabilityWithNoGapsEntry(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"exposure": map[string]any{"reachability": "unknown", "source": "edge path-map not consulted"},
		"gaps":     []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNSTATED NULL", "exposure")
}

func TestGateCatchesExposureMissingSource(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"exposure": map[string]any{"reachability": "private_network_only", "source": ""},
		"gaps":     []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "MISSING exposure.source")
}

func TestGateAcceptsAFullyPopulatedExposureRow(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"exposure": map[string]any{
			"reachability": "private_network_only",
			"source":       "edge ingress path-map, reviewed 2026-09-01",
			"observed_at":  "2026-09-01T00:00:00Z",
			"note":         "internal-only route",
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesAProtectedRowWithANullPrimaryValidatorAnchor(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"primary_validator":           map[string]any{"description": "unresolved this pass", "anchor": nil},
		"gaps":                        []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNSTATED NULL", "primary_validator")
}

func TestGateAcceptsAPublicRowWithANullPrimaryValidator(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"primary_validator": nil})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesAPublicRowWithANullPrimaryValidatorAnchor(t *testing.T) {
	// Codex round-1 gap: this rule used to be scoped to
	// classification=="protected" only, so a PUBLIC row could set
	// primary_validator to a present object with anchor=nil and gaps=[]
	// and pass. The schema's anchor rule ("null MUST be paired with a
	// gaps entry") does not carve out public rows -- it's about the
	// anchor being unresolved, not about who has to have a validator at
	// all (see TestGateAcceptsAPublicRowWithANullPrimaryValidator, where
	// primary_validator ITSELF is null).
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"primary_validator": map[string]any{"description": "unresolved this pass", "anchor": nil},
		"gaps":              []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "UNSTATED NULL", "primary_validator")
}

func TestGateRejectsARowWithTheWrongFieldType(t *testing.T) {
	// Codex round-1 P1, EXECUTED repro: primary_validator: 17 (the wrong
	// type entirely -- schema says object|null) previously returned no
	// errors. Full Draft 2020-12 validation over the whole document
	// catches this class categorically.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"primary_validator": float64(17)})})
	errs := f.check(t)
	mustContain(t, errs, "JSON SCHEMA VIOLATION")
}

func TestGateReadsIssuedCredentialDirectionAndExposureReachabilityLive(t *testing.T) {
	// Codex round-1 P2: these two enums were hardcoded Go maps instead of
	// read from the schema like every other enum -- so a legitimate
	// future schema addition to either would have been rejected as
	// UNKNOWN. Prove the live-schema contract holds for both by adding a
	// new enum value to a fixture schema copy and confirming a row using
	// it is accepted -- the same standard
	// TestSchemaEnumReadsServerActionLiveFromSchema already holds
	// surface_kind to.
	root := t.TempDir()
	seedFixtureAppGo(t, root)
	schemaPath, credentialClassesPath := seedFixtureSchemaAndCredentialClasses(t, root)
	schema, err := loadJSON(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	issuedCredential := asObject(asObject(schema["$defs"])["issuedCredential"])
	direction := asObject(asObject(issuedCredential["properties"])["direction"])
	direction["enum"] = append(asArray(direction["enum"]), "minted_to_broker")
	endpointProfile := asObject(asObject(schema["$defs"])["endpointProfile"])
	exposure := asObject(asObject(endpointProfile["properties"])["exposure"])
	reachability := asObject(asObject(exposure["properties"])["reachability"])
	reachability["enum"] = append(asArray(reachability["enum"]), "edge_and_direct")
	writeJSON(t, schemaPath, schema)

	row := minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "minted_to_broker",
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(7), "note": "Handler mints it"},
			},
		},
		"exposure": map[string]any{"reachability": "edge_and_direct", "source": "fixture"},
		"gaps":     []any{},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesDuplicateSurfaceOwnership(t *testing.T) {
	// Codex round-1 P2, EXECUTED repro: two rows with DIFFERENT ids (so
	// the plain duplicate-id check doesn't fire) both anchored at the
	// SAME discovered (file, line) surface, with conflicting
	// classifications, used to return OK.
	rowA := minimalValidRow(map[string]any{"id": "GET /healthz [dev-health-acr-api] (a)"})
	rowB := minimalValidRow(map[string]any{
		"id":                          "GET /healthz [dev-health-acr-api] (b)",
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
	})
	f := minimalValidFixture(t, []map[string]any{rowA, rowB})
	errs := f.check(t)
	mustContain(t, errs, "DUPLICATE SURFACE OWNERSHIP", fixtureAppFile)
}

func TestGateCatchesATrivialIssuedCredentialAnchor(t *testing.T) {
	// Codex round-1 P2, EXECUTED repro: an issued_credential anchor
	// mutated to an unrelated line (real file, in-bounds -- passes the
	// existence/bounds check) was accepted as a mint site.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				// line 1 of the fixture app.go is "package api" -- a
				// real, in-bounds line, but never a mint site.
				"anchor": map[string]any{"path": fixtureAppFile, "line": float64(1)},
			},
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "TRIVIAL ANCHOR", "issued_credential")
}

func TestGateCatchesTheRealCommittedOffByOneAnchorBug(t *testing.T) {
	// Coordinator-verified real defect (2026-09-01): the committed
	// POST /api/v1/context-fabric/investigations row anchored its
	// primary_validator at context_fabric_routes.go:156, which is `})` --
	// the close of the handler literal, one line off the actual
	// `return a.protectedRuntimeHandler(...)` at line 157. Fixed in the
	// data; this is the regression guard, reproducing the EXACT bug shape
	// (a `})` line immediately preceding the real return statement) against
	// a synthetic fixture so it can never silently reappear.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+
			"\n"+
			"import \"net/http\"\n"+
			"\n"+
			"func Handler() http.Handler {\n"+
			"	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n"+
			"		w.WriteHeader(http.StatusOK)\n"+
			"	})\n"+
			"	return protectedRuntimeHandler(handler)\n"+
			"}\n"+
			"\n"+
			"func protectedRuntimeHandler(next http.Handler) http.Handler { return next }\n",
	)
	schemaPath, credentialClassesPath := seedFixtureSchemaAndCredentialClasses(t, root)
	row := minimalValidRow(map[string]any{
		"primary_validator": map[string]any{
			"description": "wraps itself in protectedRuntimeHandler",
			// The exact bug: anchored one line above the real
			// `return protectedRuntimeHandler(handler)` call, at the bare
			// "})" that closes the inner handler literal instead.
			"anchor": map[string]any{"path": fixtureAppFile, "line": float64(8)},
		},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "TRIVIAL ANCHOR", "primary_validator")
}

func TestGateCatchesAnIssuedCredentialAnchorWithNoExtractableFunctionName(t *testing.T) {
	// Coordinator ruling (2026-09-01): "where [content] cannot be
	// established, say so in the message rather than passing." An anchor
	// landing on a line with no function/method declaration at or near it
	// (here: a bare import line, no enclosing func at all in this tiny
	// fixture) must be reported, not silently accepted just because the
	// file exists and the line is in bounds.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile), "package api\n\nimport \"net/http\"\n")
	schemaPath, credentialClassesPath := seedFixtureSchemaAndCredentialClasses(t, root)
	row := minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(3)},
			},
		},
		"gaps": []any{},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "ANCHOR CONTENT UNVERIFIED", "issued_credential")
}

func TestGateCatchesAnIssuedCredentialAnchorWhoseNoteNamesTheWrongFunction(t *testing.T) {
	// The core of the coordinator's fix: existence + in-bounds + non-trivial
	// is not enough. An anchor pointing at a REAL function declaration that
	// the row's own note/issuer never mentions is exactly the shape of the
	// real committed bug (an anchor near, but not at, the right site) --
	// this is the direct EXECUTED repro for the new content check, distinct
	// from the trivial-line and no-extractable-name cases above.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				// line 5 is "func Handler() http.Handler {" -- a real
				// function, but the note below names something else.
				"anchor": map[string]any{"path": fixtureAppFile, "line": float64(5), "note": "signs the token in mintCredential"},
			},
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	mustContain(t, errs, "ANCHOR CONTENT MISMATCH", "Handler")
}

func TestGateAcceptsAnIssuedCredentialAnchorWhoseIssuerFieldNamesTheFunction(t *testing.T) {
	// The content check accepts a match via EITHER anchor.note or
	// entry.issuer -- not note exclusively.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(5)},
				"issuer":    "acr Handler",
			},
		},
		"gaps": []any{},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesAnUnresolvedRegistration(t *testing.T) {
	// Codex round-1 P1, ARGUED (verified here with an EXECUTED repro): a
	// mux.Handle*(...) call whose pattern expression cannot be resolved
	// to a literal never becomes a Route at all -- it is reported under
	// discovery's separate Unresolved list. Ignoring that list makes such
	// a registration entirely invisible to the unowned-surface check: a
	// G-1 hole, not just a missing row.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+
			"\n"+
			"import \"net/http\"\n"+
			"\n"+
			"func Handler(dynamicPattern string) http.Handler {\n"+
			"	mux := http.NewServeMux()\n"+
			"	mux.HandleFunc(dynamicPattern, healthzHandler)\n"+
			"	return mux\n"+
			"}\n"+
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n")
	schemaPath, credentialClassesPath := seedFixtureSchemaAndCredentialClasses(t, root)
	inventoryPath := writeInventory(t, root, nil)
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "UNRESOLVED REGISTRATION")
}

func TestSchemaEnumReadsServerActionLiveFromSchema(t *testing.T) {
	// The gate reads surface_kind's enum LIVE from the schema file rather
	// than hardcoding it, so it accepts a schema-level vocabulary addition
	// (server_action, added for the web lane's Next.js Server Action
	// ruling) with zero checker code change.
	root := t.TempDir()
	schemaPath, _ := seedFixtureSchemaAndCredentialClasses(t, root)
	schema, err := loadJSON(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	vocab := schemaEnum(schema, "surface_kind")
	if !vocab["server_action"] {
		t.Fatalf("expected surface_kind vocabulary to include server_action, got %v", vocab)
	}
}

// ---------------------------------------------------------------------------
// DISCLOSURE-HOLD reporting: report-only, never a check() failure.
// ---------------------------------------------------------------------------

func TestDisclosureHoldMarkerIsReportedNotRejected(t *testing.T) {
	row := minimalValidRow(map[string]any{
		"gaps": []any{"DISCLOSURE-HOLD: pending fix for CHAOS-9999"},
	})
	f := minimalValidFixture(t, []map[string]any{row})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
	held := findDisclosureHoldRows([]any{row})
	if len(held) != 1 || held[0] != row["id"] {
		t.Fatalf("expected [%v], got %v", row["id"], held)
	}
}

func TestDisclosureHoldMarkerAbsentReportsNothing(t *testing.T) {
	held := findDisclosureHoldRows([]any{
		map[string]any{"id": "GET /healthz [dev-health-acr-api]", "gaps": []any{"ordinary undetermined field"}},
	})
	if len(held) != 0 {
		t.Fatalf("expected no held rows, got %v", held)
	}
}

func TestDisclosureHoldMarkerFoundAnywhereInRowNotOnlyGaps(t *testing.T) {
	row := map[string]any{
		"id":   "POST /example [dev-health-acr-api]",
		"gaps": []any{},
		"primary_validator": map[string]any{
			"description": "ok",
			"anchor":      map[string]any{"path": "x.go", "line": float64(1), "note": "DISCLOSURE-HOLD pending CHAOS-1234"},
		},
	}
	held := findDisclosureHoldRows([]any{row})
	if len(held) != 1 || held[0] != row["id"] {
		t.Fatalf("expected [%v], got %v", row["id"], held)
	}
}
