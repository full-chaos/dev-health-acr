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

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// emitSubjectlessTerminal drives one subjectless-terminal record through the
// telemetry the deployed runtime constructs and returns the decoded line.
func emitSubjectlessTerminal(t *testing.T, reason string, refusalBasis string) map[string]any {
	return emitSubjectlessTerminalWithKinds(t, reason, refusalBasis, "none", "none")
}

// emitSubjectlessTerminalWithKinds is the same drive with CHAOS-5660's two
// declared/offered kind values supplied explicitly.
func emitSubjectlessTerminalWithKinds(t *testing.T, reason string, refusalBasis string, declaredKinds string, offeredKinds string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// THROUGH THE PRODUCTION INSTALLATION, not past it. Constructing the sink
	// directly verifies its FORMATTING and nothing about the wiring, so this
	// file would stay green while hosted installed no telemetry that emits
	// these keys at all. contextFabricEngineTelemetry (open.go) is the
	// function every real deployment goes through -- a nil
	// Options.EngineTelemetry override is the production case -- so a wiring
	// regression fails here, not only a formatting one.
	telemetry := contextFabricEngineTelemetry(Options{Logger: logger})
	if telemetry == nil {
		t.Fatal("the hosted installation returned no engine telemetry at all -- every subjectless terminal would be silent in production")
	}
	telemetry.RecordSubjectlessTerminal(
		context.Background(), storage.Principal{OrgID: "org_5442"}, reason, refusalBasis, declaredKinds, offeredKinds)
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

// THE DECLARED-KIND LINE (CHAOS-5660). The reason alone says a turn offered
// nothing that could satisfy the kind its frame declared; it does not say
// WHICH kind was declared or what was offered instead, and those two are the
// whole finding. Diagnosing this class the first time required joining the
// declared kind, the withheld kind and the offered kinds by hand across the
// engine log, the harness's own warning and the store -- because only the
// first of the three was ever emitted here.
func TestTheDeployedSubjectlessTerminalNamesTheDeclaredAndOfferedKinds(t *testing.T) {
	t.Parallel()
	rec := emitSubjectlessTerminalWithKinds(t, "no_candidate_of_declared_kind", "none", "project", "ci_pipeline_run,pull_request")
	if got, _ := rec["reason"].(string); got != "no_candidate_of_declared_kind" {
		t.Errorf("reason = %q, want \"no_candidate_of_declared_kind\"", got)
	}
	if got, _ := rec["declared_kinds"].(string); got != "project" {
		t.Errorf("declared_kinds = %q, want \"project\" -- the kind the question named is the first half of the finding", got)
	}
	if got, _ := rec["offered_kinds"].(string); got != "ci_pipeline_run,pull_request" {
		t.Errorf("offered_kinds = %q, want the kinds actually offered -- without them the reason cannot be audited from the line alone", got)
	}
}

// THE ORDINARY ARM for the same pair, holding the missing-versus-measured-zero
// rule the refusal_basis key above already holds: both keys are present with
// the explicit "none" token on a turn that declared or offered nothing.
func TestTheDeployedSubjectlessTerminalCarriesExplicitNoneKinds(t *testing.T) {
	t.Parallel()
	rec := emitSubjectlessTerminal(t, "empty_pool", "none")
	declared, ok := rec["declared_kinds"].(string)
	if !ok {
		t.Fatalf("the emitted line carries no declared_kinds key at all; line: %v", rec)
	}
	offered, ok := rec["offered_kinds"].(string)
	if !ok {
		t.Fatalf("the emitted line carries no offered_kinds key at all; line: %v", rec)
	}
	if declared != "none" || offered != "none" {
		t.Errorf("declared/offered = %q/%q, want the explicit token \"none\" on both, never an empty value", declared, offered)
	}
}
