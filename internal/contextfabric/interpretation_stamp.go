package contextfabric

import (
	"context"
	"strings"
)

// SynthesisVersionNotSynthesized is the synthesis version of a result that
// ended without a synthesis call although a model ran earlier in the turn.
// It is distinct from "unwired", which means no model call happened.
const SynthesisVersionNotSynthesized = "not_synthesized"

// InterpretationStamp is the version-shaped part of one interpret call's
// receipt. The zero value means no interpret call ran.
type InterpretationStamp struct {
	InterpretationVersion string
	ModelIdentity         string
}

func interpretationStampOf(receipt ModelExecutionReceipt) InterpretationStamp {
	return InterpretationStamp{
		InterpretationVersion: strings.TrimSpace(receipt.SchemaVersion),
		ModelIdentity:         modelIdentity(receipt.Provider, receipt.Model),
	}
}

type interpretationStampKey struct{}

func withInterpretationStamp(ctx context.Context, stamp InterpretationStamp) context.Context {
	if stamp == (InterpretationStamp{}) {
		return ctx
	}
	return context.WithValue(ctx, interpretationStampKey{}, stamp)
}

func interpretationStampFrom(ctx context.Context) (InterpretationStamp, bool) {
	stamp, ok := ctx.Value(interpretationStampKey{}).(InterpretationStamp)
	return stamp, ok
}
