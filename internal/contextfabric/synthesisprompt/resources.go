package synthesisprompt

// OutputSchemaID is the $id of the served synthesis output schema.
const OutputSchemaID = "acr://contract/synthesis-output/" + OutputVersion

// OutputSchemaTitle is the title of the served synthesis output schema.
const OutputSchemaTitle = "Synthesis output (" + OutputVersion + ")"

// OutputSchema returns the JSON schema of the object the synthesize call
// returns. The text is generated from the runtime's own output type;
// genkitruntime's parity test fails when it is stale.
func OutputSchema() string { return outputSchemaJSON }
