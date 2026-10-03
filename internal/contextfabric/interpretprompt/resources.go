package interpretprompt

// OutputSchemaID is the $id of the served interpretation output schema.
const OutputSchemaID = "acr://contract/interpretation-output/" + OutputVersion

// OutputSchemaTitle is the title of the served interpretation output schema.
const OutputSchemaTitle = "Interpretation output (" + OutputVersion + ")"

// OutputSchema returns the JSON schema of the object the interpret call
// returns. The text is generated from the runtime's own output type;
// genkitruntime's parity test fails when it is stale.
func OutputSchema() string { return outputSchemaJSON }

// FactKindsGuide returns the fact-kind glossary exactly as the interpretation
// prompt states it, under a heading and the closed set.
func FactKindsGuide() string {
	return "# Fact kinds\n\n" +
		"Closed set: " + contextFabricFactKindList + ".\n\n" +
		interpretationFactKindGlossary + "\n\n" +
		interpretationFactRequirementRules + "\n"
}
