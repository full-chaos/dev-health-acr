package contractcheck

import (
	"fmt"
	"path/filepath"
	"reflect"
)

// mcpResponseDefsSync declares, for one self-contained MCP response schema,
// that its $defs[defKey] entry must stay byte-for-byte structurally
// synchronized with canonicalFile (minus $schema/$id, with any cross-file
// $ref inside canonicalFile rewritten per refRewrites to the local
// #/$defs/<key> pointer an offline client resolves without fetching a
// sibling schema file). See contracts/jsonschema/v1/mcp_context_for_task_response.v1.schema.json
// and mcp_source_evidence_response.v1.schema.json's own $defs for the
// embedded copies this keeps honest.
type mcpResponseDefsSync struct {
	responseFile  string
	defKey        string
	canonicalFile string
	refRewrites   map[string]string
	// structuredRoot marks the entry the response schema's own
	// "properties.structured.$ref" must point at (via "#/$defs/<defKey>"),
	// as opposed to a nested $defs entry (e.g. context_packet_item.v1)
	// that is only ever reached indirectly through the root entry.
	structuredRoot bool
}

// Context Fabric answer surface (CHAOS-3746). The three canonical
// documents below compose by $ref across files, so embedding them into a
// self-contained MCP response means rewriting every cross-file and
// document-root pointer to the local $defs location an offline client can
// actually resolve. Each map is declared once and shared by both response
// schemas that embed the same document, so the two copies cannot drift
// from each other.

// contextFabricCommonDefsRewrites relocates context_fabric_common.v1's own
// document-root pointers. Once the document is embedded at
// #/$defs/context_fabric_common.v1, its internal "#/$defs/SubjectRef"
// pointers would otherwise resolve against the WRAPPER's root, where
// nothing of that name exists.
var contextFabricCommonDefsRewrites = map[string]string{
	"#/$defs/AuthorizationScope":   "#/$defs/context_fabric_common.v1/$defs/AuthorizationScope",
	"#/$defs/CohortExclusion":      "#/$defs/context_fabric_common.v1/$defs/CohortExclusion",
	"#/$defs/CohortMember":         "#/$defs/context_fabric_common.v1/$defs/CohortMember",
	"#/$defs/CommitDecisionDigest": "#/$defs/context_fabric_common.v1/$defs/CommitDecisionDigest",
	"#/$defs/FactRequirement":      "#/$defs/context_fabric_common.v1/$defs/FactRequirement",
	"#/$defs/RelationshipEdge":     "#/$defs/context_fabric_common.v1/$defs/RelationshipEdge",
	"#/$defs/ScalarValue":          "#/$defs/context_fabric_common.v1/$defs/ScalarValue",
	"#/$defs/SubjectCandidate":     "#/$defs/context_fabric_common.v1/$defs/SubjectCandidate",
	"#/$defs/SubjectHint":          "#/$defs/context_fabric_common.v1/$defs/SubjectHint",
	"#/$defs/SubjectRef":           "#/$defs/context_fabric_common.v1/$defs/SubjectRef",
	"#/$defs/TimeContext":          "#/$defs/context_fabric_common.v1/$defs/TimeContext",
	// CHAOS-3900 W1: five more locally-$ref'd defs (TimeContext.evidence_window
	// -> RequestedEvidenceWindow; RequestedEvidenceWindow/EffectiveEvidenceWindow/
	// WindowOption -> RelativeWindowID; WindowClarification -> WindowOption).
	"#/$defs/RelativeWindowID":        "#/$defs/context_fabric_common.v1/$defs/RelativeWindowID",
	"#/$defs/RequestedEvidenceWindow": "#/$defs/context_fabric_common.v1/$defs/RequestedEvidenceWindow",
	"#/$defs/EffectiveEvidenceWindow": "#/$defs/context_fabric_common.v1/$defs/EffectiveEvidenceWindow",
	"#/$defs/WindowOption":            "#/$defs/context_fabric_common.v1/$defs/WindowOption",
	"#/$defs/WindowClarification":     "#/$defs/context_fabric_common.v1/$defs/WindowClarification",
	// CHAOS-3900 P1: locally-$ref'd defs KindOption/AnchorOption/
	// HandleOption/AcceptedGrammar/StructureNeeds/ConfirmedStructureEntry/
	// StructureOfferSnapshotEntry pull in (SubjectKind is new; it also
	// replaces what SubjectRef/SubjectCandidate/etc. still inline rather
	// than $ref).
	"#/$defs/SubjectKind":          "#/$defs/context_fabric_common.v1/$defs/SubjectKind",
	"#/$defs/StructureOfferSource": "#/$defs/context_fabric_common.v1/$defs/StructureOfferSource",
	"#/$defs/StructureNeedKind":    "#/$defs/context_fabric_common.v1/$defs/StructureNeedKind",
	"#/$defs/KindOption":           "#/$defs/context_fabric_common.v1/$defs/KindOption",
	"#/$defs/AnchorOption":         "#/$defs/context_fabric_common.v1/$defs/AnchorOption",
	// CHAOS-4012: subject_candidate's own new common $defs entry.
	"#/$defs/CandidateOption": "#/$defs/context_fabric_common.v1/$defs/CandidateOption",
	// CHAOS-4042: the anchor membership-verify semantic major's own two new
	// common $defs (additive; the v1 entries above are unchanged).
	"#/$defs/AnchorOptionV2":       "#/$defs/context_fabric_common.v1/$defs/AnchorOptionV2",
	"#/$defs/StructureNeedsV2":     "#/$defs/context_fabric_common.v1/$defs/StructureNeedsV2",
	"#/$defs/HandleOption":         "#/$defs/context_fabric_common.v1/$defs/HandleOption",
	"#/$defs/AcceptedGrammar":      "#/$defs/context_fabric_common.v1/$defs/AcceptedGrammar",
	"#/$defs/StructureSource":      "#/$defs/context_fabric_common.v1/$defs/StructureSource",
	"#/$defs/StructureProvenance":  "#/$defs/context_fabric_common.v1/$defs/StructureProvenance",
	"#/$defs/StructureDisposition": "#/$defs/context_fabric_common.v1/$defs/StructureDisposition",
	// CHAOS-3478/CHAOS-3813: SubjectResolution's own new locally-$ref'd
	// disposition pair (prior_subject_receipt_dispositions).
	"#/$defs/PriorSubjectReceiptDisposition":      "#/$defs/context_fabric_common.v1/$defs/PriorSubjectReceiptDisposition",
	"#/$defs/PriorSubjectReceiptDispositionEntry": "#/$defs/context_fabric_common.v1/$defs/PriorSubjectReceiptDispositionEntry",
	// CHAOS-3900 W2: InvestigationOptions.window_confirmation_mode's own
	// local ref, embedded here too even though the response schema never
	// reaches InvestigationOptions itself -- the WHOLE common.v1 document
	// is embedded verbatim, so every one of its own internal refs must
	// resolve correctly within the embedded copy, reachable or not.
	"#/$defs/WindowConfirmationMode": "#/$defs/context_fabric_common.v1/$defs/WindowConfirmationMode",
}

// contextFabricResultDefsRewrites relocates the cross-file pointers
// context_fabric_investigation_result.v1 makes into
// context_fabric_common.v1.
var contextFabricResultDefsRewrites = map[string]string{
	"context_fabric_common.v1.schema.json#/$defs/ClaimedFact":         "#/$defs/context_fabric_common.v1/$defs/ClaimedFact",
	"context_fabric_common.v1.schema.json#/$defs/Cohort":              "#/$defs/context_fabric_common.v1/$defs/Cohort",
	"context_fabric_common.v1.schema.json#/$defs/Coverage":            "#/$defs/context_fabric_common.v1/$defs/Coverage",
	"context_fabric_common.v1.schema.json#/$defs/DriverJudgment":      "#/$defs/context_fabric_common.v1/$defs/DriverJudgment",
	"context_fabric_common.v1.schema.json#/$defs/Finding":             "#/$defs/context_fabric_common.v1/$defs/Finding",
	"context_fabric_common.v1.schema.json#/$defs/InterpretedQuestion": "#/$defs/context_fabric_common.v1/$defs/InterpretedQuestion",
	"context_fabric_common.v1.schema.json#/$defs/RelationshipPath":    "#/$defs/context_fabric_common.v1/$defs/RelationshipPath",
	"context_fabric_common.v1.schema.json#/$defs/SubjectResolution":   "#/$defs/context_fabric_common.v1/$defs/SubjectResolution",
	"context_fabric_common.v1.schema.json#/$defs/VersionSet":          "#/$defs/context_fabric_common.v1/$defs/VersionSet",
	// CHAOS-3900 W1: two more cross-file pointers the result schema makes
	// into context_fabric_common.v1.
	"context_fabric_common.v1.schema.json#/$defs/EffectiveEvidenceWindow": "#/$defs/context_fabric_common.v1/$defs/EffectiveEvidenceWindow",
	"context_fabric_common.v1.schema.json#/$defs/WindowClarification":     "#/$defs/context_fabric_common.v1/$defs/WindowClarification",
	// CHAOS-3900 P1: three more cross-file pointers the result schema
	// makes into context_fabric_common.v1.
	"context_fabric_common.v1.schema.json#/$defs/StructureNeeds":              "#/$defs/context_fabric_common.v1/$defs/StructureNeeds",
	"context_fabric_common.v1.schema.json#/$defs/ConfirmedStructureEntry":     "#/$defs/context_fabric_common.v1/$defs/ConfirmedStructureEntry",
	"context_fabric_common.v1.schema.json#/$defs/StructureOfferSnapshotEntry": "#/$defs/context_fabric_common.v1/$defs/StructureOfferSnapshotEntry",
}

// contextFabricProjectionDefsRewrites relocates
// context_fabric_answer_projection.v1's own document-root pointers. The
// projection schema is self-contained as a standalone file, so all of its
// pointers are internal.
var contextFabricProjectionDefsRewrites = map[string]string{
	"#/$defs/BoundSubjectReceipt":    "#/$defs/context_fabric_answer_projection.v1/$defs/BoundSubjectReceipt",
	"#/$defs/ProjectedCandidate":     "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedCandidate",
	"#/$defs/ProjectedClarification": "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedClarification",
	"#/$defs/ProjectedCohort":        "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedCohort",
	"#/$defs/ProjectedCohortMember":  "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedCohortMember",
	"#/$defs/ProjectedCoverage":      "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedCoverage",
	"#/$defs/ProjectedDriver":        "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedDriver",
	"#/$defs/ProjectedFact":          "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectedFact",
	"#/$defs/ProjectionBudget":       "#/$defs/context_fabric_answer_projection.v1/$defs/ProjectionBudget",
	"#/$defs/ScalarValue":            "#/$defs/context_fabric_answer_projection.v1/$defs/ScalarValue",
	"#/$defs/SubjectRef":             "#/$defs/context_fabric_answer_projection.v1/$defs/SubjectRef",
	"#/$defs/TemporalLabel":          "#/$defs/context_fabric_answer_projection.v1/$defs/TemporalLabel",
	"#/$defs/TimeContext":            "#/$defs/context_fabric_answer_projection.v1/$defs/TimeContext",
	"#/$defs/VersionSet":             "#/$defs/context_fabric_answer_projection.v1/$defs/VersionSet",
	// CHAOS-3900 W1: TimeContext (reused into this file's own $defs, see
	// answer_projection_closure_test.go's pinned reused-shapes set) now
	// locally refs these two new defs.
	"#/$defs/RequestedEvidenceWindow": "#/$defs/context_fabric_answer_projection.v1/$defs/RequestedEvidenceWindow",
	"#/$defs/RelativeWindowID":        "#/$defs/context_fabric_answer_projection.v1/$defs/RelativeWindowID",
	// CHAOS-3972 P3+W2: the projection gained its own self-contained
	// copies of the window/structure disclosure defs (design brief
	// §2.3/§4) -- mirrors contextFabricCommonDefsRewrites' own entries for
	// these SAME defs, rewritten to this file's own local
	// context_fabric_answer_projection.v1 root instead.
	"#/$defs/EffectiveEvidenceWindow": "#/$defs/context_fabric_answer_projection.v1/$defs/EffectiveEvidenceWindow",
	"#/$defs/WindowOption":            "#/$defs/context_fabric_answer_projection.v1/$defs/WindowOption",
	"#/$defs/WindowClarification":     "#/$defs/context_fabric_answer_projection.v1/$defs/WindowClarification",
	"#/$defs/SubjectKind":             "#/$defs/context_fabric_answer_projection.v1/$defs/SubjectKind",
	"#/$defs/StructureOfferSource":    "#/$defs/context_fabric_answer_projection.v1/$defs/StructureOfferSource",
	"#/$defs/StructureNeedKind":       "#/$defs/context_fabric_answer_projection.v1/$defs/StructureNeedKind",
	"#/$defs/KindOption":              "#/$defs/context_fabric_answer_projection.v1/$defs/KindOption",
	"#/$defs/AnchorOption":            "#/$defs/context_fabric_answer_projection.v1/$defs/AnchorOption",
	// CHAOS-4012: subject_candidate's own new projection-local $defs entry.
	"#/$defs/CandidateOption":         "#/$defs/context_fabric_answer_projection.v1/$defs/CandidateOption",
	"#/$defs/HandleOption":            "#/$defs/context_fabric_answer_projection.v1/$defs/HandleOption",
	"#/$defs/AcceptedGrammar":         "#/$defs/context_fabric_answer_projection.v1/$defs/AcceptedGrammar",
	"#/$defs/StructureNeeds":          "#/$defs/context_fabric_answer_projection.v1/$defs/StructureNeeds",
	"#/$defs/StructureSource":         "#/$defs/context_fabric_answer_projection.v1/$defs/StructureSource",
	"#/$defs/StructureProvenance":     "#/$defs/context_fabric_answer_projection.v1/$defs/StructureProvenance",
	"#/$defs/StructureDisposition":    "#/$defs/context_fabric_answer_projection.v1/$defs/StructureDisposition",
	"#/$defs/ConfirmedStructureEntry": "#/$defs/context_fabric_answer_projection.v1/$defs/ConfirmedStructureEntry",
	// CHAOS-3478/CHAOS-3813: the projection's own new locally-$ref'd
	// disposition pair (prior_subject_receipt_dispositions).
	"#/$defs/PriorSubjectReceiptDisposition":      "#/$defs/context_fabric_answer_projection.v1/$defs/PriorSubjectReceiptDisposition",
	"#/$defs/PriorSubjectReceiptDispositionEntry": "#/$defs/context_fabric_answer_projection.v1/$defs/PriorSubjectReceiptDispositionEntry",
}

var mcpResponseDefsSyncs = []mcpResponseDefsSync{
	{
		responseFile:   "mcp_context_for_task_response.v1.schema.json",
		defKey:         "context_packet.v1",
		canonicalFile:  "context_packet.v1.schema.json",
		refRewrites:    map[string]string{"context_packet_item.v1.schema.json": "#/$defs/context_packet_item.v1"},
		structuredRoot: true,
	},
	{
		responseFile:  "mcp_context_for_task_response.v1.schema.json",
		defKey:        "context_packet_item.v1",
		canonicalFile: "context_packet_item.v1.schema.json",
	},
	{
		responseFile:  "mcp_context_for_task_response.v1.schema.json",
		defKey:        "evidence_ref.v1",
		canonicalFile: "evidence_ref.v1.schema.json",
	},
	{
		responseFile:   "mcp_source_evidence_response.v1.schema.json",
		defKey:         "expanded_evidence.v1",
		canonicalFile:  "expanded_evidence.v1.schema.json",
		refRewrites:    map[string]string{"evidence_ref.v1.schema.json": "#/$defs/evidence_ref.v1"},
		structuredRoot: true,
	},
	{
		responseFile:  "mcp_source_evidence_response.v1.schema.json",
		defKey:        "evidence_ref.v1",
		canonicalFile: "evidence_ref.v1.schema.json",
	},
	{
		responseFile:   "mcp_investigate_question_response.v1.schema.json",
		defKey:         "context_fabric_answer_projection.v1",
		canonicalFile:  "context_fabric_answer_projection.v1.schema.json",
		refRewrites:    contextFabricProjectionDefsRewrites,
		structuredRoot: true,
	},
	{
		responseFile:  "mcp_investigate_question_response.v1.schema.json",
		defKey:        "context_fabric_investigation_result.v1",
		canonicalFile: "context_fabric_investigation_result.v1.schema.json",
		refRewrites:   contextFabricResultDefsRewrites,
	},
	{
		responseFile:  "mcp_investigate_question_response.v1.schema.json",
		defKey:        "context_fabric_common.v1",
		canonicalFile: "context_fabric_common.v1.schema.json",
		refRewrites:   contextFabricCommonDefsRewrites,
	},
	{
		responseFile:   "mcp_investigation_result_response.v1.schema.json",
		defKey:         "context_fabric_investigation_result.v1",
		canonicalFile:  "context_fabric_investigation_result.v1.schema.json",
		refRewrites:    contextFabricResultDefsRewrites,
		structuredRoot: true,
	},
	{
		responseFile:  "mcp_investigation_result_response.v1.schema.json",
		defKey:        "context_fabric_common.v1",
		canonicalFile: "context_fabric_common.v1.schema.json",
		refRewrites:   contextFabricCommonDefsRewrites,
	},
}

// validateMCPSchemaDefsSync proves every self-contained MCP response
// schema's embedded $defs entry is structurally identical to its canonical
// source file, so an offline client holding only the single response
// schema file can still fully resolve "structured" without fetching
// context_packet.v1.schema.json / context_packet_item.v1.schema.json /
// expanded_evidence.v1.schema.json / evidence_ref.v1.schema.json
// separately. If a canonical schema changes without its embedded $defs
// copy being regenerated, this fails closed rather than letting the two
// silently drift apart.
func (c *repositoryCheck) validateMCPSchemaDefsSync() error {
	directory := filepath.Join(c.root, "contracts", "jsonschema", "v1")
	for _, sync := range mcpResponseDefsSyncs {
		response, ok := c.registry.byName[sync.responseFile]
		if !ok {
			return fmt.Errorf("MCP response schema %s not loaded", sync.responseFile)
		}
		defs, ok := response["$defs"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: missing $defs", sync.responseFile)
		}
		actual, ok := defs[sync.defKey].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: $defs.%s is missing or not an object", sync.responseFile, sync.defKey)
		}
		canonicalValue, err := decodeJSONFile(filepath.Join(directory, sync.canonicalFile))
		if err != nil {
			return fmt.Errorf("decode canonical %s: %w", sync.canonicalFile, err)
		}
		canonical, ok := canonicalValue.(map[string]any)
		if !ok {
			return fmt.Errorf("canonical %s must be an object", sync.canonicalFile)
		}
		expected := localizeMCPSchemaRefs(stripSchemaIdentity(canonical), sync.refRewrites)
		if !reflect.DeepEqual(expected, actual) {
			return fmt.Errorf("%s: $defs.%s has drifted from canonical %s; regenerate the embedded copy", sync.responseFile, sync.defKey, sync.canonicalFile)
		}
		if sync.structuredRoot {
			if err := requireStructuredRefsDefs(response, sync.defKey); err != nil {
				return fmt.Errorf("%s: %w", sync.responseFile, err)
			}
		}
	}
	c.ok("MCP response schemas are self-contained (%d embedded $defs in sync)", len(mcpResponseDefsSyncs))
	return nil
}

// requireStructuredRefsDefs proves response's "properties.structured" is a
// local "#/$defs/<defKey>" pointer rather than an external filename ref:
// the byte-for-byte $defs sync check above only proves the embedded copy
// is correct, not that the schema actually resolves "structured" through
// it, so this closes that gap and is what makes the document genuinely
// offline-resolvable end to end.
func requireStructuredRefsDefs(response map[string]any, defKey string) error {
	properties, ok := response["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing properties")
	}
	structured, ok := properties["structured"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing properties.structured")
	}
	ref, _ := structured["$ref"].(string)
	want := "#/$defs/" + defKey
	if ref != want {
		return fmt.Errorf("properties.structured.$ref is %q, want %q", ref, want)
	}
	return nil
}

// stripSchemaIdentity drops the top-level document-identity keywords that
// only make sense for a standalone schema file, never for a $defs entry
// embedded inside another document.
func stripSchemaIdentity(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		if key == "$schema" || key == "$id" {
			continue
		}
		out[key] = value
	}
	return out
}

// localizeMCPSchemaRefs recursively rewrites every "$ref" value found in
// node that matches a key in refRewrites, leaving every other value
// (including non-matching $refs, already-local "#/..." pointers, and
// unrelated keywords) untouched.
func localizeMCPSchemaRefs(node any, refRewrites map[string]string) any {
	switch value := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			if key == "$ref" {
				if ref, ok := child.(string); ok {
					if rewritten, exists := refRewrites[ref]; exists {
						out[key] = rewritten
						continue
					}
				}
			}
			out[key] = localizeMCPSchemaRefs(child, refRewrites)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = localizeMCPSchemaRefs(item, refRewrites)
		}
		return out
	default:
		return value
	}
}
