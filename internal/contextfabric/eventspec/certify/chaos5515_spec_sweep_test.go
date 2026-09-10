package certify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// This file is round r2's structural answer to "the same class as r1": a
// hand-picked list of pin tests (however many rounds add) can never prove
// coverage of every field of every event -- only a sweep GENERATED FROM
// eventspec.All can. It supersedes nothing above; the earlier named pins stay
// as the readable, intention-revealing controls a reader debugs from first.

// canonicalValueFor returns one internally-valid value for a declared field,
// used only to build this sweep's own control line -- never a value pinned
// as a real production expectation anywhere else.
func canonicalValueFor(f eventspec.Field) any {
	switch f.Type {
	case eventspec.FieldString:
		if len(f.ClosedVocabulary) > 0 {
			return f.ClosedVocabulary[0]
		}
		return "sweep_canonical_string"
	case eventspec.FieldInt:
		return 0
	case eventspec.FieldStringSlice:
		return []string{}
	case eventspec.FieldObjectSlice:
		row := map[string]any{}
		for _, nf := range f.Fields {
			row[nf.Key] = canonicalValueFor(nf)
		}
		// Exactly one representative row -- enough for the recursive sweep
		// below to reach every nested field without the census depending on
		// production's own row count for any real event.
		return []any{row}
	default:
		return nil
	}
}

// wrongTypeValueFor returns a JSON value of a shape declaredType does NOT
// allow -- used only to prove validateFields' own type check actually fires
// for a field the caller did not name in Want.
func wrongTypeValueFor(f eventspec.Field) any {
	if f.Type == eventspec.FieldInt {
		return "not-an-int" // a bare string is never a valid int
	}
	return 12345 // a bare number is never a valid string/slice/object_slice
}

// canonicalLineFor builds one complete, internally-consistent JSON-shaped
// line for ev: the slog envelope certify.Parse requires, plus every declared
// field carrying a canonical value.
func canonicalLineFor(ev eventspec.Event) map[string]any {
	line := map[string]any{
		"time":  "2026-09-10T00:00:00Z",
		"level": strings.ToUpper(string(ev.Level)),
		"msg":   ev.Msg,
	}
	for _, f := range ev.Fields {
		line[f.Key] = canonicalValueFor(f)
	}
	return line
}

// deepCopyLine round-trips a line through JSON so the copy carries the SAME
// decoded shapes Parse itself produces (float64, []any, map[string]any) --
// never a Go-native copy certify would not actually see from real slog
// output.
func deepCopyLine(t *testing.T, line map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("deepCopyLine: Marshal() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("deepCopyLine: Unmarshal() error = %v", err)
	}
	return out
}

// locate walks a decoded line down a sequence of object_slice keys (this
// sweep's own fixtures always carry exactly one representative row per
// nested level, by construction in canonicalValueFor) and returns the object
// at that depth to mutate.
func locate(t *testing.T, line map[string]any, path []string) map[string]any {
	t.Helper()
	obj := line
	for _, key := range path {
		arr, ok := obj[key].([]any)
		if !ok || len(arr) == 0 {
			t.Fatalf("locate: %q is not a non-empty array in the sweep fixture", key)
		}
		row, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("locate: %q[0] is not an object in the sweep fixture", key)
		}
		obj = row
	}
	return obj
}

func logFromLine(t *testing.T, line map[string]any) *Log {
	t.Helper()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("logFromLine: Marshal() error = %v", err)
	}
	log, err := Parse(b)
	if err != nil {
		t.Fatalf("logFromLine: Parse() error = %v", err)
	}
	return log
}

func countDeclaredFields(fields []eventspec.Field) int {
	n := 0
	for _, f := range fields {
		n++
		if f.Type == eventspec.FieldObjectSlice {
			n += countDeclaredFields(f.Fields)
		}
	}
	return n
}

// TestCertifySpecSweepCoversEveryDeclaredFieldOfEveryEvent is GENERATED FROM
// eventspec.All, not a hand-picked field list: for every event, every
// declared field, recursively into every nested object_slice field, it
// proves Certify refuses (a) the field missing when PresenceRequired, (b) a
// wrong JSON type, and (c) a value outside a declared closed vocabulary --
// exactly the three classes round r2 found silently accepted. The final
// census subtest proves the sweep actually reached every declared field
// rather than silently skipping one because a new event or field shape
// wasn't yet handled.
func TestCertifySpecSweepCoversEveryDeclaredFieldOfEveryEvent(t *testing.T) {
	for _, ev := range eventspec.All {
		ev := ev
		t.Run(ev.ID, func(t *testing.T) {
			base := canonicalLineFor(ev)
			attribution := map[string]any{}
			for _, k := range ev.Attribution {
				attribution[k] = base[k]
			}

			if _, err := Certify(logFromLine(t, deepCopyLine(t, base)), Assertion{Event: ev, Want: attribution}); err != nil {
				t.Fatalf("control: the sweep's own canonical line for %s was refused (fixture bug, not a finding): %v", ev.ID, err)
			}

			census := map[string]bool{}
			var sweep func(fields []eventspec.Field, path []string, keyPrefix string)
			sweep = func(fields []eventspec.Field, path []string, keyPrefix string) {
				for _, f := range fields {
					f := f
					censusKey := keyPrefix + f.Key
					census[censusKey] = true

					if f.Presence == eventspec.PresenceRequired {
						t.Run(censusKey+"/missing", func(t *testing.T) {
							mutated := deepCopyLine(t, base)
							delete(locate(t, mutated, path), f.Key)
							if _, err := Certify(logFromLine(t, mutated), Assertion{Event: ev, Want: attribution}); err == nil {
								t.Errorf("Certify() accepted %s missing on %s", censusKey, ev.ID)
							}
						})
					}

					t.Run(censusKey+"/wrong_type", func(t *testing.T) {
						mutated := deepCopyLine(t, base)
						locate(t, mutated, path)[f.Key] = wrongTypeValueFor(f)
						if _, err := Certify(logFromLine(t, mutated), Assertion{Event: ev, Want: attribution}); err == nil {
							t.Errorf("Certify() accepted %s wrong-typed on %s", censusKey, ev.ID)
						}
					})

					if len(f.ClosedVocabulary) > 0 {
						t.Run(censusKey+"/bad_vocab", func(t *testing.T) {
							mutated := deepCopyLine(t, base)
							locate(t, mutated, path)[f.Key] = "sweep_undeclared_vocab_value"
							if _, err := Certify(logFromLine(t, mutated), Assertion{Event: ev, Want: attribution}); err == nil {
								t.Errorf("Certify() accepted %s out-of-vocabulary on %s", censusKey, ev.ID)
							}
						})
					}

					if f.Type == eventspec.FieldObjectSlice {
						sweep(f.Fields, append(append([]string{}, path...), f.Key), censusKey+"[].")
					}
				}
			}
			sweep(ev.Fields, nil, "")

			if want := countDeclaredFields(ev.Fields); len(census) != want {
				t.Errorf("census: swept %d fields for %s, want %d -- every declared field, recursively, must be exercised", len(census), ev.ID, want)
			}
		})
	}
}
