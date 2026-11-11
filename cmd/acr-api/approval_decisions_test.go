package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13586: off builds nothing; shadow or enforce with no PDP client still
// starts, with one ERROR line naming what is missing.
func TestApprovalDecisions_offIsNilAndOnWithoutAClientStarts(t *testing.T) {
	off, err := approvalDecisions(config.Config{}, slog.Default())
	if err != nil || off != nil {
		t.Fatalf("off = %v, %v; want nil, nil", off, err)
	}
	logs := &bytes.Buffer{}
	on, err := approvalDecisions(config.Config{DeviceApprovalPDP: config.DeviceApprovalPDPConfig{Mode: "enforce"}}, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil || on == nil {
		t.Fatalf("enforce without a client = %v, %v; want a check and no error", on, err)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "ACR_DEVICE_APPROVAL_PDP_URL") {
		t.Fatalf("no ERROR line naming the missing setting:\n%s", logs.String())
	}
}

// acp (1): an unknown mode never refuses the start and never passes as
// enforce: off, one ERROR naming the key and the allowed values, and the
// effective-mode line says off.
func TestApprovalDecisions_unknownModeStartsOffWithOneError(t *testing.T) {
	logs := &bytes.Buffer{}
	got, err := approvalDecisions(config.Config{DeviceApprovalPDP: config.DeviceApprovalPDPConfig{Mode: "enforced"}}, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil || got != nil {
		t.Fatalf("unknown mode = %v, %v; want off (nil) and no error", got, err)
	}
	if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || !strings.Contains(logs.String(), `"key":"ACR_DEVICE_APPROVAL_PDP"`) ||
		!strings.Contains(logs.String(), `"allowed":"off,shadow,enforce"`) || strings.Contains(logs.String(), "enforced") {
		t.Fatalf("want one ERROR naming the key and the allowed values, never the value:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `"msg":"device approval decision mode","mode":"off"`) {
		t.Fatalf("no effective-mode line:\n%s", logs.String())
	}
}

// acp (1): enforce with a malformed URL starts, logs one ERROR, says enforce,
// and refuses each approval with the 503 error.
func TestApprovalDecisions_enforceWithABadURLStartsAndFailsClosed(t *testing.T) {
	logs := &bytes.Buffer{}
	cfg := config.Config{
		DeviceApprovalPDP: config.DeviceApprovalPDPConfig{Mode: "enforce", URL: "https://pdp.example.test/path"},
		AuthWorkload: config.AuthWorkloadConfig{ExchangeURL: "https://auth.example.test", Issuer: "https://auth.example.test",
			SubjectFile: "/var/run/secrets/token"},
	}
	got, err := approvalDecisions(cfg, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil || got == nil {
		t.Fatalf("enforce with a bad URL = %v, %v; want a check and no error", got, err)
	}
	if !strings.Contains(logs.String(), `"msg":"device approval decision mode","mode":"enforce"`) || strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
		t.Fatalf("want the enforce line and one ERROR:\n%s", logs.String())
	}
	principal := storage.Principal{Subject: "user_1", OrgID: "3f9a1c77-2e4b-4d10-b5c8-6a7e9d0b1c23", ApproverReference: strings.Join([]string{"eyJhbGciOiJFZERTQSJ9", "eyJzdWIiOiJ1In0", "c2lnbmF0dXJlLXNpZ24"}, ".")}
	if err := got.Check(context.Background(), principal, []string{"*"}); !errors.Is(err, auth.ErrApprovalDecisionUnavailable) {
		t.Fatalf("Check = %v; want the 503 error", err)
	}
}
