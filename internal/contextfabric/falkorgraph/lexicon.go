package falkorgraph

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
//
// targetKind (codex round-4 P1, fix A layer 1) scopes a group's additions
// to searches for EXACTLY that subject kind, when the group's alias is
// itself effectively the kind's own name/shorthand -- because such an
// alias is liable to collide with unrelated STRUCTURAL field-label text
// (search_text.go's fieldLabel* constants) that OTHER kinds' templates
// also carry as boilerplate, independent of subject-matter. "repo" is the
// concrete case: it equals fieldLabelRepo's bare word, which
// pull_request/deployment/ci_pipeline_run/pull_request_review ALL compose
// into their own search_text regardless of whether the subject has
// anything to do with a repository -- an unscoped "repo" would therefore
// surface wrong-kind lexical hits from every one of those kinds. Scoping
// to contextfabric.SubjectRepository restricts the expansion query to
// `subject_kind = 'repository'` (queries.go), which the repository kind's
// own template never labels "repo:" (repositorySearchText places the slug
// unlabeled), so the collision cannot reach it. Empty targetKind means
// kind-agnostic (applies everywhere, unchanged from before this round) --
// validateNoLexiconLabelCollisions (below) enforces that an unscoped group
// can never collide with a field-label word, so this is a checked
// invariant, not a convention.
var domainLexiconGroups = []domainLexiconGroup{
	// "PR #<number> <title>" is the pull_request template's own literal,
	// fixed lead-in (spec §2) -- "PR" is in every indexed pull_request's
	// search_text verbatim; "pull request" is contractsv1.ContextFabricSubjectPullRequest
	// ("pull_request") space-rendered. Neither word collides with a
	// field-label (validateNoLexiconLabelCollisions confirms this at
	// init), so this group stays kind-agnostic.
	{phrases: []string{"pr", "pull request"}},
	// work_item_id carries a PROVIDER PREFIX the codebase explicitly
	// derives an alias from (composition.go's ticket-key rule): "linear:"
	// (Linear's own product vocabulary is "issue", 100% of live ids today)
	// and "jira:" (Jira's own product vocabulary is "ticket", a supported-
	// but-not-yet-observed prefix per the same rule). "work item" is
	// contractsv1.ContextFabricSubjectWorkItem ("work_item") space-rendered.
	// None of the three collides with a field-label word.
	{phrases: []string{"ticket", "issue", "work item"}},
	// contractsv1.ContextFabricSubjectRepository is literally "repository";
	// every kind's own template names its repository field "repo:"
	// (search_text.go's fieldLabelRepo, spec §2) -- "repo" bare IS that
	// label's word, hence the kind scope (see the var doc comment above).
	{phrases: []string{"repo", "repository"}, targetKind: contextfabric.SubjectRepository},
	// "CI run <pipeline_name> ..." is the ci_pipeline_run template's own
	// literal, fixed lead-in (spec §2) -- "CI run" is in every indexed
	// ci_pipeline_run's search_text verbatim; "pipeline" is half the kind
	// name (contractsv1.ContextFabricSubjectCIRun == "ci_pipeline_run") and
	// literally the ClickHouse column name (pipeline_name, spec §1). No
	// member collides with a field-label word.
	{phrases: []string{"ci run", "pipeline", "pipeline run"}},
	// "<environment> deployment <release_ref>" is the deployment template's
	// own literal, fixed text (spec §2) -- "deployment" is in every indexed
	// deployment's search_text verbatim; "release" is one of the SAME
	// kind's own stored `environment` field's four live values
	// (spec §1: "publishing/github-pages/release/ci"), and release_ref is
	// composed UNLABELED (deploymentSearchText), so "release" does not
	// collide with a field-label word either.
	{phrases: []string{"deploy", "deployment", "release"}},
	// "<state> review of PR #<number>: <title>" is the pull_request_review
	// template's own literal, fixed text (spec §2) -- "review" is in every
	// indexed pull_request_review's search_text verbatim. No field-label
	// collision.
	{phrases: []string{"review", "code review"}},
	// contractsv1.ContextFabricSubjectOrganization is literally
	// "organization"; "org" is the codebase's own universal abbreviation
	// for it (storage.Principal.OrgID, every propOrgID-keyed query). No
	// field-label collision.
	{phrases: []string{"org", "organization"}},
}

// domainLexiconGroup is one entry of domainLexiconGroups -- see that var's
// doc comment for targetKind's meaning.
type domainLexiconGroup struct {
	phrases    []string
	targetKind contextfabric.SubjectKind
}

// lexiconGroup pairs a domainLexiconGroup with its lowercased phrases,
// precomputed once so expandWithLexicon never re-lowercases a fixed
// literal on the hot read path.
type lexiconGroup struct {
	phrases      []string
	lowerPhrases []string
	targetKind   contextfabric.SubjectKind
}

// compiledLexicon is built once at package init, not per call -- expansion
// runs on the hot read path (once per resolved term, plus once per
// resolution for the question).
var compiledLexicon = compileLexicon(domainLexiconGroups)

func compileLexicon(groups []domainLexiconGroup) []lexiconGroup {
	compiled := make([]lexiconGroup, 0, len(groups))
	for _, group := range groups {
		lg := lexiconGroup{phrases: group.phrases, lowerPhrases: make([]string, len(group.phrases)), targetKind: group.targetKind}
		for i, phrase := range group.phrases {
			// codex round-3 P1 (fix B): a multi-word phrase becomes a
			// double-quoted RediSearch exact-phrase clause
			// (queries.go's lexiconPhraseClause), injection-safe only
			// because every phrase here is a fixed, code-owned literal that
			// can NEVER contain a literal `"` -- enforced here, once, at
			// init, rather than trusted at every call site. A phrase that
			// somehow did contain one would let it escape the clause it is
			// built into; failing loudly at package init (a code-review-time
			// mistake, not a runtime/user-input one) is strictly better than
			// a silent, per-call injection-safety assumption nothing checks.
			if strings.Contains(phrase, `"`) {
				panic(fmt.Sprintf("falkorgraph: domain lexicon phrase %q contains a literal double quote, which would break its RediSearch exact-phrase clause -- remove or rewrite it", phrase))
			}
			lg.lowerPhrases[i] = strings.ToLower(phrase)
		}
		compiled = append(compiled, lg)
	}
	validateNoLexiconLabelCollisions(compiled)
	return compiled
}

// validateNoLexiconLabelCollisions panics if any KIND-AGNOSTIC
// (targetKind == "") lexicon phrase equals, whole-word case-insensitively,
// one of search_text.go's own field-label words (codex round-4 P1, fix A
// layer 2 -- the CLASS guard, not just the repo/repository instance layer
// 1 already scopes). An unscoped phrase equal to a field-label word would
// make lexicon expansion match ANY kind's search_text purely because that
// kind's template happens to compose a structured field with that label --
// not because the subject has anything to do with the synonym's concept --
// producing wrong-kind lexical candidates that can corroborate a
// wrong-kind vector hit into a wrong commit. This protects every FUTURE
// lexicon edit, not only the one collision already found and fixed by
// scoping: a phrase that starts colliding must be kind-scoped (targetKind)
// or renamed, and this check makes that a build-time failure instead of a
// measurement-time rediscovery.
//
// A KIND-SCOPED phrase is exempt: its expansion query is restricted to
// `subject_kind = <targetKind>` (queries.go), and none of the kinds this
// package templates labels its OWN kind's structural fields with its OWN
// kind's alias (verified case-by-case in domainLexiconGroups' own doc
// comments), so the collision cannot reach it.
//
// searchTextFieldLabelWords (search_text.go) is the single authority for
// what a label word is -- never a second, hand-maintained list here.
func validateNoLexiconLabelCollisions(groups []lexiconGroup) {
	labels := make(map[string]struct{}, len(searchTextFieldLabelWords))
	for _, word := range searchTextFieldLabelWords {
		labels[strings.ToLower(word)] = struct{}{}
	}
	for _, group := range groups {
		if group.targetKind != "" {
			continue
		}
		for i, lowerPhrase := range group.lowerPhrases {
			if _, collides := labels[lowerPhrase]; collides {
				panic(fmt.Sprintf("falkorgraph: domain lexicon phrase %q collides with a search-text field-label word and has no targetKind scope -- either scope this group to the ONE subject kind the phrase is genuinely about, or rename the phrase", group.phrases[i]))
			}
		}
	}
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

// lexiconAddition is one matched synonym phrase plus the subject kind (if
// any) a search built from it must be scoped to -- see domainLexiconGroups'
// targetKind doc comment.
type lexiconAddition struct {
	phrase     string
	targetKind contextfabric.SubjectKind
}

// lexiconAdditions returns the domain-lexicon synonym phrases matched
// WITHIN text, in fixed (domainLexiconGroups declaration) order -- the ONE
// primitive both expandWithLexicon (text-concatenation, for the vector arm)
// and queries.go's lexiconExpansionQuery (phrase-aware, kind-scoped
// OR-query construction, for the lexical arm, codex round-3 P1/P2 and
// round-4 P1) build from, so the two arms can never independently decide
// "what matched" differently. Empty input, or no match, returns nil.
func lexiconAdditions(text string) []lexiconAddition {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lowerText := strings.ToLower(text)
	seen := make(map[string]struct{})
	var additions []lexiconAddition
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
			additions = append(additions, lexiconAddition{phrase: phrase, targetKind: group.targetKind})
		}
	}
	return additions
}

// expandWithLexicon returns text widened with any domain-lexicon synonym
// phrases matched WITHIN it (lexiconAdditions), for building a wider
// RETRIEVAL QUERY only -- specifically, the VECTOR arm's embedded text
// (vector.go's vectorQueryText). The lexical arm does NOT use this
// function; queries.go's lexiconExpansionQuery builds its own phrase-aware,
// kind-scoped query from the same lexiconAdditions instead.
//
// Deliberately kind-AGNOSTIC even for a kind-scoped addition: vector
// similarity search has no per-kind index/filter concept in this codebase
// today (spec §5 L5(b), a deferred, unmeasured lever), so there is no
// equivalent scoping mechanism to apply here, and the field-label
// collision layer 1/2 close is a LEXICAL (exact-term-match) failure mode
// that has no direct analogue for continuous embedding similarity -- a
// vector for text containing "repo: <slug>" does not read as highly
// similar to a query embedding of "repository" merely because both share
// the substring "repo" the way a RediSearch OR-term match would. This
// function is also reached only when applyLexiconToVectorArm is on, which
// is NOT the shipped default (see that const's doc comment).
//
// Byte-identical to text when no lexicon phrase is found -- the common case
// for most terms/questions -- so a caller that does not match anything pays
// no behavior change at all: the embedded text is unchanged, and the
// embedcache (T11) key is unchanged (still a cache hit for a repeated
// unmatched term).
//
// CALLERS MUST NEVER key confidence/relevance scoring off this function's
// output. Any embedding similarity threshold was measured/calibrated
// against the ORIGINAL term text; this function exists only to widen what
// gets FOUND, never to change how confidently a find is scored.
func expandWithLexicon(text string) string {
	additions := lexiconAdditions(text)
	if len(additions) == 0 {
		return text
	}
	phrases := make([]string, len(additions))
	for i, addition := range additions {
		phrases[i] = addition.phrase
	}
	return text + " " + strings.Join(phrases, " ")
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
