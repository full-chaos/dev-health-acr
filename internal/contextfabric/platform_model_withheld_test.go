package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type withheldRuntime struct{ calls int }

func (r *withheldRuntime) PhraseStructureOffers(context.Context, storage.Principal, StructureOfferPhrasingInput) (StructureOfferPhrasingDraft, ModelExecutionReceipt, error) {
	r.calls++
	return StructureOfferPhrasingDraft{}, ModelExecutionReceipt{}, ErrPlatformModelWithheld
}

func TestApplyOfferPhrasing_WithheldPlatformModelKeepsStructuralLabelsAndReportsWithheld(t *testing.T) {
	t.Parallel()
	runtime := &withheldRuntime{}
	telemetry := &recordingTelemetry{}
	e := &Engine{offerPhraser: RuntimeOfferPhraser{Runtime: runtime}, telemetry: telemetry}
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{OptionID: "opt_pr", Label: "a pull request", Kind: contractsv1.ContextFabricSubjectPullRequest, OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "kindr_x"}},
	}
	got := e.applyOfferPhrasing(context.Background(), storage.Principal{OrgID: "org_1"}, "request_00000001", needs)
	if got.KindOptions[0].Phrasing != "" || got.KindOptions[0].Label != "a pull request" {
		t.Fatalf("KindOptions[0] = %+v, want the structural label and no phrasing", got.KindOptions[0])
	}
	if len(telemetry.offerPhrasingOutcomes) != 1 || telemetry.offerPhrasingOutcomes[0] != OfferPhrasingWithheld {
		t.Fatalf("telemetry.offerPhrasingOutcomes = %#v, want [%q]", telemetry.offerPhrasingOutcomes, OfferPhrasingWithheld)
	}
}

func TestPlatformModelWithheldIsUnsetOnAPlainContext(t *testing.T) {
	t.Parallel()
	if PlatformModelWithheld(context.Background()) {
		t.Fatal("a plain context reports the platform model withheld")
	}
	if !PlatformModelWithheld(WithPlatformModelWithheld(context.Background())) {
		t.Fatal("a marked context does not report the platform model withheld")
	}
}

func TestPlatformModelWithheldCarriesTheGateBasis(t *testing.T) {
	t.Parallel()
	if got := PlatformModelWithheldBasis(WithPlatformModelWithheldBasis(context.Background(), "client_model")); got != "client_model" {
		t.Fatalf("basis = %q, want client_model", got)
	}
}
