package genkitruntime

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"

type scopedMemberExample = interpretprompt.ScopedMemberExample

var (
	interpretationSystemPrompt         = interpretprompt.System()
	contextFabricFactKindList          = interpretprompt.FactKindList()
	interpretationFactKindGlossary     = interpretprompt.FactKindGlossary
	interpretationFlatFieldRows        = interpretprompt.FlatFieldRows
	interpretationEmphasisWords        = interpretprompt.EmphasisWords
	interpretationDimensionWords       = interpretprompt.DimensionWords
	interpretationScopedMemberExamples = interpretprompt.ScopedMemberExamples
)
