package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// gateLogTelemetry is recordingTelemetry plus the real gate line recorder, so a
// test can count the lines the production sink writes for a real engine turn.
type gateLogTelemetry struct {
	*recordingTelemetry
	sink SlogEngineTelemetry
}

func (g gateLogTelemetry) RecordEngineFactGate(ctx context.Context, principal storage.Principal, decision EngineFactGateDecision) {
	g.sink.RecordEngineFactGate(ctx, principal, decision)
}

func newGateLogTelemetry(buf *bytes.Buffer) gateLogTelemetry {
	return gateLogTelemetry{
		recordingTelemetry: &recordingTelemetry{},
		sink:               NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))),
	}
}

func countGateLines(t *testing.T, buf *bytes.Buffer) int {
	t.Helper()
	count := 0
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		if line["msg"] == EngineFactGateLogMessage {
			count++
		}
	}
	return count
}

var gateSitesRestricted = storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/allowed"}}

// TestEveryEngineFactReadSiteWritesOneGateLinePerRead drives the three engine
// fact read sites as a restricted caller (the main read, the group read and the
// period delta read) and requires one gate line per read, and the declared
// per-request bound to be the sum of the lines the sites wrote.
func TestEveryEngineFactReadSiteWritesOneGateLinePerRead(t *testing.T) {
	var mainBuf bytes.Buffer
	var reads []CanonicalFactRequest
	graph, _ := chaos7080ProjectGraph(StoredSubjectAdmitted, nil)
	if _, err := chaos7080Engine(t, graph, &reads, newGateLogTelemetry(&mainBuf)).Investigate(context.Background(), gateSitesRestricted, validInvestigationRequest()); err != nil {
		t.Fatalf("main read turn: %v", err)
	}
	mainLines := countGateLines(t, &mainBuf)
	if mainLines != len(reads) || mainLines == 0 {
		t.Fatalf("a plain turn made %d fact reads and wrote %d gate lines, want one per read and at least one", len(reads), mainLines)
	}

	var groupBuf bytes.Buffer
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	groupTelemetry := newGateLogTelemetry(&groupBuf)
	engine, request := groupReadEngineFixture(t, groupTelemetry, recorder)
	_, err := engine.Investigate(context.Background(), gateSitesRestricted, request)
	if err != nil {
		t.Fatalf("grouped turn: %v", err)
	}
	groupLines := countGateLines(t, &groupBuf)
	if len(recorder.requests) < 2 || groupLines != len(recorder.requests) {
		t.Fatalf("a grouped turn made %d fact reads (want at least 2: members and groups) and wrote %d gate lines, want one per read", len(recorder.requests), groupLines)
	}

	var deltaBuf bytes.Buffer
	delta := &recordingFactReader{}
	telemetry := newGateLogTelemetry(&deltaBuf)
	deltaEngine := &Engine{facts: newGatedFactReader(delta, nil, telemetry), telemetry: telemetry}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	current := []CanonicalFact{periodDeltaHealthFact(team, map[string]FactValue{"severity": StringFactValue("elevated"), "severity_as_of": StringFactValue("2026-09-18")})}
	deltaEngine.applyPeriodDelta(context.Background(), gateSitesRestricted, QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}, []SubjectRef{team}, current, time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), true)
	deltaLines := countGateLines(t, &deltaBuf)
	if len(delta.calls) != 1 || deltaLines != 1 {
		t.Fatalf("the period delta made %d fact reads and wrote %d gate lines, want 1 and 1", len(delta.calls), deltaLines)
	}

	// A grouped turn is the main read plus the group read; the period delta
	// is the third site. Their sum is the most gate lines one request writes.
	if got := groupLines + deltaLines; got != EngineFactGateReadsPerRequest {
		t.Fatalf("grouped turn (%d) + period delta (%d) gate lines = %d, want the declared per-request bound %d", groupLines, deltaLines, got, EngineFactGateReadsPerRequest)
	}
}

type factRootReasonTelemetry struct {
	*recordingTelemetry
	reasons *[]StoredResultAuthorizationReason
}

func (f factRootReasonTelemetry) RecordFactRootRefused(_ context.Context, _ storage.Principal, decision StoredResultAuthorization, _ int) {
	*f.reasons = append(*f.reasons, decision.Reason)
}

// TestFactRootRefusalReasonsRecordedByTheEngineAreTheDeclaredVocabulary drives
// the fact-root recheck through every refusal it can reach (a denied root, an
// absent root, a graph that is not projected, a root of another organization)
// and requires the recorded reasons to be exactly FactRootRefusedReasonVocabulary
// less group_unproven, which needs a result with groups and so cannot occur on
// a set of committed roots.
func TestFactRootRefusalReasonsRecordedByTheEngineAreTheDeclaredVocabulary(t *testing.T) {
	type driven struct {
		name  string
		build func() (*storedSubjectGraph, SubjectRef)
	}
	cases := []driven{
		{"denied", func() (*storedSubjectGraph, SubjectRef) { return chaos7080ProjectGraph(StoredSubjectDenied, nil) }},
		{"absent", func() (*storedSubjectGraph, SubjectRef) { return chaos7080ProjectGraph(StoredSubjectAbsent, nil) }},
		{"not projected", func() (*storedSubjectGraph, SubjectRef) {
			return chaos7080ProjectGraph(StoredSubjectAdmitted, ErrGraphNotProjected)
		}},
		{"other organization", func() (*storedSubjectGraph, SubjectRef) {
			graph, _ := chaos7080ProjectGraph(StoredSubjectAdmitted, nil)
			root := SubjectRef{Kind: SubjectOrganization, CanonicalID: "organization:other_org", Label: "other_org"}
			graph.resolution.Committed = []SubjectRef{root}
			return graph, root
		}},
	}
	seen := map[StoredResultAuthorizationReason]bool{}
	for _, tc := range cases {
		graph, _ := tc.build()
		var reasons []StoredResultAuthorizationReason
		var reads []CanonicalFactRequest
		telemetry := factRootReasonTelemetry{recordingTelemetry: &recordingTelemetry{}, reasons: &reasons}
		_, _ = chaos7080Engine(t, graph, &reads, telemetry).Investigate(context.Background(), gateSitesRestricted, validInvestigationRequest())
		if len(reasons) != 1 {
			t.Fatalf("%s: the recheck recorded %d refusals, want 1", tc.name, len(reasons))
		}
		seen[reasons[0]] = true
	}
	declared := map[StoredResultAuthorizationReason]bool{}
	for _, reason := range FactRootRefusedReasonVocabulary() {
		declared[reason] = true
	}
	for reason := range seen {
		if !declared[reason] {
			t.Errorf("the engine recorded reason %q, which is not in FactRootRefusedReasonVocabulary", reason)
		}
	}
	for reason := range declared {
		if !seen[reason] {
			t.Errorf("declared reason %q was not produced by any driven refusal", reason)
		}
	}
}
