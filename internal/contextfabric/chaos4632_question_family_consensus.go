package contextfabric

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632 §4.1 mechanism 2: N-sample consensus. SHADOW ONLY -- see
// chaos4632_question_family_vocab.go's package-level note.
//
// WHY A CONSENSUS AT ALL. The six replicate captures (kiac/dh_0830, REAL
// data; see chaos4632_question_family_precedence.go for the table) show
// THREE distinct Shape values across two questions and SubjectTerms
// missing entirely in one replicate. No single model-emitted field on this
// wire is stable enough to anchor a family decision, so any design that
// keys on one sample of one field inherits exactly the instability it set
// out to remove. The family is therefore decided by self-consistency over
// N samples, aggregated by a rule that is pure server-side arithmetic:
// fixed, total, versioned, and with its input distribution recorded.
//
// What this CANNOT do, stated so nobody reads more into it: it cannot
// invent certainty the model did not have. A split ensemble resolves to
// unclassified. That is the design working, not failing.

// QuestionFamilySource names how a family outcome was reached. Closed
// vocabulary, mirroring WindowClassSource's model|fallback|none shape,
// which chris's telemetry rules already understand.
// CHAOS-4636 promoted this vocabulary to the wire beside the family itself
// (contractsv1.ContextFabricQuestionFamilySource). Alias, never a copy --
// same reasoning as QuestionFamily.
type QuestionFamilySource = contractsv1.ContextFabricQuestionFamilySource

const (
	// QuestionFamilySourceModelConsensus: N>1 samples, a strict majority
	// agreed, and one whole winning sample was selected from among them.
	QuestionFamilySourceModelConsensus = contractsv1.ContextFabricQuestionFamilySourceModelConsensus // "model_consensus"
	// QuestionFamilySourceModel: N==1 -- the degrade path. The precedence
	// table alone decided, with no consensus behind it. §4.1: "the
	// resolver falls back to N=1 plus the precedence table, recording
	// source = model rather than model_consensus -- a visibly weaker
	// guarantee rather than an invisible cost."
	QuestionFamilySourceModel = contractsv1.ContextFabricQuestionFamilySourceModel // "model"
	// QuestionFamilySourcePluralityRejected: N>1 samples ran and NO strict
	// majority emerged. The family is unclassified. This is deliberately
	// a DIFFERENT source from "none": a tie that was refused is a
	// different operational state from never having sampled, and
	// collapsing them would make "the model was split" invisible -- which
	// is the exact class of miss CHAOS-4085 cost hours to.
	QuestionFamilySourcePluralityRejected = contractsv1.ContextFabricQuestionFamilySourcePluralityRejected // "model_plurality_rejected"
	// QuestionFamilySourceCarried: mechanism 3 -- the family came from a
	// confirmed prior-turn receipt and NO model call was made for it.
	// Declared here because it is part of the closed source vocabulary
	// §4.3 pins; the carry itself is S5's work (it extends CHAOS-4387).
	QuestionFamilySourceCarried = contractsv1.ContextFabricQuestionFamilySourceCarried // "carried"
	// QuestionFamilySourceNone: no samples at all (the interpret call
	// failed, or the resolver was not run).
	QuestionFamilySourceNone = contractsv1.ContextFabricQuestionFamilySourceNone // "none"
)

// QuestionFamilyOutcome is the resolver's whole verdict.
type QuestionFamilyOutcome struct {
	// Family is never empty -- unclassified is a real member, not a zero
	// value standing in for absence.
	Family QuestionFamily
	Source QuestionFamilySource
	// WinningSampleIndex is the index into the input samples of the ONE
	// sample every downstream field comes from, or -1 when there is no
	// winner (no majority, or no samples).
	//
	// THE WINNER IS THE SAMPLE, NOT THE LABEL. Round 3 of the design
	// review found the hole: aggregating only the family label leaves
	// GroupKind, ScopeAnchorTerm and FactRequirements unselected, so two
	// samples agreeing on grouped_cohort_status while disagreeing on
	// GroupKind would have no rule to decide the group axis -- and
	// mechanism 3 would then carry that arbitrary value across every
	// later turn. FIELD-WISE MIXING IS FORBIDDEN, because a plan
	// assembled from two samples is a plan no model actually proposed.
	WinningSampleIndex int
	// Winner is the winning sample's own resolved outcome, zero-valued
	// when WinningSampleIndex is -1.
	Winner FamilySampleOutcome
	// WinningSample is the winning sample itself -- GroupKind,
	// ScopeAnchorTerm and all. Every downstream field comes from HERE and
	// from nowhere else.
	WinningSample FamilySample
	// SampleFamilies is the per-sample POST-FILTER distribution: closed
	// keys, counts only.
	SampleFamilies map[QuestionFamily]int
	// Samples is one row per sample, in input order.
	Samples []FamilySampleOutcome
	// DowngradedCount is how many samples were downgraded.
	DowngradedCount int
	// ConsensusFieldDivergence counts samples IN THE MAJORITY that agreed
	// on the family but disagreed with the winner on GroupKind or the
	// scope anchor.
	//
	// §4.1: "a countable signal that the family was agreed but its
	// parameters were not, which is exactly the state that should later
	// justify asking rather than assuming."
	ConsensusFieldDivergence int
	// Version is the family definition-table version in force.
	Version string
}

// QuestionFamilyEnsembleMax bounds N. §4.1: interpret x N multiplies token
// cost on EVERY turn 1, the most frequent call in the product, so N has a
// configured ceiling and the resolver degrades rather than silently
// spending.
const QuestionFamilyEnsembleMax = 7

// QuestionFamilyEnsembleDefault is 1 -- the DEGRADE PATH, deliberately the
// shipped default.
//
// At N=1 the resolver makes ZERO extra model calls: it runs the precedence
// table over the single interpret sample the engine already produced, and
// records source=model. That is what makes "zero behaviour change" a
// provable property of the merged default rather than an assertion --
// there is no extra call to change latency, no extra tokens to change
// cost, and nothing gated on the outcome either way. N>1 is opt-in by
// configuration, and is what the labelled-correctness and stability
// measurements run under.
const QuestionFamilyEnsembleDefault = 1

// BoundEnsembleSize clamps a configured N into [1, QuestionFamilyEnsembleMax].
// A zero or negative configured value means "unset" and yields the
// default; an over-large one is clamped rather than rejected, because a
// misconfiguration must degrade to a weaker guarantee, never fail an
// investigation.
func BoundEnsembleSize(configured int) int {
	if configured <= 0 {
		return QuestionFamilyEnsembleDefault
	}
	if configured > QuestionFamilyEnsembleMax {
		return QuestionFamilyEnsembleMax
	}
	return configured
}

// ResolveQuestionFamily is the whole §4.1 mechanism-2 aggregation.
//
// Total, deterministic, and free of any model, graph, or database: given
// the same samples in the same order it returns the same outcome, which is
// what makes it testable without a model in the loop.
//
// The rule, in order:
//
//  1. Every sample is run through the §4.2 precedence table
//     independently, against ITS OWN Shape and terms. The compatibility
//     check survives from the design's first revision but is demoted from
//     stabiliser to per-sample sanity filter.
//  2. The answer is the STRICT MAJORITY of the N post-filter families.
//     Strict means > N/2. NO STRICT MAJORITY -> unclassified. A tie is
//     never broken by picking a side.
//  3. Among the samples holding the majority family, the winner is the
//     FIRST BY TOTAL KEY -- the sample's own canonical serialization.
//     Every downstream field comes from that one sample.
//
// On the six real captures, a majority rule already resolves Q-A (3 of 4
// discovered_cohort) and correctly REFUSES Q-B at n=2 (1-1) rather than
// confidently picking the wrong family -- which is the behaviour we want
// from a tie.
func ResolveQuestionFamily(samples []FamilySample) QuestionFamilyOutcome {
	outcome := QuestionFamilyOutcome{
		Family:             QuestionFamilyUnclassified,
		Source:             QuestionFamilySourceNone,
		WinningSampleIndex: -1,
		SampleFamilies:     map[QuestionFamily]int{},
		Version:            QuestionFamilyTableVersion,
	}
	if len(samples) == 0 {
		return outcome
	}

	outcome.Samples = make([]FamilySampleOutcome, 0, len(samples))
	for _, sample := range samples {
		resolved := ResolveFamilyForSample(sample)
		outcome.Samples = append(outcome.Samples, resolved)
		outcome.SampleFamilies[resolved.Family]++
		if resolved.Downgraded {
			outcome.DowngradedCount++
		}
	}

	// N == 1: the degrade path. There is no consensus to take -- one
	// sample cannot corroborate itself -- so the precedence table's own
	// verdict stands and the source says so.
	if len(samples) == 1 {
		outcome.Family = outcome.Samples[0].Family
		outcome.Source = QuestionFamilySourceModel
		outcome.WinningSampleIndex = 0
		outcome.Winner = outcome.Samples[0]
		outcome.WinningSample = samples[0]
		return outcome
	}

	majority, ok := strictMajorityFamily(outcome.SampleFamilies, len(samples))
	if !ok {
		// Refuse to guess. Family stays unclassified; the source records
		// that a plurality EXISTED and was rejected, which is a different
		// state from never having sampled.
		outcome.Source = QuestionFamilySourcePluralityRejected
		return outcome
	}

	winnerIndex := selectWinningSample(samples, outcome.Samples, majority)
	outcome.Family = majority
	outcome.Source = QuestionFamilySourceModelConsensus
	outcome.WinningSampleIndex = winnerIndex
	outcome.Winner = outcome.Samples[winnerIndex]
	outcome.WinningSample = samples[winnerIndex]
	outcome.ConsensusFieldDivergence = countFieldDivergence(samples, outcome.Samples, majority, winnerIndex)
	return outcome
}

// strictMajorityFamily returns the family held by MORE THAN HALF the
// samples. Strictly more than half: at N=2 a 1-1 split has no majority,
// and at N=4 a 2-2 split has none either. Nothing here breaks a tie.
func strictMajorityFamily(distribution map[QuestionFamily]int, total int) (QuestionFamily, bool) {
	for family, count := range distribution {
		if count*2 > total {
			return family, true
		}
	}
	return "", false
}

// selectWinningSample picks ONE whole sample from among those holding the
// majority family: the first by TOTAL KEY, where the key is the sample's
// own canonical JSON serialization.
//
// WHY A TOTAL KEY AND NOT "THE FIRST ONE". Input order is not a property of
// the question -- the N samples run CONCURRENTLY, so the order they land
// in the slice depends on scheduling. Selecting by input position would
// make the winner, and therefore the GroupKind and scope anchor that
// mechanism 3 carries into every later turn, depend on a race. A total key
// over the sample's own content makes the selection a function of the
// SAMPLES, not of the order they arrived in, so a replay of the same
// question with the same derived seeds reproduces the same winner.
//
// A canonical JSON encoding is the total key because FamilySample's fields
// are all strings and string slices: encoding/json emits struct fields in
// declaration order and slices in element order, so the encoding is
// deterministic for a given value. Ties in the key are impossible to break
// meaningfully -- two samples with identical keys are identical samples,
// so either is the same answer.
func selectWinningSample(samples []FamilySample, outcomes []FamilySampleOutcome, majority QuestionFamily) int {
	type keyed struct {
		index int
		key   string
	}
	candidates := make([]keyed, 0, len(samples))
	for i, resolved := range outcomes {
		if resolved.Family != majority {
			continue
		}
		candidates = append(candidates, keyed{index: i, key: canonicalSampleKey(samples[i])})
	}
	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].key != candidates[b].key {
			return candidates[a].key < candidates[b].key
		}
		// Identical keys mean identical samples; index keeps the sort
		// total so the result never depends on sort.Slice's own
		// instability.
		return candidates[a].index < candidates[b].index
	})
	return candidates[0].index
}

func canonicalSampleKey(sample FamilySample) string {
	encoded, err := json.Marshal(sample)
	if err != nil {
		// FamilySample contains only strings and string slices, so
		// Marshal cannot fail. If it somehow did, an empty key still
		// yields a TOTAL order (every such sample sorts first, then by
		// index), never a panic and never a nondeterministic winner.
		return ""
	}
	return string(encoded)
}

// countFieldDivergence counts samples that agreed with the winner on the
// FAMILY but disagreed on the parameters that family will be planned with.
//
// Only GroupKind and ScopeAnchorTerm are compared, because those are the
// two the winning-sample rule exists to decide (§4.1, round 3's finding).
// The winner itself is never counted.
func countFieldDivergence(samples []FamilySample, outcomes []FamilySampleOutcome, majority QuestionFamily, winnerIndex int) int {
	winner := samples[winnerIndex]
	divergent := 0
	for i, resolved := range outcomes {
		if i == winnerIndex || resolved.Family != majority {
			continue
		}
		if samples[i].GroupKind != winner.GroupKind || samples[i].ScopeAnchorTerm != winner.ScopeAnchorTerm {
			divergent++
		}
	}
	return divergent
}

// FamilySampler produces ONE interpret sample, identified by its SAMPLE
// INDEX -- never by a seed.
//
// The index, not a seed, is the whole point. Seed derivation is
// genkitruntime's own unexported concern behind
// Runtime.InterpretQuestionForSample (CHAOS-4631), so a caller cannot
// derive, pass, or even observe a seed. That removes by construction the
// failure this signature used to invite: two derivations drifting apart
// while each test suite stays self-consistent and neither notices.
//
// An earlier revision of this file DID derive seeds, in a local copy
// carrying a TODO-remove, because S1 had not merged when S2 needed the
// scheme. S1 has merged (d00080fd) and the copy is deleted -- which is
// what a TODO-remove is for.
type FamilySampler func(ctx context.Context, sample int) (FamilySample, error)

// ResolveQuestionFamilyEnsemble runs n interpret samples CONCURRENTLY and
// resolves the family from them.
//
// Concurrency is the design's own point: interpret is the cheap call --
// turn 1 costs 2.0-2.8 s today, and the samples are independent. Running
// them serially would multiply turn-1 latency by n, which is the cost that
// would make the ensemble unshippable.
//
// The sampler receives sample indices 0..n-1 and is expected to call
// Runtime.InterpretQuestionForSample with each. Distinct-and-derived seeds
// still hold -- they are simply derived inside genkitruntime, where the
// only copy lives.
//
// FAILED SAMPLES ARE DROPPED, NOT SUBSTITUTED. A sampler error yields no
// sample; the consensus then runs over however many succeeded, and the
// strict-majority denominator is the number of SUCCESSFUL samples, not n.
// Substituting a zero-valued sample would inject a phantom unclassified
// vote no model produced -- the field-wise fabrication this design forbids
// one level up. If every sample fails, the outcome is the zero-sample
// outcome (unclassified / source=none), the honest report that nothing was
// measured.
//
// Sample ORDER is preserved as index order, not completion order, so the
// per-sample telemetry rows are reproducible across runs. The winner is
// selected by total key anyway (selectWinningSample), so order does not
// decide the outcome -- but a reproducible ordering makes two runs of the
// same question diffable, which a completion-ordered slice would not be.
func ResolveQuestionFamilyEnsemble(ctx context.Context, n int, sampler FamilySampler) (QuestionFamilyOutcome, []FamilySample) {
	bounded := BoundEnsembleSize(n)
	collected := make([]FamilySample, bounded)
	ok := make([]bool, bounded)

	var wg sync.WaitGroup
	for i := 0; i < bounded; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			sample, err := sampler(ctx, index)
			if err != nil {
				return
			}
			collected[index] = sample
			ok[index] = true
		}(i)
	}
	wg.Wait()

	samples := make([]FamilySample, 0, bounded)
	for i := range collected {
		if ok[i] {
			samples = append(samples, collected[i])
		}
	}
	return ResolveQuestionFamily(samples), samples
}
