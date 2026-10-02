package v1

import "testing"

func TestVersionSetAcceptsNotSynthesizedAndKeepsUnwiredValid(t *testing.T) {
	t.Parallel()
	for _, synthesis := range []string{"not_synthesized", "unwired"} {
		versions := ContextFabricVersionSet{
			ServiceVersion: "s", ContractVersion: "c", Backend: "b", ProjectionVersion: "p", QueryVersion: "q",
			InterpretationVersion: "schema-v1", SynthesisVersion: synthesis, CanonicalServiceVersion: "k",
			ModelIdentity: "openai/gpt-5.6-luna",
		}
		if err := versions.Validate(); err != nil {
			t.Errorf("synthesis_version %q: Validate() = %v, want nil", synthesis, err)
		}
	}
}
