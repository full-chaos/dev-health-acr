package episode

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

func validateCreate(principal storage.Principal, create contractsv1.AgentEpisodeCreate) error {
	if strings.TrimSpace(principal.OrgID) == "" {
		return fmt.Errorf("invalid episode: authenticated organization is required")
	}
	if err := create.Validate(); err != nil {
		return fmt.Errorf("invalid episode: %w", err)
	}
	if !bounded(create.ClientEpisodeID, 1, 256) || !bounded(create.IdempotencyKey, 8, 256) || !bounded(create.ContextPacketID, 8, 256) || !bounded(create.Goal, 1, 4000) || !bounded(create.Summary, 1, 8000) || !bounded(create.TaskRef, 0, 1024) {
		return fmt.Errorf("invalid episode: identifier or summary exceeds v1 bounds")
	}
	if !bounded(create.Scope.Branch, 0, 512) || (create.Scope.CommitSHA != "" && !commitSHAPattern.MatchString(create.Scope.CommitSHA)) {
		return fmt.Errorf("invalid episode: branch or commit SHA violates v1 bounds")
	}
	if !bounded(create.Client.Name, 1, 200) || !bounded(create.Client.Version, 1, 200) || !bounded(create.Client.SidecarVersion, 1, 200) || !bounded(create.Client.AgentName, 0, 500) || !bounded(create.Client.Model, 0, 500) {
		return fmt.Errorf("invalid episode: client metadata exceeds v1 bounds")
	}
	if err := validateArtifacts(create.Artifacts); err != nil {
		return err
	}
	return validateTranscript(create.Transcript)
}

func validateArtifacts(artifacts contractsv1.EpisodeArtifacts) error {
	if len(artifacts.FilesTouched) > 500 || len(artifacts.ArtifactURIs) > 100 || len(artifacts.TestsRun) > 200 {
		return fmt.Errorf("invalid episode: artifact collection exceeds v1 bounds")
	}
	for _, file := range artifacts.FilesTouched {
		if !bounded(file, 0, 2048) {
			return fmt.Errorf("invalid episode: file path exceeds v1 bounds")
		}
	}
	for _, artifact := range artifacts.ArtifactURIs {
		if !safeHTTPSURI(artifact, 2048) {
			return fmt.Errorf("invalid episode: artifact URI is not a safe HTTPS URI")
		}
	}
	for _, test := range artifacts.TestsRun {
		if !bounded(test, 0, 2000) {
			return fmt.Errorf("invalid episode: test description exceeds v1 bounds")
		}
	}
	return nil
}

func validateTranscript(transcript contractsv1.TranscriptRef) error {
	switch transcript.Mode {
	case "none":
		if transcript.OpaqueRef != "" || transcript.RedactedSummary != "" {
			return fmt.Errorf("invalid episode: transcript mode none cannot carry content")
		}
	case "opaque_ref":
		if !safeHTTPSURI(transcript.OpaqueRef, 2048) || transcript.RedactedSummary != "" {
			return fmt.Errorf("invalid episode: transcript opaque reference must be HTTPS only")
		}
	case "redacted_summary":
		if transcript.OpaqueRef != "" || !bounded(transcript.RedactedSummary, 1, 4000) {
			return fmt.Errorf("invalid episode: transcript redacted summary is invalid")
		}
	}
	return nil
}

func bounded(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func safeHTTPSURI(value string, maximum int) bool {
	if !bounded(value, 1, maximum) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}
