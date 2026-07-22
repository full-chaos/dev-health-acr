package sidecar

import (
	"strconv"
	"strings"
)

func isAbsoluteLocalPath(value string) bool {
	return strings.HasPrefix(value, "/") || hasWindowsAbsolutePathPrefix(value)
}

func supportedCodeGraphVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	components := [3]int{}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		component, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		components[index] = component
	}
	return components[0] == 1 && components[1] >= 2
}

func validIdentifier(value string) bool {
	if !validCodeGraphText(value, maxLocalEvidenceIDBytes) || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(":_-./", character)) {
			return false
		}
	}
	return true
}

func codeGraphNodeLess(left, right codeGraphNode) bool {
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	if left.FilePath != right.FilePath {
		return left.FilePath < right.FilePath
	}
	return left.Line < right.Line
}

func duplicateCodeGraphNodes(nodes []codeGraphNode) bool {
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if _, found := seen[node.ID]; found {
			return true
		}
		seen[node.ID] = struct{}{}
	}
	return false
}

func codeGraphRelationLess(left, right codeGraphRelation) bool {
	if left.FilePath != right.FilePath {
		return left.FilePath < right.FilePath
	}
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.Name < right.Name
}

func duplicateCodeGraphRelations(relations []codeGraphRelation) bool {
	for index := 1; index < len(relations); index++ {
		if !codeGraphRelationLess(relations[index-1], relations[index]) && !codeGraphRelationLess(relations[index], relations[index-1]) {
			return true
		}
	}
	return false
}
