package certify

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// TestCertifyAbsentRejectsAFractionalIntScope pins scopeValueHasDeclaredType's
// FieldInt case against accepting a fractional float64/float32 as a
// well-typed scope value. `CertifyAbsent(log, ev, map[string]any{"pass":
// 1.5})` must never match a real pass=1 line (scopeMatch's jsonEqual
// comparison sees 1.5 != 1) and silently certify a false absence for a pass
// that actually ran -- it must refuse the malformed scope input outright, via
// isWholeNumber, shared with validateFields' own FieldInt case (see its doc
// comment for the full account).
func TestCertifyAbsentRejectsAFractionalIntScope(t *testing.T) {
	log, err := Parse([]byte(`{"time":"2026-09-11T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced","request_id":"req-real","pass":1,"stage":"anchor_slot_displaced","subject_kind":"repository","subject_canonical_id":"repo-1","anchor_slot_reserved":"repository","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CertifyAbsent(log, eventspec.AnchorSlotDisplaced, map[string]any{"request_id": "req-real", "pass": 1.5}); err == nil {
		t.Fatal("CertifyAbsent accepted pass=1.5 for a FieldInt scope and certified a real pass-1 line as absent")
	}
}
