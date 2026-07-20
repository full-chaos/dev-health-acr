package sidecar

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type codeGraphCandidate struct {
	Command   codeGraphCommand
	Type      string
	Locator   string
	Title     string
	Excerpt   string
	Path      string
	Line      int
	Truncated bool
}

type codeGraphCommand string

const (
	codeGraphCommandStatus   codeGraphCommand = "status"
	codeGraphCommandQuery    codeGraphCommand = "query"
	codeGraphCommandCallers  codeGraphCommand = "callers"
	codeGraphCommandCallees  codeGraphCommand = "callees"
	codeGraphCommandImpact   codeGraphCommand = "impact"
	codeGraphCommandAffected codeGraphCommand = "affected"
	codeGraphCommandFiles    codeGraphCommand = "files"
)

func nodeCandidates(nodes []codeGraphNode) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(nodes))
	for _, node := range nodes {
		title := "definition: " + node.Name
		candidates = append(candidates, codeGraphCandidate{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:" + node.ID, Title: boundedCandidateText(title), Excerpt: codeGraphProvenance(node.FilePath, node.Line), Path: node.FilePath, Line: node.Line, Truncated: candidateTextTruncated(title)})
	}
	return candidates
}

func relationCandidates(command codeGraphCommand, kind string, relations []codeGraphRelation) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(relations))
	for _, relation := range relations {
		locator := kind + ":" + relation.FilePath + "#" + strconv.Itoa(relation.Line) + ":" + relation.Name
		title := kind + ": " + relation.Name
		candidates = append(candidates, codeGraphCandidate{Command: command, Type: kind, Locator: locator, Title: boundedCandidateText(title), Excerpt: codeGraphProvenance(relation.FilePath, relation.Line), Path: relation.FilePath, Line: relation.Line, Truncated: candidateTextTruncated(title)})
	}
	return candidates
}

func affectedCandidates(affected codeGraphAffected) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(affected.ChangedFiles)+len(affected.AffectedTests))
	for _, path := range affected.ChangedFiles {
		title := "affected: " + path
		candidates = append(candidates, codeGraphCandidate{Command: codeGraphCommandAffected, Type: "affected", Locator: "affected:" + path, Title: boundedCandidateText(title), Excerpt: path, Path: path, Truncated: candidateTextTruncated(title)})
	}
	for _, path := range affected.AffectedTests {
		title := "test: " + path
		candidates = append(candidates, codeGraphCandidate{Command: codeGraphCommandAffected, Type: "test", Locator: "test:" + path, Title: boundedCandidateText(title), Excerpt: path, Path: path, Truncated: candidateTextTruncated(title)})
	}
	return candidates
}

func fileCandidates(files []codeGraphFile) []codeGraphCandidate {
	candidates := make([]codeGraphCandidate, 0, len(files))
	for _, file := range files {
		title := "file: " + file.Path
		candidates = append(candidates, codeGraphCandidate{Command: codeGraphCommandFiles, Type: "file", Locator: "file:" + file.Path, Title: boundedCandidateText(title), Excerpt: file.Language, Path: file.Path, Truncated: candidateTextTruncated(title)})
	}
	return candidates
}

func buildCodeGraphEvidence(candidates []codeGraphCandidate, itemLimit, tokenLimit int) ([]LocalExpandedEvidence, bool, error) {
	evidence := make([]LocalExpandedEvidence, 0, min(len(candidates), itemLimit))
	remaining := tokenLimit
	truncated := false
	for _, candidate := range candidates {
		if len(evidence) == itemLimit {
			return evidence, true, nil
		}
		queryID, validCommand := candidate.Command.queryID()
		if !validCommand || !boundedLocalLocator(candidate.Locator) || !boundedNonEmpty(candidate.Title, maxLocalEvidenceTitleBytes) || len(candidate.Excerpt) > maxLocalEvidenceExcerptBytes || (candidate.Path != "" && !validRepositoryRelativePath(candidate.Path)) || (candidate.Line != 0 && candidate.Path == "") {
			return nil, false, errCodeGraphDecode
		}
		estimatedTokens := max(1, (len(candidate.Title)+len(candidate.Excerpt)+3)/4)
		if estimatedTokens > remaining {
			return evidence, true, nil
		}
		sum := sha256.Sum256([]byte(candidate.Type + "\x00" + candidate.Locator))
		evidence = append(evidence, LocalExpandedEvidence{ID: "cg:" + hex.EncodeToString(sum[:]), Locator: candidate.Locator, Title: candidate.Title, Excerpt: candidate.Excerpt, EstimatedTokens: estimatedTokens, QueryID: queryID, Relation: candidate.Type, RepositoryPath: candidate.Path, StartLine: candidate.Line})
		remaining -= estimatedTokens
		truncated = truncated || candidate.Truncated
	}
	return evidence, truncated, nil
}

func trimCodeGraphEvidence(bundle LocalEvidenceBundle) (LocalEvidenceBundle, bool, error) {
	truncated := false
	for {
		_, _, _, err := localEvidenceBundleUsage(bundle)
		if err == nil {
			return bundle, truncated, nil
		}
		if !codeGraphPayloadBudgetError(err) {
			return LocalEvidenceBundle{}, false, err
		}
		if len(bundle.Evidence) == 0 {
			return LocalEvidenceBundle{}, false, ErrCodeGraphOutputTooLarge
		}
		bundle.Truncated = true
		bundle.Warnings = canonicalBundleWarnings(bundle.Warnings, true, bundle.IndexedCommit)
		bundle.Evidence = bundle.Evidence[:len(bundle.Evidence)-1]
		truncated = true
	}
}

func codeGraphPayloadBudgetError(err error) bool {
	var validationErr *localIndexValidationError
	return errors.As(err, &validationErr) && validationErr.field == "evidence budget"
}

func (command codeGraphCommand) queryID() (string, bool) {
	switch command {
	case codeGraphCommandQuery, codeGraphCommandCallers, codeGraphCommandCallees, codeGraphCommandImpact, codeGraphCommandAffected, codeGraphCommandFiles:
		return string(command), true
	default:
		return "", false
	}
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
	limit := maxLocalEvidenceTitleBytes
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit])
}

func candidateTextTruncated(value string) bool { return len(value) > maxLocalEvidenceTitleBytes }
