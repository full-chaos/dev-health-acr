package contextfabric

import (
	"regexp"
	"strings"
	"unicode"
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
	pattern *regexp.Regexp
}{
	{MemberTimeRoleCreated, "created_forms", regexp.MustCompile(`(?i)\b(?:created|opened|filed|raised|added|new)\b`)},
	{MemberTimeRoleCompleted, "completed_forms", regexp.MustCompile(`(?i)\b(?:completed|finished|closed|done|resolved|shipped|delivered)\b`)},
	{MemberTimeRoleUpdated, "updated_forms", regexp.MustCompile(`(?i)\b(?:updated|changed|modified|touched)\b`)},
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
// span. The clause is the sentence around the span (terminal punctuation and
// line breaks end it), so a verb in another sentence never decides it.
func BindMemberTimeRole(question string, window BoundWindowSpan) MemberTimeRoleOutcome {
	if window.SpanStart < 0 || window.SpanEnd > len(question) || window.SpanStart > window.SpanEnd {
		return MemberTimeRoleOutcome{Reason: MemberTimeRoleNoVerb}
	}
	start, end := clauseBounds(question, window)
	clause := question[start:end]
	var bound []BoundMemberTimeRole
	seen := map[MemberTimeRole]bool{}
	for _, entry := range memberTimeRoleRegistry {
		for _, loc := range entry.pattern.FindAllStringIndex(clause, -1) {
			if hyphenJoined(clause, loc[0], loc[1]) {
				continue
			}
			bound = append(bound, BoundMemberTimeRole{Role: entry.role, Grammar: entry.grammar, SpanStart: start + loc[0], SpanEnd: start + loc[1]})
			seen[entry.role] = true
		}
	}
	switch len(seen) {
	case 0:
		return MemberTimeRoleOutcome{Reason: MemberTimeRoleNoVerb}
	case 1:
		return MemberTimeRoleOutcome{Reason: MemberTimeRoleBound, Role: bound[0].Role, Bound: bound}
	}
	return MemberTimeRoleOutcome{Reason: MemberTimeRoleAmbiguous, Bound: bound}
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
	return b == '.' || b == '?' || b == '!' || b == ';' || b == '\n' || b == '\r'
}

// hyphenJoined reports a match that is one part of a hyphenated compound
// ("closed-source", "done-for-you"): the word is not the verb of the clause.
func hyphenJoined(clause string, start, end int) bool {
	if start >= 2 && clause[start-1] == '-' && isWordRune(rune(clause[start-2])) {
		return true
	}
	if end+1 < len(clause) && clause[end] == '-' && isWordRune(rune(clause[end+1])) {
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
