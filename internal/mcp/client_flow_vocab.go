package mcp

import (
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
)

// ClientFlowVocabulary names the surfaces of the client-side interpretation
// flow exactly as this package registers them. The guide generator renders
// these values, so the guide text cannot name a surface that is not served.
type ClientFlowVocabulary struct {
	Prompt          string
	PromptArgument  string
	PromptMetaKeys  []string
	InterpretTool   string
	ServerSideTool  string
	OutputSchemaURI string
	FactKindsURI    string
	CatalogURI      string
	ResultTool      string
	SystemSHA256    string
}

// ClientFlow returns the vocabulary of the client-side interpretation flow.
func ClientFlow() ClientFlowVocabulary {
	keys := make([]string, 0, 4)
	for key := range interpretPromptMeta("", "") {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return ClientFlowVocabulary{
		Prompt:          promptInterpretQuestion,
		PromptArgument:  interpretQuestionArg,
		PromptMetaKeys:  keys,
		InterpretTool:   toolInvestigateWithInterpretation,
		ServerSideTool:  toolInvestigateQuestion,
		OutputSchemaURI: uriInterpretationOutput,
		FactKindsURI:    uriFactKinds,
		CatalogURI:      uriDataCatalog,
		ResultTool:      toolInvestigationResult,
		SystemSHA256:    sha256Hex(interpretprompt.System()),
	}
}
