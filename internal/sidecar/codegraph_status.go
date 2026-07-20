package sidecar

import "encoding/json"

func codeGraphStatusFreshness(object map[string]json.RawMessage) (int, bool, bool, bool, error) {
	pending, err := codeGraphPendingChanges(object)
	if err != nil {
		return 0, false, false, false, err
	}
	mismatch, err := codeGraphWorktreeMismatch(object)
	if err != nil {
		return 0, false, false, false, err
	}
	index, err := requiredObject(object, "index")
	if err != nil {
		return 0, false, false, false, err
	}
	reindex, err := requiredBool(index, "reindexRecommended")
	if err != nil {
		return 0, false, false, false, err
	}
	built, err := requiredInt(index, "builtWithExtractionVersion")
	if err != nil {
		return 0, false, false, false, err
	}
	current, err := requiredInt(index, "currentExtractionVersion")
	if err != nil {
		return 0, false, false, false, err
	}
	return pending, mismatch, reindex, built != current, nil
}

func codeGraphPendingChanges(object map[string]json.RawMessage) (int, error) {
	pending, err := requiredObject(object, "pendingChanges")
	if err != nil {
		return 0, err
	}
	dirty := false
	for _, field := range []string{"added", "modified", "removed"} {
		value, valueErr := requiredInt(pending, field)
		if valueErr != nil || value < 0 {
			return 0, errCodeGraphDecode
		}
		dirty = dirty || value > 0
	}
	if dirty {
		return 1, nil
	}
	return 0, nil
}

func codeGraphWorktreeMismatch(object map[string]json.RawMessage) (bool, error) {
	raw, found := object["worktreeMismatch"]
	if !found || string(raw) == "null" {
		return false, nil
	}
	var mismatch string
	if err := json.Unmarshal(raw, &mismatch); err != nil {
		return false, errCodeGraphDecode
	}
	return mismatch != "", nil
}
