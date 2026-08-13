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
// # Why there is no second bundle-building path here
//
// An earlier version of this harness assembled a "naive fan-out" bundle
// itself, by calling providers directly and stamping the fields the registry
// stamps. That was a parallel re-implementation of the registry pipeline, and
// every semantic the registry applies -- per-fact state rewrites, the
// aggregate fact cap, ordering, serialization -- was a divergence waiting to
// be found one review round at a time. Three rounds found three. A
// re-implemented baseline measures its own divergence from the real code, not
// pruning.
//
// Every bundle compared below now comes from the REAL
// FactCapabilityRegistry.ReadFacts, so truncation, caps, state stamping, and
// serialization are shared by construction and cannot diverge.
//
// # What the counterfactual is, and why it is counted rather than run
//
// The naive fan-out cannot be executed through the production path at all,
// and that is not an oversight: buildFactQuery refuses to ask a provider
// about a subject kind its capability does not support, and refuses an empty
// subject list. Forcing a plan-everything mode through ReadFacts would
// therefore not produce a naive baseline -- it would reproduce the
// pre-CHAOS-3783 whole-bundle failure this ticket exists to fix.
//
// So the naive numbers are COUNTED, never executed: a planner-free
// implementation issues one round-trip per registered requirement and binds
// every investigation subject to each. That is a count of work not done --
// no bundle, no stamping, no serialization -- so it carries none of the
// divergence risk the deleted baseline did.
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
	bundleDigest     string
	elapsed          time.Duration
	failed           bool
	failure          string
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

	repositories := chaos3783Subjects(t, ctx, client, orgID, "repos", "toString(id)", contextfabric.SubjectRepository, "repository:", 3)
	workItems := chaos3783Subjects(t, ctx, client, orgID, "work_items", "work_item_id", contextfabric.SubjectWorkItem, "work_item:", 3)
	teams := chaos3783Subjects(t, ctx, client, orgID, "capacity_forecasts", "team_id", contextfabric.SubjectTeam, "team:", 3)
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
			if remaining := chaos3783Without(testCase.requirements, planned.prunedKinds); len(remaining) > 0 {
				current.reduced = chaos3783Run(ctx, principal, registry, registered, testCase.subjects, remaining)
				current.identityIsTested = true
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
		if r.identityIsTested {
			t.Logf("  identity       : facts=%d digest-match=%v", r.reduced.facts, r.reduced.bundleDigest == r.planned.bundleDigest)
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
		if r.planned.facts != r.reduced.facts || r.planned.bundleDigest != r.reduced.bundleDigest {
			t.Fatalf("case %q: pruning changed the fact bundle (facts %d vs %d, digest %s vs %s) -- it must only skip work that could not have produced facts",
				r.name, r.planned.facts, r.reduced.facts, r.planned.bundleDigest, r.reduced.bundleDigest)
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
		bundleBytes: chaos3783BundleBytes(bundle.Facts), bundleDigest: chaos3783BundleDigest(bundle.Facts),
	}
	for _, source := range bundle.Coverage.Sources {
		kind := contextfabric.FactKind(strings.TrimPrefix(source.Source, "canonical_fact:"))
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
// It deliberately builds no bundle. The naive fan-out cannot be run through
// the production path (buildFactQuery refuses an unsupported subject kind),
// and re-implementing it here is exactly the mistake that produced three
// rounds of divergence findings. A count has no state stamping, no caps, and
// no serialization, so there is nothing for it to diverge on.
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
// It is reported, never asserted on -- see chaos3783BundleDigest.
func chaos3783BundleBytes(facts []contextfabric.CanonicalFact) int {
	return len(chaos3783CanonicalEncoding(facts))
}

// chaos3783BundleDigest is what the equality assertion actually compares
// (codex round-2 R2-2). Byte LENGTH is a reporting number, not an identity:
// any two bundles of equal size compare equal under it, so a
// canonicalization regression that reordered or swapped equal-length values
// would slip straight through -- including in the permutation test meant to
// catch exactly that. The digest is over the canonical encoding, so it
// changes if any byte changes.
func chaos3783BundleDigest(facts []contextfabric.CanonicalFact) string {
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
func chaos3783Subjects(t *testing.T, ctx context.Context, client *runtimeclickhouse.Client, orgID, table, column string, kind contextfabric.SubjectKind, prefix string, limit int) []contextfabric.SubjectRef {
	t.Helper()
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
		t.Logf("discover %s subjects: %v (continuing without them)", kind, err)
		return nil
	}
	defer rows.Close()
	subjects := make([]contextfabric.SubjectRef, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Logf("scan %s subject: %v", kind, err)
			return subjects
		}
		subjects = append(subjects, contextfabric.SubjectRef{Kind: kind, CanonicalID: prefix + id, Label: id})
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].CanonicalID < subjects[j].CanonicalID })
	return subjects
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

// TestCHAOS3783BundleDigestIsOrderInsensitiveAndContentSensitive guards the
// shared serializer both compared bundles go through. It needs no database,
// so it runs in ordinary CI rather than only under the opt-in measurement.
func TestCHAOS3783BundleDigestIsOrderInsensitiveAndContentSensitive(t *testing.T) {
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

	want := chaos3783BundleDigest(facts)
	for _, permutation := range [][]int{{2, 1, 0}, {1, 2, 0}, {0, 2, 1}, {2, 0, 1}} {
		shuffled := make([]contextfabric.CanonicalFact, 0, len(facts))
		for _, index := range permutation {
			shuffled = append(shuffled, facts[index])
		}
		if got := chaos3783BundleDigest(shuffled); got != want {
			t.Fatalf("bundle digest = %s for permutation %v, want %s -- the comparison must not depend on accumulation order", got, permutation, want)
		}
	}

	// The digest must also be SENSITIVE: a bundle differing only in a value
	// of the SAME LENGTH has to produce a different digest, which is exactly
	// what a byte-count comparison could not see.
	altered := append([]contextfabric.CanonicalFact(nil), facts...)
	altered[0].Fields = map[string]contextfabric.FactValue{"commits": contextfabric.IntegerFactValue(3)}
	if chaos3783BundleDigest(altered) == want {
		t.Fatal("digest ignored an equal-length value change -- it must compare content, not size")
	}
	if chaos3783BundleBytes(altered) != chaos3783BundleBytes(facts) {
		t.Fatal("test setup no longer exercises the equal-length case the digest exists to catch")
	}
}
