package mcp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	vocabAdditive = "additive"
	vocabFrozen   = "frozen"
)

// outputVocabulary classifies one output-schema vocabulary. An additive one
// is an open string with documented values (x-documented-values), so a
// client that cached tools/list keeps validating after the server adds a
// value. A frozen one keeps its enum and carries the reason it cannot grow:
// protocol, state-machine, request-echo or scoring.
type outputVocabulary struct {
	name  string
	kind  string
	why   string
	sites []string
	// goSet is the Go constant set served for this vocabulary. Frozen: must
	// equal the schema enum. Additive: must be inside the documented values;
	// with exhaustive it must also equal them.
	goSet      []string
	exhaustive bool
	// schemaOnly names why no Go constant set can be pinned; it is reported
	// as NOT pinned.
	schemaOnly string
	// clientValidate names, for an additive vocabulary, the Go Validate a
	// client runs on a served response that carries it (a key of
	// clientValidators), or "none: <reason>" when no Go client checks it.
	// That Validate must admit an added value.
	clientValidate string
}

var outputVocabularies []outputVocabulary

func registerOutputVocabularies(v ...outputVocabulary) {
	outputVocabularies = append(outputVocabularies, v...)
}

type vocabSite struct {
	id         string
	file       string
	pointer    string
	node       map[string]any
	enum       []string
	documented []string
}

var vocabWrapperDefs = map[string]bool{
	"context_fabric_common.v1":            true,
	"context_fabric_answer_projection.v1": true,
}

func vocabSiteID(tool, pointer string) string {
	var parts []string
	for _, p := range strings.Split(pointer, "/") {
		if p == "" || p == "properties" || p == "$defs" {
			continue
		}
		parts = append(parts, p)
	}
	if len(parts) > 1 && vocabWrapperDefs[parts[0]] {
		if strings.Contains(pointer, "/$defs/"+parts[0]+"/$defs/") {
			parts = parts[1:]
		}
	}
	id := strings.Join(parts, ".")
	if strings.HasPrefix(pointer, "/properties/") {
		return tool + ":" + id
	}
	return id
}

func vocabIsCondition(pointer string) bool {
	for _, p := range strings.Split(pointer, "/") {
		switch p {
		case "if", "then", "else", "not":
			return true
		}
	}
	return false
}

func vocabStrings(t *testing.T, site string, raw any) []string {
	t.Helper()
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s: vocabulary is %T, want a list", site, raw)
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s: vocabulary value %v is %T, want string", site, v, v)
		}
		out[i] = s
	}
	return out
}

func collectVocabSites(t *testing.T) []vocabSite {
	t.Helper()
	var manifest struct {
		Tools []struct {
			Name            string `json:"name"`
			OutputSchemaRef string `json:"output_schema_ref"`
		} `json:"tools"`
	}
	raw, err := schemaFiles.ReadFile("schemas/tools.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var sites []vocabSite
	seenFile := map[string]bool{}
	for _, tool := range manifest.Tools {
		file := "schemas/" + tool.OutputSchemaRef[strings.LastIndex(tool.OutputSchemaRef, "/")+1:]
		if seenFile[file] {
			continue
		}
		seenFile[file] = true
		short := strings.TrimSuffix(strings.TrimPrefix(file, "schemas/mcp_"), "_response.v1.schema.json")
		doc := readEmbeddedJSON(t, file)
		var walk func(node any, pointer string)
		walk = func(node any, pointer string) {
			switch n := node.(type) {
			case map[string]any:
				_, hasEnum := n["enum"]
				_, hasDoc := n["x-documented-values"]
				if (hasEnum || hasDoc) && !vocabIsCondition(pointer) {
					site := vocabSite{id: vocabSiteID(short, pointer), file: file, pointer: pointer, node: n}
					if hasEnum {
						site.enum = vocabStrings(t, site.id, n["enum"])
					}
					if hasDoc {
						site.documented = vocabStrings(t, site.id, n["x-documented-values"])
					}
					sites = append(sites, site)
				}
				for k, v := range n {
					if k == "enum" || k == "x-documented-values" {
						continue
					}
					walk(v, pointer+"/"+k)
				}
			case []any:
				for i, v := range n {
					walk(v, fmt.Sprintf("%s/%d", pointer, i))
				}
			}
		}
		walk(doc, "")
	}
	if len(sites) < 150 {
		t.Fatalf("only %d vocabulary sites found; the measurement did not happen", len(sites))
	}
	return sites
}

func (v outputVocabulary) matches(id string) bool {
	for _, s := range v.sites {
		if id == s || strings.HasSuffix(id, "."+s) {
			return true
		}
	}
	return false
}

func vocabFor(t *testing.T, id string) outputVocabulary {
	t.Helper()
	var found []outputVocabulary
	for _, v := range outputVocabularies {
		if v.matches(id) {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		names := []string{}
		for _, v := range found {
			names = append(names, v.name)
		}
		t.Fatalf("%s: matched by %d vocabularies %v, want exactly 1", id, len(found), names)
	}
	return found[0]
}

func TestOutputVocabularyEverySchemaSiteIsClassified(t *testing.T) {
	sites := collectVocabSites(t)
	used := map[string]bool{}
	for _, s := range sites {
		if v, n := firstVocab(s.id); n == 0 {
			t.Errorf("%s (%s %s): enum without a classification; register it in the output vocabulary registry as additive or frozen", s.id, s.file, s.pointer)
		} else if n > 1 {
			t.Errorf("%s: matched by %d vocabularies", s.id, n)
		} else {
			used[v.name] = true
		}
	}
	for _, v := range outputVocabularies {
		if !used[v.name] {
			t.Errorf("vocabulary %q matches no schema site; remove the stale entry", v.name)
		}
		if v.kind != vocabAdditive && v.kind != vocabFrozen {
			t.Errorf("%s: kind %q", v.name, v.kind)
		}
		if v.kind == vocabFrozen {
			switch v.why {
			case "protocol", "state-machine", "request-echo", "scoring":
			default:
				t.Errorf("%s: frozen vocabulary needs a reason of protocol, state-machine, request-echo or scoring, got %q", v.name, v.why)
			}
		}
	}
}

func firstVocab(id string) (outputVocabulary, int) {
	var first outputVocabulary
	n := 0
	for _, v := range outputVocabularies {
		if v.matches(id) {
			if n == 0 {
				first = v
			}
			n++
		}
	}
	return first, n
}

func TestOutputVocabularySchemaShapeFollowsClassification(t *testing.T) {
	for _, s := range collectVocabSites(t) {
		v := vocabFor(t, s.id)
		switch v.kind {
		case vocabAdditive:
			if s.enum != nil {
				t.Errorf("%s: additive vocabulary %q still has an enum at %s %s", s.id, v.name, s.file, s.pointer)
			}
			if s.node["type"] != "string" {
				t.Errorf("%s: additive vocabulary type %v, want string", s.id, s.node["type"])
			}
			if len(s.documented) == 0 {
				t.Errorf("%s: additive vocabulary has no x-documented-values", s.id)
			}
			seen := map[string]bool{}
			for _, d := range s.documented {
				if seen[d] {
					t.Errorf("%s: documented value %q repeated", s.id, d)
				}
				seen[d] = true
			}
			description, _ := s.node["description"].(string)
			if !strings.Contains(description, "Open vocabulary") {
				t.Errorf("%s: description does not say the vocabulary is open", s.id)
			}
			for _, d := range s.documented {
				if d != "" && !strings.Contains(description, d) {
					t.Errorf("%s: description does not list documented value %q", s.id, d)
				}
			}
		case vocabFrozen:
			if s.enum == nil || s.documented != nil {
				t.Errorf("%s: frozen vocabulary %q must keep its enum and carry no x-documented-values", s.id, v.name)
			}
		}
	}
}

func TestOutputVocabularyGoConstantsAgreeWithTheSchema(t *testing.T) {
	notPinned := map[string]string{}
	for _, s := range collectVocabSites(t) {
		v := vocabFor(t, s.id)
		schema := s.enum
		if v.kind == vocabAdditive {
			schema = s.documented
		}
		if v.goSet == nil {
			if v.schemaOnly == "" {
				t.Errorf("%s (%s): no Go constant set and no schemaOnly reason", s.id, v.name)
			}
			notPinned[v.name] = v.schemaOnly
			continue
		}
		have := map[string]bool{}
		for _, x := range schema {
			have[x] = true
		}
		goHave := map[string]bool{}
		for _, g := range v.goSet {
			goHave[g] = true
			if !have[g] {
				t.Errorf("%s (%s): Go constant %q is not in the schema list %v", s.id, v.name, g, schema)
			}
		}
		if v.kind == vocabFrozen || v.exhaustive {
			for _, x := range schema {
				if !goHave[x] {
					t.Errorf("%s (%s): schema value %q has no Go constant", s.id, v.name, x)
				}
			}
		}
	}
	names := make([]string, 0, len(notPinned))
	for n := range notPinned {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("NOT pinned to Go constants: %s (%s)", n, notPinned[n])
	}
}

var vocabSentinel = "value_added_after_the_client_cached_the_schema"

func TestOutputVocabularyCachedSchemaToleratesAddedValues(t *testing.T) {
	checked := map[string]int{}
	for _, s := range collectVocabSites(t) {
		v := vocabFor(t, s.id)
		resolved := resolveVocabNode(t, s)
		err := resolved.Validate(vocabSentinel)
		switch v.kind {
		case vocabAdditive:
			if err != nil {
				t.Errorf("%s (%s %s): a client with this schema cached rejects an added value: %v", s.id, s.file, s.pointer, err)
			}
		case vocabFrozen:
			if err == nil {
				t.Errorf("%s (%s %s): frozen vocabulary accepts an unknown value", s.id, s.file, s.pointer)
			}
		}
		checked[v.kind]++
	}
	if checked[vocabAdditive] < 100 || checked[vocabFrozen] < 30 {
		t.Fatalf("checked %v; the measurement did not happen", checked)
	}
}

func resolveVocabNode(t *testing.T, s vocabSite) *jsonschema.Resolved {
	t.Helper()
	raw, err := json.Marshal(s.node)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s: %v", s.id, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: %v", s.id, err)
	}
	return resolved
}

var vocabIDShape = regexp.MustCompile(`^[A-Za-z0-9_.:]+$`)

func TestOutputVocabularyRegistryNamesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range outputVocabularies {
		if seen[v.name] {
			t.Errorf("duplicate vocabulary name %q", v.name)
		}
		seen[v.name] = true
		for _, s := range v.sites {
			if !vocabIDShape.MatchString(s) {
				t.Errorf("%s: site pattern %q", v.name, s)
			}
		}
	}
}

var vocabClosedWording = regexp.MustCompile(`(?i)\bclosed\b`)

func TestOutputVocabularyAdditiveDescriptionsDoNotClaimClosure(t *testing.T) {
	checked := 0
	for _, s := range collectVocabSites(t) {
		if vocabFor(t, s.id).kind != vocabAdditive {
			continue
		}
		checked++
		description, _ := s.node["description"].(string)
		if loc := vocabClosedWording.FindStringIndex(description); loc != nil {
			t.Errorf("%s (%s %s): an open vocabulary's description calls it closed: %q", s.id, s.file, s.pointer, description[max(0, loc[0]-40):min(len(description), loc[1]+40)])
		}
	}
	if checked < 100 {
		t.Fatalf("checked %d additive sites; the measurement did not happen", checked)
	}
}
