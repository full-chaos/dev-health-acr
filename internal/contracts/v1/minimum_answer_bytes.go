package v1

// ContextFabricMinimumAnswerBytes is the smallest serialized-byte budget under
// which an answer can exist at all. It is MEASURED, not chosen, and
// minimum_answer_bytes_test.go is where it is measured.
//
// WHAT IT MEASURES. The smallest document that is still an ANSWER rather than an
// envelope: every envelope field at its own validated maximum -- an 8000-byte
// question, maximal version metadata, a plan carrying every fact kind and the
// full ContextFabricPlanNarrowingMaxCount narrowing steps -- plus the
// irreducible answer content of one committed subject, one driver, and the two
// facts that driver cites. The document is run through Validate() BEFORE being
// measured: a measurement of a document the contract would reject bounds
// nothing.
//
// THE NUMBER IT REPLACES. The minimal-answer-floor paper carried a provisional
// 32768 and said plainly of it: "the paper does not get to invent this number."
// This is the measurement that supersedes it. It is deliberately LOWER, because
// raising a request minimum is an INPUT tightening -- every byte above the true
// worst case is caller breakage bought for nothing. It is consumer-identical to
// the provisional value: ask-dev requests 262,144 and MCP defaults to 65,536, so
// both clear either number, and ask-dev's one sub-minimum test fixture breaks at
// both. Only the caller-facing tightening differs, which is why the smaller
// number wins.
//
// WHY 16384 AND NOT THE MEASURED 13,606. The measured worst case is 13,606
// bytes. The pin is the next power of two above it, which buys headroom for a
// bounded envelope field this measurement has not thought to maximize. Rounding
// is normally how a pin goes wrong -- slack absorbs growth, and the real worst
// case creeps toward the constant while the test keeps passing -- so the guard
// is TWO-SIDED and that is what makes the rounding safe:
//
//	measured <= constant          growth fails, instead of being absorbed
//	constant <  2 * measured      drift fails, so the pin cannot wander far
//	                              above the true floor and quietly over-tighten
//	                              an INPUT bound on callers
//
// 16,384 < 27,212, so the headroom bound holds with room to spare. Either
// assertion failing is a real signal: the first says the floor moved, the
// second says the pin no longer describes it.
//
// WHY IT IS A NEW CONSTANT rather than a raise of ContextFabricSerializedBytesMin.
// That constant is load-bearing elsewhere:
// ContextFabricClaimedFactCombinedContentBytesMax derives from it, so raising it
// in place would silently multiply CHAOS-4785's dual-table fact bound -- a
// model-authorable output bound -- inside a change whose whole purpose is to
// tighten a budget. The two quantities are unrelated and now say so.
//
// THE PROOF THIS EXISTS FOR. Request validation bounds the echoed question at
// 8000 characters while ContextFabricSerializedBytesMin is 8192. The maximal
// envelope measures 12,791 bytes carrying ZERO budgeted items -- no driver, no
// fact, no candidate, no member. So a caller may today legally configure a
// service, or request a budget, at a size in which no answer can exist. The
// floor paper argued this on paper; the accompanying test executes it.
const ContextFabricMinimumAnswerBytes = 16384
