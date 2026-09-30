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
	"sort"
	"strconv"
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
// contractGateRequired reports whether this process is the CI step whose
// job is to run the real-tree contract proof.
//
// It keys on ACR_CONTRACT_GATE, set only by the "Verify endpoint-profile
// inventory against the pinned ops contract" step in .github/workflows/ci.yml,
// NOT on CI. Merge-gate finding (CHAOS-3273): CI is set in every job on
// every runner, but the sparse checkout that delivers the ops-owned inputs
// exists in exactly one step -- so a guard keyed on CI failed in `unit` and
// in every `race` shard, producing a red build that said nothing about the
// contract. Wrong predicate, right instinct.
func contractGateRequired() bool {
	return os.Getenv("ACR_CONTRACT_GATE") == "required"
}

// opsOwnedFixturePaths locates the three ops-owned inputs from explicit
// environment variables, and from nowhere else.
//
// There used to be a fallback here that walked up to a sibling
// ops-worktrees/chaos-3273-wave0 checkout on the developer's machine. It is
// deleted deliberately: that crutch is why the CI failure above never
// reproduced locally. Every local run silently found a real schema through a
// path that exists on one workstation and nowhere else, so the gate looked
// green in exactly the place a human would check before pushing. A
// convenience that makes a broken CI wiring invisible locally is the class
// this wave exists to kill, not an exception to it.
//
// Local runs now set the three variables explicitly, or the proof skips and
// says so. A skip that names its reason is honest; a pass obtained through a
// path nobody declared is not.
func opsOwnedFixturePaths(t *testing.T) (schemaPath, credentialClassesPath, credentialClassesSchemaPath string, ok bool) {
	t.Helper()
	s := os.Getenv("ACR_ENDPOINT_PROFILE_SCHEMA")
	c := os.Getenv("ACR_CREDENTIAL_CLASSES")
	cs := os.Getenv("ACR_CREDENTIAL_CLASSES_SCHEMA")
	if s != "" && c != "" && cs != "" && fileExists(s) && fileExists(c) && fileExists(cs) {
		return s, c, cs, true
	}
	return "", "", "", false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestRealTreePassesTheGateToday(t *testing.T) {
	schemaPath, credentialClassesPath, credentialClassesSchemaPath, ok := opsOwnedFixturePaths(t)
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
		if contractGateRequired() {
			t.Fatalf(
				"ops-owned schema/credential-classes files not reachable in the contract-gate step "+
					"(ACR_ENDPOINT_PROFILE_SCHEMA=%q ACR_CREDENTIAL_CLASSES=%q ACR_CREDENTIAL_CLASSES_SCHEMA=%q) -- "+
					"the pinned sparse checkout (ci/ops-contract.pin) did not deliver "+
					"usable inputs; this must fail, not skip, or the contract gate silently never runs",
				os.Getenv("ACR_ENDPOINT_PROFILE_SCHEMA"), os.Getenv("ACR_CREDENTIAL_CLASSES"),
				os.Getenv("ACR_CREDENTIAL_CLASSES_SCHEMA"),
			)
		}
		t.Skip("ops-owned endpoint-profile.schema.json / credential-classes.json / " +
			"credential-classes.schema.json not reachable (they are owned by ops and not " +
			"vendored here -- see main.go doc comment). This is a SKIP only outside the " +
			"contract-gate step; inside it (ACR_CONTRACT_GATE=required) the same condition " +
			"is a FAILURE. To run this proof locally, set ACR_ENDPOINT_PROFILE_SCHEMA, " +
			"ACR_CREDENTIAL_CLASSES and ACR_CREDENTIAL_CLASSES_SCHEMA to a checkout of the " +
			"ops commit named in ci/ops-contract.pin")
	}
	root := repoRoot(t)
	inventoryPath := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("gate found %d violation(s) on the real tree:\n%s", len(errs), strings.Join(errs, "\n"))
	}
}

func TestRealTreeProofFailsLoudlyInTheContractGateStepWhenInputsAreMissing(t *testing.T) {
	// EXECUTED repro for the fix above: actually run this package's own
	// TestRealTreePassesTheGateToday in a subprocess with
	// ACR_CONTRACT_GATE=required and the three input env vars UNSET, and
	// assert it FAILS (not skips, not passes) -- proving the branch fires,
	// not just that the code compiles.
	//
	// The predicate is ACR_CONTRACT_GATE, not CI: keyed on CI this fired in
	// every job on the runner while the inputs arrived in only one, which
	// is how the first push of this branch went red for a reason that had
	// nothing to do with the contract.
	pkgDir := filepath.Join(repoRoot(t), "ci", "checkendpointprofiles")
	cmd := exec.Command("go", "test", "-run", "^TestRealTreePassesTheGateToday$", "-v", ".")
	cmd.Dir = pkgDir
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "ACR_ENDPOINT_PROFILE_SCHEMA=") || strings.HasPrefix(kv, "ACR_CREDENTIAL_CLASSES=") ||
			strings.HasPrefix(kv, "ACR_CREDENTIAL_CLASSES_SCHEMA=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	cmd.Env = append(filtered, "ACR_CONTRACT_GATE=required")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the subprocess to fail (ACR_CONTRACT_GATE=required, no ops inputs configured), but it exited 0:\n%s", output)
	}
	if strings.Contains(string(output), "--- SKIP") {
		t.Fatalf("expected a FAIL, not a SKIP, when ACR_CONTRACT_GATE=required and ops inputs are missing:\n%s", output)
	}
	if !strings.Contains(string(output), "--- FAIL") {
		t.Fatalf("expected an explicit --- FAIL in subprocess output:\n%s", output)
	}
}

func TestRealInventoryRowCountMatchesTheWave0Baseline(t *testing.T) {
	// 22 rows = 13 protected / 9 public: the Wave 0 baseline of 16 (12 / 4)
	// plus the four public OAuth authorization-server routes hosted MCP
	// clients log in through (metadata, authorize, token, register), the
	// OAuth consent route the web consent page calls (protected: web
	// assertion only), and the public RFC 8628 POST /device_authorization
	// route (CHAOS-6233) headless clients start a device grant through, and
	// (26 = 17 / 9) the four protected direct data routes CHAOS-7071 adds
	// under /api/v1/context-fabric/data/ (catalog, subjects, facts,
	// operations), and (27 = 18 / 9) the protected read_relationships route
	// CHAOS-7074 adds there, and (28 = 19 / 9) the protected graphql_query
	// route CHAOS-7075 adds there. A different number is a finding to
	// reconcile, not an adjustment.
	root := repoRoot(t)
	inventoryPath := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
	inventory, err := loadJSON(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	rows := asArray(inventory["rows"])
	if len(rows) != 28 {
		t.Fatalf("expected 28 rows, got %d", len(rows))
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
	if protected != 19 {
		t.Errorf("expected 19 protected rows, got %d", protected)
	}
	if public != 9 {
		t.Errorf("expected 9 public rows, got %d", public)
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

// fixtureCredentialClassSchemaJSON mirrors the SHAPE of ops's real
// credential-classes.schema.json rather than its exact contents: a closed
// object per class carrying the four properties L0's inventory guarantees
// (issuer, validator, lifecycle authority, allowed route set). It is
// deliberately a fixture and not the real file for the same reason
// fixtureSchemaJSON is -- these tests must not depend on an ops checkout
// being present -- but it must REQUIRE more than class_id, because the
// merge-gate finding was that a document of bare {class_id} entries passed.
const fixtureCredentialClassSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["classes"],
  "properties": {
    "classes": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["class_id", "issuer", "validator", "lifecycle_authority", "allowed_routes"],
        "properties": {
          "class_id": {"type": "string"},
          "issuer": {"type": "string"},
          "validator": {"type": "string"},
          "lifecycle_authority": {"type": "string"},
          "allowed_routes": {"type": "array", "items": {"type": "string"}}
        }
      }
    }
  }
}`

func fixtureCredentialClass(id string) map[string]any {
	return map[string]any{
		"class_id":            id,
		"issuer":              "fixture-issuer",
		"validator":           "fixture-validator",
		"lifecycle_authority": "fixture-authority",
		"allowed_routes":      []string{"/fixture"},
	}
}

func seedFixtureSchemaAndCredentialClasses(t *testing.T, root string) (schemaPath, credentialClassesPath, credentialClassesSchemaPath string) {
	t.Helper()
	credentialClasses := map[string]any{
		"classes": []map[string]any{
			fixtureCredentialClass("acr_client_credential"),
			fixtureCredentialClass("acr_device_flow_code"),
			fixtureCredentialClass("acr_web_assertion"),
			fixtureCredentialClass("internal_svc_acr_token"),
		},
	}
	schemaPath = filepath.Join(root, "fixture-schema.json")
	credentialClassesPath = filepath.Join(root, "fixture-credential-classes.json")
	credentialClassesSchemaPath = filepath.Join(root, "fixture-credential-classes.schema.json")
	writeFile(t, schemaPath, fixtureSchemaJSON)
	writeJSON(t, credentialClassesPath, credentialClasses)
	writeFile(t, credentialClassesSchemaPath, fixtureCredentialClassSchemaJSON)
	return schemaPath, credentialClassesPath, credentialClassesSchemaPath
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
	root                        string
	inventoryPath               string
	schemaPath                  string
	credentialClassesPath       string
	credentialClassesSchemaPath string
	discovererPath              string
}

func minimalValidFixture(t *testing.T, rows []map[string]any) fixture {
	t.Helper()
	root := t.TempDir()
	seedFixtureAppGo(t, root)
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	inventoryPath := writeInventory(t, root, rows)
	return fixture{
		root:                        root,
		inventoryPath:               inventoryPath,
		schemaPath:                  schemaPath,
		credentialClassesPath:       credentialClassesPath,
		credentialClassesSchemaPath: credentialClassesSchemaPath,
		discovererPath:              realDiscovererPath(t),
	}
}

func (f fixture) check(t *testing.T) []string {
	t.Helper()
	errs, err := check(f.root, f.inventoryPath, f.schemaPath, f.credentialClassesPath, f.credentialClassesSchemaPath, f.discovererPath)
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
	mustContain(t, errs, "UNOWNED SURFACE", "POST /rogue", fixtureAppFile)
}

func TestGateCatchesAPhantomStaleRow(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{
		minimalValidRow(nil),
		minimalValidRow(map[string]any{"id": "GET /gone [dev-health-acr-api]", "route": "/gone"}),
	})
	errs := f.check(t)
	mustContain(t, errs, "PHANTOM ROW", "/gone")
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

// --- merge-gate round on 896ca76e: three EXECUTED false-negative classes ---
//
// Each of these was constructed by the reviewer as an inventory or vocabulary
// document INCONSISTENT with the source of truth that the gate nevertheless
// ACCEPTED (errs empty, exit 0). They are the reason the round happened after
// the push rather than before it.

func TestGateCatchesAValidButWrongService(t *testing.T) {
	// THE CLASS: not an unknown service (TestGateCatchesAnUnknownService
	// already covers that, and the schema's enum catches it) but a service
	// that is perfectly VALID in the enum and wrong for this surface.
	// Before the fix, relabelling a row registered on the acr api mux to
	// the also-valid dev-health-acr-mcp returned OK. `service` selects the
	// deployed app -- and therefore the middleware stack -- the row's
	// entire security analysis applies to, so a wrong-but-valid value
	// silently invalidates the row's reasoning while still satisfying G-1.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"service": "dev-health-acr-mcp",
	})})
	errs := f.check(t)
	mustContain(t, errs, "SERVICE MISMATCH", "dev-health-acr-mcp", "dev-health-acr-api")
}

func TestGateAcceptsTheServiceDiscoveryActuallyFound(t *testing.T) {
	// False-positive guard for the check above: the correct service must
	// still pass. A mismatch rule that rejects correct rows is worse than
	// no rule, because it gets disabled.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	errs := f.check(t)
	for _, e := range errs {
		if strings.Contains(e, "SERVICE MISMATCH") {
			t.Fatalf("correct service must not be reported as a mismatch: %s", e)
		}
	}
}

func TestGateCatchesCredentialClassesStrippedToIDsOnly(t *testing.T) {
	// THE CLASS: the closed vocabulary document was loaded only to harvest
	// class_ids and never validated against its own schema, so every class
	// reduced to a bare {"class_id": "..."} passed. "Closed vocabulary"
	// then means "closed set of ids", not the issuer / validator /
	// lifecycle-authority / allowed-route guarantee the inventory cites.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	writeJSON(t, f.credentialClassesPath, map[string]any{
		"classes": []map[string]any{
			{"class_id": "acr_client_credential"},
			{"class_id": "acr_device_flow_code"},
		},
	})
	errs := f.check(t)
	mustContain(t, errs, "CREDENTIAL CLASS SCHEMA VIOLATION")
}

func TestGateCatchesADuplicateCredentialClassID(t *testing.T) {
	// THE CLASS: two CONFLICTING definitions of one class_id both survived,
	// because the checker collapsed classes into a set. JSON Schema cannot
	// express uniqueness of a field across objects in an array (uniqueItems
	// compares whole items, and these differ in every other field), so this
	// has to be a checker rule.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	dup := fixtureCredentialClass("acr_client_credential")
	conflicting := fixtureCredentialClass("acr_client_credential")
	conflicting["issuer"] = "a-different-issuer"
	conflicting["validator"] = "a-different-validator"
	writeJSON(t, f.credentialClassesPath, map[string]any{
		"classes": []map[string]any{dup, conflicting},
	})
	errs := f.check(t)
	mustContain(t, errs, "DUPLICATE CREDENTIAL CLASS", "acr_client_credential")
}

// --- merge-gate round 2 on 889bef89: three more EXECUTED classes ----------

func TestGateCatchesAnAnchorThatEscapesTheRepository(t *testing.T) {
	// THE CLASS: the anchor path was joined to root with no containment
	// check, so a row could cite a file outside the repository entirely --
	// "../../../../../../etc/hosts" -- as its authentication validator and
	// the gate returned OK. The schema defines anchor paths as relative to
	// the owning repository; an anchor that leaves it is a claim about a
	// different codebase, which no amount of anchor-content checking here
	// can verify.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"primary_validator": map[string]any{
			"description": "claims an out-of-repo file as the validator",
			"anchor":      map[string]any{"path": "../../../../../../etc/hosts", "line": float64(1)},
		},
	})})
	errs := f.check(t)
	mustContain(t, errs, "ANCHOR ESCAPES REPO")
}

func TestGateCatchesAnAbsoluteAnchorPath(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"primary_validator": map[string]any{
			"description": "absolute path",
			"anchor":      map[string]any{"path": "/etc/hosts", "line": float64(1)},
		},
	})})
	errs := f.check(t)
	mustContain(t, errs, "ANCHOR ESCAPES REPO")
}

func TestGateCatchesAReversedAnchorRange(t *testing.T) {
	// THE CLASS, and it was not hypothetical: the shipped inventory carried
	// TWO of these (line=158 line_end=157, line=185 line_end=184), both
	// produced by a re-anchoring edit that bumped `line` and left `line_end`
	// behind. Only the start line was validated, and the issued-credential
	// reader silently clamped the reversed range -- so the rows read as
	// verified while describing a range that does not exist. A range the
	// gate silently repairs is a range nobody is told is wrong.
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"primary_validator": map[string]any{
			"description": "reversed range",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(7), "line_end": float64(6),
			},
		},
	})})
	errs := f.check(t)
	mustContain(t, errs, "INVALID ANCHOR RANGE", "line_end=6", "line=7")
}

func TestDiscoveryReportsAMultilineRegistrationRatherThanDroppingIt(t *testing.T) {
	// THE CLASS: the discovery regex reads one physical line, so a wrapped
	// mux.Handle( call matched nothing and was skipped -- neither discovered
	// nor listed as unresolved. Invisible, in the one file the script scans.
	// "Cannot parse" must never mean "cannot see".
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+
			"\n"+
			"import \"net/http\"\n"+
			"\n"+
			"func (a *App) Handler() http.Handler {\n"+
			"\tmux := http.NewServeMux()\n"+
			"\tmux.Handle(\n"+
			"\t\t\"GET /wrapped\",\n"+
			"\t\thttp.HandlerFunc(healthzHandler),\n"+
			"\t)\n"+
			"\treturn mux\n"+
			"}\n"+
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n")
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	inventoryPath := writeInventory(t, root, nil)
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "UNRESOLVED REGISTRATION", "but only 0 could be parsed")
}

func TestDiscoveryReportsBothRegistrationsWrittenOnOneLine(t *testing.T) {
	// Merge-gate round 3 (EXECUTED): discovery used FindStringSubmatch, one
	// match per line, so the SECOND registration on a line was silently
	// dropped -- an unprofiled route in source with the gate reporting the
	// inventory consistent with discovery. Round 2's fix (report lines that
	// match NOTHING) did not help, because this line matches once.
	//
	// The fixture profiles the FIRST registration and leaves the second
	// unowned, which is the construction that actually exposes the hole: with
	// both unprofiled the gate failed anyway, on the first one, and that
	// coincidence reads as a catch.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+
			"\n"+
			"import \"net/http\"\n"+
			"\n"+
			"func (a *App) Handler() http.Handler {\n"+
			"\tmux := http.NewServeMux()\n"+
			"\tmux.HandleFunc(\"GET /healthz\", healthzHandler); mux.HandleFunc(\"GET /unprofiled\", healthzHandler)\n"+
			"\treturn mux\n"+
			"}\n"+
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n")
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	inventoryPath := writeInventory(t, root, []map[string]any{minimalValidRow(nil)})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// Symbol addressing (CHAOS-7128) lets both registrations on one line be
	// profiled; the one with no row is simply unowned.
	mustContain(t, errs, "UNOWNED SURFACE", "GET /unprofiled")
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
	mustContain(t, errs, "PHANTOM ROW", "POST /healthz")
}

func TestGateCatchesContentDriftWhenTheRoutePathIsSwapped(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{"route": "/a-different-path"})})
	errs := f.check(t)
	mustContain(t, errs, "PHANTOM ROW", "/a-different-path")
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
				// The marker (first backticked span of the note) resolves to
				// line 7, inside func Handler() (declared at line 5) -- the
				// prose after it names Handler so the content-identity check
				// passes.
				"anchor":           map[string]any{"path": fixtureAppFile, "line": float64(7), "note": "`mux.HandleFunc(\"GET /healthz\"` -- Handler wires up the route"},
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
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
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
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(7), "note": "`mux.HandleFunc(\"GET /healthz\"` -- Handler mints it"},
			},
		},
		"exposure": map[string]any{"reachability": "edge_and_direct", "source": "fixture"},
		"gaps":     []any{},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
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
				// real line, but never a mint site. CHAOS-7245: the check
				// now runs at the MARKER's site, so the marker is what
				// resolves to it (the declared line is only a hint).
				"anchor": map[string]any{"path": fixtureAppFile, "line": float64(1), "note": "package api"},
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
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	run := func(anchor map[string]any) []string {
		t.Helper()
		row := minimalValidRow(map[string]any{
			"primary_validator": map[string]any{"description": "wraps itself in protectedRuntimeHandler", "anchor": anchor},
		})
		inventoryPath := writeInventory(t, root, []map[string]any{row})
		errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
		if err != nil {
			t.Fatal(err)
		}
		return errs
	}
	// The exact bug's line hint: one line above the real
	// `return protectedRuntimeHandler(handler)` call, at the bare "})" that
	// closes the inner handler literal instead.
	//
	// CHAOS-7245: the declared line is an advisory hint and is no longer read
	// for content, so the same hint is HARMLESS when the anchor's marker names
	// the real call (a bad hint cannot make a right anchor wrong, and a good
	// hint cannot make a wrong one right)...
	// (This fixture registers no route, so a PHANTOM ROW is expected and is
	// not what is under test: only anchor errors are counted.)
	for _, e := range run(map[string]any{"path": fixtureAppFile, "line": float64(8), "note": "return protectedRuntimeHandler(handler)"}) {
		if strings.Contains(e, "ANCHOR") {
			t.Fatalf("a stale line hint with a marker that resolves must not raise an anchor error, got: %s", e)
		}
	}
	// ...but the same anchor with NO marker is exactly the shape the bug
	// hid in, and is refused...
	mustContain(t, run(map[string]any{"path": fixtureAppFile, "line": float64(8)}), "MISSING ANCHOR MARKER", "primary_validator")
	// ...and so is a marker that itself resolves onto the `})` line.
	mustContain(t, run(map[string]any{"path": fixtureAppFile, "line": float64(9), "note": "\t})"}), "TRIVIAL ANCHOR", "primary_validator", "app.go:8")
}

// CHAOS-7128: rows are anchored by symbol (route+method for the surface, the
// anchor's note marker for the validator), not by line. A moved line passes;
// a renamed handler/route fails loudly.
func TestGateAcceptsAMovedLineBecauseAnchorsAreSymbolNotLine(t *testing.T) {
	row := minimalValidRow(map[string]any{
		// source.line and anchor.line are stale hints: the registration
		// is at line 7, the rows say 3 and 5.
		"source": map[string]any{"file": fixtureAppFile, "line": float64(3)},
		"primary_validator": map[string]any{
			"description": "mux.HandleFunc directly",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(5),
				"note": "mux.HandleFunc(\"GET /healthz\"",
			},
		},
	})
	f := minimalValidFixture(t, []map[string]any{row})
	if errs := f.check(t); len(errs) != 0 {
		t.Fatalf("a moved line must pass, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateFailsWhenTheAnchoredHandlerMarkerIsRenamed(t *testing.T) {
	// The anchored call is renamed IN THE FIXTURE SOURCE (not merely a note
	// naming something absent), so the marker that used to resolve is gone.
	row := minimalValidRow(map[string]any{
		"primary_validator": map[string]any{
			"description": "mux.HandleFunc directly",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(7),
				"note": "mux.HandleFunc(\"GET /healthz\", healthzHandler)",
			},
		},
	})
	f := minimalValidFixture(t, []map[string]any{row})
	src, err := os.ReadFile(filepath.Join(f.root, fixtureAppFile))
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.ReplaceAll(string(src), "healthzHandler", "renamedHandler")
	writeFile(t, filepath.Join(f.root, fixtureAppFile), renamed)
	errs := f.check(t)
	mustContain(t, errs, "ANCHOR MARKER NOT FOUND", "healthzHandler")
}

// r1 P1: two copies of the marker on ONE line are two occurrences, not one
// unambiguous relocation.
func TestGateRejectsTwoCopiesOfTheMarkerOnOneLine(t *testing.T) {
	row := minimalValidRow(map[string]any{
		"primary_validator": map[string]any{
			"description": "x",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(5),
				"note": "protectedRuntimeHandler(handler)",
			},
		},
	})
	f := minimalValidFixture(t, []map[string]any{row})
	src, err := os.ReadFile(filepath.Join(f.root, fixtureAppFile))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, fixtureAppFile),
		string(src)+"var a, b = \"protectedRuntimeHandler(handler)\", \"protectedRuntimeHandler(handler)\"\n")
	errs := f.check(t)
	mustContain(t, errs, "AMBIGUOUS ANCHOR MARKER", "2 times on line")
}

// r2 P1 fixture: two marker copies on the SIBLING's line; two rows declare two
// sites and two occurrences exist, but they sit on one line, so the line
// carrying both copies is ambiguous.
func TestGateRejectsTwoMarkerCopiesOnASiblingClaimedLine(t *testing.T) {
	pv := func(line float64) map[string]any {
		return map[string]any{"description": "x", "anchor": map[string]any{
			"path": fixtureAppFile, "line": line, "note": "validatorMarker(h)"}}
	}
	rowA := minimalValidRow(map[string]any{"id": "GET /a [dev-health-acr-api]", "route": "/a", "primary_validator": pv(4)})
	rowB := minimalValidRow(map[string]any{"id": "GET /b [dev-health-acr-api]", "route": "/b", "primary_validator": pv(8)})
	f := minimalValidFixture(t, []map[string]any{rowA, rowB})
	writeFile(t, filepath.Join(f.root, fixtureAppFile),
		"package api\n\nimport \"net/http\"\n\nfunc Handler() http.Handler {\n"+
			"\tmux := http.NewServeMux()\n"+
			"\tmux.HandleFunc(\"GET /a\", h)\n"+
			"\tmux.HandleFunc(\"GET /b\", h); _ = []string{\"validatorMarker(h)\", \"validatorMarker(h)\"}\n"+
			"\treturn mux\n}\n\nfunc h(w http.ResponseWriter, r *http.Request) {}\n")
	errs := f.check(t)
	mustContain(t, errs, "AMBIGUOUS ANCHOR MARKER", "validatorMarker(h)")
}

func markerFixture(t *testing.T, rows [][2]any, body string) fixture {
	t.Helper()
	var rs []map[string]any
	for _, r := range rows {
		route := r[0].(string)
		rs = append(rs, minimalValidRow(map[string]any{
			"id": "GET " + route + " [dev-health-acr-api]", "route": route,
			"primary_validator": map[string]any{"description": "x", "anchor": map[string]any{
				"path": fixtureAppFile, "line": r[1], "note": "validatorMarker(h)"}},
		}))
	}
	f := minimalValidFixture(t, rs)
	writeFile(t, filepath.Join(f.root, fixtureAppFile),
		"package api\n\nimport \"net/http\"\n\nfunc Handler() http.Handler {\n\tmux := http.NewServeMux()\n"+body+
			"\treturn mux\n}\n\nfunc h(w http.ResponseWriter, r *http.Request) {}\n")
	return f
}

// One invariant (r1+r2 P1 class): total marker occurrences in the file must
// equal the number of distinct sites the rows declare for it.
func TestGateMarkerInvariantOneCopyPerRowAnywhereInTheFilePasses(t *testing.T) {
	f := markerFixture(t, [][2]any{{"/a", float64(5)}, {"/b", float64(9)}},
		"\tmux.HandleFunc(\"GET /a\", h) // validatorMarker(h)\n\tmux.HandleFunc(\"GET /b\", h) // validatorMarker(h)\n")
	if errs := f.check(t); len(errs) != 0 {
		t.Fatalf("got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateMarkerInvariantThreeOccurrencesForTwoSitesFails(t *testing.T) {
	f := markerFixture(t, [][2]any{{"/a", float64(7)}, {"/b", float64(8)}},
		"\tmux.HandleFunc(\"GET /a\", h) // validatorMarker(h)\n\tmux.HandleFunc(\"GET /b\", h) // validatorMarker(h)\n\t// validatorMarker(h)\n")
	mustContain(t, f.check(t), "AMBIGUOUS ANCHOR MARKER", "3 times")
}

func TestGateMarkerInvariantFewerOccurrencesThanSitesFails(t *testing.T) {
	f := markerFixture(t, [][2]any{{"/a", float64(7)}, {"/b", float64(8)}},
		"\tmux.HandleFunc(\"GET /a\", h) // validatorMarker(h)\n\tmux.HandleFunc(\"GET /b\", h)\n")
	mustContain(t, f.check(t), "ANCHOR MARKER NOT FOUND", "1 time(s)")
}

// r3 P1: the same physical file addressed through an alias path must be ONE
// bucket: two rows (one via "./") declare two sites, the file holds one
// occurrence -> NOT FOUND (1 time for 2 sites), not a silent pass.
func TestGateMarkerPathAliasIsOneBucket(t *testing.T) {
	f := markerFixture(t, [][2]any{{"/a", float64(7)}, {"/b", float64(8)}},
		"\tmux.HandleFunc(\"GET /a\", h) // validatorMarker(h)\n\tmux.HandleFunc(\"GET /b\", h)\n")
	// rewrite row /b's anchor path to an alias of the same file
	raw, err := os.ReadFile(f.inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	alias := "internal/api/./app.go"
	var inv map[string]any
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	rows := inv["rows"].([]any)
	rows[1].(map[string]any)["primary_validator"].(map[string]any)["anchor"].(map[string]any)["path"] = alias
	writeJSON(t, f.inventoryPath, inv)
	errs := f.check(t)
	mustContain(t, errs, "ANCHOR MARKER NOT FOUND", "1 time(s)", "2 site(s)")
}

// Two sibling rows share one marker whose single occurrence has moved off
// the shared declared line: both pass (one occurrence, legitimately shared).
func TestGateAcceptsSiblingRowsSharingOneMovedMarker(t *testing.T) {
	pv := func() map[string]any {
		return map[string]any{
			"description": "shared dispatch",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(5),
				"note": "mux.HandleFunc(\"GET /a\"",
			},
		}
	}
	rowA := minimalValidRow(map[string]any{"id": "GET /a [dev-health-acr-api]", "route": "/a", "primary_validator": pv()})
	rowB := minimalValidRow(map[string]any{
		"id": "GET /b [dev-health-acr-api]", "route": "/b", "primary_validator": pv(),
	})
	f := minimalValidFixture(t, []map[string]any{rowA, rowB})
	// Fixture registers /healthz; register /a and /b so both rows exist, with
	// the shared marker text appearing exactly once (on /a's line).
	writeFile(t, filepath.Join(f.root, fixtureAppFile),
		"package api\n\nimport \"net/http\"\n\nfunc Handler() http.Handler {\n"+
			"\tmux := http.NewServeMux()\n"+
			"\tmux.HandleFunc(\"GET /a\", h)\n"+
			"\tmux.HandleFunc(\"GET /b\", h)\n\treturn mux\n}\n\nfunc h(w http.ResponseWriter, r *http.Request) {}\n")
	writeInventory(t, f.root, []map[string]any{rowA, rowB})
	if errs := f.check(t); len(errs) != 0 {
		t.Fatalf("got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateFailsWhenTheRegisteredRouteIsRenamed(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(nil)})
	src, err := os.ReadFile(filepath.Join(f.root, fixtureAppFile))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, fixtureAppFile), strings.ReplaceAll(string(src), "GET /healthz", "GET /healthz-renamed"))
	errs := f.check(t)
	mustContain(t, errs, "PHANTOM ROW", "GET /healthz")
	mustContain(t, errs, "UNOWNED SURFACE", "/healthz-renamed")
}
func TestGateCatchesAPrimaryValidatorAnchorWithNoMarker(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"primary_validator": map[string]any{
			"description": "no note supplied",
			"anchor":      map[string]any{"path": fixtureAppFile, "line": float64(7)},
		},
	})})
	errs := f.check(t)
	mustContain(t, errs, "MISSING ANCHOR MARKER", "primary_validator")
}

func TestGateCatchesAPrimaryValidatorAnchorWhoseMarkerIsNowhereInTheFile(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"primary_validator": map[string]any{
			"description": "marker text never appears in the fixture file",
			"anchor": map[string]any{
				"path": fixtureAppFile,
				"line": float64(7),
				"note": "thisTextIsNotInTheFixtureAppFileAnywhere(",
			},
		},
	})})
	errs := f.check(t)
	mustContain(t, errs, "ANCHOR MARKER NOT FOUND", "primary_validator")
}

func TestGatePassesAPrimaryValidatorAnchorWithAMatchingMarker(t *testing.T) {
	f := minimalValidFixture(t, []map[string]any{minimalValidRow(map[string]any{
		"primary_validator": map[string]any{
			"description": "healthzHandler is the mux route target",
			"anchor": map[string]any{
				"path": fixtureAppFile,
				"line": float64(7),
				"note": "mux.HandleFunc(\"GET /healthz\"",
			},
		},
	})})
	errs := f.check(t)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateAcceptsANonCommentTextCopyOfTheMarkerOnTheDeclaredLine_DocumentedTextMatchLimit(t *testing.T) {
	// Documents a KNOWN, disclosed limit rather than proving a gap closed: a
	// text match on the declared line is not proof the matched text is
	// executable code. A string literal (not a `//` comment, so the
	// pre-existing trivial-anchor denylist does not catch it either) that
	// happens to repeat the marker's exact text on the row's own declared
	// line is indistinguishable from a real anchor by this check, the same
	// way the pre-existing issued_credential name-match cannot tell a real
	// mint call from a same-named comment. This guards against a FUTURE
	// tightening accidentally rejecting the legitimate case two real rows
	// already rely on (model-config PUT/DELETE share one source line and
	// therefore one marker).
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+ // 1
			"\n"+ // 2
			"import \"net/http\"\n"+ // 3
			"\n"+ // 4
			"func Handler() http.Handler {\n"+ // 5
			"	var historicalNote = \"protectedRuntimeHandler(handler) used to be called here\"\n"+ // 6
			"	_ = historicalNote\n"+ // 7
			"	mux := http.NewServeMux()\n"+ // 8
			"	mux.HandleFunc(\"GET /healthz\", healthzHandler)\n"+ // 9
			"	return mux\n"+ // 10
			"}\n"+ // 11
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n"+
			"\n"+
			"func protectedRuntimeHandler(next http.Handler) http.Handler { return next }\n",
	)
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	row := minimalValidRow(map[string]any{
		"source": map[string]any{"file": fixtureAppFile, "line": float64(9)},
		"primary_validator": map[string]any{
			"description": "no longer wraps itself in protectedRuntimeHandler",
			// Declares the string-literal line -- the real call was
			// removed, but its name lives on in leftover text on the
			// declared line.
			"anchor": map[string]any{
				"path": fixtureAppFile,
				"line": float64(6),
				"note": "protectedRuntimeHandler(handler)",
			},
		},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no errors (marker is genuinely present on the declared line, even "+
			"though it is a comment, not code -- see KNOWN LIMITS: a text match is not a proof "+
			"of executable code), got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateCatchesAPrimaryValidatorMarkerThatAlsoAppearsUnexplainedElsewhere(t *testing.T) {
	// The exact shape a range-wide check let through: the declared line
	// carries only a leftover copy of the marker, and the real call sits at
	// an UNCLAIMED line elsewhere in the file (no other row's anchor claims
	// it). Neither occurrence alone tells this gate which is real, so
	// finding both must fail loudly instead of trusting the declared one.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+ // 1
			"\n"+ // 2
			"import \"net/http\"\n"+ // 3
			"\n"+ // 4
			"func Handler() http.Handler {\n"+ // 5
			"	var historicalNote = \"protectedRuntimeHandler(handler) used to be called here\"\n"+ // 6
			"	_ = historicalNote\n"+ // 7
			"	mux := http.NewServeMux()\n"+ // 8
			"	mux.HandleFunc(\"GET /healthz\", healthzHandler)\n"+ // 9
			"	return protectedRuntimeHandler(handler)\n"+ // 10 -- the real, unclaimed call
			"}\n"+ // 11
			"\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n"+
			"\n"+
			"func protectedRuntimeHandler(next http.Handler) http.Handler { return next }\n"+
			"\n"+
			"var mux2 = http.NewServeMux()\n",
	)
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	row := minimalValidRow(map[string]any{
		"source": map[string]any{"file": fixtureAppFile, "line": float64(9)},
		"primary_validator": map[string]any{
			"description": "wraps itself in protectedRuntimeHandler",
			// Declares the string-literal line, not the real call at line
			// 10 -- and no OTHER row's anchor claims line 10, so it is
			// unexplained, not a sibling.
			"anchor": map[string]any{
				"path": fixtureAppFile,
				"line": float64(6),
				"note": "protectedRuntimeHandler(handler)",
			},
		},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "AMBIGUOUS ANCHOR MARKER", "primary_validator")
}

func TestGateAcceptsTwoRowsThatLegitimatelyShareOnePrimaryValidatorMarker(t *testing.T) {
	// The model-config PUT/DELETE shape: two DIFFERENT rows, each correctly
	// anchored at its own dispatch line, whose source lines happen to be
	// textually identical and therefore share one marker. Neither row's
	// anchor may flag the other's identical, legitimately-claimed line as
	// an unexplained duplicate.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, fixtureAppFile),
		"package api\n"+ // 1
			"\n"+ // 2
			"import \"net/http\"\n"+ // 3
			"\n"+ // 4
			"func HandlerA() http.Handler {\n"+ // 5
			"	return dispatch(true)\n"+ // 6
			"}\n"+ // 7
			"\n"+
			"func HandlerB() http.Handler {\n"+ // 9
			"	return dispatch(true)\n"+ // 10
			"}\n"+ // 11
			"\n"+
			"func dispatch(ok bool) http.Handler { return nil }\n",
	)
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	rowA := minimalValidRow(map[string]any{
		"id":     "GET /healthz [dev-health-acr-api] (a)",
		"source": map[string]any{"file": fixtureAppFile, "line": float64(6)},
		"primary_validator": map[string]any{
			"description": "dispatch, handler A",
			"anchor":      map[string]any{"path": fixtureAppFile, "line": float64(6), "note": "dispatch(true)"},
		},
	})
	rowB := minimalValidRow(map[string]any{
		"id":     "GET /healthz [dev-health-acr-api] (b)",
		"source": map[string]any{"file": fixtureAppFile, "line": float64(10)},
		"primary_validator": map[string]any{
			"description": "dispatch, handler B",
			"anchor":      map[string]any{"path": fixtureAppFile, "line": float64(10), "note": "dispatch(true)"},
		},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{rowA, rowB})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// This fixture's routes are not independently discoverable (no real
	// mux.HandleFunc registration backs them), so PHANTOM ROW is expected
	// noise here; only the ANCHOR-related classes -- what this test is
	// actually about -- are asserted absent.
	for _, e := range errs {
		if strings.Contains(e, "ANCHOR") {
			t.Fatalf("expected no ANCHOR-related errors (both rows' anchors are legitimate, "+
				"sibling occurrences of one another), got:\n%s", strings.Join(errs, "\n"))
		}
	}
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
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	row := minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"issued_credential": []any{
			map[string]any{
				"class_id":  "acr_client_credential",
				"direction": "returned_to_caller",
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(3), "note": "import \"net/http\""},
			},
		},
		"gaps": []any{},
	})
	inventoryPath := writeInventory(t, root, []map[string]any{row})
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
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
				// The marker resolves to line 6, inside "func Handler()
				// http.Handler {" -- a real function, but the prose after the
				// marker names something else.
				"anchor": map[string]any{"path": fixtureAppFile, "line": float64(6), "note": "`mux := http.NewServeMux()` -- signs the token in mintCredential"},
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
				"anchor":    map[string]any{"path": fixtureAppFile, "line": float64(6), "note": "mux := http.NewServeMux()"},
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
	schemaPath, credentialClassesPath, credentialClassesSchemaPath := seedFixtureSchemaAndCredentialClasses(t, root)
	inventoryPath := writeInventory(t, root, nil)
	errs, err := check(root, inventoryPath, schemaPath, credentialClassesPath, credentialClassesSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "UNRESOLVED REGISTRATION")
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

// ---------------------------------------------------------------------------
// CHAOS-7245: every anchor kind is located by its marker, and the declared
// line is only a hint. Before this, a pure line shift (an unrelated field added
// above protectedRuntimeHandler) failed ten rows as TRIVIAL ANCHOR, and the
// row's owner re-anchored them by hand -- on #730 and again on #731. The tests
// below pair every "a moved symbol passes" with the "a real change fails" that
// keeps the pass from meaning "checks nothing".
// ---------------------------------------------------------------------------

// shiftFixtureSource inserts n comment lines directly after the package clause
// of the file at path: an edit that changes no symbol and every later line
// number, which is exactly an unrelated addition above the anchored code.
func shiftFixtureSource(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(raw), "\n", 2)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "package ") {
		t.Fatalf("%s does not start with a package clause", path)
	}
	filler := strings.Repeat("// unrelated addition that shifts every later line\n", n)
	writeFile(t, path, lines[0]+"\n"+filler+lines[1])
}

// anchorKindsRow carries one anchor of each kind against seedFixtureAppGo's
// file, every anchor with a marker, and every declared line hint computed
// against the UNSHIFTED file (so it goes stale the moment the file shifts).
func anchorKindsRow() map[string]any {
	return minimalValidRow(map[string]any{
		"classification":              "protected",
		"public_rationale":            nil,
		"accepted_credential_classes": []any{"acr_client_credential"},
		"primary_validator": map[string]any{
			"description": "the registration itself",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(7), "line_end": float64(7),
				"note": "mux.HandleFunc(\"GET /healthz\", healthzHandler)",
			},
		},
		"reachable_validators": []any{map[string]any{
			"description": "the handler the mux dispatches to",
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(11), "line_end": float64(11),
				"note": "func healthzHandler(",
			},
			"is_intended_validator":   true,
			"reachable_but_not_owner": false,
		}},
		"issued_credential": []any{map[string]any{
			"class_id":  "acr_client_credential",
			"direction": "returned_to_caller",
			// Marker = first backticked span; the prose after it names the
			// enclosing function, as the identity check requires.
			"anchor": map[string]any{
				"path": fixtureAppFile, "line": float64(8), "line_end": float64(8),
				"note": "`return mux` -- Handler returns the mux",
			},
		}},
		"gaps": []any{},
	})
}

func TestGateAcceptsAPureLineShiftOfEveryAnchorKind(t *testing.T) {
	// n=2 is chosen to bite: with the hints above, the OLD gate read hint 11 as
	// `}` (TRIVIAL ANCHOR) and hint 8 as a line inside no function
	// (ANCHOR CONTENT UNVERIFIED). 0 is the control that the row is valid at all.
	for _, n := range []int{0, 1, 2, 3, 4, 5, 9, 50} {
		t.Run("shift_"+strconv.Itoa(n), func(t *testing.T) {
			f := minimalValidFixture(t, []map[string]any{anchorKindsRow()})
			shiftFixtureSource(t, filepath.Join(f.root, fixtureAppFile), n)
			if errs := f.check(t); len(errs) != 0 {
				t.Fatalf("a pure shift of %d line(s) must pass, got:\n%s", n, strings.Join(errs, "\n"))
			}
		})
	}
}

func TestGateTreatsTheDeclaredLineAsAnAdvisoryHintEvenWhenItIsPastTheEndOfTheFile(t *testing.T) {
	// The hint is not compared with the file at all: a hint far outside it is
	// exactly as harmless as one a few lines off, because the marker resolves.
	row := anchorKindsRow()
	for _, a := range rowAnchorObjects(row) {
		a["line"], a["line_end"] = float64(9999), float64(10000)
	}
	f := minimalValidFixture(t, []map[string]any{row})
	if errs := f.check(t); len(errs) != 0 {
		t.Fatalf("a hint past the end of the file must pass when the marker resolves, got:\n%s", strings.Join(errs, "\n"))
	}
}

func TestGateFailsWhenTheMarkedSymbolOfAnyAnchorKindChanges(t *testing.T) {
	// The pair to the shift test above: the SAME row, but the marked text is
	// really renamed or removed in the source. Each kind must fail on its own
	// label, so a check that only covers one kind cannot pass this table.
	cases := []struct {
		kind, label string
		mutate      func(string) string
	}{
		{"primary_validator", "primary_validator", func(s string) string {
			return strings.Replace(s, "mux.HandleFunc(\"GET /healthz\", healthzHandler)", "mux.HandleFunc(\"GET /healthz\", healthzHandler2)", 1)
		}},
		{"reachable_validators", "reachable_validators[0]", func(s string) string {
			return strings.Replace(s, "func healthzHandler(", "func renamedHandler(", 1)
		}},
		{"issued_credential", "issued_credential", func(s string) string {
			return strings.Replace(s, "\treturn mux\n", "\treturn  mux\n", 1)
		}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			f := minimalValidFixture(t, []map[string]any{anchorKindsRow()})
			p := filepath.Join(f.root, fixtureAppFile)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			mutated := c.mutate(string(raw))
			if mutated == string(raw) {
				t.Fatal("mutation did not change the fixture source")
			}
			writeFile(t, p, mutated)
			mustContain(t, f.check(t), "ANCHOR MARKER NOT FOUND", c.label)
		})
	}
}

func TestGateFailsWhenAnyAnchorKindHasNoMarker(t *testing.T) {
	// reachable_validators and issued_credential used to be exempt, which is
	// why they broke on a pure shift: with no marker their content checks read
	// whatever the declared line held.
	for _, kind := range []string{"reachable_validators", "issued_credential"} {
		t.Run(kind, func(t *testing.T) {
			row := anchorKindsRow()
			label := kind
			if kind == "reachable_validators" {
				label = "reachable_validators[0]"
				delete(asObject(asArray(row["reachable_validators"])[0])["anchor"].(map[string]any), "note")
			} else {
				delete(asObject(asArray(row["issued_credential"])[0])["anchor"].(map[string]any), "note")
			}
			f := minimalValidFixture(t, []map[string]any{row})
			mustContain(t, f.check(t), "MISSING ANCHOR MARKER", label)
		})
	}
}

func TestGateFailsWhenAnIssuedMarkerMovesIntoAnotherFunction(t *testing.T) {
	// A move that a shift is NOT: the marked call still exists, exactly once,
	// but now sits in a different function than the row's prose names. The
	// identity check follows the marker's site, so it sees Other, not Handler.
	f := minimalValidFixture(t, []map[string]any{anchorKindsRow()})
	writeFile(t, filepath.Join(f.root, fixtureAppFile),
		"package api\n\nimport \"net/http\"\n\n"+
			"func Handler() http.Handler {\n"+
			"	mux := http.NewServeMux()\n"+
			"	mux.HandleFunc(\"GET /healthz\", healthzHandler)\n"+
			"	return nil\n"+
			"}\n\n"+
			"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n\n"+
			"func Other() http.Handler {\n"+
			"	return mux\n"+
			"}\n")
	mustContain(t, f.check(t), "ANCHOR CONTENT MISMATCH", "Other")
}

func TestGateResolvesEachSiteOfASharedMarkerByRank(t *testing.T) {
	// Two anchors cite one marker at two sites (the same call in two
	// functions). Each is paired with its own site by order, so a shift that
	// moves both keeps the pairing; swapping which prose names which function
	// is a real error and is reported for both.
	src := "package api\n\nimport \"net/http\"\n\n" +
		"func Handler() http.Handler {\n" +
		"	mux := http.NewServeMux()\n" +
		"	mux.HandleFunc(\"GET /healthz\", healthzHandler)\n" +
		"	return mux\n" +
		"}\n\n" +
		"func healthzHandler(w http.ResponseWriter, r *http.Request) {}\n\n" +
		"func mintAlpha() string {\n" + // 13
		"	return signToken()\n" + // 14
		"}\n\n" +
		"func mintBeta() string {\n" + // 17
		"	return signToken()\n" + // 18
		"}\n"
	entry := func(line int, prose string) map[string]any {
		return map[string]any{
			"class_id": "acr_client_credential", "direction": "returned_to_caller",
			"anchor": map[string]any{"path": fixtureAppFile, "line": float64(line), "note": "`return signToken()` -- " + prose},
		}
	}
	build := func(alphaProse, betaProse string) fixture {
		row := minimalValidRow(map[string]any{
			"classification":              "protected",
			"public_rationale":            nil,
			"accepted_credential_classes": []any{"acr_client_credential"},
			"issued_credential":           []any{entry(14, alphaProse), entry(18, betaProse)},
			"gaps":                        []any{},
		})
		f := minimalValidFixture(t, []map[string]any{row})
		writeFile(t, filepath.Join(f.root, fixtureAppFile), src)
		return f
	}
	for _, n := range []int{0, 3} {
		f := build("mintAlpha mints", "mintBeta mints")
		shiftFixtureSource(t, filepath.Join(f.root, fixtureAppFile), n)
		if errs := f.check(t); len(errs) != 0 {
			t.Fatalf("shift %d: each anchor must resolve to its own site, got:\n%s", n, strings.Join(errs, "\n"))
		}
	}
	swapped := build("mintBeta mints", "mintAlpha mints")
	errs := swapped.check(t)
	mustContain(t, errs, "ANCHOR CONTENT MISMATCH", "mintAlpha")
	mustContain(t, errs, "ANCHOR CONTENT MISMATCH", "mintBeta")
}

func TestGateRefusesAnAnchorWhoseLineIsNotALineNumber(t *testing.T) {
	// What is left of the hint that is wrong whatever the file says.
	row := anchorKindsRow()
	asObject(asObject(row["primary_validator"])["anchor"])["line"] = float64(0)
	f := minimalValidFixture(t, []map[string]any{row})
	mustContain(t, f.check(t), "STALE ANCHOR", "line=0")
}

func TestAnchorMarkerIsTheFirstBacktickedSpanElseTheWholeNote(t *testing.T) {
	cases := map[string]string{
		"func (a *App) protectedRuntimeHandler(":           "func (a *App) protectedRuntimeHandler(",
		"`func (s *S) Start(` -- mints it":                 "func (s *S) Start(",
		"prose first `return x` then `return y`":           "return x",
		"an unterminated `backtick is the whole note":      "an unterminated `backtick is the whole note",
		"an empty `` span is the whole note":               "an empty `` span is the whole note",
		"":                                                 "",
		"mux.HandleFunc(\"GET /healthz\", healthzHandler)": "mux.HandleFunc(\"GET /healthz\", healthzHandler)",
	}
	for note, want := range cases {
		if got := anchorMarker(note); got != want {
			t.Errorf("anchorMarker(%q) = %q, want %q", note, got, want)
		}
	}
}

// --- the real tree ------------------------------------------------------------

// realTreeOpsInputs returns the three ops-owned inputs for a real-tree proof,
// with the same rule as TestRealTreePassesTheGateToday: outside the
// contract-gate step a missing input skips and says why; inside it (the
// ACR_CONTRACT_GATE marker) a missing input FAILS, so the proof cannot
// silently never run.
func realTreeOpsInputs(t *testing.T) (schemaPath, credentialClassesPath, credentialClassesSchemaPath string) {
	t.Helper()
	s, c, cs, ok := opsOwnedFixturePaths(t)
	if ok {
		return s, c, cs
	}
	if contractGateRequired() {
		t.Fatal("ops-owned schema/credential-classes files not reachable in the contract-gate step; " +
			"this proof must fail, not skip, or it silently never runs")
	}
	t.Skip("ops-owned endpoint-profile inputs not reachable (set ACR_ENDPOINT_PROFILE_SCHEMA, " +
		"ACR_CREDENTIAL_CLASSES and ACR_CREDENTIAL_CLASSES_SCHEMA); this proof only runs in the contract-gate step")
	return "", "", ""
}

// copyRealSourceTree copies the real repository's Go sources and the real
// inventory into a temp root, so a proof can edit files without touching the
// working tree. Test files are left out: discovery does not read them.
func copyRealSourceTree(t *testing.T) string {
	t.Helper()
	realRoot := repoRoot(t)
	dst := t.TempDir()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(realRoot, top), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(realRoot, p)
			if relErr != nil {
				return relErr
			}
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			writeFile(t, filepath.Join(dst, rel), string(raw))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	inv, err := os.ReadFile(filepath.Join(realRoot, "contracts", "auth", "v1", "endpoint-profiles.acr.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dst, "contracts", "auth", "v1", "endpoint-profiles.acr.json"), string(inv))
	return dst
}

// anchoredRealFiles is every file any row's anchor points at.
func anchoredRealFiles(t *testing.T, root string) []string {
	t.Helper()
	inventory, err := loadJSON(filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, raw := range asArray(inventory["rows"]) {
		for _, a := range rowAnchorObjects(asObject(raw)) {
			if p, ok := asCanonicalPath(a["path"]); ok && p != "" {
				seen[p] = true
			}
		}
	}
	files := make([]string, 0, len(seen))
	for p := range seen {
		files = append(files, p)
	}
	sort.Strings(files)
	return files
}

func TestRealTreeSurvivesAPureLineShiftOfEveryAnchoredFile(t *testing.T) {
	// The incident, on the real tree: an unrelated addition above an anchored
	// symbol. Measured on 1a1ef01e, inserting FOUR comment lines at the top of
	// any one of 10 of the 13 anchored files failed this gate. Every anchored
	// file is shifted at once here, by 1 and by 4 lines, and the gate must not
	// notice.
	schemaPath, ccPath, ccSchemaPath := realTreeOpsInputs(t)
	for _, n := range []int{1, 4} {
		root := copyRealSourceTree(t)
		files := anchoredRealFiles(t, root)
		if len(files) < 10 {
			t.Fatalf("expected the real inventory to anchor into 10+ files, found %d: %v", len(files), files)
		}
		for _, f := range files {
			shiftFixtureSource(t, filepath.Join(root, f), n)
		}
		inventoryPath := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
		errs, err := check(root, inventoryPath, schemaPath, ccPath, ccSchemaPath, realDiscovererPath(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(errs) != 0 {
			t.Fatalf("a pure shift of %d line(s) in every anchored file must pass, got %d violation(s):\n%s", n, len(errs), strings.Join(errs, "\n"))
		}
	}
}

func TestRealTreeStillFailsWhenAnAnchoredSymbolIsRenamed(t *testing.T) {
	// The pair to the shift proof: the symbol the ten protectedRuntimeHandler
	// rows are marked by is really renamed, and the gate says so.
	schemaPath, ccPath, ccSchemaPath := realTreeOpsInputs(t)
	root := copyRealSourceTree(t)
	p := filepath.Join(root, "internal", "api", "runtime.go")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(raw), "func (a *App) protectedRuntimeHandler(", "func (a *App) protectedRuntimeHandlerRenamed(", 1)
	if mutated == string(raw) {
		t.Fatal("the marked declaration was not found in the real runtime.go; the fixture for this proof is stale")
	}
	writeFile(t, p, mutated)
	inventoryPath := filepath.Join(root, "contracts", "auth", "v1", "endpoint-profiles.acr.json")
	errs, err := check(root, inventoryPath, schemaPath, ccPath, ccSchemaPath, realDiscovererPath(t))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, errs, "ANCHOR MARKER NOT FOUND", "protectedRuntimeHandler(", "internal/api/runtime.go")
}
