package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Prompt-variant capture (SPEC-incumbent-capture.md R7): the same requests
// to the same model, with the production system message followed by a
// fixed separator and an appendix file. It is a labelled control
// ("incumbent-variant[<name>]"), never the incumbent:
//   - its artifacts have their own schema and carry the variant block;
//   - its ledger records carry the variant name, so its pairs never share a
//     pair key with the incumbent's;
//   - its response rows carry the candidate "incumbent-variant[<name>]";
//   - it runs only with chris's variant_approval record for this name and
//     this appendix, and its HTTP attempts spend from the same approval.
//
// Production builds the request. The transport proves that request is
// exactly the incumbent's (the full expected descriptor, production system
// message sha included), then changes ONE thing: the system message content
// gains the separator and the appendix. The changed request is proven again
// against the same expected descriptor with the variant's system message sha.
//
// Two modes change that one thing in two ways:
//   - append (the default, R7): the production system message, the
//     separator and an appendix file;
//   - replace (a candidate prompt): the file is the WHOLE system message
//     and takes the place of production's. The base proof is the same, so
//     the model, the user message, the seed, the response format and the
//     decoding stay production's by proof.
//
// A name is approved for one mode and one file. Append-mode records are
// written exactly as before the replace mode existed.

// variantSeparator sits between the production system message and the
// appendix. It is fixed; its sha256 is recorded in run.json and artifacts.
const variantSeparator = "\n\n"

const variantArtifactSchema = "gocapture.variant-artifact.v1"

// The appendix is prompt text: a larger file is a wrong file.
const maxAppendixBytes = 64 << 10

const systemAppendName = "system-append.md"

// systemMessageName is the kept file of a replace-mode run: the whole
// system message that was sent.
const systemMessageName = "system-message.md"

// Variant modes. The append mode is the empty string, so no append-mode
// record gains a member.
const (
	variantModeAppend  = ""
	variantModeReplace = "replace"
)

func validVariantMode(mode string) bool {
	return mode == variantModeAppend || mode == variantModeReplace
}

var variantNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func isSHA256Hex(s string) bool { return sha256HexPattern.MatchString(s) }

func validVariantName(name string) error {
	if !variantNamePattern.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid prompt-variant name %q (lower-case letters, digits, '_', '.', '-'; at most 64)", name)
	}
	return nil
}

// variantCandidate is the candidate label of every response row of a
// variant run. It can never equal "incumbent".
func variantCandidate(name string) string { return "incumbent-variant[" + name + "]" }

// In replace mode Appendix is the whole candidate system message and
// AppendixSHA256 its sha256: one file per variant, whatever the mode.
type promptVariant struct {
	Name           string
	Appendix       []byte
	AppendixSHA256 string
	Mode           string
}

// variantRecord is the variant block of run.json and of every artifact.
// SystemMessageSHA256 is set in artifacts only: the sha256 of the system
// message actually sent (production message + separator + appendix, or the
// file alone in replace mode). Mode is absent for append; in replace mode
// the appendix members describe the system message file and there is no
// separator.
type variantRecord struct {
	Name                    string `json:"name"`
	Candidate               string `json:"candidate"`
	AppendixSHA256          string `json:"appendix_sha256"`
	AppendixBytes           int    `json:"appendix_bytes"`
	SeparatorSHA256         string `json:"separator_sha256,omitempty"`
	Mode                    string `json:"mode,omitempty"`
	BaseSystemMessageSHA256 string `json:"base_system_message_sha256"`
	SystemMessageSHA256     string `json:"system_message_sha256,omitempty"`
}

func (v *promptVariant) name() string {
	if v == nil {
		return ""
	}
	return v.Name
}

func (v *promptVariant) record(baseSystemSHA, systemSHA string) *variantRecord {
	if v == nil {
		return nil
	}
	rec := &variantRecord{Name: v.Name, Candidate: variantCandidate(v.Name), AppendixSHA256: v.AppendixSHA256, AppendixBytes: len(v.Appendix),
		SeparatorSHA256: sha256Hex([]byte(variantSeparator)), BaseSystemMessageSHA256: baseSystemSHA, SystemMessageSHA256: systemSHA}
	if v.replaces() {
		rec.SeparatorSHA256, rec.Mode = "", variantModeReplace
	}
	return rec
}

func (v *promptVariant) replaces() bool { return v != nil && v.Mode == variantModeReplace }

// keptName is the file of the run directory that keeps the variant's file.
func (v *promptVariant) keptName() string {
	if v.replaces() {
		return systemMessageName
	}
	return systemAppendName
}

// fileLabel names the variant's file in messages.
func (v *promptVariant) fileLabel() string {
	if v.replaces() {
		return "system message"
	}
	return "appendix"
}

// suffix is what the system message gains.
func (v *promptVariant) suffix() string { return variantSeparator + string(v.Appendix) }

// loadVariant reads the variant flags: a name and exactly one file, or
// nothing. appendPath selects the append mode, messagePath the replace mode.
func loadVariant(name, appendPath, messagePath string) (*promptVariant, error) {
	if name == "" && appendPath == "" && messagePath == "" {
		return nil, nil
	}
	if appendPath != "" && messagePath != "" {
		return nil, errors.New("give --system-append-file (the production prompt plus an appendix) or --system-message-file (a candidate prompt in its place), not both")
	}
	path, mode, label := appendPath, variantModeAppend, "system append file"
	if messagePath != "" {
		path, mode, label = messagePath, variantModeReplace, "system message file"
	}
	if name == "" || path == "" {
		return nil, errors.New("--prompt-variant and one of --system-append-file or --system-message-file go together: give a name and one file, or neither for the incumbent")
	}
	if err := validVariantName(name); err != nil {
		return nil, err
	}
	data, err := readPrivateFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if len(data) > maxAppendixBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", label, maxAppendixBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New(label + " is not valid UTF-8")
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, errors.New(label + " is empty: an empty appendix is the incumbent, and an empty system message is no prompt")
	}
	for _, c := range data {
		if c < 0x20 && c != '\n' && c != '\t' {
			return nil, errors.New(label + " holds a control character (only newline and tab are allowed)")
		}
	}
	return &promptVariant{Name: name, Appendix: data, AppendixSHA256: sha256Hex(data), Mode: mode}, nil
}

// systemContentSpan finds the string content of the FIRST message, which
// must have role "system": body[start:end] is that one JSON string, quotes
// included, and base is its decoded text.
func systemContentSpan(body []byte) (start, end int64, base string, err error) {
	fail := func(e error) (int64, int64, string, error) { return 0, 0, "", e }
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := expectDelim(dec, '{'); err != nil {
		return fail(fmt.Errorf("request body: %w", err))
	}
	for dec.More() {
		key, err := stringToken(dec)
		if err != nil {
			return fail(fmt.Errorf("request body: %w", err))
		}
		if key != "messages" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return fail(fmt.Errorf("request body: %w", err))
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return fail(fmt.Errorf("messages: %w", err))
		}
		if !dec.More() {
			return fail(errors.New("messages is empty"))
		}
		if err := expectDelim(dec, '{'); err != nil {
			return fail(fmt.Errorf("first message: %w", err))
		}
		role := ""
		start, end = -1, -1
		var content json.RawMessage
		for dec.More() {
			member, err := stringToken(dec)
			if err != nil {
				return fail(fmt.Errorf("first message: %w", err))
			}
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return fail(fmt.Errorf("first message: %w", err))
			}
			switch member {
			case "role":
				if json.Unmarshal(value, &role) != nil {
					return fail(errors.New("first message role is not a string"))
				}
			case "content":
				if start >= 0 {
					return fail(errors.New("first message has two content members"))
				}
				end = dec.InputOffset()
				start = end - int64(len(value))
				content = value
			}
		}
		if role != "system" {
			return fail(errors.New("the first message is not the system message"))
		}
		if start < 0 || len(content) < 2 || content[0] != '"' || content[len(content)-1] != '"' {
			return fail(errors.New("the system message content is not one JSON string"))
		}
		if !bytes.Equal(body[start:end], content) {
			return fail(errors.New("could not locate the system message content in the request body"))
		}
		if err := json.Unmarshal(content, &base); err != nil {
			return fail(errors.New("the system message content does not decode"))
		}
		return start, end, base, nil
	}
	return fail(errors.New("request body has no messages"))
}

// readSystemContent decodes the first message's string content.
func readSystemContent(body []byte) (string, bool) {
	var check struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	var got string
	if err := json.Unmarshal(body, &check); err != nil || len(check.Messages) == 0 || json.Unmarshal(check.Messages[0].Content, &got) != nil {
		return "", false
	}
	return got, true
}

// spliceSystemMessage returns body with suffix added to the END of the
// string content of the FIRST message, which must have role "system", and
// that message's original content. Every byte outside that one JSON string
// is unchanged; inside it, the original bytes are kept and the suffix is
// added before the closing quote.
func spliceSystemMessage(body []byte, suffix string) ([]byte, string, error) {
	_, end, base, err := systemContentSpan(body)
	if err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(suffix)
	if err != nil {
		return nil, "", err
	}
	out := make([]byte, 0, len(body)+len(encoded))
	out = append(out, body[:end-1]...)              // everything up to the closing quote
	out = append(out, encoded[1:len(encoded)-1]...) // the suffix, JSON-escaped, without its quotes
	out = append(out, body[end-1:]...)              // the closing quote and the rest
	// Read the result back: the content is exactly base + suffix.
	if got, ok := readSystemContent(out); !ok || got != base+suffix {
		return nil, "", errors.New("the changed request does not read back as the production system message plus the appendix")
	}
	return out, base, nil
}

// replaceSystemMessage returns body with the string content of the FIRST
// message, which must have role "system", replaced by content, and that
// message's original content. Every byte outside that one JSON string is
// unchanged.
func replaceSystemMessage(body []byte, content string) ([]byte, string, error) {
	start, end, base, err := systemContentSpan(body)
	if err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, "", err
	}
	out := make([]byte, 0, len(body)+len(encoded))
	out = append(out, body[:start]...) // everything before the content string
	out = append(out, encoded...)      // the candidate, as one JSON string
	out = append(out, body[end:]...)   // everything after it
	// Read the result back: the content is exactly the candidate.
	if got, ok := readSystemContent(out); !ok || got != content {
		return nil, "", errors.New("the changed request does not read back as the candidate system message")
	}
	return out, base, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q", string(want))
	}
	return nil
}

func stringToken(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", errors.New("expected an object key")
	}
	return s, nil
}

var errVariantBase = errors.New("gocapture: the production request differs from the expected incumbent request; the variant is refused unsent")

var errVariantIsIncumbent = errors.New("gocapture: the candidate system message equals the production system message: that is the incumbent, not a candidate; refused unsent")

// rewrite proves the production request is the incumbent's, then returns
// the request the variant sends. It records the base proof on the attempt.
func (v *promptVariant) rewrite(t *captureTransport, inv *invocation, body []byte, record *attemptRecord) ([]byte, error) {
	baseExpected, err := expectedDescriptor(t.golden, t.model, inv.systemSHA, inv.inputSHA, interpretSeed(inv.question, inv.authorizedDraw))
	if err != nil {
		return nil, err
	}
	baseExpectedCJ, err := descriptorCJ(baseExpected)
	if err != nil {
		return nil, err
	}
	record.BaseExpectedDescriptorSHA256 = sha256Hex(baseExpectedCJ)
	baseObserved, err := observedDescriptor(body)
	if err != nil {
		return nil, errVariantBase
	}
	baseObservedCJ, err := descriptorCJ(baseObserved)
	if err != nil {
		return nil, errVariantBase
	}
	record.BaseObservedDescriptorSHA256 = sha256Hex(baseObservedCJ)
	if record.BaseObservedDescriptorSHA256 != record.BaseExpectedDescriptorSHA256 {
		return nil, errVariantBase
	}
	var out []byte
	var base, sent string
	if v.replaces() {
		sent = string(v.Appendix)
		out, base, err = replaceSystemMessage(body, sent)
	} else {
		out, base, err = spliceSystemMessage(body, v.suffix())
		sent = base + v.suffix()
	}
	if err != nil {
		return nil, fmt.Errorf("gocapture: %w; the variant is refused unsent", err)
	}
	if sha256Hex([]byte(base)) != inv.systemSHA {
		return nil, errVariantBase
	}
	if sent == base {
		return nil, errVariantIsIncumbent
	}
	systemSHA := sha256Hex([]byte(sent))
	if inv.variantSystemSHA != "" && inv.variantSystemSHA != systemSHA {
		return nil, errors.New("gocapture: the variant system message changed inside one invocation; refused unsent")
	}
	inv.variantSystemSHA = systemSHA
	return out, nil
}

// withBody returns a copy of request that sends body. The caller's request
// is left as production built it.
func withBody(request *http.Request, body []byte) *http.Request {
	out := request.Clone(request.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return out
}
