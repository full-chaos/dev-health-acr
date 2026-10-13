package v1

// openVocabularyToken reports whether value is shaped like a member of an
// additive output vocabulary. A client reading a response accepts such a value
// even when it is not one of the members this build knows, because the server
// may add members after the client was built. Producers keep their closed sets.
func openVocabularyToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '.' || c == ':' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// additiveOK is the vocabulary gate for an additive output vocabulary: a known
// member always passes, and an unknown token passes only on a read of a
// served or stored document.
func (b contextFabricBounds) additiveOK(known bool, token string) bool {
	return known || (b.openVocabularies && openVocabularyToken(token))
}

// additiveUnknown reports a value the gate admitted only because it is open:
// a token this build does not know. Rules keyed to specific members do not
// apply to it.
func (b contextFabricBounds) additiveUnknown(known bool, token string) bool {
	return !known && b.openVocabularies && openVocabularyToken(token)
}
