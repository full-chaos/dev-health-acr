package genkitruntime

import "testing"

// CHAOS-5774: DefaultSynthesisPromptVersion already had an exact-value pin
// (chaos4355_model_facing_rows_test.go); DefaultInterpretationPromptVersion
// and DefaultSchemaVersion did not. TestVersionedModelContractsAreBoundToTheirContent
// binds each PROMPT/SCHEMA's rendered CONTENT to a digest, but never reads
// `version` itself, so a version constant reverted on its own (content
// unchanged) passes that test silently -- exactly the asymmetry a class
// sweep over "every version constant gets an exact-value pin" closes.

func TestDefaultInterpretationPromptVersionIsExactlyCurrent(t *testing.T) {
	t.Parallel()
	const wantVersion = "context-fabric-interpretation.v15"
	if DefaultInterpretationPromptVersion != wantVersion {
		t.Fatalf("DefaultInterpretationPromptVersion = %q, want %q -- update this pin only alongside a genuine interpretation-prompt content change", DefaultInterpretationPromptVersion, wantVersion)
	}
}

func TestDefaultSchemaVersionIsExactlyCurrent(t *testing.T) {
	t.Parallel()
	const wantVersion = "context-fabric-model-output.v7"
	if DefaultSchemaVersion != wantVersion {
		t.Fatalf("DefaultSchemaVersion = %q, want %q -- update this pin only alongside a genuine model-output-schema content change", DefaultSchemaVersion, wantVersion)
	}
}
