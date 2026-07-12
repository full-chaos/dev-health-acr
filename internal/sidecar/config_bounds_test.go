package sidecar

import (
	"testing"
	"time"
)

// This file covers the numeric/duration bounds (timeout, response/request
// size ceilings) and the proxy URL override. URL shape/scheme and CA-bundle
// tests live in their own files.

func TestLoadConfigParsesTimeoutOverride(t *testing.T) {
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:  "https://acr.example.com",
		TimeoutEnvironment: "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 5*time.Second {
		t.Fatalf("unexpected timeout: %s", cfg.Timeout)
	}
}

func TestLoadConfigRejectsInvalidDuration(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:  "https://acr.example.com",
		TimeoutEnvironment: "not-a-duration",
	})); err == nil {
		t.Fatal("invalid duration was accepted")
	}
}

func TestLoadConfigRejectsTimeoutOutOfRange(t *testing.T) {
	cases := []string{"0s", "-1s", "1h"}
	for _, value := range cases {
		if _, err := loadConfig(lookupFromMap(map[string]string{
			APIURLEnvironment:  "https://acr.example.com",
			TimeoutEnvironment: value,
		})); err == nil {
			t.Fatalf("out-of-range timeout %q was accepted", value)
		}
	}
}

func TestLoadConfigRejectsSizeLimitsOutOfRange(t *testing.T) {
	cases := map[string]string{
		MaxResponseBytesEnvironment:    "1",
		MaxRequestBodyBytesEnvironment: "1",
	}
	for key, value := range cases {
		if _, err := loadConfig(lookupFromMap(map[string]string{
			APIURLEnvironment: "https://acr.example.com",
			key:               value,
		})); err == nil {
			t.Fatalf("out-of-range %s=%q was accepted", key, value)
		}
	}
}

func TestLoadConfigParsesProxyURL(t *testing.T) {
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:   "https://acr.example.com",
		ProxyURLEnvironment: "http://proxy.local:3128",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyURL == nil || cfg.ProxyURL.Host != "proxy.local:3128" {
		t.Fatalf("unexpected proxy URL: %#v", cfg.ProxyURL)
	}
}

func TestLoadConfigRejectsInvalidProxyURL(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:   "https://acr.example.com",
		ProxyURLEnvironment: "://not-a-url",
	})); err == nil {
		t.Fatal("invalid proxy URL was accepted")
	}
}
