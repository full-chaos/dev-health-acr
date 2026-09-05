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

func captureCohortKindBasisLineWithPoolTruncation(t *testing.T, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool, poolTruncation CohortPoolTruncationBasis) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	SlogTelemetry{Logger: logger}.RecordCohortKindBasis(
		context.Background(), "org_sink_test", declaredKind, basis, discovered, poolTruncation)

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

	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "request_id": true,
		"org_id": true, "member_kind": true, "basis": true, "discovered": true,
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
