package modelprovider

import (
	"fmt"
	"net/http"
	"strings"

	"context"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/llm"
)

func loopbackListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	return listener.Addr().String(), &accepted
}

func callOnce(t *testing.T, cfg Config) error {
	t.Helper()
	clients, err := newLLMClients(cfg, cfg.Model)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, callErr := clients[cfg.Model].Complete(ctx, llm.Request{Prompt: "ping"})
	time.Sleep(150 * time.Millisecond)
	return callErr
}

func byoConfig(baseURL string, publicOnly bool) Config {
	return Config{
		Provider: "openai", Model: "org-model", APIKey: llm.NewSecret("sk-synthetic-public-only-0000"),
		BaseURL: baseURL, Timeout: 3 * time.Second, MaxAttempts: 1, PublicEndpointsOnly: publicOnly,
	}
}

// The guard and its control: the same config reaches a loopback listener when
// PublicEndpointsOnly is false (so the listener can tell a refusal from
// silence) and never does when it is true.
func TestAPublicEndpointsOnlyClientNeverConnectsToALoopbackBaseURL(t *testing.T) {
	address, accepted := loopbackListener(t)
	if err := callOnce(t, byoConfig("https://"+address+"/v1", false)); err == nil {
		t.Fatal("the control call must fail its TLS handshake against a listener that closes: it reached it")
	}
	if accepted.Load() == 0 {
		t.Fatal("the control call never reached the loopback listener: the counter cannot tell a refusal from silence")
	}
	before := accepted.Load()
	if err := callOnce(t, byoConfig("https://"+address+"/v1", true)); err == nil {
		t.Fatal("a public-only call to a loopback base_url succeeded")
	}
	if got := accepted.Load() - before; got != 0 {
		t.Fatalf("a public-only client connected to the loopback listener %d times, want 0", got)
	}
}

func TestAPublicEndpointsOnlyClientRefusesAHostnameThatResolvesToLoopback(t *testing.T) {
	address, accepted := loopbackListener(t)
	_, port, _ := net.SplitHostPort(address)
	if addresses, err := net.LookupHost("localhost"); err != nil || len(addresses) == 0 {
		t.Skipf("localhost does not resolve here: %v", err)
	}
	_ = callOnce(t, byoConfig("https://localhost:"+port+"/v1", true))
	if accepted.Load() != 0 {
		t.Fatalf("a name that resolves to loopback was dialled %d times, want 0", accepted.Load())
	}
}

// The transport of the call is chosen by the config alone: a BYO config with
// no base_url (the provider's own endpoint) is held to the public rule with
// the same transport, the platform config (ConfigFromEnv) is not, and a
// credential-free endpoint still gets no Authorization header whichever
// transport it uses.
func TestThePublicEndpointRuleFollowsTheConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg     Config
		public  bool
		keyless bool
	}{
		"BYO with a base_url":    {cfg: byoConfig("https://org.example.invalid/v1", true), public: true},
		"BYO with no base_url":   {cfg: Config{Provider: "anthropic", Model: "m", APIKey: llm.NewSecret("sk-x"), PublicEndpointsOnly: true}, public: true},
		"BYO without credential": {cfg: Config{Provider: "openai", Model: "m", BaseURL: "https://org.example.invalid/v1", PublicEndpointsOnly: true}, public: true, keyless: true},
		"platform (not BYO)":     {cfg: byoConfig("http://127.0.0.1:8000/v1", false)},
		"platform no credential": {cfg: Config{Provider: "openai", Model: "m", BaseURL: "http://127.0.0.1:8000/v1"}, keyless: true},
	} {
		t.Run(name, func(t *testing.T) {
			transport := newModelHTTPClient(tc.cfg).Transport
			wrapped, stripped := transport.(withoutAuthorization)
			if stripped {
				transport = wrapped.next
			}
			if stripped != tc.keyless {
				t.Fatalf("strips the Authorization header = %v, want %v", stripped, tc.keyless)
			}
			if got := transport == http.RoundTripper(outboundhttp.SharedPublicTransport()); got != tc.public {
				t.Fatalf("public-only transport = %v, want %v", got, tc.public)
			}
			if !tc.public && transport != http.RoundTripper(outboundhttp.SharedTransport()) {
				t.Fatal("a platform call must use the shared transport")
			}
		})
	}
}

// A refused call fails as the model client's own sanitized transport error:
// none of its printed forms (the text, %v, %+v, %s) names the address, the port
// or the url the organization stored. The client keeps the dialled scheme://host
// in a nested url.Error that only an error-chain walk reaches; nothing prints it.
func TestARefusedCallsPrintedErrorNamesNeitherAddressNorURL(t *testing.T) {
	address, accepted := loopbackListener(t)
	host, port, _ := net.SplitHostPort(address)
	err := callOnce(t, byoConfig("https://"+address+"/v1", true))
	if err == nil {
		t.Fatal("a public-only call to a loopback base_url succeeded")
	}
	if accepted.Load() != 0 {
		t.Fatalf("the listener accepted %d connections, want 0", accepted.Load())
	}
	for _, printed := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%s", err)} {
		for _, secret := range []string{host, port, address, "/v1", "sk-synthetic-public-only-0000"} {
			if strings.Contains(printed, secret) {
				t.Fatalf("the printed error holds %q: %s", secret, printed)
			}
		}
	}
}
