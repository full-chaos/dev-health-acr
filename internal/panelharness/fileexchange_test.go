package panelharness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func sampleStructureNeeds() contractsv1.ContextFabricStructureNeeds {
	return contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{
			{ReceiptID: "kindr_aaaaaaaaaaaaaaaaaaaaaaaa", OptionID: "opt1", Label: "Pull request", Kind: "pull_request"},
			{ReceiptID: "kindr_bbbbbbbbbbbbbbbbbbbbbbbb", OptionID: "opt2", Label: "Work item", Kind: "work_item"},
		},
	}
}

// runFileExchangeResponder simulates an out-of-process responder: it
// watches dir/requests for exactly one new file and writes a well-formed
// response, proving this package's own polling/nonce/atomic-publish
// mechanics interoperate with an independent writer -- not just with
// itself.
//
// Runs in its own goroutine (started with `go`), so it must never call
// t.Fatal/t.Fatalf itself (the testing package forbids that from a
// non-test goroutine -- go vet flags it); it reports failure by simply
// doing nothing, which the caller's own SelectReceipts call already
// surfaces as a deadline-exceeded error the test asserts on.
func runFileExchangeResponder(dir string, respond func(request exchangeRequest) exchangeResponse) {
	requestsDir := filepath.Join(dir, "requests")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(requestsDir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			// Skip directories and dot-prefixed temp files: the real
			// requester publishes via temp-file-then-rename (fileexchange.go's
			// own exchange method), so a hidden ".<name>.tmp*" file can be
			// transiently visible here before the atomic rename completes.
			// A responder that picked it up would compute the right
			// answer but write it under the WRONG (temp) filename, which
			// the requester's own poll loop -- watching only the final
			// renamed path -- would never see, and the test would hang
			// until its own timeout. Real out-of-process responder
			// scripts must apply the identical filter.
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(requestsDir, entry.Name()))
			if err != nil {
				continue // may still be mid-write; retry
			}
			var request exchangeRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				continue
			}
			response := respond(request)
			encoded, err := json.Marshal(response)
			if err != nil {
				return
			}
			_ = os.WriteFile(filepath.Join(dir, "responses", entry.Name()), encoded, 0o644)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFileExchangeSelector_RoundTripsAGenuineSelection(t *testing.T) {
	dir := t.TempDir()
	selector, err := NewFileExchangeSelector(dir, "anthropic/sol-max", 5*time.Second)
	if err != nil {
		t.Fatalf("NewFileExchangeSelector: %v", err)
	}

	go runFileExchangeResponder(dir, func(request exchangeRequest) exchangeResponse {
		output, _ := json.Marshal(structureSelectionOutput{
			Selections: []struct {
				Member            string `json:"member"`
				SelectedReceiptID string `json:"selected_receipt_id"`
			}{{Member: "expected_kind", SelectedReceiptID: "kindr_aaaaaaaaaaaaaaaaaaaaaaaa"}},
		})
		return exchangeResponse{SessionNonce: request.SessionNonce, Output: output}
	})

	selections, err := selector.SelectReceipts(context.Background(), "Was Ask Dev ready to ship?", sampleStructureNeeds())
	if err != nil {
		t.Fatalf("SelectReceipts: %v", err)
	}
	if got, want := selections["expected_kind"], "kindr_aaaaaaaaaaaaaaaaaaaaaaaa"; got != want {
		t.Errorf("selections[expected_kind] = %q, want %q", got, want)
	}
}

func TestFileExchangeSelector_OmittedMemberMeansNoConfidentOffer(t *testing.T) {
	dir := t.TempDir()
	selector, err := NewFileExchangeSelector(dir, "anthropic/sol-max", 5*time.Second)
	if err != nil {
		t.Fatalf("NewFileExchangeSelector: %v", err)
	}

	go runFileExchangeResponder(dir, func(request exchangeRequest) exchangeResponse {
		output, _ := json.Marshal(structureSelectionOutput{}) // no selections: not confident in any offer
		return exchangeResponse{SessionNonce: request.SessionNonce, Output: output}
	})

	selections, err := selector.SelectReceipts(context.Background(), "Was Ask Dev ready to ship?", sampleStructureNeeds())
	if err != nil {
		t.Fatalf("SelectReceipts: %v", err)
	}
	if len(selections) != 0 {
		t.Errorf("selections = %v, want empty (panelist confident in nothing)", selections)
	}
}

func TestFileExchangeSelector_RejectsStaleSessionNonce(t *testing.T) {
	dir := t.TempDir()
	selector, err := NewFileExchangeSelector(dir, "anthropic/sol-max", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("NewFileExchangeSelector: %v", err)
	}

	go runFileExchangeResponder(dir, func(request exchangeRequest) exchangeResponse {
		return exchangeResponse{SessionNonce: "a-different-runs-nonce", Output: json.RawMessage(`{"selections":[]}`)}
	})

	_, err = selector.SelectReceipts(context.Background(), "Was Ask Dev ready to ship?", sampleStructureNeeds())
	if err == nil {
		t.Fatal("expected a stale-nonce response to be rejected (treated as not-ready, then time out), got nil error")
	}
}

func TestFileExchangeSelector_SurfacesResponderReportedErrorWithoutLeakingItsText(t *testing.T) {
	dir := t.TempDir()
	selector, err := NewFileExchangeSelector(dir, "anthropic/sol-max", 5*time.Second)
	if err != nil {
		t.Fatalf("NewFileExchangeSelector: %v", err)
	}

	const secretResponderText = "internal responder stack trace with sensitive detail"
	go runFileExchangeResponder(dir, func(request exchangeRequest) exchangeResponse {
		return exchangeResponse{SessionNonce: request.SessionNonce, Error: secretResponderText}
	})

	_, err = selector.SelectReceipts(context.Background(), "Was Ask Dev ready to ship?", sampleStructureNeeds())
	if err == nil {
		t.Fatal("expected an error when the responder reports one")
	}
	if got := err.Error(); got == secretResponderText {
		t.Errorf("responder error text leaked verbatim into the returned error: %q", got)
	}
	if strings.Contains(err.Error(), secretResponderText) {
		t.Errorf("returned error %q must never contain the responder's own text %q", err.Error(), secretResponderText)
	}
}
