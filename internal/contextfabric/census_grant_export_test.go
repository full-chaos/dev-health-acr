package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrHandleRedemptionPassedForTest stops a turn past canonicalization: the
// redemption was not vetoed.
var ErrHandleRedemptionPassedForTest = errors.New("the handle redemption was not vetoed")

// RedeemHandleOfferForTest runs one real Engine turn that redeems a stored
// handle offer, through the Engine's own stored-result gate. The prior result
// is the clarification that offered it: no committed subject.
func RedeemHandleOfferForTest(t *testing.T, principal storage.Principal, option HandleOption, verifier HandleVerifier) (InvestigationResult, error) {
	t.Helper()
	option.ReceiptID, option.OptionID, option.Label, option.OfferSource = "handr_confirm0001", "opt_handle", "the named subject", contractsv1.ContextFabricStructureOfferEngine
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_structure_0003"
	prior.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	prior.EvidenceRefIDs = []string{}
	prior.StructureNeeds = &StructureNeeds{Missing: []StructureNeedKind{"subject_handle"}, HandleOptions: []HandleOption{option}}
	request := validInvestigationRequest()
	request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: option.ReceiptID}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results:        &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		HandleVerifier: verifier,
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{}, ErrHandleRedemptionPassedForTest
		}),
	})
	return engine.Investigate(context.Background(), principal, request)
}

// ReverifyRememberedHandleForTest replays a remembered handle through the
// Engine's own reverify.
func ReverifyRememberedHandleForTest(principal storage.Principal, kind SubjectKind, patternID, value string, verifier HandleVerifier) (ConfirmedNeedMemberDropReason, bool) {
	engine := &Engine{handleVerifier: verifier}
	entry := ConfirmedNeedEntry{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: kind, PatternID: patternID, AppliedValue: value}
	return engine.reverifyRememberedNeed(context.Background(), principal, InvestigationRequest{}, ResolvedGraphBinding{}, InvestigationResultSchemaV1, entry)
}
