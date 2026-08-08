package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func joinIDs(ids []string) string { return strings.Join(ids, ",") }

// ifNotEmpty returns message when ids is non-empty, else "". Used so a passing check's
// Message field stays empty instead of restating a positive result.
func ifNotEmpty(ids []string, message string) string {
	if len(ids) == 0 {
		return ""
	}
	return message
}

// loadArtifact resolves and decodes one artifact JSON file via findArtifact. It returns
// found=false, err=nil when the artifact simply does not exist (a normal, checkable
// condition), and err!=nil only for a read/decode failure of a file that does exist.
func loadArtifact(rc *runContext, base, ext string, out any) (raw []byte, found bool, err error) {
	path, ok := findArtifact(rc.artifactsDir, rc.taskID, base, ext)
	if !ok {
		return nil, false, nil
	}
	raw, err = readJSONFile(path, out)
	return raw, true, err
}

// isDeniedTask reports whether this run is one of scripts/e2e/fullstack-opencode.sh's
// run_foreign_repo_task/run_unavailable_evidence_task negative tests (task-004/task-005 in
// the current fixture). Those never call run_client_task at all -- no context request, no
// OpenCode session, no MCP tool call, no agent result -- they only produce
// negative-<task_id>.json (a raw ErrorEnvelope captured straight off the HTTP response and
// validated by scripts/e2e/validate-error-receipt.sh). Detecting this from the artifact that
// actually exists, rather than from a field the oracle author must remember to set, is
// self-describing and cannot drift out of sync with the orchestrator.
func isDeniedTask(rc *runContext) bool {
	_, ok := findArtifact(rc.artifactsDir, rc.taskID, "negative", "json")
	return ok
}

// --- L1 infrastructure ---

func layerInfrastructure(rc *runContext) *Layer {
	l := newLayer("L1", "infrastructure")

	var readiness struct {
		Services []struct {
			Service string `json:"service"`
			State   string `json:"state"`
			Health  string `json:"health"`
		} `json:"services"`
	}
	_, found, err := loadArtifact(rc, "service-readiness", "json", &readiness)
	switch {
	case err != nil:
		l.add("service_readiness", false, "valid service-readiness.json", "", err.Error())
	case !found:
		l.add("service_readiness", false, "service-readiness.json present", "missing", "artifact not found")
	default:
		var notRunning []string
		for _, s := range readiness.Services {
			if s.State != "running" {
				notRunning = append(notRunning, s.Service+"="+s.State)
			}
		}
		l.add("service_readiness", len(notRunning) == 0, "all services running", joinIDs(notRunning), "")
	}

	var fv struct {
		OK bool `json:"ok"`
	}
	_, found, err = loadArtifact(rc, "fixture-verification", "json", &fv)
	switch {
	case err != nil:
		l.add("fixture_verification", false, "valid fixture-verification.json", "", err.Error())
	case !found:
		l.add("fixture_verification", false, "fixture-verification.json present", "missing", "artifact not found")
	default:
		l.add("fixture_verification", fv.OK, "ok=true", fmt.Sprintf("ok=%v", fv.OK), "")
	}

	return l
}

// --- L2 acr_api ---

func layerACRAPI(rc *runContext) *Layer {
	l := newLayer("L2", "acr_api")

	// capabilities.json is captured once per run (capture_capabilities), independent of task,
	// so this check applies regardless of whether this task is a denial.
	var caps contractsv1.Capabilities
	rawCaps, found, err := loadArtifact(rc, "capabilities", "json", &caps)
	switch {
	case err != nil:
		l.add("capabilities_decode", false, "valid capabilities.json", "", err.Error())
	case !found:
		l.add("capabilities_present", false, "capabilities.json present", "missing", "artifact not found")
	default:
		if err := rc.packetSchemas.validateJSON("capabilities.v1.schema.json", rawCaps); err != nil {
			l.add("capabilities_schema", false, "valid per capabilities.v1", "invalid", err.Error())
		} else {
			l.add("capabilities_schema", true, "", "", "")
		}

		requiredTools := []string{"context_for_task", "source_evidence"}
		if len(rc.oracle.RequiredCapabilityTools) > 0 {
			requiredTools = rc.oracle.RequiredCapabilityTools
		}
		enabled := newStringSet(caps.EnabledTools...)
		missing := enabled.missing(requiredTools)
		l.add("capabilities_required_tools", len(missing) == 0, joinIDs(requiredTools), joinIDs(enabled.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))

		forbiddenTools := []string{"record_episode"}
		if len(rc.oracle.ForbiddenCapabilityTools) > 0 {
			forbiddenTools = rc.oracle.ForbiddenCapabilityTools
		}
		present := enabled.present(forbiddenTools)
		l.add("capabilities_forbidden_tools_absent", len(present) == 0, "none of "+joinIDs(forbiddenTools), joinIDs(present), ifNotEmpty(present, "forbidden tool(s) enabled"))

		wantEpisodeWrite := false
		if rc.oracle.EpisodeWrite != nil {
			wantEpisodeWrite = *rc.oracle.EpisodeWrite
		}
		l.add("capabilities_episode_write", caps.Permissions.EpisodeWrite == wantEpisodeWrite,
			fmt.Sprintf("%v", wantEpisodeWrite), fmt.Sprintf("%v", caps.Permissions.EpisodeWrite), "")

		if len(rc.oracle.RequiredSchemaVersions) > 0 {
			supported := newStringSet(caps.SupportedSchemaVersions...)
			missingVersions := supported.missing(rc.oracle.RequiredSchemaVersions)
			l.add("capabilities_schema_versions", len(missingVersions) == 0,
				joinIDs(rc.oracle.RequiredSchemaVersions), joinIDs(supported.sorted()),
				ifNotEmpty(missingVersions, "missing: "+joinIDs(missingVersions)))
		}
	}

	// task-004/task-005-style denial (docs/fullstack-acceptance.md section 5): the request is
	// refused before packet assembly (run_foreign_repo_task/run_unavailable_evidence_task
	// never produce context-packet-<task_id>.json at all), so none of the packet/evidence
	// checks below apply.
	if isDeniedTask(rc) {
		layerHTTPDenial(rc, l)
		return l
	}

	var packet contractsv1.ContextPacket
	rawPacket, found, err := loadArtifact(rc, "context-packet", "json", &packet)
	switch {
	case err != nil:
		l.add("context_packet_decode", false, "valid context-packet.json", "", err.Error())
		return l
	case !found:
		l.add("context_packet_present", false, "context-packet.json present", "missing", "artifact not found")
		return l
	}
	rc.packet = &packet
	rc.packetRaw = rawPacket

	if err := rc.packetSchemas.validateJSON("context_packet.v1.schema.json", rawPacket); err != nil {
		l.add("context_packet_schema", false, "valid per context_packet.v1", "invalid", err.Error())
	} else {
		l.add("context_packet_schema", true, "", "", "")
	}

	ids := newStringSet()
	entityPresent := newStringSet()
	entityByID := map[string]string{}
	var categories, ruleIDs []string
	for _, item := range packet.Items {
		ids.add(item.EvidenceRefIDs...)
		categories = append(categories, string(item.Category))
		ruleIDs = append(ruleIDs, item.RuleID)
		for _, re := range item.RelatedEntities {
			entityPresent.add(entityKey(re.Type, re.ID))
		}
		// internal/contextpacket/ranking.go always builds evidence-sourced items with
		// exactly one related entity paired with exactly one evidence_ref_id; only trust the
		// ID->entity pairing in that unambiguous shape.
		if len(item.EvidenceRefIDs) == 1 && len(item.RelatedEntities) == 1 {
			entityByID[item.EvidenceRefIDs[0]] = entityKey(item.RelatedEntities[0].Type, item.RelatedEntities[0].ID)
		}
	}
	rc.packetKnownIDs = ids
	rc.entityPresent = entityPresent
	rc.entityByID = entityByID

	if rc.oracle.ExpectedPacketStatus != nil {
		l.add("packet_status", string(packet.Status) == *rc.oracle.ExpectedPacketStatus, *rc.oracle.ExpectedPacketStatus, string(packet.Status), "")
	}
	if rc.oracle.ExpectedScopeResolution != nil {
		l.add("resolved_scope_resolution", string(packet.ResolvedScope.Resolution) == *rc.oracle.ExpectedScopeResolution,
			*rc.oracle.ExpectedScopeResolution, string(packet.ResolvedScope.Resolution), "")
	}
	if len(rc.oracle.RequiredPacketCategories) > 0 {
		have := newStringSet(categories...)
		missing := have.missing(rc.oracle.RequiredPacketCategories)
		l.add("packet_required_categories", len(missing) == 0, joinIDs(rc.oracle.RequiredPacketCategories), joinIDs(have.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))
	}
	if len(rc.oracle.RequiredRuleIDs) > 0 {
		have := newStringSet(ruleIDs...)
		missing := have.missing(rc.oracle.RequiredRuleIDs)
		l.add("packet_required_rule_ids", len(missing) == 0, joinIDs(rc.oracle.RequiredRuleIDs), joinIDs(have.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))
	}
	if rc.oracle.MinExpandableEvidence > 0 {
		l.add("min_expandable_evidence", len(ids) >= rc.oracle.MinExpandableEvidence,
			fmt.Sprintf(">=%d", rc.oracle.MinExpandableEvidence), fmt.Sprintf("%d", len(ids)), "")
	}

	// required_evidence/forbidden_evidence match by (entity_type, entity_id), never by the
	// opaque wire evidence_ref_id -- see README.md#evidence-ref-id-matching.
	if len(rc.oracle.RequiredEvidence) > 0 {
		var required, missing []string
		for _, e := range rc.oracle.RequiredEvidence {
			key := entityKey(e.EntityType, e.EntityID)
			required = append(required, key)
			if !entityPresent.has(key) {
				missing = append(missing, key)
			}
		}
		l.add("required_evidence_entities_present", len(missing) == 0, joinIDs(required), joinIDs(entityPresent.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))
	}
	if len(rc.oracle.ForbiddenEvidence) > 0 {
		var forbidden, present []string
		for _, e := range rc.oracle.ForbiddenEvidence {
			key := entityKey(e.EntityType, e.EntityID)
			forbidden = append(forbidden, key)
			if entityPresent.has(key) {
				present = append(present, key)
			}
		}
		l.add("forbidden_evidence_entities_absent", len(present) == 0, "none of "+joinIDs(forbidden), joinIDs(present), ifNotEmpty(present, "forbidden entit(y/ies) present"))
	}

	if rc.oracle.ExpectedUnavailableSourcesExact || len(rc.oracle.ExpectedUnavailableSources) > 0 {
		var expectedPairs, actualPairs []string
		for _, u := range rc.oracle.ExpectedUnavailableSources {
			expectedPairs = append(expectedPairs, u.Source+":"+u.Reason)
		}
		for _, u := range packet.Coverage.SourcesUnavailable {
			actualPairs = append(actualPairs, u.Source+":"+u.Reason)
		}
		if rc.oracle.ExpectedUnavailableSourcesExact {
			expectedSet, actualSet := newStringSet(expectedPairs...), newStringSet(actualPairs...)
			exact := equalSorted(actualPairs, expectedPairs)
			message := ""
			if !exact {
				// "Good news" entries: the oracle expected these unavailable but they are
				// not anymore -- e.g. CHAOS-3068 (incidents.v1) got fixed. Call that out by
				// name instead of just reporting a generic mismatch, so the operator's first
				// reaction is "update the oracle", not "something broke".
				becameAvailable := actualSet.missing(expectedPairs)  // in expected, absent from actual
				newlyUnavailable := expectedSet.missing(actualPairs) // in actual, absent from expected
				switch {
				case len(newlyUnavailable) == 0 && len(becameAvailable) > 0:
					message = fmt.Sprintf("actual unavailable-source set is a strict subset of expected -- %s became available. If this is the CHAOS-3068 fix (incidents.v1 repointed at a real table), update this task's oracle: shrink expected_unavailable_sources accordingly and, if it is now empty, flip expected_packet_status to \"complete\".", joinIDs(becameAvailable))
				default:
					message = fmt.Sprintf("unavailable-source set does not match: newly unavailable (regression) = [%s], became available = [%s]", joinIDs(newlyUnavailable), joinIDs(becameAvailable))
				}
			}
			l.add("unavailable_sources_exact", exact, joinIDs(expectedSet.sorted()), joinIDs(actualSet.sorted()), message)
		} else {
			have := newStringSet(actualPairs...)
			missing := have.missing(expectedPairs)
			l.add("unavailable_sources_present", len(missing) == 0, joinIDs(expectedPairs), joinIDs(have.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))
		}
	}

	// scope.as_of pin agreement (testdata/fullstack/v1/README.md "The as_of pin" /
	// fixture-manifest.json's as_of_pin): basePacket() propagates request.Scope.AsOf into
	// both packet.generated_at and packet.freshness.as_of, so an oracle that declares as_of
	// gets an exact-equality check on both.
	if rc.oracle.ExpectedAsOf != nil {
		expected, err := time.Parse(time.RFC3339Nano, *rc.oracle.ExpectedAsOf)
		if err != nil {
			l.add("as_of_pin_declared_value_valid", false, "a valid RFC3339 timestamp", *rc.oracle.ExpectedAsOf, err.Error())
		} else {
			l.add("freshness_as_of_matches_pin", packet.Freshness.AsOf.Equal(expected), expected.Format(time.RFC3339Nano), packet.Freshness.AsOf.Format(time.RFC3339Nano), "")
			l.add("generated_at_matches_pin", packet.GeneratedAt.Equal(expected), expected.Format(time.RFC3339Nano), packet.GeneratedAt.Format(time.RFC3339Nano), "")
		}
	}

	return l
}

// layerHTTPDenial checks a task-004/task-005-style denial: negative-<task_id>.json, written
// by scripts/e2e/fullstack-opencode.sh's expect_task_http_error straight from the HTTP
// response body and independently validated there by
// scripts/e2e/validate-error-receipt.sh against contracts/v1 ErrorEnvelope / error.v1. This
// re-validates the same artifact against the same schema (defense in depth: this tool must
// not simply trust the shell script's own gate) and additionally compares against the
// oracle's pinned expected status/code, when present.
func layerHTTPDenial(rc *runContext, l *Layer) {
	var envelope contractsv1.ErrorEnvelope
	raw, found, err := loadArtifact(rc, "negative", "json", &envelope)
	switch {
	case err != nil:
		l.add("http_denial_decode", false, "valid negative-<task_id>.json", "", err.Error())
		return
	case !found:
		l.add("http_denial_present", false, "negative-<task_id>.json present", "missing", "artifact not found")
		return
	}
	if err := rc.packetSchemas.validateJSON("error.v1.schema.json", raw); err != nil {
		l.add("http_denial_schema", false, "valid per error.v1", "invalid", err.Error())
	} else {
		l.add("http_denial_schema", true, "", "", "")
	}
	if status, code, ok := rc.oracle.httpExpectation(); ok {
		if status != 0 {
			l.add("http_denial_status", envelope.Error.HTTPStatus == status, fmt.Sprintf("%d", status), fmt.Sprintf("%d", envelope.Error.HTTPStatus), "")
		}
		if code != "" {
			l.add("http_denial_code", envelope.Error.Code == code, code, envelope.Error.Code, "")
		}
	}
}

// --- L3 mcp ---

// recordEpisodeState resolves CHAOS-3565's cohort-scoped expectation for
// record_episode into exactly two states -- never a third "required" state,
// per review finding M4: nothing in this harness obliges an LLM agent to
// call record_episode even when it is permitted and reachable, so asserting
// "it WAS observed" would bet the whole task's pass/fail on nondeterministic
// agent behavior rather than on anything this service actually guarantees.
//
//   - recordEpisodeForbidden (default: oracle.EpisodeWrite is nil or false):
//     record_episode is disabled by default everywhere -- DisabledTools in
//     cmd/acr-mcp/main.go and an empty hosted Capabilities.EnabledTools mean
//     OpenCode is never even told the tool exists -- so it must never be
//     observed. This is every existing/landed oracle's state, unconditionally,
//     and its check id and behavior are byte-identical to before CHAOS-3565.
//   - recordEpisodePermittedOptional (oracle.EpisodeWrite == true): a
//     design-partner-cohort-scoped task where the tool is reachable
//     (capabilities_episode_write/capabilities_required_tools in L2 already
//     assert that) but optional. Observing it is not required; if it WAS
//     observed, its result must validate against the MCP contract.
type recordEpisodeState int

const (
	recordEpisodeForbidden recordEpisodeState = iota
	recordEpisodePermittedOptional
)

func recordEpisodeExpectedState(oracle Oracle) recordEpisodeState {
	if oracle.EpisodeWrite != nil && *oracle.EpisodeWrite {
		return recordEpisodePermittedOptional
	}
	return recordEpisodeForbidden
}

// mcpRecordEpisodeResult validates a record_episode tool invocation's result text against
// mcp_record_episode_response.v1, mirroring mcpSourceEvidenceResult's decode-and-validate
// pattern for source_evidence (clientevidence.go). ResultText is the raw JSON-encoded string
// OpenCode's event stream carries (see events.go).
func mcpRecordEpisodeResult(schemas *schemaLoader, resultText string) error {
	if err := schemas.validateJSON("mcp_record_episode_response.v1.schema.json", []byte(resultText)); err != nil {
		return fmt.Errorf("record_episode result does not validate against mcp_record_episode_response.v1: %w", err)
	}
	return nil
}

// recordEpisodeCheck records L3's record_episode check(s), split by state so the emitted
// check id always matches what's actually being asserted (review finding M5): a row named
// "record_episode_never_observed" always means exactly that, never "observed and valid" or
// "optionally observed". invocation is the first observed record_episode ToolInvocation, or
// nil if none was observed; it is only read when observedRecordEpisode is true.
func recordEpisodeCheck(l *Layer, state recordEpisodeState, observedRecordEpisode bool, invocation *ToolInvocation, schemas *schemaLoader) {
	switch state {
	case recordEpisodeForbidden:
		// Unconditional default, byte-identical to every oracle before
		// CHAOS-3565: the check id, expected label, and pass condition are
		// exactly what they were when this was the only branch.
		l.add("record_episode_never_observed", !observedRecordEpisode, "not observed", fmt.Sprintf("%v", observedRecordEpisode), "")
	case recordEpisodePermittedOptional:
		// A distinct check id: record_episode being present here is not a
		// failure of "record_episode_never_observed" (that check does not
		// run in this state at all), so it must not share that name.
		switch {
		case !observedRecordEpisode:
			l.skip("record_episode_permitted_optional", "record_episode was not observed this run -- writeback is optional for a design-partner-cohort-scoped task, never required")
		case invocation.Failed():
			l.add("record_episode_permitted_optional", false, "a successful record_episode result", "tool call failed", "")
		default:
			if err := mcpRecordEpisodeResult(schemas, invocation.ResultText); err != nil {
				l.add("record_episode_permitted_optional", false, "valid mcp_record_episode_response.v1", "invalid", err.Error())
			} else {
				l.add("record_episode_permitted_optional", true, "valid mcp_record_episode_response.v1", "valid", "")
			}
		}
	}
}

func layerMCP(rc *runContext) *Layer {
	l := newLayer("L3", "mcp")

	// mcp-tools.json is captured once per run (capture_mcp_tools) directly from the sidecar's
	// tools/list, independent of task.
	toolsPath, found := findArtifact(rc.artifactsDir, rc.taskID, "mcp-tools", "json")
	if !found {
		l.add("mcp_tools_present", false, "mcp-tools.json present", "missing", "artifact not found")
	} else if raw, err := os.ReadFile(toolsPath); err != nil {
		l.add("mcp_tools_read", false, "", "", err.Error())
	} else if names, err := mcpToolNames(raw); err != nil {
		l.add("mcp_tools_decode", false, "", "", err.Error())
	} else {
		// CHAOS-3564 (episode read path) annotation: adding the episode
		// get/list routes was flagged as a known risk to this exact-match
		// oracle, so it was checked deliberately. The read path is HTTP-only
		// (GET /api/v1/agent-context/episodes and .../episodes/{episode_id}),
		// registered in internal/api/app.go against internal/api's own mux --
		// it never touches internal/mcp or cmd/acr-mcp's hardcoded tool list
		// (see internal/mcp/server.go's toolContextForTask/toolSourceEvidence
		// constants and cmd/acr-mcp/main.go's EnabledTools). That is a
		// deliberate curated-contract boundary (see CHAOS-3564's "no native
		// graph/MCP exposure" constraint), not an oversight, so this baseline
		// stays exactly the two tools that exist today. If a future ticket
		// adds a curated MCP episode tool, this baseline must change with it.
		expected := []string{"context_for_task", "source_evidence"}
		if len(rc.oracle.ExpectedMCPTools) > 0 {
			expected = rc.oracle.ExpectedMCPTools
		}
		l.add("mcp_tools_exact", equalSorted(names, expected), joinIDs(expected), joinIDs(newStringSet(names...).sorted()), "")
	}

	if isDeniedTask(rc) {
		l.skip("opencode_events_not_applicable", "SKIPPED (deliberate scope choice, not verified): denied task, no OpenCode session ever ran -- run_foreign_repo_task/run_unavailable_evidence_task call the API directly. See docs/fullstack-acceptance.md section 5 task-005.")
		return l
	}

	eventsPath, found := findArtifact(rc.artifactsDir, rc.taskID, "opencode-events", "jsonl")
	if !found {
		l.add("opencode_events_present", false, "opencode-events.jsonl present", "missing", "artifact not found")
		return l
	}
	stream, err := newOpencodeEventsFromFile(eventsPath)
	if err != nil {
		l.add("opencode_events_parse", false, "", "", err.Error())
		return l
	}
	invocations, err := stream.ToolInvocations()
	if err != nil {
		l.add("opencode_events_tool_invocations", false, "", "", err.Error())
		return l
	}
	observed := newStringSet()
	var nonEvidenceIO []byte
	var recordEpisodeInvocation *ToolInvocation
	for i, inv := range invocations {
		observed.add(inv.Name)
		if inv.Name != "context_for_task" && inv.Name != "source_evidence" {
			nonEvidenceIO = append(nonEvidenceIO, inv.Arguments...)
			nonEvidenceIO = append(nonEvidenceIO, inv.ResultText...)
		}
		if inv.Name == "record_episode" && recordEpisodeInvocation == nil {
			recordEpisodeInvocation = &invocations[i]
		}
	}
	rc.nonEvidenceToolIO = nonEvidenceIO

	l.add("observed_context_for_task", observed.has("context_for_task"), "observed", fmt.Sprintf("%v", observed.has("context_for_task")), "")
	recordEpisodeCheck(l, recordEpisodeExpectedState(rc.oracle), observed.has("record_episode"), recordEpisodeInvocation, rc.packetSchemas)

	// Parse every source_evidence invocation into a clientEvidenceCall (argument + result),
	// grounded against the live packet -- this is the primary source of truth L4/L5 build on
	// (Codex finding 3), not the driver's direct-HTTP expanded-evidence/*.json captures,
	// which exist regardless of what the client itself ever called.
	calls := collectClientEvidenceCalls(rc, invocations)
	rc.clientEvidenceCalls = calls
	clientDocs := make([]contractsv1.ExpandedEvidence, 0, len(calls))
	clientKnownIDs := newStringSet()
	clientEntityByID := map[string]string{}
	for i, call := range calls {
		if !call.ArgumentParsed {
			l.add(fmt.Sprintf("source_evidence_argument_parses[%d]", i), false, "evidence_ref_id present", "", "source_evidence tool call carried no parseable evidence_ref_id argument")
			continue
		}
		l.add(fmt.Sprintf("source_evidence_argument_is_live_packet_evidence[%s]", call.ArgumentEvidenceRefID),
			call.ArgumentIsLivePacketEvidence, "member of the live packet's evidence_ref_ids", fmt.Sprintf("%v", call.ArgumentIsLivePacketEvidence),
			ifNotEmpty(boolAsList(!call.ArgumentIsLivePacketEvidence), "the client requested an evidence_ref_id the live packet never returned"))
		if call.Failed {
			continue
		}
		// The result may arrive as JSON (a client that forwards MCP StructuredContent) or as
		// the sidecar's markdown rendering (OpenCode 1.18.4, which forwards only the text
		// content); either is a real answer, and neither is an agent-behaviour signal.
		if call.Doc == nil && !call.RenderedParsed {
			l.add(fmt.Sprintf("source_evidence_result_parses[%s]", call.ArgumentEvidenceRefID), false,
				"a decodable mcp_source_evidence_response.v1 result or sidecar rendering", "invalid", errString(call.DocError))
			continue
		}
		// The reference the client got back must be the one it asked for. Without this the
		// markdown path would accept any evidence rendering as proof of any expansion.
		l.add(fmt.Sprintf("source_evidence_result_echoes_requested_reference[%s]", call.ArgumentEvidenceRefID),
			call.echoedRequestedReference(), call.ArgumentEvidenceRefID, observedEvidenceRefID(call),
			ifNotEmpty(boolAsList(!call.echoedRequestedReference()), "the client received a different evidence reference than it requested"))
		if !call.echoedRequestedReference() {
			continue
		}
		if call.Doc != nil {
			clientDocs = append(clientDocs, *call.Doc)
		}
		clientKnownIDs.add(call.ArgumentEvidenceRefID)
		if entityType, entityID, ok := call.observedEntity(); ok {
			clientEntityByID[call.ArgumentEvidenceRefID] = entityKey(entityType, entityID)
		}
	}
	rc.clientExpandedDocs = clientDocs
	rc.clientExpandedKnownIDs = clientKnownIDs
	rc.clientEntityByID = clientEntityByID

	// The expansion floor: min_expandable_evidence counts genuine, verifiable expansions --
	// argument was a real live-packet evidence reference, the call did not fail, and the
	// result actually parsed -- never merely "source_evidence was called at least once"
	// (Codex finding 3). An empty/degraded packet returns no evidence references at all, so
	// there is nothing to expand and source_evidence must NOT be called -- calling it would
	// mean the agent invented an identifier.
	successfulExpansions := 0
	for _, call := range calls {
		if call.succeeded() {
			successfulExpansions++
		}
	}
	sawEvidence := observed.has("source_evidence")
	switch {
	case rc.oracle.expectsNoEvidence():
		l.add("source_evidence_not_called_for_degraded_packet", !sawEvidence,
			"not observed", fmt.Sprintf("%v", sawEvidence),
			ifNotEmpty(boolAsList(sawEvidence), "the packet returned no evidence, so any expansion used an invented reference"))
	case rc.oracle.MinExpandableEvidence > 0:
		l.add("source_evidence_meets_expansion_floor", successfulExpansions >= rc.oracle.MinExpandableEvidence,
			fmt.Sprintf(">=%d successful expansion(s)", rc.oracle.MinExpandableEvidence), fmt.Sprintf("%d", successfulExpansions), "")
	}

	var failed []string
	for _, inv := range invocations {
		if inv.Failed() {
			failed = append(failed, fmt.Sprintf("%s:%s", inv.Name, inv.Status))
		}
	}
	l.add("no_failed_tool_invocations", len(failed) == 0, "status completed", joinIDs(failed), ifNotEmpty(failed, "tool invocation(s) did not complete"))

	if _, err := stream.FinalAssistantText(); err != nil {
		l.add("single_final_text_part", false, "<=1 non-empty final text part", "", err.Error())
	} else {
		l.add("single_final_text_part", true, "", "", "")
	}

	return l
}

// --- L4 evidence ---

func layerEvidence(rc *runContext) *Layer {
	l := newLayer("L4", "evidence")

	if isDeniedTask(rc) {
		l.skip("evidence_not_applicable", "SKIPPED (deliberate scope choice, not verified): denied task, no packet was assembled, so there is no evidence to expand.")
		return l
	}

	// content_hash pins, when the oracle sets one, are keyed by entity (never by the opaque
	// wire evidence_ref_id -- see README.md#evidence-ref-id-matching).
	contentHashByEntity := map[string]string{}
	for _, e := range rc.oracle.RequiredEvidence {
		if e.ContentHash != "" {
			contentHashByEntity[entityKey(e.EntityType, e.EntityID)] = e.ContentHash
		}
	}

	var safeHosts []string

	// Primary: what the client's own source_evidence calls actually returned (rc.clientEvidenceCalls,
	// populated by L3 from the event stream). This is what L4 grades first -- a session that
	// expanded nothing the oracle required, or whose calls returned malformed content, must
	// fail here even if the driver's independent direct-HTTP capture below looks fine
	// (Codex finding 3).
	for _, call := range rc.clientEvidenceCalls {
		if !call.ArgumentParsed || call.Failed {
			continue // already reported by L3; nothing evidence-shaped to validate here
		}
		label := call.ArgumentEvidenceRefID
		if call.DocError != nil || (call.Doc == nil && !call.RenderedParsed) {
			l.add(fmt.Sprintf("client_expanded_evidence_result[%s]", label), false,
				"a decodable expanded_evidence.v1 document or sidecar rendering", "invalid", errString(call.DocError))
			continue
		}
		if call.Doc == nil {
			// The markdown path (what OpenCode actually forwards). The rendering carries the
			// evidence's identity and availability but not the full document, so the
			// schema/content-hash grading below is not possible from it. That grading is not
			// dropped: the direct-HTTP capture of this same evidence_ref_id is schema-checked
			// and hash-checked in full, and client_and_direct_http_evidence_agree ties what
			// the client saw to that capture, so a divergence still fails.
			l.skip(fmt.Sprintf("client_expanded_evidence_schema[%s]", label),
				"SKIPPED (client limitation, not verified here): OpenCode forwards the sidecar's markdown rendering rather than MCP StructuredContent, so the full document is graded via the direct-HTTP capture plus client_and_direct_http_evidence_agree.")
			continue
		}
		doc := *call.Doc
		if err := rc.packetSchemas.validateJSON("expanded_evidence.v1.schema.json", call.DocRaw); err != nil {
			l.add(fmt.Sprintf("client_expanded_evidence_schema[%s]", label), false, "valid per expanded_evidence.v1", "invalid", err.Error())
		} else {
			l.add(fmt.Sprintf("client_expanded_evidence_schema[%s]", label), true, "", "", "")
		}
		if u, err := url.Parse(doc.Evidence.Source.SafeURI); err == nil && u.Host != "" {
			safeHosts = append(safeHosts, u.Host)
		}
		key := entityKey(doc.Evidence.Source.EntityType, doc.Evidence.Source.EntityID)
		if expectedHash, ok := contentHashByEntity[key]; ok {
			l.add(fmt.Sprintf("client_expanded_evidence_content_hash[%s]", key), doc.Evidence.ContentDigest == expectedHash, expectedHash, doc.Evidence.ContentDigest, "")
		}
	}

	// Cross-check: the driver's direct-HTTP expanded-evidence/*.json captures
	// (capture_expanded_evidence expands every evidence_ref_id the packet references, via a
	// separate direct API call, independent of what the client session did). Still validated
	// and schema-checked in full -- it proves the API's direct read path itself is
	// schema-correct -- but no longer the primary grounding for "did this citation resolve to
	// something real" (see resolveEntity / layerAgentResult's "known" set).
	dir, found := findExpandedEvidenceDir(rc.artifactsDir, rc.taskID)
	if !found {
		l.add("direct_http_expanded_evidence_present", false, "expanded-evidence directory present", "missing", "artifact not found")
	} else if files, err := listExpandedEvidenceFiles(dir); err != nil {
		l.add("direct_http_expanded_evidence_list", false, "", "", err.Error())
	} else {
		knownIDs := newStringSet()
		// What the client itself observed per evidence_ref_id, from whichever representation
		// it was handed (JSON document or markdown rendering) -- this is what the direct-HTTP
		// capture is cross-checked against below.
		type clientObservation struct{ entity, availability string }
		clientObservedByID := map[string]clientObservation{}
		for _, call := range rc.clientEvidenceCalls {
			if !call.succeeded() {
				continue
			}
			entityType, entityID, haveEntity := call.observedEntity()
			availability, haveAvailability := call.observedAvailability()
			if !haveEntity && !haveAvailability {
				continue
			}
			clientObservedByID[call.ArgumentEvidenceRefID] = clientObservation{
				entity: entityKey(entityType, entityID), availability: availability,
			}
		}
		for _, file := range files {
			name := filepath.Base(file)
			var doc contractsv1.ExpandedEvidence
			raw, err := readJSONFile(file, &doc)
			if err != nil {
				l.add(fmt.Sprintf("direct_http_expanded_evidence_decode[%s]", name), false, "", "", err.Error())
				continue
			}
			rc.expandedDocs = append(rc.expandedDocs, doc)
			rc.expandedRaw = append(rc.expandedRaw, raw)

			if err := rc.packetSchemas.validateJSON("expanded_evidence.v1.schema.json", raw); err != nil {
				l.add(fmt.Sprintf("direct_http_expanded_evidence_schema[%s]", name), false, "valid per expanded_evidence.v1", "invalid", err.Error())
			} else {
				l.add(fmt.Sprintf("direct_http_expanded_evidence_schema[%s]", name), true, "", "", "")
			}

			id := doc.Evidence.EvidenceRefID
			knownIDs.add(id)
			if id != "" {
				if u, err := url.Parse(doc.Evidence.Source.SafeURI); err == nil && u.Host != "" {
					safeHosts = append(safeHosts, u.Host)
				}
			}

			if rc.packetKnownIDs != nil {
				l.add(fmt.Sprintf("direct_http_expanded_evidence_belongs_to_packet[%s]", id), rc.packetKnownIDs.has(id),
					"member of the packet's evidence_ref_ids", fmt.Sprintf("%v", rc.packetKnownIDs.has(id)), "")
			}

			directKey := entityKey(doc.Evidence.Source.EntityType, doc.Evidence.Source.EntityID)
			// The oracle's content_hash pin is graded here rather than only on the client's
			// own copy: the rendering the client receives does not carry content_digest, so
			// pinning it solely there would silently stop checking on the markdown path.
			if expectedHash, ok := contentHashByEntity[directKey]; ok {
				l.add(fmt.Sprintf("direct_http_expanded_evidence_content_hash[%s]", directKey),
					doc.Evidence.ContentDigest == expectedHash, expectedHash, doc.Evidence.ContentDigest, "")
			}

			if observed, ok := clientObservedByID[id]; ok {
				l.add(fmt.Sprintf("client_and_direct_http_evidence_agree[%s]", id),
					observed.entity == directKey && observed.availability == string(doc.Availability),
					fmt.Sprintf("entity=%s availability=%s", observed.entity, observed.availability),
					fmt.Sprintf("entity=%s availability=%s", directKey, doc.Availability), "")
			}
		}
		rc.expandedKnownIDs = knownIDs
	}

	ok, message := checkNoOutboundFetch(rc, safeHosts)
	l.add("no_outbound_evidence_url_fetch", ok, "not fetched", "", message)

	return l
}

// checkNoOutboundFetch scans opencode-events.jsonl and logs/*.log (never the artifact files
// that legitimately echo safe_uri as data, e.g. context-packet.json / expanded-evidence
// themselves) for occurrences of each expanded evidence document's safe_uri host, per
// docs/fullstack-acceptance.md section 7 layer 4.
func checkNoOutboundFetch(rc *runContext, hosts []string) (bool, string) {
	hosts = newStringSet(hosts...).sorted()
	if len(hosts) == 0 {
		return true, ""
	}

	// The haystack is every OTHER tool call's own input/output (rc.nonEvidenceToolIO, built by
	// L3) plus the driver's logs -- deliberately NOT the raw opencode-events.jsonl text, since
	// a legitimate source_evidence/context_for_task round trip necessarily echoes each
	// evidence_ref_id's safe_uri back into the event stream as part of the structured document
	// the server returns, which would otherwise make this check fail on every compliant run.
	var haystacks [][]byte
	if len(rc.nonEvidenceToolIO) > 0 {
		haystacks = append(haystacks, rc.nonEvidenceToolIO)
	}
	logsDir := filepath.Join(rc.artifactsDir, "logs")
	if entries, err := os.ReadDir(logsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(logsDir, entry.Name())); err == nil {
				haystacks = append(haystacks, data)
			}
		}
	}

	var hit []string
	for _, host := range hosts {
		for _, data := range haystacks {
			if bytes.Contains(data, []byte(host)) {
				hit = append(hit, host)
				break
			}
		}
	}
	if len(hit) > 0 {
		return false, "evidence safe_uri host(s) appeared in events/logs, suggesting an outbound fetch was attempted: " + joinIDs(hit)
	}
	return true, ""
}

// --- L5 agent_result ---

func layerAgentResult(rc *runContext) *Layer {
	l := newLayer("L5", "agent_result")

	if isDeniedTask(rc) {
		l.skip("agent_result_not_applicable", "SKIPPED (deliberate scope choice, not verified): denied task, no OpenCode session ran, so there is no agent result.")
		return l
	}

	path, found := findArtifact(rc.artifactsDir, rc.taskID, "agent-result", "json")
	if !found {
		l.add("agent_result_present", false, "agent-result.json present", "missing", "artifact not found")
		return l
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		l.add("agent_result_read", false, "", "", err.Error())
		return l
	}
	if err := rc.resultSchemas.validateJSON(rc.resultSchema, raw); err != nil {
		l.add("agent_result_schema", false, "valid per "+rc.resultSchema, "invalid", err.Error())
	} else {
		l.add("agent_result_schema", true, "", "", "")
	}
	var result harnessAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		l.add("agent_result_decode", false, "", "", err.Error())
		return l
	}

	// known is what a citation is allowed to resolve against: the live packet's own
	// evidence_ref_ids, plus what the client session itself actually expanded via
	// source_evidence. The driver's direct-HTTP expanded-evidence/*.json captures
	// (rc.expandedKnownIDs) are deliberately excluded here -- they exist regardless of what the
	// agent did, so grading citations against them let a result cite evidence the agent never
	// actually retrieved still pass (Codex finding 3).
	known := rc.packetKnownIDs.union(rc.clientExpandedKnownIDs)

	// Identity/status/scope agreement, unconditionally, for every non-denied task -- not just
	// the degraded/empty case. Without this a result carrying another task's task_id, or the
	// wrong packet_status/scope_resolution, previously passed outright (Codex finding 4).
	l.add("agent_result_task_id_matches", result.TaskID == rc.logicalTaskID, rc.logicalTaskID, result.TaskID, "")
	if rc.packet != nil {
		l.add("agent_result_packet_status_matches_live_packet", result.PacketStatus == string(rc.packet.Status),
			string(rc.packet.Status), result.PacketStatus, "")
		l.add("agent_result_scope_resolution_matches_live_packet", result.ScopeResolution == string(rc.packet.ResolvedScope.Resolution),
			string(rc.packet.ResolvedScope.Resolution), result.ScopeResolution, "")
	}
	if rc.oracle.ExpectedPacketStatus != nil {
		l.add("agent_result_packet_status_matches_oracle", result.PacketStatus == *rc.oracle.ExpectedPacketStatus,
			*rc.oracle.ExpectedPacketStatus, result.PacketStatus, "")
	}
	if rc.oracle.ExpectedScopeResolution != nil {
		l.add("agent_result_scope_resolution_matches_oracle", result.ScopeResolution == *rc.oracle.ExpectedScopeResolution,
			*rc.oracle.ExpectedScopeResolution, result.ScopeResolution, "")
	}

	if rc.oracle.FindingsMustBeEmpty {
		l.add("findings_must_be_empty", len(result.Findings) == 0, "0 findings", fmt.Sprintf("%d findings", len(result.Findings)), "")
		if rc.oracle.expectsNoEvidence() {
			// Soft, spirit-of-the-rule check: docs/fullstack-acceptance.md section 7 layer 5 says
			// an empty/degraded packet must "stay explicit" -- the model is expected to disclose
			// the degradation (see e.g. task-003's oracle required_disclosure), not just go quiet.
			l.add("degraded_disclosed_in_assumptions", len(result.Assumptions) > 0, ">=1 assumption", fmt.Sprintf("%d assumptions", len(result.Assumptions)), "")
		}
	}

	findingByClaimID := map[string]harnessFinding{}
	var invented []string
	var claimIDs []string
	for _, finding := range result.Findings {
		claimIDs = append(claimIDs, finding.ClaimID)
		findingByClaimID[finding.ClaimID] = finding
		if finding.ClaimKind != "observed" {
			continue
		}
		if len(finding.EvidenceRefIDs) == 0 {
			l.add(fmt.Sprintf("observed_finding_has_citation[%s]", finding.ClaimID), false, ">=1 evidence_ref_id", "0", "")
			continue
		}
		for _, id := range finding.EvidenceRefIDs {
			if !known.has(id) {
				invented = append(invented, fmt.Sprintf("%s:%s", finding.ClaimID, id))
			}
		}
	}
	l.add("no_invented_evidence_ids", len(invented) == 0, "every cited ID present in the packet or an expanded-evidence doc", joinIDs(invented), ifNotEmpty(invented, "invented citation(s) found"))

	if len(rc.oracle.RequiredFindings) > 0 {
		var requiredClaimIDs []string
		for _, rf := range rc.oracle.RequiredFindings {
			requiredClaimIDs = append(requiredClaimIDs, rf.ClaimID)
		}
		have := newStringSet(claimIDs...)
		missing := have.missing(requiredClaimIDs)
		l.add("required_findings_present", len(missing) == 0, joinIDs(requiredClaimIDs), joinIDs(have.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))

		// Every required finding that is actually present is checked against what the oracle
		// declared for it -- claim_kind and, when pinned, the entity it must cite. Both checks
		// are unconditional once the finding is present: an absent check is exactly the
		// vacuity this tool keeps having to close (Codex round-2 finding 1). Before this fix,
		// an agent reporting the required claim_id with claim_kind "inferred" and
		// evidence_ref_ids:[] slipped every gate -- no_invented_evidence_ids and
		// observed_finding_has_citation both skip non-"observed" findings by design, and the
		// entity-citation check below only ever recorded a check when a citation resolved, so
		// zero citations meant the check was silently never added at all, not failed.
		for _, rf := range rc.oracle.RequiredFindings {
			finding, ok := findingByClaimID[rf.ClaimID]
			if !ok {
				continue // required_findings_present above already reports a missing claim_id
			}
			if rf.ClaimKind != "" {
				l.add(fmt.Sprintf("required_finding_claim_kind_matches[%s]", rf.ClaimID),
					finding.ClaimKind == rf.ClaimKind, rf.ClaimKind, finding.ClaimKind, "")
			}
			if rf.MustCiteEntity == nil {
				continue
			}
			wantKey := entityKey(rf.MustCiteEntity.EntityType, rf.MustCiteEntity.EntityID)
			matched := false
			var resolved []string
			for _, id := range finding.EvidenceRefIDs {
				key, ok := rc.resolveEntity(id)
				if !ok {
					continue
				}
				resolved = append(resolved, key)
				if key == wantKey {
					matched = true
				}
			}
			message := ""
			switch {
			case len(finding.EvidenceRefIDs) == 0:
				message = "required finding has zero evidence_ref_ids"
			case len(resolved) == 0:
				message = "none of the finding's evidence_ref_ids resolved to a known entity"
			case !matched:
				message = "the finding's resolved entity/entities do not include the required one"
			}
			l.add(fmt.Sprintf("required_finding_cites_entity[%s]", rf.ClaimID), matched, wantKey, joinIDs(finding.EvidenceRefIDs), message)
		}
	}
	if len(rc.oracle.RequiredChecks) > 0 {
		var checkIDs []string
		for _, c := range result.RecommendedChecks {
			checkIDs = append(checkIDs, c.CheckID)
		}
		have := newStringSet(checkIDs...)
		missing := have.missing(rc.oracle.RequiredChecks)
		l.add("required_checks_present", len(missing) == 0, joinIDs(rc.oracle.RequiredChecks), joinIDs(have.sorted()), ifNotEmpty(missing, "missing: "+joinIDs(missing)))
	}
	// forbidden_claims names an entity, not a claim_id (a fixture author cannot predict the
	// model's claim_id choices ahead of time for something that must never be claimed): no
	// finding may cite an evidence_ref_id that resolves to that entity. Wildcard entries
	// ("*"/"*", e.g. task-003/004/005's "must not fabricate anything") are documentation
	// paired with findings_must_be_empty above and are not independently checkable here.
	for _, fc := range rc.oracle.ForbiddenClaims {
		if fc.isWildcard() {
			continue
		}
		forbiddenKey := entityKey(fc.ForbiddenEntity.EntityType, fc.ForbiddenEntity.EntityID)
		var offending []string
		for _, finding := range result.Findings {
			for _, id := range finding.EvidenceRefIDs {
				if key, ok := rc.resolveEntity(id); ok && key == forbiddenKey {
					offending = append(offending, finding.ClaimID)
				}
			}
		}
		l.add(fmt.Sprintf("forbidden_claim_entity_absent[%s]", forbiddenKey), len(offending) == 0,
			"no finding cites "+forbiddenKey, joinIDs(offending), fc.Reason)
	}

	return l
}

// --- L6 web ---

// sameResolvedScope mirrors internal/contextpacket/assembler.go's own sameScope() --
// RepoID/RepoSlug/Branch/CommitSHA/Resolution/FallbackReasons -- since that is the product's
// own definition of "two resolutions describe the same scope". Unlike context_packet_id, the
// resolved scope is deterministic from the live data and the request's own goal/repo/scope
// inputs, so two independent requests carrying the same inputs against the same live backend
// are expected to agree here.
func sameResolvedScope(a, b contractsv1.ResolvedScope) bool {
	if len(a.FallbackReasons) != len(b.FallbackReasons) {
		return false
	}
	for i := range a.FallbackReasons {
		if a.FallbackReasons[i] != b.FallbackReasons[i] {
			return false
		}
	}
	return a.RepoID == b.RepoID && a.RepoSlug == b.RepoSlug && a.Branch == b.Branch &&
		a.CommitSHA == b.CommitSHA && a.Resolution == b.Resolution
}

func formatResolvedScope(s contractsv1.ResolvedScope) string {
	return fmt.Sprintf("repo_id=%s repo_slug=%s branch=%s commit_sha=%s resolution=%s fallback_reasons=%s",
		s.RepoID, s.RepoSlug, s.Branch, s.CommitSHA, s.Resolution, joinIDs(s.FallbackReasons))
}

// layerWeb only runs when web-packet.json and/or web-evidence.json exist for this task, per
// docs/fullstack-acceptance.md section 7 layer 6 ("only when the artifacts exist") -- in the
// current fixture only run_web_agreement_check's one task (task-002) produces them, flat and
// unsuffixed. It returns nil (no layer, not a failing layer) when neither is present.
func layerWeb(rc *runContext) *Layer {
	packetPath, packetFound := findArtifact(rc.artifactsDir, rc.taskID, "web-packet", "json")
	evidencePath, evidenceFound := findArtifact(rc.artifactsDir, rc.taskID, "web-evidence", "json")
	if !packetFound && !evidenceFound {
		return nil
	}
	l := newLayer("L6", "web")

	// The producer always emits web-packet.json and web-evidence.json together for a task
	// that exercises the web agreement check at all -- one present and the other absent means
	// the web capture itself partially failed, not that this task simply has nothing to check
	// for the missing half (Codex round-2 finding 4). Grading only whichever artifact happens
	// to exist would silently say nothing about that failure.
	l.add("web_artifacts_both_present", packetFound == evidenceFound,
		"both web-packet.json and web-evidence.json, or neither",
		fmt.Sprintf("web-packet=%v web-evidence=%v", packetFound, evidenceFound),
		ifNotEmpty(boolAsList(packetFound != evidenceFound), "the web capture produced only one of the two artifacts it always emits together"))

	if packetFound {
		// web-packet.json is the full contract document the browser's own POST returned
		// (scripts/e2e/svs-browser.mjs writes packetResult.json() verbatim), the same shape
		// as rc.packet -- decode it as one so every field is available to compare, not just
		// the two this layer originally hand-picked.
		var webPacket contractsv1.ContextPacket
		if _, err := readJSONFile(packetPath, &webPacket); err != nil {
			l.add("web_packet_decode", false, "", "", err.Error())
		} else if rc.packet != nil {
			// context_packet_id can never match, on any run: internal/contextpacket/
			// assembler.go's packetID() hashes org_id, request_id, repo_slug, branch,
			// commit_sha, and request_id is a server-generated per-request value
			// (internal/api/read_routes.go sets request.RequestID = RequestID(r.Context()),
			// overwriting whatever the client sent) -- never client-suppliable, never
			// reproducible across two independently issued requests. The driver's
			// direct-HTTP capture and the browser's own POST are exactly that: two
			// independent requests, so their packet IDs differ by construction every time.
			// A check that can never pass is worse than no check at all (it silently masked
			// this layer's real status), so this is recorded as skipped, not compared, and
			// replaced below by what actually IS comparable across two independent requests
			// against the same live data: repository, resolved scope, status, and (below)
			// the evidence set.
			l.skip("web_packet_id_not_comparable",
				"SKIPPED (structurally can never match, not verified): context_packet_id embeds the server-generated request_id (internal/api/read_routes.go RequestID()), which differs between the driver's direct-HTTP request and the browser's own independent request by construction -- see internal/contextpacket/assembler.go packetID().")
			l.add("web_packet_repository_matches_api", webPacket.Repository.Slug == rc.packet.Repository.Slug,
				rc.packet.Repository.Slug, webPacket.Repository.Slug, "")
			l.add("web_packet_resolved_scope_matches_api", sameResolvedScope(webPacket.ResolvedScope, rc.packet.ResolvedScope),
				formatResolvedScope(rc.packet.ResolvedScope), formatResolvedScope(webPacket.ResolvedScope), "")
			l.add("web_packet_status_matches_api", webPacket.Status == rc.packet.Status, string(rc.packet.Status), string(webPacket.Status), "")
		} else {
			l.add("web_packet_comparable", false, "an API context-packet.json to compare against", "none loaded", "L2 did not load a context packet for this task")
		}
	}

	if evidenceFound {
		var webEvidence struct {
			Evidence struct {
				EvidenceRefID string `json:"evidence_ref_id"`
			} `json:"evidence"`
			Availability string `json:"availability"`
		}
		if _, err := readJSONFile(evidencePath, &webEvidence); err != nil {
			l.add("web_evidence_decode", false, "", "", err.Error())
		} else {
			matched := false
			for _, doc := range rc.expandedDocs {
				if doc.Evidence.EvidenceRefID == webEvidence.Evidence.EvidenceRefID {
					matched = true
					l.add("web_evidence_availability_matches_api", string(doc.Availability) == webEvidence.Availability, webEvidence.Availability, string(doc.Availability), "")
					break
				}
			}
			l.add("web_evidence_identity_matches_api", matched, "an API expanded-evidence doc with the same evidence_ref_id", fmt.Sprintf("%v", matched), webEvidence.Evidence.EvidenceRefID)
		}
	}

	return l
}

// boolAsList lets a bare boolean feed ifNotEmpty, which keeps a passing check's Message empty.
func boolAsList(v bool) []string {
	if v {
		return []string{"true"}
	}
	return nil
}

// errString renders err's message, or "" for a nil error -- for feeding a Check's Message
// field without an extra nil check at every call site.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
