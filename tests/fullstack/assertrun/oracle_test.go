package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// task001UnavailableSources pins the exact, current-truth set of catalog sources task-001's
// oracle expects to be unavailable. Two confirmed, filed, NOT-fixed-inside-CHAOS-3065 product
// bugs are why this is non-empty:
//
//   - CHAOS-3068: internal/contextpacket/source_queries.go's incidents.v1 still queries the
//     `incidents` table, which ops migration 068_drop_legacy_incidents.sql drops in favor of
//     operational_incidents (a different column shape, not a drop-in rename). incidents.v1
//     therefore always fails with source_unavailable, for every task, regardless of seeding.
//   - CHAOS-3069: internal/contextpacket/source_executor.go's scanEvidenceRow scans
//     `confidence` into a *float64, but clickhouse-go v2.47's Float32.ScanRow rejects any
//     destination that is not *float32/**float32/sql.Scanner. work_graph.v1,
//     ai_workflow_artifacts.v1, ai_review_outcomes.v1, and deployment_incident_provenance.v1
//     are the catalog entries that project a bare Float32 confidence column, so all four fail
//     on every row, for every organization -- confirmed against the live acr-api log (five
//     evidence queries with "outcome":"failure") and the clickhouse-go driver source.
//
// When either bug is fixed, its source(s) drop out of task-001's oracle and this constant
// must be updated to match -- see docs/fullstack-acceptance.md and
// testdata/fullstack/v1/README.md's "Packet status" section for the full reasoning. A test
// this loose (e.g. just "non-empty") would let a real regression -- the set silently growing
// again -- through unnoticed, which defeats the point of the tripwire.
var task001UnavailableSources = []string{
	"incidents.v1",                      // CHAOS-3068
	"work_graph.v1",                     // CHAOS-3069
	"ai_workflow_artifacts.v1",          // CHAOS-3069
	"ai_review_outcomes.v1",             // CHAOS-3069
	"deployment_incident_provenance.v1", // CHAOS-3069
}

// TestLoadOracle_RealFixtureFiles decodes the actual testdata/fullstack/v1/expected/task-*
// oracle files landed by the fixture owner. It exists to catch silent decode drift: if the
// oracle's JSON shape ever changes field names, json.Unmarshal would otherwise leave the
// corresponding Oracle fields silently zero-valued instead of failing loudly.
func TestLoadOracle_RealFixtureFiles(t *testing.T) {
	root := repoRoot(t)
	expectedDir := filepath.Join(root, "testdata/fullstack/v1/expected")

	cases := []struct {
		file                string
		taskID              string
		wantPacketStatus    string // "" means expected_packet_status must be nil (denied task)
		wantScopeResolution string
		wantMinRequiredEvi  int // len(RequiredEvidence) or len(ForbiddenEvidence), whichever is checked below
		wantFindingsEmpty   bool
		wantHTTPStatus      int // 0 means not a denial
		wantHTTPCode        string
	}{
		{
			file: "task-001.oracle.json", taskID: "task-001-checkout-flake-exact-commit",
			wantPacketStatus: "partial", wantScopeResolution: "exact_commit",
		},
		{
			file: "task-002.oracle.json", taskID: "task-002-auth-refactor-branch",
			wantPacketStatus: "partial", wantScopeResolution: "branch_filtered",
		},
		{
			file: "task-003.oracle.json", taskID: "task-003-unindexed-branch-empty",
			wantPacketStatus: "degraded", wantScopeResolution: "branch_filtered",
			wantFindingsEmpty: true,
		},
		{
			file: "task-004.oracle.json", taskID: "task-004-foreign-repo-denied",
			wantFindingsEmpty: true, wantHTTPStatus: 403, wantHTTPCode: "repo_forbidden",
		},
		{
			file: "task-005.oracle.json", taskID: "task-005-unavailable-evidence",
			wantHTTPStatus: 404, wantHTTPCode: "not_found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			oracle, err := loadOracle(filepath.Join(expectedDir, tc.file))
			if err != nil {
				t.Fatalf("loadOracle: %v", err)
			}
			if oracle.SchemaVersion != "fullstack_task_oracle.v1" {
				t.Fatalf("schema_version = %q", oracle.SchemaVersion)
			}
			if oracle.TaskID != tc.taskID {
				t.Fatalf("task_id = %q, want %q", oracle.TaskID, tc.taskID)
			}

			if tc.wantPacketStatus == "" {
				if oracle.ExpectedPacketStatus != nil {
					t.Fatalf("expected_packet_status = %q, want null", *oracle.ExpectedPacketStatus)
				}
			} else {
				if oracle.ExpectedPacketStatus == nil || *oracle.ExpectedPacketStatus != tc.wantPacketStatus {
					t.Fatalf("expected_packet_status = %v, want %q", oracle.ExpectedPacketStatus, tc.wantPacketStatus)
				}
			}
			if tc.wantScopeResolution != "" {
				if oracle.ExpectedScopeResolution == nil || *oracle.ExpectedScopeResolution != tc.wantScopeResolution {
					t.Fatalf("expected_scope_resolution = %v, want %q", oracle.ExpectedScopeResolution, tc.wantScopeResolution)
				}
			}
			if oracle.FindingsMustBeEmpty != tc.wantFindingsEmpty {
				t.Fatalf("findings_must_be_empty = %v, want %v", oracle.FindingsMustBeEmpty, tc.wantFindingsEmpty)
			}

			status, code, ok := oracle.httpExpectation()
			if tc.wantHTTPStatus == 0 {
				if ok {
					t.Fatalf("httpExpectation() = (%d, %q, %v), want ok=false for a non-denial task", status, code, ok)
				}
			} else {
				if !ok || status != tc.wantHTTPStatus || code != tc.wantHTTPCode {
					t.Fatalf("httpExpectation() = (%d, %q, %v), want (%d, %q, true)", status, code, ok, tc.wantHTTPStatus, tc.wantHTTPCode)
				}
			}
		})
	}

	// task-001/002 carry non-wildcard required/forbidden evidence and forbidden_claims that
	// this tool matches by entity, and required_findings with must_cite_entity -- exercise
	// the decode of those nested shapes explicitly since they are the ones most likely to
	// drift silently.
	task001, err := loadOracle(filepath.Join(expectedDir, "task-001.oracle.json"))
	if err != nil {
		t.Fatalf("loadOracle task-001: %v", err)
	}
	if len(task001.RequiredEvidence) == 0 {
		t.Fatal("task-001 required_evidence decoded empty")
	}
	for _, e := range task001.RequiredEvidence {
		if e.EntityType == "" || e.EntityID == "" {
			t.Fatalf("task-001 required_evidence entry missing entity_type/entity_id: %+v", e)
		}
	}
	if len(task001.ForbiddenEvidence) == 0 {
		t.Fatal("task-001 forbidden_evidence decoded empty")
	}
	if len(task001.RequiredFindings) == 0 {
		t.Fatal("task-001 required_findings decoded empty")
	}
	for _, f := range task001.RequiredFindings {
		if f.ClaimID == "" {
			t.Fatal("task-001 required_findings entry missing claim_id")
		}
		if f.MustCiteEntity == nil || f.MustCiteEntity.EntityType == "" || f.MustCiteEntity.EntityID == "" {
			t.Fatalf("task-001 required_findings[%s] missing must_cite_entity", f.ClaimID)
		}
	}
	if len(task001.ForbiddenClaims) == 0 {
		t.Fatal("task-001 forbidden_claims decoded empty")
	}
	for _, c := range task001.ForbiddenClaims {
		if c.isWildcard() {
			t.Fatal("task-001's forbidden_claims entry names a real entity and should not decode as wildcard")
		}
	}
	var gotSources []string
	for _, u := range task001.ExpectedUnavailableSources {
		if u.Source == "" || u.Reason == "" {
			t.Fatalf("task-001 expected_unavailable_sources entry missing source/reason: %+v", u)
		}
		gotSources = append(gotSources, u.Source)
	}
	sort.Strings(gotSources)
	wantSources := append([]string(nil), task001UnavailableSources...)
	sort.Strings(wantSources)
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("task-001 expected_unavailable_sources = %v, want exactly %v (CHAOS-3068 + CHAOS-3069; update task001UnavailableSources when either is fixed)", gotSources, wantSources)
	}
	if !task001.ExpectedUnavailableSourcesExact {
		t.Fatal("task-001 expected_unavailable_sources_exact should be true")
	}
	if task001.MinExpandableEvidence != 4 {
		t.Fatalf("task-001 min_expandable_evidence = %d, want 4", task001.MinExpandableEvidence)
	}
	if len(task001.RequiredChecks) == 0 {
		t.Fatal("task-001 required_checks decoded empty")
	}

	task003, err := loadOracle(filepath.Join(expectedDir, "task-003.oracle.json"))
	if err != nil {
		t.Fatalf("loadOracle task-003: %v", err)
	}
	if len(task003.ForbiddenClaims) != 1 || !task003.ForbiddenClaims[0].isWildcard() {
		t.Fatalf("task-003's forbidden_claims entry should decode as a wildcard: %+v", task003.ForbiddenClaims)
	}
}

// TestAllShippedOraclesDecode is a permanent guard, requested by the team lead, against drift
// between the fixture agent's oracle files and this package's Go structs -- our
// highest-risk seam, since a silently-mismatched field name means json.Unmarshal leaves an
// Oracle field zero-valued instead of failing loudly. Beyond decoding, it checks the semantic
// invariants every shipped oracle must hold: a task_id matching its filename stem, every
// required_evidence/forbidden_evidence entry naming both entity_type and entity_id, and every
// task in tasks.json pointing at an oracle file that exists and self-identifies consistently.
func TestAllShippedOraclesDecode(t *testing.T) {
	root := repoRoot(t)
	fixtureDir := filepath.Join(root, "testdata/fullstack/v1")

	paths, err := filepath.Glob(filepath.Join(fixtureDir, "expected", "*.oracle.json"))
	if err != nil {
		t.Fatalf("glob oracle files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no oracle files found under testdata/fullstack/v1/expected")
	}

	for _, p := range paths {
		base := filepath.Base(p)
		stem := strings.TrimSuffix(base, ".oracle.json")

		oracle, err := loadOracle(p)
		if err != nil {
			t.Errorf("%s: %v", base, err)
			continue
		}

		// Filenames are the short form (task-001.oracle.json) but task_id is the full
		// descriptive form (task-001-checkout-flake-exact-commit); a prefix match is the
		// real invariant, not equality.
		if oracle.TaskID == "" {
			t.Errorf("%s: task_id is empty", base)
		} else if !strings.HasPrefix(oracle.TaskID, stem) {
			t.Errorf("%s: task_id %q does not start with its filename stem %q", base, oracle.TaskID, stem)
		}
		for _, e := range oracle.RequiredEvidence {
			if e.EntityType == "" || e.EntityID == "" {
				t.Errorf("%s: required_evidence entry missing entity_type/entity_id: %+v", base, e)
			}
		}
		for _, e := range oracle.ForbiddenEvidence {
			if e.EntityType == "" || e.EntityID == "" {
				t.Errorf("%s: forbidden_evidence entry missing entity_type/entity_id: %+v", base, e)
			}
		}
	}

	var tasks struct {
		Tasks []struct {
			TaskID string `json:"task_id"`
			Oracle string `json:"oracle"`
		} `json:"tasks"`
	}
	if _, err := readJSONFile(filepath.Join(fixtureDir, "tasks.json"), &tasks); err != nil {
		t.Fatalf("read tasks.json: %v", err)
	}
	if len(tasks.Tasks) == 0 {
		t.Fatal("tasks.json declares no tasks")
	}
	for _, task := range tasks.Tasks {
		oraclePath := filepath.Join(fixtureDir, task.Oracle)
		if _, err := os.Stat(oraclePath); err != nil {
			t.Errorf("task %s: oracle file %s does not exist: %v", task.TaskID, task.Oracle, err)
			continue
		}
		oracle, err := loadOracle(oraclePath)
		if err != nil {
			t.Errorf("task %s: %v", task.TaskID, err)
			continue
		}
		if oracle.TaskID != task.TaskID {
			t.Errorf("tasks.json task %s points at oracle %s, whose task_id is %q", task.TaskID, task.Oracle, oracle.TaskID)
		}
	}
}

// TestLoadOracle_RejectsVacuousOracles is Codex finding 6: loadOracle previously accepted
// `{}` outright -- no schema_version, no task_id, no assertions -- which would make
// assert-run pass any task vacuously. Each case here is a distinct way an oracle file could
// be technically valid JSON while asserting nothing.
func TestLoadOracle_RejectsVacuousOracles(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"completely empty object", `{}`},
		{"wrong schema_version", `{"schema_version":"something_else.v1","task_id":"t","expected_packet_status":"complete"}`},
		{"missing task_id", `{"schema_version":"fullstack_task_oracle.v1","expected_packet_status":"complete"}`},
		{"empty task_id", `{"schema_version":"fullstack_task_oracle.v1","task_id":"","expected_packet_status":"complete"}`},
		{"well-formed identity, zero assertions", `{"schema_version":"fullstack_task_oracle.v1","task_id":"task-001-checkout-flake-exact-commit"}`},
		{"only empty arrays (no real assertion)", `{"schema_version":"fullstack_task_oracle.v1","task_id":"t","required_evidence":[],"forbidden_evidence":[],"required_checks":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "oracle.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOracle(path); err == nil {
				t.Fatalf("loadOracle(%s) should have been rejected", tc.json)
			}
		})
	}
}

// TestLoadOracle_AcceptsAMinimalRealAssertion is the positive counterpart: an oracle with
// correct identity plus exactly one real assertion must still load.
func TestLoadOracle_AcceptsAMinimalRealAssertion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oracle.json")
	content := `{"schema_version":"fullstack_task_oracle.v1","task_id":"t","expected_packet_status":"complete"}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	oracle, err := loadOracle(path)
	if err != nil {
		t.Fatalf("loadOracle: %v", err)
	}
	if oracle.ExpectedPacketStatus == nil || *oracle.ExpectedPacketStatus != "complete" {
		t.Fatalf("expected_packet_status did not decode: %+v", oracle)
	}
}
