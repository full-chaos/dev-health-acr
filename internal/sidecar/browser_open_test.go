package sidecar

import (
	"errors"
	"strings"
	"testing"
)

// The verification URI is server-supplied data. Handing an unvalidated one to
// a desktop opener turns a hosted-API response into local command execution
// against any scheme the host has a handler registered for, so everything
// except https and validated loopback http must be refused before any binary
// is resolved -- let alone started.
func TestOpenVerificationURIRefusesEverythingButHTTPSAndLoopbackHTTP(t *testing.T) {
	launchable := []string{
		"https://acr.example.com/device",
		"https://acr.example.com:8443/device?code=ABCD",
		"http://127.0.0.1:8080/acr/device",
		"http://[::1]:8080/acr/device",
		"http://localhost:8080/acr/device",
		"http://LOCALHOST:8080/acr/device",
		"http://127.9.9.9/acr/device",
	}
	refused := []string{
		"",
		"http://device.acr.invalid/acr/device",
		"http://10.0.0.5/acr/device",
		"http://169.254.169.254/latest/meta-data",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vscode://extension/install?id=evil",
		"ssh://localhost/",
		"https://",
		"//acr.example.com/device",
		"/acr/device",
		"-e https://acr.example.com/device",
		"https://user:secret@acr.example.com/device",
		"https://acr.example.com/device\nopen file:///etc/passwd",
		"https://acr.example.com/dev ice",
		"https://acr.example.com/device\x00",
		"https://acr.example.com/" + strings.Repeat("a", maxVerificationURIBytes),
	}

	for _, uri := range launchable {
		t.Run("launchable/"+uri, func(t *testing.T) {
			if err := validateVerificationURI(uri); err != nil {
				t.Fatalf("validateVerificationURI(%q) = %v, want accepted", uri, err)
			}
		})
	}
	for _, uri := range refused {
		t.Run("refused/"+uri, func(t *testing.T) {
			if err := validateVerificationURI(uri); !errors.Is(err, ErrVerificationURIUnsupported) {
				t.Fatalf("validateVerificationURI(%q) = %v, want ErrVerificationURIUnsupported", uri, err)
			}
			// The refusal must land before resolution, so a hostile URI never
			// reaches the point where a binary would be selected at all.
			resolverConsulted := false
			original := currentExecutableResolver
			currentExecutableResolver = func(name string) (string, error) {
				resolverConsulted = true
				return original(name)
			}
			t.Cleanup(func() { currentExecutableResolver = original })
			if err := OpenVerificationURI(uri); !errors.Is(err, ErrVerificationURIUnsupported) {
				t.Fatalf("OpenVerificationURI(%q) = %v, want ErrVerificationURIUnsupported", uri, err)
			}
			if resolverConsulted {
				t.Fatalf("OpenVerificationURI(%q) resolved an executable for a refused address", uri)
			}
		})
	}
}

// A refused address must never leak back into an operator-facing message: the
// URI is untrusted server-supplied text.
func TestOpenVerificationURIErrorCarriesNoPartOfTheRejectedAddress(t *testing.T) {
	uri := "javascript:alert('acr-secret-marker')"
	err := OpenVerificationURI(uri)
	if err == nil {
		t.Fatal("a javascript URI was accepted")
	}
	if strings.Contains(err.Error(), "acr-secret-marker") || strings.Contains(err.Error(), uri) {
		t.Fatalf("opener error echoed the rejected address: %v", err)
	}
}

// Failure to resolve an opener is normal on a headless host and must surface
// as an ordinary error for the caller to ignore, never a panic or a hang.
func TestOpenVerificationURIReportsAnUnavailableOpener(t *testing.T) {
	original := currentExecutableResolver
	currentExecutableResolver = func(string) (string, error) { return "", ErrExecutableUnavailable }
	t.Cleanup(func() { currentExecutableResolver = original })

	err := OpenVerificationURI("https://acr.example.com/device")

	if !errors.Is(err, ErrExecutableUnavailable) {
		t.Fatalf("opener error = %v, want ErrExecutableUnavailable", err)
	}
}

func TestBrowserLaunchEnvironExcludesACRVariablesAndPinsTrustedPath(t *testing.T) {
	// Given
	t.Setenv("ACR_API_TOKEN", validTestToken(60))
	t.Setenv("ACR_API_URL", "https://api.dev-health.example.com")
	t.Setenv("HOME", "/tmp/acr-home")

	// When
	environment := browserLaunchEnviron()

	// Then
	sawHome := false
	sawPath := false
	for _, entry := range environment {
		if strings.HasPrefix(entry, "ACR_") {
			t.Fatalf("browser environment carried an ACR variable: %q", strings.SplitN(entry, "=", 2)[0])
		}
		if entry == "HOME=/tmp/acr-home" {
			sawHome = true
		}
		if strings.HasPrefix(entry, "PATH=") {
			sawPath = true
			for _, directory := range strings.Split(strings.TrimPrefix(entry, "PATH="), ":") {
				if !isUnderTrustedPrefix(directory) {
					t.Fatalf("browser PATH entry %q is outside the trusted prefixes", directory)
				}
			}
		}
	}
	if !sawHome {
		t.Fatal("browser environment dropped HOME, which every desktop opener needs")
	}
	if len(trustedExecutableSearchDirs()) != 0 && !sawPath {
		t.Fatal("browser environment did not pin PATH to the trusted search directories")
	}
}
