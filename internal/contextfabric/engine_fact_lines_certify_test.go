package contextfabric_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	factRootRefusedLegacyKeys = []string{
		"time", "level", "msg", "org_id", "reason", "refused_count", "denied_count", "absent_count", "refused_kinds", "request_id",
	}
	engineFactGateLegacyKeys = []string{
		"time", "level", "msg", "org_id", "surface", "decision", "reason", "repository_scope_count", "facts_checked", "facts_undeclared",
		"facts_withheld_from", "references_checked", "references_refused", "rows_withheld", "fields_withheld", "evidence_withheld",
		"fields_undeclared", "request_id",
	}
	organizationSubjectReadLegacyKeys = []string{
		"time", "level", "msg", "org_id", "reason", "survey_member_kind", "organization_facts_read", "organization_fact_kinds", "request_id",
	}
)

func recordFactRootRefused(decision contextfabric.StoredResultAuthorization, refused int) []byte {
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordFactRootRefused(ctx, storage.Principal{OrgID: "org_1"}, decision, refused)
	return buf.Bytes()
}

func recordEngineFactGate(decision contextfabric.EngineFactGateDecision, scopes int) []byte {
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: make([]string, scopes)}
	telemetry.RecordEngineFactGate(ctx, principal, decision)
	return buf.Bytes()
}

func recordOrganizationSubjectRead(event contextfabric.OrganizationSubjectReadEvent) []byte {
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordOrganizationSubjectRead(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

func TestFactRootRefusedLineCertifiesEveryReasonAndKind(t *testing.T) {
	t.Parallel()
	decision := contextfabric.StoredResultAuthorization{
		Reason: contextfabric.StoredResultReasonSubjectDenied, DeniedCount: 2, AbsentCount: 1,
		RefusedKinds: []string{string(contractsv1.ContextFabricSubjectKindVocabulary()[0])},
	}
	raw := recordFactRootRefused(decision, 3)
	pinKeys(t, bytes.NewBuffer(raw), factRootRefusedLegacyKeys, 1)
	line := certifyRequestLine(t, raw, eventspec.FactRootRefused, map[string]any{
		"org_id": "org_1", "reason": "subject_denied", "refused_count": 3, "denied_count": 2, "absent_count": 1,
		"refused_kinds": []any{string(contractsv1.ContextFabricSubjectKindVocabulary()[0])},
	})
	if line["level"] != "WARN" {
		t.Fatalf("the refusal must stay a Warn line, got %v", line["level"])
	}
	for _, reason := range contextfabric.FactRootRefusedReasonVocabulary() {
		d := decision
		d.Reason = reason
		certifyRequestLine(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, map[string]any{"reason": string(reason)})
	}
	for _, reason := range []contextfabric.StoredResultAuthorizationReason{
		contextfabric.StoredResultReasonNoSubjects, contextfabric.StoredResultReasonUnrestrictedPrincipal, contextfabric.StoredResultReasonSubjectsAdmitted,
		contextfabric.StoredResultReasonAuthorizerMissing, contextfabric.StoredResultReasonGraphReadFailed,
		contextfabric.StoredResultReasonGroupUnproven, contextfabric.StoredResultReasonGrantMismatch, contextfabric.StoredResultReasonGrantUnrecorded,
	} {
		d := decision
		d.Reason = reason
		certifyRefused(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, "a reason the recheck never records: "+string(reason))
	}
	var kinds []string
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kinds = append(kinds, string(kind))
	}
	d := decision
	d.RefusedKinds = kinds
	asAny := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		asAny = append(asAny, kind)
	}
	certifyRequestLine(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, map[string]any{"refused_kinds": asAny})
	d.RefusedKinds = []string{}
	certifyRequestLine(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, map[string]any{"refused_kinds": []any{}})

	d.Reason = contextfabric.StoredResultAuthorizationReason("future_reason")
	certifyRefused(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, "an unknown reason")
	d = decision
	d.RefusedKinds = []string{"future_kind"}
	certifyRefused(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, "an unknown refused kind")
	mutated := bytes.Replace(raw, []byte(`"denied_count":2,"absent_count":1`), []byte(`"absent_count":1,"denied_count":2`), 1)
	if bytes.Equal(raw, mutated) {
		t.Fatalf("the fixture did not contain the key pair to swap: %s", raw)
	}
	if got := jsonKeys(t, bytes.TrimSpace(mutated)); reflect.DeepEqual(got, factRootRefusedLegacyKeys) {
		t.Fatalf("swapping two keys must change the pinned key sequence")
	}
}

func TestEngineFactGateLinesCertifyAtTheirLevelsForEveryDecision(t *testing.T) {
	t.Parallel()
	report := contextfabric.EmbeddedSubjectReport{
		ReferencesChecked: 7, ReferencesRefused: 6, RowsWithheld: 5, FieldsWithheld: 4, EvidenceWithheld: 3,
	}
	full := func(decision, reason string, undeclared int) contextfabric.EngineFactGateDecision {
		r := report
		r.FieldsUndeclared = undeclared
		return contextfabric.EngineFactGateDecision{
			Decision: decision, Reason: reason, FactsChecked: 11, FactsUndeclared: 2, FactsWithheldFrom: 1, Report: r,
		}
	}
	want := func(decision, reason string, undeclared int) map[string]any {
		return map[string]any{
			"org_id": "org_1", "surface": "engine_fact_read", "decision": decision, "reason": reason, "repository_scope_count": 3,
			"facts_checked": 11, "facts_undeclared": 2, "facts_withheld_from": 1, "references_checked": 7, "references_refused": 6,
			"rows_withheld": 5, "fields_withheld": 4, "evidence_withheld": 3, "fields_undeclared": undeclared,
		}
	}
	cases := []struct {
		name     string
		decision contextfabric.EngineFactGateDecision
		event    eventspec.Event
		level    string
	}{
		{"clean read", full(contextfabric.EngineFactGateClean, "", 0), eventspec.EngineFactGateInfo, "INFO"},
		{"unseen subjects withheld", full(contextfabric.EngineFactGateFiltered, contextfabric.EngineFactGateReasonUnseenSubjectsWithheld, 0), eventspec.EngineFactGateInfo, "INFO"},
		{"undeclared fields removed", full(contextfabric.EngineFactGateFiltered, contextfabric.EngineFactGateReasonUndeclaredFieldsRemoved, 1), eventspec.EngineFactGateWarn, "WARN"},
		{"withheld and undeclared", full(contextfabric.EngineFactGateFiltered, contextfabric.EngineFactGateReasonUnseenSubjectsWithheld, 4), eventspec.EngineFactGateWarn, "WARN"},
		{"failed closed", full(contextfabric.EngineFactGateUnavailable, contextfabric.EngineFactGateReasonAuthorizationFailed, 0), eventspec.EngineFactGateWarn, "WARN"},
		{"canceled", full(contextfabric.EngineFactGateUnavailable, contextfabric.EngineFactGateReasonCanceled, 0), eventspec.EngineFactGateWarn, "WARN"},
	}
	for _, tc := range cases {
		raw := recordEngineFactGate(tc.decision, 3)
		pinKeys(t, bytes.NewBuffer(raw), engineFactGateLegacyKeys, 1)
		line := certifyRequestLine(t, raw, tc.event, want(tc.decision.Decision, tc.decision.Reason, tc.decision.Report.FieldsUndeclared))
		if line["level"] != tc.level {
			t.Fatalf("%s: level = %v, want %s", tc.name, line["level"], tc.level)
		}
		other := eventspec.EngineFactGateWarn
		if tc.event.ID == eventspec.EngineFactGateWarn.ID {
			other = eventspec.EngineFactGateInfo
		}
		certifyRefused(t, raw, other, tc.name+" certified as the sibling level")
	}
	certifyRefused(t, recordEngineFactGate(full("future_decision", "", 0), 0), eventspec.EngineFactGateInfo, "an unknown decision")
	certifyRefused(t, recordEngineFactGate(full(contextfabric.EngineFactGateUnavailable, "future_reason", 0), 0), eventspec.EngineFactGateWarn, "an unknown reason")
}

func TestOrganizationSubjectReadLineCertifies(t *testing.T) {
	t.Parallel()
	investment := contextfabric.OrganizationSubjectReadEvent{
		Reason: contextfabric.OrganizationSubjectReadReasonInvestment, FactsRead: 2, FactKinds: []string{"investment", "pull_requests"},
	}
	raw := recordOrganizationSubjectRead(investment)
	pinKeys(t, bytes.NewBuffer(raw), organizationSubjectReadLegacyKeys, 1)
	certifyRequestLine(t, raw, eventspec.OrganizationSubjectRead, map[string]any{
		"org_id": "org_1", "reason": "investment", "survey_member_kind": "none", "organization_facts_read": 2,
		"organization_fact_kinds": []any{"investment", "pull_requests"},
	})

	for _, reason := range contextfabric.OrganizationSubjectReadReasonVocabulary() {
		for _, kind := range contextfabric.RepairMemberKindTokenVocabulary() {
			event := contextfabric.OrganizationSubjectReadEvent{Reason: reason, FactKinds: []string{}}
			if kind != "none" {
				event.SurveyMemberKind = contextfabric.SubjectKind(kind)
			}
			certifyRequestLine(t, recordOrganizationSubjectRead(event), eventspec.OrganizationSubjectRead, map[string]any{
				"reason": reason, "survey_member_kind": kind, "organization_fact_kinds": []any{},
			})
		}
	}
	var kinds []string
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		kinds = append(kinds, string(kind))
	}
	kindsAny := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		kindsAny = append(kindsAny, kind)
	}
	certifyRequestLine(t, recordOrganizationSubjectRead(contextfabric.OrganizationSubjectReadEvent{
		Reason: contextfabric.OrganizationSubjectReadReasonInvestment, FactsRead: len(kinds), FactKinds: kinds,
	}), eventspec.OrganizationSubjectRead, map[string]any{"organization_facts_read": len(kinds), "organization_fact_kinds": kindsAny})

	certifyRefused(t, recordOrganizationSubjectRead(contextfabric.OrganizationSubjectReadEvent{Reason: "future_reason", FactKinds: []string{}}), eventspec.OrganizationSubjectRead, "an unknown reason")
	certifyRefused(t, recordOrganizationSubjectRead(contextfabric.OrganizationSubjectReadEvent{
		Reason: contextfabric.OrganizationSubjectReadReasonInvestment, FactKinds: []string{"future_kind"},
	}), eventspec.OrganizationSubjectRead, "an unknown fact kind")
}

// TestEngineFactGateLineIsCertifiableOncePerReadAndBounded drives the line a
// request writes when it makes the main, group and period delta reads: three
// lines under one request id, each singled out by Want, the fourth refused.
func TestEngineFactGateLineIsCertifiableOncePerReadAndBounded(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: make([]string, 2)}
	reads := []contextfabric.EngineFactGateDecision{
		{Decision: contextfabric.EngineFactGateClean, FactsChecked: 5},
		{Decision: contextfabric.EngineFactGateFiltered, Reason: contextfabric.EngineFactGateReasonUnseenSubjectsWithheld, FactsChecked: 6, FactsWithheldFrom: 1},
		{Decision: contextfabric.EngineFactGateClean, FactsChecked: 7},
	}
	for _, read := range reads {
		telemetry.RecordEngineFactGate(ctx, principal, read)
	}
	for _, read := range reads {
		certifyRequestLine(t, buf.Bytes(), eventspec.EngineFactGateInfo, map[string]any{"facts_checked": read.FactsChecked, "decision": read.Decision})
	}
	certifyRefused(t, buf.Bytes(), eventspec.EngineFactGateInfo, "a Want that singles out two of the three reads", map[string]any{"decision": contextfabric.EngineFactGateClean})
	telemetry.RecordEngineFactGate(ctx, principal, contextfabric.EngineFactGateDecision{Decision: contextfabric.EngineFactGateClean, FactsChecked: 8})
	certifyRefused(t, buf.Bytes(), eventspec.EngineFactGateInfo, "a fourth read over the declared bound", map[string]any{"facts_checked": 8})
}

// TestEngineFactGateLinesOfBothLevelsInOneRequestCertifyEachAndAbsence drives a
// request whose reads wrote the Info and the Warn variant of the one gate
// message: each event certifies its own line, and absence is asserted per level.
func TestEngineFactGateLinesOfBothLevelsInOneRequestCertifyEachAndAbsence(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: make([]string, 2)}
	withheld := contextfabric.EngineFactGateDecision{
		Decision: contextfabric.EngineFactGateFiltered, Reason: contextfabric.EngineFactGateReasonUnseenSubjectsWithheld, FactsChecked: 4, FactsWithheldFrom: 1,
	}
	telemetry.RecordEngineFactGate(ctx, principal, withheld)
	infoOnly := bytes.Clone(buf.Bytes())
	withheld.FactsChecked = 5
	withheld.Report.FieldsUndeclared = 2
	telemetry.RecordEngineFactGate(ctx, principal, withheld)

	certifyRequestLine(t, buf.Bytes(), eventspec.EngineFactGateInfo, map[string]any{"facts_checked": 4, "fields_undeclared": 0})
	certifyRequestLine(t, buf.Bytes(), eventspec.EngineFactGateWarn, map[string]any{"facts_checked": 5, "fields_undeclared": 2})

	log, err := certify.Parse(infoOnly)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.EngineFactGateWarn, map[string]any{"request_id": factScopeCertifyRequestID}); err != nil {
		t.Fatalf("a request with only the Info line must certify the Warn variant absent: %v", err)
	}
	log, err = certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.EngineFactGateWarn, map[string]any{"request_id": factScopeCertifyRequestID}); err == nil {
		t.Fatalf("a request that wrote the Warn variant must not certify it absent")
	}
}

// TestFactRootRefusedReasonsAreExactlyTheDeniedSubset pins the partition of
// the stored-result reasons: the denied subset certifies, every other member
// of the closed set is refused.
func TestFactRootRefusedReasonsAreExactlyTheDeniedSubset(t *testing.T) {
	t.Parallel()
	denied := map[contextfabric.StoredResultAuthorizationReason]bool{}
	for _, reason := range contextfabric.FactRootRefusedReasonVocabulary() {
		denied[reason] = true
	}
	for _, reason := range contextfabric.StoredResultAuthorizationReasonVocabulary() {
		d := contextfabric.StoredResultAuthorization{Reason: reason, RefusedKinds: []string{}}
		if denied[reason] {
			certifyRequestLine(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, map[string]any{"reason": string(reason)})
			continue
		}
		certifyRefused(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, "reason "+string(reason)+" outside the denied subset")
	}
}

// TestAMisleveledGateLineIsNeverHiddenFromAbsenceOrCertification covers the
// two ways a sibling-level line could be mistaken for the sibling's own: a
// Warn-only decision logged at INFO is not an Info-variant line, so it does not
// certify the Warn variant absent and does not certify as the Info variant.
func TestAMisleveledGateLineIsNeverHiddenFromAbsenceOrCertification(t *testing.T) {
	t.Parallel()
	failedClosed := recordEngineFactGate(contextfabric.EngineFactGateDecision{
		Decision: contextfabric.EngineFactGateUnavailable, Reason: contextfabric.EngineFactGateReasonAuthorizationFailed,
	}, 0)
	misleveled := bytes.Replace(failedClosed, []byte(`"level":"WARN"`), []byte(`"level":"INFO"`), 1)
	if bytes.Equal(misleveled, failedClosed) {
		t.Fatalf("the fixture was not a WARN line: %s", failedClosed)
	}
	log, err := certify.Parse(misleveled)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.EngineFactGateWarn, map[string]any{"request_id": factScopeCertifyRequestID}); err == nil {
		t.Fatalf("a Warn-only decision logged at INFO certified the Warn variant absent")
	}
	certifyRefused(t, misleveled, eventspec.EngineFactGateInfo, "a Warn-only decision logged at INFO certified as the Info variant")
	certifyRefused(t, misleveled, eventspec.EngineFactGateWarn, "a Warn-only decision logged at INFO certified as the Warn variant")

	log, err = certify.Parse(failedClosed)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.EngineFactGateInfo, map[string]any{"request_id": factScopeCertifyRequestID}); err != nil {
		t.Fatalf("a request with only the Warn variant must certify the Info variant absent: %v", err)
	}
}

// TestIdenticalGateLinesOfOneRequestCertifyAsOnePayload: two reads that wrote
// the same line are one payload to certify; reads that differ in a way Want
// does not separate are refused.
func TestIdenticalGateLinesOfOneRequestCertifyAsOnePayload(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: make([]string, 2)}
	clean := contextfabric.EngineFactGateDecision{Decision: contextfabric.EngineFactGateClean, FactsChecked: 5}
	telemetry.RecordEngineFactGate(ctx, principal, clean)
	telemetry.RecordEngineFactGate(ctx, principal, clean)
	certifyRequestLine(t, buf.Bytes(), eventspec.EngineFactGateInfo, map[string]any{"facts_checked": 5})
	clean.FactsChecked = 6
	telemetry.RecordEngineFactGate(ctx, principal, clean)
	certifyRefused(t, buf.Bytes(), eventspec.EngineFactGateInfo, "a Want matching two different payloads", map[string]any{"decision": contextfabric.EngineFactGateClean})
}

// TestAWarnWorthyGateLineLoggedAtInfoIsNotAnInfoLine: the producer writes
// filtered / unseen_subjects_withheld at Warn when a field was undeclared, so
// the same payload at INFO is a level regression. It is neither certified as
// the Info variant nor hidden from the Warn variant's absence check.
func TestAWarnWorthyGateLineLoggedAtInfoIsNotAnInfoLine(t *testing.T) {
	t.Parallel()
	decision := contextfabric.EngineFactGateDecision{
		Decision: contextfabric.EngineFactGateFiltered, Reason: contextfabric.EngineFactGateReasonUnseenSubjectsWithheld,
		FactsChecked: 4, FactsWithheldFrom: 1, Report: contextfabric.EmbeddedSubjectReport{FieldsUndeclared: 1},
	}
	warn := recordEngineFactGate(decision, 2)
	certifyRequestLine(t, warn, eventspec.EngineFactGateWarn, map[string]any{"fields_undeclared": 1})
	misleveled := bytes.Replace(warn, []byte(`"level":"WARN"`), []byte(`"level":"INFO"`), 1)
	if bytes.Equal(misleveled, warn) {
		t.Fatalf("the fixture was not a WARN line: %s", warn)
	}
	certifyRefused(t, misleveled, eventspec.EngineFactGateInfo, "a Warn-worthy payload at INFO certified as the Info variant")
	log, err := certify.Parse(misleveled)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.EngineFactGateWarn, map[string]any{"request_id": factScopeCertifyRequestID}); err == nil {
		t.Fatalf("a Warn-worthy payload at INFO certified the Warn variant absent")
	}
}

// TestTheGateBoundCountsEveryLevelOfTheMessage: the per-request bound is the
// most gate lines a request writes in total, not per level.
func TestTheGateBoundCountsEveryLevelOfTheMessage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: make([]string, 2)}
	for i := 1; i <= contextfabric.EngineFactGateReadsPerRequest; i++ {
		decision := contextfabric.EngineFactGateDecision{Decision: contextfabric.EngineFactGateClean, FactsChecked: i}
		if i == 2 {
			decision = contextfabric.EngineFactGateDecision{
				Decision: contextfabric.EngineFactGateUnavailable, Reason: contextfabric.EngineFactGateReasonCanceled, FactsChecked: i,
			}
		}
		telemetry.RecordEngineFactGate(ctx, principal, decision)
	}
	certifyRequestLine(t, buf.Bytes(), eventspec.EngineFactGateInfo, map[string]any{"facts_checked": 1})
	certifyRequestLine(t, buf.Bytes(), eventspec.EngineFactGateWarn, map[string]any{"facts_checked": 2})
	telemetry.RecordEngineFactGate(ctx, principal, contextfabric.EngineFactGateDecision{
		Decision: contextfabric.EngineFactGateUnavailable, Reason: contextfabric.EngineFactGateReasonCanceled, FactsChecked: 9,
	})
	certifyRefused(t, buf.Bytes(), eventspec.EngineFactGateInfo, "a fourth line of the other level over the request bound", map[string]any{"facts_checked": 1})
	certifyRefused(t, buf.Bytes(), eventspec.EngineFactGateWarn, "a fourth line over the request bound", map[string]any{"facts_checked": 9})
}
