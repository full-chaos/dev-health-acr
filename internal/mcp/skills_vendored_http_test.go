package mcp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The hosted server serves the Dev Health skill vendored from
// full-chaos/context-fabric-agents at the rev ci/context-fabric-agents.pin
// names, byte for byte.

const (
	devHealthSkillURI  = "skill://dev-health/SKILL.md"
	devHealthSkillPath = "skills/dev-health/SKILL.md"
	// sha256 and size of skills/dev-health/SKILL.md at the pinned rev.
	devHealthSkillDigest = "sha256:3ab66de8655b25b87eeb69d6632479718f0c57a69975c1a5cbb61781f2984354"
	devHealthSkillSize   = 5306
)

func TestHostedServesTheVendoredDevHealthSkill(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	s := &skillsStack{hosted: hosted, e: e, caller: hosted.issue(readScopes, []string{repoAnswers}, nil)}
	reply := s.rpc(t, bearerHeader(s.caller.token), "skills/list", nil)
	var got struct {
		ResultType string           `json:"resultType"`
		Skills     []httpSkillEntry `json:"skills"`
	}
	if reply.Error != nil || json.Unmarshal(reply.Result, &got) != nil || got.ResultType != "complete" {
		t.Fatalf("skills/list answered %s", reply.raw)
	}
	if len(got.Skills) != 1 || got.Skills[0].URI != devHealthSkillURI || got.Skills[0].Frontmatter["name"] != "dev-health" {
		t.Fatalf("skills/list served %s, want the one Dev Health skill", reply.raw)
	}
	res := got.Skills[0].Resources
	if len(res) != 1 || res[0].URI != devHealthSkillURI || res[0].Digest != devHealthSkillDigest || res[0].Size != devHealthSkillSize {
		t.Fatalf("manifest %+v, want the pinned %s/%d", res, devHealthSkillDigest, devHealthSkillSize)
	}
	vendored, err := os.ReadFile(devHealthSkillPath)
	if err != nil {
		t.Fatal(err)
	}
	read := s.rpc(t, bearerHeader(s.caller.token), "resources/read", map[string]any{"uri": devHealthSkillURI})
	var body struct {
		Contents []struct {
			Text     string `json:"text"`
			MIMEType string `json:"mimeType"`
		} `json:"contents"`
	}
	if read.Error != nil || json.Unmarshal(read.Result, &body) != nil || len(body.Contents) != 1 {
		t.Fatalf("read %s answered %s", devHealthSkillURI, read.raw)
	}
	served := []byte(body.Contents[0].Text)
	sum := sha256.Sum256(served)
	if "sha256:"+hex.EncodeToString(sum[:]) != devHealthSkillDigest || len(served) != devHealthSkillSize || string(served) != string(vendored) || body.Contents[0].MIMEType != "text/markdown" {
		t.Fatalf("served bytes differ from the pinned skill")
	}
}
