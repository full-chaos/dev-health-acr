package sidecar

func localEvidenceBundleUsage(bundle LocalEvidenceBundle) (int, int, int, error) {
	if !boundedNonEmpty(bundle.ProviderID, maxLocalIndexProviderIDBytes) || !boundedNonEmpty(bundle.ProviderVersion, maxLocalIndexProviderVersionBytes) {
		return 0, 0, 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "provider")
	}
	if !boundedNonEmpty(bundle.QueryID, maxLocalIndexQueryIDBytes) || !boundedNonEmpty(bundle.QueryVersion, maxLocalIndexQueryVersionBytes) {
		return 0, 0, 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "query")
	}
	if len(bundle.Evidence) > maxLocalEvidenceItems {
		return 0, 0, 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence count")
	}
	if err := validateLocalEvidenceMetadata(bundle); err != nil {
		return 0, 0, 0, err
	}
	metadataBytes, err := localBundleMetadataBytes(bundle)
	if err != nil {
		return 0, 0, 0, err
	}
	payloadBytes := len(bundle.ProviderID) + len(bundle.ProviderVersion) + len(bundle.QueryID) + len(bundle.QueryVersion) + metadataBytes
	tokens := 0
	for index, evidence := range bundle.Evidence {
		if err := ValidateLocalExpandedEvidence(evidence); err != nil {
			return 0, 0, 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence")
		}
		for _, previous := range bundle.Evidence[:index] {
			if previous.ID == evidence.ID || previous.Locator == evidence.Locator {
				return 0, 0, 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "duplicate evidence")
			}
		}
		evidenceMetadataBytes, err := localEvidenceMetadataBytes(evidence)
		if err != nil {
			return 0, 0, 0, err
		}
		payloadBytes += len(evidence.ID) + len(evidence.Locator) + len(evidence.Title) + len(evidence.Excerpt) + evidenceMetadataBytes
		if payloadBytes > maxLocalEvidenceBundlePayloadBytes || evidence.EstimatedTokens > maxLocalEvidenceTokens-tokens {
			return 0, 0, 0, invalidLocalIndexValue(ErrInvalidLocalEvidenceBundle, "evidence budget")
		}
		tokens += evidence.EstimatedTokens
	}
	return len(bundle.Evidence), tokens, payloadBytes, nil
}
