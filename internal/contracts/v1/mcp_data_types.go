package v1

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Direct data tool inputs (CHAOS-7072, design C.2 to C.4, D.2).
//
// data_catalog, find_subjects and run_operation are model-free reads of the
// hosted direct data routes. Their request types live here so the sidecar,
// the MCP handlers and the published schemas share ONE shape. Their RESPONSE
// documents are the hosted API's JSON, passed through unchanged (structured
// content = the API JSON, nothing added, nothing dropped); the response Go
// types live in internal/contextfabric/directread, which imports this
// package, so the response schemas carry a documented exemption in the
// contracts parity registry and are anchored by real-producer schema tests
// instead (internal/api/chaos7072_data_schema_test.go).
//
// Like every MCP input, none of these carries a schema_version wire field:
// tool identity is the MCP call itself. The contract family of the data tools
// is "acr-data.v1", reported by the API in contract_version.

// Bounds mirrored from internal/contextfabric/directread (an import cycle
// forbids a direct reference). A test in internal/mcp holds each equal to
// its directread source.
const (
	MCPDataCatalogSectionsMax = 5
	MCPFindSubjectsKindsMax   = 8
	MCPFindSubjectsQueryMax   = 256
	// MCPFindSubjectsLimitMax is 200, not the design value 25 (25 is the
	// default page). Ruled by chris 2026-09-29: client agents page through
	// subject lists and 25 forced extra round trips. The MCP tool REFUSES a
	// larger limit; the direct HTTP route clamps it to the same maximum.
	MCPFindSubjectsLimitMax  = 200
	MCPFindSubjectsCursorMax = 700
	MCPRunOperationNameMax   = 64
	MCPRunOperationMaxBytes  = 262144
)

// MCPDataCatalogSectionVocabulary is the closed set of data_catalog sections.
func MCPDataCatalogSectionVocabulary() [5]string {
	return [5]string{"operations", "facts", "subjects", "relationships", "limits"}
}

// MCPDataCatalogRequest is the input of data_catalog. Every field is
// optional: no sections means every section.
type MCPDataCatalogRequest struct {
	Sections []string `json:"sections,omitempty"`
}

// MCPFindSubjectsRequest is the input of find_subjects. Kind alone lists the
// subjects of that kind (list mode). Query, with optional kinds, finds
// subjects by exact name (name mode). Neither is refused.
type MCPFindSubjectsRequest struct {
	Kind   string   `json:"kind,omitempty"`
	Query  string   `json:"query,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
	// OwnedBy is owned_by mode (CHAOS-7126): a team canonical id.
	OwnedBy string `json:"owned_by,omitempty"`
	// Handle is handle mode (CHAOS-7126): one pull request number, work
	// item key or CI run id, for example "PR 532".
	Handle string `json:"handle,omitempty"`
}

// MCPRunOperationRequest is the input of run_operation. Variables is an open
// object here: the hosted policy artifact owns the per-operation variable
// allowlist and refuses any path it does not list.
type MCPRunOperationRequest struct {
	Operation string         `json:"operation"`
	Variables map[string]any `json:"variables,omitempty"`
	MaxBytes  int            `json:"max_bytes,omitempty"`
}

// Validate applies the schema's bounds to a data_catalog request.
func (r MCPDataCatalogRequest) Validate() error {
	if len(r.Sections) > MCPDataCatalogSectionsMax {
		return fmt.Errorf("data_catalog accepts at most %d sections", MCPDataCatalogSectionsMax)
	}
	seen := map[string]bool{}
	for _, section := range r.Sections {
		known := false
		for _, name := range MCPDataCatalogSectionVocabulary() {
			known = known || name == section
		}
		if !known {
			return fmt.Errorf("data_catalog sections must be from the closed section vocabulary")
		}
		if seen[section] {
			return fmt.Errorf("data_catalog sections must be unique")
		}
		seen[section] = true
	}
	return nil
}

func validSubjectKindName(kind string) bool {
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(kind))
}

// Validate applies the schema's bounds to a find_subjects request and the
// mode rule of the hosted lookup: list mode is exactly one kind and no
// query; name mode is a query.
func (r MCPFindSubjectsRequest) Validate() error {
	if r.Kind != "" && !validSubjectKindName(r.Kind) {
		return fmt.Errorf("find_subjects kind must be a subject kind")
	}
	if len(r.Kinds) > MCPFindSubjectsKindsMax {
		return fmt.Errorf("find_subjects accepts at most %d kinds", MCPFindSubjectsKindsMax)
	}
	for _, kind := range r.Kinds {
		if !validSubjectKindName(kind) {
			return fmt.Errorf("find_subjects kinds must be subject kinds")
		}
	}
	if utf8.RuneCountInString(r.Query) > MCPFindSubjectsQueryMax {
		return fmt.Errorf("find_subjects query is too long")
	}
	if r.Limit < 0 || r.Limit > MCPFindSubjectsLimitMax {
		return fmt.Errorf("find_subjects limit must be between 1 and %d", MCPFindSubjectsLimitMax)
	}
	if utf8.RuneCountInString(r.Cursor) > MCPFindSubjectsCursorMax {
		return fmt.Errorf("find_subjects cursor is too long")
	}
	if utf8.RuneCountInString(r.OwnedBy) > MCPFindSubjectsQueryMax || utf8.RuneCountInString(r.Handle) > MCPFindSubjectsQueryMax {
		return fmt.Errorf("find_subjects owned_by and handle are too long")
	}
	modes := 0
	for _, value := range []string{r.Query, r.OwnedBy, r.Handle} {
		if strings.TrimSpace(value) != "" {
			modes++
		}
	}
	switch {
	case modes > 1:
		return fmt.Errorf("find_subjects takes one of query, owned_by and handle")
	case strings.TrimSpace(r.Handle) != "" && (strings.TrimSpace(r.Kind) != "" || len(r.Kinds) > 0):
		return fmt.Errorf("find_subjects handle mode takes no kind; the handle names it")
	case modes == 0 && (strings.TrimSpace(r.Kind) == "" || len(r.Kinds) > 0):
		return fmt.Errorf("find_subjects needs a query (name mode), owned_by, handle, or exactly one kind and no kinds (list mode)")
	}
	return nil
}

// Validate applies the schema's bounds to a run_operation request.
func (r MCPRunOperationRequest) Validate() error {
	if !stringLengthBetween(r.Operation, 1, MCPRunOperationNameMax) {
		return fmt.Errorf("run_operation requires an operation name within v1 bounds")
	}
	for i, c := range r.Operation {
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := c >= '0' && c <= '9'
		if !letter && !(digit && i > 0) {
			return fmt.Errorf("run_operation operation must be a letter followed by letters and digits")
		}
	}
	if r.MaxBytes < 0 || r.MaxBytes > MCPRunOperationMaxBytes {
		return fmt.Errorf("run_operation max_bytes must be between 1 and %d", MCPRunOperationMaxBytes)
	}
	return nil
}
