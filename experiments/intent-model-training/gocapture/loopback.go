//go:build gocapture_loopback

package main

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Test build only (build.sh --loopback): end-to-end runs against a local
// fake OpenAI-compatible provider. Never part of the production binary.
func init() {
	loopbackHook = func(cfg *runConfig) error {
		raw := os.Getenv("GOCAPTURE_LOOPBACK_BASE_URL")
		u, err := url.Parse(raw)
		if raw == "" || err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
			return errors.New("the loopback build needs GOCAPTURE_LOOPBACK_BASE_URL=http://127.0.0.1:<port>/v1/")
		}
		cfg.allow = &allowlist{Provider: "openai", BaseURL: raw, Model: productionAllowlist.Model, Insecure: true,
			Scheme: "http", Host: u.Host, Path: "/v1/chat/completions"}
		if ms, err := strconv.Atoi(os.Getenv("GOCAPTURE_LOOPBACK_LEDGER_DELAY_MS")); err == nil && ms > 0 {
			delay := time.Duration(ms) * time.Millisecond
			cfg.ledgerHook = func(l *approvalLedger) {
				l.failAppend = func() error { time.Sleep(delay); return nil }
			}
		}
		return nil
	}
}
