package mcp

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// clientValidators are the Go Validate symbols a registry entry may name.
var clientValidators = map[string]bool{
	"contractsv1.ContextPacket.ValidateRead":              true,
	"contractsv1.EvidenceRef.ValidateRead":                true,
	"contractsv1.ExpandedEvidence.ValidateRead":           true,
	"contractsv1.ValidateStoredResult":                    true,
	"contractsv1.MCPInvestigateQuestionResponse.Validate": true,
}

func TestOutputVocabularyAdditiveNamesItsClientValidate(t *testing.T) {
	for _, v := range outputVocabularies {
		switch v.kind {
		case vocabAdditive:
			if strings.HasPrefix(v.clientValidate, "none: ") {
				if len(strings.TrimSpace(strings.TrimPrefix(v.clientValidate, "none: "))) < 10 {
					t.Errorf("%s: clientValidate %q gives no reason", v.name, v.clientValidate)
				}
				continue
			}
			if !clientValidators[v.clientValidate] {
				t.Errorf("%s: clientValidate %q is neither a known client Validate nor \"none: <reason>\"; a new additive field must say which client check reads it", v.name, v.clientValidate)
			}
		case vocabFrozen:
			if v.clientValidate != "" {
				t.Errorf("%s: a frozen vocabulary stays closed on the client; clientValidate must be empty", v.name)
			}
		}
	}
}

// clientResponses are the tool responses a Go client validates, with the
// golden example the driven test mutates.
var clientResponses = []struct {
	tool     string
	example  string
	validate func([]byte) error
	// untrusted, when set, replaces the example's declared untrusted fields
	// with the current contract list; the published example predates it.
	untrusted []string
}{
	{"context_for_task", "mcp_context_for_task_response.v1.json", decodeAndValidate[contractsv1.MCPContextForTaskResponse], nil},
	{"context_for_task", "mcp_context_for_task_response_mixed.v1.json", decodeAndValidate[contractsv1.MCPContextForTaskResponse], nil},
	{"source_evidence", "mcp_source_evidence_response.v1.json", decodeAndValidate[contractsv1.MCPSourceEvidenceResponse], nil},
	{"investigate_question", "mcp_investigate_question_response.v1.json", decodeAndValidate[contractsv1.MCPInvestigateQuestionResponse], contractsv1.MCPInvestigateQuestionUntrustedFields},
	{"investigation_result", "mcp_investigation_result_response.v1.json", decodeAndValidate[contractsv1.MCPInvestigationResultResponse], contractsv1.MCPInvestigationResultUntrustedFields},
	{"record_episode", "mcp_record_episode_response.v1.json", decodeAndValidate[contractsv1.MCPRecordEpisodeResponse], nil},
}

func decodeAndValidate[T interface{ Validate() error }](raw []byte) error {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return value.Validate()
}

// vocabLeaf is one string in a golden example governed by a vocabulary site.
type vocabLeaf struct {
	path []any
	id   string
}

func schemaRef(doc map[string]any, ref string) (map[string]any, string) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, ""
	}
	var node any = doc
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, ""
		}
		node = m[part]
	}
	m, _ := node.(map[string]any)
	return m, strings.TrimPrefix(ref, "#")
}

type schemaAt struct {
	node    map[string]any
	pointer string
}

// expandSchema follows $ref and the allOf/anyOf/oneOf branches, so a value
// is matched against every node that can govern it.
func expandSchema(doc map[string]any, at schemaAt, depth int) []schemaAt {
	if at.node == nil || depth > 40 {
		return nil
	}
	out := []schemaAt{at}
	if ref, ok := at.node["$ref"].(string); ok {
		if target, pointer := schemaRef(doc, ref); target != nil {
			out = append(out, expandSchema(doc, schemaAt{target, pointer}, depth+1)...)
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := at.node[key].([]any)
		for i, b := range branches {
			if m, ok := b.(map[string]any); ok {
				out = append(out, expandSchema(doc, schemaAt{m, at.pointer + "/" + key + "/" + itoa(i)}, depth+1)...)
			}
		}
	}
	return out
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func collectVocabLeaves(doc map[string]any, short string, governing []schemaAt, value any, path []any, out *[]vocabLeaf) {
	var nodes []schemaAt
	for _, g := range governing {
		nodes = append(nodes, expandSchema(doc, g, 0)...)
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			var next []schemaAt
			for _, n := range nodes {
				props, _ := n.node["properties"].(map[string]any)
				if p, ok := props[key].(map[string]any); ok {
					next = append(next, schemaAt{p, n.pointer + "/properties/" + key})
				}
			}
			collectVocabLeaves(doc, short, next, child, append(append([]any{}, path...), key), out)
		}
	case []any:
		var next []schemaAt
		for _, n := range nodes {
			if items, ok := n.node["items"].(map[string]any); ok {
				next = append(next, schemaAt{items, n.pointer + "/items"})
			}
		}
		for i, child := range v {
			collectVocabLeaves(doc, short, next, child, append(append([]any{}, path...), i), out)
		}
	case string:
		for _, n := range nodes {
			_, hasEnum := n.node["enum"]
			_, hasDoc := n.node["x-documented-values"]
			if (hasEnum || hasDoc) && !vocabIsCondition(n.pointer) {
				*out = append(*out, vocabLeaf{path: path, id: vocabSiteID(short, n.pointer)})
				return
			}
		}
	}
}

func setLeaf(doc any, path []any, value string) any {
	if len(path) == 0 {
		return value
	}
	switch n := doc.(type) {
	case map[string]any:
		n[path[0].(string)] = setLeaf(n[path[0].(string)], path[1:], value)
		return n
	case []any:
		i := path[0].(int)
		n[i] = setLeaf(n[i], path[1:], value)
		return n
	}
	return doc
}

// TestOutputVocabularyClientValidateAcceptsAddedValues drives the client
// Validate each registry entry names: every leaf of a golden tool response
// that an additive vocabulary governs is set to an added value, all leaves of
// one vocabulary together, each distinct original value to its own added
// value so equality and uniqueness between them still hold, and the response
// must still validate. A frozen leaf set the same way must not.
func TestOutputVocabularyClientValidateAcceptsAddedValues(t *testing.T) {
	driven := map[string]int{}
	for _, response := range clientResponses {
		file := "schemas/mcp_" + response.tool + "_response.v1.schema.json"
		schema := readEmbeddedJSON(t, file)
		raw, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", response.example))
		if err != nil {
			t.Fatal(err)
		}
		if response.untrusted != nil {
			raw = withUntrustedFields(t, raw, response.untrusted)
		}
		if err := response.validate(raw); err != nil {
			t.Fatalf("%s: the golden example does not validate: %v", response.example, err)
		}
		var example any
		if err := json.Unmarshal(raw, &example); err != nil {
			t.Fatal(err)
		}
		var leaves []vocabLeaf
		collectVocabLeaves(schema, response.tool, []schemaAt{{schema, ""}}, example, nil, &leaves)
		byVocab := map[string][]vocabLeaf{}
		for _, leaf := range leaves {
			// The federated local context is minted by the sidecar itself
			// and its byte count is pinned against its own content.
			if leaf.path[0] == "local_context" {
				continue
			}
			v := vocabFor(t, leaf.id)
			byVocab[v.name] = append(byVocab[v.name], leaf)
		}
		names := make([]string, 0, len(byVocab))
		for name := range byVocab {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := vocabFor(t, byVocab[name][0].id)
			if v.kind == vocabAdditive && strings.HasPrefix(v.clientValidate, "none: ") {
				continue
			}
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			targets := append([]vocabLeaf{}, byVocab[name]...)
			for _, pair := range clientVocabPairs[name] {
				targets = append(targets, byVocab[pair]...)
			}
			added := map[string]string{}
			for _, leaf := range targets {
				original := leafValue(example, leaf.path)
				if _, ok := added[original]; !ok {
					added[original] = vocabSentinel
					if n := len(added); n > 1 {
						added[original] = vocabSentinel + "_" + itoa(n)
					}
				}
				doc = setLeaf(doc, leaf.path, added[original])
			}
			mutated, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			err = response.validate(mutated)
			switch v.kind {
			case vocabAdditive:
				if err != nil {
					t.Errorf("%s %s (%s, %d leaves): %s rejects an added value: %v", response.example, name, v.clientValidate, len(byVocab[name]), v.clientValidate, err)
				}
				driven[name]++
			case vocabFrozen:
				if err == nil {
					t.Errorf("%s %s (%d leaves): a frozen vocabulary accepts an unknown value", response.example, name, len(byVocab[name]))
				}
			}
		}
	}
	var notDriven []string
	for _, v := range outputVocabularies {
		if v.kind == vocabAdditive && clientValidators[v.clientValidate] && driven[v.name] == 0 {
			notDriven = append(notDriven, v.name)
		}
	}
	sort.Strings(notDriven)
	t.Logf("driven through a golden tool response: %d additive vocabularies; NOT driven (no golden example carries them): %v", len(driven), notDriven)
	if len(driven) < 15 {
		t.Fatalf("only %d additive vocabularies were driven; the measurement did not happen", len(driven))
	}
}

// clientVocabPairs names vocabularies whose values a document must change
// together: a driver category that requires a claimed fact names that fact's
// kind, so an added fact kind arrives with an added category.
var clientVocabPairs = map[string][]string{
	"ClaimedFact.kind": {"DriverJudgment.category"},
}

func withUntrustedFields(t *testing.T, raw []byte, fields []string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	untrusted, _ := doc["untrusted_content"].(map[string]any)
	if untrusted == nil {
		t.Fatal("example has no untrusted_content")
	}
	untrusted["fields"] = fields
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func leafValue(doc any, path []any) string {
	for _, step := range path {
		switch n := doc.(type) {
		case map[string]any:
			doc = n[step.(string)]
		case []any:
			doc = n[step.(int)]
		}
	}
	s, _ := doc.(string)
	return s
}
