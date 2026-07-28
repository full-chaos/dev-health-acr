package mcpclientfixtures

import (
	"strings"
	"testing"
)

// TestInstallSidecarWindowsSnippetIsCanonicalEverywhere is the marker-
// based canonical-parity check for the Windows install step every guide
// embeds verbatim, mirroring TestInstallSidecarSnippetIsCanonicalEverywhere
// for the POSIX block.
func TestInstallSidecarWindowsSnippetIsCanonicalEverywhere(t *testing.T) {
	root := findRepoRoot(t)
	want := strings.TrimRight(InstallSidecarWindowsSnippet, "\n")
	for _, relPath := range docPaths {
		data := readDoc(t, root, relPath)
		if !strings.Contains(string(data), "<!-- FIXTURE:install-sidecar-windows -->") {
			continue
		}
		t.Run(relPath, func(t *testing.T) {
			got, err := ExtractMarkedBlock(data, "install-sidecar-windows")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s's install-sidecar-windows block has drifted from InstallSidecarWindowsSnippet", relPath)
			}
		})
	}
}

// posixOnlyTokens are shell/coreutils idioms that only make sense on
// POSIX systems and must never appear in the Windows install snippet:
// Windows has no chmod (executables need no execute bit), no native tar
// in scope here (the release matrix ships Windows as .zip, not .tar.gz),
// and no sha256sum/shasum binaries.
var posixOnlyTokens = []string{"chmod +x", "tar -xzf", "sha256sum", "shasum"}

// windowsOnlyTokens are PowerShell cmdlets/syntax that only make sense on
// Windows and must never appear in the POSIX install snippet.
var windowsOnlyTokens = []string{"Get-FileHash", "Expand-Archive", "Select-String", "$env:", "cosign.exe"}

// TestInstallSnippetsDoNotCrossContaminatePlatformCommands is the
// structural regression lock proving each platform's install snippet
// only ever contains commands runnable on that platform: the POSIX
// snippet must contain none of windowsOnlyTokens, and the Windows
// snippet must contain none of posixOnlyTokens. This is the closest this
// environment (no pwsh available) can come to "testing" the PowerShell
// snippet without an actual PowerShell runtime -- it cannot prove the
// PowerShell is syntactically valid, but it does prove no POSIX-only
// command silently leaked into the Windows guide, or vice versa.
func TestInstallSnippetsDoNotCrossContaminatePlatformCommands(t *testing.T) {
	for _, token := range windowsOnlyTokens {
		if strings.Contains(InstallSidecarSnippet, token) {
			t.Fatalf("InstallSidecarSnippet (POSIX) unexpectedly contains Windows-only token %q", token)
		}
	}
	for _, token := range posixOnlyTokens {
		if strings.Contains(InstallSidecarWindowsSnippet, token) {
			t.Fatalf("InstallSidecarWindowsSnippet unexpectedly contains POSIX-only token %q", token)
		}
	}
}

// TestInstallSidecarSnippetChecksumIsPortable proves the POSIX snippet's
// checksum step probes for whichever of sha256sum/shasum is present
// rather than assuming GNU coreutils -- stock macOS ships only `shasum`,
// while many minimal Linux images ship only `sha256sum`.
func TestInstallSidecarSnippetChecksumIsPortable(t *testing.T) {
	if !strings.Contains(InstallSidecarSnippet, "command -v sha256sum") {
		t.Fatal("expected the POSIX snippet to probe for sha256sum before assuming it exists")
	}
	if !strings.Contains(InstallSidecarSnippet, "shasum -a 256 --check") {
		t.Fatal("expected the POSIX snippet to fall back to shasum -a 256 --check for macOS")
	}
}

// TestInstallSidecarWindowsSnippetUsesKeylessReleaseBundle proves the Windows
// snippet verifies the same keyless Sigstore bundle and constrained GitHub
// Actions identity as the authoritative release policy.
func TestInstallSidecarWindowsSnippetUsesKeylessReleaseBundle(t *testing.T) {
	for _, required := range []string{
		"SHA256SUMS.sigstore.json",
		"cosign.exe verify-blob SHA256SUMS",
		"--certificate-identity-regexp",
		"--certificate-oidc-issuer",
		"$LASTEXITCODE -ne 0",
	} {
		if !strings.Contains(InstallSidecarWindowsSnippet, required) {
			t.Fatalf("expected the Windows snippet to contain %q", required)
		}
	}
	for _, obsolete := range []string{
		"--key signing/cosign.pub",
		"--signature SHA256SUMS.sig",
		"--insecure-ignore-tlog",
		"git show <trusted-ref>:signing/cosign.pub",
	} {
		if strings.Contains(InstallSidecarWindowsSnippet, obsolete) {
			t.Fatalf("Windows snippet still contains obsolete local-key verification token %q", obsolete)
		}
	}
}
