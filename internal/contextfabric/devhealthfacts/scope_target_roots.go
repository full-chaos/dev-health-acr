package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// targetRootSet maps a target key to every requested origin that reached it;
// the first root is the one the statement's min(origin) names, so
// FactScopeExpansionResult.TargetRoot stays what it was.
type targetRootSet map[string][]contextfabric.SubjectRef

// add records roots for key, skipping empty and repeated roots. A nil set is
// allocated on first use.
func (s targetRootSet) add(key string, roots []contextfabric.SubjectRef) targetRootSet {
	for _, root := range roots {
		if root.CanonicalID == "" {
			continue
		}
		if s == nil {
			s = targetRootSet{}
		}
		known := false
		for _, existing := range s[key] {
			if existing.Kind == root.Kind && existing.CanonicalID == root.CanonicalID {
				known = true
				break
			}
		}
		if !known {
			s[key] = append(s[key], root)
		}
	}
	return s
}

// first is the single-root view for FactScopeExpansionResult.TargetRoot.
func (s targetRootSet) first() map[string]contextfabric.SubjectRef {
	if len(s) == 0 {
		return nil
	}
	out := make(map[string]contextfabric.SubjectRef, len(s))
	for key, roots := range s {
		out[key] = roots[0]
	}
	return out
}

// originRootsOf resolves a statement's origin id list to requested origins,
// primary first; an id that names no requested origin is dropped.
func originRootsOf(primary contextfabric.SubjectRef, ids []string, originsByID map[string]contextfabric.SubjectRef) []contextfabric.SubjectRef {
	roots := targetRootSet{}.add("", []contextfabric.SubjectRef{primary})
	for _, id := range ids {
		if origin, ok := originsByID[id]; ok {
			roots = roots.add("", []contextfabric.SubjectRef{origin})
		}
	}
	return roots[""]
}

func firstRoot(roots []contextfabric.SubjectRef) contextfabric.SubjectRef {
	if len(roots) == 0 {
		return contextfabric.SubjectRef{}
	}
	return roots[0]
}
