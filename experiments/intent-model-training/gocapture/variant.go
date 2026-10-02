//go:build unix

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

// variantSeparator sits between the production system message and the
// appendix. It is fixed; its sha256 is recorded in run.json and artifacts.
const variantSeparator = "\n\n"

const variantArtifactSchema = "gocapture.variant-artifact.v1"

// The appendix is prompt text: a larger file is a wrong file.
const maxAppendixBytes = 64 << 10

const systemAppendName = "system-append.md"

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

type promptVariant struct {
	Name           string
	Appendix       []byte
	AppendixSHA256 string
}

// variantRecord is the variant block of run.json and of every artifact.
// SystemMessageSHA256 is set in artifacts only: the sha256 of the system
// message actually sent (production message + separator + appendix).
type variantRecord struct {
	Name                    string `json:"name"`
	Candidate               string `json:"candidate"`
	AppendixSHA256          string `json:"appendix_sha256"`
	AppendixBytes           int    `json:"appendix_bytes"`
	SeparatorSHA256         string `json:"separator_sha256"`
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
	return &variantRecord{Name: v.Name, Candidate: variantCandidate(v.Name), AppendixSHA256: v.AppendixSHA256, AppendixBytes: len(v.Appendix),
		SeparatorSHA256: sha256Hex([]byte(variantSeparator)), BaseSystemMessageSHA256: baseSystemSHA, SystemMessageSHA256: systemSHA}
}

// suffix is what the system message gains.
func (v *promptVariant) suffix() string { return variantSeparator + string(v.Appendix) }

// loadVariant reads the two variant flags. Both or neither: a name without
// a file, or a file without a name, is refused.
func loadVariant(name, path string) (*promptVariant, error) {
	if name == "" && path == "" {
		return nil, nil
	}
	if name == "" || path == "" {
		return nil, errors.New("--prompt-variant and --system-append-file go together: give both, or neither for the incumbent")
	}
	if err := validVariantName(name); err != nil {
		return nil, err
	}
	data, err := readPrivateFile(path)
	if err != nil {
		return nil, fmt.Errorf("system append file: %w", err)
	}
	if len(data) > maxAppendixBytes {
		return nil, fmt.Errorf("system append file is larger than %d bytes", maxAppendixBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("system append file is not valid UTF-8")
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, errors.New("system append file is empty: an empty appendix is the incumbent, not a variant")
	}
	for _, c := range data {
		if c < 0x20 && c != '\n' && c != '\t' {
			return nil, errors.New("system append file holds a control character (only newline and tab are allowed)")
		}
	}
	return &promptVariant{Name: name, Appendix: data, AppendixSHA256: sha256Hex(data)}, nil
}

// spliceSystemMessage returns body with suffix added to the END of the
// string content of the FIRST message, which must have role "system", and
// that message's original content. Every byte outside that one JSON string
// is unchanged; inside it, the original bytes are kept and the suffix is
// added before the closing quote.
func spliceSystemMessage(body []byte, suffix string) ([]byte, string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, "", fmt.Errorf("request body: %w", err)
	}
	for dec.More() {
		key, err := stringToken(dec)
		if err != nil {
			return nil, "", fmt.Errorf("request body: %w", err)
		}
		if key != "messages" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, "", fmt.Errorf("request body: %w", err)
			}
			continue
		}
		if err := expectDelim(dec, '['); err != nil {
			return nil, "", fmt.Errorf("messages: %w", err)
		}
		if !dec.More() {
			return nil, "", errors.New("messages is empty")
		}
		if err := expectDelim(dec, '{'); err != nil {
			return nil, "", fmt.Errorf("first message: %w", err)
		}
		role, start, end := "", int64(-1), int64(-1)
		var content json.RawMessage
		for dec.More() {
			member, err := stringToken(dec)
			if err != nil {
				return nil, "", fmt.Errorf("first message: %w", err)
			}
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return nil, "", fmt.Errorf("first message: %w", err)
			}
			switch member {
			case "role":
				if json.Unmarshal(value, &role) != nil {
					return nil, "", errors.New("first message role is not a string")
				}
			case "content":
				if start >= 0 {
					return nil, "", errors.New("first message has two content members")
				}
				end = dec.InputOffset()
				start = end - int64(len(value))
				content = value
			}
		}
		if role != "system" {
			return nil, "", errors.New("the first message is not the system message")
		}
		if start < 0 || len(content) < 2 || content[0] != '"' || content[len(content)-1] != '"' {
			return nil, "", errors.New("the system message content is not one JSON string")
		}
		if !bytes.Equal(body[start:end], content) {
			return nil, "", errors.New("could not locate the system message content in the request body")
		}
		var base string
		if err := json.Unmarshal(content, &base); err != nil {
			return nil, "", errors.New("the system message content does not decode")
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
		var check struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		var got string
		if err := json.Unmarshal(out, &check); err != nil || len(check.Messages) == 0 || json.Unmarshal(check.Messages[0].Content, &got) != nil || got != base+suffix {
			return nil, "", errors.New("the changed request does not read back as the production system message plus the appendix")
		}
		return out, base, nil
	}
	return nil, "", errors.New("request body has no messages")
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
	out, base, err := spliceSystemMessage(body, v.suffix())
	if err != nil {
		return nil, fmt.Errorf("gocapture: %w; the variant is refused unsent", err)
	}
	if sha256Hex([]byte(base)) != inv.systemSHA {
		return nil, errVariantBase
	}
	systemSHA := sha256Hex([]byte(base + v.suffix()))
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
