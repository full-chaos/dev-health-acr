package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// harnessFinding and harnessAgentResult mirror
// testdata/fullstack/v1/schema/context_fabric_agent_result.v1.schema.json -- the
// harness-owned schema for the agent's strict-JSON final message (docs/fullstack-acceptance.md
// section 3). It intentionally does not reuse a contracts/v1 type: ACR itself never produces
// or consumes this shape.
type harnessFinding struct {
	ClaimID        string   `json:"claim_id"`
	ClaimKind      string   `json:"claim_kind"`
	Summary        string   `json:"summary"`
	EvidenceRefIDs []string `json:"evidence_ref_ids"`
}

type harnessCheck struct {
	CheckID string `json:"check_id"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
}

type harnessAgentResult struct {
	SchemaVersion     string           `json:"schema_version"`
	TaskID            string           `json:"task_id"`
	PacketStatus      string           `json:"packet_status"`
	ScopeResolution   string           `json:"scope_resolution"`
	Findings          []harnessFinding `json:"findings"`
	RecommendedChecks []harnessCheck   `json:"recommended_checks"`
	Assumptions       []string         `json:"assumptions"`
}

// runContext carries everything the layer functions need, computed once so L4/L5 can
// cross-reference L2's evidence-ID universe without re-parsing artifacts.
type runContext struct {
	artifactsDir string
	taskID       string
	// logicalTaskID is the task_id the agent was actually asked about, which is what its
	// result must echo. It differs from taskID only when artifacts are filed under a decorated
	// prefix -- the fault self-test replays one logical task several times as
	// "<task>-<fault>" -- so grading identity against taskID would fail every replay for a
	// reason unrelated to the fault under test. Defaults to taskID.
	logicalTaskID string
	oracle        Oracle

	packetSchemas *schemaLoader
	resultSchemas *schemaLoader
	resultSchema  string

	packet         *contractsv1.ContextPacket
	packetRaw      []byte
	packetKnownIDs stringSet // union of every evidence_ref_id referenced by packet items

	// entityPresent/entityByID are derived from packet.Items[].RelatedEntities
	// (internal/contextpacket/ranking.go builds these 1:1 from EvidenceRef.Source, so they
	// carry the same plaintext entity_type/entity_id as the ClickHouse-side locator, unlike
	// the opaque wire evidence_ref_id -- see README.md#evidence-ref-id-matching).
	// entityPresent holds every "entity_type/entity_id" key reachable in the packet;
	// entityByID maps an item's sole evidence_ref_id to that same key when the item has
	// exactly one of each (the shape ranking.go always produces for evidence-sourced items).
	entityPresent stringSet
	entityByID    map[string]string

	// clientEvidenceCalls/clientExpandedDocs/clientExpandedKnownIDs/clientEntityByID are what
	// the OpenCode session itself actually did and received, parsed from the event stream by
	// L3 (see clientevidence.go) -- the primary source of truth for L4/L5's evidence-agreement
	// checks (Codex finding 3: grading the driver's direct-HTTP expanded-evidence/*.json
	// captures instead let a session that expanded nothing the oracle required still pass,
	// since capture_expanded_evidence expands every packet evidence ref via a direct API call
	// regardless of what the client itself ever requested via source_evidence).
	clientEvidenceCalls    []clientEvidenceCall
	clientExpandedDocs     []contractsv1.ExpandedEvidence
	clientExpandedKnownIDs stringSet
	clientEntityByID       map[string]string

	// expandedDocs/expandedRaw/expandedKnownIDs are the driver's direct-HTTP
	// expanded-evidence/*.json captures (capture_expanded_evidence in
	// scripts/e2e/fullstack-opencode.sh, which expands every evidence_ref_id the packet
	// references regardless of what the client session called). L4 keeps validating and
	// schema-checking these -- they still prove the API's direct read path itself works -- but
	// they are now an independent cross-check against clientExpandedDocs, never the primary
	// grounding for "did the agent's citation resolve to something real" (see resolveEntity
	// and layerAgentResult's "known" set).
	expandedDocs     []contractsv1.ExpandedEvidence
	expandedRaw      [][]byte
	expandedKnownIDs stringSet

	// nonEvidenceToolIO is every OTHER tool invocation's arguments+result, concatenated --
	// i.e. every tool call in the session that is not context_for_task/source_evidence. The
	// legitimate MCP evidence round trip necessarily echoes each evidence_ref_id's safe_uri
	// back into the event stream (it is part of the structured document the server returns),
	// so checkNoOutboundFetch must not treat that echo itself as a fetch attempt; it scans
	// this narrower haystack instead. Populated by L3; nil for denied tasks (no session ran).
	nonEvidenceToolIO []byte
}

// resolveEntity returns the "entity_type/entity_id" key a cited evidence_ref_id resolves to.
// It prefers, in order: the packet-derived mapping (always available once L2 has loaded a
// packet); the client-observed mapping (what the OpenCode session's own source_evidence calls
// actually returned, per Codex finding 3); and only then the driver's direct-HTTP capture, as
// a last resort so a citation is not penalized purely because this tool's own artifact
// resolution order did not happen to find it sooner.
func (rc *runContext) resolveEntity(evidenceRefID string) (string, bool) {
	if key, ok := rc.entityByID[evidenceRefID]; ok {
		return key, true
	}
	if key, ok := rc.clientEntityByID[evidenceRefID]; ok {
		return key, true
	}
	for _, doc := range rc.expandedDocs {
		if doc.Evidence.EvidenceRefID == evidenceRefID {
			return entityKey(doc.Evidence.Source.EntityType, doc.Evidence.Source.EntityID), true
		}
	}
	return "", false
}

func runAssertRun(args []string) int {
	fs := flag.NewFlagSet("assert-run", flag.ContinueOnError)
	task := fs.String("task", "", "task_id, e.g. task-001-checkout-flake-exact-commit")
	logicalTask := fs.String("logical-task", "", "task_id the agent was asked about, when artifacts are filed under a decorated prefix; defaults to --task")
	oraclePath := fs.String("oracle", "", "path to testdata/fullstack/v1/expected/task-*.oracle.json")
	artifacts := fs.String("artifacts", "", "run artifacts directory (.tmp/fullstack/<run-id>)")
	resultSchema := fs.String("result-schema", "", "path to context_fabric_agent_result.v1.schema.json")
	packetSchemaDir := fs.String("packet-schema-dir", "", "path to contracts/jsonschema/v1")
	junitOut := fs.String("junit", "", "path to write junit.xml")
	reportOut := fs.String("report", "", "path to write assertion-report.json")
	fixtureManifestPath := fs.String("fixture-manifest", "", "optional path to testdata/fullstack/v1/fixture-manifest.json, used as a fallback source for the as_of pin when the oracle does not declare its own")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	required := map[string]string{
		"--task": *task, "--oracle": *oraclePath, "--artifacts": *artifacts,
		"--result-schema": *resultSchema, "--packet-schema-dir": *packetSchemaDir,
		"--junit": *junitOut, "--report": *reportOut,
	}
	for name, value := range required {
		if value == "" {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL assert-run: %s is required\n", name)
			return 2
		}
	}

	oracle, err := loadOracle(*oraclePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL assert-run oracle: %s\n", redact(err.Error()))
		return 1
	}
	// The oracle's own as_of always wins when set; fixture-manifest.json's as_of_pin.value is
	// only a fallback, so a per-task oracle override is never silently shadowed.
	if oracle.ExpectedAsOf == nil && *fixtureManifestPath != "" {
		asOf, err := loadFixtureManifestAsOf(*fixtureManifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[assertrun] FAIL assert-run fixture-manifest: %s\n", redact(err.Error()))
			return 1
		}
		if asOf != "" {
			oracle.ExpectedAsOf = &asOf
		}
	}

	if *logicalTask == "" {
		*logicalTask = *task
	}

	rc := &runContext{
		artifactsDir:  *artifacts,
		taskID:        *task,
		logicalTaskID: *logicalTask,
		oracle:        oracle,
		packetSchemas: newSchemaLoader(*packetSchemaDir),
		resultSchemas: newSchemaLoader(filepath.Dir(*resultSchema)),
		resultSchema:  filepath.Base(*resultSchema),
	}

	layers := []*Layer{
		layerInfrastructure(rc),
		layerACRAPI(rc),
		layerMCP(rc),
		layerEvidence(rc),
		layerAgentResult(rc),
	}
	if l := layerWeb(rc); l != nil {
		layers = append(layers, l)
	}

	report := buildReport(runID(*artifacts), *task, layers)

	if err := writeJSONReport(*reportOut, report); err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL assert-run: %s\n", redact(err.Error()))
		return 1
	}
	if err := writeJUnit(*junitOut, report); err != nil {
		fmt.Fprintf(os.Stderr, "[assertrun] FAIL assert-run: %s\n", redact(err.Error()))
		return 1
	}

	if !report.OK {
		for _, layer := range report.Layers {
			if layer.OK {
				continue
			}
			for _, check := range layer.Checks {
				if check.OK {
					continue
				}
				fmt.Fprintf(os.Stderr, "[assertrun] FAIL %s %s/%s: expected=%q actual=%q %s\n",
					layer.Layer, layer.Name, check.Name, check.Expected, check.Actual, check.Message)
			}
		}
		return 1
	}
	return 0
}

// loadFixtureManifestAsOf reads fixture-manifest.json's as_of_pin.value (see README.md "The
// as_of pin"), the fallback source for the as_of exact-equality check when an oracle does not
// declare its own as_of. An empty result (field absent) is not an error -- the caller simply
// has nothing to fall back to.
func loadFixtureManifestAsOf(path string) (string, error) {
	var manifest struct {
		AsOfPin struct {
			Value string `json:"value"`
		} `json:"as_of_pin"`
	}
	if _, err := readJSONFile(path, &manifest); err != nil {
		return "", fmt.Errorf("read fixture manifest %s: %w", path, err)
	}
	return manifest.AsOfPin.Value, nil
}

func runID(artifactsDir string) string {
	if path, ok := findArtifact(artifactsDir, "", "run", "json"); ok {
		var manifest struct {
			RunID string `json:"run_id"`
		}
		if _, err := readJSONFile(path, &manifest); err == nil && manifest.RunID != "" {
			return manifest.RunID
		}
	}
	return filepath.Base(artifactsDir)
}
