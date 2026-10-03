package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// ContextFabricSuppliedInterpretationMaxBytes bounds the raw
	// interpretation object a client may send.
	ContextFabricSuppliedInterpretationMaxBytes = 32 << 10
	// ContextFabricClientModelMaxLength bounds the model name a client
	// declares for its own interpretation.
	ContextFabricClientModelMaxLength = 128
	// ContextFabricClientSuppliedProvider is the provider half of the
	// interpretation model identity of a client-supplied interpretation. No
	// server-side model provider may use it.
	ContextFabricClientSuppliedProvider = "client-supplied"
	// ContextFabricClientModelUndeclared is the model half when the client
	// declared none.
	ContextFabricClientModelUndeclared = "undeclared"
)

// ContextFabricInterpretationSource names who produced the interpretation a
// result was built from.
type ContextFabricInterpretationSource string

const (
	ContextFabricInterpretationSourceServer ContextFabricInterpretationSource = "server"
	ContextFabricInterpretationSourceClient ContextFabricInterpretationSource = "client"
)

func ValidContextFabricInterpretationSource(source ContextFabricInterpretationSource) bool {
	return source == ContextFabricInterpretationSourceServer || source == ContextFabricInterpretationSourceClient
}

// ContextFabricSuppliedInterpretation is an interpretation the caller ran on
// its own model. Output is the model output object, in the shape and version
// the service's own interpretation call returns; it is untrusted and is
// validated exactly as a model output is. ModelOutputVersion, PromptVersion
// and SystemSHA256 name the contract the caller ran. All three are required:
// the interpretation step refuses a value that is absent or is not the
// service's own, and names the contract the service runs.
type ContextFabricSuppliedInterpretation struct {
	Output             json.RawMessage `json:"output"`
	ModelOutputVersion string          `json:"model_output_version"`
	PromptVersion      string          `json:"prompt_version"`
	SystemSHA256       string          `json:"system_sha256"`
	ClientModel        string          `json:"client_model,omitempty"`
}

// Validate checks the shape of each value that is present. It accepts an
// absent contract value: only the interpretation step holds the contract the
// service runs, so that step refuses it, with the current values.
func (s ContextFabricSuppliedInterpretation) Validate() error {
	output := bytes.TrimSpace(s.Output)
	if len(s.Output) > ContextFabricSuppliedInterpretationMaxBytes {
		return fmt.Errorf("supplied_interpretation output exceeds %d bytes", ContextFabricSuppliedInterpretationMaxBytes)
	}
	if len(output) == 0 || output[0] != '{' || !json.Valid(output) {
		return fmt.Errorf("supplied_interpretation output must be a JSON object")
	}
	if (s.ModelOutputVersion != "" && !validVersion(s.ModelOutputVersion)) || (s.PromptVersion != "" && !validVersion(s.PromptVersion)) {
		return fmt.Errorf("supplied_interpretation versions violate v1 bounds")
	}
	if s.SystemSHA256 != "" && !validLowerHexSHA256(s.SystemSHA256) {
		return fmt.Errorf("supplied_interpretation system_sha256 must be 64 lowercase hex characters")
	}
	if s.ClientModel != "" && !ValidContextFabricClientModel(s.ClientModel) {
		return fmt.Errorf("supplied_interpretation client_model violates v1 bounds")
	}
	return nil
}

// ContextFabricReservedModelProvider reports whether provider is a name no
// server-side model configuration may use: a result's interpretation model
// identity that starts with it means the caller interpreted.
func ContextFabricReservedModelProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), ContextFabricClientSuppliedProvider)
}

// ValidContextFabricClientModel reports whether a client-declared model name
// is safe to store: 1..128 characters of [A-Za-z0-9._:/-].
func ValidContextFabricClientModel(model string) bool {
	if len(model) == 0 || len(model) > ContextFabricClientModelMaxLength {
		return false
	}
	for i := 0; i < len(model); i++ {
		c := model[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '/' || c == '-':
		default:
			return false
		}
	}
	return true
}

// ContextFabricClientSuppliedModelIdentity is the interpretation model
// identity of a client-supplied interpretation.
func ContextFabricClientSuppliedModelIdentity(clientModel string) string {
	if clientModel == "" {
		clientModel = ContextFabricClientModelUndeclared
	}
	return ContextFabricClientSuppliedProvider + "/" + clientModel
}

// IsContextFabricClientSuppliedModelIdentity reports whether identity names a
// client-supplied interpretation.
func IsContextFabricClientSuppliedModelIdentity(identity string) bool {
	return strings.HasPrefix(identity, ContextFabricClientSuppliedProvider+"/")
}

func validLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validateInterpretationProvenance checks the two version-set fields that
// name the interpretation's source. Both are optional: a result with no
// interpretation call on its turn carries neither.
func validateInterpretationProvenance(source ContextFabricInterpretationSource, identity string) error {
	if source != "" && !ValidContextFabricInterpretationSource(source) {
		return fmt.Errorf("interpretation_source is invalid")
	}
	if identity != "" && !validModelIdentity(identity) {
		return fmt.Errorf("interpretation_model_identity violates v1 bounds")
	}
	clientIdentity := IsContextFabricClientSuppliedModelIdentity(identity)
	if source == ContextFabricInterpretationSourceClient && !clientIdentity {
		return fmt.Errorf("a client interpretation_source requires a client-supplied interpretation_model_identity")
	}
	if source != ContextFabricInterpretationSourceClient && clientIdentity {
		return fmt.Errorf("a client-supplied interpretation_model_identity requires a client interpretation_source")
	}
	return nil
}

// ContextFabricInterpretationContract names one interpretation contract: the
// model output version, the prompt version and the sha256 of the system
// message. Its keys are the keys of the interpret_question prompt's _meta
// block.
type ContextFabricInterpretationContract struct {
	ModelOutputVersion string `json:"model_output_version"`
	PromptVersion      string `json:"prompt_version"`
	SystemSHA256       string `json:"system_sha256"`
}

// Missing lists the contract fields that carry no value, in the order a
// refusal names them.
func (c ContextFabricInterpretationContract) Missing() []string {
	var missing []string
	if c.ModelOutputVersion == "" {
		missing = append(missing, ContextFabricInterpretationContractFieldModelOutputVersion)
	}
	if c.PromptVersion == "" {
		missing = append(missing, ContextFabricInterpretationContractFieldPromptVersion)
	}
	if c.SystemSHA256 == "" {
		missing = append(missing, ContextFabricInterpretationContractFieldSystemSHA256)
	}
	return missing
}

// The fields of a supplied interpretation's contract that can mismatch.
const (
	ContextFabricInterpretationContractFieldModelOutputVersion = "model_output_version"
	ContextFabricInterpretationContractFieldPromptVersion      = "prompt_version"
	ContextFabricInterpretationContractFieldSystemSHA256       = "system_sha256"
)

// ContextFabricInterpretationContractDetailsKey is the error details key
// under which a contract refusal is served.
const ContextFabricInterpretationContractDetailsKey = "interpretation_contract"

// ContextFabricInterpretationContractRefusal is the body of the refusal of a
// supplied interpretation whose declared contract is not the service's own.
// Mismatch names each contract field that is absent or differs; Current is
// the contract the service runs, which the caller reads to fetch the prompt
// again.
type ContextFabricInterpretationContractRefusal struct {
	Mismatch []string                            `json:"mismatch"`
	Current  ContextFabricInterpretationContract `json:"current"`
}

func (r ContextFabricInterpretationContractRefusal) Validate() error {
	if len(r.Mismatch) == 0 || len(r.Mismatch) > 3 {
		return fmt.Errorf("interpretation contract refusal must name one to three mismatched fields")
	}
	seen := make(map[string]struct{}, len(r.Mismatch))
	for _, field := range r.Mismatch {
		switch field {
		case ContextFabricInterpretationContractFieldModelOutputVersion,
			ContextFabricInterpretationContractFieldPromptVersion,
			ContextFabricInterpretationContractFieldSystemSHA256:
		default:
			return fmt.Errorf("interpretation contract refusal names an unknown field")
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("interpretation contract refusal fields must be unique")
		}
		seen[field] = struct{}{}
	}
	if !validVersion(r.Current.ModelOutputVersion) || !validVersion(r.Current.PromptVersion) || !validLowerHexSHA256(r.Current.SystemSHA256) {
		return fmt.Errorf("interpretation contract refusal current contract violates v1 bounds")
	}
	return nil
}

// MCPInvestigateWithInterpretationRequest is the investigate_with_interpretation
// tool input: every investigate_question argument, plus the interpretation
// the client ran on its own model and the contract it ran it under.
type MCPInvestigateWithInterpretationRequest struct {
	MCPInvestigateQuestionRequest
	Interpretation json.RawMessage                     `json:"interpretation"`
	Contract       ContextFabricInterpretationContract `json:"contract"`
	ClientModel    string                              `json:"client_model,omitempty"`
	// SynthesisOutput and SynthesisContract carry the answer draft the
	// client wrote on its own model; they are sent together and only with
	// synthesis "client".
	SynthesisOutput   json.RawMessage       `json:"synthesis_output,omitempty"`
	SynthesisContract *MCPSynthesisContract `json:"synthesis_contract,omitempty"`
}

// Supplied maps the tool's interpretation arguments onto the hosted request
// field.
func (r MCPInvestigateWithInterpretationRequest) Supplied() ContextFabricSuppliedInterpretation {
	return ContextFabricSuppliedInterpretation{
		Output:             r.Interpretation,
		ModelOutputVersion: r.Contract.ModelOutputVersion,
		PromptVersion:      r.Contract.PromptVersion,
		SystemSHA256:       r.Contract.SystemSHA256,
		ClientModel:        r.ClientModel,
	}
}

// Validate refuses a contract with a missing value, as the tool's input
// schema does: the tool sends the hosted request only for arguments its
// schema accepts.
func (r MCPInvestigateWithInterpretationRequest) Validate() error {
	if err := r.MCPInvestigateQuestionRequest.Validate(); err != nil {
		return err
	}
	if missing := r.Contract.Missing(); len(missing) > 0 {
		return fmt.Errorf("contract requires %s", strings.Join(missing, ", "))
	}
	if err := r.Supplied().Validate(); err != nil {
		return err
	}
	return r.validateSynthesisWriteBack()
}
