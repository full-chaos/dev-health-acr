package v1

// ContextFabricMinimumAnswerBytes is the smallest serialized-byte budget under
// which an answer can exist FOR ANY REQUEST. It is MEASURED, not chosen, and
// minimum_answer_bytes_test.go is where it is measured.
//
// IT IS HALF OF A TWO-PART RULE, and the split is the substance of this row.
//
// A first attempt pinned this constant to the MAXIMAL valid answer. That was
// wrong, and measurably so: the maximal answer is 369,705 bytes, which is above
// every budget any shipped consumer asks for -- the web client sends 262,144
// and the MCP default is 65,536 -- so enforcing it would refuse both rather
// than tighten anything.
//
// The reason is structural. The maximal answer is dominated by two
// request-shaped fields: the echoed question (bounded at 8000 RUNES, and the
// worst-case rune costs six serialized bytes because Go's JSON encoder escapes
// it, so up to 48,002 bytes) and the interpretation's subject and comparison
// terms (50 each at 512 runes, up to ~306,000 bytes). BOTH ARE FIXED: no
// narrowing or degradation path reduces them, because narrowing reaches only
// cohort members and facts, and the budget assertion measures without reducing.
//
// So the smallest answer a request can have is a FUNCTION OF THAT REQUEST, and
// no static constant expresses it. Pinned at the maximum it refuses everyone;
// pinned lower it is not a floor for every request.
//
// THIS CONSTANT IS THEREFORE THE REQUEST-INDEPENDENT HALF: the irreducible
// envelope (a one-rune question, one one-rune term) plus the irreducible answer
// content (one committed subject, one driver, the two facts it cites). It is
// what a DEPLOYMENT can be checked against, because a deployment cannot know
// what questions it will be asked. The request-dependent half is a runtime
// check that measures the minimal answer for the request in hand.
//
// The measured document is run through Validate() BEFORE being measured. That
// gate caught three bad fixtures while this was written -- a version field
// padded past its own bound, a driver citing evidence the result did not carry,
// and a stale completeness census. A measurement of a document the contract
// rejects bounds nothing.
//
// WHY IT IS NOT ROUNDED. A rounded pin absorbs growth: add a bounded envelope
// field and a constant with slack keeps passing while the real floor creeps
// toward it. Pinned exactly, any change fails at the moment it is made and the
// mutation kills in both directions. The cost is accepted and disclosed: this
// constant is published in the request schema, so any growth is a wire-visible
// change and a consumer pin bump.
//
// WHY IT IS A NEW CONSTANT rather than a raise of ContextFabricSerializedBytesMin:
// ContextFabricClaimedFactCombinedContentBytesMax used to derive from that one
// by arithmetic, so raising it in place would have multiplied a
// model-authorable output bound inside a change meant to tighten a budget. That
// derivation is re-anchored to its own real-data measurement.
const ContextFabricMinimumAnswerBytes = 2160
