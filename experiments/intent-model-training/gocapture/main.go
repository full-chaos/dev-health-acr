//go:build unix

// Command gocapture captures the incumbent interpreter (gpt-5.6-luna behind
// the production genkit runtime) on a sealed held-out set, with a wire proof
// for every request. SPEC-incumbent-capture.md is the contract.
//
// It makes real, billable model calls when run. Credentials come from the
// environment only and are never printed or recorded.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:], false)
	case "recover":
		err = cmdRun(os.Args[2:], true)
	case "approve":
		err = cmdApprove(os.Args[2:])
	case "approve-variant":
		err = cmdApproveVariant(os.Args[2:])
	case "derive":
		err = cmdDerive(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		// Fixed prefix; errors carry no request, response or credential text.
		fmt.Fprintln(os.Stderr, "gocapture:", err)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gocapture run|recover|approve|approve-variant|derive [flags]  (see SPEC-incumbent-capture.md)")
}

func defaultGolden() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "gocapture", "testdata", "expected_envelope.golden.json")
}

func cmdRun(args []string, recovery bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "", "intent-training data directory ($IT/data)")
	session := fs.String("session", "", "evaluation session id (opened by chris)")
	approval := fs.String("approval-id", "", "approval id in the approval ledger")
	runID := fs.String("run-id", "", "run id (new) or the run to resume")
	resume := fs.Bool("resume", false, "resume an existing run id")
	runCap := fs.Int("max-http-attempts", 0, "per-run HTTP attempt cap (1..720), bound durably to the run")
	pin := fs.String("source-pin", "", "ACR source revision the binary was built from")
	profile := fs.String("deployment-profile", "", "deployment profile JSON (required; no default)")
	golden := fs.String("golden", defaultGolden(), "expected envelope golden")
	interval := fs.Duration("min-interval", time.Second, "minimum time between HTTP sends")
	pair := fs.String("pair", "", "recover only: <row_id>:<replicate>")
	by := fs.String("authorized-by", "", "recover only: human:chris")
	reason := fs.String("reason", "", "recover only: why")
	recoverIndex := fs.Int("recovery-index", 0, "recover only: the next recovery index chris authorizes (1, 2, ...)")
	promptVariant := fs.String("prompt-variant", "", "prompt-variant name: capture the labelled control incumbent-variant[<name>], NOT the incumbent (needs --system-append-file and chris's approve-variant record)")
	appendFile := fs.String("system-append-file", "", "prompt variant only: the appendix added after the production system message (0600 file)")
	messageFile := fs.String("system-message-file", "", "prompt variant only, replace mode: the whole candidate system message, sent in place of the production system message (0600 file; not with --system-append-file)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := runConfig{DataRoot: *dataRoot, SessionID: *session, ApprovalID: *approval, RunID: *runID, Resume: *resume,
		RunCap: *runCap, SourcePin: *pin, ProfilePath: *profile, GoldenPath: *golden, MinInterval: *interval,
		PromptVariant: *promptVariant, SystemAppendFile: *appendFile, SystemMessageFile: *messageFile}
	if recovery {
		idx := strings.LastIndexByte(*pair, ':')
		if idx < 1 {
			return errors.New("--pair must be <row_id>:<replicate>")
		}
		rep, err := strconv.Atoi((*pair)[idx+1:])
		if err != nil || rep < 0 || rep > 2 {
			return errors.New("--pair replicate must be 0, 1 or 2")
		}
		cfg.RecoverRow, cfg.RecoverReplicate, cfg.RecoverBy, cfg.RecoverReason, cfg.Resume = (*pair)[:idx], rep, *by, *reason, true
		cfg.RecoverIndex = *recoverIndex
	}
	if cfg.DataRoot == "" || cfg.SessionID == "" || cfg.ApprovalID == "" {
		return errors.New("--data-root, --session and --approval-id are required")
	}
	if loopbackHook != nil {
		if err := loopbackHook(&cfg); err != nil {
			return err
		}
	}
	result, err := capture(context.Background(), cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "gocapture: terminal=%d uncertain=%d stop=%q incomplete=%v\n", result.Terminal, result.Uncertain, result.StopReason, result.Incomplete)
	if cfg.PromptVariant != "" {
		fmt.Fprintf(os.Stderr, "gocapture: series=%s (a prompt-variant control, not the incumbent)\n", variantCandidate(cfg.PromptVariant))
	}
	return nil
}

func cmdApproveVariant(args []string) error {
	fs := flag.NewFlagSet("approve-variant", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "", "intent-training data directory ($IT/data)")
	approval := fs.String("approval-id", "", "the existing approval id the variant's calls spend from")
	variant := fs.String("prompt-variant", "", "prompt-variant name")
	appendFile := fs.String("system-append-file", "", "the appendix file chris approves (bound by sha256)")
	messageFile := fs.String("system-message-file", "", "replace mode: the candidate system message file chris approves (bound by sha256; not with --system-append-file)")
	source := fs.String("source", "", "approval citation (ASCII)")
	by := fs.String("approved-by", "", "must be human:chris")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataRoot == "" || *variant == "" || (*appendFile == "") == (*messageFile == "") {
		return errors.New("--data-root, --prompt-variant and exactly one of --system-append-file or --system-message-file are required")
	}
	return approveVariantFile(*dataRoot, *approval, *variant, *appendFile, *messageFile, *source, *by)
}

func cmdApprove(args []string) error {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "", "intent-training data directory ($IT/data)")
	approval := fs.String("approval-id", "", "approval id")
	cap := fs.Int("cap", 0, "total HTTP attempt cap for the approval")
	source := fs.String("source", "", "approval citation (ASCII)")
	by := fs.String("approved-by", "", "must be human:chris")
	raise := fs.String("raise-reason", "", "required for a cap above 720")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataRoot == "" {
		return errors.New("--data-root is required")
	}
	return approve(*dataRoot, *approval, *cap, *source, *by, *raise)
}

func cmdDerive(args []string) error {
	fs := flag.NewFlagSet("derive", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "", "intent-training data directory ($IT/data)")
	session := fs.String("session", "", "evaluation session id")
	approval := fs.String("approval-id", "", "approval id")
	runID := fs.String("run-id", "", "run id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return deriveCommand(*dataRoot, *session, *approval, *runID)
}

// deriveCommand regenerates derived files for a ledger-bound run only; the
// run id is validated and every path is confined (sol r1 M6).
func deriveCommand(dataRoot, session, approval, runID string) error {
	if err := validName(runID); err != nil {
		return fmt.Errorf("--run-id: %w", err)
	}
	root, err := openRoot(dataRoot)
	if err != nil {
		return err
	}
	defer root.close()
	input, err := verifyInput(root, session)
	if err != nil {
		return err
	}
	heldout, err := root.walk(false, "heldout")
	if err != nil {
		return err
	}
	defer heldout.close()
	ledger, err := openLedger(heldout, approval, false)
	if err != nil {
		return err
	}
	defer ledger.close()
	started, ok := ledger.runs[runID]
	if !ok || started.SealDigest != input.Seal.SealDigest {
		return errors.New("the run id is not a started run of this seal in the ledger")
	}
	runDir, err := heldout.walk(false, input.Seal.Set, "captures", runID)
	if err != nil {
		return err
	}
	defer runDir.close()
	config, err := runDir.readFile("run.json")
	if err != nil || sha256Hex(config) != started.RunConfigSHA256 {
		return errors.New("run.json is missing or differs from the ledger")
	}
	rawDir, err := runDir.sub("raw", false)
	if err != nil {
		return err
	}
	defer rawDir.close()
	var recorded struct {
		InterferenceThresholdMS int64          `json:"interference_threshold_ms"`
		Variant                 *variantRecord `json:"variant"`
	}
	if err := json.Unmarshal(config, &recorded); err != nil {
		return err
	}
	if name := started.Variant; (recorded.Variant == nil) != (name == "") || (recorded.Variant != nil && recorded.Variant.Name != name) {
		return errors.New("run.json and the ledger disagree on the run's series (incumbent or prompt variant)")
	}
	return deriveOutputs(ledger, input, runID, runDir, rawDir, recorded.InterferenceThresholdMS)
}

// loopbackHook is set only in the gocapture_loopback test build (loopback.go):
// it points the allowlist at a 127.0.0.1 fake provider for end-to-end tests.
// The production build (gocapture/build.sh without --loopback) has none.
var loopbackHook func(*runConfig) error
