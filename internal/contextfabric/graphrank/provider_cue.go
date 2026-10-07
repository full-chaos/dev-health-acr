package graphrank

import (
	"strings"
)

const providerAttributePrefix = "provider_"

// ProviderAttribute returns the provider of a node that carries exactly one
// provider id attribute ("provider_<name>"), or "" when it carries none or
// several: a cue that does not name one provider would mislead.
func ProviderAttribute(attributes map[string]interface{}) string {
	found := ""
	for key := range attributes {
		if !strings.HasPrefix(key, providerAttributePrefix) {
			continue
		}
		name := strings.TrimPrefix(key, providerAttributePrefix)
		if name == "" || name == "aliases" || strings.HasSuffix(key, "_aliases") {
			continue
		}
		if found != "" && found != name {
			return ""
		}
		found = name
	}
	return found
}
