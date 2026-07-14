package credentiallifecycle

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	credentialIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
	repositoryPartPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)
	tokenHashPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	tokenPrefixPattern    = regexp.MustCompile(`^fcacr_[A-Za-z0-9_-]{6,64}$`)
)

var knownScopes = map[string]struct{}{
	"context:read":  {},
	"evidence:read": {},
	"episode:write": {},
}

func normalizeCreate(input CreateInput) (CreateInput, error) {
	var err error
	input.CredentialID, err = normalizeCredentialID(input.CredentialID)
	if err != nil {
		return CreateInput{}, err
	}
	input.OrgID, err = normalizeIdentifier("organization", input.OrgID)
	if err != nil {
		return CreateInput{}, err
	}
	input.Name, err = normalizeText("name", input.Name, 200)
	if err != nil {
		return CreateInput{}, err
	}
	input.TokenPrefix = strings.TrimSpace(input.TokenPrefix)
	if !tokenPrefixPattern.MatchString(input.TokenPrefix) {
		return CreateInput{}, invalid("token prefix")
	}
	input.TokenHash, err = normalizeTokenHash(input.TokenHash)
	if err != nil {
		return CreateInput{}, err
	}
	input.RepositoryScopes, err = normalizeRepositories(input.RepositoryScopes)
	if err != nil {
		return CreateInput{}, err
	}
	input.Scopes, err = normalizeScopes(input.Scopes)
	if err != nil {
		return CreateInput{}, err
	}
	input.ActorID, err = normalizeText("actor", input.ActorID, 200)
	if err != nil {
		return CreateInput{}, err
	}
	input.ExpiresAt, err = normalizeExpiry(input.ExpiresAt)
	if err != nil {
		return CreateInput{}, err
	}
	return input, nil
}

func normalizeRotation(input RotationInput) (RotationInput, error) {
	var err error
	input.OrgID, err = normalizeIdentifier("organization", input.OrgID)
	if err != nil {
		return RotationInput{}, err
	}
	input.SourceCredentialID, err = normalizeCredentialID(input.SourceCredentialID)
	if err != nil {
		return RotationInput{}, err
	}
	input.ActorID, err = normalizeText("actor", input.ActorID, 200)
	if err != nil {
		return RotationInput{}, err
	}
	create, err := normalizeCreate(CreateInput{
		CredentialID:     input.Replacement.CredentialID,
		OrgID:            input.OrgID,
		Name:             input.Replacement.Name,
		TokenPrefix:      input.Replacement.TokenPrefix,
		TokenHash:        input.Replacement.TokenHash,
		RepositoryScopes: input.Replacement.RepositoryScopes,
		Scopes:           input.Replacement.Scopes,
		ActorID:          input.ActorID,
		ExpiresAt:        input.Replacement.ExpiresAt,
	})
	if err != nil {
		return RotationInput{}, err
	}
	if create.CredentialID == input.SourceCredentialID {
		return RotationInput{}, invalid("replacement credential id")
	}
	if !validOverlap(input.Replacement) {
		return RotationInput{}, invalid("rotation overlap")
	}
	input.Replacement = RotationReplacement{
		CredentialID:     create.CredentialID,
		Name:             create.Name,
		TokenPrefix:      create.TokenPrefix,
		TokenHash:        create.TokenHash,
		RepositoryScopes: create.RepositoryScopes,
		Scopes:           create.Scopes,
		ExpiresAt:        create.ExpiresAt,
		Overlap:          input.Replacement.Overlap,
		Immediate:        input.Replacement.Immediate,
	}
	return input, nil
}

func normalizeRevocation(input RevocationInput) (RevocationInput, error) {
	var err error
	input.OrgID, err = normalizeIdentifier("organization", input.OrgID)
	if err != nil {
		return RevocationInput{}, err
	}
	input.CredentialID, err = normalizeCredentialID(input.CredentialID)
	if err != nil {
		return RevocationInput{}, err
	}
	input.ActorID, err = normalizeText("actor", input.ActorID, 200)
	if err != nil {
		return RevocationInput{}, err
	}
	return input, nil
}

func normalizeCredentialID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !credentialIDPattern.MatchString(value) {
		return "", invalid("credential id")
	}
	return value, nil
}

func normalizeIdentifier(name, value string) (string, error) {
	return normalizeText(name, value, 200)
}

func normalizeText(name, value string, maximum int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", invalid(name)
	}
	return value, nil
}

func normalizeTokenHash(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !tokenHashPattern.MatchString(value) {
		return "", invalid("token hash")
	}
	return value, nil
}

func normalizeRepositories(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, invalid("repository scopes")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		parts := strings.Split(value, "/")
		if value != "*" && (len(parts) != 2 || !repositoryPartPattern.MatchString(parts[0]) || (parts[1] != "*" && !repositoryPartPattern.MatchString(parts[1]))) {
			return nil, invalid("repository scope")
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeScopes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, invalid("scopes")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if _, exists := knownScopes[value]; !exists {
			return nil, invalid("scope")
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeExpiry(value *time.Time) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if value.IsZero() {
		return nil, invalid("expiry")
	}
	copy := value.UTC()
	return &copy, nil
}

func normalizeUsage(ip, userAgent string) (string, string, error) {
	ip = strings.TrimSpace(ip)
	if ip != "" && net.ParseIP(ip) == nil {
		return "", "", invalid("last-used ip")
	}
	userAgent = strings.TrimSpace(userAgent)
	if len(userAgent) > 512 || !utf8.ValidString(userAgent) || strings.IndexFunc(userAgent, unicode.IsControl) >= 0 {
		return "", "", invalid("last-used user agent")
	}
	return ip, userAgent, nil
}

func validOverlap(input RotationReplacement) bool {
	return input.Immediate && input.Overlap == 0 || !input.Immediate && input.Overlap > 0 && input.Overlap <= MaximumOverlap
}

func invalid(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, field)
}
