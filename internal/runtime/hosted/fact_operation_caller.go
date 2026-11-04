package hosted

import (
	"context"
	"encoding/json"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// factOperationCaller lets a fact provider read a registered ops operation
// through the same directread.OperationRunner the run_operation tool uses (one client,
// one catalogue, one gate). It sends the caller's variables, held to the
// operation's policy like any run_operation call (CHAOS-12554); the principal
// org fills orgId from the operation policy.
type factOperationCaller struct{ runner *directread.OperationRunner }

// newFactOperationCaller wraps the runner; a nil runner yields nil.
func newFactOperationCaller(runner *directread.OperationRunner) *factOperationCaller {
	if runner == nil {
		return nil
	}
	return &factOperationCaller{runner: runner}
}

// CallOperation runs the operation and reduces the answer to the closed
// devhealthfacts outcome: served, or the call status (and refusal code).
// Only a declared complete answer is complete: unknown completeness is not.
func (c *factOperationCaller) CallOperation(ctx context.Context, principal storage.Principal, operation string, variables json.RawMessage) (devhealthfacts.OperationOutcome, error) {
	resp, err := c.runner.Run(ctx, principal, directread.OperationRequest{Operation: operation, Variables: variables})
	if err != nil {
		return devhealthfacts.OperationOutcome{}, err
	}
	if resp.Call != directread.CallServed {
		reason := string(resp.Call)
		if resp.Refusal != nil {
			reason += ":" + string(resp.Refusal.Code)
		}
		return devhealthfacts.OperationOutcome{Reason: reason}, nil
	}
	return devhealthfacts.OperationOutcome{Served: true, Complete: resp.Completeness == directread.CompletenessDeclaredComplete, Data: resp.Data}, nil
}
