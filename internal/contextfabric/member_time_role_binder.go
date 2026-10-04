package contextfabric

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MemberTimeRole is the closed time field a period names for the members of a
// scoped set: when an item was created, completed or last updated. The frame
// carries no such value; the server binds it from the question text by the
// closed registry below, beside the window binder that binds the period.
type MemberTimeRole string

const (
	MemberTimeRoleCreated   MemberTimeRole = "created"
	MemberTimeRoleCompleted MemberTimeRole = "completed"
	MemberTimeRoleUpdated   MemberTimeRole = "updated"
)

// MemberTimeRoleVocabulary is the closed role set in registry order.
func MemberTimeRoleVocabulary() []MemberTimeRole {
	return []MemberTimeRole{MemberTimeRoleCreated, MemberTimeRoleCompleted, MemberTimeRoleUpdated}
}

// WorkItemTimeColumn is the work_items column a role reads. Completion is
// work_items.completed_at, never closed_at or status.
func (r MemberTimeRole) WorkItemTimeColumn() (string, bool) {
	switch r {
	case MemberTimeRoleCreated:
		return "created_at", true
	case MemberTimeRoleCompleted:
		return "completed_at", true
	case MemberTimeRoleUpdated:
		return "updated_at", true
	}
	return "", false
}

// memberTimeRoleRegistry is the ONE list of verb forms. A widening is an entry
// added here and nowhere else. Each pattern is a whole-word, case-insensitive
// match; "active" and "in progress" are deliberately absent because they need
// status history that is not stored.
var memberTimeRoleRegistry = []struct {
	role    MemberTimeRole
	grammar string
	forms   []string
	pattern *regexp.Regexp
}{
	newMemberTimeRoleEntry(MemberTimeRoleCreated, "created_forms", "created", "opened", "filed", "raised", "added", "new"),
	newMemberTimeRoleEntry(MemberTimeRoleCompleted, "completed_forms", "completed", "finished", "closed", "done", "resolved", "shipped", "delivered"),
	newMemberTimeRoleEntry(MemberTimeRoleUpdated, "updated_forms", "updated", "changed", "modified", "touched"),
}

func newMemberTimeRoleEntry(role MemberTimeRole, grammar string, forms ...string) struct {
	role    MemberTimeRole
	grammar string
	forms   []string
	pattern *regexp.Regexp
} {
	return struct {
		role    MemberTimeRole
		grammar string
		forms   []string
		pattern *regexp.Regexp
	}{role, grammar, forms, regexp.MustCompile(`(?i)\b(?:` + strings.Join(forms, "|") + `)\b`)}
}

// MemberTimeRoleFormRow is one role with the column it reads and the verb
// forms that bind it, for the guide to quote.
type MemberTimeRoleFormRow struct {
	Role   MemberTimeRole
	Column string
	Forms  []string
}

// MemberTimeRoleFormRows lists the registry in order: the same entries the
// binder matches, so a published list cannot drift from the matcher.
func MemberTimeRoleFormRows() []MemberTimeRoleFormRow {
	rows := make([]MemberTimeRoleFormRow, 0, len(memberTimeRoleRegistry))
	for _, entry := range memberTimeRoleRegistry {
		column, _ := entry.role.WorkItemTimeColumn()
		rows = append(rows, MemberTimeRoleFormRow{Role: entry.role, Column: column, Forms: append([]string(nil), entry.forms...)})
	}
	return rows
}

// MemberTimeRoleReason is the closed outcome of binding a role.
type MemberTimeRoleReason string

const (
	// MemberTimeRoleNoVerb: no registry form appears in the window's clause.
	MemberTimeRoleNoVerb MemberTimeRoleReason = "no_verb"
	// MemberTimeRoleAmbiguous: two different roles appear in the clause.
	MemberTimeRoleAmbiguous MemberTimeRoleReason = "ambiguous"
	// MemberTimeRoleBound: exactly one distinct role appears.
	MemberTimeRoleBound MemberTimeRoleReason = "bound"
)

// BoundMemberTimeRole carries offsets only, never the matched words, so the
// question text cannot reach a log through it.
type BoundMemberTimeRole struct {
	Role      MemberTimeRole
	Grammar   string
	SpanStart int
	SpanEnd   int
}

// MemberTimeRoleOutcome is the binder verdict for one question.
type MemberTimeRoleOutcome struct {
	Reason MemberTimeRoleReason
	Role   MemberTimeRole
	// Bound lists every matched form by offsets, in question order.
	Bound []BoundMemberTimeRole
}

// BindMemberTimeRole binds the role from the clause that holds the window
// span. The clause is the stretch around the span that no sentence mark, comma
// or line break separates, so a verb elsewhere in the sentence never decides it.
func BindMemberTimeRole(question string, window BoundWindowSpan) MemberTimeRoleOutcome {
	if window.SpanStart < 0 || window.SpanEnd > len(question) || window.SpanStart > window.SpanEnd {
		return MemberTimeRoleOutcome{Reason: MemberTimeRoleNoVerb}
	}
	start, end := clauseBounds(question, window)
	clause := question[start:end]
	var bound []BoundMemberTimeRole
	seen := map[MemberTimeRole]bool{}
	predicate := map[MemberTimeRole]bool{}
	for _, entry := range memberTimeRoleRegistry {
		for _, loc := range entry.pattern.FindAllStringIndex(clause, -1) {
			if hyphenJoined(clause, loc[0], loc[1]) || wordRuneAdjacent(clause, loc[0], loc[1]) {
				continue
			}
			bound = append(bound, BoundMemberTimeRole{Role: entry.role, Grammar: entry.grammar, SpanStart: start + loc[0], SpanEnd: start + loc[1]})
			seen[entry.role] = true
			if !nounPhraseModifier(clause, loc[0], loc[1]) {
				predicate[entry.role] = true
			}
		}
	}
	if len(predicate) > 0 {
		seen = predicate
		kept := bound[:0]
		for _, form := range bound {
			if predicate[form.Role] {
				kept = append(kept, form)
			}
		}
		bound = kept
	}
	switch len(seen) {
	case 0:
		return MemberTimeRoleOutcome{Reason: MemberTimeRoleNoVerb}
	case 1:
		return MemberTimeRoleOutcome{Reason: MemberTimeRoleBound, Role: bound[0].Role, Bound: bound}
	}
	return MemberTimeRoleOutcome{Reason: MemberTimeRoleAmbiguous, Bound: bound}
}

// The form list shared by the two noun-phrase checks.
var memberTimeRoleFormAlternation = func() string {
	var forms []string
	for _, entry := range memberTimeRoleRegistry {
		forms = append(forms, entry.forms...)
	}
	return strings.Join(forms, "|")
}()

// workItemNounPhrase matches what follows a pre-nominal modifier: the head
// noun of the closed noun list, optionally after other forms ("new closed
// issues") and "work".
var workItemNounPhrase = regexp.MustCompile(`(?i)^(?:\s+(?:` + memberTimeRoleFormAlternation + `))*\s+(?:work\s+)?(?:items?|issues?|tickets?|tasks?|bugs?|stor(?:y|ies)|epics?)\b`)

// nounPhraseOpening matches what must stand before a modifier: the start of
// the clause or a word that opens a noun phrase, then only other forms. A form
// after a verb or a noun ("items opened tickets", "created and closed issues")
// is not at the opening of a noun phrase, so it is a predicate form.
var nounPhraseOpening = regexp.MustCompile(`(?i)(?:^|\b(?:which|what|the|all|any|these|those|of|among|show|list|count|many|my|our|their|a|an)\s+)(?:(?:` + memberTimeRoleFormAlternation + `)\s+)*$`)

// nounPhraseModifier reports a form that opens a noun phrase and modifies its
// head noun: it describes the items, it does not say when the period applies.
// A modifier binds the role only when no predicate form in the clause does, so
// "closed issues created in the last 30 days" binds created, while a lone
// "closed issues in the last 30 days" still binds completed.
func nounPhraseModifier(clause string, start, end int) bool {
	return nounPhraseOpening.MatchString(clause[:start]) && workItemNounPhrase.MatchString(clause[end:])
}

func clauseBounds(question string, window BoundWindowSpan) (int, int) {
	start := 0
	for i := window.SpanStart - 1; i >= 0; i-- {
		if isClauseBreak(question[i]) {
			start = i + 1
			break
		}
	}
	end := len(question)
	for i := window.SpanEnd; i < len(question); i++ {
		if isClauseBreak(question[i]) {
			end = i
			break
		}
	}
	return start, end
}

func isClauseBreak(b byte) bool {
	return b == '.' || b == '?' || b == '!' || b == ';' || b == ',' || b == '\n' || b == '\r'
}

// hyphenJoined reports a match that is one part of a hyphenated compound
// ("closed-source", "done-for-you"): the word is not the verb of the clause.
func hyphenJoined(clause string, start, end int) bool {
	if start >= 1 && clause[start-1] == '-' {
		if r, size := utf8.DecodeLastRuneInString(clause[:start-1]); size > 0 && isWordRune(r) {
			return true
		}
	}
	if end < len(clause) && clause[end] == '-' {
		if r, size := utf8.DecodeRuneInString(clause[end+1:]); size > 0 && isWordRune(r) {
			return true
		}
	}
	return false
}

// wordRuneAdjacent reports a match glued to a letter or digit the ASCII word
// boundary of the pattern does not see ("préclosed"): not a standalone verb.
func wordRuneAdjacent(clause string, start, end int) bool {
	if r, size := utf8.DecodeLastRuneInString(clause[:start]); size > 0 && isWordRune(r) {
		return true
	}
	if r, size := utf8.DecodeRuneInString(clause[end:]); size > 0 && isWordRune(r) {
		return true
	}
	return false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// memberTimeRoleClarificationLimitation is the one sentence that offers the
// three readings of a period over work items. The answer is not guessed: the
// caller names the reading and asks again.
func memberTimeRoleClarificationLimitation() string {
	roles := MemberTimeRoleVocabulary()
	names := make([]string, len(roles))
	for i, role := range roles {
		names[i] = string(role)
	}
	return "A period over work items can mean when they were " + strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1] + "; the question did not say which, so no members are listed. Ask again with one of those verbs, for example 'created in the last 30 days'."
}
