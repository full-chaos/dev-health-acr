package config

import (
	"strings"
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

func TestDeviceApprovalPDPRefusesAnUnknownModeWithoutEchoingIt(t *testing.T) {
	_, err := load(dataQueryEnv(map[string]string{"ACR_DEVICE_APPROVAL_PDP": "secret-mode"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_DEVICE_APPROVAL_PDP") || strings.Contains(err.Error(), "secret-mode") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceApprovalPDPRefusesAMalformedURL(t *testing.T) {
	_, err := load(dataQueryEnv(map[string]string{"ACR_DEVICE_APPROVAL_PDP": "enforce", "ACR_DEVICE_APPROVAL_PDP_URL": "https://secret-host.example.test/path"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_DEVICE_APPROVAL_PDP_URL") || strings.Contains(err.Error(), "secret-host") {
		t.Fatalf("err = %v", err)
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
