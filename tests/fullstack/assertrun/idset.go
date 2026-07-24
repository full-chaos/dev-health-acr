package main

import "sort"

// stringSet is a small set of strings used to compare evidence-ref ID collections,
// capability tool lists, and similar normalized structured fields. All comparisons in this
// tool operate on sorted sets, never on raw string equality of model output.
type stringSet map[string]struct{}

func newStringSet(values ...string) stringSet {
	set := make(stringSet, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}

func (s stringSet) add(values ...string) {
	for _, v := range values {
		s[v] = struct{}{}
	}
}

func (s stringSet) has(v string) bool {
	_, ok := s[v]
	return ok
}

// sorted returns the set's members as a deterministically ordered slice, suitable for both
// comparison and for rendering in a failure message.
func (s stringSet) sorted() []string {
	out := make([]string, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// union returns a new set containing every member of s and every member of others.
func (s stringSet) union(others ...stringSet) stringSet {
	out := newStringSet(s.sorted()...)
	for _, other := range others {
		out.add(other.sorted()...)
	}
	return out
}

// missing returns the sorted subset of required that is not present in s.
func (s stringSet) missing(required []string) []string {
	var out []string
	for _, id := range required {
		if !s.has(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// present returns the sorted subset of forbidden that is present in s.
func (s stringSet) present(forbidden []string) []string {
	var out []string
	for _, id := range forbidden {
		if s.has(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// entityKey builds the comparable key used to match required/forbidden evidence and
// citations by entity identity, per README.md#evidence-ref-id-matching: entityType/entityID,
// never the opaque wire evidence_ref_id.
func entityKey(entityType, entityID string) string { return entityType + "/" + entityID }

// equalSorted reports whether a and b contain exactly the same elements, ignoring order and
// duplicates.
func equalSorted(a, b []string) bool {
	sa, sb := newStringSet(a...).sorted(), newStringSet(b...).sorted()
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}
