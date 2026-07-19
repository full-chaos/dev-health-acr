package sidecar

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"time"
)

type codeGraphStatus struct {
	Version       string
	ProjectPath   string
	IndexPath     string
	LastIndexedAt time.Time
}

type codeGraphNode struct {
	ID       string
	Kind     string
	Name     string
	FilePath string
	Line     int
	Score    float64
}

type codeGraphRelation struct {
	Name     string
	Kind     string
	FilePath string
	Line     int
}

type codeGraphAffected struct {
	ChangedFiles  []string
	AffectedTests []string
}

type codeGraphFile struct {
	Path     string
	Language string
}

func decodeCodeGraphStatus(payload []byte) (codeGraphStatus, error) {
	object, err := decodeCodeGraphObject(payload)
	if err != nil {
		return codeGraphStatus{}, err
	}
	if err := rejectCodeGraphProvenance(payload, true); err != nil {
		return codeGraphStatus{}, err
	}
	initialized, err := requiredBool(object, "initialized")
	if err != nil || !initialized {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	version, err := requiredText(object, "version", maxLocalIndexProviderVersionBytes)
	if err != nil || !supportedCodeGraphVersion(version) {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	projectPath, err := requiredAbsolutePath(object, "projectPath")
	if err != nil {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	indexPath, err := requiredAbsolutePath(object, "indexPath")
	if err != nil {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	lastIndexed, err := requiredRFC3339(object, "lastIndexed")
	if err != nil || !requiredStatusFields(object) {
		return codeGraphStatus{}, errCodeGraphDecode
	}
	return codeGraphStatus{Version: version, ProjectPath: projectPath, IndexPath: indexPath, LastIndexedAt: lastIndexed}, nil
}

func decodeCodeGraphQuery(payload []byte) ([]codeGraphNode, error) {
	if err := rejectCodeGraphProvenance(payload, false); err != nil {
		return nil, err
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, errCodeGraphDecode
	}
	nodes := make([]codeGraphNode, 0, len(entries))
	for _, entry := range entries {
		node, err := requiredObject(entry, "node")
		if err != nil {
			return nil, errCodeGraphDecode
		}
		candidate, err := decodeCodeGraphNode(node)
		if err != nil {
			return nil, errCodeGraphDecode
		}
		score, err := requiredFloat(entry, "score")
		if err != nil || math.IsInf(score, 0) || math.IsNaN(score) {
			return nil, errCodeGraphDecode
		}
		candidate.Score = score
		nodes = append(nodes, candidate)
	}
	sort.Slice(nodes, func(left, right int) bool {
		if nodes[left].Score != nodes[right].Score {
			return nodes[left].Score > nodes[right].Score
		}
		return codeGraphNodeLess(nodes[left], nodes[right])
	})
	if duplicateCodeGraphNodes(nodes) {
		return nil, errCodeGraphDecode
	}
	return nodes, nil
}

func decodeCodeGraphRelations(payload []byte, field string) ([]codeGraphRelation, error) {
	if err := rejectCodeGraphProvenance(payload, false); err != nil {
		return nil, err
	}
	object, err := decodeCodeGraphObject(payload)
	if err != nil {
		return nil, errCodeGraphDecode
	}
	if _, err := requiredText(object, "symbol", maxLocalEvidenceTitleBytes); err != nil {
		return nil, errCodeGraphDecode
	}
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

func decodeCodeGraphImpact(payload []byte) ([]codeGraphRelation, error) {
	if err := rejectCodeGraphProvenance(payload, false); err != nil {
		return nil, err
	}
	object, err := decodeCodeGraphObject(payload)
	if err != nil {
		return nil, errCodeGraphDecode
	}
	if _, err := requiredText(object, "symbol", maxLocalEvidenceTitleBytes); err != nil || !requiredIntRange(object, "depth", 0, codeGraphTraversalDepth) || !requiredIntMinimum(object, "nodeCount", 0) || !requiredIntMinimum(object, "edgeCount", 0) {
		return nil, errCodeGraphDecode
	}
	return decodeCodeGraphRelationsObject(object, "affected")
}

func decodeCodeGraphAffected(payload []byte) (codeGraphAffected, error) {
	if err := rejectCodeGraphProvenance(payload, false); err != nil {
		return codeGraphAffected{}, err
	}
	object, err := decodeCodeGraphObject(payload)
	if err != nil {
		return codeGraphAffected{}, errCodeGraphDecode
	}
	changed, err := requiredPaths(object, "changedFiles")
	if err != nil {
		return codeGraphAffected{}, errCodeGraphDecode
	}
	tests, err := requiredPaths(object, "affectedTests")
	if err != nil || !requiredIntMinimum(object, "totalDependentsTraversed", 0) {
		return codeGraphAffected{}, errCodeGraphDecode
	}
	return codeGraphAffected{ChangedFiles: changed, AffectedTests: tests}, nil
}

func decodeCodeGraphFiles(payload []byte) ([]codeGraphFile, error) {
	if err := rejectCodeGraphProvenance(payload, false); err != nil {
		return nil, err
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, errCodeGraphDecode
	}
	files := make([]codeGraphFile, 0, len(entries))
	for _, entry := range entries {
		path, err := requiredRepositoryPath(entry, "path")
		if err != nil {
			return nil, errCodeGraphDecode
		}
		language, err := requiredText(entry, "language", maxLocalEvidenceTitleBytes)
		if err != nil || !requiredIntMinimum(entry, "nodeCount", 0) || !requiredIntMinimum(entry, "size", 0) {
			return nil, errCodeGraphDecode
		}
		files = append(files, codeGraphFile{Path: path, Language: language})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	for index := 1; index < len(files); index++ {
		if files[index-1].Path == files[index].Path {
			return nil, errCodeGraphDecode
		}
	}
	return files, nil
}

func decodeCodeGraphObject(payload []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errCodeGraphDecode
	}
	return object, nil
}

func decodeCodeGraphNode(object map[string]json.RawMessage) (codeGraphNode, error) {
	id, err := requiredText(object, "id", maxLocalEvidenceIDBytes)
	if err != nil || !validIdentifier(id) {
		return codeGraphNode{}, errCodeGraphDecode
	}
	kind, err := requiredText(object, "kind", maxLocalEvidenceTitleBytes)
	if err != nil {
		return codeGraphNode{}, errCodeGraphDecode
	}
	name, err := requiredText(object, "name", maxLocalEvidenceTitleBytes)
	if err != nil {
		return codeGraphNode{}, errCodeGraphDecode
	}
	if _, err := requiredText(object, "qualifiedName", maxLocalTaskBytes); err != nil || !requiredNodeFields(object) {
		return codeGraphNode{}, errCodeGraphDecode
	}
	path, err := requiredRepositoryPath(object, "filePath")
	if err != nil {
		return codeGraphNode{}, errCodeGraphDecode
	}
	line, err := requiredInt(object, "startLine")
	if err != nil || line < 0 {
		return codeGraphNode{}, errCodeGraphDecode
	}
	return codeGraphNode{ID: id, Kind: kind, Name: name, FilePath: path, Line: line}, nil
}
