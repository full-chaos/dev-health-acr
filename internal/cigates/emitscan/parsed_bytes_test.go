package emitscan_test

import (
	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan/tcheck"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// A file changed on disk after the parent parsed it must be checked as the bytes the parent parsed: the helper here changes
// the file while it runs (between the parent's parse and the parent's own type-check).
func TestKeyScopeTypeCheckUsesTheBytesTheScanParsedNotTheDisk(t *testing.T) {
	body := "func f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v) }"
	script := ackScript + `root=$(printf %s "$payload" | sed -n 's/.*"Root":"\([^"]*\)".*/\1/p'); f="$root/internal/sample/sample.go"
{ printf '//go:build ignore\n\n'; cat "$f"; } > "$f.new" && mv "$f.new" "$f"
echo "$ack"`
	restore := emitscan.SetHelperBinary(plantHelper(t, script))
	defer restore()
	if got := keyFindings(scanSource(t, header+body+"\n")); got != "R1:api_token" {
		t.Errorf("key findings %q want R1:api_token (the parsed bytes, which carry no build constraint)", got)
	}
}

func TestMatchDecidesOnTheGivenBytesNotTheDisk(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ignored := []byte("//go:build ignore\n\npackage p\n")
	plain := []byte("package p\n")
	onDisk := filepath.Join(dir, "a.go")
	if err := os.WriteFile(onDisk, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := tcheck.Match(root, "p/a.go", ignored); err != nil || got {
		t.Errorf("bytes carry //go:build ignore, disk does not: matched=%v err=%v want false", got, err)
	}
	if err := os.WriteFile(onDisk, ignored, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := tcheck.Match(root, "p/a.go", plain); err != nil || !got {
		t.Errorf("disk carries //go:build ignore, bytes do not: matched=%v err=%v want true", got, err)
	}
}

func TestProbeKeyFollowsThePassedBytes(t *testing.T) {
	rels := []string{"p/a.go"}
	a := emitscan.ProbeKeyOfSources(rels, [][]byte{[]byte("package p\n")})
	b := emitscan.ProbeKeyOfSources(rels, [][]byte{[]byte("package q\n")})
	if a == b || a != emitscan.ProbeKeyOfSources(rels, [][]byte{[]byte("package p\n")}) {
		t.Errorf("probe key must change with the bytes and only with them: %s %s", a, b)
	}
}

func TestHelperRefusesARequestMissingTheBytesOfAFile(t *testing.T) {
	root := repoRoot(t)
	rel := "internal/cigates/emitscan/typed.go" // exists on disk: a disk fallback would read it
	req := tcheck.Request{Root: root, Rels: []string{rel}, Exports: map[string]string{}, CPUSeconds: 4}
	if err := emitscan.RunHelperRequest(root, req); err == nil {
		t.Error("a request with no bytes for its file was acknowledged")
	}
	req.Sources = map[string][]byte{rel: []byte("package emitscan\n")}
	if err := emitscan.RunHelperRequest(root, req); err != nil {
		t.Errorf("control: a request with its bytes was refused: %v", err)
	}
}

func TestOversizedHelperRequestIsARefusalNamingSizeAndBound(t *testing.T) {
	root := repoRoot(t)
	defer emitscan.SetMaxRequestBytes(1000)()
	req := tcheck.Request{Root: root, Rels: []string{"x.go"}, Sources: map[string][]byte{"x.go": make([]byte, 5000)}}
	err := emitscan.RunHelperRequest(root, req)
	if err == nil || !strings.Contains(err.Error(), "too large") || !strings.Contains(err.Error(), "bound is 1000 bytes") || !strings.Contains(err.Error(), "request is ") {
		t.Errorf("want a too-large refusal naming size and bound, got %v", err)
	}
}
