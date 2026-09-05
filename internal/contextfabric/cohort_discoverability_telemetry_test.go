package contextfabric

// THE DECISION-BASIS TELEMETRY for the cohort-discoverability decision, on
// the EMITTED RECORD.
//
// The requirement derivation now refuses a computed step whose declared
// member kind has no discovery arm. That refusal is counted in the existing
// `unresolvable_member_set` arm, and truthfully so -- the frame really does
// resolve no member set -- but the arm cannot say WHICH of its two causes
// fired, and the two send an operator to opposite ends of the pipeline: an
// expression that enumerates nothing is a different question to ask, while a
// kind with no arm is retrieval work to do. This key is what names it.
//
// A SEPARATE FILE from the harm tests, deliberately: this file names a field
// that does not exist on the parent commit, so it cannot compile there. The
// harm tests compile at the parent and FAIL at runtime, which is what makes
// their red a statement about behaviour rather than about a missing
// identifier.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// captureSlogJSONAtProductionLevel captures emitted records at slog's DEFAULT
// level, which is what the service runs at.
//
// The package's other capture helper opens at Debug, which is right for the
// lines it reads and wrong for this rule: a line demoted below Info would
// still appear there, and would silently disappear in production. Opening at
// the production default is what makes a demotion fail this test the way it
// would fail an operator.
func captureSlogJSONAtProductionLevel(t *testing.T, emit func(*slog.Logger)) []map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	emit(logger)

	records := make([]map[string]any, 0, 2)
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("telemetry emitted a line that is not valid JSON: %v (%q)", err, line)
		}
		records = append(records, record)
	}
	return records
}

// canonicalRequestContext returns a context carrying a REAL request id.
//
// `observability.WithRequestID` requires `req_` plus exactly 32 lowercase hex
// characters and returns the context UNCHANGED for anything else, so a
// readable label leaves the context bare and the resulting "request_id missing" failure reads as a
// production emit defect. The constraint belongs here, in a comment beside the
// literal, rather than being rediscovered.
func canonicalRequestContext() context.Context {
	return observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
}

// discoverableEventForKind builds the frame-validation event the PRODUCTION
// builder produces for a discovered-kind cohort over `kind`.
//
// It goes through ValidateFrame and FrameValidationEventFrom, never by setting
// the field under test by hand: a hand-set field proves the sink and says
// nothing about whether production ever populates it.
//
// The second return is false when the fixture frame does not validate, so a
// caller can pick a kind that does rather than assert against an event built
// from a refused frame (where the field is empty by design).
func discoverableEventForKind(t *testing.T, kind SubjectKind) (FrameValidationEvent, bool) {
	t.Helper()
	proposed := *rankingCohortFrame(kind)
	result := ValidateFrame(proposed, nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		return FrameValidationEvent{}, false
	}
	return FrameValidationEventFrom(proposed, result, "", DeriveRequirements(result.Frame, GenerateObligationSeed(nil), nil)), true
}

// TestTheCohortDiscoverabilityReasonIsNAMEDOnTheEmittedLine is the pin, and it
// reads the EMITTED RECORD rather than the event struct.
//
// It covers BOTH decisions the `unresolvable_member_set` arm cannot separate,
// plus the served case, so the key is proven to vary rather than proven to be
// present. A key that is always the same value is a constant with a log key.
func TestTheCohortDiscoverabilityReasonIsNAMEDOnTheEmittedLine(t *testing.T) {
	t.Parallel()

	// The discoverable case: a kind with a proven arm.
	servedEvent, ok := discoverableEventForKind(t, SubjectTeam)
	if !ok {
		t.Fatalf("the discovered-kind fixture over %q does not validate, so this test cannot build the served case it exists to assert", SubjectTeam)
	}

	// The unservable case, chosen MECHANICALLY: the first published kind
	// without a proven arm whose frame validates. Hand-picking one would go
	// stale the day an arm is proven for it, and the test would then assert a
	// refusal that no longer happens.
	var unservableEvent FrameValidationEvent
	var unservableKind SubjectKind
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := SubjectKind(published)
		if CohortMemberSetResolvable(rankingCohortFrame(kind).SubjectExpression) {
			continue
		}
		event, valid := discoverableEventForKind(t, kind)
		if !valid {
			continue
		}
		unservableEvent, unservableKind = event, kind
		break
	}
	if unservableKind == "" {
		t.Fatal("no published kind without a proven arm produces a validating discovered-kind frame, so the member_kind_unservable case cannot be exercised -- fix the fixture rather than dropping the case")
	}

	// The not-a-cohort-variant case: a named subject, which declares an
	// expected kind WITH a proven arm and still enumerates nothing. It is the
	// case a kind lookup alone would get wrong.
	namedProposed := frameWith([]InvestigationGoal{GoalRankOrSurvey}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	namedResult := ValidateFrame(namedProposed, nil, "")
	if namedResult.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("the named-subject fixture does not validate (outcome %q), so the not_a_cohort_variant case cannot be built", namedResult.Outcome)
	}
	namedEvent := FrameValidationEventFrom(namedProposed, namedResult, "", DeriveRequirements(namedResult.Frame, GenerateObligationSeed(nil), nil))

	for _, testCase := range []struct {
		name  string
		event FrameValidationEvent
		want  CohortDiscoverability
	}{
		{"a kind with a proven arm", servedEvent, CohortDiscoverable},
		{"a kind with no proven arm", unservableEvent, CohortMemberKindUnservable},
		{"an expression that enumerates nothing", namedEvent, CohortNotACohortVariant},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			records := captureSlogJSONAtProductionLevel(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordFrameValidation(
					canonicalRequestContext(),
					storage.Principal{OrgID: "org_sink_test"},
					testCase.event,
				)
			})
			if len(records) != 1 {
				t.Fatalf("got %d records, want 1 -- validation fires once per frame, and a line demoted below the production default would read as zero here", len(records))
			}
			record := records[0]

			// KEY:VALUE in the sink's own encoding, never a substring of the
			// line. A substring cannot see a field disappear when its value
			// also occurs elsewhere in the record.
			raw, present := record["cohort_discoverability"]
			if !present {
				t.Fatalf("record carries no `cohort_discoverability` key -- an operator greps for it, and an absent key is not an observed value")
			}
			got, isString := raw.(string)
			if !isString {
				t.Fatalf("cohort_discoverability encoded as %T (%v), want a string: the vocabulary is a string enum and a reader parsing it would break", raw, raw)
			}
			if got != string(testCase.want) {
				t.Errorf("cohort_discoverability = %q, want %q", got, testCase.want)
			}
			if !ValidCohortDiscoverability(CohortDiscoverability(got)) {
				t.Errorf("cohort_discoverability = %q, which the closed vocabulary does not declare", got)
			}

			// The JOIN KEYS. Without them a per-request diagnosis cannot
			// attribute this line to the request that produced it, and
			// "cannot attribute" gets read as "did not run".
			if record["org_id"] != "org_sink_test" {
				t.Errorf("org_id = %v, want %q", record["org_id"], "org_sink_test")
			}
			if requestID, ok := record["request_id"]; !ok || requestID == "" {
				t.Errorf("record carries no request_id (%v); a telemetry line without one cannot prove a code path did or did not run for a given request", requestID)
			}
			// The production default level, asserted rather than assumed.
			if record["level"] != "INFO" {
				t.Errorf("level = %v, want INFO", record["level"])
			}
		})
	}
}

// TestTheCohortDiscoverabilityKeyIsEmptyAndPresentOnARefusedFrame states the
// absence rather than leaving it to chance.
//
// A refused frame has no validated expression to ask about, so there is no
// reason to report. The key is still emitted: an ABSENT key and an empty value
// look alike to nobody, because the empty string is not a member of the
// vocabulary and the same line carries the outcome that explains it. Dropping
// the key instead would make "the frame was refused" and "the classifier never
// ran" the same observation.
func TestTheCohortDiscoverabilityKeyIsEmptyAndPresentOnARefusedFrame(t *testing.T) {
	t.Parallel()
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	result := ValidateFrame(proposed, nil, "")
	if result.Outcome == FrameValidationOutcomeValid {
		t.Fatalf("the refusal fixture now VALIDATES, so this test asserts nothing about a refused frame")
	}
	event := FrameValidationEventFrom(proposed, result, "", nil)
	if event.CohortDiscoverability != "" {
		t.Errorf("a refused frame carried reason %q; there is no validated expression to ask about", event.CohortDiscoverability)
	}

	records := captureSlogJSONAtProductionLevel(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFrameValidation(
			canonicalRequestContext(),
			storage.Principal{OrgID: "org_sink_test"},
			event,
		)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	got, present := records[0]["cohort_discoverability"]
	if !present {
		t.Fatal("the key is absent on a refused frame; an absent key and an empty value must not be the same observation")
	}
	if got != "" {
		t.Errorf("cohort_discoverability = %v on a refused frame, want empty", got)
	}
}
