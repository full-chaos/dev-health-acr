package guidegen

import (
	"encoding/json"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
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
	vocab = append(vocab, c.ArgSynthesisOutput, c.ArgSynthesisContract, c.ArgClientModel, c.BudgetArg, CodeSynthesisRejected,
		"details."+c.SynthesisContractDetailsKey, "details."+c.ReasonKey, "details."+c.InputChangedDetailsKey, "details."+c.RejectionReasonKey,
		c.ReasonInputChanged, c.ReasonInterpretationRequired,
		c.SynthesisInputField+"."+c.SynthesisInputFields[0], c.SynthesisInputField+"."+c.SynthesisInputFields[2],
		"mismatch", "current")
	vocab = append(vocab, c.WriteBackContractFields...)
	vocab = append(vocab, c.TextFields...)
	vocab = append(vocab, c.SynthesisInputFields...)
	vocab = append(vocab, c.SynthesisContractFields...)
	vocab = append(vocab, c.SynthesisObservationPaths...)
	vocab = append(vocab, c.SourceWatermarkField, c.ClientWindowField, c.ClientWindowRelativeID, c.FactWindowStart, c.FactWindowEnd, c.FactWindowBasis, c.FactWindowDefaultTrailing)
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
	for _, path := range synthesisprompt.ClientInputObservationPaths() {
		want = append(want, "`"+path+"`")
	}
	want = append(want, "`input_sha256` is the sha256 of `input`", "the same `input` and the same `input_sha256`", "`"+c.ClientWindowField+"`", "`"+c.FactWindowDefaultTrailing+"`")
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

func TestClientInterpretationGuideHasTheWriteBack(t *testing.T) {
	c := clientFlowInputs()
	text, err := buildClientInterpretation(c)
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(text, "\n### Send the answer back\n")
	if !ok {
		t.Fatal("guide lacks the write-back subsection")
	}
	want := []string{c.ArgSynthesisOutput, c.ArgSynthesisContract, c.ArgClientModel, c.InterpretTool, c.ServerSideTool, c.ArgInterpretation, c.ArgContract,
		c.ArgSynthesis, c.SynthesisModeClient, strconv.Itoa(c.SynthesisOutputMaxBytes),
		c.SynthesisInputField + "." + c.SynthesisInputFields[0], c.SynthesisInputField + "." + c.SynthesisInputFields[2],
		c.SynthesisSourceField, c.SynthesisSourceClient, c.ModelIdentityField, c.ClientProvider + "/<your model>", c.ClientProvider + "/" + c.ClientUndeclared,
		c.SynthesisVersionField, c.StatusField, c.StatusComplete,
		"details." + c.SynthesisContractDetailsKey, "details." + c.ReasonKey, c.ReasonInputChanged, "details." + c.InputChangedDetailsKey,
		"details." + c.RejectionReasonKey, CodeSynthesisRejected, c.ReasonInterpretationRequired, c.BudgetArg, c.SynthesisPrompt,
		strconv.Itoa(StatusContractRefused), strconv.Itoa(StatusDraftRejected), strconv.Itoa(StatusAnswerTooLarge),
		"We never call a model", "no suffix", "stays committed"}
	want = append(want, c.WriteBackContractFields...)
	want = append(want, c.TextFields...)
	for _, value := range want {
		if value == "" || !strings.Contains(section, value) {
			t.Errorf("write-back subsection lacks %q", value)
		}
	}
}

func TestClientInterpretationWriteBackNamesMatchTheCode(t *testing.T) {
	c := clientFlowInputs()
	if c.ReasonInputChanged != "input_changed" || c.ReasonInterpretationRequired != "supplied_interpretation_required" || c.SynthesisContractDetailsKey != "synthesis_contract" ||
		c.InputChangedDetailsKey != "synthesis_input" || c.RejectionReasonKey != "rejection_reason" || c.ReasonKey != "reason" {
		t.Errorf("write-back detail names changed: %+v", c)
	}
	if !slices.Equal(c.WriteBackContractFields, []string{"model_output_version", "prompt_version", "system_sha256", "input_sha256"}) {
		t.Errorf("write-back contract fields = %v", c.WriteBackContractFields)
	}
	if c.ArgSynthesisOutput != "synthesis_output" || c.ArgSynthesisContract != "synthesis_contract" || c.SynthesisOutputMaxBytes != contractsv1.ContextFabricSuppliedSynthesisMaxBytes {
		t.Errorf("write-back arguments = %q %q %d", c.ArgSynthesisOutput, c.ArgSynthesisContract, c.SynthesisOutputMaxBytes)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contracts", "examples", "v1", "error_context_fabric_synthesis_contract.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Error struct {
			Code       string                    `json:"code"`
			HTTPStatus float64                   `json:"http_status"`
			Details    map[string]map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error.Code != CodeContractRefused || doc.Error.HTTPStatus != StatusContractRefused || doc.Error.Details[c.SynthesisContractDetailsKey]["mismatch"] == nil {
		t.Errorf("published synthesis contract example does not match the guide: %+v", doc.Error)
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
		"contract fields":                func(c *ClientFlowInputs) { c.SynthesisContractFields = nil },
		"synthesis output argument":      func(c *ClientFlowInputs) { c.ArgSynthesisOutput = "" },
		"synthesis contract argument":    func(c *ClientFlowInputs) { c.ArgSynthesisContract = "" },
		"synthesis contract details key": func(c *ClientFlowInputs) { c.SynthesisContractDetailsKey = "" },
		"reason key":                     func(c *ClientFlowInputs) { c.ReasonKey = "" },
		"input changed reason":           func(c *ClientFlowInputs) { c.ReasonInputChanged = "" },
		"interpretation required reason": func(c *ClientFlowInputs) { c.ReasonInterpretationRequired = "" },
		"rejection reason key":           func(c *ClientFlowInputs) { c.RejectionReasonKey = "" },
		"input changed details key":      func(c *ClientFlowInputs) { c.InputChangedDetailsKey = "" },
		"budget argument":                func(c *ClientFlowInputs) { c.BudgetArg = "" },
		"client model argument":          func(c *ClientFlowInputs) { c.ArgClientModel = "" },
		"write-back contract fields":     func(c *ClientFlowInputs) { c.WriteBackContractFields = c.WriteBackContractFields[:3] },
		"one write-back contract field": func(c *ClientFlowInputs) {
			c.WriteBackContractFields = slices.Clone(c.WriteBackContractFields)
			c.WriteBackContractFields[3] = ""
		},
		"write-back size bound": func(c *ClientFlowInputs) { c.SynthesisOutputMaxBytes = 0 },
		"size bound":            func(c *ClientFlowInputs) { c.SynthesisMaxBytes = 0 },
	} {
		c := clientFlowInputs()
		mutate(&c)
		if _, err := buildClientInterpretation(c); err == nil {
			t.Errorf("empty %s: build did not fail", name)
		}
	}
}
