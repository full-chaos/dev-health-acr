package sidecar

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
)

type codeGraphCandidate struct {
	Type    string
	Locator string
	Title   string
	Excerpt string
}

func nodeCandidates(nodes []codeGraphNode) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(nodes))
	for _, node := range nodes {
		candidates = append(candidates, codeGraphCandidate{Type: "definition", Locator: "node:" + node.ID, Title: boundedCandidateText("definition: " + node.Name), Excerpt: codeGraphProvenance(node.FilePath, node.Line)})
	}
	return candidates
}

func relationCandidates(kind string, relations []codeGraphRelation) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(relations))
	for _, relation := range relations {
		locator := kind + ":" + relation.FilePath + "#" + strconv.Itoa(relation.Line) + ":" + relation.Name
		candidates = append(candidates, codeGraphCandidate{Type: kind, Locator: locator, Title: boundedCandidateText(kind + ": " + relation.Name), Excerpt: codeGraphProvenance(relation.FilePath, relation.Line)})
	}
	return candidates
}

func affectedCandidates(affected codeGraphAffected) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(affected.ChangedFiles)+len(affected.AffectedTests))
	for _, path := range affected.ChangedFiles {
		candidates = append(candidates, codeGraphCandidate{Type: "affected", Locator: "affected:" + path, Title: boundedCandidateText("affected: " + path), Excerpt: path})
	}
	for _, path := range affected.AffectedTests {
		candidates = append(candidates, codeGraphCandidate{Type: "test", Locator: "test:" + path, Title: boundedCandidateText("test: " + path), Excerpt: path})
	}
	return candidates
}

func fileCandidates(files []codeGraphFile) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(files))
	for _, file := range files {
		candidates = append(candidates, codeGraphCandidate{Type: "file", Locator: "file:" + file.Path, Title: boundedCandidateText("file: " + file.Path), Excerpt: file.Language})
	}
	return candidates
}

func buildCodeGraphEvidence(candidates []codeGraphCandidate, itemLimit, tokenLimit int) ([]LocalExpandedEvidence, error) {
	evidence := make([]LocalExpandedEvidence, 0, min(len(candidates), itemLimit))
	remaining := tokenLimit
	for _, candidate := range candidates {
		if len(evidence) == itemLimit {
			break
		}
		if !boundedLocalLocator(candidate.Locator) || !boundedNonEmpty(candidate.Title, maxLocalEvidenceTitleBytes) || len(candidate.Excerpt) > maxLocalEvidenceExcerptBytes {
			return nil, errCodeGraphDecode
		}
		estimatedTokens := max(1, (len(candidate.Title)+len(candidate.Excerpt)+3)/4)
		if estimatedTokens > remaining {
			break
		}
		sum := sha256.Sum256([]byte(candidate.Type + "\x00" + candidate.Locator))
		evidence = append(evidence, LocalExpandedEvidence{ID: "cg:" + hex.EncodeToString(sum[:]), Locator: candidate.Locator, Title: candidate.Title, Excerpt: candidate.Excerpt, EstimatedTokens: estimatedTokens})
		remaining -= estimatedTokens
	}
	return evidence, nil
}

func codeGraphCandidateLess(left, right codeGraphCandidate) bool {
	if left.Type != right.Type {
		return left.Type < right.Type
	}
	return left.Locator < right.Locator
}

func duplicateCodeGraphCandidates(candidates []codeGraphCandidate) bool {
	for index := 1; index < len(candidates); index++ {
		if candidates[index-1].Type == candidates[index].Type && candidates[index-1].Locator == candidates[index].Locator {
			return true
		}
	}
	return false
}

func directoryForCodeGraphFiles(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	directory := filepath.ToSlash(filepath.Dir(paths[0]))
	if directory == "." {
		return ""
	}
	return directory
}

func codeGraphProvenance(path string, line int) string {
	return path + ":" + strconv.Itoa(line)
}

func boundedCandidateText(value string) string {
	if len(value) <= maxLocalEvidenceTitleBytes {
		return value
	}
	return strings.TrimSpace(value[:maxLocalEvidenceTitleBytes])
}
