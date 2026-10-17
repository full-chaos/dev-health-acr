package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	principalOrg1 = storage.Principal{OrgID: "org_1"}

	priorReceiptsSkippedKeys   = []string{"time", "level", "msg", "org_id", "skipped_count", "request_id"}
	priorReceiptSkipReasonKeys = []string{"time", "level", "msg", "org_id", "reason", "count", "request_id"}
	commitRetractionKeys       = []string{"time", "level", "msg", "org_id", "commit_basis", "subject_kind", "winning_mechanism", "provisional_committed", "final_committed", "request_id"}
	commitKeptKeys             = []string{"time", "level", "msg", "org_id", "subject_kind", "request_id"}
	synthesisOverrideKeys      = []string{"time", "level", "msg", "org_id", "from_status", "to_status", "reason", "committed_count", "unasked_fact_kinds", "request_id"}
	reuseOutcomeKeys           = []string{"time", "level", "msg", "org_id", "outcome", "ownership_routing_version", "request_id"}
	reuseBypassKeys            = []string{"time", "level", "msg", "org_id", "reason", "request_id"}
	reuseContainmentKeys       = []string{
		"time", "level", "msg", "org_id", "demanded_refs", "visible_refs", "missing_refs", "missing_citation", "stripped_refs", "stripped_labels",
		"dropped_candidates", "dropped_members", "dropped_drivers", "dropped_findings", "dropped_paths", "disclosure", "request_id",
	}
	subjectlessTerminalKeys = []string{
		"time", "level", "msg", "org_id", "reason", "refusal_basis", "declared_kinds", "offered_kinds", "evaluated_roles", "advanced_role",
		"advancing_channel", "offers_evaluated", "offers_advancing", "request_id",
	}
	storedAnswerabilityKeys = []string{
		"time", "level", "msg", "org_id", "surface", "determination", "decided_by", "semantic_state", "repaired", "stored_status", "served_status",
		"evaluated_roles", "advanced_role", "advancing_channel", "offers_evaluated", "offers_advancing", "request_id",
	}
	servedRequestIDKeys = []string{"time", "level", "msg", "org_id", "served_request_id", "request_id_mismatch", "request_id"}
	bindingEpochKeys    = []string{"time", "level", "msg", "org_id", "flip_during_investigation", "binding_epoch_delta", "request_id"}
)

func without(keys []string, drop ...string) []string {
	var out []string
	for _, key := range keys {
		kept := true
		for _, d := range drop {
			if key == d {
				kept = false
			}
		}
		if kept {
			out = append(out, key)
		}
	}
	return out
}

func answerLineSink(buf *bytes.Buffer, level slog.Level) (contextfabric.SlogEngineTelemetry, context.Context) {
	ctx := observability.WithRequestID(context.Background(), factScopeCertifyRequestID)
	return contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))), ctx
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sameMembers(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(sortedCopy(got), "|") != strings.Join(sortedCopy(want), "|") {
		t.Errorf("%s: producer wrote %v, the declaration is %v", what, got, want)
	}
}

func TestPriorSubjectReceiptLinesCertifyWhatTheRecorderWrites(t *testing.T) {
	t.Parallel()
	reasons, skipped, deltas := contextfabric.PriorSubjectReceiptSkipReasonsForTest()
	sameMembers(t, "receipt skip reasons", reasons, contextfabric.PriorSubjectReceiptSkipReasonVocabulary())
	if len(skipped) != 1 || skipped[0] != 4 {
		t.Fatalf("the recorder wrote the aggregate %v, want one line of 4", skipped)
	}
	for _, reason := range reasons {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordPriorSubjectReceiptSkipReason(ctx, principalOrg1, reason, 2, deltas[reason])
		keys := priorReceiptSkipReasonKeys
		want := map[string]any{"org_id": "org_1", "reason": reason, "count": 2}
		if reason == "stale_graph_epoch" {
			keys = append(append([]string(nil), keys[:len(keys)-1]...), "epoch_delta", "request_id")
			want["epoch_delta"] = 7
		}
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), keys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.PriorSubjectReceiptSkipReason, want)
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordPriorSubjectReceiptSkipReason(ctx, principalOrg1, "future_reason", 1, 0)
	certifyRefused(t, buf.Bytes(), eventspec.PriorSubjectReceiptSkipReason, "an unknown skip reason")
	buf.Reset()
	sink.RecordPriorSubjectReceiptSkipReason(ctx, principalOrg1, "stale_graph_epoch", 1, 0)
	if !strings.Contains(buf.String(), `"epoch_delta":0`) {
		t.Errorf("a stale epoch skip writes the epoch distance even at zero: %s", buf.String())
	}
	buf.Reset()
	sink.RecordPriorSubjectReceiptsSkipped(ctx, principalOrg1, 3)
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), priorReceiptsSkippedKeys, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.PriorSubjectReceiptsSkipped, map[string]any{"org_id": "org_1", "skipped_count": 3})
	buf.Reset()
	sink.RecordPriorSubjectReceiptsSkipped(context.Background(), principalOrg1, 3)
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), without(priorReceiptsSkippedKeys, "request_id"), 1)
}

func TestCommitGateLinesCertifyEveryProducerValueAndRefuseAnyOther(t *testing.T) {
	t.Parallel()
	for _, basis := range contextfabric.CommitBasisLogVocabulary() {
		for _, mechanism := range contextfabric.CommitWinningMechanismVocabulary() {
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			sink.RecordCommitAffirmationRetraction(ctx, principalOrg1, contextfabric.CommitAffirmationOutcome{
				Basis: contextfabric.CommitBasis(basis), SubjectKind: contextfabric.SubjectProject, WinningMechanism: mechanism, ProvisionalCommitted: 2,
			})
			pinKeys(t, bytes.NewBuffer(buf.Bytes()), commitRetractionKeys, 1)
			certifyRequestLine(t, buf.Bytes(), eventspec.CommitAffirmationRetraction, map[string]any{
				"org_id": "org_1", "commit_basis": basis, "winning_mechanism": mechanism, "provisional_committed": 2, "final_committed": 0,
			})
		}
	}
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordCommitKeptByNoMemberFound(ctx, principalOrg1, kind)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), commitKeptKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.CommitKeptByNoMemberFound, map[string]any{"org_id": "org_1", "subject_kind": string(kind)})
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordCommitAffirmationRetraction(ctx, principalOrg1, contextfabric.CommitAffirmationOutcome{Basis: "future_basis", SubjectKind: contextfabric.SubjectProject})
	certifyRefused(t, buf.Bytes(), eventspec.CommitAffirmationRetraction, "an unknown commit basis")
	buf.Reset()
	sink.RecordCommitAffirmationRetraction(ctx, principalOrg1, contextfabric.CommitAffirmationOutcome{SubjectKind: contextfabric.SubjectProject, WinningMechanism: "future_mechanism"})
	certifyRefused(t, buf.Bytes(), eventspec.CommitAffirmationRetraction, "an unknown match mechanism")
	buf.Reset()
	sink.RecordCommitKeptByNoMemberFound(ctx, principalOrg1, "future_kind")
	certifyRefused(t, buf.Bytes(), eventspec.CommitKeptByNoMemberFound, "an unknown subject kind")
}

func TestSynthesisStatusOverrideLineCertifiesEveryProducerValueAndRefusesAnyOther(t *testing.T) {
	t.Parallel()
	kinds := contractsv1.ContextFabricFactKindVocabulary()
	for _, reason := range contextfabric.SynthesisStatusOverrideReasonVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordSynthesisStatusOverride(ctx, principalOrg1, contextfabric.SynthesisStatusOverrideOutcome{
			From: contractsv1.ContextFabricInvestigationNoMatch, To: contractsv1.ContextFabricInvestigationPartial,
			Reason: contextfabric.SynthesisStatusOverrideReason(reason), CommittedCount: 2,
			UnaskedFactKinds: string(kinds[0]) + "," + string(kinds[1]),
		})
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), synthesisOverrideKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.SynthesisStatusOverride, map[string]any{"org_id": "org_1", "reason": reason, "committed_count": 2})
	}
	for _, status := range contextfabric.InvestigationStatusLogVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordSynthesisStatusOverride(ctx, principalOrg1, contextfabric.SynthesisStatusOverrideOutcome{
			From: contractsv1.ContextFabricInvestigationStatus(status), To: contractsv1.ContextFabricInvestigationStatus(status),
			Reason: contextfabric.SynthesisStatusOverrideClarificationUnavailable,
		})
		certifyRequestLine(t, buf.Bytes(), eventspec.SynthesisStatusOverride, map[string]any{"from_status": status, "to_status": status, "unasked_fact_kinds": ""})
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordSynthesisStatusOverride(ctx, principalOrg1, contextfabric.SynthesisStatusOverrideOutcome{
		From: contractsv1.ContextFabricInvestigationNoMatch, To: contractsv1.ContextFabricInvestigationPartial, Reason: "future_reason",
	})
	certifyRefused(t, buf.Bytes(), eventspec.SynthesisStatusOverride, "an unknown override reason")
	buf.Reset()
	sink.RecordSynthesisStatusOverride(ctx, principalOrg1, contextfabric.SynthesisStatusOverrideOutcome{
		From: contractsv1.ContextFabricInvestigationNoMatch, To: contractsv1.ContextFabricInvestigationPartial,
		Reason: contextfabric.SynthesisStatusOverrideNoMatchKeptUnaskedFactKinds, UnaskedFactKinds: "future_kind",
	})
	certifyRefused(t, buf.Bytes(), eventspec.SynthesisStatusOverride, "an unknown unasked fact kind")
}

func TestAnswerReuseLinesCertifyEveryProducerValueAndRefuseAnyOther(t *testing.T) {
	t.Parallel()
	for _, outcome := range contextfabric.AnswerReuseOutcomeVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordAnswerReuse(ctx, principalOrg1, contextfabric.AnswerReuseOutcome(outcome), contextfabric.OwnershipRoutingVersion)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), reuseOutcomeKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseOutcome, map[string]any{"org_id": "org_1", "outcome": outcome, "ownership_routing_version": contextfabric.OwnershipRoutingVersion})
	}
	for _, reason := range contextfabric.AnswerReuseBypassReasonVocabulary() {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordAnswerReuseBypass(ctx, principalOrg1, contextfabric.AnswerReuseBypassReason(reason))
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), reuseBypassKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseBypass, map[string]any{"org_id": "org_1", "reason": reason})
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordAnswerReuse(ctx, principalOrg1, "future_outcome", contextfabric.OwnershipRoutingVersion)
	certifyRefused(t, buf.Bytes(), eventspec.AnswerReuseOutcome, "an unknown reuse outcome")
	buf.Reset()
	sink.RecordAnswerReuse(ctx, principalOrg1, contextfabric.AnswerReuseHit, "ownership-routing.v0")
	certifyRefused(t, buf.Bytes(), eventspec.AnswerReuseOutcome, "an unknown ownership routing version")
	buf.Reset()
	sink.RecordAnswerReuseBypass(ctx, principalOrg1, "future_reason")
	certifyRefused(t, buf.Bytes(), eventspec.AnswerReuseBypass, "an unknown bypass reason")
}

func TestAnswerReuseContainmentLineCertifiesWithAndWithoutADisclosure(t *testing.T) {
	t.Parallel()
	event := contextfabric.AnswerReuseContainmentEvent{
		DemandedCount: 9, VisibleCount: 6, MissingCount: 3, MissingCitation: true, StrippedRefs: 3, StrippedLabels: 2,
		DroppedCandidates: 1, DroppedMembers: 2, DroppedDrivers: 3, DroppedFindings: 4, DroppedPaths: 5,
	}
	for _, disclosure := range contextfabric.AnswerReuseContainmentDisclosureVocabulary() {
		event.Disclosure = disclosure
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordAnswerReuseContainment(ctx, principalOrg1, event)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), reuseContainmentKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseContainment, map[string]any{
			"org_id": "org_1", "demanded_refs": 9, "visible_refs": 6, "missing_refs": 3, "missing_citation": true, "disclosure": disclosure,
			"dropped_paths": 5,
		})
	}
	event.Disclosure = ""
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordAnswerReuseContainment(ctx, principalOrg1, event)
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), without(reuseContainmentKeys, "disclosure"), 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseContainment, map[string]any{"org_id": "org_1"})
	buf.Reset()
	event.Disclosure = "future_form"
	sink.RecordAnswerReuseContainment(ctx, principalOrg1, event)
	certifyRefused(t, buf.Bytes(), eventspec.AnswerReuseContainment, "an unknown disclosure form")
}

func TestSubjectlessTerminalLineCertifiesWhatTheProducerDecides(t *testing.T) {
	t.Parallel()
	sameMembers(t, "subjectless terminal reasons", contextfabric.SubjectlessTerminalReasonsForTest(), contextfabric.SubjectlessTerminalReasonVocabulary())
	drivers := contextfabric.StoredAnswerabilityDriversForTest(t)
	seenChannel := map[string]bool{}
	record := func(reason, basis, declared, offered string, answerability contextfabric.SubjectlessTerminalAnswerability) []byte {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordSubjectlessTerminal(ctx, principalOrg1, reason, basis, declared, offered, answerability)
		return buf.Bytes()
	}
	none := contextfabric.SubjectlessTerminalAnswerability{EvaluatedRoles: "none", AdvancedRole: "none", AdvancingChannel: "none"}
	for _, reason := range contextfabric.SubjectlessTerminalReasonsForTest() {
		raw := record(reason, contextfabric.ObservableRefusalBasisForTest(""), contextfabric.ObservableKindsForTest(nil), contextfabric.ObservableKindsForTest(nil), none)
		pinKeys(t, bytes.NewBuffer(raw), subjectlessTerminalKeys, 1)
		certifyRequestLine(t, raw, eventspec.SubjectlessTerminal, map[string]any{"org_id": "org_1", "reason": reason, "refusal_basis": "none", "declared_kinds": "none", "offered_kinds": "none"})
	}
	for _, basis := range contractsv1.ContextFabricRefusalBasisVocabulary() {
		raw := record("frame_gate_refused", contextfabric.ObservableRefusalBasisForTest(basis), "project", "ci_pipeline_run,pull_request", none)
		certifyRequestLine(t, raw, eventspec.SubjectlessTerminal, map[string]any{"refusal_basis": string(basis)})
	}
	for _, driver := range drivers {
		observed := driver.ObservableAnswerability()
		seenChannel[observed.AdvancingChannel] = true
		raw := record("ambiguous", "none", contextfabric.ObservableKindsForTest([]contextfabric.SubjectKind{contextfabric.SubjectProject}),
			contextfabric.ObservableKindsForTest([]contextfabric.SubjectKind{contextfabric.SubjectKind(contractsv1.ContextFabricSubjectCIRun), contextfabric.SubjectKind("pull_request")}), observed)
		certifyRequestLine(t, raw, eventspec.SubjectlessTerminal, map[string]any{
			"declared_kinds": "project", "evaluated_roles": observed.EvaluatedRoles, "advanced_role": observed.AdvancedRole, "advancing_channel": observed.AdvancingChannel,
		})
	}
	if !seenChannel["subject_candidate"] || !seenChannel["none"] {
		t.Errorf("the production drivers reached channels %v, want a candidate pick and none", seenChannel)
	}
	certifyRefused(t, record("future_reason", "none", "none", "none", none), eventspec.SubjectlessTerminal, "an unknown reason")
	certifyRefused(t, record("ambiguous", "future_basis", "none", "none", none), eventspec.SubjectlessTerminal, "an unknown refusal basis")
	certifyRefused(t, record("ambiguous", "none", "future_kind", "none", none), eventspec.SubjectlessTerminal, "an unknown declared kind")
	certifyRefused(t, record("ambiguous", "none", "none", "", none), eventspec.SubjectlessTerminal, "an empty offered kind list, which is the word none")
	badRoles := none
	badRoles.EvaluatedRoles = "subject:project:future_state"
	certifyRefused(t, record("ambiguous", "none", "none", "none", badRoles), eventspec.SubjectlessTerminal, "an unknown role state")
	badChannel := none
	badChannel.AdvancingChannel = "future_channel"
	certifyRefused(t, record("ambiguous", "none", "none", "none", badChannel), eventspec.SubjectlessTerminal, "an unknown channel")
}

func TestStoredAnswerabilityLineCertifiesEveryProductionDriver(t *testing.T) {
	t.Parallel()
	drivers := contextfabric.StoredAnswerabilityDriversForTest(t)
	seenStep, seenDetermination, seenReading := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, driver := range drivers {
		for _, served := range []contextfabric.InvestigationStatus{"", contextfabric.InvestigationClarificationRequired, contextfabric.InvestigationNoMatch} {
			var buf bytes.Buffer
			sink, ctx := answerLineSink(&buf, slog.LevelInfo)
			sink.RecordStoredAnswerability(ctx, principalOrg1, contextfabric.StoredAnswerabilitySurfaceReuse, driver, contextfabric.InvestigationClarificationRequired, served)
			if driver.Determination != contextfabric.StoredAnswerabilityNotApplicable {
				pinKeys(t, bytes.NewBuffer(buf.Bytes()), storedAnswerabilityKeys, 1)
				line := certifyRequestLine(t, buf.Bytes(), eventspec.StoredAnswerability, map[string]any{"org_id": "org_1", "surface": "reuse"})
				seenStep[line["decided_by"].(string)] = true
				seenDetermination[line["determination"].(string)] = true
				seenReading[line["semantic_state"].(string)] = true
			}
		}
		if driver.Determination == contextfabric.StoredAnswerabilityNotApplicable {
			continue
		}
		// The result by id route writes the same arguments without org_id and with its own request id.
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		args := contextfabric.StoredAnswerabilityLogArgs(contextfabric.StoredAnswerabilitySurfaceResultByID, driver, contextfabric.InvestigationClarificationRequired, contextfabric.InvestigationNoMatch)
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(factScopeCertifyRequestID))
		logger.InfoContext(context.Background(), contextfabric.StoredAnswerabilityLogMessage, args...)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), without(storedAnswerabilityKeys, "org_id"), 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.StoredAnswerability, map[string]any{"surface": "result_by_id"})
	}
	sameMembers(t, "stored answerability steps", keysOf(seenStep), without(contextfabric.StoredAnswerabilityStepVocabulary(), "none"))
	sameMembers(t, "stored answerability determinations", keysOf(seenDetermination), contextfabric.StoredAnswerabilityDeterminationVocabulary())
	for _, reading := range []string{"absent", "available", "not_read"} {
		if !seenReading[reading] {
			t.Errorf("no production driver reached the reading %q", reading)
		}
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordStoredAnswerability(ctx, principalOrg1, "future_surface", drivers[1], contextfabric.InvestigationClarificationRequired, "")
	certifyRefused(t, buf.Bytes(), eventspec.StoredAnswerability, "an unknown surface")
	buf.Reset()
	sink.RecordStoredAnswerability(ctx, principalOrg1, contextfabric.StoredAnswerabilitySurfaceReuse, drivers[1], "future_status", "")
	certifyRefused(t, buf.Bytes(), eventspec.StoredAnswerability, "an unknown stored status")
}

func keysOf(set map[string]bool) []string {
	var out []string
	for key := range set {
		out = append(out, key)
	}
	return out
}

func TestReuseServedRequestIDAndBindingEpochLinesCertify(t *testing.T) {
	t.Parallel()
	for _, mismatch := range []bool{false, true} {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordAnswerReuseServedRequestID(ctx, principalOrg1, "req_stored", mismatch)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), servedRequestIDKeys, 1)
		certifyRequestLine(t, buf.Bytes(), eventspec.AnswerReuseServedRequestID, map[string]any{"org_id": "org_1", "served_request_id": "req_stored", "request_id_mismatch": mismatch})
	}
	for _, flipped := range []bool{true, false} {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelDebug)
		sink.RecordBindingEpochDelta(ctx, principalOrg1, flipped, 4)
		pinKeys(t, bytes.NewBuffer(buf.Bytes()), bindingEpochKeys, 1)
		event := eventspec.BindingEpochUnchanged
		if flipped {
			event = eventspec.BindingEpochMoved
		}
		certifyRequestLine(t, buf.Bytes(), event, map[string]any{"org_id": "org_1", "flip_during_investigation": flipped, "binding_epoch_delta": 4})
	}
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordBindingEpochDelta(ctx, principalOrg1, false, 0)
	if buf.Len() != 0 {
		t.Errorf("the unchanged epoch line is a Debug line, written at Info: %s", buf.String())
	}
	buf.Reset()
	sink, _ = answerLineSink(&buf, slog.LevelDebug)
	sink.RecordBindingEpochDelta(context.Background(), principalOrg1, true, 4)
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), without(bindingEpochKeys, "request_id"), 1)
}
