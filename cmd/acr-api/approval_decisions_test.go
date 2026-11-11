package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
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
