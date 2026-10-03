package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// Vendored input and generated output paths, relative to the repository root.
const (
	registryPath     = "contracts/mcp/ops-catalogue/registry.v1.json"
	schemaPath       = "contracts/mcp/ops-catalogue/schema.graphql"
	artifactPath     = "contracts/mcp/operations.v1.json"
	embeddedCopyPath = "internal/contextfabric/directread/operations.v1.json"
	// CHAOS-7075: the SDL copy graphql_query embeds, and its root allowlist.
	embeddedSchemaPath = "internal/contextfabric/directread/ops_schema.v1.json"
	graphqlRootsPath   = "contracts/mcp/graphql_roots.v1.json"
	generatorName      = "cmd/operationpolicy"
	defaultDeadlineS   = 30
	unlistedReasonFmt  = "not on the variable allowlist of %s"
)

// registryFile is the vendored ops registrydump output.
type registryFile struct {
	Source struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		Tool       string `json:"tool"`
		SchemaPath string `json:"schema_path"`
	} `json:"source"`
	Rows []registryRow `json:"rows"`
}

type registryRow struct {
	Operation string `json:"operation"`
	Document  string `json:"document"`
	ConstName string `json:"const_name"`
	Digest    string `json:"digest"`
	Kind      string `json:"kind"`
}

// inputs is everything generation reads. Tests build one from the vendored
// files and change a single piece.
type inputs struct {
	Registry      registryFile
	RegistryBytes []byte
	SDL           []byte
	Policy        policyDeclaration
}

// generate builds the artifact bytes. Any rule violation is an error.
func generate(in inputs) ([]byte, error) {
	file, err := build(in)
	if err != nil {
		return nil, err
	}
	out, err := encode(file)
	if err != nil {
		return nil, err
	}
	// The loader is the second gate: a generated artifact that the runtime
	// would refuse is a generation failure too.
	if _, err := directread.LoadCatalogue(out); err != nil {
		return nil, fmt.Errorf("generated artifact does not load: %w", err)
	}
	return out, nil
}

func encode(file directread.CatalogueFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func build(in inputs) (directread.CatalogueFile, error) {
	rows := map[string]registryRow{}
	for _, row := range in.Registry.Rows {
		if _, dup := rows[row.Operation]; dup {
			return directread.CatalogueFile{}, fmt.Errorf("registry lists %q twice", row.Operation)
		}
		if got := directread.DocumentDigest(row.Document); got != row.Digest {
			return directread.CatalogueFile{}, fmt.Errorf("registry %q: digest %s, recomputed %s", row.Operation, row.Digest, got)
		}
		parsed, err := directread.ParseRegisteredDocument(row.Document)
		if err != nil {
			return directread.CatalogueFile{}, fmt.Errorf("registry %q: %w", row.Operation, err)
		}
		if parsed.Kind != row.Kind {
			return directread.CatalogueFile{}, fmt.Errorf("registry %q: declared kind %q, document parses as %q", row.Operation, row.Kind, parsed.Kind)
		}
		rows[row.Operation] = row
	}

	schema, err := gqlparser.LoadSchema(&ast.Source{Name: schemaPath, Input: string(in.SDL)})
	if err != nil {
		return directread.CatalogueFile{}, fmt.Errorf("schema does not load: %w", err)
	}

	// Every registry row is classified exactly once: served or not served.
	for name := range rows {
		_, served := in.Policy.Served[name]
		_, notServed := in.Policy.NotServed[name]
		if served == notServed {
			return directread.CatalogueFile{}, fmt.Errorf("registry operation %q must be declared exactly once (served=%v, not_served=%v)", name, served, notServed)
		}
	}
	for name := range in.Policy.NotServed {
		if _, ok := rows[name]; !ok {
			return directread.CatalogueFile{}, fmt.Errorf("not_served %q is not in the registry", name)
		}
	}

	file := directread.CatalogueFile{
		Contract:         directread.OperationCatalogueContract,
		Generator:        generatorName,
		PersonNameTokens: directread.PersonNameTokens(),
		Source: directread.CatalogueSource{
			Repository:     in.Registry.Source.Repository,
			Commit:         in.Registry.Source.Commit,
			Tool:           in.Registry.Source.Tool,
			SchemaDigest:   directread.SchemaDigestOf(in.SDL),
			RegistrySHA256: sha256Hex(in.RegistryBytes),
			RegistryRows:   len(rows),
		},
		Operations: []directread.OperationPolicy{},
		NotServed:  []directread.NotServedOperation{},
	}

	names := make([]string, 0, len(in.Policy.Served))
	for name := range in.Policy.Served {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		row, ok := rows[name]
		if !ok {
			return directread.CatalogueFile{}, fmt.Errorf("served operation %q is not in the registry", name)
		}
		op, err := buildOperation(schema, row, in.Policy.Served[name])
		if err != nil {
			return directread.CatalogueFile{}, fmt.Errorf("operation %q: %w", name, err)
		}
		file.Operations = append(file.Operations, op)
	}

	notServed := make([]string, 0, len(in.Policy.NotServed))
	for name := range in.Policy.NotServed {
		notServed = append(notServed, name)
	}
	sort.Strings(notServed)
	for _, name := range notServed {
		decl := in.Policy.NotServed[name]
		row := rows[name]
		if decl.Reason == "" {
			return directread.CatalogueFile{}, fmt.Errorf("not_served %q has no reason", name)
		}
		file.NotServed = append(file.NotServed, directread.NotServedOperation{
			Name: name, Kind: row.Kind, Digest: row.Digest,
			Code: directread.RefusalUnknownOperation, Reason: decl.Reason,
		})
	}
	return file, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func buildOperation(schema *ast.Schema, row registryRow, decl operationDecl) (directread.OperationPolicy, error) {
	parsed, err := directread.ParseRegisteredDocument(row.Document)
	if err != nil {
		return directread.OperationPolicy{}, err
	}
	if parsed.Kind != directread.DocumentKindQuery {
		return directread.OperationPolicy{}, fmt.Errorf("document is a %s; only queries are served", parsed.Kind)
	}
	if parsed.Name != decl.DocumentName {
		return directread.OperationPolicy{}, fmt.Errorf("document operation name %q, declared %q", parsed.Name, decl.DocumentName)
	}
	doc, errs := gqlparser.LoadQuery(schema, row.Document)
	if len(errs) > 0 {
		return directread.OperationPolicy{}, fmt.Errorf("document does not validate against the schema: %v", errs)
	}
	opDef := doc.Operations[0]

	vars, persons, err := buildVariables(schema, opDef, decl)
	if err != nil {
		return directread.OperationPolicy{}, err
	}
	outputs, withheld, err := buildOutputs(schema, opDef, decl)
	if err != nil {
		return directread.OperationPolicy{}, err
	}

	deadline := decl.DeadlineSeconds
	if deadline == 0 {
		deadline = defaultDeadlineS
	}
	op := directread.OperationPolicy{
		Name:                  row.Operation,
		DocumentOperationName: decl.DocumentName,
		Kind:                  parsed.Kind,
		Digest:                row.Digest,
		DocumentText:          row.Document,
		CostClass:             decl.Cost,
		DeadlineSeconds:       deadline,
		MaxInFlightPerOrg:     decl.MaxInFlightPerOrg,
		Variables:             vars,
		PersonVariables:       persons,
		Constraints:           nonNil(decl.Constraints),
		Outputs:               outputs,
		WithheldOutputs:       withheld,
		Disclosure:            nonNil(decl.Disclosure),
		Notes:                 decl.Notes,
	}
	unrestricted := decl.Unrestricted
	unrestricted.Caller = directread.CallerUnrestricted
	unrestricted.Served = true
	unrestricted.Refusal = nil
	restricted := decl.Restricted
	restricted.Caller = directread.CallerRestricted
	op.Scopes = []directread.CallerScope{unrestricted, restricted}
	return op, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// variableNode is one input path found by walking the SDL.
type variableNode struct {
	Path    string
	Type    string
	Kind    string
	Enum    []string
	Default string
}

// walkVariables lists every input path of an operation's variables,
// arrays as [*] and nested input objects included.
func walkVariables(schema *ast.Schema, opDef *ast.OperationDefinition) ([]variableNode, error) {
	var nodes []variableNode
	var walk func(path string, typ *ast.Type, def *ast.Value, chain []string) error
	walk = func(path string, typ *ast.Type, def *ast.Value, chain []string) error {
		base := path
		named := typ
		for named.Elem != nil {
			base += "[*]"
			named = named.Elem
		}
		definition := schema.Types[named.NamedType]
		if definition == nil {
			return fmt.Errorf("variable path %q: type %q not in schema", path, named.NamedType)
		}
		node := variableNode{Path: path, Type: typ.String()}
		if def != nil {
			node.Default = def.String()
		}
		switch definition.Kind {
		case ast.Scalar:
			node.Kind = directread.VariableKindScalar
		case ast.Enum:
			node.Kind = directread.VariableKindEnum
			for _, v := range definition.EnumValues {
				node.Enum = append(node.Enum, v.Name)
			}
		case ast.InputObject:
			node.Kind = directread.VariableKindObject
		default:
			return fmt.Errorf("variable path %q: type %q of kind %s is not an input type", path, named.NamedType, definition.Kind)
		}
		nodes = append(nodes, node)
		if definition.Kind != ast.InputObject {
			return nil
		}
		if slices.Contains(chain, definition.Name) {
			return fmt.Errorf("variable path %q: recursive input type %q", path, definition.Name)
		}
		for _, field := range definition.Fields {
			if err := walk(base+"."+field.Name, field.Type, field.DefaultValue, append(slices.Clone(chain), definition.Name)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, vd := range opDef.VariableDefinitions {
		if err := walk(vd.Variable, vd.Type, vd.DefaultValue, nil); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// personVariables lists the person-scoped inputs the SDL walk finds: a
// person-named path segment, or an enum value that names a person.
func personVariables(nodes []variableNode) []directread.PersonVariable {
	out := []directread.PersonVariable{}
	for _, n := range nodes {
		if slices.ContainsFunc(strings.Split(strings.ReplaceAll(n.Path, "[*]", ""), "."), directread.IsPersonNamed) {
			out = append(out, directread.PersonVariable{Path: n.Path})
			continue
		}
		for _, v := range n.Enum {
			if directread.IsPersonNamed(v) {
				out = append(out, directread.PersonVariable{Path: n.Path, Value: v})
			}
		}
	}
	return out
}

const personReason = "person-scoped input: the MCP surface serves no person scope (design D.5, K1)"

func buildVariables(schema *ast.Schema, opDef *ast.OperationDefinition, decl operationDecl) ([]directread.VariableRule, []directread.PersonVariable, error) {
	nodes, err := walkVariables(schema, opDef)
	if err != nil {
		return nil, nil, err
	}
	persons := personVariables(nodes)
	personPath := map[string]bool{}
	personValues := map[string][]string{}
	for _, p := range persons {
		if p.Value == "" {
			personPath[p.Path] = true
		} else {
			personValues[p.Path] = append(personValues[p.Path], p.Value)
		}
	}
	known := map[string]variableNode{}
	for _, n := range nodes {
		known[n.Path] = n
	}
	for path := range decl.Variables {
		if _, ok := known[path]; !ok {
			return nil, nil, fmt.Errorf("declared variable %q is not an input path of the document", path)
		}
	}
	for path := range decl.RefusedPaths {
		if _, ok := known[path]; !ok {
			return nil, nil, fmt.Errorf("declared refused variable %q is not an input path of the document", path)
		}
	}

	// An object node is allowed when it is declared or holds an allowed
	// descendant; a declared refusal on an object forbids any descendant.
	allowedLeaf := func(path string) bool {
		_, ok := decl.Variables[path]
		return ok
	}
	rules := make([]directread.VariableRule, 0, len(nodes))
	for _, n := range nodes {
		rule := directread.VariableRule{Path: n.Path, Type: n.Type, Kind: n.Kind, Enum: n.Enum, Default: n.Default, Source: directread.SourceNotSettable}
		d, declared := decl.Variables[n.Path]
		if n.Kind == directread.VariableKindObject && !declared {
			for p := range decl.Variables {
				if strings.HasPrefix(p, n.Path+".") || strings.HasPrefix(p, n.Path+"[*].") {
					declared = true
					break
				}
			}
		}
		for ancestor := range decl.RefusedPaths {
			if declared && (strings.HasPrefix(n.Path, ancestor+".") || strings.HasPrefix(n.Path, ancestor+"[*].")) {
				return nil, nil, fmt.Errorf("variable %q is allowed under the refused path %q", n.Path, ancestor)
			}
		}
		if declared {
			if personPath[n.Path] {
				return nil, nil, fmt.Errorf("person variable %q is on the allowlist", n.Path)
			}
			if n.Kind == directread.VariableKindEnum {
				if allowedLeaf(n.Path) && len(d.AllowedValues) == 0 {
					return nil, nil, fmt.Errorf("enum variable %q allowed without an explicit value allowlist", n.Path)
				}
				for _, v := range d.AllowedValues {
					if directread.IsPersonNamed(v) {
						return nil, nil, fmt.Errorf("person value %s=%s is on the allowlist", n.Path, v)
					}
					if !slices.Contains(n.Enum, v) {
						return nil, nil, fmt.Errorf("allowed value %s=%s is not in the schema enum", n.Path, v)
					}
				}
			}
			if _, refused := decl.RefusedPaths[n.Path]; refused {
				return nil, nil, fmt.Errorf("variable %q is both allowed and refused", n.Path)
			}
			rule.Allowed = true
			rule.Source = d.Source
			if rule.Source == "" {
				rule.Source = directread.SourceClient
			}
			rule.ForcedValue = d.ForcedValue
			rule.AllowedValues = d.AllowedValues
			rule.Min, rule.Max = d.Min, d.Max
			rule.MaxItems, rule.MaxLength = d.MaxItems, d.MaxLength
			rule.Subject = d.Subject
			if rule.Source == directread.SourceForced && len(rule.ForcedValue) == 0 {
				return nil, nil, fmt.Errorf("variable %q forced without a value", n.Path)
			}
		} else {
			switch {
			case personPath[n.Path]:
				rule.Refusal = &directread.Refusal{Code: directread.RefusalPersonScopeNotServed, Reason: personReason}
			default:
				if r, ok := refusedSelfOrAncestor(decl.RefusedPaths, n.Path); ok {
					refusal := r
					rule.Refusal = &refusal
				} else {
					rule.Refusal = &directread.Refusal{Code: directread.RefusalVariableNotAllowed, Reason: fmt.Sprintf(unlistedReasonFmt, opDef.Name)}
				}
			}
		}
		if n.Kind == directread.VariableKindEnum {
			for _, v := range personValues[n.Path] {
				rule.RefusedValues = append(rule.RefusedValues, directread.ValueRefusal{Value: v, Code: directread.RefusalPersonScopeNotServed, Reason: personReason})
			}
			for _, rv := range d.RefusedValues {
				if slices.Contains(personValues[n.Path], rv.Value) {
					continue
				}
				if !slices.Contains(n.Enum, rv.Value) {
					return nil, nil, fmt.Errorf("refused value %s=%s is not in the schema enum", n.Path, rv.Value)
				}
				rule.RefusedValues = append(rule.RefusedValues, rv)
			}
		}
		rules = append(rules, rule)
	}
	return rules, persons, nil
}

// outputNode is one response leaf found by walking the selection set.
type outputNode struct {
	Path     string
	Type     string
	Leaf     directread.OutputLeaf
	Segments []string
	Beyond   bool
}

// walkOutputs lists every response leaf path of a validated document, from
// the selection set (response keys, aliases honoured, fragments inlined).
func walkOutputs(opDef *ast.OperationDefinition) ([]outputNode, error) {
	var out []outputNode
	seen := map[string]bool{}
	var walk func(prefix string, segments []string, set ast.SelectionSet) error
	walk = func(prefix string, segments []string, set ast.SelectionSet) error {
		for _, sel := range set {
			switch s := sel.(type) {
			case *ast.Field:
				key := s.Alias
				if key == "" {
					key = s.Name
				}
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				segs := append(slices.Clone(segments), key, s.Name)
				if s.Definition == nil || s.Definition.Type == nil {
					return fmt.Errorf("output %q has no schema definition", path)
				}
				typ := s.Definition.Type
				for t := typ; t.Elem != nil; t = t.Elem {
					path += "[*]"
				}
				if len(s.SelectionSet) > 0 {
					if err := walk(path, segs, s.SelectionSet); err != nil {
						return err
					}
					continue
				}
				leaf := directread.LeafScalar
				switch {
				case s.Name == "__typename":
					leaf = directread.LeafTypename
				case typ.Name() == "JSON":
					leaf = directread.LeafJSON
				}
				if !seen[path] {
					seen[path] = true
					out = append(out, outputNode{Path: path, Type: typ.String(), Leaf: leaf, Segments: segs})
				}
			case *ast.InlineFragment:
				if err := walk(prefix, segments, s.SelectionSet); err != nil {
					return err
				}
			case *ast.FragmentSpread:
				if s.Definition == nil {
					return fmt.Errorf("fragment %q unresolved", s.Name)
				}
				if err := walk(prefix, segments, s.Definition.SelectionSet); err != nil {
					return err
				}
			default:
				return fmt.Errorf("selection %T not handled", sel)
			}
		}
		return nil
	}
	if err := walk("", nil, opDef.SelectionSet); err != nil {
		return nil, err
	}
	return out, nil
}

// additionalOutputNodes resolves the declared output paths the registered
// document does not select against the SDL. graphql_query rebuilds the
// client's selection, so an SDL leaf listed here is servable there while the
// registered document, and its pinned digest, stay as ops registered them.
func additionalOutputNodes(schema *ast.Schema, opDef *ast.OperationDefinition, paths []string, have []outputNode) ([]outputNode, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(opDef.SelectionSet) != 1 {
		return nil, fmt.Errorf("additional outputs need a single root field, document has %d", len(opDef.SelectionSet))
	}
	rootSel, ok := opDef.SelectionSet[0].(*ast.Field)
	if !ok || rootSel.Definition == nil {
		return nil, errors.New("additional outputs: root selection is not a resolved field")
	}
	seen := map[string]bool{}
	for _, n := range have {
		seen[n.Path] = true
	}
	var out []outputNode
	for _, path := range paths {
		if seen[path] {
			return nil, fmt.Errorf("additional output %q is already selected by the document", path)
		}
		seen[path] = true
		parts := strings.Split(path, ".")
		if parts[0] != rootSel.Alias && parts[0] != rootSel.Name {
			return nil, fmt.Errorf("additional output %q does not start at root field %q", path, rootSel.Name)
		}
		def := rootSel.Definition
		segs := []string{parts[0], rootSel.Name}
		want := parts[0]
		for t := def.Type; t.Elem != nil; t = t.Elem {
			want += "[*]"
		}
		for _, raw := range parts[1:] {
			typeDef := schema.Types[def.Type.Name()]
			if typeDef == nil || typeDef.Kind != ast.Object {
				return nil, fmt.Errorf("additional output %q: %q is not an object type", path, def.Type.Name())
			}
			name := strings.TrimSuffix(raw, "[*]")
			next := typeDef.Fields.ForName(name)
			if next == nil {
				return nil, fmt.Errorf("additional output %q: %s has no field %q", path, typeDef.Name, name)
			}
			def = next
			segs = append(segs, name, name)
			want += "." + name
			for t := def.Type; t.Elem != nil; t = t.Elem {
				want += "[*]"
			}
		}
		if want != path {
			return nil, fmt.Errorf("additional output %q is not the SDL path %q", path, want)
		}
		leafDef := schema.Types[def.Type.Name()]
		if leafDef == nil || (leafDef.Kind != ast.Scalar && leafDef.Kind != ast.Enum) {
			return nil, fmt.Errorf("additional output %q is not a leaf", path)
		}
		leaf := directread.LeafScalar
		if def.Type.Name() == "JSON" {
			leaf = directread.LeafJSON
		}
		out = append(out, outputNode{Path: path, Type: def.Type.String(), Leaf: leaf, Segments: segs, Beyond: true})
	}
	return out, nil
}

// errPersonOutput marks the output-path rule failure (T17).
var errPersonOutput = errors.New("person-named or free JSON output path without a written exception")

func buildOutputs(schema *ast.Schema, opDef *ast.OperationDefinition, decl operationDecl) ([]directread.OutputPath, []directread.WithheldOutput, error) {
	nodes, err := walkOutputs(opDef)
	if err != nil {
		return nil, nil, err
	}
	extra, err := additionalOutputNodes(schema, opDef, decl.AdditionalOutputs, nodes)
	if err != nil {
		return nil, nil, err
	}
	nodes = append(nodes, extra...)
	known := map[string]bool{}
	for _, n := range nodes {
		known[n.Path] = true
	}
	for path := range decl.WithheldOutputs {
		if !known[path] {
			return nil, nil, fmt.Errorf("withheld output %q is not selected by the document", path)
		}
	}
	for path := range decl.OutputExceptions {
		if !known[path] {
			return nil, nil, fmt.Errorf("output exception %q is not selected by the document", path)
		}
	}
	outputs := []directread.OutputPath{}
	withheld := []directread.WithheldOutput{}
	var failing []string
	for _, n := range nodes {
		if reason, ok := decl.WithheldOutputs[n.Path]; ok {
			withheld = append(withheld, directread.WithheldOutput{Path: n.Path, Reason: reason})
			continue
		}
		exception := decl.OutputExceptions[n.Path]
		person := slices.ContainsFunc(n.Segments, directread.IsPersonNamed)
		if (person || n.Leaf == directread.LeafJSON) && exception == "" {
			failing = append(failing, n.Path)
			continue
		}
		outputs = append(outputs, directread.OutputPath{Path: n.Path, Type: n.Type, Leaf: n.Leaf, Exception: exception, BeyondDocument: n.Beyond})
	}
	if len(failing) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", errPersonOutput, strings.Join(failing, ", "))
	}
	return outputs, withheld, nil
}

// refusedSelfOrAncestor returns the declared refusal of a path or of its
// nearest refused ancestor, so every path under a refused object carries
// the object's code (sankey's children answer basis_dependent_shape too).
func refusedSelfOrAncestor(refused map[string]directread.Refusal, path string) (directread.Refusal, bool) {
	for p := path; p != ""; p = parentVariablePath(p) {
		if r, ok := refused[p]; ok {
			return r, true
		}
	}
	return directread.Refusal{}, false
}

func parentVariablePath(path string) string {
	if trimmed, ok := strings.CutSuffix(path, "[*]"); ok {
		return trimmed
	}
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	return ""
}
