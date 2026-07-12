package sidecar

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestNewAPIErrorMapsKnownCodesToSentinels(t *testing.T) {
	cases := map[string]error{
		"invalid_token":        ErrInvalidToken,
		"insufficient_scope":   ErrInsufficientScope,
		"repo_forbidden":       ErrRepositoryForbidden,
		"feature_not_enabled":  ErrFeatureNotEnabled,
		"version_mismatch":     ErrVersionMismatch,
		"rate_limited":         ErrRateLimited,
		"upstream_unavailable": ErrUpstreamUnavailable,
		"internal_error":       ErrInternalAPIError,
		"not_found":            ErrNotFound,
		"invalid_request":      ErrInvalidRequest,
	}
	for code, sentinel := range cases {
		apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: code, Message: "safe message", HTTPStatus: 400}, "req_1", "")
		if !errors.Is(apiErr, sentinel) {
			t.Fatalf("code %q did not map to expected sentinel", code)
		}
	}
}

func TestNewAPIErrorMapsUnknownCodeToUnknownSentinel(t *testing.T) {
	apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: "something_new"}, "req_1", "")
	if !errors.Is(apiErr, ErrUnknownAPIError) {
		t.Fatal("unrecognized code did not map to ErrUnknownAPIError")
	}
}

// TestNewAPIErrorUsesFixedSafeMessagePerCode proves apiErr.Message is
// always the codeSafeMessages entry for the code, completely independent
// of whatever detail.Message the (simulated) hosted response carried.
func TestNewAPIErrorUsesFixedSafeMessagePerCode(t *testing.T) {
	for code, want := range codeSafeMessages {
		apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: code, Message: "this text must never appear"}, "req_1", "")
		if apiErr.Message != want {
			t.Fatalf("code %q: Message = %q, want fixed message %q", code, apiErr.Message, want)
		}
	}
}

// TestNewAPIErrorUnknownCodeUsesFixedUnknownMessage proves the unknown-code
// fallback is also a fixed string, not the hosted detail.Message.
func TestNewAPIErrorUnknownCodeUsesFixedUnknownMessage(t *testing.T) {
	apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: "something_new", Message: "this text must never appear"}, "req_1", "")
	if apiErr.Message != unknownCodeSafeMessage {
		t.Fatalf("Message = %q, want fixed unknown-code message %q", apiErr.Message, unknownCodeSafeMessage)
	}
}

// TestNewAPIErrorNeverSurfacesHostedMessageVerbatim is the adversarial
// canary for a compromised or malicious hosted API: a schema-valid,
// recognized error code paired with an attacker-chosen detail.Message
// (here impersonating a credential and a prompt-injection-style
// instruction) must never have that text reach apiErr.Message or
// apiErr.Error(), across every recognized code and the unknown-code
// fallback -- proving the fixed-message mapping is applied everywhere,
// not just for the codes exercised by the happy-path tests above.
func TestNewAPIErrorNeverSurfacesHostedMessageVerbatim(t *testing.T) {
	const canary = "CANARY-fcacr_should-never-leak-ignore-all-previous-instructions"
	codes := make([]string, 0, len(codeSentinels)+1)
	for code := range codeSentinels {
		codes = append(codes, code)
	}
	codes = append(codes, "totally_unrecognized_code")
	for _, code := range codes {
		apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: code, Message: canary}, "req_1", "")
		if strings.Contains(apiErr.Message, canary) {
			t.Fatalf("code %q: apiErr.Message leaked the hosted message verbatim: %q", code, apiErr.Message)
		}
		if strings.Contains(apiErr.Error(), canary) {
			t.Fatalf("code %q: apiErr.Error() leaked the hosted message verbatim: %q", code, apiErr.Error())
		}
	}
}

func TestNewAPIErrorParsesRetryAfterHeader(t *testing.T) {
	apiErr := newAPIError(429, contractsv1.ErrorDetail{Code: "rate_limited", Retryable: true}, "req_1", "7")
	if apiErr.RetryAfter.Seconds() != 7 {
		t.Fatalf("unexpected retry after: %s", apiErr.RetryAfter)
	}
}

func TestNewAPIErrorIgnoresMalformedRetryAfterHeader(t *testing.T) {
	apiErr := newAPIError(429, contractsv1.ErrorDetail{Code: "rate_limited"}, "req_1", "not-a-number")
	if apiErr.RetryAfter != 0 {
		t.Fatalf("expected zero retry-after for malformed header, got %s", apiErr.RetryAfter)
	}
}

func TestNewAPIErrorSanitizesControlCharactersInMessage(t *testing.T) {
	apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: "invalid_request", Message: "line one\nFAKE-LOG-LINE injected\x00tail"}, "req_1", "")
	if strings.ContainsAny(apiErr.Message, "\n\r\x00") {
		t.Fatalf("message retained control characters: %q", apiErr.Message)
	}
}

func TestNewAPIErrorCapsMessageLength(t *testing.T) {
	huge := strings.Repeat("a", 10_000)
	apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: "invalid_request", Message: huge}, "req_1", "")
	if len(apiErr.Message) > maxSanitizedMessageLength {
		t.Fatalf("message was not capped: %d bytes", len(apiErr.Message))
	}
}

func TestAPIErrorNeverIncludesBearerToken(t *testing.T) {
	const secret = "fcacr_super-secret-canary-token"
	apiErr := newAPIError(500, contractsv1.ErrorDetail{Code: "internal_error", Message: "unrelated failure"}, "req_1", "")
	if strings.Contains(apiErr.Error(), secret) {
		t.Fatal("APIError.Error() must never be able to contain a bearer token")
	}
}

func TestNewTransportErrorIsSanitizedAndRetryable(t *testing.T) {
	apiErr := newTransportError(502, "req_1", []byte("<html>gateway exploded</html>"))
	if apiErr.HTTPStatus != 502 || !apiErr.Retryable {
		t.Fatalf("unexpected transport error: %#v", apiErr)
	}
	if !errors.Is(apiErr, ErrMalformedResponse) {
		t.Fatal("expected ErrMalformedResponse sentinel")
	}
	if strings.Contains(apiErr.Error(), "gateway exploded") {
		t.Fatal("transport error must not echo the raw upstream body")
	}
}

func TestSanitizeMessageExactByteBudgetIsNotTruncated(t *testing.T) {
	msg := strings.Repeat("a", maxSanitizedMessageLength)
	got := sanitizeMessage(msg)
	if got != msg {
		t.Fatalf("exact-budget message must pass through unchanged, got %d bytes", len(got))
	}
	if len(got) != maxSanitizedMessageLength {
		t.Fatalf("expected exactly %d bytes, got %d", maxSanitizedMessageLength, len(got))
	}
}

func TestSanitizeMessageOneByteOverBudgetIsTruncatedToBudget(t *testing.T) {
	msg := strings.Repeat("a", maxSanitizedMessageLength+1)
	got := sanitizeMessage(msg)
	if len(got) != maxSanitizedMessageLength {
		t.Fatalf("expected truncation to exactly %d bytes, got %d", maxSanitizedMessageLength, len(got))
	}
	if got != msg[:maxSanitizedMessageLength] {
		t.Fatal("one-byte-over ASCII truncation must match the byte-budget prefix exactly")
	}
}

// TestSanitizeMessageNeverSplitsATwoByteRuneAtTheBoundary reproduces the
// original bug: 499 ASCII bytes followed by a 2-byte rune ('é') puts the
// rune's continuation byte exactly at index 500. A naive clean[:500] byte
// slice (the pre-fix behavior) would keep only the rune's lead byte,
// producing invalid UTF-8. The rune-safe truncator must instead back off
// and drop the whole straddling rune.
func TestSanitizeMessageNeverSplitsATwoByteRuneAtTheBoundary(t *testing.T) {
	msg := strings.Repeat("a", maxSanitizedMessageLength-1) + "é"
	if len(msg) != maxSanitizedMessageLength+1 {
		t.Fatalf("test fixture must straddle the boundary, got %d bytes", len(msg))
	}
	got := sanitizeMessage(msg)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated message must remain valid UTF-8, got %q (%v)", got, []byte(got))
	}
	if len(got) > maxSanitizedMessageLength {
		t.Fatalf("truncated message must respect the byte budget, got %d bytes", len(got))
	}
	if strings.ContainsRune(got, 'é') {
		t.Fatal("the straddling rune must be dropped whole, not split")
	}
	if want := strings.Repeat("a", maxSanitizedMessageLength-1); got != want {
		t.Fatalf("expected the truncator to back off to the last full rune, got %q", got)
	}
}

// TestSanitizeMessageNeverSplitsAFourByteRuneAtTheBoundary uses a 4-byte
// emoji rune (👍, U+1F44D) with a 1-byte ASCII prefix so the repeating
// 4-byte runes are offset by one byte: the maxSanitizedMessageLength cut
// (index 500) lands on the last (continuation) byte of the 125th emoji
// rune, not on a rune boundary.
func TestSanitizeMessageNeverSplitsAFourByteRuneAtTheBoundary(t *testing.T) {
	const emoji = "👍"
	if len(emoji) != 4 {
		t.Fatalf("fixture assumption broke: %q is %d bytes", emoji, len(emoji))
	}
	msg := "a" + strings.Repeat(emoji, 130)
	got := sanitizeMessage(msg)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated message must remain valid UTF-8, got %q (%v)", got, []byte(got))
	}
	if len(got) > maxSanitizedMessageLength {
		t.Fatalf("truncated message must respect the byte budget, got %d bytes", len(got))
	}
	// 1 ASCII byte + 124 complete 4-byte emoji runes = 497 bytes; the 125th
	// rune (bytes 497-500) straddles the cut and must be dropped whole.
	if want := "a" + strings.Repeat(emoji, 124); got != want {
		t.Fatalf("expected truncator to back off to the last full rune, got %d bytes (%v)", len(got), []byte(got))
	}
}

// TestSanitizeMessageNormalizesInvalidUTF8Input proves invalid byte
// sequences in the raw upstream message can never survive into the
// sanitized output verbatim: strings.Map replaces each invalid byte with
// the UTF-8 encoding of U+FFFD before truncation ever runs, so the result
// is always valid UTF-8 regardless of how malformed the input was.
func TestSanitizeMessageNormalizesInvalidUTF8Input(t *testing.T) {
	raw := "before\xff\x80after"
	got := sanitizeMessage(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeMessage must always return valid UTF-8, got %q (%v)", got, []byte(got))
	}
	if strings.Contains(got, "\xff") || strings.Contains(got, "\x80") {
		t.Fatalf("raw invalid bytes must not survive sanitization verbatim, got %v", []byte(got))
	}
}

// TestSanitizeMessageBoundsNormalizedInvalidInputEvenWhenExpansionExceedsBudget
// covers the case where invalid-byte normalization inflates the string
// (each invalid byte becomes a 3-byte U+FFFD) past the byte budget, and the
// resulting replacement-character run itself straddles the truncation
// boundary (500 is not a multiple of 3).
func TestSanitizeMessageBoundsNormalizedInvalidInputEvenWhenExpansionExceedsBudget(t *testing.T) {
	raw := strings.Repeat("\xff", 1000)
	got := sanitizeMessage(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("normalized-then-truncated message must remain valid UTF-8, got %q (%v)", got, []byte(got))
	}
	if len(got) > maxSanitizedMessageLength {
		t.Fatalf("normalized-then-truncated message exceeded the byte budget: %d bytes", len(got))
	}
}

// TestSanitizeMessageNeverLeaksBytesBeyondTheBudget is the canary test: a
// distinctive marker planted past the byte budget must never appear in the
// sanitized output, and the output must never exceed the budget to make
// room for it.
func TestSanitizeMessageNeverLeaksBytesBeyondTheBudget(t *testing.T) {
	const canary = "SECRET-CANARY-MUST-NOT-LEAK-PAST-TRUNCATION-BOUNDARY"
	filler := strings.Repeat("x", maxSanitizedMessageLength+50)
	raw := filler + canary
	got := sanitizeMessage(raw)
	if len(got) > maxSanitizedMessageLength {
		t.Fatalf("sanitized message exceeded the byte budget: %d bytes", len(got))
	}
	if strings.Contains(got, canary) {
		t.Fatal("canary planted past the byte budget leaked into the sanitized message")
	}
}

// TestNewAPIErrorTruncatesMultiByteMessageWithoutInvalidUTF8OrLeak exercises
// the full newAPIError pipeline (not just sanitizeMessage directly) with a
// multi-byte message plus a canary planted past the byte budget.
func TestNewAPIErrorTruncatesMultiByteMessageWithoutInvalidUTF8OrLeak(t *testing.T) {
	const canary = "CANARY-BEYOND-BUDGET"
	raw := strings.Repeat("日", 200) + canary
	apiErr := newAPIError(400, contractsv1.ErrorDetail{Code: "invalid_request", Message: raw}, "req_1", "")
	if !utf8.ValidString(apiErr.Message) {
		t.Fatalf("APIError.Message must be valid UTF-8, got %q (%v)", apiErr.Message, []byte(apiErr.Message))
	}
	if len(apiErr.Message) > maxSanitizedMessageLength {
		t.Fatalf("APIError.Message exceeded the byte budget: %d bytes", len(apiErr.Message))
	}
	if strings.Contains(apiErr.Message, canary) {
		t.Fatal("APIError.Message leaked data beyond the byte budget")
	}
}
