package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// deploymentProfile is the deployment-of-record's effective configuration
// (spec §4). It is required; there is no default. It never holds a
// credential value.
type deploymentProfile struct {
	Schema                          string            `json:"schema"`
	DeploymentID                    string            `json:"deployment_id"`
	DeployedSourceRevision          string            `json:"deployed_source_revision"`
	ImageDigest                     string            `json:"image_digest"`
	ConfigSnapshotAt                string            `json:"config_snapshot_at"`
	SourceRevisionRelation          string            `json:"source_revision_relation"`
	RelationEvidence                string            `json:"relation_evidence"`
	ACRRequestTimeout               string            `json:"acr_request_timeout"`
	ModelTimeout                    string            `json:"model_timeout"`
	ModelMaxAttempts                *int              `json:"model_max_attempts"`
	ModelMaxTransportRetries        *int              `json:"model_max_transport_retries"`
	SynthesisMaxResynthesisAttempts *int              `json:"synthesis_max_resynthesis_attempts"`
	Provider                        *string           `json:"provider"`
	BaseURLResolved                 *string           `json:"base_url_resolved"`
	Model                           *string           `json:"model"`
	FallbackModel                   *string           `json:"fallback_model"`
	AllowInsecureBaseURL            *bool             `json:"allow_insecure_base_url"`
	PhrasingModel                   *string           `json:"phrasing_model"`
	OrgModelResolution              *orgResolution    `json:"org_model_resolution"`
	CredentialSourceConfirmed       *bool             `json:"credential_source_confirmed"`
	Sources                         map[string]string `json:"sources"`
}

type orgResolution struct {
	Enabled            *bool   `json:"enabled"`
	CaptureOrgOverride *bool   `json:"capture_org_override"`
	ConfigGeneration   *string `json:"config_generation"`
	Provider           *string `json:"provider"`
	BaseURL            *string `json:"base_url"`
	Model              *string `json:"model"`
	Fallback           *string `json:"fallback"`
}

const profileSchema = "gocapture.deployment-profile.v1"

// Environment names the profile's tuning must agree with. The synthesis
// knob is hosted.EnvSynthesisResynthesisAttempts (not imported: hosted
// pulls in the whole service).
const (
	envRequestTimeout       = "ACR_REQUEST_TIMEOUT"
	envSynthesisResynthesis = "ACR_CONTEXT_FABRIC_SYNTHESIS_MAX_RESYNTHESIS_ATTEMPTS"
)

type effectiveTuning struct {
	RequestTimeout      time.Duration
	ModelTimeout        time.Duration
	MaxAttempts         int
	MaxTransportRetries int
	Resynthesis         int
}

func loadProfile(path string) (deploymentProfile, []byte, error) {
	if path == "" {
		return deploymentProfile{}, nil, errors.New("--deployment-profile is required: there is no default profile (spec §4)")
	}
	data, err := readPrivateFile(path)
	if err != nil {
		return deploymentProfile{}, nil, fmt.Errorf("deployment profile: %w", err)
	}
	var profile deploymentProfile
	if err := strictUnmarshal(data, &profile); err != nil {
		return deploymentProfile{}, nil, fmt.Errorf("deployment profile: %w", err)
	}
	return profile, data, nil
}

func (p deploymentProfile) validate(allow allowlist, sourcePin string) (effectiveTuning, error) {
	var tuning effectiveTuning
	if p.Schema != profileSchema {
		return tuning, fmt.Errorf("profile schema must be %s", profileSchema)
	}
	for name, value := range map[string]string{
		"deployment_id": p.DeploymentID, "deployed_source_revision": p.DeployedSourceRevision,
		"image_digest": p.ImageDigest, "config_snapshot_at": p.ConfigSnapshotAt,
	} {
		if strings.TrimSpace(value) == "" {
			return tuning, fmt.Errorf("profile %s is required", name)
		}
	}
	if _, err := time.Parse(time.RFC3339, p.ConfigSnapshotAt); err != nil {
		return tuning, errors.New("profile config_snapshot_at must be RFC 3339")
	}
	switch p.SourceRevisionRelation {
	case "equal_to_pin":
		if p.DeployedSourceRevision != sourcePin {
			return tuning, errors.New("profile says equal_to_pin but deployed_source_revision differs from --source-pin")
		}
	case "interpreter_tree_equal":
		if strings.TrimSpace(p.RelationEvidence) == "" {
			return tuning, errors.New("interpreter_tree_equal needs relation_evidence")
		}
	default:
		return tuning, errors.New("profile source_revision_relation must be equal_to_pin or interpreter_tree_equal")
	}
	var err error
	if tuning.RequestTimeout, err = positiveDuration("acr_request_timeout", p.ACRRequestTimeout); err != nil {
		return tuning, err
	}
	if tuning.ModelTimeout, err = positiveDuration("model_timeout", p.ModelTimeout); err != nil {
		return tuning, err
	}
	// Production's own ranges (modelprovider/config.go validate).
	if tuning.ModelTimeout < time.Second || tuning.ModelTimeout > 2*time.Minute {
		return tuning, errors.New("profile model_timeout must be from 1s to 2m (production range)")
	}
	if tuning.MaxAttempts, err = boundedInt("model_max_attempts", p.ModelMaxAttempts, 1, 3); err != nil {
		return tuning, err
	}
	if tuning.MaxTransportRetries, err = boundedInt("model_max_transport_retries", p.ModelMaxTransportRetries, 0, 5); err != nil {
		return tuning, err
	}
	if tuning.Resynthesis, err = boundedInt("synthesis_max_resynthesis_attempts", p.SynthesisMaxResynthesisAttempts, 1, genkitruntime.MaxSynthesisResynthesisAttemptsCeiling); err != nil {
		return tuning, err
	}
	if p.Provider == nil || *p.Provider != allow.Provider {
		return tuning, errors.New("profile provider is outside the allowlist")
	}
	if p.BaseURLResolved == nil || *p.BaseURLResolved != allow.BaseURL {
		return tuning, errors.New("profile base_url_resolved is outside the allowlist")
	}
	if p.Model == nil || *p.Model != allow.Model {
		return tuning, errors.New("profile model is outside the allowlist")
	}
	if p.FallbackModel == nil || *p.FallbackModel != "" {
		return tuning, errors.New("profile fallback_model must be present and empty")
	}
	if p.PhrasingModel == nil || *p.PhrasingModel != "" {
		return tuning, errors.New("profile phrasing_model must be present and empty")
	}
	if p.AllowInsecureBaseURL == nil || *p.AllowInsecureBaseURL != allow.Insecure {
		return tuning, errors.New("profile allow_insecure_base_url is outside the allowlist")
	}
	org := p.OrgModelResolution
	if org == nil || org.Enabled == nil || org.CaptureOrgOverride == nil {
		return tuning, errors.New("profile org_model_resolution.enabled and capture_org_override are required")
	}
	if *org.CaptureOrgOverride {
		if !*org.Enabled || org.ConfigGeneration == nil || *org.ConfigGeneration == "" ||
			org.Provider == nil || *org.Provider != allow.Provider || org.BaseURL == nil || *org.BaseURL != allow.BaseURL ||
			org.Model == nil || *org.Model != allow.Model || org.Fallback == nil || *org.Fallback != "" {
			return tuning, errors.New("the capture org override must name the allowlisted identity with no fallback")
		}
	}
	if p.CredentialSourceConfirmed == nil || !*p.CredentialSourceConfirmed {
		return tuning, errors.New("profile credential_source_confirmed must be true")
	}
	for _, key := range []string{"acr_request_timeout", "model_timeout", "model_max_attempts", "model_max_transport_retries", "synthesis_max_resynthesis_attempts", "provider", "base_url_resolved", "model"} {
		if strings.TrimSpace(p.Sources[key]) == "" {
			return tuning, fmt.Errorf("profile sources.%s (a citation) is required", key)
		}
	}
	return tuning, nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("profile %s must be a positive Go duration", name)
	}
	return d, nil
}

func boundedInt(name string, value *int, lo, hi int) (int, error) {
	if value == nil || *value < lo || *value > hi {
		return 0, fmt.Errorf("profile %s must be an integer from %d to %d", name, lo, hi)
	}
	return *value, nil
}

// checkEnvAgrees refuses when a tuning variable is set and differs from the
// profile. A malformed set value is refused too, never defaulted.
func checkEnvAgrees(lookup func(string) (string, bool), tuning effectiveTuning) error {
	durations := map[string]time.Duration{envRequestTimeout: tuning.RequestTimeout, modelprovider.EnvTimeout: tuning.ModelTimeout}
	for name, want := range durations {
		if raw, ok := lookup(name); ok && strings.TrimSpace(raw) != "" {
			got, err := time.ParseDuration(strings.TrimSpace(raw))
			if err != nil || got != want {
				return fmt.Errorf("%s is set and differs from the deployment profile", name)
			}
		}
	}
	ints := map[string]int{
		modelprovider.EnvMaxAttempts: tuning.MaxAttempts, modelprovider.EnvMaxTransportRetries: tuning.MaxTransportRetries,
		envSynthesisResynthesis: tuning.Resynthesis,
	}
	for name, want := range ints {
		if raw, ok := lookup(name); ok && strings.TrimSpace(raw) != "" {
			got, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || got != want {
				return fmt.Errorf("%s is set and differs from the deployment profile", name)
			}
		}
	}
	return nil
}

// applyTuning sets every tuning knob from the profile.
func applyTuning(cfg *modelprovider.Config, tuning effectiveTuning) {
	cfg.Timeout = tuning.ModelTimeout
	cfg.MaxAttempts = tuning.MaxAttempts
	cfg.MaxTransportRetries = tuning.MaxTransportRetries
	cfg.MaxSynthesisResynthesisAttempts = tuning.Resynthesis
}

// checkIdentity refuses a model configuration outside the allowlist. The
// base URL is compared RESOLVED: empty means DefaultBaseURL.
func checkIdentity(cfg modelprovider.Config, allow allowlist) error {
	base := cfg.BaseURL
	if base == "" {
		base = modelprovider.DefaultBaseURL
	}
	switch {
	case cfg.Provider != allow.Provider:
		return errors.New("configured provider is outside the allowlist")
	case base != allow.BaseURL:
		return errors.New("configured base URL is outside the allowlist")
	case cfg.Model != allow.Model:
		return errors.New("configured model is outside the allowlist")
	case cfg.FallbackModel != "":
		return errors.New("a fallback model is configured; it must be unset")
	case cfg.PhrasingModel != "":
		return errors.New("a phrasing model is configured; it must be unset")
	case cfg.AllowInsecureBaseURL != allow.Insecure:
		return errors.New("insecure base URL setting is outside the allowlist")
	case cfg.APIKey == "":
		return errors.New("no credential configured (set ACR_CONTEXT_FABRIC_MODEL_API_KEY or _FILE)")
	}
	return nil
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file (symlinks are refused)")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("file must not be readable by group or others (0600)")
	}
	return os.ReadFile(path)
}
