package mcp

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// missingContractValueMessage is the fixed refusal of a contract with a
// missing value. It says where the three values come from.
const missingContractValueMessage = "investigate_with_interpretation needs all three contract values: model_output_version, prompt_version and system_sha256; fetch the prompt interpret_question with prompts/get and send the values of those three _meta keys, unchanged, as contract"

// The fixed local refusals of a write-back argument set that cannot be sent.
const (
	writeBackPairMessage            = "investigate_with_interpretation takes synthesis_output and synthesis_contract together: send both, or neither"
	writeBackNeedsClientMessage     = "investigate_with_interpretation takes synthesis_output only with synthesis \"client\": send synthesis \"client\" with it"
	missingSynthesisContractMessage = "investigate_with_interpretation needs all four values of synthesis_contract: model_output_version, prompt_version, system_sha256 and input_sha256; copy synthesis_input.contract and synthesis_input.input_sha256 of the first answer, unchanged"
)

// handleInvestigateWithInterpretation implements the
// investigate_with_interpretation tool: investigate_question with an
// interpretation the client ran on its own model. The arguments are decoded
// strictly, validated, and mapped onto the hosted investigation request by
// the same code investigate_question uses; the supplied interpretation rides
// in the one hosted field that differs.
func handleInvestigateWithInterpretation(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolInvestigateWithInterpretation), nil
	}

	args, refused := normalizedInvestigationArgs(ctx, cfg, req, toolInvestigateWithInterpretation)
	if refused != nil {
		return refused, nil
	}
	var input contractsv1.MCPInvestigateWithInterpretationRequest
	if err := decodeInvestigationArguments(args, &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: investigationArgumentsMessage(toolInvestigateWithInterpretation, err)}), nil
	}
	if len(input.Contract.Missing()) > 0 {
		return toolErrorResult(&classifiedError{category: "validation", message: missingContractValueMessage}), nil
	}
	hasOutput, hasContract := len(input.SynthesisOutput) > 0, input.SynthesisContract != nil
	if hasOutput != hasContract {
		return toolErrorResult(&classifiedError{category: "validation", message: writeBackPairMessage}), nil
	}
	if hasOutput && input.Synthesis != contractsv1.ContextFabricSynthesisModeClient {
		return toolErrorResult(&classifiedError{category: "validation", message: writeBackNeedsClientMessage}), nil
	}
	if hasContract && len(input.SynthesisContract.Missing()) > 0 {
		return toolErrorResult(&classifiedError{category: "validation", message: missingSynthesisContractMessage}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_with_interpretation arguments failed schema validation"}), nil
	}

	supplied := input.Supplied()
	return investigateAndRender(ctx, cfg, caller, toolInvestigateWithInterpretation, input.MCPInvestigateQuestionRequest, &supplied, input.SuppliedSynthesis())
}
