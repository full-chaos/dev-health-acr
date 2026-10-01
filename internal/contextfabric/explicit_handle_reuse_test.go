package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestExplicitHandleBypassesAnswerReuse(t *testing.T) {
	t.Parallel()
	request := InvestigationRequest{}
	if got := reuseBypassReason(request, requestStructureCanonicalization{}); got != "" {
		t.Fatalf("no handle: bypass = %q, want none", got)
	}
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "747"}}
	if got := reuseBypassReason(request, requestStructureCanonicalization{}); got != AnswerReuseBypassExplicitHandle {
		t.Fatalf("explicit handle: bypass = %q, want %q", got, AnswerReuseBypassExplicitHandle)
	}
}
