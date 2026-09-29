package contextfabric

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Field declarations for direct fact reads (CHAOS-7073, design CHAOS-7036
// sections C.3, C.5 and E.3).
//
// A FactCapability names its tables' shapes (Tables) but, before this, not
// its scalar field names, their types, or which of them name ANOTHER subject.
// A direct read tool serves facts to a client agent with no engine in
// between, so it needs all three:
//
//   - the catalogue shows a client what a kind returns;
//   - a field or table column that is not declared is not served (E.3 rule
//     5), so a producer cannot add a field that leaks past review;
//   - every declared subject reference goes through the subject gate before
//     the response leaves acr-api (E.3 rules 1-3), so a team or project
//     fact cannot name a repository the caller may not see.
//
// A capability with no Fields is not servable by the direct tools: the tool
// refuses the kind with a typed reason rather than guessing its shape.

// FactFieldType is the closed type vocabulary of a declared field or column.
type FactFieldType string

const (
	FactFieldString  FactFieldType = "string"
	FactFieldInteger FactFieldType = "integer"
	FactFieldNumber  FactFieldType = "number"
	FactFieldBoolean FactFieldType = "boolean"
	// FactFieldTable is a field whose value is a declared table (FactValue
	// with Rows). Its columns are declared in Columns.
	FactFieldTable FactFieldType = "table"
)

func validFactFieldType(t FactFieldType) bool {
	switch t {
	case FactFieldString, FactFieldInteger, FactFieldNumber, FactFieldBoolean, FactFieldTable:
		return true
	default:
		return false
	}
}

// FactSubjectIDForm names how a declared subject reference's raw value maps
// to a stored canonical id. The form is declared, never inferred from the
// value, so a value that does not fit its form is refused (fail closed).
type FactSubjectIDForm string

const (
	// FactSubjectIDCanonical: the value already is the stored canonical id.
	FactSubjectIDCanonical FactSubjectIDForm = "canonical"
	// FactSubjectIDRepositoryUUID: a repos.id uuid; canonical is
	// "repository:<uuid>".
	FactSubjectIDRepositoryUUID FactSubjectIDForm = "repository_uuid"
	// FactSubjectIDTeamID: a team id; canonical is "team:<id>".
	FactSubjectIDTeamID FactSubjectIDForm = "team_id"
	// FactSubjectIDOpaque: the value names a subject that the graph gate
	// cannot resolve (for example a provider work scope). A repository-
	// restricted caller never receives a row or field that carries one;
	// an unrestricted caller does (it may see every subject of its org).
	FactSubjectIDOpaque FactSubjectIDForm = "opaque"
)

func validFactSubjectIDForm(form FactSubjectIDForm) bool {
	switch form {
	case FactSubjectIDCanonical, FactSubjectIDRepositoryUUID, FactSubjectIDTeamID, FactSubjectIDOpaque:
		return true
	default:
		return false
	}
}

// CanonicalSubjectID maps a raw reference value to its canonical id under
// form. ok is false for an empty value, an opaque form, or a value that
// already carries a different prefix.
func (form FactSubjectIDForm) CanonicalSubjectID(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	switch form {
	case FactSubjectIDCanonical:
		return raw, true
	case FactSubjectIDRepositoryUUID:
		if strings.Contains(raw, ":") {
			return "", false
		}
		return "repository:" + raw, true
	case FactSubjectIDTeamID:
		if strings.HasPrefix(raw, "team:") {
			return "", false
		}
		return "team:" + raw, true
	default:
		return "", false
	}
}

// FactSubjectRefDeclaration says that a field or column names a subject.
//
// Kind is the subject kind when it is fixed. When the kind depends on a
// sibling column (health's risk_breakdown: scope = "repo" or "team"),
// KindColumn names that column and KindByValue maps its values to kinds; a
// value outside the map makes the reference unresolvable, and the row is
// withheld.
type FactSubjectRefDeclaration struct {
	Kind        SubjectKind                       `json:"kind,omitempty"`
	KindColumn  string                            `json:"kind_column,omitempty"`
	KindByValue map[string]SubjectKind            `json:"kind_by_value,omitempty"`
	IDForm      FactSubjectIDForm                 `json:"id_form"`
	FormByKind  map[SubjectKind]FactSubjectIDForm `json:"form_by_kind,omitempty"`
}

// Resolve returns the kind and id form of one reference, reading the kind
// column from row when the kind is not fixed.
func (d FactSubjectRefDeclaration) Resolve(row map[string]FactValue) (SubjectKind, FactSubjectIDForm, bool) {
	kind := d.Kind
	if d.KindColumn != "" {
		value, ok := row[d.KindColumn]
		if !ok || value.String == nil {
			return "", "", false
		}
		kind, ok = d.KindByValue[*value.String]
		if !ok {
			return "", "", false
		}
	}
	if kind == "" {
		return "", "", false
	}
	form := d.IDForm
	if override, ok := d.FormByKind[kind]; ok {
		form = override
	}
	return kind, form, true
}

// FactColumnDeclaration declares one column of a declared table.
type FactColumnDeclaration struct {
	Name       string                     `json:"name"`
	Type       FactFieldType              `json:"type"`
	Nullable   bool                       `json:"nullable,omitempty"`
	Unit       string                     `json:"unit,omitempty"`
	SubjectRef *FactSubjectRefDeclaration `json:"subject_ref,omitempty"`
}

// FactFieldDeclaration declares one field of a fact of this kind.
type FactFieldDeclaration struct {
	Name string        `json:"name"`
	Type FactFieldType `json:"type"`
	// SubjectKinds limits the declaration to facts about these subject
	// kinds; empty means every supported kind.
	SubjectKinds []SubjectKind `json:"subject_kinds,omitempty"`
	Nullable     bool          `json:"nullable,omitempty"`
	Unit         string        `json:"unit,omitempty"`
	// SubjectRef is set when the value names another subject.
	SubjectRef *FactSubjectRefDeclaration `json:"subject_ref,omitempty"`
	// Score marks a judged value (a severity, a risk). It is served only
	// together with DriversTable (decision K7 and design C.5).
	Score        bool   `json:"score,omitempty"`
	DriversTable string `json:"drivers_table,omitempty"`
	// Freshness marks a value that says when the data was computed or how
	// old it is.
	Freshness bool `json:"freshness,omitempty"`
	// Aggregate marks a scalar computed over every repository a team or
	// project reaches (decision K2): a restricted caller gets it labelled,
	// never recomputed.
	Aggregate bool `json:"aggregate,omitempty"`
	// CallerScoped marks a scalar the provider computes over only the items
	// THIS caller is authorized for (the work-item authorization rule in
	// SQL), so a repository-restricted caller receives its own subset, not
	// the subject's whole population. It is served with a population-scope
	// label, never unlabelled (CHAOS-7120 codex r1 P1). Mutually exclusive
	// with Aggregate.
	CallerScoped bool                    `json:"caller_scoped,omitempty"`
	Columns      []FactColumnDeclaration `json:"columns,omitempty"`
}

// AppliesTo reports whether the declaration covers facts about kind.
func (d FactFieldDeclaration) AppliesTo(kind SubjectKind) bool {
	return len(d.SubjectKinds) == 0 || supportsSubjectKind(d.SubjectKinds, kind)
}

// Column returns the declared column name, if any.
func (d FactFieldDeclaration) Column(name string) (FactColumnDeclaration, bool) {
	for _, column := range d.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return FactColumnDeclaration{}, false
}

// FieldDeclaration returns the capability's declaration of field name for a
// fact about kind.
func (c FactCapability) FieldDeclaration(name string, kind SubjectKind) (FactFieldDeclaration, bool) {
	for _, field := range c.Fields {
		if field.Name == name && field.AppliesTo(kind) {
			return field, true
		}
	}
	return FactFieldDeclaration{}, false
}

// DirectServable reports whether the direct tools may serve this kind.
func (c FactCapability) DirectServable() bool { return len(c.Fields) > 0 }

func validateSubjectRefDeclaration(ref *FactSubjectRefDeclaration, siblings map[string]struct{}) error {
	if ref == nil {
		return nil
	}
	if !validFactSubjectIDForm(ref.IDForm) {
		return fmt.Errorf("subject reference id form %q is invalid", ref.IDForm)
	}
	for kind, form := range ref.FormByKind {
		if !validSubjectKind(kind) || !validFactSubjectIDForm(form) {
			return fmt.Errorf("subject reference form override %q=%q is invalid", kind, form)
		}
	}
	switch {
	case ref.KindColumn == "" && ref.Kind == "":
		return fmt.Errorf("subject reference declares no kind")
	case ref.KindColumn != "" && ref.Kind != "":
		return fmt.Errorf("subject reference declares both a kind and a kind column")
	case ref.KindColumn != "":
		if siblings == nil {
			return fmt.Errorf("subject reference kind column is only valid on a table column")
		}
		if _, ok := siblings[ref.KindColumn]; !ok {
			return fmt.Errorf("subject reference kind column %q is not declared", ref.KindColumn)
		}
		if len(ref.KindByValue) == 0 {
			return fmt.Errorf("subject reference kind column %q maps no value", ref.KindColumn)
		}
		for _, kind := range ref.KindByValue {
			if !validSubjectKind(kind) {
				return fmt.Errorf("subject reference kind %q is invalid", kind)
			}
		}
	default:
		if !validSubjectKind(ref.Kind) {
			return fmt.Errorf("subject reference kind %q is invalid", ref.Kind)
		}
	}
	return nil
}

func validSubjectKind(kind SubjectKind) bool {
	return contractsv1.ValidContextFabricSubjectKind(kind)
}

// validateFieldDeclarations checks one capability's declarations: unique
// names per subject kind, valid types, a table field declares columns and a
// scalar does not, every score names a declared table field as its drivers.
func validateFieldDeclarations(capability FactCapability) error {
	type key struct {
		name string
		kind SubjectKind
	}
	seen := map[key]struct{}{}
	tables := map[string]struct{}{}
	for _, field := range capability.Fields {
		if field.Type == FactFieldTable {
			tables[field.Name] = struct{}{}
		}
	}
	for _, field := range capability.Fields {
		if strings.TrimSpace(field.Name) == "" || strings.TrimSpace(field.Name) != field.Name {
			return fmt.Errorf("fact field name %q is invalid", field.Name)
		}
		if !validFactFieldType(field.Type) {
			return fmt.Errorf("fact field %s type %q is invalid", field.Name, field.Type)
		}
		kinds := field.SubjectKinds
		if len(kinds) == 0 {
			kinds = capability.SupportedSubjectKinds
		}
		for _, kind := range kinds {
			if !supportsSubjectKind(capability.SupportedSubjectKinds, kind) {
				return fmt.Errorf("fact field %s names unsupported subject kind %q", field.Name, kind)
			}
			k := key{field.Name, kind}
			if _, dup := seen[k]; dup {
				return fmt.Errorf("fact field %s is declared twice for subject kind %q", field.Name, kind)
			}
			seen[k] = struct{}{}
		}
		if err := validateSubjectRefDeclaration(field.SubjectRef, nil); err != nil {
			return fmt.Errorf("fact field %s: %w", field.Name, err)
		}
		if field.Aggregate && field.CallerScoped {
			return fmt.Errorf("fact field %s cannot be both aggregate and caller scoped", field.Name)
		}
		if field.Score {
			if _, ok := tables[field.DriversTable]; !ok || field.DriversTable == "" {
				return fmt.Errorf("fact field %s is a score without a declared drivers table", field.Name)
			}
		} else if field.DriversTable != "" {
			return fmt.Errorf("fact field %s names a drivers table but is not a score", field.Name)
		}
		if field.Type != FactFieldTable {
			if len(field.Columns) > 0 {
				return fmt.Errorf("fact field %s is not a table but declares columns", field.Name)
			}
			continue
		}
		if len(field.Columns) == 0 {
			return fmt.Errorf("fact table field %s declares no columns", field.Name)
		}
		columns := map[string]struct{}{}
		for _, column := range field.Columns {
			if strings.TrimSpace(column.Name) == "" {
				return fmt.Errorf("fact table field %s has an unnamed column", field.Name)
			}
			if _, dup := columns[column.Name]; dup {
				return fmt.Errorf("fact table field %s declares column %s twice", field.Name, column.Name)
			}
			if !validFactFieldType(column.Type) || column.Type == FactFieldTable {
				return fmt.Errorf("fact table field %s column %s type %q is invalid", field.Name, column.Name, column.Type)
			}
			columns[column.Name] = struct{}{}
		}
		for _, column := range field.Columns {
			if err := validateSubjectRefDeclaration(column.SubjectRef, columns); err != nil {
				return fmt.Errorf("fact table field %s column %s: %w", field.Name, column.Name, err)
			}
		}
	}
	return nil
}

func copyFieldDeclarations(fields []FactFieldDeclaration) []FactFieldDeclaration {
	if fields == nil {
		return nil
	}
	out := make([]FactFieldDeclaration, len(fields))
	for i, field := range fields {
		field.SubjectKinds = copyProviderSlice(field.SubjectKinds)
		field.SubjectRef = copySubjectRefDeclaration(field.SubjectRef)
		if field.Columns != nil {
			columns := make([]FactColumnDeclaration, len(field.Columns))
			for j, column := range field.Columns {
				column.SubjectRef = copySubjectRefDeclaration(column.SubjectRef)
				columns[j] = column
			}
			field.Columns = columns
		}
		out[i] = field
	}
	return out
}

func copySubjectRefDeclaration(ref *FactSubjectRefDeclaration) *FactSubjectRefDeclaration {
	if ref == nil {
		return nil
	}
	copied := *ref
	if ref.KindByValue != nil {
		copied.KindByValue = make(map[string]SubjectKind, len(ref.KindByValue))
		for k, v := range ref.KindByValue {
			copied.KindByValue[k] = v
		}
	}
	if ref.FormByKind != nil {
		copied.FormByKind = make(map[SubjectKind]FactSubjectIDForm, len(ref.FormByKind))
		for k, v := range ref.FormByKind {
			copied.FormByKind[k] = v
		}
	}
	return &copied
}
