package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// buildManifest is written by gocapture/build.sh next to the binary
// (spec §2). It is procedural provenance, not tamper-proofing.
type buildManifest struct {
	Schema                 string   `json:"schema"`
	BinarySHA256           string   `json:"binary_sha256"`
	GoVersion              string   `json:"go_version"`
	BuiltAt                string   `json:"built_at"`
	SourcePin              string   `json:"source_pin"`
	ProductionTreeClean    bool     `json:"production_tree_clean"`
	GoSumSHA256            string   `json:"go_sum_sha256"`
	ExperimentSourceSHA256 string   `json:"experiment_source_sha256"`
	VCSModifiedPaths       []string `json:"vcs_modified_paths"`
}

type buildIdentity struct {
	Manifest       buildManifest
	ManifestSHA256 string
	BinarySHA256   string
}

// buildFacts are the inputs verifyBuild checks; tests substitute them.
type buildFacts struct {
	ExecutablePath string
	VCSRevision    string
	RepoRoot       string
}

func currentBuildFacts() (buildFacts, error) {
	exe, err := os.Executable()
	if err != nil {
		return buildFacts{}, err
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildFacts{}, errors.New("no build info")
	}
	facts := buildFacts{ExecutablePath: exe}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			facts.VCSRevision = s.Value
		}
	}
	root, err := gitOutput(filepath.Dir(exe), "rev-parse", "--show-toplevel")
	if err != nil {
		return buildFacts{}, fmt.Errorf("locate repository: %w", err)
	}
	facts.RepoRoot = strings.TrimSpace(root)
	return facts, nil
}

func verifyBuild(facts buildFacts, sourcePin string) (buildIdentity, error) {
	manifestPath := facts.ExecutablePath + ".build.json"
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return buildIdentity{}, fmt.Errorf("build manifest: %w (build with gocapture/build.sh)", err)
	}
	var manifest buildManifest
	if err := strictUnmarshal(data, &manifest); err != nil {
		return buildIdentity{}, fmt.Errorf("build manifest: %w", err)
	}
	exeSHA, err := fileSHA256(facts.ExecutablePath)
	if err != nil {
		return buildIdentity{}, err
	}
	switch {
	case manifest.Schema != "gocapture.build.v1":
		return buildIdentity{}, errors.New("build manifest schema")
	case manifest.BinarySHA256 != exeSHA:
		return buildIdentity{}, errors.New("binary sha256 differs from the build manifest")
	case sourcePin == "" || manifest.SourcePin != sourcePin || facts.VCSRevision != sourcePin:
		return buildIdentity{}, errors.New("source pin, manifest pin and build vcs.revision must all agree")
	case !manifest.ProductionTreeClean:
		return buildIdentity{}, errors.New("the binary was built from a modified production tree")
	}
	for _, p := range manifest.VCSModifiedPaths {
		if !strings.HasPrefix(p, "experiments/") {
			return buildIdentity{}, fmt.Errorf("modified path outside experiments/: %s", p)
		}
	}
	if err := productionTreeClean(facts.RepoRoot, sourcePin); err != nil {
		return buildIdentity{}, err
	}
	return buildIdentity{Manifest: manifest, ManifestSHA256: sha256Hex(data), BinarySHA256: exeSHA}, nil
}

// productionTreeClean re-runs the build-time check at start.
func productionTreeClean(repoRoot, pin string) error {
	head, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != pin {
		return errors.New("checkout HEAD differs from the source pin")
	}
	status, err := gitOutput(repoRoot, "status", "--porcelain", "--", "internal", "cmd", "go.mod", "go.sum")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("production paths are modified in the checkout")
	}
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}
