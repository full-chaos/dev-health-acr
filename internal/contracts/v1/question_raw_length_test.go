package v1

import (
	"strings"
	"testing"
	"time"
)

// Result.Question was the one member of the padded-length class left open in
// codex round 13, because closing only the RESULT side would have let a
// padded question be accepted at the door and then fail when the engine
// echoed it into the result -- the compose-then-reject failure round 7 fixed.
//
// It is closed here on BOTH sides, so the rejection happens at the boundary
// where a client can act on it: raw length at request validation (both entry
// points) and on result writes, trimmed length on stored reads under the same
// world-(b) logic as the rest of the class -- padded questions were legally
// writable, and those rows are immutable.

// validQuestionRequest is the CANONICAL golden request with only the question
// replaced, so a rejection is attributable to the question alone and the
// fixture cannot rot as the request contract gains required fields.
func validQuestionRequest(t *testing.T, question string) ContextFabricInvestigationRequest {
	t.Helper()
	var request ContextFabricInvestigationRequest
	if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_request.v1.json"), &request); err != nil {
		t.Fatalf("decode golden request: %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("the golden request is not valid, so it cannot isolate the question: %v", err)
	}
	request.Question = question
	return request
}

// paddedQuestion is raw-oversized but trims back to exactly the maximum.
func paddedQuestion(maximum int) string {
	return " " + strings.Repeat("q", maximum) + " "
}

func TestPaddedQuestionIsRejectedAtBothRequestEntryPoints(t *testing.T) {
	t.Run("hosted investigation request", func(t *testing.T) {
		request := validQuestionRequest(t, paddedQuestion(8000))
		if err := request.Validate(); err == nil {
			t.Error("a question padded past the maximum is accepted at the hosted door, so it would fail later when the result echoed it")
		}
		request.Question = strings.TrimSpace(request.Question)
		if err := request.Validate(); err != nil {
			t.Errorf("the same question trimmed is rejected: %v", err)
		}
	})

	t.Run("mcp investigate_question", func(t *testing.T) {
		request := MCPInvestigateQuestionRequest{Question: paddedQuestion(MCPInvestigationQuestionMaxLength)}
		if err := request.Validate(); err == nil {
			t.Error("a question padded past the maximum is accepted at the MCP door")
		}
		request.Question = strings.TrimSpace(request.Question)
		if err := request.Validate(); err != nil {
			t.Errorf("the same question trimmed is rejected: %v", err)
		}
	})

	// The minimum stays on the trimmed value everywhere: whitespace-only is
	// empty in every sense.
	for _, blank := range []string{"", "   ", "\n\t "} {
		if err := validQuestionRequest(t, blank).Validate(); err == nil {
			t.Errorf("a whitespace-only question %q is accepted", blank)
		}
	}
}

func TestPaddedQuestionSplitsWriteFromStoredRead(t *testing.T) {
	result := closureResult()
	result.Question = paddedQuestion(8000)

	if err := result.Validate(); err == nil {
		t.Error("the write path accepts a result whose question exceeds the schema maximum raw")
	}
	if err := result.ValidateStored(); err != nil {
		t.Errorf("a stored result carrying a padded question is no longer readable (%v); such rows were legally writable and are immutable", err)
	}
}

// TestQuestionEchoCannotComposeThenReject is the property the ruling turned
// on: any question the request layer ACCEPTS must survive being echoed into a
// result. If the two sides ever disagree again, the engine would reject an
// answer to a question it had already admitted.
func TestQuestionEchoCannotComposeThenReject(t *testing.T) {
	for _, question := range []string{
		"a plain question",
		strings.Repeat("q", 8000),             // exactly at the bound
		" " + strings.Repeat("q", 7998) + " ", // padded but raw-legal
		"question with\ttabs and\nnewlines",
	} {
		request := validQuestionRequest(t, question)
		if err := request.Validate(); err != nil {
			continue // refused at the door: nothing to echo, which is the point
		}
		result := closureResult()
		result.Question = question
		result.GeneratedAt = time.Now().UTC()
		if err := result.Validate(); err != nil {
			t.Errorf("question %q is accepted as a REQUEST but rejected when echoed into the result (%v); the engine would fail on an answer to a question it admitted", question, err)
		}
	}
}
