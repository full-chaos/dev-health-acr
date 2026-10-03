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
// receipt. Ran is true when an interpret call produced the outcome, even if
// its receipt named no version or model; the zero value means none ran.
// Source names who interpreted: the service's own model unless the
// interpreter marks the interpretation as supplied by the caller.
type InterpretationStamp struct {
	Ran                   bool
	InterpretationVersion string
	ModelIdentity         string
	Source                InterpretationSource
}

func interpretationStampOf(receipt ModelExecutionReceipt) InterpretationStamp {
	return InterpretationStamp{
		Ran:                   true,
		InterpretationVersion: strings.TrimSpace(receipt.SchemaVersion),
		ModelIdentity:         modelIdentity(receipt.Provider, receipt.Model),
		Source:                InterpretationSourceServer,
	}
}

// stampInterpretationProvenance records on a fresh result's versions who
// interpreted its question. A turn with no interpret call leaves both fields
// absent.
func stampInterpretationProvenance(ctx context.Context, versions *VersionSet) {
	stamp, interpreted := interpretationStampFrom(ctx)
	if !interpreted {
		return
	}
	versions.InterpretationSource = stamp.Source
	versions.InterpretationModelIdentity = stamp.ModelIdentity
}

type interpretationStampKey struct{}

// withInterpretationStamp always writes, so a zero stamp clears one a reused
// context carried in from an earlier turn.
func withInterpretationStamp(ctx context.Context, stamp InterpretationStamp) context.Context {
	return context.WithValue(ctx, interpretationStampKey{}, stamp)
}

func interpretationStampFrom(ctx context.Context) (InterpretationStamp, bool) {
	stamp, _ := ctx.Value(interpretationStampKey{}).(InterpretationStamp)
	return stamp, stamp.Ran
}
