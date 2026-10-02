//go:build unix

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const captureOrgID = "intent-training-capture"

type runConfig struct {
	DataRoot    string
	SessionID   string
	ApprovalID  string
	RunID       string
	Resume      bool
	RunCap      int
	SourcePin   string
	ProfilePath string
	GoldenPath  string
	MinInterval time.Duration
	// Recovery (gocapture recover): one uncertain pair, one explicit index.
	RecoverRow       string
	RecoverReplicate int
	RecoverIndex     int
	RecoverBy        string
	RecoverReason    string
	// Prompt variant (variant.go): both empty for the incumbent capture.
	PromptVariant    string
	SystemAppendFile string

	// Unexported seams for tests only; main never sets them.
	allow      *allowlist
	lookup     func(string) (string, bool)
	build      *buildIdentity
	next       http.RoundTripper
	fault      func(string) error
	now        func() time.Time
	sleep      func(time.Duration)
	stderr     io.Writer
	ledgerHook func(*approvalLedger)
}

type runConfigRecord struct {
	Schema              string   `json:"schema"`
	RunID               string   `json:"run_id"`
	ApprovalID          string   `json:"approval_id"`
	Set                 string   `json:"set"`
	SealDigest          string   `json:"seal_digest"`
	MembershipDigest    string   `json:"membership_digest"`
	SessionID           string   `json:"session_id"`
	InputSHA256         string   `json:"input_sha256"`
	ProfileSHA256       string   `json:"profile_sha256"`
	GoldenSHA256        string   `json:"golden_sha256"`
	BuildManifestSHA256 string   `json:"build_manifest_sha256"`
	BinarySHA256        string   `json:"binary_sha256"`
	SourcePin           string   `json:"source_pin"`
	Allowlist           []string `json:"allowlist"`
	MinIntervalMS       int64    `json:"min_interval_ms"`
	RunCapHTTPAttempts  int      `json:"run_cap_http_attempts"`
	Reopening           bool     `json:"reopening"`
	// Harness overhead at or above this, inside an invocation, is material.
	InterferenceThresholdMS int64 `json:"interference_threshold_ms"`
	// Variant is set for a prompt-variant run only (variant.go); an
	// incumbent run.json has no such member.
	Variant *variantRecord `json:"variant,omitempty"`
}

type runResult struct {
	StopReason string
	Terminal   int
	Uncertain  int
	Incomplete bool
}

func (c *runConfig) defaults() {
	if c.lookup == nil {
		c.lookup = os.LookupEnv
	}
	if c.next == nil {
		c.next = http.DefaultTransport
	}
	if c.fault == nil {
		c.fault = func(string) error { return nil }
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = time.Sleep
	}
	if c.stderr == nil {
		c.stderr = os.Stderr
	}
}

func prefixed(fault func(string) error, prefix string) func(string) error {
	return func(p string) error { return fault(prefix + p) }
}

// capture is the whole run (spec §3–§8). Every check, including the full
// production runtime construction, runs before the first write (sol r1 M8).
func capture(ctx context.Context, cfg runConfig) (runResult, error) {
	cfg.defaults()
	allow := productionAllowlist
	if cfg.allow != nil {
		allow = *cfg.allow
	}
	if cfg.RunCap < 1 || cfg.RunCap > hardApprovalCap {
		return runResult{}, fmt.Errorf("--max-http-attempts must be from 1 to %d", hardApprovalCap)
	}
	if cfg.MinInterval < 0 {
		return runResult{}, errors.New("--min-interval must not be negative")
	}
	if err := validName(cfg.RunID); err != nil {
		return runResult{}, fmt.Errorf("--run-id: %w", err)
	}
	variant, err := loadVariant(cfg.PromptVariant, cfg.SystemAppendFile)
	if err != nil {
		return runResult{}, err
	}
	build := cfg.build
	if build == nil {
		facts, err := currentBuildFacts()
		if err != nil {
			return runResult{}, err
		}
		b, err := verifyBuild(facts, cfg.SourcePin)
		if err != nil {
			return runResult{}, err
		}
		build = &b
	}
	profile, profileBytes, err := loadProfile(cfg.ProfilePath)
	if err != nil {
		return runResult{}, err
	}
	tuning, err := profile.validate(allow, cfg.SourcePin)
	if err != nil {
		return runResult{}, err
	}
	if err := checkEnvAgrees(cfg.lookup, tuning); err != nil {
		return runResult{}, err
	}
	modelConfig, err := modelprovider.ConfigFromEnv(cfg.lookup)
	if err != nil {
		return runResult{}, fmt.Errorf("model configuration: %w", err)
	}
	if err := checkIdentity(modelConfig, allow); err != nil {
		return runResult{}, err
	}
	applyTuning(&modelConfig, tuning)
	golden, err := loadGolden(cfg.GoldenPath)
	if err != nil {
		return runResult{}, err
	}
	goldenBytes, err := os.ReadFile(cfg.GoldenPath)
	if err != nil {
		return runResult{}, err
	}
	// The transport exists before the runtime (its logger), but it carries
	// no ledger and refuses every request until an invocation is admitted.
	transport := &captureTransport{next: cfg.next, allow: allow, model: allow.Model, golden: golden,
		scanner: newCredScanner(modelConfig.APIKey), now: cfg.now, variant: variant}
	if variant != nil {
		if first := golden.Messages[0]; first.Role != "system" || first.ContentKind != "string" {
			return runResult{}, errors.New("a prompt variant needs a system message with string content first in the envelope golden")
		}
		if transport.scanner.scan(variant.Appendix) {
			return runResult{}, errors.New("the system append file holds the configured credential")
		}
	}
	modelConfig.Logger = slog.New(&drawObserver{transport: transport})
	runtime, err := modelprovider.NewGenkitRuntime(ctx, modelConfig)
	if err != nil {
		return runResult{}, fmt.Errorf("construct the production runtime: %w", err)
	}

	root, err := openRoot(cfg.DataRoot)
	if err != nil {
		return runResult{}, err
	}
	defer root.close()
	input, err := verifyInput(root, cfg.SessionID)
	if err != nil {
		return runResult{}, err
	}
	set := input.Seal.Set
	record := runConfigRecord{
		Schema: "gocapture.run-config.v1", RunID: cfg.RunID, ApprovalID: cfg.ApprovalID, Set: set,
		SealDigest: input.Seal.SealDigest, MembershipDigest: input.Seal.MembershipDigest, SessionID: cfg.SessionID,
		InputSHA256: input.Digest, ProfileSHA256: sha256Hex(profileBytes), GoldenSHA256: sha256Hex(goldenBytes),
		BuildManifestSHA256: build.ManifestSHA256, BinarySHA256: build.BinarySHA256, SourcePin: cfg.SourcePin,
		Allowlist:     []string{allow.Provider, allow.BaseURL, allow.Model, fmt.Sprint(allow.Insecure), allow.Scheme, allow.Host, allow.Path},
		MinIntervalMS: cfg.MinInterval.Milliseconds(), RunCapHTTPAttempts: cfg.RunCap, Reopening: input.Reopen,
		InterferenceThresholdMS: interferenceThreshold(tuning).Milliseconds(),
		Variant:                 variant.record(input.Seal.ExpectedSystemMessageSHA256, ""),
	}
	configBytes, err := cj(record)
	if err != nil {
		return runResult{}, err
	}
	configSHA := sha256Hex(configBytes)

	heldout, err := root.walk(false, "heldout")
	if err != nil {
		return runResult{}, err
	}
	defer heldout.close()
	review, err := root.walk(false, "review")
	if err != nil {
		return runResult{}, err
	}
	defer review.close()
	ledger, err := openLedger(heldout, cfg.ApprovalID, false)
	if err != nil {
		return runResult{}, err
	}
	defer ledger.close()
	if cfg.ledgerHook != nil {
		cfg.ledgerHook(ledger)
	}
	if !ledger.approved {
		return runResult{}, errors.New("no approval record for this approval id (gocapture approve, by chris)")
	}
	if variant != nil {
		// A variant run makes live calls under the same approval. It needs
		// chris's own record that names this variant and this appendix.
		if approved, ok := ledger.variants[variant.Name]; !ok {
			return runResult{}, fmt.Errorf("no variant_approval record names prompt variant %q under this approval id (gocapture approve-variant, by chris)", variant.Name)
		} else if approved != variant.AppendixSHA256 {
			return runResult{}, fmt.Errorf("prompt variant %q is approved for another appendix (sha256 %s): the file changed, so it needs a new variant name and approval", variant.Name, approved)
		}
	}
	prior, started := ledger.runs[cfg.RunID]
	if cfg.Resume != started {
		if started {
			return runResult{}, errors.New("this run id already started: use --resume")
		}
		return runResult{}, errors.New("--resume names a run that never started")
	}
	if started && prior.RunConfigSHA256 != configSHA {
		return runResult{}, errors.New("run configuration changed since the run started; resume refused")
	}
	// The pairs of THIS series: the incumbent's, or this prompt variant's.
	pairs := ledger.pairsOf(variant.name())
	if !started {
		for k, st := range pairs {
			if k.SealDigest == input.Seal.SealDigest && st.Reserved {
				return runResult{}, errors.New("pairs of this seal are already reserved under another run id: resume that run")
			}
		}
	}
	needed := 0
	for _, row := range input.Rows {
		for r := 0; r < 3; r++ {
			if st, ok := pairs[pairKey{input.Seal.SealDigest, row.RowID, r}]; !ok || !st.Reserved {
				needed++
			}
		}
	}
	if cfg.RecoverRow == "" && needed > 0 && (ledger.remaining() < needed || cfg.RunCap-ledger.runSpent[cfg.RunID] < needed) {
		return runResult{}, fmt.Errorf("budget too small: %d pairs still need at least %d HTTP attempts", needed, needed)
	}
	runDir, err := heldout.walk(true, set, "captures", cfg.RunID)
	if err != nil {
		return runResult{}, err
	}
	defer runDir.close()
	rawDir, err := runDir.sub("raw", true)
	if err != nil {
		return runResult{}, err
	}
	defer rawDir.close()
	if started {
		existing, err := runDir.readFile("run.json")
		if err != nil || sha256Hex(existing) != configSHA {
			return runResult{}, errors.New("run.json is missing or differs from the ledger")
		}
		if err := runDir.removeTemps(); err != nil {
			return runResult{}, err
		}
		if err := rawDir.removeTemps(); err != nil {
			return runResult{}, err
		}
		if variant != nil {
			// The appendix the evaluator rebuilds the system message from.
			kept, err := runDir.readFile(systemAppendName)
			if err != nil || sha256Hex(kept) != variant.AppendixSHA256 {
				return runResult{}, errors.New(systemAppendName + " is missing from the run directory or differs from the appendix")
			}
		}
	} else {
		if err := runDir.publishWriteOnce("run.json", configBytes, prefixed(cfg.fault, "runjson_")); err != nil {
			return runResult{}, fmt.Errorf("publish run.json: %w", err)
		}
		started := ledgerRecord{Kind: "run_started", RunID: cfg.RunID, Set: set, SealDigest: input.Seal.SealDigest, RunConfigSHA256: configSHA}
		if variant != nil {
			if err := runDir.publishWriteOnce(systemAppendName, variant.Appendix, prefixed(cfg.fault, "append_")); err != nil {
				return runResult{}, fmt.Errorf("publish %s: %w", systemAppendName, err)
			}
			started.Variant, started.AppendixSHA256 = variant.Name, variant.AppendixSHA256
		}
		n := cfg.RunCap
		started.RunCapHTTPAttempts = &n
		if err := ledger.append(started); err != nil {
			return runResult{}, err
		}
	}
	event := "start"
	if started {
		event = "resume"
	}
	binSHA := build.BinarySHA256
	if err := appendExposure(review, openingsRecord{SessionID: cfg.SessionID, Event: event, RunID: cfg.RunID, SealDigest: input.Seal.SealDigest, BinarySHA256: &binSHA}, cfg.now()); err != nil {
		return runResult{}, fmt.Errorf("record exposure: %w", err)
	}
	if err := cfg.fault("after_exposure"); err != nil {
		return runResult{}, err
	}
	transport.ledger = ledger
	restore := installDefaultTransport(transport)
	defer restore()
	session := &captureSession{
		cfg: cfg, input: input, ledger: ledger, transport: transport, runDir: runDir, rawDir: rawDir, review: review,
		configSHA: configSHA, profileSHA: record.ProfileSHA256, build: *build, tuning: tuning,
		interpret: runtime.InterpretQuestion, variant: variant,
	}
	if cfg.RecoverRow != "" {
		return session.recover(ctx)
	}
	return session.all(ctx)
}

type interpretFunc func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error)

type captureSession struct {
	cfg        runConfig
	input      verifiedInput
	ledger     *approvalLedger
	transport  *captureTransport
	runDir     *cdir
	rawDir     *cdir
	review     *cdir
	configSHA  string
	profileSHA string
	build      buildIdentity
	tuning     effectiveTuning
	interpret  interpretFunc
	// variant is nil for the incumbent capture.
	variant *promptVariant
}

// pairs is this run's series in the ledger: the incumbent's pairs, or the
// pairs of this run's prompt variant.
func (s *captureSession) pairs() map[pairKey]*pairState {
	return s.ledger.pairsOf(s.variant.name())
}

// reconcile settles every reserved-but-unfinished pair and recovery from
// durable evidence. It never sends (sol r1 H2, H3).
func (s *captureSession) reconcile() error {
	for _, row := range s.input.Rows {
		for replicate := 0; replicate < 3; replicate++ {
			key := pairKey{SealDigest: s.input.Seal.SealDigest, RowID: row.RowID, Replicate: replicate}
			st := s.pairs()[key]
			if st == nil {
				continue
			}
			if st.Reserved && st.Terminal == nil && !st.Uncertain {
				if err := s.settle(key, 0); err != nil {
					return err
				}
			}
			for i := 1; i <= len(st.Recoveries); i++ {
				if rs := st.Recoveries[i]; rs != nil && rs.Reserved && rs.Terminal == nil && !rs.Uncertain {
					if err := s.settle(key, i); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// settle completes a pair (or recovery) whose artifact was published before
// a crash, or marks it uncertain. It never resends.
func (s *captureSession) settle(key pairKey, recovery int) error {
	name := artifactName(key.RowID, key.Replicate, recovery)
	rep := key.Replicate
	variant := s.variant.name()
	a, data, err := readArtifact(s.rawDir, name)
	if err == nil && a.ApprovalID == s.cfg.ApprovalID && a.SealDigest == key.SealDigest && a.RowID == key.RowID &&
		a.Replicate == key.Replicate && a.RunID == s.cfg.RunID && a.RunConfigSHA256 == s.configSHA && a.RecoveryIndex == recovery &&
		a.seriesError(variant) == nil {
		pr := a.ProductionReturned
		rec := ledgerRecord{Kind: "pair_terminal", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep,
			Outcome: a.Outcome, ProductionReturned: &pr, ArtifactSHA256: sha256Hex(data), ArtifactName: name, Variant: variant}
		if recovery > 0 {
			t, i := true, recovery
			rec.Recovered, rec.RecoveryIndex = &t, &i
		}
		return s.ledger.append(rec)
	}
	if recovery > 0 {
		i := recovery
		return s.ledger.append(ledgerRecord{Kind: "recovery_uncertain", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep, RecoveryIndex: &i, Variant: variant})
	}
	return s.ledger.append(ledgerRecord{Kind: "pair_uncertain", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep, Variant: variant})
}

// stop records the stop durably, then derives outputs.
func (s *captureSession) stop(result runResult, reason string) (runResult, error) {
	result.StopReason, result.Incomplete = reason, true
	if s.ledger.stopped[s.cfg.RunID] == "" {
		if err := s.ledger.append(ledgerRecord{Kind: "run_stopped", RunID: s.cfg.RunID, Reason: reason, Variant: s.variant.name()}); err != nil {
			return result, err
		}
	}
	return result, s.derive()
}

func (s *captureSession) all(ctx context.Context) (runResult, error) {
	var result runResult
	if err := s.reconcile(); err != nil {
		return result, err
	}
	// Rebuild every stop condition from durable records before any send.
	if reason := s.ledger.stopReason(s.cfg.RunID); reason != "" {
		return s.stop(result, reason)
	}
	for _, row := range s.input.Rows {
		for replicate := 0; replicate < 3; replicate++ {
			key := pairKey{SealDigest: s.input.Seal.SealDigest, RowID: row.RowID, Replicate: replicate}
			if st := s.pairs()[key]; st != nil && st.Reserved {
				continue
			}
			if _, err := s.capturePair(ctx, key, row, 0); err != nil {
				result.StopReason = outcomeWriteFailure
				return result, err
			}
			result.Terminal++
			if reason := s.ledger.stopReason(s.cfg.RunID); reason != "" {
				return s.stop(result, reason)
			}
		}
	}
	return result, s.derive()
}

// pace waits between invocations, before any deadline is armed. The
// boundary is durable: the last reservation time in the ledger survives a
// restart (sol r1 H4, M7).
func (s *captureSession) pace() {
	if s.cfg.MinInterval <= 0 {
		return
	}
	last := s.ledger.lastReservedAt
	if s.transport.lastSend.After(last) {
		last = s.transport.lastSend
	}
	if last.IsZero() {
		return
	}
	if wait := s.cfg.MinInterval - s.cfg.now().Sub(last); wait > 0 {
		s.cfg.sleep(wait)
	}
}

func (s *captureSession) capturePair(ctx context.Context, key pairKey, row captureRow, recovery int) (string, error) {
	rep := key.Replicate
	s.pace()
	// Admission: the session is re-checked, and held open, under the
	// openings lock for the whole invocation (sol r1 M5).
	lock, err := admit(s.review, s.cfg.SessionID)
	if err != nil {
		return "", err
	}
	defer lock.release()
	variant := s.variant.name()
	if recovery == 0 {
		if err := s.ledger.append(ledgerRecord{Kind: "pair_reserved", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep, Variant: variant}); err != nil {
			return "", err
		}
	} else {
		i := recovery
		if err := s.ledger.append(ledgerRecord{Kind: "recovery_reserved", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep, RecoveryIndex: &i, Variant: variant}); err != nil {
			return "", err
		}
	}
	if err := s.cfg.fault("after_pair_reserved"); err != nil {
		return "", err
	}
	inv := &invocation{pair: key, runID: s.cfg.RunID, question: row.Rendered.Decoded.Request.Question, systemSHA: s.input.Seal.ExpectedSystemMessageSHA256, inputSHA: row.Rendered.InputSHA256}
	s.transport.begin(inv)
	callCtx, cancel := context.WithTimeout(ctx, s.tuning.RequestTimeout)
	started := s.cfg.now()
	interpreted, receipt, callErr := s.interpret(callCtx, storage.Principal{OrgID: captureOrgID, Subject: captureOrgID}, row.Rendered.Decoded.Request)
	cancel()
	elapsed := s.cfg.now().Sub(started)
	s.transport.end()
	if n := len(inv.attempts); n > 0 {
		s.transport.lastSend = s.cfg.now()
	}
	if err := s.cfg.fault("after_send"); err != nil {
		return "", err
	}
	a := s.buildArtifact(key, row, inv, interpreted, receipt, callErr, elapsed, recovery)
	s.finalGate(&a)
	data, err := a.seal()
	if err != nil {
		return "", err
	}
	name := artifactName(key.RowID, key.Replicate, recovery)
	if err := s.rawDir.publishWriteOnce(name, data, prefixed(s.cfg.fault, "artifact_")); err != nil {
		return "", fmt.Errorf("publish artifact: %w", err)
	}
	if err := s.cfg.fault("before_pair_terminal"); err != nil {
		return "", err
	}
	pr := a.ProductionReturned
	term := ledgerRecord{Kind: "pair_terminal", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep, Outcome: a.Outcome, ProductionReturned: &pr, ArtifactSHA256: sha256Hex(data), ArtifactName: name, Variant: variant}
	if recovery > 0 {
		t, i := true, recovery
		term.Recovered, term.RecoveryIndex = &t, &i
	}
	if err := s.ledger.append(term); err != nil {
		return "", err
	}
	return a.Outcome, nil
}

// finalGate scans every field about to be persisted, decoded bodies
// included; a hit suppresses all content and stops the run (sol r1 H1).
func (s *captureSession) finalGate(a *artifact) {
	scanner := s.transport.scanner
	hit := false
	if a.RawText != nil && scanner.scan([]byte(*a.RawText)) {
		hit = true
	}
	for _, at := range a.Attempts {
		for _, field := range []*string{at.RequestBodyBase64, at.ResponseBodyBase64} {
			if field == nil {
				continue
			}
			if decoded, err := base64.StdEncoding.DecodeString(*field); err != nil || scanner.scan(decoded) {
				hit = true
			}
		}
	}
	if body, err := json.Marshal(a); err == nil && scanner.scan(body) {
		hit = true
	}
	if hit {
		a.Outcome, a.RunnerOwned, a.ProductionReturned, a.FinishReason = outcomeCredentialEcho, true, false, "error"
		a.LatchTripped, a.LatchReason = true, outcomeCredentialEcho
		suppressContent(a)
	}
}

func (s *captureSession) buildArtifact(key pairKey, row captureRow, inv *invocation, interpreted contextfabric.InterpretedQuestion, receipt contextfabric.ModelExecutionReceipt, callErr error, elapsed time.Duration, recovery int) artifact {
	a := artifact{
		Schema: artifactSchema, ApprovalID: s.cfg.ApprovalID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: key.Replicate,
		RunID: s.cfg.RunID, ExampleID: row.ExampleID, Set: s.input.Seal.Set, SourcePin: s.cfg.SourcePin,
		BinarySHA256: s.build.BinarySHA256, ProfileSHA256: s.profileSHA, RunConfigSHA256: s.configSHA,
		Attempts: inv.attempts, DrawAuthorizations: inv.authorizations, LatchTripped: inv.latch != "", LatchReason: inv.latch,
		DiagnosticAttemptSeqs: []int{}, DurationMS: elapsed.Milliseconds(), HarnessOverheadMS: inv.overhead.Milliseconds(),
		Recovered: recovery > 0, RecoveryIndex: recovery, CapturedAt: s.cfg.now().UTC().Format(time.RFC3339Nano),
		Receipt: receiptSummary{Outcome: receipt.Outcome, Attempts: receipt.Attempts, FallbackUsed: receipt.FallbackUsed, ErrorClass: classifyRuntimeError(callErr),
			ModelVersion: receipt.ModelVersion, InputTokens: receipt.Usage.InputTokens, OutputTokens: receipt.Usage.OutputTokens},
	}
	if s.variant != nil {
		// A prompt-variant artifact: its own schema and the variant block,
		// so no reader can take it for an incumbent artifact.
		a.Schema = variantArtifactSchema
		a.Variant = s.variant.record(s.input.Seal.ExpectedSystemMessageSHA256, inv.variantSystemSHA)
	}
	if a.Attempts == nil {
		a.Attempts = []attemptRecord{}
	}
	if a.DrawAuthorizations == nil {
		a.DrawAuthorizations = []drawAuthorization{}
	}
	if recovery > 0 {
		a.RecoveryOf = artifactName(key.RowID, key.Replicate, 0)
	}
	if inv.latch != "" {
		a.Outcome, a.RunnerOwned, a.FinishReason = inv.latch, true, "error"
		if inv.latch == outcomeCredentialEcho {
			suppressContent(&a)
		}
		return a
	}
	// The final draw is the last draw actually SENT. The rejection event of
	// the last allowed draw still authorizes a next draw that never happens.
	finalDraw := -1
	for _, at := range inv.attempts {
		if at.Sent && at.Draw > finalDraw {
			finalDraw = at.Draw
		}
	}
	content := -1
	for i := len(inv.attempts) - 1; i >= 0; i-- {
		at := inv.attempts[i]
		if at.Draw == finalDraw && at.Status >= 200 && at.Status < 300 {
			content = i
			break
		}
	}
	for i, at := range inv.attempts {
		if at.Status >= 200 && at.Status < 300 && i != content {
			a.DiagnosticAttemptSeqs = append(a.DiagnosticAttemptSeqs, at.AttemptSeq)
		}
	}
	if callErr == nil {
		if content < 0 {
			a.Outcome, a.RunnerOwned, a.FinishReason = outcomeTrace, true, "error"
			return a
		}
		text, finish, err := messageContent(inv.attempts[content])
		if err != nil {
			a.Outcome, a.RunnerOwned, a.FinishReason = outcomeTrace, true, "error"
			return a
		}
		seq := inv.attempts[content].AttemptSeq
		a.Outcome, a.ProductionReturned, a.ContentAttemptSeq, a.RawText, a.FinishReason = "ok", true, &seq, &text, finish
		if normalized, err := json.Marshal(interpreted); err == nil {
			a.NormalizedSHA256 = sha256Hex(normalized)
		}
		s.checkInterference(&a, inv)
		return a
	}
	if content >= 0 {
		a.DiagnosticAttemptSeqs = append(a.DiagnosticAttemptSeqs, inv.attempts[content].AttemptSeq)
	}
	a.Outcome, a.FinishReason = classifyRuntimeError(callErr), "error"
	s.checkInterference(&a, inv)
	return a
}

// interferenceThreshold is the fixed materiality rule (spec R5): harness
// time inside one invocation is material at 5% of the tightest deadline
// that applies to it, the per-attempt model timeout or the request deadline.
func interferenceThreshold(t effectiveTuning) time.Duration {
	d := t.RequestTimeout
	if t.ModelTimeout < d {
		d = t.ModelTimeout
	}
	return d / 20
}

// checkInterference: harness time inside production's deadlines can turn a
// served answer into a failure, or suppress a retry or redraw (sol r2). With
// material overhead, a non-ok outcome, or an ok outcome reached only after a
// timed-out or failed attempt, is not the incumbent's: the pair becomes
// harness_interference (runner-owned, not scored, capture incomplete).
func (s *captureSession) checkInterference(a *artifact, inv *invocation) {
	if a.RunnerOwned || inv.overhead < interferenceThreshold(s.tuning) {
		return
	}
	retried := false
	for _, at := range inv.attempts {
		if at.Sent && (at.TransportError != "" || at.Status < 200 || at.Status >= 300) {
			retried = true
		}
	}
	if a.Outcome != "ok" || retried {
		a.Outcome, a.RunnerOwned, a.ProductionReturned, a.FinishReason = outcomeInterference, true, false, "error"
		a.RawText, a.ContentAttemptSeq, a.NormalizedSHA256 = nil, nil, ""
	}
}

func suppressContent(a *artifact) {
	a.RawText, a.ContentAttemptSeq = nil, nil
	a.NormalizedSHA256 = ""
	for i := range a.Attempts {
		a.Attempts[i].ResponseBodyBase64 = nil
		a.Attempts[i].RequestBodyBase64 = nil
		a.Attempts[i].SystemFingerprint = ""
	}
}

func classifyRuntimeError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, contextfabric.ErrModelRateLimited):
		return "rate_limited"
	case errors.Is(err, contextfabric.ErrModelOutput):
		return "invalid_output"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, contextfabric.ErrModelUnavailable):
		return "model_unavailable"
	}
	return "runtime_error"
}

func messageContent(at attemptRecord) (string, string, error) {
	if at.ResponseBodyBase64 == nil {
		return "", "", errors.New("no response body")
	}
	body, err := base64.StdEncoding.DecodeString(*at.ResponseBodyBase64)
	if err != nil {
		return "", "", err
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == nil {
		return "", "", errors.New("no message content")
	}
	finish := parsed.Choices[0].FinishReason
	if finish != "stop" && finish != "length" {
		finish = "error"
	}
	return *parsed.Choices[0].Message.Content, finish, nil
}

// recover captures one uncertain pair under one explicit, durable recovery
// index (spec §7, sol r1 H2). Pending recoveries are settled first; a used
// index is never sent again.
func (s *captureSession) recover(ctx context.Context) (runResult, error) {
	key := pairKey{SealDigest: s.input.Seal.SealDigest, RowID: s.cfg.RecoverRow, Replicate: s.cfg.RecoverReplicate}
	if err := s.reconcile(); err != nil {
		return runResult{}, err
	}
	st := s.pairs()[key]
	if st == nil || !st.Uncertain {
		return runResult{}, errors.New("recovery applies only to an uncertain pair")
	}
	if s.cfg.RecoverBy != "human:chris" || s.cfg.RecoverReason == "" {
		return runResult{}, errors.New("recovery needs --authorized-by human:chris and --reason")
	}
	if st.recoveryTerminal() != nil {
		return runResult{Terminal: 1}, s.derive()
	}
	if s.cfg.RecoverIndex != st.Authorizations+1 {
		return runResult{}, fmt.Errorf("recovery index %d is used or out of order: the next authorizable index is %d", s.cfg.RecoverIndex, st.Authorizations+1)
	}
	if reason := s.ledger.stopReason(s.cfg.RunID); reason != "" && reason != "consecutive_model_unavailable" {
		return runResult{}, fmt.Errorf("the run is stopped (%s): recovery refused", reason)
	}
	var row *captureRow
	for i := range s.input.Rows {
		if s.input.Rows[i].RowID == key.RowID {
			row = &s.input.Rows[i]
		}
	}
	if row == nil {
		return runResult{}, errors.New("row not in the seal")
	}
	rep, idx := key.Replicate, s.cfg.RecoverIndex
	if err := s.ledger.append(ledgerRecord{Kind: "recovery_authorized", RunID: s.cfg.RunID, SealDigest: key.SealDigest, RowID: key.RowID, Replicate: &rep, RecoveryIndex: &idx, AuthorizedBy: s.cfg.RecoverBy, Reason: s.cfg.RecoverReason, Variant: s.variant.name()}); err != nil {
		return runResult{}, err
	}
	outcome, err := s.capturePair(ctx, key, *row, idx)
	if err != nil {
		return runResult{}, err
	}
	return runResult{Terminal: 1, StopReason: outcome}, s.derive()
}
