package mcp

import (
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

func vendoredCopy(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(vendoredSkillsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(vendoredSkillsFS(), p)
		if err != nil {
			return err
		}
		out[p] = &fstest.MapFile{Data: raw}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The served skill bytes are the pinned bytes: green on the vendored copy,
// red on a one-byte change, an added file and a removed one.
func TestVendoredSkillsMatchThePinnedDigests(t *testing.T) {
	if err := checkSkillDigests(vendoredSkillsFS(), vendoredSkillDigests); err != nil {
		t.Fatalf("vendored skills differ from the pin: %v", err)
	}
	if len(vendoredSkillDigests) == 0 {
		t.Fatal("no vendored skill is pinned")
	}
	embedded := vendoredCopy(t)
	if len(embedded) != len(vendoredSkillDigests) {
		t.Fatalf("the binary embeds %d skill files, the pin names %d", len(embedded), len(vendoredSkillDigests))
	}
	for p, file := range embedded {
		if len(file.Data) == 0 {
			t.Fatalf("embedded skill file %s is empty", p)
		}
	}
	for p := range vendoredSkillDigests {
		changed := vendoredCopy(t)
		data := append([]byte(nil), changed[p].Data...)
		data[len(data)/2] ^= 0x01
		changed[p] = &fstest.MapFile{Data: data}
		if err := checkSkillDigests(changed, vendoredSkillDigests); err == nil || !strings.Contains(err.Error(), p) {
			t.Fatalf("a one-byte change to %s passed the parity gate: %v", p, err)
		}
	}
	added := vendoredCopy(t)
	added["dev-health/references/extra.md"] = &fstest.MapFile{Data: []byte("# Extra\n")}
	if err := checkSkillDigests(added, vendoredSkillDigests); err == nil {
		t.Fatal("an unpinned vendored file passed the parity gate")
	}
	removed := vendoredCopy(t)
	for p := range vendoredSkillDigests {
		delete(removed, p)
		break
	}
	if err := checkSkillDigests(removed, vendoredSkillDigests); err == nil {
		t.Fatal("a missing pinned file passed the parity gate")
	}
}

// The rev in code is the rev CI checks the vendored copy out against.
func TestVendoredSkillsRevEqualsThePinFile(t *testing.T) {
	raw, err := os.ReadFile("../../ci/context-fabric-agents.pin")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != vendoredSkillsRev {
		t.Fatalf("ci/context-fabric-agents.pin names %q, the code pins %q", got, vendoredSkillsRev)
	}
}

func TestDevHealthSkillIsUnderTheClaudeCodeCaps(t *testing.T) {
	set := hostedSkills()
	if len(set.entries) != 1 || set.entries[0].URI != "skill://dev-health/SKILL.md" {
		t.Fatalf("hosted skills %+v, want the one Dev Health skill", set.entries)
	}
	total := 0
	for _, res := range set.entries[0].Resources {
		if res.Size > skillMaxFileBytes {
			t.Fatalf("%s is %d bytes, over %d", res.URI, res.Size, skillMaxFileBytes)
		}
		total += res.Size
	}
	if total > skillMaxSkillBytes {
		t.Fatalf("the Dev Health skill is %d bytes, over %d", total, skillMaxSkillBytes)
	}
	if name, _ := set.entries[0].Frontmatter["name"].(string); name != "dev-health" {
		t.Fatalf("frontmatter name %q, want dev-health (the plugin's name)", name)
	}
}

// Only the hosted transport serves the vendored skill: its text addresses the
// hosted server. STDIO declares no skills extension.
func TestVendoredSkillsAreHostedOnly(t *testing.T) {
	fx := newFixtureServer(t)
	cfg, _ := newFixtureBootstrap(t, fx).split(io.Discard)
	if !cfg.skills.empty() {
		t.Fatal("the STDIO process serves skills")
	}
	hosted := NewProcessConfig(cfg.Config, version.Info{}, io.Discard)
	if hosted.skills.empty() {
		t.Fatal("the hosted process serves no skill")
	}
}
