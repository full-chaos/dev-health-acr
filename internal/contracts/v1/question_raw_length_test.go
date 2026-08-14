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

// TestQuestionBoundsAreCountedInRunesNotBytes closes codex round-14 F2.
//
// Every case above is ASCII, where runes and bytes coincide, so a future
// change from utf8.RuneCountInString to len() would keep them all green while
// rejecting a perfectly legal question of 8000 accented characters -- and
// would do it identically at both doors, so even the coupling assertion would
// stay green. JSON Schema maxLength counts CHARACTERS, so a byte-based check
// silently under-admits every non-ASCII caller.
//
// "é" is deliberately 2 bytes: at the bound, 8000 runes is 16000 bytes, so a
// byte-based implementation rejects it unmistakably.
func TestQuestionBoundsAreCountedInRunesNotBytes(t *testing.T) {
	const multibyte = "é"
	atMax := strings.Repeat(multibyte, 8000)
	pastMax := strings.Repeat(multibyte, 8001)

	if len(atMax) != 16000 {
		t.Fatalf("fixture is not multibyte: %d bytes for 8000 runes", len(atMax))
	}

	t.Run("hosted door admits the rune maximum", func(t *testing.T) {
		if err := validQuestionRequest(t, atMax).Validate(); err != nil {
			t.Errorf("a question of exactly 8000 multibyte runes is rejected (%v); the bound is being counted in bytes", err)
		}
		if err := validQuestionRequest(t, pastMax).Validate(); err == nil {
			t.Error("a question of 8001 multibyte runes is accepted; the bound is not enforced in runes")
		}
	})

	t.Run("mcp door admits the rune maximum", func(t *testing.T) {
		maximum := MCPInvestigationQuestionMaxLength
		if err := (MCPInvestigateQuestionRequest{Question: strings.Repeat(multibyte, maximum)}).Validate(); err != nil {
			t.Errorf("a question of exactly %d multibyte runes is rejected (%v); the bound is being counted in bytes", maximum, err)
		}
		if err := (MCPInvestigateQuestionRequest{Question: strings.Repeat(multibyte, maximum+1)}).Validate(); err == nil {
			t.Errorf("a question of %d multibyte runes is accepted; the bound is not enforced in runes", maximum+1)
		}
	})

	t.Run("echo survives the rune maximum", func(t *testing.T) {
		request := validQuestionRequest(t, atMax)
		if err := request.Validate(); err != nil {
			t.Fatalf("precondition: the multibyte question must be admitted at the door: %v", err)
		}
		result := closureResult()
		result.Question = atMax
		if err := result.Validate(); err != nil {
			t.Errorf("a multibyte question admitted at the door is rejected when echoed into the result (%v); the two sides disagree on how characters are counted", err)
		}
	})

	// The ASTRAL plane is where counting schemes disagree most (self-found
	// before the round-15 verdict). "𝄞" is 4 bytes, ONE Go rune, and
	// TWO UTF-16 code units. Go and JSON Schema both count code points, so
	// 8000 of them is exactly at the bound here -- but a validator counting
	// UTF-16 units sees 16000 and a validator counting bytes sees 32000.
	//
	// A BMP case like "e-acute" only separates bytes from code points. Only an
	// astral case also separates UTF-16 units from code points, which is the
	// divergence that actually bit the Workbench route guard (CHAOS-3803).
	t.Run("astral runes count as one code point each", func(t *testing.T) {
		const astral = "\U0001D11E"
		atBound := strings.Repeat(astral, 8000)
		if len(atBound) != 32000 {
			t.Fatalf("fixture is not astral: %d bytes for 8000 runes", len(atBound))
		}
		if err := validQuestionRequest(t, atBound).Validate(); err != nil {
			t.Errorf("a question of exactly 8000 astral runes is rejected (%v); the bound is not being counted in code points", err)
		}
		if err := validQuestionRequest(t, strings.Repeat(astral, 8001)).Validate(); err == nil {
			t.Error("a question of 8001 astral runes is accepted; the bound is not enforced in code points")
		}
		result := closureResult()
		result.Question = atBound
		if err := result.Validate(); err != nil {
			t.Errorf("an astral question admitted at the door is rejected when echoed into the result (%v)", err)
		}
	})

	t.Run("padding is still rejected in runes", func(t *testing.T) {
		padded := " " + atMax + " "
		if err := validQuestionRequest(t, padded).Validate(); err == nil {
			t.Error("a padded multibyte question at the rune maximum is accepted; padding must be measured raw for multibyte text too")
		}
	})
}
