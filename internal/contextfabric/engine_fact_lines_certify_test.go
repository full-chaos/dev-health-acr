package contextfabric_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	factRootRefusedLegacyKeys = []string{
		"time", "level", "msg", "org_id", "reason", "refused_count", "denied_count", "absent_count", "refused_kinds", "request_id",
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
	for _, reason := range contextfabric.StoredResultDecideDeniedReasonVocabulary() {
		d := decision
		d.Reason = reason
		certifyRequestLine(t, recordFactRootRefused(d, 1), eventspec.FactRootRefused, map[string]any{"reason": string(reason)})
	}
	for _, reason := range []contextfabric.StoredResultAuthorizationReason{
		contextfabric.StoredResultReasonNoSubjects, contextfabric.StoredResultReasonUnrestrictedPrincipal, contextfabric.StoredResultReasonSubjectsAdmitted,
		contextfabric.StoredResultReasonAuthorizerMissing, contextfabric.StoredResultReasonGraphReadFailed,
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

// TestFactRootRefusedReasonsAreExactlyTheDeniedSubset pins the partition of
// the stored-result reasons: the denied subset certifies, every other member
// of the closed set is refused.
func TestFactRootRefusedReasonsAreExactlyTheDeniedSubset(t *testing.T) {
	t.Parallel()
	denied := map[contextfabric.StoredResultAuthorizationReason]bool{}
	for _, reason := range contextfabric.StoredResultDecideDeniedReasonVocabulary() {
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
