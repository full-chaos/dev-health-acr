package devhealthfacts_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3783 acceptance harness.
//
// Unlike this package's other integration test, which starts its own
// throwaway ClickHouse container, this one measures against a POPULATED
// environment: pruning's effect depends on how many subjects and rows a real
// organization actually has, and an empty container would report a saving of
// zero for every case regardless of whether the planner works.
//
// It is therefore opt-in and skips by default. Point it at a live read-only
// ClickHouse and give it an organization that has data:
//
//	ACR_CHAOS3783_CLICKHOUSE_DSN='clickhouse://ch:ch@127.0.0.1:9000/default' \
//	ACR_CHAOS3783_ORG_ID='<org-uuid>' \
//	go test ./internal/contextfabric/devhealthfacts/ -run CHAOS3783 -v
//
// Every statement it issues is a SELECT; it never writes.
//
// # Every compared bundle comes from the real registry
//
// This harness never assembles a fact bundle of its own. REJECTED, do not
// reintroduce: an earlier version built its own comparison bundle by calling
// providers directly and stamping the fields the registry stamps, which made
// it a transcription of the registry pipeline. Every semantic it failed to
// mirror -- per-fact state rewrites, the aggregate fact cap, ordering,
// serialization -- surfaced as a separate review finding, three rounds
// running. A re-implemented comparison measures its own divergence from
// production, not pruning.
//
// Both bundles compared below come from the REAL
// FactCapabilityRegistry.ReadFacts, so truncation, caps, state stamping, and
// serialization are shared by construction and cannot diverge.
//
// # What the counterfactual is, and why it is counted rather than run
//
// The planner-free fan-out cannot be executed through the production path at
// all, and that is not an oversight: buildFactQuery refuses to ask a provider
// about a subject kind its capability does not support, and refuses an empty
// subject list. Forcing a plan-everything mode through ReadFacts would
// therefore not produce a fan-out -- it would reproduce the pre-CHAOS-3783
// whole-bundle failure this ticket exists to fix.
//
// So the counterfactual numbers are COUNTED, never executed: a planner-free
// implementation issues one round-trip per registered requirement and binds
// every investigation subject to each. That is a count of work not done --
// no bundle, no stamping, no serialization -- so it has nothing to diverge
// on.
//
// # What proves pruning did not change the answer
//
// A second REAL ReadFacts call with the pruned fact kinds removed from the
// request. If pruning is sound, asking for {A, B, C} where C is pruned must
// produce exactly the facts of asking for {A, B}: same pipeline, same caps,
// same truncation, same ordering. That is a falsifiable property comparing
// two production runs to each other, which is the shape the deleted baseline
// only appeared to have.
const (
	chaos3783DSNEnv = "ACR_CHAOS3783_CLICKHOUSE_DSN"
	chaos3783OrgEnv = "ACR_CHAOS3783_ORG_ID"
)

// chaos3783Case is one representative investigation: the subjects a graph
// resolution committed, and the fact-kind union interpretation plus graph
// discovery produced for it. Both are stated explicitly rather than inferred
// from a model, so the harness needs no model runtime and costs no tokens.
type chaos3783Case struct {
	name         string
	subjects     []contextfabric.SubjectRef
	requirements []contextfabric.FactKind
}

type chaos3783Measurement struct {
	providersQueried int
	subjectBindings  int
	prunedSources    int
	prunedKinds      []contextfabric.FactKind
	facts            int
	bundleBytes      int
	// factsDigest covers bundle.Facts ONLY (codex round-4 R4-3). Coverage is
	// deliberately excluded because it is SUPPOSED to differ between the two
	// runs -- the pruned observations are the whole point -- so folding it in
	// would make the identity claim unfalsifiable. The coverage difference is
	// asserted separately, and exactly, rather than ignored.
	factsDigest string
	coverage    map[contextfabric.FactKind]chaos3783Observation
	elapsed     time.Duration
	failed      bool
	failure     string
}

// chaos3783Observation is one coverage entry reduced to what the identity
// assertion compares.
type chaos3783Observation struct {
	state  contextfabric.SourceState
	reason string
}

func TestCHAOS3783PruningMeasurement(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(chaos3783DSNEnv))
	orgID := strings.TrimSpace(os.Getenv(chaos3783OrgEnv))
	if dsn == "" || orgID == "" {
		t.Skipf("set %s and %s to run the CHAOS-3783 pruning measurement against a populated ClickHouse", chaos3783DSNEnv, chaos3783OrgEnv)
	}

	ctx := context.Background()
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: dsn, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open ClickHouse query client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close ClickHouse query client: %v", err)
		}
	})

	principal := storage.Principal{OrgID: orgID}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	registered := make(map[contextfabric.FactKind]contextfabric.FactCapability)
	for _, capability := range registry.Capabilities() {
		registered[capability.Kind] = capability
	}

	discover := func(table, column string, kind contextfabric.SubjectKind, prefix string) []contextfabric.SubjectRef {
		t.Helper()
		subjects, err := chaos3783Subjects(ctx, client, orgID, table, column, kind, prefix, 3)
		if err != nil {
			// R4-1: never degrade to "no subjects of that kind". A failed
			// discovery would silently shrink the measurement.
			t.Fatalf("subject discovery failed, so any measurement below would understate the fan-out: %v", err)
		}
		return subjects
	}
	// devhealthschema:not-a-production-replica these table names are ARGUMENTS to a discovery helper,
	// selecting which table to read subjects from. No column type, engine or
	// sort key is mirrored here, so this cannot drift from production the way
	// a rival schema declaration would.
	repositories := discover("repos", "toString(id)", contextfabric.SubjectRepository, "repository:")
	workItems := discover("work_items", "work_item_id", contextfabric.SubjectWorkItem, "work_item:")
	teams := discover("capacity_forecasts", "team_id", contextfabric.SubjectTeam, "team:")
	if len(repositories)+len(workItems)+len(teams) == 0 {
		t.Fatalf("organization %q has no repositories, work items, or teams -- pick one with data", orgID)
	}
	t.Logf("discovered subjects: %d repository, %d work_item, %d team", len(repositories), len(workItems), len(teams))

	// The unions below are the ones this system actually produces. A
	// team-shaped question reaches the five team-scoped families; a cohort
	// question additionally carries the FactHealth/FactWorkload pair
	// falkorgraph merges for EVERY discovered cohort (reader.go); the mixed
	// case is what an open question resolving several entity kinds looks
	// like. The project-cohort case is the live-reachable failure that
	// motivated this issue.
	teamFamilies := []contextfabric.FactKind{
		contextfabric.FactHealth, contextfabric.FactWorkload, contextfabric.FactInvestment,
		contextfabric.FactReadiness, contextfabric.FactOperationalDeficiencies,
	}
	broadUnion := []contextfabric.FactKind{
		contextfabric.FactIdentity, contextfabric.FactMembership, contextfabric.FactStatus,
		contextfabric.FactWork, contextfabric.FactBlockers, contextfabric.FactPullRequests,
		contextfabric.FactReviews, contextfabric.FactContinuousIntegration, contextfabric.FactDeployments,
		contextfabric.FactIncidents, contextfabric.FactMetrics, contextfabric.FactHealth,
		contextfabric.FactWorkload, contextfabric.FactInvestment, contextfabric.FactReadiness,
		contextfabric.FactOperationalDeficiencies, contextfabric.FactSourceHealth,
	}

	cases := []chaos3783Case{
		{name: "team question, team-scoped union", subjects: teams, requirements: teamFamilies},
		{name: "team question, broad union", subjects: teams, requirements: broadUnion},
		{name: "repository question, broad union", subjects: repositories, requirements: broadUnion},
		{name: "work item question, broad union", subjects: workItems, requirements: broadUnion},
		{name: "mixed-kind open question, broad union", subjects: chaos3783Concat(teams, repositories, workItems), requirements: broadUnion},
		{
			name:         "project cohort (the live-reachable failure)",
			subjects:     []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project:titan", Label: "Titan"}},
			requirements: []contextfabric.FactKind{contextfabric.FactHealth, contextfabric.FactWorkload},
		},
	}

	type row struct {
		name             string
		naiveRoundTrips  int
		naiveBindings    int
		planned          chaos3783Measurement
		reduced          chaos3783Measurement
		identityIsTested bool
		allPruned        bool
	}
	rows := make([]row, 0, len(cases))
	plannedFailures := 0

	for _, testCase := range cases {
		if len(testCase.subjects) == 0 {
			t.Logf("skipping %q: no subjects of that kind in this organization", testCase.name)
			continue
		}
		naiveRoundTrips, naiveBindings := chaos3783NaiveCost(testCase, registered)
		planned := chaos3783Run(ctx, principal, registry, registered, testCase.subjects, testCase.requirements)
		if planned.failed {
			plannedFailures++
		}
		current := row{name: testCase.name, naiveRoundTrips: naiveRoundTrips, naiveBindings: naiveBindings, planned: planned}
		// The identity check: the same production path, asked only for the
		// kinds the planner did not prune.
		if !planned.failed && len(planned.prunedKinds) > 0 {
			remaining := chaos3783Without(testCase.requirements, planned.prunedKinds)
			switch {
			case len(remaining) > 0:
				current.reduced = chaos3783Run(ctx, principal, registry, registered, testCase.subjects, remaining)
				current.identityIsTested = true
			default:
				// Codex round-4 R4-4: every requirement was pruned -- the
				// project-cohort case, which is this ticket's headline. The
				// reduced run cannot be made, because
				// validateCanonicalFactRequest rejects a request with zero
				// requirements, so there is no "ask for nothing" call to
				// compare against.
				//
				// The identity statement for an empty reduced set is
				// therefore stated directly rather than skipped: an
				// investigation whose every capability was pruned must
				// produce no facts at all, and must explain every one of
				// them in coverage. Leaving it untested would have left the
				// headline case as the only unverified one.
				current.allPruned = true
			}
		}
		rows = append(rows, current)
	}

	t.Log("")
	t.Log("CHAOS-3783 pruning measurement")
	t.Log("  naive    = COUNTED counterfactual (one round-trip per registered requirement, every subject bound); never executed")
	t.Log("  planner  = real FactCapabilityRegistry.ReadFacts")
	t.Log("  identity = the same real ReadFacts with the pruned kinds removed from the request")
	t.Log("")
	for _, r := range rows {
		t.Logf("case: %s", r.name)
		t.Logf("  naive (counted): round-trips=%2d subject-bindings=%3d", r.naiveRoundTrips, r.naiveBindings)
		t.Logf("  planner        : round-trips=%2d subject-bindings=%3d facts=%4d bundle-bytes=%7d pruned=%2d elapsed=%v%s",
			r.planned.providersQueried, r.planned.subjectBindings, r.planned.facts, r.planned.bundleBytes,
			r.planned.prunedSources, r.planned.elapsed.Round(time.Millisecond), chaos3783FailureNote(r.planned))
		t.Logf("  saved          : %d round-trips (%.0f%%), %d subject-bindings",
			r.naiveRoundTrips-r.planned.providersQueried,
			chaos3783Percent(r.naiveRoundTrips-r.planned.providersQueried, r.naiveRoundTrips),
			r.naiveBindings-r.planned.subjectBindings)
		switch {
		case r.identityIsTested:
			t.Logf("  identity       : facts=%d facts-digest-match=%v", r.reduced.facts, r.reduced.factsDigest == r.planned.factsDigest)
		case r.allPruned:
			t.Logf("  identity       : every requirement pruned -- asserted zero facts and all-pruned coverage instead")
		}
	}
	t.Logf("planned-path failures: %d of %d cases", plannedFailures, len(rows))

	if plannedFailures != 0 {
		t.Fatalf("%d planned investigation(s) failed, want 0 -- pruning exists so a wide union stays answerable", plannedFailures)
	}
	for _, r := range rows {
		if r.planned.providersQueried > r.naiveRoundTrips {
			t.Fatalf("case %q issued MORE round-trips than a planner-free run would (%d > %d)", r.name, r.planned.providersQueried, r.naiveRoundTrips)
		}
		if r.planned.subjectBindings > r.naiveBindings {
			t.Fatalf("case %q bound MORE subjects than a planner-free run would (%d > %d)", r.name, r.planned.subjectBindings, r.naiveBindings)
		}
		if r.allPruned {
			// R4-4: the identity statement when nothing survives pruning.
			if r.planned.facts != 0 {
				t.Fatalf("case %q: every capability was pruned but the bundle carries %d fact(s)", r.name, r.planned.facts)
			}
			for kind, observation := range r.planned.coverage {
				if observation.state != contextfabric.SourcePruned {
					t.Fatalf("case %q: kind %q has state %q, want every kind pruned", r.name, kind, observation.state)
				}
				if strings.TrimSpace(observation.reason) == "" {
					t.Fatalf("case %q: pruned kind %q has no reason -- absence must be explainable", r.name, kind)
				}
			}
			continue
		}
		if !r.identityIsTested {
			continue
		}
		if r.reduced.failed {
			t.Fatalf("case %q: the pruned-kinds-removed run failed: %s", r.name, r.reduced.failure)
		}
		// The strongest guarantee this harness gives, and the reason the
		// pruning rule was built as a proof rather than a heuristic: pruning
		// removes WORK, never ANSWER. Both sides are real ReadFacts calls, so
		// a difference cannot be an artifact of how the test assembled a
		// bundle -- it can only mean the planner dropped something real.
		if r.planned.facts != r.reduced.facts || r.planned.factsDigest != r.reduced.factsDigest {
			t.Fatalf("case %q: pruning changed the FACTS (count %d vs %d, digest %s vs %s) -- it must only skip work that could not have produced facts",
				r.name, r.planned.facts, r.reduced.facts, r.planned.factsDigest, r.reduced.factsDigest)
		}
		// R4-3: coverage is SUPPOSED to differ between these two runs, so
		// rather than excluding it from the claim, assert the difference is
		// exactly the planner's pruned set and nothing else. That converts an
		// intentional difference into a checked property: a prune that also
		// perturbed a surviving capability's observation would fail here.
		pruned := make(map[contextfabric.FactKind]struct{}, len(r.planned.prunedKinds))
		for _, kind := range r.planned.prunedKinds {
			pruned[kind] = struct{}{}
		}
		for kind, plannedObservation := range r.planned.coverage {
			reducedObservation, present := r.reduced.coverage[kind]
			if _, wasPruned := pruned[kind]; wasPruned {
				if present {
					t.Fatalf("case %q: pruned kind %q still appears in the reduced run's coverage", r.name, kind)
				}
				continue
			}
			if !present {
				t.Fatalf("case %q: kind %q is in the planned coverage but missing from the reduced run -- the delta must be the pruned set alone", r.name, kind)
			}
			if plannedObservation != reducedObservation {
				t.Fatalf("case %q: kind %q was not pruned yet its observation changed (%+v vs %+v) -- pruning must not perturb a surviving capability",
					r.name, kind, plannedObservation, reducedObservation)
			}
		}
		for kind := range r.reduced.coverage {
			if _, known := r.planned.coverage[kind]; !known {
				t.Fatalf("case %q: kind %q appears only in the reduced run's coverage", r.name, kind)
			}
		}
	}
}

// chaos3783Run executes ONE real investigation fact read and measures it from
// the coverage the caller would actually receive.
func chaos3783Run(
	ctx context.Context,
	principal storage.Principal,
	registry *contextfabric.FactCapabilityRegistry,
	registered map[contextfabric.FactKind]contextfabric.FactCapability,
	subjects []contextfabric.SubjectRef,
	kinds []contextfabric.FactKind,
) chaos3783Measurement {
	request := contextfabric.CanonicalFactRequest{
		// Engine refuses any axis but current before it ever reaches the
		// fact layer (requireCurrentTimeAxis, twice), so a current axis is
		// what a provider always sees in production. Leaving it at the zero
		// value instead makes every provider bail out in
		// checkCurrentTimeOnly, which would make pruning look like a 100%
		// saving for the wrong reason.
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     subjects,
		Requirements: chaos3783Requirements(kinds),
	}
	started := time.Now()
	bundle, err := registry.ReadFacts(ctx, principal, request)
	elapsed := time.Since(started)
	if err != nil {
		return chaos3783Measurement{elapsed: elapsed, failed: true, failure: err.Error()}
	}

	measurement := chaos3783Measurement{
		elapsed: elapsed, facts: len(bundle.Facts),
		bundleBytes: chaos3783BundleBytes(bundle.Facts), factsDigest: chaos3783FactsDigest(bundle.Facts),
		coverage: make(map[contextfabric.FactKind]chaos3783Observation, len(bundle.Coverage.Sources)),
	}
	for _, source := range bundle.Coverage.Sources {
		kind := contextfabric.FactKind(strings.TrimPrefix(source.Source, "canonical_fact:"))
		measurement.coverage[kind] = chaos3783Observation{state: source.State, reason: source.Reason}
		switch source.State {
		case contextfabric.SourcePruned:
			measurement.prunedSources++
			measurement.prunedKinds = append(measurement.prunedKinds, kind)
		case contextfabric.SourceUnconfigured:
			// Never registered, so it was not a round-trip either way.
		default:
			measurement.providersQueried++
			measurement.subjectBindings += chaos3783SupportedSubjectCount(registered[kind], subjects)
		}
	}
	return measurement
}

// chaos3783NaiveCost counts -- never executes -- what a planner-free
// implementation would have spent: one provider round-trip per registered
// requirement, with every investigation subject bound to each.
//
// It deliberately builds no bundle. The planner-free fan-out cannot be run
// through the production path (buildFactQuery refuses an unsupported subject
// kind), and re-implementing it here is exactly the mistake that produced
// three rounds of divergence findings. A count has no state stamping, no
// caps, and no serialization, so there is nothing for it to diverge on.
func chaos3783NaiveCost(testCase chaos3783Case, registered map[contextfabric.FactKind]contextfabric.FactCapability) (roundTrips, bindings int) {
	for _, kind := range testCase.requirements {
		if _, ok := registered[kind]; !ok {
			continue
		}
		roundTrips++
		bindings += len(testCase.subjects)
	}
	return roundTrips, bindings
}

// chaos3783SupportedSubjectCount reports how many of this investigation's
// subjects the capability was actually asked about, which is what the planner
// narrows.
func chaos3783SupportedSubjectCount(capability contextfabric.FactCapability, subjects []contextfabric.SubjectRef) int {
	count := 0
	for _, subject := range subjects {
		for _, supported := range capability.SupportedSubjectKinds {
			if supported == subject.Kind {
				count++
				break
			}
		}
	}
	return count
}

// chaos3783BundleBytes is the human-readable size of what reaches the model.
// It is reported, never asserted on -- see chaos3783FactsDigest.
func chaos3783BundleBytes(facts []contextfabric.CanonicalFact) int {
	return len(chaos3783CanonicalEncoding(facts))
}

// chaos3783FactsDigest is what the equality assertion actually compares
// (codex round-2 R2-2). Byte LENGTH is a reporting number, not an identity:
// any two bundles of equal size compare equal under it, so a
// canonicalization regression that reordered or swapped equal-length values
// would slip straight through -- including in the permutation test meant to
// catch exactly that. The digest is over the canonical encoding, so it
// changes if any byte changes.
func chaos3783FactsDigest(facts []contextfabric.CanonicalFact) string {
	sum := sha256.Sum256(chaos3783CanonicalEncoding(facts))
	return hex.EncodeToString(sum[:])
}

// chaos3783CanonicalEncoding is the single serialization both the size and
// the digest are taken from, so the number reported and the value asserted
// on can never describe different bytes.
func chaos3783CanonicalEncoding(facts []contextfabric.CanonicalFact) []byte {
	encoded, err := json.Marshal(chaos3783Canonical(facts))
	if err != nil {
		return nil
	}
	return encoded
}

// chaos3783Canonical returns a stably-ordered copy. Both compared bundles now
// come from the same registry and are already sorted by it, so this is
// belt-and-braces rather than the load-bearing fix it was when a
// hand-assembled baseline was in play -- but it costs nothing and keeps the
// digest meaningful if either side's ordering ever changes.
func chaos3783Canonical(facts []contextfabric.CanonicalFact) []contextfabric.CanonicalFact {
	// The key travels WITH its fact. Computing keys into a position-indexed
	// side table and sorting the facts separately would leave the comparator
	// reading whichever key now sits at that index, not the one belonging to
	// the fact being compared.
	type keyed struct {
		key  string
		fact contextfabric.CanonicalFact
	}
	pairs := make([]keyed, 0, len(facts))
	for _, fact := range facts {
		encoded, err := json.Marshal(fact)
		if err != nil {
			encoded = nil
		}
		pairs = append(pairs, keyed{
			key: string(fact.Kind) + "\x00" + string(fact.Subject.Kind) + "\x00" +
				fact.Subject.CanonicalID + "\x00" + fact.Source + "\x00" + string(encoded),
			fact: fact,
		})
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
	ordered := make([]contextfabric.CanonicalFact, 0, len(pairs))
	for _, pair := range pairs {
		ordered = append(ordered, pair.fact)
	}
	return ordered
}

func chaos3783Requirements(kinds []contextfabric.FactKind) []contextfabric.FactRequirement {
	requirements := make([]contextfabric.FactRequirement, 0, len(kinds))
	for _, kind := range kinds {
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind})
	}
	return requirements
}

func chaos3783Without(kinds, remove []contextfabric.FactKind) []contextfabric.FactKind {
	excluded := make(map[contextfabric.FactKind]struct{}, len(remove))
	for _, kind := range remove {
		excluded[kind] = struct{}{}
	}
	kept := make([]contextfabric.FactKind, 0, len(kinds))
	for _, kind := range kinds {
		if _, skip := excluded[kind]; skip {
			continue
		}
		kept = append(kept, kind)
	}
	return kept
}

// chaos3783Subjects discovers real subject IDs from the live database rather
// than hard-coding them, so the harness keeps working as the dev corpus is
// rebuilt. It is deliberately a plain SELECT of identifiers only.
//
// Codex round-4 R4-1: it returns an error instead of logging and carrying
// on. A measurement layer that degrades quietly is worse than one that
// breaks, because the caller SKIPS a case with no subjects -- so a broken
// query used to shrink the discovered subject set, shrink the measured
// savings, and still report success. Failing toward "fine" is the specific
// trap a benchmark must not have. rows.Err() is checked for the same reason:
// a mid-iteration failure truncates the set silently.
func chaos3783Subjects(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID, table, column string, kind contextfabric.SubjectKind, prefix string, limit int) ([]contextfabric.SubjectRef, error) {
	// table/column/limit are internal Go literals from the call sites just
	// above, never caller- or request-supplied, so inlining them mirrors
	// this package's existing convention for such constants (see
	// shared.go's withRowLimit). org_id is the one value that varies and it
	// goes through a binding.
	statement := fmt.Sprintf(
		"SELECT DISTINCT %s FROM %s WHERE org_id = {org_id:String} AND %s != '' ORDER BY %s LIMIT %d",
		column, table, column, column, limit,
	)
	rows, err := client.Query(ctx, statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
	if err != nil {
		return nil, fmt.Errorf("discover %s subjects: %w", kind, err)
	}
	defer rows.Close()
	subjects := make([]contextfabric.SubjectRef, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s subject: %w", kind, err)
		}
		subjects = append(subjects, contextfabric.SubjectRef{Kind: kind, CanonicalID: prefix + id, Label: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s subjects: %w", kind, err)
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].CanonicalID < subjects[j].CanonicalID })
	return subjects, nil
}

func chaos3783Concat(groups ...[]contextfabric.SubjectRef) []contextfabric.SubjectRef {
	combined := make([]contextfabric.SubjectRef, 0)
	for _, group := range groups {
		combined = append(combined, group...)
	}
	return combined
}

func chaos3783FailureNote(measurement chaos3783Measurement) string {
	if !measurement.failed {
		return ""
	}
	return " FAILED: " + measurement.failure
}

func chaos3783Percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// TestCHAOS3783FactsDigestIsOrderInsensitiveAndContentSensitive guards the
// shared serializer both compared bundles go through. It needs no database,
// so it runs in ordinary CI rather than only under the opt-in measurement.
func TestCHAOS3783FactsDigestIsOrderInsensitiveAndContentSensitive(t *testing.T) {
	t.Parallel()

	facts := []contextfabric.CanonicalFact{
		{
			Kind: contextfabric.FactMetrics, Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:b", Label: "b"},
			Fields: map[string]contextfabric.FactValue{"commits": contextfabric.IntegerFactValue(2)},
			Source: "devhealthfacts.metrics", SourceVersion: "v1", SourceState: contextfabric.SourceAvailable,
		},
		{
			Kind: contextfabric.FactHealth, Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:a", Label: "a"},
			Fields: map[string]contextfabric.FactValue{"severity": contextfabric.StringFactValue("high")},
			Source: "devhealthfacts.health", SourceVersion: "v1", SourceState: contextfabric.SourceAvailable,
		},
		{
			// Same kind, subject, and source as the first -- only the value
			// differs, which is the case a coarser sort key would leave
			// order-dependent.
			Kind: contextfabric.FactMetrics, Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:b", Label: "b"},
			Fields: map[string]contextfabric.FactValue{"commits": contextfabric.IntegerFactValue(9)},
			Source: "devhealthfacts.metrics", SourceVersion: "v1", SourceState: contextfabric.SourceAvailable,
		},
	}

	want := chaos3783FactsDigest(facts)
	for _, permutation := range [][]int{{2, 1, 0}, {1, 2, 0}, {0, 2, 1}, {2, 0, 1}} {
		shuffled := make([]contextfabric.CanonicalFact, 0, len(facts))
		for _, index := range permutation {
			shuffled = append(shuffled, facts[index])
		}
		if got := chaos3783FactsDigest(shuffled); got != want {
			t.Fatalf("bundle digest = %s for permutation %v, want %s -- the comparison must not depend on accumulation order", got, permutation, want)
		}
	}

	// The digest must also be SENSITIVE: a bundle differing only in a value
	// of the SAME LENGTH has to produce a different digest, which is exactly
	// what a byte-count comparison could not see.
	altered := append([]contextfabric.CanonicalFact(nil), facts...)
	altered[0].Fields = map[string]contextfabric.FactValue{"commits": contextfabric.IntegerFactValue(3)}
	if chaos3783FactsDigest(altered) == want {
		t.Fatal("digest ignored an equal-length value change -- it must compare content, not size")
	}
	if chaos3783BundleBytes(altered) != chaos3783BundleBytes(facts) {
		t.Fatal("test setup no longer exercises the equal-length case the digest exists to catch")
	}
}

// chaos3783FakeClient is a minimal ClickHouseQueryClient for exercising
// chaos3783Subjects' failure paths without a database.
type chaos3783FakeClient struct {
	queryErr error
	rows     *chaos3783FakeRows
}

func (c *chaos3783FakeClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return c.rows, nil
}

type chaos3783FakeRows struct {
	values   []string
	index    int
	scanErr  error
	iterErr  error
	closeErr error
}

func (r *chaos3783FakeRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *chaos3783FakeRows) Scan(targets ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if len(targets) != 1 {
		return fmt.Errorf("unexpected scan arity %d", len(targets))
	}
	target, ok := targets[0].(*string)
	if !ok {
		return fmt.Errorf("unexpected scan target %T", targets[0])
	}
	*target = r.values[r.index-1]
	return nil
}

func (r *chaos3783FakeRows) Err() error   { return r.iterErr }
func (r *chaos3783FakeRows) Close() error { return r.closeErr }

// TestCHAOS3783SubjectDiscoveryFailsLoudly is the codex round-4 R4-1
// regression. Subject discovery used to log and return whatever it had, and
// the caller SKIPS a case with no subjects -- so a broken query quietly
// shrank the discovered subject set, shrank the measured savings, and still
// reported success. A measurement layer that degrades toward "fine" is worse
// than one that breaks loudly, because nobody re-reads a green benchmark.
//
// The mid-iteration case is the subtle one: rows.Err() going unchecked
// truncates the set with no error anywhere, which looks exactly like an
// organization that genuinely has fewer subjects.
func TestCHAOS3783SubjectDiscoveryFailsLoudly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		client *chaos3783FakeClient
	}{
		{
			name:   "query error",
			client: &chaos3783FakeClient{queryErr: fmt.Errorf("clickhouse refused the connection")},
		},
		{
			name:   "scan error",
			client: &chaos3783FakeClient{rows: &chaos3783FakeRows{values: []string{"a"}, scanErr: fmt.Errorf("type mismatch")}},
		},
		{
			name:   "mid-iteration failure surfaced only by rows.Err()",
			client: &chaos3783FakeClient{rows: &chaos3783FakeRows{values: []string{"a", "b"}, iterErr: fmt.Errorf("connection reset mid-stream")}},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			subjects, err := chaos3783Subjects(context.Background(), testCase.client, "org_1", "repos", "toString(id)", contextfabric.SubjectRepository, "repository:", 3)
			if err == nil {
				t.Fatalf("chaos3783Subjects() error = nil (returned %d subjects), want a failure -- a silent partial result understates the measurement", len(subjects))
			}
		})
	}
}

// TestCHAOS3783SubjectDiscoverySucceedsOnCleanRows is the companion guard:
// failing loudly must not mean failing spuriously.
func TestCHAOS3783SubjectDiscoverySucceedsOnCleanRows(t *testing.T) {
	t.Parallel()

	client := &chaos3783FakeClient{rows: &chaos3783FakeRows{values: []string{"b", "a"}}}
	subjects, err := chaos3783Subjects(context.Background(), client, "org_1", "repos", "toString(id)", contextfabric.SubjectRepository, "repository:", 3)
	if err != nil {
		t.Fatalf("chaos3783Subjects() error = %v, want success", err)
	}
	if len(subjects) != 2 {
		t.Fatalf("subjects = %d, want 2", len(subjects))
	}
	// Sorted and prefixed, as the fact providers expect.
	if subjects[0].CanonicalID != "repository:a" || subjects[1].CanonicalID != "repository:b" {
		t.Fatalf("subjects = %+v, want sorted, prefixed canonical IDs", subjects)
	}
}
