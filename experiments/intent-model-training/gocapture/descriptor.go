package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/experiments/intent-model-training/internal/interpreq"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Descriptor (spec §6): the complete shape of one chat-completion request.
// Message contents are reduced to sha256; every other value is kept.
type descriptor struct {
	Messages []messageDescriptor        `json:"messages"`
	Seed     *string                    `json:"seed"`
	Top      map[string]json.RawMessage `json:"top"`
}

type messageDescriptor struct {
	ContentKind   string           `json:"content_kind"`
	ContentSHA256 string           `json:"content_sha256"`
	Keys          []string         `json:"keys"`
	Parts         []partDescriptor `json:"parts"`
	Role          string           `json:"role"`
}

type partDescriptor struct {
	Keys []string `json:"keys"`
	Type string   `json:"type"`
}

// envelopeGolden pins what the production client sends at the pin (T16).
type envelopeGolden struct {
	Schema   string                     `json:"schema"`
	Top      map[string]json.RawMessage `json:"top"`
	Messages []messageShape             `json:"messages"`
}

type messageShape struct {
	ContentKind string           `json:"content_kind"`
	Keys        []string         `json:"keys"`
	Parts       []partDescriptor `json:"parts"`
	Role        string           `json:"role"`
}

const goldenSchema = "gocapture.envelope-golden.v1"

func loadGolden(path string) (envelopeGolden, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return envelopeGolden{}, fmt.Errorf("read envelope golden: %w", err)
	}
	var golden envelopeGolden
	if err := strictUnmarshal(data, &golden); err != nil {
		return envelopeGolden{}, fmt.Errorf("envelope golden: %w", err)
	}
	if golden.Schema != goldenSchema || len(golden.Messages) != 2 {
		return envelopeGolden{}, errors.New("envelope golden: wrong schema or message count")
	}
	if _, ok := golden.Top["model"]; ok {
		return envelopeGolden{}, errors.New("envelope golden must not pin the model value")
	}
	return golden, nil
}

// interpretSeed reproduces genkitruntime.chaos4631InterpretSeedFor
// (runtime.go:649): FNV-1a-64 over "<QuestionHash>:<draw>", bit 63 cleared.
func interpretSeed(question string, draw int) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(contextfabric.QuestionHash(question)))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strconv.Itoa(draw)))
	return int64(h.Sum64() &^ (1 << 63))
}

func expectedDescriptor(golden envelopeGolden, model, systemSHA, inputSHA string, seed int64) (descriptor, error) {
	top := make(map[string]json.RawMessage, len(golden.Top)+1)
	for k, v := range golden.Top {
		canon, err := cjRaw(v)
		if err != nil {
			return descriptor{}, fmt.Errorf("golden top %q: %w", k, err)
		}
		top[k] = canon
	}
	modelJSON, err := cj(model)
	if err != nil {
		return descriptor{}, err
	}
	top["model"] = modelJSON
	contents := []string{systemSHA, inputSHA}
	messages := make([]messageDescriptor, 2)
	for i, shape := range golden.Messages {
		messages[i] = messageDescriptor{
			ContentKind: shape.ContentKind, ContentSHA256: contents[i],
			Keys: append([]string(nil), shape.Keys...), Parts: clonePartDescriptors(shape.Parts), Role: shape.Role,
		}
	}
	seedText := strconv.FormatInt(seed, 10)
	return descriptor{Messages: messages, Seed: &seedText, Top: top}, nil
}

func clonePartDescriptors(parts []partDescriptor) []partDescriptor {
	if parts == nil {
		return nil
	}
	out := make([]partDescriptor, len(parts))
	for i, p := range parts {
		out[i] = partDescriptor{Keys: append([]string(nil), p.Keys...), Type: p.Type}
	}
	return out
}

// observedDescriptor builds the descriptor of a request body (spec §6).
// A duplicate key anywhere is an error: the body would be ambiguous.
func observedDescriptor(body []byte) (descriptor, error) {
	if !utf8.Valid(body) {
		return descriptor{}, errors.New("request body is not valid UTF-8")
	}
	dups, err := interpreq.DuplicateKeys(body)
	if err != nil {
		return descriptor{}, fmt.Errorf("request body is not JSON: %w", err)
	}
	if len(dups) > 0 {
		return descriptor{}, fmt.Errorf("request body has duplicate keys %v", dups)
	}
	var top map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&top); err != nil {
		return descriptor{}, fmt.Errorf("request body is not an object: %w", err)
	}
	out := descriptor{Top: map[string]json.RawMessage{}}
	rawMessages, ok := top["messages"]
	if !ok {
		return descriptor{}, errors.New("request body has no messages")
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return descriptor{}, fmt.Errorf("messages: %w", err)
	}
	for i, message := range messages {
		md, err := describeMessage(message)
		if err != nil {
			return descriptor{}, fmt.Errorf("message %d: %w", i, err)
		}
		out.Messages = append(out.Messages, md)
	}
	if rawSeed, ok := top["seed"]; ok {
		seedDecoder := json.NewDecoder(bytes.NewReader(rawSeed))
		seedDecoder.UseNumber()
		var seedValue any
		if err := seedDecoder.Decode(&seedValue); err != nil {
			return descriptor{}, fmt.Errorf("seed: %w", err)
		}
		number, isNumber := seedValue.(json.Number)
		if !isNumber {
			return descriptor{}, errors.New("seed is not a number")
		}
		text := number.String()
		out.Seed = &text
	}
	for k, v := range top {
		if k == "messages" || k == "seed" {
			continue
		}
		canon, err := cjRaw(v)
		if err != nil {
			return descriptor{}, fmt.Errorf("top %q: %w", k, err)
		}
		out.Top[k] = canon
	}
	return out, nil
}

func describeMessage(message map[string]json.RawMessage) (messageDescriptor, error) {
	md := messageDescriptor{Keys: sortedKeys(message)}
	if err := json.Unmarshal(message["role"], &md.Role); err != nil {
		return md, errors.New("role is not a string")
	}
	content, ok := message["content"]
	if !ok {
		return md, errors.New("no content")
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		md.ContentKind = "string"
		md.ContentSHA256 = sha256Hex([]byte(text))
		return md, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(content, &parts); err != nil {
		return md, errors.New("content is neither a string nor an array of parts")
	}
	var joined strings.Builder
	md.ContentKind = "parts"
	md.Parts = []partDescriptor{}
	for i, part := range parts {
		pd := partDescriptor{Keys: sortedKeys(part)}
		if err := json.Unmarshal(part["type"], &pd.Type); err != nil {
			return md, fmt.Errorf("part %d type is not a string", i)
		}
		var partText string
		if err := json.Unmarshal(part["text"], &partText); err != nil {
			return md, fmt.Errorf("part %d text is not a string", i)
		}
		joined.WriteString(partText)
		md.Parts = append(md.Parts, pd)
	}
	md.ContentSHA256 = sha256Hex([]byte(joined.String()))
	return md, nil
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// descriptorCJ is the canonical serialization whose sha256 is the
// descriptor hash.
func descriptorCJ(d descriptor) ([]byte, error) {
	return cj(d)
}

func strictUnmarshal(data []byte, v any) error {
	dups, err := interpreq.DuplicateKeys(data)
	if err != nil {
		return err
	}
	if len(dups) > 0 {
		return fmt.Errorf("duplicate keys %v", dups)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data")
	}
	return nil
}
