package contextfabric_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The line's keys as main wrote them, with the per-sample indexed keys
// replaced by the one bounded list and its omitted count.
var questionFamilyLineKeys = []string{
	"time", "level", "msg", "org_id", "family", "source", "ensemble_size", "downgraded_count", "consensus_field_divergence",
	"family_version", "sample_families", "samples", "samples_omitted",
	"shadow_frame_observed", "shadow_frame_outcome", "shadow_projection_version", "shadow_projected_family", "shadow_projected_row",
	"shadow_precedence_family", "shadow_precedence_row", "shadow_agreement_class", "shadow_agreed",
	"family_source", "route_class", "route_disposition", "route_switched", "request_id",
}

func recordQuestionFamilyLine(event contextfabric.QuestionFamilyResolutionEvent) []byte {
	var buf bytes.Buffer
	telemetry, ctx := cohortTelemetry(&buf)
	telemetry.RecordQuestionFamilyResolution(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

func familySampleRow(family contextfabric.QuestionFamily) contextfabric.QuestionFamilySampleRow {
	return contextfabric.QuestionFamilySampleRow{
		Shape: contractsv1.ContextFabricShapeOpen, AttemptedFamily: family, ResolvedFamily: family,
		Row: contextfabric.FamilyPrecedenceRowGroupKind, Reason: contextfabric.FamilyIncompatibilityUnreachable,
		GroupKindSet: true, ScopeAnchorSet: true,
	}
}

func TestQuestionFamilyLineCertifiesEveryProducerValue(t *testing.T) {
	t.Parallel()
	families := contextfabric.QuestionFamilyVocabulary()
	base := contextfabric.QuestionFamilyResolutionEvent{
		Family: contextfabric.QuestionFamilyGroupedCohortStatus, Source: contextfabric.QuestionFamilySourceModelConsensus,
		EnsembleSize: 3, DowngradedCount: 1, ConsensusFieldDivergence: 2, FamilyVersion: contextfabric.QuestionFamilyTableVersion,
		SampleFamilies: map[contextfabric.QuestionFamily]int{contextfabric.QuestionFamilyGroupedCohortStatus: 2, contextfabric.QuestionFamilyTrend: 1},
		Samples:        []contextfabric.QuestionFamilySampleRow{familySampleRow(contextfabric.QuestionFamilyGroupedCohortStatus), familySampleRow(contextfabric.QuestionFamilyTrend)},
	}
	raw := recordQuestionFamilyLine(base)
	pinKeys(t, bytes.NewBuffer(raw), questionFamilyLineKeys, 1)
	line := certifyRequestLine(t, raw, eventspec.QuestionFamilyResolution, map[string]any{
		"org_id": "org_1", "family": string(contextfabric.QuestionFamilyGroupedCohortStatus), "source": string(contextfabric.QuestionFamilySourceModelConsensus),
		"ensemble_size": 3, "downgraded_count": 1, "consensus_field_divergence": 2, "family_version": contextfabric.QuestionFamilyTableVersion,
		"sample_families": map[string]any{string(contextfabric.QuestionFamilyGroupedCohortStatus): 2, string(contextfabric.QuestionFamilyTrend): 1},
		"samples_omitted": 0,
	})
	if rows, _ := line["samples"].([]any); len(rows) != 2 {
		t.Fatalf("samples = %#v, want the two rows in order", line["samples"])
	}

	// Every family, as the served, attempted, resolved, shadow and precedence family.
	for _, family := range families {
		event := base
		event.Family = family
		event.Samples = []contextfabric.QuestionFamilySampleRow{familySampleRow(family)}
		event.SampleFamilies = map[contextfabric.QuestionFamily]int{family: 1}
		event.Shadow.Agreement.ProjectedFamily, event.Shadow.Agreement.PrecedenceFamily = family, family
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"family": string(family), "shadow_projected_family": string(family)})
	}
	// Every precedence row, incompatibility reason and shape on the sample rows.
	for _, row := range contextfabric.FamilyPrecedenceRowVocabulary() {
		event := base
		sample := familySampleRow(contextfabric.QuestionFamilyTrend)
		sample.Row = contextfabric.FamilyPrecedenceRow(row)
		event.Samples = []contextfabric.QuestionFamilySampleRow{sample}
		event.Shadow.Agreement.PrecedenceRow = contextfabric.FamilyPrecedenceRow(row)
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"shadow_precedence_row": row})
	}
	for _, reason := range contextfabric.FamilyIncompatibilityReasonVocabulary() {
		event := base
		sample := familySampleRow(contextfabric.QuestionFamilyTrend)
		sample.Reason = contextfabric.FamilyIncompatibilityReason(reason)
		event.Samples = []contextfabric.QuestionFamilySampleRow{sample}
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, nil)
	}
	for _, shape := range contractsv1.ContextFabricInvestigationShapeVocabulary() {
		event := base
		sample := familySampleRow(contextfabric.QuestionFamilyTrend)
		sample.Shape = shape
		event.Samples = []contextfabric.QuestionFamilySampleRow{sample}
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, nil)
	}
	// Every source, projection row, agreement class, route source and disposition, and frame outcome.
	for _, source := range contractsv1.ContextFabricQuestionFamilySourceVocabulary() {
		event := base
		event.Source = source
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"source": string(source)})
	}
	for _, row := range contextfabric.FamilyProjectionRowVocabulary() {
		event := base
		event.Shadow.Agreement.ProjectedRow = row
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"shadow_projected_row": string(row)})
	}
	for _, class := range contextfabric.FamilyAgreementClassVocabulary() {
		event := base
		event.Shadow.Agreement.Class = class
		event.Route.Class = class
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"shadow_agreement_class": string(class), "route_class": string(class)})
	}
	for _, source := range contextfabric.FamilyRouteSourceVocabulary() {
		event := base
		event.Route.Source = source
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"family_source": string(source)})
	}
	for _, disposition := range contextfabric.FamilyRouteDispositionVocabulary() {
		event := base
		event.Route.Disposition = disposition
		event.Route.Switched = true
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"route_disposition": string(disposition), "route_switched": true})
	}
	for _, outcome := range contextfabric.FrameValidationOutcomeVocabulary() {
		event := base
		event.Shadow.FrameObserved, event.Shadow.FrameOutcome, event.Shadow.ProjectionVersion = true, outcome, contextfabric.QuestionFrameVersion
		certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{
			"shadow_frame_observed": true, "shadow_frame_outcome": string(outcome), "shadow_projection_version": contextfabric.QuestionFrameVersion,
		})
	}

	// Unknown values are refused.
	unknown := base
	unknown.Family = contextfabric.QuestionFamily("future_family")
	certifyRefused(t, recordQuestionFamilyLine(unknown), eventspec.QuestionFamilyResolution, "an unknown family")
	unknown = base
	unknown.Samples = []contextfabric.QuestionFamilySampleRow{familySampleRow(contextfabric.QuestionFamily("future_family"))}
	certifyRefused(t, recordQuestionFamilyLine(unknown), eventspec.QuestionFamilyResolution, "an unknown sample family")
}

// TestQuestionFamilySamplesPastTheEnsembleBoundAreCutAndCounted plants an
// eighth sample: the list carries the first seven, in order, and the cut is
// counted in samples_omitted, never dropped silently.
func TestQuestionFamilySamplesPastTheEnsembleBoundAreCutAndCounted(t *testing.T) {
	t.Parallel()
	families := contextfabric.QuestionFamilyVocabulary()
	event := contextfabric.QuestionFamilyResolutionEvent{
		Family: contextfabric.QuestionFamilyTrend, Source: contextfabric.QuestionFamilySourceModelConsensus, FamilyVersion: contextfabric.QuestionFamilyTableVersion,
		SampleFamilies: map[contextfabric.QuestionFamily]int{},
	}
	for i := 0; i < contextfabric.QuestionFamilyEnsembleMax+1; i++ {
		event.Samples = append(event.Samples, familySampleRow(families[i%len(families)]))
	}
	raw := recordQuestionFamilyLine(event)
	line := certifyRequestLine(t, raw, eventspec.QuestionFamilyResolution, map[string]any{"samples_omitted": 1})
	rows, _ := line["samples"].([]any)
	if len(rows) != contextfabric.QuestionFamilyEnsembleMax {
		t.Fatalf("samples carries %d rows, want the bound %d", len(rows), contextfabric.QuestionFamilyEnsembleMax)
	}
	for i, row := range rows {
		got, _ := row.(map[string]any)
		if got["attempted_family"] != string(families[i%len(families)]) {
			t.Fatalf("row %d is %v, the cut must keep the first %d samples in order", i, got["attempted_family"], contextfabric.QuestionFamilyEnsembleMax)
		}
	}
	// Exactly at the bound: nothing omitted.
	event.Samples = event.Samples[:contextfabric.QuestionFamilyEnsembleMax]
	certifyRequestLine(t, recordQuestionFamilyLine(event), eventspec.QuestionFamilyResolution, map[string]any{"samples_omitted": 0})
	// A line over the declared bound (an eighth row planted in the JSON) is refused.
	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	planted, _ := decoded["samples"].([]any)
	decoded["samples"] = append(planted, planted[0])
	over, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	certifyRefused(t, over, eventspec.QuestionFamilyResolution, "a list longer than the ensemble bound")
}
