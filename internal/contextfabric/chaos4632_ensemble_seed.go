package contextfabric

import (
	"crypto/sha256"
	"encoding/binary"
)

// CHAOS-4632 §4.1: derived per-sample seeds for the interpret ensemble.
//
// TODO(CHAOS-4631 / lane-4631, REMOVE): this derivation belongs to slice
// S1, which owns pinning the interpret sampler. S1 had not merged when
// this slice needed it, and the design makes the SHARED scheme
// load-bearing rather than incidental:
//
//	"S1's Shape-distribution measurement must sample under this same
//	 scheme, or it measures a distribution the running system will never
//	 produce."
//
// So this is a temporary internal copy, flagged in the PR body. When S1
// lands its own derivation, DELETE this file and call S1's -- do not leave
// two. Two derivations that drift silently break the exact property the
// shared scheme exists to provide, and neither test suite would catch it,
// because each would be self-consistent.
//
// The input is CanonicalizeQuestion's own hash (answer_reuse.go:45), not
// the raw question string. That choice is deliberate and is the one point
// where this could reasonably have gone the other way: using the canonical
// form means "same question, different whitespace or trailing punctuation"
// seeds identically, so a replay actually replays. It also means the seed
// inherits CanonicalizeQuestion's documented narrowness (no stemming, no
// synonym folding), which is the correct inheritance -- a seed that folded
// more aggressively than the reuse key would seed two questions the reuse
// key considers different.

// EnsembleSeed derives the seed for sample index i of the interpret
// ensemble for question.
//
// WHY DISTINCT SEEDS ARE LOAD-BEARING, not a detail. One shared pinned
// seed across all N samples would, against a provider that honours seeds
// at all, return near-identical samples. The consensus then becomes
// VACUOUSLY UNANIMOUS: it fakes the very stability this slice exists to
// measure, and it defeats refuse-to-guess by never producing a tie. A
// consensus over N copies of one sample is not a consensus.
//
// Arbitrary per-sample seeds would fix the diversity but lose replay.
// Distinct-and-DERIVED gives both: the ensemble is diverse AND an entire
// turn is reproducible from the question hash alone.
//
// The value is a non-negative int64. Providers reject negative seeds, and
// the high bit is masked off rather than the value being taken modulo some
// bound -- masking keeps the derivation injective in the low 63 bits,
// where a modulus would collide seeds for no benefit.
func EnsembleSeed(question string, index int) int64 {
	var indexBytes [8]byte
	binary.BigEndian.PutUint64(indexBytes[:], uint64(index))
	// The question hash is already the SHA-256 of the canonical question;
	// hashing it again WITH the index is what separates the samples. A
	// simple "hash + index" arithmetic offset would make consecutive
	// samples' seeds adjacent integers, which some providers map to
	// near-identical decoding paths -- the diversity this exists for
	// would be lost while looking present.
	sum := sha256.Sum256(append([]byte(QuestionHash(question)), indexBytes[:]...))
	return int64(binary.BigEndian.Uint64(sum[:8]) &^ (1 << 63))
}

// EnsembleSeeds derives all n seeds for question, in sample order.
// n is bounded by BoundEnsembleSize before use.
func EnsembleSeeds(question string, n int) []int64 {
	bounded := BoundEnsembleSize(n)
	seeds := make([]int64, 0, bounded)
	for i := 0; i < bounded; i++ {
		seeds = append(seeds, EnsembleSeed(question, i))
	}
	return seeds
}
