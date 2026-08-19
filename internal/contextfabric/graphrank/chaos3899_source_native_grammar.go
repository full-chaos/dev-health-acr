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
// or an incomplete read -> no discriminator") against the SAME
// claimantsByTerm map the anchor role already reads -- literally the
// CHAOS-3884 identity-universe completeness+uniqueness read, just applied
// to a grammar-extracted literal instead of a search-derived term.
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
	// Term is the exact extracted literal -- in-process provenance ONLY,
	// exactly like BoundHandle.Value/AnchorBinding.Term; never traced
	// (corpus-safety rule).
	Term string
	// Resolved is true iff Term, looked up verbatim against
	// claimantsByTerm, names EXACTLY ONE claimant under a COMPLETE read --
	// BindAnchor's own R4 discipline, unchanged. This IS the "keyed bind"
	// the CHAOS-3899 pre-registration measures: a false Resolved with a
	// non-empty Term is a syntactic grammar match that failed to resolve
	// (claimant lookup failure) -- a false positive in the pre-registered
	// sense, counted separately from a true keyed bind.
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
	// commit_sha's "must contain a hex letter, never all-decimal" rule.
	// A nil filter accepts every regex match.
	filter func(string) bool
}

// containsHexLetter reports whether s contains at least one of a-f
// (case-insensitive) -- commit_sha's own precision guard (see
// sourceNativeGrammarRegistry's doc comment): an all-decimal run is
// indistinguishable from an ordinary large integer (a PR number typed
// without its "PR" prefix, a CI run id, a date, a byte count) and must
// never bind here, since none of those is a plausible commit SHA in
// practice and the collision risk with genuinely ordinary prose is real.
func containsHexLetter(s string) bool {
	return strings.ContainsAny(s, "abcdefABCDEF")
}

// looksLikeBranchToken reports whether s contains at least one character
// (digit, hyphen, underscore, or slash) that an ordinary bare English word
// following "branch" ("branch out", "branch into", "branch off", "branch
// the team") essentially never contains -- branch_name_keyword's own
// precision guard: without this filter, "branch <any single common verb
// or noun>" would bind on every sentence that uses "branch" as an
// ordinary verb, exactly the prose-swallowing this registry's own doc
// comment promises not to do.
func looksLikeBranchToken(s string) bool {
	return strings.ContainsAny(s, "-_/0123456789")
}

// sourceNativeGrammarRegistry is CHAOS-3899's widening measurement
// registry (chris-ratified, 2026-08-19; see this file's own doc comment
// for the shadow-only/measurement-only scope). Every entry follows
// handleGrammarRegistry's own R3 maximal-munch/word-boundary discipline
// (\b anchors, greedy quantifiers) plus its own additional precision
// guard against ordinary prose:
//
//   - provider_qualified_name: an explicit provider-domain prefix
//     (github.com/ or gitlab.com/) followed by an org/repo slug. The
//     narrowest grammar in this registry by construction -- a bare
//     provider domain literal essentially never appears in ordinary
//     prose, so this pattern needs no additional filter.
//   - repo_slug: two path segments joined by "/", each REQUIRED to start
//     with a letter (never a digit) -- excludes a bare numeric fraction
//     ("3/4 of the tests passed", "24/7 on-call") or a ratio/date-like
//     token, the ordinary-prose shape most likely to collide with a bare
//     "word/word" pattern.
//   - branch_name_keyword: keyword-anchored ("branch <name>"), the
//     conservative case the CHAOS-3899 rider calls for -- an unanchored
//     bare slug-shaped token would be indistinguishable from prose and
//     would also collide with repo_slug's own matches. Filtered
//     (looksLikeBranchToken) to require a digit/hyphen/underscore/slash in
//     the captured token -- without it, "branch out"/"branch into"/
//     "branch off" (ordinary English verb phrases) would bind on every
//     such sentence.
//   - branch_name_prefix: a well-known branch-prefix shape (feature/,
//     fix/, bugfix/, hotfix/, release/, chore/, docs/) followed by slug
//     characters -- conservative because the prefix set is closed and
//     none of these words plus "/" is a plausible ordinary-prose
//     collision ("fix/the-thing" is not English).
//   - commit_sha: 7-40 lowercase-or-uppercase hex characters, word
//     boundary, filtered to require at least one hex LETTER (a-f) --
//     excludes every all-decimal collision (see containsHexLetter).
var sourceNativeGrammarRegistry = []sourceNativeGrammarEntry{
	{
		name:       "provider_qualified_name",
		pattern:    regexp.MustCompile(`(?i)\b(?:github\.com|gitlab\.com)/[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?/[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?\b`),
		valueGroup: 0,
	},
	{
		name:       "repo_slug",
		pattern:    regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9._-]{0,38}/[A-Za-z][A-Za-z0-9._-]{0,38}\b`),
		valueGroup: 0,
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
		filter:     containsHexLetter,
	},
}

// BindSourceNativeHandles extracts every source-native identifier grammar
// match from question and resolves each one against claimantsByTerm using
// BindAnchor's OWN completeness+uniqueness discipline (R4) -- see this
// file's own doc comment for why this is the correct binding mechanism
// for an identifier that is not a stall-kind base table's natural key,
// and for the structural shadow-only guarantee. complete mirrors
// BindAnchor's own parameter exactly: false refuses every resolution
// outright (the identity view itself is unproven), matching R4's "an
// incomplete read -> no discriminator" rule.
//
// Overlap between two DIFFERENT registry entries is not deduplicated
// (mirrors BindHandles' own convention) -- a caller measuring "did this
// case gain >=1 new keyed bind" should treat this slice as a whole (ANY
// entry Resolved==true), not assume disjointness the way
// handleGrammarRegistry's three original patterns structurally guarantee
// (repo_slug and provider_qualified_name, in particular, CAN both match
// the same "org/repo" substring inside a longer "github.com/org/repo"
// span -- both are reported, deliberately, so the per-grammar bind counts
// this ticket's pre-registration asks for are not silently undercounted).
func BindSourceNativeHandles(question string, claimantsByTerm map[string][]IdentityMatch, complete bool) []SourceNativeBind {
	var out []SourceNativeBind
	for _, entry := range sourceNativeGrammarRegistry {
		locations := entry.pattern.FindAllStringSubmatchIndex(question, -1)
		for _, loc := range locations {
			start, end := loc[0], loc[1]
			if entry.valueGroup > 0 {
				groupIndex := entry.valueGroup * 2
				if groupIndex+1 >= len(loc) || loc[groupIndex] < 0 {
					continue
				}
				start, end = loc[groupIndex], loc[groupIndex+1]
			}
			term := question[start:end]
			if entry.filter != nil && !entry.filter(term) {
				continue
			}
			bind := SourceNativeBind{Grammar: entry.name, Term: term}
			if complete {
				if matches := claimantsByTerm[term]; len(matches) == 1 {
					bind.Resolved = true
					bind.Kind = matches[0].Row.Kind
				}
			}
			out = append(out, bind)
		}
	}
	return out
}
