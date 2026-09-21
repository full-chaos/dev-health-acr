package mcp

// matrixEnvironmentPrefix names the variables that configure the auth matrix
// against a deployed endpoint.
const matrixEnvironmentPrefix = "ACR_MCP_MATRIX_"

// matrixEnvironment holds those variables as the operator exported them,
// captured by TestMain before every other ACR_ variable is removed.
var matrixEnvironment = map[string]string{}

// MatrixEnvironmentForTest returns one auth matrix setting as the operator
// exported it, or "" when it was not exported.
func MatrixEnvironmentForTest(name string) string { return matrixEnvironment[name] }
