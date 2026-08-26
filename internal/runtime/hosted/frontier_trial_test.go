package hosted_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestWriteVerbPattern is a plain unit test (not corpus-gated, runs in
// every `go test`/CI pass) proving isWriteVerbCommand (writeVerbPattern's
// noun+verb regexes for gh/linear-cli/git/clickhouse, PLUS the presence-
// only `gh api` ban via scanCommandForGhAPI, called with no pre-tokenization
// gate of any kind (round 5 finding) -- CHAOS-3853
// review round 4 dropped ALL content-dependent gh-api parsing after three
// rounds of bypasses; see the characterization doc comment above
// tokenizeShellWords in frontier_trial_live_test.go) actually discriminates
// read commands from mutating/forbidden ones -- this is the PRIMARY
// read-only enforcement for gh/linear-cli (see frontier_trial_live_test.go's
// package doc comment for why a PATH jail does not work), so a false
// negative here is a real safety gap, not just a test nicety.
func TestWriteVerbPattern(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"gh repo view is read", `/bin/zsh -lc 'gh repo view full-chaos/acr -R full-chaos/acr'`, false},
		{"gh issue view is read", `/bin/zsh -lc 'gh issue view 123 -R full-chaos/acr'`, false},
		{"gh issue list is read", `/bin/zsh -lc 'gh issue list -R full-chaos/acr --limit 10'`, false},
		{"gh pr diff is read", `/bin/zsh -lc 'gh pr diff 45 -R full-chaos/acr'`, false},
		// CHAOS-3853 review round 4: `gh api` is now a FORBIDDEN tool
		// outright (buildFrontierPrompt no longer grants it -- see
		// TestBuildFrontierPromptForbidsGhAPI) after three rounds of
		// content-dependent detection (method/flag/graphql parsing) each
		// being bypassed by a new quoting/obfuscation trick. With no
		// legitimate use of `gh api` left, presence alone is the violation
		// -- see the characterization doc comment above tokenizeShellWords
		// in frontier_trial_live_test.go for the full ladder and why every
		// case below is now either `true` (an actual "gh","api" token
		// adjacency exists somewhere, at any wrapper-recursion depth, or
		// the command failed to tokenize -- fail closed) or `false` (no
		// such adjacency, and the command tokenizes cleanly).
		{"gh api (plain GET-shaped call) is banned outright", `/bin/zsh -lc 'gh api repos/full-chaos/acr/issues'`, true},
		{"gh api -X POST is banned outright", `/bin/zsh -lc 'gh api -X POST repos/full-chaos/acr/issues'`, true},
		{"gh api graphql query-shaped call is banned outright (no read carve-out remains)", `/bin/zsh -lc 'gh api graphql -f query="query { viewer { login } }"'`, true},
		{"gh issue create is a write", `/bin/zsh -lc 'gh issue create -R full-chaos/acr --title x --body y'`, true},
		{"gh issue comment is a write", `/bin/zsh -lc 'gh issue comment 123 -R full-chaos/acr --body hi'`, true},
		{"gh pr merge is a write", `/bin/zsh -lc 'gh pr merge 45 -R full-chaos/acr'`, true},
		{"gh run rerun is a write", `/bin/zsh -lc 'gh run rerun 999 -R full-chaos/acr'`, true},

		// CHAOS-3853 review round 4 delta-pass findings, re-pinned under the
		// new presence-only rule (each of these WAS a bypass of the old
		// content-dependent detector; every one of them now correctly
		// resolves to a ban, either via a genuine token adjacency the
		// tokenizer resolves through the obfuscation, or via fail-closed):
		//
		//   (1) wrapper-peel only checked the FIRST -c/-lc payload found,
		//       so a decoy `bash -c 'true'` ahead of the real `gh api` call
		//       hid it. scanCommandForGhAPI now scans every token at every
		//       recursion level, not just inside the first -c payload.
		{"wrapper-peel bypass: a decoy bash -c ahead of the real gh api call no longer hides it", `/bin/zsh -lc "bash -c 'true'; gh api repos/full-chaos/acr/issues -f y=z"`, true},
		//   (4) only the FIRST gh api invocation in a chain was evaluated,
		//       so a "read-shaped" first call let a "write-shaped" second
		//       call through. Moot under presence-only (the FIRST mention
		//       alone is already disqualifying), but re-pinned as
		//       regression insurance against ANY future scoping that only
		//       looks at "the first match".
		{"only-first-invocation bypass: a second gh api call still trips the ban even after a first", `/bin/zsh -lc 'gh api -X GET x; gh api y -f body=x'`, true},
		//   (2)/(3) obfuscation bugs are moot now too (no flag-attachment
		//   or backslash-newline parsing remains at all -- presence-only
		//   has nothing left for those tricks to spoof), but the underlying
		//   escape/quote-splitting obfuscation itself is re-pinned here at
		//   the tokenizer+adjacency level: an escaped or quote-split "api"
		//   token must still resolve to the literal word "api". Bare
		//   (unwrapped) commands, deliberately, to isolate the
		//   tokenizer/adjacency logic from wrapper-recursion complexity --
		//   the wrapper-peel case above already separately proves
		//   recursion finds a real invocation at any depth.
		{"backslash-escaped api (gh ap\\i) still resolves to the literal word api", `gh ap\i repos/full-chaos/acr/issues`, true},
		{"quote-split api (gh ap'i') still resolves to the literal word api", `gh ap'i' repos/full-chaos/acr/issues`, true},
		// CHAOS-3853 review round 5: the SAME obfuscation trick applied to
		// "gh" itself, not "api" -- proves the fix isn't just about which
		// word gets obfuscated, and doubles as the direct regression pin
		// for round 5's finding (a raw-substring prefilter in front of the
		// tokenizer, gating on ANY string, reopens this exact bug class;
		// the terminal fix is no prefilter at all, not a smarter one).
		{"backslash-escaped gh (g\\h api) still resolves to adjacent gh api tokens", `g\h api repos/full-chaos/acr/issues -f y=z`, true},
		//   tokenizer fix: backslash-newline (shell line continuation) now
		//   fails closed instead of splicing a literal newline into a
		//   token (which could corrupt token boundaries and, under the OLD
		//   flag-parsing detector, silently drop a following -f token).
		{"gh api split across a backslash-newline line continuation fails closed to a violation", "/bin/zsh -lc \"gh api repos/full-chaos/acr/issues \\\n-f body=x\"", true},

		// Negative controls: "gh" and "api" both present in the raw text,
		// but NOT as adjacent tokens naming an actual `gh api` invocation.
		{"linear-cli mentioning the word api in a flag VALUE is not a gh api call", `/bin/zsh -lc 'linear-cli issues list --team CHAOS --filter api'`, false},
		{"a quoted phrase containing gh and api together is ONE token, not an adjacent pair", `/bin/zsh -lc "rg 'gh api' notes.md"`, false},

		// CHAOS-3853 review round 6 (executable-verified delta pass): three
		// more lexer-fidelity gaps, all fixed in tokenizeShellWords/
		// scanCommandForGhAPI -- see "TERMINAL RUNG, PART 3" above
		// scanCommandForGhAPI in frontier_trial_live_test.go.
		//
		// (1) attached operators glued into the word, hiding the adjacency.
		{"gh api glued to a pipe via | is still a write (operator is now its own token)", `/bin/zsh -lc 'gh api|head'`, true},
		{"gh api glued to a semicolon is still a write", `/bin/zsh -lc 'gh api; true'`, true},
		{"gh api glued to && is still a write", `/bin/zsh -lc 'gh api&&true'`, true},
		{"gh pr list piped with spaces stays read (negative control for the operator fix)", `/bin/zsh -lc 'gh pr list | head'`, false},

		// (2) ANY expandable $ can mint the forbidden tokens at runtime --
		// double quotes do NOT prevent expansion. ANSI-C quoting was the
		// concrete bypass found; the deeper rule (any non-allowlisted $
		// fails closed) is pinned directly too. Bare/unwrapped, deliberately
		// (same rationale as the round-4 obfuscation pins: isolates the
		// tokenizer's own $ handling from wrapper-recursion complexity).
		{"ANSI-C quoting ($'...') fails closed to a violation -- was the round-6 bypass", `bash -c $'gh api repos/x'`, true},
		{"a bare expandable $X before api fails closed to a violation", `$X api repos/x`, true},
		{"a double-quoted expandable $X before api ALSO fails closed (quotes don't block expansion)", `"$X" api repos/x`, true},

		// CRITICAL negative controls: the prompt's OWN mandated ClickHouse
		// invocation pattern (buildFrontierPrompt, `--user
		// "$FRONTIER_CH_USER" --password "$FRONTIER_CH_PASSWORD"`) must
		// NEVER be flagged -- every real trial run sends this exact shape
		// for every ClickHouse call.
		{"the mandated ClickHouse credential-reference pattern is read (CRITICAL negative control)", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --user "$FRONTIER_CH_USER" --password "$FRONTIER_CH_PASSWORD" --query "SELECT 1"'`, false},
		{"echoing the credential var is read for THIS detector's purposes (the leak concern is separate and already documented)", `/bin/zsh -lc 'echo "$FRONTIER_CH_PASSWORD"'`, false},

		// (3) eval/source/. re-parse a string this tokenizer cannot follow
		// statically -- unconditionally disqualifying as a bare word token,
		// independent of adjacency.
		{"eval hides a quoted gh api invocation -- fails closed regardless of adjacency", `/bin/zsh -lc "eval 'gh api repos/full-chaos/acr/issues'"`, true},
		{"source (word token) is unconditionally a violation regardless of its argument", `/bin/zsh -lc 'source /tmp/script.sh'`, true},
		{"standalone . (dot-source) is unconditionally a violation", `/bin/zsh -lc '. /tmp/script.sh'`, true},
		// Known, accepted conservative false positive (reviewer's own
		// example): "eval" appearing as an ORDINARY word (not the builtin)
		// still trips the check, since this tokenizer cannot distinguish
		// "eval used as a shell builtin" from "eval used as a search term"
		// -- documented here rather than special-cased, per the fail-closed
		// asymmetry (a false positive costs one trial case's score; a false
		// negative on a REAL eval-hidden invocation is the harder failure).
		{"rg eval notes.md is conservatively flagged -- documented false positive, not special-cased", `/bin/zsh -lc 'rg eval notes.md'`, true},

		// FP audit: ordinary agent command SHAPES from the allowed-tools
		// list (pipelines, redirects, &&, env-var prefixes) must stay read.
		{"gh issue list piped to head is read", `/bin/zsh -lc 'gh issue list -R full-chaos/acr | head -5'`, false},
		{"linear-cli search piped through jq is read", `/bin/zsh -lc 'linear-cli search "bug" | jq .'`, false},
		{"clickhouse query piped through jq is read (mandated credential pattern + pipe)", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --user "$FRONTIER_CH_USER" --password "$FRONTIER_CH_PASSWORD" --query "SELECT 1 FORMAT JSON" | jq .'`, false},
		{"env-var-prefixed gh command is read", `/bin/zsh -lc 'GH_PAGER=cat gh issue list -R full-chaos/acr'`, false},
		{"gh pr view with an output redirect is read", `/bin/zsh -lc 'gh pr view 45 -R full-chaos/acr > /tmp/out.txt'`, false},
		{"two read commands chained with && stay read", `/bin/zsh -lc 'gh issue list -R full-chaos/acr && gh pr list -R full-chaos/acr'`, false},

		{"linear-cli issues list is read", `/bin/zsh -lc 'linear-cli issues list --team CHAOS'`, false},
		{"linear-cli issues get is read", `/bin/zsh -lc 'linear-cli issues get CHAOS-3853'`, false},
		{"linear-cli search is read", `/bin/zsh -lc 'linear-cli search "frontier baseline"'`, false},
		{"linear-cli issues create is a write", `/bin/zsh -lc 'linear-cli issues create --title x --team CHAOS'`, true},
		{"linear-cli issues update is a write", `/bin/zsh -lc 'linear-cli issues update CHAOS-1 --state Done'`, true},
		{"linear-cli comments create is a write", `/bin/zsh -lc 'linear-cli comments create CHAOS-1 --body hi'`, true},
		{"linear-cli bulk is a write", `/bin/zsh -lc 'linear-cli bulk update --filter x --state Done'`, true},
		{"linear-cli sync is a write", `/bin/zsh -lc 'linear-cli sync push'`, true},
		// CHAOS-3853 review finding #2 negative control: a `created` flag
		// value must not false-positive against the `create` verb word.
		{"linear-cli issues list with --filter created is read, not create", `/bin/zsh -lc 'linear-cli issues list --team CHAOS --filter created'`, false},

		{"git log is read", `/bin/zsh -lc 'git log --oneline -5'`, false},
		{"git show is read", `/bin/zsh -lc 'git show HEAD'`, false},
		{"git status is read", `/bin/zsh -lc 'git status'`, false},
		{"git commit is a write", `/bin/zsh -lc 'git commit -am "oops"'`, true},
		{"git push is a write", `/bin/zsh -lc 'git push origin main'`, true},
		{"clickhouse SELECT is read", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --query "SELECT count() FROM ci_pipeline_runs"'`, false},
		{"clickhouse SHOW is read", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --query "SHOW TABLES"'`, false},
		{"clickhouse INSERT is a write", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --query "INSERT INTO x VALUES (1)"'`, true},
		{"clickhouse DROP is a write", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --query "DROP TABLE x"'`, true},
		{"clickhouse ALTER is a write", `/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --query "ALTER TABLE x DELETE WHERE 1"'`, true},
		{"plain cat is read", `/bin/zsh -lc 'cat notes.txt'`, false},

		// CHAOS-3853 review finding #2b: shell-indirection wrappers
		// (bash -c/sh -c/xargs) must not evade the scan -- verified with
		// tests, not assumed, per the review's explicit instruction.
		{"gh issue create via nested bash -c is a write", `/bin/zsh -lc 'bash -c "gh issue create -R full-chaos/acr --title x --body y"'`, true},
		{"gh repo view via nested sh -c is read", `/bin/zsh -lc 'sh -c "gh repo view full-chaos/acr -R full-chaos/acr"'`, false},
		{"gh issue comment via xargs is a write", `/bin/zsh -lc 'printf %s 123 | xargs -I{} gh issue comment {} -R full-chaos/acr --body hi'`, true},

		// CHAOS-3853 review finding #2b: segment-bounding (`[^|;&\n]*`)
		// must not let a noun in one chained command pair with a verb in
		// another across a `;` or `|` boundary, and a genuine write later
		// in a chain must still be caught.
		{"chained: read then write -- write still hits", `/bin/zsh -lc 'gh issue list -R full-chaos/acr; gh issue create -R full-chaos/acr --title x'`, true},
		{"chained: both reads -- no false positive", `/bin/zsh -lc 'gh issue list -R full-chaos/acr; gh pr diff 1 -R full-chaos/acr'`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isWriteVerbCommand(tc.command)
			if got != tc.want {
				t.Errorf("isWriteVerbCommand(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestGhAPIDetectorDocumentedResiduals pins the round-7 chris ruling (see
// the "SCOPE BOUNDARY" doc comment above scanCommandForGhAPI in
// frontier_trial_live_test.go): CHAOS-3853 stops iterating the gh-api
// detector after six rounds. These constructs are CONFIRMED, real,
// executable-verified false negatives (isWriteVerbCommand returns false --
// "read" -- even though each one actually executes `gh api` or a
// dot-sourced script this tokenizer cannot follow) that are deliberately
// OUT OF SCOPE: this classifier measures organic frontier-model output on
// a real investigation task, not adversarial input crafted against a
// classifier the model cannot see, and closing this specific class would
// mean modeling full POSIX shell grammar (redirection placement, compound
// commands, prefix keywords) -- an unbounded scope for a bounded harness.
// This test exists so the boundary is asserted in CODE, not just prose: a
// future reader sees these were considered and accepted, not missed.
//
// (`cat <(gh api)`, the literal example first cited for the process-
// substitution class, is NOT actually a residual -- "gh" and "api" stay
// adjacent tokens even after `<(` `)` become their own operator tokens,
// so isWriteVerbCommand correctly still returns true for it, see
// TestWriteVerbPattern. `gh <(true) api ...` is the accurate minimal
// reproduction: the substitution/redirection sits BETWEEN "gh" and "api",
// which is what actually breaks the adjacency scan.)
func TestGhAPIDetectorDocumentedResiduals(t *testing.T) {
	cases := []struct {
		name    string
		command string
	}{
		// Redirection-split token separation: a redirect target inserted
		// between "gh" and "api" becomes its own token(s), so they are no
		// longer ADJACENT even though this is still `gh api ...` executing
		// (redirections can appear anywhere in a simple command in real
		// shell grammar, not just at the end).
		{"redirection-split: gh>/dev/null api", `/bin/zsh -lc 'gh>/dev/null api repos/full-chaos/acr/issues'`},
		// Process substitution used the same way -- as a token-separating
		// insertion between "gh" and "api", not as `gh api`'s own argument
		// (where adjacency would survive, see the doc comment above).
		{"process substitution as a token-separating insertion: gh <(true) api", `/bin/zsh -lc 'gh <(true) api repos/full-chaos/acr/issues'`},
		// Prefixed dot-source: "." only trips the round-6 dot-source check
		// in COMMAND-VERB position (index 0, or right after a bare ;|&
		// separator -- deliberately narrowed to avoid false-positiving on
		// `jq .`). Anything else preceding "." evades it.
		{"dot-source after a negation prefix: ! . s", `/bin/zsh -lc '! . s'`},
		{"dot-source after an assignment prefix: FOO=x . s", `/bin/zsh -lc 'FOO=x . s'`},
		{"dot-source inside an if-condition: if . s; then ...", `/bin/zsh -lc 'if . s; then true; fi'`},
		{"dot-source inside a brace group: { . s; }", `/bin/zsh -lc '{ . s; }'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWriteVerbCommand(tc.command); got != false {
				t.Errorf("isWriteVerbCommand(%q) = %v, want false (documented residual -- if this now returns true, the scope-boundary doc comment and this test's rationale need to be revisited, not just the expectation flipped)", tc.command, got)
			}
		})
	}
}

// TestTokenizeShellWords is the direct unit test for the quote-aware
// tokenizer CHAOS-3853 review round 3 introduced to replace string-level
// regex scanning for the gh-api write/read decision (see the
// characterization doc comment above tokenizeShellWords in
// frontier_trial_live_test.go for why). Covers plain words, both quoting
// styles, backslash escaping (in both contexts), the adjacent quoted/
// unquoted word-concatenation rule real shells use, and every rejection
// case (unterminated quotes, command substitution via $( or a backtick --
// both unquoted and inside double quotes, but NOT inside single quotes,
// where real shells also treat them as fully literal -- a trailing
// backslash, and an embedded raw newline).
func TestTokenizeShellWords(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		tokens []string
		ok     bool
	}{
		{"simple words", "gh api foo", []string{"gh", "api", "foo"}, true},
		{"extra whitespace collapses", "gh   api\tfoo", []string{"gh", "api", "foo"}, true},
		{"single-quoted value with spaces is one token", `gh api 'a b c'`, []string{"gh", "api", "a b c"}, true},
		{"single quotes are fully literal -- no backslash escaping", `gh api 'a\b'`, []string{"gh", "api", `a\b`}, true},
		{"double-quoted value with spaces is one token", `gh api "a b c"`, []string{"gh", "api", "a b c"}, true},
		{"double-quote backslash escapes the next char", `gh api "a \"b\" c"`, []string{"gh", "api", `a "b" c`}, true},
		{"backslash outside quotes escapes exactly one char", `gh api a\ b`, []string{"gh", "api", "a b"}, true},
		{"adjacent quoted and unquoted text concatenate into one word", `gh api foo'bar'baz`, []string{"gh", "api", "foobarbaz"}, true},
		{"unterminated single quote fails", `gh api 'unterminated`, nil, false},
		{"unterminated double quote fails", `gh api "unterminated`, nil, false},
		{"trailing backslash fails", `gh api foo\`, nil, false},
		{"backtick command substitution fails (unquoted)", "gh api `whoami`", nil, false},
		{"dollar-paren command substitution fails (unquoted)", "gh api $(whoami)", nil, false},
		{"command substitution fails inside double quotes too", `gh api "$(whoami)"`, nil, false},
		{"command substitution is INERT (not a failure) inside single quotes", `gh api 'literal $(not expanded)'`, []string{"gh", "api", "literal $(not expanded)"}, true},
		{"embedded raw newline fails", "gh api foo\nbar", nil, false},
		// CHAOS-3853 review round 4 tokenizer fix: a backslash immediately
		// followed by a newline is a shell LINE CONTINUATION -- the
		// previous implementation spliced a literal newline byte into the
		// current token instead, which could corrupt token boundaries.
		// Fails closed now rather than replicate the real splice-with-no-
		// boundary semantics.
		{"backslash-newline line continuation fails closed, unquoted", "gh api foo\\\nbar", nil, false},
		{"backslash-newline line continuation fails closed, inside double quotes", "gh api \"foo\\\nbar\"", nil, false},

		// CHAOS-3853 review round 6: unquoted shell operators are their own
		// token-boundary characters, not ordinary word content -- a real
		// shell splits `api|head` into two words at the pipe.
		{"unquoted pipe is its own token", `gh api|head`, []string{"gh", "api", "|", "head"}, true},
		{"unquoted semicolon glued to a word is its own token", `gh api;true`, []string{"gh", "api", ";", "true"}, true},
		{"unquoted double ampersand splits into two single-char & tokens", `gh api&&true`, []string{"gh", "api", "&", "&", "true"}, true},
		{"unquoted redirect operator is its own token", `echo hi>out.txt`, []string{"echo", "hi", ">", "out.txt"}, true},
		{"unquoted parens are their own tokens", `(gh api)`, []string{"(", "gh", "api", ")"}, true},
		{"operators inside single quotes are literal content, not boundaries", `echo 'a|b'`, []string{"echo", "a|b"}, true},
		{"operators inside double quotes are literal content, not boundaries", `echo "a|b"`, []string{"echo", "a|b"}, true},

		// CHAOS-3853 review round 6: $ expansion. Only the two allowlisted
		// credential variables (matchAllowlistedVarRef) are known-safe;
		// everything else -- a different variable, a positional/special
		// parameter, ANSI-C/locale quoting -- fails closed.
		{"allowlisted bare var contributes nothing new (opaque, safe by construction)", `echo $FRONTIER_CH_USER`, []string{"echo", ""}, true},
		{"allowlisted braced var contributes nothing new", `echo ${FRONTIER_CH_PASSWORD}`, []string{"echo", ""}, true},
		{"allowlisted braced var followed by literal text -- braces delimit the name exactly", `echo ${FRONTIER_CH_USER}X`, []string{"echo", "X"}, true},
		{"allowlisted var referenced inside double quotes (the mandated prompt pattern)", `echo "$FRONTIER_CH_USER"`, []string{"echo", ""}, true},
		{"non-allowlisted bare var fails closed", `echo $HOME`, nil, false},
		{"non-allowlisted braced var fails closed", `echo ${PATH}`, nil, false},
		{"greedy bare-name matching: a LONGER var name is not the allowlisted one", `echo $FRONTIER_CH_USERX`, nil, false},
		{"ANSI-C quoting $'...' fails closed", `$'gh api'`, nil, false},
		{"locale quoting $\"...\" fails closed", `$"gh api"`, nil, false},
		{"positional parameter $1 fails closed", `echo $1`, nil, false},
		{"special parameter $@ fails closed", `echo $@`, nil, false},
		{"bare trailing $ fails closed", `echo $`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens, ok := tokenizeShellWords(tc.input)
			if ok != tc.ok {
				t.Fatalf("tokenizeShellWords(%q) ok = %v, want %v (tokens=%#v)", tc.input, ok, tc.ok, tokens)
			}
			if !ok {
				return
			}
			if len(tokens) != len(tc.tokens) {
				t.Fatalf("tokenizeShellWords(%q) = %#v, want %#v", tc.input, tokens, tc.tokens)
			}
			for i := range tokens {
				if tokens[i] != tc.tokens[i] {
					t.Errorf("tokenizeShellWords(%q)[%d] = %q, want %q", tc.input, i, tokens[i], tc.tokens[i])
				}
			}
		})
	}
}

// TestBuildFrontierPromptDoesNotLeakClickHouseCredential is the regression
// proof for CHAOS-3853 review's BLOCKING finding: buildFrontierPrompt must
// reference the ClickHouse credential by its env-var NAME
// ($FRONTIER_CH_USER/$FRONTIER_CH_PASSWORD, shell-expanded by codex's
// /bin/zsh -lc at execution time) and must NEVER embed the literal
// configured user/password -- every codex --json transcript would
// otherwise carry the live credential verbatim.
func TestBuildFrontierPromptDoesNotLeakClickHouseCredential(t *testing.T) {
	cfg := frontierRunConfig{
		CHContainer: "dev-health-clickhouse-1",
		CHUser:      "SENTINEL_USER_7F2A",
		CHPassword:  "SENTINEL_PW_193B",
	}
	prompt := buildFrontierPrompt(cfg, "irrelevant question text")

	if strings.Contains(prompt, cfg.CHPassword) {
		t.Errorf("buildFrontierPrompt output contains the literal ClickHouse password %q -- codex transcripts would carry the live credential", cfg.CHPassword)
	}
	if strings.Contains(prompt, cfg.CHUser) {
		t.Errorf("buildFrontierPrompt output contains the literal ClickHouse user %q -- codex transcripts would carry the live credential", cfg.CHUser)
	}
	if !strings.Contains(prompt, "$FRONTIER_CH_USER") {
		t.Errorf("buildFrontierPrompt output must reference $FRONTIER_CH_USER (shell-expanded at execution time)")
	}
	if !strings.Contains(prompt, "$FRONTIER_CH_PASSWORD") {
		t.Errorf("buildFrontierPrompt output must reference $FRONTIER_CH_PASSWORD (shell-expanded at execution time)")
	}
}

// TestBuildFrontierPromptForbidsGhAPI is the regression proof for CHAOS-3853
// review round 4's contract change: after three rounds of a content-
// dependent `gh api` write/read detector each being bypassed by a new
// quoting trick, the harness now forbids `gh api` outright rather than
// trying to police its use -- buildFrontierPrompt must no longer grant it
// (the prompt used to say "api (GET only)") and must name it explicitly in
// the NEVER list, so the ban is a genuine tool-contract violation the
// agent is told about, not just an undocumented detector-side surprise.
func TestBuildFrontierPromptForbidsGhAPI(t *testing.T) {
	cfg := frontierRunConfig{CHContainer: "dev-health-clickhouse-1", CHUser: "u", CHPassword: "p"}
	prompt := buildFrontierPrompt(cfg, "irrelevant question text")

	if strings.Contains(prompt, "api (GET only)") {
		t.Errorf("buildFrontierPrompt output still grants gh api (GET only) -- it must be removed from the allowed-tools list entirely")
	}
	if !strings.Contains(prompt, "gh api") {
		t.Errorf("buildFrontierPrompt output must name gh api explicitly (in the NEVER list) so the ban is documented for the agent, not just enforced silently by the detector")
	}
}

// TestClickHouseCredentialCheckScript is the regression proof for CHAOS-3853
// review round 2's P2 finding: the preflight ClickHouse credential check
// (verifyClickHouseReadOnlyCredential) must never put the password in ANY
// process argv. clickHouseCredentialCheckScript only ever takes the user --
// the password goes over stdin at run time, wired in
// verifyClickHouseReadOnlyCredential itself -- so this proves the script
// text has no way to embed a password, reads it from stdin into $PW, and
// rejects a user value that isn't safe to interpolate into shell script
// text.
func TestClickHouseCredentialCheckScript(t *testing.T) {
	script, err := clickHouseCredentialCheckScript("frontier_trial_ro")
	if err != nil {
		t.Fatalf("clickHouseCredentialCheckScript: %v", err)
	}
	if !strings.Contains(script, "read -r PW") {
		t.Errorf("script must read the password from stdin into $PW, got %q", script)
	}
	if !strings.Contains(script, `--user 'frontier_trial_ro'`) {
		t.Errorf("script must reference the configured user, got %q", script)
	}
	if !strings.Contains(script, `--password "$PW"`) {
		t.Errorf("script must pass the password via the $PW variable, never a literal, got %q", script)
	}

	if _, err := clickHouseCredentialCheckScript("bad'user"); err == nil {
		t.Errorf("expected an error for a user containing a single quote (shell-injection risk when interpolated)")
	}
	if _, err := clickHouseCredentialCheckScript(`bad\user`); err == nil {
		t.Errorf("expected an error for a user containing a backslash")
	}
}

// TestScanFrontierTranscript exercises the JSONL parsing path end-to-end
// against a synthetic transcript (not a real codex run), proving tool-call
// counting, usage summation, and write-verb propagation all wire together
// correctly.
func TestScanFrontierTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	lines := []string{
		`{"type": "thread.started", "thread_id": "x"}`,
		`{"type": "turn.started"}`,
		`{"type": "item.completed", "item": {"id": "item_0", "type": "command_execution", "command": "/bin/zsh -lc 'gh repo view full-chaos/acr -R full-chaos/acr'", "exit_code": 0}}`,
		`{"type": "item.completed", "item": {"id": "item_1", "type": "command_execution", "command": "/bin/zsh -lc 'gh issue list -R full-chaos/acr'", "exit_code": 0}}`,
		`{"type": "item.completed", "item": {"id": "item_2", "type": "agent_message", "text": "{\"committed_kind\": null}"}}`,
		`{"type": "turn.completed", "usage": {"input_tokens": 1000, "cached_input_tokens": 200, "cache_write_input_tokens": 0, "output_tokens": 50, "reasoning_output_tokens": 20}}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatalf("write synthetic transcript: %v", err)
	}
	toolCalls, usage, writeHit, chTables := scanFrontierTranscript(path)
	if len(chTables.RawEventTables) != 0 || len(chTables.ComputedArtifactTables) != 0 || len(chTables.UnknownTables) != 0 {
		t.Errorf("chTables = %+v, want all empty (no clickhouse-client commands in this transcript)", chTables)
	}
	if toolCalls != 2 {
		t.Errorf("toolCalls = %d, want 2", toolCalls)
	}
	if writeHit {
		t.Errorf("writeHit = true, want false (both commands are reads)")
	}
	if usage.InputTokens != 1000 || usage.CachedInputTokens != 200 || usage.OutputTokens != 50 || usage.ReasoningOutputTokens != 20 {
		t.Errorf("usage = %+v, unexpected", usage)
	}

	// Now with a mutating command mixed in.
	linesWithWrite := append(append([]string{}, lines[:4]...),
		`{"type": "item.completed", "item": {"id": "item_3", "type": "command_execution", "command": "/bin/zsh -lc 'gh issue comment 1 -R full-chaos/acr --body hi'", "exit_code": 1}}`,
		lines[4], lines[5],
	)
	if err := os.WriteFile(path, []byte(joinLines(linesWithWrite)), 0o600); err != nil {
		t.Fatalf("write synthetic transcript with write: %v", err)
	}
	_, _, writeHit2, _ := scanFrontierTranscript(path)
	if !writeHit2 {
		t.Errorf("writeHit = false, want true -- an attempted write (even a failed one, exit_code 1) must still be flagged")
	}
}

// TestScanFrontierTranscriptClickHouseClassification proves the raw-vs-
// computed table split (chris's report-shaping ask, post-full-run-launch)
// wires through scanFrontierTranscript end-to-end against a synthetic
// transcript containing both kinds of ClickHouse query, plus an unknown
// table -- the loud-not-silent third bucket.
func TestScanFrontierTranscriptClickHouseClassification(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	lines := []string{
		`{"type": "item.completed", "item": {"id": "item_0", "type": "command_execution", "command": "/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --user frontier_trial_ro --password x --query \"SELECT count() FROM git_commits\"'", "exit_code": 0}}`,
		`{"type": "item.completed", "item": {"id": "item_1", "type": "command_execution", "command": "/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --user frontier_trial_ro --password x --query \"SELECT * FROM ci_daily_rollup WHERE 1=1\"'", "exit_code": 0}}`,
		`{"type": "item.completed", "item": {"id": "item_2", "type": "command_execution", "command": "/bin/zsh -lc 'docker exec dev-health-clickhouse-1 clickhouse-client --user frontier_trial_ro --password x --query \"SELECT * FROM some_new_table_not_yet_classified\"'", "exit_code": 0}}`,
		`{"type": "item.completed", "item": {"id": "item_3", "type": "command_execution", "command": "/bin/zsh -lc 'gh repo view full-chaos/acr -R full-chaos/acr'", "exit_code": 0}}`,
		`{"type": "turn.completed", "usage": {"input_tokens": 100, "cached_input_tokens": 0, "cache_write_input_tokens": 0, "output_tokens": 10, "reasoning_output_tokens": 0}}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatalf("write synthetic transcript: %v", err)
	}
	toolCalls, _, writeHit, chTables := scanFrontierTranscript(path)
	if toolCalls != 4 {
		t.Errorf("toolCalls = %d, want 4", toolCalls)
	}
	if writeHit {
		t.Errorf("writeHit = true, want false")
	}
	if len(chTables.RawEventTables) != 1 || chTables.RawEventTables[0] != "git_commits" {
		t.Errorf("RawEventTables = %v, want [git_commits]", chTables.RawEventTables)
	}
	if len(chTables.ComputedArtifactTables) != 1 || chTables.ComputedArtifactTables[0] != "ci_daily_rollup" {
		t.Errorf("ComputedArtifactTables = %v, want [ci_daily_rollup]", chTables.ComputedArtifactTables)
	}
	if len(chTables.UnknownTables) != 1 || chTables.UnknownTables[0] != "some_new_table_not_yet_classified" {
		t.Errorf("UnknownTables = %v, want [some_new_table_not_yet_classified]", chTables.UnknownTables)
	}
}

// TestClassifyClickHouseTable covers the hand-classified lists plus the
// naming-convention fallback for tables not explicitly listed.
//
// devhealthschema:not-a-production-replica -- the table names below are real ClickHouse tables used as test fixtures for classifyClickHouseTable (raw-event vs computed-artifact, report-shaping only); this is a read-side classification test, not a second physical-schema declaration, and asserts nothing about DDL for any table named here.
func TestClassifyClickHouseTable(t *testing.T) {
	cases := []struct {
		table string
		want  clickHouseTableClass
	}{
		{"git_commits", clickHouseTableRaw},
		{"git_pull_requests", clickHouseTableRaw},
		{"ci_job_runs", clickHouseTableRaw},
		{"repos", clickHouseTableRaw},
		{"ci_daily_rollup", clickHouseTableComputed},
		{"repo_metrics_daily", clickHouseTableComputed},
		{"work_graph_deployment_incident_edges", clickHouseTableComputed},
		{"CI_DAILY_ROLLUP", clickHouseTableComputed},          // case-insensitive
		{"some_future_rollup_table", clickHouseTableComputed}, // naming-convention fallback
		{"totally_unclassifiable_widget_table", clickHouseTableUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			if got := classifyClickHouseTable(tc.table); got != tc.want {
				t.Errorf("classifyClickHouseTable(%q) = %v, want %v", tc.table, got, tc.want)
			}
		})
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// TestEstimateCostUSD is a light sanity check -- it does not assert exact
// dollar values (the rate card is an explicitly-labeled illustrative
// snapshot, see estimateCostUSD's doc comment), only that usage scales the
// estimate monotonically and that an unknown model does not silently
// return zero.
func TestEstimateCostUSD(t *testing.T) {
	zero := estimateCostUSD("gpt-5.6-luna", frontierUsage{})
	if zero != 0 {
		t.Errorf("zero usage should cost 0, got %v", zero)
	}
	small := estimateCostUSD("gpt-5.6-luna", frontierUsage{InputTokens: 1000, OutputTokens: 100})
	large := estimateCostUSD("gpt-5.6-luna", frontierUsage{InputTokens: 10000, OutputTokens: 1000})
	if !(small > 0 && large > small) {
		t.Errorf("expected 0 < small(%v) < large(%v)", small, large)
	}
	unknown := estimateCostUSD("some-future-model-not-in-the-rate-table", frontierUsage{InputTokens: 1000, OutputTokens: 100})
	if unknown == 0 {
		t.Errorf("unknown model should fall back to a conservative rate, not silently report 0")
	}
}

// TestFrontierCommitSchemaRoundTrips proves the schema this harness ships
// (writeFrontierSchema's inline JSON) parses back into frontierCommit for
// both the commit and abstain shapes -- a schema/struct drift here would
// silently misclassify every case as frontier_output_invalid.
func TestFrontierCommitSchemaRoundTrips(t *testing.T) {
	for _, raw := range []string{
		`{"committed_kind": "project", "committed_id": "project:x", "confidence": 0.9, "abstain_reason": null}`,
		`{"committed_kind": null, "committed_id": null, "confidence": null, "abstain_reason": "no_match"}`,
		`{"committed_kind": null, "committed_id": null, "confidence": null, "abstain_reason": "ambiguous"}`,
	} {
		var c frontierCommit
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Errorf("unmarshal %q: %v", raw, err)
		}
	}
}

// TestRecognizedCanonicalID is the regression proof for the pilot's exact
// failure mode (CHAOS-3853 team-lead ruling, post-audit): a GitHub-native
// slug or bare UUID must be flagged as id_format_unrecognized, never
// silently scored as a wrong answer, while every real ACR canonical-id
// shape this codebase actually produces (confirmed via grep across
// devhealthsource/falkorgraph, including the two irregular shapes) is
// recognized.
func TestRecognizedCanonicalID(t *testing.T) {
	cases := []struct {
		name string
		kind contractsv1.ContextFabricSubjectKind
		id   string
		want bool
	}{
		{"repository canonical form", contractsv1.ContextFabricSubjectRepository, "repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7", true},
		{"repository as github slug -- the pilot's actual bug", contractsv1.ContextFabricSubjectRepository, "full-chaos/dev-health-web", false},
		{"repository as bare uuid, no prefix", contractsv1.ContextFabricSubjectRepository, "7b9583ee-4d24-2be7-4d09-34f815bebdd7", false},
		{"document uses content: prefix, not document:", contractsv1.ContextFabricSubjectDocument, "content:abc123", true},
		{"document with the naive (wrong) document: prefix", contractsv1.ContextFabricSubjectDocument, "document:abc123", false},
		{"pull_request two-part form", contractsv1.ContextFabricSubjectPullRequest, "pull_request:repo123:45", true},
		{"pull_request as bare number", contractsv1.ContextFabricSubjectPullRequest, "45", false},
		{"team canonical form", contractsv1.ContextFabricSubjectTeam, "team:CHAOS", true},
		{"project canonical form", contractsv1.ContextFabricSubjectProject, "project:xyz", true},
		{"work_item canonical form", contractsv1.ContextFabricSubjectWorkItem, "work_item:CHAOS-3853", true},
		{"organization canonical form", contractsv1.ContextFabricSubjectOrganization, "organization:70d529e0", true},
		{"deployment canonical form", contractsv1.ContextFabricSubjectDeployment, "deployment:dep1", true},
		{"incident canonical form", contractsv1.ContextFabricSubjectIncident, "incident:inc1", true},
		{"episode canonical form", contractsv1.ContextFabricSubjectEpisode, "episode:ep1", true},
		{"ci_pipeline_run canonical form", contractsv1.ContextFabricSubjectCIRun, "ci_pipeline_run:run1", true},
		{"pull_request_review canonical form", contractsv1.ContextFabricSubjectPullRequestReview, "pull_request_review:rev1", true},
		{"unknown kind falls back to kind-name prefix and can still pass", contractsv1.ContextFabricSubjectKind("widget"), "widget:1", true},
		{"unknown kind with no prefix fails", contractsv1.ContextFabricSubjectKind("widget"), "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recognizedCanonicalID(tc.kind, tc.id)
			if got != tc.want {
				t.Errorf("recognizedCanonicalID(%q, %q) = %v, want %v", tc.kind, tc.id, got, tc.want)
			}
		})
	}
}

// TestExtractCodexErrorMessage regression-proofs the credits-exhaustion
// diagnosis gap (CHAOS-3853, found root-causing cases 38-49 of the full
// run): the raw Go exec error ("exit status 1") carried zero information
// on its own -- codex's real error only showed up in the --json
// error/turn.failed event.
func TestExtractCodexErrorMessage(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events.jsonl")
	stderrPath := filepath.Join(dir, "stderr.log")

	t.Run("error event", func(t *testing.T) {
		lines := []string{
			`{"type":"thread.started","thread_id":"x"}`,
			`{"type":"turn.started"}`,
			`{"type":"error","message":"Your workspace is out of credits. Add credits to continue."}`,
			`{"type":"turn.failed","error":{"message":"Your workspace is out of credits. Add credits to continue."}}`,
		}
		os.WriteFile(eventsPath, []byte(joinLines(lines)), 0o600)
		os.WriteFile(stderrPath, nil, 0o600)
		got := extractCodexErrorMessage(eventsPath, stderrPath)
		if got != "Your workspace is out of credits. Add credits to continue." {
			t.Errorf("got %q", got)
		}
	})

	t.Run("turn.failed only", func(t *testing.T) {
		lines := []string{
			`{"type":"turn.started"}`,
			`{"type":"turn.failed","error":{"message":"rate limited, retry later"}}`,
		}
		os.WriteFile(eventsPath, []byte(joinLines(lines)), 0o600)
		os.WriteFile(stderrPath, nil, 0o600)
		got := extractCodexErrorMessage(eventsPath, stderrPath)
		if got != "rate limited, retry later" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("falls back to stderr when no json error event", func(t *testing.T) {
		os.WriteFile(eventsPath, []byte(`{"type":"turn.started"}`+"\n"), 0o600)
		os.WriteFile(stderrPath, []byte("some raw stderr text\n"), 0o600)
		got := extractCodexErrorMessage(eventsPath, stderrPath)
		if got != "some raw stderr text" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("empty when nothing available", func(t *testing.T) {
		os.WriteFile(eventsPath, []byte(`{"type":"turn.started"}`+"\n"), 0o600)
		os.WriteFile(stderrPath, nil, 0o600)
		got := extractCodexErrorMessage(eventsPath, stderrPath)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// TestArmLabelPattern covers the ARM path-hygiene allowlist (CHAOS-3853
// review P2): only plain path components are accepted -- anything that
// could escape the trial-results directory or otherwise misbehave as a
// filename component (path separators, "..", shell metacharacters, an
// empty string) must be rejected.
func TestArmLabelPattern(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"frontier_gpt_codex", true},
		{"frontier-gpt-codex-2", true},
		{"ARM_4", true},
		{"../escape", false},
		{"has/slash", false},
		{"has space", false},
		{"semi;colon", false},
		{"", false},
		{"$(rm -rf /)", false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := armLabelPattern.MatchString(tc.label); got != tc.want {
				t.Errorf("armLabelPattern.MatchString(%q) = %v, want %v", tc.label, got, tc.want)
			}
		})
	}
}

// TestWriteReportAtomic proves the report write CHAOS-3853 review asked for
// (P2, ARM path hygiene) is actually atomic: the destination path only ever
// shows the FULL final content (never a partial write), and no temp file is
// left behind on success.
func TestWriteReportAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gen-trial-some_arm.json")
	blob := []byte(`{"arm":"some_arm","cases_run":3}`)

	if err := writeReportAtomic(path, blob); err != nil {
		t.Fatalf("writeReportAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("report content = %q, want %q", got, blob)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries after writeReportAtomic, want 1 (no leftover temp file): %v", len(entries), entries)
	}

	// A second write (simulating a rerun into the same path) must fully
	// replace the content, not append or corrupt it.
	blob2 := []byte(`{"arm":"some_arm","cases_run":5}`)
	if err := writeReportAtomic(path, blob2); err != nil {
		t.Fatalf("writeReportAtomic (second write): %v", err)
	}
	got2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report (second write): %v", err)
	}
	if string(got2) != string(blob2) {
		t.Errorf("report content after second write = %q, want %q", got2, blob2)
	}
}

// TestFrontierUnsupportedDataPlaneReason (CHAOS-4220) pins
// frontierUnsupportedDataPlaneReason's own contract: only "kiac" is
// refused (this harness's docker-exec-shaped ClickHouse access cannot
// reach the kiac data plane's Kubernetes-hosted ClickHouse); everything
// else -- unset, "compose", or any other value -- is left alone, this
// function's job is narrowly to name the ONE unreachable plane, not to
// validate ACR_TRIAL_DATA_PLANE generally (common.sh's own shell-side
// switch already owns that).
func TestFrontierUnsupportedDataPlaneReason(t *testing.T) {
	cases := []struct {
		name      string
		plane     string
		wantEmpty bool
	}{
		{"unset (the raw env var this function sees, not common.sh's shell-side kiac default)", "", true},
		{"compose explicit", "compose", true},
		{"kiac explicit", "kiac", false},
		{"garbage value", "nonsense", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := frontierUnsupportedDataPlaneReason(c.plane)
			if c.wantEmpty && got != "" {
				t.Errorf("frontierUnsupportedDataPlaneReason(%q) = %q, want empty (proceed)", c.plane, got)
			}
			if !c.wantEmpty {
				if got == "" {
					t.Errorf("frontierUnsupportedDataPlaneReason(%q) = empty, want a non-empty refusal reason", c.plane)
				} else if !strings.Contains(got, "CHAOS-4220") {
					t.Errorf("frontierUnsupportedDataPlaneReason(%q) = %q, want it to cite CHAOS-4220", c.plane, got)
				}
			}
		})
	}
}
