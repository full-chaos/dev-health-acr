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
// What it compares. The "before" is NOT current production behavior --
// before this change a mismatched requirement union failed the whole
// investigation rather than running slowly, so there is no slow baseline to
// measure. The baseline here is the COUNTERFACTUAL naive fix: run every
// requested capability against every resolved subject and let providers come
// back empty. That is the design coverage-driven pruning argues against, and
// it is the only honest apples-to-apples comparison. Results are labelled
// accordingly and must not be quoted as "production before".
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
	facts            int
	bundleBytes      int
	bundleDigest     string
	elapsed          time.Duration
	failed           bool
	failure          string
	prunedSources    int
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
	providers := devhealthfacts.NewProviders(client)
	registry, err := contextfabric.NewFactCapabilityRegistry(providers, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
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
		name              string
		baseline, planned chaos3783Measurement
	}
	rows := make([]row, 0, len(cases))
	baselineFailures, plannedFailures := 0, 0

	for _, testCase := range cases {
		if len(testCase.subjects) == 0 {
			t.Logf("skipping %q: no subjects of that kind in this organization", testCase.name)
			continue
		}
		baseline := chaos3783MeasureNaiveFanout(ctx, principal, providers, testCase)
		planned := chaos3783MeasurePlanned(ctx, principal, registry, testCase)
		if baseline.failed {
			baselineFailures++
		}
		if planned.failed {
			plannedFailures++
		}
		rows = append(rows, row{name: testCase.name, baseline: baseline, planned: planned})
	}

	t.Log("")
	t.Log("CHAOS-3783 pruning measurement -- baseline is the COUNTERFACTUAL naive fan-out, not production before")
	t.Log("")
	for _, r := range rows {
		t.Logf("case: %s", r.name)
		t.Logf("  naive fan-out : providers=%2d subject-bindings=%3d facts=%4d bundle-bytes=%7d elapsed=%v%s",
			r.baseline.providersQueried, r.baseline.subjectBindings, r.baseline.facts, r.baseline.bundleBytes, r.baseline.elapsed.Round(time.Millisecond), chaos3783FailureNote(r.baseline))
		t.Logf("  planner       : providers=%2d subject-bindings=%3d facts=%4d bundle-bytes=%7d elapsed=%v pruned-sources=%d%s",
			r.planned.providersQueried, r.planned.subjectBindings, r.planned.facts, r.planned.bundleBytes, r.planned.elapsed.Round(time.Millisecond), r.planned.prunedSources, chaos3783FailureNote(r.planned))
		t.Logf("  saved         : %d provider round-trips (%.0f%%), %d subject-bindings, %d bundle bytes",
			r.baseline.providersQueried-r.planned.providersQueried,
			chaos3783Percent(r.baseline.providersQueried-r.planned.providersQueried, r.baseline.providersQueried),
			r.baseline.subjectBindings-r.planned.subjectBindings,
			r.baseline.bundleBytes-r.planned.bundleBytes)
	}
	t.Logf("planned-path failures: %d of %d cases", plannedFailures, len(rows))

	// The acceptance assertions. Everything above is reported; these are the
	// properties that must hold.
	if plannedFailures != 0 {
		t.Fatalf("%d planned investigation(s) failed, want 0 -- pruning exists so a wide union stays answerable", plannedFailures)
	}
	for _, r := range rows {
		if r.planned.providersQueried > r.baseline.providersQueried {
			t.Fatalf("case %q queried MORE providers with the planner (%d > %d)", r.name, r.planned.providersQueried, r.baseline.providersQueried)
		}
		if r.planned.subjectBindings > r.baseline.subjectBindings {
			t.Fatalf("case %q bound MORE subjects with the planner (%d > %d)", r.name, r.planned.subjectBindings, r.baseline.subjectBindings)
		}
		// The strongest guarantee this harness can give, and the reason the
		// pruning rule was built as a proof rather than a heuristic: pruning
		// removes WORK, never ANSWER. A pruned capability could not have
		// produced a fact -- it filters on its own ID column, which no
		// subject of an unsupported kind matches -- so the fact bundle must
		// come out byte-identical. A difference here means the planner
		// dropped something real and the rule is wrong.
		if r.planned.facts != r.baseline.facts || r.planned.bundleDigest != r.baseline.bundleDigest {
			t.Fatalf("case %q: pruning changed the fact bundle (facts %d vs %d, digest %s vs %s) -- it must only skip work that could not have produced facts",
				r.name, r.planned.facts, r.baseline.facts, r.planned.bundleDigest, r.baseline.bundleDigest)
		}
	}
}

// chaos3783MeasurePlanned runs the real registry path -- planner included --
// and reads the saving straight out of the coverage the caller would receive.
func chaos3783MeasurePlanned(ctx context.Context, principal storage.Principal, registry *contextfabric.FactCapabilityRegistry, testCase chaos3783Case) chaos3783Measurement {
	request := contextfabric.CanonicalFactRequest{
		// Engine refuses any axis but current before it ever reaches the
		// fact layer (requireCurrentTimeAxis, twice), so a current axis is
		// what a provider always sees in production. Leaving it at the zero
		// value instead makes every provider bail out in
		// checkCurrentTimeOnly, which would make pruning look like a 100%
		// saving for the wrong reason.
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     testCase.subjects,
		Requirements: chaos3783Requirements(testCase.requirements),
	}
	started := time.Now()
	bundle, err := registry.ReadFacts(ctx, principal, request)
	elapsed := time.Since(started)
	if err != nil {
		return chaos3783Measurement{elapsed: elapsed, failed: true, failure: err.Error()}
	}

	measurement := chaos3783Measurement{elapsed: elapsed, facts: len(bundle.Facts)}
	for _, source := range bundle.Coverage.Sources {
		switch source.State {
		case contextfabric.SourcePruned:
			measurement.prunedSources++
		case contextfabric.SourceUnconfigured:
			// Never registered, so it was not a round-trip either way.
		default:
			measurement.providersQueried++
			measurement.subjectBindings += chaos3783SupportedSubjectCount(registry, source.Source, testCase.subjects)
		}
	}
	measurement.bundleBytes = chaos3783BundleBytes(bundle.Facts)
	measurement.bundleDigest = chaos3783BundleDigest(bundle.Facts)
	return measurement
}

// chaos3783MeasureNaiveFanout is the counterfactual: call every requested
// capability directly, with every resolved subject, exactly as a planner-free
// implementation would. It goes to the providers rather than the registry
// precisely so it cannot pick up the planner -- there is no production knob
// that turns pruning off, and adding one only to measure it would be a seam
// with no other reason to exist.
func chaos3783MeasureNaiveFanout(ctx context.Context, principal storage.Principal, providers []contextfabric.FactProvider, testCase chaos3783Case) chaos3783Measurement {
	byKind := make(map[contextfabric.FactKind]contextfabric.FactProvider, len(providers))
	for _, provider := range providers {
		byKind[provider.Capability().Kind] = provider
	}

	measurement := chaos3783Measurement{}
	facts := make([]contextfabric.CanonicalFact, 0)
	started := time.Now()
	for _, kind := range testCase.requirements {
		provider, registered := byKind[kind]
		if !registered {
			continue
		}
		measurement.providersQueried++
		measurement.subjectBindings += len(testCase.subjects)
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Kind:     kind,
			Subjects: testCase.subjects,
			Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		})
		if err != nil {
			// A naive fan-out swallows a provider error the same way the
			// registry does -- it is coverage, not a failed investigation.
			continue
		}
		// mergeFactProviderResult stamps Source/SourceVersion/SourceState
		// onto every fact the registry merges. Comparing raw provider facts
		// against stamped ones would report a byte difference that has
		// nothing to do with pruning, so stamp the baseline identically.
		capability := provider.Capability()
		for _, fact := range result.Facts {
			if fact.Source == "" {
				fact.Source = capability.Name
			}
			if fact.SourceVersion == "" {
				fact.SourceVersion = capability.Version
			}
			if fact.SourceState == "" {
				fact.SourceState = result.State
			}
			facts = append(facts, fact)
		}
	}
	measurement.elapsed = time.Since(started)
	measurement.facts = len(facts)
	measurement.bundleBytes = chaos3783BundleBytes(facts)
	measurement.bundleDigest = chaos3783BundleDigest(facts)
	return measurement
}

// chaos3783SupportedSubjectCount reports how many of this investigation's
// subjects the named capability was actually asked about, which is what the
// planner narrows. The capability is found by the coverage source name the
// registry emits ("canonical_fact:<kind>").
func chaos3783SupportedSubjectCount(registry *contextfabric.FactCapabilityRegistry, coverageSource string, subjects []contextfabric.SubjectRef) int {
	kind := contextfabric.FactKind(strings.TrimPrefix(coverageSource, "canonical_fact:"))
	for _, capability := range registry.Capabilities() {
		if capability.Kind != kind {
			continue
		}
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
	return 0
}

// chaos3783BundleBytes measures what actually reaches the model: the
// serialized facts. Fact COUNT alone understates the difference, because
// families differ by an order of magnitude in how wide each fact is.
//
// Codex round-1 F4: it canonicalizes ORDER first. The registry sorts its
// bundle (sortCanonicalFacts) while the naive baseline accumulates in
// requirement order, and no provider SELECT carries an outer ORDER BY, so
// two runs holding the identical SET of facts can serialize differently for
// reasons that have nothing to do with pruning. Comparing those bytes
// directly would make the byte-identity assertion flaky, and a flaky
// assertion about correctness is worse than no assertion. Both sides go
// through this one function, so both are ordered the same way.
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

// chaos3783Canonical returns a stably-ordered copy. The sort key ends with
// the fact's own serialization so that two facts identical in kind, subject,
// and source -- which a provider may legitimately return, e.g. one row per
// day -- still order deterministically instead of relying on the order the
// database happened to return them in.
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

// TestCHAOS3783BundleBytesIsOrderInsensitive proves the F4 fix without
// needing a database, so it runs in ordinary CI rather than only under the
// opt-in measurement. The byte-identity assertion is only meaningful if two
// identical fact SETS measure identically regardless of the order they were
// accumulated in -- the registry sorts its bundle, the naive baseline does
// not, and no provider SELECT has an outer ORDER BY.
func TestCHAOS3783BundleBytesIsOrderInsensitive(t *testing.T) {
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
