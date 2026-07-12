package mcpclientfixtures

import (
	"strings"
	"testing"
)

// TestBundleShareCautionIsCanonicalEverywhere is the marker-based
// canonical-parity check for the short clause every guide's "Next Steps"
// list uses when pointing at `acr-mcp diagnostics`/`doctor --bundle`: it
// must match BundleShareCaution byte-for-byte in every guide that embeds
// it, so a future hand-edit cannot quietly reintroduce "shareable" or
// "attach to an issue"-style wording that undersells the bundle's actual
// sensitivity.
func TestBundleShareCautionIsCanonicalEverywhere(t *testing.T) {
	root := findRepoRoot(t)
	want := strings.TrimRight(BundleShareCaution, "\n")
	for _, relPath := range docPaths {
		data := readDoc(t, root, relPath)
		if !strings.Contains(string(data), "<!-- FIXTURE:bundle-share-caution -->") {
			continue
		}
		t.Run(relPath, func(t *testing.T) {
			got, err := ExtractMarkedBlock(data, "bundle-share-caution")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s's bundle-share-caution clause has drifted from BundleShareCaution:\ngot:  %q\nwant: %q", relPath, got, want)
			}
		})
	}
}

// forbiddenBundleSharingPhrases are substrings that would understate a
// diagnostic bundle's sensitivity: being secrets-free does not make it
// safe for a generic public audience (it still identifies the requesting
// organization's sidecar deployment), so no guide may describe it as
// "shareable" in the abstract, or suggest attaching it to a public issue
// or issue tracker.
var forbiddenBundleSharingPhrases = []string{
	"shareable, secrets-free",
	"safe to share as-is",
	"attach to a support request or issue",
	"attach it to an issue",
	"attach to an issue",
}

// TestNoDocUnderstatesBundleSensitivity is the negative-scan regression
// lock for the wording fix above: every guide under
// docs/examples/mcp-clients/ must never contain any of
// forbiddenBundleSharingPhrases, regardless of which specific sentence
// introduced it originally.
func TestNoDocUnderstatesBundleSensitivity(t *testing.T) {
	root := findRepoRoot(t)
	for _, relPath := range docPaths {
		t.Run(relPath, func(t *testing.T) {
			data := readDoc(t, root, relPath)
			for _, phrase := range forbiddenBundleSharingPhrases {
				if strings.Contains(string(data), phrase) {
					t.Fatalf("%s contains forbidden phrase %q -- diagnostic bundles must only be described as shareable through an approved private support channel", relPath, phrase)
				}
			}
		})
	}
}
