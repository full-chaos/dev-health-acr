package falkorgraph

import (
	"regexp"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is the CHAOS-3835 (embed-text spec v2 §5 L5, §6.5 T5) id-only
// skip: a RECORD-level embed decision, distinct from embedKindSkipped's
// WHOLE-KIND decision (vector_projection.go). L5's measured target is the
// ci_pipeline_run kind -- 78% of the live corpus, degenerating to
// `CI run repo: X` for the 22% of rows with neither a pipeline_name nor a
// branch (see ciRunSearchText's own "L5/T5 skip-list" comment) -- so this
// is scoped to that kind today, at the SAME seam (collectEmbedTargets) the
// kind skip-list and MaxTextRunes gating already apply. Extending the skip
// to another kind is a template-shaped decision for that kind's own §2
// section, not a change to the general-purpose primitive below.

// idOnlyPureDigits matches a token that is NOTHING but Unicode decimal
// digits (Nd, which includes fullwidth digits, Arabic-indic digits, etc --
// deliberately not ASCII \d, since an id is exactly as "no semantic
// content" regardless of script).
var idOnlyPureDigits = regexp.MustCompile(`^\p{Nd}+$`)

// idOnlyUUID matches the canonical 8-4-4-4-12 hex-with-hyphens UUID shape
// (case-insensitive: FalkorDB-adjacent tooling and hand-typed values both
// occur). Structurally distinct enough from any real word that no digit
// presence check is needed the way isHexShapedToken requires one.
var idOnlyUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// idOnlyGeneratedPrefixDigits is the CLOSED vocabulary of known
// CI/build-tooling generated-id prefixes, one separator, then a digit run
// and nothing else (finding-3 ruling, recall-first doctrine): `run-123`,
// `build-4567`, `pipeline-89`, `job-12`, `ci-345`. This is deliberately NOT
// a general "any letters + separator + digits" shape -- that earlier form
// classified semantic branch/pipeline names ("release-2025", "sprint-42")
// as identifier-only, suppressing paraphrase retrieval over them. The
// vocabulary is CODE-OWNED and enumerated, not inferred: extending it to
// another prefix is a deliberate edit here, not something a shape-only
// regex should guess at.
var idOnlyGeneratedPrefixDigits = regexp.MustCompile(`(?i)^(?:run|build|pipeline|job|ci)[-_]\p{Nd}+$`)

// isHexShapedToken reports whether token is a bare hex-digest shape (a git
// short/full SHA, a hex build hash) -- ASCII hex characters only, at least
// TWO digits among them, and long enough to not collide with a short real
// word.
//
// CHAOS-3835 round-3 finding 1: a single-digit requirement let all-hex-
// letter English words that happen to carry exactly one digit
// ("decade2", "facade1", "beaded1" -- every letter in [a-fA-F], one
// trailing digit) false-positive as an id shape, the same over-broad
// failure mode finding 3 exists to close, one digit short of what the
// original fix caught. RULING (recall-first tiebreak): a missed digest
// costs one row a noisy embed; a missed semantic name costs recall
// entirely. Ambiguity resolves toward EMBED, so the bar moves up, not the
// direction that skips more. Two digits is still generous against the
// live corpus's real hex ids -- a git short SHA (7-10 hex chars) drawn
// from a roughly-uniform hex alphabet has a >99.9% chance of carrying two
// or more digits; the residual all-letter-or-single-digit SHA that slips
// through and gets embedded anyway is the cheap failure this tradeoff
// accepts.
//
// The length floor (7, up from 6) follows the same tightening: it no
// longer needs to independently guard short coincidental tokens now that
// the two-digit bar does most of that work, but shortening it back below
// the shortest real short-SHA convention would just readmit the class of
// token the floor exists to exclude.
func isHexShapedToken(token string) bool {
	if len(token) < 7 {
		return false
	}
	digitCount := 0
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9':
			digitCount++
		case r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			// hex letter -- keep scanning.
		default:
			return false
		}
	}
	return digitCount >= 2
}

// isPureIdentifierToken reports whether one token matches the CLOSED
// vocabulary of non-semantic id shapes (finding-3 ruling): pure digits,
// UUID-shaped, a known generated-id prefix with a numeric tail, or a bare
// hex digest. Any token outside this vocabulary -- including the previous,
// over-broad "any letters + separator + digits" shape -- is treated as
// carrying semantic content.
func isPureIdentifierToken(token string) bool {
	return idOnlyPureDigits.MatchString(token) ||
		idOnlyUUID.MatchString(token) ||
		idOnlyGeneratedPrefixDigits.MatchString(token) ||
		isHexShapedToken(token)
}

// isPureIdentifierText reports whether text carries no letter-sequence
// beyond an identifier/number shape -- the general-purpose primitive T5's
// detector is built from (spec §5 L5: "text is (kind prefix +) id/number-
// only after normalization").
//
// Rule, exactly: trim the text; split on Unicode whitespace into tokens;
// text is pure-identifier iff EVERY token matches isPureIdentifierToken's
// closed vocabulary. A single token carrying any OTHER letter content -- a
// real word ("fix", "login", "smoke"), a slug that isn't id-shaped
// ("fullstack-acceptance", ends in letters not digits), or a semantic
// branch/release name ("release-2025", "sprint-42") -- means the text has
// semantic content, so the whole text is NOT pure-identifier.
//
// Empty text is explicitly NOT this function's concern and returns false:
// an empty field is a distinct, already-existing gating reason (collectEmbedTargets'
// add() drops empty text/canonicalID before any target is even considered), and
// this detector must never double-count that case under the id-only label
// -- the two reasons stay mutually exclusive by construction (spec §7 D2:
// the count must be distinguishable by reason).
func isPureIdentifierText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, token := range strings.Fields(text) {
		if !isPureIdentifierToken(token) {
			return false
		}
	}
	return true
}

// isPureIdentifierCIRun reports whether a ci_pipeline_run row's own name
// fields carry no content beyond a bare identifier (spec §5 L5's literal
// criterion, extended one conservative step).
//
// The LITERAL L5 criterion is "rows whose composed text has no name/branch"
// -- 22% of the live corpus, matching ciRunSearchText's documented
// degenerate shape `CI run repo: X`. This function reads that same
// criterion off the SOURCE fields (pipeline_name, branch, and -- see the
// round-2 correction below -- retrievalHandles) rather than re-parsing the
// composed text, because the composed text also carries repo, which is
// structural context every CI row carries regardless of whether it has a
// name -- never part of L5's criterion, and parsing it back out of a
// flattened string is exactly the re-derivation hazard search_text.go's
// header warns against (the ONE place kind-specific field meaning is
// unambiguous is the entity struct the switch already dispatches on).
//
// It extends "empty" to "empty OR itself id-shaped" so a POPULATED but
// bare-identifier pipeline_name or branch (an autogenerated "run-12345", a
// raw build number) also counts: the row still carries no human-findable
// name for a semantic query to reach it by. repo and the run id are
// deliberately never examined here -- they are not name fields.
//
// CHAOS-3835 round-2 finding 2: ciRunSearchText (search_text.go) also
// appends retrievalHandles(entity) -- entity.Aliases and
// entity.PreviousNames -- to the SAME composed text this function's
// verdict gates the embedding of. The round-1 doc comment above called
// that line "structural context ... never part of L5's criterion", which
// was wrong: aliases and previous names are exactly the kind of
// human-assigned label L5 cares about (a CI run tagged "nightly smoke"
// carries semantic content in its handles even when pipeline_name and
// branch are both bare ids). The id-only decision must evaluate the SAME
// text sources the composer embeds, so this function now checks handles
// too -- any handle content that ISN'T itself id-shaped means the row has
// semantic content and must embed.
//
// This is a DELIBERATE, DOCUMENTED coupling to ciRunSearchText's
// composition (mirroring the file-level comment's own note that this
// function reads source fields rather than the composed text): if that
// template ever gains another text source beyond pipeline_name, branch,
// and retrievalHandles, this function must gain the matching check, or a
// row's id-only verdict will silently stop matching what actually gets
// embedded -- exactly the gap this finding closes.
func isPureIdentifierCIRun(entity contextfabric.EntityProjection) bool {
	// CHAOS-3835 round-3 finding 2: name and branch are read through the
	// SAME ciRunPipelineNameField/ciRunBranchField ciRunSearchText itself
	// calls (search_text.go) -- classifying the composer's own capped
	// bytes, not a re-derived uncapped read of the same property, so the
	// two can never disagree about what the row actually embeds.
	name := ciRunPipelineNameField(entity)
	branch := ciRunBranchField(entity)
	handles := retrievalHandles(entity)
	nameIsIDOnly := name == "" || isPureIdentifierText(name)
	branchIsIDOnly := branch == "" || isPureIdentifierText(branch)
	handlesAreIDOnly := handles == "" || isPureIdentifierText(handles)
	return nameIsIDOnly && branchIsIDOnly && handlesAreIDOnly
}

// isPureIdentifierSubject dispatches the per-kind id-only decision the same
// way subjectSearchText's switch dispatches per-kind composition -- the ONE
// place this decision is made, extensible to another kind exactly the way
// a new §2 template is added. Only ci_pipeline_run has a documented id-only
// population today (spec §1); every other kind returns false unconditionally
// rather than guessing at a criterion the spec never measured.
func isPureIdentifierSubject(entity contextfabric.EntityProjection) bool {
	switch entity.Subject.Kind {
	case contractsv1.ContextFabricSubjectCIRun:
		return isPureIdentifierCIRun(entity)
	default:
		return false
	}
}
