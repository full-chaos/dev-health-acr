package v1

import (
	"encoding/json"
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// This file anchors the hand-maintained JSON Schemas to the Go wire structs.
//
// CHAOS-4825. The contract gate validates artifacts against EACH OTHER --
// canonical schema against its embedded MCP $defs copy, published examples
// against their schema, the binary-embedded copy against canonical -- and
// every one of those links works. The Go struct is not one of the artifacts
// contractcheck reads, so the whole parity chain is anchored to nothing: a
// Go wire field can ship with no schema property while `make contract-write`
// and `make contract-test` both report OK for every line.
//
// That is not cosmetic. Every Context Fabric document sets
// `additionalProperties: false`, so a producer field with no published
// property makes every real document carrying it INVALID for a
// schema-validating consumer, while every Go-side test stays green.
//
// This is a CHECK, not a generator. The hand-maintained schemas stay the
// source of truth (chris, 2026-08-28: no schema generation during the
// migration). The check only asserts the two sides agree, and says exactly
// where they do not.
//
// What "agree" means here, bounded: the set of JSON KEYS a Go type emits
// equals the set of properties its schema publishes, and closed
// vocabularies match their published enums. It does NOT compare the wire
// TYPE of each value against the schema's "type"/"format" -- that is a
// larger assertion, tracked as its own ticket. The one tag option that
// changes a wire type without changing a key, `,string`, fails closed here
// rather than passing silently.
//
// Anchoring the CANONICAL schema is sufficient by the ticket's own ruling:
// the existing artifact-to-artifact tests (TestEmbeddedSchemasMatchCanonical
// Source in internal/mcp, validateMCPSchemaDefsSync in contractcheck) then
// propagate correctness to the embedded $defs copy and to the copy compiled
// into the acr-mcp binary. The enumeration of copies is not what needs
// fixing; the anchor is.
//
// Coverage is quantified from the SCHEMA side, the same direction
// TestSchemaAndGoBoundsAgree uses and for the same reason: the failure mode
// being closed is a published document nobody remembered to bind to Go.
// Every document, and every $def inside it, must resolve to exactly one Go
// type or carry an explicit exemption with a reason. A new schema file, or a
// new $def, that nobody has bound fails the build rather than being silently
// skipped.

// contractsV1ImportPath is the package this anchor type-checks.
const contractsV1ImportPath = "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// schemaDir is the canonical schema directory, relative to the module root.
var schemaDirParts = []string{"contracts", "jsonschema", "v1"}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// schemaRootTypes binds a canonical schema document to the Go type served at
// its ROOT.
//
// The value is a Go type name resolved through the package scope
// (types.Scope.Lookup), so the binding is by OBJECT IDENTITY, not by a
// name-string transform: renaming or deleting the type fails this test
// loudly instead of silently unbinding the document. Same mechanism, and the
// same reason, as lane-4782's sanctioned-symbol list.
var schemaRootTypes = map[string]string{
	"acr_client_credential.v1.schema.json":                         "ClientCredential",
	"agent_episode.v1.schema.json":                                 "AgentEpisode",
	"agent_episode_create.v1.schema.json":                          "AgentEpisodeCreate",
	"capabilities.v1.schema.json":                                  "Capabilities",
	"context_fabric_answer_projection.v1.schema.json":              "ContextFabricAnswerProjection",
	"context_fabric_investigation_request.v1.schema.json":          "ContextFabricInvestigationRequest",
	"context_fabric_investigation_result.v1.schema.json":           "ContextFabricInvestigationResult",
	"context_fabric_org_model_config.v1.schema.json":               "ContextFabricOrgModelConfig",
	"context_fabric_org_model_config_write_request.v1.schema.json": "ContextFabricOrgModelConfigWriteRequest",
	"context_fabric_projection_batch.v1.schema.json":               "ContextFabricProjectionBatch",
	"context_packet.v1.schema.json":                                "ContextPacket",
	"context_packet_item.v1.schema.json":                           "ContextPacketItem",
	"context_packet_request.v1.schema.json":                        "ContextPacketRequest",
	"credential_revoke_request.v1.schema.json":                     "CredentialRevokeRequest",
	"credential_revoke_response.v1.schema.json":                    "CredentialRevokeResponse",
	"credential_rotate_request.v1.schema.json":                     "CredentialRotateRequest",
	"credential_rotate_response.v1.schema.json":                    "CredentialRotateResponse",
	"device_approval_preview_request.v1.schema.json":               "DeviceApprovalPreviewRequest",
	"device_approval_preview_response.v1.schema.json":              "DeviceApprovalPreviewResponse",
	"device_approval_request.v1.schema.json":                       "DeviceApprovalRequest",
	"device_approval_response.v1.schema.json":                      "DeviceApprovalResponse",
	"device_authorization_request.v1.schema.json":                  "DeviceAuthorizationRequest",
	"device_authorization_response.v1.schema.json":                 "DeviceAuthorizationResponse",
	"device_token_request.v1.schema.json":                          "DeviceTokenRequest",
	"device_token_response.v1.schema.json":                         "DeviceTokenResponse",
	"error.v1.schema.json":                                         "ErrorEnvelope",
	"evidence_ref.v1.schema.json":                                  "EvidenceRef",
	"expanded_evidence.v1.schema.json":                             "ExpandedEvidence",
	"mcp_context_for_task_request.v1.schema.json":                  "MCPContextForTaskRequest",
	"mcp_context_for_task_response.v1.schema.json":                 "MCPContextForTaskResponse",
	"mcp_investigate_question_request.v1.schema.json":              "MCPInvestigateQuestionRequest",
	"mcp_investigate_question_response.v1.schema.json":             "MCPInvestigateQuestionResponse",
	"mcp_investigation_result_request.v1.schema.json":              "MCPInvestigationResultRequest",
	"mcp_investigation_result_response.v1.schema.json":             "MCPInvestigationResultResponse",
	"mcp_record_episode_request.v1.schema.json":                    "MCPRecordEpisodeRequest",
	"mcp_record_episode_response.v1.schema.json":                   "MCPRecordEpisodeResponse",
	"mcp_source_evidence_request.v1.schema.json":                   "MCPSourceEvidenceRequest",
	"mcp_source_evidence_response.v1.schema.json":                  "MCPSourceEvidenceResponse",
	"oauth_device_error.v1.schema.json":                            "OAuthDeviceErrorResponse",
	"oauth_token_exchange_error.v1.schema.json":                    "OAuthTokenExchangeErrorResponse",
	"token_exchange_response.v1.schema.json":                       "TokenExchangeResponse",

	// CHAOS-4042: v2's wire fields are IDENTICAL to v1's; only the meaning
	// of structure_needs.anchor_options differs (membership-verify rather
	// than unique-claimant). It is therefore served from the same Go type,
	// and binding it here is load-bearing rather than redundant: if v1 gains
	// a field and v2 is not updated alongside it, this catches the divergence
	// at the point it is introduced.
	"context_fabric_investigation_result.v2.schema.json": "ContextFabricInvestigationResult",
}

// schemaRootExemptions names documents with no Go root type, each with the
// reason. An entry is a claim that somebody looked and found the document is
// genuinely not served from a struct in this package -- it is not a place to
// park a disagreement.
var schemaRootExemptions = map[string]string{
	"context_fabric_common.v1.schema.json": "a pure $defs library with no root type of its own (the document has no top-level \"type\"); every shape it publishes is anchored through its $defs entries below, so exempting the ROOT removes nothing from coverage",
	"evaluation_demo.v1.schema.json":       "a demonstration artifact with no Go producer in this package -- nothing under internal/contracts/v1 marshals it, and contractcheck validates it purely as an example/schema pair (internal/contractcheck/run.go registers evaluation_demo.v1.json against it). With no producing struct there is no Go field set to anchor to.",
}

// schemaDefTypeOverrides binds one "<document>#<defName>" to a Go type name
// where the naming convention (defName, or ContextFabric+defName) does not
// resolve it.
var schemaDefTypeOverrides = map[string]string{
	// CHAOS-3900 P1: five per-namespace receipt defs that are, in the
	// schema's own words, "the same shape as BoundSubjectReceipt" with
	// receipt_id additionally constrained to a closed id prefix. One Go
	// type serves all of them; the property sets are identical and only the
	// pattern differs, so binding each to that type is exactly right and
	// makes a field added to the Go struct fail against all five at once.
	"context_fabric_common.v1.schema.json#AnchorBoundReceipt":               "ContextFabricBoundSubjectReceipt",
	"context_fabric_common.v1.schema.json#CandidateBoundReceipt":            "ContextFabricBoundSubjectReceipt",
	"context_fabric_common.v1.schema.json#HandleBoundReceipt":               "ContextFabricBoundSubjectReceipt",
	"context_fabric_common.v1.schema.json#KindBoundReceipt":                 "ContextFabricBoundSubjectReceipt",
	"context_fabric_common.v1.schema.json#WindowBoundReceipt":               "ContextFabricBoundSubjectReceipt",
	"mcp_investigate_question_request.v1.schema.json#AnchorBoundReceipt":    "ContextFabricBoundSubjectReceipt",
	"mcp_investigate_question_request.v1.schema.json#CandidateBoundReceipt": "ContextFabricBoundSubjectReceipt",
	"mcp_investigate_question_request.v1.schema.json#HandleBoundReceipt":    "ContextFabricBoundSubjectReceipt",
	"mcp_investigate_question_request.v1.schema.json#KindBoundReceipt":      "ContextFabricBoundSubjectReceipt",
	"mcp_investigate_question_request.v1.schema.json#WindowBoundReceipt":    "ContextFabricBoundSubjectReceipt",

	// CHAOS-4042: the v2 structure-needs shape carries the same eight
	// properties as ContextFabricStructureNeeds; only anchor_options'
	// SEMANTICS differ (membership-verify), which is a meaning change, not a
	// field-set change.
	"context_fabric_common.v1.schema.json#StructureNeedsV2": "ContextFabricStructureNeeds",

	// Two real collisions, where BOTH the bare name and the ContextFabric-
	// prefixed name exist as distinct Go types with DIFFERENT field sets.
	// The convention must never pick one silently, so the ambiguity check
	// forces the choice to be stated here. Resolved by comparing property
	// sets, not by preferring a prefix:
	//   Coverage       -- schema publishes {degraded_reasons, details,
	//                     partial, sources}; ContextFabricCoverage matches
	//                     exactly, while the bare Coverage carries
	//                     {sources_available, sources_considered,
	//                     sources_unavailable} and no details.
	//   RequestedScope -- schema publishes {project_ids, repository_slugs,
	//                     subject_hints, team_ids}; ContextFabricRequested
	//                     Scope matches exactly, while the bare
	//                     RequestedScope is the context-packet scope
	//                     {as_of, branch, commit_sha, files, task_ref,
	//                     time_window_days}.
	// Binding either to the bare type would have reported false drift on
	// every property of both shapes.
	"context_fabric_common.v1.schema.json#Coverage":       "ContextFabricCoverage",
	"context_fabric_common.v1.schema.json#RequestedScope": "ContextFabricRequestedScope",

	// Named for the wire concept rather than the Go type in each case.
	"context_fabric_common.v1.schema.json#PriorSubjectReceiptDispositionEntry":            "ContextFabricPriorSubjectReceiptEntry",
	"context_fabric_answer_projection.v1.schema.json#PriorSubjectReceiptDispositionEntry": "ContextFabricPriorSubjectReceiptEntry",
	"context_fabric_answer_projection.v1.schema.json#ProjectedFactRow":                    "ContextFabricClaimedFactRow",
	"context_fabric_answer_projection.v1.schema.json#ProjectedFactTable":                  "ContextFabricClaimedFactTable",
	"mcp_investigate_question_request.v1.schema.json#InvestigationBudget":                 "MCPInvestigationBudget",
	"mcp_investigate_question_request.v1.schema.json#InvestigationScope":                  "MCPInvestigationScope",
	"mcp_context_for_task_response.v1.schema.json#mcp_federated_budget.v1":                "MCPFederatedBudget",
	"mcp_context_for_task_response.v1.schema.json#mcp_local_context.v1":                   "MCPLocalContext",
}

// schemaDefExemptions names "<document>#<defName>" entries with no Go type,
// each with the reason.
var schemaDefExemptions = map[string]string{
	// Composition and container nodes: these publish no property set of
	// their own, so there is no field set for a Go struct to match. Each was
	// read before being listed.
	"mcp_context_for_task_response.v1.schema.json#mcp_local_evidence_ref.v1":   "an allOf composition node with no properties of its own -- it constrains the referenced evidence-ref shape rather than declaring a new one, so there is no field set to anchor",
	"mcp_context_for_task_response.v1.schema.json#mcp_local_metadata.v1":       "an open string map (additionalProperties + not), not a struct: it has no fixed property set, and Go models it as a map rather than named fields",
	"mcp_context_for_task_response.v1.schema.json#mcp_local_metadata_value.v1": "an anyOf union of scalar types, not an object -- no properties, so no field set to anchor",

	// evaluation_demo's own $defs inherit the document's exemption above:
	// the document has no Go producer, so neither do its sub-objects.
	"evaluation_demo.v1.schema.json#cost":    "sub-object of evaluation_demo.v1, which has no Go producer (see schemaRootExemptions)",
	"evaluation_demo.v1.schema.json#metrics": "sub-object of evaluation_demo.v1, which has no Go producer (see schemaRootExemptions)",
	"evaluation_demo.v1.schema.json#ratio":   "sub-object of evaluation_demo.v1, which has no Go producer (see schemaRootExemptions)",
	"evaluation_demo.v1.schema.json#surface": "sub-object of evaluation_demo.v1, which has no Go producer (see schemaRootExemptions)",
}

// embeddedDocumentDefs: a $def whose name is another canonical document's
// stem is that document's shape, localized into this file so the MCP
// response schema stays self-contained. It binds to the SAME Go type as that
// document's root -- derived, never hand-listed, so a new embedded copy is
// bound automatically and a renamed document fails loudly.
func embeddedDocumentRootType(defName string) (string, bool) {
	name, ok := schemaRootTypes[defName+".schema.json"]
	return name, ok
}

// deprecatedForConsumers is how a published property with no Go field is
// declared intentional: the property carries `"deprecated": true`, the
// standard JSON Schema 2020-12 keyword (already permitted by contractcheck's
// assertion profile -- internal/contractcheck/schema.go). No custom
// vocabulary is invented for this.
//
// Semantics: the producer has stopped emitting the property; consumers may
// still send or tolerate it. A NON-deprecated property with no Go field is a
// contract that promises something no producer can ever satisfy, and fails.
const deprecatedKeyword = "deprecated"

// ---------------------------------------------------------------------------
// enum narrowing
// ---------------------------------------------------------------------------

// enumNarrowing records a schema enum that is deliberately NARROWER than the
// Go type's closed vocabulary, for one specific field.
//
// The rule this expresses is per-FIELD, not per-type, because a named string
// type can be reachable from several fields whose producers can emit
// different subsets of it. ContextFabricNarrowingBasis is the live example:
// the vocabulary has four members, and ProjectionBudget's
// cohort_member_selection_basis admits exactly the two that
// SelectGroupCoverMembers can actually return (CHAOS-4809).
type enumNarrowing struct {
	// excluded lists the Go constant VALUES the schema deliberately omits.
	excluded []string
	// why is the reason, and must name what makes the exclusion true.
	why string
	// accepts is the PRODUCTION validator for this field. It exists so the
	// narrowing is PROVEN rather than declared: the test asserts the
	// validator REJECTS every excluded value. A narrowing entry whose
	// validator still accepts the excluded value is exactly the CHAOS-4809
	// R1-P3 defect -- a closed vocabulary wider than its producer -- and an
	// entry must not be able to paper over it.
	accepts func(string) bool
}

// vocabularyAdmitsZeroValue names closed vocabularies whose published enum
// legitimately admits the EMPTY STRING, mapped to the production validator
// that proves it.
//
// Go does not require a named constant for a type's zero value, so a
// vocabulary can have a meaningful "unset" member that appears on the wire
// and in the schema while no `const ... = ""` exists to enumerate. Treating
// every such enum member as drift would be a false positive -- the first run
// of this anchor produced exactly that for
// ContextFabricWindowConfirmationMode, whose validator accepts "" as "no
// mode requested" on a REQUEST document.
//
// As with enumNarrowings, the entry is PROVEN rather than declared: the
// validator must actually accept "". An entry whose validator rejects it is
// itself a defect, and is reported as one.
var vocabularyAdmitsZeroValue = map[string]func(string) bool{
	"ContextFabricWindowConfirmationMode": func(value string) bool {
		return ValidContextFabricWindowConfirmationMode(ContextFabricWindowConfirmationMode(value))
	},
}

// enumNarrowings is keyed by "<document>#<defName>.<propertyName>".
var enumNarrowings = map[string]enumNarrowing{
	"context_fabric_answer_projection.v1.schema.json#ProjectionBudget.cohort_member_selection_basis": {
		excluded: []string{"canonical_id_lexical", "attention_rank"},
		why:      "SelectGroupCoverMembers is the sole producer of this field and can only return the exact overlap-aware cover (within ContextFabricSetCoverGroupGuard) or the round-robin fallback beyond it. canonical_id_lexical is the FLAT narrowing's order, which by construction runs only where there is no group axis; attention_rank exists only after the fact read, later than any clamp. Admitting either would let a document claim a grouped clamp selected by an order with no code path (CHAOS-4809).",
		accepts: func(value string) bool {
			return ValidContextFabricCohortMemberSelectionBasis(ContextFabricNarrowingBasis(value))
		},
	},
}

// ---------------------------------------------------------------------------
// JSON key set, by encoding/json's own rules
// ---------------------------------------------------------------------------

// The hand-written encoding/json field-resolution model that used to live
// here is GONE. It was re-derived by hand and two review rounds found four
// separate defects in it (promotion from an unexported embedded struct,
// promotion from an empty tag name, invalid-tag-name truncation, and type
// aliases). The key set now comes from encoding/json itself -- see
// json_wire_key_oracle_test.go. Nothing in this file models what the
// encoder would do any more.

// ---------------------------------------------------------------------------
// Go closed vocabularies
// ---------------------------------------------------------------------------

// goVocabulary returns the closed vocabulary of a named string type: every
// package-level constant declared with that type.
//
// This is derived from the package scope by IDENTITY -- a constant added to
// the vocabulary appears here with no list to update, which is what makes
// mutation (iii) (a widened Go enum with an untouched schema) go red.
func goVocabulary(scope *types.Scope, named *types.Named) []string {
	var values []string
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		constObj, ok := obj.(*types.Const)
		if !ok {
			continue
		}
		constNamed, ok := constObj.Type().(*types.Named)
		if !ok || constNamed.Obj() != named.Obj() {
			continue
		}
		if constObj.Val().Kind() != constant.String {
			continue
		}
		values = append(values, constant.StringVal(constObj.Val()))
	}
	sort.Strings(values)
	return values
}

// ---------------------------------------------------------------------------
// Schema loading
// ---------------------------------------------------------------------------

type schemaSet struct {
	documents map[string]map[string]any
	dir       string
}

func loadCanonicalSchemas(t *testing.T, root string) schemaSet {
	t.Helper()
	dir := filepath.Join(append([]string{root}, schemaDirParts...)...)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read canonical schema dir %s: %v", dir, err)
	}
	set := schemaSet{documents: map[string]map[string]any{}, dir: dir}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".schema.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read schema %s: %v", name, err)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("decode schema %s: %v", name, err)
		}
		set.documents[name] = document
	}
	if len(set.documents) == 0 {
		t.Fatalf("no canonical schema documents found under %s -- the anchor would vacuously pass", dir)
	}
	return set
}

// objectNode resolves a schema node through $ref (local and cross-document)
// and returns the object it denotes.
func (s schemaSet) objectNode(document string, node map[string]any) (map[string]any, string, bool) {
	seen := 0
	current := node
	currentDoc := document
	for {
		ref, ok := current["$ref"].(string)
		if !ok {
			return current, currentDoc, true
		}
		seen++
		if seen > 16 {
			return nil, currentDoc, false
		}
		doc, pointer, found := strings.Cut(ref, "#")
		if doc != "" {
			currentDoc = filepath.Base(doc)
		}
		target, ok := s.documents[currentDoc]
		if !ok {
			return nil, currentDoc, false
		}
		if !found || pointer == "" || pointer == "/" {
			current = target
			continue
		}
		node, ok := resolvePointer(target, pointer)
		if !ok {
			return nil, currentDoc, false
		}
		current = node
	}
}

func resolvePointer(document map[string]any, pointer string) (map[string]any, bool) {
	var node any = document
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if token == "" {
			continue
		}
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[token]
		if !ok {
			return nil, false
		}
		node = next
	}
	object, ok := node.(map[string]any)
	return object, ok
}

func propertyKeys(node map[string]any) map[string]map[string]any {
	properties, ok := node["properties"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]map[string]any{}
	for key, value := range properties {
		object, ok := value.(map[string]any)
		if !ok {
			object = map[string]any{}
		}
		out[key] = object
	}
	return out
}

// ---------------------------------------------------------------------------
// Package loading
// ---------------------------------------------------------------------------

func moduleRootForParity(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("schema/Go field parity: could not resolve this test file's own path")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("schema/Go field parity: no go.mod found walking up from %s", thisFile)
		}
		dir = parent
	}
}

func loadContractsPackage(t *testing.T, root string) *packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Dir:  root,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, contractsV1ImportPath)
	if err != nil {
		t.Fatalf("packages.Load(%s): %v", contractsV1ImportPath, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("packages.Load(%s) reported %d package error(s) (printed above)", contractsV1ImportPath, n)
	}
	for _, p := range pkgs {
		if p.PkgPath == contractsV1ImportPath && p.Types != nil {
			return p
		}
	}
	t.Fatalf("packages.Load did not return usable type info for %s", contractsV1ImportPath)
	return nil
}

// resolveNamedType looks a type name up in the package scope, by identity.
func resolveNamedType(scope *types.Scope, name string) (*types.Named, bool) {
	obj := scope.Lookup(name)
	if obj == nil {
		return nil, false
	}
	typeName, ok := obj.(*types.TypeName)
	if !ok {
		return nil, false
	}
	named, ok := typeName.Type().(*types.Named)
	return named, ok
}

func strconvUnquote(quoted string) (string, error) {
	var out string
	if err := json.Unmarshal([]byte(quoted), &out); err != nil {
		return "", err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Binding
// ---------------------------------------------------------------------------

// binding is one resolved (schema object node) <-> (Go struct type) pair.
type binding struct {
	// label is "<document>#<defName>", or "<document>#" for a document root.
	label    string
	document string
	defName  string
	node     map[string]any
	named    *types.Named
}

// resolveBindings walks every canonical document and every $def inside it,
// and binds each to a Go type or records why it is unbound.
//
// A $def resolves by convention -- the def name, or ContextFabric + the def
// name -- with two loud failure modes rather than a silent one: if BOTH
// candidates exist the binding is AMBIGUOUS and must be stated explicitly in
// schemaDefTypeOverrides, and if NEITHER exists the def must be exempted with
// a reason. The convention is a shortcut for writing 87 obvious entries by
// hand, never a licence to skip one.
func resolveBindings(t *testing.T, schemas schemaSet, scope *types.Scope) ([]binding, []string) {
	t.Helper()
	var bindings []binding
	var unbound []string

	documents := make([]string, 0, len(schemas.documents))
	for name := range schemas.documents {
		documents = append(documents, name)
	}
	sort.Strings(documents)

	for _, document := range documents {
		root := schemas.documents[document]

		// Document root.
		rootLabel := document + "#"
		switch {
		case schemaRootTypes[document] != "":
			named, ok := resolveNamedType(scope, schemaRootTypes[document])
			if !ok {
				t.Errorf("schemaRootTypes[%q] names Go type %q, which does not exist in %s -- the type was renamed or removed and the binding silently unbound", document, schemaRootTypes[document], contractsV1ImportPath)
				break
			}
			bindings = append(bindings, binding{label: rootLabel, document: document, defName: "", node: root, named: named})
		case schemaRootExemptions[document] != "":
			// Exempted with a stated reason.
		default:
			unbound = append(unbound, rootLabel)
		}

		// $defs.
		defs, _ := root["$defs"].(map[string]any)
		defNames := make([]string, 0, len(defs))
		for name := range defs {
			defNames = append(defNames, name)
		}
		sort.Strings(defNames)

		for _, defName := range defNames {
			label := document + "#" + defName
			node, ok := defs[defName].(map[string]any)
			if !ok {
				continue
			}
			if schemaDefExemptions[label] != "" {
				continue
			}
			if override := schemaDefTypeOverrides[label]; override != "" {
				named, ok := resolveNamedType(scope, override)
				if !ok {
					t.Errorf("schemaDefTypeOverrides[%q] names Go type %q, which does not exist", label, override)
					continue
				}
				bindings = append(bindings, binding{label: label, document: document, defName: defName, node: node, named: named})
				continue
			}
			// A $def named after another canonical document is that
			// document's shape localized into this file (the MCP response
			// schemas embed their dependencies so they stay
			// self-contained). It is served by the same Go type, and a
			// document whose ROOT is exempt has nothing to bind here either.
			if rootType, ok := embeddedDocumentRootType(defName); ok {
				named, ok := resolveNamedType(scope, rootType)
				if !ok {
					t.Errorf("%s: embedded copy of document %q resolves to Go type %q, which does not exist", label, defName+".schema.json", rootType)
					continue
				}
				bindings = append(bindings, binding{label: label, document: document, defName: defName, node: node, named: named})
				continue
			}
			if schemaRootExemptions[defName+".schema.json"] != "" {
				continue
			}

			bare, bareOK := resolveNamedType(scope, defName)
			prefixed, prefixedOK := resolveNamedType(scope, "ContextFabric"+defName)
			switch {
			case bareOK && prefixedOK:
				t.Errorf("%s: AMBIGUOUS binding -- both %q and %q exist in %s. State the intended type in schemaDefTypeOverrides; the convention must never pick one silently.", label, defName, "ContextFabric"+defName, contractsV1ImportPath)
			case bareOK:
				bindings = append(bindings, binding{label: label, document: document, defName: defName, node: node, named: bare})
			case prefixedOK:
				bindings = append(bindings, binding{label: label, document: document, defName: defName, node: node, named: prefixed})
			default:
				unbound = append(unbound, label)
			}
		}
	}
	sort.Strings(unbound)
	return bindings, unbound
}

// TestEverySchemaDocumentAndDefIsBoundOrExempt is the coverage quantifier.
//
// It is the half of this anchor that keeps the other half honest. Without it
// the parity assertion below would be true of whatever happened to be
// registered, and a new schema document or a new $def would join the contract
// surface unchecked -- which is the same shape as the gate this ticket is
// about: a check whose name promises more than its quantifier delivers.
func TestEverySchemaDocumentAndDefIsBoundOrExempt(t *testing.T) {
	root := moduleRootForParity(t)
	schemas := loadCanonicalSchemas(t, root)
	pkg := loadContractsPackage(t, root)

	_, unbound := resolveBindings(t, schemas, pkg.Types.Scope())
	for _, label := range unbound {
		t.Errorf("%s is neither bound to a Go type nor exempted. Add a schemaRootTypes/schemaDefTypeOverrides entry naming the Go type it is served from, or a schemaRootExemptions/schemaDefExemptions entry stating why no Go type serves it.", label)
	}
}

// TestPublishedSchemaPropertiesMatchGoWireFields is the anchor itself.
//
// For every bound (schema object, Go struct) pair it asserts the published
// property set equals the wire key set the Go type actually emits, in BOTH
// directions:
//
//   - A Go wire key with no published property is the CHAOS-4825 defect. With
//     additionalProperties:false the producer emits a document that a
//     schema-validating consumer must reject, while every Go test stays
//     green and `make contract-test` reports OK.
//   - A published property with no Go wire key is a contract promising a
//     field no producer can ever emit -- unless the property is marked
//     `"deprecated": true`, which is exactly the statement "the producer has
//     stopped emitting this; consumers may still see it".
func TestPublishedSchemaPropertiesMatchGoWireFields(t *testing.T) {
	root := moduleRootForParity(t)
	schemas := loadCanonicalSchemas(t, root)
	pkg := loadContractsPackage(t, root)

	runtimeTypes := runtimeTypeIndex()
	populationIssues := 0

	bindings, _ := resolveBindings(t, schemas, pkg.Types.Scope())
	if len(bindings) == 0 {
		t.Fatal("no schema/Go bindings resolved -- the anchor would vacuously pass")
	}

	checked := 0
	for _, b := range bindings {
		node, _, ok := schemas.objectNode(b.document, b.node)
		if !ok {
			t.Errorf("%s: could not resolve the schema node (unresolvable $ref)", b.label)
			continue
		}
		// A $def bound to a named type whose underlying type is not a struct
		// is a closed VOCABULARY, not an object. It has no properties to
		// compare and is asserted by TestPublishedEnumsMatchGoVocabularies
		// instead. Splitting here rather than exempting keeps both kinds
		// inside the same total coverage quantifier.
		if _, isStruct := b.named.Underlying().(*types.Struct); !isStruct {
			continue
		}

		properties := propertyKeys(node)
		if properties == nil {
			// A bound node with no properties at all is either a composition
			// node that slipped into the registry or a schema that lost its
			// properties block. Either way the binding claims a field set
			// that is not there.
			t.Errorf("%s is bound to Go type %s but publishes no \"properties\" at all -- either the binding is wrong or the schema lost its property set", b.label, b.named.Obj().Name())
			continue
		}

		rt, ok := runtimeTypes[b.named.Obj().Name()]
		if !ok {
			t.Errorf("%s is bound to Go type %s, which is not reachable from any document root in contractRootExemplars -- the oracle cannot build a value for it, so this pair is UNCHECKED. Add the document root that reaches it, or exempt the $def with a reason.", b.label, b.named.Obj().Name())
			continue
		}
		keys, issues, err := wireKeysOf(rt)
		if err != nil {
			t.Errorf("%s: oracle could not derive wire keys for %s: %v", b.label, b.named.Obj().Name(), err)
			continue
		}
		populationIssues += len(issues)
		for _, issue := range issues {
			t.Errorf("%s: oracle could not populate %s (%s) -- an unpopulated field with `omitempty` would silently drop a key and read as a schema-only property. Give the type a populatable shape or exempt it with a reason.", b.label, issue.path, issue.reason)
		}
		fields := visibleFieldsByWireKey(rt)
		checked++

		// FAIL CLOSED on any tag option that changes the wire TYPE.
		//
		// `,string` keeps the key and changes the encoding, so a key-set
		// comparison cannot see it: an int tagged `json:"total,string"`
		// emits "7" against a published "type": "integer" and every key
		// still matches. Rather than let that gap widen silently, a bound
		// field carrying the option is RED here.
		//
		// There are ZERO occurrences of `,string` in this package today
		// (measured, not assumed), so this guard fires on nothing on main --
		// it exists so the first one cannot arrive unnoticed.
		// FAIL CLOSED, scanning the STRUCT GRAPH rather than the key-to-field
		// association. A review round evaded the association-based version
		// with an invalid tag name whose key Go truncates, so the field was
		// never associated and the guard never fired while the value went out
		// as a string against an integer contract. A guard on a wire-TYPE
		// property must not depend on knowing which KEY the field lands under.
		for _, path := range stringOptionFields(rt) {
			t.Errorf("%s: Go field %s carries the `,string` tag option, which changes the encoded TYPE without changing the key. This check compares key SETS and does not model wire types, so it cannot verify the published type is still correct. Type parity is not implemented -- see CHAOS-4844. Either drop the option or extend this anchor to compare types.", b.label, path)
		}
		// FAIL CLOSED on custom marshallers: the oracle observes ONE value,
		// which is exhaustive for an ordinary struct and proves nothing for a
		// type whose MarshalJSON can emit different keys for different values.
		for _, path := range customMarshalerTypes(rt) {
			t.Errorf("%s: %s implements json.Marshaler, so its wire key set depends on the VALUE and cannot be derived from the single sample this oracle marshals. Model it explicitly (as time.Time is) or exempt the $def with a reason.", b.label, path)
		}
		// An emitted key the association cannot explain is REPORTED, never
		// silently skipped: the enum check below would otherwise pass by
		// examining nothing.
		for key := range keys {
			if _, ok := fields[key]; !ok {
				t.Errorf("%s: wire key %q is emitted but could not be associated with a Go field, so the enum and option checks cannot examine it. Report rather than skip -- an unexaminable key is an unchecked key.", b.label, key)
			}
		}

		var goOnly, schemaOnly []string
		for key := range keys {
			if _, ok := properties[key]; !ok {
				goOnly = append(goOnly, key)
			}
		}
		for key, propertyNode := range properties {
			if _, ok := keys[key]; ok {
				continue
			}
			if deprecated, _ := propertyNode[deprecatedKeyword].(bool); deprecated {
				continue
			}
			schemaOnly = append(schemaOnly, key)
		}
		sort.Strings(goOnly)
		sort.Strings(schemaOnly)

		for _, key := range goOnly {
			t.Errorf("%s: Go type %s emits wire key %q but the published schema has no such property. With additionalProperties:false every document carrying it is INVALID for a schema-validating consumer, while the Go tests and `make contract-test` stay green -- this is CHAOS-4825's defect class. Add the property to the canonical schema, or stop emitting the field.",
				b.label, b.named.Obj().Name(), key)
		}
		for _, key := range schemaOnly {
			t.Errorf("%s: the published schema declares property %q but Go type %s never emits it. The contract promises a field no producer can satisfy. Add the Go field, remove the property, or mark the property \"deprecated\": true if consumers may still see it from an older producer.",
				b.label, key, b.named.Obj().Name())
		}
	}
	if checked == 0 {
		t.Fatal("no bound pair was actually compared -- the anchor proved nothing")
	}
	t.Logf("anchored %d schema/Go pairs across %d canonical documents", checked, len(schemas.documents))
	// Reported so the oracle's own blind spots are visible rather than
	// assumed away. A field the populator cannot fill would leave an
	// `omitempty` key out of the oracle's answer, which reads as "the schema
	// publishes a property Go never emits" -- a false finding pointing at
	// the schema instead of at the instrument.
	t.Logf("oracle population issues across all bound types: %d", populationIssues)
}

// TestPublishedEnumsMatchGoVocabularies anchors the closed vocabularies.
//
// The rule is per-FIELD rather than per-type. One named string type can be
// reachable from several fields whose producers emit different subsets of
// it, so "the schema enum equals the type's vocabulary" would be wrong on
// this repository's own main: ContextFabricNarrowingBasis has four members,
// and ProjectionBudget's cohort_member_selection_basis deliberately admits
// only the two SelectGroupCoverMembers can return (CHAOS-4809).
//
// So:
//
//   - schema enum SUBSET-OF Go vocabulary, always. A published enum member
//     with no Go constant is a contract advertising a value no producer can
//     emit, and also catches a mistyped literal.
//   - Go vocabulary MINUS schema enum must be empty unless an
//     enumNarrowings entry names the field, the excluded constants and the
//     reason -- AND the field's production validator actually REJECTS each
//     excluded value. A narrowing whose validator still accepts the value it
//     claims to exclude is the CHAOS-4809 R1-P3 defect (a closed vocabulary
//     wider than its producer), and an entry must not be able to paper over
//     it.
//
// The dangerous direction stays closed either way: a constant added to a Go
// vocabulary widens the producer past the published schema and fails here
// until someone states why.
func TestPublishedEnumsMatchGoVocabularies(t *testing.T) {
	root := moduleRootForParity(t)
	schemas := loadCanonicalSchemas(t, root)
	pkg := loadContractsPackage(t, root)
	scope := pkg.Types.Scope()
	runtimeTypes := runtimeTypeIndex()

	bindings, _ := resolveBindings(t, schemas, scope)
	checked := 0

	for _, b := range bindings {
		node, _, ok := schemas.objectNode(b.document, b.node)
		if !ok {
			continue
		}
		if _, isStruct := b.named.Underlying().(*types.Struct); isStruct {
			// Struct defs carry their enums on individual PROPERTIES, which
			// are checked below by walking the struct's own fields.
			checkStructPropertyEnums(t, schemas, scope, runtimeTypes, b, node)
			continue
		}
		// A vocabulary $def: the whole node is the enum.
		vocabulary := goVocabulary(scope, b.named)
		if len(vocabulary) == 0 {
			continue
		}
		published := schemaEnumValues(node)
		if published == nil {
			t.Errorf("%s is bound to Go vocabulary %s (%d constants) but the schema node publishes no \"enum\" -- the closed vocabulary is not actually closed on the wire", b.label, b.named.Obj().Name(), len(vocabulary))
			continue
		}
		checked++
		compareEnum(t, b.label, b.named.Obj().Name(), vocabulary, published, "")
	}
	if checked == 0 {
		t.Fatal("no vocabulary was compared -- the enum anchor proved nothing")
	}
}

// checkStructPropertyEnums walks a bound struct's fields and, for each field
// whose Go type is a named vocabulary and whose published property carries an
// enum, applies the same per-field rule.
func checkStructPropertyEnums(t *testing.T, schemas schemaSet, scope *types.Scope, runtimeTypes map[string]reflect.Type, b binding, node map[string]any) {
	t.Helper()
	rt, ok := runtimeTypes[b.named.Obj().Name()]
	if !ok {
		return
	}
	fields := visibleFieldsByWireKey(rt)
	properties := propertyKeys(node)
	for key, field := range fields {
		propertyNode, ok := properties[key]
		if !ok {
			continue
		}
		// The vocabulary is identified from the RUNTIME field type's name and
		// then resolved through go/types to read its constants. Association
		// uses reflect.VisibleFields -- the standard library's own promotion
		// and shadowing logic -- never a reimplementation of it.
		vocabularyName := namedStringTypeName(field.Type)
		if vocabularyName == "" {
			continue
		}
		named, resolvedOK := resolveNamedType(scope, vocabularyName)
		if !resolvedOK {
			continue
		}
		vocabulary := goVocabulary(scope, named)
		if len(vocabulary) == 0 {
			continue
		}
		resolved, _, ok := schemas.objectNode(b.document, propertyNode)
		if !ok {
			continue
		}
		published := schemaEnumValues(resolved)
		if published == nil {
			// The property does not restate the enum -- typically because it
			// $refs the vocabulary $def, which is checked on its own above.
			continue
		}
		label := b.document + "#" + b.defName + "." + key
		if b.defName == "" {
			label = b.document + "#(root)." + key
		}
		compareEnum(t, label, named.Obj().Name(), vocabulary, published, label)
	}
}

// compareEnum applies the subset rule and the narrowing rule.
func compareEnum(t *testing.T, label, goTypeName string, vocabulary, published []string, narrowingKey string) {
	t.Helper()
	inGo := map[string]bool{}
	for _, v := range vocabulary {
		inGo[v] = true
	}
	inSchema := map[string]bool{}
	for _, v := range published {
		inSchema[v] = true
	}

	// Direction 1: the schema may never advertise a value Go cannot emit.
	//
	// The empty string is the one member that can legitimately be absent
	// from the constant list, because Go does not name a constant for a
	// type's zero value. It is admitted only when a declared production
	// validator actually accepts it -- proven, not asserted.
	var schemaOnly []string
	for _, v := range published {
		if inGo[v] {
			continue
		}
		if v == "" {
			validator, declared := vocabularyAdmitsZeroValue[goTypeName]
			switch {
			case !declared:
				t.Errorf("%s: the published enum admits the empty string, which is not a constant of Go vocabulary %s. If \"\" is a meaningful unset value on this wire, add a vocabularyAdmitsZeroValue entry naming the validator that accepts it; otherwise remove it from the schema enum.", label, goTypeName)
			case !validator(""):
				t.Errorf("%s: vocabularyAdmitsZeroValue claims %s accepts the empty string, but its validator REJECTS it. The published enum admits a value the producer's own validator refuses.", label, goTypeName)
			}
			continue
		}
		schemaOnly = append(schemaOnly, v)
	}
	sort.Strings(schemaOnly)
	for _, v := range schemaOnly {
		t.Errorf("%s: the published enum admits %q, which is not a constant of Go vocabulary %s. The contract advertises a value no producer can emit (or the literal is mistyped).", label, v, goTypeName)
	}

	// Direction 2: Go may not emit a value the schema forbids, unless the
	// narrowing is both declared AND enforced by the field's validator.
	var goOnly []string
	for _, v := range vocabulary {
		if !inSchema[v] {
			goOnly = append(goOnly, v)
		}
	}
	sort.Strings(goOnly)
	if len(goOnly) == 0 {
		return
	}

	narrowing, declared := enumNarrowings[narrowingKey]
	if !declared {
		for _, v := range goOnly {
			t.Errorf("%s: Go vocabulary %s contains %q but the published enum does not admit it. A producer emitting it writes a document invalid against its own contract. Add it to the schema enum, or declare an enumNarrowings entry naming the excluded values, the reason, and the validator that rejects them.", label, goTypeName, v)
		}
		return
	}

	excluded := map[string]bool{}
	for _, v := range narrowing.excluded {
		excluded[v] = true
	}
	for _, v := range goOnly {
		if !excluded[v] {
			t.Errorf("%s: Go vocabulary %s contains %q, the schema does not admit it, and the enumNarrowings entry does not list it as excluded. The narrowing has gone stale against the vocabulary it narrows.", label, goTypeName, v)
		}
	}
	for _, v := range narrowing.excluded {
		if !inGo[v] {
			t.Errorf("%s: enumNarrowings excludes %q, which is not a constant of %s at all -- the entry is stale and is silently excluding nothing.", label, v, goTypeName)
		}
		if inSchema[v] {
			t.Errorf("%s: enumNarrowings excludes %q, but the published enum admits it -- the entry contradicts the schema it describes.", label, v)
		}
		// The narrowing must be PROVEN, not declared: the production
		// validator has to actually reject the value.
		if narrowing.accepts == nil {
			t.Errorf("%s: enumNarrowings entry has no validator, so the exclusion of %q is an unenforced comment. Name the production validator in accepts.", label, v)
			continue
		}
		if narrowing.accepts(v) {
			t.Errorf("%s: enumNarrowings claims %q is excluded, but the production validator ACCEPTS it. A closed vocabulary wider than its producer is not a closed vocabulary -- this is the CHAOS-4809 R1-P3 defect, and the narrowing entry must not be able to hide it.", label, v)
		}
	}
}

// namedStringTypeName returns the NAME of a named string type behind a
// field, unwrapping pointers and slices, or "" if the field is not one.
//
// It reports only the name; the constants come from go/types, which is the
// only place a package-level vocabulary can be enumerated.
func namedStringTypeName(rt reflect.Type) string {
	for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.String {
		return ""
	}
	if rt.PkgPath() == "" {
		return ""
	}
	return rt.Name()
}

func schemaEnumValues(node map[string]any) []string {
	raw, ok := node["enum"].([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			values = append(values, s)
		}
	}
	sort.Strings(values)
	return values
}

// TestReportOrphanSchemaDefs REPORTS $defs that nothing references. It never
// fails.
//
// It exists because the CHAOS-4825 drift this file was written for lived in
// exactly such a definition: context_fabric_common.v1's $defs.SourceObservation
// had gone stale against its Go producer while the shape actually validated on
// the wire was a second, INLINE copy under Coverage.properties.sources.items.
// Two representations of one shape in one document, only one of them
// maintained, and no consumer impact until somebody $refs the stale one.
//
// This is deliberately a report and not a gate. An unreferenced definition is
// not wrong -- it can be published for consumers to reference, or staged ahead
// of the code that will use it -- so failing on it would be a false-positive
// generator. What it is, reliably, is a place where drift can hide, and the
// list belongs in front of whoever is changing these files.
//
// What this keys on, stated so the next reader knows what it cannot see: a
// definition counts as REFERENCED if any "$ref" anywhere in the canonical
// schemas, the binary-embedded copies, or the canonical OpenAPI document ends
// with "/$defs/<name>". That is deliberately generous -- it matches across
// documents and cannot distinguish two same-named defs in different files --
// so it under-reports orphans rather than inventing them. Tracked for
// consolidation as its own low-priority ticket.
func TestReportOrphanSchemaDefs(t *testing.T) {
	root := moduleRootForParity(t)
	schemas := loadCanonicalSchemas(t, root)

	referenced := map[string]bool{}
	collect := func(node any) {
		var walk func(any)
		walk = func(n any) {
			switch value := n.(type) {
			case map[string]any:
				for key, child := range value {
					if key == "$ref" {
						if ref, ok := child.(string); ok {
							if index := strings.LastIndex(ref, "/$defs/"); index >= 0 {
								referenced[ref[index+len("/$defs/"):]] = true
							}
						}
					}
					walk(child)
				}
			case []any:
				for _, child := range value {
					walk(child)
				}
			}
		}
		walk(node)
	}
	for _, document := range schemas.documents {
		collect(document)
	}
	// The embedded and OpenAPI copies are separate reference surfaces: a def
	// referenced only from the shipped binary's schema is still referenced.
	for _, extra := range []string{
		filepath.Join(root, "internal", "mcp", "schemas"),
		filepath.Join(root, "contracts", "openapi"),
	} {
		entries, err := os.ReadDir(extra)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(extra, entry.Name()))
			if err != nil {
				continue
			}
			var document any
			if json.Unmarshal(raw, &document) == nil {
				collect(document)
			}
		}
	}

	var orphans []string
	documents := make([]string, 0, len(schemas.documents))
	for name := range schemas.documents {
		documents = append(documents, name)
	}
	sort.Strings(documents)
	for _, name := range documents {
		defs, _ := schemas.documents[name]["$defs"].(map[string]any)
		defNames := make([]string, 0, len(defs))
		for defName := range defs {
			defNames = append(defNames, defName)
		}
		sort.Strings(defNames)
		for _, defName := range defNames {
			if referenced[defName] {
				continue
			}
			// A definition that is itself a CONTAINER of referenced defs --
			// the embedded whole-document copies in the MCP response schemas
			// are the case -- is reached through its children, not by a $ref
			// to itself. Reporting it as an orphan would be crying wolf.
			if node, ok := defs[defName].(map[string]any); ok {
				if inner, ok := node["$defs"].(map[string]any); ok && len(inner) > 0 {
					container := false
					for innerName := range inner {
						if referenced[innerName] {
							container = true
							break
						}
					}
					if container {
						continue
					}
				}
			}
			orphans = append(orphans, name+"#$defs."+defName)
		}
	}

	if len(orphans) == 0 {
		t.Log("orphan $defs: none -- every published definition is referenced somewhere")
		return
	}
	t.Logf("orphan $defs (%d): definitions no $ref reaches, across canonical schemas, internal/mcp/schemas and contracts/openapi. Not a failure -- a place drift can hide, because nothing validates through them. Consolidate with the shape they duplicate, or delete:", len(orphans))
	for _, orphan := range orphans {
		t.Logf("  %s", orphan)
	}
}
