package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

func candidateLabelKey(c contextfabric.SubjectCandidate) string {
	return string(c.Subject.Kind) + "\x00" + strings.ToLower(strings.TrimSpace(c.Subject.Label))
}

// collidingLabelKeys returns the kind+label keys held by two or more
// distinct subjects.
func collidingLabelKeys(candidates []contextfabric.SubjectCandidate) map[string]bool {
	owners := map[string]map[string]struct{}{}
	for _, c := range candidates {
		key := candidateLabelKey(c)
		if owners[key] == nil {
			owners[key] = map[string]struct{}{}
		}
		owners[key][c.Subject.CanonicalID] = struct{}{}
	}
	out := map[string]bool{}
	for key, ids := range owners {
		if len(ids) > 1 {
			out[key] = true
		}
	}
	return out
}

// crossKindLabels returns the lowercased labels held by candidates of two or
// more kinds, which only a kind cue tells apart.
func crossKindLabels(candidates []contextfabric.SubjectCandidate) map[string]bool {
	kinds := map[string]map[contextfabric.SubjectKind]struct{}{}
	for _, c := range candidates {
		label := strings.ToLower(strings.TrimSpace(c.Subject.Label))
		if kinds[label] == nil {
			kinds[label] = map[contextfabric.SubjectKind]struct{}{}
		}
		kinds[label][c.Subject.Kind] = struct{}{}
	}
	out := map[string]bool{}
	for label, set := range kinds {
		if len(set) > 1 {
			out[label] = true
		}
	}
	return out
}
