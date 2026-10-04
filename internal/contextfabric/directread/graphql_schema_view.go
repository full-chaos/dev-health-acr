package directread

// The data_catalog "schema" section (CHAOS-7075; design D.8 "Schema for the
// client", lead ruling R3: the section only, no static MCP resource). It is
// the allowed part of the ops SDL for THIS caller class, printed from the
// same derived root policy graphql_query enforces: the roots served to the
// class, each argument with its allowed input paths (the run_operation
// variable rules, renamed to the argument), the fixed literal arguments, the
// allowed output paths, and an SDL text holding only those roots and the
// allowed fields of each reachable output type. A root, argument path or
// field the section does not list is refused by graphql_query.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// CatalogSectionSchema names the section.
const CatalogSectionSchema = "schema"

// CatalogUnavailableGraphQLNotConfigured is the section's reason when
// graphql_query cannot serve in this deployment.
const CatalogUnavailableGraphQLNotConfigured = "data_graphql_not_configured"

// CatalogSchema is the schema section.
type CatalogSchema struct {
	CallerClass       CallerClass         `json:"caller_class"`
	Available         bool                `json:"available"`
	Reason            string              `json:"reason,omitempty"`
	SchemaDigest      string              `json:"schema_digest"`
	Limits            GraphQLLimits       `json:"limits"`
	Roots             []CatalogSchemaRoot `json:"roots"`
	RefusedRootFields []string            `json:"refused_root_fields"`
	SDL               string              `json:"sdl"`
}

// CatalogSchemaRoot is one root field served to this caller class.
type CatalogSchemaRoot struct {
	Field          string                  `json:"field"`
	Operations     []string                `json:"operations"`
	CostClass      CostClass               `json:"cost_class"`
	ScopeClass     ScopeClass              `json:"scope_class"`
	ForcedArgument string                  `json:"forced_argument,omitempty"`
	RowIDPaths     []string                `json:"row_id_paths"`
	Arguments      []CatalogSchemaArgument `json:"arguments"`
	OutputPaths    []string                `json:"output_paths"`
}

// CatalogSchemaArgument is one root argument: a fixed literal, or an
// argument whose allowed input paths are listed (orgId and forced paths are
// set by acr and are not listed).
type CatalogSchemaArgument struct {
	Name  string            `json:"name"`
	Type  string            `json:"type"`
	Fixed string            `json:"fixed,omitempty"`
	Paths []CatalogVariable `json:"paths"`
}

// BuildCatalogSchema builds the section. policy may be nil (it did not
// derive): the section then lists nothing and says not configured.
func BuildCatalogSchema(policy *GraphQLPolicy, class CallerClass, servable, gate, dataRead bool) *CatalogSchema {
	section := &CatalogSchema{CallerClass: class, Available: true, Roots: []CatalogSchemaRoot{}, RefusedRootFields: []string{}}
	switch {
	case policy == nil || !servable:
		section.Available, section.Reason = false, CatalogUnavailableGraphQLNotConfigured
	case !gate:
		section.Available, section.Reason = false, CatalogUnavailableGateMissing
	case !dataRead:
		section.Available, section.Reason = false, CatalogUnavailableScopeMissing
	}
	// No detail at all for a caller graphql_query cannot serve (pr2 r1
	// P1): without data:read, or without a composed runner, the section
	// says why and lists nothing -- no root, argument, path or SDL.
	if policy == nil || !section.Available {
		return section
	}
	section.SchemaDigest = policy.catalogue.StampedSchemaDigest()
	section.Limits = policy.limits
	types := map[string]map[string]string{}  // object type -> field -> SDL type
	inputs := map[string]map[string]string{} // input type -> allowed field -> SDL type
	enums := map[string]map[string]bool{}    // enum -> values to print (empty = all)
	var queryLines []string
	for _, root := range policy.Roots() {
		var served []*rootCandidate
		for _, c := range root.candidates {
			if c.op.Scope(class).Served {
				served = append(served, c)
			}
		}
		if len(served) == 0 {
			section.RefusedRootFields = append(section.RefusedRootFields, root.Field)
			continue
		}
		entry := CatalogSchemaRoot{Field: root.Field, CostClass: root.CostClass(), ScopeClass: ScopeOrgWide, RowIDPaths: []string{}, Arguments: []CatalogSchemaArgument{}, OutputPaths: []string{}}
		outputs := map[string]bool{}
		args := map[string]*CatalogSchemaArgument{}
		for _, c := range served {
			entry.Operations = append(entry.Operations, c.op.Name)
			scope := c.op.Scope(class)
			if class == CallerRestricted {
				entry.ScopeClass = ScopeForcedGrant
				entry.ForcedArgument = argumentPath(c, scope.ForcedVariablePath)
				entry.RowIDPaths = append(entry.RowIDPaths, scope.RowIDPaths...)
			}
			for _, out := range c.op.Outputs {
				outputs[out.Path] = true
			}
			for name, lit := range c.literals {
				if _, ok := args[name]; !ok {
					args[name] = &CatalogSchemaArgument{Name: name, Type: argumentType(policy.schema, root.Field, name), Fixed: lit.String(), Paths: []CatalogVariable{}}
				}
			}
			for name, variable := range c.bindings {
				arg, ok := args[name]
				if !ok || arg.Fixed != "" {
					arg = &CatalogSchemaArgument{Name: name, Type: c.varTypes[variable], Paths: []CatalogVariable{}}
					args[name] = arg
				}
				seen := map[string]bool{}
				for _, p := range arg.Paths {
					seen[p.Path] = true
				}
				for _, rule := range c.op.Variables {
					if !rule.Allowed || rule.Source != SourceClient || rule.Kind == VariableKindObject {
						continue
					}
					top, rest, _ := strings.Cut(rule.Path, ".")
					if strings.TrimSuffix(top, "[*]") != variable {
						continue
					}
					path := name
					if rest != "" {
						path += "." + rest
					}
					if seen[path] {
						continue
					}
					seen[path] = true
					v := CatalogVariable{Path: path, Type: rule.Type, Default: rule.EffectiveDefault(), Min: rule.Min, Max: rule.Max, MaxItems: rule.MaxItems, MaxLength: rule.MaxLength}
					if rule.Kind == VariableKindEnum {
						v.EnumValues = append([]string{}, rule.AllowedValues...)
					}
					if rule.Subject != nil {
						v.SubjectKind = rule.Subject.Kind
					}
					arg.Paths = append(arg.Paths, v)
				}
				sort.Slice(arg.Paths, func(i, j int) bool { return arg.Paths[i].Path < arg.Paths[j].Path })
			}
		}
		for _, name := range sortedKeysOf(args) {
			entry.Arguments = append(entry.Arguments, *args[name])
		}
		for path := range outputs {
			entry.OutputPaths = append(entry.OutputPaths, path)
		}
		sort.Strings(entry.OutputPaths)
		sort.Strings(entry.RowIDPaths)
		entry.RowIDPaths = sortedUnique(entry.RowIDPaths)
		section.Roots = append(section.Roots, entry)
		queryLines = append(queryLines, sdlRootLine(policy.schema, root.Field, entry.Arguments))
		collectOutputTypes(policy.schema, root.Field, entry.OutputPaths, types)
		collectInputTypes(policy.schema, root.Field, entry.Arguments, inputs, enums)
	}
	section.RefusedRootFields = append(section.RefusedRootFields, policy.RefusedRootFields()...)
	sort.Strings(section.RefusedRootFields)
	section.SDL = printAllowedSDL(policy.schema, queryLines, types, inputs, enums)
	return section
}

func sortedKeysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func argumentType(schema *ast.Schema, root, arg string) string {
	if def := schema.Query.Fields.ForName(root); def != nil {
		if a := def.Arguments.ForName(arg); a != nil {
			return a.Type.String()
		}
	}
	return ""
}

// sdlRootLine prints one Query field with the arguments a client may write
// (fixed ones are shown with their only value as a default).
func sdlRootLine(schema *ast.Schema, root string, args []CatalogSchemaArgument) string {
	def := schema.Query.Fields.ForName(root)
	var parts []string
	for _, a := range args {
		if a.Fixed != "" {
			parts = append(parts, fmt.Sprintf("%s: %s = %s", a.Name, a.Type, a.Fixed))
			continue
		}
		if len(a.Paths) == 0 {
			continue // set by acr only (orgId)
		}
		parts = append(parts, a.Name+": "+strings.TrimSuffix(a.Type, "!"))
	}
	line := "  " + root
	if len(parts) > 0 {
		line += "(" + strings.Join(parts, ", ") + ")"
	}
	return line + ": " + def.Type.String()
}

// collectOutputTypes records, per object type, the allowed fields the
// output paths of one root reach.
func collectOutputTypes(schema *ast.Schema, root string, paths []string, types map[string]map[string]string) {
	def := schema.Query.Fields.ForName(root)
	for _, path := range paths {
		segs := strings.Split(strings.ReplaceAll(path, "[*]", ""), ".")
		typeName := def.Type.Name()
		for _, seg := range segs[1:] {
			typ := schema.Types[typeName]
			if typ == nil {
				break
			}
			field := typ.Fields.ForName(seg)
			if seg == "__typename" {
				break
			}
			if field == nil {
				break
			}
			if types[typeName] == nil {
				types[typeName] = map[string]string{}
			}
			types[typeName][seg] = field.Type.String()
			typeName = field.Type.Name()
		}
	}
}

// collectInputTypes records, per input object type, the fields the allowed
// argument paths reach, and the enum values a path or a fixed literal
// admits.
func collectInputTypes(schema *ast.Schema, root string, args []CatalogSchemaArgument, inputs map[string]map[string]string, enums map[string]map[string]bool) {
	def := schema.Query.Fields.ForName(root)
	for _, arg := range args {
		argDef := def.Arguments.ForName(arg.Name)
		if argDef == nil {
			continue
		}
		if arg.Fixed != "" {
			addEnumValues(schema, argDef.Type.Name(), []string{arg.Fixed}, enums)
			continue
		}
		for _, p := range arg.Paths {
			segs := strings.Split(strings.ReplaceAll(p.Path, "[*]", ""), ".")
			typeName := argDef.Type.Name()
			for _, seg := range segs[1:] {
				typ := schema.Types[typeName]
				if typ == nil || typ.Kind != ast.InputObject {
					break
				}
				field := typ.Fields.ForName(seg)
				if field == nil {
					break
				}
				if inputs[typeName] == nil {
					inputs[typeName] = map[string]string{}
				}
				inputs[typeName][seg] = field.Type.String()
				typeName = field.Type.Name()
			}
			addEnumValues(schema, typeName, p.EnumValues, enums)
		}
	}
}

// addEnumValues records values of an enum type (none = every value).
func addEnumValues(schema *ast.Schema, typeName string, values []string, enums map[string]map[string]bool) {
	typ := schema.Types[typeName]
	if typ == nil || typ.Kind != ast.Enum {
		return
	}
	if enums[typeName] == nil {
		enums[typeName] = map[string]bool{}
	}
	if len(values) == 0 {
		for _, v := range typ.EnumValues {
			enums[typeName][v.Name] = true
		}
		return
	}
	for _, v := range values {
		enums[typeName][v] = true
	}
}

// printAllowedSDL prints a self-contained SDL of the allowed part: the
// custom scalars and enums it references, the input types with their
// allowed fields, Query, and the output types with their allowed fields.
func printAllowedSDL(schema *ast.Schema, queryLines []string, types, inputs map[string]map[string]string, enums map[string]map[string]bool) string {
	scalars := map[string]bool{}
	note := func(typeText string) {
		name := strings.Trim(typeText, "[]!")
		typ := schema.Types[name]
		if typ == nil {
			return
		}
		switch typ.Kind {
		case ast.Scalar:
			switch name {
			case "Int", "Float", "String", "Boolean", "ID":
			default:
				scalars[name] = true
			}
		case ast.Enum:
			if enums[name] == nil {
				addEnumValues(schema, name, nil, enums)
			}
		}
	}
	for _, fields := range types {
		for _, t := range fields {
			note(t)
		}
	}
	for _, fields := range inputs {
		for _, t := range fields {
			note(t)
		}
	}
	for _, line := range queryLines {
		for _, part := range strings.FieldsFunc(line, func(r rune) bool { return r == '(' || r == ')' || r == ',' || r == ':' || r == ' ' || r == '=' }) {
			note(part)
		}
	}
	var b strings.Builder
	b.WriteString("# The allowed part of the ops schema for this credential (graphql_query).\n")
	b.WriteString("# orgId is set by acr from your credential; do not send it. __typename is allowed where listed.\n")
	b.WriteString("# A type's fields are the union over the roots; each root's output_paths is the exact allowlist.\n")
	for _, name := range sortedKeysOf(scalars) {
		b.WriteString("\nscalar " + name + "\n")
	}
	for _, name := range sortedKeysOf(enums) {
		b.WriteString("\nenum " + name + " {\n")
		for _, v := range sortedKeysOf(enums[name]) {
			b.WriteString("  " + v + "\n")
		}
		b.WriteString("}\n")
	}
	for _, name := range sortedKeysOf(inputs) {
		b.WriteString("\ninput " + name + " {\n")
		fields := inputs[name]
		for _, f := range sortedKeysOf(fields) {
			b.WriteString("  " + f + ": " + fields[f] + "\n")
		}
		b.WriteString("}\n")
	}
	b.WriteString("\ntype Query {\n")
	for _, line := range queryLines {
		b.WriteString(line + "\n")
	}
	b.WriteString("}\n")
	for _, name := range sortedKeysOf(types) {
		b.WriteString("\ntype " + name + " {\n")
		fields := types[name]
		for _, f := range sortedKeysOf(fields) {
			b.WriteString("  " + f + ": " + fields[f] + "\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}
