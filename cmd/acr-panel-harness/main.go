// Command acr-panel-harness is the CHAOS-3860 P6 activation driver's CLI
// entry point: it runs a configured multi-model panel through the two-turn
// select-and-continue confirmation flow against one organization's real
// data, speaking the hosted ACR contract directly over HTTP with real,
// per-panelist bearer credentials, and writes the resulting
// internal/panelharness.PanelRunManifest to disk.
//
// See docs/design/context-fabric-panel-run-manifest.md for the full design
// and scope discipline this binary follows -- in particular: it NEVER
// writes to acr.context_fabric_structure_selections.consensus_evidence (it
// has no database access at all), and it must never be pointed at the
// frozen evaluation corpus.
//
// Credential provisioning is deliberately OUT of this binary's scope: mint
// each panelist's bearer credential with the existing, already-shipped
// `acr-api credentials create --org-id <org> --repository-scopes <scopes>
// --scopes context:read,evidence:read --name <panelist> --actor <operator>`
// command (cmd/acr-api/credentials.go) and pass the resulting token via the
// panelist config file's bearer_token_env, never by inventing a second
// credential-minting path here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/panelharness"
)

// panelistConfig is one entry of the JSON array the --panelists flag names
// -- kept as a file rather than repeated flags because a real panel run
// names several panelists, each with several independent fields, and a
// file is reviewable/diffable in a way a wall of repeated flags is not.
type panelistConfig struct {
	CanonicalModelIdentity string `json:"canonical_model_identity"`
	// BearerTokenEnv names the environment variable holding this
	// panelist's own bearer credential -- the token itself is never
	// accepted as a literal in the config file, so a panelist config
	// committed to a repo or pasted into a report never carries a live
	// credential.
	BearerTokenEnv string `json:"bearer_token_env"`
	// FileExchangeDir, when set, drives this panelist through
	// internal/panelharness.FileExchangeSelector (a CLI/out-of-process
	// responder answers under this directory). Exactly one of
	// FileExchangeDir must be set per panelist in this version -- a
	// direct-API selector is a documented future extension point, not
	// implemented here (see Selector's own doc comment).
	FileExchangeDir string `json:"file_exchange_dir"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "acr-panel-harness:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr *os.File) error {
	flags := flag.NewFlagSet("acr-panel-harness", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiBaseURL := flags.String("api-base-url", "", "hosted ACR API base URL, e.g. https://acr.example.com (required)")
	orgID := flags.String("org-id", "", "organization id every panelist's credential is scoped to (required)")
	question := flags.String("question", "", "the natural-language question to run the panel on (required)")
	panelistsPath := flags.String("panelists", "", "path to a JSON array of panelist configs (required, see panelistConfig)")
	outputPath := flags.String("output", "", "path to write the panel run manifest JSON to (required)")
	callTimeout := flags.Duration("call-timeout", 60*time.Second, "per hosted-API call timeout")
	fileExchangeTimeout := flags.Duration("file-exchange-timeout", 5*time.Minute, "how long a file-exchange selector waits for a responder")
	var projectIDs, repositorySlugs, teamIDs stringSliceFlag
	flags.Var(&projectIDs, "project-id", "narrow RequestedScope.ProjectIDs (repeatable)")
	flags.Var(&repositorySlugs, "repository-slug", "narrow RequestedScope.RepositorySlugs (repeatable)")
	flags.Var(&teamIDs, "team-id", "narrow RequestedScope.TeamIDs (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*apiBaseURL) == "" || strings.TrimSpace(*orgID) == "" || strings.TrimSpace(*question) == "" || strings.TrimSpace(*panelistsPath) == "" || strings.TrimSpace(*outputPath) == "" {
		return errors.New("api-base-url, org-id, question, panelists, and output are all required")
	}

	configs, err := loadPanelistConfigs(*panelistsPath)
	if err != nil {
		return err
	}
	panelists := make([]panelharness.Panelist, 0, len(configs))
	for _, config := range configs {
		panelist, err := buildPanelist(config, *apiBaseURL, *callTimeout, *fileExchangeTimeout)
		if err != nil {
			return fmt.Errorf("panelist %s: %w", config.CanonicalModelIdentity, err)
		}
		panelists = append(panelists, panelist)
	}
	// codex adversarial review (round 2, HIGH): loadPanelistConfigs's own
	// distinct-directory check compares filepath.Abs results, which does
	// NOT resolve symlinks -- two differently-spelled paths (one behind a
	// symlink) could still name the SAME underlying directory, and each
	// FileExchangeSelector starts its own sequence numbering at zero, so
	// two panelists sharing a directory THIS WAY would collide on
	// identical request/response filenames undetected. Every buildPanelist
	// call above has already created its own requests/ subdirectory (this
	// is why the check runs here, after that loop, not before it): os.Stat
	// + os.SameFile compares actual device+inode identity, which a symlink
	// alias cannot disguise.
	if err := detectAliasedFileExchangeDirs(configs); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	manifest, err := panelharness.Run(ctx, panelharness.RunConfig{
		OrgID: *orgID, Question: *question, Panelists: panelists,
		BaseRequest: contractsv1.ContextFabricInvestigationRequest{
			RequestedScope: contractsv1.ContextFabricRequestedScope{
				ProjectIDs: projectIDs, RepositorySlugs: repositorySlugs, TeamIDs: teamIDs,
			},
			TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			Options: contractsv1.ContextFabricInvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
				AllowClarification: true,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("panel run: %w", err)
	}
	if err := manifest.WriteFile(*outputPath); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Fprintf(stdout, "wrote panel run manifest %s (panel_run_id=%s, %d member(s))\n", *outputPath, manifest.PanelRunID, len(manifest.Members))
	return nil
}

func loadPanelistConfigs(path string) ([]panelistConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read panelists config %s: %w", path, err)
	}
	var configs []panelistConfig
	if err := json.Unmarshal(raw, &configs); err != nil {
		return nil, fmt.Errorf("parse panelists config %s: %w", path, err)
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("panelists config %s named no panelists", path)
	}
	// codex adversarial review (round 1, HIGH): two panelists pointed at
	// the SAME file_exchange_dir would race on identical
	// "<seq6>-structure_selection.json" filenames (each FileExchangeSelector
	// starts its own sequence at zero, independently of any other
	// panelist's), so one panelist's request or response could overwrite
	// or be misread as another's. Every configured directory must be
	// distinct -- resolved to an absolute path first, so "./panel-a" and
	// "panel-a" (or a relative path from a different cwd) can't disguise
	// the same directory as two different ones. This is the CHEAP,
	// pre-existence check: filepath.Abs cannot resolve a symlink, so it
	// cannot catch two DIFFERENT paths that are the same directory via one
	// -- detectAliasedFileExchangeDirs (below), which runs once every
	// directory actually exists, is the authoritative check for that
	// (codex round 2, HIGH).
	seenDirs := make(map[string]string, len(configs)) // absolute dir -> first identity that used it
	for _, config := range configs {
		if strings.TrimSpace(config.FileExchangeDir) == "" {
			continue // buildPanelist reports this specific error per-panelist
		}
		absolute, err := filepath.Abs(config.FileExchangeDir)
		if err != nil {
			return nil, fmt.Errorf("resolve file_exchange_dir %q: %w", config.FileExchangeDir, err)
		}
		if first, duplicate := seenDirs[absolute]; duplicate {
			return nil, fmt.Errorf("panelists %q and %q share the same file_exchange_dir %q -- every panelist needs its own directory", first, config.CanonicalModelIdentity, config.FileExchangeDir)
		}
		seenDirs[absolute] = config.CanonicalModelIdentity
	}
	return configs, nil
}

// detectAliasedFileExchangeDirs is the authoritative distinct-directory
// check (codex adversarial review, round 2, HIGH): it compares each
// config's own requests/ subdirectory by os.SameFile (device+inode
// identity), which a symlink -- unlike loadPanelistConfigs's own
// filepath.Abs comparison -- cannot disguise. Must run only after every
// panelist has already been built (each buildPanelist call creates its own
// requests/ subdirectory via NewFileExchangeSelector), since os.Stat needs
// the path to exist.
func detectAliasedFileExchangeDirs(configs []panelistConfig) error {
	type statted struct {
		identity string
		info     os.FileInfo
	}
	seen := make([]statted, 0, len(configs))
	for _, config := range configs {
		if strings.TrimSpace(config.FileExchangeDir) == "" {
			continue
		}
		requestsDir := filepath.Join(config.FileExchangeDir, "requests")
		info, err := os.Stat(requestsDir)
		if err != nil {
			return fmt.Errorf("stat %s: %w", requestsDir, err)
		}
		for _, other := range seen {
			if os.SameFile(info, other.info) {
				return fmt.Errorf("panelists %q and %q share the same file-exchange directory (one is a symlink alias of the other) -- every panelist needs its own, independent directory", other.identity, config.CanonicalModelIdentity)
			}
		}
		seen = append(seen, statted{identity: config.CanonicalModelIdentity, info: info})
	}
	return nil
}

func buildPanelist(config panelistConfig, apiBaseURL string, callTimeout, fileExchangeTimeout time.Duration) (panelharness.Panelist, error) {
	if strings.TrimSpace(config.CanonicalModelIdentity) == "" {
		return panelharness.Panelist{}, errors.New("canonical_model_identity is required")
	}
	if strings.TrimSpace(config.BearerTokenEnv) == "" {
		return panelharness.Panelist{}, errors.New("bearer_token_env is required")
	}
	token := os.Getenv(config.BearerTokenEnv)
	if strings.TrimSpace(token) == "" {
		return panelharness.Panelist{}, fmt.Errorf("environment variable %s is unset or empty", config.BearerTokenEnv)
	}
	client, err := panelharness.NewClient(apiBaseURL, token, callTimeout)
	if err != nil {
		return panelharness.Panelist{}, err
	}
	if strings.TrimSpace(config.FileExchangeDir) == "" {
		return panelharness.Panelist{}, errors.New("file_exchange_dir is required (the only implemented Selector transport in this version)")
	}
	selector, err := panelharness.NewFileExchangeSelector(config.FileExchangeDir, config.CanonicalModelIdentity, fileExchangeTimeout)
	if err != nil {
		return panelharness.Panelist{}, err
	}
	return panelharness.Panelist{CanonicalModelIdentity: config.CanonicalModelIdentity, Client: client, Selector: selector}, nil
}

// stringSliceFlag implements flag.Value for a repeatable string flag.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}
