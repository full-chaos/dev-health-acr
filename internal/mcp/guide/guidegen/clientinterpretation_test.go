package guidegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

var backtickToken = regexp.MustCompile("`([^`]+)`")

// plainWords are English or protocol words the guide quotes that no registry owns.
var plainWords = []string{"user", "validation", "prompts/get", "role"}

func jsonNames(typ reflect.Type) []string {
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}
	return names
}

func schemaProperties(t *testing.T, file string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", file))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name, prop := range doc.Properties {
		names = append(names, name)
		for inner := range prop.Properties {
			names = append(names, inner)
		}
	}
	sort.Strings(names)
	return names
}

func guideURIs(c ClientFlowInputs) []string {
	uris := []string{c.OutputSchemaURI, c.FactKindsURI, c.CatalogURI}
	for _, res := range guide.Resources() {
		uris = append(uris, res.URI)
	}
	return uris
}

// clientFlowVocabulary is every name the guide may quote, read from the code.
func clientFlowVocabulary(t *testing.T, c ClientFlowInputs) []string {
	t.Helper()
	vocab := slices.Clone(plainWords)
	vocab = append(vocab, c.Prompt, c.PromptArgument, c.InterpretTool, c.ServerSideTool, c.ResultTool, "_meta",
		CodeContractRefused, CodeInterpretationRejected, DetailsViolatedBound, "details."+c.ContractDetailsKey, "details."+DetailsViolatedBound,
		c.ModelOutputVersion, c.PromptVersion, c.SystemSHA256,
		c.ClientProvider+"/<your model>", c.ClientProvider+"/"+c.ClientUndeclared, c.SourceClient, c.SourceServer,
		c.SourceField, c.IdentityField, c.ModelIdentityField, c.StatusField, c.WindowReceipts,
		"versions", "requested_scope",
		c.SynthesisPrompt, c.SynthesisOutputURI, c.ArgSynthesis, c.SynthesisModeClient, c.SynthesisInputField,
		c.SynthesisSourceField, c.SynthesisVersionField, c.SynthesisSourceClient, c.SynthesisSourceServer, c.SynthesisNotSynthesized,
		c.StatusComplete, c.StatusPartial, c.StatusDegraded, c.StatusNoMatch)
	vocab = append(vocab, c.TextFields...)
	vocab = append(vocab, c.SynthesisInputFields...)
	vocab = append(vocab, c.SynthesisContractFields...)
	vocab = append(vocab, c.PromptMetaKeys...)
	vocab = append(vocab, c.ContractFields...)
	vocab = append(vocab, guideURIs(c)...)
	vocab = append(vocab, schemaProperties(t, "mcp_investigate_with_interpretation_request.v1.schema.json")...)
	vocab = append(vocab, jsonNames(reflect.TypeOf(contractsv1.ContextFabricInterpretationContractRefusal{}))...)
	vocab = append(vocab, jsonNames(reflect.TypeOf(contractsv1.ContextFabricVersionSet{}))...)
	return vocab
}

// driftFindings lists each quoted name of text that is a word, field, tool,
// URI or code and is not in vocab. Quoted literals with spaces, digits only,
// or punctuation-only syntax carry no name.
func driftFindings(text string, vocab []string) []string {
	var findings []string
	for _, match := range backtickToken.FindAllStringSubmatch(text, -1) {
		token := match[1]
		if !strings.ContainsAny(token, "abcdefghijklmnopqrstuvwxyz") || strings.Contains(token, " ") {
			continue
		}
		if !slices.Contains(vocab, token) && !slices.Contains(findings, token) {
			findings = append(findings, token)
		}
	}
	return findings
}

func TestClientInterpretationGuideNamesOnlyWhatTheCodeHas(t *testing.T) {
	c := clientFlowInputs()
	text, err := buildClientInterpretation(c)
	if err != nil {
		t.Fatal(err)
	}
	if findings := driftFindings(text, clientFlowVocabulary(t, c)); len(findings) > 0 {
		t.Fatalf("the guide quotes names no code constant, schema property or registry has: %v", findings)
	}
}

func TestClientInterpretationDriftPinFailsOnARenamedName(t *testing.T) {
	c := clientFlowInputs()
	renamed := c
	renamed.InterpretTool = c.InterpretTool + "_v2"
	text, err := buildClientInterpretation(c)
	if err != nil {
		t.Fatal(err)
	}
	findings := driftFindings(text, clientFlowVocabulary(t, renamed))
	if !slices.Contains(findings, c.InterpretTool) {
		t.Fatalf("findings = %v, want the renamed tool %q reported", findings, c.InterpretTool)
	}
	stale := strings.ReplaceAll(text, "`"+c.SourceField+"`", "`interpretation_origin`")
	if findings := driftFindings(stale, clientFlowVocabulary(t, c)); !slices.Contains(findings, "interpretation_origin") {
		t.Fatalf("findings = %v, want a field that no longer exists reported", findings)
	}
}

func TestClientInterpretationErrorsMatchThePublishedExamples(t *testing.T) {
	for _, row := range []struct {
		file    string
		status  float64
		code    string
		details string
	}{
		{"error_context_fabric_interpretation_contract.v1.json", StatusContractRefused, CodeContractRefused, contractsv1.ContextFabricInterpretationContractDetailsKey},
		{"error_context_fabric_interpretation_rejected.v1.json", StatusInterpretationBad, CodeInterpretationRejected, DetailsViolatedBound},
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contracts", "examples", "v1", row.file))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Error struct {
				Code       string         `json:"code"`
				HTTPStatus float64        `json:"http_status"`
				Retryable  bool           `json:"retryable"`
				Details    map[string]any `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Error.Code != row.code || doc.Error.HTTPStatus != row.status {
			t.Errorf("%s: code %q status %v, want %q %v", row.file, doc.Error.Code, doc.Error.HTTPStatus, row.code, row.status)
		}
		if _, ok := doc.Error.Details[row.details]; !ok {
			t.Errorf("%s: details lack %q", row.file, row.details)
		}
		if row.code == CodeContractRefused {
			nested, _ := doc.Error.Details[row.details].(map[string]any)
			for _, key := range jsonNames(reflect.TypeOf(contractsv1.ContextFabricInterpretationContractRefusal{})) {
				if _, ok := nested[key]; !ok {
					t.Errorf("%s: details.%s lacks %q", row.file, row.details, key)
				}
			}
		}
		if row.code == CodeInterpretationRejected && !doc.Error.Retryable {
			t.Errorf("%s: retryable = false; the guide says the response marks it retryable", row.file)
		}
	}
}

func TestClientInterpretationGuideCarriesTheBuiltContract(t *testing.T) {
	c := clientFlowInputs()
	text, err := buildClientInterpretation(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{c.ModelOutputVersion, c.PromptVersion, c.SystemSHA256} {
		if !strings.Contains(text, value) {
			t.Errorf("guide lacks contract value %q", value)
		}
	}
}

func TestClientInterpretationGuideHasTheSynthesisSection(t *testing.T) {
	c := clientFlowInputs()
	text, err := buildClientInterpretation(c)
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(text, "\n## Write the answer on your own model\n")
	if !ok {
		t.Fatal("guide lacks the section Write the answer on your own model")
	}
	want := []string{c.ArgSynthesis, c.SynthesisModeClient, c.SynthesisInputField, c.SynthesisPrompt, c.SynthesisOutputURI,
		c.StatusPartial, c.StatusDegraded, c.StatusNoMatch, c.StatusComplete, c.SynthesisSourceField, c.SynthesisSourceClient, c.SynthesisSourceServer,
		c.SynthesisVersionField, c.SynthesisNotSynthesized, c.ResultTool, c.CommitNotAffirmed, strconv.Itoa(c.SynthesisMaxBytes)}
	want = append(want, c.SynthesisInputFields...)
	want = append(want, c.SynthesisContractFields...)
	want = append(want, c.TextFields...)
	want = append(want, "never `"+c.StatusComplete+"`", "either `"+c.ServerSideTool+"` or `"+c.InterpretTool+"`", "byte for byte")
	for _, value := range want {
		if value == "" || !strings.Contains(section, value) {
			t.Errorf("synthesis section lacks %q", value)
		}
	}
	if !strings.HasPrefix(contractsv1.ContextFabricClientSynthesisCommitNotAffirmedLimitation, c.CommitNotAffirmed) {
		t.Errorf("prefix %q is not the start of the limitation", c.CommitNotAffirmed)
	}
	if c.SynthesisMaxBytes != contractsv1.ContextFabricSynthesisInputDefaultMaxBytes {
		t.Errorf("size bound = %d", c.SynthesisMaxBytes)
	}
}

func TestClientInterpretationRefusesAnEmptySynthesisInput(t *testing.T) {
	for name, mutate := range map[string]func(*ClientFlowInputs){
		"synthesis prompt":     func(c *ClientFlowInputs) { c.SynthesisPrompt = "" },
		"synthesis schema URI": func(c *ClientFlowInputs) { c.SynthesisOutputURI = "" },
		"synthesis argument":   func(c *ClientFlowInputs) { c.ArgSynthesis = "" },
		"synthesis mode":       func(c *ClientFlowInputs) { c.SynthesisModeClient = "" },
		"input field":          func(c *ClientFlowInputs) { c.SynthesisInputField = "" },
		"source field":         func(c *ClientFlowInputs) { c.SynthesisSourceField = "" },
		"version field":        func(c *ClientFlowInputs) { c.SynthesisVersionField = "" },
		"source client":        func(c *ClientFlowInputs) { c.SynthesisSourceClient = "" },
		"source server":        func(c *ClientFlowInputs) { c.SynthesisSourceServer = "" },
		"not synthesized":      func(c *ClientFlowInputs) { c.SynthesisNotSynthesized = "" },
		"status complete":      func(c *ClientFlowInputs) { c.StatusComplete = "" },
		"status partial":       func(c *ClientFlowInputs) { c.StatusPartial = "" },
		"status degraded":      func(c *ClientFlowInputs) { c.StatusDegraded = "" },
		"status no match":      func(c *ClientFlowInputs) { c.StatusNoMatch = "" },
		"text fields":          func(c *ClientFlowInputs) { c.TextFields = nil },
		"commit prefix":        func(c *ClientFlowInputs) { c.CommitNotAffirmed = "" },
		"input fields":         func(c *ClientFlowInputs) { c.SynthesisInputFields = c.SynthesisInputFields[:4] },
		"one input field": func(c *ClientFlowInputs) {
			c.SynthesisInputFields = slices.Clone(c.SynthesisInputFields)
			c.SynthesisInputFields[2] = ""
		},
		"contract fields": func(c *ClientFlowInputs) { c.SynthesisContractFields = nil },
		"size bound":      func(c *ClientFlowInputs) { c.SynthesisMaxBytes = 0 },
	} {
		c := clientFlowInputs()
		mutate(&c)
		if _, err := buildClientInterpretation(c); err == nil {
			t.Errorf("empty %s: build did not fail", name)
		}
	}
}
