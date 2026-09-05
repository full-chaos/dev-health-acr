package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// captureCohortKindBasisLine emits one cohort-kind-basis line through the
// PRODUCTION sink and returns it decoded.
//
// It goes through SlogTelemetry rather than a fake, because the property
// under test is what the production line actually carries -- a fake would let
// the sink and the assertion drift apart, and this file exists to stop that.
func captureCohortKindBasisLine(t *testing.T, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool) map[string]any {
	t.Helper()
	return captureCohortKindBasisLineWithPoolTruncation(t, declaredKind, basis, discovered, CohortPoolTruncationNone)
}

// productionSinkLevel is the level a DEPLOYED handler is configured at, and it
// is what every content test below captures through.
//
// This is not a detail. These tests used to capture at Debug, which admits
// everything, so the line's own level was unobservable: demote it to Debug in
// production and every assertion here still passed while the whole diagnostic
// vanished from a real deployment. Capturing at the production default makes a
// demoted line disappear HERE exactly as it would THERE -- so the level is
// pinned by every test in this file, not only by the one that names it.
const productionSinkLevel = slog.LevelInfo

func captureCohortKindBasisLineWithPoolTruncation(t *testing.T, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool, poolTruncation CohortPoolTruncationBasis) map[string]any {
	t.Helper()
	return captureCohortKindBasisLineFull(t, context.Background(), productionSinkLevel, declaredKind, basis, discovered, poolTruncation, nil)
}

// captureCohortKindBasisLineFull is the one place the production sink is
// driven. Every parameter exists because a test below varies it: the CONTEXT
// (so request-id propagation is observable), the handler LEVEL (so the line's
// own level is observable rather than assumed), and the arms.
func captureCohortKindBasisLineFull(t *testing.T, ctx context.Context, level slog.Level, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool, poolTruncation CohortPoolTruncationBasis, arms []CohortPoolTruncationArm) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: level}))
	SlogTelemetry{Logger: logger}.RecordCohortKindBasis(
		ctx, "org_sink_test", declaredKind, basis, discovered, poolTruncation, arms)

	lines := 0
	record := map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("telemetry emitted a line that is not valid JSON: %v (%q)", err, line)
		}
	}
	if lines != 1 {
		t.Fatalf("got %d lines, want exactly 1", lines)
	}
	return record
}

// captureCohortKindBasisLineCount emits one line at the given handler level
// and returns HOW MANY lines the handler admitted, so a test can observe the
// line being SUPPRESSED rather than only its content when it is not.
func captureCohortKindBasisLineCount(t *testing.T, level slog.Level) int {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: level}))
	SlogTelemetry{Logger: logger}.RecordCohortKindBasis(
		context.Background(), "org_sink_test", contextfabric.SubjectTeam,
		graphrank.CohortKindFromFrameMemberKind, true, CohortPoolTruncationNone, nil)
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// TestCohortKindBasisLineNamesTheRefusedKind is the REQUIRED half, and it is
// the whole reason this line changed.
//
// The line used to carry a basis and no kind, so a refusal could say THAT a
// member kind was unservable and never WHICH. The kind then got read off the
// question text -- "open incidents per repository" was recorded as declaring
// member kind `repository` in three documents, when invariant I6 makes that
// frame impossible and the real declared member kind is `incident`.
func TestCohortKindBasisLineNamesTheRefusedKind(t *testing.T) {
	record := captureCohortKindBasisLine(t,
		contextfabric.SubjectIncident, graphrank.CohortKindMemberKindUnservable, false)

	if got := record["member_kind"]; got != string(contextfabric.SubjectIncident) {
		t.Errorf("member_kind = %v, want %q -- a refusal that cannot name the kind it refused is a refusal someone will attribute by guessing", got, contextfabric.SubjectIncident)
	}
	if got := record["basis"]; got != string(graphrank.CohortKindMemberKindUnservable) {
		t.Errorf("basis = %v, want %q", got, graphrank.CohortKindMemberKindUnservable)
	}
	if got := record["discovered"]; got != false {
		t.Errorf("discovered = %v, want false", got)
	}
}

// TestCohortKindBasisLineNamesTheServedKind is the served direction of the
// same key: on a discovery the line names the kind that WAS served.
func TestCohortKindBasisLineNamesTheServedKind(t *testing.T) {
	record := captureCohortKindBasisLine(t,
		contextfabric.SubjectRepository, graphrank.CohortKindFromFrameMemberKind, true)

	if got := record["member_kind"]; got != string(contextfabric.SubjectRepository) {
		t.Errorf("member_kind = %v, want %q", got, contextfabric.SubjectRepository)
	}
	if got := record["discovered"]; got != true {
		t.Errorf("discovered = %v, want true", got)
	}
}

// TestCohortKindBasisLineIsEmptyKindWhereNoKindWasDeclared is the MIRROR on
// the key's value.
//
// Three of the five bases -- frame_absent, not_a_cohort_variant,
// no_member_kind -- describe turns where the frame declared no member kind at
// all. The honest value there is the empty string, and asserting only the
// populated direction would leave a version that stamped some default kind
// onto those turns unpinned.
func TestCohortKindBasisLineIsEmptyKindWhereNoKindWasDeclared(t *testing.T) {
	for _, basis := range []graphrank.CohortKindBasis{
		graphrank.CohortKindFrameAbsent,
		graphrank.CohortKindNotACohortVariant,
		graphrank.CohortKindNoMemberKind,
	} {
		t.Run(string(basis), func(t *testing.T) {
			record := captureCohortKindBasisLine(t, "", basis, false)
			if got := record["member_kind"]; got != "" {
				t.Errorf("member_kind = %v for basis %q, want the empty kind -- no member kind was declared on this turn", got, basis)
			}
		})
	}
}

// TestCohortKindBasisLineCarriesNoKeyOutsideItsAllowList is the ALLOW-LIST
// half.
//
// The member kind is admitted here for the same reason the basis is: it is a
// member of the published, closed subject-kind vocabulary, never free text. A
// subject LABEL on this line would be content, and the allow-list is what
// makes adding one a test failure rather than a review question.
func TestCohortKindBasisLineCarriesNoKeyOutsideItsAllowList(t *testing.T) {
	record := captureCohortKindBasisLine(t,
		contextfabric.SubjectRepository, graphrank.CohortKindFromFrameMemberKind, true)

	// pool_truncation is admitted on the SAME standard as basis and
	// member_kind: it is a member of a published, closed vocabulary
	// (CohortPoolTruncationBasisVocabulary), never free text and never
	// corpus content. TestCohortKindBasisLineReportsOnlyPublishedPoolTruncations
	// below is what holds it to that standard rather than this comment.
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "request_id": true,
		"org_id": true, "member_kind": true, "basis": true, "discovered": true,
		"pool_truncation": true, "pool_truncation_arms": true,
	}
	for key := range record {
		if !allowed[key] {
			t.Errorf("unexpected key %q on the cohort kind basis line -- every field must be an explicitly allowed closed-vocabulary value", key)
		}
	}
}

// TestCohortKindBasisLineReportsOnlyPublishedKinds is the vocabulary half: a
// value on this key is always a member of the published subject-kind
// vocabulary or the empty string, never anything else.
//
// It quantifies over the whole basis vocabulary rather than a hand-picked
// pair, so a basis added later without a decision about its kind fails here.
func TestCohortKindBasisLineReportsOnlyPublishedKinds(t *testing.T) {
	published := map[string]bool{"": true}
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		published[string(kind)] = true
	}
	for _, basis := range graphrank.CohortKindBasisVocabulary() {
		kind := contextfabric.SubjectKind("")
		if basis == graphrank.CohortKindFromFrameMemberKind || basis == graphrank.CohortKindMemberKindUnservable {
			kind = contextfabric.SubjectRepository
		}
		record := captureCohortKindBasisLine(t, kind, basis, false)
		value, _ := record["member_kind"].(string)
		if !published[value] {
			t.Errorf("basis %q emitted member_kind %q, which is not a published subject kind", basis, value)
		}
	}
}

// TestCohortKindBasisLineReportsOnlyPublishedPoolTruncations is the
// vocabulary half of the key added beside basis and member_kind.
//
// It quantifies over the DECLARED vocabulary rather than over a hand-typed
// pair, so a member added later without a decision about how it is emitted
// fails here. Admitting a key to the allow-list without this is how a closed
// vocabulary becomes free text one commit at a time.
func TestCohortKindBasisLineReportsOnlyPublishedPoolTruncations(t *testing.T) {
	vocabulary := CohortPoolTruncationBasisVocabulary()
	if len(vocabulary) == 0 {
		t.Fatal("CohortPoolTruncationBasisVocabulary() is empty -- the loop below cannot fail, so this test would pass over anything")
	}
	published := make(map[string]bool, len(vocabulary))
	for _, basis := range vocabulary {
		published[string(basis)] = true
	}
	for _, basis := range vocabulary {
		record := captureCohortKindBasisLineFull(t, context.Background(), productionSinkLevel,
			contextfabric.SubjectRepository, graphrank.CohortKindFromFrameMemberKind, true, basis, nil)
		got, ok := record["pool_truncation"].(string)
		if !ok {
			t.Fatalf("pool_truncation = %v (%T), want a string", record["pool_truncation"], record["pool_truncation"])
		}
		if !published[got] {
			t.Errorf("pool_truncation = %q, which is not a member of the declared vocabulary %v", got, vocabulary)
		}
	}
}

// TestCohortPoolTruncationProducesOnlyPublishedBases closes the other end: the
// only producer of this value must never mint one the vocabulary does not
// declare. Quantified over the whole closed input space, so a new arm cannot
// return an undeclared member on a combination nobody wrote a case for.
func TestCohortPoolTruncationProducesOnlyPublishedBases(t *testing.T) {
	published := make(map[CohortPoolTruncationBasis]bool)
	for _, basis := range CohortPoolTruncationBasisVocabulary() {
		published[basis] = true
	}
	if len(published) == 0 {
		t.Fatal("empty vocabulary -- nothing below can fail")
	}
	seen := make(map[CohortPoolTruncationBasis]bool)
	for _, fulltext := range []bool{false, true} {
		for _, hopWalk := range []bool{false, true} {
			for _, exactName := range []bool{false, true} {
				for _, census := range []bool{false, true} {
					basis, _, _ := cohortPoolTruncation(fulltext, hopWalk, exactName, census)
					if !published[basis] {
						t.Errorf("cohortPoolTruncation(%v, %v, %v, %v) returned %q, which the vocabulary does not declare", fulltext, hopWalk, exactName, census, basis)
					}
					seen[basis] = true
				}
			}
		}
	}
	// A member the producer can never emit is dead vocabulary, and a dead
	// member on a telemetry line reads to an operator as a state the system
	// can reach.
	for basis := range published {
		if !seen[basis] {
			t.Errorf("vocabulary member %q is never produced by cohortPoolTruncation over its whole input space -- it is dead vocabulary", basis)
		}
	}
}

// TestCohortKindBasisLineCarriesTheRequestID is r1 finding 2, as a test.
//
// The reviewer's mutant: delete graphRequestIDLogAttrs(ctx) from the sink and
// the whole package stays green, because every existing sink test drives it
// with context.Background(). The key that mutant removes is the JOIN KEY --
// without it an operator holding a suspicious member count cannot tie this
// line to the investigation that produced it, which is the exact failure this
// adapter already paid for once (see graphRequestIDLogAttrs' own doc comment:
// a per-request grep returned nothing and "cannot attribute" was misread as
// "did not run").
//
// WithRequestID silently rejects a non-canonical id -- `req_` plus exactly 32
// lowercase hex characters -- and returns the context UNCHANGED, so a
// readable label here would leave the context bare and this test would fail
// against correct production code. The id below is canonical for that reason.
func TestCohortKindBasisLineCarriesTheRequestID(t *testing.T) {
	t.Parallel()
	const canonicalRequestID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), canonicalRequestID)
	if _, ok := observability.RequestIDFromContext(ctx); !ok {
		t.Fatalf("the fixture's own context carries no request id -- WithRequestID rejected %q, so this test would pass over correct code", canonicalRequestID)
	}

	record := captureCohortKindBasisLineFull(t, ctx, productionSinkLevel,
		contextfabric.SubjectTeam, graphrank.CohortKindFromFrameMemberKind, true,
		CohortPoolTruncationTruncated, []CohortPoolTruncationArm{CohortPoolTruncationArmFulltext})

	if got := record["request_id"]; got != canonicalRequestID {
		t.Errorf("request_id = %v, want %q -- without it this line cannot be attributed to an investigation, and a telemetry line that cannot be attributed cannot prove a path did or did not run",
			got, canonicalRequestID)
	}
}

// TestCohortKindBasisLineOmitsTheRequestIDWhenTheContextHasNone is the
// complement: the key is absent, never present-and-empty. A line that always
// carries the key, sometimes blank, makes "no request id" and "the id was the
// empty string" the same reading.
func TestCohortKindBasisLineOmitsTheRequestIDWhenTheContextHasNone(t *testing.T) {
	t.Parallel()
	record := captureCohortKindBasisLineFull(t, context.Background(), productionSinkLevel,
		contextfabric.SubjectTeam, graphrank.CohortKindFromFrameMemberKind, true, CohortPoolTruncationNone, nil)

	if _, present := record["request_id"]; present {
		t.Errorf("request_id is present on a line emitted with no request id in context: %v", record["request_id"])
	}
}

// TestCohortKindBasisLineIsEmittedAtInfo is r1 finding 4, as a test.
//
// The reviewer's mutant: change the sink's Info to Debug and the package stays
// green, because the existing sink tests configure a Debug-level handler. At
// the production default that mutant deletes the entire diagnostic line while
// every test still passes -- the "a measurement that did not happen must fail
// loudly" shape, one level out.
//
// Asserted by OBSERVING the handler admit the line at Info and by proving the
// same instrument can suppress one: the negative half is what makes the
// positive half mean anything, since a handler that admitted everything would
// pass the first assertion over any level at all.
func TestCohortKindBasisLineIsEmittedAtTheProductionLevel(t *testing.T) {
	t.Parallel()
	if productionSinkLevel != slog.LevelInfo {
		t.Fatalf("productionSinkLevel = %v, want Info -- the constant every other test captures through must be the level a deployed handler actually runs at, or those tests stop pinning anything", productionSinkLevel)
	}
	if got := captureCohortKindBasisLineCount(t, productionSinkLevel); got != 1 {
		t.Errorf("a production-level handler admitted %d cohort-kind-basis lines, want 1 -- below this level the line is invisible in a real deployment and the whole diagnostic is gone", got)
	}
	if got := captureCohortKindBasisLineCount(t, slog.LevelError); got != 0 {
		t.Fatalf("an Error-level handler admitted %d lines, want 0 -- the instrument cannot suppress anything, so the assertion above proves nothing about the line's own level", got)
	}
}

// TestCohortKindBasisLineNamesTheCutArms pins the second key: the decision and
// the arms are separate values, and the arms render in vocabulary order.
func TestCohortKindBasisLineNamesTheCutArms(t *testing.T) {
	t.Parallel()
	record := captureCohortKindBasisLineFull(t, context.Background(), productionSinkLevel,
		contextfabric.SubjectTeam, graphrank.CohortKindFromFrameMemberKind, true,
		CohortPoolTruncationTruncated,
		// Supplied out of vocabulary order on purpose: the renderer must not
		// let a caller's ordering reach the line, or two identical outcomes
		// produce two different strings.
		[]CohortPoolTruncationArm{CohortPoolTruncationArmHopWalk, CohortPoolTruncationArmFulltext})

	if got := record["pool_truncation"]; got != string(CohortPoolTruncationTruncated) {
		t.Errorf("pool_truncation = %v, want %q", got, CohortPoolTruncationTruncated)
	}
	if got := record["pool_truncation_arms"]; got != "fulltext,hop_walk" {
		t.Errorf("pool_truncation_arms = %v, want %q -- rendered in vocabulary order, not the caller's", got, "fulltext,hop_walk")
	}
}

// TestCohortKindBasisLineCarriesEmptyArmsWhenNothingWasCut keeps the two keys'
// vocabularies from colliding: the arms key is EMPTY when no arm was cut, not
// the string "none", which is a member of the DECISION vocabulary on the other
// key. An operator grepping one key's value must never match the other's.
func TestCohortKindBasisLineCarriesEmptyArmsWhenNothingWasCut(t *testing.T) {
	t.Parallel()
	record := captureCohortKindBasisLineFull(t, context.Background(), productionSinkLevel,
		contextfabric.SubjectTeam, graphrank.CohortKindFromFrameMemberKind, true, CohortPoolTruncationNone, nil)

	if got := record["pool_truncation_arms"]; got != "" {
		t.Errorf("pool_truncation_arms = %q, want the empty string", got)
	}
	if got := record["pool_truncation"]; got != string(CohortPoolTruncationNone) {
		t.Errorf("pool_truncation = %v, want %q", got, CohortPoolTruncationNone)
	}
}
