package v1

func schemaVocabulary(node map[string]any) ([]any, bool) {
	if raw, ok := node["enum"].([]any); ok {
		return raw, true
	}
	raw, ok := node["x-documented-values"].([]any)
	return raw, ok
}
