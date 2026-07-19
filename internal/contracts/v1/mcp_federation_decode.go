package v1

import (
	"encoding/json"
	"fmt"
)

func validateMCPLocalContextPayload(data []byte) error {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(data, &response); err != nil {
		return nil
	}
	local, present := response["local_context"]
	if !present || isExplicitJSONNull(local) {
		return nil
	}
	var context map[string]json.RawMessage
	if err := json.Unmarshal(local, &context); err != nil {
		return nil
	}
	if err := validateMCPWarningsPayload(context["warnings"]); err != nil {
		return err
	}
	return validateMCPLocalEvidencePayload(context["evidence_refs"])
}

func validateMCPWarningsPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var warnings []json.RawMessage
	if err := json.Unmarshal(raw, &warnings); err != nil {
		return nil
	}
	for index, warning := range warnings {
		if isExplicitJSONNull(warning) {
			return fmt.Errorf("context_for_task response.local_context.warnings[%d]: must not be JSON null", index)
		}
	}
	return nil
}

func validateMCPLocalEvidencePayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var evidenceRefs []json.RawMessage
	if err := json.Unmarshal(raw, &evidenceRefs); err != nil {
		return nil
	}
	for index, evidenceRaw := range evidenceRefs {
		var evidence map[string]json.RawMessage
		if err := json.Unmarshal(evidenceRaw, &evidence); err != nil {
			continue
		}
		metadata, present := evidence["metadata"]
		if !present {
			continue
		}
		if isExplicitJSONNull(metadata) {
			return fmt.Errorf("context_for_task response.local_context.evidence_refs[%d].metadata: must not be JSON null", index)
		}
		if err := validateMCPLocalMetadataPayload(metadata); err != nil {
			return fmt.Errorf("context_for_task response.local_context.evidence_refs[%d].metadata: %w", index, err)
		}
	}
	return nil
}

func validateMCPLocalMetadataPayload(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return validateMCPLocalMetadata(value)
}
