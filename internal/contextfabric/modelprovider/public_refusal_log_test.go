package modelprovider

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func refusalLines(lines []string) []string {
	var out []string
	for _, line := range lines {
		if strings.Contains(line, "non_public_address") {
			out = append(out, line)
		}
	}
	return out
}

func capturingLogger() (*slog.Logger, *lockedBuffer) {
	buffer := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})), buffer
}

const refusalTestOrg = "0123456789abcdef"

func TestARefusedBYODialIsNamedInOneWarnLineWithNoEndpointText(t *testing.T) {
	address, accepted := loopbackListener(t)
	host, port, _ := net.SplitHostPort(address)
	logger, logs := capturingLogger()
	cfg := byoConfig("https://"+address+"/v1", true)
	cfg.Logger, cfg.OrgID = logger, refusalTestOrg

	if err := callOnce(t, cfg); err == nil {
		t.Fatal("a public-only call to a loopback base_url succeeded")
	}
	if accepted.Load() != 0 {
		t.Fatalf("the listener accepted %d connections, want 0", accepted.Load())
	}
	named := refusalLines(logs.lines())
	if len(named) == 0 || len(named) != len(logs.lines()) {
		t.Fatalf("log lines = %q, want one or more refusal lines (one per refused dial) and nothing else", logs.lines())
	}
	for _, line := range named {
		for _, want := range []string{"level=WARN", "reason=non_public_address", "org_id=01234567"} {
			if !strings.Contains(line, want) {
				t.Fatalf("a refusal line lacks %q: %s", want, line)
			}
		}
	}
	for _, line := range logs.lines() {
		for _, forbidden := range []string{host, port, address, "/v1", "127.0.0.1", "localhost", "sk-synthetic", "89abcdef", "0123456789"} {
			if strings.Contains(line, forbidden) {
				t.Fatalf("a log line holds %q: %s", forbidden, line)
			}
		}
	}
}

func TestANonPublicBYOEndpointAllowedByTheDeploymentEmitsNoRefusalLine(t *testing.T) {
	address, accepted := loopbackListener(t)
	logger, logs := capturingLogger()
	cfg := byoConfig("https://"+address+"/v1", false)
	cfg.Logger, cfg.OrgID = logger, refusalTestOrg

	_ = callOnce(t, cfg)
	if accepted.Load() == 0 {
		t.Fatal("the control call never reached the loopback listener: it cannot tell a refusal from silence")
	}
	if named := refusalLines(logs.lines()); len(named) != 0 {
		t.Fatalf("a call that was allowed to dial emitted a refusal line: %q", named)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRefusalLoggingNamesOnlyANonPublicRefusal(t *testing.T) {
	refused := fmt.Errorf("Get %q: %w", "https://secret-host.invalid:8443/v1", outboundhttp.ErrNonPublicAddress)
	outage := errors.New("dial tcp: connection refused")
	for name, tc := range map[string]struct {
		org      string
		response *http.Response
		err      error
		want     int
		wantOrg  string
	}{
		"a public dial that succeeds":      {org: refusalTestOrg, response: &http.Response{StatusCode: 200}},
		"a network outage":                 {org: refusalTestOrg, err: outage},
		"a refusal":                        {org: refusalTestOrg, err: refused, want: 1, wantOrg: "org_id=01234567"},
		"a refusal, organization unknown":  {err: refused, want: 1},
		"a refusal, organization under 8":  {org: "org-1", err: refused, want: 1, wantOrg: "org_id=org-1 "},
		"a refusal, newline in the org id": {org: "ab\ncd\nefgh", err: refused, want: 1, wantOrg: "org_id=ab?cd?ef"},
	} {
		t.Run(name, func(t *testing.T) {
			logger, logs := capturingLogger()
			transport := refusalLogging{
				next:   roundTripperFunc(func(*http.Request) (*http.Response, error) { return tc.response, tc.err }),
				logger: logger, orgID: tc.org,
			}
			request, err := http.NewRequest(http.MethodPost, "https://secret-host.invalid:8443/v1/chat", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, gotErr := transport.RoundTrip(request)
			if response != tc.response || !errors.Is(gotErr, tc.err) || (tc.err == nil) != (gotErr == nil) {
				t.Fatalf("the wrapper changed the outcome: response %v err %v", response, gotErr)
			}
			named := refusalLines(logs.lines())
			if len(named) != tc.want || len(logs.lines()) != tc.want {
				t.Fatalf("lines = %q, want %d refusal line(s) and nothing else", logs.lines(), tc.want)
			}
			if tc.want == 0 {
				return
			}
			if strings.Contains(named[0], "secret-host") || strings.Contains(named[0], "8443") || strings.Contains(named[0], "/v1") {
				t.Fatalf("the line names the endpoint: %s", named[0])
			}
			if tc.wantOrg == "" && strings.Contains(named[0], "org_id") {
				t.Fatalf("an unknown organization still printed an org_id: %s", named[0])
			}
			if tc.wantOrg != "" && !strings.Contains(named[0]+" ", tc.wantOrg) {
				t.Fatalf("the line lacks %q: %s", tc.wantOrg, named[0])
			}
		})
	}
}
