package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

// schemaLoader resolves and caches JSON Schemas from a single directory (either
// contracts/jsonschema/v1 for product wire contracts, or testdata/fullstack/v1/schema for the
// harness-owned agent result schema). It uses github.com/google/jsonschema-go, the Draft
// 2020-12 validator already present in go.sum as an indirect dependency of
// github.com/modelcontextprotocol/go-sdk -- this promotes it to a direct import rather than
// hand-rolling a second schema engine. See docs/fullstack-acceptance.md and the report back
// to the team lead for why: the repository's other validator, internal/contractcheck, has no
// exported entry point that validates an arbitrary in-memory instance against a named schema
// (its Run only re-validates the repository's own fixed golden examples), and this package may
// not modify internal/contractcheck to add one.
//
// jsonschema-go treats "format" (date-time, uri, uuid) as an annotation only, per the Draft
// 2020-12 default vocabulary, and does not assert it -- spec-compliant, but it means a
// safe_uri of "not a uri" would otherwise pass structural validation and then have no host
// for L4's no-outbound-fetch check to scan (Codex finding 11). validateJSON therefore runs a
// second, purpose-built pass (assertFormats below) after structural validation succeeds,
// walking the same schema tree and asserting every "format" keyword it actually finds against
// the corresponding instance value.
type schemaLoader struct {
	dir   string
	cache map[string]*jsonschema.Resolved
}

func newSchemaLoader(dir string) *schemaLoader {
	return &schemaLoader{dir: dir, cache: map[string]*jsonschema.Resolved{}}
}

// resolve loads and resolves the named schema file (e.g. "context_packet.v1.schema.json"),
// following any $ref to a sibling file in the same directory.
func (l *schemaLoader) resolve(filename string) (*jsonschema.Resolved, error) {
	if resolved, ok := l.cache[filename]; ok {
		return resolved, nil
	}
	schema, err := readSchemaFile(filepath.Join(l.dir, filename))
	if err != nil {
		return nil, err
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: l.load})
	if err != nil {
		return nil, fmt.Errorf("resolve schema %s: %w", filename, err)
	}
	l.cache[filename] = resolved
	return resolved, nil
}

// load implements jsonschema.Loader for cross-file $ref targets within the same directory,
// e.g. context_packet.v1.schema.json referring to context_packet_item.v1.schema.json by its
// $id's final path segment.
func (l *schemaLoader) load(uri *url.URL) (*jsonschema.Schema, error) {
	name := path.Base(uri.Path)
	return readSchemaFile(filepath.Join(l.dir, name))
}

func readSchemaFile(path string) (*jsonschema.Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", path, err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("decode schema %s: %w", path, err)
	}
	return &schema, nil
}

// validateJSON decodes data generically (so additionalProperties:false and similar
// structural checks see exactly what is on disk, not just the fields a Go DTO happens to
// know about), validates it against the named schema, then asserts every "format" keyword
// the schema actually declares (see the schemaLoader doc comment for why the second pass
// exists).
func (l *schemaLoader) validateJSON(filename string, data []byte) error {
	resolved, err := l.resolve(filename)
	if err != nil {
		return err
	}
	var instance any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&instance); err != nil {
		return fmt.Errorf("decode instance for schema %s: %w", filename, err)
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf("schema %s: %w", filename, err)
	}
	root, err := readSchemaFile(filepath.Join(l.dir, filename))
	if err != nil {
		return err
	}
	var violations []string
	l.walkFormat(root, instance, "$", &violations)
	if len(violations) > 0 {
		return fmt.Errorf("schema %s: format violation(s): %s", filename, strings.Join(violations, "; "))
	}
	return nil
}

// walkFormat recursively walks schema in step with instance, asserting any "format" keyword
// it encounters against the corresponding string value. It understands the same limited
// keyword subset the actual contract schemas use for format-bearing fields: $ref (same
// cross-file resolution as the main loader), properties, items, and allOf; anyOf/oneOf/if
// are not needed because no format-bearing field in these contracts is guarded by one.
// Unresolvable refs or unsupported format names are skipped rather than treated as failures --
// structural validation (resolved.Validate above) is already the authority on those; this
// pass only adds assertions for formats it positively recognizes.
func (l *schemaLoader) walkFormat(schema *jsonschema.Schema, instance any, path string, violations *[]string) {
	if schema == nil {
		return
	}
	if schema.Ref != "" {
		// Our schema files only $ref a sibling file by bare filename, never a same-document
		// "#/$defs/..." fragment (those only appear in the MCP wrapper schemas, which this
		// package never format-walks); skip rather than mis-resolve one as a filename.
		if !strings.HasPrefix(schema.Ref, "#") {
			if target, err := l.load(&url.URL{Path: schema.Ref}); err == nil {
				l.walkFormat(target, instance, path, violations)
			}
		}
		return
	}
	if schema.Format != "" {
		if s, ok := instance.(string); ok {
			if err := assertFormat(schema.Format, s); err != nil {
				*violations = append(*violations, fmt.Sprintf("%s: %v", path, err))
			}
		}
	}
	switch v := instance.(type) {
	case map[string]any:
		for name, propSchema := range schema.Properties {
			if val, ok := v[name]; ok {
				l.walkFormat(propSchema, val, childPath(path, name), violations)
			}
		}
	case []any:
		if schema.Items != nil {
			for i, item := range v {
				l.walkFormat(schema.Items, item, fmt.Sprintf("%s[%d]", path, i), violations)
			}
		}
	}
	for _, sub := range schema.AllOf {
		l.walkFormat(sub, instance, path, violations)
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// assertFormat checks value against the named JSON Schema format, for the small set this
// package's contracts actually declare (date-time, uri; uuid is supported defensively though
// unused as of this writing). Any other format name is not asserted -- annotation-only, per
// the Draft 2020-12 default vocabulary -- rather than guessed at.
func assertFormat(format, value string) error {
	switch format {
	case "date-time":
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return fmt.Errorf("invalid date-time %q: %w", value, err)
		}
	case "uri":
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" {
			return fmt.Errorf("invalid absolute URI %q", value)
		}
	case "uuid":
		if !uuidPattern.MatchString(value) {
			return fmt.Errorf("invalid UUID %q", value)
		}
	}
	return nil
}

func childPath(parent, property string) string {
	if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(property) {
		return parent + "." + property
	}
	return parent + "[" + strconv.Quote(property) + "]"
}
