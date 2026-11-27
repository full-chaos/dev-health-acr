package mcp

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
)

// The hosted server's skills: public text vendored byte for byte from
// full-chaos/context-fabric-agents at vendoredSkillsRev (the rev
// ci/context-fabric-agents.pin names), the same bytes the Claude Code plugin
// ships. Edit the source there, re-vendor, and move the pin, the rev and the
// digests in one change; CI fails when they disagree.
const vendoredSkillsRev = "7a7f17c05bf02d329b53ffb30a41b195a01b0ee3"

// vendoredSkillDigests is the sha256 of every vendored file at the pinned
// rev, keyed by its path under skills/.
var vendoredSkillDigests = map[string]string{
	"dev-health/SKILL.md": "sha256:99ed41d1c4b35b779f1b8acc3abf534313ff2e008e418b8942b2e698897c0d23",
}

//go:embed skills
var vendoredSkillsRoot embed.FS

func vendoredSkillsFS() fs.FS {
	sub, err := fs.Sub(vendoredSkillsRoot, "skills")
	if err != nil {
		panic("mcp: vendored skills: " + err.Error())
	}
	return sub
}

// checkSkillDigests fails unless fsys holds exactly the files pins names,
// each with its pinned sha256.
func checkSkillDigests(fsys fs.FS, pins map[string]string) error {
	seen := map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, ok := pins[p]
		if !ok {
			return fmt.Errorf("vendored skill file %q has no pinned digest", p)
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if got := "sha256:" + hex.EncodeToString(sum[:]); got != want {
			return fmt.Errorf("vendored skill file %q is %s, pinned %s", p, got, want)
		}
		seen[p] = true
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	for p := range pins {
		if !seen[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("pinned skill files are not vendored: %v", missing)
	}
	return nil
}

// hostedSkills is the skill set the hosted transport serves. The embedded
// bytes are fixed at build time and pinned by the parity cells, so a failure
// here is a broken build, not a runtime condition.
func hostedSkills() *skillSet {
	fsys := vendoredSkillsFS()
	if err := checkSkillDigests(fsys, vendoredSkillDigests); err != nil {
		panic("mcp: " + err.Error())
	}
	set, err := newSkillSet(fsys)
	if err != nil {
		panic("mcp: vendored skills: " + err.Error())
	}
	return set
}
