package identity_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestMaxNaturalKeyBytesCountsBytesNotCodePoints is the H6-minor pin
// (design brief D10 / §5 item 6): a multi-byte-rune natural key can sit at
// or under 256 UTF-8 CODE POINTS -- what
// internal/contracts/v1/validation_helpers.go's stringLengthBetween counts
// via utf8.RuneCountInString -- while its BYTE length exceeds 256, which
// is the bound the registry actually enforces. "日" is one code point, 3
// UTF-8 bytes.
func TestMaxNaturalKeyBytesCountsBytesNotCodePoints(t *testing.T) {
	segment := strings.Repeat("日", 90) // 90 code points, 270 bytes

	if codePoints := utf8.RuneCountInString(segment); codePoints > 256 {
		t.Fatalf("test setup invalid: segment has %d code points, want <= 256 to prove the code-point/byte distinction", codePoints)
	}
	if byteLength := len(segment); byteLength <= identity.MaxNaturalKeyBytes {
		t.Fatalf("test setup invalid: segment is %d bytes, want > %d to prove the code-point/byte distinction", byteLength, identity.MaxNaturalKeyBytes)
	}

	ledger := &identity.Ledger{}
	id, omitted, err := identity.Derive(identity.KindDeployment, []string{"repo-1", segment}, ledger)
	if err != nil {
		t.Fatalf("Derive error: %v", err)
	}
	if !omitted {
		t.Fatalf("expected whole-row omit for a >256-byte (but <=256-code-point) natural key; got id %q", id)
	}
	if id != "" {
		t.Fatalf("an omitted row must not also produce an id; got %q", id)
	}
	entries := ledger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one ledger entry, got %d", len(entries))
	}
	if entries[0].Kind != identity.KindDeployment {
		t.Fatalf("ledger entry kind = %q, want %q", entries[0].Kind, identity.KindDeployment)
	}
	if entries[0].ByteLength <= identity.MaxNaturalKeyBytes {
		t.Fatalf("ledger entry ByteLength = %d, want > %d", entries[0].ByteLength, identity.MaxNaturalKeyBytes)
	}
}

// TestMaxNaturalKeyBytesBoundary pins the exact edge: an id of exactly 256
// bytes is retained (design brief D10: "256 BYTES retained"); one byte
// over is refused.
func TestMaxNaturalKeyBytesBoundary(t *testing.T) {
	const prefixAndSeparator = len("project.v2:") + len("p") + len(":") // 13 fixed bytes before the padded segment

	atBound := strings.Repeat("a", identity.MaxNaturalKeyBytes-prefixAndSeparator)
	overBound := strings.Repeat("a", identity.MaxNaturalKeyBytes-prefixAndSeparator+1)

	id, omitted, err := identity.Derive(identity.KindProject, []string{"p", atBound}, nil)
	if err != nil {
		t.Fatalf("Derive error: %v", err)
	}
	if omitted {
		t.Fatalf("an id of exactly %d bytes must be retained, not omitted", identity.MaxNaturalKeyBytes)
	}
	if len(id) != identity.MaxNaturalKeyBytes {
		t.Fatalf("test setup invalid: id is %d bytes, want exactly %d", len(id), identity.MaxNaturalKeyBytes)
	}

	_, omitted, err = identity.Derive(identity.KindProject, []string{"p", overBound}, nil)
	if err != nil {
		t.Fatalf("Derive error: %v", err)
	}
	if !omitted {
		t.Fatalf("an id of %d bytes must be omitted", identity.MaxNaturalKeyBytes+1)
	}
}

// TestLedgerRecordsOmissionsPerKind proves the ledger accumulates across
// multiple omissions and reports per-kind counts (§5b bound-omit-ledger
// signal shape).
func TestLedgerRecordsOmissionsPerKind(t *testing.T) {
	ledger := &identity.Ledger{}
	over := strings.Repeat("a", 300)

	if _, omitted, err := identity.Derive(identity.KindProject, []string{"p", over}, ledger); err != nil || !omitted {
		t.Fatalf("Derive(project) omitted=%v err=%v, want omitted=true err=nil", omitted, err)
	}
	if _, omitted, err := identity.Derive(identity.KindDeployment, []string{"repo-1", over}, ledger); err != nil || !omitted {
		t.Fatalf("Derive(deployment) omitted=%v err=%v, want omitted=true err=nil", omitted, err)
	}
	if _, omitted, err := identity.Derive(identity.KindProject, []string{"p2", over}, ledger); err != nil || !omitted {
		t.Fatalf("Derive(project) #2 omitted=%v err=%v, want omitted=true err=nil", omitted, err)
	}

	counts := ledger.CountByKind()
	if counts[identity.KindProject] != 2 {
		t.Errorf("CountByKind[project] = %d, want 2", counts[identity.KindProject])
	}
	if counts[identity.KindDeployment] != 1 {
		t.Errorf("CountByKind[deployment] = %d, want 1", counts[identity.KindDeployment])
	}
	if len(ledger.Entries()) != 3 {
		t.Errorf("len(Entries()) = %d, want 3", len(ledger.Entries()))
	}
}

// TestDeriveNilLedgerIsValid proves omission works without a ledger --
// every Derive call site takes *Ledger, not Ledger, so "don't record" is a
// valid, non-panicking choice.
func TestDeriveNilLedgerIsValid(t *testing.T) {
	over := strings.Repeat("a", 300)
	id, omitted, err := identity.Derive(identity.KindProject, []string{"p", over}, nil)
	if err != nil {
		t.Fatalf("Derive error: %v", err)
	}
	if !omitted || id != "" {
		t.Fatalf("Derive(nil ledger) = (%q, %v), want (\"\", true)", id, omitted)
	}
}
