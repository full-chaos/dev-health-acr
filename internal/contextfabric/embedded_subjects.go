package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The embedded-subject gate core (CHAOS-7073 design CHAOS-7036 section E.3,
// decision K2 as ruled: "remove rows and references of unseen subjects, keep
// a count, serve the labelled aggregate"). It lives here so BOTH read paths
// apply one implementation: the direct read tools (directread's
// EmbeddedSubjectGate, which decides references through its SubjectGate) and
// the investigation engine's fact read (CHAOS-7127, engine_fact_gate.go,
// which decides them through the StoredResultGate the fact-root re-check
// uses). Neither path carries a second copy of the filter.
//
// A fact about an admitted team or project can NAME other subjects: a
// project's health fact holds one risk_breakdown row per repository its teams
// own. A repository-restricted caller admitted to the project through ONE
// granted repository must not receive the id, name or values of the others.
//
//  1. every field and table column must be declared by the capability
//     (FactCapability.Fields); an undeclared one is removed and counted;
//  2. every declared subject reference, in scalars, table rows and evidence
//     references, is decided by the caller's authorize function, in batches;
//  3. a row naming a refused subject is removed and the table keeps a count
//     (RowsWithheld); a scalar naming one is removed (FieldsWithheld); an
//     evidence reference naming one is removed (EvidenceWithheld). Ids and
//     labels of refused subjects never leave;
//  4. a reference that cannot be resolved to a graph subject (an opaque form,
//     or a value that does not fit its declared form) is refused for a
//     repository-restricted caller and admitted for any other caller;
//  5. an authorize function that is missing or fails fails the whole read
//     (ErrEmbeddedSubjectGateUnavailable); nothing unfiltered is served.
//
// Aggregate scalars (K2) are NOT recomputed; they are served labelled.

// ErrEmbeddedSubjectGateUnavailable: the references could not be decided.
// Nothing is served.
var ErrEmbeddedSubjectGateUnavailable = errors.New("embedded subject gate is unavailable")

// EmbeddedGatedFact is one fact after the embedded-subject gate.
type EmbeddedGatedFact struct {
	Fact CanonicalFact
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

// Withheld reports whether the gate removed anything that named a refused
// subject from this fact.
func (g EmbeddedGatedFact) Withheld() bool {
	return len(g.FieldsWithheld) > 0 || sumCounts(g.RowsWithheld) > 0 || g.EvidenceWithheld > 0
}

// EmbeddedSubjectReport is the gate's count summary for telemetry. Counts
// only, never ids.
type EmbeddedSubjectReport struct {
	ReferencesChecked int
	ReferencesRefused int
	RowsWithheld      int
	FieldsWithheld    int
	FieldsUndeclared  int
	EvidenceWithheld  int
}

// EmbeddedSubjectAuthorizeFunc decides one batch of referenced subjects for
// the caller and returns the admitted ones. An error fails the read closed.
type EmbeddedSubjectAuthorizeFunc func(ctx context.Context, batch []SubjectRef) ([]SubjectRef, error)

type embeddedRef struct {
	kind      SubjectKind
	canonical string
	opaque    bool
}

func (r embeddedRef) key() string { return string(r.kind) + "\x00" + r.canonical }

// EmbeddedFilterOptions selects the caller-dependent parts of the gate.
type EmbeddedFilterOptions struct {
	// Restricted is true for a repository-restricted caller: an unresolvable
	// or opaque reference is then refused (rule 4).
	Restricted bool
	// KeepUndeclared keeps fields and columns the capability does not declare
	// instead of removing them (rule 1). The direct tools never set it: their
	// response is the declared catalogue. The engine sets it: its fact bundle
	// also carries registry-composed fields no capability declares, and a
	// declared kind's provider emits only declared fields (the catalogue-truth
	// test), so every subject reference a provider emits is still decided.
	KeepUndeclared bool
	// BatchSize bounds one authorize call; zero means one call.
	BatchSize int
}

// FilterEmbeddedSubjects applies the gate to facts. admittedRoots are the
// subjects the caller's root gate already admitted (their references are not
// re-decided); authorize decides every other reference.
func FilterEmbeddedSubjects(ctx context.Context, options EmbeddedFilterOptions, admittedRoots []SubjectRef, facts []CanonicalFact, capabilities map[FactKind]FactCapability, authorize EmbeddedSubjectAuthorizeFunc) ([]EmbeddedGatedFact, EmbeddedSubjectReport, error) {
	restricted, batchSize := options.Restricted, options.BatchSize
	admitted := map[string]bool{}
	for _, root := range admittedRoots {
		admitted[string(root.Kind)+"\x00"+root.CanonicalID] = true
	}
	// Pass 1: collect every reference the facts name.
	pending := map[string]SubjectRef{}
	var report EmbeddedSubjectReport
	visit := func(_ CanonicalFact, ref embeddedRef, ok bool) {
		report.ReferencesChecked++
		if !ok || ref.opaque {
			return
		}
		if _, decided := admitted[ref.key()]; decided {
			return
		}
		pending[ref.key()] = SubjectRef{Kind: ref.kind, CanonicalID: ref.canonical}
	}
	for _, fact := range facts {
		forEachReference(fact, capabilities[fact.Kind], visit)
	}
	if len(pending) > 0 {
		if authorize == nil {
			return nil, report, ErrEmbeddedSubjectGateUnavailable
		}
		if batchSize <= 0 {
			batchSize = len(pending)
		}
		keys := make([]string, 0, len(pending))
		for key := range pending {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for start := 0; start < len(keys); start += batchSize {
			end := min(start+batchSize, len(keys))
			batch := make([]SubjectRef, 0, end-start)
			for _, key := range keys[start:end] {
				batch = append(batch, pending[key])
			}
			granted, err := authorize(ctx, batch)
			if err != nil {
				return nil, report, fmt.Errorf("%w: %w", ErrEmbeddedSubjectGateUnavailable, err)
			}
			for _, subject := range batch {
				admitted[string(subject.Kind)+"\x00"+subject.CanonicalID] = false
			}
			for _, subject := range granted {
				key := string(subject.Kind) + "\x00" + subject.CanonicalID
				if _, asked := pending[key]; asked {
					admitted[key] = true
				}
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
	out := make([]EmbeddedGatedFact, 0, len(facts))
	for _, fact := range facts {
		gated := filterFact(fact, capabilities[fact.Kind], allows, options.KeepUndeclared)
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

// tableRows is the row set of a table value: its Rows, or, for a table
// carried only in its declared form, the table's own rows. Both passes read
// the same set, so a reference is never decided on rows the filter skips.
func tableRows(value FactValue) []FactValueRow {
	if value.Rows == nil && value.Table != nil {
		return value.Table.Rows
	}
	return value.Rows
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
func forEachReference(fact CanonicalFact, capability FactCapability, visit func(CanonicalFact, embeddedRef, bool)) {
	for name, value := range fact.Fields {
		declaration, declared := capability.FieldDeclaration(name, fact.Subject.Kind)
		if !declared {
			continue
		}
		if declaration.SubjectRef != nil && value.String != nil {
			ref, ok := resolveReference(*declaration.SubjectRef, nil, *value.String)
			visit(fact, ref, ok)
		}
		if declaration.Type == FactFieldTable {
			for _, row := range tableRows(value) {
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

func resolveReference(declaration FactSubjectRefDeclaration, row map[string]FactValue, raw string) (embeddedRef, bool) {
	kind, form, ok := declaration.Resolve(row)
	if !ok {
		return embeddedRef{}, false
	}
	if form == FactSubjectIDOpaque {
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
func resolveEvidence(subject SubjectRef, id string) (ref embeddedRef, ok bool, own bool) {
	rest, found := strings.CutPrefix(id, contractsv1.ContextFabricEvidenceRefPrefix)
	if !found {
		return embeddedRef{}, false, false
	}
	entity, raw, found := strings.Cut(rest, ":")
	if !found || raw == "" {
		return embeddedRef{}, false, false
	}
	var kind SubjectKind
	var form FactSubjectIDForm
	switch contractsv1.ContextFabricEvidenceEntityType(entity) {
	case contractsv1.ContextFabricEvidenceEntityRepository:
		kind, form = contractsv1.ContextFabricSubjectRepository, FactSubjectIDRepositoryUUID
	case contractsv1.ContextFabricEvidenceEntityTeam:
		kind, form = contractsv1.ContextFabricSubjectTeam, FactSubjectIDTeamID
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
	// Entity evidence (CHAOS-7120). Each form maps DETERMINISTICALLY to the
	// canonical id the graph stores for that entity, which is then compared
	// with the fact's own subject or gated like any other subject -- never
	// assumed own because the kinds match (codex r1 P1 on CHAOS-7073).
	case contractsv1.ContextFabricEvidenceEntityWorkItem:
		return resolveRepoScopedEvidence(subject, raw, identity.KindWorkItem, contractsv1.ContextFabricSubjectWorkItem)
	case contractsv1.ContextFabricEvidenceEntityCI:
		return resolveRepoScopedEvidence(subject, raw, identity.KindCIPipelineRun, contractsv1.ContextFabricSubjectCIRun)
	case contractsv1.ContextFabricEvidenceEntityDeployment:
		return resolveRepoScopedEvidence(subject, raw, identity.KindDeployment, contractsv1.ContextFabricSubjectDeployment)
	case contractsv1.ContextFabricEvidenceEntityPullRequest:
		// "<repo_id>:<number>"; the stored id is "pull_request:<repo_id>:<number>"
		// (devhealthsource/tables.go queryPullRequests).
		repo, number, split := strings.Cut(raw, ":")
		if !split || repo == "" || !decimalDigits(number) {
			return embeddedRef{}, false, false
		}
		return ownOrGated(subject, contractsv1.ContextFabricSubjectPullRequest, "pull_request:"+raw)
	case contractsv1.ContextFabricEvidenceEntityIncident:
		// "<incident_id>"; the stored id is "incident:<incident_id>"
		// (devhealthsource/tables.go queryIncidents).
		return ownOrGated(subject, contractsv1.ContextFabricSubjectIncident, "incident:"+raw)
	case contractsv1.ContextFabricEvidenceEntityReview:
		// "<repo_id>:<review_id>" lacks the pull request number the stored
		// id carries, so no canonical id can be derived. It is recognised
		// only as the fact's OWN review, by the subject's own segments; any
		// other review reference is unresolvable (withheld for a
		// repository-restricted caller).
		if subject.Kind == contractsv1.ContextFabricSubjectPullRequestReview {
			if segments, parsed := identity.Segments(identity.KindPullRequestReview, subject.CanonicalID); parsed && len(segments) == 3 &&
				segments[0] != "" && segments[2] != "" && raw == segments[0]+":"+segments[2] {
				return embeddedRef{}, true, true
			}
		}
		return embeddedRef{}, false, false
	default:
		// Evidence that names no subject the gate can decide (a work item
		// dependency edge, a commit, a file) is opaque: withheld for a
		// repository-restricted caller.
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

// resolveRepoScopedEvidence maps "<repo_id>:<entity_id>" evidence to the
// v2 canonical id of kind. The repository uuid carries no ':', so the raw
// value is cut at its FIRST colon; the entity id may contain more.
func resolveRepoScopedEvidence(subject SubjectRef, raw, kind string, subjectKind SubjectKind) (embeddedRef, bool, bool) {
	repo, id, split := strings.Cut(raw, ":")
	if !split || repo == "" || id == "" {
		return embeddedRef{}, false, false
	}
	canonical, omitted, err := identity.Derive(kind, []string{repo, id}, nil)
	if err != nil || omitted {
		return embeddedRef{}, false, false
	}
	return ownOrGated(subject, subjectKind, canonical)
}

// ownOrGated reports a reference as the fact's own when it is exactly the
// fact's subject, and otherwise returns it for the subject gate.
func ownOrGated(subject SubjectRef, kind SubjectKind, canonical string) (embeddedRef, bool, bool) {
	if subject.Kind == kind && subject.CanonicalID == canonical {
		return embeddedRef{}, true, true
	}
	return embeddedRef{kind: kind, canonical: canonical}, true, false
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func filterFact(fact CanonicalFact, capability FactCapability, allows func(embeddedRef, bool) bool, keepUndeclared bool) EmbeddedGatedFact {
	gated := EmbeddedGatedFact{
		RowsWithheld:      map[string]int{},
		ColumnsUndeclared: map[string][]string{},
	}
	fields := make(map[string]FactValue, len(fact.Fields))
	for name, value := range fact.Fields {
		declaration, declared := capability.FieldDeclaration(name, fact.Subject.Kind)
		if !declared {
			if keepUndeclared {
				fields[name] = value
				continue
			}
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
		if declaration.Type == FactFieldTable && (value.Rows != nil || value.Table != nil) {
			value = filterTable(name, declaration, value, allows, &gated, keepUndeclared)
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

func filterTable(name string, declaration FactFieldDeclaration, value FactValue, allows func(embeddedRef, bool) bool, gated *EmbeddedGatedFact, keepUndeclared bool) FactValue {
	undeclared := map[string]struct{}{}
	source := tableRows(value)
	rows := make([]FactValueRow, 0, len(source))
	for _, row := range source {
		keep := true
		cells := make(map[string]FactValue, len(row.Fields))
		for column, cell := range row.Fields {
			columnDeclaration, ok := declaration.Column(column)
			if !ok {
				if keepUndeclared {
					cells[column] = cell
					continue
				}
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
		rows = append(rows, FactValueRow{Fields: cells})
	}
	if len(undeclared) > 0 {
		for column := range undeclared {
			gated.ColumnsUndeclared[name] = append(gated.ColumnsUndeclared[name], column)
		}
		sort.Strings(gated.ColumnsUndeclared[name])
	}
	out := FactValue{Rows: rows}
	if value.Table != nil {
		table := *value.Table
		table.Rows = rows
		out.Table = &table
	}
	return out
}
