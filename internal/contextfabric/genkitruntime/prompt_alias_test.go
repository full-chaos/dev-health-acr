package genkitruntime

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

type (
	scopedMemberExample = interpretprompt.ScopedMemberExample
	synthesisInput      = synthesisprompt.Input
)

var (
	interpretationSystemPrompt         = interpretprompt.System()
	contextFabricFactKindList          = interpretprompt.FactKindList()
	interpretationFactKindGlossary     = interpretprompt.FactKindGlossary
	interpretationFlatFieldRows        = interpretprompt.FlatFieldRows
	interpretationEmphasisWords        = interpretprompt.EmphasisWords
	interpretationDimensionWords       = interpretprompt.DimensionWords
	interpretationScopedMemberExamples = interpretprompt.ScopedMemberExamples
	synthesisSystemPrompt              = synthesisprompt.System()
	synthesisInputFromDomain           = synthesisprompt.InputFromDomain
	modelFacingAnswerBudget            = synthesisprompt.ModelFacingAnswerBudget
	modelFacingFacts                   = synthesisprompt.ModelFacingFacts
)
