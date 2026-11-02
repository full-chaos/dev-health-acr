package eventspec

import "testing"

// TestEveryRequiredWhenNamesAStringSibling (CHAOS-12519): the certifier reads
// a RequiredWhen trigger as a string, and skips silently when it is not one.
// So every trigger must name a sibling field of string type in the same field
// list, or a future non-string trigger would stop guarding without a failure.
func TestEveryRequiredWhenNamesAStringSibling(t *testing.T) {
	t.Parallel()
	var check func(id string, fields []Field)
	count := 0
	check = func(id string, fields []Field) {
		for _, field := range fields {
			if rw := field.RequiredWhen; rw != nil {
				count++
				var sibling *Field
				for i := range fields {
					if fields[i].Key == rw.Key {
						sibling = &fields[i]
					}
				}
				switch {
				case sibling == nil:
					t.Errorf("%s: %q requires on %q, which is not a sibling field", id, field.Key, rw.Key)
				case sibling.Type != FieldString:
					t.Errorf("%s: %q requires on %q, which has type %s, not string", id, field.Key, rw.Key, sibling.Type)
				}
				if field.Presence != PresenceConditional || len(rw.Values) == 0 {
					t.Errorf("%s: %q declares RequiredWhen but is not conditional or lists no values", id, field.Key)
				}
			}
			check(id, field.Fields)
		}
	}
	for _, event := range All {
		check(event.ID, event.Fields)
	}
	if count == 0 {
		t.Error("no RequiredWhen is declared; the check would pass an empty set")
	}
}
