package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const testTaskID = "task-001-checkout-flake-exact-commit"
const testEvidenceRefID = "acr:v1:commit:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

// repoRoot locates the repository root from this test's working directory
// (tests/fullstack/assertrun) so schema paths resolve regardless of how `go test` is invoked.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "..")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validCapabilities = `{
  "schema_version":"capabilities.v1","service":"dev-health-acr","service_version":"1.0.0",
  "minimum_sidecar_version":"1.0.0","supported_schema_versions":["capabilities.v1"],
  "enabled_tools":["context_for_task","source_evidence"],
  "entitlements":{"agent_context_runtime":true},
  "permissions":{"context_read":true,"evidence_read":true,"episode_write":false},
  "limits":{"max_items":10,"max_output_tokens":100,"max_serialized_bytes":1000,"requests_per_minute":10},
  "generated_at":"2026-01-14T12:00:00Z"
}`

const validMCPTools = `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"context_for_task"},{"name":"source_evidence"}]}}`

const validServiceReadiness = `{"project":"acr-fs-test","services":[{"service":"postgres","state":"running","health":""},{"service":"clickhouse","state":"running","health":""}]}`

const validFixtureVerification = `{"schema_version":"fullstack_fixture_verification.v1","fixture_version":"2026-07-23.1","corpus_hashes":[],"seed_hashes":[],"probes":[],"ok":true}`

func validContextPacket(status, resolution string) string {
	return `{
	  "schema_version":"context_packet.v1","context_packet_id":"cp_0000000000000001",
	  "request_id":"req_0000000000000001","generated_at":"2026-01-14T12:00:00Z",
	  "status":"` + status + `","goal":"investigate",
	  "repository":{"slug":"example-org/widget-service"},
	  "requested_scope":{"commit_sha":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
	  "resolved_scope":{"repo_id":"00000000-3065-4000-8000-000000000001","repo_slug":"example-org/widget-service","resolution":"` + resolution + `","fallback_reasons":[]},
	  "query_version":"v1","ranking_version":"v1","summary":"s",
	  "items":[{
	    "schema_version":"context_packet_item.v1","packet_item_id":"item_00000001","category":"evidence",
	    "claim_kind":"observed","title":"t","summary":"s","why_included":"w","rule_id":"rule-checkout-flake",
	    "confidence":1.0,"severity":"info","rank":1,
	    "validity_scope":{},"flags":{"stale":false,"uncertain":false,"conflicting":false,"untrusted_content":false},
	    "related_entities":[{"type":"commit","id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","label":"checkout fix"}],
	    "evidence_ref_ids":["` + testEvidenceRefID + `"]
	  }],
	  "required_checks":[],"recommended_next_steps":[],
	  "freshness":{"as_of":"2026-01-14T12:00:00Z","stale_after_seconds":0,"watermarks":[]},
	  "coverage":{"sources_considered":[],"sources_available":[],"sources_unavailable":[],"partial":false,"degraded_reasons":[]},
	  "budget":{"max_items":10,"items_used":1,"max_output_tokens":100,"estimated_tokens":10,"max_serialized_bytes":1000,"serialized_bytes":10,"truncated":false},
	  "warnings":[],
	  "compatibility":{"service_version":"1.0.0","minimum_sidecar_version":"1.0.0","supported_schema_versions":["context_packet.v1"]}
	}`
}

// contextPacketWithForeignEntity is validContextPacket plus a second item whose related
// entity belongs to example-org/other-service -- the shape task-001/002's real oracles
// forbid via forbidden_evidence, per README.md#cross-task-evidence-bleed.
func contextPacketWithForeignEntity() string {
	return `{
	  "schema_version":"context_packet.v1","context_packet_id":"cp_0000000000000001",
	  "request_id":"req_0000000000000001","generated_at":"2026-01-14T12:00:00Z",
	  "status":"complete","goal":"investigate",
	  "repository":{"slug":"example-org/widget-service"},
	  "requested_scope":{"commit_sha":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
	  "resolved_scope":{"repo_id":"00000000-3065-4000-8000-000000000001","repo_slug":"example-org/widget-service","resolution":"exact_commit","fallback_reasons":[]},
	  "query_version":"v1","ranking_version":"v1","summary":"s",
	  "items":[
	    {
	      "schema_version":"context_packet_item.v1","packet_item_id":"item_00000001","category":"evidence",
	      "claim_kind":"observed","title":"t","summary":"s","why_included":"w","rule_id":"rule-checkout-flake",
	      "confidence":1.0,"severity":"info","rank":1,
	      "validity_scope":{},"flags":{"stale":false,"uncertain":false,"conflicting":false,"untrusted_content":false},
	      "related_entities":[{"type":"commit","id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","label":"checkout fix"}],
	      "evidence_ref_ids":["` + testEvidenceRefID + `"]
	    },
	    {
	      "schema_version":"context_packet_item.v1","packet_item_id":"item_00000002","category":"cause",
	      "claim_kind":"observed","title":"leaked","summary":"s","why_included":"w","rule_id":"evidence.observed.cause.v1",
	      "confidence":1.0,"severity":"info","rank":2,
	      "validity_scope":{},"flags":{"stale":false,"uncertain":false,"conflicting":false,"untrusted_content":false},
	      "related_entities":[{"type":"commit","id":"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4","label":"other-service commit"}],
	      "evidence_ref_ids":["acr:v1:commit:c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"]
	    }
	  ],
	  "required_checks":[],"recommended_next_steps":[],
	  "freshness":{"as_of":"2026-01-14T12:00:00Z","stale_after_seconds":0,"watermarks":[]},
	  "coverage":{"sources_considered":[],"sources_available":[],"sources_unavailable":[],"partial":false,"degraded_reasons":[]},
	  "budget":{"max_items":10,"items_used":2,"max_output_tokens":100,"estimated_tokens":10,"max_serialized_bytes":1000,"serialized_bytes":10,"truncated":false},
	  "warnings":[],
	  "compatibility":{"service_version":"1.0.0","minimum_sidecar_version":"1.0.0","supported_schema_versions":["context_packet.v1"]}
	}`
}

func validExpandedEvidence() string {
	return `{
	  "schema_version":"expanded_evidence.v1",
	  "evidence":{
	    "schema_version":"evidence_ref.v1","evidence_ref_id":"` + testEvidenceRefID + `",
	    "source":{"system":"dev_health","entity_type":"commit","entity_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","display_label":"checkout fix","safe_uri":"https://git.example.invalid/widget-service/commit/a1b2"},
	    "provenance":"native","confidence":1.0,"citation":"checkout fix","observed_at":"2026-01-14T12:00:00Z",
	    "availability":"available"
	  },
	  "resolved_at":"2026-01-14T12:00:00Z","availability":"available",
	  "structured_fields":{"hash":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}
	}`
}

// validSourceEvidenceResultText is what a compliant source_evidence tool call's "output" field
// actually carries: a JSON-encoded mcp_source_evidence_response.v1 wrapper (schema_version +
// structured expanded_evidence.v1 doc + rendered_markdown), per
// contracts/examples/v1/mcp_source_evidence_response.v1.json -- not just an opaque placeholder
// string. L3/L4 now decode this for real (Codex finding 3), so a fixture that cannot be decoded
// makes every test using it fail closed rather than silently exercising nothing.
func validSourceEvidenceResultText() string {
	wrapper := `{"schema_version":"mcp_source_evidence_response.v1","structured":` + validExpandedEvidence() +
		`,"rendered_markdown":{"markdown":"evidence","untrusted":true,"truncated":false}}`
	encoded, err := json.Marshal(wrapper)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func validOpencodeEvents() string {
	return `{"type":"tool","part":{"tool":"context_for_task","state":{"input":{"goal":"investigate"},"output":"packet"}}}
{"type":"tool","part":{"tool":"source_evidence","state":{"input":{"evidence_ref_id":"` + testEvidenceRefID + `"},"output":` + validSourceEvidenceResultText() + `}}}
`
}

// renderedEvidenceMarkdown is what a source_evidence result's "output" field carries on a real
// run: the sidecar's markdown rendering, produced here by the production renderer so the
// fixture cannot drift from it. OpenCode 1.18.4 forwards only the MCP text content, never
// StructuredContent, so this -- not JSON -- is the normal shape the assertion tool must grade.
func renderedEvidenceMarkdown(t *testing.T, expandedEvidenceJSON string) string {
	t.Helper()
	var doc contractsv1.ExpandedEvidence
	if err := json.Unmarshal([]byte(expandedEvidenceJSON), &doc); err != nil {
		t.Fatalf("evidence fixture is not a valid expanded_evidence.v1 document: %v", err)
	}
	markdown, _ := sidecar.RenderEvidenceMarkdown(doc, 64*1024)
	return markdown
}

// markdownOpencodeEvents is validOpencodeEvents with the source_evidence result carried as the
// sidecar rendering rather than as JSON.
func markdownOpencodeEvents(t *testing.T, requestedRefID, markdown string) string {
	t.Helper()
	encoded, err := json.Marshal(markdown)
	if err != nil {
		t.Fatalf("could not encode rendering: %v", err)
	}
	return `{"type":"tool","part":{"tool":"context_for_task","state":{"input":{"goal":"investigate"},"output":"packet"}}}
{"type":"tool","part":{"tool":"source_evidence","state":{"input":{"evidence_ref_id":"` + requestedRefID + `"},"output":` + string(encoded) + `}}}
`
}

// expandedEvidenceJSONFor is validExpandedEvidence generalized to an arbitrary
// evidence_ref_id/entity_id pair, so Codex-finding-3 tests can construct a source_evidence
// result (or a direct-HTTP expanded-evidence/*.json capture) for an ID that is deliberately
// NOT testEvidenceRefID -- e.g. one the live packet never referenced, or one whose entity
// disagrees with another capture of the "same" evidence_ref_id.
func expandedEvidenceJSONFor(evidenceRefID, entityID string) string {
	return `{
	  "schema_version":"expanded_evidence.v1",
	  "evidence":{
	    "schema_version":"evidence_ref.v1","evidence_ref_id":"` + evidenceRefID + `",
	    "source":{"system":"dev_health","entity_type":"commit","entity_id":"` + entityID + `","display_label":"checkout fix","safe_uri":"https://git.example.invalid/widget-service/commit/a1b2"},
	    "provenance":"native","confidence":1.0,"citation":"checkout fix","observed_at":"2026-01-14T12:00:00Z",
	    "availability":"available"
	  },
	  "resolved_at":"2026-01-14T12:00:00Z","availability":"available",
	  "structured_fields":{"hash":"` + entityID + `"}
	}`
}

// sourceEvidenceResultTextFor is validSourceEvidenceResultText generalized the same way --
// what a source_evidence tool call's "output" field carries when the client requests
// evidenceRefID and the server (correctly or not, per the test) resolves it to entityID.
func sourceEvidenceResultTextFor(evidenceRefID, entityID string) string {
	wrapper := `{"schema_version":"mcp_source_evidence_response.v1","structured":` + expandedEvidenceJSONFor(evidenceRefID, entityID) +
		`,"rendered_markdown":{"markdown":"evidence","untrusted":true,"truncated":false}}`
	encoded, err := json.Marshal(wrapper)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// opencodeEventsWithSourceEvidenceCall builds an event stream with a single context_for_task
// call followed by a single source_evidence call: the client requests argEvidenceRefID and
// receives resultText back. Used by the Codex-finding-3 tests to control exactly what the
// client asked for versus what it got, independent of the driver's direct-HTTP capture.
func opencodeEventsWithSourceEvidenceCall(argEvidenceRefID, resultText string) string {
	return `{"type":"tool","part":{"tool":"context_for_task","state":{"input":{"goal":"investigate"},"output":"packet"}}}
{"type":"tool","part":{"tool":"source_evidence","state":{"input":{"evidence_ref_id":"` + argEvidenceRefID + `"},"output":` + resultText + `}}}
`
}

// contextForTaskOnlyOpencodeEvents is what a compliant degraded/empty-packet run actually
// produces: context_for_task is still called, but there is nothing to expand, so
// source_evidence is never invoked.
func contextForTaskOnlyOpencodeEvents() string {
	return `{"type":"tool","part":{"tool":"context_for_task","state":{"input":{"goal":"investigate"},"output":"packet"}}}
`
}

func validAgentResult(status, resolution string, findings string) string {
	return `{
	  "schema_version":"context_fabric_agent_result.v1","task_id":"` + testTaskID + `",
	  "packet_status":"` + status + `","scope_resolution":"` + resolution + `",
	  "findings":[` + findings + `],
	  "recommended_checks":[{"check_id":"rerun-checkout-e2e","label":"Re-run checkout e2e","reason":"flaky"}],
	  "assumptions":[]
	}`
}

// setupRunDir writes every artifact a happy-path task-001 run would have produced, so each
// test only needs to override the one or two files it wants to exercise.
func setupRunDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "capabilities.json"), validCapabilities)
	writeFile(t, filepath.Join(dir, "mcp-tools.json"), validMCPTools)
	writeFile(t, filepath.Join(dir, "service-readiness.json"), validServiceReadiness)
	writeFile(t, filepath.Join(dir, "fixture-verification.json"), validFixtureVerification)
	writeFile(t, filepath.Join(dir, "context-packet-"+testTaskID+".json"), validContextPacket("complete", "exact_commit"))
	writeFile(t, filepath.Join(dir, "expanded-evidence", testTaskID, "1.json"), validExpandedEvidence())
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"), validOpencodeEvents())
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "exact_commit", `{"claim_id":"c1","claim_kind":"observed","summary":"s","evidence_ref_ids":["`+testEvidenceRefID+`"]}`))
	return dir
}

func runAssertRunArgs(t *testing.T, dir, oraclePath string) (code int, report AssertionReport) {
	t.Helper()
	root := repoRoot(t)
	reportPath := filepath.Join(dir, "assertion-report.json")
	junitPath := filepath.Join(dir, "junit.xml")
	code = runAssertRun([]string{
		"--task", testTaskID,
		"--oracle", oraclePath,
		"--artifacts", dir,
		"--result-schema", filepath.Join(root, "testdata/fullstack/v1/schema/context_fabric_agent_result.v1.schema.json"),
		"--packet-schema-dir", filepath.Join(root, "contracts/jsonschema/v1"),
		"--junit", junitPath,
		"--report", reportPath,
	})
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("assertion-report.json was not written: %v", err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("assertion-report.json is not valid JSON: %v", err)
	}
	if _, err := os.Stat(junitPath); err != nil {
		t.Fatalf("junit.xml was not written: %v", err)
	}
	return code, report
}

// runAssertRunArgsExtra is runAssertRunArgs without a preset --task, so a caller can supply
// its own task identity flags. junit.xml is still required to be written.
func runAssertRunArgsExtra(t *testing.T, dir, oraclePath string, extra ...string) (code int, report AssertionReport) {
	t.Helper()
	root := repoRoot(t)
	reportPath := filepath.Join(dir, "assertion-report.json")
	junitPath := filepath.Join(dir, "junit.xml")
	args := append([]string{
		"--oracle", oraclePath,
		"--artifacts", dir,
		"--result-schema", filepath.Join(root, "testdata/fullstack/v1/schema/context_fabric_agent_result.v1.schema.json"),
		"--packet-schema-dir", filepath.Join(root, "contracts/jsonschema/v1"),
		"--junit", junitPath,
		"--report", reportPath,
	}, extra...)
	code = runAssertRun(args)
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("assertion-report.json was not written: %v", err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("assertion-report.json is not valid JSON: %v", err)
	}
	return code, report
}

// renameArtifact moves one captured artifact (file or directory) within a run directory.
func renameArtifact(t *testing.T, dir, from, to string) {
	t.Helper()
	if err := os.Rename(filepath.Join(dir, from), filepath.Join(dir, to)); err != nil {
		t.Fatalf("could not rename artifact %s -> %s: %v", from, to, err)
	}
}

// runAssertRunArgsWithFixtureManifest is runAssertRunArgs plus --fixture-manifest, for tests
// exercising the as_of fallback fixture-manifest.json provides when an oracle omits its own.
func runAssertRunArgsWithFixtureManifest(t *testing.T, dir, oraclePath, fixtureManifestPath string) (code int, report AssertionReport) {
	t.Helper()
	root := repoRoot(t)
	reportPath := filepath.Join(dir, "assertion-report.json")
	junitPath := filepath.Join(dir, "junit.xml")
	code = runAssertRun([]string{
		"--task", testTaskID,
		"--oracle", oraclePath,
		"--artifacts", dir,
		"--result-schema", filepath.Join(root, "testdata/fullstack/v1/schema/context_fabric_agent_result.v1.schema.json"),
		"--packet-schema-dir", filepath.Join(root, "contracts/jsonschema/v1"),
		"--fixture-manifest", fixtureManifestPath,
		"--junit", junitPath,
		"--report", reportPath,
	})
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("assertion-report.json was not written: %v", err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("assertion-report.json is not valid JSON: %v", err)
	}
	return code, report
}

func findCheck(t *testing.T, report AssertionReport, layerTag, checkName string) Check {
	t.Helper()
	for _, l := range report.Layers {
		if l.Layer != layerTag {
			continue
		}
		for _, c := range l.Checks {
			if c.Name == checkName {
				return c
			}
		}
	}
	t.Fatalf("no check %s/%s found in report: %+v", layerTag, checkName, report)
	return Check{}
}

func TestAssertRun_HappyPathPasses(t *testing.T) {
	dir := setupRunDir(t)
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "expected_packet_status":"complete","expected_scope_resolution":"exact_commit",
	  "required_evidence":[{"query_id":"git_commits.v1","entity_type":"commit","entity_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}],
	  "forbidden_evidence":[{"query_id":"git_commits.v1","entity_type":"commit","entity_id":"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"}],
	  "required_findings":[{"claim_id":"c1","claim_kind":"observed","must_cite_entity":{"entity_type":"commit","entity_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}}],
	  "forbidden_claims":[{"reason":"must not fabricate about other-service","forbidden_entity":{"entity_type":"commit","entity_id":"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"}}],
	  "required_checks":["rerun-checkout-e2e"],"min_expandable_evidence":1}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code != 0 || !report.OK {
		for _, l := range report.Layers {
			for _, c := range l.Checks {
				if !c.OK {
					t.Logf("FAIL %s/%s: expected=%q actual=%q msg=%q", l.Layer, c.Name, c.Expected, c.Actual, c.Message)
				}
			}
		}
		t.Fatalf("expected a fully passing happy-path run: code=%d ok=%v", code, report.OK)
	}
}

// TestAssertRun_AgentResultWrongTaskIDFails is Codex finding 4: previously nothing compared
// agent-result.json's own task_id against the run it was captured for, so a result carrying
// another task's identity (e.g. a mixed-up artifact, or a scripted-model bug) passed outright.
func TestAssertRun_AgentResultWrongTaskIDFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"), `{
	  "schema_version":"context_fabric_agent_result.v1","task_id":"task-002-auth-refactor-branch",
	  "packet_status":"complete","scope_resolution":"exact_commit",
	  "findings":[],"recommended_checks":[],"assumptions":[]
	}`)
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: agent-result.json carries a different task's task_id")
	}
	if findCheck(t, report, "L5", "agent_result_task_id_matches").OK {
		t.Fatal("agent_result_task_id_matches should have failed")
	}
}

// TestAssertRun_MarkdownSourceEvidenceResultIsAnExpansion is the shape every real run has.
// Grading source_evidence results by JSON-decoding the event stream's output field failed the
// whole suite with "invalid character '#'" on live runs while passing here, because the JSON
// fixtures did not mirror what OpenCode records.
func TestAssertRun_MarkdownSourceEvidenceResultIsAnExpansion(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"),
		markdownOpencodeEvents(t, testEvidenceRefID, renderedEvidenceMarkdown(t, validExpandedEvidence())))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],"min_expandable_evidence":1}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code != 0 || !report.OK {
		for _, l := range report.Layers {
			for _, c := range l.Checks {
				if !c.OK && !c.Skipped {
					t.Logf("FAIL %s/%s: expected=%q actual=%q msg=%q", l.Layer, c.Name, c.Expected, c.Actual, c.Message)
				}
			}
		}
		t.Fatalf("a markdown source_evidence result must count as a real expansion: code=%d ok=%v", code, report.OK)
	}
	if !findCheck(t, report, "L3", "source_evidence_meets_expansion_floor").OK {
		t.Fatal("the expansion floor should be met by a markdown result the client genuinely received")
	}
	if !findCheck(t, report, "L4", "client_and_direct_http_evidence_agree["+testEvidenceRefID+"]").OK {
		t.Fatal("the client's rendering and the direct-HTTP capture describe the same evidence and must agree")
	}
}

// TestAssertRun_MarkdownResultForADifferentReferenceFails is why the markdown path is still a
// round-trip proof: the rendering must name the reference the client asked for. Without that,
// any evidence rendering would count as an expansion of any reference.
func TestAssertRun_MarkdownResultForADifferentReferenceFails(t *testing.T) {
	const otherRefID = "acr:v1:commit:c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"),
		markdownOpencodeEvents(t, testEvidenceRefID,
			renderedEvidenceMarkdown(t, expandedEvidenceJSONFor(otherRefID, "c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"))))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],"min_expandable_evidence":1}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the client was handed a different evidence reference than it requested")
	}
	if findCheck(t, report, "L3", "source_evidence_result_echoes_requested_reference["+testEvidenceRefID+"]").OK {
		t.Fatal("source_evidence_result_echoes_requested_reference should have failed")
	}
	if findCheck(t, report, "L3", "source_evidence_meets_expansion_floor").OK {
		t.Fatal("a mismatched result must not count toward the expansion floor")
	}
}

// TestAssertRun_SourceEvidenceJSONMissingRenderedMarkdownFails is Codex round-2 finding 3: a
// client that forwards MCP StructuredContent as JSON was previously graded only on
// schema_version and a non-empty structured field -- two hand checks that do not enforce the
// rest of mcp_source_evidence_response.v1, in particular the required, shaped
// rendered_markdown object. A response missing it is not a valid mcp_source_evidence_response.v1
// document and must be rejected as such, not silently accepted because the two checked fields
// happened to be present.
func TestAssertRun_SourceEvidenceJSONMissingRenderedMarkdownFails(t *testing.T) {
	dir := setupRunDir(t)
	wrapper := `{"schema_version":"mcp_source_evidence_response.v1","structured":` + validExpandedEvidence() + `}`
	encoded, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"),
		opencodeEventsWithSourceEvidenceCall(testEvidenceRefID, string(encoded)))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the JSON result omits the required rendered_markdown field")
	}
	check := findCheck(t, report, "L3", "source_evidence_result_parses["+testEvidenceRefID+"]")
	if check.OK {
		t.Fatalf("source_evidence_result_parses should have failed for a response missing rendered_markdown, got %+v", check)
	}
	if !strings.Contains(check.Message, "mcp_source_evidence_response.v1") {
		t.Fatalf("failure message should name the contract that failed to validate, got %q", check.Message)
	}
}

// TestAssertRun_LogicalTaskSeparatesIdentityFromArtifactPrefix pins the fault self-test's
// artifact layout: it replays one logical task several times as "<task>-<fault>", so the
// artifacts carry a decorated prefix while the agent is still answering about the undecorated
// task. Without --logical-task every replay fails agent_result_task_id_matches for a reason
// that has nothing to do with the injected fault, which would mask the check the self-test
// exists to prove.
func TestAssertRun_LogicalTaskSeparatesIdentityFromArtifactPrefix(t *testing.T) {
	decorated := testTaskID + "-invent-evidence"
	oracleJSON := `{"schema_version":"fullstack_task_oracle.v1","task_id":"` + testTaskID + `","required_checks":["rerun-checkout-e2e"]}`

	setupDecorated := func(t *testing.T) (string, string) {
		t.Helper()
		dir := setupRunDir(t)
		for _, name := range []string{
			"context-packet-" + testTaskID + ".json",
			"opencode-events-" + testTaskID + ".jsonl",
			"agent-result-" + testTaskID + ".json",
		} {
			renameArtifact(t, dir, name, strings.Replace(name, testTaskID, decorated, 1))
		}
		renameArtifact(t, dir,
			filepath.Join("expanded-evidence", testTaskID), filepath.Join("expanded-evidence", decorated))
		oracle := filepath.Join(dir, "oracle.json")
		writeFile(t, oracle, oracleJSON)
		return dir, oracle
	}

	t.Run("without the flag identity is graded against the decorated prefix", func(t *testing.T) {
		dir, oracle := setupDecorated(t)
		_, report := runAssertRunArgsExtra(t, dir, oracle, "--task", decorated)
		if findCheck(t, report, "L5", "agent_result_task_id_matches").OK {
			t.Fatal("expected the decorated prefix to be what identity was compared against")
		}
	})

	t.Run("with the flag identity is graded against the logical task", func(t *testing.T) {
		dir, oracle := setupDecorated(t)
		_, report := runAssertRunArgsExtra(t, dir, oracle, "--task", decorated, "--logical-task", testTaskID)
		if !findCheck(t, report, "L5", "agent_result_task_id_matches").OK {
			t.Fatal("agent_result_task_id_matches should pass: the result echoes the task it was asked about")
		}
	})
}

// TestAssertRun_AgentResultPacketStatusMismatchFails is the other half of Codex finding 4:
// the agent's reported packet_status must match the live packet's actual status for a normal
// (non-degraded) task, not just inside the degraded/empty branch.
func TestAssertRun_AgentResultPacketStatusMismatchFails(t *testing.T) {
	dir := setupRunDir(t)
	// The live packet (from setupRunDir) is status "complete"/"exact_commit"; the agent claims
	// a healthier-sounding but wrong status -- exactly the inflate-status fault shape.
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("partial", "exact_commit", ""))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: agent-result.json's packet_status disagrees with the live packet")
	}
	check := findCheck(t, report, "L5", "agent_result_packet_status_matches_live_packet")
	if check.OK {
		t.Fatal("agent_result_packet_status_matches_live_packet should have failed")
	}
	if check.Expected != "complete" || check.Actual != "partial" {
		t.Fatalf("expected/actual should be normalized to the live packet vs. the agent's claim, got %+v", check)
	}
}

// TestAssertRun_AgentResultScopeResolutionMismatchFails covers the scope_resolution half.
func TestAssertRun_AgentResultScopeResolutionMismatchFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "branch_filtered", ""))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: agent-result.json's scope_resolution disagrees with the live packet")
	}
	if findCheck(t, report, "L5", "agent_result_scope_resolution_matches_live_packet").OK {
		t.Fatal("agent_result_scope_resolution_matches_live_packet should have failed")
	}
}

// TestAssertRun_InventedEvidenceIDFails is the failure mode the team lead explicitly asked
// this suite to cover: a finding that cites an evidence_ref_id no tool response ever
// returned must fail L5, even though it is otherwise well-formed.
// TestAssertRun_RequiredFindingClaimKindDowngradeFails is Codex round-2 finding 1's first
// gate: an agent that reports the oracle's required claim_id with a DIFFERENT claim_kind than
// the oracle declares (e.g. "inferred" instead of "observed") must fail on that mismatch
// directly, not merely evade the observed-only checks that a claim_kind switch bypasses
// (no_invented_evidence_ids and observed_finding_has_citation both skip non-"observed"
// findings by design).
func TestAssertRun_RequiredFindingClaimKindDowngradeFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "exact_commit", `{"claim_id":"c1","claim_kind":"inferred","summary":"s","evidence_ref_ids":[]}`))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],
	  "required_findings":[{"claim_id":"c1","claim_kind":"observed","must_cite_entity":{"entity_type":"commit","entity_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}}]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the required finding's claim_kind does not match the oracle")
	}
	kindCheck := findCheck(t, report, "L5", "required_finding_claim_kind_matches[c1]")
	if kindCheck.OK {
		t.Fatalf("required_finding_claim_kind_matches[c1] should have failed, got %+v", kindCheck)
	}
	if kindCheck.Expected != "observed" || kindCheck.Actual != "inferred" {
		t.Fatalf("required_finding_claim_kind_matches[c1] = %+v, want expected=observed actual=inferred", kindCheck)
	}
}

// TestAssertRun_RequiredFindingEmptyCitationListIsARecordedFailure is Codex round-2 finding
// 1's core vacuity bug: before this fix, a required finding with must_cite_entity and zero
// evidence_ref_ids never got a required_finding_cites_entity check added at all -- "checked"
// stayed false and the ledger simply had nothing to say about it, which reads as a pass from
// outside (findCheck could not even find the check to inspect). claim_kind is deliberately
// "inferred" here, not "observed", so observed_finding_has_citation (which only applies to
// "observed" findings) cannot be credited with catching this -- required_finding_cites_entity
// must catch it on its own.
func TestAssertRun_RequiredFindingEmptyCitationListIsARecordedFailure(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "exact_commit", `{"claim_id":"c1","claim_kind":"inferred","summary":"s","evidence_ref_ids":[]}`))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],
	  "required_findings":[{"claim_id":"c1","must_cite_entity":{"entity_type":"commit","entity_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}}]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the required finding has no citations to prove it")
	}
	// findCheck itself t.Fatal()s if the check is absent -- reaching the OK assertion below at
	// all is part of what this test proves.
	check := findCheck(t, report, "L5", "required_finding_cites_entity[c1]")
	if check.OK {
		t.Fatalf("required_finding_cites_entity[c1] should have failed, got %+v", check)
	}
	if !strings.Contains(check.Message, "zero evidence_ref_ids") {
		t.Fatalf("failure message should name the empty-citation-list cause, got %q", check.Message)
	}
}

// TestAssertRun_RequiredFindingCitesWrongEntityFails covers the third failure mode: a citation
// that resolves to a real, known entity, just not the one the oracle requires.
func TestAssertRun_RequiredFindingCitesWrongEntityFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "exact_commit", `{"claim_id":"c1","claim_kind":"observed","summary":"s","evidence_ref_ids":["`+testEvidenceRefID+`"]}`))
	oracle := filepath.Join(dir, "oracle.json")
	// testEvidenceRefID resolves to commit/a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 in the
	// default packet fixture; requiring a different commit entity makes this a genuine
	// wrong-entity mismatch rather than a missing/unresolvable citation.
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],
	  "required_findings":[{"claim_id":"c1","claim_kind":"observed","must_cite_entity":{"entity_type":"commit","entity_id":"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"}}]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the finding cites a real entity, but not the required one")
	}
	check := findCheck(t, report, "L5", "required_finding_cites_entity[c1]")
	if check.OK {
		t.Fatalf("required_finding_cites_entity[c1] should have failed, got %+v", check)
	}
	if !strings.Contains(check.Message, "do not include the required one") {
		t.Fatalf("failure message should name the wrong-entity cause, got %q", check.Message)
	}
}

func TestAssertRun_InventedEvidenceIDFails(t *testing.T) {
	dir := setupRunDir(t)
	invented := "acr:v1:commit:0000000000000000000000000000000000000000"
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "exact_commit", `{"claim_id":"c1","claim_kind":"observed","summary":"s","evidence_ref_ids":["`+invented+`"]}`))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail when a finding cites an invented evidence_ref_id")
	}
	check := findCheck(t, report, "L5", "no_invented_evidence_ids")
	if check.OK {
		t.Fatal("no_invented_evidence_ids should have failed")
	}
	if !strings.Contains(check.Actual, "c1:"+invented) {
		t.Fatalf("failure message should name the offending claim/id pair, got %q", check.Actual)
	}
}

// TestAssertRun_ForbiddenEvidenceEntityPresentFailsL2 is the other failure mode the team lead
// explicitly asked this suite to cover: an evidence entity the oracle forbids (belongs to a
// different repository) that nonetheless appears in the packet must fail L2, independent of
// whether it also gets cited by any finding.
func TestAssertRun_ForbiddenEvidenceEntityPresentFailsL2(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "context-packet-"+testTaskID+".json"), contextPacketWithForeignEntity())
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "forbidden_evidence":[{"query_id":"git_commits.v1","entity_type":"commit","entity_id":"c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4","reason":"belongs to example-org/other-service"}]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail when a forbidden entity appears in the packet")
	}
	check := findCheck(t, report, "L2", "forbidden_evidence_entities_absent")
	if check.OK {
		t.Fatal("forbidden_evidence_entities_absent should have failed")
	}
	if !strings.Contains(check.Actual, "commit/c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4") {
		t.Fatalf("failure message should name the offending entity, got %q", check.Actual)
	}
}

// TestAssertRun_DegradedOracleRequiresEmptyFindings is the other failure mode the team lead
// explicitly asked this suite to cover: for an empty/degraded oracle, silently reporting
// findings anyway is itself a failure, independent of whether those findings cite real IDs.
func TestAssertRun_DegradedOracleRequiresEmptyFindings(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "context-packet-"+testTaskID+".json"), validContextPacket("empty", "unresolved"))
	// A degraded/empty packet should have no expandable evidence; drop the expanded doc too
	// so this fixture is internally consistent (not required for the assertion to fire, but
	// keeps the test focused on exactly one failure). The directory itself must still exist:
	// capture_expanded_evidence always mkdir -p's it even when there is nothing to expand.
	if err := os.Remove(filepath.Join(dir, "expanded-evidence", testTaskID, "1.json")); err != nil {
		t.Fatal(err)
	}
	// A compliant run for a degraded/empty packet never calls source_evidence either (see
	// TestAssertRun_SourceEvidenceCalledForDegradedPacketFails for the case where it does).
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"), contextForTaskOnlyOpencodeEvents())
	// The agent fabricated a finding instead of reporting the degradation honestly.
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("empty", "unresolved", `{"claim_id":"c1","claim_kind":"recommendation","summary":"do something anyway","evidence_ref_ids":[]}`))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "expected_packet_status":"empty","expected_scope_resolution":"unresolved","findings_must_be_empty":true}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail when a degraded/empty oracle still has findings")
	}
	check := findCheck(t, report, "L5", "degraded_findings_empty")
	if check.OK {
		t.Fatal("degraded_findings_empty should have failed: the agent reported a finding for a degraded packet")
	}
	// This fixture's event stream is otherwise compliant (no source_evidence call for a
	// packet with nothing to expand), so L3's degraded-specific check must NOT also fire --
	// the test should isolate exactly the L5 failure it's about.
	if c := findCheck(t, report, "L3", "source_evidence_not_called_for_degraded_packet"); !c.OK {
		t.Fatalf("source_evidence_not_called_for_degraded_packet should have passed for this compliant event stream: %+v", c)
	}
}

// TestAssertRun_SourceEvidenceCalledForDegradedPacketFails is the L3 half of the same rule:
// an empty/degraded packet has no evidence references to expand, so any source_evidence call
// necessarily used an invented identifier and must fail, independent of L5's findings check.
func TestAssertRun_SourceEvidenceCalledForDegradedPacketFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "context-packet-"+testTaskID+".json"), validContextPacket("empty", "unresolved"))
	if err := os.Remove(filepath.Join(dir, "expanded-evidence", testTaskID, "1.json")); err != nil {
		t.Fatal(err)
	}
	// validOpencodeEvents() (the default from setupRunDir) still calls source_evidence --
	// exactly the non-compliant shape this check exists to catch.
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("empty", "unresolved", ""))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "expected_packet_status":"empty","expected_scope_resolution":"unresolved","findings_must_be_empty":true}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: source_evidence was called for a packet with nothing to expand")
	}
	check := findCheck(t, report, "L3", "source_evidence_not_called_for_degraded_packet")
	if check.OK {
		t.Fatal("source_evidence_not_called_for_degraded_packet should have failed")
	}
}

// TestAssertRun_ExpansionFloorNotMetFails is Codex finding 3's core claim: previously L3 only
// recorded whether source_evidence was observed as a boolean, so a session that never called it
// at all -- zero genuine expansions -- still passed a min_expandable_evidence:1 oracle, because
// L4/L5 graded the driver's direct-HTTP expanded-evidence/*.json capture (which exists
// regardless of what the client did) instead of what the client's own tool calls returned.
func TestAssertRun_ExpansionFloorNotMetFails(t *testing.T) {
	dir := setupRunDir(t)
	// The agent never called source_evidence -- context_for_task only -- even though the
	// packet is complete (evidence is available) and the oracle requires at least one
	// verifiable expansion.
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"), contextForTaskOnlyOpencodeEvents())
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],"min_expandable_evidence":1}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the client made zero verifiable source_evidence expansions against a min_expandable_evidence:1 oracle")
	}
	check := findCheck(t, report, "L3", "source_evidence_meets_expansion_floor")
	if check.OK {
		t.Fatal("source_evidence_meets_expansion_floor should have failed: no source_evidence calls were observed in the event stream")
	}
	if check.Actual != "0" {
		t.Fatalf("expected the reported successful-expansion count to be 0, got %q", check.Actual)
	}
}

// TestAssertRun_InventedSourceEvidenceArgumentFails is Codex finding 3's second claim: L3 must
// check what the client actually sent source_evidence, not just that it was called. A call
// whose evidence_ref_id argument the live packet never returned means the client invented (or
// mis-copied) an identifier -- exactly the kind of call layerEvidence must not treat as a
// genuine, oracle-satisfying expansion.
func TestAssertRun_InventedSourceEvidenceArgumentFails(t *testing.T) {
	dir := setupRunDir(t)
	invented := "acr:v1:commit:deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"),
		opencodeEventsWithSourceEvidenceCall(invented, sourceEvidenceResultTextFor(invented, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"],"min_expandable_evidence":1}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: source_evidence was called with an evidence_ref_id the live packet never returned")
	}
	check := findCheck(t, report, "L3", "source_evidence_argument_is_live_packet_evidence["+invented+"]")
	if check.OK {
		t.Fatal("source_evidence_argument_is_live_packet_evidence should have failed for an invented argument")
	}
	// An invented argument cannot count toward the expansion floor either -- it is not a
	// genuine, verifiable expansion of anything the packet offered.
	if floor := findCheck(t, report, "L3", "source_evidence_meets_expansion_floor"); floor.OK {
		t.Fatal("source_evidence_meets_expansion_floor should not credit a call whose argument was invented")
	}
}

// TestAssertRun_ClientAndDirectHTTPEvidenceDisagreeFails is Codex finding 3's cross-check: when
// the client's own source_evidence result and the driver's independent direct-HTTP
// expanded-evidence/*.json capture disagree about what the same evidence_ref_id resolves to,
// that disagreement itself must fail the run -- it means either the MCP server is not
// deterministic/idempotent for this ID, or one of the two capture paths is compromised, and
// silently trusting one over the other would hide a real bug.
func TestAssertRun_ClientAndDirectHTTPEvidenceDisagreeFails(t *testing.T) {
	dir := setupRunDir(t)
	// The driver's direct-HTTP capture (written by setupRunDir) resolves testEvidenceRefID to
	// entity a1b2c3d4...; the client's own source_evidence call for the SAME evidence_ref_id
	// resolves it to a different commit entirely.
	disagreeingEntityID := "9999999999999999999999999999999999999999"
	writeFile(t, filepath.Join(dir, "opencode-events-"+testTaskID+".jsonl"),
		opencodeEventsWithSourceEvidenceCall(testEvidenceRefID, sourceEvidenceResultTextFor(testEvidenceRefID, disagreeingEntityID)))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the client-observed and direct-HTTP-captured expanded evidence disagree about the same evidence_ref_id")
	}
	check := findCheck(t, report, "L4", "client_and_direct_http_evidence_agree["+testEvidenceRefID+"]")
	if check.OK {
		t.Fatal("client_and_direct_http_evidence_agree should have failed when the two captures disagree on entity identity")
	}
}

// TestAssertRun_AgentResultCitesDirectHTTPOnlyEvidenceFails is Codex finding 3's remaining
// claim about layerAgentResult specifically: before the fix, "known" (what a citation is
// allowed to resolve against) was packetKnownIDs union the driver's direct-HTTP
// expandedKnownIDs -- so a citation could be laundered as legitimate purely because the driver
// happened to capture it via a direct API call, even though neither the live packet itself nor
// the client's own source_evidence calls ever produced it. This is exactly "a session that
// expanded nothing the oracle required" still passing L5.
func TestAssertRun_AgentResultCitesDirectHTTPOnlyEvidenceFails(t *testing.T) {
	dir := setupRunDir(t)
	// An evidence_ref_id that only exists because the driver's independent direct-HTTP
	// expanded-evidence capture wrote a second file for it -- the live packet never
	// referenced it (not in any item's evidence_ref_ids), and the client's own event stream
	// never called source_evidence for it either.
	directHTTPOnly := "acr:v1:commit:1111111111111111111111111111111111111111"
	writeFile(t, filepath.Join(dir, "expanded-evidence", testTaskID, "2.json"),
		expandedEvidenceJSONFor(directHTTPOnly, "1111111111111111111111111111111111111111"))
	writeFile(t, filepath.Join(dir, "agent-result-"+testTaskID+".json"),
		validAgentResult("complete", "exact_commit", `{"claim_id":"c1","claim_kind":"observed","summary":"s","evidence_ref_ids":["`+directHTTPOnly+`"]}`))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the citation resolves only via the driver's direct-HTTP capture, which neither the live packet nor the client's own session ever produced")
	}
	check := findCheck(t, report, "L5", "no_invented_evidence_ids")
	if check.OK {
		t.Fatal("no_invented_evidence_ids should have failed: direct-HTTP-only expanded evidence must not launder an uncalled citation (Codex finding 3)")
	}
	if !strings.Contains(check.Actual, "c1:"+directHTTPOnly) {
		t.Fatalf("failure message should name the offending claim/id pair, got %q", check.Actual)
	}
}

// TestAssertRun_UnavailableSourcesBecameAvailableNamesCHAOS3068 exercises the case the team
// lead specifically asked to be handled well: when the actual unavailable-source set is a
// strict subset of what the oracle expects (a source that used to be unavailable is not
// anymore -- e.g. the CHAOS-3068 fix landing), the failure message should say so by name
// rather than reading like a generic, unexplained mismatch.
func TestAssertRun_UnavailableSourcesBecameAvailableNamesCHAOS3068(t *testing.T) {
	dir := setupRunDir(t)
	// The packet's actual coverage.sources_unavailable is empty (incidents.v1 got fixed),
	// but the oracle still expects it -- exact-set semantics must fail this until the oracle
	// is updated.
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`",
	  "expected_unavailable_sources":[{"source":"incidents.v1","reason":"source_unavailable"}],
	  "expected_unavailable_sources_exact":true}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: oracle expects an unavailable source the packet no longer reports")
	}
	check := findCheck(t, report, "L2", "unavailable_sources_exact")
	if check.OK {
		t.Fatal("unavailable_sources_exact should have failed")
	}
	if !strings.Contains(check.Message, "CHAOS-3068") || !strings.Contains(check.Message, "incidents.v1") {
		t.Fatalf("failure message should name CHAOS-3068 and the source that became available, got %q", check.Message)
	}
}

// TestAssertRun_AsOfPinMismatchFails exercises the optional as_of exact-equality check.
func TestAssertRun_AsOfPinMismatchFails(t *testing.T) {
	dir := setupRunDir(t)
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","as_of":"2026-01-14T12:00:00.000Z","min_expandable_evidence":1}`)
	code, report := runAssertRunArgs(t, dir, oracle)
	if code != 0 || !report.OK {
		for _, l := range report.Layers {
			for _, c := range l.Checks {
				if !c.OK {
					t.Logf("FAIL %s/%s: expected=%q actual=%q msg=%q", l.Layer, c.Name, c.Expected, c.Actual, c.Message)
				}
			}
		}
		t.Fatalf("expected a matching as_of pin to pass: code=%d", code)
	}

	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","as_of":"2026-02-01T00:00:00.000Z","min_expandable_evidence":1}`)
	code, report = runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected a mismatched as_of pin to fail")
	}
	if findCheck(t, report, "L2", "freshness_as_of_matches_pin").OK {
		t.Fatal("freshness_as_of_matches_pin should have failed")
	}
}

// TestAssertRun_FixtureManifestAsOfFallback exercises --fixture-manifest: an oracle that
// declares no as_of of its own still gets the check, sourced from fixture-manifest.json's
// as_of_pin.value, and an oracle-declared as_of always wins over the fixture manifest's.
func TestAssertRun_FixtureManifestAsOfFallback(t *testing.T) {
	dir := setupRunDir(t)
	fixtureManifest := filepath.Join(dir, "fixture-manifest.json")
	writeFile(t, fixtureManifest, `{"as_of_pin":{"value":"2026-01-14T12:00:00.000Z"}}`)

	// No as_of in the oracle at all: falls back to the fixture manifest and passes.
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","min_expandable_evidence":1}`)
	code, report := runAssertRunArgsWithFixtureManifest(t, dir, oracle, fixtureManifest)
	if code != 0 || !report.OK {
		for _, l := range report.Layers {
			for _, c := range l.Checks {
				if !c.OK {
					t.Logf("FAIL %s/%s: expected=%q actual=%q msg=%q", l.Layer, c.Name, c.Expected, c.Actual, c.Message)
				}
			}
		}
		t.Fatalf("expected the fixture-manifest as_of fallback to pass: code=%d", code)
	}
	if findCheck(t, report, "L2", "freshness_as_of_matches_pin").Expected != "2026-01-14T12:00:00Z" {
		t.Fatalf("expected the check to have run using the fixture manifest's pinned value, got %+v", findCheck(t, report, "L2", "freshness_as_of_matches_pin"))
	}

	// An oracle-declared as_of, even a wrong one, must win over the fixture manifest's --
	// the fallback must never shadow an explicit per-task override.
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","min_expandable_evidence":1,"as_of":"2099-01-01T00:00:00.000Z"}`)
	code, report = runAssertRunArgsWithFixtureManifest(t, dir, oracle, fixtureManifest)
	if code == 0 || report.OK {
		t.Fatal("expected the oracle's own (mismatched) as_of to take precedence over the fixture manifest's")
	}
	if findCheck(t, report, "L2", "freshness_as_of_matches_pin").Expected != "2099-01-01T00:00:00Z" {
		t.Fatalf("expected the check to use the oracle's own as_of, not the fixture manifest's, got %+v", findCheck(t, report, "L2", "freshness_as_of_matches_pin"))
	}
}

func TestAssertRun_DeniedTaskSkipsPacketLayersAndChecksErrorEnvelope(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "capabilities.json"), validCapabilities)
	writeFile(t, filepath.Join(dir, "mcp-tools.json"), validMCPTools)
	writeFile(t, filepath.Join(dir, "service-readiness.json"), validServiceReadiness)
	writeFile(t, filepath.Join(dir, "fixture-verification.json"), validFixtureVerification)
	writeFile(t, filepath.Join(dir, "negative-"+testTaskID+".json"),
		`{"schema_version":"error.v1","request_id":"req_0000000000000002","error":{"code":"repo_forbidden","message":"repository is out of credential scope","http_status":403,"retryable":false}}`)

	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","expected_http_status":403,"expected_error_code":"repo_forbidden"}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code != 0 || !report.OK {
		for _, l := range report.Layers {
			for _, c := range l.Checks {
				if !c.OK {
					t.Logf("FAIL %s/%s: expected=%q actual=%q msg=%q", l.Layer, c.Name, c.Expected, c.Actual, c.Message)
				}
			}
		}
		t.Fatalf("expected a denied task with a matching negative-*.json to pass: code=%d", code)
	}
	if findCheck(t, report, "L2", "http_denial_status").Actual != "403" {
		t.Fatal("expected the denial's HTTP status to be checked")
	}
}

// --- L6 web ---

// validWebPacket builds the shape scripts/e2e/svs-browser.mjs actually writes to
// web-packet.json: the full context_packet.v1 document the browser's own POST returned
// (packetResult.json(), written verbatim), not a hand-picked subset -- layerWeb now decodes
// the whole thing. repositorySlug/resolvedScopeRepoSlug/resolution are separated so a test can
// vary exactly one of them and isolate exactly one comparison check; every other case in this
// file wants all three to agree with the default fixture's own packet
// (validContextPacket("complete","exact_commit"), repository example-org/widget-service).
func validWebPacket(status, repositorySlug, resolvedScopeRepoSlug, resolution string) string {
	return `{
	  "context_packet_id":"pkt_browserowned00000001","status":"` + status + `",
	  "repository":{"slug":"` + repositorySlug + `"},
	  "resolved_scope":{"repo_id":"00000000-3065-4000-8000-000000000001","repo_slug":"` + resolvedScopeRepoSlug + `","resolution":"` + resolution + `","fallback_reasons":[]}
	}`
}

// matchingWebPacket is validWebPacket agreeing on every field this layer now compares, for
// every test whose point is something other than a repository/resolved-scope mismatch.
func matchingWebPacket() string {
	return validWebPacket("complete", "example-org/widget-service", "example-org/widget-service", "exact_commit")
}

func validWebEvidence(evidenceRefID, availability string) string {
	return `{"evidence":{"evidence_ref_id":"` + evidenceRefID + `"},"availability":"` + availability + `"}`
}

// TestAssertRun_WebArtifactsBothPresentPasses is the happy path L6 needs before the
// missing-sibling and mismatch checks below can be trusted: both artifacts present and
// internally consistent must still pass cleanly.
func TestAssertRun_WebArtifactsBothPresentPasses(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "web-packet-"+testTaskID+".json"), matchingWebPacket())
	writeFile(t, filepath.Join(dir, "web-evidence-"+testTaskID+".json"), validWebEvidence(testEvidenceRefID, "available"))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code != 0 || !report.OK {
		for _, l := range report.Layers {
			for _, c := range l.Checks {
				if !c.OK {
					t.Logf("FAIL %s/%s: expected=%q actual=%q msg=%q", l.Layer, c.Name, c.Expected, c.Actual, c.Message)
				}
			}
		}
		t.Fatalf("expected both web artifacts present and consistent to pass: code=%d ok=%v", code, report.OK)
	}
	if !findCheck(t, report, "L6", "web_artifacts_both_present").OK {
		t.Fatal("web_artifacts_both_present should have passed with both artifacts present")
	}
	if !findCheck(t, report, "L6", "web_packet_repository_matches_api").OK {
		t.Fatal("web_packet_repository_matches_api should have passed")
	}
	if !findCheck(t, report, "L6", "web_packet_resolved_scope_matches_api").OK {
		t.Fatal("web_packet_resolved_scope_matches_api should have passed")
	}
}

// TestAssertRun_WebPacketIDCheckIsExplicitlySkipped is the redesign Codex's round-2 followup
// asked for: web_packet_id_matches_api could never pass on any run --
// internal/contextpacket/assembler.go's packetID() hashes the server-generated request_id
// (internal/api/read_routes.go), so the driver's direct-HTTP capture and the browser's own
// independent POST always produce different context_packet_ids, by construction. A check that
// can never pass is worse than none (it silently hid L6's real status), so it must now read as
// an explicit SKIPPED, never as a passed check and never as an absent one.
func TestAssertRun_WebPacketIDCheckIsExplicitlySkipped(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "web-packet-"+testTaskID+".json"), matchingWebPacket())
	writeFile(t, filepath.Join(dir, "web-evidence-"+testTaskID+".json"), validWebEvidence(testEvidenceRefID, "available"))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	_, report := runAssertRunArgs(t, dir, oracle)
	check := findCheck(t, report, "L6", "web_packet_id_not_comparable")
	if !check.Skipped {
		t.Fatalf("web_packet_id_not_comparable should be recorded as Skipped, got %+v", check)
	}
	if !check.OK {
		t.Fatalf("a skipped check must not read as a failure either, got %+v", check)
	}
}

// TestAssertRun_WebPacketWrongRepositoryFails is one half of the "must be able to FAIL"
// requirement: a web packet naming a different repository than the API's own packet must fail
// L6, isolated to exactly the repository check (resolved_scope.repo_slug still agrees).
func TestAssertRun_WebPacketWrongRepositoryFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "web-packet-"+testTaskID+".json"),
		validWebPacket("complete", "example-org/other-service", "example-org/widget-service", "exact_commit"))
	writeFile(t, filepath.Join(dir, "web-evidence-"+testTaskID+".json"), validWebEvidence(testEvidenceRefID, "available"))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the web packet names a different repository than the API's own packet")
	}
	check := findCheck(t, report, "L6", "web_packet_repository_matches_api")
	if check.OK {
		t.Fatalf("web_packet_repository_matches_api should have failed, got %+v", check)
	}
	if !findCheck(t, report, "L6", "web_packet_resolved_scope_matches_api").OK {
		t.Fatal("resolved_scope still agrees in this fixture, so that check should not also have failed")
	}
}

// TestAssertRun_WebPacketWrongResolvedScopeFails is the other half: a web packet whose
// resolved_scope disagrees with the API's own packet (same repository) must fail L6, isolated
// to exactly the resolved-scope check.
func TestAssertRun_WebPacketWrongResolvedScopeFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "web-packet-"+testTaskID+".json"),
		validWebPacket("complete", "example-org/widget-service", "example-org/widget-service", "branch_filtered"))
	writeFile(t, filepath.Join(dir, "web-evidence-"+testTaskID+".json"), validWebEvidence(testEvidenceRefID, "available"))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: the web packet's resolved_scope disagrees with the API's own packet")
	}
	check := findCheck(t, report, "L6", "web_packet_resolved_scope_matches_api")
	if check.OK {
		t.Fatalf("web_packet_resolved_scope_matches_api should have failed, got %+v", check)
	}
	if !findCheck(t, report, "L6", "web_packet_repository_matches_api").OK {
		t.Fatal("repository still agrees in this fixture, so that check should not also have failed")
	}
}

// TestAssertRun_WebPacketPresentWithoutWebEvidenceFails is Codex round-2 finding 4: the web
// capture always emits web-packet.json and web-evidence.json together for a task that runs
// the web check at all, so one present and the other missing means the capture itself
// partially failed. layerWeb previously said nothing about the missing sibling and simply
// graded whichever artifact existed.
func TestAssertRun_WebPacketPresentWithoutWebEvidenceFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "web-packet-"+testTaskID+".json"), matchingWebPacket())
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: web-evidence.json is missing while web-packet.json is present")
	}
	check := findCheck(t, report, "L6", "web_artifacts_both_present")
	if check.OK {
		t.Fatalf("web_artifacts_both_present should have failed, got %+v", check)
	}
}

// TestAssertRun_WebEvidencePresentWithoutWebPacketFails is the mirror image: web-evidence.json
// present without web-packet.json must fail the same way, not just the packet-missing
// direction.
func TestAssertRun_WebEvidencePresentWithoutWebPacketFails(t *testing.T) {
	dir := setupRunDir(t)
	writeFile(t, filepath.Join(dir, "web-evidence-"+testTaskID+".json"), validWebEvidence(testEvidenceRefID, "available"))
	oracle := filepath.Join(dir, "oracle.json")
	writeFile(t, oracle, `{"schema_version":"fullstack_task_oracle.v1","task_id":"`+testTaskID+`","required_checks":["rerun-checkout-e2e"]}`)

	code, report := runAssertRunArgs(t, dir, oracle)
	if code == 0 || report.OK {
		t.Fatal("expected the run to fail: web-packet.json is missing while web-evidence.json is present")
	}
	check := findCheck(t, report, "L6", "web_artifacts_both_present")
	if check.OK {
		t.Fatalf("web_artifacts_both_present should have failed, got %+v", check)
	}
}
