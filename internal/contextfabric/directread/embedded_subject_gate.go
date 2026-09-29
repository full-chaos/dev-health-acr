package directread

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The embedded-subject gate (CHAOS-7073, design CHAOS-7036 section E.3,
// decision K2 as ruled: "remove rows and references of unseen subjects, keep
// a count, serve the labelled aggregate").
//
// The subject gate admits the ROOT subjects of a read. A fact about an
// admitted team or project can still NAME other subjects: a project's health
// fact holds one risk_breakdown row per repository its teams own, a team's
// investment table names teams, every fact carries evidence references. A
// repository-restricted caller admitted to a team through ONE granted
// repository must not receive the id, name or values of the team's other
// repositories.
//
// So before a response leaves acr-api:
//
//  1. every field and table column must be declared by the capability
//     (FactCapability.Fields); an undeclared one is removed and counted,
//     never served;
//  2. every declared subject reference, in scalars, table rows and evidence
//     references, goes through the SAME SubjectGate the roots passed (one
//     batch per read), so this gate agrees with the root gate -- including
//     the CHAOS-7080 wildcard rule -- by construction, not by a second copy
//     of the predicate;
//  3. a row naming a refused subject is removed and the table keeps a count
//     (rows_withheld); a scalar naming one is removed (fields_withheld); an
//     evidence reference naming one is removed (evidence_withheld). Ids and
//     labels of refused subjects never leave;
//  4. a reference the gate cannot resolve to a graph subject (an opaque form,
//     or a value that does not fit its declared form) is refused for a
//     repository-restricted caller and admitted for any other caller: fail
//     closed where the grant matters;
//  5. a gate that is unavailable fails the whole read (ErrEmbeddedGateUnavailable);
//     it never serves unfiltered rows.
//
// Aggregate scalars (K2) are NOT recomputed: the caller receives them
// labelled with their aggregate scope (read_facts.go).

// ErrEmbeddedGateUnavailable is returned when the subject gate cannot decide
// the embedded references. The tool answers unavailable; nothing is served.
var ErrEmbeddedGateUnavailable = errors.New("embedded subject gate is unavailable")

// GatedFact is one fact after the embedded-subject gate.
type GatedFact struct {
	Fact contextfabric.CanonicalFact
	// FieldsWithheld names scalars removed because they named an unseen
	// subject; FieldsUndeclared names fields removed because the capability
	// does not declare them.
	FieldsWithheld   []string
	FieldsUndeclared []string
	// RowsWithheld counts removed rows per table field; ColumnsUndeclared
	// names undeclared columns removed per table field.
	RowsWithheld      map[string]int
	ColumnsUndeclared map[string][]string
	// EvidenceWithheld counts removed evidence references.
	EvidenceWithheld int
}

// EmbeddedReport is the gate's count summary for telemetry. It carries
// counts only, never ids.
type EmbeddedReport struct {
	ReferencesChecked int
	ReferencesRefused int
	RowsWithheld      int
	FieldsWithheld    int
	FieldsUndeclared  int
	EvidenceWithheld  int
}

// EmbeddedSubjectGate filters facts through the subject gate.
type EmbeddedSubjectGate struct {
	gate *SubjectGate
}

// NewEmbeddedSubjectGate wraps the root subject gate. A nil gate makes
// every filter fail closed.
func NewEmbeddedSubjectGate(gate *SubjectGate) *EmbeddedSubjectGate {
	return &EmbeddedSubjectGate{gate: gate}
}

type embeddedRef struct {
	kind      contextfabric.SubjectKind
	canonical string
	opaque    bool
}

func (r embeddedRef) key() string { return string(r.kind) + "\x00" + r.canonical }

// Filter applies the gate to facts read for roots. capabilities are the
// registry's declarations by kind. The roots value must have been issued to
// principal by the subject gate.
func (g *EmbeddedSubjectGate) Filter(ctx context.Context, principal storage.Principal, roots AuthorizedSubjects, facts []contextfabric.CanonicalFact, capabilities map[contextfabric.FactKind]contextfabric.FactCapability) ([]GatedFact, EmbeddedReport, error) {
	if !roots.IssuedTo(principal) || roots.Len() == 0 {
		return nil, EmbeddedReport{}, ErrUngatedRead
	}
	restricted := ClassifyPrincipal(principal) == ClassRestricted
	admitted := map[string]bool{}
	for _, root := range roots.Subjects() {
		admitted[string(root.Kind)+"\x00"+root.CanonicalID] = true
	}
	// Pass 1: collect every reference the facts name.
	pending := map[string]contextfabric.SubjectRef{}
	var report EmbeddedReport
	visit := func(fact contextfabric.CanonicalFact, ref embeddedRef, ok bool) {
		report.ReferencesChecked++
		if !ok || ref.opaque {
			return
		}
		if _, decided := admitted[ref.key()]; decided {
			return
		}
		pending[ref.key()] = contextfabric.SubjectRef{Kind: ref.kind, CanonicalID: ref.canonical}
	}
	for _, fact := range facts {
		capability := capabilities[fact.Kind]
		forEachReference(fact, capability, visit)
	}
	if len(pending) > 0 {
		if g == nil || g.gate == nil {
			return nil, report, ErrEmbeddedGateUnavailable
		}
		keys := make([]string, 0, len(pending))
		for key := range pending {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for start := 0; start < len(keys); start += MaxSubjectsPerRequest {
			end := min(start+MaxSubjectsPerRequest, len(keys))
			batch := make([]contextfabric.SubjectRef, 0, end-start)
			for _, key := range keys[start:end] {
				batch = append(batch, pending[key])
			}
			granted, decision := g.gate.Authorize(ctx, principal, batch)
			if decision.Decision == DecisionUnavailable {
				return nil, report, fmt.Errorf("%w: %s", ErrEmbeddedGateUnavailable, decision.Reason)
			}
			for _, subject := range batch {
				admitted[string(subject.Kind)+"\x00"+subject.CanonicalID] = false
			}
			for _, subject := range granted.Subjects() {
				admitted[string(subject.Kind)+"\x00"+subject.CanonicalID] = true
			}
		}
	}
	allows := func(ref embeddedRef, ok bool) bool {
		if !ok || ref.opaque {
			return !restricted
		}
		return admitted[ref.key()]
	}
	// Pass 2: remove what names a refused subject or is undeclared.
	out := make([]GatedFact, 0, len(facts))
	for _, fact := range facts {
		capability := capabilities[fact.Kind]
		gated := filterFact(fact, capability, allows)
		report.RowsWithheld += sumCounts(gated.RowsWithheld)
		report.FieldsWithheld += len(gated.FieldsWithheld)
		report.FieldsUndeclared += len(gated.FieldsUndeclared)
		for _, columns := range gated.ColumnsUndeclared {
			report.FieldsUndeclared += len(columns)
		}
		report.EvidenceWithheld += gated.EvidenceWithheld
		out = append(out, gated)
	}
	for key, ok := range admitted {
		if !ok && pending[key].CanonicalID != "" {
			report.ReferencesRefused++
		}
	}
	return out, report, nil
}

func sumCounts(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// forEachReference calls visit for every declared reference in fact:
// scalar fields, table cells and evidence references.
func forEachReference(fact contextfabric.CanonicalFact, capability contextfabric.FactCapability, visit func(contextfabric.CanonicalFact, embeddedRef, bool)) {
	for name, value := range fact.Fields {
		declaration, declared := capability.FieldDeclaration(name, fact.Subject.Kind)
		if !declared {
			continue
		}
		if declaration.SubjectRef != nil && value.String != nil {
			ref, ok := resolveReference(*declaration.SubjectRef, nil, *value.String)
			visit(fact, ref, ok)
		}
		if declaration.Type == contextfabric.FactFieldTable {
			for _, row := range value.Rows {
				for column, cell := range row.Fields {
					columnDeclaration, ok := declaration.Column(column)
					if !ok || columnDeclaration.SubjectRef == nil || cell.String == nil {
						continue
					}
					ref, resolved := resolveReference(*columnDeclaration.SubjectRef, row.Fields, *cell.String)
					visit(fact, ref, resolved)
				}
			}
		}
	}
	for _, id := range fact.EvidenceRefIDs {
		ref, ok, own := resolveEvidence(fact.Subject, id)
		if own {
			continue
		}
		visit(fact, ref, ok)
	}
}

func resolveReference(declaration contextfabric.FactSubjectRefDeclaration, row map[string]contextfabric.FactValue, raw string) (embeddedRef, bool) {
	kind, form, ok := declaration.Resolve(row)
	if !ok {
		return embeddedRef{}, false
	}
	if form == contextfabric.FactSubjectIDOpaque {
		return embeddedRef{kind: kind, opaque: true}, true
	}
	canonical, ok := form.CanonicalSubjectID(raw)
	if !ok {
		return embeddedRef{}, false
	}
	return embeddedRef{kind: kind, canonical: canonical}, true
}

// resolveEvidence maps an evidence reference to a subject. own is true when
// the reference is the fact's own subject (a provider cites the subject it
// answered for), which the root gate already admitted.
func resolveEvidence(subject contextfabric.SubjectRef, id string) (ref embeddedRef, ok bool, own bool) {
	rest, found := strings.CutPrefix(id, contractsv1.ContextFabricEvidenceRefPrefix)
	if !found {
		return embeddedRef{}, false, false
	}
	entity, raw, found := strings.Cut(rest, ":")
	if !found || raw == "" {
		return embeddedRef{}, false, false
	}
	var kind contextfabric.SubjectKind
	var form contextfabric.FactSubjectIDForm
	switch contractsv1.ContextFabricEvidenceEntityType(entity) {
	case contractsv1.ContextFabricEvidenceEntityRepository:
		kind, form = contractsv1.ContextFabricSubjectRepository, contextfabric.FactSubjectIDRepositoryUUID
	case contractsv1.ContextFabricEvidenceEntityTeam:
		kind, form = contractsv1.ContextFabricSubjectTeam, contextfabric.FactSubjectIDTeamID
	case contractsv1.ContextFabricEvidenceEntityProject:
		// A project's evidence id is its "<provider>:<id>" key; its
		// canonical id is derived from the same two segments. The
		// reference is compared with the fact's subject by that derived
		// id and otherwise gated like any other project -- never assumed
		// to be the fact's own because the kinds match (codex r1 P1).
		provider, id, split := strings.Cut(raw, ":")
		if !split || provider == "" || id == "" {
			return embeddedRef{}, false, false
		}
		canonical, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
		if err != nil || omitted {
			return embeddedRef{}, false, false
		}
		if subject.Kind == contractsv1.ContextFabricSubjectProject && canonical == subject.CanonicalID {
			return embeddedRef{}, true, true
		}
		return embeddedRef{kind: contractsv1.ContextFabricSubjectProject, canonical: canonical}, true, false
	case contractsv1.ContextFabricEvidenceEntityOrganization:
		// Gated by the organization rule (only the caller's own
		// organization), never assumed own.
		return embeddedRef{kind: contractsv1.ContextFabricSubjectOrganization, canonical: raw}, true, false
	default:
		// Row-level evidence (a work item, a pull request, a CI run) names a
		// source row, not a subject the root gate decided: opaque.
		return embeddedRef{opaque: true}, true, false
	}
	canonical, mapped := form.CanonicalSubjectID(raw)
	if !mapped {
		return embeddedRef{}, false, false
	}
	if kind == subject.Kind && canonical == subject.CanonicalID {
		return embeddedRef{}, true, true
	}
	return embeddedRef{kind: kind, canonical: canonical}, true, false
}

func filterFact(fact contextfabric.CanonicalFact, capability contextfabric.FactCapability, allows func(embeddedRef, bool) bool) GatedFact {
	gated := GatedFact{
		RowsWithheld:      map[string]int{},
		ColumnsUndeclared: map[string][]string{},
	}
	fields := make(map[string]contextfabric.FactValue, len(fact.Fields))
	for name, value := range fact.Fields {
		declaration, declared := capability.FieldDeclaration(name, fact.Subject.Kind)
		if !declared {
			gated.FieldsUndeclared = append(gated.FieldsUndeclared, name)
			continue
		}
		if declaration.SubjectRef != nil && value.String != nil {
			ref, ok := resolveReference(*declaration.SubjectRef, nil, *value.String)
			if !allows(ref, ok) {
				gated.FieldsWithheld = append(gated.FieldsWithheld, name)
				continue
			}
		}
		if declaration.Type == contextfabric.FactFieldTable && value.Rows != nil {
			value = filterTable(name, declaration, value, allows, &gated)
		}
		fields[name] = value
	}
	sort.Strings(gated.FieldsUndeclared)
	sort.Strings(gated.FieldsWithheld)
	evidence := make([]string, 0, len(fact.EvidenceRefIDs))
	for _, id := range fact.EvidenceRefIDs {
		ref, ok, own := resolveEvidence(fact.Subject, id)
		if !own && !allows(ref, ok) {
			gated.EvidenceWithheld++
			continue
		}
		evidence = append(evidence, id)
	}
	fact.Fields = fields
	fact.EvidenceRefIDs = evidence
	gated.Fact = fact
	return gated
}

func filterTable(name string, declaration contextfabric.FactFieldDeclaration, value contextfabric.FactValue, allows func(embeddedRef, bool) bool, gated *GatedFact) contextfabric.FactValue {
	undeclared := map[string]struct{}{}
	rows := make([]contextfabric.FactValueRow, 0, len(value.Rows))
	for _, row := range value.Rows {
		keep := true
		cells := make(map[string]contextfabric.FactValue, len(row.Fields))
		for column, cell := range row.Fields {
			columnDeclaration, ok := declaration.Column(column)
			if !ok {
				undeclared[column] = struct{}{}
				continue
			}
			if columnDeclaration.SubjectRef != nil && !cell.Null {
				raw := ""
				if cell.String != nil {
					raw = *cell.String
				}
				ref, resolved := resolveReference(*columnDeclaration.SubjectRef, row.Fields, raw)
				if !allows(ref, resolved) {
					keep = false
					break
				}
			}
			cells[column] = cell
		}
		if !keep {
			gated.RowsWithheld[name]++
			continue
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: cells})
	}
	if len(undeclared) > 0 {
		for column := range undeclared {
			gated.ColumnsUndeclared[name] = append(gated.ColumnsUndeclared[name], column)
		}
		sort.Strings(gated.ColumnsUndeclared[name])
	}
	out := contextfabric.FactValue{Rows: rows}
	if value.Table != nil {
		table := *value.Table
		table.Rows = rows
		out.Table = &table
	}
	return out
}
