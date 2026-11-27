package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// The MCP Skills extension (ext-skills specification/beta/skills.mdx,
// SEP-2640). Skills are static public text served from a fixed file tree: no
// handler reads a caller, a credential or organization state, and serving a
// skill grants no scope. Every tool a skill names stays authorized live on
// its own call.
const (
	skillsExtension  = "io.modelcontextprotocol/skills"
	methodSkillsList = "skills/list"
	methodSkillsGet  = "skills/get"
	skillScheme      = "skill://"
	skillFileName    = "SKILL.md"

	// Claude Code loads an MCP skill only under these caps ("MCP skill
	// limitations"); 100 KB and 400 KB are read as decimal, the stricter
	// reading. The spec's own bound is 512 files per skill.
	skillMaxFileBytes  = 100_000
	skillMaxSkillBytes = 400_000
	skillMaxFiles      = 512
	skillMaxNameRunes  = 64
	skillMaxDescRunes  = 1024
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type skillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

type skillEntry struct {
	URI         string          `json:"uri"`
	Frontmatter map[string]any  `json:"frontmatter"`
	Resources   []skillResource `json:"resources"`
}

type skillFile struct {
	uri  string
	mime string
	text string
}

// skillSet is the immutable set of skills one process serves, in name order.
type skillSet struct {
	entries []skillEntry
	files   []skillFile
	byURI   map[string]int
}

func (s *skillSet) empty() bool { return s == nil || len(s.entries) == 0 }

// newSkillSet reads every top-level directory of fsys as one skill. It
// refuses the whole set when any skill breaks the Agent Skills format or a
// Claude Code cap, so a bad skill can never be half served.
func newSkillSet(fsys fs.FS) (*skillSet, error) {
	dirs, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("skills: read root: %w", err)
	}
	set := &skillSet{byURI: map[string]int{}}
	for _, dir := range dirs {
		if !dir.IsDir() {
			return nil, fmt.Errorf("skills: %q is not a skill directory", dir.Name())
		}
		entry, files, err := loadSkill(fsys, dir.Name())
		if err != nil {
			return nil, err
		}
		set.byURI[entry.URI] = len(set.entries)
		set.entries = append(set.entries, entry)
		set.files = append(set.files, files...)
	}
	return set, nil
}

func loadSkill(fsys fs.FS, name string) (skillEntry, []skillFile, error) {
	if utf8.RuneCountInString(name) > skillMaxNameRunes || !skillNamePattern.MatchString(name) {
		return skillEntry{}, nil, fmt.Errorf("skills: %q is not a valid skill name", name)
	}
	var paths []string
	err := fs.WalkDir(fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("skills: %q is not a regular file", p)
		}
		if d.Type().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return skillEntry{}, nil, err
	}
	if len(paths) > skillMaxFiles {
		return skillEntry{}, nil, fmt.Errorf("skills: %q has %d files, over %d", name, len(paths), skillMaxFiles)
	}
	sort.Strings(paths)
	skillURI := skillScheme + name + "/" + skillFileName
	entry := skillEntry{URI: skillURI}
	var files []skillFile
	total := 0
	for _, p := range paths {
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return skillEntry{}, nil, fmt.Errorf("skills: read %q: %w", p, err)
		}
		if len(raw) > skillMaxFileBytes {
			return skillEntry{}, nil, fmt.Errorf("skills: %q is %d bytes, over %d", p, len(raw), skillMaxFileBytes)
		}
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			return skillEntry{}, nil, fmt.Errorf("skills: %q is not text", p)
		}
		total += len(raw)
		sum := sha256.Sum256(raw)
		uri := skillScheme + p
		entry.Resources = append(entry.Resources, skillResource{URI: uri, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: len(raw)})
		files = append(files, skillFile{uri: uri, mime: skillMIME(p), text: string(raw)})
		if uri == skillURI {
			frontmatter, err := parseSkillFrontmatter(raw)
			if err != nil {
				return skillEntry{}, nil, fmt.Errorf("skills: %q: %w", p, err)
			}
			entry.Frontmatter = frontmatter
		}
	}
	if total > skillMaxSkillBytes {
		return skillEntry{}, nil, fmt.Errorf("skills: %q is %d bytes, over %d", name, total, skillMaxSkillBytes)
	}
	if entry.Frontmatter == nil {
		return skillEntry{}, nil, fmt.Errorf("skills: %q has no %s", name, skillFileName)
	}
	if got, _ := entry.Frontmatter["name"].(string); got != name {
		return skillEntry{}, nil, fmt.Errorf("skills: frontmatter name %q does not equal its directory %q", got, name)
	}
	desc, _ := entry.Frontmatter["description"].(string)
	if n := utf8.RuneCountInString(desc); n == 0 || n > skillMaxDescRunes {
		return skillEntry{}, nil, fmt.Errorf("skills: %q description must be 1 to %d characters", name, skillMaxDescRunes)
	}
	return entry, files, nil
}

// parseSkillFrontmatter returns the YAML block between the leading "---"
// line and the next "---" line, decoded as a JSON-ready object.
func parseSkillFrontmatter(raw []byte) (map[string]any, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, errors.New("does not begin with YAML frontmatter")
	}
	block, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if block, ok = strings.CutSuffix(rest, "\n---"); !ok {
			return nil, errors.New("frontmatter is not closed")
		}
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(block), &out); err != nil {
		return nil, errors.New("frontmatter is not valid YAML")
	}
	if out == nil {
		return nil, errors.New("frontmatter is empty")
	}
	return out, nil
}

func skillMIME(p string) string {
	if strings.EqualFold(path.Ext(p), ".md") {
		return "text/markdown"
	}
	return "text/plain"
}
