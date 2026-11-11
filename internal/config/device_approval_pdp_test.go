package config

import (
	"testing"
)

// CHAOS-13586: ACR_DEVICE_APPROVAL_PDP and its URL.

func TestDeviceApprovalPDPUnsetIsOff(t *testing.T) {
	cfg, err := load(dataQueryEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceApprovalPDP != (DeviceApprovalPDPConfig{}) {
		t.Fatalf("unset must be the zero config: %+v", cfg.DeviceApprovalPDP)
	}
}

// The setting is optional: an unknown mode or a malformed URL never stops the
// load (product rule 3); acr-api logs it at start and the check stays off or
// fails closed.
func TestDeviceApprovalPDPUnknownModeAndMalformedURLStillLoad(t *testing.T) {
	cfg, err := load(dataQueryEnv(map[string]string{"ACR_DEVICE_APPROVAL_PDP": "enforced", "ACR_DEVICE_APPROVAL_PDP_URL": "https://pdp.example.test/path"}))
	if err != nil {
		t.Fatalf("load refused: %v", err)
	}
	if cfg.DeviceApprovalPDP.Mode != "enforced" || cfg.DeviceApprovalPDP.Configured(cfg.AuthWorkload) {
		t.Fatalf("%+v", cfg.DeviceApprovalPDP)
	}
}

// Missing URL or workload client is not a start refusal (product rule 3): the
// approval check logs an ERROR and enforce refuses each approval instead.
func TestDeviceApprovalPDPOnWithoutURLOrWorkloadStillLoads(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		cfg, err := load(dataQueryEnv(map[string]string{"ACR_DEVICE_APPROVAL_PDP": mode}))
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if cfg.DeviceApprovalPDP.Mode != mode || cfg.DeviceApprovalPDP.Configured(cfg.AuthWorkload) {
			t.Fatalf("%s: %+v", mode, cfg.DeviceApprovalPDP)
		}
	}
}
