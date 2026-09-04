package v1

// ContextFabricMinimumAnswerBytes is the smallest serialized size a VALID
// investigation result can have: 1001 bytes.
//
// IT GOVERNS NO VALIDATION, AND THAT IS DELIBERATE. Read this before pointing
// any validator at it.
//
// The measured floor sits BELOW ContextFabricSerializedBytesMin (8192), which
// is the smallest budget a caller may already request. Every existing surface
// -- request options, ACR_MAX_SERIALIZED_BYTES, and the MCP budget -- refuses
// below 8192, and 8192 comfortably holds a 1001-byte answer with 7191 bytes to
// spare. Re-pointing any of those at this constant would LOOSEN them by 7191
// bytes, and lowering the published `minimum` in the JSON Schemas and the Helm
// values schema would loosen the wire contract for every consumer.
//
// This constant therefore documents a fact; it does not enforce a rule.
// Enforcement belongs to the PER-REQUEST floor, because the size an answer
// needs depends on the question: the question-driven floor is checked at the
// request boundary before the model call, and the full floor after
// interpretation, each refusing with its own closed reason token.
//
// HOW IT IS DERIVED. The irreducible fixture in answer_bound_fixtures.go is
// built from answerBoundTable() with every field at its minimum, and
// TestMinimumAnswerBytesMatchesTheConstructedFixture asserts this constant
// equals that fixture's measured size. Changing any Min in the table fails
// that test rather than silently moving the floor.
//
// WHY THE TIE MATTERS. A hand-built fixture measured this number four times and
// was wrong four times -- 13606, 64166, 2160, 2153 -- because nothing connected
// the constant to a document anyone had validated. Each value was reviewed,
// merged or nearly merged, and wrong. Three further adversarial rounds against
// the CONSTRUCTED fixture moved it again, 1054 -> 1004 -> 1001, each time
// because a field was not actually at its minimum.
//
// The fifth move is the one that mattered most: once the number was right, it
// turned out to sit below a bound the service already enforced, which inverted
// the premise of the work built on top of it. A static constant derived from a
// measured artifact must be compared against every EXISTING bound before any
// validator is pointed at it.
//
// 1001 -> 1023, S7c (+22 bytes). The completeness block gained a required
// `state`, so the smallest valid answer now carries `"state":"not_derived"`
// and cannot be smaller. The outcome set itself contributes nothing to the
// floor -- it is omitempty and the irreducible answer has no rows -- so the
// whole of the increase is the one field every answer must carry.
//
// It is stated as a move rather than edited quietly because this number's
// own history is the argument for doing so: it moved five times, four of
// them wrongly, and the fifth move inverted the premise of the work built on
// it. The move is safe against every existing bound, which is the check that
// history demands: the smallest budget a caller may request is
// ContextFabricSerializedBytesMin at 8192, so 1023 still leaves 7169 bytes
// of headroom and no request, config or MCP surface changes meaning.
const ContextFabricMinimumAnswerBytes = 1023
