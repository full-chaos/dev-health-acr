package hosted

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The composition hands the production synthesizer the synthesis contract this
// binary runs. A synthesizer built without it refuses every client turn.
func TestTheClientSynthesisAssemblyCarriesTheContractThisBinaryRuns(t *testing.T) {
	t.Parallel()
	assembly := synthesisprompt.ClientAssembly()
	sum := sha256.Sum256([]byte(synthesisprompt.System()))
	if assembly.PromptVersion != synthesisprompt.PromptVersion || assembly.ModelOutputVersion != synthesisprompt.OutputVersion || assembly.SystemSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("assembly contract = %q %q %q, want the synthesis prompt package's own", assembly.PromptVersion, assembly.ModelOutputVersion, assembly.SystemSHA256)
	}
	if !reflect.DeepEqual(assembly.Rules, synthesisprompt.ClientRules()) {
		t.Fatalf("assembly rules = %q, want the client rules", assembly.Rules)
	}
	if assembly.MaxBytes != contractsv1.ContextFabricSynthesisInputDefaultMaxBytes || assembly.Encode == nil {
		t.Fatalf("assembly bound = %d, encode nil = %v", assembly.MaxBytes, assembly.Encode == nil)
	}
	if !(contextfabric.RuntimeAnswerSynthesizer{ClientSynthesis: assembly}).ClientSynthesisAvailable() {
		t.Fatal("a synthesizer carrying the assembly reports client synthesis unavailable")
	}
}

// The production synthesizer literal in the composition names the assembly.
func TestTheProductionSynthesizerIsWiredWithTheClientSynthesisAssembly(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("open.go")
	if err != nil {
		t.Fatalf("read open.go: %v", err)
	}
	if !strings.Contains(string(source), "contextfabric.RuntimeAnswerSynthesizer{Runtime: modelRuntime, Sink: receiptSink, Options: contextFabricSynthesizerOptions(request.options.ServiceVersion), Telemetry: engineTelemetry, ClientSynthesis: synthesisprompt.ClientAssembly()}") {
		t.Fatal("open.go builds the production synthesizer without ClientSynthesis: synthesisprompt.ClientAssembly()")
	}
}
