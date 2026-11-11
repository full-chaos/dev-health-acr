package config

import (
	"strings"
)

// DeviceApprovalPDPConfig (CHAOS-13586) is whether a device or consent approval asks the policy decision point
// (auth-service POST /v1/authorize) about its approver. Mode is ACR_DEVICE_APPROVAL_PDP: off (default), shadow
// (ask and record, never refuse) or enforce (refuse a deny with 403 and a failure with 503). URL is
// ACR_DEVICE_APPROVAL_PDP_URL, auth-service's origin; the call carries the acr-api workload token
// (ACR_AUTH_WORKLOAD_*). The setting is optional, so it never stops acr-api from starting (product rule 3): an
// unknown mode is off, and shadow or enforce with a missing or malformed URL or no workload client has no client;
// each logs one ERROR at start, and enforce then refuses every approval (fail closed).
type DeviceApprovalPDPConfig struct {
	Mode string
	URL  string
}

// Configured reports whether the client can be built: a well-formed URL and the workload client that mints its
// token.
func (d DeviceApprovalPDPConfig) Configured(workload AuthWorkloadConfig) bool {
	return d.URL != "" && workload.Enabled() && validateExchangeURL(d.URL, workload.AllowPlainHTTP) == nil
}

func deviceApprovalPDPValue(lookup lookupEnv) DeviceApprovalPDPConfig {
	return DeviceApprovalPDPConfig{
		Mode: strings.TrimSpace(stringValue(lookup, "ACR_DEVICE_APPROVAL_PDP", "")),
		URL:  strings.TrimSpace(stringValue(lookup, "ACR_DEVICE_APPROVAL_PDP_URL", "")),
	}
}
