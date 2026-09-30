// Package evidenceref is the one grammar of the Context Fabric evidence refs
// whose ids carry more than one component (CHAOS-7252).
//
// The five kinds it covers replace five retired kinds whose producers joined
// colon-capable ids with a bare ':' (a work item id "jira:A:B" and a target
// "jira:C" gave the same text as "jira:A" and "B:jira:C"), so one ref could
// name two rows and a source-row read by that text served whichever row was
// left or admitted (CHAOS-7226 r2 P1). Here every segment goes through
// identity.EncodeSegment ('%' -> "%25" first, then ':' -> "%3A"), so no
// segment holds a ':' and every ':' in an id is a separator. With a fixed
// segment count per kind the id is injective, and Parse is its exact
// inverse: split, decode, and refuse any text Mint would not produce.
//
// A UUID segment is the text of a UUID column: the same encoding (a no-op on
// canonical UUID text, which holds neither '%' nor ':'), rendered in SQL
// through toString. The grammar does not require the text to be a canonical
// UUID -- injectivity does not depend on it -- so a reader that binds the
// segment as a UUID (the source-row resolver's repository lookup) checks
// CanonicalUUID itself, before any read.
//
// Old and new refs never meet: the successors are distinct kinds (the
// ".v2" segment), the kind is cut at the first ':' of the ref, and a kind
// holds no ':'. An old ref keeps its retired kind and its persisted-record
// route; this package never parses it.
//
// Every producer of these kinds mints through Mint, and every SQL statement
// that must name the same row renders its evidence_ref_id through SQL, so
// the Go and ClickHouse spellings cannot drift apart unnoticed (the
// ClickHouse parity test runs both on the same rows).
package evidenceref

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Form is how one id segment is written.
type Form int

const (
	// Encoded: identity.EncodeSegment of a String column (the empty string
	// too).
	Encoded Form = iota
	// UUID: identity.EncodeSegment of a UUID column's text (ClickHouse's
	// toString(UUID), canonical 8-4-4-4-12 lower-case hexadecimal).
	UUID
)

// Segment is one component of an id: the column it carries and its form.
type Segment struct {
	Column string
	Form   Form
}

// Grammar is the id grammar of one kind: its segments in order.
type Grammar struct {
	Kind     contractsv1.ContextFabricEvidenceEntityType
	Segments []Segment
}

var grammars = map[contractsv1.ContextFabricEvidenceEntityType]Grammar{
	// One canonical dependency RELATION: the twin rows whose raw
	// relationship_type spellings share one dependencyrelation.Key are one
	// relation (CHAOS-7177). No repository: work_item_dependencies has none;
	// the rows' repositories are the endpoints' (resolved at read time).
	contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2: {Segments: []Segment{
		{Column: "source_work_item_id", Form: Encoded},
		{Column: "target_work_item_id", Form: Encoded},
		{Column: "relation_key", Form: Encoded},
	}},
	// The CHILD work_items row, keyed (org_id, repo_id, work_item_id), and
	// the parent it names.
	contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2: {Segments: []Segment{
		{Column: "repo_id", Form: UUID},
		{Column: "work_item_id", Form: Encoded},
		{Column: "parent_id", Form: Encoded},
	}},
	// The work_item_team_attributions row, keyed (org_id, repo_id,
	// work_item_id, ifNull(team_id, ''), source). repo_id is the
	// ATTRIBUTION row's own (the zero UUID for most rows), not the work
	// item's repository.
	contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2: {Segments: []Segment{
		{Column: "repo_id", Form: UUID},
		{Column: "work_item_id", Form: Encoded},
		{Column: "team_id", Form: Encoded},
		{Column: "source", Form: Encoded},
	}},
	// The work_graph_deployment_incident_edges row, keyed (org_id,
	// deployment_id, incident_id, source), in the repository it names.
	contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2: {Segments: []Segment{
		{Column: "repo_id", Form: UUID},
		{Column: "deployment_id", Form: Encoded},
		{Column: "incident_id", Form: Encoded},
		{Column: "source", Form: Encoded},
	}},
	// One project -> team ownership GROUP as the projector forms it:
	// (provider, projects.id, team_id, source) over the team_project_ownership
	// assertions (valid_from and the raw project_id are aggregated away).
	contractsv1.ContextFabricEvidenceEntityProjectTeamV2: {Segments: []Segment{
		{Column: "provider", Form: Encoded},
		{Column: "project_id", Form: Encoded},
		{Column: "team_id", Form: Encoded},
		{Column: "source", Form: Encoded},
	}},
}

// Lookup returns the grammar of kind.
func Lookup(kind contractsv1.ContextFabricEvidenceEntityType) (Grammar, bool) {
	grammar, ok := grammars[kind]
	if !ok {
		return Grammar{}, false
	}
	grammar.Kind = kind
	grammar.Segments = append([]Segment(nil), grammar.Segments...)
	return grammar, true
}

// Kinds lists the kinds this package declares, sorted.
func Kinds() []contractsv1.ContextFabricEvidenceEntityType {
	kinds := make([]contractsv1.ContextFabricEvidenceEntityType, 0, len(grammars))
	for kind := range grammars {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// ID renders the id segment of kind's ref from its component values, in
// grammar order. ok is false for an undeclared kind or a wrong value count.
func ID(kind contractsv1.ContextFabricEvidenceEntityType, values ...string) (string, bool) {
	grammar, ok := grammars[kind]
	if !ok || len(values) != len(grammar.Segments) {
		return "", false
	}
	return identity.JoinSegments(values...), true
}

// Mint returns kind's evidence ref for the component values, and whether it
// fits the evidence-ref contract bound (8..256 runes, no surrounding space,
// no '|'). The ref is returned even when it does not fit, so a producer
// hands it to the contract validator it already runs (the projection
// quarantines the item as a bound violation) rather than truncating or
// dropping it unseen; ref is "" only when ID refuses the values (an
// undeclared kind or a wrong value count: a programming error).
func Mint(kind contractsv1.ContextFabricEvidenceEntityType, values ...string) (ref string, fits bool) {
	id, ok := ID(kind, values...)
	if !ok {
		return "", false
	}
	ref = contractsv1.EvidenceRefID(kind, id)
	return ref, fitsContract(ref)
}

// fitsContract mirrors the evidence-ref bound every projection item and
// answer applies (contracts/v1 boundedEvidenceRefs).
func fitsContract(ref string) bool {
	length := utf8.RuneCountInString(ref)
	return length >= 8 && length <= contractsv1.ContextFabricEvidenceRefIDMaxLength && strings.TrimSpace(ref) == ref && !strings.Contains(ref, "|")
}

// Parse inverts ID: the component values of kind's id, or false when id is
// not exactly what ID renders for some values (a wrong segment count, or a
// segment the codec would not produce, such as a bare '%' or a lower-case
// "%3a").
func Parse(kind contractsv1.ContextFabricEvidenceEntityType, id string) ([]string, bool) {
	grammar, ok := grammars[kind]
	if !ok {
		return nil, false
	}
	pieces := strings.Split(id, ":")
	if len(pieces) != len(grammar.Segments) {
		return nil, false
	}
	values := make([]string, len(pieces))
	for index := range pieces {
		decoded, err := identity.DecodeSegment(pieces[index])
		if err != nil {
			return nil, false
		}
		values[index] = decoded
	}
	if again, ok := ID(kind, values...); !ok || again != id {
		return nil, false
	}
	return values, true
}

// SQL renders the ClickHouse expression of kind's full evidence ref from one
// SQL expression per segment, in grammar order (see IDSQL).
func SQL(kind contractsv1.ContextFabricEvidenceEntityType, expressions ...string) string {
	return "concat('" + contractsv1.ContextFabricEvidenceRefPrefix + string(kind) + ":', " + IDSQL(kind, expressions...) + ")"
}

// IDSQL renders the ClickHouse expression of kind's id segment: every
// segment escaped with identity.SQLSegmentEncodeFragment (the same two
// replacements as EncodeSegment, in the same order), a UUID segment's
// expression first rendered with toString. It panics on an undeclared kind
// or a wrong expression count: statements are fixed Go source, so that is a
// programming error.
func IDSQL(kind contractsv1.ContextFabricEvidenceEntityType, expressions ...string) string {
	grammar, ok := grammars[kind]
	if !ok || len(expressions) != len(grammar.Segments) {
		panic("evidenceref: SQL called with an undeclared kind or a wrong expression count: " + string(kind))
	}
	parts := make([]string, 0, 2*len(expressions))
	for index, segment := range grammar.Segments {
		if index > 0 {
			parts = append(parts, "':'")
		}
		expression := expressions[index]
		if segment.Form == UUID {
			expression = "toString(" + expression + ")"
		}
		parts = append(parts, EncodeSQL(expression))
	}
	return "concat(" + strings.Join(parts, ", ") + ")"
}

// EncodeSQL is identity.SQLSegmentEncodeFragment applied to expression.
func EncodeSQL(expression string) string {
	return strings.Replace(identity.SQLSegmentEncodeFragment, "col", expression, 1)
}

// CanonicalUUID reports the canonical 8-4-4-4-12 lower-case hexadecimal
// UUID text.
func CanonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", char) {
				return false
			}
		}
	}
	return true
}
