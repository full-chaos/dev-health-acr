package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestSuppliedInterpretationRequestDomainAtTheRoute sends the request field
// through the real route decode and validation, one cell per field and input
// shape, into an investigator that runs the production contract gate and
// nothing else of the interpretation step. want is the HTTP status; reached
// says what the investigator saw: "supplied" (the field arrived), "plain"
// (the request arrived with no supplied interpretation) or "" (the route
// refused before the investigator). A 409 is the gate's refusal of a contract
// value that is absent or is not the service's own.
func TestSuppliedInterpretationRequestDomainAtTheRoute(t *testing.T) {
	gate, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	contract := gate.Contract()
	quoted := func(text string) string { encoded, _ := json.Marshal(text); return string(encoded) }
	const object = `{"shape":"open"}`
	version, prompt, sha := quoted(contract.ModelOutputVersion), quoted(contract.PromptVersion), quoted(contract.SystemSHA256)
	otherSHA := strings.Repeat("a", 64)
	// field writes one supplied_interpretation object; an empty argument
	// leaves that key out.
	field := func(output, modelOutputVersion, promptVersion, systemSHA256, extra string) string {
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
		if systemSHA256 != "" {
			parts = append(parts, `"system_sha256":`+systemSHA256)
		}
		if extra != "" {
			parts = append(parts, extra)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	valid := func(extra string) string { return field(object, version, prompt, sha, extra) }
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

		{"output absent", field("", version, prompt, sha, ""), 400, ""},
		{"output null", field(`null`, version, prompt, sha, ""), 400, ""},
		{"output empty object", field(`{}`, version, prompt, sha, ""), 200, "supplied"},
		{"output zero", field(`0`, version, prompt, sha, ""), 400, ""},
		{"output fractional", field(`1.5`, version, prompt, sha, ""), 400, ""},
		{"output true", field(`true`, version, prompt, sha, ""), 400, ""},
		{"output empty string", field(`""`, version, prompt, sha, ""), 400, ""},
		{"output string", field(`"open"`, version, prompt, sha, ""), 400, ""},
		{"output empty array", field(`[]`, version, prompt, sha, ""), 400, ""},
		{"output array of object", field(`[{}]`, version, prompt, sha, ""), 400, ""},
		{"output at the byte bound", field(atBound, version, prompt, sha, ""), 200, "supplied"},
		{"output one byte over the bound", field(strings.Replace(atBound, `"k"`, `"kk"`, 1), version, prompt, sha, ""), 400, ""},
	}
	for _, name := range []string{"model_output_version", "prompt_version"} {
		with := func(value string) string {
			if name == "model_output_version" {
				return field(object, value, prompt, sha, "")
			}
			return field(object, version, value, sha, "")
		}
		current := version
		if name == "prompt_version" {
			current = prompt
		}
		cells = append(cells,
			cell{name + " absent", with(""), 409, "supplied"},
			cell{name + " null", with(`null`), 409, "supplied"},
			cell{name + " empty string", with(`""`), 409, "supplied"},
			cell{name + " blank string", with(`"  "`), 400, ""},
			cell{name + " padded", with(`" v8"`), 400, ""},
			cell{name + " zero", with(`0`), 400, ""},
			cell{name + " fractional", with(`1.5`), 400, ""},
			cell{name + " true", with(`true`), 400, ""},
			cell{name + " array", with(`["v8"]`), 400, ""},
			cell{name + " object", with(`{}`), 400, ""},
			cell{name + " the service's own", with(current), 200, "supplied"},
			cell{name + " out of vocabulary", with(`"not-a-version"`), 409, "supplied"},
			cell{name + " 256 characters", with(quoted(strings.Repeat("v", 256))), 409, "supplied"},
			cell{name + " 257 characters", with(quoted(strings.Repeat("v", 257))), 400, ""},
		)
	}
	digest := func(value string) string { return field(object, version, prompt, value, "") }
	optional := func(name string, value string) string { return valid(`"` + name + `":` + value) }
	cells = append(cells,
		cell{"system_sha256 absent", digest(""), 409, "supplied"},
		cell{"system_sha256 null", digest(`null`), 409, "supplied"},
		cell{"system_sha256 empty string", digest(`""`), 409, "supplied"},
		cell{"system_sha256 blank string", digest(`"  "`), 400, ""},
		cell{"system_sha256 zero", digest(`0`), 400, ""},
		cell{"system_sha256 true", digest(`true`), 400, ""},
		cell{"system_sha256 array", digest(`[]`), 400, ""},
		cell{"system_sha256 object", digest(`{}`), 400, ""},
		cell{"system_sha256 63 hex", digest(quoted(otherSHA[:63])), 400, ""},
		cell{"system_sha256 the service's own", digest(sha), 200, "supplied"},
		cell{"system_sha256 64 lowercase hex of another prompt", digest(quoted(otherSHA)), 409, "supplied"},
		cell{"system_sha256 64 uppercase hex", digest(quoted(strings.ToUpper(contract.SystemSHA256))), 400, ""},
		cell{"system_sha256 64 non-hex", digest(quoted(strings.Repeat("g", 64))), 400, ""},
		cell{"system_sha256 65 hex", digest(quoted(otherSHA + "a")), 400, ""},

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
		app, token := newContextFabricTestApp(t, investigatorFunc(func(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			reached = "plain"
			if request.SuppliedInterpretation != nil {
				reached = "supplied"
				var mismatch *contextfabric.SuppliedInterpretationContractMismatch
				if _, _, err := gate.InterpretSuppliedQuestion(ctx, principal, request); errors.As(err, &mismatch) {
					return contextfabric.InvestigationResult{}, err
				}
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
	t.Logf("cells = %d: served with the supplied field = %d, served as a plain request = %d, refused 400 before the investigator = %d, refused 409 by the contract gate = %d",
		len(cells), tally["200 supplied"], tally["200 plain"], tally["400 "], tally["409 supplied"])
	if len(cells) < 70 || tally["200 supplied"] == 0 || tally["200 plain"] != 2 || tally["400 "] == 0 || tally["409 supplied"] != 14 || len(tally) != 4 {
		t.Fatalf("cells = %d tally = %v: the table did not exercise every class", len(cells), tally)
	}
}
