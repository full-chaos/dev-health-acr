package panelharness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// maxExchangeResponseBytes bounds how much of a single response file this
// package will read (codex adversarial review, round 1, MEDIUM): a
// writable responses/ directory could otherwise let an oversized or
// symlinked file force unbounded memory allocation via a plain
// os.ReadFile. A genuine structure_selection response is a handful of
// short JSON fields; 1 MiB is generous headroom above any real one.
const maxExchangeResponseBytes = 1 << 20

// readBoundedResponseFile reads respPath through openNoFollowNonBlocking
// (fileexchange_open_unix.go / fileexchange_open_unsupported.go) and a
// size-limited reader, preserving os.Open's os.IsNotExist-compatible error
// for a not-yet-published response (the exchange poll loop's own "not
// ready yet" branch depends on that).
//
// codex adversarial review, round 2: a plain os.Open/os.ReadFile (round 1's
// own fix) still (a) followed a symlink planted at respPath after this
// package's own directory-level ownership check ran once at construction
// time (a TOCTOU window -- MEDIUM), and (b) could block INSIDE os.Open
// itself, forever, if respPath had been replaced with a FIFO -- before
// this function's size bound or the caller's own context/deadline ever got
// a chance to apply (MEDIUM). openNoFollowNonBlocking's O_NOFOLLOW makes
// the kernel refuse a symlink atomically with the open; O_NONBLOCK makes a
// FIFO open return immediately instead of blocking, so the fstat regular-
// file check just below rejects it as a normal, fast error instead of a
// hang.
func readBoundedResponseFile(respPath string) ([]byte, error) {
	file, err := openNoFollowNonBlocking(respPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("panelharness: response path %s is not a regular file (%s)", respPath, info.Mode().Type())
	}
	if info.Size() > maxExchangeResponseBytes {
		return nil, fmt.Errorf("panelharness: response file %s exceeded %d bytes", respPath, maxExchangeResponseBytes)
	}
	limited := io.LimitReader(file, maxExchangeResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxExchangeResponseBytes {
		return nil, fmt.Errorf("panelharness: response file %s exceeded %d bytes", respPath, maxExchangeResponseBytes)
	}
	return raw, nil
}

// FileExchangeSelector is a Selector for non-API panelists (CLI-driven
// models such as sol/luna, answered by an out-of-process responder script)
// -- it mirrors CHAOS-3742 arm 4/5's own file-exchange envelope
// (internal/runtime/hosted/file_exchange_runtime_test.go's fileExchangeRuntime,
// the exact contract that file's own R3-review comment names authoritative)
// field-for-field and mechanic-for-mechanic: same "<seq6>-<operation>.json"
// naming under requests/ and responses/, same temp-file-then-rename atomic
// publish, same per-run session-nonce guard against a stale leftover
// response file, same torn-read tolerance (a response file read mid-write
// is treated as "not ready yet," not a hard failure), same
// responder-error-content-never-surfaced discipline. It is a SEPARATE type
// rather than an import of that one: fileExchangeRuntime/exchangeRequest/
// exchangeResponse live in an internal _test.go file (test-only, legitimately
// unexported outside that package) and this package needs the identical
// WIRE SHAPE, not that package's contextfabric.ModelRuntime plumbing -- see
// this package's own doc comment for why a P6 panelist is a different
// consumer of the same file-exchange IDEA at a different layer (HTTP, not
// in-process Engine).
//
// operation is always "structure_selection" (never "interpret"/"synthesize"
// -- those remain that other transport's own operations); this envelope
// asks a panelist to choose receipts across every StructureNeeds member in
// ONE exchange, matching CHAOS-3860's own stated delta ("select-and-continue
// carrying receipt arrays," plural).
type FileExchangeSelector struct {
	dir     string
	model   string
	timeout time.Duration
	poll    time.Duration
	nonce   string
	seq     atomic.Int64
}

const fileExchangeOperation = "structure_selection"

// NewFileExchangeSelector creates the requests/ and responses/
// subdirectories under dir (creating dir itself if needed) and mints a
// fresh per-run session nonce -- one Selector per panelist per run, exactly
// as fileExchangeRuntime is one instance per test process.
func NewFileExchangeSelector(dir, canonicalModelIdentity string, timeout time.Duration) (*FileExchangeSelector, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("panelharness: create file-exchange dir: %w", err)
	}
	// codex adversarial review (round 2, MEDIUM): round 1 checked
	// requests/ and responses/ but never dir itself -- a writable PARENT
	// directory lets another local writer replace or relocate either
	// subdirectory (e.g. rename requests/ aside and create a new one it
	// controls) even when the subdirectories' own permissions look fine at
	// the moment they were checked. dir's own ownership/permissions are
	// verified once here, in addition to each subdirectory below.
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("panelharness: stat file-exchange dir: %w", err)
	}
	if err := verifyExchangeDirOwnership(dirInfo); err != nil {
		return nil, fmt.Errorf("panelharness: %s: %w", dir, err)
	}
	for _, sub := range []string{"requests", "responses"} {
		subPath := filepath.Join(dir, sub)
		if err := os.MkdirAll(subPath, 0o755); err != nil {
			return nil, fmt.Errorf("panelharness: create file-exchange dir %s: %w", sub, err)
		}
		// codex adversarial review (round 1, HIGH): this envelope discloses
		// its own session nonce in every request it writes -- the nonce
		// guard alone cannot stop another local writer, in a shared or
		// world-writable directory, from planting a forged response ahead
		// of the real responder. Verify ownership/permissions on the
		// ACTUAL directory a request/response will be read from and
		// written to, after MkdirAll (so a pre-existing directory this
		// call did not create is checked too, not only a freshly-created
		// one).
		info, err := os.Stat(subPath)
		if err != nil {
			return nil, fmt.Errorf("panelharness: stat file-exchange dir %s: %w", sub, err)
		}
		if err := verifyExchangeDirOwnership(info); err != nil {
			return nil, fmt.Errorf("panelharness: %s: %w", subPath, err)
		}
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("panelharness: generate file-exchange session nonce: %w", err)
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &FileExchangeSelector{
		dir: dir, model: canonicalModelIdentity, timeout: timeout,
		poll: 500 * time.Millisecond, nonce: hex.EncodeToString(nonceBytes),
	}, nil
}

// exchangeRequest/exchangeResponse mirror
// internal/runtime/hosted/file_exchange_runtime_test.go's own
// exchangeRequest/exchangeResponse field-for-field -- see this file's own
// package doc comment for why this is a parallel definition, not a shared
// import.
type exchangeRequest struct {
	Operation    string          `json:"operation"`
	Seq          int64           `json:"seq"`
	SessionNonce string          `json:"session_nonce"`
	System       string          `json:"system"`
	Prompt       string          `json:"prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Instructions string          `json:"instructions"`
}

type exchangeResponse struct {
	SessionNonce string          `json:"session_nonce"`
	Output       json.RawMessage `json:"output"`
	Error        string          `json:"error,omitempty"`
}

// structureSelectionPrompt is the bounded JSON user payload this
// operation's prompt field carries -- the question text (the panelist is a
// genuine acceptance-run participant judging real org data, the same
// information boundary interpretation/synthesis already cross for this
// same question) plus every offered receipt, member-tagged, ids/enums/label
// only.
type structureSelectionPrompt struct {
	Question string            `json:"question"`
	Missing  []string          `json:"missing"`
	Offers   []offerProjection `json:"offers"`
}

// structureSelectionOutput is the exact shape output_schema below requires
// and ParseStructureSelectionOutput decodes: one entry per member the
// panelist chose to confirm. A member absent from Selections, or present
// with an empty SelectedReceiptID, means "no offer for this member was
// worth confirming" -- never guessed or defaulted by this package.
type structureSelectionOutput struct {
	Selections []struct {
		Member            string `json:"member"`
		SelectedReceiptID string `json:"selected_receipt_id"`
	} `json:"selections"`
}

// structureSelectionOutputSchema is the fixed JSON Schema this envelope
// discloses to the responder as output_schema -- a closed, minimal shape
// (no free-text rationale field: this package's own "ids/enums only" sink
// discipline extends to what it ever ASKS a model to produce, not only what
// it stores).
const structureSelectionOutputSchema = `{
  "type": "object",
  "required": ["selections"],
  "additionalProperties": false,
  "properties": {
    "selections": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["member", "selected_receipt_id"],
        "additionalProperties": false,
        "properties": {
          "member": {"type": "string"},
          "selected_receipt_id": {"type": "string"}
        }
      }
    }
  }
}`

var errFileExchangeResponderReported = errors.New("panelharness: file-exchange responder reported an error")
var errFileExchangeSessionMismatch = errors.New("panelharness: file-exchange response session nonce mismatch")

// SelectReceipts implements Selector by writing one structure_selection
// exchange request and polling for its response, exactly as
// fileExchangeRuntime.exchange does for interpret/synthesize.
func (f *FileExchangeSelector) SelectReceipts(ctx context.Context, question string, needs contractsv1.ContextFabricStructureNeeds) (map[string]string, error) {
	missing := make([]string, 0, len(needs.Missing))
	for _, member := range needs.Missing {
		missing = append(missing, string(member))
	}
	promptPayload, err := json.Marshal(structureSelectionPrompt{Question: question, Missing: missing, Offers: projectOffers(needs)})
	if err != nil {
		return nil, fmt.Errorf("panelharness: encode structure selection prompt: %w", err)
	}

	raw, err := f.exchange(ctx, string(promptPayload))
	if err != nil {
		return nil, err
	}
	var output structureSelectionOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, fmt.Errorf("panelharness: decode structure selection output: %w", err)
	}
	selections := make(map[string]string, len(output.Selections))
	for _, entry := range output.Selections {
		if entry.SelectedReceiptID == "" {
			continue
		}
		selections[entry.Member] = entry.SelectedReceiptID
	}
	return selections, nil
}

const structureSelectionSystemPrompt = "You are one independent panelist in a multi-model acceptance panel. You will be given a real engineering question and a list of offered receipts, each tagged with the intent-frame member it resolves (expected_kind | subject_anchor | subject_handle) and a human-readable label. For each member listed in `missing` that has at least one offer you are confident resolves the question, choose exactly one offer's receipt_id. Never invent a receipt_id not present in `offers`. Never select an offer you are not genuinely confident about -- omitting a member from your answer is always acceptable and expected when no offer fits."

func (f *FileExchangeSelector) exchange(ctx context.Context, prompt string) (json.RawMessage, error) {
	seq := f.seq.Add(1)
	name := fmt.Sprintf("%06d-%s.json", seq, fileExchangeOperation)
	reqPath := filepath.Join(f.dir, "requests", name)
	respPath := filepath.Join(f.dir, "responses", name)

	request := exchangeRequest{
		Operation: fileExchangeOperation, Seq: seq, SessionNonce: f.nonce,
		System: structureSelectionSystemPrompt, Prompt: prompt, OutputSchema: json.RawMessage(structureSelectionOutputSchema),
		Instructions: "Produce a single JSON object as the response file's `output` field, matching `output_schema` exactly. Echo `session_nonce` back UNCHANGED in your response. Write the response as {\"session_nonce\": <the same value>, \"output\": <the JSON object>} to the response file path you were given for this request.",
	}
	body, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("panelharness: marshal exchange request: %w", err)
	}
	// Temp-file-then-rename publish (mirrors fileExchangeRuntime.exchange's
	// own atomicity precisely): a responder polling requests/ must never
	// observe a partially-written request file.
	tmp, err := os.CreateTemp(filepath.Join(f.dir, "requests"), "."+name+".tmp*")
	if err != nil {
		return nil, fmt.Errorf("panelharness: create temp exchange request: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("panelharness: write temp exchange request: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("panelharness: close temp exchange request: %w", err)
	}
	if err := os.Rename(tmpPath, reqPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("panelharness: publish exchange request: %w", err)
	}

	deadline := time.Now().Add(f.timeout)
	ticker := time.NewTicker(f.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			raw, err := readBoundedResponseFile(respPath)
			if err != nil {
				if os.IsNotExist(err) {
					if time.Now().After(deadline) {
						return nil, fmt.Errorf("%w: waited %s for %s", context.DeadlineExceeded, f.timeout, respPath)
					}
					continue
				}
				return nil, fmt.Errorf("panelharness: read exchange response: %w", err)
			}
			var response exchangeResponse
			if unmarshalErr := json.Unmarshal(raw, &response); unmarshalErr != nil {
				// Torn-read tolerance: a response observed mid-write is
				// "not ready yet," retried until the deadline -- same
				// reasoning as fileExchangeRuntime.exchange.
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("%w: response file never parsed cleanly after %s (last error: %v)", context.DeadlineExceeded, f.timeout, unmarshalErr)
				}
				continue
			}
			if response.SessionNonce != f.nonce {
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("%w: %w: waited %s for a matching session_nonce on %s", context.DeadlineExceeded, errFileExchangeSessionMismatch, f.timeout, respPath)
				}
				continue
			}
			if response.Error != "" {
				// The responder's error text is untrusted, out-of-process
				// content -- only a fixed class and its byte length ever
				// reach an error message, never the text itself (same
				// discipline fileExchangeRuntime.exchange documents).
				return nil, fmt.Errorf("%w (%d bytes)", errFileExchangeResponderReported, len(response.Error))
			}
			return response.Output, nil
		}
	}
}
