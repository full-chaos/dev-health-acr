package v1

import "fmt"

func (r ContextPacketRequest) Validate() error {
	if r.SchemaVersion != ContextPacketRequestSchema {
		return fmt.Errorf("schema_version must be %q", ContextPacketRequestSchema)
	}
	if !stringLengthBetween(r.RequestID, 8, 256) || !stringLengthBetween(r.Goal, 1, 4000) {
		return fmt.Errorf("request_id or goal exceeds v1 bounds")
	}
	if !repositorySlugPattern.MatchString(r.Repository.Slug) || !stringLengthBetween(r.Repository.Slug, 0, 512) {
		return fmt.Errorf("repository.slug violates v1 bounds")
	}
	if !stringLengthBetween(r.Repository.RepoID, 0, 128) || !optionalURI(r.Repository.RemoteURL, 2048) {
		return fmt.Errorf("repository metadata violates v1 bounds")
	}
	if !stringLengthBetween(r.Scope.Branch, 0, 512) || !stringLengthBetween(r.Scope.TaskRef, 0, 1024) {
		return fmt.Errorf("scope string violates v1 bounds")
	}
	if r.Scope.CommitSHA != "" && !commitSHAPattern.MatchString(r.Scope.CommitSHA) {
		return fmt.Errorf("scope.commit_sha violates v1 bounds")
	}
	if len(r.Scope.Files) > 200 || !uniqueStrings(r.Scope.Files) {
		return fmt.Errorf("scope.files violates v1 bounds")
	}
	for _, file := range r.Scope.Files {
		if !stringLengthBetween(file, 1, 2048) {
			return fmt.Errorf("scope.files violates v1 bounds")
		}
	}
	if r.Scope.TimeWindowDays != 0 && (r.Scope.TimeWindowDays < 1 || r.Scope.TimeWindowDays > 365) {
		return fmt.Errorf("scope.time_window_days violates v1 bounds")
	}
	if r.Options.MaxItems < 1 || r.Options.MaxItems > 50 || r.Options.MaxOutputTokens < 500 || r.Options.MaxOutputTokens > 16000 || r.Options.MaxSerializedBytes < 8192 || r.Options.MaxSerializedBytes > 1048576 {
		return fmt.Errorf("packet options violate v1 bounds")
	}
	if !uniquePacketCategories(r.Options.RequestedCategories) {
		return fmt.Errorf("requested_categories violates v1 bounds")
	}
	if !stringLengthBetween(r.Client.Name, 1, 200) || !stringLengthBetween(r.Client.Version, 1, 200) || !stringLengthBetween(r.Client.SidecarVersion, 0, 200) {
		return fmt.Errorf("client metadata violates v1 bounds")
	}
	return nil
}

func uniquePacketCategories(categories []PacketCategory) bool {
	seen := make(map[PacketCategory]bool, len(categories))
	for _, category := range categories {
		switch category {
		case CategoryState, CategoryPressure, CategoryCause, CategoryEvidence, CategoryAction:
		default:
			return false
		}
		if seen[category] {
			return false
		}
		seen[category] = true
	}
	return true
}
