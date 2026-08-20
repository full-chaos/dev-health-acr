package v1

import "testing"

// TestContextFabricInvestigationRequest_DuplicateSubjectHandlesRejected is
// the codex xhigh review round-1 finding 4 regression pin: the published
// JSON Schema declares subject_handles uniqueItems, so Go's Validate must
// reject an identical duplicate too, not merely bound the list length.
func TestContextFabricInvestigationRequest_DuplicateSubjectHandlesRejected(t *testing.T) {
	t.Parallel()
	request := validContextFabricContractRequest()
	handle := ContextFabricRequestedHandle{Kind: ContextFabricSubjectPullRequest, PatternID: "pull_request_number", Value: "532"}
	request.SubjectHandles = []ContextFabricRequestedHandle{handle, handle}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error -- duplicate subject_handles entries must be rejected")
	}
}

// TestContextFabricInvestigationRequest_DistinctSubjectHandlesAccepted is
// the positive twin: two genuinely distinct handles are fine.
func TestContextFabricInvestigationRequest_DistinctSubjectHandlesAccepted(t *testing.T) {
	t.Parallel()
	request := validContextFabricContractRequest()
	request.SubjectHandles = []ContextFabricRequestedHandle{
		{Kind: ContextFabricSubjectPullRequest, PatternID: "pull_request_number", Value: "532"},
		{Kind: ContextFabricSubjectWorkItem, PatternID: "work_item_ticket_key", Value: "CHAOS-3972"},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for two distinct handles", err)
	}
}

// TestMCPInvestigateQuestionRequest_DuplicateSubjectHandlesRejected mirrors
// the hosted pin above for the MCP tool's own request boundary.
func TestMCPInvestigateQuestionRequest_DuplicateSubjectHandlesRejected(t *testing.T) {
	t.Parallel()
	handle := ContextFabricRequestedHandle{Kind: ContextFabricSubjectPullRequest, PatternID: "pull_request_number", Value: "532"}
	request := MCPInvestigateQuestionRequest{
		Question:       "What is the status of PR 532?",
		SubjectHandles: []ContextFabricRequestedHandle{handle, handle},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error -- duplicate subject_handles entries must be rejected")
	}
}
