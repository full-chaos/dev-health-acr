package v1

import "fmt"

// ContextFabricModelSource says whose model served the service-side model
// calls of one investigation turn.
//
// WHY IT IS ON THE WIRE. An organization can bring its own model, and the
// platform can serve a turn on its own. Both produce the same answer shape, so
// without this field a reader of a stored or served answer cannot tell whose
// model, and so whose spend and whose data-handling terms, produced it.
//
// ADDITIVE AND OPTIONAL. Present only when at least one service-side model call
// ran in the turn (interpretation, synthesis or offer phrasing). A caller that
// supplied the interpretation and synthesized on the server still carries the
// field. Absent means no service-side model call ran: a refusal before any
// model, a deterministic-only turn, or caller-side synthesis with a supplied
// interpretation. One turn never mixes sources; a turn that would is refused,
// never labelled.
type ContextFabricModelSource string

const (
	// ContextFabricModelSourceOrganization: the organization's own saved model.
	ContextFabricModelSourceOrganization ContextFabricModelSource = "organization"
	// ContextFabricModelSourcePlatform: the deployment's own model.
	ContextFabricModelSourcePlatform ContextFabricModelSource = "platform"
)

// ContextFabricModelSources returns the closed vocabulary.
func ContextFabricModelSources() []ContextFabricModelSource {
	return []ContextFabricModelSource{ContextFabricModelSourceOrganization, ContextFabricModelSourcePlatform}
}

// Validate admits exactly the closed members, on writes and on stored reads
// alike: the vocabulary is closed, and a new member goes in with a consumer pin
// bump. An empty value is "absent" and is valid; the field is omitempty.
func (s ContextFabricModelSource) Validate() error {
	if s == "" {
		return nil
	}
	for _, member := range ContextFabricModelSources() {
		if s == member {
			return nil
		}
	}
	return fmt.Errorf("model_source %q is not a vocabulary member", s)
}
