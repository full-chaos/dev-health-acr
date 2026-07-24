package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Oracle is testdata/fullstack/v1/expected/task-*.oracle.json's shape, as actually authored
// by the fixture owner (not a guess -- these fields were read directly off the landed oracle
// files and testdata/fullstack/v1/README.md). Only fields this tool actually consumes are
// modeled; the oracle files carry substantial additional prose (*_reasoning, *_note,
// evidence_ref_id_matching_contract, ...) that documents *why* each expectation holds but is
// not machine-checked here -- json.Unmarshal ignores it.
//
// Critical constraint, from README.md#evidence-ref-id-matching: a live evidence_ref_id is an
// opaque, per-request HMAC token (internal/contextpacket/evidence_id.go EvidenceIDCodec) that
// can never be precomputed ahead of a run. required_evidence/forbidden_evidence therefore
// name entities by (query_id, entity_type, entity_id), NOT by the wire evidence_ref_id --
// this tool matches by entity_type/entity_id against contracts/v1 ContextPacketItem's
// RelatedEntities (internal/contextpacket/ranking.go build them 1:1 from
// EvidenceRef.Source.EntityType/EntityID, so they carry the same, unencoded, plaintext
// values), never by string-comparing an oracle-pinned literal ID. query_id itself is not
// observable from any wire artifact, so it is decoded but not matched on.
type Oracle struct {
	SchemaVersion string `json:"schema_version"`
	TaskID        string `json:"task_id"`

	// Nullable in task-004/005 (no packet is ever assembled for those requests).
	ExpectedPacketStatus    *string `json:"expected_packet_status"`
	ExpectedScopeResolution *string `json:"expected_scope_resolution"`

	// ExpectedAsOf, when set, must equal fixture-manifest.json's as_of_pin.value
	// (2026-01-14T12:00:00.000Z as of this writing) -- basePacket() propagates
	// request.Scope.AsOf into both packet.generated_at and packet.freshness.as_of, so this
	// is a cheap, sharp check that the orchestrator's as_of pin actually took effect. No
	// landed oracle sets it yet (the pin lives in fixture-manifest.json, which assert-run
	// does not currently take a flag for); the field exists so a future oracle build step
	// can turn this check on without a code change.
	ExpectedAsOf *string `json:"as_of,omitempty"`

	ExpectedUnavailableSources      []UnavailableSourceOracle `json:"expected_unavailable_sources,omitempty"`
	ExpectedUnavailableSourcesExact bool                      `json:"expected_unavailable_sources_exact,omitempty"`

	RequiredEvidence  []EvidenceEntityOracle `json:"required_evidence,omitempty"`
	ForbiddenEvidence []EvidenceEntityOracle `json:"forbidden_evidence,omitempty"`

	RequiredPacketCategories []string `json:"required_packet_categories,omitempty"`
	RequiredRuleIDs          []string `json:"required_rule_ids,omitempty"`
	RequiredChecks           []string `json:"required_checks,omitempty"`

	MinExpandableEvidence int `json:"min_expandable_evidence,omitempty"`

	RequiredFindings    []RequiredFindingOracle `json:"required_findings,omitempty"`
	ForbiddenClaims     []ForbiddenClaimOracle  `json:"forbidden_claims,omitempty"`
	FindingsMustBeEmpty bool                    `json:"findings_must_be_empty,omitempty"`

	// Denial expectations. task-004 puts these at the top level; task-005 nests them under
	// source_evidence_request (its context_for_task_request block describes a normal 200 the
	// current orchestrator does not actually issue for that task -- see
	// scripts/e2e/fullstack-opencode.sh run_unavailable_evidence_task, which only calls the
	// evidence endpoint directly). httpExpectation() below tries both shapes.
	ExpectedHTTPStatus    int    `json:"expected_http_status,omitempty"`
	ExpectedErrorCode     string `json:"expected_error_code,omitempty"`
	SourceEvidenceRequest *struct {
		ExpectedHTTPStatus int    `json:"expected_http_status,omitempty"`
		ExpectedErrorCode  string `json:"expected_error_code,omitempty"`
	} `json:"source_evidence_request,omitempty"`

	// Not read by the shell orchestrator or any of the five landed oracle files (which rely
	// on this tool's built-in defaults: context_for_task+source_evidence
	// required/enabled, record_episode forbidden/disabled, episode_write=false). Kept so a
	// future oracle can override those defaults without a code change.
	RequiredCapabilityTools  []string `json:"required_capability_tools,omitempty"`
	ForbiddenCapabilityTools []string `json:"forbidden_capability_tools,omitempty"`
	EpisodeWrite             *bool    `json:"episode_write,omitempty"`
	RequiredSchemaVersions   []string `json:"required_schema_versions,omitempty"`
	ExpectedMCPTools         []string `json:"expected_mcp_tools,omitempty"`
}

// httpExpectation returns the expected HTTP status/code for a denied task, trying the
// top-level fields (task-004) then source_evidence_request (task-005). ok is false when
// neither is populated, in which case layerHTTPDenial still validates schema shape but skips
// the status/code comparison.
func (o Oracle) httpExpectation() (status int, code string, ok bool) {
	if o.ExpectedHTTPStatus != 0 || o.ExpectedErrorCode != "" {
		return o.ExpectedHTTPStatus, o.ExpectedErrorCode, true
	}
	if o.SourceEvidenceRequest != nil {
		return o.SourceEvidenceRequest.ExpectedHTTPStatus, o.SourceEvidenceRequest.ExpectedErrorCode, true
	}
	return 0, "", false
}

// UnavailableSourceOracle mirrors contracts/v1 UnavailableSource, matched against
// ContextPacket.Coverage.SourcesUnavailable.
type UnavailableSourceOracle struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// EvidenceEntityOracle names one entity a required/forbidden evidence entry must (not) be
// reachable as, per README.md#evidence-ref-id-matching. ContentHash, when set, is compared
// directly against the matching expanded-evidence document's contracts/v1
// EvidenceRef.ContentDigest (the wire's own stable content-identity field -- this tool does
// not recompute a digest itself, since it has no authority over what algorithm/scope the
// real digest covers); no landed oracle currently sets it, but the field is kept for the
// "stable content hash matches when the oracle pins one" requirement.
type EvidenceEntityOracle struct {
	QueryID     string `json:"query_id,omitempty"`
	EntityType  string `json:"entity_type"`
	EntityID    string `json:"entity_id"`
	Reason      string `json:"reason,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

// RequiredFindingOracle mirrors one entry of the oracle's required_findings array exactly as
// scripts/e2e/fullstack-opencode.sh:write_model_plan consumes it.
type RequiredFindingOracle struct {
	ClaimID        string           `json:"claim_id"`
	ClaimKind      string           `json:"claim_kind,omitempty"`
	MustCiteEntity *EntityRefOracle `json:"must_cite_entity,omitempty"`
}

// ForbiddenClaimOracle names an entity no finding may cite. A wildcard ("*"/"*",
// forbidden_entity present but not resolvable to a real entity) is documentation-only,
// paired with FindingsMustBeEmpty -- entries like that are skipped by entity matching and
// covered entirely by the findings-empty check instead.
type ForbiddenClaimOracle struct {
	Reason          string           `json:"reason,omitempty"`
	ForbiddenEntity *EntityRefOracle `json:"forbidden_entity,omitempty"`
}

func (c ForbiddenClaimOracle) isWildcard() bool {
	return c.ForbiddenEntity == nil || c.ForbiddenEntity.EntityType == "*" || c.ForbiddenEntity.EntityID == "*"
}

// EntityRefOracle names an entity a citation must (not) actually point at.
type EntityRefOracle struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}

const oracleSchemaVersion = "fullstack_task_oracle.v1"

// hasAnyAssertion reports whether the oracle declares at least one field this tool actually
// checks against. An oracle with none would make assert-run pass vacuously -- every layer's
// oracle-driven checks are conditional on the corresponding field being set, so a caller could
// hand a well-formed-but-empty oracle and receive a silent "everything passed" (Codex finding
// 6). loadOracle refuses to load one.
func (o Oracle) hasAnyAssertion() bool {
	if o.ExpectedPacketStatus != nil || o.ExpectedScopeResolution != nil || o.ExpectedAsOf != nil {
		return true
	}
	if len(o.ExpectedUnavailableSources) > 0 || len(o.RequiredEvidence) > 0 || len(o.ForbiddenEvidence) > 0 {
		return true
	}
	if len(o.RequiredPacketCategories) > 0 || len(o.RequiredRuleIDs) > 0 || len(o.RequiredChecks) > 0 {
		return true
	}
	if o.MinExpandableEvidence > 0 || len(o.RequiredFindings) > 0 || len(o.ForbiddenClaims) > 0 || o.FindingsMustBeEmpty {
		return true
	}
	if _, _, ok := o.httpExpectation(); ok {
		return true
	}
	if len(o.RequiredCapabilityTools) > 0 || len(o.ForbiddenCapabilityTools) > 0 || o.EpisodeWrite != nil ||
		len(o.RequiredSchemaVersions) > 0 || len(o.ExpectedMCPTools) > 0 {
		return true
	}
	return false
}

func loadOracle(path string) (Oracle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Oracle{}, fmt.Errorf("read oracle %s: %w", path, err)
	}
	var oracle Oracle
	if err := json.Unmarshal(data, &oracle); err != nil {
		return Oracle{}, fmt.Errorf("decode oracle %s: %w", path, err)
	}
	if oracle.SchemaVersion != oracleSchemaVersion {
		return Oracle{}, fmt.Errorf("oracle %s: schema_version must be %q, got %q", path, oracleSchemaVersion, oracle.SchemaVersion)
	}
	if oracle.TaskID == "" {
		return Oracle{}, fmt.Errorf("oracle %s: task_id is required", path)
	}
	if !oracle.hasAnyAssertion() {
		return Oracle{}, fmt.Errorf("oracle %s: declares zero assertions (no expected status/scope/evidence/findings/checks/denial fields set) -- assert-run would pass this task vacuously", path)
	}
	return oracle, nil
}
