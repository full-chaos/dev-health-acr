package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestSuppliedInterpretationRequestDomainAtTheRoute sends the request field
// through the real route decode and validation, one cell per field and input
// shape. want is the HTTP status; reached says what the investigator saw:
// "supplied" (the field arrived), "plain" (the request arrived with no
// supplied interpretation) or "" (the route refused before the investigator).
func TestSuppliedInterpretationRequestDomainAtTheRoute(t *testing.T) {
	const (
		version = `"context-fabric-model-output.v8"`
		prompt  = `"context-fabric-interpretation.v21"`
		object  = `{"shape":"open"}`
	)
	sha := strings.Repeat("a", 64)
	field := func(output, modelOutputVersion, promptVersion, extra string) string {
		parts := []string{}
		if output != "" {
			parts = append(parts, `"output":`+output)
		}
		if modelOutputVersion != "" {
			parts = append(parts, `"model_output_version":`+modelOutputVersion)
		}
		if promptVersion != "" {
			parts = append(parts, `"prompt_version":`+promptVersion)
		}
		if extra != "" {
			parts = append(parts, extra)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	valid := func(extra string) string { return field(object, version, prompt, extra) }
	quoted := func(text string) string { encoded, _ := json.Marshal(text); return string(encoded) }
	atBound := `{"k":"` + strings.Repeat("a", contractsv1.ContextFabricSuppliedInterpretationMaxBytes-8) + `"}`

	type cell struct {
		name    string
		raw     string // the supplied_interpretation value; "" leaves the key out
		want    int
		reached string
	}
	cells := []cell{
		{"field absent", "", 200, "plain"},
		{"field null", `null`, 200, "plain"},
		{"field empty object", `{}`, 400, ""},
		{"field zero", `0`, 400, ""},
		{"field string", `"x"`, 400, ""},
		{"field array", `[]`, 400, ""},
		{"field true", `true`, 400, ""},
		{"field canonical", valid(""), 200, "supplied"},
		{"field unknown key", valid(`"service_version":"x"`), 400, ""},
		{"field duplicate key, last wins and is valid", valid(`"prompt_version":` + prompt), 200, "supplied"},

		{"output absent", field("", version, prompt, ""), 400, ""},
		{"output null", field(`null`, version, prompt, ""), 400, ""},
		{"output empty object", field(`{}`, version, prompt, ""), 200, "supplied"},
		{"output zero", field(`0`, version, prompt, ""), 400, ""},
		{"output fractional", field(`1.5`, version, prompt, ""), 400, ""},
		{"output true", field(`true`, version, prompt, ""), 400, ""},
		{"output empty string", field(`""`, version, prompt, ""), 400, ""},
		{"output string", field(`"open"`, version, prompt, ""), 400, ""},
		{"output empty array", field(`[]`, version, prompt, ""), 400, ""},
		{"output array of object", field(`[{}]`, version, prompt, ""), 400, ""},
		{"output at the byte bound", field(atBound, version, prompt, ""), 200, "supplied"},
		{"output one byte over the bound", field(strings.Replace(atBound, `"k"`, `"kk"`, 1), version, prompt, ""), 400, ""},
	}
	for _, name := range []string{"model_output_version", "prompt_version"} {
		with := func(value string) string {
			if name == "model_output_version" {
				return field(object, value, prompt, "")
			}
			return field(object, version, value, "")
		}
		cells = append(cells,
			cell{name + " absent", with(""), 400, ""},
			cell{name + " null", with(`null`), 400, ""},
			cell{name + " empty string", with(`""`), 400, ""},
			cell{name + " blank string", with(`"  "`), 400, ""},
			cell{name + " padded", with(`" v8"`), 400, ""},
			cell{name + " zero", with(`0`), 400, ""},
			cell{name + " fractional", with(`1.5`), 400, ""},
			cell{name + " true", with(`true`), 400, ""},
			cell{name + " array", with(`["v8"]`), 400, ""},
			cell{name + " object", with(`{}`), 400, ""},
			cell{name + " out of vocabulary", with(`"not-a-version"`), 200, "supplied"},
			cell{name + " 256 characters", with(quoted(strings.Repeat("v", 256))), 200, "supplied"},
			cell{name + " 257 characters", with(quoted(strings.Repeat("v", 257))), 400, ""},
		)
	}
	optional := func(name string, value string) string { return valid(`"` + name + `":` + value) }
	cells = append(cells,
		cell{"system_sha256 absent", valid(""), 200, "supplied"},
		cell{"system_sha256 null", optional("system_sha256", `null`), 200, "supplied"},
		cell{"system_sha256 empty string", optional("system_sha256", `""`), 200, "supplied"},
		cell{"system_sha256 blank string", optional("system_sha256", `"  "`), 400, ""},
		cell{"system_sha256 zero", optional("system_sha256", `0`), 400, ""},
		cell{"system_sha256 true", optional("system_sha256", `true`), 400, ""},
		cell{"system_sha256 array", optional("system_sha256", `[]`), 400, ""},
		cell{"system_sha256 object", optional("system_sha256", `{}`), 400, ""},
		cell{"system_sha256 63 hex", optional("system_sha256", quoted(sha[:63])), 400, ""},
		cell{"system_sha256 64 lowercase hex", optional("system_sha256", quoted(sha)), 200, "supplied"},
		cell{"system_sha256 64 uppercase hex", optional("system_sha256", quoted(strings.ToUpper(sha))), 400, ""},
		cell{"system_sha256 64 non-hex", optional("system_sha256", quoted(strings.Repeat("g", 64))), 400, ""},
		cell{"system_sha256 65 hex", optional("system_sha256", quoted(sha+"a")), 400, ""},

		cell{"client_model absent", valid(""), 200, "supplied"},
		cell{"client_model null", optional("client_model", `null`), 200, "supplied"},
		cell{"client_model empty string", optional("client_model", `""`), 200, "supplied"},
		cell{"client_model blank string", optional("client_model", `"  "`), 400, ""},
		cell{"client_model zero", optional("client_model", `0`), 400, ""},
		cell{"client_model true", optional("client_model", `true`), 400, ""},
		cell{"client_model array", optional("client_model", `["m"]`), 400, ""},
		cell{"client_model object", optional("client_model", `{}`), 400, ""},
		cell{"client_model canonical", optional("client_model", `"anthropic/claude-test:1.0_a-b"`), 200, "supplied"},
		cell{"client_model 128 characters", optional("client_model", quoted(strings.Repeat("m", 128))), 200, "supplied"},
		cell{"client_model 129 characters", optional("client_model", quoted(strings.Repeat("m", 129))), 400, ""},
		cell{"client_model with a space", optional("client_model", `"claude test"`), 400, ""},
		cell{"client_model with a newline", optional("client_model", `"claude\ntest"`), 400, ""},
		cell{"client_model outside ASCII", optional("client_model", `"modèle"`), 400, ""},
	)

	base, err := json.Marshal(investigationRequestBody())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	tally := map[string]int{}
	for _, c := range cells {
		if seen[c.name] {
			t.Fatalf("duplicate cell name %q", c.name)
		}
		seen[c.name] = true
		body := string(base)
		if c.raw != "" {
			body = strings.TrimSuffix(body, "}") + `,"supplied_interpretation":` + c.raw + "}"
		}
		reached := ""
		app, token := newContextFabricTestApp(t, investigatorFunc(func(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			reached = "plain"
			if request.SuppliedInterpretation != nil {
				reached = "supplied"
			}
			return validContextFabricInvestigationResult(), nil
		}))
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader([]byte(body)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)

		if response.Code != c.want || reached != c.reached {
			t.Errorf("%s: status = %d reached = %q, want %d and %q", c.name, response.Code, reached, c.want, c.reached)
		}
		tally[fmt.Sprintf("%d %s", response.Code, reached)]++
	}
	t.Logf("cells = %d: served with the supplied field = %d, served as a plain request = %d, refused 400 = %d",
		len(cells), tally["200 supplied"], tally["200 plain"], tally["400 "])
	if len(cells) < 70 || tally["200 supplied"] == 0 || tally["200 plain"] != 2 || tally["400 "] == 0 {
		t.Fatalf("cells = %d tally = %v: the table did not exercise every class", len(cells), tally)
	}
}
