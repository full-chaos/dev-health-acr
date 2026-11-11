package config

import (
	"errors"
	"strings"
)

// DeviceApprovalPDPConfig (CHAOS-13586) is whether a device or consent approval asks the policy decision point
// (auth-service POST /v1/authorize) about its approver. Mode is ACR_DEVICE_APPROVAL_PDP: off (default), shadow
// (ask and record, never refuse) or enforce (refuse a deny with 403 and a failure with 503). URL is
// ACR_DEVICE_APPROVAL_PDP_URL, auth-service's origin; the call carries the acr-api workload token
// (ACR_AUTH_WORKLOAD_*). shadow or enforce without URL or workload client still starts: acr-api logs one ERROR and
// enforce refuses every approval (fail closed).
type DeviceApprovalPDPConfig struct {
	Mode string
	URL  string
}

// Configured reports whether the client can be built: a URL and the workload client that mints its token.
func (d DeviceApprovalPDPConfig) Configured(workload AuthWorkloadConfig) bool {
	return d.URL != "" && workload.Enabled()
}

// Validate refuses an unknown mode or a malformed URL. Errors name the variable, never its value. A missing URL or
// workload client is not refused here (see the type comment).
func (d DeviceApprovalPDPConfig) Validate(workload AuthWorkloadConfig) error {
	switch d.Mode {
	case "", "off", "shadow", "enforce":
	default:
		return errors.New("ACR_DEVICE_APPROVAL_PDP must be off, shadow or enforce")
	}
	if d.URL != "" {
		if err := validateExchangeURL(d.URL, workload.AllowPlainHTTP); err != nil {
			return errors.New("ACR_DEVICE_APPROVAL_PDP_URL is invalid: " + err.Error())
		}
	}
	return nil
}

func deviceApprovalPDPValue(lookup lookupEnv) DeviceApprovalPDPConfig {
	return DeviceApprovalPDPConfig{
		Mode: strings.TrimSpace(stringValue(lookup, "ACR_DEVICE_APPROVAL_PDP", "")),
		URL:  strings.TrimSpace(stringValue(lookup, "ACR_DEVICE_APPROVAL_PDP_URL", "")),
	}
}
