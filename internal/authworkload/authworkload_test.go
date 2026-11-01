package authworkload

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/workloadtoken"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

const testIssuer = "https://auth.example.test"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// exchange is a fake auth-service exchange that records every subject token it receives and answers with a
// workload token for the audience asked.
type exchange struct {
	server   *httptest.Server
	mu       sync.Mutex
	subjects []string
	audience []string
}

func newExchange(t *testing.T) *exchange {
	t.Helper()
	e := &exchange{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		e.mu.Lock()
		n := len(e.subjects)
		e.subjects = append(e.subjects, form.Get("subject_token"))
		e.audience = append(e.audience, form.Get("audience"))
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:jwt","expires_in":300}`, workloadToken(form.Get("audience"), n))
	}))
	t.Cleanup(e.server.Close)
	return e
}

func (e *exchange) seen() (subjects, audiences []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.subjects...), append([]string(nil), e.audience...)
}

func workloadToken(audience string, n int) string {
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	header := `{"alg":"EdDSA","typ":"workload+jwt","kid":"test-key"}`
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"prn_AAAAAAAAAAAAAAAAAAAAAA","principal_type":"workload","jti":"%024d","iat":%d,"nbf":%d,"exp":%d}`,
		testIssuer, audience, n, now-5, now-5, now+295)
	return enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString(make([]byte, 64))
}

func subjectJWT(tag string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString([]byte(`{"sub":"`+tag+`"}`)) + "." + enc.EncodeToString([]byte("signature"))
}

func writeSubject(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func configFor(e *exchange, subjectFile string) config.AuthWorkloadConfig {
	return config.AuthWorkloadConfig{ExchangeURL: e.server.URL, AllowPlainHTTP: true, Issuer: testIssuer, SubjectFile: subjectFile}
}

func TestBuildDisabledReturnsNoClient(t *testing.T) {
	client, err := Build(config.AuthWorkloadConfig{}, quiet)
	if err != nil || client != nil {
		t.Fatalf("off must build nothing: client=%v err=%v", client, err)
	}
}

func TestBuildMakesNoExchangeAtStart(t *testing.T) {
	e := newExchange(t)
	subject := filepath.Join(t.TempDir(), "token")
	writeSubject(t, subject, subjectJWT("one"))
	client, err := Build(configFor(e, subject), quiet)
	if err != nil || client == nil {
		t.Fatalf("Build: client=%v err=%v", client, err)
	}
	if subjects, _ := e.seen(); len(subjects) != 0 {
		t.Fatalf("Build made %d exchange(s); it must make none", len(subjects))
	}
}

func TestBuildDoesNotNeedTheSubjectFileOrTheExchangeAtStart(t *testing.T) {
	e := newExchange(t)
	cfg := configFor(e, filepath.Join(t.TempDir(), "absent"))
	e.server.Close()
	client, err := Build(cfg, quiet)
	if err != nil || client == nil {
		t.Fatalf("an auth-service outage or a late token must not stop the build: client=%v err=%v", client, err)
	}
	if _, err := client.Token(context.Background(), "dev-health-api"); !errors.Is(err, workloadtoken.ErrNoUsableToken) {
		t.Fatalf("with no exchange the token must be unusable, not a fallback: %v", err)
	}
}

func TestBuildFailsClosedOnABadConfig(t *testing.T) {
	good := config.AuthWorkloadConfig{ExchangeURL: "https://auth-service.example.test", Issuer: testIssuer, SubjectFile: "/var/run/secrets/dev-health/auth-workload/token"}
	for name, mutate := range map[string]func(*config.AuthWorkloadConfig){
		"no issuer":               func(c *config.AuthWorkloadConfig) { c.Issuer = "" },
		"http issuer":             func(c *config.AuthWorkloadConfig) { c.Issuer = "http://auth.example.test" },
		"no exchange url":         func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "" },
		"http without the allow":  func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "http://auth-service.example.test" },
		"url with a path":         func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "https://auth-service.example.test/internal" },
		"no subject":              func(c *config.AuthWorkloadConfig) { c.SubjectFile = "" },
		"two subjects":            func(c *config.AuthWorkloadConfig) { c.BootstrapFile = "/run/secrets/bootstrap" },
		"port above range":        func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "https://auth-service.example.test:99999" },
		"empty host label":        func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "https://auth..service" },
		"issuer port above range": func(c *config.AuthWorkloadConfig) { c.Issuer = "https://auth.example.test:99999" },
		"issuer empty host label": func(c *config.AuthWorkloadConfig) { c.Issuer = "https://auth..example" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := good
			mutate(&cfg)
			client, err := Build(cfg, quiet)
			if err == nil || client != nil {
				t.Fatalf("a bad config must fail the build: client=%v err=%v", client, err)
			}
			if strings.Contains(err.Error(), "example.test") || strings.Contains(err.Error(), "/var/run") {
				t.Fatalf("the error must not echo a configured value: %v", err)
			}
		})
	}
	if _, err := Build(good, quiet); err != nil {
		t.Fatalf("the good config must build: %v", err)
	}
}

func TestTransportIsExplicitAndNeverTheProxyOne(t *testing.T) {
	rt := newTransport()
	if err := workloadtoken.CheckTransport(rt); err != nil {
		t.Fatal(err)
	}
	if rt == http.DefaultTransport {
		t.Fatal("the exchange transport is http.DefaultTransport")
	}
	transport, ok := rt.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("the exchange transport must be an *http.Transport with no Proxy: %T", rt)
	}
	other := newTransport()
	if other == rt {
		t.Fatal("each client gets its own transport: its credential must not share a pool with another client")
	}
}

func TestSubjectFileIsReadAgainForEveryExchange(t *testing.T) {
	e := newExchange(t)
	subject := filepath.Join(t.TempDir(), "token")
	first, second := subjectJWT("first"), subjectJWT("second")
	writeSubject(t, subject, first)
	client, err := Build(configFor(e, subject), quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.Token(ctx, "dev-health-api"); err != nil {
		t.Fatal(err)
	}
	writeSubject(t, subject, second)
	if _, err := client.Token(ctx, "auth-service"); err != nil {
		t.Fatal(err)
	}
	client.Invalidate("dev-health-api")
	if _, err := client.Token(ctx, "dev-health-api"); err != nil {
		t.Fatal(err)
	}
	subjects, audiences := e.seen()
	if len(subjects) != 3 || subjects[0] != first || subjects[1] != second || subjects[2] != second {
		t.Fatalf("each exchange must present the file as it is now: audiences=%v subjects=%q", audiences, subjects)
	}
}

func TestBootstrapFileIsTheSubjectWhenNamed(t *testing.T) {
	e := newExchange(t)
	bootstrap := filepath.Join(t.TempDir(), "bootstrap")
	value := "fcwlb_" + strings.Repeat("A", 43)
	writeSubject(t, bootstrap, value)
	cfg := configFor(e, "")
	cfg.BootstrapFile = bootstrap
	client, err := Build(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Token(context.Background(), "auth-service"); err != nil {
		t.Fatal(err)
	}
	if subjects, _ := e.seen(); len(subjects) != 1 || subjects[0] != value {
		t.Fatalf("the bootstrap credential must be the subject: %q", subjects)
	}
}
