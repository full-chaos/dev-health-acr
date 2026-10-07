package contextfabric

import "strings"

// ClarificationPrompt builds the caller-facing ambiguity prompt from the
// (post-truncation) candidate set.
func ClarificationPrompt(candidates []SubjectCandidate) string {
	max := 3
	if len(candidates) < max {
		max = len(candidates)
	}
	labels := make([]string, 0, max)
	shown := candidates
	if len(shown) > max {
		shown = shown[:max]
	}
	colliding := collidingLabelKeys(shown)
	crossKind := crossKindLabels(shown)
	for _, candidate := range shown {
		label := candidate.Subject.Label
		cues := make([]string, 0, 2)
		if crossKind[strings.ToLower(strings.TrimSpace(candidate.Subject.Label))] {
			cues = append(cues, string(candidate.Subject.Kind))
		}
		if candidate.Provider != "" && colliding[candidateLabelKey(candidate)] {
			cues = append(cues, candidate.Provider)
		}
		if len(cues) > 0 {
			label += " (" + strings.Join(cues, ", ") + ")"
		}
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		// An empty candidate list has no subject to ask about, and the
		// prompt this used to build -- "Which subject did you mean: ?" --
		// is prose no caller can act on. Returning empty also means that if
		// the guarded rebuild site in resolve.go is ever called on an empty
		// list, it degrades to "no prompt" (and so to the ordinary no_match
		// terminal) rather than shipping a broken question to a user.
		return ""
	}
	return "Which subject did you mean: " + strings.Join(labels, ", ") + "?"
}

func candidateLabelKey(c SubjectCandidate) string {
	return string(c.Subject.Kind) + "\x00" + strings.ToLower(strings.TrimSpace(c.Subject.Label))
}

// collidingLabelKeys returns the kind+label keys held by two or more
// distinct subjects.
func collidingLabelKeys(candidates []SubjectCandidate) map[string]bool {
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
func crossKindLabels(candidates []SubjectCandidate) map[string]bool {
	kinds := map[string]map[SubjectKind]struct{}{}
	for _, c := range candidates {
		label := strings.ToLower(strings.TrimSpace(c.Subject.Label))
		if kinds[label] == nil {
			kinds[label] = map[SubjectKind]struct{}{}
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
