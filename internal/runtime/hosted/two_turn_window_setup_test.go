package hosted_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type unstatedWindowOnlyInvestigator struct {
	mu       sync.Mutex
	requests []contractsv1.ContextFabricInvestigationRequest
}

func (f *unstatedWindowOnlyInvestigator) Investigate(_ context.Context, _ storage.Principal, req contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	result := contractsv1.ContextFabricInvestigationResult{ResultID: "result_" + req.RequestID}
	if req.TimeContext.EvidenceWindow != nil {
		return result, nil
	}
	options := []contractsv1.ContextFabricWindowOption{
		{RelativeID: contractsv1.ContextFabricRelativeWindowID("all_time"), ReceiptID: "r_all_time"},
		{RelativeID: contractsv1.ContextFabricRelativeWindowID("trailing_90d"), ReceiptID: "r_90d"},
	}
	result.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: options}
	result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{WindowOptions: options}
	return result, nil
}

func TestTwoTurnWindowConfirmedWrongSetupSendsNoExplicitWindow(t *testing.T) {
	fake := &unstatedWindowOnlyInvestigator{}
	entry := twoTurnOracleEntry{Index: 20, Member: string(contractsv1.ContextFabricStructureNeedWindow), PositiveWindowBand: "all_time", NegativeWindowBand: "trailing_90d", NegativeCommittable: true}
	res := runTwoTurnConfirmedWrongArm(t, context.Background(), fake, nil, storage.Principal{}, 20, trialCase{Question: "q"}, entry, time.Second, anchorTermIndex{}, "tok", nil)
	if res.ArmInvalidReason != "" {
		t.Fatalf("window confirmed_wrong setup could not obtain the negative window offer: %q", res.ArmInvalidReason)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("calls = %d, want setup + redeem", len(fake.requests))
	}
	if got := fake.requests[1].PriorWindowReceipts; len(got) != 1 || got[0].ReceiptID != "r_90d" {
		t.Fatalf("redeem receipts = %+v, want the trailing_90d receipt", got)
	}
}

func TestTwoTurnInferredTierWindowPreconditionSendsNoExplicitWindow(t *testing.T) {
	fake := &unstatedWindowOnlyInvestigator{}
	entry := twoTurnOracleEntry{Index: 20, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "repository", NegativeKind: "work_item"}
	res := runTwoTurnInferredTierArm(t, context.Background(), fake, storage.Principal{}, 20, trialCase{Question: "q"}, entry, time.Second, &twoTurnTraceCapture{}, "all_time", "inferred")
	if strings.Contains(res.ArmInvalidReason, "window precondition setup turn did not offer") {
		t.Fatalf("window precondition setup could not obtain the window offer: %q", res.ArmInvalidReason)
	}
}
