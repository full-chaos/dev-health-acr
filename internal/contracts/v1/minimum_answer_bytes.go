package v1

// ContextFabricMinimumAnswerBytes is the smallest serialized size a VALID
// investigation result can have.
//
// It is DERIVED, not chosen. The irreducible fixture in answer_bound_fixtures.go
// is built from answerBoundTable() with every field at its minimum, and
// TestMinimumAnswerBytesMatchesTheConstructedFixture asserts this constant
// equals that fixture's measured size. Changing any Min in the table therefore
// fails that test rather than silently moving the floor.
//
// WHY THE TIE MATTERS. A hand-built fixture measured this number four times and
// was wrong four times -- 13606, 64166, 2160, 2153 -- because nothing connected
// the constant to a document anyone had validated. Each value was reviewed,
// merged or nearly merged, and wrong: the fixture padded a rune-counted field
// with ASCII, omitted the interpretation's terms, carried a live clock so the
// same document measured differently between runs, and inherited two
// non-minimal fields from a fixture written for another purpose. Three further
// adversarial rounds against the CONSTRUCTED fixture moved it again, 1054 ->
// 1004 -> 1001, each time because a field was not actually at its minimum.
//
// The lesson is not "measure more carefully". It is that a constant which is
// not mechanically tied to the artifact it describes will drift, and no amount
// of review reliably catches it.
const ContextFabricMinimumAnswerBytes = 1001
