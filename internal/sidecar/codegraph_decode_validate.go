package sidecar

import (
	"encoding/json"
	"errors"
	"sort"
	"time"
)

var errCodeGraphDecode = errors.New("codegraph JSON did not satisfy the pinned contract")

func requiredStatusFields(object map[string]json.RawMessage) bool {
	for _, field := range []string{"fileCount", "nodeCount", "edgeCount", "dbSizeBytes"} {
		if !requiredIntMinimum(object, field, 0) {
			return false
		}
	}
	for _, field := range []string{"backend", "journalMode"} {
		if _, err := requiredText(object, field, maxLocalEvidenceTitleBytes); err != nil {
			return false
		}
	}
	if !validStatusMap(object["nodesByKind"]) || !validStatusLanguages(object["languages"]) || !validPendingChanges(object["pendingChanges"]) || !validWorktreeMismatch(object["worktreeMismatch"]) {
		return false
	}
	index, err := requiredObject(object, "index")
	if err != nil {
		return false
	}
	if _, err := requiredText(index, "builtWithVersion", maxLocalIndexProviderVersionBytes); err != nil || !requiredIntMinimum(index, "builtWithExtractionVersion", 0) || !requiredIntMinimum(index, "currentExtractionVersion", 0) {
		return false
	}
	_, err = requiredBool(index, "reindexRecommended")
	return err == nil
}

func requiredNodeFields(object map[string]json.RawMessage) bool {
	for _, field := range []string{"language", "signature"} {
		if _, err := requiredText(object, field, maxLocalTaskBytes); err != nil {
			return false
		}
	}
	for _, field := range []string{"endLine", "startColumn", "endColumn", "updatedAt"} {
		if !requiredIntMinimum(object, field, 0) {
			return false
		}
	}
	for _, field := range []string{"isExported", "isAsync", "isStatic", "isAbstract"} {
		if _, err := requiredBool(object, field); err != nil {
			return false
		}
	}
	return validNullableText(object["visibility"])
}

func decodeCodeGraphRelationsObject(object map[string]json.RawMessage, field string) ([]codeGraphRelation, error) {
	var entries []map[string]json.RawMessage
	if err := requiredValue(object, field, &entries); err != nil {
		return nil, errCodeGraphDecode
	}
	relations := make([]codeGraphRelation, 0, len(entries))
	for _, entry := range entries {
		relation, err := decodeCodeGraphRelation(entry)
		if err != nil {
			return nil, errCodeGraphDecode
		}
		relations = append(relations, relation)
	}
	sort.Slice(relations, func(left, right int) bool { return codeGraphRelationLess(relations[left], relations[right]) })
	if duplicateCodeGraphRelations(relations) {
		return nil, errCodeGraphDecode
	}
	return relations, nil
}

func decodeCodeGraphRelation(object map[string]json.RawMessage) (codeGraphRelation, error) {
	name, err := requiredText(object, "name", maxLocalEvidenceTitleBytes)
	if err != nil {
		return codeGraphRelation{}, errCodeGraphDecode
	}
	kind, err := requiredText(object, "kind", maxLocalEvidenceTitleBytes)
	if err != nil {
		return codeGraphRelation{}, errCodeGraphDecode
	}
	path, err := requiredRepositoryPath(object, "filePath")
	if err != nil {
		return codeGraphRelation{}, errCodeGraphDecode
	}
	line, err := requiredInt(object, "startLine")
	if err != nil || line < 0 {
		return codeGraphRelation{}, errCodeGraphDecode
	}
	return codeGraphRelation{Name: name, Kind: kind, FilePath: path, Line: line}, nil
}

func requiredText(object map[string]json.RawMessage, field string, maximum int) (string, error) {
	var value string
	if err := requiredValue(object, field, &value); err != nil || !validCodeGraphText(value, maximum) {
		return "", errCodeGraphDecode
	}
	return value, nil
}

func requiredRepositoryPath(object map[string]json.RawMessage, field string) (string, error) {
	value, err := requiredText(object, field, maxLocalEvidenceLocatorBytes)
	if err != nil || !validRepositoryRelativePath(value) {
		return "", errCodeGraphDecode
	}
	return value, nil
}

func requiredAbsolutePath(object map[string]json.RawMessage, field string) (string, error) {
	value, err := requiredText(object, field, maxLocalTaskBytes)
	if err != nil || !isAbsoluteLocalPath(value) {
		return "", errCodeGraphDecode
	}
	return value, nil
}

func requiredRFC3339(object map[string]json.RawMessage, field string) (time.Time, error) {
	value, err := requiredText(object, field, maxLocalIndexProviderVersionBytes)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errCodeGraphDecode
	}
	return parsed, nil
}

func requiredInt(object map[string]json.RawMessage, field string) (int, error) {
	var value int
	if err := requiredValue(object, field, &value); err != nil {
		return 0, errCodeGraphDecode
	}
	return value, nil
}

func requiredIntMinimum(object map[string]json.RawMessage, field string, minimum int) bool {
	value, err := requiredInt(object, field)
	return err == nil && value >= minimum
}

func requiredIntRange(object map[string]json.RawMessage, field string, minimum, maximum int) bool {
	value, err := requiredInt(object, field)
	return err == nil && value >= minimum && value <= maximum
}

func requiredBool(object map[string]json.RawMessage, field string) (bool, error) {
	var value bool
	if err := requiredValue(object, field, &value); err != nil {
		return false, errCodeGraphDecode
	}
	return value, nil
}

func requiredFloat(object map[string]json.RawMessage, field string) (float64, error) {
	var value float64
	if err := requiredValue(object, field, &value); err != nil {
		return 0, errCodeGraphDecode
	}
	return value, nil
}

func requiredObject(object map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := requiredValue(object, field, &value); err != nil || value == nil {
		return nil, errCodeGraphDecode
	}
	return value, nil
}

func requiredValue(object map[string]json.RawMessage, field string, destination any) error {
	raw, found := object[field]
	if !found || string(raw) == "null" || json.Unmarshal(raw, destination) != nil {
		return errCodeGraphDecode
	}
	return nil
}

func requiredPaths(object map[string]json.RawMessage, field string) ([]string, error) {
	var values []string
	if err := requiredValue(object, field, &values); err != nil {
		return nil, errCodeGraphDecode
	}
	return normalizeRepositoryPaths(values)
}

func validStatusMap(raw json.RawMessage) bool {
	var values map[string]int
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return false
	}
	for name, count := range values {
		if !validCodeGraphText(name, maxLocalEvidenceTitleBytes) || count < 0 {
			return false
		}
	}
	return true
}

func validStatusLanguages(raw json.RawMessage) bool {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	for _, value := range values {
		if !validCodeGraphText(value, maxLocalEvidenceTitleBytes) {
			return false
		}
	}
	return true
}

func validPendingChanges(raw json.RawMessage) bool {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return false
	}
	for _, field := range []string{"added", "modified", "removed"} {
		if !requiredIntMinimum(values, field, 0) {
			return false
		}
	}
	return true
}

func validWorktreeMismatch(raw json.RawMessage) bool {
	if string(raw) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && validCodeGraphText(value, maxLocalTaskBytes)
}

func validNullableText(raw json.RawMessage) bool {
	if string(raw) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && validCodeGraphText(value, maxLocalEvidenceTitleBytes)
}
