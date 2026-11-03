package contextfabric

import (
	"reflect"
	"slices"
	"strings"
	"sync"
)

// withReaderSubjectText restates, on a served copy, the raw source ids a result
// saved before CHAOS-12619 holds in its graph text. The read repairs a fresh
// answer's labels and facts (falkorgraph nodeProperties, graphrank edgeFact), but a
// stored row keeps what its graph reads gave it, and a read by id serves it with
// no rebuild: the query version retires reuse, not the row.
//
//   - Every subject ref anywhere in the result whose label is its own raw id reads
//     as SubjectReadableLabel (exact: a title or a name stands).
//   - A path's why_relevant, composed from its one edge, is repaired with that
//     edge's ends (RepairRawEdgeFact: the whole composition only).
//   - A graph driver's summary is the same fact, and its title ends with the
//     edge's To label, so both are repaired from the one path the driver cites.
//
// Only what changes is copied; a result with no raw text is returned as it is, and
// the stored result is never changed.
func withReaderSubjectText(result InvestigationResult) InvestigationResult {
	edges := make(map[string]RelationshipEdge, len(result.Paths))
	var paths []RelationshipPath
	for index, path := range result.Paths {
		if len(path.Edges) != 1 {
			continue
		}
		edge := path.Edges[0]
		edges[path.PathID] = edge
		if why := RepairRawEdgeFact(path.WhyRelevant, string(edge.Type), edge.From, edge.To); why != path.WhyRelevant {
			if paths == nil {
				paths = slices.Clone(result.Paths)
			}
			paths[index].WhyRelevant = why
		}
	}
	if paths != nil {
		result.Paths = paths
	}
	var drivers []DriverJudgment
	for index, driver := range result.Drivers {
		if len(driver.PathIDs) != 1 {
			continue
		}
		edge, found := edges[driver.PathIDs[0]]
		if !found {
			continue
		}
		summary := RepairRawEdgeFact(driver.Summary, string(edge.Type), edge.From, edge.To)
		title := readerDriverTitle(driver.Title, string(edge.Type), edge.To)
		if summary == driver.Summary && title == driver.Title {
			continue
		}
		if drivers == nil {
			drivers = slices.Clone(result.Drivers)
		}
		drivers[index].Summary, drivers[index].Title = summary, title
	}
	if drivers != nil {
		result.Drivers = drivers
	}
	if restated, changed := readerSubjectValue(reflect.ValueOf(result)); changed {
		return restated.Interface().(InvestigationResult)
	}
	return result
}

// ReaderSubjectText is withReaderSubjectText for a route that projects the answer
// itself (the unstamped projection tier), so no serve tier shows a raw id.
func ReaderSubjectText(result InvestigationResult) InvestigationResult {
	return withReaderSubjectText(result)
}

// readerDriverTitle repairs a graph driver's title, which graphrank composes as the
// relationship's words, ": " and the To label (graphrank.relationTitle). Only that
// whole composition changes: the relationship's own title words, ": " and the To
// label while that is a raw form of the endpoint's id (isRawForm: its raw id, or
// the reader form an earlier rule gave it, CHAOS-12716). Any other title stands
// (codex r4).
func readerDriverTitle(title, relation string, to SubjectRef) string {
	raw, ok := RawSubjectLabel(to.Kind, to.CanonicalID)
	if !ok || !isRawForm(to.Kind, raw, to.Label) {
		return title
	}
	head := RelationTitleWords(relation)
	if head == "" || title != head+": "+to.Label {
		return title
	}
	return head + ": " + SubjectReadableLabel(to)
}

// RelationTitleWords are a relationship type's words in a graph driver's title:
// lower case, '_' as a space, each word with a capital first letter. graphrank's
// relationTitle composes the title with it, so the serve repair reads the same
// words the writer wrote.
func RelationTitleWords(relation string) string {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(relation, "_", " ")))
	for index, word := range words {
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

var subjectRefType = reflect.TypeOf(SubjectRef{})

// readerSubjectValue is value with every subject ref restated, and whether any
// was. It copies only the containers on the way to a change.
func readerSubjectValue(value reflect.Value) (reflect.Value, bool) {
	if !mayHoldSubject(value.Type()) {
		return value, false
	}
	switch value.Kind() {
	case reflect.Struct:
		if value.Type() == subjectRefType {
			subject := value.Interface().(SubjectRef)
			if label := SubjectReadableLabel(subject); label != subject.Label {
				subject.Label = label
				return reflect.ValueOf(subject), true
			}
			return value, false
		}
		var out reflect.Value
		for index := 0; index < value.NumField(); index++ {
			if !value.Type().Field(index).IsExported() {
				continue
			}
			field, changed := readerSubjectValue(value.Field(index))
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.New(value.Type()).Elem()
				out.Set(value)
			}
			out.Field(index).Set(field)
		}
		if out.IsValid() {
			return out, true
		}
	case reflect.Pointer:
		if value.IsNil() {
			return value, false
		}
		if elem, changed := readerSubjectValue(value.Elem()); changed {
			out := reflect.New(value.Type().Elem())
			out.Elem().Set(elem)
			return out, true
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return value, false
		}
		var out reflect.Value
		for index := 0; index < value.Len(); index++ {
			elem, changed := readerSubjectValue(value.Index(index))
			if !changed {
				continue
			}
			if !out.IsValid() {
				if value.Kind() == reflect.Slice {
					out = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
				} else {
					out = reflect.New(value.Type()).Elem()
				}
				reflect.Copy(out, value)
			}
			out.Index(index).Set(elem)
		}
		if out.IsValid() {
			return out, true
		}
	case reflect.Map:
		if value.IsNil() {
			return value, false
		}
		var out reflect.Value
		iter := value.MapRange()
		for iter.Next() {
			elem, changed := readerSubjectValue(iter.Value())
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.MakeMapWithSize(value.Type(), value.Len())
				copier := value.MapRange()
				for copier.Next() {
					out.SetMapIndex(copier.Key(), copier.Value())
				}
			}
			out.SetMapIndex(iter.Key(), elem)
		}
		if out.IsValid() {
			return out, true
		}
	}
	return value, false
}

var subjectHolders sync.Map // reflect.Type -> bool

// mayHoldSubject reports whether a value of t can contain a subject ref through
// exported fields, pointers, slices, arrays or map values. An interface is not
// followed: the result's typed fields carry its subjects.
func mayHoldSubject(t reflect.Type) bool {
	if held, ok := subjectHolders.Load(t); ok {
		return held.(bool)
	}
	held := typeHoldsSubject(t, map[reflect.Type]bool{})
	subjectHolders.Store(t, held)
	return held
}

// typeHoldsSubject decides mayHoldSubject; a type met again on its own path adds
// nothing new, so it answers false there and the other paths decide.
func typeHoldsSubject(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if t == subjectRefType {
		return true
	}
	if visiting[t] {
		return false
	}
	visiting[t] = true
	switch t.Kind() {
	case reflect.Struct:
		for index := 0; index < t.NumField(); index++ {
			if t.Field(index).IsExported() && typeHoldsSubject(t.Field(index).Type, visiting) {
				return true
			}
		}
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return typeHoldsSubject(t.Elem(), visiting)
	}
	return false
}
