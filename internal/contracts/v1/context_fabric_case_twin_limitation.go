package v1

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CHAOS-12663: a term matched several subjects whose labels differ from it only
// in case, and exactly one label spells it with its case. That subject is the one
// the term commits, and the answer says so: which subject the term was read as,
// and each other same-name subject, so the user can name one of them instead.
//
// EVERY WORD IS SERVED OR FIXED. The term is what the question typed; each subject
// is named by its served label, its kind noun (ContextFabricSubjectKindNoun) and
// its provider word, as the clarification prompt names a provider ("Label
// (provider)"). The term and every label are quoted (strconv.Quote), so a label
// holding this sentence's own joining words or punctuation still parses back to
// exactly one reading.
const (
	contextFabricCaseTwinReadAs     = " was read as the "
	contextFabricCaseTwinFirstTwin  = ". The "
	contextFabricCaseTwinNextTwin   = ", the "
	contextFabricCaseTwinLastTwin   = " and the "
	contextFabricCaseTwinSuffixOne  = " has the same name apart from case; name it with its own case to ask about it."
	contextFabricCaseTwinSuffixMany = " have the same name apart from case; name one with its own case to ask about it."
)

// ContextFabricCaseTwinSubject is one subject a case-twin sentence names.
// Provider is the provider word of the subject, or "" when it names none.
type ContextFabricCaseTwinSubject struct {
	Kind     ContextFabricSubjectKind
	Label    string
	Provider string
}

// ContextFabricCaseTwinReadingLimitation composes the sentence for term, read
// as read, over twins (in the order given).
//
// THE SOLE COMPOSER: recognition is a parse over exactly what this writes, and a
// second hand-rolled sentence would be one the parser may reject -- a service
// disclosure it does not recognise is displaceable. It returns "" for anything
// it cannot state in full within the limitation bound: no twin, an untrimmed or
// empty term or label, a kind outside the vocabulary, or a provider word with a
// parenthesis. The caller then does not read the term by case at all, so a
// commit is never made without its sentence.
func ContextFabricCaseTwinReadingLimitation(term string, read ContextFabricCaseTwinSubject, twins []ContextFabricCaseTwinSubject) string {
	if term == "" || strings.TrimSpace(term) != term || len(twins) == 0 {
		return ""
	}
	subject, ok := contextFabricCaseTwinSubjectText(read)
	if !ok {
		return ""
	}
	var sentence strings.Builder
	sentence.WriteString(strconv.Quote(term))
	sentence.WriteString(contextFabricCaseTwinReadAs)
	sentence.WriteString(subject)
	for index, twin := range twins {
		text, ok := contextFabricCaseTwinSubjectText(twin)
		if !ok {
			return ""
		}
		switch {
		case index == 0:
			sentence.WriteString(contextFabricCaseTwinFirstTwin)
		case index == len(twins)-1:
			sentence.WriteString(contextFabricCaseTwinLastTwin)
		default:
			sentence.WriteString(contextFabricCaseTwinNextTwin)
		}
		sentence.WriteString(text)
	}
	if len(twins) == 1 {
		sentence.WriteString(contextFabricCaseTwinSuffixOne)
	} else {
		sentence.WriteString(contextFabricCaseTwinSuffixMany)
	}
	if utf8.RuneCountInString(sentence.String()) > ContextFabricLimitationMaxLength {
		return ""
	}
	return sentence.String()
}

// contextFabricCaseTwinSubjectText is one subject after its article: the kind
// noun, the quoted label and, when there is one, the provider word in
// parentheses.
func contextFabricCaseTwinSubjectText(subject ContextFabricCaseTwinSubject) (string, bool) {
	noun := ContextFabricSubjectKindNoun(subject.Kind, 1)
	if noun == "" || subject.Label == "" || strings.TrimSpace(subject.Label) != subject.Label ||
		strings.TrimSpace(subject.Provider) != subject.Provider || strings.ContainsAny(subject.Provider, "()") {
		return "", false
	}
	text := noun + " " + strconv.Quote(subject.Label)
	if subject.Provider != "" {
		text += " (" + subject.Provider + ")"
	}
	return text, true
}

var (
	contextFabricCaseTwinTermPattern    = regexp.MustCompile(`^"(?:[^"\\]|\\.)*"`)
	contextFabricCaseTwinSubjectPattern = regexp.MustCompile(`^([^"]+) ("(?:[^"\\]|\\.)*")(?: \(([^()]*)\))?`)
)

// IsContextFabricCaseTwinReadingLimitation reports whether a limitation is one
// ContextFabricCaseTwinReadingLimitation composed. It parses the term and every
// subject, requires each noun to be a member of the closed kind vocabulary, and
// accepts the string only when composing the parsed parts gives it back byte for
// byte -- so a lookalike, a non-canonical quoting or a half sentence is not
// recognised.
func IsContextFabricCaseTwinReadingLimitation(limitation string) bool {
	if utf8.RuneCountInString(limitation) > ContextFabricLimitationMaxLength {
		return false
	}
	quotedTerm := contextFabricCaseTwinTermPattern.FindString(limitation)
	if quotedTerm == "" {
		return false
	}
	term, err := strconv.Unquote(quotedTerm)
	if err != nil {
		return false
	}
	rest, ok := strings.CutPrefix(limitation[len(quotedTerm):], contextFabricCaseTwinReadAs)
	if !ok {
		return false
	}
	read, rest, ok := parseContextFabricCaseTwinSubject(rest)
	if !ok {
		return false
	}
	rest, ok = strings.CutPrefix(rest, contextFabricCaseTwinFirstTwin)
	var twins []ContextFabricCaseTwinSubject
	for ok {
		var twin ContextFabricCaseTwinSubject
		if twin, rest, ok = parseContextFabricCaseTwinSubject(rest); !ok {
			return false
		}
		twins = append(twins, twin)
		if next, cut := strings.CutPrefix(rest, contextFabricCaseTwinNextTwin); cut {
			rest = next
			continue
		}
		rest, ok = strings.CutPrefix(rest, contextFabricCaseTwinLastTwin)
	}
	return len(twins) > 0 && ContextFabricCaseTwinReadingLimitation(term, read, twins) == limitation
}

func parseContextFabricCaseTwinSubject(text string) (ContextFabricCaseTwinSubject, string, bool) {
	match := contextFabricCaseTwinSubjectPattern.FindStringSubmatch(text)
	if match == nil {
		return ContextFabricCaseTwinSubject{}, "", false
	}
	kind, ok := contextFabricSubjectKindFromNoun(match[1], 1)
	if !ok {
		return ContextFabricCaseTwinSubject{}, "", false
	}
	label, err := strconv.Unquote(match[2])
	if err != nil {
		return ContextFabricCaseTwinSubject{}, "", false
	}
	return ContextFabricCaseTwinSubject{Kind: kind, Label: label, Provider: match[3]}, text[len(match[0]):], true
}
