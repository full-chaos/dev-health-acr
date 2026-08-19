package graphrank

import (
	"regexp"
	"strings"
)

// This file is CHAOS-3899's WIDENING measurement (chris-ratified
// pre-registered shadow measurement, 2026-08-19): the ORIGINAL
// handleGrammarRegistry (chaos3899_handle_grammar.go) closes over exactly
// 3 patterns -- PR number, CHAOS-#### ticket key, CI run id -- each bound
// to one of the 4 closed census-kind stall tables via a literal
// equality-match VALUE. Source-native identifiers -- a repository slug, a
// branch name, a commit SHA, a provider-qualified repo name -- do not fit
// that shape: none of them is a stall-kind base table's own natural-key
// column, and "repository" is not itself a census kind (it is an ANCHOR
// kind, chaos3899_anchor.go's own doc comment). Rather than inventing new
// census predicates for a kind the closed registry does not cover, this
// widening reuses BindAnchor's OWN discipline verbatim (R4: "≥2 claimants
// or an incomplete read -> no discriminator") against the SAME rows the
// anchor role's own claimantsByTerm map already carries -- literally the
// CHAOS-3884 identity-universe completeness+uniqueness read, just applied
// to a grammar-extracted literal instead of a search-derived term (see
// this file's own "REACHABILITY NOTE" for exactly how).
//
// ALIAS-SHAPE NOTE (codex xhigh review finding round 1, confirmed and
// fixed, 2026-08-19): a repository's identity-universe alias strings
// (devhealthsource's repositoryBareNameAlias/repositoryProviderAlias --
// graphrank cannot import that package, see CensusOutcome's own doc
// comment, so this file duplicates the two transforms rather than sharing
// them) are the BARE name alone ("dev-health-acr") and, when a provider is
// known, "<provider>:<org>/<repo>" ("github:full-chaos/dev-health-acr") --
// there is no "<org>/<repo>" (slash, no provider) alias at all. Each entry
// below carries its own lookupKey transform, applied AFTER extraction/
// filtering, to produce the alias-shaped string actually worth searching
// for -- Term itself (the traced-never provenance field) stays the raw,
// as-typed extracted literal; only the SEARCH VALUE changes.
//
// REACHABILITY NOTE (codex xhigh review finding round 2, confirmed and
// fixed, 2026-08-19): a round-1 fix looked the transformed alias up as a
// MAP KEY against claimantsByTerm -- WRONG, because that map's keys are
// the ORIGINAL, UN-normalized SubjectTerms the question interpreter itself
// extracted (resolve.go's own aliasClaimantsByTerm), never an alias
// string. MatchIdentityRows (chaos3884_identity_universe.go) keys its
// result by "the ORIGINAL (as-passed, un-normalized) term", so
// claimantsByTerm["dev-health-acr"] exists ONLY IF the interpreter
// independently extracted the LITERAL STRING "dev-health-acr" as one of
// its own SubjectTerms -- something this grammar's own regex match on the
// raw question text has no bearing on and cannot assume. A map-key lookup
// would therefore have UNDER-COUNTED (silently, never observably) even
// when a real, resolvable claimant existed.
//
// The fix: every IdentityMatch claimantsByTerm's VALUES carry is a FULL
// IdentityRow, including that row's OWN Aliases/ProviderAliases/Label
// fields -- the SAME data MatchIdentityRows itself already matched
// against, just not keyed the way this file needs. resolveSourceNative
// (below) SCANS every row already present anywhere in claimantsByTerm's
// values (deduplicated by canonical id, since one physical row can appear
// under several different interpreter-extracted term keys) and checks
// EACH row's own Label/Aliases/ProviderAliases against this entry's
// lookupKey-transformed search value, using NormalizeAliasTerm --
// mirroring matchedTermsForRow's own priority scan verbatim. This finds a
// row REGARDLESS of which term the interpreter happened to extract for
// it, as long as the row is discoverable in claimantsByTerm's value set
// at all (i.e. SOME interpreter term matched it via SOME alias) -- a
// strict superset of what a map-key lookup could ever find, still zero
// new reads (every row scanned here was already fetched and matched by
// the SAME AliasLookup call the anchor role already consumes).
//
// SHADOW-ONLY / MEASUREMENT-ONLY, structurally: BindSourceNativeHandles
// performs NO live query of its own (claimantsByTerm is a map already
// fetched by resolve.go for the anchor role, before this function is ever
// called -- widening this registry adds zero new reads, zero new
// round-trip latency, zero new production dependency). Its only caller
// (RunShadowEvidenceRound, chaos3899_evidence_round.go) feeds the result
// EXCLUSIVELY into two new trace stages (evidence_source_native/
// evidence_source_native_probe, resolve.go's ResolutionTraceEvent) --
// never into base/Attestation.Outcome, Attestation.Reason,
// Attestation.Kinds, censusKinds, handle, or anchorOK, the exact set of
// variables the round's decisive switch statement branches on. The
// decisive switch statement itself is untouched by this file -- see
// RunShadowEvidenceRound's own doc comment for where the two new stages
// are fired and why that placement cannot reach the decisive path.
type SourceNativeBind struct {
	// Grammar is the registry entry's own fixed name -- safe to trace,
	// never derived from question text (identical discipline to
	// BoundHandle.Grammar).
	Grammar string
	// Term is the exact extracted literal, AS IT APPEARED IN THE QUESTION
	// -- in-process provenance ONLY, exactly like BoundHandle.Value/
	// AnchorBinding.Term; never traced (corpus-safety rule). This is NOT
	// necessarily what was looked up against claimantsByTerm -- see
	// lookupKey's own doc comment.
	Term string
	// Resolved is true iff Term's own SEARCH VALUE (Term itself, unless the
	// matching registry entry's lookupKey transforms it -- e.g. a
	// provider_qualified_name match's search value swaps its URL-style
	// "github.com/" prefix for the identity universe's own "github:"
	// alias prefix) names EXACTLY ONE claimant among every row already
	// present in claimantsByTerm's values, under a COMPLETE read --
	// BindAnchor's own R4 discipline, unchanged (see resolveSourceNative
	// and this file's own "REACHABILITY NOTE" for why this is a row-content
	// scan, not a map-key lookup). This IS the "keyed bind" the CHAOS-3899
	// pre-registration measures: a false Resolved with a non-empty Term is
	// a syntactic grammar match that failed to resolve (claimant lookup
	// failure) -- a false positive in the pre-registered sense, counted
	// separately from a true keyed bind.
	Resolved bool
	// Kind is the resolved claimant's own kind -- meaningful only when
	// Resolved is true (zero value otherwise).
	Kind CensusKind
}

type sourceNativeGrammarEntry struct {
	name       string
	pattern    *regexp.Regexp
	valueGroup int
	// filter, when non-nil, is an additional Go-side acceptance check the
	// regex alone cannot express (RE2 has no lookahead) -- e.g.
	// commit_sha's "must contain a hex letter AND a digit, never a
	// plausible English word" rule. A nil filter accepts every regex
	// match.
	filter func(string) bool
	// lookupKey, when non-nil, transforms the extracted (and filter-passed)
	// literal into the identity universe's own alias shape before
	// resolveSourceNative searches for it -- see this file's own
	// "ALIAS-SHAPE NOTE" and "REACHABILITY NOTE" doc comments. nil means
	// the raw extracted literal IS already the search value (branch/commit
	// grammars -- the identity universe carries no alias for either shape
	// today, so there is no transform to apply; the search simply misses,
	// exactly like any other unresolved match). Returning "" means "do not
	// attempt a search at all" (a defensive guard, never expected in
	// practice given each transform's own preconditions).
	lookupKey func(string) string
	// avoidOverlapWith names OTHER registry entries (by their own `name`,
	// listed EARLIER in sourceNativeGrammarRegistry) whose matched spans
	// this entry's own candidate matches must not intersect -- see
	// BindSourceNativeHandles' own doc comment for why this exists (the
	// provider_qualified_name / repo_slug false-split bug, codex xhigh
	// review finding).
	avoidOverlapWith []string
}

// containsHexLetter reports whether s contains at least one of a-f
// (case-insensitive).
func containsHexLetter(s string) bool {
	return strings.ContainsAny(s, "abcdefABCDEF")
}

// containsDigit reports whether s contains at least one ASCII digit.
func containsDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}

// looksLikePlausibleSHA is commit_sha's own precision guard (codex xhigh
// review finding, confirmed and fixed: the original filter,
// containsHexLetter alone, still accepted plain English hex-look words --
// "deadbeef", "cafebabe", "feedface" -- every one of them 7+ letters drawn
// only from a-f). Requiring BOTH a hex letter (excludes an all-decimal
// collision -- see this function's own predecessor doc comment, still
// true: a PR number, CI run id, date, or byte count typed bare) AND a
// digit (excludes the all-letter "hex-speak" English-word collision) is
// satisfied by essentially every REAL git SHA (a near-random hex string of
// length >=7 is overwhelmingly likely to contain both a digit and a
// letter) while excluding both prose-collision classes this grammar has
// to guard against.
func looksLikePlausibleSHA(s string) bool {
	return containsHexLetter(s) && containsDigit(s)
}

// isoDatePattern matches a bare ISO-shaped date token (2026-08-19) --
// branch_name_keyword's own additional precision guard: "branch
// 2026-08-19" ("what shipped on branch 2026-08-19" reads as ordinary,
// plausible prose) is excluded even though it satisfies
// looksLikeBranchToken's own digit/hyphen requirement.
var isoDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// looksLikeBranchToken reports whether s contains at least one character
// (digit, hyphen, underscore, or slash) that an ordinary bare English word
// following "branch" ("branch out", "branch into", "branch off", "branch
// the team") essentially never contains -- branch_name_keyword's own
// precision guard: without this filter, "branch <any single common verb
// or noun>" would bind on every sentence that uses "branch" as an
// ordinary verb, exactly the prose-swallowing this registry's own doc
// comment promises not to do. Also excludes a bare ISO date token (see
// isoDatePattern) -- a second, common prose collision this grammar's
// keyword anchor alone does not rule out.
func looksLikeBranchToken(s string) bool {
	return strings.ContainsAny(s, "-_/0123456789") && !isoDatePattern.MatchString(s)
}

// repoBareName mirrors devhealthsource.repositoryBareNameAlias verbatim
// (graphrank cannot import that package -- see this file's own "ALIAS-SHAPE
// NOTE"): a repository's own (non-provider) identity-universe alias is
// its BARE name, the text after the LAST "/" -- never the full
// "org/repo" slug a repo_slug match actually extracts. Returns s
// unchanged (no "/" present) if s does not look like a slug at all --
// defensive only, since every repo_slug match structurally contains
// exactly one "/" by construction.
func repoBareName(s string) string {
	idx := strings.LastIndex(s, "/")
	if idx < 0 {
		return s
	}
	return s[idx+1:]
}

// providerQualifiedNamePattern matches an explicit provider-domain prefix
// (github.com/ or gitlab.com/) followed by an org/repo slug, ANCHORED so
// the domain token cannot appear as a suffix or subdomain of a DIFFERENT,
// unrelated host (CodeQL go/regex/missing-regexp-anchor, confirmed and
// fixed, 2026-08-19): the previous pattern anchored only with \b, which
// Go RE2 treats as "a word/non-word transition" -- "-" is a non-word
// character, so "evil-github.com/org/repo" satisfied \b immediately
// before "github" (transitioning from "-" to "g") and matched the
// EMBEDDED "github.com/org/repo" as if it were the real github.com, even
// though the actual host is "evil-github.com". Group 1
// (?:^|[^A-Za-z0-9._-]) requires the character immediately before the
// domain token to be EITHER the start of the string OR something that
// cannot itself be part of a hostname/subdomain -- "evil-github.com" and
// "sub.github.com" both fail this (the character before "github" is "-"
// or "." respectively, both valid hostname characters), while "see
// github.com/org/repo" and a string-initial "github.com/org/repo" both
// succeed (a space, or start-of-string, is not a valid hostname
// character). Group 2 is the actual match value -- valueGroup below
// points at it, never at the whole match (which would include the guard
// character).
var providerQualifiedNamePattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9._-])((?:github\.com|gitlab\.com)/[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?/[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)

// providerAliasKey transforms a provider_qualified_name match's own
// URL-shaped extracted literal ("github.com/org/repo", the shape a
// question actually uses) into the identity universe's own
// "<provider>:<org>/<repo>" alias shape (devhealthsource's
// repositoryProviderAlias, mirrored here for the same import-boundary
// reason repoBareName is) -- "github.com" -> "github", "gitlab.com" ->
// "gitlab", joined with ":" instead of the URL's "/" before the org/repo
// slug. Returns "" (no lookup attempted) for a match this function cannot
// confidently parse -- defensive only, since sourceNativeGrammarRegistry's
// own provider_qualified_name pattern only ever matches one of the two
// known domains followed by exactly one "/".
func providerAliasKey(s string) string {
	domain, rest, ok := strings.Cut(s, "/")
	if !ok || rest == "" {
		return ""
	}
	var provider string
	switch strings.ToLower(domain) {
	case "github.com":
		provider = "github"
	case "gitlab.com":
		provider = "gitlab"
	default:
		return ""
	}
	return provider + ":" + rest
}

// sourceNativeGrammarRegistry is CHAOS-3899's widening measurement
// registry (chris-ratified, 2026-08-19; see this file's own doc comment
// for the shadow-only/measurement-only scope and the alias-shape lookup
// discipline). Every entry follows handleGrammarRegistry's own R3
// maximal-munch/word-boundary discipline (\b anchors, greedy quantifiers)
// plus its own additional precision guard against ordinary prose:
//
//   - provider_qualified_name: an explicit provider-domain prefix
//     (github.com/ or gitlab.com/) followed by an org/repo slug. The
//     narrowest grammar in this registry by construction -- a bare
//     provider domain literal essentially never appears in ordinary
//     prose, so this pattern needs no additional filter. lookupKey
//     (providerAliasKey) normalizes to the identity universe's own
//     "provider:org/repo" alias shape before resolveSourceNative searches
//     for it.
//   - repo_slug: two path segments joined by "/", each REQUIRED to start
//     with a letter (never a digit) -- excludes a bare numeric fraction
//     ("3/4 of the tests passed", "24/7 on-call") or a ratio/date-like
//     token, the ordinary-prose shape most likely to collide with a bare
//     "word/word" pattern. avoidOverlapWith provider_qualified_name
//     (codex xhigh review finding, confirmed and fixed: without this, a
//     "github.com/org/repo" URL span was mis-split -- "github.com" itself
//     contains a "." accepted by this pattern's own character class, so
//     an earlier version matched the BOGUS "github.com/org" as its own
//     repo_slug, one path segment short of the real org/repo pair).
//     lookupKey (repoBareName) normalizes to the identity universe's own
//     bare-name alias (the text after the LAST "/") before
//     resolveSourceNative searches for it -- there is no "org/repo"-shaped
//     alias in the schema at all.
//   - branch_name_keyword: keyword-anchored ("branch <name>"), the
//     conservative case the CHAOS-3899 rider calls for -- an unanchored
//     bare slug-shaped token would be indistinguishable from prose and
//     would also collide with repo_slug's own matches. Filtered
//     (looksLikeBranchToken) to require a digit/hyphen/underscore/slash in
//     the captured token AND exclude a bare ISO date -- without the
//     first, "branch out"/"branch into"/"branch off" (ordinary English
//     verb phrases) would bind on every such sentence; without the
//     second, "branch 2026-08-19" (an ordinary date reference) would too.
//     No lookupKey: the identity universe carries no branch-name alias at
//     all today, so the raw literal is the (never-matching) lookup key.
//   - branch_name_prefix: a well-known branch-prefix shape (feature/,
//     fix/, bugfix/, hotfix/, release/, chore/, docs/) followed by slug
//     characters -- conservative because the prefix set is closed and
//     none of these words plus "/" is a plausible ordinary-prose
//     collision ("fix/the-thing" is not English). No lookupKey, same
//     reason as branch_name_keyword.
//   - commit_sha: 7-40 lowercase-or-uppercase hex characters, word
//     boundary, filtered (looksLikePlausibleSHA) to require BOTH a hex
//     LETTER (a-f, excludes an all-decimal collision -- a PR number typed
//     bare, a CI run id, a date, a byte count) AND a digit (excludes a
//     plausible English "hex-speak" word -- "deadbeef", "cafebabe",
//     "feedface"). No lookupKey: the identity universe carries no
//     commit-SHA alias at all today.
var sourceNativeGrammarRegistry = []sourceNativeGrammarEntry{
	{
		name:       "provider_qualified_name",
		pattern:    providerQualifiedNamePattern,
		valueGroup: 1,
		lookupKey:  providerAliasKey,
	},
	{
		name:             "repo_slug",
		pattern:          regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9._-]{0,38}/[A-Za-z][A-Za-z0-9._-]{0,38}\b`),
		valueGroup:       0,
		lookupKey:        repoBareName,
		avoidOverlapWith: []string{"provider_qualified_name"},
	},
	{
		name:       "branch_name_keyword",
		pattern:    regexp.MustCompile(`(?i)\bbranch\s+['"` + "`" + `]?([A-Za-z0-9][A-Za-z0-9._/-]{1,78}[A-Za-z0-9])['"` + "`" + `]?`),
		valueGroup: 1,
		filter:     looksLikeBranchToken,
	},
	{
		name:       "branch_name_prefix",
		pattern:    regexp.MustCompile(`\b((?:feature|fix|bugfix|hotfix|release|chore|docs)/[A-Za-z0-9][A-Za-z0-9._-]{0,78})\b`),
		valueGroup: 1,
	},
	{
		name:       "commit_sha",
		pattern:    regexp.MustCompile(`\b[0-9a-fA-F]{7,40}\b`),
		valueGroup: 0,
		filter:     looksLikePlausibleSHA,
	},
}

// BindSourceNativeHandles extracts every source-native identifier grammar
// match from question and resolves each one against claimantsByTerm using
// BindAnchor's OWN completeness+uniqueness discipline (R4) -- see this
// file's own doc comment for why this is the correct binding mechanism
// for an identifier that is not a stall-kind base table's natural key,
// for the alias-shape lookupKey discipline, and for the structural
// shadow-only guarantee. complete mirrors BindAnchor's own parameter
// exactly: false refuses every resolution outright (the identity view
// itself is unproven), matching R4's "an incomplete read -> no
// discriminator" rule.
//
// Overlap between two DIFFERENT registry entries is not deduplicated in
// general (mirrors BindHandles' own convention) -- a caller measuring "did
// this case gain >=1 new keyed bind" should treat this slice as a whole
// (ANY entry Resolved==true). The ONE exception is avoidOverlapWith
// (repo_slug vs. provider_qualified_name, see the registry's own doc
// comment): entries are evaluated in registry order, and an entry naming
// an EARLIER entry in avoidOverlapWith skips any candidate match whose
// span intersects one of that earlier entry's ALREADY-ACCEPTED (filter-
// passed) matches, so a "github.com/org/repo" URL is reported ONCE, as a
// provider_qualified_name match, never also as a mis-split repo_slug
// fragment. Every OTHER pair of entries (e.g. branch_name_prefix and
// repo_slug both matching the identical "fix/handle-widening" span) is
// deliberately left to overlap and both are reported, so the per-grammar
// bind counts this ticket's pre-registration asks for are not silently
// undercounted for a token that is genuinely ambiguous between two
// grammars.
func BindSourceNativeHandles(question string, claimantsByTerm map[string][]IdentityMatch, complete bool) []SourceNativeBind {
	var out []SourceNativeBind
	acceptedSpans := make(map[string][][2]int, len(sourceNativeGrammarRegistry))
	for _, entry := range sourceNativeGrammarRegistry {
		locations := entry.pattern.FindAllStringSubmatchIndex(question, -1)
	nextMatch:
		for _, loc := range locations {
			start, end := loc[0], loc[1]
			if entry.valueGroup > 0 {
				groupIndex := entry.valueGroup * 2
				if groupIndex+1 >= len(loc) || loc[groupIndex] < 0 {
					continue
				}
				start, end = loc[groupIndex], loc[groupIndex+1]
			}
			for _, avoid := range entry.avoidOverlapWith {
				for _, span := range acceptedSpans[avoid] {
					if start < span[1] && span[0] < end {
						continue nextMatch
					}
				}
			}
			term := question[start:end]
			if entry.filter != nil && !entry.filter(term) {
				continue
			}
			acceptedSpans[entry.name] = append(acceptedSpans[entry.name], [2]int{start, end})
			searchValue := term
			if entry.lookupKey != nil {
				searchValue = entry.lookupKey(term)
			}
			bind := SourceNativeBind{Grammar: entry.name, Term: term}
			if complete && searchValue != "" {
				bind.Resolved, bind.Kind = resolveSourceNative(searchValue, claimantsByTerm)
			}
			out = append(out, bind)
		}
	}
	return out
}

// sourceNativeCandidateKey is resolveSourceNative's own dedup/uniqueness
// key -- identical shape to BindAnchor's own unexported candidateKey
// (chaos3899_anchor.go), kept as a separate type since Go has no
// package-private type reuse across files that would change this.
type sourceNativeCandidateKey struct {
	kind CensusKind
	id   string
}

// resolveSourceNative implements this file's own "REACHABILITY NOTE": it
// scans every IdentityRow already present ANYWHERE in claimantsByTerm's
// values (not the map's keys) and checks each row's own Label/Aliases/
// ProviderAliases against searchValue, using NormalizeAliasTerm --
// mirroring matchedTermsForRow's (chaos3884_identity_universe.go) own
// priority scan verbatim, just evaluated against ONE target string
// instead of a whole term set. Requires R4 uniqueness across the WHOLE
// scan (exactly one distinct (kind, canonical id) pair, mirroring
// BindAnchor's own seen-map discipline) -- two DIFFERENT rows that both
// happen to carry searchValue as an alias (should not occur in practice
// for a well-formed identity universe, but not assumed away) refuse
// exactly like an ambiguous anchor term does, never guess.
func resolveSourceNative(searchValue string, claimantsByTerm map[string][]IdentityMatch) (resolved bool, kind CensusKind) {
	target := NormalizeAliasTerm(searchValue)
	if target == "" {
		return false, ""
	}
	seen := map[sourceNativeCandidateKey]bool{}
	for _, matches := range claimantsByTerm {
		for _, m := range matches {
			if rowHasAlias(m.Row, target) {
				seen[sourceNativeCandidateKey{kind: m.Row.Kind, id: m.Row.CanonicalID}] = true
			}
		}
	}
	if len(seen) != 1 {
		return false, ""
	}
	for k := range seen {
		return true, k.kind
	}
	return false, "" // unreachable (len(seen)==1 above), satisfies the compiler
}

// rowHasAlias reports whether row's own Label, or any of its Aliases/
// ProviderAliases, equals target under NormalizeAliasTerm -- target is
// ALREADY normalized by the caller (resolveSourceNative); row's own
// fields are normalized here, exactly mirroring matchedTermsForRow's own
// per-field normalization.
func rowHasAlias(row IdentityRow, target string) bool {
	if NormalizeAliasTerm(row.Label) == target {
		return true
	}
	for _, alias := range row.Aliases {
		if NormalizeAliasTerm(alias) == target {
			return true
		}
	}
	for _, alias := range row.ProviderAliases {
		if NormalizeAliasTerm(alias) == target {
			return true
		}
	}
	return false
}
