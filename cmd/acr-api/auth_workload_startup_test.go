package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/config"
)

// workloadStartupConfig is a development config with the ACR_AUTH_WORKLOAD_* variables set and read through the
// real loader, as acr-api reads them.
func workloadStartupConfig(t *testing.T, exchangeURL, issuer string) config.Config {
	t.Helper()
	cfg := developmentServeConfig(t)
	cfg.RequireBackingStores = false
	t.Setenv("ACR_AUTH_WORKLOAD_EXCHANGE_URL", exchangeURL)
	t.Setenv("ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP", "true")
	t.Setenv("ACR_AUTH_WORKLOAD_ISSUER", issuer)
	t.Setenv("ACR_AUTH_WORKLOAD_SUBJECT_FILE", t.TempDir()+"/token")
	loaded, err := config.LoadAuthWorkload(func(key string) (string, bool) {
		return os.LookupEnv(key)
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthWorkload = loaded
	return cfg
}

func prepareWithWorkload(t *testing.T, cfg config.Config) (serverCreated bool, readiness []string, err error) {
	t.Helper()
	_, closeRuntime, err := prepareServer(context.Background(), serverBuildRequest{
		config: cfg, logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), serviceVersion: "test",
		newServer: func(api.ServerConfig, http.Handler, *slog.Logger) (serverRunner, error) {
			serverCreated = true
			return nil, nil
		},
	})
	if closeRuntime != nil {
		defer func() { _ = closeRuntime() }()
	}
	deps, depErr := developmentDependencies(cfg, "test", slog.Default())
	if depErr != nil {
		t.Fatal(depErr)
	}
	for _, check := range deps.ReadinessChecks {
		readiness = append(readiness, check.Name())
	}
	return serverCreated, readiness, err
}

func TestPrepareServerRefusesABadAuthWorkloadConfig(t *testing.T) {
	cfg := workloadStartupConfig(t, "http://127.0.0.1:1", "https://auth.example.test")
	cfg.AuthWorkload.Issuer = "http://auth.example.test"
	created, _, err := prepareWithWorkload(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "auth workload") {
		t.Fatalf("a bad auth workload config must stop the start naming it: %v", err)
	}
	if created {
		t.Fatal("the server was created after the auth workload client failed to build")
	}
	if strings.Contains(err.Error(), "example.test") {
		t.Fatalf("the error must not echo a configured value: %v", err)
	}
}

func TestPrepareServerMakesNoExchangeAtStart(t *testing.T) {
	var calls atomic.Int64
	exchange := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer exchange.Close()
	cfg := workloadStartupConfig(t, exchange.URL, "https://auth.example.test")
	created, readiness, err := prepareWithWorkload(t, cfg)
	if err != nil || !created {
		t.Fatalf("a good auth workload config must start: created=%v err=%v", created, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("acr-api made %d exchange call(s) at start; it must make none", calls.Load())
	}
	for _, name := range readiness {
		if strings.Contains(name, "workload") || strings.Contains(name, "auth") {
			t.Fatalf("readiness check %q ties acr-api's readiness to the exchange", name)
		}
	}
}

func TestPrepareServerStartsWhileAuthServiceIsDown(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	cfg := workloadStartupConfig(t, url, "https://auth.example.test")
	created, _, err := prepareWithWorkload(t, cfg)
	if err != nil || !created {
		t.Fatalf("an auth-service outage must not stop acr-api: created=%v err=%v", created, err)
	}
}

// A config built around the loader (a malformed URL a loader change let through) still stops the start.
func TestPrepareServerRefusesMalformedAuthWorkloadURLs(t *testing.T) {
	for name, mutate := range map[string]func(*config.AuthWorkloadConfig){
		"exchange port above range": func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "https://auth-service.example.test:99999" },
		"exchange empty host label": func(c *config.AuthWorkloadConfig) { c.ExchangeURL = "https://auth..service" },
		"issuer port above range":   func(c *config.AuthWorkloadConfig) { c.Issuer = "https://auth.example.test:99999" },
		"issuer empty host label":   func(c *config.AuthWorkloadConfig) { c.Issuer = "https://auth..service" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := workloadStartupConfig(t, "https://auth-service.example.test", "https://auth.example.test")
			cfg.AuthWorkload.AllowPlainHTTP = false
			mutate(&cfg.AuthWorkload)
			created, _, err := prepareWithWorkload(t, cfg)
			if err == nil || created {
				t.Fatalf("a malformed URL must stop the start: created=%v err=%v", created, err)
			}
		})
	}
}

// The start-up path is the one validator of both URLs: every cell of the corpus (the r1 table, the malformed forms and
// the valid ones: trailing slash, IPv6, a single-label service name, host:port) is decided at prepareServer.
func TestPrepareServerDecidesTheURLCorpus(t *testing.T) {
	cells, err := os.ReadFile("../../internal/config/testdata/auth_workload_urls.tsv")
	if err != nil {
		t.Fatal(err)
	}
	base := workloadStartupConfig(t, "https://auth-service.example.test", "https://auth.example.test")
	n := 0
	for _, line := range strings.Split(string(cells), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		field, plain, value, verdict := parts[0], parts[1] == "true", parts[2], parts[3]
		cfg := base
		cfg.AuthWorkload = config.AuthWorkloadConfig{ExchangeURL: "https://auth-service.example.test", Issuer: "https://auth.example.test", SubjectFile: base.AuthWorkload.SubjectFile, AllowPlainHTTP: plain}
		if field == "issuer" {
			cfg.AuthWorkload.Issuer = value
		} else {
			cfg.AuthWorkload.ExchangeURL = value
		}
		created, _, err := prepareWithWorkload(t, cfg)
		n++
		if verdict == "accept" && (err != nil || !created) {
			t.Errorf("%s %q must start: created=%v err=%v", field, value, created, err)
		}
		if verdict == "refuse" && (err == nil || created) {
			t.Errorf("%s %q must refuse the start: created=%v", field, value, created)
		}
	}
	if n < 100 {
		t.Fatalf("read %d cells", n)
	}
}
