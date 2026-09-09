package hosted

// CHAOS-5442: THE REFUSAL, READ BACK OUT OF THE SINK THE DEPLOYED
// CONSTRUCTION RETURNS.
//
// The change this file guards is a DISCLOSURE, and a disclosure regresses
// silently: a build that stopped naming the basis produces the same status,
// the same limitation count and the same everything else. The only thing that
// distinguishes it is a key on the line, and a test that hands its own sink
// to a test adapter proves formatting while staying green the day the runtime
// stops installing one -- so this drives open.go's OWN construction, at the
// production log level, exactly as its frame-gate sibling in this package
// does.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// emitSubjectlessTerminal drives one subjectless-terminal record through the
// telemetry the deployed runtime constructs and returns the decoded line.
func emitSubjectlessTerminal(t *testing.T, reason string, refusalBasis string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	contextfabric.NewSlogEngineTelemetry(logger).RecordSubjectlessTerminal(
		context.Background(), storage.Principal{OrgID: "org_5442"}, reason, refusalBasis)
	if buf.Len() == 0 {
		t.Fatal("the deployed telemetry emitted NOTHING at the production log level -- a refusal an operator cannot see is a refusal that cannot be counted")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO -- a line demoted below the production level disappears in prod exactly as it would here", level)
	}
	return rec
}

// THE REFUSING LINE. Before this change a refused frame reported the reason
// `empty_pool` and no basis at all, so in the collected logs it was
// indistinguishable from an organization whose graph genuinely held nothing.
func TestTheDeployedSubjectlessTerminalNamesTheRefusalAndItsBasis(t *testing.T) {
	t.Parallel()
	rec := emitSubjectlessTerminal(t, "frame_gate_refused", "member_kind_unservable")
	if got, _ := rec["reason"].(string); got != "frame_gate_refused" {
		t.Errorf("reason = %q, want \"frame_gate_refused\" -- a refused frame and an empty pool must not share a token", got)
	}
	if got, _ := rec["refusal_basis"].(string); got != "member_kind_unservable" {
		t.Errorf("refusal_basis = %q, want \"member_kind_unservable\" -- the reason says the gate refused, the basis says what it refused on, and an operator needs both to act", got)
	}
}

// THE ORDINARY LINE, and the arm that makes the one above mean something. The
// key is present with the explicit token "none" on a turn nothing refused: if
// it appeared only on refusals, its absence would be indistinguishable from a
// build that no longer emits it, which is the regression that would otherwise
// be invisible at Info.
func TestTheDeployedSubjectlessTerminalCarriesAnExplicitNoneBasis(t *testing.T) {
	t.Parallel()
	rec := emitSubjectlessTerminal(t, "empty_pool", "none")
	if got, _ := rec["reason"].(string); got != "empty_pool" {
		t.Errorf("reason = %q, want \"empty_pool\"", got)
	}
	got, ok := rec["refusal_basis"].(string)
	if !ok {
		t.Fatalf("the emitted line carries no refusal_basis key at all -- an absent key and a measured 'not refused' must never read alike; line: %v", rec)
	}
	if got != "none" {
		t.Errorf("refusal_basis = %q, want the explicit token \"none\", never an empty value", got)
	}
}
