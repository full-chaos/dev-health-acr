package falkorgraph

import (
	"strings"
)

// This file is the CHAOS-3838 (embed-text spec v2 §5 L13, §6 T8) query-side
// domain lexicon: a CLOSED, code-owned vocabulary of short-hand/canonical
// phrase pairs a caller's question commonly uses interchangeably with this
// corpus's own vocabulary ("PR" for "pull request", "ticket" for "issue" or
// "work item", ...). Static only -- no LLM expansion in this ticket (spec:
// "static domain lexicon first").
//
// Single authority (CHAOS-3838 design constraint): this is the ONE table
// both retrieval arms read. queries.go's fulltextSearchNodes widens its
// RediSearch OR-query with it; vector.go's hybridSearchNodes/SearchQuestion
// widen the text handed to the embedder with it. Neither arm keeps its own
// copy or its own matching logic -- extending coverage means editing
// domainLexiconGroups here, once, and both arms pick it up.

// domainLexiconGroups is the closed vocabulary itself: each inner slice is a
// set of phrases this corpus treats as interchangeable ways to name the SAME
// concept. Extending a group, or adding a new one, is a deliberate, reviewed
// edit here, exactly like composition.go's idOnlyGeneratedPrefixDigits' own
// "closed vocabulary, extend explicitly" rule.
//
// GROUNDING RULE (codex/team-lead ratification rider 2): every entry is
// justified against something the BACKEND ITSELF stores or names --
// contractsv1's SubjectKind constants, a §2 embed-text TEMPLATE's own
// literal fixed text (spec doc, re-verified against composition.go /
// search_text.go for this ticket), or a stored FIELD VALUE the spec's live
// ClickHouse population probe (§1) documented -- never invented from
// general domain familiarity, and never from the withheld corpus. A phrase
// this codebase's own vocabulary does not support was trimmed rather than
// kept on "it's common industry usage" grounds alone (e.g. "merge request",
// "build", "workflow run" were considered and dropped -- no stored template
// or field value evidences them here; GitLab connectivity exists at the
// PROJECT level (spec §1 project_key population) but nothing confirms
// GitLab-sourced PRs populate the SAME git_pull_requests-derived
// pull_request kind under that vocabulary).
//
// Order is fixed (declaration order of both the outer slice and each inner
// slice) and expandWithLexicon walks it deterministically, so two calls
// against the same text always produce the same expansion string -- required
// for the query text to be a stable cache key (embedcache) and for the
// RediSearch query string to be reproducible across identical requests.
var domainLexiconGroups = [][]string{
	// "PR #<number> <title>" is the pull_request template's own literal,
	// fixed lead-in (spec §2) -- "PR" is in every indexed pull_request's
	// search_text verbatim; "pull request" is contractsv1.ContextFabricSubjectPullRequest
	// ("pull_request") space-rendered.
	{"pr", "pull request"},
	// work_item_id carries a PROVIDER PREFIX the codebase explicitly
	// derives an alias from (composition.go's ticket-key rule): "linear:"
	// (Linear's own product vocabulary is "issue", 100% of live ids today)
	// and "jira:" (Jira's own product vocabulary is "ticket", a supported-
	// but-not-yet-observed prefix per the same rule). "work item" is
	// contractsv1.ContextFabricSubjectWorkItem ("work_item") space-rendered.
	{"ticket", "issue", "work item"},
	// contractsv1.ContextFabricSubjectRepository is literally "repository";
	// every kind's own template names its repository field "repo:"
	// (search_text.go's repo_slug field label, spec §2).
	{"repo", "repository"},
	// "CI run <pipeline_name> ..." is the ci_pipeline_run template's own
	// literal, fixed lead-in (spec §2) -- "CI run" is in every indexed
	// ci_pipeline_run's search_text verbatim; "pipeline" is half the kind
	// name (contractsv1.ContextFabricSubjectCIRun == "ci_pipeline_run") and
	// literally the ClickHouse column name (pipeline_name, spec §1).
	{"ci run", "pipeline", "pipeline run"},
	// "<environment> deployment <release_ref>" is the deployment template's
	// own literal, fixed text (spec §2) -- "deployment" is in every indexed
	// deployment's search_text verbatim; "release" is one of the SAME
	// kind's own stored `environment` field's four live values
	// (spec §1: "publishing/github-pages/release/ci").
	{"deploy", "deployment", "release"},
	// "<state> review of PR #<number>: <title>" is the pull_request_review
	// template's own literal, fixed text (spec §2) -- "review" is in every
	// indexed pull_request_review's search_text verbatim.
	{"review", "code review"},
	// contractsv1.ContextFabricSubjectOrganization is literally
	// "organization"; "org" is the codebase's own universal abbreviation
	// for it (storage.Principal.OrgID, every propOrgID-keyed query).
	{"org", "organization"},
}

// lexiconGroup pairs a domainLexiconGroups entry with its lowercased
// phrases, precomputed once so expandWithLexicon never re-lowercases a
// fixed literal on the hot read path.
type lexiconGroup struct {
	phrases      []string
	lowerPhrases []string
}

// compiledLexicon is built once at package init, not per call -- expansion
// runs on the hot read path (once per resolved term, plus once per
// resolution for the question).
var compiledLexicon = compileLexicon(domainLexiconGroups)

func compileLexicon(groups [][]string) []lexiconGroup {
	compiled := make([]lexiconGroup, 0, len(groups))
	for _, group := range groups {
		lg := lexiconGroup{phrases: group, lowerPhrases: make([]string, len(group))}
		for i, phrase := range group {
			lg.lowerPhrases[i] = strings.ToLower(phrase)
		}
		compiled = append(compiled, lg)
	}
	return compiled
}

// hasWholeWordPhrase reports whether lowerPhrase occurs in lowerText as a
// genuine whole-word/whole-phrase run: the rune immediately before the
// match (if any) and the rune immediately after it (if any) are NOT
// unicode letters or digits, per isFulltextWordRune -- the SAME predicate
// queries.go's fulltext tokenizer and matched-term-coverage math use, so a
// boundary decision here can never disagree with what that path already
// treats as "inside a word". Both arguments must already be lowercased by
// the caller (this function does no case folding of its own).
//
// This replaces a naive `\b<phrase>\b` regexp (codex round-2 P2): Go's
// regexp \b is an ASCII-only word-boundary test (RE2's \w is exactly
// [0-9A-Za-z_]), so it reads the transition from an ASCII letter to ANY
// non-ASCII letter as a boundary -- backwards for a genuine Unicode word.
// `\bpr\b` matched "pr" at the start of "prévision" (a boundary between
// ASCII 'r' and non-ASCII 'é' that isn't a real word boundary at all),
// silently expanding a French/Spanish/etc. word into unrelated English
// synonyms. isFulltextWordRune's unicode.IsLetter/IsDigit test treats 'é'
// as a word rune like any other letter, so no boundary exists there and
// this function correctly does not match.
func hasWholeWordPhrase(lowerText, lowerPhrase string) bool {
	if lowerPhrase == "" {
		return false
	}
	text := []rune(lowerText)
	phrase := []rune(lowerPhrase)
	n, m := len(text), len(phrase)
	for start := 0; start+m <= n; start++ {
		if start > 0 && isFulltextWordRune(text[start-1]) {
			continue
		}
		end := start + m
		if end < n && isFulltextWordRune(text[end]) {
			continue
		}
		match := true
		for i := 0; i < m; i++ {
			if text[start+i] != phrase[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// expandWithLexicon returns text widened with any domain-lexicon synonym
// phrases matched WITHIN it, for building a wider RETRIEVAL QUERY only.
//
// Byte-identical to text when no lexicon phrase is found -- the common case
// for most terms/questions -- so a caller that does not match anything pays
// no behavior change at all: the RediSearch query is unchanged, the embedded
// text is unchanged, and the embedcache (T11) key is unchanged (still a
// cache hit for a repeated unmatched term).
//
// CALLERS MUST NEVER key confidence/relevance scoring off this function's
// output. fulltextSearchNodes' matched-term coverage and any embedding
// similarity threshold were measured/calibrated against the ORIGINAL term
// text; this function exists only to widen what gets FOUND, never to change
// how confidently a find is scored. See queries.go's fulltextSearchNodes and
// vector.go's hybridSearchNodes for how each arm keeps that boundary.
func expandWithLexicon(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	lowerText := strings.ToLower(text)
	seen := make(map[string]struct{})
	var additions []string
	for _, group := range compiledLexicon {
		matched := false
		for _, lowerPhrase := range group.lowerPhrases {
			if hasWholeWordPhrase(lowerText, lowerPhrase) {
				matched = true
				seen[lowerPhrase] = struct{}{}
			}
		}
		if !matched {
			continue
		}
		for i, phrase := range group.phrases {
			key := group.lowerPhrases[i]
			if _, already := seen[key]; already {
				continue
			}
			seen[key] = struct{}{}
			additions = append(additions, phrase)
		}
	}
	if len(additions) == 0 {
		return text
	}
	return text + " " + strings.Join(additions, " ")
}

// applyLexiconToVectorArm controls whether the domain lexicon also widens
// the VECTOR arm's embedded query text (the "dual-arm" half of L13), versus
// staying lexical-only. Kept as a single, deliberately easy-to-flip seam --
// not deleted now that it is measured -- because CHAOS-3829 (parked) may
// change the corroboration commit geometry, at which point this is exactly
// the knob a future re-measurement would flip first.
//
// MEASURED DECISION (CHAOS-3838 freeze, live ambiguity benchmark, same
// 50-case corpus, tau=0.30/efR=200 policy): lexical-only (false) produced
// MORE corroborated-but-blocked candidates than dual-arm (true) -- 25/50 vs
// 23/50 -- with IDENTICAL AC-3778-2/3/4 outcomes either way (lift +0.0pp
// both configurations; wrong-commits 1->0 both; 20/20 no-match controls
// clean both). Dual-arm's own risk (CHAOS-3834's tau=0.30/efRuntime=200 was
// calibrated against BARE-term/question embeddings, with no fresh S+/S-
// distribution for lexicon-widened embed text) bought no offsetting
// upside on this corpus, so lexical-only ships: equal or better measured
// breadth at lower calibration risk. See the freeze report for the full
// numbers and for why NEITHER setting clears the lift bar (a distinct,
// structural finding: every corroborated candidate on this corpus competed
// against another candidate in the same resolution, and a corroborated
// confidence's 0.86 ceiling can never clear the 0.88 top-of-two commit
// gate against ANY competitor -- CHAOS-3829 territory, not this ticket's
// to fix).
const applyLexiconToVectorArm = false

// vectorQueryText returns the text a query-side vector search should embed:
// lexicon-expanded when applyLexiconToVectorArm is on, the bare input
// otherwise. Both vector.go call sites (hybridSearchNodes' per-term embed,
// questionVectorSearchNodes' question embed) go through this ONE function,
// so they can never independently drift on which mode is live.
func vectorQueryText(text string) string {
	if !applyLexiconToVectorArm {
		return text
	}
	return expandWithLexicon(text)
}
