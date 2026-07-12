// Package depcheck holds dependency/advisory regression tests that are not
// tied to any single production package. This file locks the remediation
// for GO-2026-4770 (GHSA-q382-vc8q-7jhj): "Improper handling of null Unicode
// character when parsing JSON in github.com/modelcontextprotocol/go-sdk".
//
// Root cause (upstream analysis, segmentio/encoding#161): the fast
// keyset-based struct decoder in github.com/segmentio/encoding/json
// compared JSON object keys against known struct field names using a
// zero-padded SIMD lookup. A key with a trailing NUL (\u0000) appended was
// zero-padded to the same byte pattern as the legitimate key, so the
// decoder matched them as equal. Combined with duplicate keys, this gave a
// "last key wins" override that could smuggle an attacker-controlled value
// into a field an inspector believed was already set/validated by the
// first (exact-matching) key.
//
// Fix: segmentio/encoding@7d5a25d (released as v0.5.4) added a length
// check before accepting a keyset match, so a NUL-suffixed key can never
// match a shorter legitimate field name. The upstream go-sdk fix
// (724dd47aa3431b9d4cf9ac2eebbf7b38a629afca, released as go-sdk v1.4.1)
// is exactly this dependency bump — go-sdk's own source is unchanged.
//
// go.mod pins github.com/segmentio/encoding to >= v0.5.4 directly so Go's
// minimal version selection resolves the patched decoder for the whole
// build, including go-sdk's usage, without requiring a go-sdk/Go
// toolchain upgrade. These tests prove that pin is effective and will
// fail loudly if it is ever weakened.
package depcheck

import (
	"context"
	"encoding/json" //nolint:depguard // decodes trusted `go list` tool output, not wire data
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	segmentiojson "github.com/segmentio/encoding/json"
)

const minPatchedSegmentioEncodingVersion = "v0.5.4"

// parseSemverTriple extracts (major, minor, patch) from a "vX.Y.Z" tag,
// ignoring any pre-release/build/pseudo-version suffix after the patch
// digits. It intentionally avoids pulling in golang.org/x/mod/semver as a
// new dependency for a single narrow comparison.
func parseSemverTriple(v string) (major, minor, patch int, err error) {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("not a MAJOR.MINOR.PATCH version: %q", v)
	}
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, err
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, err
	}
	if patch, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, err
	}
	return major, minor, patch, nil
}

// semverAtLeast reports whether got is >= want, comparing MAJOR.MINOR.PATCH
// numerically (so v0.5.10 correctly outranks v0.5.4, unlike a lexicographic
// string comparison).
func semverAtLeast(got, want string) (bool, error) {
	gMaj, gMin, gPatch, err := parseSemverTriple(got)
	if err != nil {
		return false, fmt.Errorf("parsing resolved version %q: %w", got, err)
	}
	wMaj, wMin, wPatch, err := parseSemverTriple(want)
	if err != nil {
		return false, fmt.Errorf("parsing minimum version %q: %w", want, err)
	}
	if gMaj != wMaj {
		return gMaj > wMaj, nil
	}
	if gMin != wMin {
		return gMin > wMin, nil
	}
	return gPatch >= wPatch, nil
}

// TestSegmentioEncodingResolvedVersion locks the module version that Go's
// minimal version selection resolves for github.com/segmentio/encoding.
//
// Given the module graph produced by go.mod/go.sum,
// When `go list -m` resolves github.com/segmentio/encoding for this build,
// Then the selected version must be the GO-2026-4770 fix release (v0.5.4)
// or newer, not the vulnerable v0.5.3 shipped by go-sdk v1.3.1.
//
// This shells out to the go tool (rather than runtime/debug.BuildInfo,
// whose Deps list is not populated for `go test` binaries) to inspect the
// same resolved module graph `go build`/`go mod verify` would use.
func TestSegmentioEncodingResolvedVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- fixed argv, no user input
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-json", "github.com/segmentio/encoding")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m -json github.com/segmentio/encoding: %v", err)
	}

	var mod struct {
		Path    string `json:"Path"`
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatalf("decoding `go list -m -json` output: %v\n%s", err, out)
	}
	if mod.Path != "github.com/segmentio/encoding" {
		t.Fatalf("go list resolved unexpected module path %q", mod.Path)
	}

	ok, err := semverAtLeast(mod.Version, minPatchedSegmentioEncodingVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("github.com/segmentio/encoding resolved to %s, want >= %s (GO-2026-4770 fix); "+
			"go.mod pin was weakened or removed", mod.Version, minPatchedSegmentioEncodingVersion)
	}
}

// TestNullSuffixKeyDoesNotMatchShorterField reproduces the exact upstream
// repro from segmentio/encoding#161 / GHSA-q382-vc8q-7jhj.
//
// Given a JSON object whose only key is the legitimate field name with a
// trailing NUL Unicode character appended ("field\u0000"),
// When it is decoded into a struct with a "field" JSON field,
// Then the NUL-suffixed key must NOT be treated as a match for "field" —
// the field must stay at its zero value, exactly like encoding/json.
func TestNullSuffixKeyDoesNotMatchShorterField(t *testing.T) {
	type target struct {
		Field string `json:"field"`
	}

	var got target
	if err := segmentiojson.Unmarshal([]byte(`{"field\u0000": "value"}`), &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if got.Field != "" {
		t.Fatalf("NUL-suffixed key matched shorter field name: Field = %q, want empty (GO-2026-4770 regression)", got.Field)
	}
}

// TestDuplicateKeyWithNullSuffixCannotOverrideLegitimateValue mirrors the
// MCP wire-level attack the advisory describes: a legitimate key is
// followed by a duplicate key with a NUL suffix, attempting a
// "last key wins" override of an already-inspected field.
//
// Given a JSON object with a legitimate "name" key followed by a
// duplicate "name\u0000" key carrying an attacker-controlled value,
// When it is decoded into a struct with a "name" JSON field,
// Then the field must retain the value from the first, exact-matching
// key — the NUL-suffixed duplicate must be ignored, not treated as an
// override.
func TestDuplicateKeyWithNullSuffixCannotOverrideLegitimateValue(t *testing.T) {
	type toolCall struct {
		Name string `json:"name"`
	}

	var got toolCall
	payload := []byte(`{"name":"safe_tool","name\u0000":"evil_tool"}`)
	if err := segmentiojson.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if got.Name != "safe_tool" {
		t.Fatalf("duplicate NUL-suffixed key overrode inspected value: Name = %q, want %q (GO-2026-4770 regression)",
			got.Name, "safe_tool")
	}
}
